package e2proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
)

func TestProxyOutcomeOfReceipt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		r      Receipt
		err    error
		class  ProxyOutcomeClass
		reason Reason
	}{
		{"policy denial", Receipt{Decision: "denied", Reason: PolicyDenied, NotSent: true}, ErrDenied, OutcomeNotSent, PolicyDenied},
		{"unsupported", Receipt{Decision: "unsupported", Reason: Unsupported, NotSent: true}, ErrDenied, OutcomeNotSent, Unsupported},
		{"budget", Receipt{Decision: "denied", Reason: BudgetExhausted, NotSent: true}, ErrDenied, OutcomeNotSent, BudgetExhausted},
		{"connection never opened", Receipt{Decision: "allowed", Reason: TransportFailed, NotSent: true}, ErrDenied, OutcomeNotSent, TransportFailed},
		// 연결을 시도했으면 사유가 무엇이든 대상이 받았을 수 있다.
		{"forwarded without response", Receipt{Decision: "allowed", Reason: Unknown, ForwardAttempted: true}, ErrDenied, OutcomeUnknown, Unknown},
		{"forwarded then audit failed", Receipt{Decision: "allowed", Reason: Complete, ForwardAttempted: true}, ErrAudit, OutcomeUnknown, AuditFailed},
		{"audit failed before send", Receipt{Decision: "denied", Reason: AuditFailed, NotSent: true}, ErrAudit, OutcomeNotSent, AuditFailed},
		// 영수증을 만들기 전에 막혔다(실행 종료·시도 상한).
		{"closed before receipt", Receipt{}, ErrClosed, OutcomeNotSent, Closed},
		{"config error before receipt", Receipt{}, ErrConfig, OutcomeNotSent, Invalid},
		{"no response without error", Receipt{Reason: Complete}, nil, OutcomeNotSent, Invalid},
		{"unlisted reason", Receipt{Reason: Reason("rule_proxy")}, ErrDenied, OutcomeNotSent, Invalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := proxyOutcomeOf(tc.r, tc.err)
			if got.class != tc.class || got.reason != tc.reason {
				t.Fatalf("got %s/%s want %s/%s", got.class, got.reason, tc.class, tc.reason)
			}
		})
	}
}

// 사유를 새로 만들면 모델에 돌려줄 설명도 함께 정해야 한다. 설명이 없으면 그 사유는 invalid로 뭉개진다.
func TestProxyOutcomeHintsCoverEveryReason(t *testing.T) {
	source, err := os.ReadFile("types.go")
	if err != nil {
		t.Fatal(err)
	}
	declared := regexp.MustCompile(`(?m)^\s+\w+\s+Reason = "([a-z_]+)"`).FindAllSubmatch(source, -1)
	if len(declared) < len(proxyOutcomeHints) {
		t.Fatalf("reason declarations not found: %d", len(declared))
	}
	ascii := regexp.MustCompile(`^[\x20-\x7e]+$`)
	for _, match := range declared {
		reason := Reason(match[1])
		if reason == Complete {
			if _, ok := proxyOutcomeHints[reason]; ok {
				t.Fatal("complete is not a failure outcome")
			}
			continue
		}
		hint, ok := proxyOutcomeHints[reason]
		if !ok {
			t.Errorf("no model hint for reason %s", reason)
			continue
		}
		// 모델이 읽는 글이라 영어 한 줄이다.
		for _, class := range []ProxyOutcomeClass{OutcomeNotSent, OutcomeUnknown} {
			body := proxyOutcome{class: class, reason: reason}.body()
			if !ascii.MatchString(hint) || !strings.HasSuffix(body, "\n") || strings.Count(body, "\n") != 1 ||
				!strings.Contains(body, "reason="+string(reason)+".") || !strings.Contains(body, hint) {
				t.Errorf("%s/%s: %q", class, reason, body)
			}
		}
	}
}

func TestProxyOutcomeBodySeparatesNotSentFromUnknown(t *testing.T) {
	notSent := proxyOutcome{class: OutcomeNotSent, reason: PolicyDenied}.body()
	unknown := proxyOutcome{class: OutcomeUnknown, reason: Unknown}.body()
	if !strings.HasPrefix(notSent, "E2 did not send this request") || strings.Contains(notSent, "unknown") {
		t.Fatal(notSent)
	}
	if !strings.HasPrefix(unknown, "E2 forwarded this request") || !strings.Contains(unknown, "Check the resulting state") {
		t.Fatal(unknown)
	}
}

func proxyOutcomeResponse(t *testing.T, resp *http.Response) (ProxyOutcomeClass, Reason, string) {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return ProxyOutcomeClass(resp.Header.Get(OutcomeHeader)), Reason(resp.Header.Get(ReasonHeader)), string(body)
}

