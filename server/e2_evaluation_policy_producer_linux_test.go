//go:build linux

package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/e2config"
	"github.com/Autumn-27/artex/e2proxy"
)

// 러너의 정책 생산자가 만든 파일을 실제 도구·로더·factory·독립 backend가 받는지 끝까지 대조한다.
// 자리값으로 오프라인 계산한 예상 지문이 실제 회차의 지문과 같아야 공식 프로파일에 동결한 값이 쓸모가 있다.
func TestE2EvaluationPolicyProducerGoConsumer(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool unavailable")
	}
	_, thisFile, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(thisFile), "..")
	tool := filepath.Join(t.TempDir(), "e2-eval-policy")
	build := exec.CommandContext(t.Context(), goTool, "build", "-o", tool, "./cmd/e2-eval-policy")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("policy tool build failed: %s", output)
	}

	host, _, policy := e2HostPolicyFixture(t)
	prep := filepath.Join(host.AuthorityRoot, "prepared")
	control := filepath.Join(host.AuthorityRoot, "control")
	evidence := filepath.Join(host.AuthorityRoot, "evidence")
	for _, dir := range []string{prep, control, evidence} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	connectIP := policy.Rules[0].ConnectIP
	policy.HeaderMode = e2proxy.HeaderEvaluationCallerPassthrough
	policy.Evaluation = &e2proxy.EvaluationCallerContext{RunRef: policy.RunRef, LLMBackend: "api", TargetKind: "public_practice"}
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	var semantic map[string]any
	if json.Unmarshal(raw, &semantic) != nil {
		t.Fatal("fixture decode")
	}
	for _, name := range []string{"audit_key", "run_ref", "valid_from", "valid_until", "state_dir", "helper", "helper_sha256", "ca_cert", "ca_key"} {
		delete(semantic, name)
	}
	delete(semantic["evaluation"].(map[string]any), "run_ref")
	for _, rule := range semantic["rules"].([]any) {
		delete(rule.(map[string]any), "connect_ip")
	}
	templateBytes, err := json.Marshal(semantic)
	if err != nil {
		t.Fatal(err)
	}
	template := filepath.Join(host.AuthorityRoot, "semantic-policy.json")
	if err := os.WriteFile(template, templateBytes, 0600); err != nil {
		t.Fatal(err)
	}

	scripts := filepath.Join(root, "scripts", "eval")
	const produce = `import sys; sys.path.insert(0, sys.argv[1])
from datetime import datetime, timezone
from pathlib import Path
from e2_prepare import E2RunResources, E2RunValues, approval_window, issue_run_ca, prepare_e2_config, render_policy
prep, template, tool, helper, digest, state, ip = sys.argv[2:9]
run_ref = "e2run-go-consumer"
start, end = approval_window(datetime.now(timezone.utc), 3600, "2099-01-01T00:00:00Z")
cert, key = issue_run_ca(Path(tool), Path(prep), run_ref, end, 60)
policy = render_policy(Path(template), E2RunResources(run_ref, start, end, state, helper, digest, cert, key, ip))
prepare_e2_config(Path(prep), E2RunValues("eval-run-1", "model-1", "evaluation_caller_passthrough", {"llm_backend": "api", "target_kind": "public_practice"}, 8080), policy)
`
	cmd := exec.CommandContext(t.Context(), python, "-I", "-c", produce, scripts, prep, template, tool, policy.Helper, policy.HelperSHA256, policy.StateDir, connectIP)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("producer failed: %s", output)
	}
	offline, err := exec.CommandContext(t.Context(), python, "-I", filepath.Join(scripts, "e2_policy_fingerprint.py"), "--template", template, "--tool", tool, "--timeout-seconds", "60").Output()
	if err != nil {
		t.Fatal("offline fingerprint failed:", err)
	}
	expected := strings.TrimSpace(string(offline))

	configPath := filepath.Join(prep, "e2-config.json")
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	// 준비기의 컨테이너 경로만 시험용 보호 경로로 바꾼다.
	configBytes = bytes.Replace(configBytes, []byte("/run/e2-control"), []byte(control), 1)
	configBytes = bytes.Replace(configBytes, []byte("/evidence"), []byte(evidence), 1)
	if err := os.WriteFile(configPath, configBytes, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := e2config.Load(configPath)
	if err != nil {
		t.Fatal("runner config rejected:", err)
	}
	prepared, err := os.ReadFile(filepath.Join(prep, "policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	document, err := ValidateE2HTTPPolicyDocument(prepared, true)
	if err != nil {
		t.Fatal("prepared policy rejected:", err)
	}
	if document.RunRef != "e2run-go-consumer" || document.Evaluation.RunRef != document.RunRef || document.RunRef == c.EvalRunID || document.Rules[0].ConnectIP != connectIP {
		t.Fatal("run values not filled")
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	authority := &standaloneAuthorityFixture{ctx: ctx, digest: sha256.Sum256(prepared)}
	factory, err := LoadE2HTTPRuntimeOptions(ctx, E2HTTPFileOptions{PolicyFile: c.PolicyPath, Authority: authority, Supervisor: standaloneSupervisorFixture{}})
	if err != nil {
		t.Fatal("policy factory rejected the producer output:", err)
	}
	if factory.PolicyFingerprintSHA256 != expected {
		t.Fatalf("offline fingerprint %q differs from the real run %q", expected, factory.PolicyFingerprintSHA256)
	}
	backend, err := NewE2StandaloneBackend(ctx, E2StandaloneOptions{Config: c, Authority: authority, Supervisor: standaloneSupervisorFixture{}, Readiness: &standaloneReadinessFixture{}, ExecutionRef: "fixture-execution", ExpectedPolicyFingerprintSHA256: expected})
	if err != nil {
		t.Fatal("backend rejected the producer output:", err)
	}
	t.Cleanup(func() {
		if err := backend.Dispose(); err != nil {
			t.Error(err)
		}
	})
	ready, err := backend.Ready(ctx)
	if err != nil || ready.PolicyFingerprintSHA256 != expected {
		t.Fatal("ready fingerprint mismatch", err)
	}
}
