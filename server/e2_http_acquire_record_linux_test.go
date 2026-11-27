//go:build linux

package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func acquireRecordOptions(t *testing.T) (context.Context, E2HTTPRuntimeOptions, E2HTTPFilePolicy) {
	t.Helper()
	cfg, _, policy := e2HostPolicyFixture(t)
	raw, err := json.Marshal(policy)
	if err != nil || os.WriteFile(cfg.PolicyFile, raw, 0600) != nil {
		t.Fatal("합성 정책 준비 실패")
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	options, err := LoadE2HTTPRuntimeOptions(ctx, E2HTTPFileOptions{PolicyFile: cfg.PolicyFile, Authority: &e2HTTPPolicyAuthorityFixture{ctx: ctx}, Supervisor: &e2HTTPSupervisorFixture{}})
	if err != nil {
		t.Fatal("호스트 준비 실패", err)
	}
	return ctx, options, policy
}

// 실행은 E②를 잡을 때 취득 기록(E2AcquireRecordFile)을 상태 디렉터리에 남긴다(계약 workbench-live-data §4). 작업대 e2-flow가
// 이 기록으로 '처음부터 E②를 잡지 않은 실행'과 '저널이 없어진 실행'을 가른다. E②를 잡기 전에는 기록도 저널도 없다.
func TestE2HTTPFactoryRecordsAcquireBesideJournal(t *testing.T) {
	ctx, options, policy := acquireRecordOptions(t)
	record := filepath.Join(policy.StateDir, E2AcquireRecordFile)
	journal := filepath.Join(policy.StateDir, "events.jsonl")
	for _, path := range []string{record, journal} {
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("E②를 잡기 전에 %s가 있음: %v", filepath.Base(path), err)
		}
	}
	lease, err := options.Acquire(ctx, E2HTTPRequest{TaskID: "11", TaskIDNumber: 11, ExplorationID: 27, IntentID: 31})
	if err != nil {
		t.Fatal("실제 실행 획득 실패", err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = lease.Runner.Close(cleanup)
		_ = lease.Execution.Stop(cleanup)
		_ = lease.Release(cleanup)
	})
	info, err := os.Lstat(record)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("취득 기록 파일: %v %v", info, err)
	}
	raw, err := os.ReadFile(record)
	var got struct {
		Version string `json:"version"`
		Scope   struct {
			TaskRef string
			RunRef  string
		} `json:"scope"`
		PolicyRevision string `json:"policy_revision"`
	}
	if err != nil || json.Unmarshal(raw, &got) != nil || got.Version != e2AcquireRecordVersion || got.Scope.TaskRef != "11" || got.Scope.RunRef != policy.RunRef || got.PolicyRevision != policy.PolicyRevision {
		t.Fatalf("취득 기록 내용: %s %v", raw, err)
	}
	if _, err := os.Lstat(journal); err != nil {
		t.Fatal("E②를 잡았는데 저널이 없음", err)
	}
}

// 취득 기록은 저널보다 먼저 남기고, 남기지 못하면 E②를 잡지 않는다(2026-11-27 결정). 기록을 쓸 수 없게 해 두면(같은 이름이
// 이미 있어 배타 생성 실패) 획득이 실패하고 저널·실행 조건 기록이 생기지 않는다 — 기록이 저널 뒤였다면 저널이 남았을 것이다.
func TestE2HTTPFactoryDoesNotAcquireWithoutAcquireRecord(t *testing.T) {
	ctx, options, policy := acquireRecordOptions(t)
	record := filepath.Join(policy.StateDir, E2AcquireRecordFile)
	if err := os.WriteFile(record, []byte("occupied\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lease, err := options.Acquire(ctx, E2HTTPRequest{TaskID: "11", TaskIDNumber: 11, ExplorationID: 27, IntentID: 31})
	if !errors.Is(err, ErrE2HTTPHost) || lease.Execution != nil || lease.Runner != nil || lease.Release != nil {
		t.Fatalf("취득 기록을 남기지 못했는데 E②를 잡음: %v", err)
	}
	for _, name := range []string{"events.jsonl", "runtime-conditions.json", "calls"} {
		if _, err := os.Lstat(filepath.Join(policy.StateDir, name)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("취득 기록 없이 %s가 생김: %v", name, err)
		}
	}
	if raw, err := os.ReadFile(record); err != nil || string(raw) != "occupied\n" {
		t.Fatalf("이미 있던 파일을 덮어씀: %q %v", raw, err)
	}
}
