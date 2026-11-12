package e2proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHeaderModeAndClassifiedInputs(t *testing.T) {
	cfg := coreConfig(t, "http://target.invalid", &coreRecorder{}, 2)
	execution, err := NewExecution(t.Context(), cfg)
	if err != nil || execution.Snapshot().HeaderMode != HeaderPreserve || execution.Policy().HeaderMode != HeaderPreserve {
		t.Fatal("default mode was not preserved")
	}
	for _, name := range []string{"default_credentials", "unknown_mode", "ordinary_auth", "case_duplicate", "class_overlap", "credential_encoding", "credential_upgrade", "credential_framing", "credential_hop"} {
		t.Run(name, func(t *testing.T) {
			cfg := coreConfig(t, "http://target.invalid", &coreRecorder{}, 2)
			switch name {
			case "default_credentials":
				cfg.Rules[0].CredentialHeaders = http.Header{"X-Session": {"private"}}
			case "unknown_mode":
				cfg.HeaderMode = "unknown"
			case "ordinary_auth":
				cfg.Rules[0].Headers = http.Header{"authorization": {"private"}}
			case "case_duplicate":
				cfg.Rules[0].Headers = http.Header{"Accept": {"x"}, "accept": {"x"}}
			case "class_overlap":
				cfg.HeaderMode = HeaderProtectedSessionProposal
				cfg.Rules[0].Headers = http.Header{"X-Session": {"private"}}
				cfg.Rules[0].CredentialHeaders = http.Header{"x-session": {"private"}}
			default:
				cfg.HeaderMode = HeaderProtectedSessionProposal
				field := map[string]string{"credential_encoding": "Accept-Encoding", "credential_upgrade": "Upgrade", "credential_framing": "Content-Length", "credential_hop": "Connection"}[name]
				cfg.Rules[0].CredentialHeaders = http.Header{field: {"private"}}
			}
			if e, err := NewExecution(t.Context(), cfg); err == nil || e != nil {
				t.Fatal("ambiguous or unsupported configuration accepted")
			}
		})
	}
}

func TestCallerCredentialPresenceIsUnsupportedBeforeReservation(t *testing.T) {
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer target.Close()
	for _, tc := range []struct {
		name   string
		values []string
	}{
		{"Authorization", []string{"caller"}}, {"aUtHoRiZaTiOn", []string{""}}, {"Cookie", nil}, {"cOoKiE", []string{"caller"}}, {"x-session", []string{"bound-custom"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := coreConfig(t, target.URL, &coreRecorder{}, 2)
			cfg.HeaderMode = HeaderProtectedSessionProposal
			cfg.Rules[0].CredentialHeaders = http.Header{"X-Session": {"bound-custom"}}
			e, call := coreOpen(t, cfg)
			req := coreRequest(t, "GET", target.URL+"/allowed")
			req.Header[tc.name] = tc.values
			response, receipt, err := call.Do(t.Context(), req)
			remaining, budgetErr := e.BudgetRemaining()
			if err == nil || response != nil || receipt.Decision != "unsupported" || receipt.Reason != Unsupported || !receipt.NotSent || receipt.ForwardAttempted || remaining != 2 || budgetErr != nil || received.Load() != 0 {
				t.Fatal("caller credential was removed, overwritten or sent")
			}
			if e.Snapshot().Counts.Unsupported != 1 {
				t.Fatal("credential refusal was not counted")
			}
		})
	}
}

func TestOrdinaryHeadersAreConstraintsAndPublicResponseIsUnchanged(t *testing.T) {
	const body = "application/json identity public"
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		if r.Header.Get("Accept") != "application/json" || r.Header.Get("Accept-Encoding") != "identity" || r.Header.Get("X-Public") != "public" || headerPresent(r.Header, "User-Agent") {
			t.Error("ordinary request values or absent UA changed")
		}
		w.Header().Set("X-Public-Result", body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}))
	defer target.Close()
	cfg := coreConfig(t, target.URL, &coreRecorder{}, 3)
	cfg.Rules[0].Headers = http.Header{"Accept": {"application/json"}, "Accept-Encoding": {"identity"}, "X-Public": {"public"}}
	e, call := coreOpen(t, cfg)
	cfg.Rules[0].Headers["Accept"][0] = "changed-after-construction"
	for _, name := range []string{"missing", "different", "hop_removed", "matches"} {
		t.Run(name, func(t *testing.T) {
			req := coreRequest(t, "GET", target.URL+"/allowed")
			req.Header.Set("Accept", "application/json")
			req.Header.Set("Accept-Encoding", "identity")
			req.Header.Set("X-Public", "public")
			switch name {
			case "missing":
				req.Header.Del("Accept")
			case "different":
				req.Header.Set("Accept", "text/plain")
			case "hop_removed":
				req.Header.Set("Connection", "Accept")
			}
			response, receipt, err := call.Do(t.Context(), req)
			if name != "matches" {
				if err == nil || response != nil || receipt.Decision != "unsupported" || !receipt.NotSent || received.Load() != 0 {
					t.Fatal("ordinary value was injected or overwritten")
				}
				return
			}
			if err != nil || response == nil || receipt.Reason != Complete {
				t.Fatal("normal preserve-mode delivery failed")
			}
			got, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if string(got) != body || response.Header.Get("X-Public-Result") != body || !bytes.Equal(e.RedactOutput([]byte(body)), []byte(body)) {
				t.Fatal("ordinary public data was classified as a credential or edited")
			}
		})
	}
	if received.Load() != 1 {
		t.Fatal("ordinary constraints did not gate actual sends")
	}
}

