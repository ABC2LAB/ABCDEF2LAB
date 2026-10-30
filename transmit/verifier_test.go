package transmit_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
)

type e2ChangingKG struct {
	markers     []idor.Marker
	policyCalls int
	markerCalls int
}

func (kg *e2ChangingKG) ResolvePolicy(resource string, owner, evaluated idor.AccountRef) (oracle.PolicyAssertion, bool) {
	kg.policyCalls++
	if kg.policyCalls > 1 {
		return oracle.PolicyAssertion{}, false
	}
	return (e2Policy{}).ResolvePolicy(resource, owner, evaluated)
}

func (kg *e2ChangingKG) Canaries(string, idor.AccountRef) ([]idor.Marker, bool) {
	kg.markerCalls++
	if kg.markerCalls > 1 {
		return nil, false
	}
	return kg.markers, true
}

func TestVerifyReadPinsExactCanaryAndPolicySnapshot(t *testing.T) {
	for _, mode := range []string{"matching-reordered", "substring", "field", "trivial", "duplicate", "missing"} {
		t.Run(mode, func(t *testing.T) {
			var hits atomic.Int64
			kg := &e2ChangingKG{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				// Mutation after preflight must not change the frozen KG answers.
				if mode == "matching-reordered" {
					kg.markers[0].Value = "changed-after-snapshot"
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"status":"ok","data":{"note":"`+e2Canary+`"}}`)
			}))
			defer server.Close()
			cfg := e2FixtureConfig(server.URL, &e2Recorder{})
			cfg.Resources[0].View.Markers = append(cfg.Resources[0].View.Markers, idor.Marker{Field: "owner", Value: "owner-data-marker"})
			kg.markers = append([]idor.Marker(nil), cfg.Resources[0].View.Markers...)
			switch mode {
			case "matching-reordered":
				kg.markers[0], kg.markers[1] = kg.markers[1], kg.markers[0]
			case "substring":
				kg.markers[0].Value = "fixture-"
			case "field":
				kg.markers[0].Field = "unrelated"
			case "trivial":
				kg.markers[0].Trivial = true
			case "duplicate":
				kg.markers[1] = kg.markers[0]
			case "missing":
				kg.markers = nil
			}
			tx, err := transmit.NewHTTP(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Stop()
			// The constructor must also isolate nested marker/path configuration.
			cfg.Resources[0].View.Markers[0].Value = "caller-mutated"
			cfg.Resources[0].View.DataPath[0] = "wrong-path"
			cfg.Resources[0].View.StatusPath[0] = "wrong-status"
			candidate := idor.Candidate{Owner: cfg.Sessions[0].Account, Evaluated: cfg.Sessions[1].Account, ResourceRef: cfg.Resources[0].Ref}
			result, reason := tx.VerifyRead(candidate, kg)
			if mode == "matching-reordered" {
				if reason != "" || result.Verdict.Value != oracle.Confirmed || hits.Load() != 2 {
					t.Fatalf("snapshot changed after binding: reason=%s verdict=%s hits=%d", reason, result.Verdict.Value, hits.Load())
				}
			} else if reason != transmit.CanaryMismatch || result.Verdict.Value != oracle.Undecidable || hits.Load() != 0 {
				t.Fatalf("mismatched snapshot passed: reason=%s verdict=%s hits=%d", reason, result.Verdict.Value, hits.Load())
			}
			if kg.policyCalls != 1 || kg.markerCalls != 1 {
				t.Fatalf("KG was re-queried: policy=%d markers=%d", kg.policyCalls, kg.markerCalls)
			}
		})
	}
}

func TestDuplicateSessionAndAmbiguousCanaryRegistrationRefused(t *testing.T) {
	for _, mode := range []string{"same-session", "same-marker-field"} {
		t.Run(mode, func(t *testing.T) {
			cfg := e2FixtureConfig("http://127.0.0.1:1", &e2Recorder{})
			if mode == "same-session" {
				cfg.Sessions[1].Account.Handle = cfg.Sessions[0].Account.Handle
			} else {
				cfg.Resources[0].View.Markers = append(cfg.Resources[0].View.Markers, cfg.Resources[0].View.Markers[0])
			}
			if _, err := transmit.NewHTTP(context.Background(), cfg); err == nil {
				t.Fatal("ambiguous registration accepted")
			}
		})
	}
}
