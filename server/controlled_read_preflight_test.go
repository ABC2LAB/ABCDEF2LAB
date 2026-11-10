//go:build linux

package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pgdb "github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/kgfixture"
	"github.com/Autumn-27/artex/transmit"
)

type preparationDBConnector struct{ connects *atomic.Int32 }

func (c preparationDBConnector) Connect(context.Context) (driver.Conn, error) {
	c.connects.Add(1)
	return nil, errors.New("synthetic database must not be contacted")
}
func (c preparationDBConnector) Driver() driver.Driver { return preparationDBDriver{} }

type preparationDBDriver struct{}

func (preparationDBDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("synthetic connector required")
}

type preparationSupervisor struct{}

func (preparationSupervisor) CheckTaskQuiescent(context.Context, int64) error { return nil }
func (preparationSupervisor) AuthorizeExecution(context.Context, transmit.Scope, int64) error {
	return nil
}
func (preparationSupervisor) Attach(*Server) error { return nil }

func TestRegisteredPreparationRejectsBeforeHostAuthority(t *testing.T) {
	binding := localBindingFixture(t)
	path := filepath.Join(filepath.Dir(binding.State.Path), "inputs", "host.json")
	body, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	var connects atomic.Int32
	database := sql.OpenDB(preparationDBConnector{&connects})
	defer database.Close()
	manager := &Manager{pg: &pgdb.DB{DB: database}}
	_, err = StartFirstDiagnosisControlled(t.Context(), manager, database, FirstDiagnosisControlledStartOptions{
		BindingFile: path, MetadataFile: filepath.Join(filepath.Dir(path), "missing-metadata.json"),
		SnapshotDir: filepath.Dir(path), RegistryFile: "registry.json", Supervisor: preparationSupervisor{}, CleanupTimeout: time.Second,
	})
	if err == nil || connects.Load() != 0 {
		t.Fatal("preparation mismatch reached database authority")
	}
}

func registeredPreparationFixture(t *testing.T, target string) (*kgBindingFixture, ControlledReadAssemblyMetadata, string) {
	t.Helper()
	f := newKGBindingFixture(t, target)
	f.input.MinIntervalMS = 500
	f.input.ExpiresAt = time.Now().Add(10 * time.Minute).UTC()
	f.write(t, "approved.json", kgBindingJSON(t, f.input))
	kg, err := kgfixture.Parse(kgBindingJSON(t, f.registry))
	if err != nil {
		t.Fatal(err)
	}
	defer kg.Invalidate()
	owner := idor.AccountRef{Label: f.input.Sessions[0].Label, Handle: f.input.Sessions[0].Handle, Status: f.input.Sessions[0].Status}
	peer := idor.AccountRef{Label: f.input.Sessions[1].Label, Handle: f.input.Sessions[1].Handle, Status: f.input.Sessions[1].Status}
	policy, ok := kg.ResolvePolicy("doc:one", owner, peer)
	if !ok {
		t.Fatal("fixture policy missing")
	}
	doc := ControlledReadAssemblyMetadata{Format: ControlledReadAssemblyMetadataFormat, SnapshotRef: "approved", TargetOrigin: target,
		Source: e3record.Source{Expected: evidencebind.Context{
			Scope:    transmit.Scope{TaskRef: f.input.TaskID, CaseRef: f.input.CaseRef, ResetRef: f.input.ResetRef, PolicyRef: f.input.PolicyRef},
			IntentID: f.input.IntentID, ResourceRef: "doc:one", ReadApprovalRef: "read-approval", KGPolicyVersion: kg.PolicyVersion(), Owner: owner, Evaluated: peer,
		}, ExplorationID: 9, KGSnapshotRef: kg.Ref(), KGSnapshotDigest: kg.Digest(), GenerationRef: kg.GenerationRef(), InstanceRef: kg.InstanceRef(),
			ResetGeneration: kg.ResetGeneration(), ModelTag: "synthetic-model", Policy: policy, CanaryFields: []string{"note"}, Severity: "medium"}}
	path := filepath.Join(f.store.snapshotDir, "metadata.json")
	f.write(t, "metadata.json", kgBindingJSON(t, doc))
	return f, doc, path
}

