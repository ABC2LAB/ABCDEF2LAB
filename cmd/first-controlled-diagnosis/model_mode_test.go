package main

import (
	"encoding/json"
	"testing"
)

func openRouterLaunchConfig() map[string]any {
	c := launchTestConfig()
	c["model_execution_mode"] = "openrouter"
	delete(c, "local_llm_manifest_path")
	c["openrouter_manifest_path"] = "/srv/diagnosis/private/openrouter.json"
	c["model_format"] = "openai"
	c["model_base_url"] = "https://openrouter.ai/api/v1"
	c["model_ref"] = "qwen/qwen3.5-35b-a3b"
	return c
}
func TestOpenRouterLaunchModeRequiresExplicitScope(t *testing.T) {
	good := openRouterLaunchConfig()
	data, _ := json.Marshal(good)
	c, err := parseLaunchConfig(data)
	model, modelErr := c.modelOptions()
	if err != nil || modelErr != nil || model.Mode != "openrouter" || c.LocalLLMManifestPath != "" {
		t.Fatal("explicit external configuration rejected")
	}
	cases := map[string]func(map[string]any){
		"missing-mode":               func(c map[string]any) { delete(c, "model_execution_mode") },
		"unknown-mode":               func(c map[string]any) { c["model_execution_mode"] = "external" },
		"conflicting-local-manifest": func(c map[string]any) { c["local_llm_manifest_path"] = "/srv/diagnosis/model.json" },
		"missing-manifest":           func(c map[string]any) { delete(c, "openrouter_manifest_path") },
		"relative-manifest":          func(c map[string]any) { c["openrouter_manifest_path"] = "model.json" },
		"changed-endpoint":           func(c map[string]any) { c["model_base_url"] = "https://example.com/api/v1" },
		"non-tls":                    func(c map[string]any) { c["model_base_url"] = "http://openrouter.ai/api/v1" },
		"changed-model":              func(c map[string]any) { c["model_ref"] = "other/model" },
		"changed-protocol":           func(c map[string]any) { c["model_format"] = "anthropic" },
		"registered-get": func(c map[string]any) {
			c["mode"] = "registered_get"
			delete(c, "general_http")
			c["registered_get"] = map[string]any{"authority_dsn_file": "/srv/diagnosis/authority.dsn", "host_binding_file": "/srv/diagnosis/binding.json", "metadata_file": "/srv/diagnosis/input.json", "snapshot_dir": "/srv/diagnosis/snapshots", "state_dir": "/srv/diagnosis/state", "registry_file": "registry.json"}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			c := openRouterLaunchConfig()
			change(c)
			data, _ := json.Marshal(c)
			if _, err := parseLaunchConfig(data); err != errPreparation {
				t.Fatalf("scope violation did not return preparation error: %v", err)
			}
		})
	}
	local := launchTestConfig()
	local["model_execution_mode"] = "local_only"
	data, _ = json.Marshal(local)
	if _, err := parseLaunchConfig(data); err != nil {
		t.Fatal("explicit local mode regressed")
	}
}
