//go:build linux

package diagnosisprep

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

var errStorage = errors.New("진단 준비 기록의 무결성을 확인할 수 없습니다")
var errStorageIO = errors.New("진단 준비 기록의 입출력을 확인할 수 없습니다")

// Keep operational failures distinct without exposing paths or OS error details.
func storageReadFailure(err error) error {
	if errors.Is(err, errStorage) {
		return err
	}
	return errors.Join(errStorage, errStorageIO)
}

const journalName = "preparation.journal"
const headName = "preparation.head"
const maxJournalBytes = 32 << 20

// C34: 준비 작업만 128 MiB까지 읽는다. 대상·계정·접촉 저널의 32 MiB 상한은 유지한다.
const maxPreparationJournalBytes = 128 << 20
const currentJournalDirectory = "preparation.current"
const maxRecordBytes = 128 << 10

type protectedRoot struct {
	file *os.File
	path string
	stat syscall.Stat_t
}

func openProtectedRoot(path string) (*protectedRoot, error) {
	if os.Geteuid() <= 0 || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, errStorage
	}
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, storageReadFailure(err)
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		next, e := syscall.Openat(fd, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		_ = syscall.Close(fd)
		if e != nil {
			return nil, storageReadFailure(e)
		}
		fd = next
		var st syscall.Stat_t
		if err := syscall.Fstat(fd, &st); err != nil {
			_ = syscall.Close(fd)
			return nil, storageReadFailure(err)
		}
		if (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) || (st.Mode&0022 != 0 && !(st.Uid == 0 && st.Mode&syscall.S_ISVTX != 0)) {
			_ = syscall.Close(fd)
			return nil, errStorage
		}
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		_ = syscall.Close(fd)
		return nil, storageReadFailure(err)
	}
	if st.Uid != uint32(os.Geteuid()) || st.Mode&07777 != 0700 {
		_ = syscall.Close(fd)
		return nil, errStorage
	}
	return &protectedRoot{os.NewFile(uintptr(fd), "diagnosis-preparation-root"), path, st}, nil
}

func (r *protectedRoot) current() bool {
	return r.checkCurrent() == nil
}
func (r *protectedRoot) checkCurrent() error {
	fresh, err := openProtectedRoot(r.path)
	if err != nil {
		return err
	}
	defer fresh.file.Close()
	if fresh.stat.Dev != r.stat.Dev || fresh.stat.Ino != r.stat.Ino {
		return errStorage
	}
	return nil
}
func regular(st syscall.Stat_t) bool {
	return st.Mode&syscall.S_IFMT == syscall.S_IFREG && st.Mode&07777 == 0600 && st.Nlink == 1 && st.Uid == uint32(os.Geteuid())
}
func sameStat(a, b syscall.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Mode == b.Mode && a.Uid == b.Uid && a.Gid == b.Gid && a.Nlink == b.Nlink && a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}
func (r *protectedRoot) open(name string, flags int) (*os.File, syscall.Stat_t, error) {
	var st syscall.Stat_t
	fd, err := syscall.Openat(int(r.file.Fd()), name, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, st, err
	}
	f := os.NewFile(uintptr(fd), "diagnosis-preparation-protected-file")
	if err := syscall.Fstat(fd, &st); err != nil {
		_ = f.Close()
		return nil, st, storageReadFailure(err)
	}
	if !regular(st) {
		_ = f.Close()
		return nil, st, errStorage
	}
	return f, st, nil
}
func (r *protectedRoot) matches(name string, f *os.File, want syscall.Stat_t) bool {
	return r.checkMatch(name, f, want) == nil
}
func (r *protectedRoot) checkMatch(name string, f *os.File, want syscall.Stat_t) error {
	var got syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &got); err != nil {
		return storageReadFailure(err)
	}
	if !sameStat(got, want) {
		return errStorage
	}
	at, st, err := r.open(name, syscall.O_RDONLY)
	if err != nil {
		return storageReadFailure(err)
	}
	defer at.Close()
	if !sameStat(got, st) {
		return errStorage
	}
	return nil
}

