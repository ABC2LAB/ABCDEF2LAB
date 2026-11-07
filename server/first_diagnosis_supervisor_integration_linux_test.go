//go:build linux

package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Autumn-27/norma/llm"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	pgdb "github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/transmit"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/sys/unix"
)

func firstDiagnosisSupervisorConstructorClose(t *testing.T, manager *Manager, installer *pgdb.DB, task *Task) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("M01 실제 비특권 운영자 UID가 필요함")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	// 제품은 인증 자료를 생성하지 않는다. 일회용 설치자가 먼저 준비한 자료만 공급한다.
	hash, err := bcrypt.GenerateFromPassword([]byte("synthetic-owned-constructor-fixture"), bcrypt.MinCost)
	if err != nil || installer.SetSetting(authPassKey, string(hash)) != nil {
		t.Fatal("소유 fixture의 사전 인증 자료 설치 실패")
	}
	if _, err := installer.ExecContext(ctx, "UPDATE tasks SET paused=true,queued=false WHERE id=$1", task.ID); err != nil {
		t.Fatal("명시한 fixture task의 준비 상태 저장 실패")
	}
	task.updateLifecycle(func(state *taskLifecycleState) { state.Paused = true; state.Queued = false })
	cfg, grant, policy := e2HostPolicyFixture(t)
	grant.TaskID = mustFirstDiagnosisTaskNumber(task.ID)
	grant.ExplorationID = task.ExpID
	e2HostWriteApproval(t, cfg, grant)
	privateDir := func() string {
		dir := t.TempDir()
		if os.Chmod(dir, 0700) != nil {
			t.Fatal("시험 디렉터리 보호 실패")
		}
		return dir
	}
	skillDir, dataDir, keyDir := privateDir(), privateDir(), privateDir()
	if os.WriteFile(filepath.Join(keyDir, jwtKeyFilename), []byte("synthetic-owned-jwt-fixture-key-material"), 0600) != nil {
		t.Fatal("시험 JWT 공급 실패")
	}
	manifest := firstDiagnosisSupervisorModelManifest(t, installer)
	_ = firstDiagnosisPinFixtureTaskProfile(t, installer, task)
	supervisor, err := NewFirstDiagnosisSupervisor(ctx, manager)
	if err != nil {
		t.Fatal("실제 Supervisor 생성 실패")
	}
	runtime, err := StartFirstDiagnosisHTTP(ctx, manager, FirstDiagnosisHTTPStartOptions{Host: cfg, SkillDir: skillDir, DataDir: dataDir, KeyDir: keyDir, LocalLLMManifestPath: manifest, Supervisor: supervisor, Shutdown: supervisor.Shutdown(), CleanupTimeout: 5 * time.Second})
	if err != nil || runtime == nil {
		t.Fatal("실제 runtime LOGIN pool·Supervisor·보호 입력으로 서버 기동 실패")
	}
	defer func() {
		if err := runtime.Close(context.Background()); err != nil {
			t.Error("실제 constructor fixture 종료 실패")
		}
	}()
	if runtime.server == nil || supervisor.server != runtime.server || !runtime.server.firstDiagnosis || runtime.server.firstDiagnosisPreparation {
		t.Fatal("실제 production 서버가 감독자와 결속되지 않음")
	}
	if err := supervisor.AuthorizeExecution(ctx, transmit.Scope{RunRef: policy.RunRef, ResetRef: policy.ResetRef, PolicyRef: policy.PolicyRef, CaseRef: policy.CaseRef, TaskRef: task.ID}, 1); err == nil {
		t.Fatal("실행 admission 이전에 E② 실행 권한을 발급함")
	}
	if err := supervisor.Attach(runtime.server); !errors.Is(err, ErrFirstDiagnosisSupervisor) {
		t.Fatal("중복 Attach가 수용됨")
	}
	if err := supervisor.CheckTaskQuiescent(ctx, grant.TaskID); err != nil {
		t.Fatal("실제 준비 task의 초기 정지 상태를 증명하지 못함")
	}
	foreign, foreignErr := manager.CreateTask("preserve unrelated terminal fixture", "Unrelated completed fixture", nil, 0, 0)
	if foreignErr != nil || foreign == nil {
		t.Fatal("독립 보존 task fixture 생성 실패")
	}
	if _, err := installer.ExecContext(ctx, "UPDATE tasks SET paused=false,status='done' WHERE id=$1", foreign.ID); err != nil {
		t.Fatal("보존 task fixture 저장 실패")
	}
	foreign.updateLifecycle(func(state *taskLifecycleState) { state.Paused = false; state.Status = "done" })
	if err := supervisor.CheckTaskQuiescent(ctx, mustFirstDiagnosisTaskNumber(foreign.ID)); !errors.Is(err, ErrFirstDiagnosisSupervisor) {
		t.Fatal("이미 소유한 실행 밖의 task를 감독 범위로 편입함")
	}
	// 선택한 실행은 Run의 검증된 admission 없이 원본 Engine에서 시작할 수 없다.
	runtime.server.engine.Run(ctx, task)
	if runtime.server.engine.Started(task.ID) {
		t.Fatal("Supervisor Run 밖 원본 Engine admission이 열림")
	}
	if _, err := os.Stat(filepath.Join(cfg.AuthorityRoot, "state", "events.jsonl")); !os.IsNotExist(err) {
		t.Fatal("실제 intent 없이 HTTP 감사 실행을 생성함")
	}
	closeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err := runtime.Close(closeCtx); err != nil {
		t.Fatal("실제 Supervisor의 정상 서버 마감이 실패함")
	}
	if err := runtime.Close(closeCtx); err != nil {
		t.Fatal("이미 완료한 마감이 멱등하지 않음")
	}
	var foreignPaused bool
	var foreignStatus string
	if err := installer.QueryRowContext(closeCtx, "SELECT paused,status FROM tasks WHERE id=$1", foreign.ID).Scan(&foreignPaused, &foreignStatus); err != nil || foreignPaused || foreignStatus != "done" {
		t.Fatal("마감이 소유하지 않은 task의 상태를 덮어씀")
	}

	if err := supervisor.CheckTaskQuiescent(closeCtx, grant.TaskID); err != nil {
		t.Fatal("마감 뒤 실제 quiescence 확인을 closed 표지만으로 거부함")
	}
	// 취소와 달리 실제 Close는 flock을 반환해야 한다. 시작 마커는 그대로 남겨 재사용을 막는다.
	lock, err := os.OpenFile(filepath.Join(cfg.AuthorityRoot, ".first-http.lock"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal("실제 호스트 소유 lock 파일을 읽지 못함")
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal("정상 마감 뒤 실제 배타 lease가 반환되지 않음")
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	if _, err := os.Stat(filepath.Join(cfg.AuthorityRoot, ".first-http.started")); err != nil {
		t.Fatal("단회 시작 마커가 사라짐")
	}
	if err := supervisor.Run(closeCtx, task.ID, 0, ""); !errors.Is(err, ErrFirstDiagnosisSupervisor) {
		t.Fatal("서버 마감 뒤 새 일반 실행이 수용됨")
	}
}

func firstDiagnosisSupervisorEngineRun(t *testing.T, manager *Manager, installer *pgdb.DB, task *Task, mode string) {
	t.Helper()
	helper := firstDiagnosisNativeHelper(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var hits atomic.Int64
	targetEntered := make(chan struct{}, 1)
	targetExited := make(chan struct{}, 1)
	releaseTarget := make(chan struct{})
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if mode == "cancel" {
			select {
			case targetEntered <- struct{}{}:
			default:
			}
			select {
			case <-r.Context().Done():
				select {
				case targetExited <- struct{}{}:
				default:
				}
			case <-releaseTarget:
			}
			return
		}
		if r.URL.Path != "/allowed" {
			t.Error("생산 Engine의 범위 밖 요청이 대상에 도달함")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"owned_fixture":true}`))
	}))
	defer func() { close(releaseTarget); target.Close() }()
	cfg, grant, policy := e2HostPolicyFixture(t)
	grant.TaskID = mustFirstDiagnosisTaskNumber(task.ID)
	grant.ExplorationID = task.ExpID
	helperRaw, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal("native helper 읽기 실패")
	}
	digest := sha256.Sum256(helperRaw)
	policy.StateDir = filepath.Join(firstDiagnosisPrivateTemp(t), "state")
	if err := os.Mkdir(policy.StateDir, 0700); err != nil {
		t.Fatal("짧은 실제 socket 상태 디렉터리 생성 실패")
	}
	policy.Helper = helper
	policy.HelperSHA256 = hex.EncodeToString(digest[:])
	policy.Rules = []E2HTTPFileRule{{Ref: "owned-native-rule", Origin: target.URL, ConnectIP: "127.0.0.1", PathPrefix: "/allowed", PathScopeRef: "owned-native-path", Methods: []string{"GET"}}}
	policy.Limits.CallTimeoutMS = 10000
	policy.Limits.RequestTimeoutMS = 5000
	policy.Proxy.RequestTimeoutMS = 5000
	policy.Runtime.WorkerTimeoutMS = 15000
	policy.Runtime.MaxWorkerTurns = 4
	policy.Runtime.MaxPlannerRounds = 4
	raw, err := json.Marshal(policy)
	if err != nil || os.WriteFile(cfg.PolicyFile, raw, 0600) != nil {
		t.Fatal("실제 native 정책 공급 실패")
	}
	digest = sha256.Sum256(raw)
	grant.PolicySHA256 = hex.EncodeToString(digest[:])
	e2HostWriteApproval(t, cfg, grant)
	privateDir := func() string {
		dir := t.TempDir()
		if os.Chmod(dir, 0700) != nil {
			t.Fatal("시험 디렉터리 보호 실패")
		}
		return dir
	}
	skillDir, dataDir, keyDir := privateDir(), privateDir(), privateDir()
	if os.WriteFile(filepath.Join(keyDir, jwtKeyFilename), []byte("synthetic-owned-jwt-fixture-key-material"), 0600) != nil {
		t.Fatal("시험 JWT 공급 실패")
	}
	manifest := firstDiagnosisSupervisorModelManifest(t, installer)
	profileID := firstDiagnosisPinFixtureTaskProfile(t, installer, task)
	supervisor, err := NewFirstDiagnosisSupervisor(ctx, manager)
	if err != nil {
		t.Fatal("실제 Supervisor 생성 실패")
	}
	runtime, err := StartFirstDiagnosisHTTP(ctx, manager, FirstDiagnosisHTTPStartOptions{Host: cfg, SkillDir: skillDir, DataDir: dataDir, KeyDir: keyDir, LocalLLMManifestPath: manifest, Supervisor: supervisor, Shutdown: supervisor.Shutdown(), CleanupTimeout: 5 * time.Second})
	if err != nil || runtime == nil {
		t.Fatal("실제 production HTTP 기동 실패")
	}
	if mode == "prepare-fail" {
		runtime.host.runtime.options.AuthorizeIntent = func(context.Context, E2HTTPRequest) error { return ErrE2HTTPHost }
	}
	if mode == "close-fail" {
		acquire := runtime.host.runtime.options.Acquire
		runtime.host.runtime.options.Acquire = func(ctx context.Context, request E2HTTPRequest) (E2HTTPRunOptions, error) {
			options, err := acquire(ctx, request)
			if err != nil {
				return options, err
			}
			release := options.Release
			options.Release = func(ctx context.Context) error {
				if release == nil {
					return ErrE2HTTPClosure
				}
				return errors.Join(release(ctx), ErrE2HTTPClosure)
			}
			return options, nil
		}
	}
	defer func() {
		closeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := runtime.Close(closeCtx); err != nil && !((mode == "denied" || mode == "cancel" || mode == "prepare-fail" || mode == "planner-only") && errors.Is(err, ErrFirstDiagnosisOutcome) || mode == "close-fail" && errors.Is(err, ErrE2HTTPClosure)) {
			t.Error("실제 Engine 경로 최종 종료 실패")
		}

	}()
	goalID, err := task.Store.AddNode(pgdb.KindGoal, map[string]any{"text": "Keep the security goal open until a separate trusted verdict exists."}, 0, "open", "system", nil)
	if err != nil || goalID <= 0 {
		t.Fatal("실제 미판정 목표 fixture 생성 실패")
	}
	path := "/allowed"
	if mode == "denied" {
		path = "/forbidden"
	}
	provider := &firstDiagnosisE2Provider{command: "curl --silent --show-error --max-time 5 " + target.URL + path}
	if mode == "tool-failure" {
		provider.command += " | grep -q never-present-owned-fixture"
	}
	plannerProvider := &firstDiagnosisPlannerProbeProvider{firstDiagnosisE2Provider: firstDiagnosisE2Provider{command: "curl --silent --show-error --max-time 1 " + target.URL + "/denied"}, textOnly: mode != "planner-only"}
	// 실제 taskLLMRuntime.current의 role/model guard를 유지하고 profile cache의 모델만 대체한다.
	firstDiagnosisInjectProfileProvider(t, runtime.server, profileID, &firstDiagnosisRoleRoutedProvider{worker: provider, planner: plannerProvider})
	var runErr error
	if mode == "cancel" {
		runCtx, stopRun := context.WithCancel(ctx)
		defer stopRun()
		done := make(chan error, 1)
		go func() { done <- supervisor.Run(runCtx, task.ID, 0, "") }()
		select {
		case <-targetEntered:
		case <-ctx.Done():
			t.Fatal("취소 인수의 실제 대상 요청이 시작되지 않음")
		}
		stopRun()
		select {
		case runErr = <-done:
		case <-ctx.Done():
			t.Fatal("실제 Supervisor Run 취소가 반환하지 않음")
		}
	} else if mode == "planner-only" {
		short, stopRun := context.WithTimeout(ctx, 4*time.Second)
		runErr = supervisor.Run(short, task.ID, 0, "")
		stopRun()
	} else {
		runErr = supervisor.Run(ctx, task.ID, 0, "")
	}
	if mode == "planner-only" || mode == "prepare-fail" {
		if runErr == nil || hits.Load() != 0 || provider.calls != 0 {
			t.Fatal("Planner 또는 준비 실패가 통제 밖 대상 송신으로 이어짐")
		}
		if mode == "planner-only" && plannerProvider.calls == 0 {
			t.Fatal("intent 없는 시작에서 원본 Planner 경로를 거치지 않음")
		}
		return
	}
	if mode == "close-fail" {
		if runErr == nil || hits.Load() != 1 || plannerProvider.calls == 0 {
			t.Fatal("감사 자원 해제 실패를 실행 성공으로 수용함")
		}
		return
	}
	if mode == "cancel" {
		if runErr == nil || !errors.Is(runErr, context.Canceled) {
			t.Fatal("진행 중 실제 HTTP 요청 취소를 성공으로 반환하거나 취소 원인을 잃음")
		}
		closeCtx, stop := context.WithTimeout(context.Background(), 8*time.Second)
		defer stop()
		if err := runtime.Close(closeCtx); !errors.Is(err, ErrFirstDiagnosisOutcome) {
			t.Fatal("취소 실패를 보존한 실제 task·자식·터널·감사 drain이 완료되지 않음")
		}
		select {
		case <-targetExited:
		case <-closeCtx.Done():
			t.Fatal("취소 뒤 실제 대상 연결이 종료되지 않음")
		}
		if hits.Load() != 1 || runtime.server.engine.inflightCount(task.ID) != 0 || runtime.server.engine.ActiveLLMCalls(task.ID) != 0 {
			t.Fatal("취소 뒤 실제 writer·추가 송신이 남음")
		}
		runtime.server.engine.workMu.Lock()
		workLeft := len(runtime.server.engine.work)
		runtime.server.engine.workMu.Unlock()
		if workLeft != 0 {
			t.Fatal("취소 뒤 실제 Worker handle이 남음")
		}
		execution, ok := runtime.host.Runtime().Execution(task.ID)
		if !ok || execution == nil || len(execution.Calls()) != 1 || !execution.Calls()[0].Final || execution.Calls()[0].Counts.Unknown == 0 {
			t.Fatal("취소된 전송 불명을 실제 최종 감사에서 보존하지 않음")
		}
		if err := supervisor.Run(closeCtx, task.ID, 0, ""); !errors.Is(err, ErrFirstDiagnosisSupervisor) {
			t.Fatal("취소·drain 뒤 이전 실행 권한을 재사용함")
		}
		if hits.Load() != 1 {
			t.Fatal("취소 뒤 새 대상 송신이 발생함")
		}
		var intentState, blockedReason string
		if err := installer.QueryRowContext(closeCtx, "SELECT state,blocked_reason FROM exploration_nodes WHERE exploration_id=$1 AND kind='intent'", task.ExpID).Scan(&intentState, &blockedReason); err != nil || intentState != "blocked" || blockedReason != "e2_http_result_unknown" {
			t.Fatal("전송 불명 intent를 재실행 가능한 open으로 보존함")
		}
		return
	}
	if mode == "denied" {
		if runErr == nil {
			t.Fatal("실제 범위 밖 정책 거부가 실행 마감 성공으로 바뀜")
		}
		closeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		closeErr := runtime.Close(closeCtx)
		if !errors.Is(closeErr, ErrFirstDiagnosisOutcome) {
			t.Fatal("실제 거부 결과를 보존하면서 안전한 자원 반환을 완료하지 못함")
		}
		results := runtime.host.Runtime().Results(task.ID)
		if hits.Load() != 0 || len(results) != 1 || results[0].Classification != "policy_not_executed" || results[0].Call.Counts.Denied != 1 || !results[0].Final || !results[0].ProcessDrained || !results[0].ProxyDrained {
			t.Fatal("실제 Engine 정책 거부에서 대상0·프로그램 분류·drain을 증명하지 못함")
		}
		lock, err := os.OpenFile(filepath.Join(cfg.AuthorityRoot, ".first-http.lock"), os.O_RDWR, 0)
		if err != nil {
			t.Fatal("거부 마감의 실제 lock 확인 실패")
		}
		defer lock.Close()
		if unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
			t.Fatal("거부 진단을 실패로 보존한 뒤 안전한 lease 반환이 누락됨")
		}
		defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
		if err := supervisor.Run(closeCtx, task.ID, 0, ""); !errors.Is(err, ErrFirstDiagnosisSupervisor) {
			t.Fatal("거부 후 이전 권한으로 재실행함")
		}
		return
	}
	if runErr != nil {
		active, _ := task.Store.HasActiveIntent()
		view, _ := runtime.host.Runtime().Execution(task.ID)
		classification := "missing"
		callReason := "missing"
		var attempts, unknown uint64
		if provider.result != nil {
			classification = provider.result.Trusted.Classification
			callReason = string(provider.result.Trusted.Call.Reason)
			attempts = provider.result.Trusted.Call.Counts.Attempts
			unknown = provider.result.Trusted.Call.Counts.Unknown
		}
		t.Fatalf("실제 production 실행 마감 실패: target=%d worker_provider_calls=%d planner_provider_calls=%d active_intent=%v execution=%v task_status=%s paused=%v classification=%s call_reason=%s attempts=%d unknown=%d", hits.Load(), provider.calls, plannerProvider.calls, active, view != nil, task.lifecycleSnapshot().Status, task.lifecycleSnapshot().Paused, classification, callReason, attempts, unknown)
	}
	if plannerProvider.calls == 0 {
		t.Fatal("Worker 완료 뒤 원본 Planner 재계획 전에 감독자가 마감함")
	}
	expectedClass := "completed"
	if mode == "tool-failure" {
		expectedClass = "tool_failure"
	}
	if hits.Load() != 1 || provider.result == nil || !provider.result.Trusted.Final || provider.result.Trusted.Classification != expectedClass {
		t.Fatal("실제 소유 대상 도달과 native 프로그램 결과를 확인하지 못함")
	}
	if provider.leaked || provider.unsupportedTool || plannerProvider.outboundToolExposed || plannerProvider.leaked {
		t.Fatalf("실제 엔진의 도구·비밀 경계 위반: worker_unsupported=%v worker_leaked=%v planner_outbound=%v planner_leaked=%v", provider.unsupportedTool, provider.leaked, plannerProvider.outboundToolExposed, plannerProvider.leaked)
	}
	state := task.lifecycleSnapshot()
	goal, err := task.Store.GetNode(goalID)
	if err != nil || goal == nil || goal.State != "open" || !state.Paused || state.Status == "done" {
		t.Fatal("일반 실행 마감을 목표 완료나 E③ 판정으로 바꾸거나 task를 정지하지 않음")
	}
	execution, ok := runtime.host.Runtime().Execution(task.ID)
	if !ok || execution == nil || execution.Snapshot().Counts.Calls != 1 {
		t.Fatal("동일 실행의 실제 HTTP call을 보존하지 못함")
	}
	results := runtime.host.Runtime().Results(task.ID)
	if len(results) != 1 || !results[0].Final || results[0].Classification != expectedClass || results[0].Call.Reason != e2proxy.Complete {
		t.Fatal("실제 trusted result 마감 근거가 보존되지 않음")
	}
	closeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err := runtime.Close(closeCtx); err != nil {
		t.Fatal("실제 마감 뒤 authority·서버 자원 반환 실패")
	}
}

func firstDiagnosisSupervisorModelManifest(t *testing.T, installer *pgdb.DB) string {
	t.Helper()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("생성자 또는 주입 뒤 실행이 fixture 외 실제 모델 호출로 샘")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(model.Close)
	profile := &pgdb.LLMProfile{Name: fmt.Sprintf("owned-supervisor-fixture-%d", time.Now().UnixNano()), Format: "openai", Model: "local:synthetic", BaseURL: model.URL + "/v1", APIKey: "synthetic-owned-model-key", Streaming: false, ContextWindowK: 12}
	id, err := installer.SaveProfile(profile)
	if err != nil || id <= 0 || installer.SetActiveProfile(id) != nil {
		t.Fatal("실제 fixture 모델 profile 사전 설치 실패")
	}
	dir := t.TempDir()
	if os.Chmod(dir, 0700) != nil {
		t.Fatal("모델 계약 디렉터리 보호 실패")
	}
	path := filepath.Join(dir, "models.json")
	body := fmt.Sprintf(`{"version":1,"profiles":[{"profile_id":%d,"format":"openai","model":"local:synthetic","base_url":%q,"addresses":["127.0.0.1"]}]}`, id, profile.BaseURL)
	if os.WriteFile(path, []byte(body), 0600) != nil {
		t.Fatal("실제 profile ID의 보호 모델 계약 공급 실패")
	}
	return path
}

func firstDiagnosisPinFixtureTaskProfile(t *testing.T, installer *pgdb.DB, task *Task) int64 {
	t.Helper()
	profile, err := installer.ActiveProfile()
	if err != nil || profile == nil || profile.ID <= 0 {
		t.Fatal("실제 준비 profile ID 확인 실패")
	}
	if installer.ReplaceTaskLLMProfiles(mustFirstDiagnosisTaskNumber(task.ID), []int64{profile.ID}, profile.ID) != nil {
		t.Fatal("설치자의 실제 단일 task chain 준비 실패")
	}
	row, err := installer.GetTask(mustFirstDiagnosisTaskNumber(task.ID))
	if err != nil || row == nil || len(row.LLMProfileIDs) != 1 || row.LLMProfileIDs[0] != profile.ID {
		t.Fatal("실제 저장된 단일 task chain 확인 실패")
	}
	task.setLLMState(row.LLMProfileID, row.ActiveLLMProfileID, row.LLMProfileIDs, row.LLMChainRevision, row.LLMFailoverState, row.LLMFailoverReason)
	return profile.ID
}

func firstDiagnosisInjectProfileProvider(t *testing.T, server *Server, profileID int64, provider llm.Provider) {
	t.Helper()
	actual, cfg, ok := server.providerForProfile(profileID)
	if !ok || actual == nil || provider == nil {
		t.Fatal("실제 profile builder가 준비되지 않음")
	}
	current, currentOK := server.loadProfileConfig(profileID)
	if !currentOK || current != cfg {
		t.Fatal("실제 DB profile tuple이 cache와 달라짐")
	}
	server.provCacheMu.Lock()
	server.provByProfile[profileID] = &provEntry{prov: provider, cfg: cfg}
	server.provCacheMu.Unlock()
}

type firstDiagnosisRoleRoutedProvider struct {
	worker  llm.Provider
	planner llm.Provider
}

func (p *firstDiagnosisRoleRoutedProvider) Complete(ctx context.Context, request llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	for _, tool := range request.Tools {
		if tool.Name == "Bash" {
			return p.worker.Complete(ctx, request)
		}
	}
	return p.planner.Complete(ctx, request)
}
func (p *firstDiagnosisRoleRoutedProvider) Stream(ctx context.Context, request llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{}, errors.New("unexpected fixture streaming"))
	}
}
