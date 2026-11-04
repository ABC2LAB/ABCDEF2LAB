//go:build linux

package evidencerevision

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

type descriptorRoot struct {
	dir           *os.File
	path          string
	info          os.FileInfo
	ctx, leaseCtx context.Context
}

func descriptorName(operation string) string {
	digest := sha256.Sum256([]byte("artex.evidence.descriptor.file.v1\x00" + operation))
	return "descriptor-" + hex.EncodeToString(digest[:]) + ".json"
}

func descriptorDirectory(info os.FileInfo) bool {
	if info == nil || info.Mode() != os.ModeDir|0o700 {
		return false
	}
	return descriptorOwner(info, false)
}

func descriptorFile(info os.FileInfo) bool {
	return info != nil && info.Mode() == 0o600 && info.Size() >= 0 && info.Size() <= DescriptorFileMaxBytes && descriptorOwner(info, true)
}

func (s *DescriptorFiles) acquireDescriptorRoot(ctx context.Context, operation string, purpose DescriptorFilePurpose) (*descriptorRoot, DescriptorRootScope, error) {
	if s == nil || descriptorNil(s.provider) || ctx == nil || ctx.Err() != nil {
		return nil, nil, ErrScope
	}
	scope, err := s.provider.AcquireDescriptorRoot(ctx, DescriptorFileAccess{Purpose: purpose, OperationRef: operation})
	if err != nil {
		return nil, nil, ErrScope
	} // provider owns partial cleanup on error
	if descriptorNil(scope) {
		return nil, nil, ErrScope
	}
	leaseCtx, anchor, path := scope.Context(), scope.Anchor(), scope.Directory()
	if leaseCtx == nil || leaseCtx.Err() != nil || ctx.Err() != nil || anchor == nil || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, scope, ErrScope
	}
	trusted, err := anchor.Stat()
	if err != nil || !descriptorDirectory(trusted) {
		return nil, scope, ErrScope
	}
	// Duplicate the already trusted anchor, never reconstruct it from a path.
	fd, err := unix.Openat(int(anchor.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, scope, descriptorFileError{"root_open", ErrDescriptorFileStorage}
	}
	r := &descriptorRoot{dir: os.NewFile(uintptr(fd), "descriptor-root"), path: path, info: trusted, ctx: ctx, leaseCtx: leaseCtx}
	if r.current() != nil {
		return r, scope, ErrScope
	}
	return r, scope, nil
}

func (r *descriptorRoot) live() bool { return r.ctx.Err() == nil && r.leaseCtx.Err() == nil }

func (r *descriptorRoot) current() error {
	if !r.live() {
		return ErrScope
	}
	info, err := r.dir.Stat()
	if err != nil || !descriptorDirectory(info) || !os.SameFile(r.info, info) {
		return ErrScope
	}
	fd, err := unix.Open(r.path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return ErrScope
	}
	current := os.NewFile(uintptr(fd), "descriptor-current-root")
	info, statErr := current.Stat()
	closeErr := current.Close()
	if statErr != nil || closeErr != nil || !descriptorDirectory(info) || !os.SameFile(r.info, info) || !r.live() {
		return ErrScope
	}
	return nil
}

