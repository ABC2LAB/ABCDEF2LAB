//go:build !linux

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestUnsupportedPlatformDoesNotCreateKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.key")
	var out, diagnostic bytes.Buffer
	code := run(context.Background(), []string{"--key-file", path}, &out, &diagnostic)
	if code != 1 || out.Len() != 0 || diagnostic.String() != unsupportedPlatform+"\n" {
		t.Fatal("unsupported platform accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("unsupported platform created key")
	}
	file, dir, result := openPrivateKey(path)
	if file != nil || dir != nil || result != unsupportedPlatform {
		t.Fatal("unsupported storage accepted")
	}
}
