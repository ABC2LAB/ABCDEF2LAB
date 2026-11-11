package e2proxy

import (
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func evaluationCallerConfig(t testing.TB, origin string, rec Recorder, max uint64, backend string) Config {
	t.Helper()
	cfg := coreConfig(t, origin, rec, max)
	cfg.HeaderMode = HeaderEvaluationCallerPassthrough
	cfg.Evaluation = &EvaluationCallerContext{
		RunRef: cfg.Scope.RunRef, LLMBackend: backend, TargetKind: EvaluationPublicPractice,
	}
	return cfg
}

func TestEvaluationCallerConfiguration(t *testing.T) {
	for _, name := range []string{"missing", "wrong-run", "empty-run", "unknown-backend", "missing-backend", "non-practice", "preserve-mixed", "protected-mixed", "injected-auth", "injected-custom"} {
		t.Run(name, func(t *testing.T) {
			cfg := evaluationCallerConfig(t, "http://target.invalid", &coreRecorder{}, 2, "local")
			switch name {
			case "missing":
				cfg.Evaluation = nil
			case "wrong-run":
				cfg.Evaluation.RunRef = "another-run"
			case "empty-run":
				cfg.Evaluation.RunRef = ""
			case "unknown-backend":
				cfg.Evaluation.LLMBackend = "other"
			case "missing-backend":
				cfg.Evaluation.LLMBackend = ""
			case "non-practice":
				cfg.Evaluation.TargetKind = "organization"
			case "preserve-mixed":
				cfg.HeaderMode = HeaderPreserve
			case "protected-mixed":
				cfg.HeaderMode = HeaderProtectedSessionProposal
			case "injected-auth":
				cfg.Rules[0].CredentialHeaders = http.Header{"Authorization": {"host-secret"}}
			case "injected-custom":
				cfg.Rules[0].CredentialHeaders = http.Header{"X-Session": {"host-secret"}}
			}
			if e, err := NewExecution(t.Context(), cfg); !errors.Is(err, ErrConfig) || e != nil {
				t.Fatal("incomplete, mixed or injected evaluation configuration accepted")
			}
		})
	}
	for _, backend := range []string{"local", "api"} {
		t.Run(backend, func(t *testing.T) {
			cfg := evaluationCallerConfig(t, "http://target.invalid", &coreRecorder{}, 2, backend)
			expected := *cfg.Evaluation
			e, _ := coreOpen(t, cfg)
			cfg.Evaluation.RunRef, cfg.Evaluation.LLMBackend = "mutated", "other"
			for _, value := range []*EvaluationCallerContext{e.Policy().Evaluation, e.Snapshot().Evaluation} {
				if value == nil || *value != expected {
					t.Fatal("immutable evaluation declaration lost")
				}
				value.TargetKind = "mutated-view"
			}
			if *e.Policy().Evaluation != expected || *e.Snapshot().Evaluation != expected ||
				e.Policy().HeaderMode != HeaderEvaluationCallerPassthrough ||
				e.Snapshot().HeaderMode != HeaderEvaluationCallerPassthrough {
				t.Fatal("policy/snapshot context was mutable or mode was hidden")
			}
		})
	}
}

// Real proxy HTTP and inspected HTTPS, with synthetic target-issued credentials.
// No Bash, preserved C, external model or actual evaluation deployment is used.
func TestEvaluationCallerPassThroughOverProxy(t *testing.T) {
	const auth = "synthetic-eval-target-bearer-784b"
	const cookie = "synthetic-eval-target-cookie-26d1"
	for _, backend := range []string{"local", "api"} {
		for _, scheme := range []string{"http", "https"} {
			t.Run(backend+"/"+scheme, func(t *testing.T) {
				var received atomic.Int64
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					n := received.Add(1)
					if n == 1 {
						if headerPresent(r.Header, "Authorization") || headerPresent(r.Header, "Cookie") {
							t.Error("anonymous synthetic login had credentials injected")
						}
						_, _ = io.WriteString(w, auth)
						return
					}
					if !reflect.DeepEqual(r.Header.Values("Authorization"), []string{"Bearer " + auth}) ||
						!reflect.DeepEqual(r.Header.Values("Cookie"), []string{"token=" + cookie}) {
						t.Error("caller credential values were changed, removed or injected")
					}
					_, _ = io.WriteString(w, "authenticated fixture response")
				})
				var target *httptest.Server
				if scheme == "https" {
					target = httptest.NewTLSServer(handler)
				} else {
					target = httptest.NewServer(handler)
				}
				defer target.Close()
				rec := &coreRecorder{}
				cfg := evaluationCallerConfig(t, target.URL, rec, 2, backend)
				if scheme == "https" {
					cfg.RootCAs = x509.NewCertPool()
					cfg.RootCAs.AddCert(target.Certificate())
				}
				e, call := coreOpen(t, cfg)
				_, addr, roots := proxyTestListener(t, call)
				client := proxyTestClient(t, addr, roots)
				response, err := client.Get(target.URL + "/allowed/login")
				if err != nil {
					t.Fatal("synthetic login request failed")
				}
				body, readErr := io.ReadAll(response.Body)
				response.Body.Close()
				if readErr != nil || response.StatusCode != 200 || string(body) != auth ||
					string(e.RedactOutput(body)) != auth {
					t.Fatal("target-issued synthetic login body was altered")
				}
				req := coreRequest(t, "GET", target.URL+"/allowed/private")
				req.Header.Set("Authorization", "Bearer "+string(body))
				req.Header.Set("Cookie", "token="+cookie)
				response, err = client.Do(req)
				if err != nil {
					t.Fatal("evaluation credential request failed")
				}
				body, readErr = io.ReadAll(response.Body)
				response.Body.Close()
				if readErr != nil || response.StatusCode != 200 || string(body) != "authenticated fixture response" ||
					received.Load() != 2 || e.Snapshot().Counts.Responses != 2 {
					t.Fatal("authenticated proxy result/counts changed")
				}
				response, err = client.Do(coreRequest(t, "GET", target.URL+"/allowed"))
				if err != nil {
					t.Fatal("budget refusal response unavailable")
				}
				response.Body.Close()
				remaining, budgetErr := e.BudgetRemaining()
				if response.StatusCode != 502 || received.Load() != 2 || remaining != 0 || budgetErr != nil {
					t.Fatal("caller credentials widened the cumulative budget")
				}
				rec.mu.Lock()
				events := append([]Event(nil), rec.events...)
				rec.mu.Unlock()
				artifact, err := json.Marshal(struct {
					Events []Event
					Policy PolicyView
					View   ExecutionView
					Call   CallResult
				}{events, e.Policy(), e.Snapshot(), call.Result()})
				if err != nil || strings.Contains(string(artifact), auth) || strings.Contains(string(artifact), cookie) {
					t.Fatal("synthetic credential appeared in trusted result/audit projection")
				}
			})
		}
	}
}

