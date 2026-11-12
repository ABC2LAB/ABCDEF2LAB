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
	// 항목 이름 바이트 합계 상한. 긴 이름에 붙는 PAX 확장 머리말의 경로 기록 크기를 내용 상한 밖에서 묶는다.
	maxWorkspaceNameBytes = 1 << 20
	// tar 항목 하나에서 이름 길이를 뺀 머리말·채움 바이트 상한: ustar 머리말 512 + PAX 확장 머리말 512 + PAX 기록의
	// 고정 글자·채움(512 미만) + 내용 채움(512 미만). 넉넉히 5블록으로 둔다. 이름 길이는 maxWorkspaceNameBytes가 묶는다.
	workspaceEntryOverhead = 5 * 512
	// 끝 블록(0으로 찬 512바이트 2개).
	workspaceTrailer = 1024
)

// workspaceArchiveLimit은 내용 상한이 contentLimit인 작업 공간 tar의 바이트 상한이다. 묶는 쪽과 받는 쪽이 같은
// 값을 쓴다(packWorkspace는 결과가 이 값을 넘으면 묶지 않는다).
func workspaceArchiveLimit(contentLimit uint64) int64 {
	return int64(contentLimit) + maxWorkspaceEntries*workspaceEntryOverhead + maxWorkspaceNameBytes + workspaceTrailer
}

// packWorkspace는 root 아래의 일반 파일·디렉터리를 tar로 묶는다. 내용 합계가 contentLimit를 넘거나 항목이 너무
// 많으면 ErrWorkspaceTooLarge다. 묶는 동안 root를 바꾸는 프로세스가 없어야 한다(호출자가 보장).
func packWorkspace(root string, contentLimit uint64) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	var total uint64
	entries, nameBytes := 0, 0
	// 항목 하나를 셀 때 수·이름 바이트 상한을 같이 본다(디렉터리 이름은 끝의 "/"까지 센다).
	count := func(name string) bool {
		entries++
		nameBytes += len(name)
		return entries <= maxWorkspaceEntries && nameBytes <= maxWorkspaceNameBytes
	}
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
			if !count(name + "/") {
				return ErrWorkspaceTooLarge
			}
			return tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: 0o700, Format: tar.FormatPAX})
		case info.Mode().IsRegular():
			if st, ok := info.Sys().(*syscall.Stat_t); !ok || st.Nlink != 1 {
				return nil // 하드 링크는 버린다.
			}
			if !count(name) {
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
	// 받는 쪽 상한과 같은 기준이다. 위 상한 안이면 넘지 않지만, 넘으면 받을 수 없는 tar를 내보내지 않는다.
	if int64(buf.Len()) > workspaceArchiveLimit(contentLimit) {
		return nil, ErrWorkspaceTooLarge
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
	entries, nameBytes := 0, 0
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
		entries++
		nameBytes += len(hdr.Name)
		if entries > maxWorkspaceEntries || nameBytes > maxWorkspaceNameBytes {
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
