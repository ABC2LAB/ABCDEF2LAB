//go:build linux

package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosis"
	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/exploration"
	"github.com/Autumn-27/norma/llm"
	"golang.org/x/crypto/bcrypt"
)

// 하나의 승인된 provider 경로를 도구 프로필로 분기한다. 탐색 도구를 진단 Worker에 보태지 않는다.
type explorationTransitionProvider struct {
	exploration *pipelineProvider
	diagnosis   *firstDiagnosisE2Provider
	planner     *firstDiagnosisPlannerProbeProvider
	target      string
}

func (p *explorationTransitionProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	for _, tool := range req.Tools {
		if tool.Name == "mcp__explore_browser__browser_navigate" {
			msg, stop, usage, err := p.exploration.Complete(ctx, req)
			for i := range msg.Content {
				if msg.Content[i].Type == llm.BlockToolUse && msg.Content[i].Name == tool.Name {
					msg.Content[i].Input, _ = json.Marshal(map[string]string{"url": p.target + "/read/browser-api"})
				}
			}
			return msg, stop, usage, err
		}
	}
	for _, tool := range req.Tools {
		if tool.Name == "Bash" {
			return p.diagnosis.Complete(ctx, req)
		}
	}
	return p.planner.Complete(ctx, req)
}
func (*explorationTransitionProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{}, errors.New("unexpected transition fixture streaming"))
	}
}

