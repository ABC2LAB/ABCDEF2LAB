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
	if got, err := readPolicySource(runs, liveJob); err != nil || !got.Present || got.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("동결본 digest: %+v %v", got, err)
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
