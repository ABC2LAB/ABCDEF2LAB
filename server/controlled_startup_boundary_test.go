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

// 회차 도중 엔진 일시정지를 걸 수 있는 입구가 첫 진단 서버에서 닫혀 있다(계약 §7.3 일시정지 원인 표): 일괄 control(운영자
// 일시정지·동시 실행 조정), 설정(동시 실행 제한을 켜 llm_unavailable_queued), 재개(queued_for_admission), 작업 삭제
// (BeginDelete), 보관 복원(paused_on_reload), 대화(Main Agent의 pause_task 도구). 단일 control은 운영자 중지로 받는다.
func TestFirstDiagnosisClosesEnginePauseEntries(t *testing.T) {
	s := &Server{firstDiagnosis: true}
	for _, item := range []struct{ method, path string }{
		{"POST", "/api/tasks/control/batch"}, {"PUT", "/api/settings"}, {"POST", "/api/tasks/1/resume"}, {"DELETE", "/api/tasks/1"},
		{"POST", "/api/task-archives/1/restore"}, {"POST", "/api/task-archives/restore/batch"}, {"POST", "/api/tasks/1/archive"},
		{"POST", "/api/chat"}, {"POST", "/api/tasks/1/intents/1/control"},
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
