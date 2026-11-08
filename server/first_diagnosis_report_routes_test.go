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
