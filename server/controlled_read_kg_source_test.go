package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/kgfixture"
)

type kgSourceCase struct {
	f        *kgBindingFixture
	request  ControlledReadVerificationRequest
	expected evidencebind.Context
	snapshot *kgfixture.Snapshot
	source   e3record.Source
}

func kgSourceMetadata(expected evidencebind.Context, snapshot *kgfixture.Snapshot) e3record.Source {
	// Synthetic metadata tests consistency only; false completion flags remain
	// false and no authority, audit head or storage authorization is manufactured.
	return e3record.Source{Expected: expected, ExplorationID: 91, ModeVersion: 7, EvidenceRevision: 4,
		KGSnapshotRef: snapshot.Ref(), KGSnapshotDigest: snapshot.Digest(), GenerationRef: snapshot.GenerationRef(), InstanceRef: snapshot.InstanceRef(), ResetGeneration: snapshot.ResetGeneration(),
		ModelTag: "synthetic-source-model", EvalRunID: "separate-evaluation-ref", Severity: "medium", CanaryFields: []string{"note"}, TrafficEvidenceIDs: []int64{81},
	}
}

func newKGSourceCase(t *testing.T) *kgSourceCase {
	t.Helper()
	f := newKGBindingFixture(t, "http://127.0.0.1:1")
	spec := f.register(t)
	c := &kgSourceCase{f: f}
	f.e.controlledReads.verification = &controlledReadVerificationConfig{lifecycle: t.Context(), resolve: func(_ context.Context, r ControlledReadVerificationRequest) (ControlledReadVerificationPlan, error) {
		c.request = r
		return ControlledReadVerificationPlan{}, ErrControlledFindingUnavailable
	}}
	f.resume()
	run, finish, err := f.e.prepareControlledRead(t.Context(), f.task, f.intent)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = finish(false) })
	provider := &runtimeReadProvider{t: t, name: agent.ControlledHTTPToolName, input: `{"account_ref":"peer","resource_ref":"doc:one"}`}
	_, _, _ = runtimeWorker(t, provider).Execute(run, "worker", 17, nil, nil, f.intent, nil, nil, nil, nil)
	c.snapshot, err = c.request.FixtureSnapshot(t.Context())
	if err != nil {
		t.Fatal("actual runtime handle unavailable")
	}
	c.expected = configRuntimeExpected(c.request, spec)
	c.expected.KGPolicyVersion = c.snapshot.PolicyVersion()
	c.source = kgSourceMetadata(c.expected, c.snapshot)
	return c
}
func (c *kgSourceCase) plan(reader e3record.SourceReader) ControlledReadFindingPlan {
	return ControlledReadFindingPlan{Expected: c.expected, Candidate: idor.Candidate{Owner: c.expected.Owner, Evaluated: c.expected.Evaluated, ResourceRef: c.expected.ResourceRef}, KG: c.snapshot, Source: reader}
}
func assertKGSourceRefusal(t *testing.T, source e3record.Source, err error) {
	t.Helper()
	if !errors.Is(err, ErrControlledFindingUnavailable) || !reflect.DeepEqual(source, e3record.Source{}) {
		t.Fatal("source failure retained data or did not refuse")
	}
	if strings.Contains(fmt.Sprintf("%#v", err), runtimeSecret) || strings.Contains(err.Error(), verificationTestMarker) {
		t.Fatal("source error leaked private input")
	}
}

func TestControlledReadKGSourcePreservesAuthenticatedReaderContextAndFields(t *testing.T) {
	c := newKGSourceCase(t)
	calls := 0
	type contextKey struct{}
	finalCtx, cancel := context.WithCancel(context.WithValue(t.Context(), contextKey{}, "scope-marker"))
	defer cancel()
	read := func(ctx context.Context, expected evidencebind.Context) (e3record.Source, error) {
		calls++
		if ctx != finalCtx || ctx.Value(contextKey{}) != "scope-marker" || expected != c.expected {
			t.Error("finalizer context or exact expected changed")
		}
		return c.source, nil
	}
	wrapped := bindControlledReadFixtureFindings(func(context.Context, ControlledReadVerificationRequest) (ControlledReadFindingPlan, error) {
		return c.plan(read), nil
	})
	plan, err := wrapped(t.Context(), c.request)
	if err != nil || calls != 0 {
		t.Fatal("source was called during planning")
	}
	// Closing new Config lookups must not prevent an already captured Source from
	// using the KG inside finalization. The real runtime ordering is tested below.
	c.request.config.state.mu.Lock()
	c.request.config.state.closed = true
	c.request.config.state.mu.Unlock()
	got, err := plan.Source(finalCtx, c.expected)
	if err != nil || calls != 1 || !reflect.DeepEqual(got, c.source) {
		t.Fatal("valid source fields were rejected or rewritten")
	}
	if got.Completed || got.Finalized || got.EvalRunID != c.source.EvalRunID || got.ModelTag != c.source.ModelTag {
		t.Fatal("wrapper manufactured completion or rewrote non-KG fields")
	}
}

