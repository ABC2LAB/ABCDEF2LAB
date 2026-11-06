package agent

import (
	"context"
	"strings"
	"testing"
)

func TestRenderSystemOverrideAndFallback(t *testing.T) {
	ctx := context.Background()

	// no override → built-in default
	if got := renderSystem(ctx, "planner", "DEFAULT", PlannerVars{Goal: "g"}); got != "DEFAULT" {
		t.Fatalf("no override should give default, got %q", got)
	}

	// override → rendered with vars
	h := RuntimeHooks{PromptOverride: func(k string) (string, bool) {
		if k == "planner" {
			return "目标:{{.Goal}} 范围:{{.Scope}}", true
		}
		return "", false
	}}
	ctx = WithRuntimeHooks(ctx, h)
	if got := renderSystem(ctx, "planner", "DEFAULT", PlannerVars{Goal: "拿下X", Scope: "*.x.com"}); got != "目标:拿下X 范围:*.x.com" {
		t.Fatalf("override render: %q", got)
	}

	// override referencing a non-catalog var → execution error → fallback to default
	h.PromptOverride = func(k string) (string, bool) { return "{{.NotInCatalog}}", true }
	ctx = WithRuntimeHooks(ctx, h)
	if got := renderSystem(ctx, "planner", "DEFAULT", PlannerVars{Goal: "x"}); got != "DEFAULT" {
		t.Fatalf("bad var should fall back to default, got %q", got)
	}

	// full plannerSystem path: DB body [A] is honored, then the code-owned tail
	// [C] (the artifact rule) is ALWAYS appended — editing the body can't drop it.
	h.PromptOverride = func(k string) (string, bool) { return "PLANNER {{.Goal}}", true }
	ctx = WithRuntimeHooks(ctx, h)
	got := plannerSystem(ctx, "拿下X", "/data", "/data")
	if !strings.HasPrefix(got, "PLANNER 拿下X") {
		t.Fatalf("plannerSystem body not honored: %q", got)
	}
	if !strings.Contains(got, "Rule for intermediate artifacts") || !strings.Contains(got, "/data") {
		t.Fatalf("plannerSystem missing code-owned artifact tail: %q", got)
	}

	// worker dual-text via {{if .ProxyAddr}} in a user template, plus the code tail:
	// [B] trafficTool present only when RECORDING (caCert set — the MITM is on, so
	// the traffic_* tools exist), [C] artifact spec always present. The trafficTool
	// block is gated on the CA (arg 2), NOT on ProxyAddr — a global egress proxy
	// with capture off routes traffic but records nothing.
	h.PromptOverride = func(k string) (string, bool) {
		return "{{if .ProxyAddr}}走代理 {{.ProxyAddr}}{{else}}手动{{end}}", true
	}
	ctx = WithRuntimeHooks(ctx, h)
	recording := workerSystem(ctx, "127.0.0.1:8080", "/ca.pem", "/data", "/data")
	if !strings.HasPrefix(recording, "走代理 127.0.0.1:8080") {
		t.Fatalf("worker proxy branch body: %q", recording)
	}
	if !strings.Contains(recording, "traffic_search") {
		t.Fatalf("worker while recording should inject trafficTool: %q", recording)
	}
	if strings.Contains(recording, "traffic_refs") {
		t.Fatalf("worker bypassed shared optional evidence policy: %q", recording)
	}
	if !strings.Contains(recording, "Rule for intermediate artifacts") {
		t.Fatalf("worker missing artifact tail: %q", recording)
	}
	// Egress proxy set but capture OFF (no CA): the ProxyAddr template branch still
	// renders, but the trafficTool block must NOT — those tools are not registered.
	egressOnly := workerSystem(ctx, "127.0.0.1:8080", "", "/data", "/data")
	if !strings.HasPrefix(egressOnly, "走代理 127.0.0.1:8080") {
		t.Fatalf("worker egress-only branch body: %q", egressOnly)
	}
	if strings.Contains(egressOnly, "traffic_search") {
		t.Fatalf("worker without recording must NOT inject trafficTool: %q", egressOnly)
	}
	noProxy := workerSystem(ctx, "", "", "/data", "/data")
	if !strings.HasPrefix(noProxy, "手动") {
		t.Fatalf("worker no-proxy branch body: %q", noProxy)
	}
	if strings.Contains(noProxy, "traffic_search") {
		t.Fatalf("worker without proxy must NOT inject trafficTool: %q", noProxy)
	}
}

