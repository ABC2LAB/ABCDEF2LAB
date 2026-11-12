package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"iter"
	"net/url"
	"os"
	"strconv"
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
)

type pipelineMemoryStore struct {
	mu  sync.Mutex
	row diagnosisflow.Record
}

func clonePipelineRecord(row diagnosisflow.Record) diagnosisflow.Record {
	raw, _ := json.Marshal(row)
	var out diagnosisflow.Record
	_ = json.Unmarshal(raw, &out)
	return out
}
func (s *pipelineMemoryStore) Create(_ context.Context, row diagnosisflow.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.row = clonePipelineRecord(row)
	return nil
}
func (s *pipelineMemoryStore) Save(_ context.Context, revision uint64, row diagnosisflow.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.row.Snapshot.Revision != revision {
		return diagnosisflow.ErrConflict
	}
	s.row = clonePipelineRecord(row)
	return nil
}
func (s *pipelineMemoryStore) Read(context.Context, string, string) (diagnosisflow.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clonePipelineRecord(s.row), nil
}

type pipelinePrivateReader struct {
	mu           sync.Mutex
	binding      diagnosisflow.ExplorationBinding
	rows         map[uint64]diagnosisflow.ExplorationObservation
	boundary     *explorationFixtureBoundary
	http         *explorationMCPFixture
	next         uint64
	browserAdded bool
}

func (r *pipelinePrivateReader) Session(ctx context.Context) (diagnosisflow.ExplorationBinding, error) {
	state, err := r.boundary.lease.Status(ctx)
	if err != nil || !state.Active || ctx.Err() != nil {
		return diagnosisflow.ExplorationBinding{}, diagnosisflow.ErrExplorationClosed
	}
	return r.binding, nil
}
func (r *pipelinePrivateReader) ReadObservation(_ context.Context, b diagnosisflow.ExplorationBinding, seq uint64) (diagnosisflow.ExplorationObservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[seq]
	if !ok || b != r.binding {
		return row, diagnosisflow.ErrExplorationObservation
	}
	return row, nil
}
func (r *pipelinePrivateReader) add(path, source string, body []byte) uint64 {
	r.next++
	seq := r.next
	r.rows[seq] = diagnosisflow.ExplorationObservation{Binding: r.binding, Seq: seq, At: time.Now(), Method: "GET", Path: path, Source: source, Kind: "api", Transport: "http", Status: 200, Forwarded: true, ResponseReceived: true, ResponseContentType: "text/html", ResponseSHA256: sha256.Sum256(body)}
	return seq
}
func (r *pipelinePrivateReader) fetch(ctx context.Context, target string) (diagnosisflow.ExplorationFetchResult, error) {
	if ctx.Err() != nil {
		return diagnosisflow.ExplorationFetchResult{}, ctx.Err()
	}
	u, _ := url.Parse(target)
	body := []byte(`<a href="/read/item">item</a>`)
	r.mu.Lock()
	seq := r.add(u.Path, diagnosisflow.SourceAPIReconRuntime, body)
	r.mu.Unlock()
	return diagnosisflow.ExplorationFetchResult{Seq: seq, Body: body, Complete: true}, nil
}
func (r *pipelinePrivateReader) sequences(_ context.Context, b diagnosisflow.ExplorationBinding) ([]uint64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b != r.binding {
		return nil, diagnosisflow.ErrExplorationBinding
	}
	if r.http.navigate.Load() > 0 && !r.browserAdded {
		r.add("/read/browser-api", diagnosisflow.SourceBrowserXHR, nil)
		r.browserAdded = true
	}
	seqs := []uint64{}
	for seq := range r.rows {
		seqs = append(seqs, seq)
	}
	return seqs, nil
}

type pipelineProvider struct {
	calls   atomic.Int32
	flow    *diagnosisflow.Controller
	t       *testing.T
	failure error
}

