package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/kgfixture"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
)

type kgBindingFixture struct {
	e        *Engine
	task     *Task
	intent   *db.Node
	store    *controlledReadStore
	input    snapshotJSON
	registry map[string]any
}

func kgBindingJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (f *kgBindingFixture) write(t *testing.T, name string, body []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.store.snapshotDir, name), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func newKGBindingFixture(t *testing.T, target string) *kgBindingFixture {
	t.Helper()
	dir := t.TempDir()
	store, err := openControlledReadStore(filepath.Join(dir, "snapshots"), filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour).UTC()
	in := snapshotJSON{TaskID: "17", IntentID: 22, CaseRef: "read-case", ResetRef: "reset-1", PolicyRef: "policy-1", Origin: target, ConnectIP: "127.0.0.1", ExpiresAt: expires, MaxRequests: 2, RequestTimeoutMS: 1000, MaxBodyBytes: 1024, KeyRef: "key-1", KeyFile: "audit.key",
		Sessions: []sessionJSON{
			{Label: "owner", Handle: "session-1", Status: oracle.SubjectVerified, ExpiresAt: expires, HeaderFiles: map[string]string{"X-Session": "owner.secret"}},
			{Label: "peer", Handle: "session-2", Status: oracle.SubjectVerified, ExpiresAt: expires, HeaderFiles: map[string]string{"X-Session": "peer.secret"}},
		}, Resources: []resourceJSON{{Ref: "doc:one", URL: target + "/document", ApprovalRef: "read-approval", ReadOnly: true, Accounts: []string{"owner", "peer"}, View: &transmit.JSONViewRule{StatusPath: []string{"status"}, NormalValue: "ok", DataPath: []string{"data"}, Markers: []idor.Marker{{Field: "note", Value: verificationTestMarker}}}}},
	}
	registry := map[string]any{"format": kgfixture.FormatV1, "snapshot_id": "kg-bound-1", "run_id": "prep-1", "reset_generation": "reset-1", "generation_ref": "generation-1", "instance_ref": "instance-1",
		"principals": []any{map[string]any{"label": "owner"}, map[string]any{"label": "peer"}},
		"resources":  []any{map[string]any{"resource_ref": "doc:one", "owner_ref": "owner", "policies": []any{map[string]any{"evaluated_ref": "peer", "expected_access": "deny", "sharing": "resolved_none", "delegation": "resolved_none", "valid_from": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), "valid_until": expires.Format(time.RFC3339Nano)}}, "canaries": []any{map[string]any{"field": "note", "value": verificationTestMarker, "trivial": false}}}},
	}
	e := NewEngine(nil)
	e.controlledReads = newControlledReadRuntime(store)
	f := &kgBindingFixture{e: e, task: &Task{ID: "17", Paused: true}, intent: &db.Node{ID: 22, Kind: "intent"}, store: store, input: in, registry: registry}
	e.paused.Store(f.task.ID, true)
	f.write(t, "approved.json", kgBindingJSON(t, in))
	f.write(t, "kg.json", kgBindingJSON(t, registry))
	f.write(t, "audit.key", bytes.Repeat([]byte{0x71}, 32))
	f.write(t, "owner.secret", []byte(runtimeSecret))
	f.write(t, "peer.secret", []byte(runtimeSecret+"-peer"))
	files := map[string]string{"approved": "kg.json"}
	s := &Server{ctx: t.Context(), engine: e}
	if err := s.configureControlledReadFixtureRegistries(files); err != nil {
		t.Fatal(err)
	}
	files["approved"] = "changed-by-caller.json" // the bootstrap copy must remain pinned
	return f
}