// 탐색 MCP·private reader는 명시적 fixture다. 진단은 production 생성자·원본 Worker·native E②·TLS GET·실제 감사를 지난다.
func TestOwnedPostgresExplorationSelectionThenProtectedDiagnosis(t *testing.T) {
	helper := firstDiagnosisNativeHelper(t)
	fixture := newControlledLocalDatabaseFixture(t)
	installer := &db.DB{DB: fixture.Installer}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	manager, err := NewFirstDiagnosisManager(ctx, t.TempDir(), fixture.Runtime)
	if err != nil {
		t.Fatal("owned transition manager failed")
	}
	task, err := manager.CreateTask("Owned exploration diagnosis transition", "Preserve selected exploration evidence and protected diagnosis", nil, 60, 60)
	if err != nil || task == nil {
		t.Fatal("owned transition task failed")
	}
	if _, err = installer.ExecContext(ctx, "UPDATE tasks SET paused=true,queued=false WHERE id=$1", task.ID); err != nil {
		t.Fatal("owned transition pause failed")
	}
	task.updateLifecycle(func(s *taskLifecycleState) { s.Paused = true; s.Queued = false })
	if _, err = task.Store.AddIntent(map[string]any{"summary": "Inspect the selected owned read endpoint"}, 1, nil, "system"); err != nil {
		t.Fatal("owned transition intent failed")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("synthetic-owned-transition-fixture"), bcrypt.MinCost)
	if err != nil || installer.SetSetting(authPassKey, string(hash)) != nil {
		t.Fatal("owned transition authentication setup failed")
	}
	var hits atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/read/browser-api" {
			t.Error("unselected request reached owned transition target")
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"owned_fixture":true}`)); err != nil {
			t.Error("owned transition response failed")
		}
	}))
	defer target.Close()
	cfg, grant, policy := e2HostPolicyFixture(t)
	grant.TaskID = mustFirstDiagnosisTaskNumber(task.ID)
	grant.ExplorationID = task.ExpID
	fixtureServer, fixtureTask, o, browserBoundary, browser, mcp, exploreProvider := pipelineFixtureWithStore(t, nil, task.ID, policy.RunRef)
	if diagnosisflow.Migrate(ctx, installer.DB) != nil {
		t.Fatal("owned transition workflow migration failed")
	}
	flow, err := diagnosisflow.New(ctx, diagnosisflow.Binding{JobID: o.Binding.JobID, TaskID: task.ID, RunRef: policy.RunRef, SourceDigest: o.SourceDigest, InputDigest: o.SourceDigest, TargetRef: o.Binding.TargetRef, TargetRevision: o.Binding.TargetRevision, Owner: "ARTEX", Mode: diagnosisflow.Manual, TargetURL: target.URL + "/read", PathPrefixes: []string{"/read"}, ReadonlyPrefixes: []string{"/read"}, Methods: []string{"GET", "HEAD"}, Limits: diagnosisflow.Limits{Format: diagnosisflow.Version, MaxPages: 2, MaxEndpoints: 32, MaxResponseBytes: 4096}, Current: func() error { return nil }}, diagnosisflow.Postgres{DB: installer.DB})
	if err != nil {
		t.Fatal("owned transition workflow creation failed")
	}
	o.Workflow, o.TargetURL = flow, target.URL+"/read"
	exploreProvider.flow = flow
	factory := o.OpenHost
	fixtureWorker, err := fixtureServer.ExplorationWorkerFor(fixtureTask)
	if err != nil {
		t.Fatal("owned transition fixture Worker unavailable")
	}
	o.OpenHost = func(ctx context.Context, b exploration.Binding, w *agent.Worker) (ExplorationPipelineHost, error) {
		if w == nil || !w.ExplorationReady() {
			t.Error("transition did not reuse approved Worker")
		}
		return factory(ctx, b, fixtureWorker)
	}
	binary, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal("owned transition helper read failed")
	}
	digest := sha256.Sum256(binary)
	policy.Helper, policy.HelperSHA256 = helper, hex.EncodeToString(digest[:])
	policy.StateDir = filepath.Join(firstDiagnosisPrivateTemp(t), "state")
	if os.Mkdir(policy.StateDir, 0700) != nil {
		t.Fatal("owned transition state directory failed")
	}
	upstream := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: target.Certificate().Raw})
	if os.WriteFile(filepath.Join(filepath.Dir(cfg.PolicyFile), "transition-upstream.pem"), upstream, 0600) != nil {
		t.Fatal("owned transition TLS roots failed")
	}
	digest = sha256.Sum256(upstream)
	policy.UpstreamCA = &E2HTTPFilePin{File: "transition-upstream.pem", SHA256: hex.EncodeToString(digest[:])}
	policy.Workflow = &E2HTTPWorkflowPolicy{Format: diagnosisflow.Version, Mode: diagnosisflow.Manual, InputSHA256: o.SourceDigest}
	policy.Rules = []E2HTTPFileRule{{Ref: "transition-read-rule", Origin: target.URL, ConnectIP: "127.0.0.1", PathPrefix: "/read", PathScopeRef: "transition-read-path", Methods: []string{"GET"}}}
	policy.Runtime.WorkerTimeoutMS = 15000
	policy.Runtime.MaxWorkerTurns = 4
	policy.Runtime.MaxPlannerRounds = 4
	raw, err := json.Marshal(policy)
	if err != nil || os.WriteFile(cfg.PolicyFile, raw, 0600) != nil {
		t.Fatal("owned transition policy failed")
	}
	digest = sha256.Sum256(raw)
	grant.PolicySHA256 = hex.EncodeToString(digest[:])
	e2HostWriteApproval(t, cfg, grant)
	skillDir, dataDir, keyDir := firstDiagnosisPrivateTemp(t), firstDiagnosisPrivateTemp(t), firstDiagnosisPrivateTemp(t)
	if os.WriteFile(filepath.Join(keyDir, jwtKeyFilename), []byte("synthetic-owned-transition-jwt-material"), 0600) != nil {
		t.Fatal("owned transition JWT failed")
	}
	manifest := firstDiagnosisSupervisorModelManifest(t, installer)
	profileID := firstDiagnosisPinFixtureTaskProfile(t, installer, task)
	supervisor, err := NewFirstDiagnosisSupervisor(ctx, manager)
	if err != nil {
		t.Fatal("owned transition Supervisor failed")
	}
	stageDone := make(chan error, 1)
	var stageCalls, acquisitions atomic.Int32
	hooks := FirstDiagnosisHTTPHooks{Workflow: flow, Exploration: func(host *Server) E2HTTPExplorationStage {
		return func(call context.Context, actual *Task) error {
			stageCalls.Add(1)
			_, err := host.RunExplorationPipeline(call, actual, o)
			stageDone <- err
			return err
		}
	}}
	runtime, err := StartFirstDiagnosisHTTP(ctx, manager, FirstDiagnosisHTTPStartOptions{Host: cfg, SkillDir: skillDir, DataDir: dataDir, KeyDir: keyDir, LocalLLMManifestPath: manifest, Supervisor: supervisor, Hooks: hooks, Shutdown: supervisor.Shutdown(), CleanupTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal("owned transition production constructor failed")
	}
	defer func() {
		if err := runtime.Close(context.Background()); err != nil {
			t.Error("owned transition final cleanup failed")
		}
	}()
	acquire := runtime.host.runtime.options.Acquire
	runtime.host.runtime.options.Acquire = func(ctx context.Context, r E2HTTPRequest) (E2HTTPRunOptions, error) {
		acquisitions.Add(1)
		return acquire(ctx, r)
	}
	diagnosticProvider := &firstDiagnosisE2Provider{command: "curl --silent --show-error --max-time 5 '" + target.URL + "/read/browser-api'"}
	plannerProvider := &firstDiagnosisPlannerProbeProvider{textOnly: true}
	firstDiagnosisInjectProfileProvider(t, runtime.server, profileID, &explorationTransitionProvider{exploration: exploreProvider, diagnosis: diagnosticProvider, planner: plannerProvider, target: target.URL})
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx, task.ID, 0, "") }()
	select {
	case err := <-stageDone:
		if err != nil {
			t.Fatal("owned transition exploration stage failed")
		}
	case <-ctx.Done():
		t.Fatal("owned transition exploration did not finish")
	}
	snapshot, _ := flow.Snapshot()
	if snapshot.Stage != "awaiting_selection" || snapshot.ExplorationStatus != "COMPLETED" || !snapshot.InitialCompleted || snapshot.DeepRuns != 0 || snapshot.Counts.ExplorationRequests != 3 || hits.Load() != 0 || acquisitions.Load() != 0 || stageCalls.Load() != 1 || browserBoundary.lease.closed() != 1 || browser.closes.Load() != 1 || mcp.deleted.Load() != 1 {
		t.Fatal("diagnosis began before exploration cleanup and explicit selection")
	}
	var selected string
	for _, e := range snapshot.Endpoints {
		if e.Path == "/read/browser-api" && e.Method == "GET" && e.DiscoveryConfidence == "observed" {
			selected = e.ID
		}
	}
	if selected == "" {
		t.Fatal("actual private observation was not selectable")
	}
	if _, err = flow.Command(ctx, "ARTEX", "selection", "owned-transition-selection", snapshot.Revision, []string{selected}); err != nil {
		t.Fatal("owned transition explicit selection failed")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("owned transition actual Supervisor diagnosis failed")
		}
	case <-ctx.Done():
		t.Fatal("owned transition diagnosis did not finish")
	}
	result := supervisor.RunResult()
	execution, ok := runtime.host.runtime.Execution(task.ID)
	results := runtime.host.runtime.Results(task.ID)
	snapshot, _ = flow.Snapshot()
	if result == nil || result.Outcome != diagnosis.OutcomeComplete || result.IntentOutcomes.Done != 1 || result.IntentOutcomes.Unknown != 0 || stageCalls.Load() != 1 || acquisitions.Load() != 1 || hits.Load() != 1 || !ok || execution == nil || len(execution.Calls()) != 1 || len(results) != 1 || !results[0].Final || !results[0].ProcessDrained || !results[0].ProxyDrained || !results[0].Call.AuditComplete || results[0].Call.Counts.ForwardAttempted != 1 || results[0].Call.Counts.Responses != 1 || results[0].Call.Counts.Unknown != 0 {
		t.Fatal("protected diagnosis did not preserve actual GET, audit, drain and final outcome")
	}
	if snapshot.DeepRuns != 1 || snapshot.DeepClosed != 1 || snapshot.Counts.Sent != 1 || snapshot.Counts.Attempted != 1 || snapshot.Counts.ExplorationRequests != 3 || diagnosticProvider.leaked || diagnosticProvider.unsupportedTool || plannerProvider.outboundToolExposed {
		t.Fatal("legacy crawl replayed or diagnostic profile bypassed the controlled boundary")
	}
	observed, inferred := 0, 0
	for _, fact := range flow.DiscoveryInventory() {
		if fact.Confidence == "observed" {
			observed++
		}
		if fact.Confidence == "inferred" {
			inferred++
		}
	}
	if observed != 3 || inferred != 2 {
		t.Fatalf("transition discovery count mismatch: observed=%d inferred=%d total=%d", observed, inferred, len(flow.DiscoveryInventory()))
	}
	closeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err = runtime.Close(closeCtx); err != nil || !runtime.ResourcesClosed() {
		t.Fatal("owned transition authority/drain close failed")
	}
	stored, err := supervisor.PersistRunOutcomeStored(closeCtx, nil)
	state := runtime.server.requestApprovals.runState(mustFirstDiagnosisTaskNumber(task.ID))
	if err != nil || !stored || state.Phase != "closed" || !state.DrainComplete || state.ClosureFailed || state.Outcome != "completed" || state.Execution == nil || state.Execution.Counts.Calls != 1 {
		t.Fatal("owned transition stored result or request audit phase was not closed")
	}
	persisted, err := (diagnosisflow.Postgres{DB: installer.DB}).Read(ctx, "ARTEX", o.Binding.JobID)
	if err != nil || persisted.Snapshot.DeepRuns != 1 || persisted.Snapshot.DeepClosed != 1 || persisted.Snapshot.Counts.Sent != 1 || persisted.Snapshot.Counts.ExplorationRequests != 3 || len(persisted.DiscoveryInventory) != 5 {
		t.Fatal("owned transition private and diagnostic observations were not persisted")
	}
}
