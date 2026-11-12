package e2sandbox

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"
)

// 작업 공간 보관(docs/workspace-decision.md, C29). 같은 intent의 호출 사이에 /work를 이어 주려고 일반 파일과
// 디렉터리만 tar로 묶고 푼다. 링크·장치·FIFO·소켓·하드 링크는 버린다. 권한은 0600/0700으로 다시 쓰고 소유자·
// 시간은 옮기지 않는다. 풀 때는 빈 디렉터리에만 풀고, 상대 경로만 받는다.

var (
	ErrWorkspaceTooLarge = errors.New("e2sandbox: workspace exceeds the scratch limit")
	ErrWorkspaceInvalid  = errors.New("e2sandbox: workspace archive invalid")
)

const (
	// 작업 공간 항목(파일·디렉터리) 수 상한. tar 머리말 크기를 내용 상한 밖에서 묶는다.
	maxWorkspaceEntries = 4096
	// 경로 하나의 바이트 상한(PATH_MAX).
	maxWorkspacePath = 4096
	// tar 항목 하나의 머리말·채움 바이트 상한과 끝 블록(1KiB).
	workspaceEntryOverhead = 1024
	workspaceTrailer       = 1024
)

// workspaceArchiveLimit은 내용 상한이 contentLimit인 작업 공간 tar의 바이트 상한이다.
func workspaceArchiveLimit(contentLimit uint64) int64 {
	return int64(contentLimit) + maxWorkspaceEntries*workspaceEntryOverhead + workspaceTrailer
}

// packWorkspace는 root 아래의 일반 파일·디렉터리를 tar로 묶는다. 내용 합계가 contentLimit를 넘거나 항목이 너무
// 많으면 ErrWorkspaceTooLarge다. 묶는 동안 root를 바꾸는 프로세스가 없어야 한다(호출자가 보장).
func packWorkspace(root string, contentLimit uint64) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	var total uint64
	entries := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case info.Mode().IsDir():
			if entries++; entries > maxWorkspaceEntries {
				return ErrWorkspaceTooLarge
			}
			return tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: 0o700, Format: tar.FormatPAX})
		case info.Mode().IsRegular():
			if st, ok := info.Sys().(*syscall.Stat_t); !ok || st.Nlink != 1 {
				return nil // 하드 링크는 버린다.
			}
			if entries++; entries > maxWorkspaceEntries {
				return ErrWorkspaceTooLarge
			}
			size := uint64(info.Size())
			if total += size; total > contentLimit {
				return ErrWorkspaceTooLarge
			}
			f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
			if err != nil {
				return err
			}
			defer f.Close()
			if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o600, Size: int64(size), Format: tar.FormatPAX}); err != nil {
				return err
			}
			n, err := io.Copy(tw, io.LimitReader(f, int64(size)))
			if err != nil || n != int64(size) {
				return ErrWorkspaceInvalid
			}
			return nil
		default:
			return nil // 심볼릭 링크·장치·FIFO·소켓은 버린다.
		}
	})
	if err != nil {
		if errors.Is(err, ErrWorkspaceTooLarge) {
			return nil, ErrWorkspaceTooLarge
		}
		return nil, ErrWorkspaceInvalid
	}
	if err := tw.Close(); err != nil {
		return nil, ErrWorkspaceInvalid
	}
	return buf.Bytes(), nil
}

// validWorkspaceName은 tar 항목 이름이 root 기준 상대 경로이고 정규화돼 있는지 본다.
func validWorkspaceName(name string) bool {
	if name == "" || len(name) > maxWorkspacePath || !utf8.ValidString(name) || strings.ContainsRune(name, 0) ||
		strings.HasPrefix(name, "/") || path.Clean(name) != name || name == "." {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." || part == "." || part == "" {
			return false
		}
	}
	return true
}

// unpackWorkspace는 packWorkspace가 만든 tar를 빈 디렉터리 root에 푼다. 일반 파일·디렉터리만 받고, 경로·크기·
// 항목 수·중복을 검사한다. 빈 입력은 빈 작업 공간이다.
func unpackWorkspace(data []byte, root string, contentLimit uint64) error {
	if len(data) == 0 {
		return nil
	}
	if int64(len(data)) > workspaceArchiveLimit(contentLimit) {
		return ErrWorkspaceTooLarge
	}
	tr := tar.NewReader(bytes.NewReader(data))
	seen := make(map[string]bool)
	var total uint64
	entries := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return ErrWorkspaceInvalid
		}
		name := hdr.Name
		if hdr.Typeflag == tar.TypeDir {
			name = strings.TrimSuffix(name, "/")
		}
		if !validWorkspaceName(name) || seen[name] {
			return ErrWorkspaceInvalid
		}
		seen[name] = true
		if entries++; entries > maxWorkspaceEntries {
			return ErrWorkspaceTooLarge
		}
		target := filepath.Join(root, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			// 부모는 앞 항목이 만든 디렉터리여야 한다(MkdirAll로 만들지 않는다).
			if err := os.Mkdir(target, 0o700); err != nil {
				return ErrWorkspaceInvalid
			}
		case tar.TypeReg:
			if hdr.Size < 0 {
				return ErrWorkspaceInvalid
			}
			if total += uint64(hdr.Size); total > contentLimit {
				return ErrWorkspaceTooLarge
			}
			f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
			if err != nil {
				return ErrWorkspaceInvalid
			}
			n, copyErr := io.Copy(f, io.LimitReader(tr, hdr.Size))
			closeErr := f.Close()
			if copyErr != nil || closeErr != nil || n != hdr.Size {
				return ErrWorkspaceInvalid
			}
		default:
			return ErrWorkspaceInvalid
		}
	}
}

