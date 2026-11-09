//go:build linux

package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2config"
	"github.com/Autumn-27/artex/e2proxy"
)

func TestE2EvaluationPolicyDocumentStages(t *testing.T) {
	_, _, policy := e2HostPolicyFixture(t)
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	var template map[string]any
	if json.Unmarshal(raw, &template) != nil {
		t.Fatal("fixture decode")
	}
	delete(template, "audit_key")
	base, _ := json.Marshal(template)
	if _, err := ValidateE2HTTPPolicyDocument(base, false); err != nil {
		t.Fatal("base policy rejected:", err)
	}
	future := policy
	future.ValidFrom = time.Now().Add(time.Hour)
	future.ValidUntil = future.ValidFrom.Add(time.Hour)
	futureRaw, err := json.Marshal(future)
	if err != nil || json.Unmarshal(futureRaw, &template) != nil {
		t.Fatal("future fixture encode")
	}
	delete(template, "audit_key")
	futureBase, _ := json.Marshal(template)
	if _, err := ValidateE2HTTPPolicyDocument(futureBase, false); err != nil {
		t.Fatal("future offline template rejected:", err)
	}
	if _, err := ValidateE2HTTPPolicyDocument(raw, true); err != nil {
		t.Fatal("prepared policy rejected:", err)
	}
	if _, err := ValidateE2HTTPPolicyDocument(base, true); err == nil {
		t.Fatal("prepared policy without audit pin accepted")
	}
	if _, err := ValidateE2HTTPPolicyDocument(raw, false); err == nil {
		t.Fatal("template with audit pin accepted")
	}
	cases := map[string][]byte{
		"unknown":   bytes.Replace(base, []byte(`"format":`), []byte(`"unexpected":1,"format":`), 1),
		"duplicate": bytes.Replace(base, []byte(`"format":`), []byte(`"format":"shadow","format":`), 1),
		"missing":   bytes.Replace(base, []byte(`"max_requests":2,`), nil, 1),
		"null":      bytes.Replace(base, []byte(`"max_requests":2`), []byte(`"max_requests":null`), 1),
		"type":      bytes.Replace(base, []byte(`"max_requests":2`), []byte(`"max_requests":"2"`), 1),
		"approval":  bytes.Replace(base, []byte(`"preauthorized_scope"`), []byte(`"confirm_every_request"`), 1),
		"method":    bytes.Replace(base, []byte(`"GET"`), []byte(`"CONNECT"`), 1),
		"path":      bytes.Replace(base, []byte(`"path_prefix":"/"`), []byte(`"path_prefix":"/%2f"`), 1),
		"origin":    bytes.Replace(base, []byte(`"https://example.invalid"`), []byte(`"https://example.invalid/path"`), 1),
		"budget":    bytes.Replace(base, []byte(`"max_attempts":5`), []byte(`"max_attempts":40001`), 1),
		"size":      bytes.Repeat([]byte{' '}, e2config.MaxPolicyBytes+1),
	}
	for name, mutated := range cases {
		t.Run(name, func(t *testing.T) {
			if bytes.Equal(mutated, base) {
				t.Fatal("fixture mutation did not apply")
			}
			if _, err := ValidateE2HTTPPolicyDocument(mutated, false); err == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
}

// factory의 후속 정적 검사만으로 통과하는 누락 필드를 backend가 직접 거부해야 한다.
func TestE2StandaloneUsesPreparedDocumentValidation(t *testing.T) {
	host, _, policy := e2HostPolicyFixture(t)
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if json.Unmarshal(raw, &document) != nil {
		t.Fatal("fixture decode")
	}
	delete(document, "evaluation")
	raw, err = json.Marshal(document)
	if err != nil || os.WriteFile(host.PolicyFile, raw, 0600) != nil {
		t.Fatal("incomplete policy write")
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	authority := &standaloneAuthorityFixture{ctx: ctx, digest: sha256.Sum256(raw)}
	factory, err := LoadE2HTTPRuntimeOptions(ctx, E2HTTPFileOptions{PolicyFile: host.PolicyFile, Authority: authority, Supervisor: standaloneSupervisorFixture{}})
	if err != nil {
		t.Fatal("fixture must reach the old factory boundary:", err)
	}
	config := e2config.Config{Format: e2config.Format, EvalRunID: "eval-fixture", ReservationRef: "reservation-fixture", ModelTag: "model-fixture", HeaderMode: "preserve", PolicyFile: filepath.Base(host.PolicyFile), AuditKeyFile: policy.AuditKey.File, Listen: ":18432", Upstream: "http://eval-meter-upstream:18432", EvidenceDir: firstDiagnosisPrivateTemp(t), ControlDir: e2ControlPrivateTestDir(t)}
	configRaw, err := json.Marshal(config)
	configPath := filepath.Join(host.AuthorityRoot, "e2-config.json")
	if err != nil || os.WriteFile(configPath, configRaw, 0600) != nil {
		t.Fatal("config write")
	}
	loaded, err := e2config.Load(configPath)
	if err != nil {
		t.Fatal("preflight must accept the fixture:", err)
	}
	if _, err := NewE2StandaloneBackend(ctx, E2StandaloneOptions{Config: loaded, Authority: authority, Supervisor: standaloneSupervisorFixture{}, Readiness: &standaloneReadinessFixture{}, ExecutionRef: "fixture-execution", ExpectedPolicyFingerprintSHA256: factory.PolicyFingerprintSHA256}); err == nil {
		t.Fatal("backend accepted a prepared policy without the required evaluation field")
	}
}

// 기존 Python 준비기가 만든 바이트를 Go의 보호 로더·정책 factory·독립 backend가 함께 소비한다.
func TestE2EvaluationPythonPrepareGoConsumer(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
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
	for _, pin := range []E2HTTPFilePin{policy.CACert, policy.CAKey} {
		value, err := os.ReadFile(filepath.Join(host.AuthorityRoot, pin.File))
		if err != nil || os.WriteFile(filepath.Join(prep, pin.File), value, 0600) != nil {
			t.Fatal("synthetic CA copy failed")
		}
	}
	policy.HeaderMode = e2proxy.HeaderEvaluationCallerPassthrough
	policy.Evaluation = &e2proxy.EvaluationCallerContext{RunRef: policy.RunRef, LLMBackend: "api", TargetKind: "public_practice"}
	template, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if json.Unmarshal(template, &document) != nil {
		t.Fatal("fixture decode")
	}
	delete(document, "audit_key")
	template, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateE2HTTPPolicyDocument(template, false); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(host.AuthorityRoot, "base-policy.json")
	if err := os.WriteFile(source, template, 0600); err != nil {
		t.Fatal(err)
	}
	_, thisFile, _, _ := runtime.Caller(0)
	scripts := filepath.Join(filepath.Dir(thisFile), "..", "scripts", "eval")
	prepare := `import sys; sys.path.insert(0, sys.argv[1]); from pathlib import Path; from e2_prepare import E2RunValues, prepare_e2_config; prepare_e2_config(Path(sys.argv[2]), E2RunValues("eval-fixture", "model-fixture", "evaluation_caller_passthrough", {"llm_backend":"api","target_kind":"public_practice"}, 18432), Path(sys.argv[3]))`
	cmd := exec.CommandContext(t.Context(), python, "-I", "-c", prepare, scripts, prep, source)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Python preparation failed: %s", output)
	}
	configPath := filepath.Join(prep, "e2-config.json")
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	// 준비기의 고정 경로를 시험용 보호 경로로만 바꾼 뒤 정확한 새 바이트를 검증한다.
	configBytes = bytes.Replace(configBytes, []byte("/run/e2-control"), []byte(control), 1)
	configBytes = bytes.Replace(configBytes, []byte("/evidence"), []byte(evidence), 1)
	if err := os.WriteFile(configPath, configBytes, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := e2config.Load(configPath)
	if err != nil {
		t.Fatal("Go runner config rejected Python output:", err)
	}
	raw, err := c.ReadPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateE2HTTPPolicyDocument(raw, true); err != nil {
		t.Fatal("Go policy validation rejected Python output:", err)
	}
	if c.ConfigSHA256 != shaHex(configBytes) || c.PolicySHA256 != shaHex(raw) || c.ConfigSHA256 == c.PolicySHA256 {
		t.Fatal("exact byte digests not distinguished")
	}
	audit, err := c.ReadAuditKey()
	if err != nil {
		t.Fatal(err)
	}
	var prepared E2HTTPFilePolicy
	if json.Unmarshal(raw, &prepared) != nil || prepared.AuditKey.SHA256 != shaHex(audit) || len(audit) != 32 {
		t.Fatal("audit pin mismatch")
	}
	clear(audit)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	authority := &standaloneAuthorityFixture{ctx: ctx, digest: sha256.Sum256(raw)}
	factory, err := LoadE2HTTPRuntimeOptions(ctx, E2HTTPFileOptions{PolicyFile: c.PolicyPath, Authority: authority, Supervisor: standaloneSupervisorFixture{}})
	if err != nil {
		t.Fatal("Go policy factory rejected Python output:", err)
	}
	backend, err := NewE2StandaloneBackend(ctx, E2StandaloneOptions{Config: c, Authority: authority, Supervisor: standaloneSupervisorFixture{}, Readiness: &standaloneReadinessFixture{}, ExecutionRef: "fixture-execution", ExpectedPolicyFingerprintSHA256: factory.PolicyFingerprintSHA256})
	if err != nil {
		t.Fatal("Go backend rejected Python output:", err)
	}
	t.Cleanup(func() {
		if err := backend.Dispose(); err != nil {
			t.Error(err)
		}
	})
	if ready, err := backend.Ready(ctx); err != nil || ready.PolicyFingerprintSHA256 != factory.PolicyFingerprintSHA256 || ready.ConfigSHA256 != c.ConfigSHA256 {
		t.Fatal("ready digest binding failed")
	}
	if _, err := NewE2StandaloneBackend(ctx, E2StandaloneOptions{Config: c, Authority: authority, Supervisor: standaloneSupervisorFixture{}, Readiness: &standaloneReadinessFixture{}, ExecutionRef: "fixture-execution-2", ExpectedPolicyFingerprintSHA256: strings.Repeat("0", 64)}); err == nil {
		t.Fatal("different expected fingerprint accepted")
	}
	second := filepath.Join(host.AuthorityRoot, "prepared-second")
	if err := os.Mkdir(second, 0700); err != nil {
		t.Fatal(err)
	}
	for _, pin := range []E2HTTPFilePin{policy.CACert, policy.CAKey} {
		value, err := os.ReadFile(filepath.Join(host.AuthorityRoot, pin.File))
		if err != nil || os.WriteFile(filepath.Join(second, pin.File), value, 0600) != nil {
			t.Fatal("second synthetic CA copy failed")
		}
	}
	cmd = exec.CommandContext(t.Context(), python, "-I", "-c", prepare, scripts, second, source)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("second Python preparation failed: %s", output)
	}
	secondPolicy, err := os.ReadFile(filepath.Join(second, "policy.json"))
	if err != nil || bytes.Equal(secondPolicy, raw) || shaHex(secondPolicy) == c.PolicySHA256 {
		t.Fatal("round-specific audit pin did not change policy bytes")
	}
	secondAuthority := &standaloneAuthorityFixture{ctx: ctx, digest: sha256.Sum256(secondPolicy)}
	secondFactory, err := LoadE2HTTPRuntimeOptions(ctx, E2HTTPFileOptions{PolicyFile: filepath.Join(second, "policy.json"), Authority: secondAuthority, Supervisor: standaloneSupervisorFixture{}})
	if err != nil || secondFactory.PolicyFingerprintSHA256 != factory.PolicyFingerprintSHA256 {
		t.Fatal("audit-key rotation changed the policy meaning fingerprint")
	}
}

func shaHex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