type journalRecord struct {
	Sequence uint64          `json:"sequence"`
	Previous string          `json:"previous"`
	Payload  json.RawMessage `json:"payload"`
	Digest   string          `json:"digest"`
}
type journalHead struct {
	Sequence uint64 `json:"sequence"`
	Digest   string `json:"digest"`
}
type journal struct {
	lease          *protectedRoot
	maxBytes       int64
	snapshotCount  int
	sourceSequence uint64
	sourceDigest   string
	root           *protectedRoot
	file           *os.File
	head           *os.File
	stat           syscall.Stat_t
	headStat       syscall.Stat_t
	sequence       uint64
	digest         string
	poisoned       bool
}

func recordDigest(sequence uint64, previous string, raw []byte) string {
	sum := sha256.Sum256([]byte(strconv.FormatUint(sequence, 10) + "\n" + previous + "\n" + string(raw)))
	return hex.EncodeToString(sum[:])
}
func openJournal(path string) (*journal, []json.RawMessage, error) {
	return openJournalWithLimit(path, maxJournalBytes, false)
}
func openPreparationJournal(path string) (*journal, []json.RawMessage, error) {
	return openJournalWithLimit(path, maxPreparationJournalBytes, true)
}
func openJournalWithLimit(path string, limit int64, snapshots bool) (*journal, []json.RawMessage, error) {
	root, err := openProtectedRoot(path)
	if err != nil {
		return nil, nil, errStorage
	}
	j := &journal{root: root, lease: root, maxBytes: limit}
	fail := func() (*journal, []json.RawMessage, error) { _ = j.close(); return nil, nil, errStorage }
	if syscall.Flock(int(root.file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return fail()
	}
	generation := false
	if _, err := os.Lstat(filepath.Join(path, currentJournalDirectory)); err == nil {
		if !snapshots {
			return fail()
		}
		child, err := openProtectedRoot(filepath.Join(path, currentJournalDirectory))
		if err != nil {
			return fail()
		}
		j.root = child
		generation = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail()
	}
	if !generation {
		f, st, err := root.open(journalName, syscall.O_RDWR|syscall.O_APPEND|syscall.O_CREAT|syscall.O_EXCL)
		if err == nil {
			j.file, j.stat = f, st
			if previous, _, e := root.open(headName, syscall.O_RDONLY); e == nil {
				_ = previous.Close()
				return fail()
			} else if !errors.Is(e, syscall.ENOENT) {
				return fail()
			}
			if j.append(json.RawMessage(`{"format":"diagnosis-preparation-journal.v1"}`)) != nil {
				return fail()
			}
			return j, nil, nil
		}
		if !errors.Is(err, syscall.EEXIST) {
			return fail()
		}
	}
	records, err := readJournalData(j)
	if err != nil || generation != (j.snapshotCount > 0) {
		return fail()
	}
	return j, records, nil
}

// chain/head/descriptor 검사는 옛 저널과 압축 후보에 똑같이 적용한다.
func readJournalData(j *journal) ([]json.RawMessage, error) {
	f, st, err := j.root.open(journalName, syscall.O_RDWR|syscall.O_APPEND)
	if err != nil {
		return nil, storageReadFailure(err)
	}
	j.file, j.stat = f, st
	if st.Size <= 0 || st.Size > j.maxBytes {
		return nil, errStorage
	}
	raw, err := io.ReadAll(io.LimitReader(f, j.maxBytes+1))
	if err != nil {
		return nil, storageReadFailure(err)
	}
	if int64(len(raw)) != st.Size || raw[len(raw)-1] != '\n' {
		return nil, errStorage
	}
	if err := j.root.checkMatch(journalName, f, st); err != nil {
		return nil, err
	}
	lines := bytes.Split(raw[:len(raw)-1], []byte{'\n'})
	records := make([]json.RawMessage, 0, len(lines))
	for i, line := range lines {
		var record journalRecord
		if len(line) > maxRecordBytes || strictDecode(line, &record) != nil || record.Sequence != j.sequence+1 || record.Previous != j.digest || record.Digest != recordDigest(record.Sequence, record.Previous, record.Payload) {
			return nil, errStorage
		}
		if i == 0 {
			if string(record.Payload) != `{"format":"diagnosis-preparation-journal.v1"}` {
				var header compactionHeader
				if strictDecode(record.Payload, &header) != nil || header.Format != compactionFormat || header.SnapshotCount < 1 || header.SnapshotCount > maxJobs || header.SourceSequence < uint64(header.SnapshotCount+1) || !digestPattern.MatchString(header.SourceDigest) {
					return nil, errStorage
				}
				j.snapshotCount = header.SnapshotCount
				j.sourceSequence, j.sourceDigest = header.SourceSequence, header.SourceDigest
			}
		} else {
			records = append(records, record.Payload)
		}
		j.sequence, j.digest = record.Sequence, record.Digest
	}
	if len(records) < j.snapshotCount {
		return nil, errStorage
	}
	j.head, j.headStat, err = j.root.open(headName, syscall.O_RDONLY)
	if err != nil {
		return nil, storageReadFailure(err)
	}
	if j.headStat.Size > 1024 {
		return nil, errStorage
	}
	headRaw, err := io.ReadAll(io.LimitReader(j.head, 1025))
	var head journalHead
	if err != nil {
		return nil, storageReadFailure(err)
	}
	if strictDecode(headRaw, &head) != nil || head.Sequence != j.sequence || head.Digest != j.digest {
		return nil, errStorage
	}
	if err := j.root.checkMatch(headName, j.head, j.headStat); err != nil {
		return nil, err
	}
	return records, nil
}
func (j *journal) current() bool {
	return j.checkCurrent() == nil
}
func (j *journal) checkCurrent() error {
	if j.root == nil || j.lease == nil || j.file == nil {
		return errStorage
	}
	if err := j.root.checkCurrent(); err != nil {
		return err
	}
	if j.root != j.lease {
		if err := j.lease.checkCurrent(); err != nil {
			return err
		}
	}
	if err := j.root.checkMatch(journalName, j.file, j.stat); err != nil {
		return err
	}
	if j.head != nil {
		return j.root.checkMatch(headName, j.head, j.headStat)
	}
	return nil
}

func (j *journal) append(payload json.RawMessage) error {
	if j.poisoned || j.file == nil || !j.current() {
		j.poisoned = true
		return errStorage
	}
	j.poisoned = true
	record := journalRecord{Sequence: j.sequence + 1, Previous: j.digest, Payload: payload}
	record.Digest = recordDigest(record.Sequence, record.Previous, record.Payload)
	raw, err := json.Marshal(record)
	if err != nil || len(raw)+1 > maxRecordBytes || j.stat.Size+int64(len(raw))+1 > j.maxBytes {
		return errStorage
	}
	raw = append(raw, '\n')
	if n, err := j.file.Write(raw); err != nil || n != len(raw) {
		return errStorage
	}
	if j.file.Sync() != nil {
		return errStorage
	}
	headRaw, _ := json.Marshal(journalHead{record.Sequence, record.Digest})
	name := fmt.Sprintf(".head-%d", record.Sequence)
	next, _, err := j.root.open(name, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL)
	if err != nil {
		return errStorage
	}
	defer next.Close()
	if n, err := next.Write(headRaw); err != nil || n != len(headRaw) || next.Sync() != nil {
		return errStorage
	}
	// journal fsync와 head fsync 사이의 중단은 재시작 때 불일치로 닫힌다.
	if syscall.Renameat(int(j.root.file.Fd()), name, int(j.root.file.Fd()), headName) != nil || j.root.file.Sync() != nil {
		return errStorage
	}
	if j.head != nil {
		if j.head.Close() != nil {
			return errStorage
		}
	}
	j.head, j.headStat, err = j.root.open(headName, syscall.O_RDONLY)
	if err != nil || syscall.Fstat(int(j.file.Fd()), &j.stat) != nil {
		return errStorage
	}
	j.sequence, j.digest = record.Sequence, record.Digest
	j.poisoned = false
	return nil
}
func (j *journal) closeData() error {
	var errs []error
	if j.file != nil {
		errs = append(errs, j.file.Close())
		j.file = nil
	}
	if j.head != nil {
		errs = append(errs, j.head.Close())
		j.head = nil
	}
	if j.root != nil && j.root != j.lease {
		errs = append(errs, j.root.file.Close())
		j.root = nil
	}
	return errors.Join(errs...)
}

func (j *journal) close() error {
	err := j.closeData()
	if j.lease != nil {
		err = errors.Join(err, j.lease.file.Close())
		j.lease = nil
	}
	j.root = nil
	return err
}
