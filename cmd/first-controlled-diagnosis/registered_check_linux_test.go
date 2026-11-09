//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/kgfixture"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/server"
	"github.com/Autumn-27/artex/transmit"
)

const preparationMarker = "synthetic-preflight-marker-8d72f09a"

func registeredCheckFixture(t *testing.T, origin string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(5 * time.Minute).UTC()
	registry := map[string]any{
		"format": kgfixture.FormatV1, "snapshot_id": "preflight-kg", "run_id": "synthetic-prep", "reset_generation": "reset-one", "generation_ref": "generation-one", "instance_ref": "instance-one",
		"principals": []any{map[string]any{"label": "owner"}, map[string]any{"label": "peer"}},
		"resources": []any{map[string]any{"resource_ref": "document:one", "owner_ref": "owner", "canaries": []any{map[string]any{"field": "note", "value": preparationMarker, "trivial": false}},
			"policies": []any{map[string]any{"evaluated_ref": "peer", "expected_access": "deny", "sharing": "resolved_none", "delegation": "resolved_none", "valid_from": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), "valid_until": until.Format(time.RFC3339Nano)}}}},
	}
	encode := func(v any) []byte {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	kg, err := kgfixture.Parse(encode(registry))
	if err != nil {
		t.Fatal(err)
	}
	defer kg.Invalidate()
	owner := idor.AccountRef{Label: "owner", Handle: "owner-session", Status: oracle.SubjectVerified}
	peer := idor.AccountRef{Label: "peer", Handle: "peer-session", Status: oracle.SubjectVerified}
	policy, ok := kg.ResolvePolicy("document:one", owner, peer)
	if !ok {
		t.Fatal("missing policy")
	}
	doc := server.ControlledReadAssemblyMetadata{Format: server.ControlledReadAssemblyMetadataFormat, SnapshotRef: "approved", TargetOrigin: origin,
		Source: e3record.Source{Expected: evidencebind.Context{Scope: transmit.Scope{TaskRef: "17", CaseRef: "case-one", ResetRef: "reset-one", PolicyRef: "policy-one"}, IntentID: 22, ResourceRef: "document:one", ReadApprovalRef: "read-one", KGPolicyVersion: kg.PolicyVersion(), Owner: owner, Evaluated: peer},
			ExplorationID: 9, KGSnapshotRef: kg.Ref(), KGSnapshotDigest: kg.Digest(), GenerationRef: kg.GenerationRef(), InstanceRef: kg.InstanceRef(), ResetGeneration: kg.ResetGeneration(), ModelTag: "synthetic-model", Policy: policy, CanaryFields: []string{"note"}, Severity: "medium"}}
	snapshot := map[string]any{"task_id": "17", "intent_id": 22, "case_ref": "case-one", "reset_ref": "reset-one", "policy_ref": "policy-one", "origin": origin, "connect_ip": "127.0.0.1", "expires_at": until,
		"max_requests": 2, "min_interval_ms": 500, "request_timeout_ms": 1000, "max_body_bytes": 1024, "key_ref": "synthetic-key", "key_file": "audit.key",
		"sessions":  []any{map[string]any{"label": owner.Label, "handle": owner.Handle, "status": owner.Status, "expires_at": until, "header_files": map[string]string{"X-Session": "owner.secret"}}, map[string]any{"label": peer.Label, "handle": peer.Handle, "status": peer.Status, "expires_at": until, "header_files": map[string]string{"X-Session": "peer.secret"}}},
		"resources": []any{map[string]any{"ref": "document:one", "url": origin + "/document", "approval_ref": "read-one", "read_only": true, "accounts": []string{"owner", "peer"}, "view": &transmit.JSONViewRule{StatusPath: []string{"status"}, NormalValue: "ok", DataPath: []string{"data"}, Markers: []idor.Marker{{Field: "note", Value: preparationMarker}}}}}}
	for name, body := range map[string][]byte{"metadata.json": encode(doc), "approved.json": encode(snapshot), "registry.json": encode(registry), "audit.key": bytes.Repeat([]byte{0x71}, 32), "owner.secret": []byte("synthetic-owner-credential"), "peer.secret": []byte("synthetic-peer-credential")} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir, filepath.Join(dir, "metadata.json")
}