// 실제 프록시 경로에서 거부·미지원·결과 불명이 서로 다른 결과로 돌아가고, 통제 내부 값은 싣지 않는다.
func TestProxyFailureCarriesOutcomeAndReason(t *testing.T) {
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		io.WriteString(w, strings.Repeat("x", 9000)) // proxyTestCall의 MaxBodyBytes(8192)를 넘는다.
	}))
	defer target.Close()
	exec, call, _ := proxyTestCall(t, target.URL, nil)
	_, addr, roots := proxyTestListener(t, call)
	client := proxyTestClient(t, addr, roots)
	send := func(path, encoding string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, target.URL+path, nil)
		if encoding != "" {
			req.Header.Set("Accept-Encoding", encoding)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	var bodies []string
	for _, tc := range []struct {
		name, path, encoding string
		class                ProxyOutcomeClass
		reason               Reason
		reached              int64
	}{
		{"outside scope", "/outside", "", OutcomeNotSent, PolicyDenied, 0},
		{"compressed encoding", "/allowed", "gzip", OutcomeNotSent, Unsupported, 0},
		{"response over limit", "/allowed", "", OutcomeUnknown, Unknown, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := received.Load()
			resp := send(tc.path, tc.encoding)
			class, reason, body := proxyOutcomeResponse(t, resp)
			bodies = append(bodies, body)
			if resp.StatusCode != http.StatusBadGateway || class != tc.class || reason != tc.reason ||
				!strings.Contains(body, "reason="+string(tc.reason)+".") || received.Load()-before != tc.reached {
				t.Fatalf("%d %s %s %q", resp.StatusCode, class, reason, body)
			}
			for _, internal := range []string{"rule_proxy", "revision_proxy", "path_proxy", "session-secret", target.URL, "xxxx"} {
				if strings.Contains(body, internal) {
					t.Fatalf("outcome leaked %q: %q", internal, body)
				}
			}
		})
	}
	attempts := 0
	for _, ev := range exec.Events(0, 200) {
		if ev.AttemptID == "" {
			continue
		}
		attempts++
		for _, body := range bodies {
			if strings.Contains(body, ev.AttemptID) {
				t.Fatal("outcome leaked an attempt ID")
			}
		}
	}
	if attempts == 0 || len(bodies) != 3 {
		t.Fatal("no attempts to compare", attempts, len(bodies))
	}
}

// 잘못된 형식의 요청(400)과 CONNECT 거부(터널 전)도 사유를 싣는다.
func TestProxyEarlyRejectionsCarryOutcome(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer target.Close()
	_, call, _ := proxyTestCall(t, target.URL, nil)
	_, addr, _ := proxyTestListener(t, call)
	malformed := proxyTransferRaw(t, addr, http.MethodGet, target.URL+"/allowed", "other.invalid", "")
	class, reason, _ := proxyOutcomeResponse(t, malformed)
	if malformed.StatusCode != http.StatusBadRequest || class != OutcomeNotSent || reason != Unsupported {
		t.Fatalf("%d %s %s", malformed.StatusCode, class, reason)
	}
	connect := proxyTransferRaw(t, addr, http.MethodConnect, "other.invalid:443", "other.invalid:443", "")
	class, reason, _ = proxyOutcomeResponse(t, connect)
	if connect.StatusCode != http.StatusBadGateway || class != OutcomeNotSent || reason != PolicyDenied {
		t.Fatalf("%d %s %s", connect.StatusCode, class, reason)
	}
}

// 대상이 결과 헤더를 흉내 내도 도구에는 가지 않는다. 다른 대상 헤더는 그대로다.
func TestProxyTargetCannotForgeOutcomeHeaders(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header()["x-abc2lab-e2-outcome"] = []string{"not_sent"}
		w.Header().Set(ReasonHeader, string(PolicyDenied))
		w.Header().Set("X-E2-Public", "original")
		w.Header().Set("Trailer", OutcomeHeader)
		io.WriteString(w, "target body")
		w.Header().Set(OutcomeHeader, "not_sent")
	}))
	defer target.Close()
	_, call, _ := proxyTestCall(t, target.URL, nil)
	_, addr, roots := proxyTestListener(t, call)
	resp, err := proxyTestClient(t, addr, roots).Get(target.URL + "/allowed")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "target body" || resp.Header.Get("X-E2-Public") != "original" {
		t.Fatalf("%d %q %v", resp.StatusCode, body, resp.Header)
	}
	for _, fields := range []http.Header{resp.Header, resp.Trailer} {
		for key := range fields {
			if strings.EqualFold(key, OutcomeHeader) || strings.EqualFold(key, ReasonHeader) {
				t.Fatalf("target forged %s", key)
			}
		}
	}
}
