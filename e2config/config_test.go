//go:build linux

package e2config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func fixture(t *testing.T) (string, map[string]any) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"prep", "control", "evidence"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	obj := map[string]any{"format": Format, "eval_run_id": "run-1", "reservation_ref": "reservation-1", "model_tag": "model:tag", "header_mode": "preserve", "evaluation": nil, "policy_file": "policy.json", "audit_key_file": "audit.key", "listen": ":8123", "upstream": "http://eval-meter-upstream:8123", "control_dir": filepath.Join(root, "control"), "evidence_dir": filepath.Join(root, "evidence")}
	path := filepath.Join(root, "prep", "e2-config.json")
	write(t, filepath.Join(root, "prep", "policy.json"), []byte(`{"approval_mode":"preauthorized_scope"}`))
	write(t, filepath.Join(root, "prep", "audit.key"), bytes.Repeat([]byte{19}, 32))
	put(t, path, obj)
	return path, obj
}
func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func put(t *testing.T, path string, obj map[string]any) {
	t.Helper()
	data, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, data)
}
func reject(t *testing.T, path string) {
	t.Helper()
	if c, err := Load(path); err != ErrInvalid || c != nil {
		t.Fatal("invalid configuration was accepted")
	}
}

func TestLoadAndExactDigests(t *testing.T) {
	path, _ := fixture(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	write(t, path, raw)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.ConfigSHA256 != digest(raw) || c.ConfigSHA256 == c.PolicySHA256 {
		t.Fatal("raw digest binding failed")
	}
	policy, err := c.ReadPolicy()
	if err != nil || c.PolicySHA256 != digest(policy) {
		t.Fatal("policy digest failed")
	}
	clear(policy)
	key, err := c.ReadAuditKey()
	if err != nil || len(key) != 32 {
		t.Fatal("key failed")
	}
	clear(key)
	if fmt.Sprintf("%+v %#v", c, c) != "e2config.Config[protected] e2config.Config[protected]" {
		t.Fatal("unsafe formatting")
	}
	encoded, err := json.Marshal(c)
	if err != nil || strings.Contains(string(encoded), "SHA256") || strings.Contains(string(encoded), "PolicyPath") {
		t.Fatal("private metadata exposed")
	}
}
func TestRejectConfigMutations(t *testing.T) {
	tests := map[string]func(map[string]any){
		"unknown":               func(o map[string]any) { o["secret"] = "value" },
		"wrong-case":            func(o map[string]any) { o["Format"] = o["format"]; delete(o, "format") },
		"missing":               func(o map[string]any) { delete(o, "reservation_ref") },
		"version":               func(o map[string]any) { o["format"] = "abc2lab.e2-runner-config/v2" },
		"null":                  func(o map[string]any) { o["model_tag"] = nil },
		"type":                  func(o map[string]any) { o["listen"] = 8123 },
		"url-ref":               func(o map[string]any) { o["model_tag"] = "http://example" },
		"traversal":             func(o map[string]any) { o["policy_file"] = "../policy.json" },
		"absolute":              func(o map[string]any) { o["audit_key_file"] = "/audit.key" },
		"same-file":             func(o map[string]any) { o["audit_key_file"] = "policy.json" },
		"header-mode":           func(o map[string]any) { o["header_mode"] = "other" },
		"unexpected-evaluation": func(o map[string]any) { o["evaluation"] = map[string]any{} },
		"missing-evaluation":    func(o map[string]any) { o["header_mode"] = "evaluation_caller_passthrough" },
		"extra-evaluation": func(o map[string]any) {
			o["header_mode"] = "evaluation_caller_passthrough"
			o["evaluation"] = map[string]any{"llm_backend": "api", "target_kind": "public_practice", "extra": "x"}
		},
		"port-zero":      func(o map[string]any) { o["listen"] = ":0" },
		"port-large":     func(o map[string]any) { o["listen"] = ":65536" },
		"port-mismatch":  func(o map[string]any) { o["upstream"] = "http://eval-meter-upstream:8124" },
		"userinfo":       func(o map[string]any) { o["upstream"] = "http://user:secret@eval-meter-upstream:8123" },
		"path":           func(o map[string]any) { o["upstream"] = "http://eval-meter-upstream:8123/path" },
		"query":          func(o map[string]any) { o["upstream"] = "http://eval-meter-upstream:8123?token=x" },
		"same-directory": func(o map[string]any) { o["control_dir"] = o["evidence_dir"] },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) { path, o := fixture(t); mutate(o); put(t, path, o); reject(t, path) })
	}
}
func TestRejectMalformedJSON(t *testing.T) {
	tests := map[string]func([]byte) []byte{
		"duplicate":         func(b []byte) []byte { return append([]byte(`{"format":"bad",`), b[1:]...) },
		"invalid-utf8":      func(b []byte) []byte { return bytes.Replace(b, []byte("model:tag"), []byte{0xff}, 1) },
		"trailing-document": func(b []byte) []byte { return append(b, []byte(" {}")...) },
		"too-large":         func(b []byte) []byte { return append(b, bytes.Repeat([]byte(" "), MaxConfigBytes)...) },
		"nested-duplicate": func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"evaluation":null`), []byte(`"evaluation":{"llm_backend":"api","llm_backend":"local","target_kind":"public_practice"}`), 1)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			path, _ := fixture(t)
			raw, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			write(t, path, mutate(raw))
			reject(t, path)
		})
	}
}
func TestRejectProtectedFiles(t *testing.T) {
	for _, name := range []string{"e2-config.json", "policy.json", "audit.key"} {
		for _, mutation := range []string{"public-mode", "symlink", "hardlink", "fifo", "empty", "owner"} {
			t.Run(name+"/"+mutation, func(t *testing.T) {
				path, _ := fixture(t)
				target := filepath.Join(filepath.Dir(path), name)
				var err error
				switch mutation {
				case "public-mode":
					err = os.Chmod(target, 0644)
				case "symlink":
					err = os.Rename(target, target+".old")
					if err == nil {
						err = os.Symlink(target+".old", target)
					}
				case "hardlink":
					err = os.Link(target, target+".link")
				case "fifo":
					err = os.Remove(target)
					if err == nil {
						err = unix.Mkfifo(target, 0600)
					}
				case "empty":
					err = os.Truncate(target, 0)
				case "owner":
					if os.Geteuid() != 0 {
						t.Skip("owner mutation requires root")
					}
					err = os.Chown(target, 65534, -1)
				}
				if err != nil {
					t.Fatal(err)
				}
				reject(t, path)
			})
		}
	}
}
func TestRejectDirectories(t *testing.T) {
	for _, name := range []string{"prep", "control", "evidence"} {
		for _, mutation := range []string{"mode", "symlink", "ancestor-symlink"} {
			t.Run(name+"/"+mutation, func(t *testing.T) {
				path, _ := fixture(t)
				root := filepath.Dir(filepath.Dir(path))
				dir := filepath.Join(root, name)
				var err error
				switch mutation {
				case "mode":
					err = os.Chmod(dir, 0755)
				case "symlink":
					err = os.Rename(dir, dir+".old")
					if err == nil {
						err = os.Symlink(dir+".old", dir)
					}
				case "ancestor-symlink":
					err = os.Symlink(root, root+"-link")
					if err == nil {
						t.Cleanup(func() { _ = os.Remove(root + "-link") })
						path = filepath.Join(root+"-link", "prep", "e2-config.json")
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				reject(t, path)
			})
		}
	}
}
func TestRecheckRejectsReplacement(t *testing.T) {
	for _, name := range []string{"e2-config.json", "policy.json", "audit.key", "control", "evidence", "prep"} {
		t.Run(name, func(t *testing.T) {
			path, _ := fixture(t)
			c, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(filepath.Dir(path), name)
			if name == "control" || name == "evidence" || name == "prep" {
				target = filepath.Join(filepath.Dir(filepath.Dir(path)), name)
				if err = os.Rename(target, target+".old"); err == nil {
					err = os.Mkdir(target, 0700)
				}
			} else {
				var raw []byte
				raw, err = os.ReadFile(target)
				if err == nil {
					err = os.Rename(target, target+".old")
				}
				if err == nil {
					err = os.WriteFile(target, raw, 0600)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.Check() != ErrInvalid {
				t.Fatal("replacement accepted")
			}
			if b, e := c.ReadAuditKey(); e != ErrInvalid || b != nil {
				t.Fatal("replacement key exposed")
			}
		})
	}
}
func TestRejectActualPolicyAndKey(t *testing.T) {
	for _, raw := range []string{`{"approval_mode":"confirm_every_request"}`, `{"approval_mode":null}`, `{"approval_mode":42}`, `{"approval_mode":"preauthorized_scope","approval_mode":"preauthorized_scope"}`, `[]`, `null`} {
		t.Run(raw, func(t *testing.T) {
			path, _ := fixture(t)
			write(t, filepath.Join(filepath.Dir(path), "policy.json"), []byte(raw))
			reject(t, path)
		})
	}
	for _, size := range []int{31, 33} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			path, _ := fixture(t)
			write(t, filepath.Join(filepath.Dir(path), "audit.key"), make([]byte, size))
			reject(t, path)
		})
	}
}
func TestPythonProducerCompatibility(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"preserve", "protected_session_proposal", "evaluation_caller_passthrough"} {
		t.Run(mode, func(t *testing.T) {
			path, o := fixture(t)
			prep := filepath.Dir(path)
			for _, name := range []string{"policy.json", "audit.key", "e2-config.json"} {
				if err := os.Remove(filepath.Join(prep, name)); err != nil {
					t.Fatal(err)
				}
			}
			source := filepath.Join(filepath.Dir(prep), "source-policy.json")
			write(t, source, []byte("{\n  \"approval_mode\": \"preauthorized_scope\"\n}\n"))
			script := `import sys
from pathlib import Path
sys.path.insert(0,sys.argv[1])
import e2_prepare as p
p.CONTAINER_CONTROL_DIR=sys.argv[4]
p.CONTAINER_EVIDENCE_DIR=sys.argv[5]
mode=sys.argv[6]
ev={"llm_backend":"api","target_kind":"public_practice"} if mode=="evaluation_caller_passthrough" else None
p.prepare_e2_config(Path(sys.argv[2]),p.E2RunValues("run-1","model:tag",mode,ev,8123),Path(sys.argv[3]))
`
			cmd := exec.Command(python, "-I", "-c", script, filepath.Join(cwd, "..", "scripts", "eval"), prep, source, o["control_dir"].(string), o["evidence_dir"].(string), mode)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("producer failed: %v, %s", err, output)
			}
			c, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if c.ConfigSHA256 != digest(raw) || c.HeaderMode != mode {
				t.Fatal("producer mismatch")
			}
		})
	}
}

func TestOptionalPolicyNull(t *testing.T) {
	path, _ := fixture(t)
	write(t, filepath.Join(filepath.Dir(path), "policy.json"), []byte(`{"approval_mode":"preauthorized_scope","evaluation":null,"credentials":null}`))
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
}

func TestRejectEveryRequiredFieldNull(t *testing.T) {
	_, obj := fixture(t)
	for name := range obj {
		if name == "evaluation" {
			continue
		}
		t.Run(name, func(t *testing.T) { path, o := fixture(t); o[name] = nil; put(t, path, o); reject(t, path) })
	}
}
func TestRejectOversizedPolicy(t *testing.T) {
	path, _ := fixture(t)
	write(t, filepath.Join(filepath.Dir(path), "policy.json"), append([]byte(`{"approval_mode":"preauthorized_scope"}`), bytes.Repeat([]byte(" "), MaxPolicyBytes)...))
	reject(t, path)
}
func TestRecheckRejectsInPlaceMutation(t *testing.T) {
	for _, name := range []string{"e2-config.json", "policy.json", "audit.key"} {
		t.Run(name, func(t *testing.T) {
			path, _ := fixture(t)
			c, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(filepath.Dir(path), name)
			data, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			data[len(data)-1] ^= 1
			write(t, target, data)
			if c.Check() != ErrInvalid {
				t.Fatal("in-place mutation accepted")
			}
		})
	}
}
func TestPolicyStrictStructure(t *testing.T) {
	for _, raw := range [][]byte{[]byte(`{"approval_mode":"preauthorized_scope","nested":{"x":1,"x":2}}`), []byte(`{"approval_mode":"preauthorized_scope"} {}`), append([]byte(`{"approval_mode":"preauthorized_scope","x":"`), 0xff, '"', '}')} {
		path, _ := fixture(t)
		write(t, filepath.Join(filepath.Dir(path), "policy.json"), raw)
		reject(t, path)
	}
}

