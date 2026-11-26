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
	"github.com/Autumn-27/artex/server"
)

// 실행 결속(C14 구현 설계 5): 지금 test이고 자격·로그인 방식이 있는 계정만 골라 실행 정책에 인증 준비 칸·규칙을 넣고
// 자격 사본을 결속 디렉터리에 고정한다. 결속한 계정이 해제되면 그 실행의 확인이 실패하고(새 송신 정지) 사본을 지운다.
// accountRunFixture는 실제 제품 설치·대상 등록·승인까지 마친 제품 실행 환경이다(DNS·TLS 확인은 가짜).
func accountRunFixture(t *testing.T) (*productRuntime, urlPreparationInstallation, diagnosisprep.ProductTarget, diagnosisprep.Input) {
	t.Helper()
	f := newURLNegativeFixture(t)
	completeProductTemplateForTest(f)
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
	if auth == nil || !auth.ModelLogin || auth.PasswordResponse != e2proxy.PasswordResponseModelVisible || len(auth.Accounts) != 2 || auth.Accounts[0].Label != "real-b" || auth.Accounts[1].Label != "tester-a" || auth.Login.Path != "/read/login" || loaded.policy.HeaderMode != e2proxy.HeaderTestAccountPassthrough {
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
	// 옛 시험 표시 API는 결속에 쓰지 않는다(화면 정리 전까지 받기만 한다). 표시해도 결속은 그대로다.
	if _, err := r.markTestAccount("ARTEX", target.ID, "real-b", true); err != nil {
		t.Fatal(err)
	}
	if loaded.current() != nil {
		t.Fatal("marking an account stopped the run")
	}
	if _, err := r.markTestAccount("ARTEX", target.ID, "tester-a", true); err != nil {
		t.Fatal(err)
	}
	// 표시를 떼면(옛 API) 그 계정 자격을 지운다. 결속한 실행은 확인이 실패한다(새 송신 정지, 더 안전한 쪽). 결속 디렉터리와
	// 실행 디렉터리의 사본도 지운다(다른 계정 사본은 둔다).
	runCopy := filepath.Join(cfg.RunsDir, "job-unmark", bindingCredentialName(target.ID, "tester-a", strings.Repeat("c", 64)))
	otherCopy := filepath.Join(cfg.RunsDir, "job-unmark", bindingCredentialName(target.ID, "real-b", strings.Repeat("d", 64)))
	if os.MkdirAll(filepath.Dir(runCopy), 0o700) != nil || os.WriteFile(runCopy, []byte("{}"), 0o600) != nil || os.WriteFile(otherCopy, []byte("{}"), 0o600) != nil {
		t.Fatal("run copy fixture")
	}
	endpoint := productPreparationEndpoint{product: runtime}
	req := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "product-control", Principal: "ARTEX", Command: diagnosisprep.ProductControl,
		Product: &diagnosisprep.ProductRequest{Operation: "account_unmark", TargetID: target.ID, Account: &diagnosisprep.AccountRequest{Label: "tester-a"}}}
	if resp, err := endpoint.Call(t.Context(), req); err != nil || resp.Product == nil || resp.Product.Reason != "" {
		t.Fatalf("unmark: %v %+v", err, resp)
	}
	if err := loaded.current(); !errors.Is(err, errAccountBindingChanged) {
		t.Fatalf("unmarked bound account did not stop the run: %v", err)
	}
	if _, err := os.Stat(runCopy); !os.IsNotExist(err) {
		t.Fatal("run directory credential copy left after unmark")
	}
	if _, err := os.Stat(otherCopy); err != nil {
		t.Fatal("another account's run copy removed")
	}
	if copies, _ := filepath.Glob(filepath.Join(cfg.StateDir, "bindings", "*", "account-"+target.ID+".tester-a.*")); len(copies) != 0 {
		t.Fatalf("credential copy left after unmark: %v", copies)
	}
	// 새 실행은 자격이 남은 real-b로 결속하고, 대상을 철회하면 그 대상의 사본을 모두 지운다.
	next, err := runtime.load("ARTEX", input, false, "")
	if err != nil {
		t.Fatal(err)
	}
	next.clearMaterials()
	if next.policy.Auth == nil || len(next.policy.Auth.Accounts) != 1 || next.policy.Auth.Accounts[0].Label != "real-b" {
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
	policy := server.E2HTTPFilePolicy{Auth: &server.E2HTTPFileAuth{RuleRef: "auth-login", Login: diagnosisprep.TargetLogin{Path: "/read/login"}}}
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

// 실행 고르기: 로그인 방식이 없거나 자격이 없는 계정은 고르지 않는다. 시험 표시는 보지 않는다(분류 없음, 2026-11-26).
// 자격을 바꾸면 요약이 바뀌고 결속한 실행은 멈춘다.
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
		if err != nil || !account.CredentialConfigured || !account.LoginMethodMissing {
			t.Fatal("자격 등록 응답에서 로그인 방식 미설정 상태 누락", err)
		}
	}
	for _, label := range []string{"tester-a", "tester-b"} {
		account, err := r.markTestAccount("ARTEX", target.ID, label, true)
		if err != nil || account.LoginMethodMissing != (label == "tester-a") {
			t.Fatal("시험 계정 표시 후 자격과 로그인 설정 상태 불일치", err)
		}
	}
	if got, digest, err := r.runAccounts(rec); err != nil || digest != "" || len(got.Accounts) != 0 {
		t.Fatalf("selection without a login method: %+v %q %v", got, digest, err)
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
	if err != nil || digest == "" || len(got.Accounts) != 2 || got.Accounts[0].Label != "real-c" || got.Accounts[1].Label != "tester-a" {
		t.Fatalf("selection: %+v %v", got, err)
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
}

// 저장 보고서는 현재 송신 권한·계정 선택과 상관없이 그 실행의 결속을 다시 찾는다: 계정 없는 실행은 대상 철회 뒤에도,
// 계정을 결속한 실행은 해제·철회 뒤에도 작업의 원본 요약으로 같은 결속을 연다(#520 리뷰).
func TestProductReportLoadsBindingAfterRevokeAndUnmark(t *testing.T) {
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
	if _, err := r.markTestAccount("ARTEX", target.ID, "tester-a", true); err != nil {
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
	unmark := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "product-control", Principal: "ARTEX", Command: diagnosisprep.ProductControl,
		Product: &diagnosisprep.ProductRequest{Operation: "account_unmark", TargetID: target.ID, Account: &diagnosisprep.AccountRequest{Label: "tester-a"}}}
	if resp, err := endpoint.Call(t.Context(), unmark); err != nil || resp.Product == nil || resp.Product.Reason != "" {
		t.Fatalf("unmark: %v %+v", err, resp)
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