func (f *kgBindingFixture) register(t *testing.T) controlledReadSpec {
	t.Helper()
	if err := f.e.registerControlledRead(t.Context(), f.task, f.intent.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	return f.e.controlledReads.pending[controlledWorkKey{f.task.ID, f.intent.ID}]
}
func (f *kgBindingFixture) resume() {
	f.e.paused.Delete(f.task.ID)
	f.task.updateLifecycle(func(s *taskLifecycleState) { s.Paused = false })
}

func TestControlledReadKGPairingRejectsBeforePublication(t *testing.T) {
	for _, name := range []string{"labels", "resources", "reset", "marker field", "marker value", "marker trivial", "no view", "unmapped", "missing", "permissions", "symlink", "oversize", "unknown field", "duplicate JSON"} {
		t.Run(name, func(t *testing.T) {
			f := newKGBindingFixture(t, "http://127.0.0.1:1")
			code := kgfixture.CodePairingMarkers
			resource := f.registry["resources"].([]any)[0].(map[string]any)
			marker := resource["canaries"].([]any)[0].(map[string]any)
			rewrite := true
			switch name {
			case "labels":
				f.registry["principals"] = append(f.registry["principals"].([]any), map[string]any{"label": "third"})
				code = kgfixture.CodePairingLabels
			case "resources":
				resource["resource_ref"] = "doc:other"
				code = kgfixture.CodePairingResources
			case "reset":
				f.registry["reset_generation"] = "another-reset"
				code = kgfixture.CodePairingReset
			case "marker field":
				marker["field"] = "other"
			case "marker value":
				marker["value"] = "synthetic-different-marker"
			case "marker trivial":
				marker["trivial"] = true
			case "no view":
				f.input.Resources[0].View = nil
				f.write(t, "approved.json", kgBindingJSON(t, f.input))
			case "unmapped":
				f.store.fixtureRegistries = map[string]string{"other": "kg.json"}
				code = kgfixture.CodeUnreadable
			case "missing":
				if err := os.Remove(filepath.Join(f.store.snapshotDir, "kg.json")); err != nil {
					t.Fatal(err)
				}
				rewrite = false
				code = kgfixture.CodeUnreadable
			case "permissions":
				if err := os.Chmod(filepath.Join(f.store.snapshotDir, "kg.json"), 0o640); err != nil {
					t.Fatal(err)
				}
				rewrite = false
				code = kgfixture.CodeUnreadable
			case "symlink":
				p := filepath.Join(f.store.snapshotDir, "kg.json")
				if err := os.Rename(p, p+".real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(p+".real", p); err != nil {
					t.Fatal(err)
				}
				rewrite = false
				code = kgfixture.CodeUnreadable
			case "oversize":
				f.write(t, "kg.json", bytes.Repeat([]byte{'x'}, kgfixture.MaxRegistryBytes+1))
				rewrite = false
				code = kgfixture.CodeUnreadable
			case "unknown field":
				f.registry["PRIVATE-CANARY"] = "PRIVATE-CONTENT"
				code = kgfixture.CodeUnknownField
			case "duplicate JSON":
				f.write(t, "kg.json", []byte(`{"format":"a","format":"b"}`))
				rewrite = false
				code = kgfixture.CodeDuplicate
			}
			if rewrite {
				f.write(t, "kg.json", kgBindingJSON(t, f.registry))
			}
			got, err := f.store.load("17", 22, "approved")
			var coded *kgfixture.Error
			if err == nil || !errors.As(err, &coded) || coded.Code != code || got.Key != nil || got.fixture != nil {
				t.Fatalf("load did not reject by fixed expected code %q: %v", code, err)
			}
			for _, private := range []string{runtimeSecret, verificationTestMarker, f.store.snapshotDir, "PRIVATE-CONTENT"} {
				if strings.Contains(err.Error(), private) {
					t.Fatal("private pairing input escaped error")
				}
			}
			if err := f.e.registerControlledRead(t.Context(), f.task, 22, "approved"); err == nil {
				t.Fatal("mismatched registration published")
			}
			if len(f.e.controlledReads.pending) != 0 {
				t.Fatal("mismatch left a pending execution")
			}
			if marked, err := f.store.marked("17", 22); err != nil || !marked {
				t.Fatal("failed registration lost downgrade barrier")
			}
			f.resume()
			if _, finish, err := f.e.prepareControlledRead(t.Context(), f.task, f.intent); err == nil || finish != nil {
				t.Fatal("mismatch entered execution")
			}
		})
	}
}

func TestControlledReadKGMappingBootstrapAndCompatibility(t *testing.T) {
	for _, files := range []map[string]string{{}, {"../approved": "kg.json"}, {"approved": "../kg.json"}, {"approved": "/kg.json"}, {"approved": "approved.json"}, {"approved": ".hidden"}} {
		if _, err := cloneControlledReadFixtureRegistries(files); err == nil {
			t.Fatal("invalid trusted mapping accepted")
		}
	}
	if got, err := cloneControlledReadFixtureRegistries(nil); err != nil || got != nil {
		t.Fatal("nil mapping changed compatibility")
	}
	f := newKGBindingFixture(t, "http://127.0.0.1:1")
	s := &Server{ctx: t.Context(), engine: f.e}
	if s.configureControlledReadFixtureRegistries(map[string]string{"approved": "kg.json"}) == nil {
		t.Fatal("registry configuration replaced in place")
	}
	first, err := f.store.load("17", 22, "approved")
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.store.load("17", 22, "approved")
	if err != nil {
		t.Fatal(err)
	}
	if first.fixture == nil || first.fixture == second.fixture {
		t.Fatal("registrations shared a mutable lifetime")
	}
	first.fixture.Invalidate()
	if second.fixture.Err() != nil {
		t.Fatal("one registration invalidated another")
	}
	f.store.fixtureRegistries = nil
	f.write(t, "kg.json", []byte("invalid but unused"))
	spec, err := f.store.load("17", 22, "approved")
	if err != nil || spec.fixture != nil {
		t.Fatal("opt-out path started consuming registry")
	}
	f.e.controlledReads.pending[controlledWorkKey{"17", 22}] = spec
	if s.configureControlledReadFixtureRegistries(map[string]string{"approved": "kg.json"}) == nil {
		t.Fatal("configuration changed after registration")
	}
}

func TestControlledReadKGNoViewMeansNoMarkers(t *testing.T) {
	f := newKGBindingFixture(t, "http://127.0.0.1:1")
	f.input.Resources[0].View = nil
	f.write(t, "approved.json", kgBindingJSON(t, f.input))
	f.registry["resources"].([]any)[0].(map[string]any)["canaries"] = []any{}
	f.write(t, "kg.json", kgBindingJSON(t, f.registry))
	if spec, err := f.store.load("17", 22, "approved"); err != nil || spec.fixture == nil {
		t.Fatal("absent views were not paired with empty markers")
	}
}

func TestControlledReadKGActualWorkerUsesFrozenRegistration(t *testing.T) {
	var hits, finalized atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		want := runtimeSecret
		if n == 2 {
			want += "-peer"
		}
		if r.Header.Get("X-Session") != want {
			t.Error("wrong registered session")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","data":{"note":%q,"private":%q}}`, verificationTestMarker, runtimeSecret)
	}))
	defer target.Close()
	f := newKGBindingFixture(t, target.URL)
	spec := f.register(t)
	frozen := spec.fixture
	// Replacing either input file after registration must not reload or rebind it.
	f.registry["snapshot_id"] = "replaced"
	f.registry["resources"].([]any)[0].(map[string]any)["canaries"].([]any)[0].(map[string]any)["value"] = "replaced-marker"
	f.write(t, "kg.json", kgBindingJSON(t, f.registry))
	f.input.MaxRequests = 1
	f.write(t, "approved.json", kgBindingJSON(t, f.input))
	var request ControlledReadVerificationRequest
	f.e.controlledReads.verification = &controlledReadVerificationConfig{lifecycle: t.Context(), resolve: func(ctx context.Context, r ControlledReadVerificationRequest) (ControlledReadVerificationPlan, error) {
		request = r
		kg, err := r.FixtureSnapshot(ctx)
		if err != nil || kg != frozen || kg.Ref() != "kg-bound-1" {
			t.Error("runtime did not retain registered snapshot")
			return ControlledReadVerificationPlan{}, ErrControlledFindingUnavailable
		}
		expected := configRuntimeExpected(r, spec)
		expected.KGPolicyVersion = kg.PolicyVersion()
		bound, err := r.BindRealInputs(ctx, expected, kg, configRuntimeSource)
		if err != nil {
			return ControlledReadVerificationPlan{}, err
		}
		for _, value := range []any{r, r.config, *r.config} {
			body, err := json.Marshal(value)
			text := string(body) + fmt.Sprintf("%#v", value)
			if err != nil || strings.Contains(text, runtimeSecret) || strings.Contains(text, verificationTestMarker) || strings.Contains(text, f.store.snapshotDir) {
				t.Error("private registry escaped handle representation")
			}
		}
		return ControlledReadVerificationPlan{Candidate: bound.Candidate, KG: bound.KG, Finalize: func(ctx context.Context, e ControlledReadVerificationEvidence) error {
			if e.Request != r || kg.Err() != nil {
				t.Error("KG lifetime ended before finalization returned")
				return ErrControlledFindingUnavailable
			}
			if _, err := r.FixtureSnapshot(ctx); err == nil {
				t.Error("new snapshot lookup remained live during finalization")
				return ErrControlledFindingUnavailable
			}
			finalized.Add(1)
			return nil
		}}, nil
	}}
	f.resume()
	ctx := context.WithValue(t.Context(), controlledReadCompletionClaimKey{}, func() bool { return true })
	run, finish, err := f.e.prepareControlledRead(ctx, f.task, f.intent)
	if err != nil {
		t.Fatal(err)
	}
	defer finish(false)
	provider := &runtimeReadProvider{t: t, name: agent.ControlledHTTPToolName, input: `{"account_ref":"peer","resource_ref":"doc:one"}`}
	if _, _, err := runtimeWorker(t, provider).Execute(run, "worker", 17, nil, nil, f.intent, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatalf("owner/cross requests=%d", hits.Load())
	}
	if err := finish(true); err != nil || finalized.Load() != 1 {
		t.Fatal("audit/finalization failed")
	}
	if frozen.Err() == nil {
		t.Fatal("completed snapshot remained usable")
	}
	if _, err := request.FixtureSnapshot(t.Context()); err == nil {
		t.Fatal("completed handle reopened snapshot")
	}
}

