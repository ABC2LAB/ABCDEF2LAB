//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func preparationFixture(t *testing.T) (preparationConfig, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	c := validTestConfig()
	c.DataDir = filepath.Join(root, "data")
	c.KeyDir = filepath.Join(root, "keys")
	c.SkillDir = filepath.Join(root, "skills")
	c.SnapshotDir = filepath.Join(root, "snapshots")
	c.StateDir = filepath.Join(root, "state")
	for _, dir := range []string{c.DataDir, c.KeyDir, c.SkillDir, c.SnapshotDir, c.StateDir} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(c.KeyDir, "jwt.key"), bytes.Repeat([]byte("s"), 32), 0600); err != nil {
		t.Fatal(err)
	}
	c.LocalLLMManifestPath = filepath.Join(root, "pins.json")
	manifest := `{"version":1,"profiles":[{"profile_id":7,"format":"openai","model":"local:test","base_url":"http://model.internal:11434/v1","addresses":["10.0.0.1"]}]}`
	if err := os.WriteFile(c.LocalLLMManifestPath, []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "check.json")
	body, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return c, path
}

func TestCheckReportsStageAndNeverClaimsAuthority(t *testing.T) {
	_, path := preparationFixture(t)
	var out, errOut bytes.Buffer
	calls := 0
	checker := func(context.Context, string) error { calls++; return nil }
	if code := run(context.Background(), []string{"check", "-config", path}, &out, &errOut, func(string) string { return "PRIVATE-DSN" }, checker); code != 0 || errOut.Len() != 0 || calls != 0 {
		t.Fatal("file check failed or contacted database")
	}
	var report checkReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Configuration != "PASS" || report.Paths != "PASS" || report.ModelPin != "PASS" || report.Database != "SKIP" || report.Providers != "NOT_VERIFIED" || report.Diagnosis != "NOT_RUN" {
		t.Fatal("check overstated readiness")
	}
	if strings.Contains(out.String(), path) || strings.Contains(out.String(), "PRIVATE") {
		t.Fatal("check disclosed configuration")
	}
	for _, mode := range []string{"success", "no-dsn", "failure"} {
		t.Run(mode, func(t *testing.T) {
			out.Reset()
			errOut.Reset()
			calls = 0
			getenv := func(string) string {
				if mode == "no-dsn" {
					return ""
				}
				return "PRIVATE-DSN"
			}
			checker := func(ctx context.Context, dsn string) error {
				calls++
				if dsn != "PRIVATE-DSN" {
					t.Error("wrong DSN")
				}
				if mode == "failure" {
					return errors.New("PRIVATE-DB-ERROR")
				}
				return nil
			}
			code := run(context.Background(), []string{"check", "-config", path, "-database"}, &out, &errOut, getenv, checker)
			if mode == "success" {
				if code != 0 || calls != 1 || !strings.Contains(out.String(), `"database":"PASS"`) {
					t.Fatal("database check result missing")
				}
			} else if code != 1 || out.Len() != 0 {
				t.Fatal("missing/failed database treated as passing")
			}
			if mode == "no-dsn" && calls != 0 {
				t.Fatal("missing DSN contacted database")
			}
			if strings.Contains(out.String()+errOut.String(), "PRIVATE") {
				t.Fatal("database error disclosed input")
			}
		})
	}
}

func TestCheckRejectsBadFilesWithoutCreatingKeys(t *testing.T) {
	for _, mode := range []string{"missing-key", "short-key", "manifest-mismatch", "directory-mode", "manifest-missing"} {
		t.Run(mode, func(t *testing.T) {
			c, _ := preparationFixture(t)
			key := filepath.Join(c.KeyDir, "jwt.key")
			switch mode {
			case "missing-key":
				if err := os.Remove(key); err != nil {
					t.Fatal(err)
				}
			case "short-key":
				if err := os.WriteFile(key, []byte("short"), 0600); err != nil {
					t.Fatal(err)
				}
			case "manifest-mismatch":
				c.ModelRef = "not-the-pin"
			case "directory-mode":
				if err := os.Chmod(c.StateDir, 0755); err != nil {
					t.Fatal(err)
				}
			case "manifest-missing":
				if err := os.Remove(c.LocalLLMManifestPath); err != nil {
					t.Fatal(err)
				}
			}
			if checkFiles(c) != errPreparation {
				t.Fatal("unprepared files accepted")
			}
			if mode == "missing-key" {
				if _, err := os.Stat(key); !os.IsNotExist(err) {
					t.Fatal("check generated a key")
				}
			}
		})
	}
}

func TestCheckUsageErrorsAndCancellationAreRedacted(t *testing.T) {
	for _, args := range [][]string{nil, {"serve"}, {"check", "-PRIVATE-TOKEN"}, {"check", "-config", "/PRIVATE-PATH"}, {"check", "-config", "/PRIVATE-PATH", "extra"}} {
		var out, errOut bytes.Buffer
		if run(context.Background(), args, &out, &errOut, nil, nil) == 0 || out.Len() != 0 || strings.Contains(errOut.String(), "PRIVATE") {
			t.Fatal("usage/file error exposed input or passed")
		}
	}
	_, path := preparationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errOut bytes.Buffer
	if run(ctx, []string{"check", "-config", path}, &out, &errOut, nil, nil) != 1 || out.Len() != 0 {
		t.Fatal("cancelled check passed")
	}
}
