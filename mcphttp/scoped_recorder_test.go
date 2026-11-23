package mcphttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// 탐색 MCP 세션 기록(2026-11-24 재시작 정리): 열린 세션은 기록하고, 지운 세션만 기록에서 뺀다. 남은 세션은
// DeleteScopedSession으로 지운다.

type recorderFixture struct {
	mu     sync.Mutex
	opened []string
	closed []string
}

func (r *recorderFixture) Opened(address, sid string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.opened = append(r.opened, address+" "+sid)
	return nil
}

func (r *recorderFixture) Closed(address, sid string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = append(r.closed, address+" "+sid)
}

func scopedMCPFixture(t *testing.T, deleteStatus int, deleted *[]string, mu *sync.Mutex) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			mu.Lock()
			*deleted = append(*deleted, r.Header.Get("Mcp-Session-Id")+" "+r.Header.Get("MCP-Protocol-Version"))
			mu.Unlock()
			w.WriteHeader(deleteStatus)
			return
		}
		var req rpcRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			w.WriteHeader(400)
			return
		}
		if req.Method == "notifications/initialized" {
			w.WriteHeader(202)
			return
		}
		w.Header().Set("Mcp-Session-Id", "browser-session-1")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"protocolVersion": protocolVersion}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestScopedSessionRecorderTracksOpenAndDeletedOnly(t *testing.T) {
	rec := &recorderFixture{}
	SetScopedSessionRecorder(rec)
	t.Cleanup(func() { SetScopedSessionRecorder(nil) })
	var mu sync.Mutex
	var deleted []string
	ok := scopedMCPFixture(t, http.StatusOK, &deleted, &mu)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	client, err := NewScoped(ctx, "owned", ok.URL+"/mcp")
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.opened) != 1 || rec.opened[0] != ok.URL+"/mcp browser-session-1" {
		t.Fatalf("열린 세션 기록: %v", rec.opened)
	}
	if err := client.CloseContext(t.Context()); err != nil || len(rec.closed) != 1 {
		t.Fatalf("지운 세션 기록: %v %v", rec.closed, err)
	}
	// 지우기를 거부하면 기록에서 빼지 않는다(다음 시작 때 다시 지운다).
	bad := scopedMCPFixture(t, http.StatusInternalServerError, &deleted, &mu)
	client, err = NewScoped(ctx, "owned", bad.URL+"/mcp")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CloseContext(t.Context()); err == nil || len(rec.closed) != 1 {
		t.Fatalf("거부된 지우기를 기록에서 뺌: %v %v", rec.closed, err)
	}
}

func TestDeleteScopedSession(t *testing.T) {
	var mu sync.Mutex
	var deleted []string
	srv := scopedMCPFixture(t, http.StatusNotFound, &deleted, &mu)
	if err := DeleteScopedSession(t.Context(), srv.URL+"/mcp", "left-session"); err != nil {
		t.Fatal("이미 없는 세션은 성공이어야 함:", err)
	}
	mu.Lock()
	got := append([]string(nil), deleted...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "left-session "+protocolVersion {
		t.Fatalf("지우기 요청: %v", got)
	}
	for _, bad := range []struct{ address, sid string }{{"https://explore-mcp:8931/mcp", "s"}, {srv.URL + "/other", "s"}, {srv.URL + "/mcp", ""}, {"http://user@explore-mcp:8931/mcp", "s"}} {
		if DeleteScopedSession(t.Context(), bad.address, bad.sid) == nil {
			t.Fatalf("잘못된 입력을 받음: %+v", bad)
		}
	}
	fail := scopedMCPFixture(t, http.StatusBadGateway, &deleted, &mu)
	if DeleteScopedSession(t.Context(), fail.URL+"/mcp", "x") == nil {
		t.Fatal("거부를 성공으로 봄")
	}
}
