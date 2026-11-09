//go:build linux

package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Autumn-27/artex/server"
)

func privateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "prepared")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func execute(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func caArgs(dir, cert, key, ref string, expires time.Time) []string {
	return []string{"ca", "--cert-file", filepath.Join(dir, cert), "--key-file", filepath.Join(dir, key), "--run-ref", ref, "--expires", expires.Format(time.RFC3339)}
}

func soon() time.Time { return time.Now().Add(2 * time.Hour).Truncate(time.Second) }

func TestCAWritesMatchingPrivatePair(t *testing.T) {
	dir := privateDir(t)
	expires := soon()
	code, stdout, stderr := execute(caArgs(dir, "ca.pem", "ca.key", "run-1", expires)...)
	if code != 0 || stdout != "e2_eval_ca_created\n" || stderr != "" {
		t.Fatal(code, stdout, stderr)
	}
	for _, name := range []string{"ca.pem", "ca.key"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal(name, err, info.Mode())
		}
	}
	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, "ca.pem"), filepath.Join(dir, "ca.key"))
	if err != nil || len(pair.Certificate) != 1 {
		t.Fatal("tls loader rejected the pair", err)
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || !cert.IsCA || !cert.NotAfter.Equal(expires) || cert.Subject.SerialNumber != "run-1" {
		t.Fatal("unexpected certificate", err, cert.IsCA, cert.NotAfter)
	}
}

func TestCAEachRunHasAFreshKey(t *testing.T) {
	first, second := privateDir(t), privateDir(t)
	expires := soon()
	for _, dir := range []string{first, second} {
		if code, _, stderr := execute(caArgs(dir, "ca.pem", "ca.key", "run-1", expires)...); code != 0 {
			t.Fatal(stderr)
		}
	}
	a, _ := os.ReadFile(filepath.Join(first, "ca.key"))
	b, _ := os.ReadFile(filepath.Join(second, "ca.key"))
	if len(a) == 0 || bytes.Equal(a, b) {
		t.Fatal("key reused between runs")
	}
}

func TestCARefusesToOverwrite(t *testing.T) {
	dir := privateDir(t)
	args := caArgs(dir, "ca.pem", "ca.key", "run-1", soon())
	if code, _, stderr := execute(args...); code != 0 {
		t.Fatal(stderr)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "ca.key"))
	code, stdout, stderr := execute(args...)
	after, _ := os.ReadFile(filepath.Join(dir, "ca.key"))
	if code != 1 || stdout != "" || stderr != "e2_eval_policy_file_exists\n" || !bytes.Equal(before, after) {
		t.Fatal(code, stdout, stderr)
	}
}

