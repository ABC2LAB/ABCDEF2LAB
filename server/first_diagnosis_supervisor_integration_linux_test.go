//go:build linux

package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Autumn-27/norma/llm"
	"io"
	"iter"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	pgdb "github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosis"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/e3contract/e3fake"
	"github.com/Autumn-27/artex/e3contract/e3v3"
	"github.com/Autumn-27/artex/e3contract/e3v3/e2live"
	"github.com/Autumn-27/artex/internal/diagnosticlog"
	"github.com/Autumn-27/artex/transmit"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/sys/unix"
)

func firstDiagnosisSupervisorConstructorClose(t *testing.T, manager *Manager, installer *pgdb.DB, task *Task, checks ...func(*FirstDiagnosisHTTPRuntime)) {
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
	for _, check := range checks {
		check(runtime)
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
	// 작업 뒤 목표 달성 순서를 쓰는 모드다. goal-met-unmatched-limit는 같은 순서에 E② 근거 없는 정책 제한 intent를 하나
	// 더 둬서, 목표 달성 마감에서도 근거 대조 실패는 마감 실패이고 그 사유 코드가 진단 로그에 남는지 본다.
	goalAfterWork := mode == "goal-met-after-work" || mode == "goal-met-unmatched-limit" || mode == "goal-met-auth-unknown"
	// 한 intent가 목표를 증명하는 순간 다른 Worker의 Bash 명령이 아직 진행 중인 모드다(실제 회차 24b). denied는 그 명령의
	// 시도가 모두 보내지 않은 거부, mid-response는 그 명령이 보낸 요청의 응답을 받는 도중이다.
	goalConcurrent := mode == "goal-met-concurrent-denied" || mode == "goal-met-concurrent-mid-response"
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	e3Logged := mode == "e3-baseline" || mode == "e3-hook" || mode == "e3-hook-disabled"
	if mode == "cancel" || mode == "user-stop" || mode == "pause" || mode == "engine-pause" || mode == "close-fail" || mode == "plan-turns" || goalAfterWork || goalConcurrent || e3Logged {
		logger, err := diagnosticlog.Create(firstDiagnosisPrivateTemp(t), "run", mustFirstDiagnosisTaskNumber(task.ID), "")
		if err != nil {
			t.Fatal(err)
		}
		ctx = diagnosticlog.With(ctx, logger)
		defer func() {
			if err := logger.Close(); err != nil {
				t.Error("실제 회차 로그 마감", err)
				return
			}
			if mode == "plan-turns" {
				verifyPlannerTodoLog(t, logger.Path())
				return
			}
			if e3Logged {
				firstDiagnosisE3SupervisorFlows.Store(mode, supervisorFlowLog(t, logger.Path()))
				return
			}
			verifySupervisorFailureLog(t, logger.Path(), mode)
		}()
	}
	var hits atomic.Int64
	targetEntered := make(chan struct{}, 1)
	targetExited := make(chan struct{}, 1)
	// 대상이 붙잡은 요청을 받은 시각·놓은 시각이다(시간 상한 모드가 진행 중 요청이 요청 시간 상한 전에 끊겼는지 본다).
	var targetEnteredAt, targetExitedAt atomic.Int64
	releaseTarget := make(chan struct{})
	// 목표를 증명할 intent의 요청은 다른 Worker의 명령이 자리를 잡을 때까지 대상에서 붙잡는다. 두 명령이 실제로 겹쳤는지
	// overlapped로 남긴다.
	concurrentGate := make(chan struct{})
	openConcurrentGate := sync.OnceFunc(func() { close(concurrentGate) })
	var overlapped atomic.Bool
	login := &authLoginTarget{token: "goal-met-owned-session-77cc"}
	loginHandler := login.handler(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if goalAfterWork && r.URL.Path == "/login" {
			// 서버가 실행을 잡을 때 시험 계정마다 보내는 인증 준비 로그인이다(C14). 진단 요청 수와 따로 센다.
			if mode == "goal-met-auth-unknown" {
				login.posts.Add(1)
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					conn.Close()
				}
				return
			}
			loginHandler.ServeHTTP(w, r)
			return
		}
		hits.Add(1)
		if goalConcurrent {
			switch r.URL.Query().Get("owned") {
			case "hold":
				// 다른 Worker의 요청: 상태 줄·헤더와 본문 앞부분만 보내고 붙잡는다(응답 수신 도중).
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Content-Length", "4096")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"owned_fixture":`))
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				select {
				case targetEntered <- struct{}{}:
					targetEnteredAt.CompareAndSwap(0, time.Now().UnixNano())
				default:
				}
				openConcurrentGate()
				select {
				case <-r.Context().Done():
					select {
					case targetExited <- struct{}{}:
						targetExitedAt.CompareAndSwap(0, time.Now().UnixNano())
					default:
					}
				case <-releaseTarget:
				}
				return
			case "gate":
				select {
				case <-concurrentGate:
					overlapped.Store(true)
				case <-r.Context().Done():
					return
				}
			}
		}
		if mode == "planner-kill-mid-response" {
			// 상태 줄·헤더와 본문 앞부분만 보내고 나머지를 붙잡는다. E②는 응답을 받는 중이다(응답 수신 도중 Planner kill).
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Length", "4096")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"owned_fixture":`))
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		if mode == "cancel" || mode == "user-stop" || mode == "pause" || mode == "engine-pause" || mode == "planner-kill" || mode == "time-budget" || mode == "planner-kill-mid-response" {
			select {
			case targetEntered <- struct{}{}:
				targetEnteredAt.CompareAndSwap(0, time.Now().UnixNano())
			default:
			}
			select {
			case <-r.Context().Done():
				select {
				case targetExited <- struct{}{}:
					targetExitedAt.CompareAndSwap(0, time.Now().UnixNano())
				default:
				}
			case <-releaseTarget:
			}
			return
		}
		if r.URL.Path != "/allowed" {
			t.Error("생산 Engine의 범위 밖 요청이 대상에 도달함")
		}
		if mode == "result-unknown" {
			// 요청을 받은 뒤 응답 없이 연결을 끊는다. E②는 대상이 요청을 처리했는지 알 수 없다(결과 불명).
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Error("소유 대상 연결 끊기 불가")
				return
			}
			conn, _, err := hijacker.Hijack()
			if err != nil {
				t.Error("소유 대상 연결 끊기 실패", err)
				return
			}
			_ = conn.Close()
			return
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
	ruleOrigin := target.URL
	refusedOrigin := ""
	if mode == "planner-kill-refused" {
		// 승인했지만 아무도 듣지 않는 포트다. E②는 연결을 시도하고(forward_attempted) 연결이 거부돼 송신 실패로 끝난다.
		closed, listenErr := net.Listen("tcp", "127.0.0.1:0")
		if listenErr != nil {
			t.Fatal("닫힌 포트 fixture 준비 실패", listenErr)
		}
		refusedOrigin = "http://" + closed.Addr().String()
		if err := closed.Close(); err != nil {
			t.Fatal("닫힌 포트 fixture 정리 실패", err)
		}
		policy.Rules = append(policy.Rules, E2HTTPFileRule{Ref: "owned-refused-rule", Origin: refusedOrigin, ConnectIP: "127.0.0.1", PathPrefix: "/allowed", PathScopeRef: "owned-refused-path", Methods: []string{"GET"}})
	}
	if mode == "insert-assets" {
		// 하위 도메인 이름의 승인 origin(연결은 고정 IP). 원본 upsert가 그 루트 도메인을 작업에 붙이지 않는지 본다(C24).
		targetURL, parseErr := url.Parse(target.URL)
		if parseErr != nil {
			t.Fatal("대상 주소 해석 실패")
		}
		ruleOrigin = "http://app.owned-assets.test:" + targetURL.Port()
		policy.Rules[0].Origin = ruleOrigin
	}
	policy.Limits.CallTimeoutMS = 10000
	policy.Limits.RequestTimeoutMS = 5000
	policy.Proxy.RequestTimeoutMS = 5000
	policy.Runtime.WorkerTimeoutMS = 15000
	policy.Runtime.MaxWorkerTurns = 4
	policy.Runtime.MaxPlannerRounds = 4
	if mode == "planner-limit" {
		// 도구 호출 하나만 허용한다. 작업 뒤 Planner 준비가 실행 상한에 닿는다(실행 결과 상태 값 계약 budget_limited).
		policy.Runtime.MaxToolCalls = 1
	}
	if mode == "request-budget" || mode == "request-budget-overflow" {
		// 승인 요청 예산을 한 번으로 둔다. 작업이 그 한 번을 쓰면 남은 E② 예산이 0이 된다. 예산 소진은 실패가 아니라
		// 실행 상한 마감이다(계약 §3.1 "승인된 요청·시간·비용 예산 소진", budget_limited).
		policy.MaxRequests = 1
	}
	if goalConcurrent {
		policy.MaxRequests = 8
		policy.Limits.MaxAttempts = 10
		policy.Limits.MaxCalls = 8
		policy.Runtime.MaxToolCalls = 8
		policy.Runtime.MaxPlannerRounds = 8
		// Bash 자리는 min(Worker 수, 동시 송신 상한)이다(#598). 제품 기본 Worker 3에 맞춰 3자리가 되게 한다.
		policy.Limits.MaxConcurrent = firstDiagnosisConcurrentBashSlots
	}
	if goalAfterWork {
		if mode == "goal-met-auth-unknown" {
			policy.Limits.MaxUnknown = 4
		}
		// 세 intent의 작업이 모두 끝난 뒤 Planner가 목표 달성을 판정해야 한다. 그 전에 실행 상한(요청 예산·도구 호출·
		// Planner 라운드)에 닿으면 목표 달성이 아니라 budget_limited 마감이 되므로 상한을 넉넉히 둔다.
		policy.MaxRequests = 8
		policy.Limits.MaxAttempts = 10
		policy.Limits.MaxCalls = 8
		policy.Runtime.MaxToolCalls = 8
		policy.Runtime.MaxPlannerRounds = 8
		// 실제 회차처럼 시험 계정 둘을 결속한다. 서버가 실행을 잡을 때 계정마다 로그인하고(프로그램 호출), Worker는 시험
		// 계정 세션 헤더를 그대로 보낼 수 있다.
		policy.Rules = append(policy.Rules, E2HTTPFileRule{Ref: "auth-login", Origin: target.URL, ConnectIP: "127.0.0.1", PathPrefix: "/login", PathScopeRef: "auth-login", Methods: []string{"POST"}, Purpose: e2proxy.PurposeAuthPrep})
		policy.HeaderMode = e2proxy.HeaderTestAccountPassthrough
		secondUser := "tester@owned.test"
		if mode == "goal-met-auth-unknown" {
			secondUser = "tester-b@owned.test"
		}
		auth := cookieAuth(
			E2HTTPFileAuthAccount{Label: "tester-a", Credential: authCredentialPin(t, cfg.AuthorityRoot, "tester-a.json", "tester@owned.test", "seeded-pass-88ff")},
			E2HTTPFileAuthAccount{Label: "tester-b", Credential: authCredentialPin(t, cfg.AuthorityRoot, "tester-b.json", secondUser, "seeded-pass-88ff")},
		)
		policy.Auth = &auth
	}
	// 설치 입력의 Plan당 턴 상한(C26·C32)은 launch 설정에서 host 설정으로 들어온다.
	const installedPlanTurns = 2
	if mode == "plan-turns" {
		cfg.PlannerLimits = agent.E2HTTPPlannerLimits{MaxTurns: installedPlanTurns}
	}
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
	if mode == "exploration-fail" {
		// 탐색 단계가 실패하면 계획 준비가 planning_exploration_failed로 실패하고 엔진이 작업을 실패로 표시한다(NodeGoat 회차 1).
		runtime.host.runtime.options.Exploration = func(context.Context, *Task) (ExplorationCharge, error) {
			return ExplorationCharge{}, errors.New("synthetic exploration failure")
		}
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
		if err := runtime.Close(closeCtx); err != nil && !(((mode == "cancel" || mode == "user-stop" || mode == "pause" || mode == "engine-pause") || mode == "prepare-fail" || mode == "exploration-fail" || mode == "pre-run-stop" || mode == "goal-met-unmatched-limit") && errors.Is(err, ErrFirstDiagnosisOutcome) || mode == "close-fail" && errors.Is(err, ErrE2HTTPClosure)) {
			t.Error("실제 Engine 경로 최종 종료 실패")
		}

	}()
	goalID, err := task.Store.AddNode(pgdb.KindGoal, map[string]any{"text": "Keep the security goal open until a separate trusted verdict exists."}, 0, "open", "system", nil)
	if err != nil || goalID <= 0 {
		t.Fatal("실제 미판정 목표 fixture 생성 실패")
	}
	path := "/allowed"
	if mode == "denied" || mode == "partial-denied" {
		path = "/forbidden"
	}
	provider := &firstDiagnosisE2Provider{command: "curl --silent --show-error --max-time 5 " + target.URL + path}
	if mode == "partial-denied" {
		provider.command += "; curl --silent --show-error --max-time 5 " + target.URL + "/allowed"
	}
	if mode == "unsupported" {
		provider.command = "curl --silent --show-error --max-time 5 -X CONNECT " + target.URL + "/allowed"
	}
	if mode == "tool-failure" {
		provider.command += " | grep -q never-present-owned-fixture"
	}
	if mode == "command-timeout" {
		// 대상은 바로 답하고, 명령은 호출 시간 상한을 넘겨 호스트가 멈춘다(결과 불명 송신 없음).
		provider.command += "; sleep 30"
	}
	switch mode {
	case "planner-kill-denied":
		// 정책 거부(보내지 않음) 뒤 명령이 아직 돈다. 명령 시간 상한(5초) 전에 Planner가 kill_work로 멈춘다.
		provider.command = "curl --silent --show-error --max-time 5 " + target.URL + "/forbidden; sleep 30"
	case "planner-kill-refused":
		// 연결을 시도했지만 연결 전에 거부된 송신 뒤 명령이 아직 돈다. 전달하지 않은 송신 실패라 그 intent의 미완료다(C02).
		provider.command = "curl --silent --show-error --max-time 5 " + refusedOrigin + "/allowed; sleep 30"
	case "planner-kill-mid-response":
		// 정책 거부 하나 뒤의 요청이 응답을 받는 도중 멈춘다. 거부가 있어도 그 intent는 정책 제한이 아니라 결과 불명이다.
		provider.command = "curl --silent --show-error --max-time 5 " + target.URL + "/forbidden; curl --silent --show-error --max-time 5 " + target.URL + "/allowed"
	}
	if mode == "request-budget-overflow" {
		// 남은 예산이 하나인데 한 명령에서 두 번 보낸다. 둘째 요청은 E②가 예산 소진으로 보내지 않는다.
		provider.command += "; curl --silent --show-error --max-time 5 " + target.URL + "/allowed"
	}
	plannerProvider := &firstDiagnosisPlannerProbeProvider{firstDiagnosisE2Provider: firstDiagnosisE2Provider{command: "curl --silent --show-error --max-time 1 " + target.URL + "/denied"}, textOnly: mode != "planner-only"}
	if mode == "plan-turns" {
		plannerProvider.loopTool = "list_goals"
		// SDK가 붙이는 TodoWrite를 한 번 불러 진단 기록에 등록 도구로 남는지 본다(C26).
		plannerProvider.todoOnce = true
	}
	var otherHits atomic.Int64
	workerCallsAtScope := -1
	if mode == "task-scope" {
		// Planner의 범위 도구는 커버리지 분모만 바꾸고 E② 승인 정책은 바꾸지 않는다(C26, 루프 복원 결정안 §4).
		// Planner가 대상 호스트 IP 전체를 작업 범위에 넣은 뒤에도, 같은 호스트의 승인 안 된 포트로 가는 진단 요청은
		// E②가 보내지 않아야 한다.
		other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { otherHits.Add(1) }))
		defer other.Close()
		otherURL, parseErr := url.Parse(other.URL)
		if parseErr != nil {
			t.Fatal("범위 밖 fixture 주소 해석 실패")
		}
		plannerProvider.scopeValue = otherURL.Hostname()
		plannerProvider.beforeScope = func() { workerCallsAtScope = provider.calls }
		provider.command = "curl --silent --show-error --max-time 5 " + other.URL + "/allowed"
	}
	approvedAsset, otherAsset := ruleOrigin+"/api/items", ""
	if mode == "insert-assets" {
		// 진단 Worker의 insert_assets(C24): 승인 origin 안의 자산만 실제 runtime 역할로 기록하고, 같은 호스트의 다른 포트
		// 자산은 기록하지 않는다.
		other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		defer other.Close()
		otherURL, parseErr := url.Parse(other.URL)
		if parseErr != nil {
			t.Fatal("범위 밖 fixture 주소 해석 실패")
		}
		otherAsset = "http://app.owned-assets.test:" + otherURL.Port() + "/api/items"
		provider.assets = []map[string]any{{"type": "endpoint", "url": approvedAsset, "method": "GET"}, {"type": "endpoint", "url": otherAsset, "method": "GET"}}
		provider.command = "curl --silent --show-error --max-time 5 " + ruleOrigin + "/allowed"
	}
	var killIntent atomic.Int64
	plannerKill := mode == "planner-kill" || mode == "planner-kill-denied" || mode == "planner-kill-refused" || mode == "planner-kill-mid-response"
	if plannerKill {
		// Worker의 요청이 대상에 붙잡혀 있는 동안(또는 거부·송신 실패 뒤 명령이 아직 도는 동안) Planner가 깨어나
		// kill_work로 그 작업을 멈춘다(C27).
		plannerProvider.killIntent = killIntent.Load
	}
	if mode == "goal-met" {
		// Planner가 prove_goal로 하나뿐인 목표를 달성시키면 작업이 done이 되고 실행은 정상 마감된다(C26).
		factID, err := task.Store.AddNode(pgdb.KindFact, map[string]any{"text": "Owned fixture evidence for the goal."}, 0, "confirmed", "system", nil)
		if err != nil || factID <= 0 {
			t.Fatal("목표 근거 fixture 생성 실패")
		}
		plannerProvider.goalID, plannerProvider.evidenceID = goalID, factID
	}
	var worker llm.Provider = provider
	var intentWorker *firstDiagnosisIntentWorker
	if goalAfterWork {
		// 실제 회차 순서: Worker들이 정책 거부가 섞인 작업과 정상 작업을 끝낸 뒤에야 Planner가 모든 목표를 달성으로
		// 판정한다. 엔진은 원본대로 작업을 done으로 두고 실행 문맥을 취소한다(AbortGoalMet). 그 뒤 감독자가 마감한다.
		factID, err := task.Store.AddNode(pgdb.KindFact, map[string]any{"text": "Owned fixture evidence for the goal."}, 0, "confirmed", "system", nil)
		if err != nil || factID <= 0 {
			t.Fatal("목표 근거 fixture 생성 실패")
		}
		plannerProvider.goalID, plannerProvider.evidenceID = goalID, factID
		plannerProvider.proveWhen = func() bool {
			var pending int
			if err := installer.QueryRowContext(ctx, "SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND kind='intent' AND state IN ('open','running')", task.ExpID).Scan(&pending); err != nil {
				return false
			}
			return pending == 0
		}
		intentWorker = &firstDiagnosisIntentWorker{steps: map[string][]string{}}
		for _, work := range firstDiagnosisGoalMetWork {
			for _, step := range work.steps {
				intentWorker.steps[work.marker] = append(intentWorker.steps[work.marker], strings.ReplaceAll(step, "{T}", target.URL))
			}
		}
		worker = intentWorker
		if mode == "goal-met-unmatched-limit" {
			// 실행 전에 이미 정책 제한으로 표시된 intent다. Worker가 집지 않고, 이 intent의 E② 거부 호출은 없다.
			unmatched, err := task.Store.AddIntent(map[string]any{"summary": "Owned goal-met unmatched limitation"}, 1, nil, "system")
			if err != nil || task.Store.SetIntentBlockedReason(unmatched, "e2_http_policy_limited") != nil {
				t.Fatal("근거 없는 제한 intent fixture 생성 실패")
			}
		}
	}
	var proveReady atomic.Bool
	if goalConcurrent {
		// 시험이 두 명령의 겹침을 확인한 뒤에만 Planner가 목표를 증명한다(prove_goal → 엔진 목표 달성 취소).
		factID, err := task.Store.AddNode(pgdb.KindFact, map[string]any{"text": "Owned fixture evidence for the goal."}, 0, "confirmed", "system", nil)
		if err != nil || factID <= 0 {
			t.Fatal("목표 근거 fixture 생성 실패")
		}
		plannerProvider.goalID, plannerProvider.evidenceID = goalID, factID
		plannerProvider.proveWhen = proveReady.Load
		intentWorker = &firstDiagnosisIntentWorker{steps: map[string][]string{}}
		for _, work := range firstDiagnosisGoalConcurrentWork(mode) {
			for _, step := range work.steps {
				intentWorker.steps[work.marker] = append(intentWorker.steps[work.marker], strings.ReplaceAll(step, "{T}", target.URL))
			}
		}
		worker = intentWorker
	}
	// 실제 taskLLMRuntime.current의 role/model guard를 유지하고 profile cache의 모델만 대체한다.
	// 작업이 끝난 뒤 남은 작업 없이 하는 Planner 호출(빈 계획)을 센다. 원본과 같은 끝 규칙에서는 그 뒤에도 실행이 마감되지
	// 않는다.
	idlePlanner := firstDiagnosisIdleWatch(installer, task, plannerProvider)
	firstDiagnosisInjectProfileProvider(t, runtime.server, profileID, &firstDiagnosisRoleRoutedProvider{worker: worker, planner: idlePlanner})
	// E③ 확정 단계 훅(결정 §5): 모델 활동이 모두 끝난 뒤(진행 중 작업·모델 호출 0, 실행 허가 닫힘), E② 마감 전에 한 번
	// 불린다. 확정 단계가 실제로 시작할 때만(begin) 엔진 정리 상태가 된다. e3-hook-error는 훅이 실패해도 모델 단계 실행 결과가
	// 그대로인지, e3-hook-disabled는 꺼진 확정 단계(선언 없음·연결 없음)가 begin을 부르지 않아 감독자 흐름이 E③ 없는 실행과
	// 같은지 본다(Go 조건 2).
	e3Hooked := mode == "e3-hook" || mode == "e3-hook-error" || mode == "e3-hook-disabled"
	// 중지 마감(cancel·user-stop)에서도 E③ 결과가 남도록 중지 기록 훅이 한 번 불리는지 본다(#738 이동찬 리뷰).
	// 마감 경로 관찰(사용자 결정 2026-11-20): 확정 단계 훅(Run)·중지 기록(Stopped)이 불린 때의 E② 연결 사유·모델 단계
	// 마감 여부와 그때까지의 대상 요청 수를 남긴다. 실행 뒤 경로별 기대와 "모델 단계 마감 뒤 모델 단계 송신 0"을 본다.
	var closeProbe struct {
		runCalls, stopCalls int
		handle              string
		modelClosed         bool
		hitsAtClose         int64
	}
	probeClose := func(taskID string, stopped bool) {
		if stopped {
			closeProbe.stopCalls++
		} else {
			closeProbe.runCalls++
		}
		exec, _, reason := runtime.E3HandleReason(taskID)
		closeProbe.handle, closeProbe.modelClosed, closeProbe.hitsAtClose = reason, exec != nil && exec.ModelStageClosed(), hits.Load()
	}
	e3StopHooked := mode == "cancel" || mode == "user-stop" || mode == "pause" || mode == "engine-pause" || mode == "pre-run-stop"
	e3Calls, e3State := 0, ""
	var e3StoppedCalls atomic.Int64
	var e3StoppedCtxLive atomic.Bool
	stopRecord := func(hookCtx context.Context, taskID string) error {
		if taskID == task.ID {
			e3StoppedCalls.Add(1)
			probeClose(taskID, true)
		}
		e3StoppedCtxLive.Store(hookCtx.Err() == nil)
		return nil
	}
	if e3StopHooked {
		if err := supervisor.SetE3Confirmation(FirstDiagnosisE3Hooks{Run: func(_ context.Context, taskID string, _ func()) error {
			e3Calls++
			probeClose(taskID, false)
			return nil
		}, Stopped: stopRecord}); err != nil {
			t.Fatal("E③ 중지 기록 훅 설정 실패", err)
		}
	}
	// 확정 단계 훅 때 E② 실행 연결(E3Handle)을 꺼낼 수 있는지 본다. 이 픽스처는 시험 계정을 결속하지 않는다. 그래도 연결이
	// 있어야 제품 확정 단계가 어댑터를 붙여, 공식 회차처럼 계정·선언이 없는 회차가 not_wired 대신 실제 사유를 낸다(Go 조건 2).
	var e3HandleOK bool
	if e3Hooked {
		if err := supervisor.SetE3Confirmation(FirstDiagnosisE3Hooks{Stopped: stopRecord, Run: func(hookCtx context.Context, taskID string, begin func()) error {
			e3Calls++
			probeClose(taskID, false)
			exec, _, ok := runtime.E3Handle(taskID)
			e3HandleOK = ok && exec != nil
			if mode != "e3-hook-disabled" {
				begin()
			}
			r := runtime.host.runtime
			r.mu.Lock()
			current := r.task
			r.mu.Unlock()
			fenced := current == nil
			if current != nil {
				current.mu.Lock()
				fenced = current.closed
				current.mu.Unlock()
			}
			supervisor.mu.Lock()
			admission := supervisor.admissionGranted
			supervisor.mu.Unlock()
			e3State = fmt.Sprintf("task=%v inflight=%d llm=%d admission=%v fenced=%v settling=%v ctx=%v", taskID == task.ID,
				runtime.server.engine.inflightCount(taskID), runtime.server.engine.ActiveLLMCalls(taskID), admission, fenced,
				runtime.server.engine.isSettling(taskID), hookCtx.Err() == nil)
			if mode == "e3-hook-error" {
				return errors.New("owned e3 confirmation fixture failure")
			}
			return nil
		}}); err != nil {
			t.Fatal("E③ 확정 단계 훅 설정 실패", err)
		}
	}
	// 목표 달성으로 끝나는 실행에서도 확정 단계 훅 때 E② 실행이 살아 있어야 한다. #805 뒤 실제 제품 합성 회차(목표 달성
	// 마감)는 엔진의 목표 달성 취소가 E② 작업을 먼저 닫아(FenceTask: 작업 문맥 취소 → 실행 문맥 취소) 확정 단계가 늘
	// not_wired였다. 가짜 handle이 아니라 이 실제 runtime의 E3HandleReason → 제품과 같은 지연 어댑터(e3v3.LazyE2, 실제
	// 브리지 e2live.NewLive) → e3contract.Run까지 간다. 선언 없음은 declaration_missing(브리지 0), 선언 있음은 어댑터가 붙어
	// 표식 등록에서 실제 실행으로 브리지를 만든다(이 픽스처의 E②는 v3 Host가 없어 브리지가 거부 → marker_registration_failed).
	// 훅은 감독자 begin을 부르지 않아 감독자 흐름은 그대로다.
	var goalE3Calls int
	var goalE3State string
	// 시간 상한 마감(유예 뒤 진행 중 작업 취소)에서도 확정 단계 훅 때 E② 실행이 살아 있어야 한다. 2026-11-20 실제 모델
	// VAmPI 회차는 시간 상한 마감에서 유예 끝 취소가 E② 작업을 먼저 닫아(task_fenced) 확정 단계가 not_wired였다.
	var timeE3Calls int
	var timeE3Reason string
	if mode == "time-budget" {
		if err := supervisor.SetE3Confirmation(FirstDiagnosisE3Hooks{Stopped: stopRecord, Run: func(hookCtx context.Context, taskID string, _ func()) error {
			timeE3Calls++
			probeClose(taskID, false)
			exec, _, reason := runtime.E3HandleReason(taskID)
			timeE3Reason = fmt.Sprintf("handle=%q exec=%v ctx=%v", reason, exec != nil, hookCtx.Err() == nil)
			return nil
		}}); err != nil {
			t.Fatal("시간 상한 E③ 확정 단계 훅 설정 실패", err)
		}
	}
	if mode == "goal-met-after-work" {
		if err := supervisor.SetE3Confirmation(FirstDiagnosisE3Hooks{Stopped: stopRecord, Run: func(hookCtx context.Context, taskID string, _ func()) error {
			goalE3Calls++
			probeClose(taskID, false)
			goalE3State = firstDiagnosisRealE3Hook(t, hookCtx, runtime, taskID)
			return nil
		}}); err != nil {
			t.Fatal("목표 달성 E③ 확정 단계 훅 설정 실패", err)
		}
	}
	if !e3StopHooked && !e3Hooked && mode != "time-budget" && mode != "goal-met-after-work" {
		if err := supervisor.SetE3Confirmation(FirstDiagnosisE3Hooks{Stopped: stopRecord, Run: func(_ context.Context, taskID string, _ func()) error {
			probeClose(taskID, false)
			return nil
		}}); err != nil {
			t.Fatal("마감 경로 관찰 훅 설정 실패", err)
		}
	}
	if mode == "pre-run-stop" {
		hash, err := runtime.server.reloadPassHash()
		if err != nil {
			t.Fatal(err)
		}
		token, err := signJWT(runtime.server.signingKey(hash))
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/api/tasks/"+task.ID+"/request-approvals/stop", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		runtime.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("실행 전 중지 API: %d", rec.Code)
		}
		err = supervisor.Run(ctx, task.ID, 0, "")
		state := runtime.server.requestApprovals.runState(mustFirstDiagnosisTaskNumber(task.ID))
		if !errors.Is(err, context.Canceled) || state.Phase != "closed" || !state.DrainComplete || state.Outcome != "cancelled" || hits.Load() != 0 || provider.calls != 0 || plannerProvider.calls != 0 {
			t.Fatalf("실행 전 중지 마감 실패: state=%+v hits=%d", state, hits.Load())
		}
		// 실행에 들어가기 전에 중지돼도 E③ 결과가 남도록 중지 기록이 한 번 불린다(대상 요청 0, 확정 단계 0, #738 이동찬 리뷰).
		if e3Calls != 0 || e3StoppedCalls.Load() != 1 || !e3StoppedCtxLive.Load() {
			t.Fatalf("실행 전 중지의 E③ 기록: 확정 %d·중지 기록 %d·문맥 %v", e3Calls, e3StoppedCalls.Load(), e3StoppedCtxLive.Load())
		}
		return
	}
	var runErr error
	if mode == "cancel" || mode == "user-stop" || mode == "pause" || mode == "engine-pause" {
		runCtx, stopRun := context.WithCancel(ctx)
		defer stopRun()
		done := make(chan error, 1)
		go func() { done <- supervisor.Run(runCtx, task.ID, 0, "") }()
		select {
		case <-targetEntered:
		case <-ctx.Done():
			t.Fatal("취소 인수의 실제 대상 요청이 시작되지 않음")
		}
		if mode == "pause" {
			// 제품 경로의 일시정지는 회차를 바로 끝내는 운영자 중지다(사용자 결정 2026-11-20 분기점 1).
			code, body := firstDiagnosisControlRequest(t, runtime, task.ID, "pause")
			if code != 200 || !strings.Contains(body, `"status":"stopping"`) {
				t.Fatalf("일시정지가 중지로 처리되지 않음: %d %s", code, body)
			}
		} else if mode == "engine-pause" {
			// 운영자 control이 아닌 엔진 일시정지는 회차 도중 생기는 경로가 없다(일시정지 원인 표). 방어 경로가 운영자 중지와
			// 섞이지 않는지 직접 걸어 본다. 그 전에 회차 도중 엔진 일시정지 원인들이 닫혀 있는지 이 실제 서버에서 확인한다.
			if runtime.server.engine.controlledReads != nil {
				t.Fatal("E② HTTP 첫 진단 서버에 통제 조회 대기(paused_by_orchestrator) 경로가 열려 있음")
			}
			if on, _ := runtime.server.m.ConcurrencyLimit(); on {
				t.Fatal("첫 진단 서버에 동시 실행 제한이 켜져 있음(llm_unavailable_queued 경로)")
			}
			if !runtime.server.firstDiagnosis || runtime.server.engine.IsPaused(task.ID) {
				t.Fatal("회차 도중 엔진이 이미 일시정지됨")
			}
			runtime.server.engine.Pause(task.ID, agent.AbortPausedByOrchestrator)
		} else if mode == "user-stop" {
			hash, err := runtime.server.reloadPassHash()
			if err != nil {
				t.Fatal(err)
			}
			token, err := signJWT(runtime.server.signingKey(hash))
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", "/api/tasks/"+task.ID+"/request-approvals/stop", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			runtime.Handler().ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Fatalf("사용자 중지 API 실패: %d %s", rec.Code, rec.Body.String())
			}
		} else {
			stopRun()
		}
		select {
		case runErr = <-done:
		case <-ctx.Done():
			t.Fatal("실제 Supervisor Run 취소가 반환하지 않음")
		}
	} else if plannerKill {
		done := make(chan error, 1)
		go func() { done <- supervisor.Run(ctx, task.ID, 0, "") }()
		if mode == "planner-kill-denied" || mode == "planner-kill-refused" {
			// 대상에는 아무것도 닿지 않는다. E② 기록에 거부·송신 실패 시도가 남으면(명령은 sleep 중) 멈춘다.
			firstDiagnosisWaitForCall(t, ctx, runtime, task, func(c e2proxy.CallResult) bool {
				if mode == "planner-kill-denied" {
					return c.Counts.Denied > 0
				}
				return c.Counts.Transient > 0
			})
		} else {
			select {
			case <-targetEntered:
			case <-ctx.Done():
				t.Fatal("Planner 동시 실행 시험의 실제 대상 요청이 시작되지 않음")
			}
		}
		// Worker의 Bash가 대상에서 붙잡혀 있다. 그 intent를 Planner에 알리고 Planner를 깨운다. C27 전에는 이 라운드가
		// 실행 중 작업 때문에 건너뛰어진다.
		var running int64
		if err := installer.QueryRowContext(ctx, "SELECT id FROM exploration_nodes WHERE exploration_id=$1 AND kind='intent' AND state='running'", task.ExpID).Scan(&running); err != nil {
			t.Fatal("실행 중 intent 조회 실패", err)
		}
		killIntent.Store(running)
		task.Notify()
		// Planner가 멈춘 작업은 Worker 완료 알림을 내지 않아 원본처럼 다음 알림·심장박동까지 다시 계획하지 않는다. 남은 작업이
		// 없어도 마감하지 않고, 실행 상한에 닿아야 마감한다.
		runErr = firstDiagnosisRunPastIdle(t, ctx, supervisor, task, func() bool {
			pending, total, err := idlePlanner.intents(ctx)
			return err == nil && total > 0 && pending == 0
		}, done)
	} else if goalConcurrent {
		done := make(chan error, 1)
		go func() { done <- supervisor.Run(ctx, task.ID, 0, "") }()
		// 다른 Worker의 명령이 자리를 잡는다: 거부 시도가 기록된 채 명령이 sleep 중이거나, 보낸 요청의 응답을 받는 중이다.
		if mode == "goal-met-concurrent-denied" {
			firstDiagnosisWaitForCall(t, ctx, runtime, task, func(c e2proxy.CallResult) bool { return c.Counts.Denied > 0 && !c.Final })
			openConcurrentGate()
		} else {
			select {
			case <-targetEntered:
			case <-ctx.Done():
				t.Fatal("다른 Worker의 요청이 대상에 붙잡히지 않음")
			}
		}
		// 목표를 증명할 intent의 작업이 끝나기를 기다린다. 그동안 다른 Worker의 명령은 계속 돈다.
		slowIntent := firstDiagnosisWaitIntentStates(t, ctx, installer, task)
		// 증명 직전에 다른 Worker의 E② 호출이 아직 열려 있어야 한다(명령 진행 중).
		busyAtProve := false
		if execution, ok := runtime.host.Runtime().Execution(task.ID); ok && execution != nil {
			for _, call := range execution.Calls() {
				busyAtProve = busyAtProve || call.IntentID == slowIntent && !call.Final
			}
		}
		if !busyAtProve || !overlapped.Load() {
			t.Fatalf("목표 증명 때 두 명령이 겹치지 않음 busy=%v overlapped=%v", busyAtProve, overlapped.Load())
		}
		proveReady.Store(true)
		task.Notify()
		select {
		case runErr = <-done:
		case <-ctx.Done():
			t.Fatal("목표 달성 뒤 Supervisor Run이 반환하지 않음")
		}
	} else if mode == "time-budget" {
		// 회차 실행 시간 상한(계약 §3.4): 마감 시작 시각이 오면 새 계획·새 intent 없이, 대상에 붙잡힌 작업은 유예 뒤
		// 멈추고 실행을 budget_limited로 마감한다. 시작 시각은 Worker 요청이 대상에 붙잡힌 뒤에 오도록 둔다.
		const timeBudgetStart, timeBudgetDrain = 3 * time.Second, 300 * time.Millisecond
		if err := supervisor.SetTimeBudget(time.Now().Add(timeBudgetStart), timeBudgetDrain); err != nil {
			t.Fatal("시간 상한 설정 실패", err)
		}
		started := time.Now()
		runErr = supervisor.Run(ctx, task.ID, 0, "")
		// 유예 뒤 진행 중 작업을 멈췄다면 대상이 붙잡은 요청은 E② 요청 시간 상한 전에 끊긴다. 대상이 요청을 붙잡은 시간을
		// 직접 본다. Run 전체 시간은 정리·감사 마감을 포함해 부하에 따라 흔들리므로(race 부하에서 5초를 넘긴 적 있음,
		// 2026-11-20 dev·새 판 비교에서 분포가 같았다) 느슨한 상한만 둔다.
		// 기대 끊김 시점은 시간 상한 시작 + 유예이고, 끊지 않으면 E② 요청 시간 상한에 끊긴다. 그 중간보다 먼저 끊겨야 한다.
		requestTimeout := time.Duration(policy.Limits.RequestTimeoutMS) * time.Millisecond
		drainAt := timeBudgetStart + timeBudgetDrain
		cutBy := drainAt + (requestTimeout-drainAt)/2
		entered, exited := targetEnteredAt.Load(), targetExitedAt.Load()
		if entered == 0 || exited == 0 || time.Duration(exited-started.UnixNano()) >= cutBy {
			t.Fatalf("시간 상한 유예 뒤 진행 중 요청을 끊지 않음(E② 요청 시간 상한 쪽으로 감) cut=%s by=%s",
				time.Duration(exited-started.UnixNano()), cutBy)
		}
		if elapsed := time.Since(started); elapsed >= 2*requestTimeout {
			t.Fatalf("시간 상한 마감이 너무 늦음 elapsed=%s", elapsed)
		}
	} else if mode == "planner-only" || mode == "plan-turns" {
		// 작업이 없는 실행이다. 계획이 비어도 마감하지 않고(원본과 같은 끝 규칙), 시간 상한에서 no_intents·budget_limited로
		// 마감한다. 시간 상한 전에 돌아오면 빈 계획으로 마감한 것이다.
		closeAt := time.Now().Add(firstDiagnosisNoWorkTimeBudget)
		if err := supervisor.SetTimeBudget(closeAt, 0); err != nil {
			t.Fatal("시간 상한 설정 실패", err)
		}
		runErr = supervisor.Run(ctx, task.ID, 0, "")
		if early := time.Until(closeAt); early > 0 {
			t.Fatalf("시간 상한 %s 전에 빈 계획으로 마감함 err=%v result=%+v", early, runErr, supervisor.RunResult())
		}
	} else if goalAfterWork || mode == "goal-met" || mode == "request-budget" || mode == "request-budget-overflow" || mode == "planner-limit" || mode == "prepare-fail" || mode == "exploration-fail" {
		// 목표 달성 선언·실행 상한으로 스스로 끝나거나 실행에 들어가지 못하는 모드다.
		runErr = supervisor.Run(ctx, task.ID, 0, "")
	} else {
		// 작업이 다 끝나 Planner가 빈 계획을 내도 마감하지 않는지 보고, 실행 상한에 닿게 해서 마감 판정·근거 대조를 본다.
		done := make(chan error, 1)
		go func() { done <- supervisor.Run(ctx, task.ID, 0, "") }()
		runErr = firstDiagnosisRunPastIdle(t, ctx, supervisor, task, func() bool { return idlePlanner.idle.Load() > 0 }, done)
	}
	if reason, ok := diagnosisCheckReason(runErr); ok && reason == reasonEnginePaused && mode != "engine-pause" {
		// 회차 도중 운영자 밖 엔진 일시정지가 생기는 경로가 없음을 모든 마감 경로에서 고정한다.
		t.Fatalf("회차 도중 엔진 일시정지로 끝남 mode=%s", mode)
	}
	t.Logf("마감 경로 관찰 mode=%s run=%d stop=%d handle=%q modelClosed=%v hitsAtClose=%d hitsNow=%d err=%v",
		mode, closeProbe.runCalls, closeProbe.stopCalls, closeProbe.handle, closeProbe.modelClosed, closeProbe.hitsAtClose, hits.Load(), runErr)
	firstDiagnosisCheckClosingPath(t, mode, closeProbe.runCalls, closeProbe.stopCalls, closeProbe.handle, closeProbe.modelClosed,
		hits.Load()-closeProbe.hitsAtClose)
	if mode == "planner-only" {
		// 시간 상한까지 intent가 없으면 실패가 아니라 no_intents로 마감한다(C11 결정 2). 마감 사유는 시간 상한(budget_limited)이고
		// 대상 송신은 없다.
		result := supervisor.RunResult()
		if runErr != nil || result == nil || result.Outcome != diagnosis.OutcomeNoIntents || result.ClosingReason != diagnosis.ClosingBudgetLimited || result.IntentOutcomes != (diagnosis.IntentOutcomes{}) || !result.Valid() ||
			hits.Load() != 0 || provider.calls != 0 || plannerProvider.calls == 0 {
			t.Fatalf("intent 없는 실행을 no_intents로 마감하지 않음 err=%v result=%+v hits=%d", runErr, result, hits.Load())
		}
		return
	}
	if mode == "insert-assets" {
		var approved, other, linked, rootLinked int
		taskNumber := mustFirstDiagnosisTaskNumber(task.ID)
		if installer.QueryRow("SELECT count(*) FROM assets WHERE type='root_domain' AND domain='owned-assets.test' AND $1=ANY(task_ids)", taskNumber).Scan(&rootLinked) != nil {
			t.Fatal("루트 도메인 조회 실패")
		}
		if installer.QueryRow("SELECT count(*) FROM assets WHERE type='endpoint' AND url=$1 AND $2=ANY(task_ids)", approvedAsset, taskNumber).Scan(&approved) != nil ||
			installer.QueryRow("SELECT count(*) FROM assets WHERE url=$1", otherAsset).Scan(&other) != nil ||
			installer.QueryRow("SELECT count(*) FROM task_asset_links l JOIN assets a ON a.id=l.asset_id WHERE l.task_id=$1 AND a.url=$2 AND l.source='agent'", taskNumber, approvedAsset).Scan(&linked) != nil {
			t.Fatal("자산 조회 실패")
		}
		var result struct {
			Results []struct {
				Index int `json:"index"`
			} `json:"results"`
			Errors []struct {
				Index int    `json:"index"`
				Error string `json:"error"`
			} `json:"errors"`
		}
		decodeErr := json.Unmarshal([]byte(provider.assetsResult), &result)
		if runErr != nil || decodeErr != nil || approved != 1 || other != 0 || linked != 1 || rootLinked != 0 || len(result.Results) != 1 || result.Results[0].Index != 0 || len(result.Errors) != 1 || result.Errors[0].Index != 1 || provider.unsupportedTool {
			t.Fatalf("insert_assets 기록이 승인 대상과 다름 err=%v approved=%d other=%d linked=%d root=%d result=%q", runErr, approved, other, linked, rootLinked, provider.assetsResult)
		}
		return
	}
	if mode == "task-scope" {
		var scopes int
		if installer.QueryRow("SELECT count(*) FROM task_scope WHERE task_id=$1 AND source='agent' AND net=($2||'/32')::cidr", mustFirstDiagnosisTaskNumber(task.ID), plannerProvider.scopeValue).Scan(&scopes) != nil {
			t.Fatal("작업 범위 조회 실패")
		}
		execution, ok := runtime.host.Runtime().Execution(task.ID)
		// 실행 정책은 범위를 더한 뒤에 만들어졌지만 승인 파일의 판·규칙 그대로여야 한다.
		revision, rules := "", ""
		if ok && execution != nil {
			view := execution.Policy()
			revision = view.PolicyRevision
			for _, rule := range view.Rules {
				rules += rule.Ref + ";"
			}
		}
		wantRules := ""
		for _, rule := range policy.Rules {
			wantRules += rule.Ref + ";"
		}
		closeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		closeErr := runtime.Close(closeCtx)
		results := runtime.host.Runtime().Results(task.ID)
		if runErr != nil || closeErr != nil || !plannerProvider.scoped || workerCallsAtScope != 0 || provider.calls == 0 || scopes != 1 ||
			revision != policy.PolicyRevision || rules != wantRules || otherHits.Load() != 0 || hits.Load() != 0 ||
			len(results) != 1 || results[0].Classification != "policy_not_executed" || results[0].Call.Counts.Denied != 1 || !results[0].Call.AuditComplete || !results[0].Final {
			t.Fatalf("범위 도구가 E② 승인 범위를 넓힘 err=%v close=%v scoped=%v workerAtScope=%d scopes=%d other=%d hits=%d results=%d", runErr, closeErr, plannerProvider.scoped, workerCallsAtScope, scopes, otherHits.Load(), hits.Load(), len(results))
		}
		return
	}
	if mode == "goal-met" {
		result := supervisor.RunResult()
		if runErr != nil || result == nil || !result.Valid() || !plannerProvider.proved || task.lifecycleSnapshot().Status != "done" || hits.Load() != 0 || provider.calls != 0 {
			t.Fatalf("목표 달성으로 끝난 실행을 정상 마감하지 않음 err=%v result=%+v proved=%v status=%s", runErr, result, plannerProvider.proved, task.lifecycleSnapshot().Status)
		}
		return
	}
	if mode == "goal-met-unmatched-limit" {
		// 목표 달성으로 마감해도 E② 근거 대조는 그대로다. 근거 없는 정책 제한 intent가 있으면 실행은 마감 실패이고 결과가 없다.
		if !errors.Is(runErr, ErrFirstDiagnosisSupervisor) || supervisor.RunResult() != nil || !plannerProvider.proved || task.lifecycleSnapshot().Status != "done" || hits.Load() != 3 {
			t.Fatalf("근거 없는 정책 제한을 목표 달성 마감으로 수용함 err=%v result=%+v proved=%v status=%s hits=%d", runErr, supervisor.RunResult(), plannerProvider.proved, task.lifecycleSnapshot().Status, hits.Load())
		}
		if reason, _ := diagnosisCheckReason(runErr); reason != reasonClosurePolicyLimitedUnmatched {
			t.Fatalf("목표 달성 마감 실패의 검사 사유 코드: %q", reason)
		}
		return
	}
	if goalConcurrent {
		firstDiagnosisCheckGoalConcurrent(t, ctx, mode, runErr, supervisor, runtime, installer, task, plannerProvider, intentWorker, hits.Load(), targetExited)
		return
	}
	if mode == "goal-met-after-work" && (goalE3Calls != 1 || goalE3State != firstDiagnosisRealE3Want || e3StoppedCalls.Load() != 0) {
		t.Fatalf("목표 달성 마감에서 확정 단계 훅 때 E② 실행을 쓸 수 없음 calls=%d state=%q stopped=%d", goalE3Calls, goalE3State, e3StoppedCalls.Load())
	}
	if mode == "goal-met-after-work" || mode == "goal-met-auth-unknown" {
		// 목표 달성은 Planner의 판정이고 실행 마감 실패가 아니다. 정책 거부로 끝난 intent는 정책 제한, 정상 작업은 done이다.
		// 남은 intent가 없으므로 계약(C08 §3.2) 대조식대로 complete다(정책 제한은 실패도 미완료도 아니다).
		result := supervisor.RunResult()
		results := runtime.host.Runtime().Results(task.ID)
		classes := map[string]int{}
		for _, r := range results {
			classes[r.Classification]++
		}
		intentWorker.mu.Lock()
		workerCalls, unmatched := intentWorker.calls, intentWorker.unmatched
		intentWorker.mu.Unlock()
		want := diagnosis.IntentOutcomes{Done: 1, PolicyLimited: 2}
		auth := runtime.host.Runtime().AccountAuth()
		program := runtime.host.Runtime().ProgramCalls(task.ID)
		wantAuth := E2HTTPAuthAuthenticated
		if mode == "goal-met-auth-unknown" {
			wantAuth = E2HTTPAuthResultUnknown
			execution, ok := runtime.host.Runtime().Execution(task.ID)
			if !ok || execution == nil || execution.Snapshot().ProgramCounts.Unknown != 2 || execution.Snapshot().IntentCounts.Unknown != 0 {
				t.Fatal("프로그램 불명이 Worker 그래프에 섞이거나 누계에서 누락됨")
			}
		}
		if login.posts.Load() != 2 || auth["tester-a"] != wantAuth || auth["tester-b"] != wantAuth || len(program) == 0 {
			t.Fatalf("시험 계정 인증 준비가 실제 회차와 다름 logins=%d auth=%v program=%d", login.posts.Load(), auth, len(program))
		}
		if runErr != nil || result == nil || !result.Valid() || result.Outcome != diagnosis.OutcomeComplete || result.ClosingReason != "" || result.IntentOutcomes != want ||
			!plannerProvider.proved || task.lifecycleSnapshot().Status != "done" || hits.Load() != 3 || unmatched || workerCalls == 0 ||
			len(results) != 4 || classes["completed"] != 1 || classes["tool_failure"] != 1 || classes["partial_policy_denial"] != 1 || classes["policy_not_executed"] != 1 {
			t.Fatalf("작업 뒤 목표 달성으로 끝난 실행을 정상 마감하지 않음 err=%v result=%+v proved=%v status=%s hits=%d classes=%v unmatched=%v", runErr, result, plannerProvider.proved, task.lifecycleSnapshot().Status, hits.Load(), classes, unmatched)
		}
		closeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := runtime.Close(closeCtx); err != nil {
			t.Fatal("목표 달성 마감 뒤 자원 반환 실패")
		}
		return
	}
	if mode == "prepare-fail" {
		if runErr == nil || hits.Load() != 0 || provider.calls != 0 {
			t.Fatal("Planner 또는 준비 실패가 통제 밖 대상 송신으로 이어짐")
		}
		return
	}
	if mode == "exploration-fail" {
		// 엔진이 분류한 실패 사유가 감독자의 실행 실패 사유(진단 상태 reason_code로 가는 값)까지 간다(B1).
		if !errors.Is(runErr, ErrFirstDiagnosisSupervisor) || hits.Load() != 0 || task.lifecycleSnapshot().Status != "failed" ||
			diagnosis.ReasonOf(runErr) != "planning_exploration_failed" {
			t.Fatalf("탐색 실패 사유가 실행 실패 사유로 가지 않음 err=%v reason=%q status=%s hits=%d", runErr, diagnosis.ReasonOf(runErr), task.lifecycleSnapshot().Status, hits.Load())
		}
		return
	}
	if mode == "close-fail" {
		if runErr == nil || hits.Load() != 1 || plannerProvider.calls == 0 {
			t.Fatal("감사 자원 해제 실패를 실행 성공으로 수용함")
		}
		return
	}
	if mode == "plan-turns" {
		// 한 Plan 안에서 턴마다 assistant·도구 결과 두 메시지가 붙는다. 설치 상한 2턴이면 마지막 요청이 첫 요청보다
		// 2개 많다(기본 16턴이면 30개). 턴 상한에 닿은 Plan은 원본처럼 "이번 라운드만 끝" 마무리 턴을 받는다(C26).
		// intent가 없어 대상 송신 없이 시간 상한에서 no_intents로 마감한다.
		result := supervisor.RunResult()
		if runErr != nil || plannerProvider.calls < installedPlanTurns || plannerProvider.maxMessages-plannerProvider.minMessages != 2*(installedPlanTurns-1) || plannerProvider.wrapUps == 0 ||
			result == nil || result.Outcome != diagnosis.OutcomeNoIntents || result.ClosingReason != diagnosis.ClosingBudgetLimited || hits.Load() != 0 || provider.calls != 0 {
			t.Fatalf("설치한 Plan당 턴 상한·마무리 턴을 쓰지 않음 err=%v calls=%d messages=%d..%d wrapUps=%d result=%+v", runErr, plannerProvider.calls, plannerProvider.minMessages, plannerProvider.maxMessages, plannerProvider.wrapUps, result)
		}
		return
	}
	if mode == "result-unknown" {
		// 결과 불명 송신은 그 intent만 멈춘다(C01). 감사가 닫히면 실행은 실패가 아니라 completed + partial이고,
		// 결과 불명 intent는 다시 보내지 않는다(계약 §6.3).
		result := supervisor.RunResult()
		results := runtime.host.Runtime().Results(task.ID)
		var intentState, blockedReason string
		if err := installer.QueryRowContext(ctx, "SELECT state,COALESCE(blocked_reason,'') FROM exploration_nodes WHERE exploration_id=$1 AND kind='intent'", task.ExpID).Scan(&intentState, &blockedReason); err != nil {
			t.Fatal("결과 불명 intent 조회 실패", err)
		}
		if runErr != nil || result == nil || !result.Valid() || result.Outcome != diagnosis.OutcomePartial || result.ClosingReason != diagnosis.ClosingBudgetLimited ||
			result.IntentOutcomes != (diagnosis.IntentOutcomes{Unknown: 1}) || hits.Load() != 1 ||
			len(results) != 1 || results[0].Classification != "result_unknown" || results[0].Call.Counts.Unknown != 1 || !results[0].Final || !results[0].Call.AuditComplete ||
			intentState != "blocked" || blockedReason != "e2_http_result_unknown" {
			t.Fatalf("결과 불명 intent를 부분 완료로 마감하지 않음 err=%v result=%+v hits=%d results=%d intent=%s/%s", runErr, result, hits.Load(), len(results), intentState, blockedReason)
		}
		// 결과 불명 송신 근거가 없는 결과 불명 intent는 마감하지 않는다. 근거가 있는 intent와 끝난 intent가 섞이면
		// 둘 다 센다.
		for _, tc := range []struct {
			state, reason string
			ready         bool
			want          diagnosis.IntentOutcomes
		}{
			{"blocked", "e2_http_result_unknown", false, diagnosis.IntentOutcomes{}},
			{"done", "", true, diagnosis.IntentOutcomes{Done: 1, Unknown: 1}},
		} {
			extra, addErr := task.Store.AddIntent(map[string]any{"summary": "Owned unknown evidence fixture " + tc.state + tc.reason}, 1, nil, "system")
			if addErr != nil {
				t.Fatal("결과 불명 근거 fixture intent 생성 실패")
			}
			if _, err := installer.ExecContext(ctx, "UPDATE exploration_nodes SET state=$1, blocked_reason=NULLIF($2,'') WHERE exploration_id=$3 AND id=$4", tc.state, tc.reason, task.ExpID, extra); err != nil {
				t.Fatal("결과 불명 근거 fixture 상태 설정 실패", err)
			}
			ready, got, err := supervisor.generalCompletion(ctx, runtime.server, task)
			if tc.ready {
				if err != nil || !ready || got.IntentOutcomes != tc.want || got.Outcome != diagnosis.OutcomePartial || !got.Valid() {
					t.Fatalf("%s/%s: 근거 있는 결과 불명과 끝난 intent를 같이 세지 않음 ready=%v result=%+v err=%v", tc.state, tc.reason, ready, got, err)
				}
			} else if err == nil || ready {
				t.Fatalf("%s/%s: 결과 불명 송신 근거 없는 결과 불명 intent를 마감함", tc.state, tc.reason)
			}
			if _, err := installer.ExecContext(ctx, "DELETE FROM exploration_nodes WHERE exploration_id=$1 AND id=$2", task.ExpID, extra); err != nil {
				t.Fatal("결과 불명 근거 fixture 정리 실패")
			}
		}
		// 결과 불명 송신이 있는 호출의 intent를 다른 결과로 바꾸면 마감하지 않는다(그 intent는 다시 보내지 않아야 한다).
		callIntent := results[0].Call.IntentID
		for _, state := range []string{"done", "exhausted"} {
			if _, err := installer.ExecContext(ctx, "UPDATE exploration_nodes SET state=$1, blocked_reason=NULL WHERE exploration_id=$2 AND id=$3", state, task.ExpID, callIntent); err != nil {
				t.Fatal("결과 불명 intent 상태 바꾸기 실패", err)
			}
			if ready, _, err := supervisor.generalCompletion(ctx, runtime.server, task); err == nil || ready {
				t.Fatalf("결과 불명 송신이 있는 intent를 %s로 마감함", state)
			} else if reason, _ := diagnosisCheckReason(err); reason != reasonClosureUnknownIntentMismatch {
				t.Fatalf("결과 불명 송신이 있는 %s intent 마감 실패의 사유 코드: %q", state, reason)
			}
		}
		// 근거 없는 다른 intent를 결과 불명으로 두어 개수를 맞춰도, 결과 불명 송신이 있는 intent가 끝난 것으로 남으면
		// 마감하지 않는다(개수가 아니라 intent마다 대조한다).
		swapped, addErr := task.Store.AddIntent(map[string]any{"summary": "Owned swapped unknown fixture"}, 1, nil, "system")
		if addErr != nil {
			t.Fatal("맞바꾼 결과 불명 fixture 생성 실패")
		}
		if _, err := installer.ExecContext(ctx, "UPDATE exploration_nodes SET state='blocked', blocked_reason='e2_http_result_unknown' WHERE exploration_id=$1 AND id=$2", task.ExpID, swapped); err != nil {
			t.Fatal("맞바꾼 결과 불명 fixture 상태 설정 실패", err)
		}
		if ready, _, err := supervisor.generalCompletion(ctx, runtime.server, task); err == nil || ready {
			t.Fatal("결과 불명 송신이 있는 intent 대신 근거 없는 intent를 결과 불명으로 셈")
		}
		if _, err := installer.ExecContext(ctx, "DELETE FROM exploration_nodes WHERE exploration_id=$1 AND id=$2", task.ExpID, swapped); err != nil {
			t.Fatal("맞바꾼 결과 불명 fixture 정리 실패")
		}
		if _, err := installer.ExecContext(ctx, "UPDATE exploration_nodes SET state='blocked', blocked_reason='e2_http_result_unknown' WHERE exploration_id=$1 AND id=$2", task.ExpID, callIntent); err != nil {
			t.Fatal("결과 불명 intent 상태 되돌리기 실패", err)
		}
		if ready, got, err := supervisor.generalCompletion(ctx, runtime.server, task); err != nil || !ready || got.IntentOutcomes != (diagnosis.IntentOutcomes{Unknown: 1}) {
			t.Fatalf("되돌린 결과 불명 intent를 다시 마감하지 않음 ready=%v result=%+v err=%v", ready, got, err)
		}
		return
	}
	if mode == "planner-kill" {
		// Planner가 실행 중 작업을 멈췄고, 진행 중이던 송신은 결과 불명이라 그 intent만 결과 불명으로 끝난다(C01·C08).
		// 대상 연결은 E② 요청 시간 상한 전에 끊긴다. 결과 불명 송신은 다시 보내지 않는다.
		result := supervisor.RunResult()
		var intentState, blockedReason string
		if err := installer.QueryRowContext(ctx, "SELECT state,COALESCE(blocked_reason,'') FROM exploration_nodes WHERE exploration_id=$1 AND kind='intent'", task.ExpID).Scan(&intentState, &blockedReason); err != nil {
			t.Fatal("멈춘 intent 조회 실패", err)
		}
		exited := false
		select {
		case <-targetExited:
			exited = true
		default:
		}
		if runErr != nil || !plannerProvider.killed || result == nil || !result.Valid() || result.Outcome != diagnosis.OutcomePartial ||
			result.IntentOutcomes != (diagnosis.IntentOutcomes{Unknown: 1}) || hits.Load() != 1 || !exited || intentState != "blocked" || blockedReason != "e2_http_result_unknown" {
			var trusted []string
			for _, r := range runtime.host.Runtime().Results(task.ID) {
				trusted = append(trusted, fmt.Sprintf("class=%s final=%v bash=%v pdrain=%v xdrain=%v exit=%d reason=%s counts=%+v callfinal=%v audit=%v", r.Classification, r.Final, r.ProcessBashExited, r.ProcessDrained, r.ProxyDrained, r.ExitCode, r.Call.Reason, r.Call.Counts, r.Call.Final, r.Call.AuditComplete))
			}
			var calls []string
			if execution, ok := runtime.host.Runtime().Execution(task.ID); ok && execution != nil {
				for _, c := range execution.Calls() {
					calls = append(calls, fmt.Sprintf("final=%v audit=%v reason=%s counts=%+v", c.Final, c.AuditComplete, c.Reason, c.Counts))
				}
			}
			t.Fatalf("실행 중 작업을 Planner가 멈추지 못함 err=%v killed=%v kill_result=%q result=%+v hits=%d exited=%v intent=%s/%s trusted=%v calls=%v", runErr, plannerProvider.killed, plannerProvider.killResult, result, hits.Load(), exited, intentState, blockedReason, trusted, calls)
		}
		return
	}
	if plannerKill {
		firstDiagnosisCheckStoppedCommand(t, ctx, mode, runErr, supervisor, runtime, installer, task, plannerProvider, hits.Load(), targetExited)
		return
	}
	if mode == "time-budget" {
		if timeE3Calls != 1 || timeE3Reason != `handle="" exec=true ctx=true` {
			t.Fatalf("시간 상한 마감의 확정 단계 훅 때 E② 연결: 호출 %d %s", timeE3Calls, timeE3Reason)
		}
		result := supervisor.RunResult()
		entered, exited := false, false
		select {
		case <-targetEntered:
			entered = true
		default:
		}
		select {
		case <-targetExited:
			exited = true
		default:
		}
		var intentState, blockedReason string
		if err := installer.QueryRowContext(ctx, "SELECT state,COALESCE(blocked_reason,'') FROM exploration_nodes WHERE exploration_id=$1 AND kind='intent'", task.ExpID).Scan(&intentState, &blockedReason); err != nil {
			t.Fatal("시간 상한 intent 조회 실패", err)
		}
		if runErr != nil || result == nil || !result.Valid() || result.ClosingReason != diagnosis.ClosingBudgetLimited || result.Outcome != diagnosis.OutcomePartial ||
			// 유예 뒤 멈춘 작업의 진행 중 송신은 결과 불명이라 그 intent만 결과 불명이다(C01·C08). 정리 상태는 새 계획·새 intent를 막는다.
			result.IntentOutcomes != (diagnosis.IntentOutcomes{Unknown: 1}) || hits.Load() != 1 || !entered || !exited ||
			intentState != "blocked" || blockedReason != "e2_http_result_unknown" || !runtime.server.engine.isSettling(task.ID) {
			t.Fatalf("시간 상한을 budget_limited 마감으로 처리하지 않음 err=%v result=%+v hits=%d entered=%v exited=%v intent=%s/%s status=%s", runErr, result, hits.Load(), entered, exited, intentState, blockedReason, task.lifecycleSnapshot().Status)
		}
		return
	}
	if mode == "command-timeout" {
		// 명령 시간 상한은 그 명령·intent의 미완료다(계약 열린 항목 2). 실행은 마감 실패가 아니라 partial로 끝난다.
		result := supervisor.RunResult()
		var trusted e2runner.TrustedResult
		if provider.result != nil {
			trusted = provider.result.Trusted
		}
		if runErr != nil || result == nil || !result.Valid() || result.Outcome != diagnosis.OutcomePartial || result.IntentOutcomes != (diagnosis.IntentOutcomes{Incomplete: 1}) ||
			hits.Load() != 1 || trusted.Classification != "transient_failure" || trusted.Termination != e2runner.TerminationTimeout || trusted.Call.Counts.Unknown != 0 {
			t.Fatalf("명령 시간 상한을 intent 미완료로 마감하지 않음 err=%v result=%+v hits=%d class=%s termination=%q counts=%+v status=%s", runErr, result, hits.Load(), trusted.Classification, trusted.Termination, trusted.Call.Counts, task.lifecycleSnapshot().Status)
		}
		return
	}
	if mode == "request-budget" || mode == "request-budget-overflow" {
		result := supervisor.RunResult()
		settled := result != nil && result.IntentOutcomes.Done == 1
		if mode == "request-budget-overflow" {
			// 둘째 요청은 예산 소진 거부(E② 근거가 있는 정책 제한)다.
			results := runtime.host.Runtime().Results(task.ID)
			settled = result != nil && result.IntentOutcomes.PolicyLimited == 1 && len(results) == 1 && results[0].Call.Counts.Denied == 1 && results[0].Call.Reason == e2proxy.BudgetExhausted
		}
		if runErr != nil || result == nil || result.ClosingReason != diagnosis.ClosingBudgetLimited || !settled || !result.Valid() || hits.Load() != 1 {
			t.Fatalf("E② 요청 예산 소진을 실행 상한 마감으로 처리하지 않음 err=%v result=%+v hits=%d status=%s", runErr, result, hits.Load(), task.lifecycleSnapshot().Status)
		}
		return
	}
	if mode == "planner-limit" {
		result := supervisor.RunResult()
		if runErr != nil || result == nil || result.ClosingReason != diagnosis.ClosingBudgetLimited || result.IntentOutcomes.Done == 0 || !result.Valid() || hits.Load() != 1 {
			t.Fatalf("Planner 라운드 상한을 실행 상한 마감으로 처리하지 않음 err=%v result=%+v hits=%d", runErr, result, hits.Load())
		}
		return
	}
	if mode == "cancel" || mode == "user-stop" || mode == "pause" || mode == "engine-pause" {
		if mode == "engine-pause" {
			// 운영자 밖 엔진 일시정지는 운영자 중지(취소)와 섞지 않고 감독 실패 engine_paused로 끝난다.
			if reason, ok := diagnosisCheckReason(runErr); !ok || reason != reasonEnginePaused || !errors.Is(runErr, ErrFirstDiagnosisSupervisor) || errors.Is(runErr, context.Canceled) {
				t.Fatalf("엔진 일시정지 마감 사유: %v", runErr)
			}
			if runtime.server.requestApprovals.userStopped(mustFirstDiagnosisTaskNumber(task.ID)) {
				t.Fatal("엔진 일시정지를 운영자 중지로 기록함")
			}
		} else if runErr == nil || !errors.Is(runErr, context.Canceled) {
			t.Fatal("진행 중 실제 HTTP 요청 취소를 성공으로 반환하거나 취소 원인을 잃음")
		}
		// 중지 마감은 확정 단계를 부르지 않고 중지 기록을 한 번 남긴다. 실행 문맥이 취소돼도 기록 문맥은 살아 있다.
		if e3Calls != 0 || e3StoppedCalls.Load() != 1 || !e3StoppedCtxLive.Load() {
			t.Fatalf("중지 마감의 E③ 기록: 확정 %d·중지 기록 %d·문맥 %v", e3Calls, e3StoppedCalls.Load(), e3StoppedCtxLive.Load())
		}
		if mode == "pause" {
			// 재개는 지원하지 않는다: 고정 사유로 거절한다(planning_task_closed 실패로 보이지 않게).
			code, body := firstDiagnosisControlRequest(t, runtime, task.ID, "resume")
			if code != 409 || !strings.Contains(body, `"reason_code":"resume_unsupported"`) {
				t.Fatalf("재개 거절 사유: %d %s", code, body)
			}
		}
		if mode == "user-stop" || mode == "pause" {
			state := runtime.server.requestApprovals.runState(mustFirstDiagnosisTaskNumber(task.ID))
			if state.Phase != "closed" || state.Outcome != "cancelled" || !state.DrainComplete || state.ClosureFailed {
				t.Fatalf("중지 마감 상태 불일치: %+v", state)
			}
		}
		if mode == "engine-pause" {
			state := runtime.server.requestApprovals.runState(mustFirstDiagnosisTaskNumber(task.ID))
			t.Logf("엔진 일시정지 마감 상태: %+v closure=%+v", state, supervisor.ClosureResult())
			if state.Outcome == "cancelled" {
				t.Fatalf("엔진 일시정지를 취소(운영자 중지)로 닫음: %+v", state)
			}
		}
		closeCtx, stop := context.WithTimeout(context.Background(), 8*time.Second)
		defer stop()
		if err := runtime.Close(closeCtx); !errors.Is(err, ErrFirstDiagnosisOutcome) {
			t.Fatal("취소 실패를 보존한 실제 task·자식·터널·감사 drain이 완료되지 않음")
		}
		// 중지된 실행은 마감 뒤 intent 결과 개수만 남긴다(C08 2-2). 정상 마감 결과는 없다. 진행 중 송신이 끊긴
		// intent는 결과 불명(unknown)이다.
		if stopped := supervisor.ClosureResult(); supervisor.RunResult() != nil || stopped == nil || !stopped.StoppedValid() ||
			stopped.IntentOutcomes != (diagnosis.IntentOutcomes{Unknown: 1}) {
			t.Fatalf("중지 실행 intent 결과 불일치: %+v", stopped)
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
	if mode == "denied" || mode == "partial-denied" || mode == "unsupported" {
		if runErr != nil || plannerProvider.calls == 0 {
			t.Fatal("정상 정책 거부 뒤 재계획·감사 마감을 실패로 처리함")
		}
		// 제한 사유만 복사한 별도 intent에는 해당 call의 감사 근거가 없다.
		unmatched, addErr := task.Store.AddIntent(map[string]any{"summary": "Owned unmatched limitation fixture"}, 1, nil, "system")
		if addErr != nil || task.Store.SetIntentBlockedReason(unmatched, "e2_http_policy_limited") != nil {
			t.Fatal("미결속 제한 fixture 생성 실패")
		}
		if ready, _, err := supervisor.generalCompletion(ctx, runtime.server, task); err == nil || ready {
			t.Fatal("call 감사 없는 제한 intent를 정상 마감으로 수용함")
		} else if reason, _ := diagnosisCheckReason(err); reason != reasonClosurePolicyLimitedUnmatched || !errors.Is(err, ErrFirstDiagnosisSupervisor) {
			t.Fatalf("근거 없는 정책 제한 마감 실패의 사유 코드: %q", reason)
		}
		if _, err := installer.ExecContext(ctx, "DELETE FROM exploration_nodes WHERE exploration_id=$1 AND id=$2", task.ExpID, unmatched); err != nil {
			t.Fatal("이 시험이 만든 미결속 intent 정리 실패")
		}
		if ready, result, err := supervisor.generalCompletion(ctx, runtime.server, task); err != nil || !ready || result.IntentOutcomes.PolicyLimited == 0 || result.Outcome == diagnosis.OutcomeNoIntents || result.ClosingReason != diagnosis.ClosingBudgetLimited {
			t.Fatal("감사에 결속된 정상 제한을 마감 실패로 처리함")
		}
		// 실행 결과 상태 값(C08): 불완전·운영자 중지 intent가 있으면 실패가 아니라 부분 완료다. 결과 불명 intent는
		// E②가 결과 불명 송신을 기록한 intent일 때만 센다. 이 fixture에는 그 근거가 없으므로 마감하지 않는다.
		for _, tc := range []struct {
			state, reason string
			ready         bool
			bucket        func(diagnosis.IntentOutcomes) int64
		}{
			{"exhausted", "", true, func(o diagnosis.IntentOutcomes) int64 { return o.Incomplete }},
			{"stopped", "", true, func(o diagnosis.IntentOutcomes) int64 { return o.Incomplete }},
			{"blocked", "llm_quota", true, func(o diagnosis.IntentOutcomes) int64 { return o.Incomplete }},
			{"paused", "", true, func(o diagnosis.IntentOutcomes) int64 { return o.Stopped }},
			{"blocked", "e2_http_result_unknown", false, nil},
		} {
			extra, addErr := task.Store.AddIntent(map[string]any{"summary": "Owned outcome fixture " + tc.state + tc.reason}, 1, nil, "system")
			if addErr != nil {
				t.Fatal("결과 fixture intent 생성 실패")
			}
			if _, err := installer.ExecContext(ctx, "UPDATE exploration_nodes SET state=$1, blocked_reason=NULLIF($2,'') WHERE exploration_id=$3 AND id=$4", tc.state, tc.reason, task.ExpID, extra); err != nil {
				t.Fatal("결과 fixture intent 상태 설정 실패", err)
			}
			ready, result, err := supervisor.generalCompletion(ctx, runtime.server, task)
			outcomes := result.IntentOutcomes
			if tc.ready {
				if err != nil || !ready || tc.bucket(outcomes) != 1 || outcomes.Outcome() != diagnosis.OutcomePartial {
					t.Fatalf("%s/%s: 부분 완료로 마감하지 않음 ready=%v outcomes=%+v err=%v", tc.state, tc.reason, ready, outcomes, err)
				}
			} else if err == nil || ready {
				t.Fatalf("%s/%s: 결과 불명 intent를 마감함", tc.state, tc.reason)
			} else if reason, _ := diagnosisCheckReason(err); reason != reasonClosureUnknownIntentMismatch {
				t.Fatalf("%s/%s: 근거 없는 결과 불명 마감 실패의 사유 코드: %q", tc.state, tc.reason, reason)
			}
			if _, err := installer.ExecContext(ctx, "DELETE FROM exploration_nodes WHERE exploration_id=$1 AND id=$2", task.ExpID, extra); err != nil {
				t.Fatal("결과 fixture intent 정리 실패")
			}
		}
		// 실행 상한(budget_limited)에 닿으면 Planner를 기다리지 않고, 시작하지 못한 intent를 incomplete로 세서 마감한다.
		{
			extra, addErr := task.Store.AddIntent(map[string]any{"summary": "Owned budget-limited fixture"}, 1, nil, "system")
			if addErr != nil {
				t.Fatal("상한 fixture intent 생성 실패")
			}
			// 실행은 이미 실행 상한으로 끝났다(firstDiagnosisRunPastIdle). 표시를 바꾸지 않고 그대로 둔다.
			limited := task.firstDiagnosisBudgetLimited.Swap(true)
			ready, result, err := supervisor.generalCompletion(ctx, runtime.server, task)
			task.firstDiagnosisBudgetLimited.Store(limited)
			if err != nil || !ready || result.ClosingReason != diagnosis.ClosingBudgetLimited || result.IntentOutcomes.Incomplete == 0 || result.Outcome != diagnosis.OutcomePartial || !result.Valid() {
				t.Fatalf("실행 상한 마감 아님 ready=%v result=%+v err=%v", ready, result, err)
			}
			if _, err := installer.ExecContext(ctx, "DELETE FROM exploration_nodes WHERE exploration_id=$1 AND id=$2", task.ExpID, extra); err != nil {
				t.Fatal("상한 fixture intent 정리 실패")
			}
		}
		// 실제 E② 호출이 있는 intent가 불완전으로 끝나도(턴 소진 등) 그 호출의 감사 결과로 부분 완료가 된다.
		if execution, ok := runtime.host.Runtime().Execution(task.ID); ok && execution != nil && len(execution.Calls()) > 0 {
			callIntent := execution.Calls()[0].IntentID
			var oldState string
			var oldReason sql.NullString
			if err := installer.QueryRowContext(ctx, "SELECT state, blocked_reason FROM exploration_nodes WHERE exploration_id=$1 AND id=$2", task.ExpID, callIntent).Scan(&oldState, &oldReason); err != nil {
				t.Fatal("호출 intent 상태 읽기 실패")
			}
			if _, err := installer.ExecContext(ctx, "UPDATE exploration_nodes SET state='exhausted', blocked_reason=NULL WHERE exploration_id=$1 AND id=$2", task.ExpID, callIntent); err != nil {
				t.Fatal("호출 intent 상태 설정 실패")
			}
			ready, result, err := supervisor.generalCompletion(ctx, runtime.server, task)
			outcomes := result.IntentOutcomes
			if err != nil || !ready || outcomes.Incomplete == 0 || outcomes.Outcome() != diagnosis.OutcomePartial {
				t.Fatalf("호출이 있는 불완전 intent를 부분 완료로 받지 않음 ready=%v outcomes=%+v err=%v", ready, outcomes, err)
			}
			if _, err := installer.ExecContext(ctx, "UPDATE exploration_nodes SET state=$1, blocked_reason=$2 WHERE exploration_id=$3 AND id=$4", oldState, oldReason, task.ExpID, callIntent); err != nil {
				t.Fatal("호출 intent 상태 복원 실패")
			}
			if mode == "partial-denied" {
				firstDiagnosisUntrustedClosure(t, ctx, supervisor, runtime, installer, task, callIntent, oldState, oldReason)
			}
		} else {
			t.Fatal("정책 거부 실행에 E② 호출이 없음")
		}
		closeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		closeErr := runtime.Close(closeCtx)
		if closeErr != nil {
			t.Fatal("실제 거부 결과를 보존하면서 안전한 자원 반환을 완료하지 못함")
		}
		results := runtime.host.Runtime().Results(task.ID)
		wantHits, wantClass, wantReason := int64(0), "policy_not_executed", "e2_http_policy_limited"
		if mode == "partial-denied" {
			wantHits, wantClass = 1, "partial_policy_denial"
		}
		if mode == "unsupported" {
			wantClass, wantReason = "unsupported", "e2_http_unsupported"
		}
		if hits.Load() != wantHits || len(results) != 1 || results[0].Classification != wantClass || results[0].Call.Counts.Denied+results[0].Call.Counts.Unsupported != 1 || !results[0].Call.AuditComplete || results[0].Call.Counts.Unknown != 0 || !results[0].Final || !results[0].ProcessDrained || !results[0].ProxyDrained {
			t.Fatal("실제 Engine 정책 거부에서 대상0·프로그램 분류·drain을 증명하지 못함")
		}
		var intentState, blockedReason string
		if err := installer.QueryRowContext(closeCtx, "SELECT state,blocked_reason FROM exploration_nodes WHERE exploration_id=$1 AND kind='intent'", task.ExpID).Scan(&intentState, &blockedReason); err != nil || intentState != "blocked" || blockedReason != wantReason {
			t.Fatal("정상 마감이 진단 제한 사유를 지우거나 done으로 바꿈")
		}
		goal, err := task.Store.GetNode(goalID)
		if err != nil || goal == nil || goal.State != "open" {
			t.Fatal("실행 마감이 미판정 목표를 충족으로 바꿈")
		}
		lock, err := os.OpenFile(filepath.Join(cfg.AuthorityRoot, ".first-http.lock"), os.O_RDWR, 0)
		if err != nil {
			t.Fatal("거부 마감의 실제 lock 확인 실패")
		}
		defer lock.Close()
		if unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
			t.Fatal("정책 제한 보존 뒤 안전한 lease 반환이 누락됨")
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
	if e3Hooked {
		wantE3State := "task=true inflight=0 llm=0 admission=false fenced=false settling=true ctx=true"
		if mode == "e3-hook-disabled" {
			// 꺼진 확정 단계는 엔진을 정리 상태로 두지 않는다. 실행이 끝난 뒤에도 E③ 없는 실행처럼 정리 상태가 아니다.
			wantE3State = "task=true inflight=0 llm=0 admission=false fenced=false settling=false ctx=true"
			if runtime.server.engine.isSettling(task.ID) {
				t.Fatal("꺼진 E③ 확정 단계가 엔진 정리 상태를 남김")
			}
		}
		if !e3HandleOK {
			t.Fatal("시험 계정 없는 실행에서 확정 단계 훅 때 E② 연결을 꺼내지 못함")
		}
		result := supervisor.RunResult()
		if e3Calls != 1 || e3StoppedCalls.Load() != 0 || e3State != wantE3State || result == nil || !result.Valid() || result.Outcome != diagnosis.OutcomeComplete ||
			result.IntentOutcomes != (diagnosis.IntentOutcomes{Done: 1}) || result.ClosingReason != diagnosis.ClosingBudgetLimited {
			t.Fatalf("E③ 확정 단계 훅이 모델 단계 뒤·E② 마감 전에 한 번 불리지 않았거나 실행 결과가 바뀜 calls=%d state=%q result=%+v", e3Calls, e3State, result)
		}
	} else if runtime.server.engine.isSettling(task.ID) {
		t.Fatal("E③ 확정 단계 훅이 없는 실행이 엔진 정리 상태로 바뀜")
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

// 감독자 검사의 사유 코드는 모두 진단 로그 어휘에 있어야 한다. 어휘에 없는 사유는 로거가 기록 전체를 장애로 바꾼다.
func TestFirstDiagnosisCheckReasonsAreLoggable(t *testing.T) {
	logger, err := diagnosticlog.Create(firstDiagnosisPrivateTemp(t), "run", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := diagnosticlog.With(t.Context(), logger)
	for _, reason := range []string{reasonClosureWorkflowFailed, reasonClosureWorkflowNotReady, reasonClosureIntentReadFailed, reasonClosureCallsWithoutIntents,
		reasonClosureExecutionMissing, reasonClosureCallsMissing, reasonClosureResultsUnavailable, reasonClosureResultNotClosed, reasonClosureToolOutcomeInvalid,
		reasonClosureResultMismatch, reasonClosureUntrustedIntentMismatch, reasonClosureProgramCallOpen, reasonClosureCallMismatch, reasonClosureUnknownIntentMismatch,
		reasonClosurePolicyLimitedUnmatched, reasonClosureIntentStateInvalid, reasonClosureCallCountMismatch, reasonClosureNotReady, reasonEngineTaskFailed, reasonEnginePaused} {
		failed := firstDiagnosisCheckFailed(reason, ErrFirstDiagnosisSupervisor)
		diagnosisStage(ctx, 1, "execution", failed, diagnosisFailureReason("execution", failed), time.Now())
		if logger.Err() != nil {
			t.Fatalf("진단 로그 어휘에 없는 검사 사유: %s", reason)
		}
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
}

// firstDiagnosisGoalMetWork는 목표 달성 마감 시험의 intent다. marker로 Worker 모델이 배정된 intent를 고르고, steps의
// Bash 명령을 차례로 한 번씩 보낸다({T}는 소유 대상 주소). 순서대로 전부 거부(policy_not_executed), 0이 아닌 종료 코드와
// 거부가 섞인 명령(tool_failure) 뒤 거부 뒤 허용(partial_policy_denial), 허용(completed)이다. 실제 회차의 Worker 명령처럼
// 거부된 요청이 있는 명령도 종료 코드가 0이 아닐 수 있다(curl --fail, 파이프 뒤 파서 실패 등).
var firstDiagnosisGoalMetWork = []struct {
	marker string
	steps  []string
}{
	{"Owned goal-met refused read", []string{"curl --silent --show-error --max-time 5 {T}/forbidden"}},
	{"Owned goal-met mixed read", []string{
		"curl --silent --show-error --max-time 5 {T}/allowed; curl --silent --show-error --fail --max-time 5 {T}/forbidden",
		"curl --silent --show-error --max-time 5 {T}/forbidden; curl --silent --show-error --max-time 5 {T}/allowed",
	}},
	{"Owned goal-met allowed read", []string{"curl --silent --show-error --max-time 5 {T}/allowed"}},
}

// firstDiagnosisIntentWorkerToolID는 intent별 Worker 모델이 부르는 Bash 호출 ID의 앞부분이다. 뒤에 단계 번호가 붙는다.
const firstDiagnosisIntentWorkerToolID = "goal-met-bash-"

// firstDiagnosisIntentWorker는 intent마다 정해진 Bash 명령들을 차례로 한 번씩 보내고, 마지막 결과를 받으면 끝내는 Worker
// 모델이다. Worker 여럿이 함께 부를 수 있어 잠근다. 배정된 intent는 시스템 프롬프트의 배정 줄로만 고른다(그래프 개요에는
// 다른 intent도 보인다).
type firstDiagnosisIntentWorker struct {
	mu        sync.Mutex
	steps     map[string][]string
	calls     int
	unmatched bool
}

func (p *firstDiagnosisIntentWorker) Complete(_ context.Context, request llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	marker := ""
	if _, assigned, ok := strings.Cut(strings.Join(request.System, "\n\n"), "Assigned intent (untrusted task data):\n"); ok {
		line, _, _ := strings.Cut(assigned, "\n")
		for m := range p.steps {
			if strings.Contains(line, m) {
				marker = m
			}
		}
	}
	steps, ok := p.steps[marker]
	if !ok {
		p.unmatched = true
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("No assigned owned fixture intent.")}}, "end_turn", llm.Usage{}, nil
	}
	// 이 intent 세션이 이미 받은 Bash 결과 수가 다음 단계 번호다. 결과 내용(신뢰 결과)은 시험이 런타임에서 따로 읽는다.
	done := 0
	for _, message := range request.Messages {
		for _, block := range message.Content {
			if block.Type == llm.BlockToolResult && strings.HasPrefix(block.ToolUseID, firstDiagnosisIntentWorkerToolID) {
				done++
			}
		}
	}
	if done >= len(steps) {
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("The owned fixture step is complete.")}}, "end_turn", llm.Usage{}, nil
	}
	input, err := json.Marshal(map[string]any{"command": steps[done], "timeout_ms": 5000})
	if err != nil {
		return llm.Message{}, "", llm.Usage{}, err
	}
	id := firstDiagnosisIntentWorkerToolID + strconv.Itoa(done)
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: id, Name: "Bash", Input: input}}}, "tool_use", llm.Usage{}, nil
}