func TestRejectNoncanonicalUpstream(t *testing.T) {
	for _, upstream := range []string{"http://localhost:8123", "http://127.0.0.1:8123", "http://other-target:8123", "http://EVAL-METER-UPSTREAM:8123", "HTTP://eval-meter-upstream:8123", "http://eval-meter-upstream.:8123", "http://eval-meter-upstream:08123", "http://eval-meter-upstream:8123/", "http://eval-meter-upstream:8123#", "http://eval-meter-upstream:8123?", "http://eval-meter-upstream:8123/#fragment"} {
		t.Run(upstream, func(t *testing.T) { path, o := fixture(t); o["upstream"] = upstream; put(t, path, o); reject(t, path) })
	}
}
func TestRejectMutatedLoadedConfig(t *testing.T) {
	tests := map[string]func(*Config){
		"format":            func(c *Config) { c.Format = "other" },
		"run":               func(c *Config) { c.EvalRunID = "other" },
		"reservation":       func(c *Config) { c.ReservationRef = "other" },
		"model":             func(c *Config) { c.ModelTag = "other" },
		"header":            func(c *Config) { c.HeaderMode = "preserve" },
		"evaluation":        func(c *Config) { c.Evaluation.LLMBackend = "local" },
		"evaluation-target": func(c *Config) { c.Evaluation.TargetKind = "other" },
		"evaluation-nil":    func(c *Config) { c.Evaluation = nil },
		"policy-file":       func(c *Config) { c.PolicyFile = "other.json" },
		"key-file":          func(c *Config) { c.AuditKeyFile = "other.key" },
		"listen":            func(c *Config) { c.Listen = ":9999" },
		"upstream":          func(c *Config) { c.Upstream = "http://localhost:8123" },
		"evidence":          func(c *Config) { c.EvidenceDir += "/other" },
		"control":           func(c *Config) { c.ControlDir += "/other" },
		"config-sha":        func(c *Config) { c.ConfigSHA256 = "other" },
		"policy-sha":        func(c *Config) { c.PolicySHA256 = "other" },
		"policy-path":       func(c *Config) { c.PolicyPath += ".other" },
		"key-path":          func(c *Config) { c.AuditKeyPath += ".other" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			path, o := fixture(t)
			o["header_mode"] = "evaluation_caller_passthrough"
			o["evaluation"] = map[string]any{"llm_backend": "api", "target_kind": "public_practice"}
			put(t, path, o)
			c, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			mutate(c)
			if c.Check() != ErrInvalid {
				t.Fatal("mutated exported config accepted")
			}
			if raw, err := c.ReadPolicy(); err != ErrInvalid || raw != nil {
				t.Fatal("policy exposed for mutated config")
			}
			if raw, err := c.ReadAuditKey(); err != ErrInvalid || raw != nil {
				t.Fatal("key exposed for mutated config")
			}
		})
	}
}
func TestRejectNestedDirectories(t *testing.T) {
	for _, pair := range [][2]string{{"control_dir", "evidence_dir"}, {"evidence_dir", "control_dir"}, {"prep", "control_dir"}, {"prep", "evidence_dir"}} {
		t.Run(pair[0]+"/"+pair[1], func(t *testing.T) {
			path, o := fixture(t)
			parent := filepath.Dir(path)
			if pair[0] != "prep" {
				parent = o[pair[0]].(string)
			}
			child := filepath.Join(parent, "nested")
			if err := os.Mkdir(child, 0700); err != nil {
				t.Fatal(err)
			}
			o[pair[1]] = child
			put(t, path, o)
			reject(t, path)
		})
	}
	for _, field := range []string{"control_dir", "evidence_dir"} {
		t.Run(field+"/ancestor-of-prep", func(t *testing.T) {
			path, o := fixture(t)
			o[field] = filepath.Dir(filepath.Dir(path))
			put(t, path, o)
			reject(t, path)
		})
	}
}
