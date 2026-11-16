//go:build linux

package diagnosisprep

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"path/filepath"
	"sort"
	"syscall"

	"golang.org/x/sys/unix"
)

const compactionFormat = "diagnosis-preparation-snapshot.v2"
const legacyCompactionGuard = "requires-preparation-snapshot-reader.v2\n"

var errCompactionValidation = errors.New("준비 기록 압축 후보가 원본과 일치하지 않습니다")

func compactionCandidateFailure(err error) error {
	if errors.Is(err, errStorageIO) {
		return err
	}
	return errors.Join(errStorage, errCompactionValidation)
}

type compactionHeader struct {
	Format         string `json:"format"`
	SnapshotCount  int    `json:"snapshot_count"`
	SourceSequence uint64 `json:"source_sequence"`
	SourceDigest   string `json:"source_digest"`
}

// hook는 Go 시험의 디스크 오류 주입이며 설정·RPC에서 받지 않는다.
type compactionHook func(string) error

func compactionStep(h compactionHook, stage string) error {
	if h != nil {
		return h(stage)
	}
	return nil
}
func preparationSnapshots(jobs map[string]receipt) ([]json.RawMessage, error) {
	ids := make([]string, 0, len(jobs))
	for id := range jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]json.RawMessage, 0, len(ids))
	for _, id := range ids {
		raw, err := json.Marshal(jobs[id])
		if err != nil {
			return nil, errStorage
		}
		out = append(out, raw)
	}
	return out, nil
}
func compactionName() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", errStorage
	}
	return ".preparation-compact-" + hex.EncodeToString(nonce[:]), nil
}

// 후보는 별도 디렉터리에 쓰고 data/head/디렉터리를 동기화한 뒤 검증한다.
func writeCompaction(ctx context.Context, root *protectedRoot, header compactionHeader, records []json.RawMessage, limit int64, hook compactionHook) error {
	f, _, err := root.open(journalName, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL)
	if err != nil {
		return errStorage
	}
	closed := false
	defer func() {
		if !closed {
			if f.Close() != nil {
				log.Print("준비 기록: 압축 후보 파일 마감을 확인하지 못했습니다")
			}
		}
	}()
	sequence := uint64(0)
	digest := ""
	var total int64
	payload, err := json.Marshal(header)
	if err != nil {
		return errStorage
	}
	write := func(raw json.RawMessage) error {
		if ctx.Err() != nil || compactionStep(hook, "journal_write") != nil {
			return errStorage
		}
		rec := journalRecord{Sequence: sequence + 1, Previous: digest, Payload: raw}
		rec.Digest = recordDigest(rec.Sequence, rec.Previous, raw)
		line, err := json.Marshal(rec)
		if err != nil || len(line)+1 > maxRecordBytes || total+int64(len(line)+1) > limit {
			return errStorage
		}
		line = append(line, '\n')
		if n, err := f.Write(line); err != nil || n != len(line) {
			return errStorage
		}
		sequence, digest, total = rec.Sequence, rec.Digest, total+int64(len(line))
		return nil
	}
	if write(payload) != nil {
		return errStorage
	}
	for _, raw := range records {
		if write(raw) != nil {
			return errStorage
		}
	}
	if compactionStep(hook, "journal_sync") != nil || f.Sync() != nil {
		return errStorage
	}
	if err = f.Close(); err != nil {
		closed = true
		return errStorage
	}
	closed = true
	head, _, err := root.open(headName, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL)
	if err != nil {
		return errStorage
	}
	raw, err := json.Marshal(journalHead{sequence, digest})
	if err != nil {
		return errors.Join(errStorage, head.Close())
	}
	if compactionStep(hook, "head_write") != nil {
		return errors.Join(errStorage, head.Close())
	}
	n, writeErr := head.Write(raw)
	syncErr := compactionStep(hook, "head_sync")
	if syncErr == nil {
		syncErr = head.Sync()
	}
	closeErr := head.Close()
	if writeErr != nil || n != len(raw) || syncErr != nil || closeErr != nil {
		return errStorage
	}
	if compactionStep(hook, "directory_sync") != nil || root.file.Sync() != nil {
		return errStorage
	}
	return nil
}

