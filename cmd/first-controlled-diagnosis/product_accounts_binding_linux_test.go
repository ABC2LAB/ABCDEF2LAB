//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/llmpolicy"
	"github.com/Autumn-27/artex/server"
)

// 실행 결속(C14 구현 설계 5): 지금 test이고 자격·로그인 방식이 있는 계정만 골라 실행 정책에 인증 준비 칸·규칙을 넣고
// 자격 사본을 결속 디렉터리에 고정한다. 결속한 계정이 해제되면 그 실행의 확인이 실패하고(새 송신 정지) 사본을 지운다.
// accountRunFixture는 실제 제품 설치·대상 등록·승인까지 마친 제품 실행 환경이다(DNS·TLS 확인은 가짜).
func accountRunFixture(t *testing.T) (*productRuntime, urlPreparationInstallation, diagnosisprep.ProductTarget, diagnosisprep.Input) {
	t.Helper()
	return accountRunFixtureWith(t, nil)
}

// accountRunFixtureWith는 accountRunFixture와 같고, 제품 설치를 만들기 전에 설치 틀을 바꾼다(외부 모델 설치 등).
func accountRunFixtureWith(t *testing.T, change func(*urlNegativeFixture)) (*productRuntime, urlPreparationInstallation, diagnosisprep.ProductTarget, diagnosisprep.Input) {
	t.Helper()
	f := newURLNegativeFixture(t)
	completeProductTemplateForTest(f)
	if change != nil {
		change(f)
	}
	m := f.config.Profiles[0]
	m.TargetRef, m.ScopeRef, m.AllowPrivateTarget, m.Attestation = "", "", false, urlNonmutatingAttestation{}
	f.policy.Rules = f.policy.Rules[:0]
	m.PolicySHA256 = urlNegativeWrite(t, f.launch.HTTP.PolicyFile, f.policy)
	f.config.Profiles = nil
	limits := &diagnosisflow.Limits{Format: diagnosisflow.Version, MaxPages: 2, MaxEndpoints: 32, MaxResponseBytes: 4096}
	policyPath := filepath.Join(filepath.Dir(f.path), "account-target-policy.json")
	digest := urlNegativeWrite(t, policyPath, productTargetPolicy{Format: "abc2lab.product-target-policy/v1", AllowedPorts: []int{443}, PrivateCIDRs: []string{"10.200.1.0/24"}, MaxRequests: 4, MaxTargets: 32, Workflow: limits})
	f.config.Product = &urlProductInstallation{Format: "abc2lab.product-runtime/v2", TargetPolicy: urlProtectedPin{File: policyPath, SHA256: digest}, Models: []urlPreparationProfile{m}}
	urlNegativeWrite(t, f.path, f.config)
	cfg, pin, err := readURLInstallation(f.path)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := newProductRuntime(cfg, pin)
	if err != nil {
		t.Fatal(err)
	}
	r := runtime.targets
	txt := map[string][]string{}
	r.lookupTXT = func(_ context.Context, name string) ([]string, error) { return txt[name], nil }
	r.lookupIP = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.200.1.10")}, nil
	}
	r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
		return strings.Repeat("a", 64), time.Now().Add(time.Hour), nil
	}
	spec := targetTestSpec("owned.example.test")
	spec.Requests = m.Budget.Requests
	target, err := r.register("ARTEX", spec)
	if err != nil {
		t.Fatal(err)
	}
	txt[target.ChallengeName] = []string{target.ChallengeValue}
	if _, err = r.approve(t.Context(), "ARTEX", target.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.targets.Close() })
	return runtime, cfg, target, diagnosisprep.Input{TargetURL: "https://owned.example.test/read", ProfileID: m.ID, DisplayName: "Owned account run"}
}

