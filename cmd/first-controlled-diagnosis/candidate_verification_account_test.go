package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/server"
)

// candidateAccountProfile은 제품 결속(runAccounts)이 만든 두 계정 실행 자료를 흉내 낸다. 제품 진단의 비밀번호 응답 정책은
// model_visible_v1이고, 자격은 합성 시험 값이다.
func candidateAccountProfile() (*urlLoadedProfile, map[string][]byte, diagnosisprep.TargetLogin) {
	login := diagnosisprep.TargetLogin{Method: "POST", Path: "/login", Format: "form", UsernameField: "email", PasswordField: "password", Cookies: []string{"session"}}
	shaA, shaB := strings.Repeat("a", 64), strings.Repeat("b", 64)
	materials := map[string][]byte{
		"cred-a.json": []byte(`{"username":"tester-a@owned.test","password":"abc12"}`),
		"cred-b.json": []byte(`{"username":"tester-b@owned.test","password":"other-account-pass"}`),
	}
	auth := &server.E2HTTPFileAuth{RuleRef: "auth-login", Login: &login, PasswordResponse: e2proxy.PasswordResponseModelVisible, ModelLogin: true,
		Accounts: []server.E2HTTPFileAuthAccount{
			{Label: "tester-a", Credential: server.E2HTTPFilePin{File: "cred-a.json", SHA256: shaA}},
			{Label: "tester-b", Credential: server.E2HTTPFilePin{File: "cred-b.json", SHA256: shaB}},
		}}
	l := &urlLoadedProfile{policy: server.E2HTTPFilePolicy{Auth: auth, HeaderMode: e2proxy.HeaderTestAccountPassthrough}, materials: materials,
		productAccounts: []productRunAccount{{Label: "tester-a", CredentialSHA256: shaA}, {Label: "tester-b", CredentialSHA256: shaB}}}
	return l, materials, login
}

func candidateAccountPlan(login diagnosisprep.TargetLogin, label, sha string) server.CandidateLLMPlan {
	return server.CandidateLLMPlan{AccountRef: server.CandidateAccountRevision(label, sha),
		AuthPreparation: &server.CandidateLLMAuthPreparation{Method: login.Method, ExactPath: login.Path, LoginDigest: server.CandidateLoginDigest(login)}}
}

func zeroed(raw []byte) bool { return bytes.Equal(raw, make([]byte, len(raw))) }

// 선택 후보 검증 회차도 일반 제품 진단과 같은 비밀번호 응답 정책(model_visible_v1)을 쓴다(절대규칙 2, 2026-11-26 DLP 폐기).
// 선택한 계정 하나만 남고, 다른 계정 자격 사본은 메모리에서 지워진다. 짧은 비밀번호 계정도 그대로 결속된다.
// 적재 자료에 이전 정책(mask_spans_v1·칸 생략)이 남아 있어도 선택 후보 회차는 제품 정책으로 맞춘다.
func TestCandidateLLMSelectedAccountUsesProductPasswordPolicy(t *testing.T) {
	for _, loaded := range []string{e2proxy.PasswordResponseModelVisible, e2proxy.PasswordResponseMaskSpans, ""} {
		t.Run("loaded="+loaded, func(t *testing.T) { checkCandidateLLMSelectedAccount(t, loaded) })
	}
}

