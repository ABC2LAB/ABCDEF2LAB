package e2proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
)

// e3ShareFixture는 대상 예산 total 가운데 확정 몫 share를 떼어 둔 실행이다(신원 규칙·인증 준비 규칙 포함, 나머지는
// e3IdentityFixture와 같음).
func e3ShareFixture(t *testing.T, total, share uint64) (*Execution, E3IdentityInput, *httptest.Server, *atomic.Int64) {
	t.Helper()
	var received atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "synthetic-relogin-session"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"subject-a"}`)
	}))
	t.Cleanup(server.Close)
	_, in := e3IdentityFixture(t, server.URL, &coreRecorder{})
	cfg := coreConfig(t, server.URL, &coreRecorder{}, total)
	cfg.AuditVersion = JournalVersionV3
	cfg.E3Verifier = e3IdentityVerifier{}
	cfg.E3ConfirmationRequests = share
	cfg.Rules = append(cfg.Rules,
		Rule{Ref: "identity-rule", Purpose: PurposeE3Identity, Origin: server.URL, ConnectIP: netip.MustParseAddr("127.0.0.1"),
			PathPrefix: "/allowed/identity", PathScopeRef: "identity-path", Methods: []string{"GET"}},
		Rule{Ref: "auth-login", Purpose: PurposeAuthPrep, Origin: server.URL, ConnectIP: netip.MustParseAddr("127.0.0.1"),
			PathPrefix: "/login", PathScopeRef: "auth-login-path", Methods: []string{"POST"}})
	e, err := NewExecution(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	value := in.Marker.Reveal()
	if err := e.RegisterProgramMarker(value); err != nil {
		t.Fatal(err)
	}
	clear(value)
	return e, in, server, &received
}

// modelRequests는 모델 단계 Worker 요청 n건을 보내고 보낸 수와 마지막 거부 사유를 돌려준다.
func modelRequests(t *testing.T, e *Execution, target string, n int) (admitted int, last Reason) {
	t.Helper()
	c, err := e.BeginCall(t.Context(), "model")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = c.Close(t.Context()) }()
	for i := 0; i < n; i++ {
		resp, r, err := c.Do(t.Context(), coreRequest(t, "GET", target+"/allowed"))
		if err == nil {
			resp.Body.Close()
			admitted++
			continue
		}
		last = r.Reason
	}
	return admitted, last
}

// relogin은 다시 로그인 호출 하나를 열어 로그인 요청을 보낸다. e3이 참이면 확정 단계 전용 호출, 거짓이면 Worker
// refresh_session 호출이다.
func relogin(t *testing.T, e *Execution, target string, e3 bool) (sent bool, reason Reason) {
	t.Helper()
	open := func() (*Call, error) { return e.BeginProgramCall(t.Context(), ProgramRefreshSession, "auth-refresh") }
	if e3 {
		open = func() (*Call, error) { return e.BeginE3ReloginCall(t.Context(), "e3-relogin") }
	}
	c, err := open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = c.Close(t.Context()) }()
	_, r, err := c.AuthPrep(t.Context(), loginRequest(t, target+"/login"), cookieExtractor("session"))
	return err == nil, r.Reason
}

// 계약 §7.1: 확정 몫은 확정 단계 요청(E③ 프로그램 요청·확정 단계가 연 다시 로그인)만 쓴다. 모델 단계 요청은 확정 단계
// 전이든 뒤든(늦게 도착한 Worker 요청) 예산의 마지막 확정 몫만큼을 쓰지 못하고(budget_exhausted), 남은 예산 조회도 그만큼
// 뺀다. 확정 단계 요청이 몫을 쓸 때마다 떼어 둘 몫이 줄고, 몫을 다 쓰면 확정 단계 요청도 막힌다.
func TestE3ShareOnlyForConfirmationRequests(t *testing.T) {
	e, in, server, received := e3ShareFixture(t, 6, 3)
	if remaining, err := e.BudgetRemaining(); err != nil || remaining != 3 {
		t.Fatalf("처음 남은 예산 %d %v (예산 6 − 확정 몫 3)", remaining, err)
	}
	if admitted, last := modelRequests(t, e, server.URL, 5); admitted != 3 || last != BudgetExhausted {
		t.Fatalf("모델 단계가 확정 몫까지 씀: 보냄 %d 마지막 거부 %s", admitted, last)
	}
	if remaining, err := e.BudgetRemaining(); err != nil || remaining != 0 {
		t.Fatalf("확정 몫만 남았는데 남은 예산 %d %v (0이어야 모델 단계가 마감)", remaining, err)
	}
	// 확정 단계: 신원 요청은 몫을 쓴다.
	before := received.Load()
	if event, err := e.SendE3Identity(t.Context(), in); err != nil || event.NotSent || received.Load() != before+1 {
		t.Fatalf("확정 단계 신원 요청이 몫으로 나가지 않음: %+v %v", event, err)
	}
	// 확정 단계가 시작된 뒤에도 늦게 도착한 모델 단계 요청·Worker refresh_session은 몫을 쓰지 못한다.
	if admitted, last := modelRequests(t, e, server.URL, 1); admitted != 0 || last != BudgetExhausted {
		t.Fatalf("확정 단계 중 모델 단계 요청이 몫을 씀: %d %s", admitted, last)
	}
	if sent, reason := relogin(t, e, server.URL, false); sent || reason != BudgetExhausted {
		t.Fatalf("Worker refresh_session이 확정 몫을 씀: %v %s", sent, reason)
	}
	// 확정 단계가 연 다시 로그인은 몫을 쓴다(남은 몫 2).
	for i := 0; i < 2; i++ {
		if sent, reason := relogin(t, e, server.URL, true); !sent {
			t.Fatalf("확정 전 다시 로그인 %d이 몫으로 나가지 않음: %s", i, reason)
		}
	}
	// 몫을 다 쓰면 확정 단계 요청도 더 보내지 않는다.
	if sent, reason := relogin(t, e, server.URL, true); sent || reason != BudgetExhausted {
		t.Fatalf("확정 몫을 넘겨 다시 로그인: %v %s", sent, reason)
	}
	if event, err := e.SendE3Identity(t.Context(), in); err != nil || !event.NotSent {
		t.Fatalf("확정 몫을 넘겨 신원 요청: %+v %v", event, err)
	}
}

// 확정 단계 요청이 몫을 쓴 만큼 떼어 둘 몫이 줄어, 그 밖의 예산은 모델 단계가 쓴다.
func TestE3ShareShrinksAsConfirmationUsesIt(t *testing.T) {
	e, in, server, _ := e3ShareFixture(t, 5, 2)
	if admitted, _ := modelRequests(t, e, server.URL, 2); admitted != 2 {
		t.Fatal("몫 밖 예산을 모델 단계가 쓰지 못함")
	}
	if event, err := e.SendE3Identity(t.Context(), in); err != nil || event.NotSent {
		t.Fatalf("확정 단계 신원 요청: %+v %v", event, err)
	}
	if remaining, err := e.BudgetRemaining(); err != nil || remaining != 1 {
		t.Fatalf("확정 단계가 몫을 쓴 뒤 남은 예산 %d %v", remaining, err)
	}
	if admitted, last := modelRequests(t, e, server.URL, 2); admitted != 1 || last != BudgetExhausted {
		t.Fatalf("떼어 둘 몫이 확정 단계 사용만큼 줄지 않음: 보냄 %d 마지막 %s", admitted, last)
	}
}

// 확정 단계가 꺼진 회차(몫 0)는 지금처럼 모델 단계가 예산 전부를 쓰고, 확정 단계 전용 다시 로그인은 보낼 몫이 없다.
func TestE3ShareZeroLeavesModelBudget(t *testing.T) {
	e, _, server, _ := e3ShareFixture(t, 5, 0)
	if remaining, err := e.BudgetRemaining(); err != nil || remaining != 5 {
		t.Fatalf("몫 0인데 남은 예산 %d %v", remaining, err)
	}
	if admitted, last := modelRequests(t, e, server.URL, 6); admitted != 5 || last != BudgetExhausted {
		t.Fatalf("몫 0인데 모델 예산이 줄어듦: 보냄 %d 마지막 %s", admitted, last)
	}
	e2, _, server2, _ := e3ShareFixture(t, 5, 0)
	if sent, reason := relogin(t, e2, server2.URL, true); sent || reason != BudgetExhausted {
		t.Fatalf("몫 0 회차에서 확정 단계 다시 로그인이 나감: %v %s", sent, reason)
	}
	if sent, _ := relogin(t, e2, server2.URL, false); !sent {
		t.Fatal("몫 0 회차의 Worker refresh_session이 막힘")
	}
}

// 확정 단계 다시 로그인도 확정 몫 사용으로 센다. 몫을 다 쓰면 예산이 남아도 확정 단계 요청을 더 보내지 않는다.
func TestE3ShareCountsRelogins(t *testing.T) {
	e, _, server, _ := e3ShareFixture(t, 10, 2)
	for i := 0; i < 2; i++ {
		if sent, reason := relogin(t, e, server.URL, true); !sent {
			t.Fatalf("확정 전 다시 로그인 %d: %s", i, reason)
		}
	}
	// 확정 단계 요청은 떼어 둔 몫에서 나가므로 모델 단계가 쓸 남은 예산(10 − 2)은 그대로다.
	if remaining, err := e.BudgetRemaining(); err != nil || remaining != 8 {
		t.Fatalf("확정 단계 요청 뒤 남은 예산 %d %v", remaining, err)
	}
	if sent, reason := relogin(t, e, server.URL, true); sent || reason != BudgetExhausted {
		t.Fatalf("확정 몫을 넘겨 다시 로그인(예산은 남음): %v %s", sent, reason)
	}
}