func (*firstDiagnosisIntentWorker) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{}, errors.New("unexpected fixture streaming"))
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

// firstDiagnosisNoWorkTimeBudget은 작업이 없는 실행 시험의 시간 상한 마감 시작까지의 시간이다. 준비 계획과 첫 실행 계획이
// 그 안에 끝난다.
const firstDiagnosisNoWorkTimeBudget = 2 * time.Second

// firstDiagnosisIdleHold는 빈 계획 라운드 뒤 실행이 마감하지 않는지 지켜보는 시간이다. 감독자 판정 주기(25ms)의 여러 배다.
const firstDiagnosisIdleHold = 500 * time.Millisecond

// firstDiagnosisIdlePlanner는 작업(intent)이 있었고 남은 작업(open·running)이 없을 때의 Planner 호출을 센다. 작업이 생기기
// 전의 첫 계획은 세지 않는다.
type firstDiagnosisIdlePlanner struct {
	llm.Provider
	intents func(context.Context) (pending, total int, err error)
	idle    atomic.Int64
}

// firstDiagnosisIdleWatch는 task의 intent 상태를 installer 연결로 읽어 빈 계획 호출을 세는 Planner 감싸개다.
func firstDiagnosisIdleWatch(installer *pgdb.DB, task *Task, planner llm.Provider) *firstDiagnosisIdlePlanner {
	return &firstDiagnosisIdlePlanner{Provider: planner, intents: func(c context.Context) (pending, total int, err error) {
		err = installer.QueryRowContext(c, "SELECT count(*) FILTER (WHERE state IN ('open','running')), count(*) FROM exploration_nodes WHERE exploration_id=$1 AND kind='intent' AND state<>'deleted'", task.ExpID).Scan(&pending, &total)
		return pending, total, err
	}}
}

