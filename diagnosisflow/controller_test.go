package diagnosisflow

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryStore struct {
	mu      sync.Mutex
	records map[string]Record
	fail    bool
}

func (m *memoryStore) Create(_ context.Context, r Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail || m.records[r.Snapshot.JobID].Snapshot.Revision != 0 {
		return ErrUnavailable
	}
	m.records[r.Snapshot.JobID] = clone(r)
	return nil
}
func (m *memoryStore) Save(_ context.Context, old uint64, r Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return ErrUnavailable
	}
	if m.records[r.Snapshot.JobID].Snapshot.Revision != old {
		return ErrConflict
	}
	m.records[r.Snapshot.JobID] = clone(r)
	return nil
}
func (m *memoryStore) Read(_ context.Context, owner, id string) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.records[id]
	if r.Owner != owner {
		return Record{}, ErrUnavailable
	}
	return clone(r), nil
}
func flowFixture(t *testing.T, mode string) (*Controller, *memoryStore) {
	t.Helper()
	store := &memoryStore{records: map[string]Record{}}
	c, err := New(t.Context(), Binding{JobID: "prep-owned", TaskID: "1", RunRef: "run-owned", SourceDigest: strings.Repeat("a", 64), Owner: "ARTEX", Mode: mode, TargetURL: "https://owned.test/read", PathPrefixes: []string{"/read"}, ReadonlyPrefixes: []string{"/read"}, Methods: []string{"GET", "HEAD", "POST"}, Excluded: []string{"/read/excluded"}, ReversibleWrites: true, Limits: Limits{Format: Version, MaxPages: 2, MaxEndpoints: 32, MaxResponseBytes: 4096, AllowReversibleWrites: true}, Current: func() error { return nil }}, store)
	if err != nil {
		t.Fatal(err)
	}
	return c, store
}
func syntheticCrawl(t *testing.T, c *Controller) {
	t.Helper()
	calls := 0
	handled, err := c.Crawl(t.Context(), func(ctx context.Context, target string) (e2runner.Result, error) {
		calls++
		view := e2proxy.RequestView{Method: "GET", URL: target}
		if c.Before(ctx, view) != nil {
			t.Fatal("crawler request outside scope", target)
		}
		receipt := e2proxy.Receipt{AttemptID: statusText(calls), CallRef: "owned-call", Decision: "allowed", ForwardAttempted: true, RequestWritten: true, ResponseReceived: true, ResponseComplete: true, StatusCode: 200}
		if c.After(ctx, view, receipt) != nil {
			t.Fatal("persist observation")
		}
		body := `<a href="/read/item">item</a><a href="https://outside.test/path">outside</a><a href="/read/excluded">excluded</a><a href="/read/logout">logout</a><form method="post" action="/read/cart/add"></form>`
		if strings.HasSuffix(target, "/item") {
			body = `<a href="/read/item">duplicate</a>`
		}
		return e2runner.Result{ToolOutput: "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\n\r\n" + body, Trusted: e2runner.TrustedResult{Version: e2proxy.Version, Final: true, ProcessDrained: true, ProxyDrained: true, Call: e2proxy.CallResult{Final: true, AuditComplete: true, Counts: e2proxy.Counts{ForwardAttempted: 1}}}}, nil
	})
	if !handled || err != nil || calls != 2 {
		t.Fatal("bounded crawl", handled, calls, err)
	}
}
func TestManualCrawlSelectionAndExactRequestFence(t *testing.T) {
	c, store := flowFixture(t, Manual)
	syntheticCrawl(t, c)
	s, _ := c.Snapshot()
	if !s.InitialCompleted || s.Stage != "awaiting_selection" || s.Counts.Sent != 2 || s.Counts.Visited != 2 {
		t.Fatal("unobserved crawl state", s)
	}
	var post, get string
	for _, e := range s.Endpoints {
		if e.Path == "/read/cart/add" {
			post = e.ID
		}
		if e.Path == "/read/item" {
			get = e.ID
		}
	}
	if post == "" || get == "" {
		t.Fatal("actual parsed form/link missing")
	}
	if c.Before(t.Context(), e2proxy.RequestView{Method: "POST", URL: "https://owned.test/read/cart/add"}) == nil {
		t.Fatal("deep request before selection")
	}
	if _, err := c.Command(t.Context(), "ARTEX", "selection", "bad", s.Revision, []string{"invented"}); !errors.Is(err, ErrDenied) {
		t.Fatal("invented endpoint selected")
	}
	selected, err := c.Command(t.Context(), "ARTEX", "selection", "select-once", s.Revision, []string{post})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := c.Command(t.Context(), "ARTEX", "selection", "select-once", s.Revision, []string{post})
	if err != nil || replay.Revision != selected.Revision {
		t.Fatal("lost response replay duplicated selection")
	}
	if _, err = c.Command(t.Context(), "ARTEX", "selection", "select-once", s.Revision, []string{get}); !errors.Is(err, ErrConflict) {
		t.Fatal("key reused for different selection")
	}
	postView := e2proxy.RequestView{Method: "POST", URL: "https://owned.test/read/cart/add", BodyPresent: true}
	if c.Before(t.Context(), postView) != nil || c.PreapprovedRead(postView) {
		t.Fatal("state change must be selected and individually approved")
	}
	if c.Before(t.Context(), e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read/item"}) == nil {
		t.Fatal("unselected method/path admitted")
	}
	record, err := store.Read(t.Context(), "ARTEX", "prep-owned")
	if err != nil || record.Snapshot.Stage != "diagnosing" {
		t.Fatal("selection not persisted")
	}
}

