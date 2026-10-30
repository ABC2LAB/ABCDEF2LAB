package transmit_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
)

const e2Canary = "fixture-canary-37f9a4"
const e2Secret = "PRIVATE-SENTINEL-MUST-NOT-LEAVE"

type e2Recorder struct {
	mu     sync.Mutex
	events []transmit.Event
}

func (r *e2Recorder) Record(_ context.Context, event transmit.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}

func e2FixtureConfig(target string, recorder transmit.Recorder) transmit.Config {
	u, _ := url.Parse(target)
	expiry := time.Now().Add(time.Hour)
	return transmit.Config{
		Scope:  transmit.Scope{RunRef: "run-1", TaskRef: "task-1", CaseRef: "case-1", ResetRef: "reset-1", PolicyRef: "execution-approval-1"},
		Origin: target, ConnectIP: netip.MustParseAddr(u.Hostname()), ExpiresAt: expiry,
		Sessions: []transmit.Session{
			{Account: idor.AccountRef{Label: "owner", Handle: "session-a", Status: oracle.SubjectVerified}, ExpiresAt: expiry, Headers: http.Header{"Authorization": {"Bearer " + e2Secret}}},
			{Account: idor.AccountRef{Label: "peer", Handle: "session-b", Status: oracle.SubjectVerified}, ExpiresAt: expiry, Headers: http.Header{"Cookie": {"session=" + e2Secret}}},
		},
		Resources: []transmit.Resource{{
			Ref: "doc:one", URL: target + "/document", ApprovalRef: "read-approval-1", ReadOnly: true, Accounts: []string{"owner", "peer"},
			View: &transmit.JSONViewRule{StatusPath: []string{"status"}, NormalValue: "ok", ErrorValues: []string{"error"}, DataPath: []string{"data"}, Markers: []idor.Marker{{Field: "note", Value: e2Canary}}},
		}},
		MaxRequests: 10, RequestTimeout: time.Second, MaxBodyBytes: 4096, Recorder: recorder,
	}
}

func e2Request(cfg transmit.Config, account int) idor.AccessRequest {
	return idor.AccessRequest{As: cfg.Sessions[account].Account, Method: "GET", ResourceRef: cfg.Resources[0].Ref}
}

func TestProjectionOnlyRegisteredExactCanaryLeavesBoundary(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantBody   string
		normal     bool
		classified bool
		errorBody  bool
	}{
		{name: "exact with unrelated sensitive fields", body: `{"status":"ok","data":{"note":"` + e2Canary + `","private":"` + e2Secret + `"}}`, wantBody: e2Canary, normal: true, classified: true},
		{name: "partial match with sensitive suffix", body: `{"status":"ok","data":{"note":"` + e2Canary + e2Secret + `"}}`, normal: true, classified: true},
		{name: "unregistered field", body: `{"status":"ok","data":{"private":"` + e2Canary + `"}}`},
		{name: "HTTP 200 error template", body: `{"status":"error","data":{"note":"` + e2Canary + `"}}`, classified: true, errorBody: true},
		{name: "empty data", body: `{"status":"ok","data":{}}`},
		{name: "unknown status", body: `{"status":"maybe","data":{"note":"` + e2Canary + `"}}`},
		{name: "duplicate key", body: `{"status":"error","status":"ok","data":{"note":"` + e2Canary + `"}}`},
		{name: "escaped duplicate", body: `{"status":"ok","data":{"note":"x","n\u006fte":"` + e2Canary + `"}}`},
		{name: "duplicate in unrelated branch", body: `{"status":"ok","data":{"note":"` + e2Canary + `"},"other":{"x":1,"x":2}}`},
		{name: "trailing document", body: `{"status":"ok","data":{"note":"` + e2Canary + `"}} {}`},
		{name: "malformed", body: `{"status":"ok","data":`},
		{name: "invalid UTF8", body: `{"status":"ok","data":{"note":"` + e2Canary + `"},"other":"` + string([]byte{0xff}) + `"}`},
		{name: "too deep", body: `{"status":"ok","data":{"note":"` + e2Canary + `"},"other":` + strings.Repeat("[", 35) + "0" + strings.Repeat("]", 35) + "}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			recorder := &e2Recorder{}
			cfg := e2FixtureConfig(server.URL, recorder)
			tx, err := transmit.NewHTTP(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Stop()
			capture, receipt, ok := tx.SendDetailed(e2Request(cfg, 0))
			if !ok || receipt.Reason != transmit.Completed || !receipt.RequestWritten || !receipt.ResponseReceived {
				t.Fatalf("bounded response not captured: ok=%v receipt=%+v", ok, receipt)
			}
			if capture.View.Body != tc.wantBody || capture.View.Normal != tc.normal || capture.View.TemplateClassified != tc.classified || capture.View.ErrorTemplate != tc.errorBody {
				t.Fatalf("incorrect projection flags/body: %+v", capture.View)
			}
			data, err := json.Marshal([]any{capture, receipt, recorder.events})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), e2Secret) || strings.Contains(string(data), server.URL) || strings.Contains(string(data), tc.body) {
				t.Fatal("raw response, credential or target URL escaped capture/audit boundary")
			}
		})
	}
}

