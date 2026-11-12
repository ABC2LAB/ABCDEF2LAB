package server

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/exploration"
	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
)

type explorationEvents struct {
	mu     sync.Mutex
	events []string
}

func (e *explorationEvents) add(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, s)
}
func (e *explorationEvents) read() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string{}, e.events...)
}

type explorationFixtureBoundary struct {
	lease *explorationFixtureLease
	opens atomic.Int32
}

func (b *explorationFixtureBoundary) Open(_ context.Context, binding exploration.Binding) (exploration.Lease, error) {
	b.opens.Add(1)
	if binding != b.lease.state.Binding {
		return nil, exploration.ErrBinding
	}
	b.lease.events.add("boundary:open")
	return b.lease, nil
}

type explorationFixtureLease struct {
	closeReply func(exploration.State) exploration.State
	mu         sync.Mutex
	state      exploration.State
	err        error
	closeErr   error
	closes     int
	events     *explorationEvents
}

func (l *explorationFixtureLease) Status(context.Context) (exploration.State, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state, l.err
}
func (l *explorationFixtureLease) Close(_ context.Context, reason exploration.Reason) (exploration.State, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closes++
	l.events.add("boundary:close")
	l.state.Active = false
	l.state.ClosedReason = reason
	l.state.ActiveConnections = 0
	if l.closeReply != nil {
		return l.closeReply(l.state), l.closeErr
	}
	return l.state, l.closeErr
}
func (l *explorationFixtureLease) stop(reason exploration.Reason) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.state.Active = false
	l.state.ClosedReason = reason
}
func (l *explorationFixtureLease) closed() int { l.mu.Lock(); defer l.mu.Unlock(); return l.closes }

type explorationFixtureBrowser struct {
	info     ExplorationBrowserInfo
	events   *explorationEvents
	closes   atomic.Int32
	closeErr error
}

func (b *explorationFixtureBrowser) Info() ExplorationBrowserInfo { return b.info }
func (b *explorationFixtureBrowser) Close(context.Context) error {
	b.closes.Add(1)
	b.events.add("browser:close")
	return b.closeErr
}

type explorationFixtureProvider struct {
	mu        sync.Mutex
	requests  []llm.CompletionRequest
	finalText string
	failure   error
	calls     atomic.Int32
	invalid   atomic.Bool
	block     bool
	started   chan struct{}
}

func (p *explorationFixtureProvider) Complete(ctx context.Context, r llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.mu.Lock()
	p.requests = append(p.requests, r)
	p.mu.Unlock()
	n := p.calls.Add(1)
	if p.failure != nil {
		return llm.Message{}, "", llm.Usage{}, p.failure
	}
	for _, tool := range r.Tools {
		if !strings.HasPrefix(tool.Name, "mcp__explore_browser__") && tool.Name != "report_discovery" {
			p.invalid.Store(true)
		}
	}
	if p.block {
		close(p.started)
		<-ctx.Done()
		return llm.Message{}, "", llm.Usage{}, ctx.Err()
	}
	name, input := "mcp__explore_browser__browser_navigate", `{"url":"https://owned.test/"}`
	if n == 2 {
		name, input = "report_discovery", `{"discoveries":[{"method":"GET","path":"/read/api"}]}`
	}
	if n <= 2 {
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "owned-fixture-call", Name: name, Input: json.RawMessage(input)}}}, "tool_use", llm.Usage{}, nil
	}
	text := p.finalText
	if text == "" {
		text = "탐색 완료"
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(text)}}, "end_turn", llm.Usage{}, nil
}
func (p *explorationFixtureProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{}, errors.New("fixture stream unused"))
	}
}

type explorationFixtureReporter struct{ count int }

func (r *explorationFixtureReporter) ReportStatic(_ context.Context, d []diagnosisflow.Discovery) (int, error) {
	r.count += len(d)
	return len(d), nil
}