// TestLangDirectiveKoreanOnly 는 과거 언어 코드에도 한국어를 지명하고, 기술 원문 보존,
// 대상·자료 언어 모방 금지, planner 상황 요약 포함, 내부 추론 비노출을 유지하는지 고정한다.
func TestLangDirectiveKoreanOnly(t *testing.T) {
	for _, code := range []string{"ko", "en", "zh", "es", "ja", "fr", ""} {
		dir := langDirective(code)
		if !strings.Contains(dir, "[Korean]") || !strings.Contains(dir, "in Korean only") {
			t.Fatalf("%s: display language must be Korean: %q", code, dir)
		}
		for _, r := range dir {
			if r > 127 {
				t.Fatalf("%s: model-facing directive must be English: %q", code, dir)
			}
		}
		if !strings.Contains(dir, "payloads") || !strings.Contains(dir, "kept exactly as they are") {
			t.Fatalf("%s: langDirective must keep commands/payloads verbatim, got %q", code, dir)
		}
		if !strings.Contains(dir, "Don't mirror or copy the language of the target or material") {
			t.Fatalf("%s: langDirective must forbid mirroring the target/material language, got %q", code, dir)
		}
		if !strings.Contains(dir, "planner's situation summary") {
			t.Fatalf("%s: langDirective must name the planner situation summary as user-facing, got %q", code, dir)
		}
		if !strings.Contains(dir, "record_fact summary and detail") {
			t.Fatalf("%s: langDirective must name record_fact summary and detail as user-facing, got %q", code, dir)
		}
		if !strings.Contains(dir, "invisible internal reasoning") {
			t.Fatalf("%s: langDirective must keep internal reasoning out of the visible reply, got %q", code, dir)
		}
		if hasHan(dir) {
			t.Fatalf("%s: langDirective must not contain Han characters, got %q", code, dir)
		}
	}
}

// 모든 사용자 대면 역할에 한국어 표시 지시를 붙인다. DB 본문이나 옛 언어 훅으로
// 이 지시가 빠지거나 다른 표시 언어로 바뀌지 않는지 확인한다.
func TestLangDirectiveAppendedToUserFacingRoles(t *testing.T) {
	ctx := context.Background()

	// Even with a DB body that is pure non-directive text, the code-owned tail is
	// still appended for each user-facing builder — identical guarantee to the
	// artifact tail. A custom body can never translate away the output-language mandate.
	h := RuntimeHooks{PromptOverride: func(string) (string, bool) { return "BODY-ONLY", true }}

	for _, code := range []string{"ko", "en", "zh", "es", "ja", "fr", ""} {
		h.OutputLanguage = func() string { return code }
		ctx = WithRuntimeHooks(context.Background(), h)
		marker := "[Korean]"
		cases := map[string]string{
			"worker":    workerSystem(ctx, "", "", "/data", "/data"),
			"planner":   plannerSystem(ctx, "g", "/data", "/data"),
			"mainagent": mainAgentSystem(ctx, "g", "/data", "/data"),
			"chat":      chatSystem(ctx, "chat", "/data", "/data"),
			// goals is user-facing too: set_goals/set_constraints persist goal and
			// constraint nodes shown in the UI graph/plan tab. withScope=true exercises
			// the longer assembly (body + scope tail), so the language tail must still
			// land last — after both the body and the code-owned scope tail.
			"goals": goalsSystem(ctx, "/data", true),
		}
		for role, sys := range cases {
			if !strings.HasPrefix(sys, "BODY-ONLY") {
				t.Fatalf("%s/%s: DB body not honored: %q", code, role, sys)
			}
			if !strings.Contains(sys, marker) {
				t.Fatalf("%s/%s: missing output-language tail (%q): %q", code, role, marker, sys)
			}
			// The directive is the tail — it must come AFTER the body (recency).
			if strings.Index(sys, marker) <= strings.Index(sys, "BODY-ONLY") {
				t.Fatalf("%s/%s: langDirective must be appended after the body: %q", code, role, sys)
			}
		}
	}
}
