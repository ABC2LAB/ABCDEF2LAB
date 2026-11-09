//go:build linux

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func commandFixture(t *testing.T) (string, []byte) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"prep", "control", "evidence"} {
		if err := os.Mkdir(filepath.Join(root, p), 0700); err != nil {
			t.Fatal(err)
		}
	}
	prep := filepath.Join(root, "prep")
	config := map[string]any{"format": "abc2lab.e2-runner-config/v1", "eval_run_id": "synthetic-eval", "reservation_ref": "synthetic-reservation", "model_tag": "synthetic-model", "header_mode": "preserve", "evaluation": nil, "policy_file": "policy.json", "audit_key_file": "audit.key", "listen": ":8123", "upstream": "http://eval-meter-upstream:8123", "control_dir": filepath.Join(root, "control"), "evidence_dir": filepath.Join(root, "evidence")}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"e2-config.json": raw, "policy.json": []byte(`{"approval_mode":"preauthorized_scope"}`), "audit.key": bytes.Repeat([]byte{7}, 32)} {
		if err := os.WriteFile(filepath.Join(prep, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(prep, "e2-config.json"), raw
}
func TestValidInputsAreNotServiceReadiness(t *testing.T) {
	path, raw := commandFixture(t)
	var out, errout bytes.Buffer
	if run([]string{"--config", path}, &out, &errout) != 0 || errout.Len() != 0 {
		t.Fatal("준비 입력 검사 실패")
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if result["state"] != "inputs_validated" || result["service_ready"] != false || result["product_policy_validated"] != false || result["config_sha256"] != hex.EncodeToString(sum[:]) || len(result) != 5 {
		t.Fatal("준비 입력을 제품 준비/승인으로 승격함")
	}
	for _, forbidden := range [][]byte{[]byte(path), []byte("synthetic-reservation"), []byte("synthetic-model"), []byte("audit.key")} {
		if bytes.Contains(out.Bytes(), forbidden) {
			t.Fatal("보호 입력 노출")
		}
	}
	for _, name := range []string{"control", "evidence"} {
		files, err := os.ReadDir(filepath.Join(filepath.Dir(filepath.Dir(path)), name))
		if err != nil || len(files) != 0 {
			t.Fatal("검사가 실행 자원을 생성함")
		}
	}
}
func TestValidInputOutputFailure(t *testing.T) {
	path, _ := commandFixture(t)
	if run([]string{"--config", path}, brokenWriter{}, io.Discard) != 1 {
		t.Fatal("출력 실패 수용")
	}
}
