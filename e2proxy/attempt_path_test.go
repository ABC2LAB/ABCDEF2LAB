package e2proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

type observedAttempt struct {
	receipt Receipt
	path    string
}

type attemptRecorder struct {
	mu   sync.Mutex
	rows []observedAttempt
}

func (a *attemptRecorder) observe(r Receipt, path string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rows = append(a.rows, observedAttempt{r, path})
}

func (a *attemptRecorder) all() []observedAttempt {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]observedAttempt(nil), a.rows...)
}

// 시도마다 한 번, 감사 기록이 끝난 최종 영수증과 계측기와 같은 경로 표현으로 부른다.
func TestAttemptPathObserverSeesEveryAttemptOnce(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer target.Close()
	seen := &attemptRecorder{}
	cfg := coreConfig(t, target.URL, &coreRecorder{}, 10)
	cfg.AttemptPathObserver = seen.observe
	_, call := coreOpen(t, cfg)
	do := func(path string) Receipt {
		req := coreRequest(t, http.MethodGet, target.URL+"/allowed")
		req.URL.Path, req.URL.RawPath = path, ""
		if decoded, err := url.PathUnescape(path); err == nil && decoded != path {
			req.URL.Path, req.URL.RawPath = decoded, path
		}
		response, receipt, _ := call.Do(t.Context(), req)
		if response != nil {
			response.Body.Close()
		}
		return receipt
	}
	allowed := do("/allowed/item")
	denied := do("/outside/7")
	encoded := do("/allowed/a%2Fb")
	empty := do("")
	unsupported, err := call.Reject(t.Context(), http.MethodGet, Unsupported)
	if err != nil {
		t.Fatal(err)
	}
	want := []observedAttempt{
		{allowed, "/allowed/item"},
		{denied, "/outside/7"},
		{encoded, "/allowed/a%2Fb"},
		{empty, "/"},
		{unsupported, ""},
	}
	got := seen.all()
	if len(got) != len(want) {
		t.Fatalf("observer calls %d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].path != want[i].path || got[i].receipt.Sequence != want[i].receipt.Sequence ||
			got[i].receipt.Decision != want[i].receipt.Decision || got[i].receipt.Reason != want[i].receipt.Reason ||
			!got[i].receipt.AuditComplete {
			t.Fatalf("%d: got %+v %q want %+v %q", i, got[i].receipt, got[i].path, want[i].receipt, want[i].path)
		}
	}
	if allowed.Decision != "allowed" || denied.Decision != "denied" || unsupported.Decision != "unsupported" || got[1].receipt.ForwardAttempted {
		t.Fatalf("fixture decisions: %+v %+v %+v", allowed, denied, unsupported)
	}
}

// 관찰 훅은 판단·송신·계수를 바꾸지 않는다. 훅 없이 같은 요청을 보낸 결과와 같다.
func TestAttemptPathObserverDoesNotChangeOutcomes(t *testing.T) {
	run := func(observer AttemptPathObserver) (Counts, []string) {
		var received []string
		var mu sync.Mutex
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			received = append(received, r.URL.EscapedPath())
			mu.Unlock()
		}))
		defer target.Close()
		cfg := coreConfig(t, target.URL, &coreRecorder{}, 10)
		cfg.AttemptPathObserver = observer
		e, call := coreOpen(t, cfg)
		for _, path := range []string{"/allowed", "/outside", "/allowed/x"} {
			response, _, _ := call.Do(t.Context(), coreRequest(t, http.MethodGet, target.URL+path))
			if response != nil {
				response.Body.Close()
			}
		}
		mu.Lock()
		defer mu.Unlock()
		return e.Snapshot().Counts, append([]string(nil), received...)
	}
	plainCounts, plainReceived := run(nil)
	seen := &attemptRecorder{}
	observedCounts, observedReceived := run(seen.observe)
	if plainCounts != observedCounts || len(plainReceived) != len(observedReceived) || len(seen.all()) != 3 {
		t.Fatalf("observer changed outcomes: %+v %v vs %+v %v", plainCounts, plainReceived, observedCounts, observedReceived)
	}
}
