//go:build linux

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/e2proxy"
)

func TestE2HTTPFileRulePurposeFailsClosed(t *testing.T) {
	host, _, policy := e2HostPolicyFixture(t)
	ordinary, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	program := policy.Rules[0]
	program.Ref, program.PathScopeRef, program.PathPrefix = "auth-only", "auth-only-path", "/login"
	program.Methods, program.Purpose = []string{"POST"}, e2proxy.PurposeAuthPrep
	policy.Rules = append(policy.Rules, program)
	withProgram, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	purpose := []byte(`"purpose":"auth_prep"`)
	if bytes.Count(withProgram, purpose) != 1 {
		t.Fatal("합성 정책에 인증 준비 목적이 한 번 있어야 합니다")
	}
	replace := func(field string) []byte {
		return bytes.Replace(withProgram, purpose, []byte(field), 1)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(ordinary, &doc); err != nil {
		t.Fatal(err)
	}
	rulesAlias := append(bytes.Clone(withProgram[:len(withProgram)-1]), []byte(`,"Rules":`)...)
	rulesAlias = append(rulesAlias, doc["rules"]...)
	rulesAlias = append(rulesAlias, '}')
	for _, tc := range []struct {
		name string
		raw  []byte
		want bool
	}{
		{"ordinary-omits-purpose", ordinary, true},
		{"canonical-auth-prep", withProgram, true},
		{"null-purpose", replace(`"purpose":null`), false},
		{"empty-purpose", replace(`"purpose":""`), false},
		{"unknown-purpose", replace(`"purpose":"unsupported-program"`), false},
		{"non-string-purpose", replace(`"purpose":true`), false},
		{"case-alias", replace(`"Purpose":"auth_prep"`), false},
		{"purpose-alias-override", replace(`"purpose":"auth_prep","Purpose":""`), false},
		{"duplicate-purpose", replace(`"purpose":"auth_prep","purpose":""`), false},
		{"rules-alias-override", rulesAlias, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			options, runtimeErr := loadE2HTTPRuntimeOptions(ctx, E2HTTPFileOptions{
				PolicyFile: host.PolicyFile, Authority: &e2HTTPPolicyAuthorityFixture{ctx: ctx},
				Supervisor: &e2HTTPSupervisorFixture{},
			}, tc.raw, nil)
			_, fingerprint, documentErr := ValidateE2HTTPRunPolicyDocument(tc.raw)
			if tc.want {
				if runtimeErr != nil || documentErr != nil || fingerprint == "" || fingerprint != options.PolicyFingerprintSHA256 {
					t.Fatalf("합성 정책의 로더·문서 검증 결과가 다릅니다: %v / %v", runtimeErr, documentErr)
				}
				return
			}
			if !errors.Is(runtimeErr, ErrE2HTTPHost) || options.Acquire != nil || options.PolicyFingerprintSHA256 != "" {
				t.Error("잘못된 목적 선언이 런타임 정책으로 로드됐습니다")
			}
			if !errors.Is(documentErr, ErrE2HTTPHost) || fingerprint != "" {
				t.Error("잘못된 목적 선언에 유효한 정책 지문을 반환했습니다")
			}
		})
	}
}

func TestE2HTTPFileE3RuleCannotExtendWorkerApproval(t *testing.T) {
	host, _, policy := e2HostPolicyFixture(t)
	program := policy.Rules[0]
	program.Ref, program.PathScopeRef = "e3-read", "e3-read-path"
	program.Purpose, program.PathPrefix = e2proxy.PurposeE3Read, "/items/{id}"
	program.Origin = "https://EXAMPLE.invalid:443"
	for _, tc := range []struct {
		name, ip string
		want     bool
	}{
		{"same_approval", policy.Rules[0].ConnectIP, true},
		{"different_ip", "127.0.0.2", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			program.ConnectIP = tc.ip
			policy.Rules = append(policy.Rules[:1], program)
			raw, err := json.Marshal(policy)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			options, runtimeErr := loadE2HTTPRuntimeOptions(ctx, E2HTTPFileOptions{
				PolicyFile: host.PolicyFile, Authority: &e2HTTPPolicyAuthorityFixture{ctx: ctx},
				Supervisor: &e2HTTPSupervisorFixture{},
			}, raw, nil)
			_, fingerprint, documentErr := ValidateE2HTTPRunPolicyDocument(raw)
			if !tc.want {
				if !errors.Is(runtimeErr, ErrE2HTTPHost) || options.Acquire != nil || !errors.Is(documentErr, ErrE2HTTPHost) || fingerprint != "" {
					t.Fatalf("policy accepted an unapproved program IP: %v / %v", runtimeErr, documentErr)
				}
				return
			}
			if runtimeErr != nil || documentErr != nil || fingerprint == "" || fingerprint != options.PolicyFingerprintSHA256 {
				t.Fatalf("bound program rule failed preflight: %v / %v", runtimeErr, documentErr)
			}
			if len(options.ApprovedOrigins) != 1 || options.ApprovedOrigins[0] != policy.Rules[0].Origin {
				t.Fatalf("program origin entered Worker asset approvals: %v", options.ApprovedOrigins)
			}
		})
	}
}