func checkCandidateLLMSelectedAccount(t *testing.T, loaded string) {
	l, materials, login := candidateAccountProfile()
	l.policy.Auth.PasswordResponse = loaded
	other := materials["cred-b.json"]
	if err := bindCandidateLLMAccount(l, candidateAccountPlan(login, "tester-a", strings.Repeat("a", 64))); err != nil {
		t.Fatal(err)
	}
	if l.policy.Auth == nil || l.policy.Auth.PasswordResponse != e2proxy.PasswordResponseModelVisible {
		t.Fatalf("선택 후보 검증 비밀번호 정책이 제품과 다름: %+v", l.policy.Auth)
	}
	if len(l.policy.Auth.Accounts) != 1 || l.policy.Auth.Accounts[0].Label != "tester-a" || len(l.productAccounts) != 1 || l.productAccounts[0].Label != "tester-a" {
		t.Fatalf("선택 계정만 남지 않음: %+v %+v", l.policy.Auth.Accounts, l.productAccounts)
	}
	if l.policy.SelectedAccountRef != server.CandidateAccountRevision("tester-a", strings.Repeat("a", 64)) || l.policy.HeaderMode != e2proxy.HeaderTestAccountPassthrough {
		t.Fatal("선택 계정 판본·헤더 모드 결속 변경")
	}
	if _, ok := l.materials["cred-b.json"]; ok || !zeroed(other) {
		t.Fatal("선택하지 않은 계정 자격 사본이 남음")
	}
	if string(l.materials["cred-a.json"]) != `{"username":"tester-a@owned.test","password":"abc12"}` {
		t.Fatal("선택 계정 자격 사본이 바뀌거나 빠짐")
	}
}

// 익명 원본 후보 검증에는 계정 자격이 하나도 가지 않는다.
func TestCandidateLLMAnonymousPlanDropsAccountMaterial(t *testing.T) {
	l, materials, _ := candidateAccountProfile()
	a, b := materials["cred-a.json"], materials["cred-b.json"]
	if err := bindCandidateLLMAccount(l, server.CandidateLLMPlan{}); err != nil {
		t.Fatal(err)
	}
	if l.policy.Auth != nil || l.productAccounts != nil || l.policy.HeaderMode != e2proxy.HeaderPreserve || l.policy.SelectedAccountRef != "" {
		t.Fatal("익명 검증에 계정 결속이 남음")
	}
	if len(l.materials) != 0 || !zeroed(a) || !zeroed(b) {
		t.Fatal("익명 검증에 계정 자격 사본이 남음")
	}
}

// 계획과 지금 결속이 어긋나면(자격 판본·로그인 방식·헤더 모드 변경, 계정 없는 계획에 인증 준비) 실행하지 않는다.
func TestCandidateLLMAccountBindingRejectsChangedState(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*urlLoadedProfile, *server.CandidateLLMPlan)
	}{
		{"credential revision changed", func(l *urlLoadedProfile, _ *server.CandidateLLMPlan) {
			l.policy.Auth.Accounts[0].Credential.SHA256 = strings.Repeat("c", 64)
		}},
		{"run account revision changed", func(l *urlLoadedProfile, _ *server.CandidateLLMPlan) {
			l.productAccounts[0].CredentialSHA256 = strings.Repeat("c", 64)
		}},
		{"account not bound", func(_ *urlLoadedProfile, p *server.CandidateLLMPlan) {
			p.AccountRef = server.CandidateAccountRevision("tester-c", strings.Repeat("a", 64))
		}},
		{"login changed", func(l *urlLoadedProfile, _ *server.CandidateLLMPlan) { l.policy.Auth.Login.Path = "/signin" }},
		{"login digest changed", func(_ *urlLoadedProfile, p *server.CandidateLLMPlan) {
			p.AuthPreparation.LoginDigest = strings.Repeat("0", 64)
		}},
		{"header mode changed", func(l *urlLoadedProfile, _ *server.CandidateLLMPlan) { l.policy.HeaderMode = e2proxy.HeaderPreserve }},
		{"no auth preparation", func(_ *urlLoadedProfile, p *server.CandidateLLMPlan) { p.AuthPreparation = nil }},
		{"anonymous plan with auth preparation", func(_ *urlLoadedProfile, p *server.CandidateLLMPlan) { p.AccountRef = "" }},
		{"no auth in run", func(l *urlLoadedProfile, _ *server.CandidateLLMPlan) { l.policy.Auth = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, _, login := candidateAccountProfile()
			plan := candidateAccountPlan(login, "tester-a", strings.Repeat("a", 64))
			tc.mutate(l, &plan)
			if bindCandidateLLMAccount(l, plan) == nil {
				t.Fatal("어긋난 결속으로 실행을 준비함")
			}
		})
	}
}