func TestControlledReadKGRejectsReplacementResolverBeforeSend(t *testing.T) {
	for _, kind := range []string{"same-content copy", "different implementation", "invalidated registered", "typed nil"} {
		t.Run(kind, func(t *testing.T) {
			var hits atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); fmt.Fprint(w, `{}`) }))
			defer target.Close()
			f := newKGBindingFixture(t, target.URL)
			spec := f.register(t)
			f.e.controlledReads.verification = &controlledReadVerificationConfig{lifecycle: t.Context(), resolve: func(ctx context.Context, r ControlledReadVerificationRequest) (ControlledReadVerificationPlan, error) {
				kg, err := r.FixtureSnapshot(ctx)
				if err != nil {
					return ControlledReadVerificationPlan{}, err
				}
				expected := configRuntimeExpected(r, spec)
				expected.KGPolicyVersion = kg.PolicyVersion()
				bound, err := r.BindRealInputs(ctx, expected, kg, configRuntimeSource)
				if err != nil {
					return ControlledReadVerificationPlan{}, err
				}
				var replacement idor.PolicyResolver
				switch kind {
				case "same-content copy":
					copy, err := kgfixture.Parse(kgBindingJSON(t, f.registry))
					if err != nil {
						t.Fatal(err)
					}
					if copy.Digest() != kg.Digest() {
						t.Fatal("copy fixture differed")
					}
					replacement = copy
				case "different implementation":
					replacement = configRuntimeKG()
				case "invalidated registered":
					kg.Invalidate()
					replacement = kg
				case "typed nil":
					var empty *kgfixture.Snapshot
					replacement = empty
				}
				if _, err := r.BindRealInputs(ctx, expected, replacement, configRuntimeSource); err == nil {
					t.Error("Bind accepted replacement registry")
				}
				// Bypass Bind to exercise the actual execution's independent pointer guard.
				return ControlledReadVerificationPlan{Candidate: bound.Candidate, KG: replacement, Finalize: func(context.Context, ControlledReadVerificationEvidence) error { return nil }}, nil
			}}
			f.resume()
			ctx, finish, err := f.e.prepareControlledRead(t.Context(), f.task, f.intent)
			if err != nil {
				t.Fatal(err)
			}
			defer finish(false)
			provider := &runtimeReadProvider{t: t, name: agent.ControlledHTTPToolName, input: `{"account_ref":"peer","resource_ref":"doc:one"}`}
			_, _, _ = runtimeWorker(t, provider).Execute(ctx, "worker", 17, nil, nil, f.intent, nil, nil, nil, nil)
			if hits.Load() != 0 {
				t.Fatal("replacement resolver sent target requests")
			}
			if finish(true) == nil {
				t.Fatal("rejected verification reported completion")
			}
		})
	}
}

