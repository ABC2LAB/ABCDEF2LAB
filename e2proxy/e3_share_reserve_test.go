package e2proxy

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
)

// e3ShareFixture는 대상 예산 total 가운데 확정 몫 share를 떼어 둔 실행이다(신원 규칙 포함, 나머지는 e3IdentityFixture와 같음).
func e3ShareFixture(t *testing.T, total, share uint64) (*Execution, E3IdentityInput, *httptest.Server, *atomic.Int64) {
	t.Helper()
	var received atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"subject-a"}`)
	}))
	t.Cleanup(server.Close)
	_, in := e3IdentityFixture(t, server.URL, &coreRecorder{})
	cfg := coreConfig(t, server.URL, &coreRecorder{}, total)
	cfg.AuditVersion = JournalVersionV3
	cfg.E3Verifier = e3IdentityVerifier{}
	cfg.E3ConfirmationRequests = share
	cfg.Rules = append(cfg.Rules, Rule{Ref: "identity-rule", Purpose: PurposeE3Identity, Origin: server.URL,
		ConnectIP: netip.MustParseAddr("127.0.0.1"), PathPrefix: "/allowed/identity", PathScopeRef: "identity-path", Methods: []string{"GET"}})
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

// 계약 §7.1: 모델 단계는 확정 몫을 뺀 범위에서 돈다. 몫이 열리기 전 확정 단계 밖 요청은 예산의 마지막 확정 몫만큼을
// 쓰지 못하고(budget_exhausted), 남은 예산 조회도 그만큼 뺀다. 몫을 열면 확정 단계가 그 몫으로 보낸다.
func TestE3ShareIsHeldFromModelStage(t *testing.T) {
	e, in, server, received := e3ShareFixture(t, 5, 2)
	if remaining, err := e.BudgetRemaining(); err != nil || remaining != 3 {
		t.Fatalf("처음 남은 예산 %d %v (예산 5 − 확정 몫 2)", remaining, err)
	}
	admitted, last := modelRequests(t, e, server.URL, 5)
	if admitted != 3 || last != BudgetExhausted {
		t.Fatalf("모델 단계가 확정 몫까지 씀: 보냄 %d 마지막 거부 %s", admitted, last)
	}
	if remaining, err := e.BudgetRemaining(); err != nil || remaining != 0 {
		t.Fatalf("몫이 열리기 전 남은 예산 %d %v (0이어야 모델 단계가 마감)", remaining, err)
	}
	e.OpenE3Share()
	if remaining, err := e.BudgetRemaining(); err != nil || remaining != 2 {
		t.Fatalf("몫을 연 뒤 남은 예산 %d %v", remaining, err)
	}
	before := received.Load()
	event, err := e.SendE3Identity(t.Context(), in)
	if err != nil || event.NotSent || received.Load() != before+1 {
		t.Fatalf("확정 단계 신원 요청이 몫으로 나가지 않음: %+v %v", event, err)
	}
	// 연 뒤에는 확정 단계의 다시 로그인처럼 확정 단계 밖 경로 요청도 남은 예산을 쓴다.
	if admitted, _ := modelRequests(t, e, server.URL, 1); admitted != 1 {
		t.Fatal("몫을 연 뒤 남은 예산을 쓰지 못함")
	}
}

// 확정 몫이 열리기 전에도 E③ 프로그램 요청 자체는 몫을 쓴다(몫은 확정 단계 밖 요청만 막는다). 프로그램 요청이 쓴 만큼
// 떼어 둘 몫이 줄어든다.
func TestE3ShareHeldStillAdmitsProgramRequests(t *testing.T) {
	e, in, server, received := e3ShareFixture(t, 5, 2)
	if admitted, _ := modelRequests(t, e, server.URL, 2); admitted != 2 {
		t.Fatal("몫 밖 예산을 모델 단계가 쓰지 못함")
	}
	before := received.Load()
	event, err := e.SendE3Identity(t.Context(), in)
	if err != nil || event.NotSent || received.Load() != before+1 {
		t.Fatalf("E③ 프로그램 요청이 떼어 둔 몫을 쓰지 못함: %+v %v", event, err)
	}
	// 남은 예산 2 가운데 떼어 둘 몫은 이제 1이라 모델 단계는 1건만 더 쓴다.
	if remaining, err := e.BudgetRemaining(); err != nil || remaining != 1 {
		t.Fatalf("E③가 몫을 쓴 뒤 남은 예산 %d %v", remaining, err)
	}
	if admitted, last := modelRequests(t, e, server.URL, 2); admitted != 1 || last != BudgetExhausted {
		t.Fatalf("떼어 둘 몫이 E③ 사용만큼 줄지 않음: 보냄 %d 마지막 %s", admitted, last)
	}
}

// 확정 단계가 꺼진 회차(몫 0)는 지금처럼 모델 단계가 예산 전부를 쓴다.
func TestE3ShareZeroLeavesModelBudget(t *testing.T) {
	e, _, server, _ := e3ShareFixture(t, 5, 0)
	admitted, last := modelRequests(t, e, server.URL, 6)
	if admitted != 5 || last != BudgetExhausted {
		t.Fatalf("몫 0인데 모델 예산이 줄어듦: 보냄 %d 마지막 %s", admitted, last)
	}
	if remaining, err := e.BudgetRemaining(); err != nil || remaining != 0 {
		t.Fatalf("남은 예산 %d %v", remaining, err)
	}
	e.OpenE3Share() // 몫 0이면 아무 것도 바뀌지 않는다.
	if admitted, _ := modelRequests(t, e, server.URL, 1); admitted != 0 {
		t.Fatal("몫 0 회차에서 몫을 열자 예산이 늘어남")
	}
}

// nil 실행에서 OpenE3Share는 아무 일도 하지 않는다.
func TestOpenE3ShareNil(t *testing.T) {
	var e *Execution
	e.OpenE3Share()
	if _, err := e.BudgetRemaining(); !errors.Is(err, ErrConfig) {
		t.Fatal("nil 실행의 남은 예산")
	}
}