func TestRegisteredCheckCLIIsOfflineAndReportsLimits(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer target.Close()
	dir, metadata := registeredCheckFixture(t, target.URL)
	var out, errOut bytes.Buffer
	code := run(t.Context(), []string{"check-registered-get", "-metadata", metadata, "-snapshot-dir", dir, "-registry", "registry.json"}, &out, &errOut,
		func(string) string { t.Fatal("read environment"); return "" }, func(context.Context, string) error { t.Fatal("connected database"); return nil })
	if code != 0 || errOut.Len() != 0 || hits.Load() != 0 {
		t.Fatal("offline check failed or contacted target")
	}
	var report map[string]any
	if json.Unmarshal(out.Bytes(), &report) != nil || len(report) != 10 {
		t.Fatal("unexpected report")
	}
	for key, want := range map[string]string{"registration": "PASS", "response_rule": "PASS", "pairing": "PASS", "metadata": "PASS", "authentication": "NOT_VERIFIED", "authority": "NOT_ACQUIRED", "database": "NOT_CHECKED", "model": "NOT_CALLED", "diagnosis": "NOT_RUN"} {
		if report[key] != want {
			t.Fatalf("wrong %s status", key)
		}
	}
	if report["target_requests"] != float64(0) {
		t.Fatal("check reported a request")
	}
	for _, private := range []string{metadata, dir, target.URL, preparationMarker, "synthetic-owner-credential", "owner-session"} {
		if strings.Contains(out.String()+errOut.String(), private) {
			t.Fatal("private input in output")
		}
	}
}

type registeredOutputFailure struct{}

func (registeredOutputFailure) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestRegisteredCheckCLIRejectsUnavailableInputAndOutput(t *testing.T) {
	dir, metadata := registeredCheckFixture(t, "http://127.0.0.1:1")
	base := []string{"-metadata", metadata, "-snapshot-dir", dir, "-registry", "registry.json"}
	for _, mode := range []string{"missing argument", "extra argument", "missing file", "traversal", "cancel", "nil context", "output"} {
		t.Run(mode, func(t *testing.T) {
			args := append([]string(nil), base...)
			ctx := t.Context()
			var out, errOut bytes.Buffer
			var stdout io.Writer = &out
			want := 1
			switch mode {
			case "missing argument":
				args = args[:4]
				want = 2
			case "extra argument":
				args = append(args, "unexpected")
				want = 2
			case "missing file":
				args[1] = filepath.Join(dir, "PRIVATE-missing.json")
			case "traversal":
				args[5] = "../registry.json"
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "nil context":
				ctx = nil
			case "output":
				stdout = registeredOutputFailure{}
			}
			code := runRegisteredCheck(ctx, args, stdout, &errOut)
			if code != want || out.Len() != 0 {
				t.Fatal("failure reported as success")
			}
			if mode != "missing argument" && mode != "extra argument" && strings.Contains(errOut.String(), "PRIVATE") {
				t.Fatal("private path in error")
			}
		})
	}
}

func TestLaunchCheckRejectsRegisteredMismatchBeforeDatabase(t *testing.T) {
	dir, metadata := registeredCheckFixture(t, "http://127.0.0.1:1")
	files, _ := preparationFixture(t)
	c := launchConfig{Mode: "registered_get", DataDir: files.DataDir, KeyDir: files.KeyDir, SkillDir: files.SkillDir, LocalLLMManifestPath: files.LocalLLMManifestPath,
		ModelProfileID: files.ModelProfileID, ModelFormat: files.ModelFormat, ModelBaseURL: files.ModelBaseURL, ModelRef: files.ModelRef,
		Registered: &registeredLaunch{MetadataFile: metadata, SnapshotDir: dir, StateDir: files.StateDir, RegistryFile: "registry.json"}}
	var out, errOut bytes.Buffer
	if checkLaunch(t.Context(), c, false, &out, &errOut) != 0 {
		t.Fatal("normal launch check failed")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var connects atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			connects.Add(1)
			_ = conn.Close()
		}
	}()
	defer func() { _ = listener.Close(); <-done }()
	c.RuntimeDSNFile = filepath.Join(dir, "runtime.dsn")
	dsn := fmt.Sprintf("postgres://synthetic:synthetic@%s/synthetic?sslmode=disable&connect_timeout=1", listener.Addr())
	if err := os.WriteFile(c.RuntimeDSNFile, []byte(dsn), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "registry.json")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if checkLaunch(t.Context(), c, true, &out, &errOut) != 1 || out.Len() != 0 || connects.Load() != 0 {
		t.Fatal("mismatch reached database or passed")
	}
}
