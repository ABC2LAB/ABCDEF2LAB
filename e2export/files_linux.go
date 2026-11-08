//go:build linux

package e2export

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

type exportDirectory struct {
	root *os.Root
	dir  *os.File
	info os.FileInfo
}

func openExportDirectory(path string, exclusive bool) (*exportDirectory, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrConfig
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return nil, ErrConfig
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, ErrConfig
	}
	info, err := f.Stat()
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		f.Close()
		return nil, ErrConfig
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) {
		f.Close()
		return nil, ErrConfig
	}
	if exclusive && syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.Close()
		return nil, ErrConfig
	}
	root, err := os.OpenRoot("/proc/self/fd/" + itoaFD(f.Fd()))
	if err != nil {
		f.Close()
		return nil, ErrConfig
	}
	return &exportDirectory{root: root, dir: f, info: info}, nil
}
func itoaFD(fd uintptr) string {
	if fd == 0 {
		return "0"
	}
	var b [32]byte
	i := len(b)
	for fd > 0 {
		i--
		b[i] = byte('0' + fd%10)
		fd /= 10
	}
	return string(b[i:])
}
func (d *exportDirectory) same(other *exportDirectory) bool {
	return d != nil && other != nil && os.SameFile(d.info, other.info)
}
func (d *exportDirectory) close() {
	if d == nil {
		return
	}
	if d.root != nil {
		_ = d.root.Close()
	}
	if d.dir != nil {
		_ = d.dir.Close()
	}
}
func (d *exportDirectory) read(name string, maximum int64) ([]byte, error) {
	if !filePattern.MatchString(name) {
		return nil, ErrPublish
	}
	f, err := d.root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, ErrPublish
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !privateFile(before) || before.Size() > maximum {
		return nil, ErrPublish
	}
	data, err := io.ReadAll(io.LimitReader(f, maximum+1))
	if err != nil || int64(len(data)) > maximum {
		return nil, ErrPublish
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, ErrPublish
	}
	return data, nil
}
func privateFile(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && info.Mode().Perm() == 0600 && st.Uid == uint32(os.Geteuid()) && st.Nlink == 1
}
func (d *exportDirectory) write(name string, data []byte) error {
	if !filePattern.MatchString(name) {
		return ErrPublish
	}
	temp := ".pending-" + name
	f, err := d.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return ErrPublish
	}
	clean := true
	defer func() {
		if clean {
			_ = d.root.Remove(temp)
		}
	}()
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return ErrPublish
	}
	if f.Sync() != nil {
		_ = f.Close()
		return ErrPublish
	}
	if f.Close() != nil {
		return ErrPublish
	}
	// Link는 기존 완성 파일을 교체하지 않으면서 같은 파일시스템에 원자 게시한다.
	if d.root.Link(temp, name) != nil {
		return ErrPublish
	}
	if d.root.Remove(temp) != nil {
		return ErrPublish
	}
	clean = false
	if d.dir.Sync() != nil {
		return ErrPublish
	}
	published, err := d.read(name, int64(len(data)))
	if err != nil || !bytes.Equal(published, data) {
		return ErrPublish
	}
	return nil
}
