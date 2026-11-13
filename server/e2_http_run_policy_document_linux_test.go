//go:build linux

package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/e2proxy"
)

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
