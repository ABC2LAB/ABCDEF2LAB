package e2export

import (
	"bytes"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/transmit"
)

func policyFixture() (e2proxy.Config, PolicyBehavior) {
	return e2proxy.Config{ApprovalMode: e2proxy.ApprovalPreauthorizedScope, Scope: transmit.Scope{RunRef: "run-fixture"}, HeaderMode: e2proxy.HeaderPreserve, Rules: []e2proxy.Rule{{Ref: "rule-first", PathScopeRef: "scope-first", Origin: "https://example.invalid", ConnectIP: netip.MustParseAddr("192.0.2.1"), PathPrefix: "/read", Methods: []string{"GET", "HEAD"}, Headers: http.Header{"X-Mode": []string{"read-only"}}}, {Ref: "rule-second", PathScopeRef: "scope-second", Origin: "https://example.invalid:443", ConnectIP: netip.MustParseAddr("192.0.2.2"), PathPrefix: "/read/private", Methods: []string{"GET"}}}, Limits: e2proxy.Limits{MaxAttempts: 20, MaxCalls: 10, MaxConcurrent: 2, MaxBodyBytes: 4096, RequestTimeout: time.Second, CallTimeout: time.Minute, MaxDenials: 3, MaxTransient: 3, MaxUnknown: 2}}, PolicyBehavior{MaxWorkerTurns: 5, MaxToolCalls: 10, WorkerTimeout: time.Minute, MaxPlannerRounds: 2, MaxIntentRuns: 1, DrainTimeout: time.Second, OutputLimit: 4096, ProxyHeaderTimeout: time.Second, ProxyRequestTimeout: time.Second, ProxyMaxHeaderBytes: 8192, ProxyMaxConnections: 4}
}
func policyDigest(t *testing.T, c e2proxy.Config, b PolicyBehavior) string {
	t.Helper()
	s, err := PolicyFingerprint(c, 20, b)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPolicyFingerprintCanonicalAndRunIndependent(t *testing.T) {
	c, b := policyFixture()
	original := policyDigest(t, c, b)
	c.Scope = transmit.Scope{RunRef: "other-run", TaskRef: "123", ResetRef: "reset-other", PolicyRef: "policy-other", CaseRef: "case-other"}
	c.PolicyRevision = "other-revision"
	c.OwnerRef = "other-owner"
	c.AccountRef = "other-account"
	c.SessionRef = "other-session"
	c.ExpiresAt = time.Now().Add(time.Hour)
	c.IntentID = 177
	c.Rules[0].Ref = "changed-ref"
	c.Rules[0].PathScopeRef = "changed-path-ref"
	c.Rules[0].ConnectIP = netip.MustParseAddr("192.0.2.44")
	c.Rules[0].Origin = "https://EXAMPLE.invalid:0443/"
	c.Rules[0].Headers = http.Header{"x-mode": []string{"read-only"}}
	if got := policyDigest(t, c, b); got != original {
		t.Fatal("회차 참조나 동등한 origin/헤더 표기로 내용 지문이 바뀜")
	}
	data, err := canonicalPolicy(c, 20, b)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"other-run", "other-revision", "other-owner", "192.0.2.44", "changed-ref"} {
		if bytes.Contains(data, []byte(private)) {
			t.Fatal("회차 값이 canonical 입력에 섞임")
		}
	}
	if bytes.Contains(data, []byte("\n")) {
		t.Fatal("canonical 개행")
	}
}
func TestPolicyFingerprintChangesEffectiveMeaning(t *testing.T) {
	baseline, b := policyFixture()
	expected := policyDigest(t, baseline, b)
	cases := map[string]func(*e2proxy.Config, *PolicyBehavior){
		"approval_mode":   func(c *e2proxy.Config, _ *PolicyBehavior) { c.ApprovalMode = e2proxy.ApprovalConfirmEveryRequest },
		"rule_order":      func(c *e2proxy.Config, _ *PolicyBehavior) { c.Rules[0], c.Rules[1] = c.Rules[1], c.Rules[0] },
		"path":            func(c *e2proxy.Config, _ *PolicyBehavior) { c.Rules[0].PathPrefix = "/different" },
		"method":          func(c *e2proxy.Config, _ *PolicyBehavior) { c.Rules[0].Methods = []string{"GET"} },
		"header":          func(c *e2proxy.Config, _ *PolicyBehavior) { c.Rules[0].Headers.Set("X-Mode", "different") },
		"origin":          func(c *e2proxy.Config, _ *PolicyBehavior) { c.Rules[0].Origin = "http://example.invalid" },
		"attempts":        func(c *e2proxy.Config, _ *PolicyBehavior) { c.Limits.MaxAttempts++ },
		"calls":           func(c *e2proxy.Config, _ *PolicyBehavior) { c.Limits.MaxCalls++ },
		"concurrent":      func(c *e2proxy.Config, _ *PolicyBehavior) { c.Limits.MaxConcurrent++ },
		"body":            func(c *e2proxy.Config, _ *PolicyBehavior) { c.Limits.MaxBodyBytes++ },
		"request_timeout": func(c *e2proxy.Config, _ *PolicyBehavior) { c.Limits.RequestTimeout++ },
		"call_timeout":    func(c *e2proxy.Config, _ *PolicyBehavior) { c.Limits.CallTimeout++ },
		"denials":         func(c *e2proxy.Config, _ *PolicyBehavior) { c.Limits.MaxDenials++ },
		"transient":       func(c *e2proxy.Config, _ *PolicyBehavior) { c.Limits.MaxTransient++ },
		"unknown":         func(c *e2proxy.Config, _ *PolicyBehavior) { c.Limits.MaxUnknown++ },
		"runtime":         func(_ *e2proxy.Config, b *PolicyBehavior) { b.MaxToolCalls++ },
		"proxy":           func(_ *e2proxy.Config, b *PolicyBehavior) { b.ProxyMaxConnections++ },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			c, b := policyFixture()
			change(&c, &b)
			if policyDigest(t, c, b) == expected {
				t.Fatal("유효 정책 변경이 지문에 반영되지 않음")
			}
		})
	}
	c, b := policyFixture()
	got, err := PolicyFingerprint(c, 21, b)
	if err != nil || got == expected {
		t.Fatal("승인 요청 상한 변경이 지문에 반영되지 않음")
	}
}
func TestPolicyFingerprintCredentialValuesExcluded(t *testing.T) {
	c, b := policyFixture()
	c.HeaderMode = e2proxy.HeaderProtectedSessionProposal
	c.Rules[0].CredentialHeaders = http.Header{"X-Private-Token": []string{"synthetic-secret-one"}}
	before := policyDigest(t, c, b)
	c.Rules[0].CredentialHeaders.Set("X-Private-Token", "synthetic-secret-two")
	if policyDigest(t, c, b) != before {
		t.Fatal("자격 원문이 내용 지문에 포함됨")
	}
	raw, err := canonicalPolicy(c, 20, b)
	if err != nil || strings.Contains(string(raw), "synthetic-secret") {
		t.Fatal("자격 원문이 canonical에 포함됨")
	}
	c.Rules[0].CredentialHeaders = http.Header{"X-Other-Token": []string{"synthetic-secret-two"}}
	if policyDigest(t, c, b) == before {
		t.Fatal("자격 헤더 이름 변경 누락")
	}
}
func TestPolicyFingerprintRejectsInvalid(t *testing.T) {
	c, b := policyFixture()
	c.Rules[0].ConnectIP = netip.Addr{}
	if _, err := PolicyFingerprint(c, 20, b); err == nil {
		t.Fatal("미확정 IP 결속을 수용함")
	}
	c, b = policyFixture()
	b.DrainTimeout = 0
	if _, err := PolicyFingerprint(c, 20, b); err == nil {
		t.Fatal("미정 종료 상한을 수용함")
	}
}