func (p *firstDiagnosisIdlePlanner) Complete(ctx context.Context, request llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	pending, total, err := p.intents(ctx)
	message, stop, usage, callErr := p.Provider.Complete(ctx, request)
	if err == nil && callErr == nil && total > 0 && pending == 0 {
		p.idle.Add(1)
	}
	return message, stop, usage, callErr
}

// firstDiagnosisIdleCap은 원본과 같은 끝 규칙을 본다: 작업이 다 끝나(quiet) Planner가 빈 계획을 낸 뒤에도 실행은 마감하지
// 않는다(Planner는 다음 알림·심장박동 때 다시 계획한다). 그 뒤 지켜본 다음, 요청·모델 예산 도달과 같은 실행 상한 표시를 해서
// 마감 판정·근거 대조를 이어 본다. 대기 루프가 주기마다 step을 부른다.
type firstDiagnosisIdleCap struct {
	quiet  func() bool
	seen   time.Time
	capped bool
}

func (c *firstDiagnosisIdleCap) step(task *Task) {
	if c.capped || !c.quiet() {
		return
	}
	if c.seen.IsZero() {
		c.seen = time.Now()
		return
	}
	if time.Since(c.seen) >= firstDiagnosisIdleHold {
		c.capped = true
		task.firstDiagnosisBudgetLimited.Store(true)
	}
}

