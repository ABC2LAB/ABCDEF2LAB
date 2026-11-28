//go:build linux

package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/server"
)

// requireHostPolicyAccepted는 발급한 실행 정책이 Host가 정책 내용에 하는 검사를 지나는지 본다: ValidateE2HTTPRunPolicyDocument
// (칸 모양 · 인증 칸 validE2HTTPFileAuth · 로그인 시도 상한 · 지문 계산 안의 E② 정책 컴파일 e2proxy.ValidatePolicyTemplate).
// Host 정책 로딩(server.LoadE2HTTPRuntimeOptions)이 그 밖에 보는 것은 헬퍼 바이너리 · CA · 상태 디렉터리 같은 설치 환경이라
// 이 패키지 시험에서는 띄우지 못한다(그 틀은 server 시험 패키지 안 e2HostPolicyFixture).
func requireHostPolicyAccepted(t *testing.T, step, policyFile string) server.E2HTTPFilePolicy {
	t.Helper()
	raw, err := os.ReadFile(policyFile)
	if err != nil {
		t.Fatal(step, err)
	}
	policy, fingerprint, err := server.ValidateE2HTTPRunPolicyDocument(raw)
	if err != nil || fingerprint == "" {
		t.Fatalf("%s: Host 정책 검사(E② 정책 컴파일 포함)가 발급 정책을 거부함: %v", step, err)
	}
	return policy
}

// hostLoadableTemplate은 설치 정책 틀에 Host 정책 검사가 요구하는 참조 칸을 채운다(product_exact_path 시험 틀과 같은 값).
// accountRunFixture 틀은 이 칸이 비어 계정과 상관없이 Host 정책 검사를 못 지난다.
func hostLoadableTemplate(f *urlNegativeFixture) {
	f.policy.RunRef, f.policy.ResetRef, f.policy.CaseRef = "template-run", "template-reset", "template-case"
	f.policy.PolicyRef, f.policy.PolicyRevision, f.policy.BudgetRef = "template-policy", "template-revision", "template-budget"
	f.policy.AccountRef, f.policy.SessionRef, f.policy.StateDir = "anonymous", "anonymous-session", f.launch.DataDir
}

// accountOnlyLoaded는 9칸 없이 시험 계정 자격만 등록한 대상을 제품 경로로 읽은 실행이다(실행 정책 파일까지 발급됨).
func accountOnlyLoaded(t *testing.T) (*urlLoadedProfile, diagnosisprep.ProductTarget) {
	t.Helper()
	runtime, _, target, input := accountRunFixtureWith(t, hostLoadableTemplate)
	r := runtime.targets
	if _, err := r.registerAccount("ARTEX", target.ID, "tester-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.setAccountCredential("ARTEX", target.ID, "tester-a", diagnosisprep.AccountCredential{Username: "u", Password: "host-load-pass-5c1e"}); err != nil {
		t.Fatal(err)
	}
	l, err := runtime.load("ARTEX", input, false, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.clearMaterials)
	return l, target
}

// issueDetachedOnHost는 계정을 뗀 후속 검증 실행을 발급하고(저장 후보는 issue, #950은 issueSelectedCandidate) Host 정책 검사에 태운다.
func issueDetachedOnHost(t *testing.T, step string, l *urlLoadedProfile, job string, selected bool) {
	t.Helper()
	if err := l.prepareTargetConnection(t.Context(), nil, job); err != nil {
		t.Fatal(step, err)
	}
	runs := t.TempDir()
	if err := os.Chmod(runs, 0700); err != nil {
		t.Fatal(err)
	}
	b := &urlPreparationRuntime{config: urlPreparationInstallation{RunsDir: runs}}
	issue := b.issue
	if selected {
		issue = func(l *urlLoadedProfile, job string, actual server.FirstDiagnosisPreparedTask, _ ...string) (launchConfig, error) {
			return b.issueSelectedCandidate(l, job, actual)
		}
	}
	launch, err := issue(l, job, server.FirstDiagnosisPreparedTask{TaskID: 95, ExplorationID: 96})
	if err != nil {
		t.Fatal(step, err)
	}
	policy := requireHostPolicyAccepted(t, step, launch.HTTP.PolicyFile)
	if policy.Auth != nil || policy.HeaderMode != e2proxy.HeaderPreserve || launch.HTTP.ExternalTestAccounts {
		t.Fatalf("%s: 계정 상태가 남은 발급: auth=%v header=%s external=%v", step, policy.Auth, policy.HeaderMode, launch.HTTP.ExternalTestAccounts)
	}
}

// 9칸 없이 자격만 등록한 대상(#1003 P1 교훈: 서버 인증 칸 검사만으로는 E② 정책 컴파일 거부가 안 보임). 세 정책이 다 실제
// 제품 코드로 만들어져 Host 정책 검사(E② 정책 컴파일 포함)를 지난다: 제품 결속 정책(인증 칸 · model_visible_v1 · 인증 준비 규칙
// 없음), 계정을 뗀 저장 후보 검증(active/v1 — 수용 시나리오가 없어 이 시험이 유일한 확인), 계정을 뗀 #950 익명 검증.
func TestAccountOnlyTargetPoliciesPassHostPolicyCheck(t *testing.T) {
	bound, _ := accountOnlyLoaded(t)
	policy := requireHostPolicyAccepted(t, "제품 결속", bound.launch.HTTP.PolicyFile)
	if policy.Auth == nil || policy.Auth.Login != nil || policy.Auth.PasswordResponse != e2proxy.PasswordResponseModelVisible ||
		policy.HeaderMode != e2proxy.HeaderTestAccountPassthrough {
		t.Fatalf("fixture: 9칸 없는 결속 정책이 아님: auth=%+v header=%s", policy.Auth, policy.HeaderMode)
	}
	for _, rule := range policy.Rules {
		if rule.Purpose == e2proxy.PurposeAuthPrep {
			t.Fatal("fixture: 9칸 없는 결속 정책에 인증 준비 규칙")
		}
	}

	stored, target := accountOnlyLoaded(t)
	if _, err := stored.detachAccounts(); err != nil {
		t.Fatal(err)
	}
	shapeStoredCandidatePolicy(stored, server.CandidateActivePlan{Origin: target.Origin, ExactPath: "/read", ExpiresAt: time.Now().Add(time.Hour)}, strings.Repeat("e", 64))
	issueDetachedOnHost(t, "저장 후보 검증(active/v1)", stored, "candidate-host-stored", false)

	anonymous, target := accountOnlyLoaded(t)
	plan := server.CandidateLLMPlan{Version: "candidate-llm-plan/v1", TargetID: target.ID, Origin: target.Origin, MaxRequests: 1, DurationSeconds: 30,
		ExpiresAt: time.Now().Add(time.Hour), Actions: []server.CandidateLLMPlanAction{{Purpose: "reproduction", Method: "GET", ExactPath: "/read"}}}
	if err := bindCandidateLLMAccount(anonymous, plan); err != nil {
		t.Fatal(err)
	}
	if _, _, err := shapeCandidateLLMPolicy(anonymous, plan, strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	issueDetachedOnHost(t, "#950 익명 검증", anonymous, "candidate-host-llm", true)
}