func TestPolicyFingerprintEvaluationModeAndRunBinding(t *testing.T) {
	c, b := policyFixture()
	preserve := policyDigest(t, c, b)
	c.HeaderMode = e2proxy.HeaderEvaluationCallerPassthrough
	c.Evaluation = &e2proxy.EvaluationCallerContext{RunRef: c.Scope.RunRef, LLMBackend: "local", TargetKind: "public_practice"}
	evaluation := policyDigest(t, c, b)
	if evaluation == preserve {
		t.Fatal("전달 모드 변경 누락")
	}
	c.Scope.RunRef = "new-run"
	c.Evaluation.RunRef = "new-run"
	if policyDigest(t, c, b) != evaluation {
		t.Fatal("회차별 evaluation run ref가 지문에 포함됨")
	}
	c.Evaluation.LLMBackend = "api"
	if policyDigest(t, c, b) == evaluation {
		t.Fatal("모델 backend 의미 변경 누락")
	}
	c.Evaluation.RunRef = "mismatch"
	if _, err := PolicyFingerprint(c, 20, b); err == nil {
		t.Fatal("실제 run 결속 불일치 수용")
	}
}

func TestPolicyFingerprintGoldenVector(t *testing.T) {
	c, b := policyFixture()
	first := policyDigest(t, c, b)
	second := policyDigest(t, c, b)
	if first != second {
		t.Fatal("same policy changed fingerprint")
	}
	const expected = "e9f33de9a7d59ca273a97abe3e9e33c20beef552b6b4e3687acc7a53f35d6f0c"
	if first != expected {
		t.Fatal("synthetic policy content encoder changed")
	}
}

func TestPolicyFingerprintIncludesTLSApprovalModeWithoutRunKey(t *testing.T) {
	c, b := policyFixture()
	without := policyDigest(t, c, b)
	c.Rules[0].TLSSPKISHA256 = strings.Repeat("a", 64)
	pinned := policyDigest(t, c, b)
	if pinned == without {
		t.Fatal("TLS approval enforcement missing from meaning")
	}
	c.Rules[0].TLSSPKISHA256 = strings.Repeat("b", 64)
	if policyDigest(t, c, b) != pinned {
		t.Fatal("per-approval TLS key leaked into meaning")
	}
}
