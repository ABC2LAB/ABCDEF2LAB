//go:build linux

package server

import (
	"encoding/hex"
	"os"
	"strings"

	"github.com/Autumn-27/artex/internal/privateinput"
	"golang.org/x/sys/unix"
)

func openControlledLocalPhysical(b ControlledReadLocalBinding, digest [32]byte) (*controlledLocalPhysical, error) {
	p := &controlledLocalPhysical{}
	ok := false
	defer func() {
		if !ok {
			_ = closeControlledLocalPhysical(p)
		}
	}()
	for _, item := range []struct {
		pin    ControlledReadLocalDirectory
		target **os.File
	}{{b.State, &p.state}, {b.Journals, &p.journals}, {b.Descriptors, &p.descriptors}, {b.Keys, &p.keys}} {
		file, namespace, err := openControlledLocalDirectory(item.pin)
		if err != nil {
			return nil, ErrControlledReadLocalHost
		}
		*item.target = file
		p.namespaces = append(p.namespaces, namespace)
	}
	// 실제 inode에 배타 잠금을 유지한다. 같은 경로명의 교체 inode는 승인되지 않는다.
	if unix.Flock(int(p.state.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil || unix.Flock(int(p.keys.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return nil, ErrControlledReadLocalHost
	}
	name := "first-diagnosis-" + hex.EncodeToString(digest[:]) + ".consumed"
	fd, err := unix.Openat(int(p.keys.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, ErrControlledReadLocalHost
	}
	p.writer = os.NewFile(uintptr(fd), name)
	if p.writer == nil {
		return nil, ErrControlledReadLocalHost
	}
	// 한번 만들어진 표식은 실패·종료에서도 지우지 않는다. 복구에는 별도 설치 권위가 필요하다.
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Nlink != 1 || st.Uid != uint32(os.Geteuid()) || st.Mode&07777 != 0600 {
		return nil, ErrControlledReadLocalHost
	}
	body := []byte(hex.EncodeToString(digest[:]) + "\n")
	if n, e := p.writer.Write(body); e != nil || n != len(body) {
		return nil, ErrControlledReadLocalHost
	}
	if p.writer.Sync() != nil || p.keys.Sync() != nil || checkControlledLocalPhysical(p, b) != nil {
		return nil, ErrControlledReadLocalHost
	}
	ok = true
	return p, nil
}
func openControlledLocalDirectory(pin ControlledReadLocalDirectory) (*os.File, *controlledLocalNamespace, error) {
	if privateinput.CheckDirectory(pin.Path, true) != nil {
		return nil, nil, ErrControlledReadLocalHost
	}
	fd, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, ErrControlledReadLocalHost
	}
	ns := &controlledLocalNamespace{parts: strings.Split(strings.TrimPrefix(pin.Path, "/"), "/")}
	root := os.NewFile(uintptr(fd), "/")
	ns.parents = append(ns.parents, root)
	fail := func() (*os.File, *controlledLocalNamespace, error) {
		for _, f := range ns.parents {
			_ = f.Close()
		}
		if ns.target != nil {
			_ = ns.target.Close()
		}
		return nil, nil, ErrControlledReadLocalHost
	}
	for i, part := range ns.parts {
		flags := unix.O_PATH | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
		if i == len(ns.parts)-1 {
			flags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
		}
		next, e := unix.Openat(int(ns.parents[len(ns.parents)-1].Fd()), part, flags, 0)
		if e != nil {
			return fail()
		}
		file := os.NewFile(uintptr(next), part)
		if i == len(ns.parts)-1 {
			ns.target = file
		} else {
			ns.parents = append(ns.parents, file)
		}
	}
	if checkControlledLocalDirectory(ns.target, pin) != nil || checkControlledLocalNamespace(ns) != nil {
		return fail()
	}
	return ns.target, ns, nil
}
func checkControlledLocalNamespace(ns *controlledLocalNamespace) error {
	if ns == nil || len(ns.parents) != len(ns.parts) || ns.target == nil {
		return ErrControlledReadLocalHost
	}
	chain := append(append([]*os.File(nil), ns.parents...), ns.target)
	for i, file := range chain {
		var held unix.Stat_t
		if unix.Fstat(int(file.Fd()), &held) != nil || held.Mode&unix.S_IFMT != unix.S_IFDIR || (held.Uid != 0 && held.Uid != uint32(os.Geteuid())) || held.Mode&0022 != 0 && !(held.Uid == 0 && held.Mode&unix.S_ISVTX != 0) {
			return ErrControlledReadLocalHost
		}
		if i > 0 {
			var at unix.Stat_t
			if unix.Fstatat(int(chain[i-1].Fd()), ns.parts[i-1], &at, unix.AT_SYMLINK_NOFOLLOW) != nil || at.Dev != held.Dev || at.Ino != held.Ino || at.Mode != held.Mode || at.Uid != held.Uid {
				return ErrControlledReadLocalHost
			}
		}
	}
	return nil
}
func checkControlledLocalDirectory(f *os.File, pin ControlledReadLocalDirectory) error {
	if f == nil {
		return ErrControlledReadLocalHost
	}
	var held, current unix.Stat_t
	if unix.Fstat(int(f.Fd()), &held) != nil || unix.Lstat(pin.Path, &current) != nil || held.Dev != pin.Device || held.Ino != pin.Inode || current.Dev != held.Dev || current.Ino != held.Ino || held.Mode&unix.S_IFMT != unix.S_IFDIR || current.Mode&unix.S_IFMT != unix.S_IFDIR || held.Mode&07777 != 0700 || current.Mode&07777 != 0700 || held.Uid != uint32(os.Geteuid()) || current.Uid != held.Uid {
		return ErrControlledReadLocalHost
	}
	return nil
}
func checkControlledLocalPhysical(p *controlledLocalPhysical, b ControlledReadLocalBinding) error {
	if p == nil {
		return ErrControlledReadLocalHost
	}
	for _, ns := range p.namespaces {
		if checkControlledLocalNamespace(ns) != nil {
			return ErrControlledReadLocalHost
		}
	}
	for _, item := range []struct {
		file *os.File
		pin  ControlledReadLocalDirectory
	}{{p.state, b.State}, {p.journals, b.Journals}, {p.descriptors, b.Descriptors}, {p.keys, b.Keys}} {
		if checkControlledLocalDirectory(item.file, item.pin) != nil {
			return ErrControlledReadLocalHost
		}
	}
	if p.writer == nil {
		return ErrControlledReadLocalHost
	}
	var held, current unix.Stat_t
	if unix.Fstat(int(p.writer.Fd()), &held) != nil || unix.Fstatat(int(p.keys.Fd()), p.writer.Name(), &current, unix.AT_SYMLINK_NOFOLLOW) != nil || held.Dev != current.Dev || held.Ino != current.Ino || held.Nlink != 1 || current.Nlink != 1 || current.Mode&unix.S_IFMT != unix.S_IFREG || current.Mode&07777 != 0600 || current.Uid != uint32(os.Geteuid()) {
		return ErrControlledReadLocalHost
	}
	return nil
}
func closeControlledLocalPhysical(p *controlledLocalPhysical) error {
	if p == nil {
		return nil
	}
	var result error
	for _, f := range []*os.File{p.writer, p.descriptors, p.journals, p.state, p.keys} {
		if f != nil {
			if f.Close() != nil {
				result = ErrControlledReadLocalHost
			}
		}
	}
	for _, ns := range p.namespaces {
		for i := len(ns.parents) - 1; i >= 0; i-- {
			if ns.parents[i].Close() != nil {
				result = ErrControlledReadLocalHost
			}
		}
	}
	return result
}

// 설치용 비민감 진단 값이며 권위를 생성하지 않는다.
func ControlledReadLocalDirectoryIdentity(path string) (ControlledReadLocalDirectory, error) {
	if privateinput.CheckDirectory(path, true) != nil {
		return ControlledReadLocalDirectory{}, ErrControlledReadLocalHost
	}
	var st unix.Stat_t
	if unix.Lstat(path, &st) != nil || st.Ino == 0 || st.Dev == 0 {
		return ControlledReadLocalDirectory{}, ErrControlledReadLocalHost
	}
	return ControlledReadLocalDirectory{Path: path, Device: st.Dev, Inode: st.Ino}, nil
}
