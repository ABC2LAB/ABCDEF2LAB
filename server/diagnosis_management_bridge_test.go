package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/artex/diagnosisapi"
	"github.com/Autumn-27/artex/report"
)

func bridgeTestAuth(r *http.Request) (string, error) {
	if r.Header.Get("Authorization") != "Bearer synthetic-bridge" {
		return "", errors.New("unauthorized")
	}
	return "ARTEX", nil
}
func bridgeTestAllowed(_ context.Context, owner, task string) bool {
	return owner == "ARTEX" && task == "42"
}
func TestManagementBridgeOnlyOperatorLoopbackOrigin(t *testing.T) {
	for _, origin := range []string{"http://example.com:80", "http://localhost:80", "http://127.0.0.1:80/path", "http://user:secret@127.0.0.1:80", "http://127.0.0.1:80?task=42", "http://127.0.0.1", "file:///tmp/path", "http://127.0.0.1:99999"} {
		if _, err := newDiagnosisManagementBridge(t.Context(), origin, bridgeTestAuth, bridgeTestAllowed); err == nil {
			t.Errorf("accepted %s", origin)
		}
	}
}
func TestManagementBridgeRouteClosure(t *testing.T) {
	for _, test := range []struct {
		method, path string
		allow        bool
	}{
		{"GET", "/api/tasks/42/request-approvals", true},
		{"GET", "/api/tasks/42/request-approvals/approval-1", true},
		{"POST", "/api/tasks/42/request-approvals/approval-1/decision", true},
		{"POST", "/api/tasks/42/request-approvals/stop", true},
		{"GET", "/api/tasks/42/diagnosis-run", true},
		// 작업대 실데이터(#533): 읽기만, 전송 통제 흐름만 커서 after 하나.
		{"GET", "/api/tasks/42/control-policy", true},
		{"GET", "/api/tasks/42/e2-flow", true},
		{"GET", "/api/tasks/42/e2-flow?after=7", true},
		{"POST", "/api/tasks/42/e2-flow", false},
		{"GET", "/api/tasks/42/control-policy?after=7", false},
		{"GET", "/api/tasks/42/e2-flow?after=7&after=8", false},
		{"GET", "/api/tasks/42/e2-flow?cursor=7", false},
		{"GET", "/api/tasks/042/e2-flow", false},
		{"GET", "/api/tasks/42/e2-flow/extra", false},
		{"GET", "/api/tasks/42/accounts", false},
		{"GET", "/api/report/diagnostic?task=42&audience=internal&download=1", true},
		{"GET", "/api/report/diagnostic?task=42&audience=internal&format=docx", true},
		{"GET", "/api/report/diagnostic/image?task=42&audience=internal&entry=finding-1&step=step-abc&digest=abc", true},
		{"GET", "/api/report/diagnostic/image?task=42&task=43&entry=finding-1", false},
		{"GET", "/api/report/diagnostic/image?task=42&url=https://other.test", false},
		{"POST", "/api/report/diagnostic/remediation?task=42&audience=internal", true},
		{"GET", "/api/report/diagnostic/remediation/prompt?task=42&entry=e&record=1&digest=x", true},
		{"POST", "/api/tasks", false},
		{"POST", "/api/tasks/42/control", false},
		{"GET", "/api/tasks/42", false},
		{"GET", "/api/tasks/042/diagnosis-run", false},
		{"GET", "/api/tasks/42/request-approvals?token=secret", false},
		{"GET", "/api/report/diagnostic?task=42&task=43", false},
		{"GET", "/api/report/diagnostic?task=42&token=secret", false},
		{"GET", "/api/report/diagnostic?task=42&Target=http://other", false},
		{"POST", "/api/report/diagnostic?task=42", false},
		{"GET", "/api/llm", false},
		{"POST", "/api/tasks/42/request-approvals/a%2Fb/decision", false},
	} {
		t.Run(test.method+test.path, func(t *testing.T) {
			r := httptest.NewRequest(test.method, test.path, nil)
			r.Header.Set("Authorization", "Bearer synthetic-bridge")
			r.Header.Set("Content-Type", "application/json")
			task, ok := managementBridgeTask(r)
			if ok != test.allow || ok && task != "42" {
				t.Fatalf("allow=%v task=%q", ok, task)
			}
		})
	}
}
func TestManagementBridgeBoundTaskHeadersAndNoRedirect(t *testing.T) {
	var calls atomic.Int32
	var redirect atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer synthetic-bridge" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Secret") != "" || r.Header.Get("Idempotency-Key") != "" {
			t.Error("unexpected forwarded headers")
		}
		if redirect.Load() {
			http.Redirect(w, r, "http://unapproved.invalid/", http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "forbidden=value")
		w.Header().Set("Location", "http://unapproved.invalid")
		io.WriteString(w, `{"run":"synthetic"}`)
	}))
	defer upstream.Close()
	bridge, err := newDiagnosisManagementBridge(t.Context(), upstream.URL, bridgeTestAuth, bridgeTestAllowed)
	if err != nil {
		t.Fatal(err)
	}
	call := func(path, auth string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", auth)
		r.Header.Set("Cookie", "secret=value")
		r.Header.Set("X-Secret", "hidden")
		r.Header.Set("Idempotency-Key", "not-forwarded")
		w := httptest.NewRecorder()
		bridge.ServeHTTP(w, r)
		return w
	}
	if w := call("/api/tasks/42/request-approvals", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := call("/api/tasks/43/request-approvals", "Bearer synthetic-bridge"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if calls.Load() != 0 {
		t.Fatal("unauthorized reached upstream")
	}
	if w := call("/api/tasks/42/request-approvals", "Bearer synthetic-bridge"); w.Code != 200 || w.Header().Get("Set-Cookie") != "" || w.Header().Get("Location") != "" {
		t.Fatal(w.Code, w.Header())
	}
	redirect.Store(true)
	if w := call("/api/tasks/42/request-approvals", "Bearer synthetic-bridge"); w.Code != 503 && w.Code != 502 {
		t.Fatal(w.Code)
	}
	if calls.Load() != 2 {
		t.Fatal("redirect followed", calls.Load())
	}
}
func TestManagementBridgeUnknownPostIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	}))
	defer upstream.Close()
	bridge, err := newDiagnosisManagementBridge(t.Context(), upstream.URL, bridgeTestAuth, bridgeTestAllowed)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/tasks/42/request-approvals/stop", strings.NewReader("{}"))
	r.Header.Set("Authorization", "Bearer synthetic-bridge")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	bridge.ServeHTTP(w, r)
	if w.Code != 503 || calls.Load() != 1 {
		t.Fatal("POST replayed", w.Code, calls.Load())
	}
}
func TestManagementBridgeBoundsRequestAndResponse(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, strings.Repeat("x", managementBridgeResponseLimit+1))
	}))
	defer upstream.Close()
	bridge, err := newDiagnosisManagementBridge(t.Context(), upstream.URL, bridgeTestAuth, bridgeTestAllowed)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/tasks/42/request-approvals/stop", strings.NewReader(strings.Repeat(" ", managementBridgeRequestLimit+1)))
	r.Header.Set("Authorization", "Bearer synthetic-bridge")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	bridge.ServeHTTP(w, r)
	if w.Code != 413 || calls.Load() != 0 {
		t.Fatal(w.Code, calls.Load())
	}
	r = httptest.NewRequest("GET", "/api/report/diagnostic?task=42", nil)
	r.Header.Set("Authorization", "Bearer synthetic-bridge")
	w = httptest.NewRecorder()
	bridge.ServeHTTP(w, r)
	if w.Code != 502 {
		t.Fatal(w.Code)
	}
}