func removeCompactionDirectory(parent *protectedRoot, name string, child *protectedRoot) error {
	var errs []error
	for _, file := range []string{journalName, headName} {
		if err := syscall.Unlinkat(int(child.file.Fd()), file); err != nil && !errors.Is(err, syscall.ENOENT) {
			errs = append(errs, err)
		}
	}
	if err := unix.Unlinkat(int(parent.file.Fd()), name, unix.AT_REMOVEDIR); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// data와 head를 한 디렉터리로 원자 교체하여 서로 다른 세대가 섞이지 않게 한다.
func (j *journal) compact(ctx context.Context, records []json.RawMessage, validate func([]json.RawMessage, int) error, hook compactionHook) error {
	if ctx == nil || ctx.Err() != nil || j.poisoned || !j.current() || len(records) < 1 || len(records) > maxJobs || validate == nil || j.maxBytes != maxPreparationJournalBytes {
		return errStorage
	}
	parent := j.lease
	name, err := compactionName()
	if err != nil {
		return err
	}
	if compactionStep(hook, "temporary_directory") != nil || syscall.Mkdirat(int(parent.file.Fd()), name, 0700) != nil {
		return errStorage
	}
	var child *protectedRoot
	if err = compactionStep(hook, "candidate_directory_open"); err != nil {
		err = storageReadFailure(err)
	} else {
		child, err = openProtectedRoot(filepath.Join(parent.path, name))
	}
	if err != nil {
		// Only remove the empty directory created by this attempt; never recurse.
		if unix.Unlinkat(int(parent.file.Fd()), name, unix.AT_REMOVEDIR) != nil {
			log.Print("준비 기록: 열지 못한 빈 압축 후보를 정리하지 못했습니다")
		}
		return compactionCandidateFailure(err)
	}
	var next *journal
	published := false
	transferred := false
	defer func() {
		if !transferred {
			if next != nil {
				if next.closeData() != nil {
					log.Print("준비 기록: 압축 후보 마감을 확인하지 못했습니다")
				}
			} else if child.file.Close() != nil {
				log.Print("준비 기록: 압축 후보 디렉터리 마감을 확인하지 못했습니다")
			}
		}
		if !published {
			// child가 닫혔으면 다시 보호된 경로를 연 뒤 우리 두 후보 파일만 지운다.
			cleanup, e := openProtectedRoot(filepath.Join(parent.path, name))
			if e == nil {
				removeErr := removeCompactionDirectory(parent, name, cleanup)
				closeErr := cleanup.file.Close()
				if removeErr != nil || closeErr != nil {
					log.Print("준비 기록: 미채택 압축 후보를 정리하지 못했습니다")
				}
			}
		}
	}()
	header := compactionHeader{compactionFormat, len(records), j.sequence, j.digest}
	if writeCompaction(ctx, child, header, records, j.maxBytes, hook) != nil {
		return errStorage
	}
	next = &journal{root: child, lease: parent, maxBytes: j.maxBytes}
	if compactionStep(hook, "candidate_ready") != nil {
		return errStorage
	}
	checked, err := readJournalData(next)
	if err != nil {
		return compactionCandidateFailure(err)
	}
	if next.snapshotCount != len(records) || next.sourceSequence != j.sequence || next.sourceDigest != j.digest {
		return errors.Join(errStorage, errCompactionValidation)
	}
	if compactionStep(hook, "validate_snapshot") != nil {
		return errStorage
	}
	if validate(checked, next.snapshotCount) != nil {
		return errors.Join(errStorage, errCompactionValidation)
	}
	if err := compactionStep(hook, "candidate_recheck"); err != nil {
		return storageReadFailure(err)
	}
	if err := next.checkCurrent(); err != nil {
		return compactionCandidateFailure(err)
	}
	if ctx.Err() != nil || !j.current() || compactionStep(hook, "publish") != nil {
		return errStorage
	}
	old := *j
	if old.root == parent {
		err = syscall.Renameat(int(parent.file.Fd()), name, int(parent.file.Fd()), currentJournalDirectory)
	} else {
		err = unix.Renameat2(int(parent.file.Fd()), name, int(parent.file.Fd()), currentJournalDirectory, unix.RENAME_EXCHANGE)
	}
	if err != nil {
		return errStorage
	}
	published = true
	next.root.path = filepath.Join(parent.path, currentJournalDirectory)
	*j = *next
	transferred = true
	defer func() {
		if old.closeData() != nil {
			log.Print("준비 기록: 이전 압축 세대 마감을 확인하지 못했습니다")
		}
	}()
	if compactionStep(hook, "parent_sync") != nil || parent.file.Sync() != nil {
		j.poisoned = true
		return errStorage
	}
	if j.blockLegacyReader(hook) != nil {
		j.poisoned = true
		return errStorage
	}
	if compactionStep(hook, "cleanup") != nil {
		log.Print("준비 기록: 검증된 압축 뒤 이전 세대 정리를 보류했습니다")
		return nil
	}
	if old.root != parent {
		if removeCompactionDirectory(parent, name, old.root) != nil {
			log.Print("준비 기록: 이전 압축 세대 정리를 확인하지 못했습니다")
		}
	}
	if parent.file.Sync() != nil {
		log.Print("준비 기록: 이전 세대 정리 동기화를 확인하지 못했습니다")
	}
	return nil
}

// 옛 binary가 오래된 회계만 읽고 재개하지 못하도록 평면 head를 명시적으로 닫는다.
func (j *journal) blockLegacyReader(hook compactionHook) error {
	if j.snapshotCount < 1 || j.root == j.lease || !j.current() {
		return errStorage
	}
	parent := j.lease
	// 같은 가드가 있으면 유지한다. 소유자·mode·link 검사는 일반 보호 파일과 같다.
	if f, st, err := parent.open(headName, syscall.O_RDONLY); err == nil {
		raw := make([]byte, len(legacyCompactionGuard)+1)
		n, readErr := f.Read(raw)
		matched := parent.matches(headName, f, st)
		closeErr := f.Close()
		if readErr == nil && n == len(legacyCompactionGuard) && string(raw[:n]) == legacyCompactionGuard && matched {
			return closeErr
		}
	}
	name, err := compactionName()
	if err != nil {
		return err
	}
	if compactionStep(hook, "legacy_guard") != nil {
		return errStorage
	}
	f, _, err := parent.open(name, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL)
	if err != nil {
		return errStorage
	}
	n, writeErr := f.WriteString(legacyCompactionGuard)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || n != len(legacyCompactionGuard) || syncErr != nil || closeErr != nil {
		return errStorage
	}
	if syscall.Renameat(int(parent.file.Fd()), name, int(parent.file.Fd()), headName) != nil || parent.file.Sync() != nil {
		return errStorage
	}
	// guard를 먼저 동기화했으므로 옛 reader는 남은 옛 journal이 있어도 닫힌다.
	if err := syscall.Unlinkat(int(parent.file.Fd()), journalName); err != nil && !errors.Is(err, syscall.ENOENT) {
		log.Print("준비 기록: 이전 평면 저널 정리를 확인하지 못했습니다")
	}
	return parent.file.Sync()
}