func TestProductRunBindsRegisteredAccountsAndStopsOnUnmark(t *testing.T) {
	runtime, cfg, target, input := accountRunFixture(t)
	r := runtime.targets
	// 계정이 없으면 인증 준비 칸이 없다.
	plain, err := runtime.load("ARTEX", input, false, "")
	if err != nil {
		t.Fatal(err)
	}
	plain.clearMaterials()
	if plain.policy.Auth != nil || len(plain.productAccounts) != 0 || plain.policy.HeaderMode != e2proxy.HeaderPreserve {
		t.Fatal("run without test accounts got an auth section or another header mode")
	}
	// 계정이 없는 회차도 로그인 시도 판정 설정을 받는다(등록 밖 아이디의 연속 실패 상한, 사용자 결정 2026-11-26).
	if plain.policy.LoginAttempts == nil || plain.policy.LoginAttempts.MaxConsecutiveFailures != productLoginConsecutiveFailures {
		t.Fatal("계정 없는 회차에 로그인 시도 판정 설정이 없음")
	}
	login := diagnosisprep.TargetLogin{Method: "POST", Path: "/read/login", Format: "json", UsernameField: "email", PasswordField: "password", Cookies: []string{"session"}}
	if _, err := r.setTargetLogin("ARTEX", target.ID, login); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"tester-a", "real-b"} {
		if _, err := r.registerAccount("ARTEX", target.ID, label); err != nil {
			t.Fatal(err)
		}
		if _, err := r.setAccountCredential("ARTEX", target.ID, label, diagnosisprep.AccountCredential{Username: "u-" + label, Password: "seeded-pass-" + label}); err != nil {
			t.Fatal(err)
		}
	}
	// 계정을 시험·실계정으로 따로 분류하지 않는다(사용자 결정 2026-11-26, CLAUDE.md 절대규칙 2). 자격이 있고 대상에 로그인
	// 방식이 있으면 시험 표시 없이도 등록 계정이 실행에 결속된다(모델이 직접 로그인한다).
	loaded, err := runtime.load("ARTEX", input, false, "")
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.clearMaterials()
	registered, err := r.listAccounts("ARTEX", target.ID)
	if err != nil || len(registered) != 2 || !registered[0].CredentialConfigured || !registered[1].CredentialConfigured {
		t.Fatal("등록 계정 목록", err)
	}
	auth := loaded.policy.Auth
	if auth == nil || !auth.ModelLogin || loaded.policy.LoginAttempts == nil || loaded.policy.LoginAttempts.MaxConsecutiveFailures != productLoginConsecutiveFailures || auth.PasswordResponse != e2proxy.PasswordResponseModelVisible || len(auth.Accounts) != 2 || auth.Accounts[0].Label != "real-b" || auth.Accounts[1].Label != "tester-a" || auth.Login.Path != "/read/login" || loaded.policy.HeaderMode != e2proxy.HeaderTestAccountPassthrough {
		t.Fatalf("auth section: %+v mode=%s", auth, loaded.policy.HeaderMode)
	}
	var rule bool
	for _, x := range loaded.policy.Rules {
		rule = rule || x.Ref == auth.RuleRef && x.Purpose == e2proxy.PurposeAuthPrep && x.PathPrefix == "/read/login" && len(x.Methods) == 1 && x.Methods[0] == "POST"
	}
	if !rule {
		t.Fatalf("auth rule missing: %+v", loaded.policy.Rules)
	}
	// 서버는 실행 디렉터리에서 자격 사본을 pin으로 읽는다. 실행 자료에 pin과 같은 사본이 있어야 한다(S07 실제 회차).
	for _, account := range auth.Accounts {
		cred := account.Credential
		if raw, ok := loaded.materials[cred.File]; !ok || urlSHA(raw) != cred.SHA256 {
			t.Fatal("account credential copy missing from run materials", account.Label)
		}
		copies, _ := filepath.Glob(filepath.Join(cfg.StateDir, "bindings", "*", "account-"+target.ID+"."+account.Label+".*"))
		if len(copies) != 1 {
			t.Fatalf("credential copy in binding: %v", copies)
		}
		if raw, err := os.ReadFile(copies[0]); err != nil || urlSHA(raw) != cred.SHA256 {
			t.Fatal("credential copy does not match its pin")
		}
	}
	if loaded.current() != nil {
		t.Fatal("fresh binding failed its check")
	}
	// 결속한 계정의 자격을 바꾸면(제품 제어 account_credential_set) 결속한 실행은 확인이 실패한다(새 송신 정지). 결속 디렉터리와
	// 실행 디렉터리의 그 계정 사본도 지운다(다른 계정 사본은 둔다). 시험 계정 표시 · 해제는 없다(계정 분류 폐지, 2026-11-26).
	runCopy := filepath.Join(cfg.RunsDir, "job-replace", bindingCredentialName(target.ID, "tester-a", strings.Repeat("c", 64)))
	otherCopy := filepath.Join(cfg.RunsDir, "job-replace", bindingCredentialName(target.ID, "real-b", strings.Repeat("d", 64)))
	if os.MkdirAll(filepath.Dir(runCopy), 0o700) != nil || os.WriteFile(runCopy, []byte("{}"), 0o600) != nil || os.WriteFile(otherCopy, []byte("{}"), 0o600) != nil {
		t.Fatal("run copy fixture")
	}
	endpoint := productPreparationEndpoint{product: runtime}
	req := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "product-control", Principal: "ARTEX", Command: diagnosisprep.ProductControl,
		Product: &diagnosisprep.ProductRequest{Operation: "account_credential_set", TargetID: target.ID, Account: &diagnosisprep.AccountRequest{Label: "tester-a"},
			Credential: &diagnosisprep.AccountCredential{Username: "u-replaced", Password: "seeded-pass-replaced"}}}
	if resp, err := endpoint.Call(t.Context(), req); err != nil || resp.Product == nil || resp.Product.Reason != "" {
		t.Fatalf("credential replace: %v %+v", err, resp)
	}
	if err := loaded.current(); !errors.Is(err, errAccountBindingChanged) {
		t.Fatalf("replaced credential of a bound account did not stop the run: %v", err)
	}
	if _, err := os.Stat(runCopy); !os.IsNotExist(err) {
		t.Fatal("run directory credential copy left after credential replace")
	}
	if _, err := os.Stat(otherCopy); err != nil {
		t.Fatal("another account's run copy removed")
	}
	if copies, _ := filepath.Glob(filepath.Join(cfg.StateDir, "bindings", "*", "account-"+target.ID+".tester-a.*")); len(copies) != 0 {
		t.Fatalf("credential copy left after credential replace: %v", copies)
	}
	// 새 실행은 바뀐 자격으로 두 계정을 다시 결속하고, 대상을 철회하면 그 대상의 사본을 모두 지운다.
	next, err := runtime.load("ARTEX", input, false, "")
	if err != nil {
		t.Fatal(err)
	}
	next.clearMaterials()
	if next.policy.Auth == nil || len(next.policy.Auth.Accounts) != 2 {
		t.Fatalf("next binding: %+v", next.policy.Auth)
	}
	if _, err := runtime.revoke("ARTEX", target.ID); err != nil {
		t.Fatal(err)
	}
	if copies, _ := filepath.Glob(filepath.Join(cfg.StateDir, "bindings", "*", "account-"+target.ID+".*")); len(copies) != 0 {
		t.Fatalf("credential copies left after revoke: %v", copies)
	}
}

