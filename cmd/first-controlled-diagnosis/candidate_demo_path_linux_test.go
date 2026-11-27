//go:build linux

package main

import (
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/server"
)

// 시연 경로(9칸-1, 민준 2026-11-27 — 시연에서 이 장면을 쓴다): 시험 계정이 등록된 대상 → 익명 후보 → 선택 검증(#950) → 외부
// 모델 설치의 E② 호스트가 그 실행 정책을 받는다. 익명 분기(prepareAnonymousCandidate가 인증 칸을 뗌)와 호스트의 거부 조건(외부
// 모델 + 인증 칸 + 시험 계정 동의 없음, server.firstDiagnosisHTTPPolicyAllowed)을 따로 시험하지 않고 한 시험으로 묶는다: 실제
// 제품 결속 · 실제 #950 익명 분기 · 실제 #950 호스트 시작 옵션 · 서버의 같은 판정을 이어서 지나므로 어느 쪽을 바꿔도 깨진다.
func TestDemoPathAnonymousCandidateOnAccountTargetRunsWithExternalModel(t *testing.T) {
	runtime, _, target, input := accountRunFixtureWith(t, externalModelInstall)
	r := runtime.targets
	if _, err := r.registerAccount("ARTEX", target.ID, "tester-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.setAccountCredential("ARTEX", target.ID, "tester-a", diagnosisprep.AccountCredential{Username: "u", Password: "seeded-pass-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := writeProductExternalConsent(runtime.config.StateDir, productExternalConsentInstall, "ARTEX", time.Now()); err != nil {
		t.Fatal(err)
	}
	l, err := runtime.load("ARTEX", input, false, "")
	if err != nil {
		t.Fatal(err)
	}
	defer l.clearMaterials()
	if l.policy.Auth == nil || !l.externalTestAccounts {
		t.Fatal("fixture: registered account not bound to the external model run")
	}
	// 익명 분기 전: 계정이 붙은 정책은 #950 외부 모델 호스트가 받지 않는다(#950 시작 옵션에는 시험 계정 동의 칸이 없다).
	external, admitted, err := server.FirstDiagnosisHTTPHostAdmits(candidateLLMStartOptions(l.launch), l.policy)
	if err != nil || !external || admitted {
		t.Fatalf("account-bound #950 policy on the external model host: external=%v admitted=%v err=%v", external, admitted, err)
	}
	if l.prepareAnonymousCandidate(server.CandidateLLMPlan{Version: "candidate-llm-plan/v1", TargetID: target.ID}) != nil {
		t.Fatal("anonymous candidate preparation failed")
	}
	external, admitted, err = server.FirstDiagnosisHTTPHostAdmits(candidateLLMStartOptions(l.launch), l.policy)
	if err != nil || !external || !admitted {
		t.Fatalf("시연 경로: anonymous #950 refused by the external model host: external=%v admitted=%v err=%v", external, admitted, err)
	}
	// 계정 참조나 계정 준비가 붙은 계획은 익명 분기로 오지 않는다.
	for _, plan := range []server.CandidateLLMPlan{{AccountRef: "tester-a-ref"}, {AuthPreparation: &server.CandidateLLMAuthPreparation{}}} {
		if l.prepareAnonymousCandidate(plan) == nil {
			t.Fatalf("anonymous preparation accepted an account plan: %+v", plan)
		}
	}
}