func TestProjectionSuppressesActualRequestReflection(t *testing.T) {
	for _, source := range []string{"path", "query", "header", "input", "label", "resource", "handle"} {
		t.Run(source, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"status":"ok","data":{"note":"`+e2Canary+`"}}`)
			}))
			defer server.Close()
			cfg := e2FixtureConfig(server.URL, &e2Recorder{})
			switch source {
			case "path":
				cfg.Resources[0].URL += "/" + e2Canary
			case "query":
				// The decoded query, as well as the raw URI, must be considered.
				cfg.Resources[0].URL += "?echo=" + strings.ReplaceAll(e2Canary, "-", "%2D")
			case "header":
				cfg.Sessions[0].Headers.Set("X-Echo", e2Canary)
			case "label":
				cfg.Sessions[0].Account.Label = e2Canary
				cfg.Resources[0].Accounts[0] = e2Canary
			case "resource":
				cfg.Resources[0].Ref = e2Canary
			case "handle":
				cfg.Sessions[0].Account.Handle = e2Canary
			}
			tx, err := transmit.NewHTTP(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Stop()
			request := e2Request(cfg, 0)
			if source == "input" {
				request.Inputs = []string{e2Canary}
			}
			capture, ok := tx.Send(request)
			if !ok || capture.View.Body != "" {
				t.Fatalf("reflection was projected: ok=%v view=%+v", ok, capture.View)
			}
		})
	}
}

func TestProjectionCannotInferNormalFromHTTP200(t *testing.T) {
	for _, mode := range []string{"no-rule", "not-json", "trivial-marker", "HTTP-error"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if mode == "not-json" {
					w.Header().Set("Content-Type", "text/plain")
				}
				if mode == "HTTP-error" {
					w.WriteHeader(403)
				}
				fmt.Fprint(w, `{"status":"ok","data":{"note":"`+e2Canary+`"}}`)
			}))
			defer server.Close()
			cfg := e2FixtureConfig(server.URL, &e2Recorder{})
			if mode == "no-rule" {
				cfg.Resources[0].View = nil
			}
			if mode == "trivial-marker" {
				cfg.Resources[0].View.Markers[0].Trivial = true
			}
			tx, err := transmit.NewHTTP(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Stop()
			capture, ok := tx.Send(e2Request(cfg, 0))
			if !ok || capture.View.Normal || capture.View.TemplateClassified || capture.View.Body != "" {
				t.Fatalf("unclassified response became evidence: %+v", capture.View)
			}
		})
	}
}

type e2Policy struct{ expired bool }

func (p e2Policy) ResolvePolicy(string, idor.AccountRef, idor.AccountRef) (oracle.PolicyAssertion, bool) {
	return oracle.PolicyAssertion{
		ExpectedAccess: oracle.AccessDeny, Sharing: oracle.ResolvedNone, Delegation: oracle.ResolvedNone,
		HasRegisteredSource: true, HintMatchesCanonical: true,
		ValidFrom: time.Now().Add(-time.Hour), ValidUntil: time.Now().Add(time.Hour),
	}, !p.expired
}

func (e2Policy) Canaries(string, idor.AccountRef) ([]idor.Marker, bool) {
	return []idor.Marker{{Field: "note", Value: e2Canary}}, true
}

func TestVerifierWithRealReadOnlyTransport(t *testing.T) {
	for _, scenario := range []string{"fixture-leak", "error-template", "unverified-session", "expired-policy"} {
		t.Run(scenario, func(t *testing.T) {
			var reads, writes atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					writes.Add(1)
					return
				}
				reads.Add(1)
				status := "ok"
				if scenario == "error-template" {
					status = "error"
				}
				w.Header().Set("Content-Type", "application/json")
				// A non-sensitive canary was planted by the fixture, never by Verify.
				fmt.Fprintf(w, `{"status":%q,"data":{"note":%q,"secret":%q}}`, status, e2Canary, e2Secret)
			}))
			defer server.Close()
			recorder := &e2Recorder{}
			cfg := e2FixtureConfig(server.URL, recorder)
			if scenario == "unverified-session" {
				cfg.Sessions[1].Account.Status = oracle.SubjectUnknown
			}
			tx, err := transmit.NewHTTP(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Stop()
			candidate := idor.Candidate{Owner: cfg.Sessions[0].Account, Evaluated: cfg.Sessions[1].Account, ResourceRef: cfg.Resources[0].Ref}
			// A caller-supplied verified flag must not override the trusted registry.
			candidate.Evaluated.Status = oracle.SubjectVerified
			result, reason := tx.VerifyRead(candidate, e2Policy{expired: scenario == "expired-policy"})
			if scenario == "unverified-session" && reason != transmit.UnverifiedAccount || scenario != "unverified-session" && reason != "" {
				t.Fatalf("unexpected preflight reason: %s", reason)
			}
			want := oracle.Undecidable
			if scenario == "fixture-leak" {
				want = oracle.Confirmed
			}
			if result.Verdict.Value != want {
				t.Fatalf("verdict=%s want=%s reasons=%v", result.Verdict.Value, want, result.Verdict.ReasonCodes)
			}
			wantReads := int64(2)
			if scenario == "unverified-session" {
				wantReads = 0
			}
			if reads.Load() != wantReads || writes.Load() != 0 {
				t.Fatalf("target reads=%d writes=%d want reads=%d writes=0", reads.Load(), writes.Load(), wantReads)
			}
			data, err := json.Marshal([]any{result, recorder.events})
			if err != nil {
				t.Fatal(err)
			}
			wantEvents := 1 + int(wantReads)*2 // denied setup result + admitted reads' intent/results
			if scenario == "unverified-session" {
				wantEvents = 0 // preflight refuses before invoking Verify or Send
			}
			if strings.Contains(string(data), e2Secret) || len(recorder.events) != wantEvents {
				t.Fatal("unsafe or missing audit events for denied setup and two reads")
			}
		})
	}
}