// closedEarly는 실행 상한 표시 전에 정상 마감했는지 본다(빈 계획 마감). 실패로 돌아온 Run은 여기서 보지 않는다.
func (c *firstDiagnosisIdleCap) closedEarly(supervisor *FirstDiagnosisSupervisor) bool {
	return !c.capped && supervisor.RunResult() != nil
}

// firstDiagnosisRunPastIdle은 Run이 돌아올 때까지 firstDiagnosisIdleCap을 돌린다. 상한 표시 전에 정상 마감하면 실패다.
func firstDiagnosisRunPastIdle(t *testing.T, ctx context.Context, supervisor *FirstDiagnosisSupervisor, task *Task, quiet func() bool, done <-chan error) error {
	t.Helper()
	idle := &firstDiagnosisIdleCap{quiet: quiet}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			if idle.closedEarly(supervisor) {
				t.Fatalf("계획이 비자 실행 상한 전에 마감함(원본에 없는 끝 규칙) err=%v result=%+v", err, supervisor.RunResult())
			}
			return err
		case <-ctx.Done():
			t.Fatalf("Supervisor Run이 반환하지 않음 capped=%v", idle.capped)
		case <-ticker.C:
			idle.step(task)
		}
	}
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

// firstDiagnosisE3SupervisorFlows는 E③ 확정 단계 훅 모드별 감독자 흐름(supervisorFlowLog)이다. 같은 실행 픽스처의
// 훅 없음(e3-baseline)·꺼진 확정 단계(e3-hook-disabled)·켜진 확정 단계(e3-hook)를 서로 대조한다.
var firstDiagnosisE3SupervisorFlows sync.Map

