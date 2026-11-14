//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestProductInstallRejectsUnapprovedInputBeforeSideEffects(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("installer deliberately rejects root")
	}
	for _, change := range []map[string]any{
		{"format": "unknown"}, {"admin_password": "short"}, {"nonmutating_scope_acknowledged": false},
		{"target_origin": "http://juice.test:9443"}, {"target_origin": "https://u:p@juice.test:9443"},
		{"target_origin": "https://juice.test:9443?x=1"}, {"target_ip": "127.0.0.1", "allow_private_target": false},
		{"path_prefixes": []string{"/%2e"}}, {"target_policy": map[string]any{"allowed_ports": []int{0}}}, {"target_policy": map[string]any{"private_cidrs": []string{"0.0.0.0/0"}}}, {"valid_for_hours": 8761}, {"max_runs": 100001}, {"unexpected": true},
		{"runtime_limits": map[string]any{"product": map[string]any{"run_seconds": 3601}}}, {"runtime_limits": map[string]any{"product": map[string]any{"run_seconds": 0}}}, {"runtime_limits": map[string]any{"product": map[string]any{"model_concurrency": 2}}}, {"runtime_limits": map[string]any{"product": map[string]any{"planner_turns_per_plan": 0}}}, {"runtime_limits": map[string]any{"product": map[string]any{"planner_seconds_per_plan": 3601}}}, {"runtime_limits": map[string]any{"product": map[string]any{"status_read_retries": 201}}},
	} {
		root := t.TempDir()
		if os.Chmod(root, 0700) != nil {
			t.Fatal("fixture permissions")
		}
		c := map[string]any{"format": "abc2lab.product-install/v2", "database_host": "192.0.2.10", "database_name": "abc2lab", "admin_password": "synthetic-only-install-password", "valid_for_hours": 24, "max_runs": 1, "local_model": map[string]any{"model": "owned-synthetic", "base_url": "http://192.0.2.30:8080/v1", "addresses": []string{"192.0.2.30"}, "context_window_k": 128, "max_tokens": 2048}}
		input := filepath.Join(root, "input.json")
		valid, _ := json.Marshal(c)
		if os.WriteFile(input, valid, 0600) != nil {
			t.Fatal("private fixture")
		}
		var baselineOut, baselineErr bytes.Buffer
		// The valid baseline reaches missing private-directory setup (1), rather
		// than another input guard (2), before any DB or persistent state exists.
		if runProductInstall(t.Context(), []string{"-input", input, "-root", root}, &baselineOut, &baselineErr) != 1 {
			t.Fatal("invalid baseline hides input guard removal")
		}
		for k, v := range change {
			c[k] = v
		}
		raw, _ := json.Marshal(c)
		if os.WriteFile(input, raw, 0600) != nil {
			t.Fatal("private fixture")
		}
		var stdout, stderr bytes.Buffer
		if code := runProductInstall(t.Context(), []string{"-input", input, "-root", root}, &stdout, &stderr); code != 2 {
			t.Fatal("unapproved install input accepted", change, code)
		}
		if _, e := os.Stat(filepath.Join(root, "config", ".installing")); !os.IsNotExist(e) {
			t.Fatal("rejected install created persistent state")
		}
		if stdout.Len() != 0 || stderr.Len() != 0 {
			t.Fatal("rejected input leaked details")
		}
	}
}

func TestProductPrivateWriteNeverOverwrites(t *testing.T) {
	p := filepath.Join(t.TempDir(), "authority")
	if productWrite(p, []byte("original")) != nil || productWrite(p, []byte("replacement")) == nil {
		t.Fatal("protected write did not enforce exclusive creation")
	}
	raw, e := os.ReadFile(p)
	info, se := os.Stat(p)
	if e != nil || se != nil || string(raw) != "original" || info.Mode().Perm() != 0600 {
		t.Fatal("protected material changed")
	}
}

func TestProductPreflightRejectsCallerCommands(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if runProductPreflight(t.Context(), []string{"-command", "curl https://unapproved.invalid"}, &stdout, &stderr) != 2 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatal("preflight accepted caller-selected execution")
	}
}