func TestControlledReadKGSnapshotRequiresLiveExactRequest(t *testing.T) {
	f := newKGBindingFixture(t, "http://127.0.0.1:1")
	spec := f.register(t)
	f.e.controlledReads.verification = &controlledReadVerificationConfig{lifecycle: t.Context(), resolve: func(ctx context.Context, r ControlledReadVerificationRequest) (ControlledReadVerificationPlan, error) {
		if _, err := r.FixtureSnapshot(ctx); err != nil {
			t.Error("actual handle unavailable")
		}
		for _, field := range []string{"task", "intent", "scope", "account", "resource", "JSON"} {
			other := r
			switch field {
			case "task":
				other.TaskID++
			case "intent":
				other.IntentID++
			case "scope":
				other.Scope.RunRef = "other"
			case "account":
				other.AccountRef = "owner"
			case "resource":
				other.ResourceRef = "other:one"
			case "JSON":
				body := kgBindingJSON(t, r)
				var reconstructed ControlledReadVerificationRequest
				if json.Unmarshal(body, &reconstructed) != nil {
					t.Fatal("fixture JSON")
				}
				other = reconstructed
			}
			if _, err := other.FixtureSnapshot(ctx); err == nil {
				t.Errorf("other %s accepted", field)
			}
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := r.FixtureSnapshot(canceled); err == nil {
			t.Error("canceled lookup accepted")
		}
		r.config.state.mu.Lock()
		until := r.config.state.until
		r.config.state.until = time.Now().Add(-time.Second)
		r.config.state.mu.Unlock()
		if _, err := r.FixtureSnapshot(ctx); err == nil {
			t.Error("expired lookup accepted")
		}
		r.config.state.mu.Lock()
		r.config.state.until = until
		r.config.state.mu.Unlock()
		return ControlledReadVerificationPlan{}, ErrControlledFindingUnavailable
	}}
	f.resume()
	ctx, finish, err := f.e.prepareControlledRead(t.Context(), f.task, f.intent)
	if err != nil {
		t.Fatal(err)
	}
	defer finish(false)
	provider := &runtimeReadProvider{t: t, name: agent.ControlledHTTPToolName, input: `{"account_ref":"peer","resource_ref":"doc:one"}`}
	_, _, _ = runtimeWorker(t, provider).Execute(ctx, "worker", 17, nil, nil, f.intent, nil, nil, nil, nil)
	if _, err := (ControlledReadVerificationRequest{}).FixtureSnapshot(t.Context()); err == nil {
		t.Fatal("zero request accepted")
	}
	_ = spec
}
