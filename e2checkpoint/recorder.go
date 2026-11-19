package e2checkpoint

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/transmit"
)

var (
	ErrRecorderFailed = errors.New("e2checkpoint: recorder failed")
	ErrRecorderClosed = errors.New("e2checkpoint: recorder closed")
)

// RecorderConfig는 이미 권한을 확인한 host가 제공한다. Store의 Host lease와
// 저널 디렉터리 독점 소유를 별도로 보장해야 한다.
type RecorderConfig struct {
	Store        *Store
	Epoch        Epoch
	PublisherRef string
	Directory    string
	Filename     string
	Scope        transmit.Scope
	KeyRef       string
	Key          []byte
}

type Recorder struct {
	mu        sync.Mutex
	store     *Store
	journal   *e2proxy.Journal
	epoch     Epoch
	binding   Binding
	directory string
	filename  string
	fileInfo  os.FileInfo
	dirInfo   os.FileInfo
	head      Head
	failed    bool
	closed    bool
}

var _ e2proxy.Recorder = (*Recorder)(nil)

// OpenRecorder는 새 v3 헤더를 Sync한 후 현재 head 등록 ACK가 확인돼야 반환한다.
// 실패한 파일은 진단용으로 남기되, 사용할 수 있는 Recorder를 반환하지 않는다.
func OpenRecorder(ctx context.Context, c RecorderConfig) (*Recorder, error) {
	if ctx == nil || ctx.Err() != nil || c.Store == nil || !validEpoch(c.Epoch) ||
		!validRef(c.PublisherRef) || !validRef(c.KeyRef) || runtime.GOOS != "linux" ||
		!filepath.IsAbs(c.Directory) || filepath.Clean(c.Directory) != c.Directory {
		return nil, ErrConfig
	}
	journal, err := e2proxy.CreateJournalV3(c.Directory, c.Filename, c.Key, c.Scope, c.KeyRef)
	if err != nil {
		return nil, err
	}
	cp := journal.Checkpoint()
	r := &Recorder{store: c.Store, journal: journal, epoch: c.Epoch,
		binding:   Binding{PublisherRef: c.PublisherRef, KeyRef: c.KeyRef, Scope: c.Scope, JournalID: cp.JournalID},
		directory: c.Directory, filename: c.Filename}
	directory, dirErr := os.Lstat(c.Directory)
	info, err := os.Lstat(filepath.Join(c.Directory, c.Filename))
	if dirErr != nil || !validJournalDirectory(directory) || err != nil || !validJournalFile(info) {
		return nil, errors.Join(ErrIntegrity, journal.Close())
	}
	r.dirInfo, r.fileInfo = directory, info
	data, err := r.snapshot(ctx)
	if err != nil {
		return nil, errors.Join(err, journal.Close())
	}
	head, err := c.Store.Publish(ctx, Request{Epoch: c.Epoch, Binding: r.binding, Kind: Register, Candidate: cp}, bytes.NewReader(data))
	clear(data)
	if err != nil {
		return nil, errors.Join(err, journal.Close())
	}
	r.head = head
	return r, nil
}

func validJournalDirectory(info os.FileInfo) bool {
	if info == nil || info.Mode() != os.ModeDir|0700 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && stat.Nlink > 0
}
func validJournalFile(info os.FileInfo) bool {
	if info == nil || info.Mode() != 0600 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && stat.Nlink == 1
}