func (r *descriptorRoot) currentFile(name string, expected os.FileInfo) bool {
	if r.current() != nil {
		return false
	}
	var st unix.Stat_t
	if unix.Fstatat(int(r.dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW) != nil {
		return false
	}
	return descriptorSameStat(expected, st) && st.Mode == unix.S_IFREG|0o600 && st.Uid == uint32(os.Geteuid()) && st.Nlink == 1 && st.Size >= 0 && st.Size <= DescriptorFileMaxBytes
}

func (r *descriptorRoot) read(name string) (*os.File, []byte, error) {
	if r.current() != nil {
		return nil, nil, ErrScope
	}
	fd, err := unix.Openat(int(r.dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, descriptorFileError{"file_open", ErrDescriptorFileStorage}
	}
	f := os.NewFile(uintptr(fd), "descriptor-file")
	before, err := f.Stat()
	if err != nil || !descriptorFile(before) || !r.currentFile(name, before) {
		return f, nil, descriptorFileError{"file_metadata", ErrDescriptorFileStorage}
	}
	body, err := io.ReadAll(io.LimitReader(f, DescriptorFileMaxBytes+1))
	after, statErr := f.Stat()
	if err != nil || statErr != nil || !descriptorFile(after) || len(body) > DescriptorFileMaxBytes || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || !r.currentFile(name, after) {
		return f, nil, descriptorFileError{"file_read", ErrDescriptorFileStorage}
	}
	return f, body, nil
}

func (s *DescriptorFiles) putDescriptor(ctx context.Context, operation string, body []byte) (err error) {
	r, scope, err := s.acquireDescriptorRoot(ctx, operation, PersistDescriptorFile)
	if scope != nil {
		defer scope.Release()
	}
	if r != nil {
		defer func() {
			closeErr := r.dir.Close()
			if s.fail("root_close") || closeErr != nil {
				err = descriptorFileError{"root_close", ErrDescriptorFileUnknown}
			}
		}()
	}
	if err != nil {
		return err
	}
	name := descriptorName(operation)
	if handled, err := s.retryDescriptor(r, name, body); handled {
		return err
	}
	if r.current() != nil {
		return ErrScope
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return descriptorFileError{"random", ErrDescriptorFileStorage}
	}
	tmp := name + ".pending-" + hex.EncodeToString(nonce[:])
	fd, err := unix.Openat(int(r.dir.Fd()), tmp, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return descriptorFileError{"create", ErrDescriptorFileUnknown}
	}
	f := os.NewFile(uintptr(fd), "descriptor-pending")
	defer func() {
		closeErr := f.Close()
		if s.fail("file_close") || closeErr != nil {
			err = descriptorFileError{"file_close", ErrDescriptorFileUnknown}
		}
	}()
	// Correct only this new O_EXCL file for a restrictive umask; never chmod root
	// or an existing artifact. Failed/unknown files are deliberately retained.
	if f.Chmod(0o600) != nil {
		return descriptorFileError{"mode", ErrDescriptorFileUnknown}
	}
	before, statErr := f.Stat()
	if statErr != nil || !descriptorFile(before) || !r.currentFile(tmp, before) {
		return descriptorFileError{"created_metadata", ErrDescriptorFileUnknown}
	}
	if s.fail("write") {
		_, _ = f.Write(body[:len(body)/2])
		return descriptorFileError{"write", ErrDescriptorFileUnknown}
	}
	n, writeErr := f.Write(body)
	if writeErr != nil || n != len(body) {
		return descriptorFileError{"write", ErrDescriptorFileUnknown}
	}
	if s.fail("file_sync") || f.Sync() != nil {
		return descriptorFileError{"file_sync", ErrDescriptorFileUnknown}
	}
	after, statErr := f.Stat()
	if statErr != nil || !descriptorFile(after) || !os.SameFile(before, after) || after.Size() != int64(len(body)) || !r.currentFile(tmp, after) {
		return descriptorFileError{"written_metadata", ErrDescriptorFileUnknown}
	}
	if s.fail("publish") {
		return descriptorFileError{"publish", ErrDescriptorFileUnknown}
	}
	if err := unix.Renameat2(int(r.dir.Fd()), tmp, int(r.dir.Fd()), name, unix.RENAME_NOREPLACE); err != nil {
		if errors.Is(err, unix.EEXIST) {
			handled, retryErr := s.retryDescriptor(r, name, body)
			if !handled {
				return descriptorFileError{"publish_race", ErrDescriptorFileUnknown}
			}
			return retryErr
		}
		return descriptorFileError{"publish", ErrDescriptorFileUnknown}
	}
	if s.fail("dir_sync") || r.dir.Sync() != nil {
		return descriptorFileError{"dir_sync", ErrDescriptorFileUnknown}
	}
	if !r.currentFile(name, after) {
		return descriptorFileError{"published_metadata", ErrDescriptorFileUnknown}
	}
	if s.fail("ack") {
		return descriptorFileError{"ack", ErrDescriptorFileUnknown}
	}
	return nil
}

func (s *DescriptorFiles) retryDescriptor(r *descriptorRoot, name string, body []byte) (handled bool, err error) {
	f, existing, err := r.read(name)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if f != nil {
		defer func() {
			closeErr := f.Close()
			if s.fail("retry_close") || closeErr != nil {
				err = descriptorFileError{"retry_close", ErrDescriptorFileUnknown}
			}
		}()
	}
	if err != nil {
		return true, err
	}
	if !bytes.Equal(body, existing) {
		return true, ErrConflict
	}
	if s.fail("retry_file_sync") || f.Sync() != nil {
		return true, descriptorFileError{"retry_file_sync", ErrDescriptorFileUnknown}
	}
	if s.fail("retry_dir_sync") || r.dir.Sync() != nil {
		return true, descriptorFileError{"retry_dir_sync", ErrDescriptorFileUnknown}
	}
	info, statErr := f.Stat()
	if statErr != nil || !descriptorFile(info) || !r.currentFile(name, info) {
		return true, descriptorFileError{"retry_metadata", ErrDescriptorFileUnknown}
	}
	return true, nil
}

func (s *DescriptorFiles) loadDescriptor(ctx context.Context, operation string) (out Descriptor, err error) {
	r, scope, err := s.acquireDescriptorRoot(ctx, operation, ReadDescriptorFile)
	if scope != nil {
		defer scope.Release()
	}
	if r != nil {
		defer func() {
			closeErr := r.dir.Close()
			if s.fail("root_close") || closeErr != nil {
				out, err = Descriptor{}, descriptorFileError{"root_close", ErrDescriptorFileStorage}
			}
		}()
	}
	if err != nil {
		return Descriptor{}, err
	}
	f, body, err := r.read(descriptorName(operation))
	if f != nil {
		defer func() {
			closeErr := f.Close()
			if s.fail("file_close") || closeErr != nil {
				out, err = Descriptor{}, descriptorFileError{"file_close", ErrDescriptorFileStorage}
			}
		}()
	}
	if err != nil {
		return Descriptor{}, err
	}
	out, err = decodeDescriptorFile(body, operation)
	if err == nil && r.current() != nil {
		return Descriptor{}, ErrScope
	}
	return out, err
}
