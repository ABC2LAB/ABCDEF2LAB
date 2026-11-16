package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFirstDiagnosisReportRouteScope(t *testing.T) {
	s := &Server{firstDiagnosis: true, firstModel: &firstDiagnosisModelPin{TaskID: "17", TaskNumber: 17, ExplorationID: 9}}
	for _, tc := range []struct {
		method, path string
		allowed      bool
	}{
		{"GET", "/api/tasks/17/e2-prompt-projection?run_ref=run-1", true},
		{"GET", "/api/tasks/18/e2-prompt-projection?run_ref=run-1", false},
		{"GET", "/api/tasks/17/e2-prompt-projection?run_ref=run-1&run_ref=other", false},
		{"GET", "/api/tasks/17/e2-prompt-projection?run_ref=run-1&raw=true", false},
		{"HEAD", "/api/tasks/17/e2-prompt-projection?run_ref=run-1", false},
		{"GET", "/api/tasks/17/access-map", true},
		{"GET", "/api/tasks/18/access-map", false},
		{"GET", "/api/tasks/17/access-map?run_ref=past", false},
		{"GET", "/api/tasks/17/access-map?case_ref=case-1", false},
		{"GET", "/api/tasks/17/access-map?", false},
		{"HEAD", "/api/tasks/17/access-map", false},
		{"POST", "/api/tasks/17/access-map", false},
		{"GET", "/api/report/diagnostic?task=17", true},
		{"HEAD", "/api/report/diagnostic?task=17&download=1", true},
		{"GET", "/api/report/diagnostic/remediation?task=17&audience=internal", true},
		{"POST", "/api/report/diagnostic/remediation?task=17&audience=internal", true},
		{"GET", "/api/report/diagnostic/remediation/prompt?task=17", true},
		{"GET", "/api/report/diagnostic?task=18", false},
		{"GET", "/api/report/diagnostic", false},
		{"GET", "/api/report/diagnostic?task=017", false},
		{"GET", "/api/report/diagnostic?task=17&task=18", false},
		{"GET", "/api/report/diagnostic?task=17&task=17", false},
		{"GET", "/api/report/diagnostic?task=17&run=&run=foreign", false},
		{"GET", "/api/report/diagnostic?task=17&audience=shared&audience=internal", false},
		{"GET", "/api/report/diagnostic/remediation/prompt?task=17&record=1&record=2", false},
		{"GET", "/api/report/diagnostic?task=17&bad=%zz", false},
		{"POST", "/api/report/diagnostic?task=17", false},
		{"DELETE", "/api/report/diagnostic/remediation?task=17", false},
		{"POST", "/api/report/diagnostic/remediation/prompt?task=17", false},
		{"GET", "/api/report?task=17", false},
		{"GET", "/api/report/diagnostic/remediation/anything?task=17", false},
		{"POST", "/api/tasks/17/resume", false},
		{"POST", "/api/chat?task=17", false},
		{"GET", "/api/update/check", false},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			calls := 0
			handler := s.firstDiagnosisRoutes(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusNoContent) }))
			w := httptest.NewRecorder()
			request := httptest.NewRequest(tc.method, tc.path, nil)
			request.Header.Set("Authorization", "Bearer synthetic-route-test")
			request.Header.Set("Content-Type", "application/json")
			handler.ServeHTTP(w, request)
			if tc.allowed && (w.Code != http.StatusNoContent || calls != 1) || !tc.allowed && (w.Code != http.StatusForbidden || calls != 0) {
				t.Fatalf("report route status=%d calls=%d allowed=%v", w.Code, calls, tc.allowed)
			}
		})
	}
	for _, mode := range []string{"unbound", "preparation"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "unbound" {
				s.firstModel = nil
			} else {
				s.firstModel = &firstDiagnosisModelPin{TaskID: "17", TaskNumber: 17, ExplorationID: 9}
				s.firstDiagnosisPreparation = true
			}
			w := httptest.NewRecorder()
			s.firstDiagnosisRoutes(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("unbound/preparation report admitted") })).ServeHTTP(w, httptest.NewRequest("GET", "/api/report/diagnostic?task=17", nil))
			if w.Code != http.StatusForbidden {
				t.Fatal("missing admission refusal")
			}
		})
	}
}