type explorationMCPFixture struct {
	server       *httptest.Server
	requests     atomic.Int32
	deleted      atomic.Int32
	headersOK    atomic.Bool
	navigate     atomic.Int32
	events       *explorationEvents
	catalog      []string
	closeFail    bool
	deleteStatus int
	entered      chan struct{}
	cancelled    chan struct{}
}

func newExplorationMCPFixture(t *testing.T, events *explorationEvents, catalog []string) *explorationMCPFixture {
	t.Helper()
	f := &explorationMCPFixture{events: events, catalog: catalog}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		if r.Method == http.MethodDelete {
			f.deleted.Add(1)
			f.headersOK.Store(r.Header.Get("Mcp-Session-Id") == "fixture-session" && r.Header.Get("MCP-Protocol-Version") == "2025-06-18")
			f.events.add("mcp:delete")
			if f.deleteStatus != 0 {
				w.WriteHeader(f.deleteStatus)
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
			return
		}
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			w.WriteHeader(400)
			return
		}
		var result any = map[string]any{}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "fixture-session")
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "owned fixture", "version": "1"}}
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/list":
			tools := []any{}
			for _, name := range f.catalog {
				tools = append(tools, map[string]any{"name": name, "description": "Owned fixture tool", "inputSchema": map[string]any{"type": "object"}})
			}
			result = map[string]any{"tools": tools}
		case "tools/call":
			f.events.add("mcp:" + req.Params.Name)
			if req.Params.Name == "browser_navigate" {
				f.navigate.Add(1)
				if f.entered != nil {
					close(f.entered)
					<-r.Context().Done()
					close(f.cancelled)
					return
				}
			}
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "fixture-private-marker"}}, "isError": f.closeFail && req.Params.Name == "browser_close"}
		default:
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Cleanup(f.server.Close)
	return f
}
func explorationSessionFixture(t *testing.T, catalog []string) (ExplorationSessionOptions, *explorationFixtureBoundary, *explorationFixtureBrowser, *explorationMCPFixture, *explorationFixtureProvider) {
	t.Helper()
	binding := exploration.Binding{SessionRef: "session-owned", RunRef: "run-owned", JobID: "job-owned", TaskID: "7", TargetRef: "target-owned", TargetRevision: 1, WriteMode: exploration.ReadOnly, CommMode: exploration.FullLocal, Limits: exploration.Limits{TimeBudgetSeconds: 10, MaxRequests: 10, MaxConcurrency: 1, MaxResponseBytes: 4096}}
	events := &explorationEvents{}
	b := &explorationFixtureBoundary{lease: &explorationFixtureLease{state: exploration.State{Binding: binding, Active: true}, events: events}}
	f := newExplorationMCPFixture(t, events, catalog)
	browser := &explorationFixtureBrowser{info: ExplorationBrowserInfo{Binding: binding, PackageVersion: exploration.MCPPackageVersion, MCP: db.MCPServer{Name: exploration.MCPServerName, Transport: "http", URL: f.server.URL + "/mcp"}}, events: events}
	provider := &explorationFixtureProvider{}
	worker := agent.NewWorker(provider, "owned", t.TempDir(), nil, 0, 10)
	worker.SetNonStreaming(func() bool { return true })
	return ExplorationSessionOptions{Worker: worker, Binding: binding, Boundary: b, StartBrowser: func(context.Context, exploration.Binding) (ExplorationBrowserLease, error) {
		events.add("browser:start")
		return browser, nil
	}, Redact: func(raw []byte) []byte {
		return []byte(strings.ReplaceAll(string(raw), "fixture-private-marker", "[REDACTED]"))
	}, CloseTimeout: time.Second, StatusInterval: 5 * time.Millisecond}, b, browser, f, provider
}
func assertExplorationClosed(t *testing.T, s *ExplorationSession, b *explorationFixtureBoundary, browser *explorationFixtureBrowser, f *explorationMCPFixture, reason exploration.Reason) {
	t.Helper()
	if err := s.Close(t.Context(), reason); err != nil {
		t.Fatal("close failed", err)
	}
	if b.lease.closed() != 1 || browser.closes.Load() != 1 || f.deleted.Load() != 1 || !f.headersOK.Load() {
		t.Fatal("lease or MCP cleanup missing")
	}
	if s.Check(t.Context()) == nil || s.Context().Err() == nil {
		t.Fatal("closed session remained usable")
	}
	events := b.lease.events.read()
	tail := events[len(events)-4:]
	if !reflect.DeepEqual(tail, []string{"mcp:browser_close", "mcp:delete", "browser:close", "boundary:close"}) {
		t.Fatal("cleanup order changed", tail)
	}
}
func TestExplorationSessionRunsActualHTTPMCPWithOriginalWorkerAndClosesEverything(t *testing.T) {
	catalog := append(exploration.BrowserToolNames(), "browser_run_code_unsafe", "browser_file_upload", "browser_network_request")
	o, b, browser, f, p := explorationSessionFixture(t, catalog)
	s, err := OpenExplorationSession(t.Context(), o)
	if err != nil {
		t.Fatal("open failed", err)
	}
	reporter := &explorationFixtureReporter{}
	result, err := s.Run(t.Context(), o.Worker, agent.ExplorationRunOptions{TargetURL: "https://owned.test/", Reporter: reporter, MaxTurns: 5, MaxDuration: time.Second}, nil)
	if err != nil || f.navigate.Load() != 1 || p.calls.Load() != 3 || p.invalid.Load() || reporter.count != 1 || len(result.Tools) != 22 {
		t.Fatal("worker/MCP execution did not connect", err)
	}
	assertExplorationClosed(t, s, b, browser, f, exploration.Completed)
	calls := p.calls.Load()
	if _, err := s.Run(t.Context(), o.Worker, agent.ExplorationRunOptions{}, nil); err == nil || p.calls.Load() != calls {
		t.Fatal("session replay accepted")
	}
}
func TestExplorationSessionRequiresHostCapabilitiesBeforeBoundaryOrBrowserIO(t *testing.T) {
	o, b, _, f, _ := explorationSessionFixture(t, exploration.BrowserToolNames())
	for _, change := range []func(*ExplorationSessionOptions){func(o *ExplorationSessionOptions) { o.Worker = nil }, func(o *ExplorationSessionOptions) { o.Worker = &agent.Worker{} }, func(o *ExplorationSessionOptions) { o.Boundary = nil }, func(o *ExplorationSessionOptions) { o.StartBrowser = nil }, func(o *ExplorationSessionOptions) { o.Redact = nil }, func(o *ExplorationSessionOptions) { o.Binding.TargetRevision = 0 }, func(o *ExplorationSessionOptions) { o.CloseTimeout = 0 }} {
		copy := o
		change(&copy)
		if _, err := OpenExplorationSession(t.Context(), copy); err == nil {
			t.Fatal("incomplete host capabilities accepted")
		}
	}
	if b.opens.Load() != 0 || f.requests.Load() != 0 {
		t.Fatal("network setup preceded prerequisites")
	}
}
func TestExplorationSessionRejectsCrossBindingAndUnsafeControlMetadataBeforeMCPIO(t *testing.T) {
	for _, change := range []func(*ExplorationBrowserInfo){func(i *ExplorationBrowserInfo) { i.Binding.RunRef = "foreign" }, func(i *ExplorationBrowserInfo) { i.Binding.TargetRevision++ }, func(i *ExplorationBrowserInfo) { i.PackageVersion = "unfixed" }, func(i *ExplorationBrowserInfo) { i.MCP.Name = "browser" }, func(i *ExplorationBrowserInfo) { i.MCP.Transport = "stdio" }, func(i *ExplorationBrowserInfo) { i.MCP.Command = "node" }, func(i *ExplorationBrowserInfo) { i.MCP.Args = json.RawMessage(`broken`) }, func(i *ExplorationBrowserInfo) { i.MCP.Env = json.RawMessage(`broken`) }, func(i *ExplorationBrowserInfo) { i.MCP.Env = json.RawMessage(`{"Authorization":"seeded"}`) }, func(i *ExplorationBrowserInfo) { i.MCP.Insecure = true }, func(i *ExplorationBrowserInfo) { i.MCP.URL = "http://user:password@127.0.0.1:9/mcp" }, func(i *ExplorationBrowserInfo) { i.MCP.URL += "?key=seeded" }} {
		o, b, browser, f, _ := explorationSessionFixture(t, exploration.BrowserToolNames())
		change(&browser.info)
		if _, err := OpenExplorationSession(t.Context(), o); err == nil || f.requests.Load() != 0 || browser.closes.Load() != 1 || b.lease.closed() != 1 {
			t.Fatal("unsafe control metadata was not fenced and cleaned")
		}
	}
}
func TestExplorationSessionRejectsMissingOrDuplicateBrowserCatalogAndClosesLeases(t *testing.T) {
	for _, catalog := range [][]string{exploration.BrowserToolNames()[:1], append(exploration.BrowserToolNames(), "browser_snapshot")} {
		o, b, browser, f, _ := explorationSessionFixture(t, catalog)
		if _, err := OpenExplorationSession(t.Context(), o); err == nil || browser.closes.Load() != 1 || b.lease.closed() != 1 || f.navigate.Load() != 0 || f.deleted.Load() != 1 {
			t.Fatal("bad MCP catalog accepted or leaked")
		}
	}
}
func TestExplorationSessionBoundaryRevocationCancelsWorkerAndCleansUp(t *testing.T) {
	o, b, browser, f, p := explorationSessionFixture(t, exploration.BrowserToolNames())
	p.block = true
	p.started = make(chan struct{})
	s, err := OpenExplorationSession(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = s.Run(t.Context(), o.Worker, agent.ExplorationRunOptions{TargetURL: "https://owned.test/", Reporter: &explorationFixtureReporter{}, MaxTurns: 5, MaxDuration: time.Second}, nil)
	}()
	select {
	case <-p.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	b.lease.stop(exploration.TargetEnded)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("revoked session did not cancel worker")
	}
	assertExplorationClosed(t, s, b, browser, f, exploration.TargetEnded)
	if s.ClosedReason() != exploration.TargetEnded || f.navigate.Load() != 0 {
		t.Fatal("revocation reason or no-new-call fence lost")
	}
}
func TestExplorationSessionCancellationInterruptsInFlightHTTPAndCleanupSurvivesErrors(t *testing.T) {
	o, b, browser, f, _ := explorationSessionFixture(t, exploration.BrowserToolNames())
	f.entered = make(chan struct{})
	f.cancelled = make(chan struct{})
	f.closeFail = true
	browser.closeErr = errors.New("fixture cleanup failure")
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	s, err := OpenExplorationSession(parent, o)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = s.Run(parent, o.Worker, agent.ExplorationRunOptions{TargetURL: "https://owned.test/", Reporter: &explorationFixtureReporter{}, MaxTurns: 5, MaxDuration: time.Second}, nil)
	}()
	select {
	case <-f.entered:
	case <-time.After(time.Second):
		t.Fatal("HTTP tool did not start")
	}
	cancel()
	select {
	case <-f.cancelled:
	case <-time.After(time.Second):
		t.Fatal("in-flight HTTP not cancelled")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup stalled")
	}
	if !errors.Is(s.Close(t.Context(), exploration.Cancelled), exploration.ErrUnavailable) || b.lease.closed() != 1 || browser.closes.Load() != 1 || f.deleted.Load() != 1 || s.Check(t.Context()) == nil {
		t.Fatal("cleanup error skipped later leases or reopened session")
	}
}
func TestExplorationCatalogStaysEqualToServerPlaywrightPolicy(t *testing.T) {
	names := exploration.BrowserToolNames()
	if len(names) != len(playwrightMCPAllowedTools) {
		t.Fatal("catalog drift")
	}
	for _, name := range names {
		if !playwrightMCPAllowedTools[name] {
			t.Fatal("catalog drift", name)
		}
	}
}
func TestExplorationWorkerHelperReusesAuthoritativeResolvedPointerWithoutFallback(t *testing.T) {
	p := &explorationFixtureProvider{}
	worker := agent.NewWorker(p, "owned", t.TempDir(), nil, 0, 10)
	e := NewEngine(nil)
	s := &Server{engine: e}
	task := &Task{ID: "7"}
	if _, err := s.ExplorationWorkerFor(task); !errors.Is(err, exploration.ErrUnavailable) {
		t.Fatal("unresolved worker fallback")
	}
	e.SetAuthoritativeAgentResolver(func(got *Task) (*agent.Planner, *agent.Worker) {
		if got != task {
			t.Fatal("task changed")
		}
		return &agent.Planner{}, worker
	})
	got, err := s.ExplorationWorkerFor(task)
	if err != nil || got != worker || p.calls.Load() != 0 {
		t.Fatal("helper recreated provider or called model")
	}
	e.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) { return nil, nil })
	if _, err := s.ExplorationWorkerFor(task); !errors.Is(err, exploration.ErrUnavailable) {
		t.Fatal("exhausted authoritative route fell back")
	}
}