// snapshot은 이 Recorder가 소유한 파일만 읽는다. Store는 받은 바이트를
// 다시 소유 복사하고 저널 MAC을 검증한다.
func (r *Recorder) snapshot(ctx context.Context) ([]byte, error) {
	if r == nil || ctx == nil || ctx.Err() != nil || r.store == nil {
		return nil, ErrIntegrity
	}
	root, err := os.OpenRoot(r.directory)
	if err != nil {
		return nil, ErrIntegrity
	}
	defer root.Close()
	openedDir, err := root.Stat(".")
	pathDir, pathErr := os.Lstat(r.directory)
	if err != nil || pathErr != nil || !validJournalDirectory(openedDir) ||
		!validJournalDirectory(pathDir) || !os.SameFile(r.dirInfo, openedDir) ||
		!os.SameFile(r.dirInfo, pathDir) {
		return nil, ErrIntegrity
	}
	file, err := root.Open(r.filename)
	if err != nil {
		return nil, ErrIntegrity
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !validJournalFile(before) || r.fileInfo != nil && !os.SameFile(r.fileInfo, before) ||
		before.Size() <= 0 || before.Size() > r.store.maxBytes {
		return nil, ErrIntegrity
	}
	data, err := io.ReadAll(io.LimitReader(file, r.store.maxBytes+1))
	if err != nil {
		clear(data)
		return nil, ErrIntegrity
	}
	after, err := file.Stat()
	current, statErr := root.Stat(r.filename)
	currentDir, dirErr := os.Lstat(r.directory)
	if err != nil || statErr != nil || dirErr != nil || !validJournalDirectory(currentDir) ||
		!os.SameFile(r.dirInfo, currentDir) || !validJournalFile(after) || !validJournalFile(current) ||
		!os.SameFile(before, after) || !os.SameFile(before, current) ||
		before.Size() != after.Size() || after.Size() != int64(len(data)) ||
		!before.ModTime().Equal(after.ModTime()) || ctx.Err() != nil {
		clear(data)
		return nil, ErrIntegrity
	}
	return data, nil
}

// Record는 Journal.Record의 Write·Sync 뒤에 현재 head를 게시하고 재조회한다.
// 어느 단계든 실패하면 이 Recorder에서 후속 기록을 영구 거부한다.
func (r *Recorder) Record(ctx context.Context, event e2proxy.Event) error {
	if r == nil || ctx == nil {
		return ErrConfig
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRecorderClosed
	}
	if r.failed {
		return ErrRecorderFailed
	}
	if r.head.Phase != Open || r.journal.Checkpoint() != r.head.Checkpoint {
		r.failed = true
		return ErrRecorderFailed
	}
	if err := r.journal.Record(ctx, event); err != nil {
		r.failed = true
		return errors.Join(ErrRecorderFailed, err)
	}
	data, err := r.snapshot(ctx)
	if err != nil {
		r.failed = true
		return errors.Join(ErrRecorderFailed, err)
	}
	request := Request{Epoch: r.epoch, Binding: r.binding, Kind: Append,
		ExpectedRevision: r.head.Revision, Previous: r.head.Checkpoint, Candidate: r.journal.Checkpoint()}
	head, err := r.store.Publish(ctx, request, bytes.NewReader(data))
	clear(data)
	if err != nil {
		r.failed = true
		return errors.Join(ErrRecorderFailed, err)
	}
	r.head = head
	return nil
}

// VerifyCurrent는 저장소의 인증된 현재 head와 전체 저널을 다시 대조한다.
// 실패한 Recorder는 이전 성공 시점을 현재 권위로 대신하지 않는다.
func (r *Recorder) VerifyCurrent(ctx context.Context) (e2proxy.JournalSummary, error) {
	if r == nil || ctx == nil {
		return e2proxy.JournalSummary{}, ErrConfig
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failed {
		return e2proxy.JournalSummary{}, ErrRecorderFailed
	}
	data, err := r.snapshot(ctx)
	if err != nil {
		r.failed = true
		return e2proxy.JournalSummary{}, errors.Join(ErrRecorderFailed, err)
	}
	summary, head, err := r.store.VerifyCurrent(ctx, Query{Epoch: r.epoch, Binding: r.binding}, bytes.NewReader(data))
	clear(data)
	if err != nil || head != r.head {
		r.failed = true
		return e2proxy.JournalSummary{}, ErrRecorderFailed
	}
	return summary, nil
}

// Seal은 상위 실행 종료와 drain을 Host가 확인한 경우에만 승인된다.
// 저널 Close 실패 또는 Seal 게시 ACK 실패는 성공으로 보이지 않는다.
func (r *Recorder) Seal(ctx context.Context) error {
	if r == nil || ctx == nil {
		return ErrConfig
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRecorderClosed
	}
	r.closed = true
	closeErr := r.journal.Close()
	if r.failed || closeErr != nil {
		r.failed = true
		return errors.Join(ErrRecorderFailed, closeErr)
	}
	data, err := r.snapshot(ctx)
	if err != nil {
		r.failed = true
		return errors.Join(ErrRecorderFailed, err)
	}
	request := Request{Epoch: r.epoch, Binding: r.binding, Kind: Seal,
		ExpectedRevision: r.head.Revision, Previous: r.head.Checkpoint, Candidate: r.head.Checkpoint}
	head, err := r.store.Publish(ctx, request, bytes.NewReader(data))
	clear(data)
	if err != nil {
		r.failed = true
		return errors.Join(ErrRecorderFailed, err)
	}
	r.head = head
	return nil
}

// Abort는 미확인 결과를 봉인하지 않고 파일을 닫는다.
func (r *Recorder) Abort() error {
	if r == nil {
		return ErrConfig
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRecorderClosed
	}
	r.closed, r.failed = true, true
	if err := r.journal.Close(); err != nil {
		return ErrRecorderFailed
	}
	return nil
}