func TestProductPinWriteFailureRemainsLatched(t *testing.T) {
	for _, failed := range []string{"ca.pem", "ca-key.pem", "audit-key", "upstream.pem"} {
		t.Run(failed, func(t *testing.T) {
			root := t.TempDir()
			if os.Mkdir(filepath.Join(root, failed), 0700) != nil {
				t.Fatal("fixture")
			}
			pins := productPinWriter{dir: root}
			for _, name := range []string{"ca.pem", "ca-key.pem", "audit-key", "upstream.pem"} {
				pins.write(name, []byte("synthetic-only"))
			}
			if pins.err == nil {
				t.Fatal("write failure lost")
			}
			pins.write("after-failure", []byte("must not persist"))
			if _, err := os.Stat(filepath.Join(root, "after-failure")); !os.IsNotExist(err) {
				t.Fatal("continued after failed pin")
			}
		})
	}
	root := t.TempDir()
	pins := productPinWriter{dir: root}
	pin := pins.write("ca.pem", []byte("synthetic-only"))
	if pins.err != nil || pin.SHA256 != urlSHA([]byte("synthetic-only")) {
		t.Fatal("valid write failed")
	}
}

func TestProductAPIProjectionPublishesOnlyAllowlistedInputs(t *testing.T) {
	root := t.TempDir()
	if os.Chmod(root, 0700) != nil {
		t.Fatal("fixture")
	}
	files := map[string][]byte{"config/api.dsn": []byte("synthetic-api-only"), "keys/jwt.key": []byte("synthetic-jwt")}
	if productPublishAPIProjection(root, files) != nil || productPublishAPIProjection(root, files) != nil {
		t.Fatal("publish/restart")
	}
	for _, name := range []string{"runtime.dsn", "ca-key.pem", "audit-key"} {
		if _, err := os.Stat(filepath.Join(root, "api", "config", name)); !os.IsNotExist(err) {
			t.Fatal("execution secret projected")
		}
	}
	if os.WriteFile(filepath.Join(root, "api", "config", "api.dsn"), []byte("changed"), 0600) != nil {
		t.Fatal("fixture")
	}
	if productPublishAPIProjection(root, files) == nil {
		t.Fatal("changed projection overwritten")
	}
}

func TestProductAPIProjectionCopiesBoundInstallationWithoutExecutionSecrets(t *testing.T) {
	f := newURLNegativeFixture(t)
	root := filepath.Dir(f.path)
	config := filepath.Join(root, "config")
	if os.Mkdir(config, 0700) != nil {
		t.Fatal("fixture")
	}
	f.path = filepath.Join(config, "installation.json")
	f.config.APILaunch.File = filepath.Join(config, "api-launch.json")
	f.config.Profiles[0].Launch.File = filepath.Join(config, "local_only-launch.json")
	f.write()
	for _, name := range []string{"config/api.dsn", "config/model-writer.dsn", "keys/jwt.key", "config/runtime.dsn", "config/ca-key.pem", "config/audit-key"} {
		if productWrite(filepath.Join(root, name), []byte("owned-synthetic-"+name)) != nil {
			t.Fatal("fixture", name)
		}
	}
	if productPrepareAPIProjection(root) != nil || productPrepareAPIProjection(root) != nil {
		t.Fatal("bound projection/restart failed")
	}
	for _, name := range []string{"config/installation.json", "config/api-launch.json", "config/local_only-launch.json", "config/api.dsn", "config/model-writer.dsn", "keys/jwt.key"} {
		source, _ := os.ReadFile(filepath.Join(root, name))
		copy, err := readPrivateFile(filepath.Join(root, "api", name), 256<<10)
		if err != nil || !bytes.Equal(source, copy) {
			t.Fatal("bound input missing", name)
		}
	}
	for _, name := range []string{"runtime.dsn", "ca-key.pem", "audit-key"} {
		if _, err := os.Stat(filepath.Join(root, "api", "config", name)); !os.IsNotExist(err) {
			t.Fatal("execution secret projected")
		}
	}
	if os.WriteFile(f.config.Profiles[0].Launch.File, []byte("changed"), 0600) != nil {
		t.Fatal("fixture")
	}
	if productPrepareAPIProjection(root) == nil {
		t.Fatal("unbound model launch projected")
	}
	f.assertNoConnections()
}