// 인증 준비 규칙은 auth_prep·POST 하나·인증 칸이 가리키는 규칙·증명한 경로 접두 안이어야 결속을 읽는다.
func TestURLAuthRuleValid(t *testing.T) {
	policy := server.E2HTTPFilePolicy{Auth: &server.E2HTTPFileAuth{RuleRef: "auth-login", Login: &diagnosisprep.TargetLogin{Path: "/read/login"}}}
	rule := server.E2HTTPFileRule{Ref: "auth-login", PathPrefix: "/read/login", Methods: []string{"POST"}, Purpose: e2proxy.PurposeAuthPrep}
	if !urlAuthRuleValid(policy, rule, []string{"/read"}) {
		t.Fatal("valid auth rule rejected")
	}
	for name, change := range map[string]func(*server.E2HTTPFilePolicy, *server.E2HTTPFileRule, *[]string){
		"outside prefix": func(_ *server.E2HTTPFilePolicy, _ *server.E2HTTPFileRule, p *[]string) { *p = []string{"/other"} },
		"sibling prefix": func(_ *server.E2HTTPFilePolicy, r *server.E2HTTPFileRule, p *[]string) { *p = []string{"/rea"} },
		"no auth":        func(x *server.E2HTTPFilePolicy, _ *server.E2HTTPFileRule, _ *[]string) { x.Auth = nil },
		"other ref": func(x *server.E2HTTPFilePolicy, _ *server.E2HTTPFileRule, _ *[]string) {
			x.Auth = &server.E2HTTPFileAuth{RuleRef: "x", Login: x.Auth.Login}
		},
		"get":     func(_ *server.E2HTTPFilePolicy, r *server.E2HTTPFileRule, _ *[]string) { r.Methods = []string{"GET"} },
		"purpose": func(_ *server.E2HTTPFilePolicy, r *server.E2HTTPFileRule, _ *[]string) { r.Purpose = "admin" },
	} {
		x, r, prefixes := policy, rule, []string{"/read"}
		change(&x, &r, &prefixes)
		if urlAuthRuleValid(x, r, prefixes) {
			t.Errorf("%s accepted", name)
		}
	}
}