// supervisorFlowLog는 진단 기록에서 감독자 사건의 순서(사건·단계·상태·사유)를 시각·소요 시간 없이 뽑는다.
func supervisorFlowLog(t *testing.T, path string) []string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Error(err)
		return nil
	}
	defer file.Close()
	var flow []string
	for decoder := json.NewDecoder(file); ; {
		var event diagnosticlog.Event
		if err := decoder.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			t.Error("감독자 흐름 JSONL 손상", err)
			return nil
		}
		if event.Component == "supervisor" {
			flow = append(flow, event.Event+"/"+event.Stage+"/"+event.Status+"/"+event.ReasonCode)
		}
	}
	return flow
}

// verifyPlannerTodoLog는 Planner가 부른 TodoWrite가 진단 기록에 등록 도구 선택으로 남고, 미등록 도구 실패가 없는지 본다.
func verifyPlannerTodoLog(t *testing.T, path string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Error(err)
		return
	}
	defer file.Close()
	todo, unregistered := 0, 0
	for decoder := json.NewDecoder(file); ; {
		var event diagnosticlog.Event
		if err := decoder.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			t.Error("Plan 회차 JSONL 손상", err)
			return
		}
		if event.Event == "tool_selected" && event.Tool == "TodoWrite" && event.Status == "succeeded" && event.Role == "planner" {
			todo++
		}
		if event.ReasonCode == "unregistered_tool" {
			unregistered++
		}
	}
	if todo != 1 || unregistered != 0 {
		t.Errorf("TodoWrite 진단 기록: 선택 %d건, 미등록 %d건", todo, unregistered)
	}
}