func TestProtectedSessionProposalHasSafePositiveAndPrivateEchoUnknown(t *testing.T) {
	const secret = "explicit-host-session-value"
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+secret || r.Header.Get("X-Session") != "custom-private" {
			t.Error("explicit protected binding was not applied")
		}
		w.Header().Set("X-Public", "identity")
		if strings.HasSuffix(r.URL.Path, "/echo") {
			io.WriteString(w, secret)
			return
		}
		io.WriteString(w, "public identity response")
	}))
	defer target.Close()
	cfg := coreConfig(t, target.URL, &coreRecorder{}, 3)
	cfg.HeaderMode = HeaderProtectedSessionProposal
	cfg.Rules[0].CredentialHeaders = http.Header{"Authorization": {"Bearer " + secret}, "X-Session": {"custom-private"}}
	e, call := coreOpen(t, cfg)
	// Constructor ownership includes both classified maps and their value slices.
	cfg.Rules[0].CredentialHeaders["Authorization"][0] = "mutated"
	response, receipt, err := call.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed"))
	if err != nil || response == nil || receipt.Reason != Complete || e.Snapshot().HeaderMode != HeaderProtectedSessionProposal || e.Policy().HeaderMode != HeaderProtectedSessionProposal {
		t.Fatal("safe proposal positive failed")
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != "public identity response" || response.Header.Get("X-Public") != "identity" {
		t.Fatal("safe proposal response changed")
	}
	response, receipt, err = call.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed/echo"))
	if err == nil || response != nil || receipt.Decision != "allowed" || receipt.Reason != Withheld || receipt.ResponseComplete || receipt.NotSent || !receipt.ForwardAttempted || !receipt.ResponseReceived || received.Load() != 2 {
		t.Fatal("private echo was edited into a successful response")
	}
}

func TestCredentialBearingResponseRepresentationIsWithheld(t *testing.T) {
	const secret = "configured-private-marker"
	for _, name := range []string{"set_cookie", "challenge", "authentication_info", "custom_new_value", "known_body", "known_header_value", "known_header_name", "known_status", "known_trailer"} {
		t.Run(name, func(t *testing.T) {
			var received atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received.Add(1)
				switch name {
				case "set_cookie":
					w.Header().Set("Set-Cookie", "new-session=not-in-config")
				case "challenge":
					w.Header().Set("WWW-Authenticate", "Bearer realm=public")
				case "authentication_info":
					w.Header().Set("Authentication-Info", "nextnonce=not-in-config")
				case "custom_new_value":
					w.Header().Set("X-Session", "new-value-not-in-config")
				case "known_header_value":
					w.Header().Set("X-Observed", secret)
				case "known_header_name":
					w.Header().Set(secret, "public")
				case "known_status":
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.Close()
					_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 %s\r\nContent-Length: 6\r\n\r\npublic", secret)
					return
				case "known_trailer":
					w.Header().Set("Trailer", "X-Observed")
				}
				if name == "known_body" {
					io.WriteString(w, secret)
				} else {
					io.WriteString(w, "public")
				}
				if name == "known_trailer" {
					w.Header().Set("X-Observed", secret)
				}
			}))
			defer target.Close()
			cfg := coreConfig(t, target.URL, &coreRecorder{}, 2)
			if strings.HasPrefix(name, "known_") || name == "custom_new_value" {
				cfg.HeaderMode = HeaderProtectedSessionProposal
				cfg.Rules[0].CredentialHeaders = http.Header{"X-Session": {secret}}
			}
			_, call := coreOpen(t, cfg)
			response, receipt, err := call.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed"))
			if err == nil || response != nil || receipt.Decision != "allowed" || receipt.Reason != Withheld || receipt.ResponseComplete || receipt.NotSent || !receipt.ForwardAttempted || !receipt.ResponseReceived || !receipt.AuditComplete || received.Load() != 1 {
				t.Fatal("unsafe response was exposed, edited into complete, or called not sent")
			}
		})
	}
}