func TestEvaluationCallerDoesNotWidenRequestScope(t *testing.T) {
	// 압축 협상은 거부하지 않고 identity로 바꿔 보내므로(C15) 이 목록에 없다. 자격과 함께 보낸 경우는
	// TestCoreCompressedNegotiationIsSentAsIdentity의 평가 자격 모드 경우가 본다.
	for _, mode := range []string{"method", "path", "origin", "hop-auth", "hop-cookie", "nil-cookie", "empty-auth", "duplicate-case", "ordinary-constraint"} {
		t.Run(mode, func(t *testing.T) {
			var received atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
			defer target.Close()
			cfg := evaluationCallerConfig(t, target.URL, &coreRecorder{}, 2, "api")
			if mode == "ordinary-constraint" {
				cfg.Rules[0].Headers = http.Header{"X-Public": {"required"}}
			}
			e, call := coreOpen(t, cfg)
			req := coreRequest(t, "GET", target.URL+"/allowed")
			req.Header.Set("Authorization", "Bearer synthetic-caller")
			req.Header.Set("Cookie", "token=synthetic-caller")
			switch mode {
			case "method":
				req.Method = "POST"
			case "path":
				req.URL.Path = "/else"
			case "origin":
				req.URL.Host, req.Host = "other.invalid", "other.invalid"
			case "hop-auth":
				req.Header.Set("Connection", "Authorization")
			case "hop-cookie":
				req.Header.Set("Connection", "keep-alive, Cookie")
			case "nil-cookie":
				req.Header["Cookie"] = nil
			case "empty-auth":
				req.Header["Authorization"] = []string{""}
			case "duplicate-case":
				req.Header["authorization"] = []string{"second"}
			}
			resp, receipt, err := call.Do(t.Context(), req)
			remaining, budgetErr := e.BudgetRemaining()
			wantReason, wantDecision := Unsupported, "unsupported"
			if mode == "method" || mode == "path" || mode == "origin" {
				wantReason, wantDecision = PolicyDenied, "denied"
			}
			if !errors.Is(err, ErrDenied) || resp != nil || receipt.Reason != wantReason || receipt.Decision != wantDecision || !receipt.NotSent || receipt.ForwardAttempted ||
				received.Load() != 0 || remaining != 2 || budgetErr != nil {
				t.Fatal("evaluation credentials bypassed policy/header rejection or consumed a grant")
			}
		})
	}
}