// firstDiagnosisWaitForCall은 이 실행의 E② 호출 중 match가 참인 수가 생길 때까지 기다린다. 대상에 닿지 않는 요청(정책
// 거부·연결 거부)을 낸 명령이 아직 도는 동안 Planner를 깨우는 데 쓴다.
func firstDiagnosisWaitForCall(t *testing.T, ctx context.Context, runtime *FirstDiagnosisHTTPRuntime, task *Task, match func(e2proxy.CallResult) bool) {
	t.Helper()
	const poll = 10 * time.Millisecond
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		if execution, ok := runtime.host.Runtime().Execution(task.ID); ok && execution != nil {
			for _, call := range execution.Calls() {
				if match(call) {
					return
				}
			}
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("멈출 명령의 E② 시도 기록이 생기지 않음")
		}
	}
}

// firstDiagnosisCheckStoppedCommand는 Planner kill_work로 멈춘 명령(bash 미종료, 명령 시간 상한 아님)의 마감을 본다.
// 시도 단위로 모두 대상에 전달되지 않았으면(보내지 않음, 연결 전 송신 실패) 실행은 실패가 아니라 그 intent의
// 미완료(partial)다. 연결 뒤 응답 수신 도중 멈춘 시도가 있으면 거부가 섞여도 그 intent는 정책 제한·미완료가 아니라
// 결과 불명이다(C02·C08).
func firstDiagnosisCheckStoppedCommand(t *testing.T, ctx context.Context, mode string, runErr error, supervisor *FirstDiagnosisSupervisor, runtime *FirstDiagnosisHTTPRuntime, installer *pgdb.DB, task *Task, planner *firstDiagnosisPlannerProbeProvider, hits int64, targetExited <-chan struct{}) {
	t.Helper()
	result := supervisor.RunResult()
	results := runtime.host.Runtime().Results(task.ID)
	var intentState, blockedReason string
	if err := installer.QueryRowContext(ctx, "SELECT state,COALESCE(blocked_reason,'') FROM exploration_nodes WHERE exploration_id=$1 AND kind='intent'", task.ExpID).Scan(&intentState, &blockedReason); err != nil {
		t.Fatal("멈춘 intent 조회 실패", err)
	}
	var trusted e2runner.TrustedResult
	if len(results) == 1 {
		trusted = results[0]
	}
	c := trusted.Call.Counts
	// 시도 단위 영수증으로 연결 성립 여부를 직접 본다(연결 전 송신 실패인지, 연결 뒤 응답 수신 도중인지).
	var attempts []e2proxy.Receipt
	if execution, found := runtime.host.Runtime().Execution(task.ID); found && execution != nil {
		attempts = firstDiagnosisCallAttempts(execution)[trusted.Call.CallRef]
	}
	attemptWith := func(reason e2proxy.Reason) (e2proxy.Receipt, bool) {
		for _, a := range attempts {
			if a.Reason == reason {
				return a, true
			}
		}
		return e2proxy.Receipt{}, false
	}
	stopped := runErr == nil && planner.killed && result != nil && result.Valid() && result.Outcome == diagnosis.OutcomePartial && result.ClosingReason == diagnosis.ClosingBudgetLimited &&
		len(results) == 1 && trusted.Final && trusted.ProcessDrained && trusted.ProxyDrained && !trusted.ProcessBashExited && trusted.Termination == "" &&
		trusted.Call.AuditComplete && trusted.Execution.AuditComplete
	var ok bool
	switch mode {
	case "planner-kill-denied":
		ok = stopped && hits == 0 && trusted.Classification == "policy_not_executed" && c.Denied == 1 && c.ForwardAttempted == 0 && c.Unknown == 0 &&
			result.IntentOutcomes == (diagnosis.IntentOutcomes{Incomplete: 1}) && intentState == "stopped"
	case "planner-kill-refused":
		// 연결을 시도했지만(forward_attempted) 연결 전에 거부된 송신이다. E②는 전달하지 않은 송신 실패로 기록하므로(C02)
		// 결과 불명이 아니라 그 intent의 미완료다.
		refused, found := attemptWith(e2proxy.TransportFailed)
		ok = stopped && hits == 0 && found && refused.ForwardAttempted && !refused.RequestWritten && !refused.ResponseReceived &&
			trusted.Classification == "transient_failure" && c.Transient == 1 && c.ForwardAttempted == 1 && c.Responses == 0 && c.Unknown == 0 &&
			result.IntentOutcomes == (diagnosis.IntentOutcomes{Incomplete: 1}) && intentState == "stopped"
	case "planner-kill-mid-response":
		// 대상 연결은 멈춘 뒤 끊긴다. 서버 쪽 감지는 비동기라 잠깐 기다린다.
		const exitWait = 2 * time.Second
		exited := false
		select {
		case <-targetExited:
			exited = true
		case <-time.After(exitWait):
		}
		// 연결이 맺어지고 요청을 쓴 뒤 상태 줄·헤더를 받았고 본문을 끝까지 받지 못했다(연결 뒤 응답 수신 도중).
		mid, found := attemptWith(e2proxy.Unknown)
		ok = stopped && hits == 1 && exited && found && mid.ForwardAttempted && mid.RequestWritten && mid.ResponseReceived && !mid.ResponseComplete &&
			trusted.Classification == "result_unknown" && c.Denied == 1 && c.ForwardAttempted == 1 && c.Responses == 1 && c.Unknown == 1 &&
			result.IntentOutcomes == (diagnosis.IntentOutcomes{Unknown: 1}) && intentState == "blocked" && blockedReason == "e2_http_result_unknown"
	}
	if !ok {
		check, _ := diagnosisCheckReason(runErr)
		t.Fatalf("%s: 멈춘 명령의 마감이 시도 단위 결과와 다름 err=%v check=%q killed=%v result=%+v hits=%d intent=%s/%s class=%s bash=%v termination=%q reason=%s counts=%+v",
			mode, runErr, check, planner.killed, result, hits, intentState, blockedReason, trusted.Classification, trusted.ProcessBashExited, trusted.Termination, trusted.Call.Reason, c)
	}
}

