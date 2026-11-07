package server

import (
	"context"
	"github.com/Autumn-27/artex/agent"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFirstDiagnosisRejectsSideEffectingReadAndUncontrolledEntry(t *testing.T) {
	s := &Server{firstDiagnosis: true}
	for _, item := range []struct{ method, path string }{
		{"GET", "/api/update/check"}, {"GET", "/api/sync/scopesentry/projects"}, {"GET", "/api/sync/scopesentry/tasks"}, {"GET", "/api/mcp/1/tools"}, {"POST", "/api/chat"}, {"POST", "/api/tasks"}, {"POST", "/api/tasks/1/resume"}, {"GET", "/api/exploration/findings/1/retests"},
	} {
		t.Run(item.method+item.path, func(t *testing.T) {
			calls := 0
			h := s.firstDiagnosisRoutes(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
			response := httptest.NewRecorder()
			h.ServeHTTP(response, httptest.NewRequest(item.method, item.path, nil))
			if response.Code != http.StatusForbidden || calls != 0 {
				t.Fatalf("차단 실패: status=%d calls=%d", response.Code, calls)
			}
		})
	}
}

func TestFirstDiagnosisPreservesExistingResultReads(t *testing.T) {
	s := &Server{firstDiagnosis: true}
	for _, path := range []string{"/api/health", "/api/tasks/1", "/api/exploration/findings", "/api/exploration/findings/12", "/api/exploration/findings/12/lineage"} {
		t.Run(path, func(t *testing.T) {
			calls := 0
			h := s.firstDiagnosisRoutes(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusNoContent) }))
			response := httptest.NewRecorder()
			h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusNoContent || calls != 1 {
				t.Fatalf("조회 전달 실패: status=%d calls=%d", response.Code, calls)
			}
		})
	}
	s.firstDiagnosisPreparation = true
	response := httptest.NewRecorder()
	s.firstDiagnosisRoutes(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("준비 서버 HTTP 진입") })).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if response.Code != http.StatusForbidden {
		t.Fatal("준비 서버 차단 누락")
	}
}

func TestE2HTTPTimeoutNeverReopensUncontrolledPlanner(t *testing.T) {
	calls := 0
	e := &Engine{e2HTTP: &E2HTTPRuntime{}, resolve: func(*Task) (*agent.Planner, *agent.Worker) { calls++; return nil, nil }, resolveAuthoritative: true}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if e.runFinalPlannerRound(ctx, &Task{ID: "17"}) || calls != 0 {
		t.Fatalf("종결 Planner가 다시 열림: calls=%d", calls)
	}
}
