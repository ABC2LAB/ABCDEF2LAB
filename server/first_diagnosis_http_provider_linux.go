//go:build linux

package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/internal/privateinput"
	"golang.org/x/sys/unix"
)

const firstHTTPAuthorityLimit = 16 << 10

type firstHTTPHeldFile struct {
	file   *os.File
	name   string
	stat   unix.Stat_t
	digest [32]byte
}
type firstHTTPM01Authority struct {
	mu                sync.Mutex
	ctx               context.Context
	cancel            context.CancelFunc
	cfg               FirstDiagnosisHTTPHostConfig
	grant             FirstDiagnosisHTTPApproval
	root              *os.File
	rootStat          unix.Stat_t
	lock              *os.File
	marker            *os.File
	markerStat        unix.Stat_t
	authority, policy firstHTTPHeldFile
	closed            bool
	closeErr          error
}

func (*firstHTTPM01Authority) String() string   { return "server.firstHTTPM01Authority[protected]" }
func (*firstHTTPM01Authority) GoString() string { return "server.firstHTTPM01Authority[protected]" }

func firstHTTPStatEqual(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Mode == b.Mode && a.Uid == b.Uid && a.Gid == b.Gid && a.Nlink == b.Nlink && a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}
func firstHTTPRegular(stat unix.Stat_t, uid uint32) bool {
	return stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Mode&07777 == 0600 && stat.Uid == uid && stat.Nlink == 1
}

