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

const journalName = "preparation.journal"
const headName = "preparation.head"
const maxJournalBytes = 32 << 20
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
		return nil, errStorage
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		next, e := syscall.Openat(fd, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		_ = syscall.Close(fd)
		if e != nil {
			return nil, errStorage
		}
		fd = next
		var st syscall.Stat_t
		if syscall.Fstat(fd, &st) != nil || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) || (st.Mode&0022 != 0 && !(st.Uid == 0 && st.Mode&syscall.S_ISVTX != 0)) {
			_ = syscall.Close(fd)
			return nil, errStorage
		}
	}
	var st syscall.Stat_t
	if syscall.Fstat(fd, &st) != nil || st.Uid != uint32(os.Geteuid()) || st.Mode&07777 != 0700 {
		_ = syscall.Close(fd)
		return nil, errStorage
	}
	return &protectedRoot{os.NewFile(uintptr(fd), "diagnosis-preparation-root"), path, st}, nil
}

func (r *protectedRoot) current() bool {
	fresh, err := openProtectedRoot(r.path)
	if err != nil {
		return false
	}
	defer fresh.file.Close()
	return fresh.stat.Dev == r.stat.Dev && fresh.stat.Ino == r.stat.Ino
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
	if syscall.Fstat(fd, &st) != nil || !regular(st) {
		_ = f.Close()
		return nil, st, errStorage
	}
	return f, st, nil
}
func (r *protectedRoot) matches(name string, f *os.File, want syscall.Stat_t) bool {
	var got syscall.Stat_t
	if syscall.Fstat(int(f.Fd()), &got) != nil || !sameStat(got, want) {
		return false
	}
	at, st, err := r.open(name, syscall.O_RDONLY)
	if err != nil {
		return false
	}
	defer at.Close()
	return sameStat(got, st)
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
	root     *protectedRoot
	file     *os.File
	head     *os.File
	stat     syscall.Stat_t
	headStat syscall.Stat_t
	sequence uint64
	digest   string
	poisoned bool
}

func recordDigest(sequence uint64, previous string, raw []byte) string {
	sum := sha256.Sum256([]byte(strconv.FormatUint(sequence, 10) + "\n" + previous + "\n" + string(raw)))
	return hex.EncodeToString(sum[:])
}
func openJournal(path string) (*journal, []json.RawMessage, error) {
	root, err := openProtectedRoot(path)
	if err != nil {
		return nil, nil, errStorage
	}
	j := &journal{root: root}
	fail := func() (*journal, []json.RawMessage, error) { _ = j.close(); return nil, nil, errStorage }
	// 같은 설치 루트의 두 준비 프로세스가 총량을 따로 예약하지 못한다.
	if syscall.Flock(int(root.file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return fail()
	}
	f, st, err := root.open(journalName, syscall.O_RDWR|syscall.O_APPEND|syscall.O_CREAT|syscall.O_EXCL)
	fresh := err == nil
	if errors.Is(err, syscall.EEXIST) {
		f, st, err = root.open(journalName, syscall.O_RDWR|syscall.O_APPEND)
	}
	if err != nil {
		return fail()
	}
	j.file, j.stat = f, st
	if fresh {
		// 초기 기록도 봉인한다. 기존의 빈 파일은 새 저장소로 취급하지 않는다.
		if previous, _, err := root.open(headName, syscall.O_RDONLY); err == nil {
			_ = previous.Close()
			return fail()
		} else if !errors.Is(err, syscall.ENOENT) {
			return fail()
		}
		if err := j.append(json.RawMessage(`{"format":"diagnosis-preparation-journal.v1"}`)); err != nil {
			return fail()
		}
		return j, nil, nil
	}
	if st.Size <= 0 || st.Size > maxJournalBytes {
		return fail()
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxJournalBytes+1))
	if err != nil || int64(len(raw)) != st.Size || raw[len(raw)-1] != '\n' || !root.matches(journalName, f, st) {
		return fail()
	}
	lines := bytes.Split(raw[:len(raw)-1], []byte{'\n'})
	records := make([]json.RawMessage, 0, len(lines))
	for i, line := range lines {
		var record journalRecord
		if len(line) > maxRecordBytes || strictDecode(line, &record) != nil || record.Sequence != j.sequence+1 || record.Previous != j.digest || record.Digest != recordDigest(record.Sequence, record.Previous, record.Payload) {
			return fail()
		}
		if i == 0 {
			if string(record.Payload) != `{"format":"diagnosis-preparation-journal.v1"}` {
				return fail()
			}
		} else {
			records = append(records, record.Payload)
		}
		j.sequence, j.digest = record.Sequence, record.Digest
	}
	j.head, j.headStat, err = root.open(headName, syscall.O_RDONLY)
	if err != nil || j.headStat.Size > 1024 {
		return fail()
	}
	headRaw, err := io.ReadAll(io.LimitReader(j.head, 1025))
	var head journalHead
	if err != nil || strictDecode(headRaw, &head) != nil || head.Sequence != j.sequence || head.Digest != j.digest || !root.matches(headName, j.head, j.headStat) {
		return fail()
	}
	return j, records, nil
}
func (j *journal) append(payload json.RawMessage) error {
	if j.poisoned || j.file == nil || !j.root.current() || !j.root.matches(journalName, j.file, j.stat) || (j.head != nil && !j.root.matches(headName, j.head, j.headStat)) {
		j.poisoned = true
		return errStorage
	}
	j.poisoned = true
	record := journalRecord{Sequence: j.sequence + 1, Previous: j.digest, Payload: payload}
	record.Digest = recordDigest(record.Sequence, record.Previous, record.Payload)
	raw, err := json.Marshal(record)
	if err != nil || len(raw)+1 > maxRecordBytes || j.stat.Size+int64(len(raw))+1 > maxJournalBytes {
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
func (j *journal) close() error {
	var errs []error
	if j.file != nil {
		errs = append(errs, j.file.Close())
		j.file = nil
	}
	if j.head != nil {
		errs = append(errs, j.head.Close())
		j.head = nil
	}
	if j.root != nil {
		errs = append(errs, j.root.file.Close())
		j.root = nil
	}
	return errors.Join(errs...)
}