func TestControlledReadKGSourceRejectsEachProvenanceMismatch(t *testing.T) {
	for _, field := range []string{"snapshot ref", "snapshot digest", "generation", "instance", "reset", "source context"} {
		t.Run(field, func(t *testing.T) {
			c := newKGSourceCase(t)
			source := c.source
			switch field {
			case "snapshot ref":
				source.KGSnapshotRef = "other-snapshot"
			case "snapshot digest":
				source.KGSnapshotDigest[0] ^= 1
			case "generation":
				source.GenerationRef = "other-generation"
			case "instance":
				source.InstanceRef = "other-instance"
			case "reset":
				source.ResetGeneration = "other-reset"
			case "source context":
				source.Expected.Scope.RunRef = "other-run"
			}
			reader := func(context.Context, evidencebind.Context) (e3record.Source, error) { return source, nil }
			plan, err := bindControlledReadFixtureFindings(func(context.Context, ControlledReadVerificationRequest) (ControlledReadFindingPlan, error) {
				return c.plan(reader), nil
			})(t.Context(), c.request)
			if err != nil {
				t.Fatal(err)
			}
			got, err := plan.Source(t.Context(), c.expected)
			assertKGSourceRefusal(t, got, err)
		})
	}
}

func TestControlledReadKGSourceRejectsPlanBeforeReaderUse(t *testing.T) {
	if bindControlledReadFixtureFindings(nil) != nil {
		t.Fatal("nil resolver acquired a wrapper")
	}
	for _, kind := range []string{"policy version", "nil source", "different KG", "typed nil KG", "resolver error", "cancel during resolver", "close during resolver", "nil context", "canceled context", "zero request"} {
		t.Run(kind, func(t *testing.T) {
			c := newKGSourceCase(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			readerCalls := 0
			resolverCalls := 0
			reader := func(context.Context, evidencebind.Context) (e3record.Source, error) {
				readerCalls++
				return c.source, nil
			}
			resolve := func(context.Context, ControlledReadVerificationRequest) (ControlledReadFindingPlan, error) {
				resolverCalls++
				plan := c.plan(reader)
				switch kind {
				case "policy version":
					plan.Expected.KGPolicyVersion = "stale-policy-version"
				case "nil source":
					plan.Source = nil
				case "different KG":
					copy, err := kgfixture.Parse(kgBindingJSON(t, c.f.registry))
					if err != nil {
						t.Fatal(err)
					}
					plan.KG = copy
				case "typed nil KG":
					var empty *kgfixture.Snapshot
					plan.KG = empty
				case "resolver error":
					return plan, errors.New(runtimeSecret)
				case "cancel during resolver":
					cancel()
				case "close during resolver":
					c.request.config.state.mu.Lock()
					c.request.config.state.closed = true
					c.request.config.state.mu.Unlock()
				}
				return plan, nil
			}
			request := c.request
			if kind == "nil context" {
				ctx = nil
			}
			if kind == "canceled context" {
				cancel()
			}
			if kind == "zero request" {
				request = ControlledReadVerificationRequest{}
			}
			plan, err := bindControlledReadFixtureFindings(resolve)(ctx, request)
			if !errors.Is(err, ErrControlledFindingUnavailable) || plan.Source != nil || plan.KG != nil || readerCalls != 0 {
				t.Fatal("invalid plan was exposed or source ran early")
			}
			if (kind == "nil context" || kind == "canceled context" || kind == "zero request") && resolverCalls != 0 {
				t.Fatal("invalid handle/context reached provider")
			}
			if strings.Contains(err.Error(), runtimeSecret) {
				t.Fatal("provider error leaked")
			}
		})
	}
}

func TestControlledReadKGSourceChecksFreshContextAndSnapshotAroundEveryRead(t *testing.T) {
	for _, kind := range []string{"wrong expected", "nil context", "canceled before", "canceled during", "invalid before", "invalid during", "reader error"} {
		t.Run(kind, func(t *testing.T) {
			c := newKGSourceCase(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			reader := func(context.Context, evidencebind.Context) (e3record.Source, error) {
				calls++
				switch kind {
				case "canceled during":
					cancel()
				case "invalid during":
					c.snapshot.Invalidate()
				case "reader error":
					return c.source, errors.New(runtimeSecret)
				}
				return c.source, nil
			}
			plan, err := bindControlledReadFixtureFindings(func(context.Context, ControlledReadVerificationRequest) (ControlledReadFindingPlan, error) {
				return c.plan(reader), nil
			})(t.Context(), c.request)
			if err != nil {
				t.Fatal(err)
			}
			expected := c.expected
			switch kind {
			case "wrong expected":
				expected.Scope.PolicyRef = "other-policy"
			case "nil context":
				ctx = nil
			case "canceled before":
				cancel()
			case "invalid before":
				c.snapshot.Invalidate()
			}
			source, err := plan.Source(ctx, expected)
			assertKGSourceRefusal(t, source, err)
			before := kind == "wrong expected" || kind == "nil context" || kind == "canceled before" || kind == "invalid before"
			if before && calls != 0 {
				t.Fatal("rejected read invoked provider")
			}
			if !before && calls != 1 {
				t.Fatal("reader was not invoked exactly once")
			}
		})
	}
	t.Run("no cached metadata", func(t *testing.T) {
		c := newKGSourceCase(t)
		calls := 0
		reader := func(context.Context, evidencebind.Context) (e3record.Source, error) {
			calls++
			source := c.source
			if calls == 2 {
				source.GenerationRef = "changed-generation"
			}
			return source, nil
		}
		plan, err := bindControlledReadFixtureFindings(func(context.Context, ControlledReadVerificationRequest) (ControlledReadFindingPlan, error) {
			return c.plan(reader), nil
		})(t.Context(), c.request)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := plan.Source(t.Context(), c.expected); err != nil {
			t.Fatal(err)
		}
		source, err := plan.Source(t.Context(), c.expected)
		assertKGSourceRefusal(t, source, err)
		if calls != 2 {
			t.Fatal("source was cached")
		}
	})
}

func TestControlledReadKGSourceActualWorkerFinalizationBoundary(t *testing.T) {
	for _, kind := range []string{"valid", "wrong policy version", "wrong source digest"} {
		t.Run(kind, func(t *testing.T) {
			started := time.Now()
			var hits, sourceCalls, accepted atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"status":"ok","data":{"note":%q}}`, verificationTestMarker)
			}))
			defer target.Close()
			f := newKGBindingFixture(t, target.URL)
			spec := f.register(t)
			type finalScopeKey struct{}
			finding := bindControlledReadFixtureFindings(func(ctx context.Context, r ControlledReadVerificationRequest) (ControlledReadFindingPlan, error) {
				snapshot, err := r.FixtureSnapshot(ctx)
				if err != nil {
					return ControlledReadFindingPlan{}, err
				}
				expected := configRuntimeExpected(r, spec)
				expected.KGPolicyVersion = snapshot.PolicyVersion()
				if kind == "wrong policy version" {
					expected.KGPolicyVersion = "stale-policy-version"
				}
				reader := func(ctx context.Context, received evidencebind.Context) (e3record.Source, error) {
					sourceCalls.Add(1)
					if ctx.Value(finalScopeKey{}) != "trusted-scope-context" || received != expected || snapshot.Err() != nil {
						t.Error("finalization context or KG lifetime changed")
					}
					if _, err := r.FixtureSnapshot(ctx); err == nil {
						t.Error("fresh Config lookup remained open during finalization")
					}
					source := kgSourceMetadata(expected, snapshot)
					if kind == "wrong source digest" {
						source.KGSnapshotDigest[0] ^= 1
					}
					return source, nil
				}
				return r.BindRealInputs(ctx, expected, snapshot, reader)
			})
			f.e.controlledReads.verification = &controlledReadVerificationConfig{lifecycle: t.Context(), resolve: func(ctx context.Context, r ControlledReadVerificationRequest) (ControlledReadVerificationPlan, error) {
				plan, err := finding(ctx, r)
				if err != nil {
					return ControlledReadVerificationPlan{}, err
				}
				return ControlledReadVerificationPlan{Candidate: plan.Candidate, KG: plan.KG, Finalize: func(ctx context.Context, _ ControlledReadVerificationEvidence) error {
					ctx = context.WithValue(ctx, finalScopeKey{}, "trusted-scope-context")
					if _, err := plan.Source(ctx, plan.Expected); err != nil {
						return err
					}
					accepted.Add(1)
					return nil
				}}, nil
			}}
			f.resume()
			parent := context.WithValue(t.Context(), controlledReadCompletionClaimKey{}, func() bool { return true })
			// The finalizer supplies its own Source read context below.
			run, finish, err := f.e.prepareControlledRead(parent, f.task, f.intent)
			if err != nil {
				t.Fatal(err)
			}
			defer finish(false)
			provider := &runtimeReadProvider{t: t, name: agent.ControlledHTTPToolName, input: `{"account_ref":"peer","resource_ref":"doc:one"}`}
			_, _, _ = runtimeWorker(t, provider).Execute(run, "worker", 17, nil, nil, f.intent, nil, nil, nil, nil)
			err = finish(true)
			if kind == "valid" {
				if err != nil || hits.Load() != 2 || sourceCalls.Load() != 1 || accepted.Load() != 1 {
					t.Fatalf("valid boundary failed rejected=%v hits=%d source=%d accepted=%d elapsed=%s", err != nil, hits.Load(), sourceCalls.Load(), accepted.Load(), time.Since(started))
				}
			} else {
				if err == nil || accepted.Load() != 0 {
					t.Fatalf("mismatched provenance boundary rejected=%v hits=%d source=%d accepted=%d elapsed=%s", err != nil, hits.Load(), sourceCalls.Load(), accepted.Load(), time.Since(started))
				}
				wantHits, wantCalls := int64(2), int64(1)
				if kind == "wrong policy version" {
					wantHits, wantCalls = 0, 0
				}
				if hits.Load() != wantHits || sourceCalls.Load() != wantCalls {
					t.Fatalf("unexpected boundary counts rejected=%v hits=%d source=%d accepted=%d elapsed=%s", err != nil, hits.Load(), sourceCalls.Load(), accepted.Load(), time.Since(started))
				}
			}
		})
	}
}

func TestControlledReadKGSourceStrictBootstrapInstallsOptInGuard(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled_%v", enabled), func(t *testing.T) {
			if os.Getenv("ARTEX_PG_DSN") == "" {
				t.Skip("set ARTEX_PG_DSN to a dedicated synthetic PostgreSQL database")
			}
			c := newKGSourceCase(t)
			m, err := NewManager(t.TempDir(), "")
			if err != nil {
				t.Fatal("configured synthetic PostgreSQL fixture unavailable")
			}
			t.Cleanup(func() { _ = m.Close() })
			t.Setenv("ARTEX_E2_SNAPSHOT_DIR", c.f.store.snapshotDir)
			t.Setenv("ARTEX_E2_STATE_DIR", c.f.store.stateDir)
			t.Setenv("ARTEX_LLM_API_KEY", "")
			t.Setenv(notifyBackgroundDisabledEnv, "1")
			preserveRevisionWireAuth(t, m.pg)
			if err := m.pg.SetSetting(authPassKey, "synthetic-disabled-login"); err != nil {
				t.Fatal(err)
			}
			keyDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(keyDir, jwtKeyFilename), bytes.Repeat([]byte("s"), 32), 0o600); err != nil {
				t.Fatal(err)
			}
			anchor, err := os.Open(c.f.store.stateDir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = anchor.Close() })
			wrong := c.expected
			wrong.KGPolicyVersion = "stale-policy-version"
			sourceCalls := 0
			options := ControlledReadHostOptions{Mode: ControlledTaskModeHost{RootPath: c.f.store.stateDir, RootAnchor: anchor, CheckRoot: func(ctx context.Context) error { return ctx.Err() }, CheckTaskQuiescent: func(ctx context.Context, _ int64) error { return ctx.Err() }}, Findings: func(context.Context, ControlledReadVerificationRequest) (ControlledReadFindingPlan, error) {
				plan := c.plan(func(context.Context, evidencebind.Context) (e3record.Source, error) {
					sourceCalls++
					return c.source, nil
				})
				plan.Expected = wrong
				return plan, nil
			}}
			if enabled {
				options.FixtureRegistries = map[string]string{"approved": "kg.json"}
			}
			skillDir, dataDir := t.TempDir(), t.TempDir()
			life, cancel := context.WithCancel(t.Context())
			var strict *Server
			t.Cleanup(func() { cancel(); drainTestServer(t, strict) })
			strict, err = NewWithControlledReadHost(life, m, skillDir, dataDir, keyDir, options)
			if err != nil {
				t.Fatal("synthetic strict bootstrap failed")
			}
			_, err = strict.engine.controlledReads.verification.resolve(t.Context(), c.request)
			if enabled && err == nil {
				t.Fatal("strict fixture host did not install source/version guard")
			}
			if !enabled && err != nil {
				t.Fatal("nil fixture option changed compatibility path")
			}
			if sourceCalls != 0 {
				t.Fatal("bootstrap/resolve called final Source early")
			}
		})
	}
}
