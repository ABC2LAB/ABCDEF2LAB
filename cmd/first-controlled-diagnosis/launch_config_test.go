package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func launchTestConfig() map[string]any {
	return map[string]any{"format": launchFormat, "mode": "general_http", "listen_addr": "127.0.0.1:8080", "data_dir": "/srv/diagnosis/data", "key_dir": "/srv/diagnosis/keys", "skill_dir": "/srv/diagnosis/skills", "local_llm_manifest_path": "/srv/diagnosis/model.json", "model_profile_id": 1, "model_format": "openai", "model_base_url": "http://127.0.0.1:11434/v1", "model_ref": "fixture-model", "runtime_dsn_file": "/srv/diagnosis/runtime.dsn", "shutdown_timeout_seconds": 60, "general_http": map[string]any{"policy_file": "/srv/diagnosis/http/policy.json", "authority_file": "/srv/diagnosis/http/authority.json", "authority_root": "/srv/diagnosis/http"}}
}
func TestLaunchConfigKeepsModesSeparate(t *testing.T) {
	c := launchTestConfig()
	body, _ := json.Marshal(c)
	got, err := parseLaunchConfig(body)
	if err != nil || got.Registered != nil || got.HTTP == nil {
		t.Fatal("일반 HTTP가 등록 조회 자료를 요구했습니다")
	}
	c["mode"] = "registered_get"
	c["registered_get"] = map[string]any{"authority_dsn_file": "/srv/diagnosis/authority.dsn", "host_binding_file": "/srv/diagnosis/binding.json", "metadata_file": "/srv/diagnosis/input.json", "snapshot_dir": "/srv/diagnosis/snapshots", "state_dir": "/srv/diagnosis/state", "registry_file": "registry.json"}
	body, _ = json.Marshal(c)
	if _, err = parseLaunchConfig(body); err == nil {
		t.Fatal("두 모드 동시 구성을 수용했습니다")
	}
	delete(c, "general_http")
	body, _ = json.Marshal(c)
	if _, err = parseLaunchConfig(body); err != nil {
		t.Fatal("등록 조회 설정을 거부했습니다")
	}
}
func TestLaunchConfigRejectsAmbiguousOrOpenInput(t *testing.T) {
	good, _ := json.Marshal(launchTestConfig())
	tests := []string{strings.Replace(string(good), `"mode":"general_http"`, `"mode":"general_http","mode":"general_http"`, 1), strings.Replace(string(good), `"mode":`, `"Mode":`, 1), strings.Replace(string(good), `"general_http":{`, `"general_http":{"policy_file":"/other",`, 1), strings.Replace(string(good), `"general_http":{`, `"general_http":{"unexpected":true,`, 1), strings.Replace(string(good), `"model_profile_id":1`, `"model_profile_id":null`, 1), string(good) + "{}"}
	for i, body := range tests {
		if _, err := parseLaunchConfig([]byte(body)); err == nil {
			t.Fatalf("애매한 입력 %d 수용", i)
		}
	}
}
func TestLaunchConfigRejectsUnprotectedTopology(t *testing.T) {
	for _, change := range []func(map[string]any){func(c map[string]any) { c["key_dir"] = "/srv/diagnosis/data/keys" }, func(c map[string]any) { c["listen_addr"] = "0.0.0.0:8080" }, func(c map[string]any) { c["shutdown_timeout_seconds"] = 61 }, func(c map[string]any) { c["runtime_dsn_file"] = "relative" }} {
		c := launchTestConfig()
		change(c)
		body, _ := json.Marshal(c)
		if _, err := parseLaunchConfig(body); err == nil {
			t.Fatal("잘못된 기동 경계를 수용했습니다")
		}
	}
}
