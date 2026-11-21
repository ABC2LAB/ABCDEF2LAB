package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
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

// 시험 계정 세션 가림 칸 계산: 계정마다 승인 구간 ÷ 최소 재로그인 간격 + 1개 세션, 세션마다 값 셋(쿠키 방식은 쿠키마다),
// 확정 단계가 켜진 실행만 계정마다 다시 로그인 한 번 몫을 더한다. 시험 계정이 없는 실행(공식 측정 O 등)은 0이다.
func TestE2HTTPSessionRedactLimits(t *testing.T) {
	header := &E2HTTPFileAuth{Login: diagnosisprep.TargetLogin{Header: "Authorization", Prefix: "Bearer "},
		Accounts: []E2HTTPFileAuthAccount{{Label: "owner-a"}, {Label: "cross-b"}}}
	cookies := &E2HTTPFileAuth{Login: diagnosisprep.TargetLogin{Cookies: []string{"sid", "csrf"}}, Accounts: header.Accounts}
	many := &E2HTTPFileAuth{Login: header.Login}
	for i := 0; i < 16; i++ {
		many.Accounts = append(many.Accounts, E2HTTPFileAuthAccount{Label: fmt.Sprintf("acct-%02d", i)})
	}
	for _, tc := range []struct {
		name              string
		auth              *E2HTTPFileAuth
		window            time.Duration
		e3                uint64
		capacity, reserve int
	}{
		{name: "no accounts", auth: nil, window: 30 * time.Minute, e3: 8},
		{name: "header 1800s confirmation", auth: header, window: 1800 * time.Second, e3: 8, capacity: 2*3*181 + 6, reserve: 6},
		{name: "header 1800s no confirmation", auth: header, window: 1800 * time.Second, capacity: 2 * 3 * 181},
		{name: "two cookies", auth: cookies, window: 1800 * time.Second, e3: 8, capacity: 2*6*181 + 12, reserve: 12},
		{name: "clamped", auth: many, window: 3660 * time.Second, e3: 8, capacity: e2proxy.MaxSessionRedactValues, reserve: 48},
	} {
		capacity, reserve := e2HTTPSessionRedactLimits(tc.auth, tc.window, tc.e3)
		if capacity != tc.capacity || reserve != tc.reserve {
			t.Errorf("%s: 칸 %d·몫 %d, 기대 %d·%d", tc.name, capacity, reserve, tc.capacity, tc.reserve)
		}
	}
	// 토큰 60초 대상의 1800초 회차는 계정마다 세션 31개(값 93개)로 칸에 한참 못 미친다.
	if capacity, _ := e2HTTPSessionRedactLimits(header, 1800*time.Second, 8); 2*3*(1800/60+1)+6 > capacity {
		t.Fatal("현실적인 회차가 칸에 닿음")
	}
}
