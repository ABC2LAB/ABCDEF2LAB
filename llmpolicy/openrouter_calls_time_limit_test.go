package llmpolicy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// callEndDeadline은 기한만 알려 주고 아직 끝났다고 알리지 않는 문맥이다(호출 시간 상한 타이머가 문맥보다 먼저 요청을 끊은
// 순간이 기한 지남이다).
type callEndDeadline struct {
	context.Context
	at time.Time
}

func (c callEndDeadline) Deadline() (time.Time, bool) { return c.at, true }

func TestOpenRouterCallerStopped(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, tc := range map[string]struct {
		ctx  context.Context
		want bool
	}{
		"no context":      {nil, false},
		"no deadline":     {context.Background(), false},
		"cancelled":       {cancelled, true},
		"deadline passed": {callEndDeadline{context.Background(), time.Now().Add(-time.Millisecond)}, true},
		"deadline ahead":  {callEndDeadline{context.Background(), time.Now().Add(time.Hour)}, false},
	} {
		if got := openRouterCallerStopped(tc.ctx); got != tc.want {
			t.Fatalf("%s: %v", name, got)
		}
	}
}

// 문맥 기한이 지난 뒤 끝까지 받지 못한 호출은, 문맥이 아직 끝났다고 알리지 않았어도 이쪽이 멈춘 것으로 남는다(응답 머리를
// 기다리다 끊김·본문을 받다 끊김 모두). 기한이 남았으면 지금처럼 위쪽 끊김이다.
func TestOpenRouterCallRecordEndAtDeadline(t *testing.T) {
	passed := func() context.Context {
		return callEndDeadline{context.Background(), time.Now().Add(-time.Millisecond)}
	}
	ahead := func() context.Context {
		return callEndDeadline{context.Background(), time.Now().Add(time.Hour)}
	}
	headers := func(*http.Request) (*http.Response, error) { return nil, errors.New("net/http: request canceled") }
	body := func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(&cutReader{strings.NewReader("  \n")}), Header: make(http.Header)}, nil
	}
	for _, tc := range []struct {
		name    string
		ctx     func() context.Context
		respond func(*http.Request) (*http.Response, error)
		end     string
		status  int
	}{
		{"awaiting headers, deadline passed", passed, headers, OpenRouterEndClientClosedBeforeDone, 0},
		{"reading body, deadline passed", passed, body, OpenRouterEndClientClosedBeforeDone, 200},
		{"awaiting headers, deadline ahead", ahead, headers, OpenRouterEndUpstreamCutBeforeDone, 0},
		{"reading body, deadline ahead", ahead, body, OpenRouterEndUpstreamCutBeforeDone, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, records := recordingFixture(t)
			tr := &openRouterTransport{policy: p, base: openRouterRT(tc.respond)}
			if resp, err := tr.RoundTrip(openRouterRequest().WithContext(tc.ctx())); err == nil {
				_ = resp.Body.Close()
				t.Fatal("끊긴 호출이 성공으로 보임")
			}
			got := records()
			if len(got) != 1 || got[0].Decision != OpenRouterCallUpstreamError || got[0].UnfinishedEnd != tc.end || got[0].Status != tc.status {
				t.Fatalf("records %+v", got)
			}
		})
	}
}

// 호출 시간 상한(timeout_seconds)에 끊긴 호출의 끝 모양은 늘 '이쪽이 멈춤'이다. 실제 http.Client·http.Transport로 본다
// (상한 타이머는 표준 라이브러리 것이고, 고치기 전에는 같은 끊김이 호출마다 두 값으로 갈렸다).
func TestOpenRouterCallTimeLimitEndsAsClientClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header bool
		status int
	}{{"awaiting headers", false, 0}, {"reading body", true, 200}} {
		t.Run(tc.name, func(t *testing.T) {
			release := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.header {
					w.WriteHeader(http.StatusOK)
					_, _ = io.WriteString(w, "  \n")
					w.(http.Flusher).Flush()
				}
				select {
				case <-release:
				case <-r.Context().Done():
				}
			}))
			defer srv.Close()
			defer close(release)
			p, records := recordingFixture(t)
			p.manifest.TimeoutSeconds = 1
			limit := time.Duration(p.manifest.TimeoutSeconds) * time.Second
			// 제품 전송과 같은 설정(응답 머리 상한 = 호출 시간 상한)에 대상만 시험 서버로 바꾼다.
			base := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: limit,
				DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "tcp", srv.Listener.Addr().String())
				}}
			req := openRouterRequest().WithContext(t.Context())
			req.Header.Set("Authorization", "Bearer synthetic")
			if resp, err := openRouterClient(p, base).Do(req); err == nil {
				_ = resp.Body.Close()
				t.Fatal("상한에 끊긴 호출이 성공으로 보임")
			}
			got := records()
			if len(got) != 1 || got[0].Decision != OpenRouterCallUpstreamError || got[0].UnfinishedEnd != OpenRouterEndClientClosedBeforeDone ||
				got[0].Status != tc.status || got[0].GenerationID != "" {
				t.Fatalf("records %+v", got)
			}
		})
	}
}
