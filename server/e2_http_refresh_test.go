package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
)

// 실제 대기 없이 60초 세션 수명·재로그인 간격을 진행한다. HTTP handler와도 공유한다.
type e2RefreshClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *e2RefreshClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *e2RefreshClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// 간격은 시작 시각이 아니라 완료 시각부터 잰다. 실행 중 같은 계정은 거부하고 다른 계정은 허용한다.
func TestE2HTTPRefreshIntervalFromCompletion(t *testing.T) {
	clock := &e2RefreshClock{at: time.Now()}
	counter := &e2HTTPRefreshCounter{now: clock.Now}
	if result := counter.begin("a"); result != "" {
		t.Fatal(result)
	}
	clock.Advance(time.Minute)
	if result := counter.begin("a"); result != E2HTTPRefreshCooldown {
		t.Fatal("in-flight account admitted", result)
	}
	if result := counter.begin("b"); result != "" {
		t.Fatal("other account blocked", result)
	}
	counter.finish("b", true)
	counter.finish("a", false)
	clock.Advance(minSessionRefreshInterval - time.Nanosecond)
	if result := counter.begin("a"); result != E2HTTPRefreshCooldown {
		t.Fatal("interval shortened by login time", result)
	}
	clock.Advance(time.Nanosecond)
	if result := counter.begin("a"); result != "" {
		t.Fatal("exact boundary refused", result)
	}
	counter.finish("a", true)
}

// HTTP 성공이어도 세션을 얻지 못한 경우, 송신 전 거부, 송신 결과 불명도 연속 실패로 센다.
// 결과 불명을 자동 재전송하지 않으며 간격 뒤 별도 명령만 새 시도로 센다.
func TestE2HTTPRefreshUnusableSessionCountsAsFailure(t *testing.T) {
	for _, status := range []string{E2HTTPAuthSessionNotFound, E2HTTPAuthNotSent, E2HTTPAuthResultUnknown} {
		t.Run(status, func(t *testing.T) {
			var sends int
			var mu sync.Mutex
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				sends++
				mu.Unlock()
				if status == E2HTTPAuthResultUnknown {
					connection, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					connection.Close()
				}
			}))
			defer target.Close()
			dir := authTestDir(t)
			pin := authCredentialPin(t, dir, "account.json", "tester@owned.test", "seeded-pass-88ff")
			if status == E2HTTPAuthNotSent {
				pin.SHA256 = strings.Repeat("0", 64)
			}
			auth := cookieAuth(E2HTTPFileAuthAccount{Label: "a", Credential: pin})
			execution := authTestExecution(t, target.URL, &authEventCounter{}, e2proxy.HeaderTestAccountPassthrough)
			defer execution.Stop(context.Background())
			clock := &e2RefreshClock{at: time.Now()}
			counter := &e2HTTPRefreshCounter{now: clock.Now}
			// 모르는 계정과 이미 취소된 명령은 admission 전에 끝나 실패 상태를 만들지 않는다.
			canceled, cancel := context.WithCancel(t.Context())
			cancel()
			for _, ctx := range []context.Context{nil, canceled} {
				e2HTTPRefreshSession(ctx, execution, dir, target.URL, auth, counter, nil, "a", false)
			}
			e2HTTPRefreshSession(t.Context(), execution, dir, target.URL, auth, counter, nil, "unknown", false)
			if len(counter.accounts) != 0 {
				t.Fatal("unadmitted command changed account state")
			}
			for range maxSessionRefreshFailuresPerAccount {
				if _, result := e2HTTPRefreshSession(t.Context(), execution, dir, target.URL, auth, counter, nil, "a", false); result != status {
					t.Fatalf("result=%s want=%s", result, status)
				}
				clock.Advance(minSessionRefreshInterval)
			}
			calls := len(execution.Calls())
			if _, result := e2HTTPRefreshSession(t.Context(), execution, dir, target.URL, auth, counter, nil, "a", false); result != E2HTTPRefreshLimit || len(execution.Calls()) != calls {
				t.Fatal("consecutive failure limit did not refuse before BeginCall", result)
			}
			mu.Lock()
			defer mu.Unlock()
			want := maxSessionRefreshFailuresPerAccount
			if status == E2HTTPAuthNotSent {
				want = 0
			}
			if sends != want {
				t.Fatalf("HTTP requests=%d want=%d (no automatic retry)", sends, want)
			}
		})
	}
}
