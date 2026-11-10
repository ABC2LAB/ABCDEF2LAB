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
		return e2runner.Result{ToolOutput: "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\n\r\n" + body, Trusted: e2runner.TrustedResult{Final: true, ProcessDrained: true, ProxyDrained: true, Call: e2proxy.CallResult{AuditComplete: true, Counts: e2proxy.Counts{ForwardAttempted: 1}}}}, nil
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
func TestAutomaticModeDoesNotGrantStateChangesOrExcludedPaths(t *testing.T) {
	c, _ := flowFixture(t, Auto)
	syntheticCrawl(t, c)
	s, _ := c.Snapshot()
	if s.Stage != "diagnosing" {
		t.Fatal(s.Stage)
	}
	for _, view := range []e2proxy.RequestView{{Method: "POST", URL: "https://owned.test/read/cart/add"}, {Method: "GET", URL: "https://owned.test/read/logout"}, {Method: "GET", URL: "https://owned.test/read/excluded"}, {Method: "GET", URL: "https://other.test/read"}, {Method: "GET", URL: "https://owned.test/read/newly-found"}} {
		if c.Before(t.Context(), view) == nil {
			t.Fatal("automatic scope widened", view)
		}
	}
	if c.Before(t.Context(), e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read/item"}) != nil {
		t.Fatal("approved discovered read denied")
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
