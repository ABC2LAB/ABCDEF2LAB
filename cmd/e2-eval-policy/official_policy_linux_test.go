//go:build linux

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 러너의 ready 대조까지 가기 전에, 템플릿이나 형식 코드가 바뀌어 동결값이 낡은 것을 잡는다.
func TestOfficialPolicyFingerprintMatchesRunbook(t *testing.T) {
	docs := filepath.Join("..", "..", "docs", "evaluation")
	raw, err := os.ReadFile(filepath.Join(docs, "profiles", "official-e2-policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	var policy map[string]any
	if err = json.Unmarshal(raw, &policy); err != nil {
		t.Fatal(err)
	}
	// 회차 값은 내용 지문에서 빠지므로 형식에 맞는 자리값이면 된다.
	placeholder := validPolicy(nil)
	for _, key := range []string{"run_ref", "valid_from", "valid_until", "state_dir", "helper", "helper_sha256", "ca_cert", "ca_key"} {
		policy[key] = placeholder[key]
	}
	policy["evaluation"].(map[string]any)["run_ref"] = placeholder["run_ref"]
	firstRule(policy)["connect_ip"] = firstRule(placeholder)["connect_ip"]
	path, _ := writePolicy(t, policy)
	code, stdout, stderr := execute("fingerprint", "--policy-file", path)
	if code != 0 || stderr != "" {
		t.Fatal(code, stderr)
	}
	runbook, err := os.ReadFile(filepath.Join(docs, "official-run.md"))
	if err != nil {
		t.Fatal(err)
	}
	pinned := regexp.MustCompile(`official_e2_policy_fingerprint_sha256 = ([0-9a-f]{64})`).FindSubmatch(runbook)
	if pinned == nil {
		t.Fatal("실행 절차에 동결 지문이 없음")
	}
	if got := strings.TrimSpace(stdout); got != string(pinned[1]) {
		t.Fatalf("동결 지문이 낡음 got=%s pinned=%s", got, pinned[1])
	}
}
