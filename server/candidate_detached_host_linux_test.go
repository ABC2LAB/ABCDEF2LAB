//go:build linux

package server

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/Autumn-27/artex/e2proxy"
)

// 계정을 뗀 후속 검증 정책 모양(#1003 P1 교훈: E② 정책 컴파일 · Host 로딩까지 시험)이 E② Host 정책 로딩을 지난다. 제품 코드가
// 그 모양을 만드는지는 cmd/first-controlled-diagnosis 의 TestAccountOnlyTargetPoliciesPassHostPolicyCheck 가 본다(그 패키지는 Host
// 설치 환경을 띄우지 못함). 저장 후보 검증(active/v1): 정확한 GET 하나 · 상한 2(shapeStoredCandidatePolicy), #950 익명:
// candidateLLMReadRule 모양(정확 경로 · 엄격 헤더 · 정확 질의) · 상한 = 행동 수. 둘 다 인증 칸이 없어 PasswordResponse 도 없다.
func TestE2HTTPFactoryLoadsDetachedCandidatePolicies(t *testing.T) {
	for _, shape := range []struct {
		name  string
		rule  E2HTTPFileRule
		limit int
	}{
		{"active/v1", E2HTTPFileRule{Ref: "candidate-exact", PathMatch: "exact", PathScopeRef: "candidate-exact"}, 2},
		{"#950 익명", E2HTTPFileRule{Ref: "candidate-llm-1", PathMatch: "exact", PathScopeRef: "candidate-llm-1", ExactQuery: CandidateActionQuery(nil),
			StrictHeaders: true, ApprovalDisplay: RequestApprovalDisplay{QueryKeys: []string{}, JSONFields: []string{}}}, 1},
	} {
		t.Run(shape.name, func(t *testing.T) {
			cfg, _, policy := e2HostPolicyFixture(t)
			base := policy.Rules[0]
			rule := shape.rule
			rule.Origin, rule.ConnectIP, rule.PathPrefix, rule.Methods = base.Origin, base.ConnectIP, "/read", []string{"GET"}
			policy.Rules = []E2HTTPFileRule{rule}
			policy.Auth, policy.HeaderMode, policy.ApprovalMode, policy.Workflow = nil, e2proxy.HeaderPreserve, e2proxy.ApprovalPreauthorizedScope, nil
			policy.MaxRequests, policy.Limits.MaxAttempts, policy.Limits.MaxCalls = uint64(shape.limit), uint64(shape.limit), shape.limit
			policy.Limits.MaxConcurrent, policy.Runtime.MaxToolCalls, policy.Runtime.MaxIntentRuns = 1, shape.limit, 1
			raw, err := json.Marshal(policy)
			if err != nil || os.WriteFile(cfg.PolicyFile, raw, 0o600) != nil {
				t.Fatal("정책 준비 실패", err)
			}
			_, fingerprint, err := ValidateE2HTTPRunPolicyDocument(raw)
			if err != nil || fingerprint == "" {
				t.Fatalf("계정 뗀 실행 정책 거부: %v", err)
			}
			options, err := LoadE2HTTPRuntimeOptions(t.Context(), E2HTTPFileOptions{PolicyFile: cfg.PolicyFile,
				Authority: &e2HTTPPolicyAuthorityFixture{ctx: t.Context()}, Supervisor: &e2HTTPSupervisorFixture{}})
			if err != nil || options.PolicyFingerprintSHA256 != fingerprint {
				t.Fatalf("계정 뗀 정책 Host 로딩 실패: %v", err)
			}
		})
	}
}
