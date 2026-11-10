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
		{"path_prefixes": []string{"/%2e"}}, {"valid_for_hours": 169}, {"max_runs": 1001}, {"unexpected": true},
	} {
		root := t.TempDir()
		if os.Chmod(root, 0700) != nil {
			t.Fatal("fixture permissions")
		}
		c := map[string]any{"format": "abc2lab.product-install/v1", "database_host": "192.0.2.10", "database_name": "abc2lab", "admin_password": "synthetic-only-install-password", "target_origin": "https://juice.test:9443", "target_ip": "10.200.0.20", "path_prefixes": []string{"/rest/products"}, "allow_private_target": true, "nonmutating_scope_acknowledged": true, "valid_for_hours": 24, "max_runs": 1, "local_model": map[string]any{"model": "owned-synthetic", "base_url": "http://192.0.2.30:8080/v1", "addresses": []string{"192.0.2.30"}, "context_window_k": 128, "max_tokens": 2048}}
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
