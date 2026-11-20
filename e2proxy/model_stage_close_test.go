package e2proxy

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

// 모델 단계를 마감하면(CloseModelStage) E②는 확정 단계 요청만 받는다(사용자 결정 2026-11-20). 마감 전에 열린 모델 단계
// 호출의 진행 중 요청은 끊기고 늦은 요청은 대상에 가지 않으며, 새 모델 단계 호출·인증 준비·Worker refresh_session은 열리지
// 않는다. 확정 단계 신원 요청과 확정 단계 다시 로그인은 그대로 나간다.
func TestCloseModelStageAdmitsOnlyConfirmation(t *testing.T) {
	var held, modelHits atomic.Int64
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/allowed/held":
			held.Add(1)
			entered <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
			}
		case "/allowed":
			modelHits.Add(1)
		case "/login":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "synthetic-relogin-session"})
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"subject-a"}`)
		}
	}))
	defer server.Close()
	defer close(release)
	_, in := e3IdentityFixture(t, server.URL, &coreRecorder{})
	cfg := coreConfig(t, server.URL, &coreRecorder{}, 20)
	cfg.AuditVersion = JournalVersionV3
	cfg.E3Verifier = e3IdentityVerifier{}
	cfg.E3ConfirmationRequests = 4
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

	// 마감 전에 연 모델 단계 호출 하나가 대상에 붙잡힌 요청을 들고 있다.
	worker, err := e.BeginCall(t.Context(), "worker")
	if err != nil {
		t.Fatal(err)
	}
	inflight := make(chan error, 1)
	go func() {
		resp, _, err := worker.Do(t.Context(), coreRequest(t, "GET", server.URL+"/allowed/held"))
		if err == nil {
			resp.Body.Close()
		}
		inflight <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("붙잡힌 요청이 대상에 닿지 않음")
	}
	if e.ModelStageClosed() {
		t.Fatal("마감 전인데 모델 단계가 닫힘")
	}
	e.CloseModelStage()
	e.CloseModelStage() // 여러 번 불러도 같다.
	if !e.ModelStageClosed() {
		t.Fatal("모델 단계가 마감되지 않음")
	}
	select {
	case err := <-inflight:
		if err == nil {
			t.Fatal("마감 뒤에도 진행 중 모델 단계 요청이 끝까지 감")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("모델 단계 마감이 진행 중 요청을 끊지 않음")
	}
	// 같은 호출의 늦은 요청은 대상에 가지 않는다.
	if _, r, err := worker.Do(t.Context(), coreRequest(t, "GET", server.URL+"/allowed")); err == nil || modelHits.Load() != 0 {
		t.Fatalf("마감 뒤 늦은 모델 단계 요청이 나감: %s %v", r.Reason, err)
	}
	_, _ = worker.Close(t.Context())
	// 새 모델 단계 호출·인증 준비·Worker refresh_session은 열리지 않는다.
	if _, err := e.BeginCall(t.Context(), "late-worker"); !errors.Is(err, ErrClosed) {
		t.Fatalf("마감 뒤 새 Worker 호출: %v", err)
	}
	if _, err := e.BeginProgramCall(t.Context(), ProgramRefreshSession, "auth-refresh"); !errors.Is(err, ErrClosed) {
		t.Fatalf("마감 뒤 Worker refresh_session: %v", err)
	}
	if _, err := e.BeginProgramCall(t.Context(), ProgramAuthPrep, "auth-prep"); !errors.Is(err, ErrClosed) {
		t.Fatalf("마감 뒤 인증 준비: %v", err)
	}
	// 확정 단계 요청은 나간다.
	if sent, reason := relogin(t, e, server.URL, true); !sent {
		t.Fatalf("마감 뒤 확정 단계 다시 로그인이 막힘: %s", reason)
	}
	if event, err := e.SendE3Identity(t.Context(), in); err != nil || event.NotSent {
		t.Fatalf("마감 뒤 확정 단계 신원 요청이 막힘: %+v %v", event, err)
	}
	if modelHits.Load() != 0 || held.Load() != 1 {
		t.Fatalf("마감 뒤 모델 단계 송신: 일반 %d 붙잡힘 %d", modelHits.Load(), held.Load())
	}
}

// nil 실행은 아무 일도 하지 않는다.
func TestCloseModelStageNil(t *testing.T) {
	var e *Execution
	e.CloseModelStage()
	if e.ModelStageClosed() {
		t.Fatal("nil 실행이 닫힘으로 보임")
	}
}
