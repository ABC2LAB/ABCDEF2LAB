//go:build linux

package llmpolicy

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func manifestFileFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "pins.json")
	if err := os.WriteFile(path, []byte(manifestValid), 0600); err != nil {
		t.Fatal(err)
	}
	return dir, path
}

func requireManifestFileRejected(t *testing.T, path string) {
	t.Helper()
	resolver, err := LoadProfileManifestFile(path)
	if resolver != nil || err != ErrInvalidProfileManifestFile || !errors.Is(err, ErrInvalidProfileManifestFile) {
		t.Fatalf("expected fixed file error and nil resolver, got %v %v", resolver, err)
	}
	for _, forbidden := range []string{path, "local:test", "model.internal"} {
		if strings.Contains(fmt.Sprintf("%+v", err), forbidden) {
			t.Fatal("file error disclosed path or policy input")
		}
	}
}

func TestLoadProfileManifestFile(t *testing.T) {
	_, path := manifestFileFixture(t)
	resolver, err := LoadProfileManifestFile(path)
	if err != nil || resolver == nil {
		t.Fatalf("load failed: %v", err)
	}
	target := ProfileTarget{ID: 7, Format: "openai", Model: "local:test", BaseURL: "http://model.internal:11434/v1"}
	policy, err := resolver.Resolve(target)
	if err != nil {
		t.Fatal(err)
	}
	ip := netip.MustParseAddr("10.0.0.1")
	if !policy.Decide(Input{URL: target.BaseURL + "/chat/completions", Transport: Direct, ResolvedIPs: []netip.Addr{ip}, ConnectIP: ip}).Allowed() {
		t.Fatal("loaded binding denied the positive control")
	}
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(target); err != nil {
		t.Fatal("loaded resolver changed after source file mutation")
	}
	requireManifestFileRejected(t, path)
}

func TestLoadProfileManifestFileRejectsUntrustedPaths(t *testing.T) {
	dir, path := manifestFileFixture(t)
	for name, candidate := range map[string]string{
		"relative": path[1:], "unclean": dir + "/./pins.json", "parent": dir + "/../" + filepath.Base(dir) + "/pins.json",
		"trailing slash": path + "/", "root parent": "/pins.json", "missing": filepath.Join(dir, "missing.json"),
	} {
		t.Run(name, func(t *testing.T) { requireManifestFileRejected(t, candidate) })
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	requireManifestFileRejected(t, link)
	linkedDir := filepath.Join(filepath.Dir(dir), filepath.Base(dir)+"-link")
	if err := os.Symlink(dir, linkedDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(linkedDir) })
	requireManifestFileRejected(t, filepath.Join(linkedDir, "pins.json"))
}

func TestLoadProfileManifestFileRejectsUnsafeMetadata(t *testing.T) {
	t.Run("directory mode", func(t *testing.T) {
		dir, path := manifestFileFixture(t)
		if err := os.Chmod(dir, 0750); err != nil {
			t.Fatal(err)
		}
		requireManifestFileRejected(t, path)
	})
	t.Run("file mode", func(t *testing.T) {
		_, path := manifestFileFixture(t)
		if err := os.Chmod(path, 0640); err != nil {
			t.Fatal(err)
		}
		requireManifestFileRejected(t, path)
	})
	t.Run("hard link", func(t *testing.T) {
		dir, path := manifestFileFixture(t)
		if err := os.Link(path, filepath.Join(dir, "other.json")); err != nil {
			t.Fatal(err)
		}
		requireManifestFileRejected(t, path)
	})
	t.Run("nonregular", func(t *testing.T) {
		_, path := manifestFileFixture(t)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := unix.Mkfifo(path, 0600); err != nil {
			t.Fatal(err)
		}
		requireManifestFileRejected(t, path)
	})
}

func TestLoadProfileManifestFileBoundsAndParser(t *testing.T) {
	_, path := manifestFileFixture(t)
	valid := append([]byte(manifestValid), bytes.Repeat([]byte(" "), MaxProfileManifestBytes-len(manifestValid))...)
	if err := os.WriteFile(path, valid, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfileManifestFile(path); err != nil {
		t.Fatalf("max size rejected: %v", err)
	}
	if err := os.WriteFile(path, append(valid, ' '), 0600); err != nil {
		t.Fatal(err)
	}
	requireManifestFileRejected(t, path)
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	requireManifestFileRejected(t, path)
}