func TestFirstDiagnosisReportQueryRejectsDuplicateValues(t *testing.T) {
	for _, path := range []string{
		"/api/report/diagnostic?task=17&task=17",
		"/api/report/diagnostic?task=17&task=18",
		"/api/report/diagnostic?task=17&audience=shared&audience=internal",
	} {
		if _, ok := firstDiagnosisReportRequestTask(httptest.NewRequest("GET", path, nil)); ok {
			t.Fatal("ambiguous report query accepted", path)
		}
	}
	for _, path := range []string{"/api/report/diagnostic?task=17", "/api/report/diagnostic?audience=internal&task=17", "/api/report/diagnostic?task=17&download=1"} {
		if task, ok := firstDiagnosisReportRequestTask(httptest.NewRequest("GET", path, nil)); !ok || task != 17 {
			t.Fatal("unambiguous control rejected", path)
		}
	}
}

// C33: 제품 경로에서 활동 조회(읽기)와 일시정지·재개(control)를 연다. 채팅·intent 조종·rerun·활동 쓰기는
// admission/전송 인접이라 막힌 채로 둔다(E² 담당 정렬 전까지). preparation 서버는 그대로 전부 막는다.
func TestFirstDiagnosisC33OperatorRoutes(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		allowed      bool
	}{
		// 활동 조회(읽기 전용) — 연다.
		{"GET", "/api/exploration/activity", true},
		{"GET", "/api/exploration/activity/history", true},
		{"GET", "/api/exploration/activity/stream", true},
		{"GET", "/api/exploration/activity/5", true},
		// 일시정지·재개(핸들러가 action을 pause|resume로 제한) — 연다.
		{"POST", "/api/tasks/17/control", true},
		{"POST", "/api/tasks/9/control", true},
		// 아직 막는 것: 쓰기 메서드의 활동, 비숫자 seq, 잘못된 모양.
		{"POST", "/api/exploration/activity", false},
		{"GET", "/api/exploration/activity/abc", false},
		{"GET", "/api/tasks/17/control", false},
		{"POST", "/api/tasks/17/control/extra", false},
		{"POST", "/api/tasks/abc/control", false},
		// admission/전송 인접 — 이동찬 허용 판단으로 보류(게이트가 의도적으로 막는 "수정 API").
		{"POST", "/api/tasks/17/intents/3/messages", false},    // 채팅
		{"POST", "/api/tasks/17/intents/3/control", false},     // intent 조종(steer)
		{"POST", "/api/tasks/17/intents/3/rerun", false},       // rerun(종료 task 재admission)
		{"POST", "/api/tasks/17/intents/rerun-blocked", false}, // rerun 일괄
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			s := &Server{firstDiagnosis: true, firstModel: &firstDiagnosisModelPin{TaskID: "17", TaskNumber: 17, ExplorationID: 9}}
			calls := 0
			handler := s.firstDiagnosisRoutes(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusNoContent) }))
			w := httptest.NewRecorder()
			request := httptest.NewRequest(tc.method, tc.path, nil)
			request.Header.Set("Authorization", "Bearer synthetic-route-test")
			request.Header.Set("Content-Type", "application/json")
			handler.ServeHTTP(w, request)
			if tc.allowed && (w.Code != http.StatusNoContent || calls != 1) || !tc.allowed && (w.Code != http.StatusForbidden || calls != 0) {
				t.Fatalf("c33 route status=%d calls=%d allowed=%v", w.Code, calls, tc.allowed)
			}
		})
	}
	// preparation 서버는 활동·control도 전부 막는다(단락 평가 유지).
	t.Run("preparation blocks operator routes", func(t *testing.T) {
		s := &Server{firstDiagnosis: true, firstDiagnosisPreparation: true, firstModel: &firstDiagnosisModelPin{TaskID: "17", TaskNumber: 17, ExplorationID: 9}}
		for _, req := range []*http.Request{
			httptest.NewRequest("GET", "/api/exploration/activity", nil),
			httptest.NewRequest("POST", "/api/tasks/17/control", nil),
		} {
			w := httptest.NewRecorder()
			s.firstDiagnosisRoutes(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("preparation admitted operator route") })).ServeHTTP(w, req)
			if w.Code != http.StatusForbidden {
				t.Fatal("preparation did not block", req.URL.Path)
			}
		}
	})
}