// 실행 고르기: 자격이 없는 계정은 고르지 않는다. 로그인 방식(9칸)이 없어도 자격이 있으면 결속하고(9칸-1, 계약
// model-login-attempts §7) 로그인 방식 칸은 비운다. 계정 응답은 login_method_missing을 싣지 않는다. 로그인 방식을 넣으면 같은 계정이 로그인 방식과 함께 결속되고 요약이
// 바뀐다. 시험 표시는 보지 않는다(분류 없음, 2026-11-26). 자격을 바꾸면 요약이 바뀌고 결속한 실행은 멈춘다.
func TestProductRunAccountsSelection(t *testing.T) {
	r, _, target := accountTestTarget(t, true)
	rec := r.approvals[target.ID]
	for _, label := range []string{"tester-a", "tester-b", "real-c"} {
		if _, err := r.registerAccount("ARTEX", target.ID, label); err != nil {
			t.Fatal(err)
		}
	}
	for _, label := range []string{"tester-a", "real-c"} {
		account, err := r.setAccountCredential("ARTEX", target.ID, label, diagnosisprep.AccountCredential{Username: "u", Password: "p-" + label})
		if err != nil || !account.CredentialConfigured || account.LoginMethodMissing {
			t.Fatal("자격 등록 응답: 자격 있음 · 로그인 방식 미설정 칸 없음(9칸-1)", err)
		}
		if raw, _ := json.Marshal(account); strings.Contains(string(raw), "login_method_missing") {
			t.Fatalf("자격 등록 응답에 login_method_missing: %s", raw)
		}
	}
	// 시험 계정 표시 없이 목록이 자격 · 로그인 방식 상태를 그대로 보인다(계정 분류 폐지, 2026-11-26)
	listed, err := r.listAccounts("ARTEX", target.ID)
	if err != nil || len(listed) != 3 {
		t.Fatal("계정 목록 조회 실패", err)
	}
	// 로그인 방식 없이 자격만 있는 계정도 목록에 login_method_missing을 싣지 않는다(9칸-1: 결속해서 모델이 직접 로그인한다)
	for _, account := range listed {
		raw, _ := json.Marshal(account)
		if account.LoginMethodMissing || strings.Contains(string(raw), "login_method_missing") || account.CredentialConfigured != (account.Label != "tester-b") {
			t.Fatalf("자격 상태 불일치 또는 login_method_missing: %s", raw)
		}
	}
	noLogin, noLoginDigest, err := r.runAccounts(rec)
	if err != nil || noLoginDigest == "" || noLogin.Login != nil || len(noLogin.Accounts) != 2 ||
		noLogin.Accounts[0].Label != "real-c" || noLogin.Accounts[1].Label != "tester-a" {
		t.Fatalf("selection without a login method: %+v %q %v", noLogin, noLoginDigest, err)
	}
	if _, err := r.setTargetLogin("ARTEX", target.ID, cookieLogin()); err != nil {
		t.Fatal(err)
	}
	accounts, err := r.listAccounts("ARTEX", target.ID)
	if err != nil || len(accounts) != 3 {
		t.Fatal("로그인 방식 설정 후 계정 목록 조회 실패", err)
	}
	for _, account := range accounts {
		if account.LoginMethodMissing || account.CredentialConfigured != (account.Label != "tester-b") {
			t.Fatal("로그인 방식 설정 후 계정 상태 불일치", account.Label)
		}
	}
	got, digest, err := r.runAccounts(rec)
	if err != nil || digest == "" || got.Login == nil || len(got.Accounts) != 2 || got.Accounts[0].Label != "real-c" || got.Accounts[1].Label != "tester-a" {
		t.Fatalf("selection: %+v %v", got, err)
	}
	if digest == noLoginDigest {
		t.Fatal("adding a login method kept the binding digest")
	}
	if r.accountsCurrent(rec, got.Accounts) != nil {
		t.Fatal("fresh selection not current")
	}
	if _, err := r.setAccountCredential("ARTEX", target.ID, "tester-a", diagnosisprep.AccountCredential{Username: "u", Password: "replaced"}); err != nil {
		t.Fatal(err)
	}
	if _, digest2, _ := r.runAccounts(rec); digest2 == digest {
		t.Fatal("replacing a credential kept the binding digest")
	}
	if !errors.Is(r.accountsCurrent(rec, got.Accounts), errAccountBindingChanged) {
		t.Fatal("run bound to the replaced credential kept sending")
	}
	if r.accountsCurrent(rec, nil) != nil {
		t.Fatal("run without bound accounts stopped")
	}
	// 그 계정만 자격을 철회하면(#982 이동찬 P2) 다음 결속에서 빠지고, 그 계정을 결속한 실행은 멈춘다
	fresh, _, err := r.runAccounts(rec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.clearAccountCredential("ARTEX", target.ID, "tester-a"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(r.accountsCurrent(rec, fresh.Accounts), errAccountBindingChanged) {
		t.Fatal("run bound to a cleared credential kept sending")
	}
	after, _, err := r.runAccounts(rec)
	if err != nil || len(after.Accounts) != 1 || after.Accounts[0].Label != "real-c" {
		t.Fatalf("selection after clear: %+v %v", after, err)
	}
}

// 저장 보고서는 현재 송신 권한·계정 선택과 상관없이 그 실행의 결속을 다시 찾는다: 계정 없는 실행은 대상 철회 뒤에도,
// 계정을 결속한 실행은 자격 교체·철회 뒤에도 작업의 원본 요약으로 같은 결속을 연다(#520 리뷰).
func TestProductReportLoadsBindingAfterRevokeAndCredentialChange(t *testing.T) {
	runtime, _, target, input := accountRunFixture(t)
	r := runtime.targets
	plain, err := runtime.load("ARTEX", input, false, "")
	if err != nil {
		t.Fatal(err)
	}
	plainSource := diagnosisprep.SourceDigest(input, plain.inspection.InstallationDigest)
	plain.clearMaterials()
	if _, err := r.setTargetLogin("ARTEX", target.ID, diagnosisprep.TargetLogin{Method: "POST", Path: "/read/login", Format: "form", UsernameField: "email", PasswordField: "password", Cookies: []string{"session"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.registerAccount("ARTEX", target.ID, "tester-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.setAccountCredential("ARTEX", target.ID, "tester-a", diagnosisprep.AccountCredential{Username: "u", Password: "seeded-pass-a"}); err != nil {
		t.Fatal(err)
	}
	withAccounts, err := runtime.load("ARTEX", input, false, "")
	if err != nil {
		t.Fatal(err)
	}
	accountSource := diagnosisprep.SourceDigest(input, withAccounts.inspection.InstallationDigest)
	withAccounts.clearMaterials()
	if accountSource == plainSource {
		t.Fatal("account binding shares the plain binding digest")
	}
	endpoint := productPreparationEndpoint{product: runtime}
	replace := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "product-control", Principal: "ARTEX", Command: diagnosisprep.ProductControl,
		Product: &diagnosisprep.ProductRequest{Operation: "account_credential_set", TargetID: target.ID, Account: &diagnosisprep.AccountRequest{Label: "tester-a"},
			Credential: &diagnosisprep.AccountCredential{Username: "u", Password: "seeded-pass-b"}}}
	if resp, err := endpoint.Call(t.Context(), replace); err != nil || resp.Product == nil || resp.Product.Reason != "" {
		t.Fatalf("credential replace: %v %+v", err, resp)
	}
	if _, err := runtime.revoke("ARTEX", target.ID); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		source string
		auth   bool
	}{"plain run": {plainSource, false}, "account run": {accountSource, true}} {
		report, err := runtime.load("ARTEX", input, true, target.ID, c.source)
		if err != nil {
			t.Fatalf("%s report after revoke: %v", name, err)
		}
		if got := diagnosisprep.SourceDigest(input, report.inspection.InstallationDigest); got != c.source || (report.policy.Auth != nil) != c.auth {
			t.Errorf("%s report opened another binding (auth=%v)", name, report.policy.Auth != nil)
		}
		report.clearMaterials()
	}
	// 원본 요약 없이 읽으면 지금처럼 계정 없는 결속이다.
	if report, err := runtime.load("ARTEX", input, true, target.ID); err != nil || report.policy.Auth != nil {
		t.Fatalf("report without a source digest: %v", err)
	} else {
		report.clearMaterials()
	}
	if _, err := runtime.load("ARTEX", input, true, target.ID, strings.Repeat("0", 64)); err == nil {
		t.Fatal("report with an unknown source digest opened a binding")
	}
}

// 외부 모델 실행의 시험 계정 동의는 launch 칸으로 서버 호스트 설정에 그대로 간다(동의 자체는 설치 단위 기록이 정한다).
func TestExternalAccountsConsentCarriedToHost(t *testing.T) {
	c := launchConfig{HTTP: &httpLaunch{PolicyFile: "p", AuthorityFile: "a", AuthorityRoot: "r", ExternalTestAccounts: true}}
	if host, ok := c.httpHostConfig(); !ok || !host.ExternalTestAccounts {
		t.Fatal("launch consent not carried to the host config")
	}
	c.HTTP.ExternalTestAccounts = false
	if host, ok := c.httpHostConfig(); !ok || host.ExternalTestAccounts {
		t.Fatal("host config consent without launch consent")
	}
}

// 외부 API 실행의 시험 계정 동의는 대상 승인자만 남기고, 진술 해시·주체·작업이 계정 저널에 남는다. 저널을 다시 읽어도 받으며,
// 진술 해시가 다르거나 계정 라벨·다른 줄의 작업 칸이 섞인 줄은 거부한다(#552 윤재상 리뷰).
func TestExternalConsentRecordedInAccountJournal(t *testing.T) {
	r, root, target := accountTestTarget(t, true)
	if err := r.recordExternalConsent("someone-else", target.ID, "job-consent"); err == nil {
		t.Fatal("non-approver recorded an external consent")
	}
	if err := r.recordExternalConsent("ARTEX", target.ID, "../bad"); err == nil {
		t.Fatal("invalid job ref accepted")
	}
	if err := r.recordExternalConsent("ARTEX", target.ID, "job-consent"); err != nil {
		t.Fatal(err)
	}
	// 거부한 시도는 저널에 아무것도 남기지 않고, 남긴 동의 줄은 다시 열어도 받는다.
	accountDir := r.accountDir
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	// 새로 남긴 동의 줄은 지금 진술(11/23 문장 포함)의 해시다. 이전 진술 해시는 재생 때만 받는다.
	journal, events, err := diagnosisprep.OpenProtectedJournal(accountDir)
	if err != nil {
		t.Fatal(err)
	}
	consents := 0
	for _, raw := range events {
		var e productAccountEvent
		if json.Unmarshal(raw, &e) == nil && e.Action == accountExternalConsent {
			consents++
			if e.StatementSHA256 != productExternalConsentStatementSHA256() {
				t.Fatal("consent recorded with a statement other than the current one")
			}
		}
	}
	if err := journal.Close(); err != nil || consents != 1 {
		t.Fatalf("consent lines=%d close=%v", consents, err)
	}
	reopened, err := newProductTargetRegistry(root, r.policy, func() error { return nil })
	if err != nil {
		t.Fatal("account journal does not reopen after consent attempts:", err)
	}
	defer reopened.Close()
	r = reopened
	rec := r.records[target.ID]
	good := productAccountEvent{TargetID: target.ID, Action: accountExternalConsent, Actor: "ARTEX", At: time.Now(), TargetRevision: rec.Target.Revision,
		StatementSHA256: productExternalConsentStatementSHA256(), JobRef: "job-replay"}
	if err := r.replayAccount(good); err != nil {
		t.Fatal("valid consent line rejected on replay:", err)
	}
	// 11/23 전 진술로 남은 동의 줄도 다시 읽는다(진술을 바꿔도 이전 설치의 저널이 열려야 한다). 새로 남기는 줄은 지금 진술이다.
	previous := good
	previous.StatementSHA256, previous.JobRef = statementSHA256(productExternalConsentStatementV1), "job-replay-v1"
	if err := r.replayAccount(previous); err != nil {
		t.Fatal("previous consent statement rejected on replay:", err)
	}
	if productExternalConsentStatementSHA256() == statementSHA256(productExternalConsentStatementV1) {
		t.Fatal("consent statement did not change")
	}
	for name, change := range map[string]func(*productAccountEvent){
		"statement": func(e *productAccountEvent) { e.StatementSHA256 = productAccountStatementSHA256() },
		"label":     func(e *productAccountEvent) { e.Label = "tester-a" },
		"no-job":    func(e *productAccountEvent) { e.JobRef = "" },
		"actor":     func(e *productAccountEvent) { e.Actor = "someone-else" },
	} {
		bad := good
		change(&bad)
		if err := r.replayAccount(bad); err == nil {
			t.Fatalf("%s: tampered consent line accepted", name)
		}
	}
	// 다른 줄에 작업 칸이 섞이면 거부한다.
	if err := r.replayAccount(productAccountEvent{TargetID: target.ID, Label: "tester-x", Action: accountRegister, Actor: "ARTEX", At: time.Now(), TargetRevision: rec.Target.Revision, JobRef: "job-x"}); err == nil {
		t.Fatal("job ref on a register line accepted")
	}
	// 회차 감사 줄은 설치 단위 문장의 해시를 남긴다. 이전 회차별 진술 두 판으로 남은 줄도 다시 읽는다.
	if productExternalConsentStatementSHA256() != productExternalTransferStatementSHA256() || productExternalConsentStatementSHA256() == productAccountStatementSHA256() {
		t.Fatal("consent line statement not the install statement")
	}
	for _, old := range []string{productExternalConsentStatementV2, productExternalConsentStatementV1} {
		if !knownExternalConsentStatementSHA256(statementSHA256(old)) {
			t.Fatal("earlier consent statement no longer replays")
		}
	}
}

// 실행이 끝나면 실행 디렉터리의 자격 사본(정책 pin 이름)을 지우고 다른 실행 자료는 둔다(#551 윤재상 리뷰).
func TestRemoveRunCredentials(t *testing.T) {
	runs := t.TempDir()
	dir := filepath.Join(runs, "job-1")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cred := "account-target-1.tester-a.0123456789abcdef.json"
	for _, name := range []string{cred, "policy.json", "audit-key"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	auth := &server.E2HTTPFileAuth{Accounts: []server.E2HTTPFileAuthAccount{{Label: "tester-a", Credential: server.E2HTTPFilePin{File: cred}}, {Label: "gone", Credential: server.E2HTTPFilePin{File: "account-target-1.gone.0123456789abcdef.json"}}}}
	removeRunCredentials(runs, "job-1", auth)
	if _, err := os.Stat(filepath.Join(dir, cred)); !os.IsNotExist(err) {
		t.Fatal("credential copy left in the run directory")
	}
	for _, name := range []string{"policy.json", "audit-key"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal("other run material removed:", name)
		}
	}
	removeRunCredentials(runs, "../escape", auth)
	removeRunCredentials(runs, "job-1", nil)
}

// 9칸-1(계약 model-login-attempts §7): 로그인 방식 없이 자격만 등록한 계정도 실행에 결속한다. 정책 인증 칸에는 로그인 방식 ·
// 규칙 참조가 없고 인증 준비 규칙도 없다(프로그램 로그인 없음, 모델이 직접 로그인). 시험 계정 헤더 모드로 묶인다.
func TestProductBindsAccountWithoutLoginMethod(t *testing.T) {
	runtime, _, target, input := accountRunFixture(t)
	r := runtime.targets
	if _, err := r.registerAccount("ARTEX", target.ID, "tester-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.setAccountCredential("ARTEX", target.ID, "tester-a", diagnosisprep.AccountCredential{Username: "u", Password: "seeded-pass-a"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := runtime.load("ARTEX", input, false, "")
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.clearMaterials()
	auth := loaded.policy.Auth
	if auth == nil || auth.Login != nil || auth.RuleRef != "" || !auth.ModelLogin || len(auth.Accounts) != 1 || auth.Accounts[0].Label != "tester-a" ||
		loaded.policy.HeaderMode != e2proxy.HeaderTestAccountPassthrough || len(loaded.productAccounts) != 1 {
		t.Fatalf("binding without a login method: %+v header=%s accounts=%d", auth, loaded.policy.HeaderMode, len(loaded.productAccounts))
	}
	for _, rule := range loaded.policy.Rules {
		if rule.Purpose == e2proxy.PurposeAuthPrep || rule.Ref == "auth-login" {
			t.Fatalf("auth preparation rule without a login method: %+v", rule)
		}
	}
	if loaded.materials[auth.Accounts[0].Credential.File] == nil {
		t.Fatal("bound credential copy missing")
	}
}

// 로그인 방식이 있는 결속의 요약은 9칸-1 전과 같은 값이다(이미 만든 결속 디렉터리 이름이 그대로 맞는다).
func TestRunAccountsDigestKeepsLoginBindings(t *testing.T) {
	login := diagnosisprep.TargetLogin{Method: "POST", Path: "/login", Format: "form", UsernameField: "email", PasswordField: "password", Cookies: []string{"session"}}
	accounts := []productRunAccount{{Label: "tester-a", CredentialSHA256: strings.Repeat("a", 64)}}
	got, err := runAccountsDigest(productRunAccounts{Login: &login, Accounts: accounts, PasswordResponse: e2proxy.PasswordResponseModelVisible})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(struct {
		Login            diagnosisprep.TargetLogin `json:"login"`
		Accounts         []productRunAccount       `json:"accounts"`
		PasswordResponse string                    `json:"password_response,omitempty"`
	}{login, accounts, e2proxy.PasswordResponseModelVisible})
	if got != urlSHA(before) {
		t.Fatal("binding digest with a login method changed")
	}
	without, err := runAccountsDigest(productRunAccounts{Accounts: accounts, PasswordResponse: e2proxy.PasswordResponseModelVisible})
	if err != nil || without == "" || without == got {
		t.Fatalf("binding digest without a login method: %q %v", without, err)
	}
	if _, err := runAccountsDigest(productRunAccounts{Login: &login}); err == nil {
		t.Fatal("digest without accounts")
	}
}

// externalModelInstall은 설치 틀의 모델을 외부 API(OpenRouter) 모델로 바꾼다(TestURLPreparationOpenRouterInspectProtectedManifest와 같은 판).
func externalModelInstall(f *urlNegativeFixture) {
	f.launch.ModelExecutionMode = "openrouter"
	f.launch.LocalLLMManifestPath = ""
	f.launch.OpenRouterManifestPath = filepath.Join(filepath.Dir(f.path), "external.json")
	f.launch.ModelBaseURL, f.launch.ModelRef = llmpolicy.OpenRouterBaseURL, llmpolicy.OpenRouterModel
	p := &f.config.Profiles[0]
	p.InputTokensPerCall = 262144
	p.Budget.InputTokens = 524288
	p.Budget.CostUSDMicros = 600000
	p.AggregateBudget = p.Budget
	f.write()
	p.ManifestSHA256 = urlNegativeWrite(f.t, f.launch.OpenRouterManifestPath, map[string]any{
		"version": 1, "profile_id": 1, "format": "openai", "base_url": llmpolicy.OpenRouterBaseURL, "model": llmpolicy.OpenRouterModel,
		"provider_slug": "fixture/endpoint", "expected_provider_name": "Fixture", "approval_id": "url-inspect", "expires_at": time.Now().Add(time.Hour),
		"strict_privacy_ack": true, "account_spend_cap_ack": true, "max_requests": 2, "max_input_tokens": 524288, "max_output_tokens": 2048, "max_cost_usd_micros": 600000,
		"max_input_tokens_per_call": 262144, "max_output_tokens_per_call": 1024, "timeout_seconds": 2, "max_concurrent": 1,
		"max_input_price_usd_micros_per_million": 1000000, "max_output_price_usd_micros_per_million": 1000000,
	})
	urlNegativeWrite(f.t, f.path, f.config)
}

// 외부 모델 동의 관문(9칸-1): 로그인 방식(9칸) 없이 자격만 있는 계정도 결속하므로 외부 모델로 가는 자격이 늘 수 있다. 그런
// 계정도 같은 관문을 지난다 — 설치 단위 외부 전송 동의가 없으면 회차를 시작하지 않고(자격이 모델로 가지 않음), 동의가 있으면
// 결속하고 그 동의를 launch로 넘긴다(서버가 외부 모델 회차의 인증 칸을 받음 · 시작 전에 감사 저널에 동의를 남김).
func TestExternalModelAccountWithoutLoginMethodNeedsConsent(t *testing.T) {
	runtime, _, target, input := accountRunFixtureWith(t, externalModelInstall)
	r := runtime.targets
	if _, err := r.registerAccount("ARTEX", target.ID, "tester-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.setAccountCredential("ARTEX", target.ID, "tester-a", diagnosisprep.AccountCredential{Username: "u", Password: "seeded-pass-a"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := runtime.load("ARTEX", input, false, "")
	var failure urlPreparationError
	if err == nil || !errors.As(err, &failure) || failure != "external_transfer_consent_required" {
		if loaded != nil {
			loaded.clearMaterials()
		}
		t.Fatalf("external model run without consent: %v", err)
	}
	if _, err := writeProductExternalConsent(runtime.config.StateDir, productExternalConsentInstall, "ARTEX", time.Now()); err != nil {
		t.Fatal(err)
	}
	loaded, err = runtime.load("ARTEX", input, false, "")
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.clearMaterials()
	if loaded.policy.Auth == nil || loaded.policy.Auth.Login != nil || len(loaded.productAccounts) != 1 || !loaded.externalTestAccounts {
		t.Fatalf("consented external run: auth=%+v accounts=%d external=%v", loaded.policy.Auth, len(loaded.productAccounts), loaded.externalTestAccounts)
	}
}
