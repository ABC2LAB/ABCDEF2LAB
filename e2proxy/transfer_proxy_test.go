package e2proxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func proxyTransferRaw(t *testing.T, addr, method, uri, host, headers string) *http.Response {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.WriteString(conn, method+" "+uri+" HTTP/1.1\r\nHost: "+host+"\r\n"+headers+"\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: method})
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestProxyCallerCredentialCannotDisappearBeforeGate(t *testing.T) {
	for _, field := range []string{"Authorization", "Cookie", "X-Session"} {
		t.Run(field, func(t *testing.T) {
			var received atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
			defer target.Close()
			cfg := coreConfig(t, target.URL, &coreRecorder{}, 2)
			if field == "X-Session" {
				cfg.HeaderMode = HeaderProtectedSessionProposal
				cfg.Rules[0].CredentialHeaders = http.Header{"X-Session": {"bound-private"}}
			}
			e, call := coreOpen(t, cfg)
			_, addr, _ := proxyTestListener(t, call)
			u, _ := url.Parse(target.URL)
			response := proxyTransferRaw(t, addr, "GET", target.URL+"/allowed", u.Host, "Connection: "+field+"\r\n"+field+": caller\r\n")
			response.Body.Close()
			remaining, err := e.BudgetRemaining()
			if response.StatusCode != 502 || received.Load() != 0 || e.Snapshot().Counts.Unsupported != 1 || remaining != 2 || err != nil {
				t.Fatal("hop removal bypassed caller-credential refusal")
			}
		})
	}
}

func TestProxyCredentialResponseIsUnknownWithoutEditedSuccess(t *testing.T) {
	for _, kind := range []string{"set_cookie", "challenge", "known_echo"} {
		t.Run(kind, func(t *testing.T) {
			var received atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received.Add(1)
				if kind == "set_cookie" {
					w.Header().Set("Set-Cookie", "new-token=not-in-config")
				}
				if kind == "challenge" {
					w.Header().Set("WWW-Authenticate", "Bearer realm=public")
				}
				if kind == "known_echo" {
					io.WriteString(w, r.Header.Get("X-Session"))
					return
				}
				io.WriteString(w, "untouched target response")
			}))
			defer target.Close()
			cfg := coreConfig(t, target.URL, &coreRecorder{}, 2)
			if kind == "known_echo" {
				cfg.HeaderMode = HeaderProtectedSessionProposal
				cfg.Rules[0].CredentialHeaders = http.Header{"X-Session": {"bound-private"}}
			}
			e, call := coreOpen(t, cfg)
			_, addr, roots := proxyTestListener(t, call)
			response, err := proxyTestClient(t, addr, roots).Get(target.URL + "/allowed")
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			counts := e.Snapshot().Counts
			if response.StatusCode != 502 || received.Load() != 1 || counts.Allowed != 1 || counts.Unknown != 1 || counts.Denied != 0 || strings.Contains(string(body), "bound-private") || strings.Contains(string(body), "[REDACTED]") || response.Header.Get("Set-Cookie") != "" || response.Header.Get("WWW-Authenticate") != "" {
				t.Fatal("credential-bearing response was exposed, edited into success, or called denied")
			}
		})
	}
}

func TestProxyRawHeadAndNotModifiedRetainLengthMetadata(t *testing.T) {
	for _, tc := range []struct {
		method string
		status int
		length string
	}{{"HEAD", 200, "57"}, {"GET", 304, "57"}, {"GET", 204, ""}} {
		t.Run(fmt.Sprint(tc.method, tc.status), func(t *testing.T) {
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.Close()
				length := ""
				if tc.length != "" {
					length = "Content-Length: " + tc.length + "\r\n"
				}
				_, _ = fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\n%sX-Public: identity application/json\r\nX-E2-Public: original\r\nConnection: close\r\n\r\n", tc.status, http.StatusText(tc.status), length)
			}))
			defer target.Close()
			cfg := coreConfig(t, target.URL, &coreRecorder{}, 2)
			cfg.Rules[0].Methods = []string{tc.method}
			_, call := coreOpen(t, cfg)
			_, addr, _ := proxyTestListener(t, call)
			u, _ := url.Parse(target.URL)
			response := proxyTransferRaw(t, addr, tc.method, target.URL+"/allowed", u.Host, "")
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != tc.status || len(body) != 0 || response.Header.Get("Content-Length") != tc.length || response.Header.Get("X-Public") != "identity application/json" || response.Header.Get("X-E2-Public") != "original" {
				t.Fatal("safe no-body wire response lost original metadata")
			}
		})
	}
}

func TestProxyRawUserAgentPresenceIsPreserved(t *testing.T) {
	observed := make(chan string, 1)
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		observed <- fmt.Sprint(headerPresent(r.Header, "User-Agent"), "|", r.Header.Get("User-Agent"))
		io.WriteString(w, "normal")
	}))
	defer target.Close()
	e, call := coreOpen(t, coreConfig(t, target.URL, &coreRecorder{}, 4))
	_, addr, _ := proxyTestListener(t, call)
	u, _ := url.Parse(target.URL)
	for _, tc := range []struct{ header, want string }{{"", "false|"}, {"User-Agent: caller-agent\r\n", "true|caller-agent"}} {
		response := proxyTransferRaw(t, addr, "GET", target.URL+"/allowed", u.Host, tc.header)
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode != 200 || <-observed != tc.want {
			t.Fatal("proxy manufactured or changed caller User-Agent")
		}
	}
	response := proxyTransferRaw(t, addr, "GET", target.URL+"/allowed", u.Host, "User-Agent:\r\n")
	response.Body.Close()
	if response.StatusCode != 502 || received.Load() != 2 || e.Snapshot().Counts.Unsupported != 1 {
		t.Fatal("unpreservable empty User-Agent was silently changed")
	}
}

func TestResponseHopFilterPreservesPublicControlLookingFields(t *testing.T) {
	headers := http.Header{"Connection": {"X-Hop"}, "X-Hop": {"remove"}, "Proxy-Connection": {"close"}, "X-E2-Public": {"untrusted original"}, "X-Public": {"identity"}}
	stripResponseHopHeaders(headers)
	if len(headers) != 2 || headers.Get("X-E2-Public") != "untrusted original" || headers.Get("X-Public") != "identity" {
		t.Fatal("ordinary response fields were treated as trusted control or removed")
	}
}
