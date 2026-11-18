//go:build linux

package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"testing"
)

// 확정 단계 몫 칸은 실행 정책에서만 받는다. 실행 정책이 아닌 검사(설치·평가 정책)는 같은 문서에 칸이 있으면 거부한다.
func TestE3ConfirmationShareOnlyInRunPolicy(t *testing.T) {
	_, _, policy := e2HostPolicyFixture(t)
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := E2HTTPPolicyContentFingerprint(raw, true); err != nil {
		t.Fatal("양성 대조: 칸 없는 정책", err)
	}
	requests, timeout := uint64(0), int64(0)
	policy.Limits.E3ConfirmationRequests, policy.Limits.E3ConfirmationTimeoutMS = &requests, &timeout
	withShare, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := E2HTTPPolicyContentFingerprint(withShare, true); err == nil {
		t.Fatal("실행 정책이 아닌 검사에서 확정 몫 칸 수용")
	}
}

// 확정 몫 상한(요청 1/4·유효 창 1/4)은 실행 정책 문서 검사와 실행 factory가 똑같이 본다. 상한 밖 정책은 두 경로 모두
// 거부하고, 안이면 두 경로의 지문이 같다(#716 이동찬 리뷰).
func TestE3ConfirmationShareCapsInBothPaths(t *testing.T) {
	host, _, policy := e2HostPolicyFixture(t)
	policy.MaxRequests = 400 // 제품 기본 요청 상한. 시험 정책의 작은 상한이면 1/4 몫이 0이 된다.
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	window := policy.ValidUntil.Sub(policy.ValidFrom)
	quarterRequests := policy.MaxRequests / E3ConfirmationShareDivisor
	quarterMS := (window / E3ConfirmationShareDivisor).Milliseconds()
	if quarterRequests == 0 || quarterMS == 0 {
		t.Fatal("시험 정책의 상한이 너무 작음")
	}
	paths := func(requests uint64, timeoutMS int64) (string, error, string, error) {
		t.Helper()
		p := policy
		p.Limits.E3ConfirmationRequests, p.Limits.E3ConfirmationTimeoutMS = &requests, &timeoutMS
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		_, document, docErr := ValidateE2HTTPRunPolicyDocument(raw)
		if err := os.WriteFile(host.PolicyFile, raw, 0600); err != nil {
			t.Fatal(err)
		}
		authority := &standaloneAuthorityFixture{ctx: ctx, digest: sha256.Sum256(raw)}
		loaded, loadErr := LoadE2HTTPRuntimeOptions(ctx, E2HTTPFileOptions{PolicyFile: host.PolicyFile, Authority: authority, Supervisor: standaloneSupervisorFixture{}})
		return document, docErr, loaded.PolicyFingerprintSHA256, loadErr
	}
	for name, c := range map[string]struct {
		requests  uint64
		timeoutMS int64
	}{
		"zero":           {0, 0},
		"exact quarters": {quarterRequests, quarterMS},
	} {
		document, docErr, runtime, loadErr := paths(c.requests, c.timeoutMS)
		if docErr != nil || loadErr != nil || document == "" || document != runtime {
			t.Fatalf("%s: 상한 안 몫 거부 또는 두 경로 지문 다름: %v %v", name, docErr, loadErr)
		}
	}
	for name, c := range map[string]struct {
		requests  uint64
		timeoutMS int64
	}{
		"requests over quarter": {quarterRequests + 1, 1000},
		"time over quarter":     {1, quarterMS + 1},
	} {
		_, docErr, _, loadErr := paths(c.requests, c.timeoutMS)
		if docErr == nil || loadErr == nil {
			t.Fatalf("%s: 상한 밖 몫 수용(문서 %v, 런타임 %v)", name, docErr, loadErr)
		}
	}
}