func TestExplorationSessionBoundaryStatusFailureAndDeadlineCloseAllLeases(t *testing.T) {
	for _, reason := range []exploration.Reason{exploration.BoundaryUnavailable, exploration.CapExhausted} {
		o, b, browser, f, _ := explorationSessionFixture(t, exploration.BrowserToolNames())
		parent := t.Context()
		cancel := func() {}
		if reason == exploration.CapExhausted {
			parent, cancel = context.WithTimeout(parent, 30*time.Millisecond)
		}
		s, err := OpenExplorationSession(parent, o)
		if err != nil {
			cancel()
			t.Fatal("open failed", err)
		}
		if reason == exploration.BoundaryUnavailable {
			b.lease.mu.Lock()
			b.lease.err = errors.New("fixture status unavailable")
			b.lease.mu.Unlock()
		}
		select {
		case <-s.Context().Done():
		case <-time.After(2 * time.Second):
			cancel()
			t.Fatal("boundary fault did not fence session")
		}
		assertExplorationClosed(t, s, b, browser, f, reason)
		if s.ClosedReason() != reason {
			t.Fatal("boundary fault reason lost")
		}
		cancel()
	}
}

func TestExplorationSessionFinalizeRunsAfterBrowserCloseBeforeLeaseCleanup(t *testing.T) {
	o, b, browser, f, p := explorationSessionFixture(t, exploration.BrowserToolNames())
	var s *ExplorationSession
	var finalized atomic.Int32
	o.Finalize = func(ctx context.Context, binding exploration.Binding) error {
		finalized.Add(1)
		if binding != o.Binding || ctx.Err() != nil || s.Check(ctx) == nil || s.Context().Err() == nil {
			t.Error("finalization did not receive closed tool admission and live private context")
		}
		if b.lease.closed() != 0 || browser.closes.Load() != 0 || f.deleted.Load() != 0 {
			t.Error("private observation resources closed before finalization")
		}
		events := b.lease.events.read()
		if events[len(events)-1] != "mcp:browser_close" {
			t.Error("browser remained active during finalization")
		}
		calls := p.calls.Load()
		if _, err := s.Run(ctx, o.Worker, agent.ExplorationRunOptions{}, nil); err == nil || p.calls.Load() != calls {
			t.Error("finalization admitted a new worker")
		}
		b.lease.events.add("host:finalize")
		return nil
	}
	var err error
	s, err = OpenExplorationSession(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Run(t.Context(), o.Worker, agent.ExplorationRunOptions{TargetURL: "https://owned.test/", Reporter: &explorationFixtureReporter{}, MaxTurns: 5, MaxDuration: time.Second}, nil)
	if err != nil || finalized.Load() != 1 {
		t.Fatal("finalization did not complete once", err)
	}
	if err := s.Close(t.Context(), exploration.Completed); err != nil || finalized.Load() != 1 {
		t.Fatal("finalization replayed")
	}
	events := b.lease.events.read()
	tail := events[len(events)-5:]
	if !reflect.DeepEqual(tail, []string{"mcp:browser_close", "host:finalize", "mcp:delete", "browser:close", "boundary:close"}) {
		t.Fatal("finalization order changed", tail)
	}
}
func TestExplorationSessionFinalizePreservesErrorsAndRunsOnCancellationCapsAndModelFailure(t *testing.T) {
	for _, mode := range []string{"model-error", "cancelled", "cap"} {
		t.Run(mode, func(t *testing.T) {
			o, b, browser, f, p := explorationSessionFixture(t, exploration.BrowserToolNames())
			var finalized atomic.Int32
			o.Finalize = func(ctx context.Context, binding exploration.Binding) error {
				finalized.Add(1)
				if ctx.Err() != nil || binding != o.Binding {
					t.Error("private finalization inherited cancellation")
				}
				return errors.New("fixture-private-marker")
			}
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "cap" {
				var stop context.CancelFunc
				parent, stop = context.WithTimeout(parent, 30*time.Millisecond)
				defer stop()
			}
			if mode == "model-error" {
				p.failure = errors.New("fixture model failure")
			} else {
				p.block = true
				p.started = make(chan struct{})
			}
			s, err := OpenExplorationSession(parent, o)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := s.Run(parent, o.Worker, agent.ExplorationRunOptions{TargetURL: "https://owned.test/", Reporter: &explorationFixtureReporter{}, MaxTurns: 5, MaxDuration: time.Second}, nil)
				done <- err
			}()
			if mode == "cancelled" {
				select {
				case <-p.started:
					cancel()
				case <-time.After(time.Second):
					t.Fatal("worker did not start")
				}
			}
			select {
			case err := <-done:
				if !errors.Is(err, exploration.ErrUnavailable) || strings.Contains(err.Error(), "fixture-private-marker") {
					t.Fatal("finalization failure hidden or secret exposed")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("finalization stalled")
			}
			if finalized.Load() != 1 || b.lease.closed() != 1 || browser.closes.Load() != 1 || f.deleted.Load() != 1 {
				t.Fatal("failed run did not finalize and clean up once")
			}
		})
	}
}
func TestExplorationSessionFinalizeDeadlineDoesNotSkipRemainingCleanup(t *testing.T) {
	o, b, browser, f, _ := explorationSessionFixture(t, exploration.BrowserToolNames())
	o.CloseTimeout = 200 * time.Millisecond
	var finalized atomic.Int32
	o.Finalize = func(ctx context.Context, _ exploration.Binding) error { finalized.Add(1); <-ctx.Done(); return nil }
	s, err := OpenExplorationSession(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err = s.Close(t.Context(), exploration.Cancelled)
	if !errors.Is(err, exploration.ErrUnavailable) || time.Since(started) > time.Second || finalized.Load() != 1 || b.lease.closed() != 1 || browser.closes.Load() != 1 || f.deleted.Load() != 1 {
		t.Fatal("finalization deadline was hidden or skipped resource cleanup")
	}
}

func TestExplorationSessionHTTPRedactsModelRequestsAndActivity(t *testing.T) {
	o, b, browser, f, p := explorationSessionFixture(t, exploration.BrowserToolNames())
	p.finalText = "fixture-private-marker 탐색 완료"
	session, err := OpenExplorationSession(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	var activity []db.Activity
	_, err = session.Run(t.Context(), o.Worker, agent.ExplorationRunOptions{TargetURL: "https://owned.test/", Reporter: &explorationFixtureReporter{}, MaxTurns: 5, MaxDuration: time.Second}, func(a db.Activity) { activity = append(activity, a) })
	if err != nil || p.calls.Load() != 3 || f.navigate.Load() != 1 {
		t.Fatal("actual HTTP MCP Worker flow did not run", err)
	}
	p.mu.Lock()
	requests := append([]llm.CompletionRequest{}, p.requests...)
	p.mu.Unlock()
	redacted := false
	for _, request := range requests {
		raw, _ := json.Marshal(request)
		if strings.Contains(string(raw), "fixture-private-marker") {
			t.Fatal("host MCP marker reached model request")
		}
		redacted = redacted || strings.Contains(string(raw), "[REDACTED]")
	}
	if !redacted {
		t.Fatal("model request did not contain redacted actual MCP result")
	}
	for _, a := range activity {
		if strings.Contains(a.Summary+a.Detail+string(a.Metadata), "fixture-private-marker") {
			t.Fatal("host marker reached persisted activity")
		}
	}
	assertExplorationClosed(t, session, b, browser, f, exploration.Completed)
}
func TestExplorationSessionRunFailureDoesNotCloseAsCompleted(t *testing.T) {
	for _, mode := range []string{"model-failure", "rejected-options"} {
		t.Run(mode, func(t *testing.T) {
			o, b, browser, f, p := explorationSessionFixture(t, exploration.BrowserToolNames())
			target := "https://owned.test/"
			want := exploration.ModelError
			if mode == "model-failure" {
				p.failure = errors.New("fixture-private-marker")
			} else {
				target = "ftp://owned.test/"
				want = exploration.RuntimeUnavailable
			}
			session, err := OpenExplorationSession(t.Context(), o)
			if err != nil {
				t.Fatal(err)
			}
			_, err = session.Run(t.Context(), o.Worker, agent.ExplorationRunOptions{TargetURL: target, Reporter: &explorationFixtureReporter{}, MaxTurns: 5, MaxDuration: time.Second}, nil)
			if err == nil || session.ClosedReason() != want {
				t.Fatal("failed or rejected run recorded as completed")
			}
			if mode == "rejected-options" && p.calls.Load() != 0 {
				t.Fatal("rejected options called provider")
			}
			assertExplorationClosed(t, session, b, browser, f, want)
		})
	}
}
func TestExplorationSessionCleanupReportsOnlySafeStageAndKind(t *testing.T) {
	for _, stage := range []string{"browser_close", "finalize", "mcp_delete", "browser_lease", "boundary_close"} {
		t.Run(stage, func(t *testing.T) {
			o, b, browser, f, _ := explorationSessionFixture(t, exploration.BrowserToolNames())
			switch stage {
			case "browser_close":
				f.closeFail = true
			case "finalize":
				o.Finalize = func(context.Context, exploration.Binding) error { return errors.New("fixture-private-marker") }
			case "mcp_delete":
				f.deleteStatus = 503
			case "browser_lease":
				browser.closeErr = errors.New("fixture-private-marker")
			case "boundary_close":
				b.lease.closeErr = errors.New("fixture-private-marker")
			}
			session, err := OpenExplorationSession(t.Context(), o)
			if err != nil {
				t.Fatal(err)
			}
			err = session.Close(t.Context(), exploration.Cancelled)
			var failure exploration.Failure
			if !errors.Is(err, exploration.ErrUnavailable) || !errors.As(err, &failure) || failure.Stage != stage || failure.Kind == "" || strings.Contains(err.Error(), "fixture-private-marker") {
				t.Fatal("cleanup stage lost or raw detail leaked")
			}
			if b.lease.closed() != 1 || browser.closes.Load() != 1 || f.deleted.Load() != 1 {
				t.Fatal("cleanup error skipped later stages")
			}
		})
	}
}
func TestExplorationSessionRejectsDifferentWorkerWithoutSpendingProvider(t *testing.T) {
	o, _, _, _, p := explorationSessionFixture(t, exploration.BrowserToolNames())
	session, err := OpenExplorationSession(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(context.Background(), exploration.Cancelled)
	other := agent.NewWorker(&explorationFixtureProvider{}, "foreign", t.TempDir(), nil, 0, 10)
	if _, err := session.Run(t.Context(), other, agent.ExplorationRunOptions{}, nil); err == nil || p.calls.Load() != 0 {
		t.Fatal("session used a foreign budget/provider worker")
	}
}

func TestExplorationSessionCheckAndWatchValidateExactStateBinding(t *testing.T) {
	t.Run("check", func(t *testing.T) {
		o, b, _, _, _ := explorationSessionFixture(t, exploration.BrowserToolNames())
		o.StatusInterval = time.Second
		session, err := OpenExplorationSession(t.Context(), o)
		if err != nil {
			t.Fatal(err)
		}
		b.lease.mu.Lock()
		b.lease.state.Binding.TargetRevision++
		b.lease.mu.Unlock()
		if session.Check(t.Context()) == nil {
			t.Fatal("changed authority accepted by Check")
		}
		b.lease.mu.Lock()
		b.lease.state.Binding = o.Binding
		b.lease.mu.Unlock()
		if err := session.Close(t.Context(), exploration.Cancelled); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("watch", func(t *testing.T) {
		o, b, browser, _, _ := explorationSessionFixture(t, exploration.BrowserToolNames())
		session, err := OpenExplorationSession(t.Context(), o)
		if err != nil {
			t.Fatal(err)
		}
		b.lease.mu.Lock()
		b.lease.state.Binding.TargetRevision++
		b.lease.mu.Unlock()
		select {
		case <-session.Context().Done():
		case <-time.After(250 * time.Millisecond):
			t.Fatal("changed authority accepted by watch")
		}
		if err := session.Close(t.Context(), exploration.Cancelled); !errors.Is(err, exploration.ErrUnavailable) || browser.closes.Load() != 1 || session.ClosedReason() != exploration.BoundaryUnavailable {
			t.Fatal("invalid status did not close safely")
		}
	})
}
func TestExplorationSessionRejectsActiveBoundaryCloseReply(t *testing.T) {
	o, b, browser, _, _ := explorationSessionFixture(t, exploration.BrowserToolNames())
	b.lease.closeReply = func(state exploration.State) exploration.State {
		state.Active = true
		state.ClosedReason = ""
		return state
	}
	session, err := OpenExplorationSession(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	err = session.Close(t.Context(), exploration.Cancelled)
	var failure exploration.Failure
	if !errors.As(err, &failure) || failure.Stage != "boundary_close" || failure.Kind != "binding_invalid" || browser.closes.Load() != 1 {
		t.Fatal("active boundary close was reported as successful")
	}
}

func TestExplorationSessionRejectsGenericDirectCapabilitiesBeforeAnyIO(t *testing.T) {
	for _, name := range []string{"Bash", "Skill"} {
		o, b, _, f, p := explorationSessionFixture(t, exploration.BrowserToolNames())
		tool := actool.Build(actool.Spec{Name: name})
		if name == "Bash" {
			o.DirectBash = tool
		} else {
			o.APIRecon = tool
		}
		if _, err := OpenExplorationSession(t.Context(), o); !errors.Is(err, exploration.ErrUnavailable) || b.opens.Load() != 0 || f.requests.Load() != 0 || p.calls.Load() != 0 {
			t.Fatal("generic direct capability reached boundary/browser/model")
		}
	}
}
