//go:build linux

package main

import (
	"context"
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
func TestProductRunBindsTestAccountsAndStopsOnUnmark(t *testing.T) {
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
	defer runtime.targets.Close()
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
	input := diagnosisprep.Input{TargetURL: "https://owned.example.test/read", ProfileID: m.ID, DisplayName: "Owned account run"}
	// 계정이 없으면 인증 준비 칸이 없다.
	plain, err := runtime.load("ARTEX", input, false, "")
	if err != nil {
		t.Fatal(err)
	}
	plain.clearMaterials()
	if plain.policy.Auth != nil || len(plain.productAccounts) != 0 {
		t.Fatal("run without test accounts got an auth section")
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
	if _, err := r.markTestAccount("ARTEX", target.ID, "tester-a", true); err != nil {
		t.Fatal(err)
	}
	loaded, err := runtime.load("ARTEX", input, false, "")
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.clearMaterials()
	auth := loaded.policy.Auth
	if auth == nil || len(auth.Accounts) != 1 || auth.Accounts[0].Label != "tester-a" || auth.Login.Path != "/read/login" {
		t.Fatalf("auth section: %+v", auth)
	}
	var rule bool
	for _, x := range loaded.policy.Rules {
		rule = rule || x.Ref == auth.RuleRef && x.Purpose == e2proxy.PurposeAuthPrep && x.PathPrefix == "/read/login" && len(x.Methods) == 1 && x.Methods[0] == "POST"
	}
	if !rule {
		t.Fatalf("auth rule missing: %+v", loaded.policy.Rules)
	}
	copies, _ := filepath.Glob(filepath.Join(cfg.StateDir, "bindings", "*", "account-"+target.ID+".tester-a.*"))
	if len(copies) != 1 {
		t.Fatalf("credential copy in binding: %v", copies)
	}
	if raw, err := os.ReadFile(copies[0]); err != nil || urlSHA(raw) != auth.Accounts[0].Credential.SHA256 {
		t.Fatal("credential copy does not match its pin")
	}
	if others, _ := filepath.Glob(filepath.Join(cfg.StateDir, "bindings", "*", "account-"+target.ID+".real-b.*")); len(others) != 0 {
		t.Fatal("real account credential copied into the run binding")
	}
	if loaded.current() != nil {
		t.Fatal("fresh binding failed its check")
	}
	// 결속 뒤 다른 계정을 새로 표시해도 이 실행은 계속된다.
	if _, err := r.markTestAccount("ARTEX", target.ID, "real-b", true); err != nil {
		t.Fatal(err)
	}
	if loaded.current() != nil {
		t.Fatal("marking an unrelated account stopped the run")
	}
	// 결속한 계정을 해제하면 확인이 실패한다(새 송신 정지). 결속 디렉터리의 사본도 지운다.
	endpoint := productPreparationEndpoint{product: runtime}
	req := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "product-control", Principal: "ARTEX", Command: diagnosisprep.ProductControl,
		Product: &diagnosisprep.ProductRequest{Operation: "account_unmark", TargetID: target.ID, Account: &diagnosisprep.AccountRequest{Label: "tester-a"}}}
	if resp, err := endpoint.Call(t.Context(), req); err != nil || resp.Product == nil || resp.Product.Reason != "" {
		t.Fatalf("unmark: %v %+v", err, resp)
	}
	if err := loaded.current(); !errors.Is(err, errAccountBindingChanged) {
		t.Fatalf("unmarked bound account did not stop the run: %v", err)
	}
	if copies, _ := filepath.Glob(filepath.Join(cfg.StateDir, "bindings", "*", "account-"+target.ID+".tester-a.*")); len(copies) != 0 {
		t.Fatalf("credential copy left after unmark: %v", copies)
	}
	// 새 실행은 지금 test인 real-b로 결속하고, 대상을 철회하면 그 대상의 사본을 모두 지운다.
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

// 실행 고르기: 로그인 방식이 없거나, real이거나, 자격이 없는 계정은 고르지 않는다. 자격을 바꾸면 요약이 바뀌고 결속한
// 실행은 멈춘다.
func TestProductRunAccountsSelection(t *testing.T) {
	r, _, target := accountTestTarget(t, true)
	rec := r.approvals[target.ID]
	for _, label := range []string{"tester-a", "tester-b", "real-c"} {
		if _, err := r.registerAccount("ARTEX", target.ID, label); err != nil {
			t.Fatal(err)
		}
	}
	for _, label := range []string{"tester-a", "real-c"} {
		if _, err := r.setAccountCredential("ARTEX", target.ID, label, diagnosisprep.AccountCredential{Username: "u", Password: "p-" + label}); err != nil {
			t.Fatal(err)
		}
	}
	for _, label := range []string{"tester-a", "tester-b"} {
		if _, err := r.markTestAccount("ARTEX", target.ID, label, true); err != nil {
			t.Fatal(err)
		}
	}
	if got, digest, err := r.runAccounts(rec); err != nil || digest != "" || len(got.Accounts) != 0 {
		t.Fatalf("selection without a login method: %+v %q %v", got, digest, err)
	}
	if _, err := r.setTargetLogin("ARTEX", target.ID, cookieLogin()); err != nil {
		t.Fatal(err)
	}
	got, digest, err := r.runAccounts(rec)
	if err != nil || digest == "" || len(got.Accounts) != 1 || got.Accounts[0].Label != "tester-a" {
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
