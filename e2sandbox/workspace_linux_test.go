package e2sandbox

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func workspaceTar(t *testing.T, entries ...tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range entries {
		h := h
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg && h.Size > 0 {
			if _, err := tw.Write(bytes.Repeat([]byte("x"), int(h.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// 일반 파일·디렉터리는 내용 그대로 오가고, 링크·FIFO·하드 링크는 묶이지 않는다. 권한은 0600/0700이다.
func TestWorkspaceRoundTripKeepsOnlyRegularFilesAndDirectories(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "d", "e"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"a.txt": "alpha", "d/b.txt": "beta", "d/e/c.bin": "\x00\x01"} {
		if err := os.WriteFile(filepath.Join(src, filepath.FromSlash(name)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(src, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "shared"), []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(src, "shared"), filepath.Join(src, "shared2")); err != nil {
		t.Fatal(err)
	}
	data, err := packWorkspace(src, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateWorkspace(data, 1<<20); err != nil {
		t.Fatal("packed workspace failed validation", err)
	}
	dst := t.TempDir()
	if err := unpackWorkspace(data, dst, 1<<20); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"a.txt": "alpha", "d/b.txt": "beta", "d/e/c.bin": "\x00\x01"} {
		got, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(name)))
		if err != nil || string(got) != body {
			t.Fatalf("%s: %q %v", name, got, err)
		}
		info, _ := os.Stat(filepath.Join(dst, filepath.FromSlash(name)))
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v", name, info.Mode())
		}
	}
	if info, err := os.Stat(filepath.Join(dst, "d")); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v %v", info, err)
	}
	for _, gone := range []string{"link", "fifo", "shared", "shared2"} {
		if _, err := os.Lstat(filepath.Join(dst, gone)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s carried over: %v", gone, err)
		}
	}
}

// 내용 합계·항목 수가 상한을 넘으면 묶지 않는다.
func TestPackWorkspaceLimits(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "big"), bytes.Repeat([]byte("x"), 2048), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := packWorkspace(src, 2047); !errors.Is(err, ErrWorkspaceTooLarge) {
		t.Fatal("content over the limit packed", err)
	}
	if _, err := packWorkspace(src, 2048); err != nil {
		t.Fatal("content at the limit rejected", err)
	}
	many := t.TempDir()
	for i := 0; i <= maxWorkspaceEntries; i++ {
		if err := os.WriteFile(filepath.Join(many, "f"+strings.Repeat("0", 0)+itoa(i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := packWorkspace(many, 1<<20); !errors.Is(err, ErrWorkspaceTooLarge) {
		t.Fatal("too many entries packed", err)
	}
}

// 긴 이름이 많아 PAX 확장 머리말이 붙어도 묶은 tar는 같은 상한으로 다시 풀린다(#443 이동찬 재현: 최소 ScratchBytes
// 1MiB에 이름 184바이트 빈 파일 3600개).
func TestWorkspaceLongNamesRoundTripAtMinimumScratch(t *testing.T) {
	const scratch = 1 << 20
	src := t.TempDir()
	prefix := strings.Repeat("n", 176)
	for i := 0; i < 3600; i++ {
		name := prefix + itoa(10000000+i) // 184바이트
		if err := os.WriteFile(filepath.Join(src, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	data, err := packWorkspace(src, scratch)
	if err != nil {
		t.Fatal("long names not packed", err)
	}
	if int64(len(data)) > workspaceArchiveLimit(scratch) {
		t.Fatalf("archive %d over the receive limit %d", len(data), workspaceArchiveLimit(scratch))
	}
	dst := t.TempDir()
	if err := unpackWorkspace(data, dst, scratch); err != nil {
		t.Fatal("packed archive rejected on unpack", err)
	}
	got, err := os.ReadDir(dst)
	if err != nil || len(got) != 3600 {
		t.Fatalf("restored %d entries: %v", len(got), err)
	}
}

// 이름 바이트 합계가 상한을 넘으면 묶지도 풀지도 않는다. 항목 수 상한보다 먼저 걸리도록 깊은 디렉터리 안의 긴
// 이름으로 만든다.
func TestWorkspaceNameBytesLimit(t *testing.T) {
	part := strings.Repeat("d", 250)
	var headers []tar.Header
	dir, names := "", 0
	for depth := 0; depth < 15; depth++ {
		if dir != "" {
			dir += "/"
		}
		dir += part
		headers = append(headers, tar.Header{Typeflag: tar.TypeDir, Name: dir + "/", Mode: 0o700})
		names += len(dir) + 1
	}
	for i := 0; names <= maxWorkspaceNameBytes; i++ {
		name := dir + "/f" + itoa(1000+i)
		headers = append(headers, tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o600})
		names += len(name)
	}
	if len(headers) >= maxWorkspaceEntries {
		t.Fatalf("fixture hits the entry limit first: %d", len(headers))
	}
	over := workspaceTar(t, headers...)
	if err := unpackWorkspace(over, t.TempDir(), 1<<20); !errors.Is(err, ErrWorkspaceTooLarge) {
		t.Fatal("name bytes over the limit unpacked", err)
	}
	if err := ValidateWorkspace(over, 1<<20); !errors.Is(err, ErrWorkspaceTooLarge) {
		t.Fatal("name bytes over the limit validated", err)
	}
	src := t.TempDir()
	for _, h := range headers {
		target := filepath.Join(src, filepath.FromSlash(strings.TrimSuffix(h.Name, "/")))
		var err error
		if h.Typeflag == tar.TypeDir {
			err = os.Mkdir(target, 0o700)
		} else {
			err = os.WriteFile(target, nil, 0o600)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := packWorkspace(src, 1<<20); !errors.Is(err, ErrWorkspaceTooLarge) {
		t.Fatal("name bytes over the limit packed", err)
	}
	// 마지막 파일 하나를 빼면 상한 안이라 묶이고 다시 풀린다.
	if err := os.Remove(filepath.Join(src, filepath.FromSlash(headers[len(headers)-1].Name))); err != nil {
		t.Fatal(err)
	}
	data, err := packWorkspace(src, 1<<20)
	if err != nil {
		t.Fatal("name bytes at the limit rejected", err)
	}
	if err := unpackWorkspace(data, t.TempDir(), 1<<20); err != nil {
		t.Fatal("name bytes at the limit not unpacked", err)
	}
	if err := ValidateWorkspace(data, 1<<20); err != nil {
		t.Fatal("name bytes at the limit not validated", err)
	}
}

// 풀 때는 일반 파일·디렉터리만, 상대 정규 경로만, 중복 없이, 상한 안에서만 받는다.
func TestUnpackWorkspaceRejectsUnsafeArchives(t *testing.T) {
	reg := func(name string, size int64) tar.Header {
		return tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o600, Size: size}
	}
	dir := func(name string) tar.Header { return tar.Header{Typeflag: tar.TypeDir, Name: name, Mode: 0o700} }
	for name, data := range map[string][]byte{
		"parent":        workspaceTar(t, reg("../x", 1)),
		"inner-parent":  workspaceTar(t, dir("a/"), reg("a/../../x", 1)),
		"absolute":      workspaceTar(t, reg("/etc/x", 1)),
		"dot":           workspaceTar(t, reg("./x", 1)),
		"double-slash":  workspaceTar(t, dir("a/"), reg("a//x", 1)),
		"symlink":       workspaceTar(t, tar.Header{Typeflag: tar.TypeSymlink, Name: "l", Linkname: "/etc/passwd"}),
		"hardlink":      workspaceTar(t, reg("a", 1), tar.Header{Typeflag: tar.TypeLink, Name: "b", Linkname: "a"}),
		"fifo":          workspaceTar(t, tar.Header{Typeflag: tar.TypeFifo, Name: "f"}),
		"duplicate":     workspaceTar(t, reg("a", 1), reg("a", 1)),
		"missing-dir":   workspaceTar(t, reg("nodir/a", 1)),
		"over-content":  workspaceTar(t, reg("a", 600), reg("b", 600)),
		"not-a-tar":     []byte("plain text, not an archive"),
		"truncated-tar": workspaceTar(t, reg("a", 512))[:600],
	} {
		dst := t.TempDir()
		if err := unpackWorkspace(data, dst, 1024); err == nil {
			t.Fatalf("%s: unsafe archive accepted", name)
		}
		// 호스트 검사도 같은 규칙으로 거부한다(파일 시스템에 쓰지 않고).
		if err := ValidateWorkspace(data, 1024); err == nil {
			t.Fatalf("%s: unsafe archive validated", name)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(dst), "x")); err == nil {
			t.Fatalf("%s: wrote outside the root", name)
		}
	}
	// Go tar 작성기는 NUL 이름을 만들지 않으므로 이름 검사를 직접 본다.
	for _, bad := range []string{"a\x00b", "", ".", "..", "a/", "\xff"} {
		if validWorkspaceName(bad) {
			t.Fatalf("%q accepted", bad)
		}
	}
	if err := unpackWorkspace(nil, t.TempDir(), 1024); err != nil {
		t.Fatal("empty workspace rejected", err)
	}
	if err := unpackWorkspace(bytes.Repeat([]byte{0}, int(workspaceArchiveLimit(1024))+1), t.TempDir(), 1024); !errors.Is(err, ErrWorkspaceTooLarge) {
		t.Fatal("oversized archive accepted", err)
	}
}
