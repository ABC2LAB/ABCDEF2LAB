package transmit_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
)

func TestVerifyReadDetailedBindsActualObservationsWithoutResending(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, "{\"status\":\"ok\",\"data\":{\"note\":\""+e2Canary+"\",\"private\":\""+e2Secret+"\"}}")
	}))
	defer server.Close()
	rec := &e2Recorder{}
	cfg := e2FixtureConfig(server.URL, rec)
	tx, err := transmit.NewHTTP(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Stop()
	candidate := idor.Candidate{Owner: cfg.Sessions[0].Account, Evaluated: cfg.Sessions[1].Account, ResourceRef: cfg.Resources[0].Ref}
	started := time.Now()
	detail, reason := tx.VerifyReadDetailed(candidate, e2Policy{})
	if reason != "" || detail.Result.Verdict.Value != oracle.Confirmed || hits.Load() != 2 {
		t.Fatalf("verification reason=%s verdict=%s hits=%d", reason, detail.Result.Verdict.Value, hits.Load())
	}
	if !detail.PolicyResolved || detail.FrozenPolicy.ExpectedAccess != oracle.AccessDeny ||
		len(detail.CanaryFields) != 1 || detail.CanaryFields[0] != "note" {
		t.Fatalf("frozen policy/field metadata missing: %+v", detail)
	}
	for i, read := range []transmit.ReadObservation{detail.Owner, detail.Evaluated} {
		if !read.Captured || read.Receipt.Reason != transmit.Completed || !read.Receipt.RequestWritten ||
			!read.Receipt.ResponseReceived || read.Receipt.Scope != cfg.Scope || read.Receipt.Attempt != uint64(i+1) ||
			read.Receipt.AccountRef != cfg.Sessions[i].Account.Label || read.Receipt.SessionRef != cfg.Sessions[i].Account.Handle ||
			read.Receipt.ResourceRef != candidate.ResourceRef || read.Capture.ObservedAt.Before(started) ||
			read.Capture.ObservedAt.After(time.Now()) {
			t.Fatalf("read %d is not the actual complete observation: %+v", i, read)
		}
	}
	if detail.Evaluated.Capture.ObservedAt.Before(detail.Owner.Capture.ObservedAt) || len(rec.events) != 4 {
		t.Fatal("owner/cross order or actual audit sequence lost")
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{e2Canary, e2Secret, server.URL} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatal("ephemeral capture or raw private input was exported")
		}
	}
}

func TestVerifyReadDetailedPreservesPartialReadsAndPreflightRefusal(t *testing.T) {
	for _, mode := range []string{"second-read-budget", "preflight-marker"} {
		t.Run(mode, func(t *testing.T) {
			var hits atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, "{\"status\":\"ok\",\"data\":{\"note\":\""+e2Canary+"\"}}")
			}))
			defer server.Close()
			cfg := e2FixtureConfig(server.URL, &e2Recorder{})
			if mode == "second-read-budget" {
				cfg.MaxRequests = 1
			} else {
				cfg.Resources[0].View.Markers[0].Value = "different-registered-marker"
			}
			tx, err := transmit.NewHTTP(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Stop()
			candidate := idor.Candidate{Owner: cfg.Sessions[0].Account, Evaluated: cfg.Sessions[1].Account, ResourceRef: cfg.Resources[0].Ref}
			detail, reason := tx.VerifyReadDetailed(candidate, e2Policy{})
			if detail.Result.Verdict.Value != oracle.Undecidable {
				t.Fatal("missing observation became a confirmed result")
			}
			if mode == "preflight-marker" {
				if reason != transmit.CanaryMismatch || hits.Load() != 0 || detail.Owner.Captured || detail.Evaluated.Captured ||
					detail.Owner.Receipt.Attempt != 0 || detail.Evaluated.Receipt.Attempt != 0 || detail.PolicyResolved {
					t.Fatalf("preflight refusal produced execution evidence: %+v reason=%s hits=%d", detail, reason, hits.Load())
				}
			} else if reason != "" || hits.Load() != 1 || !detail.Owner.Captured || detail.Evaluated.Captured ||
				detail.Evaluated.Receipt.Reason != transmit.BudgetExhausted || detail.Evaluated.Receipt.Admitted {
				t.Fatalf("partial read evidence lost: %+v reason=%s hits=%d", detail, reason, hits.Load())
			}
		})
	}
}