func preparationFiles(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[path] = sha256.Sum256(body)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRegisteredReadPreparationIsReadOnlyAndDoesNotSend(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer target.Close()
	f, _, path := registeredPreparationFixture(t, target.URL)
	before := preparationFiles(t, filepath.Dir(f.store.snapshotDir))
	if err := CheckRegisteredReadPreparation(t.Context(), path, f.store.snapshotDir, "kg.json"); err != nil {
		t.Fatal(err)
	}
	after := preparationFiles(t, filepath.Dir(f.store.snapshotDir))
	if len(before) != len(after) || hits.Load() != 0 || len(f.e.controlledReads.pending) != 0 {
		t.Fatal("preparation created state, registered work or sent a request")
	}
	for name, digest := range before {
		if after[name] != digest {
			t.Fatal("preparation modified an input")
		}
	}
}

func TestRegisteredReadPreparationRejectsMismatches(t *testing.T) {
	mutations := map[string]func(*kgBindingFixture, *ControlledReadAssemblyMetadata){
		"read only": func(f *kgBindingFixture, _ *ControlledReadAssemblyMetadata) { f.input.Resources[0].ReadOnly = false },
		"origin": func(f *kgBindingFixture, _ *ControlledReadAssemblyMetadata) {
			f.input.Resources[0].URL = "http://other.invalid/document"
		},
		"approval expired": func(f *kgBindingFixture, _ *ControlledReadAssemblyMetadata) {
			f.input.ExpiresAt = time.Now().Add(-time.Minute)
		},
		"too many requests": func(f *kgBindingFixture, _ *ControlledReadAssemblyMetadata) { f.input.MaxRequests = 3 },
		"no interval":       func(f *kgBindingFixture, _ *ControlledReadAssemblyMetadata) { f.input.MinIntervalMS = 0 },
		"session expired": func(f *kgBindingFixture, _ *ControlledReadAssemblyMetadata) {
			f.input.Sessions[1].ExpiresAt = time.Now().Add(-time.Minute)
		},
		"extra label": func(f *kgBindingFixture, _ *ControlledReadAssemblyMetadata) {
			extra := f.input.Sessions[1]
			extra.Label, extra.Handle = "third", "third-session"
			f.input.Sessions = append(f.input.Sessions, extra)
		},
		"extra resource": func(f *kgBindingFixture, _ *ControlledReadAssemblyMetadata) {
			extra := f.input.Resources[0]
			extra.Ref = "doc:extra"
			f.input.Resources = append(f.input.Resources, extra)
		},
		"marker field": func(f *kgBindingFixture, _ *ControlledReadAssemblyMetadata) {
			f.input.Resources[0].View.Markers[0].Field = "other"
		},
		"marker value": func(f *kgBindingFixture, _ *ControlledReadAssemblyMetadata) {
			f.input.Resources[0].View.Markers[0].Value = "synthetic-other-marker"
		},
		"marker trivial": func(f *kgBindingFixture, _ *ControlledReadAssemblyMetadata) {
			f.input.Resources[0].View.Markers[0].Trivial = true
		},
		"no view": func(f *kgBindingFixture, _ *ControlledReadAssemblyMetadata) { f.input.Resources[0].View = nil },
		"no status path": func(f *kgBindingFixture, _ *ControlledReadAssemblyMetadata) {
			f.input.Resources[0].View.StatusPath = nil
		},
		"ambiguous status": func(f *kgBindingFixture, _ *ControlledReadAssemblyMetadata) {
			f.input.Resources[0].View.ErrorValues = []string{"ok"}
		},
		"generation": func(_ *kgBindingFixture, d *ControlledReadAssemblyMetadata) {
			d.Source.GenerationRef = "other-generation"
		},
		"instance": func(_ *kgBindingFixture, d *ControlledReadAssemblyMetadata) { d.Source.InstanceRef = "other-instance" },
		"digest":   func(_ *kgBindingFixture, d *ControlledReadAssemblyMetadata) { d.Source.KGSnapshotDigest[0] ^= 1 },
		"policy version": func(_ *kgBindingFixture, d *ControlledReadAssemblyMetadata) {
			d.Source.Expected.KGPolicyVersion = "other-policy"
		},
		"reset":           func(_ *kgBindingFixture, d *ControlledReadAssemblyMetadata) { d.Source.ResetGeneration = "other-reset" },
		"target metadata": func(_ *kgBindingFixture, d *ControlledReadAssemblyMetadata) { d.TargetOrigin = "http://other.invalid" },
		"owner handle": func(_ *kgBindingFixture, d *ControlledReadAssemblyMetadata) {
			d.Source.Expected.Owner.Handle = "other-session"
		},
		"same subject": func(_ *kgBindingFixture, d *ControlledReadAssemblyMetadata) {
			d.Source.Expected.Evaluated = d.Source.Expected.Owner
		},
		"canary fields": func(_ *kgBindingFixture, d *ControlledReadAssemblyMetadata) {
			d.Source.CanaryFields = []string{"other"}
		},
		"policy meaning": func(_ *kgBindingFixture, d *ControlledReadAssemblyMetadata) {
			d.Source.Policy.HasRegisteredSource = false
		},
		"collector facts": func(_ *kgBindingFixture, d *ControlledReadAssemblyMetadata) { d.Source.Completed = true },
	}
	for name, change := range mutations {
		t.Run(name, func(t *testing.T) {
			f, doc, path := registeredPreparationFixture(t, "http://127.0.0.1:1")
			if err := CheckRegisteredReadPreparation(t.Context(), path, f.store.snapshotDir, "kg.json"); err != nil {
				t.Fatal("normal control failed", err)
			}
			change(f, &doc)
			f.write(t, "approved.json", kgBindingJSON(t, f.input))
			f.write(t, "metadata.json", kgBindingJSON(t, doc))
			err := CheckRegisteredReadPreparation(t.Context(), path, f.store.snapshotDir, "kg.json")
			if err == nil || err != ErrControlledReadHostConfiguration || strings.Contains(err.Error(), verificationTestMarker) {
				t.Fatal("mismatched input accepted or disclosed")
			}
		})
	}
}

func TestRegisteredReadPreparationRejectsMissingAndUnsafeFiles(t *testing.T) {
	for _, name := range []string{"metadata", "snapshot", "registry", "key", "credential", "mode", "symlink", "duplicate", "registry traversal", "cancel", "nil context"} {
		t.Run(name, func(t *testing.T) {
			f, _, path := registeredPreparationFixture(t, "http://127.0.0.1:1")
			ctx := t.Context()
			registry := "kg.json"
			remove := map[string]string{"metadata": "metadata.json", "snapshot": "approved.json", "registry": "kg.json", "key": "audit.key", "credential": "owner.secret"}[name]
			if remove != "" {
				if err := os.Remove(filepath.Join(f.store.snapshotDir, remove)); err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "mode":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(path, path+".real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".real", path); err != nil {
					t.Fatal(err)
				}
			case "duplicate":
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				f.write(t, "metadata.json", bytes.Replace(body, []byte(`"snapshot_ref":"approved"`), []byte(`"snapshot_ref":"approved","snapshot_ref":"approved"`), 1))
			case "registry traversal":
				registry = "../kg.json"
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "nil context":
				ctx = nil
			}
			if CheckRegisteredReadPreparation(ctx, path, f.store.snapshotDir, registry) == nil {
				t.Fatal("unsafe or unavailable input accepted")
			}
		})
	}
}

type preflightTestRecorder struct{ events []transmit.Event }

func (r *preflightTestRecorder) Record(_ context.Context, event transmit.Event) error {
	r.events = append(r.events, event)
	return nil
}

func TestRegisteredReadPreparationUsesConfiguredProjectionForBothAccounts(t *testing.T) {
	for _, shape := range []string{"exact", "alternate path", "array", "partial", "numeric status"} {
		t.Run(shape, func(t *testing.T) {
			var hits atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if r.Method != "GET" || r.URL.Path != "/document" {
					t.Error("registered object changed")
				}
				w.Header().Set("Content-Type", "application/json")
				var body any = map[string]any{"status": "ok", "data": map[string]any{"note": verificationTestMarker, "private": runtimeSecret}}
				switch shape {
				case "alternate path":
					body = map[string]any{"meta": map[string]any{"phase": "ready"}, "record": map[string]any{"caption": verificationTestMarker}}
				case "array":
					body = map[string]any{"status": "ok", "data": []any{map[string]any{"note": verificationTestMarker}}}
				case "partial":
					body = map[string]any{"status": "ok", "data": map[string]any{"note": verificationTestMarker + "-suffix"}}
				case "numeric status":
					body = map[string]any{"status": 200, "data": map[string]any{"note": verificationTestMarker}}
				}
				if err := json.NewEncoder(w).Encode(body); err != nil {
					t.Error(err)
				}
			}))
			defer target.Close()
			f, doc, path := registeredPreparationFixture(t, target.URL)
			if shape == "alternate path" {
				f.input.Resources[0].View.StatusPath = []string{"meta", "phase"}
				f.input.Resources[0].View.NormalValue = "ready"
				f.input.Resources[0].View.DataPath = []string{"record"}
				f.input.Resources[0].View.Markers[0].Field = "caption"
				resource := f.registry["resources"].([]any)[0].(map[string]any)
				resource["canaries"].([]any)[0].(map[string]any)["field"] = "caption"
				kg, err := kgfixture.Parse(kgBindingJSON(t, f.registry))
				if err != nil {
					t.Fatal(err)
				}
				doc.Source.KGSnapshotDigest, doc.Source.Expected.KGPolicyVersion = kg.Digest(), kg.PolicyVersion()
				doc.Source.CanaryFields = []string{"caption"}
				kg.Invalidate()
				f.write(t, "kg.json", kgBindingJSON(t, f.registry))
				f.write(t, "metadata.json", kgBindingJSON(t, doc))
				f.write(t, "approved.json", kgBindingJSON(t, f.input))
			}
			if err := CheckRegisteredReadPreparation(t.Context(), path, f.store.snapshotDir, "kg.json"); err != nil || hits.Load() != 0 {
				t.Fatal("preflight failed or sent", err)
			}
			spec, err := f.store.load(f.input.TaskID, f.input.IntentID, "approved")
			if err != nil {
				t.Fatal(err)
			}
			defer clear(spec.Key)
			recorder := &preflightTestRecorder{}
			spec.HTTP.Scope.RunRef, spec.HTTP.Recorder = "synthetic-preflight-run", recorder
			tx, err := transmit.NewHTTP(t.Context(), spec.HTTP)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Stop()
			for _, session := range spec.HTTP.Sessions {
				capture, receipt, ok := tx.SendDetailed(idor.AccessRequest{As: session.Account, Method: "GET", ResourceRef: "doc:one"})
				if !ok || receipt.Reason != transmit.Completed || !receipt.ResponseReceived {
					t.Fatal("controlled read failed")
				}
				want := ""
				if shape == "exact" || shape == "alternate path" {
					want = verificationTestMarker
				}
				if capture.View.Body != want || strings.Contains(capture.View.Body, runtimeSecret) {
					t.Fatal("incorrect or unsafe response projection")
				}
				if (shape == "array" || shape == "numeric status") && capture.View.Normal {
					t.Fatal("invalid shape classified normal")
				}
			}
			if hits.Load() != 2 || len(recorder.events) != 4 {
				t.Fatal("owner/cross sends or audit events missing")
			}
		})
	}
}
