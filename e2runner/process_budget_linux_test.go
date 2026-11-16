//go:build linux

package e2runner

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

type unreadableProcessFS struct{}

func (unreadableProcessFS) Open(string) (fs.File, error) { return nil, fs.ErrPermission }

func TestRunnerChecksVisibleCgroupBudgetBeforeAdmission(t *testing.T) {
	base := runnerTestDirectory(t)
	helper := filepath.Join(base, "helper")
	if os.WriteFile(helper, []byte("#!/bin/sh\nexit 1\n"), 0700) != nil {
		t.Fatal("fixture executable unavailable")
	}
	// The executable is never launched. The rest is the real protected config,
	// so a missing constructor check would wrongly accept the small budget.
	f := newRunnerFixture(t, base, helper, "http://127.0.0.1", nil)
	for _, tc := range []struct {
		value string
		ok    bool
	}{{"128\n", false}, {"255\n", false}, {"256\n", true}, {"512\n", true}, {"max\n", true}, {"", false}, {"-1", false}, {"garbled", false}, {"18446744073709551616", false}} {
		t.Run(tc.value, func(t *testing.T) {
			r, err := newWithProcessFS(f.cfg, fstest.MapFS{"pids.max": &fstest.MapFile{Data: []byte(tc.value)}})
			if (err == nil) != tc.ok {
				t.Fatalf("실제 PID 한도와 호출 예산 불일치를 수용: %v", err)
			}
			if r != nil {
				_ = r.Close(t.Context())
			}
		})
	}
	if _, err := newWithProcessFS(f.cfg, unreadableProcessFS{}); !errors.Is(err, ErrConfig) {
		t.Fatal("읽을 수 없는 cgroup 한도를 수용")
	}
	if _, err := newWithProcessFS(f.cfg, fstest.MapFS{}); err != nil {
		t.Fatal("cgroup v2 루트 파일이 없는 기존 호스트 경로를 거부", err)
	}
}

func TestMountedProcessBudgetOwnedDocker(t *testing.T) {
	expected := os.Getenv("ARTEX_PIDS_BUDGET_TEST_EXPECT")
	if expected == "" {
		t.Skip("소유 Docker PID128/256 입출력 수용 전용")
	}
	if expected != "accept" && expected != "reject" {
		t.Fatal("invalid owned fixture expectation")
	}
	err := checkProcessBudget(os.DirFS("/sys/fs/cgroup"), 256)
	if (err == nil) != (expected == "accept") {
		t.Fatal("실제 마운트된 pids.max 경계 검사 불일치", err)
	}
}
