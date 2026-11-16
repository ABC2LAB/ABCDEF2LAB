//go:build linux

package main

import (
	"context"
	"encoding/json"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/server"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProductExactPathProtectedApprovalAndRuntime(t *testing.T) {
	f := newURLNegativeFixture(t)
	completeProductTemplateForTest(f)
	f.policy.RunRef = "template-run"
	f.policy.ResetRef = "template-reset"
	f.policy.CaseRef = "template-case"
	f.policy.PolicyRef = "template-policy"
	f.policy.PolicyRevision = "template-revision"
	f.policy.BudgetRef = "template-budget"
	f.policy.AccountRef = "anonymous"
	f.policy.SessionRef = "anonymous-session"
	f.policy.StateDir = f.launch.DataDir
	m := f.config.Profiles[0]
	m.TargetRef = ""
	m.ScopeRef = ""
	m.AllowPrivateTarget = false
	m.Attestation = urlNonmutatingAttestation{}
	f.policy.Rules = f.policy.Rules[:0]
	m.PolicySHA256 = urlNegativeWrite(t, f.launch.HTTP.PolicyFile, f.policy)
	f.config.Profiles = nil
	policyPath := filepath.Join(filepath.Dir(f.path), "targets-policy.json")
	digest := urlNegativeWrite(t, policyPath, productTargetPolicy{Format: "abc2lab.product-target-policy/v1", AllowedPorts: []int{443}, PrivateCIDRs: []string{"10.200.1.0/24"}, MaxRequests: 4, MaxTargets: 32})
	f.config.Product = &urlProductInstallation{Format: "abc2lab.product-runtime/v2", TargetPolicy: urlProtectedPin{File: policyPath, SHA256: digest}, Models: []urlPreparationProfile{m}}
	urlNegativeWrite(t, f.path, f.config)
	cfg, pin, e := readURLInstallation(f.path)
	if e != nil {
		t.Fatal(e)
	}
	runtime, e := newProductRuntime(cfg, pin)
	if e != nil {
		t.Fatal(e)
	}
	defer runtime.targets.Close()
	if profiles, e := runtime.profiles(); e != nil || len(profiles) != 1 {
		t.Fatal("targetless model catalog", e)
	} else if profiles[0].Workflow != nil || profiles[0].WorkflowRequestLimit != 4 {
		// 일반 HTTP 모드 프로필에도 설치의 회차당 요청 상한(대상 정책 max_requests)을 싣는다. 화면은 이 값으로 등록한다.
		t.Fatalf("general profile request limit %d", profiles[0].WorkflowRequestLimit)
	}
	r := runtime.targets
	txt := map[string][]string{}
	r.lookupTXT = func(_ context.Context, name string) ([]string, error) { return txt[name], nil }
	r.lookupIP = func(_ context.Context, name string) ([]netip.Addr, error) {
		ip := "10.200.1.10"
		if strings.HasPrefix(name, "b.") {
			ip = "10.200.1.20"
		}
		return []netip.Addr{netip.MustParseAddr(ip)}, nil
	}
	r.probe = func(context.Context, string, string, int) (string, time.Time, error) {
		return strings.Repeat("a", 64), time.Now().Add(time.Hour), nil
	}
	approve := func(host string) diagnosisprep.ProductTarget {
		spec := targetTestSpec(host)
		spec.Requests = m.Budget.Requests
		spec.URL = "https://" + host + "/"
		spec.PathPrefixes = []string{"=/", "/users/v1"}
		v, e := r.register("ARTEX", spec)
		if e != nil {
			t.Fatal(e)
		}
		txt[v.ChallengeName] = []string{v.ChallengeValue}
		v, e = r.approve(t.Context(), "ARTEX", v.ID)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}

	a := approve("a.example.test")
	if !containsExactExpression(a.PathPrefixes) {
		t.Fatal("영속 승인에서 정확 일치 표현 누락")
	}
	input := diagnosisprep.Input{ProfileID: m.ID, TargetURL: "https://a.example.test/"}
	loaded, err := runtime.load("ARTEX", input, false, "")
	if err != nil {
		t.Fatal("정확 루트의 새 진단 준비 실패", err)
	}
	defer loaded.clearMaterials()
	if len(loaded.policy.Rules) != 2 || loaded.policy.Rules[1].PathMatch != "exact" || loaded.policy.Rules[1].PathPrefix != "/" || !containsExactExpression(loaded.inspection.Scope.PathPrefixes) {
		t.Fatal("승인·정책·준비 범위 의미 불일치")
	}
	raw, err := os.ReadFile(loaded.launch.HTTP.PolicyFile)
	if err != nil {
		t.Fatal(err)
	}
	_, fingerprint, err := server.ValidateE2HTTPRunPolicyDocument(raw)
	if err != nil || fingerprint == "" {
		t.Fatal("실제 실행 정책 검증 실패", err)
	}
	for _, url := range []string{"https://a.example.test/createdb", "https://a.example.test/users/v10", "https://other.example.test/"} {
		if _, err := runtime.load("ARTEX", diagnosisprep.Input{ProfileID: m.ID, TargetURL: url}, false, ""); err == nil {
			t.Fatal("범위 밖 진단 시작 준비 허용")
		}
	}
	// 보호 저널을 다시 읽어도 같은 정확 범위를 쓴다.
	root, policy := filepath.Dir(r.accountDir), r.policy
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newProductTargetRegistry(root, policy, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err = reopened.lookup("ARTEX", input.TargetURL, ""); err != nil {
		t.Fatal("재시작 뒤 정확 루트 소실", err)
	}
	if _, err = reopened.lookup("ARTEX", "https://a.example.test/createdb", ""); err == nil {
		t.Fatal("재시작 뒤 정확 범위가 접두로 확대")
	}
	// 모드 한 칸만 바꾼 정책은 내용 지문이 다르고, 기존 실행 결속 파일로 수용하지 않는다.
	var altered map[string]any
	if json.Unmarshal(raw, &altered) != nil {
		t.Fatal("합성 정책 JSON")
	}
	rules := altered["rules"].([]any)
	delete(rules[1].(map[string]any), "path_match")
	changed, _ := json.Marshal(altered)
	_, prefixFingerprint, err := server.ValidateE2HTTPRunPolicyDocument(changed)
	if err != nil || prefixFingerprint == fingerprint {
		t.Fatal("접두·정확 정책 지문이 같음", err)
	}
	for _, value := range []any{nil, "", false, "prefix", "EXACT"} {
		rules[1].(map[string]any)["path_match"] = value
		invalid, _ := json.Marshal(altered)
		if _, _, err := server.ValidateE2HTTPRunPolicyDocument(invalid); err == nil {
			t.Fatal("알 수 없거나 null인 모드를 접두 기본값으로 수용")
		}
	}
	f.assertNoConnections()
}

func containsExactExpression(expressions []string) bool {
	for _, expression := range expressions {
		if expression == "=/" {
			return true
		}
	}
	return false
}
