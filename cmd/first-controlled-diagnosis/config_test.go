package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func validTestConfig() preparationConfig {
	return preparationConfig{ListenAddr: "127.0.0.1:8787", DataDir: "/private/data", KeyDir: "/private/keys", SkillDir: "/private/skills", SnapshotDir: "/private/snapshots", StateDir: "/private/state", LocalLLMManifestPath: "/private/pins.json", TargetOrigin: "http://target.invalid", ModelProfileID: 7, ModelFormat: "openai", ModelBaseURL: "http://model.internal:11434/v1", ModelRef: "local:test", ShutdownTimeoutSeconds: 30}
}

func TestConfigStrictSchemaAndNonEvaluation(t *testing.T) {
	c := validTestConfig()
	body, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := parseConfig(body); err != nil || got != c {
		t.Fatal("valid configuration refused")
	}
	for name, bad := range map[string]string{
		"unknown":      strings.Replace(string(body), `"listen_addr"`, `"credential"`, 1),
		"case":         strings.Replace(string(body), `"listen_addr"`, `"LISTEN_ADDR"`, 1),
		"duplicate":    strings.Replace(string(body), `"listen_addr":`, `"listen_addr":"127.0.0.1:1","listen_addr":`, 1),
		"trailing":     string(body) + ` {}`,
		"null":         strings.Replace(string(body), `"eval_run_id":""`, `"eval_run_id":null`, 1),
		"evaluation":   strings.Replace(string(body), `"eval_run_id":""`, `"eval_run_id":"not-this-run"`, 1),
		"array":        "[]",
		"empty":        "",
		"oversize":     strings.Repeat(" ", maxConfigBytes+1),
		"invalid-utf8": string([]byte{0xff}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseConfig([]byte(bad)); err != errPreparation {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestConfigRejectsUnsafeOrIncompleteMetadata(t *testing.T) {
	for _, name := range []string{"relative", "unclean", "nul", "key-equals-data", "key-inside-data", "wildcard-listener", "zero-port", "invalid-port", "target-userinfo", "target-query", "target-empty-query", "target-fragment", "target-path", "target-encoded-root", "target-scheme", "model-missing", "profile-missing", "timeout-zero", "timeout-limit"} {
		t.Run(name, func(t *testing.T) {
			c := validTestConfig()
			switch name {
			case "relative":
				c.DataDir = "data"
			case "unclean":
				c.DataDir = "/private/../data"
			case "nul":
				c.DataDir = "/private\x00data"
			case "key-equals-data":
				c.KeyDir = c.DataDir
			case "key-inside-data":
				c.KeyDir = c.DataDir + "/keys"
			case "wildcard-listener":
				c.ListenAddr = "0.0.0.0:8787"
			case "zero-port":
				c.ListenAddr = "127.0.0.1:0"
			case "invalid-port":
				c.ListenAddr = "127.0.0.1:no"
			case "target-userinfo":
				c.TargetOrigin = "http://PRIVATE-USER:PRIVATE-TOKEN@target.invalid"
			case "target-query":
				c.TargetOrigin = "http://target.invalid?token=PRIVATE"
			case "target-empty-query":
				c.TargetOrigin = "http://target.invalid?"
			case "target-fragment":
				c.TargetOrigin = "http://target.invalid#"
			case "target-path":
				c.TargetOrigin = "http://target.invalid/api"
			case "target-encoded-root":
				c.TargetOrigin = "http://target.invalid/%2f"
			case "target-scheme":
				c.TargetOrigin = "ftp://target.invalid"
			case "model-missing":
				c.ModelRef = ""
			case "profile-missing":
				c.ModelProfileID = 0
			case "timeout-zero":
				c.ShutdownTimeoutSeconds = 0
			case "timeout-limit":
				c.ShutdownTimeoutSeconds = 301
			}
			if validConfig(c) {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
}