func TestEvaluationCallerHopCredentialRejectedOverProxy(t *testing.T) {
	for _, field := range []string{"Authorization", "Cookie"} {
		t.Run(field, func(t *testing.T) {
			var hits atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
			defer target.Close()
			e, call := coreOpen(t, evaluationCallerConfig(t, target.URL, &coreRecorder{}, 2, "local"))
			_, addr, _ := proxyTestListener(t, call)
			u, _ := url.Parse(target.URL)
			response := proxyTransferRaw(t, addr, "GET", target.URL+"/allowed", u.Host, "Connection: "+field+"\r\n"+field+": synthetic-caller"+"\r\n")
			response.Body.Close()
			remaining, err := e.BudgetRemaining()
			if response.StatusCode != 502 || hits.Load() != 0 || e.Snapshot().Counts.Unsupported != 1 || remaining != 2 || err != nil {
				t.Fatal("proxy silently stripped caller credential and sent anonymously")
			}
		})
	}
}

func TestEvaluationCallerKeepsResponseWithholdingAndStop(t *testing.T) {
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Set-Cookie", "synthetic-response-token")
		_, _ = io.WriteString(w, "whole response must stay withheld")
	}))
	defer target.Close()
	cfg := evaluationCallerConfig(t, target.URL, &coreRecorder{}, 3, "api")
	cfg.Limits.MaxUnknown = 1
	e, call := coreOpen(t, cfg)
	req := coreRequest(t, "GET", target.URL+"/allowed")
	req.Header.Set("Authorization", "Bearer synthetic-caller")
	response, receipt, err := call.Do(t.Context(), req)
	if err == nil || response != nil || receipt.Reason != Unknown || !receipt.ResponseComplete ||
		!receipt.AuditComplete || hits.Load() != 1 || e.Snapshot().Counts.Unknown != 1 {
		t.Fatal("evaluation mode exposed or reclassified a credential-bearing response")
	}
	response, receipt, err = call.Do(t.Context(), req)
	if !errors.Is(err, ErrDenied) || response != nil || receipt.Reason != UnknownLimit || !receipt.NotSent || hits.Load() != 1 || e.Snapshot().Reason != UnknownLimit {
		t.Fatal("withholding did not retain the declared unknown stop limit")
	}
}

func TestEvaluationCallerStillRequiresAuditACK(t *testing.T) {
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer target.Close()
	rec := &coreRecorder{failStage: "intent"}
	e, call := coreOpen(t, evaluationCallerConfig(t, target.URL, rec, 2, "local"))
	req := coreRequest(t, "GET", target.URL+"/allowed")
	req.Header.Set("Authorization", "Bearer synthetic-caller")
	response, receipt, err := call.Do(t.Context(), req)
	if !errors.Is(err, ErrAudit) || response != nil || receipt.ForwardAttempted || hits.Load() != 0 ||
		e.Snapshot().AuditComplete || e.Snapshot().Reason != AuditFailed {
		t.Fatal("evaluation credentials bypassed audit ACK failure")
	}
}