// ValidateWorkspace는 tar가 풀기 규칙(일반 파일·디렉터리, 상대 정규 경로, 중복 없음, 부모 디렉터리가 앞에 있음,
// 내용·항목 상한)에 맞는지 파일 시스템에 쓰지 않고 본다. 호스트가 샌드박스에서 받은 작업 공간을 보관하기 전에 쓴다.
func ValidateWorkspace(data []byte, contentLimit uint64) error {
	if len(data) == 0 {
		return nil
	}
	if int64(len(data)) > workspaceArchiveLimit(contentLimit) {
		return ErrWorkspaceTooLarge
	}
	tr := tar.NewReader(bytes.NewReader(data))
	seen := make(map[string]bool)
	dirs := map[string]bool{".": true}
	var total uint64
	entries := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return ErrWorkspaceInvalid
		}
		name := hdr.Name
		if hdr.Typeflag == tar.TypeDir {
			name = strings.TrimSuffix(name, "/")
		}
		if !validWorkspaceName(name) || seen[name] || !dirs[path.Dir(name)] {
			return ErrWorkspaceInvalid
		}
		seen[name] = true
		if entries++; entries > maxWorkspaceEntries {
			return ErrWorkspaceTooLarge
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			dirs[name] = true
		case tar.TypeReg:
			if hdr.Size < 0 {
				return ErrWorkspaceInvalid
			}
			if total += uint64(hdr.Size); total > contentLimit {
				return ErrWorkspaceTooLarge
			}
			if n, err := io.Copy(io.Discard, io.LimitReader(tr, hdr.Size)); err != nil || n != hdr.Size {
				return ErrWorkspaceInvalid
			}
		default:
			return ErrWorkspaceInvalid
		}
	}
}

// AppendWorkspaceFile은 작업 공간 tar에 파일 하나를 더한 새 tar를 돌려준다. 부모 디렉터리가 없으면 만들고, 같은 이름이
// 있으면 바꾼다. 결과도 풀기 규칙과 내용·항목 상한을 지켜야 한다(넘으면 ErrWorkspaceTooLarge). 러너가 큰 출력을 작업
// 공간에 남길 때 쓴다(docs/workspace-decision.md §2-3).
func AppendWorkspaceFile(archive []byte, name string, content []byte, contentLimit uint64) ([]byte, error) {
	if !validWorkspaceName(name) || ValidateWorkspace(archive, contentLimit) != nil {
		return nil, ErrWorkspaceInvalid
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	dirs := map[string]bool{".": true}
	var total uint64
	entries := 0
	if len(archive) > 0 {
		tr := tar.NewReader(bytes.NewReader(archive))
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, ErrWorkspaceInvalid
			}
			entry := strings.TrimSuffix(hdr.Name, "/")
			if entry == name {
				continue // 같은 이름은 새 내용으로 바꾼다.
			}
			if hdr.Typeflag == tar.TypeDir {
				dirs[entry] = true
			} else {
				total += uint64(hdr.Size)
			}
			entries++
			if err := tw.WriteHeader(hdr); err != nil {
				return nil, ErrWorkspaceInvalid
			}
			if _, err := io.Copy(tw, tr); err != nil {
				return nil, ErrWorkspaceInvalid
			}
		}
	}
	// 부모 디렉터리를 바깥쪽부터 만든다.
	var parents []string
	for dir := path.Dir(name); dir != "."; dir = path.Dir(dir) {
		parents = append([]string{dir}, parents...)
	}
	for _, dir := range parents {
		if !dirs[dir] {
			dirs[dir] = true
			entries++
			if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: dir + "/", Mode: 0o700, Format: tar.FormatPAX}); err != nil {
				return nil, ErrWorkspaceInvalid
			}
		}
	}
	if total += uint64(len(content)); total > contentLimit || entries+1 > maxWorkspaceEntries {
		return nil, ErrWorkspaceTooLarge
	}
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o600, Size: int64(len(content)), Format: tar.FormatPAX}); err != nil {
		return nil, ErrWorkspaceInvalid
	}
	if _, err := tw.Write(content); err != nil {
		return nil, ErrWorkspaceInvalid
	}
	if err := tw.Close(); err != nil {
		return nil, ErrWorkspaceInvalid
	}
	out := buf.Bytes()
	if ValidateWorkspace(out, contentLimit) != nil {
		return nil, ErrWorkspaceInvalid
	}
	return out, nil
}