func (p *pipelineProvider) Complete(_ context.Context, _ llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	n := p.calls.Add(1)
	if p.failure != nil {
		return llm.Message{}, "", llm.Usage{}, p.failure
	}
	if n == 2 {
		snapshot, _ := p.flow.Snapshot()
		if snapshot.Counts.ExplorationRequests < 3 {
			p.t.Error("observation was not stored before next model call")
		}
	}
	if n <= 2 {
		name, input := "mcp__explore_browser__browser_navigate", `{"url":"https://owned.test/read/browser-api"}`
		if n == 2 {
			name, input = "report_discovery", `{"discoveries":[{"method":"GET","path":"/read/inferred"}]}`
		}
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "pipeline-call", Name: name, Input: json.RawMessage(input)}}}, "tool_use", llm.Usage{InputTokens: 1, OutputTokens: 1}, nil
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("탐색 완료")}}, "end_turn", llm.Usage{InputTokens: 1, OutputTokens: 1}, nil
}
func (p *pipelineProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{}, errors.New("fixture streaming disabled"))
	}
}
func pipelineFixture(t *testing.T) (*Server, *Task, ExplorationPipelineOptions, *explorationFixtureBoundary, *explorationFixtureBrowser, *explorationMCPFixture, *pipelineProvider) {
	return pipelineFixtureWithStore(t, nil, "")
}
func pipelineFixtureWithStore(t *testing.T, store diagnosisflow.Store, taskID string, runRef ...string) (*Server, *Task, ExplorationPipelineOptions, *explorationFixtureBoundary, *explorationFixtureBrowser, *explorationMCPFixture, *pipelineProvider) {
	t.Helper()
	session, b, browser, http, _ := explorationSessionFixture(t, exploration.BrowserToolNames())
	binding := session.Binding
	if taskID != "" {
		binding.TaskID = taskID
		binding.JobID = "pipeline-owned-" + taskID
		session.Binding = binding
		b.lease.state.Binding = binding
		browser.info.Binding = binding
	}
	if len(runRef) > 0 {
		binding.RunRef = runRef[0]
		session.Binding = binding
		b.lease.state.Binding = binding
		browser.info.Binding = binding
	}
	digest := strings.Repeat("a", 64)
	if store == nil {
		store = &pipelineMemoryStore{}
	}
	flow, err := diagnosisflow.New(t.Context(), diagnosisflow.Binding{TargetRef: binding.TargetRef, TargetRevision: binding.TargetRevision, JobID: binding.JobID, TaskID: binding.TaskID, RunRef: binding.RunRef, SourceDigest: digest, Owner: "ARTEX", Mode: diagnosisflow.Auto, TargetURL: "https://owned.test/read", PathPrefixes: []string{"/read"}, ReadonlyPrefixes: []string{"/read"}, Methods: []string{"GET", "HEAD"}, Limits: diagnosisflow.Limits{Format: diagnosisflow.Version, MaxPages: 2, MaxEndpoints: 32, MaxResponseBytes: 4096}, Current: func() error { return nil }}, store)
	if err != nil {
		t.Fatal(err)
	}
	provider := &pipelineProvider{flow: flow, t: t}
	worker := agent.NewWorker(provider, "owned", t.TempDir(), nil, 0, 10)
	worker.SetNonStreaming(func() bool { return true })
	engine := NewEngine(nil)
	engine.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) { return &agent.Planner{}, worker })
	server := &Server{engine: engine}
	task := &Task{ID: binding.TaskID}
	reader := &pipelinePrivateReader{binding: explorationFlowBinding(binding, digest), rows: map[uint64]diagnosisflow.ExplorationObservation{}, boundary: b, http: http}
	options := ExplorationPipelineOptions{Workflow: flow, Binding: binding, SourceDigest: digest, TargetURL: "https://owned.test/read", MaxTurns: 5, MaxDuration: time.Second, OpenHost: func(_ context.Context, got exploration.Binding, w *agent.Worker) (ExplorationPipelineHost, error) {
		if got != binding || w != worker {
			t.Error("factory did not reuse approved binding/provider")
		}
		return ExplorationPipelineHost{Session: session, Reader: reader, Fetch: reader.fetch, Sequences: reader.sequences}, nil
	}}
	return server, task, options, b, browser, http, provider
}
func TestExplorationPipelineRunsStaticWorkerObservedAndFinishBeforeDiagnosticAdmission(t *testing.T) {
	server, task, o, b, browser, http, p := pipelineFixture(t)
	result, err := server.RunExplorationPipeline(t.Context(), task, o)
	snapshot, _ := o.Workflow.Snapshot()
	if err != nil || result.Status != "COMPLETED" || result.Reason != "completed" || p.calls.Load() != 3 || snapshot.Counts.ExplorationRequests != 3 || snapshot.Counts.Sent != 0 || snapshot.Counts.Attempted != 0 || !snapshot.InitialCompleted || snapshot.Stage != "diagnosing" {
		t.Fatal("product exploration pipeline was not connected", err, result.Status, snapshot.Counts)
	}
	if b.lease.closed() != 1 || browser.closes.Load() != 1 || http.deleted.Load() != 1 {
		t.Fatal("pipeline left exploration resources alive before diagnosis")
	}
	inferred, observed := 0, 0
	for _, fact := range o.Workflow.DiscoveryInventory() {
		if fact.Confidence == "inferred" {
			inferred++
		}
		if fact.Confidence == "observed" {
			observed++
		}
	}
	if inferred == 0 || observed != 3 {
		t.Fatal("trusted and inferred discoveries were not preserved separately")
	}
}
func TestExplorationPipelineWithoutCapabilitiesRecordsNotRunAndKeepsLegacyCrawlAvailable(t *testing.T) {
	server, task, o, b, _, http, p := pipelineFixture(t)
	o.OpenHost = nil
	result, err := server.RunExplorationPipeline(t.Context(), task, o)
	snapshot, _ := o.Workflow.Snapshot()
	if err != nil || result.Status != "NOT_RUN" || snapshot.ExplorationStatus != "NOT_RUN" || snapshot.Stage != "crawling" || snapshot.InitialCompleted || p.calls.Load() != 0 || b.opens.Load() != 0 || http.requests.Load() != 0 {
		t.Fatal("missing capabilities enabled or consumed discovery")
	}
}
func TestExplorationPipelineRejectsForeignReaderAndDoesNotHideCleanupFailure(t *testing.T) {
	t.Run("foreign-reader", func(t *testing.T) {
		server, task, o, b, browser, http, p := pipelineFixture(t)
		factory := o.OpenHost
		o.OpenHost = func(ctx context.Context, binding exploration.Binding, w *agent.Worker) (ExplorationPipelineHost, error) {
			host, err := factory(ctx, binding, w)
			reader := host.Reader.(*pipelinePrivateReader)
			reader.binding.TargetRevision++
			return host, err
		}
		_, err := server.RunExplorationPipeline(t.Context(), task, o)
		if err == nil || p.calls.Load() != 0 || http.navigate.Load() != 0 || b.lease.closed() != 1 || browser.closes.Load() != 1 {
			t.Fatal("cross-run reader accepted or resources leaked")
		}
	})
	t.Run("cleanup-failure", func(t *testing.T) {
		server, task, o, b, browser, _, _ := pipelineFixture(t)
		browser.closeErr = errors.New("fixture-private-marker")
		_, err := server.RunExplorationPipeline(t.Context(), task, o)
		if !errors.Is(err, ErrExplorationClosure) || strings.Contains(err.Error(), "fixture-private-marker") || b.lease.closed() != 1 {
			t.Fatal("cleanup failure hidden or private detail exposed")
		}
	})
}
func TestExplorationPipelineModelFailurePreservesStaticPartialWithoutSynthesis(t *testing.T) {
	server, task, o, _, _, http, p := pipelineFixture(t)
	p.failure = errors.New("fixture model failure")
	result, err := server.RunExplorationPipeline(t.Context(), task, o)
	snapshot, _ := o.Workflow.Snapshot()
	if err != nil || result.Status != "PARTIAL" || result.Reason != "model_error" || snapshot.Counts.ExplorationRequests != 2 || http.navigate.Load() != 0 || p.calls.Load() != 1 {
		t.Fatal("model failure was fabricated as completion or discarded partial", err, result.Status, result.Reason)
	}
}
func TestE2HTTPExplorationStageRunsOnceAndExcludesConcurrentDiagnosis(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	runtime, task := e2HTTPRuntimeFixture(t, func(o *E2HTTPRuntimeOptions) {
		o.Exploration = func(ctx context.Context, got *Task) error {
			calls.Add(1)
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})
	if err := runtime.AdmitTask(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := runtime.PreparePlanning(t.Context(), task); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("stage did not start")
	}
	workCtx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, _, err := runtime.PrepareWork(workCtx, task, &db.Node{ID: 9, Kind: db.KindIntent})
	if err == nil || runtime.task.lease != nil {
		t.Fatal("diagnosis entered before exploration close")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.PreparePlanning(t.Context(), task); err != nil || calls.Load() != 1 {
		t.Fatal("stage replayed")
	}
}
func TestE2HTTPExplorationStageFailureCannotFallThroughOrReplay(t *testing.T) {
	var calls atomic.Int32
	runtime, task := e2HTTPRuntimeFixture(t, func(o *E2HTTPRuntimeOptions) {
		o.Exploration = func(context.Context, *Task) error { calls.Add(1); return ErrExplorationClosure }
	})
	if err := runtime.AdmitTask(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := runtime.PreparePlanning(t.Context(), task); !errors.Is(err, ErrExplorationClosure) {
			t.Fatal("stage failure became a successful plan")
		}
	}
	if calls.Load() != 1 || runtime.task.plannerRounds != 0 || runtime.task.lease != nil {
		t.Fatal("failed exploration replayed or used diagnostic authority")
	}
}

func TestExplorationPipelineObservationFailureStopsBeforeNextModelCall(t *testing.T) {
	server, task, o, _, _, http, p := pipelineFixture(t)
	factory := o.OpenHost
	o.OpenHost = func(ctx context.Context, binding exploration.Binding, w *agent.Worker) (ExplorationPipelineHost, error) {
		host, err := factory(ctx, binding, w)
		sequences := host.Sequences
		host.Sequences = func(ctx context.Context, b diagnosisflow.ExplorationBinding) ([]uint64, error) {
			if http.navigate.Load() > 0 {
				return nil, errors.New("fixture-private-marker")
			}
			return sequences(ctx, b)
		}
		return host, err
	}
	_, err := server.RunExplorationPipeline(t.Context(), task, o)
	snapshot, _ := o.Workflow.Snapshot()
	if !errors.Is(err, ErrExplorationObservation) || strings.Contains(err.Error(), "fixture-private-marker") || p.calls.Load() != 1 || snapshot.Counts.ExplorationRequests != 2 || !snapshot.InitialCompleted {
		t.Fatal("observation failure admitted another model call or discarded stored discoveries", err)
	}
}
func TestExplorationPipelineOpenCleanupFailureCannotUseLegacyFallback(t *testing.T) {
	server, task, o, b, browser, http, p := pipelineFixture(t)
	browser.info.PackageVersion = "unexpected"
	browser.closeErr = errors.New("fixture-private-marker")
	_, err := server.RunExplorationPipeline(t.Context(), task, o)
	snapshot, _ := o.Workflow.Snapshot()
	if !errors.Is(err, ErrExplorationClosure) || strings.Contains(err.Error(), "fixture-private-marker") || snapshot.ExplorationStatus == "NOT_RUN" || p.calls.Load() != 0 || http.requests.Load() != 0 || b.lease.closed() != 1 {
		t.Fatal("partially open resource cleanup was hidden as fallback")
	}
}

func TestExplorationPipelineStaticBoundaryCapPreservesPartialAndContinuesToDiagnosis(t *testing.T) {
	server, task, o, b, _, _, p := pipelineFixture(t)
	factory := o.OpenHost
	o.OpenHost = func(ctx context.Context, binding exploration.Binding, w *agent.Worker) (ExplorationPipelineHost, error) {
		host, err := factory(ctx, binding, w)
		host.Fetch = func(ctx context.Context, _ string) (diagnosisflow.ExplorationFetchResult, error) {
			b.lease.stop(exploration.CapExhausted)
			<-ctx.Done()
			return diagnosisflow.ExplorationFetchResult{}, ctx.Err()
		}
		return host, err
	}
	result, err := server.RunExplorationPipeline(t.Context(), task, o)
	snapshot, _ := o.Workflow.Snapshot()
	if err != nil || result.Status != "PARTIAL" || result.Reason != "cap_exhausted" || snapshot.Stage != "diagnosing" || !snapshot.InitialCompleted || p.calls.Load() != 0 {
		t.Fatal("static exploration cap was treated as a diagnostic failure", err, result.Status, result.Reason)
	}
}

func TestPostgresExplorationProductPipelinePersistsActualStageInventory(t *testing.T) {
	dsn := os.Getenv("ARTEX_FIRST_DIAGNOSIS_ROLE_TEST_DSN")
	if dsn == "" || os.Getenv("ARTEX_FIRST_DIAGNOSIS_ROLE_TEST_OWNED") != "1" {
		t.Skip("owned PostgreSQL16 required")
	}
	pool, err := db.Open(dsn)
	if err != nil {
		t.Fatal("owned PostgreSQL open failed")
	}
	defer pool.Close()
	if diagnosisflow.Migrate(t.Context(), pool.DB) != nil {
		t.Fatal("owned workflow migrate failed")
	}
	row, err := pool.CreateTask("Owned exploration pipeline persistence", "Private synthetic metadata only", nil, 0, 0)
	if err != nil {
		t.Fatal("owned task create failed")
	}
	store := diagnosisflow.Postgres{DB: pool.DB}
	server, task, options, _, _, _, _ := pipelineFixtureWithStore(t, store, strconv.FormatInt(row.ID, 10))
	result, err := server.RunExplorationPipeline(t.Context(), task, options)
	if err != nil || result.Status != "COMPLETED" {
		t.Fatal("persisted product stage failed", err)
	}
	reopened, err := db.Open(dsn)
	if err != nil {
		t.Fatal("owned database reopen failed")
	}
	defer reopened.Close()
	saved, err := (diagnosisflow.Postgres{DB: reopened.DB}).Read(t.Context(), "ARTEX", options.Binding.JobID)
	if err != nil || saved.Exploration == nil || saved.Exploration.Binding.TargetRef != options.Binding.TargetRef || saved.Exploration.Binding.TargetRevision != options.Binding.TargetRevision || saved.Snapshot.ExplorationStatus != "COMPLETED" || saved.Snapshot.Counts.ExplorationRequests != 3 || len(saved.DiscoveryInventory) < 4 {
		t.Fatal("stage binding or partial inventory was not durable")
	}
}

func TestExplorationPipelineRejectsURLOrAuthorityChangesBeforeHostFactory(t *testing.T) {
	for _, change := range []func(*ExplorationPipelineOptions){func(o *ExplorationPipelineOptions) { o.TargetURL = "https://other.owned.test/read" }, func(o *ExplorationPipelineOptions) { o.TargetURL = "https://owned.test/read/other" }, func(o *ExplorationPipelineOptions) { o.SourceDigest = strings.Repeat("b", 64) }, func(o *ExplorationPipelineOptions) { o.Binding.TargetRevision++ }} {
		server, task, o, b, _, http, p := pipelineFixture(t)
		change(&o)
		factory := o.OpenHost
		called := false
		o.OpenHost = func(ctx context.Context, binding exploration.Binding, w *agent.Worker) (ExplorationPipelineHost, error) {
			called = true
			return factory(ctx, binding, w)
		}
		_, err := server.RunExplorationPipeline(t.Context(), task, o)
		if !errors.Is(err, exploration.ErrBinding) || called || b.opens.Load() != 0 || http.requests.Load() != 0 || p.calls.Load() != 0 {
			t.Fatal("changed URL or authority reached host factory")
		}
	}
}

func TestE2HTTPExplorationModelBudgetLimitKeepsTaskBudgetDisposition(t *testing.T) {
	runtime, task := e2HTTPRuntimeFixture(t, func(o *E2HTTPRuntimeOptions) {
		o.Exploration = func(context.Context, *Task) error { return ErrE2HTTPBudgetLimited }
	})
	if err := runtime.AdmitTask(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.PreparePlanning(t.Context(), task); !errors.Is(err, ErrE2HTTPBudgetLimited) || !task.firstDiagnosisBudgetLimited.Load() {
		t.Fatal("shared model budget exhaustion did not retain task budget disposition")
	}
}
