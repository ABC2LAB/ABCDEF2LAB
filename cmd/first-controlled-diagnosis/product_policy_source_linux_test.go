//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e3contract"
)

// policySourceBase는 기본 준비 서비스의 상태 조회만 흉내 낸다(회차 소유 확인).
type policySourceBase struct {
	job   *diagnosisprep.Job
	error string
	err   error
	calls []diagnosisprep.Request
}

func (b *policySourceBase) Call(_ context.Context, r diagnosisprep.Request) (diagnosisprep.Response, error) {
	b.calls = append(b.calls, r)
	if b.err != nil {
		return diagnosisprep.Response{}, b.err
	}
	return diagnosisprep.Response{Version: r.Version, RequestID: r.RequestID, Job: b.job, Error: b.error}, nil
}

func policySourceRuns(t *testing.T) string {
	t.Helper()
	runs := t.TempDir()
	if os.Chmod(runs, 0o700) != nil || os.Mkdir(filepath.Join(runs, liveJob), 0o700) != nil {
		t.Fatal("회차 디렉터리")
	}
	return runs
}

// 동결본 바이트의 SHA-256만 돌려주고, 동결본이 없으면 present=false다. 읽기 규칙(권한·링크)은 확정 단계 읽기와 같다.
func TestReadPolicySource(t *testing.T) {
	runs := policySourceRuns(t)
	if got, err := readPolicySource(runs, liveJob); err != nil || got.Present || got.SHA256 != "" {
		t.Fatalf("동결본 없음: %+v %v", got, err)
	}
	if got, err := readPolicySource(runs, "job-without-dir"); err != nil || got.Present {
		t.Fatalf("회차 디렉터리 없음: %+v %v", got, err)
	}
	raw := []byte(`{"frozen":"declaration"}`)
	path := filepath.Join(runs, liveJob, productFrozenDeclarationFile)
	if os.WriteFile(path, raw, 0o600) != nil {
		t.Fatal("동결본 쓰기")
	}
	sum := sha256.Sum256(raw)
	if got, err := readPolicySource(runs, liveJob); err != nil || !got.Present || got.SHA256 != hex.EncodeToString(sum[:]) ||
		got.OwnerAccountLabel != "" || got.CrossAccountLabel != "" {
		t.Fatalf("동결본 digest(선언 모양 아님 → 라벨 없음): %+v %v", got, err)
	}
	if os.Chmod(path, 0o644) != nil {
		t.Fatal("권한 바꾸기")
	}
	if _, err := readPolicySource(runs, liveJob); err == nil {
		t.Fatal("다른 사용자가 읽을 수 있는 동결본을 받음")
	}
	if os.Remove(path) != nil || os.Symlink(filepath.Join(runs, "elsewhere"), path) != nil {
		t.Fatal("링크 만들기")
	}
	if _, err := readPolicySource(runs, liveJob); err == nil {
		t.Fatal("링크 동결본을 받음")
	}
	for _, job := range []string{"", ".", "..", "a/b", "../" + liveJob} {
		if _, err := readPolicySource(runs, job); err == nil {
			t.Fatalf("회차 참조 %q를 받음", job)
		}
	}
	if _, err := readPolicySource("", liveJob); err == nil {
		t.Fatal("회차 디렉터리 설정 없이 받음")
	}
}

// IPC policy_source: 회차 소유는 기본 준비 서비스의 상태 조회(Principal·Job)로 확인하고, 응답은 계약 검사를 통과한다.
func TestProductPolicySourceEndpoint(t *testing.T) {
	runs := policySourceRuns(t)
	raw := []byte(`{"frozen":"declaration"}`)
	if os.WriteFile(filepath.Join(runs, liveJob, productFrozenDeclarationFile), raw, 0o600) != nil {
		t.Fatal("동결본 쓰기")
	}
	sum := sha256.Sum256(raw)
	request := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "verdict-policy-source", Principal: "ARTEX",
		Command: diagnosisprep.ProductControl, Product: &diagnosisprep.ProductRequest{Operation: "policy_source", JobID: liveJob}}
	for name, tc := range map[string]struct {
		base   *policySourceBase
		runs   string
		reason string
		want   *diagnosisprep.ProductPolicySource
	}{
		"owned":     {base: &policySourceBase{job: &diagnosisprep.Job{ID: liveJob}}, runs: runs, want: &diagnosisprep.ProductPolicySource{Present: true, SHA256: hex.EncodeToString(sum[:])}},
		"not_owned": {base: &policySourceBase{error: "not_found"}, runs: runs, reason: "not_found"},
		"forbidden": {base: &policySourceBase{error: "forbidden"}, runs: runs, reason: "not_found"},
		// 오류와 함께 회차가 실려 와도 오류가 이긴다.
		"error_with_job": {base: &policySourceBase{job: &diagnosisprep.Job{ID: liveJob}, error: "forbidden"}, runs: runs, reason: "not_found"},
		"other_job":      {base: &policySourceBase{job: &diagnosisprep.Job{ID: "job-other"}}, runs: runs, reason: "not_found"},
		"no_job":         {base: &policySourceBase{}, runs: runs, reason: "not_found"},
		"base_down":      {base: &policySourceBase{err: errors.New("closed")}, runs: runs, reason: "policy_source_unavailable"},
		"runs_missing":   {base: &policySourceBase{job: &diagnosisprep.Job{ID: liveJob}}, reason: "policy_source_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			endpoint := productPreparationEndpoint{base: tc.base, product: &productRuntime{config: urlPreparationInstallation{RunsDir: tc.runs}}}
			out, err := endpoint.Call(t.Context(), request)
			if err != nil || diagnosisprep.ValidateResponse(request, out) != nil || out.Error != "" || out.Product == nil {
				t.Fatalf("응답: %+v %v", out, err)
			}
			if out.Product.Reason != tc.reason || (tc.want == nil) != (out.Product.PolicySource == nil) ||
				tc.want != nil && *out.Product.PolicySource != *tc.want {
				t.Fatalf("policy_source: reason=%q source=%+v", out.Product.Reason, out.Product.PolicySource)
			}
			if len(tc.base.calls) != 1 || tc.base.calls[0].Command != diagnosisprep.Status || tc.base.calls[0].Principal != "ARTEX" || tc.base.calls[0].JobID != liveJob {
				t.Fatalf("회차 소유 확인: %+v", tc.base.calls)
			}
		})
	}
}