var _ diagnosisapi.Authenticate = bridgeTestAuth

func TestManagementBridgeRechecksAfterBody(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer upstream.Close()
	var allowed atomic.Bool
	allowed.Store(true)
	bridge, err := newDiagnosisManagementBridge(context.Background(), upstream.URL, bridgeTestAuth, func(context.Context, string, string) bool { return allowed.Load() })
	if err != nil {
		t.Fatal(err)
	}
	reader, writer := io.Pipe()
	req := httptest.NewRequest("POST", "/api/tasks/42/request-approvals/approval/decision", reader)
	req.Header.Set("Authorization", "Bearer synthetic-bridge")
	req.Header.Set("Content-Type", "application/json")
	done := make(chan struct{})
	w := httptest.NewRecorder()
	go func() { bridge.ServeHTTP(w, req); reader.Close(); close(done) }()
	// A write completes only after the handler begins reading the body.
	if _, err = writer.Write([]byte("{")); err != nil {
		t.Fatal(err)
	}
	allowed.Store(false)
	writer.Write([]byte("}"))
	writer.Close()
	<-done
	if w.Code != 403 || calls.Load() != 0 {
		t.Fatalf("status=%d upstream=%d", w.Code, calls.Load())
	}
}

func TestManagementBridgeAcceptsReportWithSeparateImageBudget(t *testing.T) {
	n := report.MaxDiagnosticReportBytes + 1
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, strings.Repeat("x", n))
	}))
	defer upstream.Close()
	bridge, err := newDiagnosisManagementBridge(t.Context(), upstream.URL, bridgeTestAuth, bridgeTestAllowed)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/api/report/diagnostic?task=42&audience=internal", nil)
	request.Header.Set("Authorization", "Bearer synthetic-bridge")
	response := httptest.NewRecorder()
	bridge.ServeHTTP(response, request)
	if response.Code != 200 || response.Body.Len() != n {
		t.Fatal("캡처 별도 예산의 보고서를 브리지가 거부함", response.Code, response.Body.Len())
	}
}
