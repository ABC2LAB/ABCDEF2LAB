//go:build linux

package server

import (
	"encoding/json"
	"os"
	"testing"
)

func TestExactPathFileFactoryDoesNotTreatNullAsPrefix(t *testing.T) {
	cfg, _, policy := e2HostPolicyFixture(t)
	authority := &e2HTTPPolicyAuthorityFixture{ctx: t.Context()}
	input := E2HTTPFileOptions{PolicyFile: cfg.PolicyFile, Authority: authority, Supervisor: &e2HTTPSupervisorFixture{}}
	raw, _ := json.Marshal(policy)
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		t.Fatal("합성 정책")
	}
	rule := body["rules"].([]any)[0].(map[string]any)
	rule["path_prefix"] = "/"
	rule["path_match"] = "exact"
	write := func() {
		t.Helper()
		data, _ := json.Marshal(body)
		if os.WriteFile(cfg.PolicyFile, data, 0600) != nil {
			t.Fatal("합성 정책 저장")
		}
	}
	write()
	if opts, err := LoadE2HTTPRuntimeOptions(t.Context(), input); err != nil || opts.Acquire == nil {
		t.Fatal("정확 경로 factory 거부", err)
	}
	for _, value := range []any{nil, "", false, "unsupported"} {
		rule["path_match"] = value
		write()
		if opts, err := LoadE2HTTPRuntimeOptions(t.Context(), input); err == nil || opts.Acquire != nil {
			t.Fatal("지원하지 않는 모드가 접두 기본값으로 확대")
		}
	}
	delete(rule, "path_match")
	rule["PATH_MATCH"] = nil
	write()
	if _, err := LoadE2HTTPRuntimeOptions(t.Context(), input); err == nil {
		t.Fatal("대소문자 별칭 null 수용")
	}
}