// 계정 라벨은 그 회차의 동결본에서만 읽는다. 대상의 현재 선언을 다른 라벨로 바꿔도 결과가 같다(현재 등록으로 대신하지 않음).
func TestReadPolicySourceLabelsFromFrozenOnly(t *testing.T) {
	f := newE3HookFixture(t, true, nil)
	before, err := readPolicySource(f.runs, liveJob)
	if err != nil || !before.Present || before.OwnerAccountLabel != "tester-a" || before.CrossAccountLabel != "tester-b" {
		t.Fatalf("동결본 라벨: %+v %v", before, err)
	}
	in := declarationTestInput()
	in.OwnerAccountLabel, in.CrossAccountLabel = "tester-c", "tester-d"
	current, err := f.r.lookup("ARTEX", f.target.Origin+"/app", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.r.putDeclaration("ARTEX", current.Target.ID, current.Target.Revision, in); err != nil {
		t.Fatal(err)
	}
	after, err := readPolicySource(f.runs, liveJob)
	if err != nil || *after != *before {
		t.Fatalf("현재 선언을 바꾸자 결과가 바뀜: %+v → %+v %v", before, after, err)
	}
}

// 확정 단계 결과 파일은 상태·사유만 읽는다. 없으면 present=false, 있는데 읽지 못하거나 계약 밖이면 unreadable이다(없음과 섞지
// 않는다).
func TestReadConfirmationState(t *testing.T) {
	runs := policySourceRuns(t)
	path := filepath.Join(runs, liveJob, productE3ConfirmationFile)
	if got := readConfirmationState(runs, liveJob); *got != (diagnosisprep.ProductConfirmationState{}) {
		t.Fatalf("파일 없음: %+v", got)
	}
	write := func(raw string, mode os.FileMode) {
		t.Helper()
		_ = os.Remove(path)
		if os.WriteFile(path, []byte(raw), mode) != nil || os.Chmod(path, mode) != nil {
			t.Fatal("결과 파일 쓰기")
		}
	}
	version := e3contract.RunResultVersion
	unreadable := diagnosisprep.ProductConfirmationState{Present: true, Status: diagnosisprep.ConfirmationUnreadable}
	for name, tc := range map[string]struct {
		raw  string
		mode os.FileMode
		want diagnosisprep.ProductConfirmationState
	}{
		"disabled":       {`{"version":"` + version + `","status":"disabled","reason":"declaration_missing","verdicts":[]}`, 0o600, diagnosisprep.ProductConfirmationState{Present: true, Status: "disabled", Reason: "declaration_missing"}},
		"completed":      {`{"version":"` + version + `","status":"completed","verdicts":[{"resource_type":"note"}]}`, 0o600, diagnosisprep.ProductConfirmationState{Present: true, Status: "completed"}},
		"broken_json":    {`{"version":`, 0o600, unreadable},
		"wrong_version":  {`{"version":"other/v9","status":"completed"}`, 0o600, unreadable},
		"unknown_status": {`{"version":"` + version + `","status":"maybe"}`, 0o600, unreadable},
		"status_literal": {`{"version":"` + version + `","status":"unreadable"}`, 0o600, unreadable},
		"bad_reason":     {`{"version":"` + version + `","status":"disabled","reason":"Not Wired"}`, 0o600, unreadable},
		"world_readable": {`{"version":"` + version + `","status":"completed"}`, 0o644, unreadable},
	} {
		t.Run(name, func(t *testing.T) {
			write(tc.raw, tc.mode)
			if got := readConfirmationState(runs, liveJob); *got != tc.want {
				t.Fatalf("%+v, want %+v", got, tc.want)
			}
		})
	}
	_ = os.Remove(path)
	if os.Symlink(filepath.Join(runs, "elsewhere"), path) != nil {
		t.Fatal("링크 만들기")
	}
	if got := readConfirmationState(runs, liveJob); *got != unreadable {
		t.Fatalf("링크 결과 파일: %+v", got)
	}
}
