//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Autumn-27/artex/budgetlocal"
)

func TestSubmitFailureEmitsNoApprovalMetadata(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "config.json")
	request := filepath.Join(dir, "request.json")
	body, err := json.Marshal(budgetlocal.ClientConfig{SocketPath: filepath.Join(dir, "missing.sock"), ServerUID: uint32(os.Geteuid() + 1)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, body, 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	body, err = json.Marshal(budgetlocal.Request{RequestRef: "request.1", TaskRef: "task.1", PolicyRef: "policy.1", ResetRef: "reset.1", MaxRequests: 1, ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(request, body, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = run(context.Background(), []string{"--config", config, "--request", request}, &output)
	if !errors.Is(err, budgetlocal.ErrUnknown) || output.Len() != 0 {
		t.Fatalf("Submit failure: err=%v stdout_bytes=%d", err, output.Len())
	}
	code, message := resultDiagnostic(err)
	if code != 3 || !strings.Contains(message, "same approval_request_ref") {
		t.Fatalf("unknown diagnostic: %d %q", code, message)
	}
}

func TestApprovalOutcomeDiagnosticsAreFixed(t *testing.T) {
	for _, tc := range []struct {
		err     error
		code    int
		message string
	}{
		{nil, 0, ""}, {budgetlocal.ErrInvalid, 1, "budget approval rejected"}, {budgetlocal.ErrDenied, 1, "budget approval rejected"},
		{budgetlocal.ErrUnknown, 3, "budget approval outcome unknown; retry the same approval_request_ref"},
		{errors.New("PRIVATE_DATABASE_DETAIL"), 3, "budget approval outcome unknown; retry the same approval_request_ref"},
	} {
		if code, message := resultDiagnostic(tc.err); code != tc.code || message != tc.message {
			t.Fatalf("diagnostic=%d/%q want=%d/%q", code, message, tc.code, tc.message)
		}
	}
}

type requestDirectoryInfo struct {
	uid  uint32
	mode os.FileMode
}

func (requestDirectoryInfo) Name() string        { return "directory" }
func (requestDirectoryInfo) Size() int64         { return 0 }
func (s requestDirectoryInfo) Mode() os.FileMode { return os.ModeDir | s.mode }
func (requestDirectoryInfo) ModTime() time.Time  { return time.Time{} }
func (requestDirectoryInfo) IsDir() bool         { return true }
func (s requestDirectoryInfo) Sys() any          { return &syscall.Stat_t{Uid: s.uid} }

func TestRequestAncestorTrustIndependentOfDAC(t *testing.T) {
	for _, tc := range []struct {
		name string
		uid  uint32
		mode os.FileMode
		want bool
	}{
		{"trusted_root", 0, 0o755, true}, {"root_nonsticky_writable", 0, 0o777, false}, {"third_uid", 2000, 0o755, false}, {"root_sticky", 0, os.ModeSticky | 0o777, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lstat := func(path string) (os.FileInfo, error) {
				switch path {
				case "/safe/requests":
					return requestDirectoryInfo{1000, 0o700}, nil
				case "/safe":
					return requestDirectoryInfo{tc.uid, tc.mode}, nil
				case "/":
					return requestDirectoryInfo{0, 0o755}, nil
				default:
					return nil, os.ErrNotExist
				}
			}
			if got := trustedPrivateRequestDirectoryWithLstat("/safe/requests", 1000, lstat); got != tc.want {
				t.Fatalf("ancestor trust=%t want=%t", got, tc.want)
			}
		})
	}
}