// 조립의 수명 동안 디렉터리 FD를 유지한다. 각 조상은 신뢰 UID 소유이며 쓰기 경계를 넘지 않는다.
func firstHTTPOpenRoot(path string) (*os.File, unix.Stat_t, error) {
	var empty unix.Stat_t
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || privateinput.CheckDirectory(path, true) != nil {
		return nil, empty, ErrFirstDiagnosisHTTPHost
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, empty, ErrFirstDiagnosisHTTPHost
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if part == "" {
			continue
		}
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if e != nil {
			return nil, empty, ErrFirstDiagnosisHTTPHost
		}
		fd = next
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) || (st.Mode&0022 != 0 && !(st.Uid == 0 && st.Mode&unix.S_ISVTX != 0)) {
			_ = unix.Close(fd)
			return nil, empty, ErrFirstDiagnosisHTTPHost
		}
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != uint32(os.Geteuid()) || st.Mode&07777 != 0700 {
		_ = unix.Close(fd)
		return nil, empty, ErrFirstDiagnosisHTTPHost
	}
	return os.NewFile(uintptr(fd), "first-http-authority-root"), st, nil
}
func firstHTTPReadHeld(root *os.File, name string, limit int64) (firstHTTPHeldFile, []byte, error) {
	var empty firstHTTPHeldFile
	fd, err := unix.Openat(int(root.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return empty, nil, ErrFirstDiagnosisHTTPHost
	}
	f := os.NewFile(uintptr(fd), "first-http-protected-input")
	var before, after, atPath unix.Stat_t
	if unix.Fstat(fd, &before) != nil || !firstHTTPRegular(before, uint32(os.Geteuid())) || before.Size <= 0 || before.Size > limit {
		_ = f.Close()
		return empty, nil, ErrFirstDiagnosisHTTPHost
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) != before.Size || unix.Fstat(fd, &after) != nil || unix.Fstatat(int(root.Fd()), name, &atPath, unix.AT_SYMLINK_NOFOLLOW) != nil || !firstHTTPStatEqual(before, after) || !firstHTTPStatEqual(before, atPath) {
		clear(raw)
		_ = f.Close()
		return empty, nil, ErrFirstDiagnosisHTTPHost
	}
	return firstHTTPHeldFile{f, name, before, sha256.Sum256(raw)}, raw, nil
}
func firstHTTPHeldCurrent(root *os.File, held firstHTTPHeldFile) bool {
	if held.file == nil {
		return false
	}
	var fdstat, pathstat unix.Stat_t
	if unix.Fstat(int(held.file.Fd()), &fdstat) != nil || unix.Fstatat(int(root.Fd()), held.name, &pathstat, unix.AT_SYMLINK_NOFOLLOW) != nil || !firstHTTPStatEqual(held.stat, fdstat) || !firstHTTPStatEqual(held.stat, pathstat) {
		return false
	}
	raw, err := io.ReadAll(io.NewSectionReader(held.file, 0, held.stat.Size+1))
	defer clear(raw)
	var after unix.Stat_t
	return err == nil && int64(len(raw)) == held.stat.Size && sha256.Sum256(raw) == held.digest && unix.Fstat(int(held.file.Fd()), &after) == nil && firstHTTPStatEqual(held.stat, after)
}

func newFirstDiagnosisHTTPAuthority(ctx context.Context, cfg FirstDiagnosisHTTPHostConfig) (firstDiagnosisHTTPAuthority, error) {
	if ctx == nil || ctx.Err() != nil || os.Geteuid() <= 0 || cfg.AuthorityRoot == "" || filepath.Dir(cfg.PolicyFile) != cfg.AuthorityRoot || filepath.Dir(cfg.AuthorityFile) != cfg.AuthorityRoot || cfg.PolicyFile == cfg.AuthorityFile {
		return nil, ErrFirstDiagnosisHTTPHost
	}
	for _, p := range []string{cfg.PolicyFile, cfg.AuthorityFile} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || !e2HTTPHostRef.MatchString(filepath.Base(p)) {
			return nil, ErrFirstDiagnosisHTTPHost
		}
	}
	root, st, err := firstHTTPOpenRoot(cfg.AuthorityRoot)
	if err != nil {
		return nil, err
	}
	a := &firstHTTPM01Authority{cfg: cfg, root: root, rootStat: st}
	failed := func() (firstDiagnosisHTTPAuthority, error) { _ = a.close(); return nil, ErrFirstDiagnosisHTTPHost }
	authority, raw, err := firstHTTPReadHeld(root, filepath.Base(cfg.AuthorityFile), firstHTTPAuthorityLimit)
	if err != nil {
		return failed()
	}
	a.authority = authority
	var grant FirstDiagnosisHTTPApproval
	decodeErr := strictJSON(raw, &grant)
	clear(raw)
	digest, err := hex.DecodeString(grant.PolicySHA256)
	now := time.Now()
	if decodeErr != nil || grant.Format != FirstDiagnosisHTTPAuthorityFormat || !e2HTTPHostRef.MatchString(grant.ApprovalRef) || grant.OperatorUID != uint32(os.Geteuid()) || grant.TaskID <= 0 || grant.ExplorationID <= 0 || err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != grant.PolicySHA256 || grant.ValidFrom.IsZero() || grant.ValidFrom.After(now) || !grant.ValidUntil.After(now) || !grant.ValidUntil.After(grant.ValidFrom) || grant.ValidUntil.Sub(grant.ValidFrom) > MaxFirstDiagnosisHTTPAuthority {
		return failed()
	}
	a.grant = grant
	policy, policyRaw, err := firstHTTPReadHeld(root, filepath.Base(cfg.PolicyFile), 256<<10)
	if err != nil {
		return failed()
	}
	a.policy = policy
	clear(policyRaw)
	if !bytes.Equal(digest, policy.digest[:]) {
		return failed()
	}
	lockfd, err := unix.Openat(int(root.Fd()), ".first-http.lock", unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return failed()
	}
	a.lock = os.NewFile(uintptr(lockfd), "first-http-authority-lease")
	var lockstat unix.Stat_t
	if unix.Fstat(lockfd, &lockstat) != nil || !firstHTTPRegular(lockstat, uint32(os.Geteuid())) || unix.Flock(lockfd, unix.LOCK_EX|unix.LOCK_NB) != nil {
		return failed()
	}
	// O_EXCL 마커는 Close 후에도 남긴다. 운영자는 새 승인과 새 전용 디렉터리를 준비해야 한다.
	markerfd, err := unix.Openat(int(root.Fd()), ".first-http.started", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return failed()
	}
	marker := os.NewFile(uintptr(markerfd), "first-http-start-marker")
	a.marker = marker
	markerErr := marker.Sync()
	if markerErr != nil || unix.Fstat(markerfd, &a.markerStat) != nil || root.Sync() != nil {
		return failed()
	}
	a.ctx, a.cancel = context.WithDeadline(ctx, grant.ValidUntil)
	if a.CheckRoot(ctx) != nil {
		return failed()
	}
	return a, nil
}
func (a *firstHTTPM01Authority) Context() context.Context {
	if a == nil {
		return nil
	}
	return a.ctx
}
func (a *firstHTTPM01Authority) approval() FirstDiagnosisHTTPApproval {
	if a == nil {
		return FirstDiagnosisHTTPApproval{}
	}
	return a.grant
}
func (a *firstHTTPM01Authority) revoke() {
	if a != nil && a.cancel != nil {
		a.cancel()
	}
}
func (a *firstHTTPM01Authority) CheckRoot(ctx context.Context) error {
	if a == nil || ctx == nil || ctx.Err() != nil {
		return ErrFirstDiagnosisHTTPHost
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.ctx == nil || a.ctx.Err() != nil || a.root == nil || a.lock == nil || uint32(os.Geteuid()) != a.grant.OperatorUID || privateinput.CheckDirectory(a.cfg.AuthorityRoot, true) != nil {
		a.revoke()
		return ErrFirstDiagnosisHTTPHost
	}
	var held, atPath, lockstat, lockPath unix.Stat_t
	if unix.Fstat(int(a.root.Fd()), &held) != nil || unix.Lstat(a.cfg.AuthorityRoot, &atPath) != nil || held.Dev != a.rootStat.Dev || held.Ino != a.rootStat.Ino || held.Mode&07777 != 0700 || held.Uid != a.grant.OperatorUID || held.Dev != atPath.Dev || held.Ino != atPath.Ino || held.Mode != atPath.Mode || held.Uid != atPath.Uid || unix.Fstat(int(a.lock.Fd()), &lockstat) != nil || unix.Fstatat(int(a.root.Fd()), ".first-http.lock", &lockPath, unix.AT_SYMLINK_NOFOLLOW) != nil || !firstHTTPRegular(lockstat, a.grant.OperatorUID) || lockstat.Dev != lockPath.Dev || lockstat.Ino != lockPath.Ino || !firstHTTPHeldCurrent(a.root, a.authority) || !firstHTTPHeldCurrent(a.root, a.policy) {
		a.revoke()
		return ErrFirstDiagnosisHTTPHost
	}
	var marker, markerHeld unix.Stat_t
	if a.marker == nil || unix.Fstat(int(a.marker.Fd()), &markerHeld) != nil || unix.Fstatat(int(a.root.Fd()), ".first-http.started", &marker, unix.AT_SYMLINK_NOFOLLOW) != nil || !firstHTTPStatEqual(a.markerStat, markerHeld) || !firstHTTPStatEqual(a.markerStat, marker) || !firstHTTPRegular(marker, a.grant.OperatorUID) || marker.Size != 0 {
		a.revoke()
		return ErrFirstDiagnosisHTTPHost
	}
	return nil
}
func (a *firstHTTPM01Authority) AuthorizePolicy(ctx context.Context, digest [32]byte) error {
	if a.CheckRoot(ctx) != nil || digest != a.policy.digest {
		return ErrFirstDiagnosisHTTPHost
	}
	return nil
}
func (a *firstHTTPM01Authority) close() error {
	if a == nil {
		return ErrFirstDiagnosisHTTPHost
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return a.closeErr
	}
	a.revoke()
	// 실행 drain은 호출자가 검증한 뒤에만 이 소유권 해제를 호출한다.
	var failure bool
	for _, f := range []*os.File{a.authority.file, a.policy.file, a.marker, a.lock, a.root} {
		if f != nil && f.Close() != nil {
			failure = true
		}
	}
	a.closed = true
	if failure {
		a.closeErr = ErrE2HTTPClosure
		return a.closeErr
	}
	return nil
}