// 자동 모드는 대상에 승인한 쓰기를 건별 확인 없이 보낸다(C21). 쓰기 승인이 없으면 쓰기는 고르지도 보내지도 않는다.
// 어느 쪽이든 제외 경로·위험 동작·다른 origin은 막는다. 처음 보는 경로도 승인 범위 안이면 보낸다(2026-11-13 결정).
func TestAutomaticModeSendsApprovedWritesButNotExcludedPaths(t *testing.T) {
	for _, writes := range []bool{true, false} {
		c, _ := flowFixture(t, Auto)
		c.binding.ReversibleWrites = writes
		syntheticCrawl(t, c)
		s, _ := c.Snapshot()
		if s.Stage != "diagnosing" {
			t.Fatal(s.Stage)
		}
		post := e2proxy.RequestView{Method: "POST", URL: "https://owned.test/read/cart/add", BodyPresent: true}
		if err := c.Before(t.Context(), post); (err == nil) != writes || endpointAt(c, "POST", "/read/cart/add").Selected != writes {
			t.Fatalf("writes=%v: approved write admission %v", writes, err)
		}
		if c.PreapprovedRead(post) {
			t.Fatal("write counted as a preapproved read")
		}
		for _, view := range []e2proxy.RequestView{{Method: "GET", URL: "https://owned.test/read/logout"}, {Method: "GET", URL: "https://owned.test/read/excluded"}, {Method: "GET", URL: "https://other.test/read"}, {Method: "POST", URL: "https://owned.test/read/excluded"}} {
			if c.Before(t.Context(), view) == nil {
				t.Fatal("automatic scope widened", view)
			}
		}
		if c.Before(t.Context(), e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read/item"}) != nil {
			t.Fatal("approved discovered read denied")
		}
	}
}