func TestCARequiresPrivateDirectory(t *testing.T) {
	cases := map[string]func(t *testing.T) string{
		"group-readable": func(t *testing.T) string {
			dir := filepath.Join(t.TempDir(), "open")
			if err := os.Mkdir(dir, 0700); err != nil || os.Chmod(dir, 0750) != nil {
				t.Fatal(err)
			}
			return dir
		},
		"missing": func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent") },
		"symlink": func(t *testing.T) string {
			link := filepath.Join(t.TempDir(), "link")
			if err := os.Symlink(privateDir(t), link); err != nil {
				t.Fatal(err)
			}
			return link
		},
	}
	for name, make := range cases {
		t.Run(name, func(t *testing.T) {
			dir := make(t)
			code, stdout, stderr := execute(caArgs(dir, "ca.pem", "ca.key", "run-1", soon())...)
			if code != 1 || stdout != "" || stderr != "e2_eval_policy_private_directory_required\n" {
				t.Fatal(code, stdout, stderr)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 && name != "symlink" {
				t.Fatal("files written", entries)
			}
		})
	}
}

func TestCARejectsBadInputWithoutEchoingIt(t *testing.T) {
	dir := privateDir(t)
	for name, tc := range map[string]struct {
		args []string
		exit int
		err  string
	}{
		"bad-ref":     {caArgs(dir, "ca.pem", "ca.key", "bad ref/secret-value", soon()), 1, "e2_eval_policy_input_invalid\n"},
		"expired":     {caArgs(dir, "ca.pem", "ca.key", "run-1", time.Now().Add(-time.Hour)), 1, "e2_eval_policy_input_invalid\n"},
		"too-long":    {caArgs(dir, "ca.pem", "ca.key", "run-1", time.Now().Add(25*time.Hour)), 1, "e2_eval_policy_input_invalid\n"},
		"bad-expires": {[]string{"ca", "--cert-file", dir + "/ca.pem", "--key-file", dir + "/ca.key", "--run-ref", "run-1", "--expires", "tomorrow-secret-value"}, 1, "e2_eval_policy_input_invalid\n"},
		"same-file":   {caArgs(dir, "ca.pem", "ca.pem", "run-1", soon()), 2, usage},
		"relative":    {[]string{"ca", "--cert-file", "ca.pem", "--key-file", "ca.key", "--run-ref", "run-1", "--expires", soon().Format(time.RFC3339)}, 2, usage},
		"two-dirs":    {[]string{"ca", "--cert-file", dir + "/ca.pem", "--key-file", dir + "/sub/ca.key", "--run-ref", "run-1", "--expires", soon().Format(time.RFC3339)}, 2, usage},
		"bad-name":    {caArgs(dir, ".ca.pem", "ca.key", "run-1", soon()), 2, usage},
		"extra-arg":   {append(caArgs(dir, "ca.pem", "ca.key", "run-1", soon()), "secret-value"), 2, usage},
		"unknown":     {[]string{"ca", "--secret-value"}, 2, usage},
	} {
		t.Run(name, func(t *testing.T) {
			code, stdout, stderr := execute(tc.args...)
			if code != tc.exit || stdout != "" || stderr != tc.err || strings.Contains(stderr, "secret-value") {
				t.Fatal(code, stdout, stderr)
			}
		})
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatal("files written", entries)
	}
}

func validPolicy(edit func(map[string]any)) map[string]any {
	digest := strings.Repeat("a", 64)
	now := time.Now().UTC()
	policy := map[string]any{
		"format": "abc2lab.e2-http-host-policy/v1", "approval_mode": "preauthorized_scope", "run_ref": "run-1", "reset_ref": "reset-1", "policy_ref": "policy-1",
		"case_ref": "case-1", "policy_revision": "revision-1", "budget_ref": "budget-1", "owner_ref": "owner-1", "account_ref": "account-1", "session_ref": "session-1",
		"valid_from": now.Add(-time.Minute).Format(time.RFC3339), "valid_until": now.Add(time.Hour).Format(time.RFC3339), "max_requests": 20,
		"header_mode": "preserve", "evaluation": nil, "state_dir": "/run/e2-state", "helper": "/usr/local/bin/helper", "helper_sha256": digest,
		"ca_cert": map[string]any{"file": "ca.pem", "sha256": digest}, "ca_key": map[string]any{"file": "ca.key", "sha256": digest},
		"rules": []any{map[string]any{"ref": "rule-1", "origin": "http://eval-meter:8080", "connect_ip": "172.30.0.10", "path_prefix": "/", "path_scope_ref": "scope-1", "methods": []any{"GET", "HEAD"},
			"approval_display": map[string]any{"public_path": true, "query_keys": []any{}, "json_fields": []any{}}}},
		"limits":  map[string]any{"max_attempts": 20, "max_calls": 5, "max_concurrent": 1, "max_body_bytes": 4096, "request_timeout_ms": 1000, "call_timeout_ms": 2000, "max_denials": 5, "max_transient": 5, "max_unknown": 1},
		"proxy":   map[string]any{"header_timeout_ms": 1000, "request_timeout_ms": 2000, "max_header_bytes": 4096, "max_connections": 4},
		"runtime": map[string]any{"max_worker_turns": 8, "max_tool_calls": 8, "worker_timeout_ms": 60000, "max_planner_rounds": 2, "max_intent_runs": 2, "drain_timeout_ms": 1000, "output_limit": 4096},
	}
	if edit != nil {
		edit(policy)
	}
	return policy
}

func writePolicy(t *testing.T, policy map[string]any) (string, []byte) {
	t.Helper()
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path, raw
}

func fingerprintOf(t *testing.T, edit func(map[string]any)) string {
	t.Helper()
	path, _ := writePolicy(t, validPolicy(edit))
	code, stdout, stderr := execute("fingerprint", "--policy-file", path)
	if code != 0 || stderr != "" {
		t.Fatal(code, stderr)
	}
	return stdout
}

func firstRule(p map[string]any) map[string]any { return p["rules"].([]any)[0].(map[string]any) }

func TestFingerprintPrintsTheContentDigest(t *testing.T) {
	path, raw := writePolicy(t, validPolicy(nil))
	want, err := server.E2HTTPPolicyContentFingerprint(raw, false)
	if err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := execute("fingerprint", "--policy-file", path)
	if code != 0 || stderr != "" || stdout != want+"\n" || !regexp.MustCompile(`^[0-9a-f]{64}\n$`).MatchString(stdout) {
		t.Fatal(code, stdout, stderr, want)
	}
}

// 러너가 자리값으로 계산한 예상 지문이 실제 회차 값과 같아야 비교가 성립한다.
func TestFingerprintIgnoresRunValues(t *testing.T) {
	base := fingerprintOf(t, nil)
	for name, edit := range map[string]func(map[string]any){
		"run_ref":   func(p map[string]any) { p["run_ref"] = "run-2" },
		"reset_ref": func(p map[string]any) { p["reset_ref"] = "reset-2" },
		"window":    func(p map[string]any) { p["valid_until"] = time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339) },
		"future_window": func(p map[string]any) {
			p["valid_from"], p["valid_until"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339), time.Now().Add(2*time.Hour).UTC().Format(time.RFC3339)
		},
		"expired_window": func(p map[string]any) {
			p["valid_from"], p["valid_until"] = time.Now().Add(-2*time.Hour).UTC().Format(time.RFC3339), time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
		},
		"state_dir":  func(p map[string]any) { p["state_dir"] = "/run/other-state" },
		"helper":     func(p map[string]any) { p["helper"] = "/opt/other"; p["helper_sha256"] = strings.Repeat("b", 64) },
		"ca_pin":     func(p map[string]any) { p["ca_cert"].(map[string]any)["sha256"] = strings.Repeat("c", 64) },
		"connect_ip": func(p map[string]any) { firstRule(p)["connect_ip"] = "172.30.0.11" },
	} {
		t.Run(name, func(t *testing.T) {
			if got := fingerprintOf(t, edit); got != base {
				t.Fatal("run value changed the fingerprint")
			}
		})
	}
}