// firstDiagnosisConcurrentBashSlots는 동시 명령 시험의 Bash 자리 수다(제품 기본 Worker 수 3).
const firstDiagnosisConcurrentBashSlots = 3

// firstDiagnosisGoalConcurrentWork는 진행 중 명령이 있는 목표 달성 시험의 intent다. 빠른 intent는 다른 Worker의 명령이
// 자리를 잡은 뒤에야 대상이 답하는 요청 하나로 끝난다(done). 느린 intent는 denied면 거부 뒤 sleep, mid-response면 거부 뒤
// 대상이 응답 도중 붙잡는 요청이다. 목표 증명은 빠른 intent가 끝나고 느린 intent의 명령이 아직 돌 때 한다.
func firstDiagnosisGoalConcurrentWork(mode string) []struct {
	marker string
	steps  []string
} {
	slow := "curl --silent --show-error --max-time 5 {T}/forbidden; sleep 30"
	if mode == "goal-met-concurrent-mid-response" {
		slow = "curl --silent --show-error --max-time 5 {T}/forbidden; curl --silent --show-error --max-time 5 '{T}/allowed?owned=hold'"
	}
	return []struct {
		marker string
		steps  []string
	}{
		{"Owned concurrent goal quick read", []string{"curl --silent --show-error --max-time 5 '{T}/allowed?owned=gate'"}},
		{"Owned concurrent goal slow read", []string{slow}},
	}
}

// firstDiagnosisWaitIntentStates는 intent 하나가 done이고 하나가 running일 때까지 기다리고 running intent를 돌려준다.
func firstDiagnosisWaitIntentStates(t *testing.T, ctx context.Context, installer *pgdb.DB, task *Task) int64 {
	t.Helper()
	const poll = 10 * time.Millisecond
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		var done, running int
		var runningID int64
		rows, err := installer.QueryContext(ctx, "SELECT id,state FROM exploration_nodes WHERE exploration_id=$1 AND kind='intent'", task.ExpID)
		if err != nil {
			t.Fatal("intent 상태 조회 실패", err)
		}
		for rows.Next() {
			var id int64
			var state string
			if err := rows.Scan(&id, &state); err != nil {
				_ = rows.Close()
				t.Fatal("intent 상태 읽기 실패", err)
			}
			switch state {
			case "done":
				done++
			case "running":
				running++
				runningID = id
			}
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatal("intent 상태 조회 마감 실패", err)
		}
		if done == 1 && running == 1 {
			return runningID
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("목표를 증명할 intent가 끝나지 않거나 다른 Worker의 명령이 먼저 끝남")
		}
	}
}

// firstDiagnosisCheckGoalConcurrent는 다른 Worker의 Bash 명령이 진행 중일 때 목표 달성으로 끝난 실행의 마감을 본다.
// 진행 중이던 명령의 시도가 모두 보내지 않은 거부면 실행은 실패가 아니라 그 intent의 미완료(C08 partial)다. 그 명령이 보낸
// 요청의 응답을 받는 도중이었으면 거부가 섞여도 그 intent는 결과 불명이다.
func firstDiagnosisCheckGoalConcurrent(t *testing.T, ctx context.Context, mode string, runErr error, supervisor *FirstDiagnosisSupervisor, runtime *FirstDiagnosisHTTPRuntime, installer *pgdb.DB, task *Task, planner *firstDiagnosisPlannerProbeProvider, worker *firstDiagnosisIntentWorker, hits int64, targetExited <-chan struct{}) {
	t.Helper()
	result := supervisor.RunResult()
	results := runtime.host.Runtime().Results(task.ID)
	var slow e2runner.TrustedResult
	completed, slots := 0, true
	for _, r := range results {
		slots = slots && r.ProcessLimits.BashSlots == firstDiagnosisConcurrentBashSlots
		if r.Classification == "completed" {
			completed++
			continue
		}
		slow = r
	}
	states := map[string]int{}
	var slowState, slowReason string
	rows, err := installer.QueryContext(ctx, "SELECT state,COALESCE(blocked_reason,'') FROM exploration_nodes WHERE exploration_id=$1 AND kind='intent'", task.ExpID)
	if err != nil {
		t.Fatal("intent 조회 실패", err)
	}
	for rows.Next() {
		var state, reason string
		if err := rows.Scan(&state, &reason); err != nil {
			_ = rows.Close()
			t.Fatal("intent 읽기 실패", err)
		}
		states[state]++
		if state != "done" {
			slowState, slowReason = state, reason
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal("intent 조회 마감 실패", err)
	}
	worker.mu.Lock()
	unmatched := worker.unmatched
	worker.mu.Unlock()
	c := slow.Call.Counts
	closed := runErr == nil && result != nil && result.Valid() && result.ClosingReason == "" &&
		planner.proved && task.lifecycleSnapshot().Status == "done" && !unmatched && slots && len(results) == 2 && completed == 1 && states["done"] == 1 &&
		slow.Final && slow.ProcessDrained && slow.ProxyDrained && !slow.ProcessBashExited && slow.Termination == "" && slow.Call.AuditComplete && slow.Execution.AuditComplete
	var ok bool
	switch mode {
	case "goal-met-concurrent-denied":
		// 목표 달성으로 취소된 진행 중 intent는 사유 goal_met이 남고 미완료가 아니라 목표 달성으로 중단으로 센다(#875 발견 6).
		ok = closed && hits == 1 && slow.Classification == "policy_not_executed" && c.Denied == 1 && c.ForwardAttempted == 0 && c.Unknown == 0 &&
			result.Outcome == diagnosis.OutcomeComplete && result.IntentOutcomes == (diagnosis.IntentOutcomes{Done: 1, GoalMet: 1}) &&
			slowState == "stopped" && slowReason == pgdb.IntentStopReasonGoalMet
	case "goal-met-concurrent-mid-response":
		const exitWait = 2 * time.Second
		exited := false
		select {
		case <-targetExited:
			exited = true
		case <-time.After(exitWait):
		}
		// 결과 불명 송신은 목표 달성 뒤에도 따로 보여 partial이다.
		ok = closed && hits == 2 && exited && slow.Classification == "result_unknown" && c.Denied == 1 && c.ForwardAttempted == 1 && c.Responses == 1 && c.Unknown == 1 &&
			result.Outcome == diagnosis.OutcomePartial && result.IntentOutcomes == (diagnosis.IntentOutcomes{Done: 1, Unknown: 1}) && slowState == "blocked" && slowReason == "e2_http_result_unknown"
	}
	if !ok {
		check, _ := diagnosisCheckReason(runErr)
		t.Fatalf("%s: 진행 중 명령이 있는 목표 달성 마감이 시도 단위 결과와 다름 err=%v check=%q result=%+v proved=%v status=%s hits=%d slots=%v results=%d completed=%d states=%v slow=%s/%s class=%s bash=%v termination=%q reason=%s counts=%+v",
			mode, runErr, check, result, planner.proved, task.lifecycleSnapshot().Status, hits, slots, len(results), completed, states, slowState, slowReason, slow.Classification, slow.ProcessBashExited, slow.Termination, slow.Call.Reason, c)
	}
}

func verifySupervisorFailureLog(t *testing.T, path, mode string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Error(err)
		return
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	found := 0
	for {
		var event diagnosticlog.Event
		err := decoder.Decode(&event)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Error("실패 JSONL 손상", err)
			return
		}
		if event.Event != "run_finished" {
			continue
		}
		found++
		switch mode {
		case "cancel", "user-stop", "pause":
			if event.Status != "cancelled" || event.ReasonCode != "user_cancelled" {
				t.Error("실제 취소와 로그 상태 불일치")
			}
		case "engine-pause":
			// 운영자 밖 엔진 일시정지는 운영자 취소(user_cancelled)가 아니라 실행 단계 실패 engine_paused로 남는다.
			if event.Status != "failed" || event.Stage != "execution" || event.ReasonCode != reasonEnginePaused {
				t.Errorf("엔진 일시정지의 로그 상태 불일치: stage=%s status=%s reason=%s", event.Stage, event.Status, event.ReasonCode)
			}
		case "close-fail":
			if event.Status != "failed" || event.Stage != "audit_close" || event.ReasonCode != "audit_failed" {
				t.Error("실제 감사 마감 실패와 로그 상태 불일치")
			}
		case "goal-met-after-work", "goal-met-auth-unknown":
			if event.Status != "succeeded" || event.ReasonCode != "" {
				t.Errorf("목표 달성 마감의 로그 상태 불일치: stage=%s status=%s reason=%s", event.Stage, event.Status, event.ReasonCode)
			}
		case "goal-met-concurrent-denied", "goal-met-concurrent-mid-response":
			if event.Status != "succeeded" || event.ReasonCode != "" {
				t.Errorf("%s: 진행 중 명령이 있는 목표 달성 마감의 로그 상태 불일치: stage=%s status=%s reason=%s", mode, event.Stage, event.Status, event.ReasonCode)
			}
		case "goal-met-unmatched-limit":
			// 어느 검사가 마감을 막았는지 일반 execution_failed가 아니라 검사 사유 코드로 남는다.
			if event.Status != "failed" || event.Stage != "execution" || event.ReasonCode != reasonClosurePolicyLimitedUnmatched {
				t.Errorf("마감 검사 실패의 로그 사유 불일치: stage=%s status=%s reason=%s", event.Stage, event.Status, event.ReasonCode)
			}
		}
	}
	if found != 1 {
		t.Errorf("실제 실패 회차 마감 이벤트 수=%d", found)
	}
}