// 자동 모드는 크롤이 찾지 못했거나 고르지 않은 경로·메서드도 대상 승인 범위(경로 접두·메서드) 안이면 보낸다(2026-11-13
// 사용자 결정). 승인 범위 밖·제외 경로·위험 동작·결과 불명 쓰기 재전송은 그대로 막고, 쓰기 승인이 없으면 부작용 가능
// 요청은 보내지 않는다. 운영자가 고르는 수동 모드는 고른 끝점만 보낸다.
func TestAutomaticModeSendsUndiscoveredPathsWithinApprovedScope(t *testing.T) {
	for _, writes := range []bool{true, false} {
		for _, mode := range []string{Auto, Manual} {
			c, _ := flowFixture(t, mode)
			c.binding.ReversibleWrites = writes
			c.mu.Lock()
			item := c.addLocked("GET", "/read/item", "page", "html_link", "/read")
			item.Selected, item.Change = true, "attested_read"
			form := c.addLocked("GET", "/read/search", "form", "html_form", "/read")
			form.Change = "possible" // 발견했지만 고르지 않은 GET 폼
			c.record.Snapshot.Stage = "diagnosing"
			c.mu.Unlock()
			auto := mode == Auto
			for _, tc := range []struct {
				view  e2proxy.RequestView
				allow bool
			}{
				{e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read/newly-found"}, auto},
				{e2proxy.RequestView{Method: "HEAD", URL: "https://owned.test/read/item"}, auto},
				{e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read/api/Users/7?q=1"}, auto},
				{e2proxy.RequestView{Method: "POST", URL: "https://owned.test/read/newly-found", BodyPresent: true, BodyBytes: 2}, auto && writes},
				{e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read/search?q=x"}, auto && writes},
				{e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read/item"}, true},
				{e2proxy.RequestView{Method: "GET", URL: "https://owned.test/admin"}, false},
				{e2proxy.RequestView{Method: "PUT", URL: "https://owned.test/read/newly-found"}, false},
				{e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read/excluded/deeper"}, false},
				{e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read/newly-found?action=delete"}, false},
				{e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read/account/delete"}, false},
				{e2proxy.RequestView{Method: "GET", URL: "https://other.test/read/newly-found"}, false},
				{e2proxy.RequestView{Method: "POST", URL: "https://owned.test/read/big", BodyPresent: true, BodyBytes: 65537}, false},
			} {
				if err := c.Before(t.Context(), tc.view); (err == nil) != tc.allow {
					t.Fatalf("%s writes=%v %s %s: allow=%v err=%v", mode, writes, tc.view.Method, tc.view.URL, tc.allow, err)
				}
			}
			if !auto {
				continue
			}
			// 처음 보는 경로라도 결과 불명으로 끝난 쓰기는 같은 메서드·경로로 다시 보내지 않는다.
			post := e2proxy.RequestView{Method: "POST", URL: "https://owned.test/read/new-write", BodyPresent: true, BodyBytes: 2}
			if writes {
				if err := c.Before(t.Context(), post); err != nil {
					t.Fatal("approved write to an undiscovered path denied", err)
				}
				if err := c.After(t.Context(), post, e2proxy.Receipt{AttemptID: "unknown-write", CallRef: "call-0", Decision: "allowed", ForwardAttempted: true, RequestWritten: true, Reason: e2proxy.Unknown}); err != nil {
					t.Fatal(err)
				}
				if err := c.Before(t.Context(), post); !errors.Is(err, ErrDenied) {
					t.Fatal("unknown write to an undiscovered path resent", err)
				}
			}
		}
	}
}

// 자동 모드에서 처음 보는 경로라도 확인한 읽기(읽기 확인 접두 안의 GET·HEAD)로 허용한 요청은, 결과가 불명이어도
// 발견한 읽기처럼 다시 보낼 수 있다. 허용과 결과 불명 기억이 같은 분류를 쓴다(#557 이동찬 리뷰). 부작용 가능 요청
// (쓰기·고르지 않은 GET 폼)은 결과 불명 뒤 다시 보내지 않는다.
func TestAutomaticUndiscoveredReadSurvivesUnknownResult(t *testing.T) {
	for _, writes := range []bool{false, true} {
		c, _ := flowFixture(t, Auto)
		c.binding.ReversibleWrites = writes
		c.mu.Lock()
		found := c.addLocked("GET", "/read/found", "page", "html_link", "/read")
		found.Selected, found.Change = true, "attested_read"
		form := c.addLocked("GET", "/read/form", "form", "html_form", "/read")
		form.Change = "possible"
		c.record.Snapshot.Stage = "diagnosing"
		c.mu.Unlock()
		unknown := func(view e2proxy.RequestView, id string) {
			t.Helper()
			if err := c.After(t.Context(), view, e2proxy.Receipt{AttemptID: id, CallRef: "call-0", Decision: "allowed", ForwardAttempted: true, RequestWritten: true, Reason: e2proxy.Unknown}); err != nil {
				t.Fatal(err)
			}
		}
		for i, view := range []e2proxy.RequestView{{Method: "GET", URL: "https://owned.test/read/found"}, {Method: "HEAD", URL: "https://owned.test/read/found"}, {Method: "GET", URL: "https://owned.test/read/retryable"}, {Method: "HEAD", URL: "https://owned.test/read/retryable"}} {
			if err := c.Before(t.Context(), view); err != nil {
				t.Fatal("read denied", writes, view.Method, view.URL, err)
			}
			unknown(view, "read-"+statusText(i))
			if err := c.Before(t.Context(), view); err != nil {
				t.Fatalf("writes=%v %s %s: read blocked after an unknown result: %v", writes, view.Method, view.URL, err)
			}
		}
		if !writes {
			continue
		}
		for i, view := range []e2proxy.RequestView{{Method: "GET", URL: "https://owned.test/read/form?x=1"}, {Method: "POST", URL: "https://owned.test/read/new-write", BodyPresent: true, BodyBytes: 2}} {
			if err := c.Before(t.Context(), view); err != nil {
				t.Fatal("possible side effect denied before unknown", view.Method, view.URL, err)
			}
			unknown(view, "write-"+statusText(i))
			if err := c.Before(t.Context(), view); !errors.Is(err, ErrDenied) {
				t.Fatal("possible side effect resent after unknown", view.Method, view.URL, err)
			}
		}
	}
}

// 크롤 단계는 그대로 발견해서 확인한 읽기만 보낸다. 자동 모드라도 처음 보는 경로는 진단 단계 전에는 보내지 않는다.
func TestAutomaticModeCrawlStageKeepsDiscoveredReadsOnly(t *testing.T) {
	c, _ := flowFixture(t, Auto)
	if s, _ := c.Snapshot(); s.Stage != "crawling" {
		t.Fatal(s.Stage)
	}
	if err := c.Before(t.Context(), e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read/newly-found"}); !errors.Is(err, ErrDenied) {
		t.Fatal("undiscovered path sent during crawling", err)
	}
}

// 모델 지침도 모드에 맞춘다: 자동 모드는 승인 범위 안 어디든, 수동 모드는 고른 끝점만이라고 말한다.
func TestWorkflowInstructionsFollowMode(t *testing.T) {
	auto, _ := flowFixture(t, Auto)
	manual, _ := flowFixture(t, Manual)
	if a := auto.Instructions(); !strings.Contains(a, "any path and method within the approved target scope") || strings.Contains(a, "Only selected endpoints may be tested") {
		t.Fatal("automatic instructions still limit the model to selected endpoints")
	}
	if m := manual.Instructions(); !strings.Contains(m, "Only selected endpoints may be tested") || strings.Contains(m, "any path and method") {
		t.Fatal("manual instructions widened")
	}
}
func TestPauseWaitAndFailedStorageDenyNewSideEffects(t *testing.T) {
	c, store := flowFixture(t, Auto)
	syntheticCrawl(t, c)
	s, _ := c.Snapshot()
	paused, err := c.Command(t.Context(), "ARTEX", "pause", "pause-once", s.Revision, nil)
	if err != nil || !paused.Paused {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if err = c.Before(ctx, e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read/item"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("pause did not stop admission", err)
	}
	if _, err = c.Command(t.Context(), "ARTEX", "resume", "resume-once", paused.Revision, nil); err != nil {
		t.Fatal(err)
	}
	store.fail = true
	s, _ = c.Snapshot()
	if _, err = c.Command(t.Context(), "ARTEX", "pause", "pause-failed", s.Revision, nil); err == nil {
		t.Fatal("failed command storage accepted")
	}
	if c.Before(t.Context(), e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read/item"}) == nil {
		t.Fatal("storage failure did not fence request")
	}
	if c.PreapprovedRead(e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read/item"}) {
		t.Fatal("poisoned store retained automatic read exemption")
	}
}
func TestWorkflowPublicProjectionNeverReturnsCredentialPath(t *testing.T) {
	s := Project(Snapshot{Endpoints: []Endpoint{{Path: "/read/user@example.test", Request: "GET /read/user@example.test HTTP/1.1", From: "/read/user@example.test"}}})
	raw, _ := json.Marshal(s)
	if strings.Contains(string(raw), "user@example.test") {
		t.Fatal("credential-like path exposed")
	}
}

func TestGETFormCannotInheritReadExemptionFromEarlierLink(t *testing.T) {
	c, _ := flowFixture(t, Auto)
	c.mu.Lock()
	c.discoverBodyLocked([]byte(`<a href="/read/change">link</a><form method="get" action="/read/change"></form><a href="/read/change">later link</a>`), "text/html", "/read")
	c.mu.Unlock()
	s, _ := c.Snapshot()
	for _, e := range s.Endpoints {
		if e.Path == "/read/change" {
			if e.Change != "possible" || e.Kind != "form" || e.Duplicates != 2 {
				t.Fatal("GET form inherited read exemption", e)
			}
			return
		}
	}
	t.Fatal("GET form not discovered")
}

func TestHTMLFormDoesNotInventGETAndStaticMethodIsUnknown(t *testing.T) {
	c, _ := flowFixture(t, Manual)
	c.mu.Lock()
	c.discoverBodyLocked([]byte(`<form method="post" action="/api/Users"></form><script>client.get('/api/Products'); client.post('/api/Profile'); const unresolved='/api/Unresolved';</script>`), "text/html", "/read")
	c.mu.Unlock()
	s, _ := c.Snapshot()
	seen := map[string]bool{}
	for _, e := range s.Endpoints {
		seen[e.Method+" "+e.Path] = true
	}
	if seen["GET /api/Users"] || !seen["POST /api/Users"] || !seen["GET /api/Products"] || !seen["POST /api/Profile"] || !seen["UNKNOWN /api/Unresolved"] {
		t.Fatal("invented method or missed actual source", seen)
	}
	if !strings.Contains(c.Instructions(), "Workbench workflow mode: user_approval_v1") {
		t.Fatal("worker did not receive bound execution mode")
	}
}

func TestCrawlingOnlyAdmitsAttestedGETWithoutBody(t *testing.T) {
	for _, mode := range []string{Auto, Manual} {
		t.Run(mode, func(t *testing.T) {
			c, _ := flowFixture(t, mode)
			c.mu.Lock()
			c.discoverBodyLocked([]byte(`<form method="get" action="/read/form"></form><form method="post" action="/read/write"></form>`), "text/html", "/read")
			c.addLocked("HEAD", "/read/head", "api", "script_call", "/read")
			c.mu.Unlock()
			if snap, err := c.Snapshot(); err != nil || snap.Stage != "crawling" {
				t.Fatal("fixture already left crawling")
			}
			if err := c.Before(t.Context(), e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read"}); err != nil {
				t.Fatal("attested crawl GET rejected", err)
			}
			for _, tc := range []struct {
				name, method, path string
				body               bool
			}{
				{"post", "POST", "/read/write", false}, {"get_body", "GET", "/read", true}, {"get_form", "GET", "/read/form", false}, {"head", "HEAD", "/read/head", false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					c.mu.Lock()
					e := c.endpointLocked(tc.method, tc.path)
					eligible := e != nil && e.InScope && e.Change != "blocked"
					c.mu.Unlock()
					if !eligible {
						t.Fatal("another scope guard would hide the crawling restriction")
					}
					if err := c.Before(t.Context(), e2proxy.RequestView{Method: tc.method, URL: "https://owned.test" + tc.path, BodyPresent: tc.body}); !errors.Is(err, ErrDenied) {
						t.Fatal("crawling admitted request needing a separate approval", err)
					}
				})
			}
		})
	}
}

func TestAttestedReadExemptionIsPathScopedAndDoesNotValidateQuery(t *testing.T) {
	c, _ := flowFixture(t, Auto)
	syntheticCrawl(t, c)
	v := e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read/item?owned_fixture=value"}
	if err := c.Before(t.Context(), v); err != nil || !c.PreapprovedRead(v) {
		t.Fatal("path-scoped query contract changed", err)
	}
}

// Deliberately bypass upstream selection/classification to exercise the last
// request boundary independently. Automatic and manual modes admit the same
// selected requests (C21); only the target's reversible-write grant decides
// whether a possible side effect may be sent.
func TestAutomaticRequestBoundaryMatchesManual(t *testing.T) {
	for _, tc := range []struct {
		name, method, change string
		body                 bool
	}{
		{"selected_POST", "POST", "possible", true},
		{"POST_misclassified_as_read", "POST", "attested_read", false},
		{"GET_with_body", "GET", "attested_read", true},
		{"GET_form", "GET", "possible", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, writes := range []bool{true, false} {
				for _, mode := range []string{Auto, Manual} {
					c, _ := flowFixture(t, mode)
					c.binding.ReversibleWrites = writes
					c.mu.Lock()
					e := c.addLocked(tc.method, "/read/selected", "form", "html_form", "/read")
					e.Selected = true
					e.Change = tc.change
					c.record.Snapshot.Stage = "diagnosing"
					c.mu.Unlock()
					view := e2proxy.RequestView{Method: tc.method, URL: "https://owned.test/read/selected", BodyPresent: tc.body}
					err := c.Before(t.Context(), view)
					denied := tc.change != "attested_read" && !writes
					if denied && !errors.Is(err, ErrDenied) || !denied && err != nil {
						t.Fatalf("%s writes=%v: %v", mode, writes, err)
					}
				}
			}
		})
	}
}

// 결과 불명으로 끝난 상태 변경 요청과 같은 메서드·경로는 다른 명령(본문·query가 달라도)으로도 이 회차에서 다시 보내지
// 않는다. 결과를 받은 쓰기와 읽기는 막지 않는다(CLAUDE.md "결과 불명 쓰기는 자동으로 다시 보내지 않는다", #525 리뷰).
func TestUnknownWriteIsNotResentWithAnotherCommand(t *testing.T) {
	for _, mode := range []string{Auto, Manual} {
		c, _ := flowFixture(t, mode)
		c.mu.Lock()
		for _, path := range []string{"/read/cart/add", "/read/cart/other"} {
			e := c.addLocked("POST", path, "form", "html_form", "/read")
			e.Selected, e.Change = true, "possible"
		}
		read := c.addLocked("GET", "/read/cart/add", "page", "html_link", "/read")
		read.Selected, read.Change = true, "attested_read"
		form := c.addLocked("GET", "/read/submit", "form", "html_form", "/read")
		form.Selected, form.Change = true, "possible"
		c.record.Snapshot.Stage = "diagnosing"
		c.mu.Unlock()
		// 상태를 바꿀 수 있는 GET 폼도 결과 불명 뒤 query만 바꿔 다시 보내지 않는다(#525 이동찬 리뷰). 비변경을 확인한 읽기는
		// 결과 불명이어도 다시 보낸다.
		getForm := e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read/submit?x=1"}
		if err := c.Before(t.Context(), getForm); err != nil {
			t.Fatal(mode, "GET form denied", err)
		}
		if err := c.After(t.Context(), getForm, e2proxy.Receipt{AttemptID: "attempt-form", CallRef: "call-0", Decision: "allowed", ForwardAttempted: true, RequestWritten: true, Reason: e2proxy.Unknown}); err != nil {
			t.Fatal(err)
		}
		if err := c.Before(t.Context(), e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read/submit?x=2"}); !errors.Is(err, ErrDenied) {
			t.Fatalf("%s: GET form with an unknown result resent: %v", mode, err)
		}
		readView := e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read/cart/add"}
		if err := c.After(t.Context(), readView, e2proxy.Receipt{AttemptID: "attempt-read", CallRef: "call-0", Decision: "allowed", ForwardAttempted: true, RequestWritten: true, Reason: e2proxy.Unknown}); err != nil {
			t.Fatal(err)
		}
		if err := c.Before(t.Context(), readView); err != nil {
			t.Fatal(mode, "attested read blocked after an unknown result", err)
		}
		post := e2proxy.RequestView{Method: "POST", URL: "https://owned.test/read/cart/add", BodyPresent: true}
		if err := c.Before(t.Context(), post); err != nil {
			t.Fatal(mode, "first write denied", err)
		}
		unknown := e2proxy.Receipt{AttemptID: "attempt-unknown", CallRef: "call-1", Decision: "allowed", ForwardAttempted: true, RequestWritten: true, Reason: e2proxy.Unknown}
		if err := c.After(t.Context(), post, unknown); err != nil {
			t.Fatal(err)
		}
		for _, again := range []e2proxy.RequestView{post, {Method: "post", URL: "https://owned.test/read/cart/add?retry=1", BodyPresent: true}} {
			if err := c.Before(t.Context(), again); !errors.Is(err, ErrDenied) {
				t.Fatalf("%s: write with an unknown result resent: %v", mode, err)
			}
		}
		// 다른 경로의 쓰기, 같은 경로의 읽기, 결과를 받은 쓰기는 그대로 보낸다.
		other := e2proxy.RequestView{Method: "POST", URL: "https://owned.test/read/cart/other", BodyPresent: true}
		if err := c.Before(t.Context(), other); err != nil {
			t.Fatal(mode, "unrelated write denied", err)
		}
		done := e2proxy.Receipt{AttemptID: "attempt-done", CallRef: "call-2", Decision: "allowed", ForwardAttempted: true, RequestWritten: true, ResponseReceived: true, ResponseComplete: true, StatusCode: 200, Reason: e2proxy.Complete}
		if err := c.After(t.Context(), other, done); err != nil {
			t.Fatal(err)
		}
		if err := c.Before(t.Context(), other); err != nil {
			t.Fatal(mode, "completed write blocked", err)
		}
		if err := c.Before(t.Context(), e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read/cart/add"}); err != nil {
			t.Fatal(mode, "read of the same path denied", err)
		}
	}
}

// http 대상(C22)도 진단 흐름을 연다. origin은 scheme까지 같아야 한다.
func TestHTTPTargetFlowKeepsSchemeInOrigin(t *testing.T) {
	store := &memoryStore{records: map[string]Record{}}
	c, err := New(t.Context(), Binding{JobID: "prep-owned", TaskID: "1", RunRef: "run-owned", SourceDigest: strings.Repeat("a", 64), Owner: "ARTEX", Mode: Auto, TargetURL: "http://owned.test/read", PathPrefixes: []string{"/read"}, ReadonlyPrefixes: []string{"/read"}, Methods: []string{"GET", "HEAD"}, Limits: Limits{Format: Version, MaxPages: 2, MaxEndpoints: 32, MaxResponseBytes: 4096}, Current: func() error { return nil }}, store)
	if err != nil {
		t.Fatal("http target flow refused", err)
	}
	if c.Before(t.Context(), e2proxy.RequestView{Method: "GET", URL: "http://owned.test/read"}) != nil {
		t.Fatal("http start page denied")
	}
	if c.Before(t.Context(), e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read"}) == nil {
		t.Fatal("https request admitted for an http target")
	}
	if _, err := New(t.Context(), Binding{JobID: "prep-owned-2", TaskID: "1", RunRef: "run-owned", SourceDigest: strings.Repeat("a", 64), Owner: "ARTEX", Mode: Auto, TargetURL: "ftp://owned.test/read", PathPrefixes: []string{"/read"}, ReadonlyPrefixes: []string{"/read"}, Methods: []string{"GET"}, Limits: Limits{Format: Version, MaxPages: 2, MaxEndpoints: 32, MaxResponseBytes: 4096}, Current: func() error { return nil }}, store); err == nil {
		t.Fatal("ftp target accepted")
	}
}