func TestFingerprintChangesWithPolicyMeaning(t *testing.T) {
	base := fingerprintOf(t, nil)
	for name, edit := range map[string]func(map[string]any){
		"path":         func(p map[string]any) { firstRule(p)["path_prefix"] = "/api" },
		"methods":      func(p map[string]any) { firstRule(p)["methods"] = []any{"GET"} },
		"origin":       func(p map[string]any) { firstRule(p)["origin"] = "http://eval-meter:8081" },
		"max_requests": func(p map[string]any) { p["max_requests"] = 21 },
	} {
		t.Run(name, func(t *testing.T) {
			if got := fingerprintOf(t, edit); got == base {
				t.Fatal("meaning change kept the fingerprint")
			}
		})
	}
}

func TestFingerprintRefusesCredentialRules(t *testing.T) {
	path, _ := writePolicy(t, validPolicy(func(p map[string]any) {
		p["header_mode"] = "protected_session_proposal"
		firstRule(p)["credentials"] = map[string]any{"file": "credentials.json", "sha256": strings.Repeat("a", 64)}
	}))
	code, stdout, stderr := execute("fingerprint", "--policy-file", path)
	if code != 1 || stdout != "" || stderr != "e2_eval_policy_invalid\n" {
		t.Fatal(code, stdout, stderr)
	}
}

func TestFingerprintStageFlag(t *testing.T) {
	pin := map[string]any{"file": "audit.key", "sha256": strings.Repeat("d", 64)}
	prepared, _ := writePolicy(t, validPolicy(func(p map[string]any) { p["audit_key"] = pin }))
	code, stdout, stderr := execute("fingerprint", "--policy-file", prepared, "--prepared")
	if code != 0 || stdout != fingerprintOf(t, nil) {
		t.Fatal("audit pin changed the fingerprint", code, stderr)
	}
	code, stdout, stderr = execute("fingerprint", "--policy-file", prepared)
	if code != 1 || stdout != "" || stderr != "e2_eval_policy_invalid\n" {
		t.Fatal(code, stdout, stderr)
	}
	base, _ := writePolicy(t, validPolicy(nil))
	code, stdout, stderr = execute("fingerprint", "--policy-file", base, "--prepared")
	if code != 1 || stdout != "" || stderr != "e2_eval_policy_invalid\n" {
		t.Fatal(code, stdout, stderr)
	}
}

func TestFingerprintRejectsUnreadableAndInvalid(t *testing.T) {
	dir := t.TempDir()
	huge := filepath.Join(dir, "huge.json")
	if err := os.WriteFile(huge, bytes.Repeat([]byte{' '}, 256*1024+1), 0600); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(broken, []byte(`{"format":`), 0600); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		path string
		want string
	}{
		"missing":   {filepath.Join(dir, "absent.json"), "e2_eval_policy_unavailable\n"},
		"directory": {dir, "e2_eval_policy_unavailable\n"},
		"oversize":  {huge, "e2_eval_policy_invalid\n"},
		"broken":    {broken, "e2_eval_policy_invalid\n"},
	} {
		t.Run(name, func(t *testing.T) {
			code, stdout, stderr := execute("fingerprint", "--policy-file", tc.path)
			if code != 1 || stdout != "" || stderr != tc.want {
				t.Fatal(code, stdout, stderr)
			}
		})
	}
}

// FIFO를 열면 쓰는 쪽이 없을 때 영원히 멈춘다.
func TestFingerprintDoesNotOpenSpecialFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := execute("fingerprint", "--policy-file", path)
	if code != 1 || stdout != "" || stderr != "e2_eval_policy_unavailable\n" {
		t.Fatal(code, stdout, stderr)
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"unknown"}, {"fingerprint"}, {"fingerprint", "--policy-file", "x", "extra"}} {
		code, stdout, stderr := execute(args...)
		if code != 2 || stdout != "" || stderr != usage {
			t.Fatal(args, code, stdout, stderr)
		}
	}
	code, stdout, stderr := execute("--help")
	if code != 0 || stdout != usage || stderr != "" {
		t.Fatal(code, stdout, stderr)
	}
}