// 제품 실행 정책 검증기는 실행 factory가 받는 진단 워크플로 칸·승인 방식을 받고, 평가 단독 E② 서비스 검증기는
// 지금처럼 거부한다. 워크플로 칸은 닫힌 형식만 받는다.
func TestE2HTTPRunPolicyDocumentAcceptsWorkflowRuns(t *testing.T) {
	_, _, base := e2HostPolicyFixture(t)
	encode := func(change func(*E2HTTPFilePolicy)) []byte {
		p := base
		change(&p)
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	workflow := func(p *E2HTTPFilePolicy) {
		p.Workflow = &E2HTTPWorkflowPolicy{Format: diagnosisflow.Version, Mode: "ai_auto_v1", InputSHA256: strings.Repeat("a", 64)}
	}
	plain := encode(func(*E2HTTPFilePolicy) {})
	_, plainFP, err := ValidateE2HTTPRunPolicyDocument(plain)
	if err != nil {
		t.Fatal("plain prepared policy rejected:", err)
	}
	if want, err := E2HTTPPolicyContentFingerprint(plain, true); err != nil || want != plainFP {
		t.Fatal("run validator fingerprint differs from the evaluation fingerprint", err)
	}
	for name, change := range map[string]func(*E2HTTPFilePolicy){
		"ai_auto":   workflow,
		"workbench": func(p *E2HTTPFilePolicy) { workflow(p); p.ApprovalMode = e2proxy.ApprovalWorkbenchV1 },
		"confirm":   func(p *E2HTTPFilePolicy) { p.ApprovalMode = e2proxy.ApprovalConfirmEveryRequest },
	} {
		raw := encode(change)
		if _, _, err := ValidateE2HTTPRunPolicyDocument(raw); err != nil {
			t.Fatalf("%s: run policy rejected: %v", name, err)
		}
		if _, err := ValidateE2HTTPPolicyDocument(raw, true); err == nil {
			t.Fatalf("%s: evaluation validator accepted a product run policy", name)
		}
	}
	for name, change := range map[string]func(*E2HTTPFilePolicy){
		"format":   func(p *E2HTTPFilePolicy) { workflow(p); p.Workflow.Format = "other/v1" },
		"mode":     func(p *E2HTTPFilePolicy) { workflow(p); p.Workflow.Mode = "" },
		"hash":     func(p *E2HTTPFilePolicy) { workflow(p); p.Workflow.InputSHA256 = "short" },
		"approval": func(p *E2HTTPFilePolicy) { p.ApprovalMode = "unknown_mode" },
	} {
		if _, _, err := ValidateE2HTTPRunPolicyDocument(encode(change)); err == nil {
			t.Fatalf("%s: malformed run policy accepted", name)
		}
	}
	// 워크플로 칸에 모르는 키가 더 있으면 거부한다(닫힌 형식).
	var doc map[string]any
	_ = json.Unmarshal(encode(workflow), &doc)
	doc["workflow"].(map[string]any)["extra"] = "x"
	extra, _ := json.Marshal(doc)
	if _, _, err := ValidateE2HTTPRunPolicyDocument(extra); err == nil {
		t.Fatal("open workflow block accepted")
	}
}

// 워크플로 칸의 운영자 제외 경로(#627)는 선택 칸이다. 쓰면 비지 않은 유효 패턴 목록이어야 하고(null·빈 배열·잘못된
// 패턴·33개 이상 거부), 지문에 들어간다. 제외 경로 없는 정책의 지문은 그대로다.
func TestE2HTTPRunPolicyDocumentExcludedPaths(t *testing.T) {
	_, _, base := e2HostPolicyFixture(t)
	encode := func(excluded []string) []byte {
		p := base
		p.Workflow = &E2HTTPWorkflowPolicy{Format: diagnosisflow.Version, Mode: "ai_auto_v1", InputSHA256: strings.Repeat("a", 64), ExcludedPaths: excluded}
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	plain := encode(nil)
	if strings.Contains(string(plain), "excluded_paths") {
		t.Fatal("제외 경로 없는 정책에 칸이 생김(바이트·지문이 바뀜)")
	}
	_, plainFP, err := ValidateE2HTTPRunPolicyDocument(plain)
	if err != nil {
		t.Fatal(err)
	}
	withPolicy, withFP, err := ValidateE2HTTPRunPolicyDocument(encode([]string{"/read/über", "/read/*/secret"}))
	if err != nil {
		t.Fatal("valid exclusions rejected:", err)
	}
	if withFP == plainFP || len(withPolicy.excludedPaths()) != 2 {
		t.Fatal("제외 경로가 지문·정책에 실리지 않음")
	}
	if _, otherFP, err := ValidateE2HTTPRunPolicyDocument(encode([]string{"/read/über"})); err != nil || otherFP == withFP {
		t.Fatal("다른 제외 목록이 같은 지문", err)
	}
	many := make([]string, 33)
	for i := range many {
		many[i] = "/read/x"
	}
	for name, excluded := range map[string][]string{"pattern": {"/read/a*"}, "encoded": {"/read/%C3%BC"}, "dots": {"/read/../x"}, "many": many} {
		if _, _, err := ValidateE2HTTPRunPolicyDocument(encode(excluded)); err == nil {
			t.Fatalf("%s: invalid exclusions accepted", name)
		}
	}
	var doc map[string]any
	_ = json.Unmarshal(encode([]string{"/read/x"}), &doc)
	for _, empty := range []any{nil, []string{}} {
		doc["workflow"].(map[string]any)["excluded_paths"] = empty
		raw, _ := json.Marshal(doc)
		if _, _, err := ValidateE2HTTPRunPolicyDocument(raw); err == nil {
			t.Fatalf("%v exclusions accepted as no exclusions", empty)
		}
	}
}

// 선언·쓰기 동의가 있는 제품 회차의 실행 정책(e3_write_consent·e3_resettable)을 실행 정책 문서 검증이 받는다. factory가
// 받는 정책을 이 검증이 거부하면 확정 몫 읽기가 실패해 회차가 시작부터 gateway_unavailable이었다(합성 A/B 회차, 11/20).
// 지문은 factory와 같게 두 칸을 넣지 않는다. 확정 몫 없이 두 칸만 있거나 평가 단독 정책에 있으면 거부한다.
func TestE2HTTPRunPolicyDocumentAcceptsE3WriteConsent(t *testing.T) {
	_, _, base := e2HostPolicyFixture(t)
	encode := func(change func(*E2HTTPFilePolicy)) []byte {
		p := base
		change(&p)
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	withShare := func(p *E2HTTPFilePolicy) {
		requests, timeout := uint64(E3ConfirmationIdentityRequests+E3ConfirmationRequestsPerType), int64(1000)
		p.Limits.E3ConfirmationRequests, p.Limits.E3ConfirmationTimeoutMS = &requests, &timeout
		// 몫은 실행 요청 상한의 1/4 이하여야 한다.
		p.MaxRequests = max(p.MaxRequests, requests*E3ConfirmationShareDivisor)
	}
	_, shareFP, err := ValidateE2HTTPRunPolicyDocument(encode(withShare))
	if err != nil {
		t.Fatal("확정 몫만 있는 정책", err)
	}
	_, consentFP, err := ValidateE2HTTPRunPolicyDocument(encode(func(p *E2HTTPFilePolicy) { withShare(p); p.E3WriteConsent, p.E3Resettable = true, true }))
	if err != nil || consentFP != shareFP {
		t.Fatalf("쓰기 동의 정책: %v 지문 같음=%v", err, consentFP == shareFP)
	}
	for name, change := range map[string]func(*E2HTTPFilePolicy){
		"consent_without_share":    func(p *E2HTTPFilePolicy) { p.E3WriteConsent = true },
		"resettable_without_share": func(p *E2HTTPFilePolicy) { p.E3Resettable = true },
	} {
		if _, _, err := ValidateE2HTTPRunPolicyDocument(encode(change)); err == nil {
			t.Fatalf("%s: 받음", name)
		}
	}
	if _, err := ValidateE2HTTPPolicyDocument(encode(func(p *E2HTTPFilePolicy) { p.E3WriteConsent = true }), true); err == nil {
		t.Fatal("평가 단독 정책이 쓰기 동의 칸을 받음")
	}
}

func TestE2HTTPRunPolicyExplorationEvidenceVersion(t *testing.T) {
	_, _, base := e2HostPolicyFixture(t)
	encode := func(mode, digest string) []byte {
		p := base
		p.ExplorationLoginEvidence, p.ExplorationSourceDigest = mode, digest
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	digest := strings.Repeat("a", 64)
	if p, _, err := ValidateE2HTTPRunPolicyDocument(encode("required/v2", digest)); err != nil || p.ExplorationSourceDigest != digest {
		t.Fatalf("결속된 신규 정책 거부: %v", err)
	}
	for _, input := range [][2]string{{"required/v2", ""}, {"required/v2", "short"}, {"required/v1", digest}, {"", digest}} {
		if _, _, err := ValidateE2HTTPRunPolicyDocument(encode(input[0], input[1])); err == nil {
			t.Fatalf("잘못된 출처 정책 허용: %q", input)
		}
	}
	if _, err := ValidateE2HTTPPolicyDocument(encode("required/v2", digest), true); err == nil {
		t.Fatal("제품 전용 근거 요구를 평가 정책으로 허용")
	}
}