func TestSafeHeadAndNoBodyResponseMetadataIsPreserved(t *testing.T) {
	for _, tc := range []struct {
		method string
		status int
	}{{"HEAD", 200}, {"GET", 204}, {"GET", 304}} {
		t.Run(fmt.Sprint(tc.method, tc.status), func(t *testing.T) {
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "57")
				w.Header().Set("X-Public", "unchanged")
				w.WriteHeader(tc.status)
			}))
			defer target.Close()
			cfg := coreConfig(t, target.URL, &coreRecorder{}, 2)
			cfg.Rules[0].Methods = []string{tc.method}
			_, call := coreOpen(t, cfg)
			response, receipt, err := call.Do(t.Context(), coreRequest(t, tc.method, target.URL+"/allowed"))
			if err != nil || response == nil || receipt.Reason != Complete {
				t.Fatal("safe no-body response failed")
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if len(body) != 0 || response.StatusCode != tc.status || response.Header.Get("X-Public") != "unchanged" || response.Request == nil || response.Request.Method != tc.method || len(response.Request.Header) != 0 {
				t.Fatal("no-body metadata or safe method context changed")
			}
			if tc.method == "HEAD" && (response.ContentLength != 57 || response.Header.Get("Content-Length") != "57") {
				t.Fatal("HEAD length was replaced by received body length")
			}
		})
	}
}

func TestClassifiedCredentialsRemainPrivateInJSONAndFormatting(t *testing.T) {
	const secret = "explicit-custom-private"
	cfg := coreConfig(t, "http://target.invalid", &coreRecorder{}, 2)
	cfg.HeaderMode = HeaderProtectedSessionProposal
	cfg.Rules[0].Headers = http.Header{"Accept": {"application/json"}}
	cfg.Rules[0].CredentialHeaders = http.Header{"X-Session": {secret}}
	for _, value := range []any{cfg, &cfg, cfg.Rules[0], &cfg.Rules[0]} {
		data, err := json.Marshal(value)
		if err != nil || bytes.Contains(data, []byte(secret)) || strings.Contains(fmt.Sprintf("%v %+v %#v", value, value, value), secret) {
			t.Fatal("classified credential escaped configuration serialization")
		}
	}
	_, err := NewExecution(context.Background(), cfg)
	if err != nil {
		t.Fatal("explicit proposal setup failed")
	}
}

func TestCredentialWireWhitespaceIsRejectedAtConstruction(t *testing.T) {
	for _, value := range []string{"private ", " private", "\tprivate", "private\t"} {
		cfg := coreConfig(t, "http://target.invalid", &coreRecorder{}, 2)
		cfg.HeaderMode = HeaderProtectedSessionProposal
		cfg.Rules[0].CredentialHeaders = http.Header{"X-Session": {value}}
		if execution, err := NewExecution(t.Context(), cfg); err == nil || execution != nil {
			t.Fatal("credential would be normalized after private-value registration")
		}
	}
}

func TestQuotedCredentialCookiePositiveAndEffectiveValueEchoUnknown(t *testing.T) {
	const cookie = `sid="quoted-private-cookie"`
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		parsed, err := r.Cookie("sid")
		if err != nil || parsed.Value != "quoted-private-cookie" || r.Header.Get("Cookie") != cookie {
			t.Error("protected cookie binding was rewritten")
			return
		}
		if strings.HasSuffix(r.URL.Path, "/echo") {
			io.WriteString(w, parsed.Value)
			return
		}
		io.WriteString(w, "normal response")
	}))
	defer target.Close()
	cfg := coreConfig(t, target.URL, &coreRecorder{}, 3)
	cfg.HeaderMode = HeaderProtectedSessionProposal
	cfg.Rules[0].CredentialHeaders = http.Header{"Cookie": {cookie}}
	_, call := coreOpen(t, cfg)
	response, receipt, err := call.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed"))
	if err != nil || response == nil || receipt.Reason != Complete {
		t.Fatal("normal protected-cookie positive failed")
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != "normal response" {
		t.Fatal("normal response changed")
	}
	response, receipt, err = call.Do(t.Context(), coreRequest(t, "GET", target.URL+"/allowed/echo"))
	if err == nil || response != nil || receipt.Decision != "allowed" || receipt.Reason != Withheld || receipt.ResponseComplete || receipt.NotSent || !receipt.ResponseReceived || received.Load() != 2 {
		t.Fatal("effective cookie value was disclosed or called not sent")
	}
}
