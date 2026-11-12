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
	var preparedDoc map[string]any
	if json.Unmarshal(raw, &preparedDoc) != nil {
		t.Fatal("prepared fixture decode")
	}
	preparedDoc["audit_key"].(map[string]any)["sha256"] = "bad"
	badAudit, _ := json.Marshal(preparedDoc)
	if _, err := ValidateE2HTTPPolicyDocument(badAudit, true); err == nil {
		t.Fatal("malformed prepared audit pin accepted")
	}
	preparedDoc["audit_key"] = nil
	nullAudit, _ := json.Marshal(preparedDoc)
	if _, err := ValidateE2HTTPPolicyDocument(nullAudit, true); err == nil {
		t.Fatal("null prepared audit pin accepted")
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

// 오프라인 검증·지문은 회차 시각과 분리하고 실제 시작의 유효창 검사는 유지한다.
func TestE2EvaluationOfflineValidityAndRuntimeWindow(t *testing.T) {
	host, _, policy := e2HostPolicyFixture(t)
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	want, err := E2HTTPPolicyContentFingerprint(raw, true)
	if err != nil {
		t.Fatal("current fingerprint:", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	load := func(raw []byte) (E2HTTPRuntimeOptions, error) {
		t.Helper()
		if err := os.WriteFile(host.PolicyFile, raw, 0600); err != nil {
			t.Fatal(err)
		}
		authority := &standaloneAuthorityFixture{ctx: ctx, digest: sha256.Sum256(raw)}
		return LoadE2HTTPRuntimeOptions(ctx, E2HTTPFileOptions{PolicyFile: host.PolicyFile, Authority: authority, Supervisor: standaloneSupervisorFixture{}})
	}
	if _, err := load(raw); err != nil {
		t.Fatal("current runtime control:", err)
	}
	for name, offset := range map[string]time.Duration{"future": time.Hour, "expired": -2 * time.Hour} {
		t.Run(name, func(t *testing.T) {
			changed := policy
			changed.ValidFrom = time.Now().Add(offset)
			changed.ValidUntil = changed.ValidFrom.Add(time.Hour)
			wire, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			for _, prepared := range []bool{false, true} {
				input := wire
				if !prepared {
					var document map[string]json.RawMessage
					if err := json.Unmarshal(wire, &document); err != nil {
						t.Fatal(err)
					}
					delete(document, "audit_key")
					input, err = json.Marshal(document)
					if err != nil {
						t.Fatal(err)
					}
				}
				if _, err := ValidateE2HTTPPolicyDocument(input, prepared); err != nil {
					t.Fatal("offline document:", err)
				}
				if got, err := E2HTTPPolicyContentFingerprint(input, prepared); err != nil || got != want {
					t.Fatal("offline fingerprint changed with the validity window:", err)
				}
			}
			if _, err := load(wire); err != ErrE2HTTPHost {
				t.Fatal("runtime accepted a policy outside the current validity window:", err)
			}
		})
	}
}

// 문서의 필드별 거부 규칙은 구현 분기를 하나씩 약화해도 깨지도록 독립 입력으로 고정한다.
func TestE2EvaluationPolicyDocumentFieldFailures(t *testing.T) {
	_, _, policy := e2HostPolicyFixture(t)
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	var source map[string]any
	if json.Unmarshal(raw, &source) != nil {
		t.Fatal("fixture decode")
	}
	delete(source, "audit_key")
	cases := map[string]func(map[string]any){
		"format":               func(o map[string]any) { o["format"] = "abc2lab.e2-http-host-policy/v2" },
		"header_mode_empty":    func(o map[string]any) { o["header_mode"] = "" },
		"run_ref_invalid":      func(o map[string]any) { o["run_ref"] = "https://example.invalid" },
		"helper_digest":        func(o map[string]any) { o["helper_sha256"] = strings.Repeat("A", 64) },
		"state_dir_empty":      func(o map[string]any) { o["state_dir"] = "" },
		"helper_empty":         func(o map[string]any) { o["helper"] = "" },
		"ca_pin_file":          func(o map[string]any) { o["ca_cert"].(map[string]any)["file"] = "../ca.pem" },
		"ca_pin_hash":          func(o map[string]any) { o["ca_key"].(map[string]any)["sha256"] = "bad" },
		"ca_pin_missing":       func(o map[string]any) { delete(o["ca_cert"].(map[string]any), "file") },
		"ca_pin_null":          func(o map[string]any) { o["ca_cert"].(map[string]any)["sha256"] = nil },
		"limits_missing":       func(o map[string]any) { delete(o["limits"].(map[string]any), "max_calls") },
		"limits_null":          func(o map[string]any) { o["limits"].(map[string]any)["max_calls"] = nil },
		"proxy_missing":        func(o map[string]any) { delete(o["proxy"].(map[string]any), "max_connections") },
		"runtime_null":         func(o map[string]any) { o["runtime"].(map[string]any)["output_limit"] = nil },
		"rule_display_missing": func(o map[string]any) { delete(o["rules"].([]any)[0].(map[string]any), "approval_display") },
		"rule_display_null":    func(o map[string]any) { o["rules"].([]any)[0].(map[string]any)["approval_display"] = nil },
		"rule_display_type": func(o map[string]any) {
			o["rules"].([]any)[0].(map[string]any)["approval_display"].(map[string]any)["query_keys"] = "one"
		},
		"credential_wrong_mode": func(o map[string]any) { o["rules"].([]any)[0].(map[string]any)["credentials"] = o["ca_cert"] },
		"future_before_start":   func(o map[string]any) { o["valid_until"] = o["valid_from"] },
		"evaluation_wrong_mode": func(o map[string]any) {
			o["evaluation"] = map[string]any{"run_ref": "fixture-run", "llm_backend": "api", "target_kind": "public_practice"}
		},
		"evaluation_run_mismatch": func(o map[string]any) {
			o["header_mode"] = "evaluation_caller_passthrough"
			o["evaluation"] = map[string]any{"run_ref": "other-run", "llm_backend": "api", "target_kind": "public_practice"}
		},
		"evaluation_missing": func(o map[string]any) {
			o["header_mode"] = "evaluation_caller_passthrough"
		},
		"upstream_pin_invalid": func(o map[string]any) { o["upstream_ca"] = map[string]any{"file": "ca.pem", "sha256": "bad"} },
		"limits_unknown":       func(o map[string]any) { o["limits"].(map[string]any)["max_response_bytes"] = 1 },
		"response_limit_null":  func(o map[string]any) { o["limits"].(map[string]any)["max_response_body_bytes"] = nil },
		"response_scan_short": func(o map[string]any) {
			o["limits"].(map[string]any)["max_response_body_bytes"] = 1 << 20
			o["limits"].(map[string]any)["max_response_scan_bytes"] = 1 << 10
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			var input map[string]any
			sourceRaw, _ := json.Marshal(source)
			if json.Unmarshal(sourceRaw, &input) != nil {
				t.Fatal("fixture copy")
			}
			change(input)
			wire, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ValidateE2HTTPPolicyDocument(wire, false); err == nil {
				t.Fatal("invalid field accepted")
			}
		})
	}
}

// 응답 전달·검사 상한(C16 결정안 3)은 선택 칸이다. 설치가 쓴 정책 파일을 서버가 그대로 읽어 E② 한도에 넣는다.
func TestE2HTTPPolicyDocumentAcceptsResponseLimits(t *testing.T) {
	_, _, policy := e2HostPolicyFixture(t)
	policy.Limits.MaxResponseBodyBytes = policy.Limits.MaxBodyBytes * 2
	policy.Limits.MaxResponseScanBytes = policy.Limits.MaxBodyBytes*2 + 4096
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	// 준비 전 문서에는 감사 키 pin이 없다(TestE2EvaluationPolicyDocumentFieldFailures와 같은 입력).
	var document map[string]json.RawMessage
	if json.Unmarshal(raw, &document) != nil {
		t.Fatal("fixture decode")
	}
	delete(document, "audit_key")
	wire, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ValidateE2HTTPPolicyDocument(wire, false)
	if err != nil {
		t.Fatal("response limits rejected:", err)
	}
	if got.Limits.MaxResponseBodyBytes != policy.Limits.MaxResponseBodyBytes || got.Limits.MaxResponseScanBytes != policy.Limits.MaxResponseScanBytes {
		t.Fatalf("response limits not kept: %+v", got.Limits)
	}
}

// 인증 준비 규칙(C14)은 정책 파일의 선택 칸 purpose로 받고, 목적이 틀리면 거부한다.
func TestE2HTTPPolicyDocumentAcceptsAuthPrepRule(t *testing.T) {
	_, _, policy := e2HostPolicyFixture(t)
	login := policy.Rules[0]
	login.Ref, login.PathScopeRef, login.PathPrefix, login.Methods, login.Purpose, login.Credentials = "auth-login", "auth-login-path", "/login", []string{"POST"}, "auth_prep", nil
	policy.Rules = append(policy.Rules, login)
	encode := func(p E2HTTPFilePolicy) []byte {
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]json.RawMessage
		if json.Unmarshal(raw, &document) != nil {
			t.Fatal("fixture decode")
		}
		delete(document, "audit_key")
		wire, _ := json.Marshal(document)
		return wire
	}
	got, err := ValidateE2HTTPPolicyDocument(encode(policy), false)
	if err != nil || got.Rules[len(got.Rules)-1].Purpose != "auth_prep" {
		t.Fatalf("auth prep rule rejected: %v", err)
	}
	// 인증 준비 칸은 그 규칙과 맞아야 한다.
	pin := E2HTTPFilePin{File: "account-tester-a.json", SHA256: strings.Repeat("a", 64)}
	a := cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: pin})
	policy.Auth = &a
	if _, err := ValidateE2HTTPPolicyDocument(encode(policy), false); err != nil {
		t.Fatal("auth section rejected:", err)
	}
	a.Login.Path = "/other"
	if _, err := ValidateE2HTTPPolicyDocument(encode(policy), false); err == nil {
		t.Fatal("auth section that does not match its rule accepted")
	}
	a.Login.Path = "/login"
	policy.Rules[len(policy.Rules)-1].Purpose = "admin"
	if _, err := ValidateE2HTTPPolicyDocument(encode(policy), false); err == nil {
		t.Fatal("unknown purpose accepted")
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

// 자격 헤더 이름은 지문에 들어가는데 오프라인에서는 pin 파일을 읽지 않는다. 틀린 값을 내느니 값을 내지 않는다.
func TestE2HTTPPolicyContentFingerprintRefusesCredentialRules(t *testing.T) {
	_, _, policy := e2HostPolicyFixture(t)
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if json.Unmarshal(raw, &document) != nil {
		t.Fatal("fixture decode")
	}
	delete(document, "audit_key")
	document["header_mode"] = string(e2proxy.HeaderProtectedSessionProposal)
	withoutCredentials, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint, err := E2HTTPPolicyContentFingerprint(withoutCredentials, false); err != nil || len(fingerprint) != 64 {
		t.Fatal("protected mode without credentials rejected:", err)
	}
	document["rules"].([]any)[0].(map[string]any)["credentials"] = map[string]any{"file": "credentials.json", "sha256": strings.Repeat("a", 64)}
	withCredentials, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateE2HTTPPolicyDocument(withCredentials, false); err != nil {
		t.Fatal("credential policy is valid and must stay valid:", err)
	}
	if fingerprint, err := E2HTTPPolicyContentFingerprint(withCredentials, false); err == nil || fingerprint != "" {
		t.Fatal("credential rule produced an offline fingerprint")
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
	if offline, err := E2HTTPPolicyContentFingerprint(raw, true); err != nil || offline != factory.PolicyFingerprintSHA256 {
		t.Fatal("offline fingerprint differs from the factory")
	}
	if _, err := E2HTTPPolicyContentFingerprint(raw, false); err == nil {
		t.Fatal("prepared policy accepted as a base policy")
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
