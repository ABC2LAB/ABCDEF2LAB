//go:build linux

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/exploration"
)

// 재시작 정리(2026-11-24): 새로 시작한 준비 서비스가 이전 실행이 남긴 경계 세션을 닫고, 기록된 MCP 세션(브라우저 문맥)을 지운다.

type mcpDeleteFixture struct {
	mu      sync.Mutex
	deleted []string
	status  int
}

func (m *mcpDeleteFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.Method != http.MethodDelete {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	m.deleted = append(m.deleted, r.Header.Get("Mcp-Session-Id"))
	w.WriteHeader(m.status)
}

func (m *mcpDeleteFixture) got() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.deleted...)
}

func cleanupFixture(t *testing.T, status int) (hostFixture, *mcpDeleteFixture) {
	t.Helper()
	f := newHostFixture(t)
	mcp := &mcpDeleteFixture{status: status}
	srv := httptest.NewServer(mcp)
	t.Cleanup(srv.Close)
	store, err := newProductMCPSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.host.mcpURL = srv.URL + "/mcp"
	f.host.mcpSessions = store
	return f, mcp
}

// 이전 실행이 연 경계 세션과 MCP 세션이 남아 있으면, 정리는 경계 세션을 닫고(닫힘 확인) MCP 세션을 지운다.
func TestCleanupStaleClosesBoundarySessionAndBrowserContext(t *testing.T) {
	f, mcp := cleanupFixture(t, http.StatusOK)
	host, err := f.host.factory(f.rec, f.current, f.digest, nil, 4096, &productTargetConnection{IP: f.rec.SelectedIP, SPKI: f.rec.Target.TLSSPKISHA256, jobID: f.binding.JobID})(t.Context(), f.binding, &agent.Worker{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.Session.Boundary.Open(t.Context(), f.binding); err != nil {
		t.Fatal(err)
	}
	if err := f.host.mcpSessions.Opened(f.host.mcpURL, "mcp-session-old"); err != nil {
		t.Fatal(err)
	}
	// 같은 설치의 MCP가 아닌 주소의 기록은 보내지 않고 뺀다.
	if err := f.host.mcpSessions.Opened("http://other-mcp:8931/mcp", "mcp-session-elsewhere"); err != nil {
		t.Fatal(err)
	}
	// 준비 서비스가 죽고 새로 시작했다: 새 host는 아직 아무 세션도 열지 않았다.
	f.host.cleaned = false
	closed, err := f.host.cleanupStale(t.Context())
	if err != nil || !closed {
		t.Fatalf("정리: closed=%v err=%v", closed, err)
	}
	if st, err := f.host.control.CurrentSession(t.Context()); err != nil || st == nil || st.Active || st.ClosedReason != exploration.Cancelled {
		t.Fatalf("경계 세션이 닫히지 않음: %+v %v", st, err)
	}
	if got := mcp.got(); len(got) != 1 || got[0] != "mcp-session-old" {
		t.Fatalf("MCP 세션 지우기: %v", got)
	}
	if left, err := f.host.mcpSessions.pending(); err != nil || len(left) != 0 {
		t.Fatalf("기록이 남음: %v %v", left, err)
	}
	info, err := os.Stat(f.host.mcpSessions.path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("기록 파일 권한: %v %v", info, err)
	}
	// 정리 뒤에는 새 탐색 세션을 열 수 있다.
	next := f.binding
	next.SessionRef = "explore-host-2"
	if _, err := host.Session.Boundary.Open(t.Context(), next); err != nil {
		t.Fatal("정리 뒤 새 세션:", err)
	}
}

// MCP가 세션 지우기를 거부하면(오류·405 종료 미허용) 기록을 남겨 다음 시작 때 다시 지운다. 경계 세션은 그래도 닫는다
// (페이지가 대상에 닿지 못함).
func TestCleanupStaleKeepsUndeletedBrowserContext(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusMethodNotAllowed} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f, mcp := cleanupFixture(t, status)
			if err := f.host.mcpSessions.Opened(f.host.mcpURL, "mcp-session-old"); err != nil {
				t.Fatal(err)
			}
			closed, err := f.host.cleanupStale(t.Context())
			if err != nil || closed {
				t.Fatalf("열린 경계 세션이 없을 때: closed=%v err=%v", closed, err)
			}
			if len(mcp.got()) != 1 {
				t.Fatal("MCP 세션 지우기를 시도하지 않음")
			}
			if left, err := f.host.mcpSessions.pending(); err != nil || len(left) != 1 || left[0].SessionID != "mcp-session-old" {
				t.Fatalf("지우지 못한 기록을 잃음: %v %v", left, err)
			}
		})
	}
}

// 경계에 닿지 못하면 정리를 끝내지 못하고, 그동안 새 탐색을 열지 않는다.
func TestEnsureCleanBlocksExplorationUntilCleanup(t *testing.T) {
	f, _ := cleanupFixture(t, http.StatusOK)
	good := f.host.control
	f.host.control.Path = f.host.control.Path + ".missing"
	if _, err := f.host.factory(f.rec, f.current, f.digest, nil, 4096, &productTargetConnection{IP: f.rec.SelectedIP, SPKI: f.rec.Target.TLSSPKISHA256, jobID: f.binding.JobID})(t.Context(), f.binding, &agent.Worker{}); err == nil {
		t.Fatal("정리 전에 탐색을 엶")
	}
	f.host.control = good
	if _, err := f.host.factory(f.rec, f.current, f.digest, nil, 4096, &productTargetConnection{IP: f.rec.SelectedIP, SPKI: f.rec.Target.TLSSPKISHA256, jobID: f.binding.JobID})(t.Context(), f.binding, &agent.Worker{}); err != nil {
		t.Fatal("정리 뒤 탐색:", err)
	}
}

// 기록 저장소는 상한을 넘겨 쌓지 않고, 지운 세션만 뺀다.
func TestProductMCPSessionStoreBounds(t *testing.T) {
	store, err := newProductMCPSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxProductMCPSessions; i++ {
		if err := store.Opened("http://explore-mcp:8931/mcp", "s"+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	if store.Opened("http://explore-mcp:8931/mcp", "over") == nil {
		t.Fatal("상한을 넘겨 기록함")
	}
	store.Closed("http://explore-mcp:8931/mcp", "sa")
	if left, _ := store.pending(); len(left) != maxProductMCPSessions-1 {
		t.Fatalf("지운 세션: %d", len(left))
	}
}
