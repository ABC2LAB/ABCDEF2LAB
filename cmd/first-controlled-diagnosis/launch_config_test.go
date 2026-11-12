package main

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
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

// 상태 조회 재시도(C10·C32): 일반 HTTP launch만 받고, 다시 묻는 시간(횟수 × 조회 간격)이 정리 시간 상한 안이어야 한다.
func TestLaunchConfigStatusReadRetries(t *testing.T) {
	parse := func(change func(map[string]any)) (launchConfig, error) {
		c := launchTestConfig()
		change(c)
		body, _ := json.Marshal(c)
		return parseLaunchConfig(body)
	}
	// launchTestConfig의 정리 시간 상한은 60초다.
	upper := urlMaxStatusReadRetries(60)
	if upper*int(urlStatusPollInterval) != 60*int(time.Second) {
		t.Fatalf("상한 계산이 정리 시간과 다름: %d", upper)
	}
	// 칸이 없으면 0이다(명시한 0은 다른 선택 칸처럼 엄격 해석이 거부한다).
	if got, err := parse(func(map[string]any) {}); err != nil || got.StatusReadRetries != 0 {
		t.Fatal("칸이 없는 launch의 재시도가 0이 아님")
	}
	for _, n := range []int{1, upper} {
		if got, err := parse(func(c map[string]any) { c["status_read_retries"] = n }); err != nil || got.StatusReadRetries != n {
			t.Fatalf("유효한 재시도 %d를 거부함", n)
		}
	}
	for name, change := range map[string]func(map[string]any){
		"over": func(c map[string]any) { c["status_read_retries"] = upper + 1 },
		"over-shutdown": func(c map[string]any) {
			c["shutdown_timeout_seconds"], c["status_read_retries"] = 1, urlMaxStatusReadRetries(1)+1
		},
		"negative": func(c map[string]any) { c["status_read_retries"] = -1 },
		"registered-mode": func(c map[string]any) {
			c["mode"] = "registered_get"
			delete(c, "general_http")
			c["registered_get"] = map[string]any{"authority_dsn_file": "/srv/diagnosis/authority.dsn", "host_binding_file": "/srv/diagnosis/binding.json", "metadata_file": "/srv/diagnosis/input.json", "snapshot_dir": "/srv/diagnosis/snapshots", "state_dir": "/srv/diagnosis/state", "registry_file": "registry.json"}
			c["status_read_retries"] = 1
		},
	} {
		if _, err := parse(change); err == nil {
			t.Fatalf("%s: 잘못된 상태 조회 재시도를 수용함", name)
		}
	}
}

// Plan당 상한(C26·C32): 일반 HTTP launch만 받고, 0은 기본값, 음수·코드 상한 밖·다른 모드는 거부한다.
func TestLaunchConfigPlannerLimits(t *testing.T) {
	parse := func(change func(map[string]any)) (launchConfig, error) {
		c := launchTestConfig()
		change(c)
		body, _ := json.Marshal(c)
		return parseLaunchConfig(body)
	}
	got, err := parse(func(c map[string]any) { c["planner_turns_per_plan"], c["planner_seconds_per_plan"] = 40, 300 })
	limits, ok := got.plannerLimits()
	if err != nil || !ok || limits.MaxTurns != 40 || limits.MaxDuration != 300*time.Second {
		t.Fatalf("유효한 Plan당 상한을 거부함 err=%v limits=%+v", err, limits)
	}
	// 준비 서버는 모델 설정 묶음으로, 실행 서버는 host 설정으로 같은 값을 받는다.
	host, hostOK := got.httpHostConfig()
	if got.modelOptions().PlannerLimits != limits || !hostOK || host.PlannerLimits != limits || host.PolicyFile != got.HTTP.PolicyFile || host.AuthorityFile != got.HTTP.AuthorityFile || host.AuthorityRoot != got.HTTP.AuthorityRoot {
		t.Fatal("준비·실행 서버 설정에 Plan당 상한이 빠짐")
	}
	if _, ok := (launchConfig{HTTP: got.HTTP, PlannerTurnsPerPlan: 1001}).httpHostConfig(); ok {
		t.Fatal("범위 밖 Plan당 상한으로 실행 host 설정을 만듦")
	}
	got, err = parse(func(map[string]any) {})
	if limits, ok := got.plannerLimits(); err != nil || !ok || limits != (agent.E2HTTPPlannerLimits{}) {
		t.Fatal("칸이 없는 launch가 기본값이 아님")
	}
	upper, _ := parse(func(c map[string]any) {
		c["planner_turns_per_plan"], c["planner_seconds_per_plan"] = 1000, int64(time.Hour/time.Second)
	})
	if limits, ok := upper.plannerLimits(); !ok || limits.MaxTurns != 1000 || limits.MaxDuration != time.Hour {
		t.Fatal("코드 상한 값을 거부함")
	}
	for name, change := range map[string]func(map[string]any){
		"turns-negative":   func(c map[string]any) { c["planner_turns_per_plan"] = -1 },
		"turns-over":       func(c map[string]any) { c["planner_turns_per_plan"] = 1001 },
		"seconds-negative": func(c map[string]any) { c["planner_seconds_per_plan"] = -1 },
		"seconds-over":     func(c map[string]any) { c["planner_seconds_per_plan"] = int64(time.Hour/time.Second) + 1 },
		"seconds-overflow": func(c map[string]any) { c["planner_seconds_per_plan"] = int64(math.MaxInt64) },
		"registered-mode": func(c map[string]any) {
			c["mode"] = "registered_get"
			delete(c, "general_http")
			c["registered_get"] = map[string]any{"authority_dsn_file": "/srv/diagnosis/authority.dsn", "host_binding_file": "/srv/diagnosis/binding.json", "metadata_file": "/srv/diagnosis/input.json", "snapshot_dir": "/srv/diagnosis/snapshots", "state_dir": "/srv/diagnosis/state", "registry_file": "registry.json"}
			c["planner_turns_per_plan"] = 40
		},
	} {
		if _, err := parse(change); err == nil {
			t.Fatalf("%s: 잘못된 Plan당 상한을 수용함", name)
		}
	}
}