// C09: 신뢰 결과 대조가 깨진 intent(runtime이 결과를 저장하지 않고 표시만 함)는 결과 불명으로 끝났을 때만 마감에서
// 센다. 실제 E② 호출 기록(최종·감사 완료)은 그대로 대조한다. 표시 없이 결과가 빠지면 마감하지 않는다.
func firstDiagnosisUntrustedClosure(t *testing.T, ctx context.Context, supervisor *FirstDiagnosisSupervisor, runtime *FirstDiagnosisHTTPRuntime, installer *pgdb.DB, task *Task, callIntent int64, oldState string, oldReason sql.NullString) {
	t.Helper()
	rt := runtime.host.Runtime()
	rt.mu.Lock()
	owned := rt.task
	rt.mu.Unlock()
	owned.mu.Lock()
	saved := owned.results
	owned.mu.Unlock()
	setRuntime := func(results []e2runner.TrustedResult, untrusted map[int64]bool) {
		owned.mu.Lock()
		owned.results, owned.untrusted = results, untrusted
		owned.mu.Unlock()
	}
	setIntent := func(state string, reason any) {
		if _, err := installer.ExecContext(ctx, "UPDATE exploration_nodes SET state=$1, blocked_reason=$2 WHERE exploration_id=$3 AND id=$4", state, reason, task.ExpID, callIntent); err != nil {
			t.Fatal("대조 실패 fixture intent 상태 설정 실패", err)
		}
	}
	defer func() {
		setRuntime(saved, nil)
		setIntent(oldState, oldReason)
	}()
	// reason은 마감하지 않을 때 진단 로그에 남을 검사 사유 코드다.
	expect := func(name string, ready bool, want diagnosis.IntentOutcomes, reason string) {
		t.Helper()
		got, result, err := supervisor.generalCompletion(ctx, runtime.server, task)
		logged, _ := diagnosisCheckReason(err)
		if ready && (err != nil || !got || result.IntentOutcomes != want || !result.Valid()) || !ready && (err == nil || got || logged != reason) {
			t.Fatalf("%s: ready=%v result=%+v err=%v reason=%q", name, got, result, err, logged)
		}
	}
	untrusted := map[int64]bool{callIntent: true}
	// 결과가 빠졌는데 대조 실패 표시가 없으면, intent가 불완전으로 끝났어도 마감하지 않는다.
	setIntent("exhausted", nil)
	setRuntime([]e2runner.TrustedResult{}, nil)
	expect("missing-without-mark", false, diagnosis.IntentOutcomes{}, reasonClosureCallMismatch)
	// 대조 실패 표시가 있어도 그 intent가 결과 불명이 아니면 마감하지 않는다.
	setRuntime([]e2runner.TrustedResult{}, untrusted)
	expect("mark-without-unknown-intent", false, diagnosis.IntentOutcomes{}, reasonClosureUntrustedIntentMismatch)
	// 근거 없는 다른 intent를 결과 불명으로 두어 개수를 맞춰도 마감하지 않는다(intent마다 대조).
	swapped, addErr := task.Store.AddIntent(map[string]any{"summary": "Owned swapped untrusted fixture"}, 1, nil, "system")
	if addErr != nil {
		t.Fatal("맞바꾼 대조 실패 fixture 생성 실패")
	}
	if _, err := installer.ExecContext(ctx, "UPDATE exploration_nodes SET state='blocked', blocked_reason='e2_http_result_unknown' WHERE exploration_id=$1 AND id=$2", task.ExpID, swapped); err != nil {
		t.Fatal("맞바꾼 대조 실패 fixture 상태 설정 실패", err)
	}
	expect("swapped-unknown-intent", false, diagnosis.IntentOutcomes{}, reasonClosureUntrustedIntentMismatch)
	if _, err := installer.ExecContext(ctx, "DELETE FROM exploration_nodes WHERE exploration_id=$1 AND id=$2", task.ExpID, swapped); err != nil {
		t.Fatal("맞바꾼 대조 실패 fixture 정리 실패")
	}
	// 결과 불명으로 끝난 intent는 부분 완료로 센다(실행 전체 실패 아님).
	setIntent("blocked", "e2_http_result_unknown")
	expect("marked-unknown-intent", true, diagnosis.IntentOutcomes{Unknown: 1}, "")
	// 실제 호출에 없는 결과가 섞이면 마감하지 않는다.
	if len(saved) != 1 {
		t.Fatal("대조 실패 fixture는 실제 결과 하나가 필요함")
	}
	// 마감할 수 없는 호출 분류(실행기가 붙이는 forward_not_attempted)는 그 검사 사유로 남는다.
	invalid := saved[0]
	invalid.Call = e2proxy.CloneCallResult(invalid.Call)
	invalid.Classification = e2runner.ClassificationForwardNotAttempted
	setRuntime([]e2runner.TrustedResult{invalid}, nil)
	expect("invalid-tool-outcome", false, diagnosis.IntentOutcomes{}, reasonClosureToolOutcomeInvalid)
	stray := saved[0]
	stray.Call = e2proxy.CloneCallResult(stray.Call)
	stray.Call.CallRef = "stray-owned-call"
	setRuntime([]e2runner.TrustedResult{stray}, untrusted)
	expect("stray-result", false, diagnosis.IntentOutcomes{}, reasonClosureCallCountMismatch)
	// 같은 결과가 두 번 저장되면 마감하지 않는다.
	setRuntime(append(append([]e2runner.TrustedResult{}, saved...), saved...), untrusted)
	expect("duplicate-result", false, diagnosis.IntentOutcomes{}, reasonClosureResultMismatch)
	// 결과 기록 자체가 실패했으면(nil) 대조 실패 표시로 덮지 않는다.
	setRuntime(nil, untrusted)
	owned.mu.Lock()
	owned.resultsFailed = true
	owned.mu.Unlock()
	expect("results-failed", false, diagnosis.IntentOutcomes{}, reasonClosureResultsUnavailable)
	owned.mu.Lock()
	owned.resultsFailed = false
	owned.mu.Unlock()
}

// firstDiagnosisRealE3Want는 실제 runtime 확정 단계 훅의 기대 결과다(firstDiagnosisRealE3Hook).
const firstDiagnosisRealE3Want = "handle= ctx=true | undeclared=disabled/declaration_missing bridges=0 started=0 | declared=start_refused/marker_registration_failed bridges=1 started=1 requests=0"

// firstDiagnosisRealE3Hook은 감독자 훅 안에서 실제 runtime으로 확정 단계를 두 번 돈다. 선언 없음은 연결이 붙어도 사유만
// 내고 브리지를 만들지 않는다. 선언 있음은 표식 등록 때 실제 E② 실행으로 브리지를 만든다.
func firstDiagnosisRealE3Hook(t *testing.T, ctx context.Context, runtime *FirstDiagnosisHTTPRuntime, taskID string) string {
	exec, accounts, reason := runtime.E3HandleReason(taskID)
	state := fmt.Sprintf("handle=%s ctx=%v", reason, ctx.Err() == nil)
	if reason != "" {
		return state
	}
	run := func(in e3contract.Input, decl *e3contract.Declaration) string {
		bridges, started := 0, 0
		lazy, err := e3v3.NewLazy(func(binding e3contract.RunBinding) (e3v3.Bridge, error) {
			bridges++
			return e2live.NewLive(exec, accounts, binding)
		})
		if err != nil {
			t.Fatal(err)
		}
		defer lazy.Close()
		adapter, err := e3v3.New(lazy)
		if err != nil {
			t.Fatal(err)
		}
		stored := map[string]e3contract.Declaration{}
		if decl != nil {
			stored[in.Binding.Policy.Ref] = *decl
		}
		res := e3contract.Run(ctx, e3contract.Deps{Transport: adapter, Journal: adapter, Declarations: e3fake.Declarations{Stored: stored},
			Reset: e3fake.Reset{State: e3contract.ResetState{Residue: e3contract.ResidueAbsent}}, Writes: &e3fake.Writes{},
			Started: func() { started++ }}, in)
		return fmt.Sprintf("%s/%s bridges=%d started=%d", res.Status, res.Reason, bridges, started) +
			map[bool]string{true: fmt.Sprintf(" requests=%d", res.Requests), false: ""}[decl != nil]
	}
	scope := exec.Snapshot().Scope
	now := time.Now()
	in := e3contract.Input{TargetRef: "target-harness", WriteConsent: true, RequestTimeout: E3ConfirmationRequestTimeoutDefault, AdmittedTypes: 1,
		Binding: e3contract.RunBinding{JobRef: "job-harness", RunRef: scope.RunRef, TaskRef: taskID, E2PolicyRef: scope.PolicyRef,
			GenerationRef: "generation-harness", InstanceRef: "instance-harness"},
		PolicyValidFrom: now.Add(-time.Hour), PolicyValidUntil: now.Add(time.Hour)}
	undeclared := run(in, nil)
	decl := firstDiagnosisHarnessDeclaration(t, in.TargetRef)
	digest, err := decl.Digest()
	if err != nil {
		t.Fatal(err)
	}
	in.Binding.Policy = e3contract.PolicySource{Kind: e3contract.PolicyProductDeclaration, Ref: "declaration-harness", DigestSHA256: digest,
		TargetApprovalRevision: decl.TargetApprovalRevision}
	in.Owner = e3contract.Account{Label: decl.OwnerAccountLabel, Authenticated: true}
	in.Cross = e3contract.Account{Label: decl.CrossAccountLabel, Authenticated: true}
	declared := run(in, &decl)
	return state + " | undeclared=" + undeclared + " | declared=" + declared
}

// firstDiagnosisHarnessDeclaration은 VAmPI 확정 시나리오의 선언(scripts/eval/e3_vampi/declaration.json)을 이 시험 대상에
// 붙인 동결본이다.
func firstDiagnosisHarnessDeclaration(t *testing.T, targetRef string) e3contract.Declaration {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "scripts", "eval", "e3_vampi", "declaration.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decl e3contract.Declaration
	if err := json.Unmarshal(raw, &decl); err != nil {
		t.Fatal(err)
	}
	decl.Version, decl.TargetRef, decl.TargetApprovalRevision = e3contract.DeclarationVersion, targetRef, "approval-harness"
	decl.DeclaredBy, decl.DeclaredAt = "ARTEX", time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	if err := decl.Validate(); err != nil {
		t.Fatal("시험 선언", err)
	}
	return decl
}

// firstDiagnosisControlRequest는 제품 경로의 작업 control(pause·resume) 요청을 운영자 인증으로 보내고 상태 코드와 본문을 돌려준다.
func firstDiagnosisControlRequest(t *testing.T, runtime *FirstDiagnosisHTTPRuntime, taskID, action string) (int, string) {
	t.Helper()
	hash, err := runtime.server.reloadPassHash()
	if err != nil {
		t.Fatal(err)
	}
	token, err := signJWT(runtime.server.signingKey(hash))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/tasks/"+taskID+"/control", strings.NewReader(`{"action":"`+action+`"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	runtime.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// firstDiagnosisCheckClosingPath는 마감 경로 표(사용자 결정 2026-11-20)를 본다. 모든 경로에서 확정 단계 훅(Run)과 중지
// 기록(Stopped) 가운데 정확히 하나가 한 번 불리고, 확정 단계 훅 때 E② 연결은 살아 있고 모델 단계는 마감돼 있거나(사유
// 없음), E② 실행을 한 번도 잡지 않은 경우(lease_missing)뿐이다. 닫힘 사유(task_fenced·execution_stopped 등)로 확정 단계
// 훅이 불리면 그 경로가 확정 단계 전에 E②를 닫은 것이다(#809 목표 달성, #825 중지 기록, 시간 상한 유예 초과). 확정 단계
// 훅·중지 기록 뒤에는 모델 단계 송신이 0이다. 대표 경로는 어느 쪽으로 끝나는지도 고정한다.
func firstDiagnosisCheckClosingPath(t *testing.T, mode string, runCalls, stopCalls int, handle string, modelClosed bool, sentAfter int64) {
	t.Helper()
	if runCalls+stopCalls != 1 {
		t.Fatalf("%s: 확정 단계 훅 %d·중지 기록 %d(정확히 하나가 한 번이어야 함)", mode, runCalls, stopCalls)
	}
	if runCalls == 1 && !(handle == "" && modelClosed || handle == E3HandleLeaseMissing) {
		t.Fatalf("%s: 확정 단계 훅 때 E② 연결 %q·모델 단계 마감 %v(확정 단계 전에 E②가 닫혔거나 모델 단계가 열려 있음)", mode, handle, modelClosed)
	}
	if sentAfter != 0 {
		t.Fatalf("%s: 모델 단계 마감(또는 중지) 뒤 대상 송신 %d건", mode, sentAfter)
	}
	want := map[string]string{
		// 모델 단계가 스스로 끝남 → 확정 단계가 돈다.
		"goal-met-after-work": "run", "time-budget": "run", "request-budget": "run", "request-budget-overflow": "run",
		"planner-limit": "run", "planner-kill": "run", "command-timeout": "run",
		// 스스로 끝났지만 E② 실행을 한 번도 잡지 않음 → 확정 단계 훅이 실행 없음으로 기록한다.
		"planner-only": "no-execution", "plan-turns": "no-execution", "goal-met": "no-execution",
		// 강제 중지·실패 → 중지 기록(interrupted).
		"cancel": "stop", "user-stop": "stop", "pause": "stop", "engine-pause": "stop", "prepare-fail": "stop", "goal-met-unmatched-limit": "stop",
	}[mode]
	got := "run"
	switch {
	case stopCalls == 1:
		got = "stop"
	case handle == E3HandleLeaseMissing:
		got = "no-execution"
	}
	if want != "" && got != want {
		t.Fatalf("%s: 마감 경로 %s, 표에서는 %s", mode, got, want)
	}
}
