package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/diagnosis"
	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/internal/diagnosticlog"
	"github.com/Autumn-27/artex/server"
)

type urlPreparationRuntime struct {
	ctx                 context.Context
	path                string
	config              urlPreparationInstallation
	endpoint            diagnosisprep.Endpoint
	resolver            *net.Resolver // nil이면 시스템 resolver. 시험은 전역 DNS를 변경하지 않는다.
	mu                  sync.RWMutex
	live                map[string]*urlLiveHandler
	recovery            map[string]*urlGatewayRunner
	preparationRecovery map[string]*urlPreparationRecovery
	reports             map[string]*savedReportService
	product             *productRuntime
	hostCapabilities    urlRuntimeHostCapabilities
}

func newURLPreparationRuntime(ctx context.Context, path string) (*urlPreparationRuntime, error) {
	c, pin, err := readURLInstallation(path)
	if err != nil {
		return nil, errPreparation
	}
	b := &urlPreparationRuntime{ctx: ctx, path: path, config: c, live: map[string]*urlLiveHandler{}, reports: map[string]*savedReportService{}}
	if c.Product != nil {
		b.product, err = newProductRuntime(c, pin)
		if err != nil {
			return nil, err
		}
		b.resolver = b.product.targets.resolver
	}
	return b, nil
}
func (b *urlPreparationRuntime) installation() (urlPreparationInstallation, urlInputPin, error) {
	c, pin, err := readURLInstallation(b.path)
	if err != nil || c.StateDir != b.config.StateDir || c.RunsDir != b.config.RunsDir || c.SocketPath != b.config.SocketPath || c.ReportListenAddr != b.config.ReportListenAddr || c.APILaunch != b.config.APILaunch || !reflect.DeepEqual(c.ProtectedEndpoints, b.config.ProtectedEndpoints) || !sameProductInstallation(c.Product, b.config.Product) {
		return c, pin, errPreparation
	}
	return c, pin, nil
}
func (b *urlPreparationRuntime) Profiles(ctx context.Context, principal string) ([]diagnosisprep.Profile, error) {
	if ctx == nil || ctx.Err() != nil || principal != "ARTEX" {
		return nil, errPreparation
	}
	c, pin, err := b.installation()
	if err != nil {
		return nil, err
	}
	if b.product != nil {
		return b.product.profiles()
	}
	out := []diagnosisprep.Profile{}
	for _, p := range c.Profiles {
		if p.Principal == principal {
			public := urlProfilePublic(p)
			loaded, loadErr := urlLoadProfile(c, pin, p)
			if loaded != nil {
				defer loaded.clearMaterials()
			}
			if loadErr == nil && loaded.inspection.Scope != nil {
				public.Model = loaded.inspection.Scope.Model
			}
			out = append(out, public)
		}
	}
	return out, nil
}
func (b *urlPreparationRuntime) load(principal string, input diagnosisprep.Input) (*urlLoadedProfile, error) {
	return b.loadForReport(principal, input, false)
}
func (b *urlPreparationRuntime) loadForReport(principal string, input diagnosisprep.Input, reportOnly bool, reportTarget ...string) (*urlLoadedProfile, error) {
	c, pin, err := b.installation()
	if err != nil {
		return nil, err
	}
	if b.product != nil {
		id := ""
		if len(reportTarget) > 0 {
			id = reportTarget[0]
		}
		// 두 번째 값은 저장 보고서 작업의 원본 요약이다. 계정을 결속한 실행의 결속을 찾을 때 쓴다(C14).
		return b.product.load(principal, input, reportOnly, id, reportTarget[min(len(reportTarget), 1):]...)
	}
	for _, p := range c.Profiles {
		if p.ID == input.ProfileID && p.Principal == principal {
			return urlLoadProfileForReport(c, pin, p, reportOnly)
		}
	}
	return nil, errPreparation
}
func (b *urlPreparationRuntime) Inspect(ctx context.Context, principal string, input diagnosisprep.Input) (diagnosisprep.Inspection, error) {
	if ctx == nil || ctx.Err() != nil || principal != "ARTEX" {
		return diagnosisprep.Inspection{}, errPreparation
	}
	canonical, err := diagnosisprep.CanonicalInput(input)
	if err != nil || canonical != input {
		return diagnosisprep.Inspection{}, errPreparation
	}
	l, err := b.load(principal, input)
	if err != nil {
		var failure urlPreparationError
		if errors.As(err, &failure) {
			return diagnosisprep.Inspection{ReasonCode: string(failure)}, nil
		}
		return diagnosisprep.Inspection{ReasonCode: "installation_unavailable"}, nil
	}
	defer l.clearMaterials()
	i := l.inspection
	u, _ := url.Parse(input.TargetURL)
	if u.Scheme+"://"+u.Host != l.origin {
		return diagnosisprep.Inspection{ReasonCode: "target_not_approved"}, nil
	}
	matched := false
	for _, p := range i.Scope.PathPrefixes {
		if urlPathWithin(u.Path, p) {
			matched = true
		}
	}
	if !matched {
		return diagnosisprep.Inspection{ReasonCode: "path_not_approved"}, nil
	}
	if time.Now().Before(l.profile.ValidFrom) || !time.Now().Before(l.profile.ValidUntil) {
		i.ReasonCode = "approval_expired"
	}
	return i, nil
}

type urlProgress struct {
	mu      sync.Mutex
	update  diagnosisprep.Update
	publish diagnosisprep.Publish
}

func (p *urlProgress) set(edit func(*diagnosisprep.Update)) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	edit(&p.update)
	return p.publish(p.update)
}
func (p *urlProgress) value() diagnosisprep.Update { p.mu.Lock(); defer p.mu.Unlock(); return p.update }

func (b *urlPreparationRuntime) Execute(parent context.Context, execution diagnosisprep.Execution, publish diagnosisprep.Publish) (resultErr error) {
	if parent == nil || publish == nil || execution.Job.Scope == nil {
		return errPreparation
	}
	l, err := b.load(execution.Principal, execution.Job.Input)
	if err != nil {
		return errPreparation
	}
	defer l.clearMaterials()
	if l.inspection.InstallationDigest != execution.Inspection.InstallationDigest || l.inspection.DelegationRef != execution.Inspection.DelegationRef {
		return errPreparation
	}
	hard, cancel := context.WithTimeout(parent, time.Duration(l.profile.Budget.DurationSeconds)*time.Second)
	defer cancel()
	ctx, stopTarget := context.WithCancelCause(hard)
	defer stopTarget(context.Canceled)
	var flow *diagnosisflow.Controller
	var flowPool *sql.DB
	p := &urlProgress{publish: publish, update: diagnosisprep.Update{State: diagnosisprep.Preparing, Phase: diagnosisprep.ScopeCheck, PreparationStarted: true, AuthorityState: diagnosisprep.AuthorityNotIssued}}
	knownFailure := func(code string, drained bool) error {
		state := diagnosisprep.Failed
		if ctx.Err() != nil {
			state = diagnosisprep.Cancelled
		}
		return p.set(func(u *diagnosisprep.Update) {
			u.State = state
			u.Phase = diagnosisprep.Closure
			u.ReasonCode = code
			u.DrainComplete = drained
		})
	}
	unknown := func(code string, drained bool) error {
		_ = p.set(func(u *diagnosisprep.Update) {
			authorityMayExist := u.Phase == diagnosisprep.Authority || u.Phase == diagnosisprep.Gateway
			u.State = diagnosisprep.Unknown
			u.Phase = diagnosisprep.Closure
			u.ReasonCode = code
			u.DrainComplete = drained
			if (authorityMayExist || u.AuthorityState != diagnosisprep.AuthorityNotIssued) && u.AuthorityState != diagnosisprep.AuthorityConsumed {
				u.AuthorityState = diagnosisprep.AuthorityUnknown
			}
		})
		return errPreparation
	}
	if l.productTarget != nil {
		// 대상에 묶고, 선언이 있는 대상이면 모델 단계 전에 E③ 리셋 게이트를 본다(결정 §6.1). 거절 사유는 회차의 reason_code다.
		release, refusal := b.product.attachRun(ctx, *l.productTarget, execution.Job.ID, b.config.RunsDir, func() { stopTarget(server.ErrFirstDiagnosisTargetEnded) })
		if refusal != "" {
			return knownFailure(refusal, true)
		}
		defer release()
		// 외부 API 실행에 시험 계정을 쓰면 그 동의(진술 해시·주체·시각·작업)를 시작 전에 감사 저널에 남긴다. 남기지 못하면
		// 시작하지 않는다.
		if l.externalTestAccounts && b.product.targets.recordExternalConsent(execution.Principal, l.productTarget.Target.ID, execution.Job.ID) != nil {
			return knownFailure("external_consent_unrecorded", true)
		}
	}
	var diagnostics *launchDiagnostics
	check := urlRuntimeCheck(ctx, l.current)
	if l.hold() != nil {
		return knownFailure("installation_check_failed", true)
	}
	defer l.releaseInputs()

	startedAt := time.Now()
	ctx, diagnostics, err = beginLaunchDiagnostics(ctx, l.launch, "preparation", 0, io.Discard)
	if err != nil {
		return knownFailure("diagnostic_logging_unavailable", true)
	}
	if diagnostics.logger.BindRun("run-"+execution.Job.ID) != nil {
		code := 1
		diagnostics.finish(&code)
		return knownFailure("diagnostic_logging_unavailable", true)
	}
	finishDiagnostics := func(u *diagnosisprep.Update) {
		if diagnostics.closed {
			return
		}
		code := 1
		if u.State == diagnosisprep.Completed {
			code = 0
		}
		e := diagnosticlog.Event{Component: "cli", Event: "run_summary", Stage: "shutdown", Status: "succeeded", DurationMS: time.Since(startedAt).Milliseconds(), ExecutionClosed: &u.DrainComplete, ResultStored: &u.ResultReady}
		if u.State != diagnosisprep.Completed {
			e.Status = "incomplete"
		}
		if usage := u.ModelUsage; usage != nil {
			e.Count = int(usage.Requests)
			e.InputTokens = usage.InputTokens
			e.OutputTokens = usage.OutputTokens
			e.ReservedInputTokens = usage.ReservedInputTokens
			e.ReservedOutputTokens = usage.ReservedOutputTokens
			e.CostMicros = usage.CostUSDMicros
			e.Complete = &usage.UsageKnown
		}
		_ = diagnosticlog.Emit(ctx, e)
		diagnostics.finish(&code)
		markDiagnosticLogWarning(u, diagnostics.logger.Err() != nil || code != 0, diagnostics.logger.Incomplete())
		// 이 경로는 진단 stderr를 버리므로 로그 장애 사유와 누락 건수를 서버 로그에 남긴다(C10).
		urlDiagnosticLogLoss(diagnostics.logger, log.Printf)
	}
	p.publish = func(u diagnosisprep.Update) error {
		switch u.State {
		case diagnosisprep.Completed, diagnosisprep.Failed, diagnosisprep.Cancelled, diagnosisprep.Unknown:
			u = settleProductWorkflow(parent, flow, u, finishDiagnostics)
		}
		return publish(urlClosingResult(u))
	}
	defer func() {
		if !diagnostics.closed {
			u := p.value()
			finishDiagnostics(&u)
		}
	}()
	diagnostics.next("protected_files", "protected_files_unavailable")
	if check() != nil || p.set(func(u *diagnosisprep.Update) { u.Phase = diagnosisprep.ScopeCheck }) != nil {
		return errPreparation
	}
	if err := l.prepareTargetConnection(ctx, b.resolver, execution.Job.ID); err != nil {
		return knownFailure(targetConnectionFailure(err), true)
	}
	// 위임 삭제·교체·만료는 실행 중에도 신규 전송을 중단하는 context 철회다.
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		watchURLPreparation(hard, watchDone, ticker.C, l.current, cancel, stopTarget)
	}()
	observe := urlModelObserver(ctx, startedAt, l.productTarget == nil, check, func(usage diagnosisprep.ModelUsage) error {
		return p.set(func(u *diagnosisprep.Update) { copy := usage; u.ModelUsage = &copy })
	})
	var modelBudget *server.FirstDiagnosisModelBudget
	if l.launch.ModelExecutionMode == "openrouter" {
		// v4 승인은 이 회차(Job ID)의 장부로 센다. 장부는 여기서 한 번만 새로 만들고, 이미 있으면 예산을 만들지 않는다.
		modelBudget, err = server.NewFirstDiagnosisOpenRouterRunBudget(l.profile.Budget, l.profile.InputTokensPerCall, l.profile.OutputTokensPerCall, l.launch.OpenRouterManifestPath, execution.Job.ID, observe)
	} else {
		modelBudget, err = server.NewFirstDiagnosisModelBudget(l.profile.Budget, l.profile.InputTokensPerCall, l.profile.OutputTokensPerCall, observe)
	}
	if err != nil || modelBudget.ConfigureConcurrency(max(l.launch.ModelConcurrency, 1)) != nil {
		return knownFailure("model_budget_invalid", true)
	}
	if l.productTarget != nil && modelBudget.ConfigureTargetDrain(hard, min(time.Duration(l.launch.ShutdownTimeoutSeconds)*time.Second/2, 30*time.Second), check) != nil {
		return knownFailure("model_budget_invalid", true)
	}
	diagnostics.next("database", "database_unavailable")
	dsnPin, pinErr := l.databasePin()
	if pinErr != nil {
		return knownFailure("database_unavailable", true)
	}
	pool, err := urlOpenDatabase(ctx, b.config, dsnPin)
	if err != nil {
		return knownFailure("database_unavailable", true)
	}
	if checkLaunchFilesContext(ctx, l.launch) != nil || checkLaunchDatabaseRole(ctx, pool, l.launch) != nil || checkLaunchProfile(ctx, pool, l.launch) != nil || check() != nil {
		_ = pool.Close()
		return knownFailure("installation_check_failed", true)
	}
	if p.set(func(u *diagnosisprep.Update) { u.Phase = diagnosisprep.TaskCreation }) != nil {
		_ = pool.Close()
		return errPreparation
	}
	description := "승인된 URL 진단"
	if execution.Job.Input.DisplayName != "" {
		description = execution.Job.Input.DisplayName
	}
	goal, goalCommon, goalProduct := urlDiagnosisGoalParts(execution.Job.Input.TargetURL, execution.Job.Input.WorkflowMode, execution.Job.Scope.Methods, execution.Job.Scope.PathPrefixes, urlGoalAccountLabels(l.policy.Auth))
	if check() != nil {
		_ = pool.Close()
		return knownFailure("installation_changed", true)
	}
	diagnostics.next("task_creation", "task_creation_failed")
	actual, err := server.PrepareFirstDiagnosisTask(ctx, pool, description, goal, l.launch.ModelProfileID, int(l.profile.Budget.DurationSeconds))
	if err != nil {
		_ = pool.Close()
		return unknown("task_creation_unconfirmed", true)
	}
	if diagnostics.bindTask(actual.TaskID) != nil {
		_ = pool.Close()
		return unknown("diagnostic_binding_failed", true)
	}
	// 원본 작업 시작처럼 승인 대상 자산 하나를 등록한다(C24). 원본도 등록 실패로 작업을 멈추지 않으므로 로그만 남긴다.
	if seedErr := server.SeedFirstDiagnosisTarget(ctx, pool, actual, execution.Job.Input.TargetURL); seedErr != nil {
		log.Print("첫 진단 대상 자산 등록 실패, 진단은 계속한다")
	}
	diagnostics.next("task_binding", "task_binding_failed")
	// 재시작 정리: 강제 종료로 drain을 못 한 이전 실행의 작업이 남아 있으면 아래 준비 서버가 시작을 거부한다(#912 재현).
	released, releaseErr := server.ReleaseInterruptedFirstDiagnosisTasks(ctx, pool, actual.TaskID)
	if releaseErr != nil {
		diagnostics.reason = "preparation_interrupted_task_release_failed"
		_ = pool.Close()
		return knownFailure("preparation_host_failed", true)
	}
	if released > 0 {
		log.Printf("재시작 정리: 이전 실행이 마감하지 못한 작업 %d개를 일시정지 상태로 돌림", released)
	}
	taskID := strconv.FormatInt(actual.TaskID, 10)
	flow, flowPool, err = b.createWorkflow(ctx, l, execution, taskID, check)
	if err != nil {
		_ = pool.Close()
		return knownFailure("workflow_storage_unavailable", true)
	}
	if flowPool != nil {
		defer func() {
			b.product.flowMu.Lock()
			delete(b.product.flows, execution.Job.ID)
			b.product.flowMu.Unlock()
			flowPool.Close()
		}()
		if modelBudget.ConfigureAdmission(flow.Wait) != nil {
			_ = pool.Close()
			return knownFailure("model_budget_invalid", true)
		}
	}

	if p.set(func(u *diagnosisprep.Update) { u.TaskID = taskID }) != nil {
		_ = pool.Close()
		return errPreparation
	}
	prepareCtx, finishPrepare := context.WithCancel(ctx)
	manager, err := server.NewFirstDiagnosisManager(prepareCtx, l.launch.DataDir, pool)
	if err != nil {
		diagnostics.reason = "preparation_manager_failed"
		finishPrepare()
		_ = pool.Close()
		return knownFailure("preparation_host_failed", true)
	}
	supervisor, err := server.NewFirstDiagnosisSupervisor(prepareCtx, manager)
	if err != nil {
		diagnostics.reason = "preparation_supervisor_failed"
		finishPrepare()
		manager.Close()
		return knownFailure("preparation_host_failed", true)
	}
	// 목표 문장의 공통 부분·제품 전용 부분 SHA를 실행 기록에 남긴다(평가 러너가 원본 본문의 공통 부분과 대조).
	commonSum, productSum := sha256.Sum256([]byte(goalCommon)), sha256.Sum256([]byte(goalProduct))
	goalCommonSHA, goalProductSHA := hex.EncodeToString(commonSum[:]), hex.EncodeToString(productSum[:])
	if supervisor.SetGoalDigests(goalCommonSHA, goalProductSHA) != nil {
		diagnostics.reason = "preparation_goal_binding_failed"
		finishPrepare()
		manager.Close()
		return knownFailure("preparation_host_failed", true)
	}
	options, optionsErr := l.launch.modelOptions()
	if optionsErr != nil {
		diagnostics.reason = "preparation_options_invalid"
		finishPrepare()
		manager.Close()
		return knownFailure("preparation_host_failed", true)
	}
	options.Budget = modelBudget
	preparation, err := server.NewFirstDiagnosisPreparationServerWithModel(prepareCtx, manager, l.launch.SkillDir, l.launch.DataDir, l.launch.KeyDir, options)
	if err != nil {
		diagnostics.reason = "preparation_server_failed"
		finishPrepare()
		manager.Close()
		return knownFailure("preparation_host_failed", true)
	}
	if supervisor.Attach(preparation) != nil {
		diagnostics.reason = "preparation_supervisor_attach_failed"
		finishPrepare()
		manager.Close()
		return knownFailure("preparation_host_failed", true)
	}
	if check() != nil {
		finishPrepare()
		manager.Close()
		return knownFailure("installation_changed", true)
	}
	if p.set(func(u *diagnosisprep.Update) { u.Phase = diagnosisprep.Planning }) != nil {
		finishPrepare()
		manager.Close()
		return errPreparation
	}
	diagnostics.next("planning", "planner_failed")
	planErr := supervisor.PrepareIntents(prepareCtx, taskID)
	// 실제 intent 행이 같은 exploration에 저장됐는지 마감 전에 독립 조회한다.
	var intents int
	if planErr == nil {
		if pool.QueryRowContext(ctx, `SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND kind='intent' AND state='open'`, actual.ExplorationID).Scan(&intents) != nil || intents > 128 {
			planErr = errPreparation
		}
	}
	cleanup, done := launchCleanupContext(context.WithoutCancel(parent), l.launch)
	shutdown := supervisor.Shutdown()
	var closeErr error
	for _, phase := range []func(context.Context, *server.Server) error{shutdown.BlockAdmission, shutdown.CancelAndDrain, shutdown.CheckResults} {
		if phase == nil {
			closeErr = errPreparation
			break
		}
		if closeErr = phase(cleanup, preparation); closeErr != nil {
			break
		}
	}
	saveErr := supervisor.PersistPreparationModelUsage(cleanup, taskID, errors.Join(planErr, closeErr))
	done()
	finishPrepare()
	if closeErr == nil && manager.Close() != nil {
		closeErr = errPreparation
	}
	if closeErr != nil || saveErr != nil {
		if closeErr == nil {
			return unknown("preparation_drain_unconfirmed", true)
		}
		// 결과·사용량은 바꾸지 않고 이미 취소한 준비 자원의 drain만 다시 검증한다.
		recovery := &urlPreparationRecovery{taskID: taskID, closeResources: func(recoveryCtx context.Context) error {
			for _, phase := range []func(context.Context, *server.Server) error{shutdown.BlockAdmission, shutdown.CancelAndDrain} {
				if phase == nil {
					return errPreparation
				}
				if err := phase(recoveryCtx, preparation); err != nil {
					return errPreparation
				}
			}
			if manager.Close() != nil {
				return errPreparation
			}
			return nil
		}}
		b.mu.Lock()
		if b.preparationRecovery == nil {
			b.preparationRecovery = map[string]*urlPreparationRecovery{}
		}
		b.preparationRecovery[execution.Job.ID] = recovery
		b.mu.Unlock()
		return unknown("preparation_drain_unconfirmed", false)
	}
	usage := modelBudget.Snapshot()
	if !usage.UsageKnown {
		return unknown("model_usage_unknown", true)
	}
	if planErr != nil {
		return knownFailure("planning_failed", true)
	}
	if check() != nil {
		return knownFailure("installation_changed", true)
	}
	if p.set(func(u *diagnosisprep.Update) { u.Phase = diagnosisprep.Authority }) != nil {
		return errPreparation
	}
	diagnostics.next("e2_policy", "e2_policy_failed")
	launch, err := b.issue(l, execution.Job.ID, actual)
	// 실행 디렉터리에 옮긴 시험 계정 자격 사본은 실행이 끝나면(마감·실패·중지·결과 불명 모두) 지운다. 실행 디렉터리는
	// 저장 보고서·작업대 조회 때문에 남으므로 사본이 쌓이지 않게 한다(#551 윤재상 리뷰). refresh_session이 실행 중에
	// 다시 읽으므로 인증 준비 직후가 아니라 실행이 끝날 때 지운다.
	defer removeRunCredentials(b.config.RunsDir, execution.Job.ID, l.policy.Auth)
	if err != nil {
		return unknown("authority_issuance_unconfirmed", true)
	}
	if p.set(func(u *diagnosisprep.Update) {
		u.AuthorityState = diagnosisprep.AuthorityIssued
		u.Phase = diagnosisprep.Gateway
	}) != nil {
		return errPreparation
	}
	if check() != nil {
		return unknown("installation_changed", true)
	}
	diagnostics.next("host_startup", "host_startup_failed")
	ctx = withURLExplorationStage(ctx, b.productExplorationStage(l, execution, taskID, flow))
	d, err := startLaunchWithDatabase(ctx, launch, modelBudget, check, func(call context.Context, path string) (*sql.DB, error) {
		if path != dsnPin.path {
			return nil, errPreparation
		}
		return urlOpenDatabase(call, b.config, dsnPin)
	}, flow)
	if err != nil {
		drained := d == nil
		if d != nil {
			cleanup, done := launchCleanupContext(context.WithoutCancel(parent), launch)
			closeErr := d.Close(cleanup)
			done()
			drained = closeErr == nil
		}
		return unknown("gateway_startup_unconfirmed", drained)
	}
	if p.set(func(u *diagnosisprep.Update) { u.AuthorityState = diagnosisprep.AuthorityConsumed }) != nil {
		cleanup, done := launchCleanupContext(context.WithoutCancel(parent), launch)
		_ = d.Close(cleanup)
		done()
		return errPreparation
	}
	// 회차 실행 시간 상한에 닿기 전에 budget_limited 마감을 시작해, 끝난 작업의 결과를 버리지 않고 실행 시간 안에서
	// 마감한다(실행 결과 상태 값 계약 §3.4). 실행 끝 시각은 위 hard 문맥의 기한이다.
	// 실행 기록(실행 단계 감독자가 씀)에도 준비 때와 같은 목표 SHA를 남긴다. 준비 단계 감독자에만 넣으면 실행 기록의
	// goal 칸이 비어 평가 러너가 원본 본문의 공통 부분과 대조하지 못한다(VAmPI 3차 회차에서 발견).
	// 제품 대상 회차는 모델 단계가 끝난 뒤 E② 마감 전에 E③ 확정 단계를 돈다(결정 §5). 시험 계정 인증 결과는 실행 runtime에서
	// 그때 읽는다.
	var e3Confirmation server.FirstDiagnosisE3Hooks
	// 확정 단계 시간 몫은 모델 단계 시간 상한에서 뺀다(모델 단계는 "전체 상한 − 확정 몫"에서 끝난다). 발급 정책을 읽지 못하면
	// 확정 단계가 쓸 시간을 지킬 수 없으므로 시작하지 않는다.
	var confirmShare time.Duration
	var shareErr error
	if b.product != nil && l.productTarget != nil {
		runtime := d.runtime
		confirmation := newProductE3Confirmation(b.config.RunsDir, execution.Job.ID, *l.productTarget, b.product.targets,
			func() map[string]string { return launchAccountAuthForE3(runtime) })
		if local, ok := runtime.(*server.FirstDiagnosisHTTPRuntime); ok {
			confirmation.handle = local.E3HandleReason
			confirmation.store = local.RecordProductVerdict
		}
		confirmation.modelTag = productE3ModelTag(l.inspection.Scope, l.launch.ModelProfileID)
		e3Confirmation = server.FirstDiagnosisE3Hooks{Run: confirmation.run, Stopped: confirmation.stopped}
		confirmShare, shareErr = confirmation.timeShare()
	}
	if end, ok := hard.Deadline(); !ok || shareErr != nil || d.supervisor == nil || d.supervisor.SetTimeBudget(urlTimeBudgetClose(end, time.Duration(l.profile.Budget.DurationSeconds)*time.Second, time.Duration(launch.ShutdownTimeoutSeconds)*time.Second, confirmShare)) != nil ||
		d.supervisor.SetGoalDigests(goalCommonSHA, goalProductSHA) != nil || e3Confirmation.Run != nil && d.supervisor.SetE3Confirmation(e3Confirmation) != nil {
		cleanup, done := launchCleanupContext(context.WithoutCancel(parent), launch)
		closeErr := d.Close(cleanup)
		done()
		return unknown("gateway_unavailable", closeErr == nil)
	}
	liveHandler := newURLLiveHandler(d.runtime.Handler())
	if runtime, ok := d.runtime.(*server.FirstDiagnosisHTTPRuntime); ok && b.product != nil && l.productTarget != nil {
		liveHandler.candidates = &urlLiveCandidates{
			handler: runtime.ProductCandidateHandler,
			jobID:   execution.Job.ID, sourceDigest: execution.Job.SourceDigest,
			targetRef: l.productTarget.Target.ID, targetRevision: l.productTarget.Target.Revision,
		}
	}
	b.mu.Lock()
	b.live[taskID] = liveHandler
	b.mu.Unlock()
	runner := &urlGatewayRunner{backend: b, launch: d, config: launch, loaded: l, taskID: taskID, operatorStop: func() bool {
		return urlOperatorStop(parent, ctx)
	}}
	b.mu.Lock()
	if b.recovery == nil {
		b.recovery = map[string]*urlGatewayRunner{}
	}
	b.recovery[execution.Job.ID] = runner
	runner.recoveryLive = b.live[taskID]
	b.mu.Unlock()
	gateway, err := diagnosis.NewPrepared(ctx, taskID, execution.Job.ID, runner, time.Duration(launch.ShutdownTimeoutSeconds)*time.Second)
	if err != nil {
		cleanup, done := launchCleanupContext(context.WithoutCancel(parent), launch)
		_, _ = runner.Close(cleanup)
		done()
		return unknown("gateway_unavailable", true)
	}
	// Started는 모델 준비가 아니라 실제 독립 Gateway Start의 확인된 사실이다.
	if check() != nil {
		cleanup, done := launchCleanupContext(context.WithoutCancel(parent), launch)
		_ = gateway.Shutdown(cleanup)
		done()
		return unknown("installation_changed", true)
	}
	if p.set(func(u *diagnosisprep.Update) { u.Phase = diagnosisprep.ExecutionPhase }) != nil {
		cleanup, done := launchCleanupContext(context.WithoutCancel(parent), launch)
		_ = gateway.Shutdown(cleanup)
		done()
		return errPreparation
	}
	// 기록 실패로 실행을 취소하지 않는다(C10).
	_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "cli", Event: "stage_finished", Stage: "admission", Status: "succeeded", DurationMS: time.Since(startedAt).Milliseconds()})
	diagnostics.next("execution", "execution_failed")
	response, err := gateway.Call(ctx, diagnosis.Request{Version: diagnosis.Version, RequestID: "url-start", Command: diagnosis.Start, TaskID: taskID, PreparationRef: execution.Job.ID, IdempotencyKey: execution.Job.ID})
	if err != nil || response.Snapshot == nil {
		return unknown("gateway_start_unconfirmed", false)
	}
	if p.set(func(u *diagnosisprep.Update) { u.State = diagnosisprep.Running; u.Started = response.Snapshot.Started }) != nil {
		cancel()
	}
	ticker := time.NewTicker(urlStatusPollInterval)
	defer ticker.Stop()
	statusReads := urlStatusReads{retries: launch.StatusReadRetries}
	statusLost := false
	for {
		select {
		case <-ctx.Done():
			cleanup, done := launchCleanupContext(context.WithoutCancel(parent), launch)
			_ = gateway.Shutdown(cleanup)
			done()
			state := runner.final()
			usage := modelBudget.Snapshot()
			if state == nil {
				return unknown("execution_closure_unconfirmed", false)
			}
			return p.set(urlStoppedUpdate(ctx, state, usage, statusLost))
		case <-ticker.C:
			status, e := gateway.Call(ctx, diagnosis.Request{Version: diagnosis.Version, RequestID: "url-status", Command: diagnosis.Status, TaskID: taskID, PreparationRef: execution.Job.ID})
			if e != nil || status.Snapshot == nil {
				// 정한 횟수까지는 다시 묻는다(C10). 다 쓰면 실행을 멈추고 결과를 unknown으로 둔다.
				// A target stop may already be draining model usage. A failed
				// status read with that cancelled context must not hard-cancel it.
				statusLost = urlStatusReadFailed(ctx, cancel, &statusReads) || statusLost
				continue
			}
			statusReads.succeeded()
			s := status.Snapshot
			switch s.State {
			case diagnosis.Completed, diagnosis.Cancelled, diagnosis.Failed, diagnosis.Unknown:
				usage := modelBudget.Snapshot()
				return p.set(func(u *diagnosisprep.Update) {
					u.Phase = diagnosisprep.Report
					u.State = diagnosisprep.State(s.State)
					u.Started = s.Started
					u.DrainComplete = s.DrainComplete
					u.ResultReady = s.ResultReady
					u.ModelUsage = &usage
					u.Result = s.Result
					u.AccountAuth = s.AccountAuth
					if !usage.UsageKnown {
						u.State = diagnosisprep.Unknown
						u.ReasonCode = "model_usage_unknown"
					}
				})
			}
		}
	}
}

type urlGatewayRunner struct {
	runDone      chan struct{}
	runStarted   bool
	closing      bool
	recoveryLive *urlLiveHandler
	backend      *urlPreparationRuntime
	launch       *launchedDiagnosis
	config       launchConfig
	loaded       *urlLoadedProfile
	taskID       string
	// operatorStop은 실행이 운영자 중지(진단 준비 중지가 상위 문맥을 취소)나 대상 철회로 끝났는지 본다. 설치·위임
	// 검사 실패, 상태 조회 실패, 시간 상한은 여기에 들지 않는다(저장 기록은 failed 그대로).
	operatorStop func() bool
	mu           sync.Mutex
	closed       *diagnosis.Closure
	runErr       error
}

func (r *urlGatewayRunner) Run(ctx context.Context) error {
	r.mu.Lock()
	if r.closing || r.runStarted || r.launch == nil || r.launch.supervisor == nil {
		r.mu.Unlock()
		return errPreparation
	}
	r.runStarted = true
	if r.runDone == nil {
		r.runDone = make(chan struct{})
	}
	r.mu.Unlock()
	err := r.launch.supervisor.Run(ctx, r.taskID, 0, "")
	r.mu.Lock()
	r.runErr = err
	close(r.runDone)
	r.mu.Unlock()
	return err
}
func (r *urlGatewayRunner) final() *diagnosis.Closure {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed == nil {
		return nil
	}
	value := *r.closed
	return &value
}

// urlTimeCloseShare는 시간 상한 마감에 쓰는 유예·결과 저장 여유가 각각 회차 실행 시간에서 차지할 수 있는 몫의
// 분모다. 짧은 회차도 실행 시간의 절반 이상은 작업에 쓴다.
const urlTimeCloseShare = 4

// urlTimeBudgetClose는 시간 상한 마감 시작 시각과 진행 중 작업 유예를 정한다. E③ 확정 단계 시간 몫(confirm, 실행 시작 때
// 발급 정책에 적은 값), 유예(엔진 정리 유예 상한), 결과 저장 여유(launch 정리 시간 상한)를 실행 끝 시각에서 빼서, 모델 단계가
// 시간 상한까지 가도 확정 단계가 예약한 시간을 다 쓰고 마감이 승인한 실행 시간 안에서 끝나게 한다. 계획이 비어도 마감하지
// 않으므로(원본과 같은 끝 규칙) 확정 단계를 켠 회차는 보통 이 시각에 모델 단계를 끝낸다. 확정 몫은
// server.ComputeE3ConfirmationShare가 실행 시간의 1/4 이하로 정하므로 모델 단계는 실행 시간의 1/4 이상을 쓴다.
func urlTimeBudgetClose(end time.Time, budget, shutdown, confirm time.Duration) (time.Time, time.Duration) {
	drain := min(server.FirstDiagnosisTimeBudgetDrain, budget/urlTimeCloseShare)
	margin := min(shutdown, budget/urlTimeCloseShare)
	return end.Add(-confirm - drain - margin), drain
}

// urlStoppedUpdate는 실행 문맥이 끝나 멈춘 실행(운영자 중지·대상 철회·결속 계정 변경·상태 조회 포기)의 마감 갱신이다.
// 마감과 사용량을 확인하지 못하면 결과 불명으로 둔다.
func urlStoppedUpdate(ctx context.Context, state *diagnosis.Closure, usage diagnosisprep.ModelUsage, statusLost bool) func(*diagnosisprep.Update) {
	return func(u *diagnosisprep.Update) {
		u.Phase = diagnosisprep.Report
		u.State = diagnosisprep.Cancelled
		u.ReasonCode = urlStopReason(ctx)
		if statusLost {
			markURLStatusLost(u)
		}
		u.ModelUsage = &usage
		u.Started = true
		u.DrainComplete = state.DrainComplete
		u.ResultReady = state.ResultReady
		// 마감 때 읽은 intent 결과 개수(C08 2-2). urlClosingResult가 중지에는 개수만 남긴다.
		u.Result = state.Result
		u.AccountAuth = state.AccountAuth
		if !state.DrainComplete || !state.ResultReady {
			u.State = diagnosisprep.Unknown
			u.ReasonCode = "execution_closure_unconfirmed"
		}
		if !usage.UsageKnown {
			u.State = diagnosisprep.Unknown
			u.ReasonCode = "model_usage_unknown"
		}
	}
}

// urlClosingResult는 마감 갱신의 실행 결과 칸을 상태에 맞춘다. 결과는 completed로 마감한 경우에 싣고, 중지
// (cancelled)에는 intent 결과 개수만 싣는다(계약 §3.2). 마감 확인 실패 등으로 상태가 바뀌었으면 지운다.
func urlClosingResult(u diagnosisprep.Update) diagnosisprep.Update {
	switch u.State {
	case diagnosisprep.Completed:
	case diagnosisprep.Cancelled:
		if u.Result != nil {
			u.Result = &diagnosis.RunResult{IntentOutcomes: u.Result.IntentOutcomes}
		}
		u.DiagnosticLogIncomplete = false
	default:
		u.Result = nil
		u.DiagnosticLogIncomplete = false
	}
	// 계정별 인증 결과는 completed·cancelled·failed에 싣는다(계약 §3.4). 결과 불명 등에는 싣지 않는다.
	if u.State != diagnosisprep.Completed && u.State != diagnosisprep.Cancelled && u.State != diagnosisprep.Failed {
		u.AccountAuth = nil
	}
	return u
}

// launchAccountAuth는 실행 runtime이 아는 시험 계정별 인증 준비 결과다. 계정을 결속하지 않았거나 모르면 nil이다.
func launchAccountAuth(runtime any) map[string]string {
	if a, ok := runtime.(interface{ AccountAuth() map[string]string }); ok {
		return diagnosis.CloneAccountAuth(a.AccountAuth())
	}
	return nil
}

// E③ 시작 검사는 최초 준비 기록 대신 같은 회차에 지금 결속된 세션을 본다. 뒤이어 E③가 재로그인·신원을 확인한다.
func launchAccountAuthForE3(runtime any) map[string]string {
	if a, ok := runtime.(interface{ AccountAuthForE3() map[string]string }); ok {
		return diagnosis.CloneAccountAuth(a.AccountAuthForE3())
	}
	return launchAccountAuth(runtime)
}

func (r *urlGatewayRunner) Close(ctx context.Context) (diagnosis.Closure, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closing = true
	if r.closed != nil {
		return *r.closed, nil
	}
	resultErr := r.launch.runtime.Close(ctx)
	drained := resultErr == nil
	if state, ok := r.launch.runtime.(interface{ ResourcesClosed() bool }); ok {
		drained = state.ResourcesClosed()
	}
	closureErr := resultErr
	if drained {
		closureErr = nil
	}
	stored, saveErr := r.launch.supervisor.PersistRunOutcomeStoredStop(ctx, closureErr, r.operatorStop != nil && r.operatorStop())
	result := diagnosis.Closure{DrainComplete: drained, ResultReady: stored && saveErr == nil, Result: r.launch.supervisor.ClosureResult(), AccountAuth: launchAccountAuth(r.launch.runtime)}
	if drained {
		// map 교체는 진행 중 HTTP handler가 끝난 뒤 수행한다. 실행 DB와 보고서 DB는 별개다.
		r.backend.mu.Lock()
		live := r.backend.live[r.taskID]
		delete(r.backend.live, r.taskID)
		r.backend.mu.Unlock()
		if live != nil && live.drain(ctx) != nil {
			result.DrainComplete = false
			r.closed = &result
			return result, errPreparation
		}
		if result.ResultReady {
			task, _ := strconv.ParseInt(r.taskID, 10, 64)
			report, err := r.backend.openURLReport(r.backend.ctx, r.loaded, task)
			if err != nil {
				saveErr = errors.Join(saveErr, errPreparation)
			} else {
				r.backend.mu.Lock()
				r.backend.reports[r.taskID] = report
				r.backend.mu.Unlock()
			}
		}
		if r.closeStores() != nil {
			result.DrainComplete = false
			r.closed = &result
			return result, errors.Join(errPreparation, saveErr)
		}
	}
	r.closed = &result
	if result.DrainComplete {
		r.forgetRecovery()
	}
	if !drained {
		return result, errors.Join(resultErr, saveErr)
	}
	if resultErr != nil && (!errors.Is(resultErr, server.ErrFirstDiagnosisOutcome) || !onlyRunOutcome(resultErr, r.runErr)) {
		return result, errors.Join(resultErr, saveErr)
	}
	return result, saveErr
}
func (b *urlPreparationRuntime) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if server.ProductCandidatePath(r) {
		b.serveLiveCandidates(w, r)
		return
	}
	task := r.URL.Query().Get("task")
	if tail, ok := strings.CutPrefix(r.URL.Path, "/api/tasks/"); ok {
		task = strings.Split(tail, "/")[0]
	}
	id, err := strconv.ParseInt(task, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != task || b.endpoint == nil {
		http.Error(w, "저장 진단을 확인할 수 없습니다", 404)
		return
	}
	auth, err := b.endpoint.Call(r.Context(), diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "report-binding", Command: diagnosisprep.AuthorizeTask, Principal: "ARTEX", TaskID: task})
	if err != nil || auth.Authorized == nil || !*auth.Authorized || auth.Job == nil || auth.Job.TaskID != task {
		http.Error(w, "작업 결속을 확인할 수 없습니다", 403)
		return
	}
	// 작업대 실데이터(통제 정책·전송 통제 흐름)는 실행 결속 디렉터리에서 직접 읽는다(계약 workbench-live-data §3·§4).
	if view := liveDataView(r.URL.Path); view != "" {
		serveLiveData(w, r, b.config.RunsDir, auth.Job.ID, view)
		return
	}
	if auth.Job.Scope != nil {
		r = r.WithContext(server.WithDiagnosticReportScope(r.Context(), task, *auth.Job.Scope))
	}
	if b.product != nil && r.URL.Path == "/api/report/diagnostic" {
		r = r.WithContext(server.WithDiagnosticProductReport(r.Context(), task, auth.Job.ID,
			productPreparationEndpoint{base: b.endpoint, product: b.product}))
	}
	b.mu.RLock()
	if live := b.live[task]; live != nil {
		b.mu.RUnlock()
		live.ServeHTTP(w, r)
		return
	}
	if report := b.reports[task]; report != nil {
		b.mu.RUnlock()
		report.handler.ServeHTTP(w, r)
		return
	}
	b.mu.RUnlock()
	// 재시작 후 저장 결과 조회는 실행이나 모델을 복원하지 않는다.
	b.mu.Lock()
	if b.reports[task] == nil {
		targetRef := ""
		if auth.Job.Scope != nil {
			targetRef = auth.Job.Scope.TargetRef
		}
		loaded, loadErr := b.loadForReport("ARTEX", auth.Job.Input, true, targetRef, auth.Job.SourceDigest)
		if loadErr == nil {
			defer loaded.clearMaterials()
			// 만료는 저장 보고서를 지우지 않지만 설치/DB pin 교체를 과거 task에 적용하지 않는다.
			valid := diagnosisprep.SourceDigest(auth.Job.Input, loaded.inspection.InstallationDigest) == auth.Job.SourceDigest
			for _, pin := range loaded.pins {
				if pin.check() != nil {
					valid = false
					break
				}
			}
			if valid {
				report, openErr := b.openURLReport(b.ctx, loaded, id)
				if openErr == nil {
					for _, pin := range loaded.pins {
						if pin.check() != nil {
							valid = false
							break
						}
					}
					if !valid {
						_ = report.close()
					} else {
						b.reports[task] = report
					}
				}
			}
		}
	}
	b.mu.Unlock()
	b.mu.RLock()
	report := b.reports[task]
	b.mu.RUnlock()
	if report == nil {
		http.Error(w, "저장 보고서를 사용할 수 없습니다", 503)
		return
	}
	report.handler.ServeHTTP(w, r)
}
func (b *urlPreparationRuntime) closeReports() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	var err error
	for task, report := range b.reports {
		err = errors.Join(err, report.close())
		delete(b.reports, task)
	}
	return err
}

var _ diagnosisprep.Backend = (*urlPreparationRuntime)(nil)
var _ http.Handler = (*urlPreparationRuntime)(nil)

// urlDiagnosticLogLoss는 진단 로그 장애가 있었으면 사유와 누락 건수만 서버 로그로 남긴다. 기록 내용은 남기지 않는다.
// URL 실행은 진단 경고 stderr를 버리기 때문에(launch 진단 기본 출력) 이 줄이 운영자가 보는 유일한 흔적이다.
func urlDiagnosticLogLoss(logger *diagnosticlog.Logger, printf func(string, ...any)) {
	if logger == nil || logger.Err() == nil {
		return
	}
	printf("[first-diagnosis] 진단 로그 기록 실패: 사유=%s 누락=%d건", diagnosticFailureCode(logger.Err()), logger.Dropped())
}

// markDiagnosticLogWarning은 진단 로그 상태를 실행 결과에 반영하는 유일한 곳이다. 진단 로그는 감사가 아니다
// (P3 결정안 C10). 기록 실패·관찰 불완전 표시는 실행 상태를 바꾸지 않고, 완료 실행에 경고 사유로만 남긴다.
// 실행 종료·감사·결과 저장의 확인은 Update의 다른 값(DrainComplete·ResultReady·ModelUsage)이 맡는다.
func markDiagnosticLogWarning(u *diagnosisprep.Update, logFailed, observationIncomplete bool) {
	// 마감 사유(reason_code)와 따로 둔다. 다른 사유가 있어도 경고가 덮이지 않는다(실행 결과 상태 값 계약 §3.2).
	if u.State == diagnosisprep.Completed && (logFailed || observationIncomplete) {
		u.DiagnosticLogIncomplete = true
	}
}

// urlRuntimeCheck는 실행 중 단계마다 쓰는 공통 검사다: 문맥, 설치·위임·대상 승인(current).
// 진단 로거 상태는 보지 않는다(P3 결정안 C10). 로거를 처음 만들 수 없으면 실행 전에 실패하는 것은 따로다.
func urlRuntimeCheck(ctx context.Context, current func() error) func() error {
	return func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return current()
	}
}

// urlModelObserver는 모델 예산의 예약·정산 기록 콜백이다. 새 예약(요청 수 증가) 전에 admission 검사를 하고
// (checkAdmission이면), 저장은 save가 맡는다. 진단 로그 기록 실패는 예약을 막지 않는다(C10). 이 콜백이
// 오류를 내면 모델 예산이 영구 차단되므로, 여기에 진단 로거 의존을 넣지 않는다.
func urlModelObserver(ctx context.Context, startedAt time.Time, checkAdmission bool, check func() error, save func(diagnosisprep.ModelUsage) error) func(diagnosisprep.ModelUsage) error {
	var lastModelRequest int64
	return func(usage diagnosisprep.ModelUsage) error {
		if usage.Requests > lastModelRequest {
			// 대상 호출 admission은 예약 전에 검사한다. 마감 usage는 종료 뒤에도 저장한다.
			if checkAdmission && check() != nil {
				return errPreparation
			}
			lastModelRequest = usage.Requests
			if usage.Requests == 1 {
				_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "cli", Event: "model_request_started", Stage: "model_call", Status: "started", Count: 1, DurationMS: time.Since(startedAt).Milliseconds()})
			}
		}
		return save(usage)
	}
}

// 진단 로그 기록 실패는 여기서 실행을 취소하지 않는다(P3 결정안 C10). 감사·E② 집행 장애는 실행 경로가 직접 멈춘다.
func watchURLPreparation(hard context.Context, done <-chan struct{}, ticks <-chan time.Time, current func() error, cancel context.CancelFunc, stopTarget context.CancelCauseFunc) {
	for {
		select {
		case <-done:
			return
		case <-hard.Done():
			return
		case <-ticks:
			if err := current(); err != nil {
				// 대상 철회와 결속 계정 변경(해제·만료·판 변경·자격 교체)은 운영자 쪽 사건이다. 원인을 실어 멈추고 마감은
				// 이어서 확인한다. 실행 결과에 원인이 남는다(C14, 계약 run-outcome-states §3.4).
				if errors.Is(err, server.ErrFirstDiagnosisTargetEnded) || errors.Is(err, errAccountBindingChanged) {
					stopTarget(err)
					continue
				}
				cancel()
				return
			}
		}
	}
}

// urlStopReason은 중지(cancelled) 실행의 사유 코드다. 결속한 시험 계정이 해제·만료·판 변경·자격 교체되어 새 송신을
// 멈춘 경우만 이름을 붙인다(C14). 다른 중지는 지금처럼 사유 없이 둔다.
func urlStopReason(ctx context.Context) string {
	if errors.Is(context.Cause(ctx), errAccountBindingChanged) {
		return "account_binding_changed"
	}
	return ""
}

// urlOperatorStop은 실행 문맥이 운영자 중지(진단 준비 서비스가 상위 문맥을 취소)·대상 철회
// (ErrFirstDiagnosisTargetEnded)·결속 계정 변경으로 끝났는지 본다. 감시자의 설치·위임 검사 실패, 상태 조회·진행 저장
// 실패, 시간 상한은 원인 없는 취소나 시간 초과라 운영자 중지가 아니다.
func urlOperatorStop(parent, ctx context.Context) bool {
	cause := context.Cause(ctx)
	return parent.Err() != nil || errors.Is(cause, server.ErrFirstDiagnosisTargetEnded) || errors.Is(cause, errAccountBindingChanged)
}

// A failed read after a soft target stop must preserve an active usage drain.
// cancelURLStatusFailure는 진행 중인 실행을 멈추고 멈췄는지 돌려준다. 대상 철회로 이미 정리 중이면 건드리지 않는다.
func cancelURLStatusFailure(ctx context.Context, cancel context.CancelFunc) bool {
	if ctx.Err() == nil {
		cancel()
		return true
	}
	return false
}

// urlStatusReads는 실행 상태 조회의 연속 실패를 센다(C10). retries만큼은 다시 묻고, 그보다 많이 연속으로 실패하면
// 포기한다. 한 번 성공하면 다시 센다.
type urlStatusReads struct{ retries, failures int }

func (s *urlStatusReads) failed() bool {
	s.failures++
	return s.failures > s.retries
}

func (s *urlStatusReads) succeeded() { s.failures = 0 }

// urlStatusReadFailed는 상태 조회 실패 한 번을 처리한다. 정한 횟수를 넘어 진행 중인 실행을 멈췄으면 true다(결과 불명).
func urlStatusReadFailed(ctx context.Context, cancel context.CancelFunc, reads *urlStatusReads) bool {
	return reads.failed() && cancelURLStatusFailure(ctx, cancel)
}

// markURLStatusLost는 상태 조회를 다 못 해 멈춘 실행을 unknown으로 둔다(C10). 이미 다른 사유로 unknown이면 그 사유를 둔다.
func markURLStatusLost(u *diagnosisprep.Update) {
	if u.State == diagnosisprep.Cancelled {
		u.State = diagnosisprep.Unknown
		u.ReasonCode = "status_read_failed"
	}
}

func settleProductWorkflow(ctx context.Context, flow *diagnosisflow.Controller, u diagnosisprep.Update, finish func(*diagnosisprep.Update)) diagnosisprep.Update {
	finish(&u)
	if u.ModelUsage != nil && !u.ModelUsage.UsageKnown {
		u.State = diagnosisprep.Unknown
		u.ReasonCode = "model_usage_unknown"
	}
	if flow != nil {
		if flow.Failed() && u.State != diagnosisprep.Unknown {
			u.State = diagnosisprep.Failed
			u.ReasonCode = "workflow_failed"
		}
		if flow.Close(context.WithoutCancel(ctx), workflowTerminal(u.State)) != nil && u.State != diagnosisprep.Unknown {
			u.State = diagnosisprep.Failed
			u.ReasonCode = "workflow_projection_failed"
		}
	}
	return u
}

// urlDiagnosisGoal은 작업 목표 문장을 승인 기록의 메서드·작업 방식·결속 계정 라벨로 만든다(루프 복원 결정안 §3, C25). 모델이 정한
// 값이 아니다. 읽기 메서드만 승인됐으면 읽기 전용이라고 적는다. 쓰기 메서드가 승인됐으면 그 메서드를 밝히고, 건별 확인 흐름
// (수동 모드, 작업 방식 없는 실행)에서는 요청마다 운영자 확인을 받아야 E②가 보낸다고, 자동 모드에서는 사전 승인 범위 안에서
// 건별 확인 없이 보낸다고 적는다(C21). 실제 허용·차단은 E②가 정한다.
//
// 목적 문장은 취약점 찾기다. 원본 ARTEX는 사람이 쓴 목표("대상의 취약점을 찾아라")를 목표 분해기가 결과로 나누는데, 이
// 문장이 관찰·후보 모으기만 말하면 분해기가 "엔드포인트 정리" 같은 목표를 만들고 Planner가 탐색이 끝나자마자 그 목표를
// 달성으로 마감한다(2026-11-14 소유 Juice Shop 회차: 목표 하나가 탐색 뒤 met, 진단 Worker 둘이 명령 1·3번 만에 멈춤,
// 후보 제출 0). 인가(IDOR·접근통제)를 먼저 적는 것은 개발 계획의 초기 집중 순서다. 계정 라벨은 이름만이고 세션 값은
// Worker 실행에서만 준다(CLAUDE.md 규칙 2).
// urlDiagnosisGoal은 작업 목표 전체 문장이다. 공통 부분·제품 전용 부분은 urlDiagnosisGoalParts가 나눠 준다.
func urlDiagnosisGoal(target, workflowMode string, methods, paths, accountLabels []string) string {
	full, _, _ := urlDiagnosisGoalParts(target, workflowMode, methods, paths, accountLabels)
	return full
}

// urlDiagnosisGoalParts는 목표 전체 문장과, 원본(B) 본문에도 같게 들어가는 공통 부분, 제품(O)에만 있는 부분(E② 경계·
// 워크플로·끝 문장을 순서대로 이은 글)을 돌려준다. 실행 기록에는 두 부분의 SHA를 따로 남긴다(평가 결정안 §6).
func urlDiagnosisGoalParts(target, workflowMode string, methods, paths, accountLabels []string) (full, common, product string) {
	var writes []string
	for _, m := range methods {
		if m != "GET" && m != "HEAD" {
			writes = append(writes, m)
		}
	}
	scope := "Inspect the approved read-only HTTPS target " + target + " through the enforced E2 boundary and find its security vulnerabilities. Approved methods: " + strings.Join(methods, ", ") + "."
	if len(writes) > 0 {
		scope = "Inspect the approved HTTPS target " + target + " through the enforced E2 boundary and find its security vulnerabilities. Approved methods: " + strings.Join(methods, ", ") + "."
		if workflowMode == diagnosisflow.Auto {
			scope += " Write methods (" + strings.Join(writes, ", ") + ") are pre-approved for this automatic run within the approved scope: E2 sends them without per-request confirmation and still blocks requests outside the scope. Never resend a request whose result is unknown."
		} else {
			scope += " Write methods (" + strings.Join(writes, ", ") + ") are approved only per request: E2 holds each such request until the operator confirms it. Never invent that confirmation and never resend a request whose result is unknown."
		}
	}
	common = urlDiagnosisCommonGoal(urlGoalOrigin(target), paths, accountLabels)
	if workflowMode == "" {
		tail := " Do not request other origins."
		if len(writes) == 0 {
			tail = " Do not request state changes or other origins."
		}
		return scope + common + tail, common, scope + tail
	}
	flow := " Workbench workflow mode: " + workflowMode + ". First prepare an initial discovery intent. The host will perform bounded discovery in the protected Worker. After discovery, plan deep diagnostic intents from the endpoints the server lists, following the workflow instructions given with them."
	if len(writes) == 0 {
		flow += " State-changing requests require separately granted scope; never invent that permission."
	}
	tail := " Do not request other origins."
	return scope + flow + common + tail, common, scope + flow + tail
}

// urlGoalOrigin은 점검 주소의 origin(scheme://host[:port])이다. 승인 경로를 같은 origin의 절대 URL로 렌더할 때 쓴다.
func urlGoalOrigin(target string) string {
	u, err := url.Parse(target)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// urlDiagnosisCommonGoal은 목표 문장 중 원본(B) 작업 본문에도 바이트까지 같게 들어가는 부분이다: 승인 경로 목록,
// 목적(인가 먼저·시험했다고 볼 범위), 보고 문장. E② 경계·워크플로 문장은 제품(O)에만 있는 선언 차이라 여기 넣지
// 않는다(평가 결정안 exploration-split-decision §6, 2026-11-16 사용자 결정). 대상 특정 경로나 평가 대상 지식은
// 넣지 않는다(CLAUDE.md 규칙 7). 바꾸면 testdata/goal-common-example.txt와 평가 러너의 B 본문을 같이 바꾼다.
func urlDiagnosisCommonGoal(origin string, paths, accountLabels []string) string {
	text := ""
	if len(paths) > 0 && origin != "" {
		// 점검 주소 하나만 주면 모델이 그 경로를 대상 전체로 여긴다(VAmPI 2차 회차: 진단 요청 67건이 모두 점검 주소
		// 경로 아래로만 갔다). 승인 경로 전체를 시작점으로 알린다. 보낼 수 있는 범위는 E②가 정하므로 통제는 그대로다.
		// 맨 경로 대신 같은 origin의 절대 URL로 점검 주소 뒤에 렌더한다. 점이 든 경로(/openapi.json 등)가 맨 글자로 있으면
		// 원본 parseTarget·러너 작업 본문 검사가 도메인으로 읽고, 목표 분해기가 범위 등록 때 호스트로 오독할 수 있다.
		// URL 끝에 쉼표 같은 문장 부호가 붙어 한 URL로 잡히지 않게 공백으로 구분한다.
		urls := make([]string, 0, len(paths))
		for _, expression := range paths {
			if strings.HasPrefix(expression, "=") {
				urls = append(urls, origin+strings.TrimPrefix(expression, "=")+" (exact)")
				continue
			}
			urls = append(urls, origin+expression)
		}
		text += " Approved paths on this origin, space-separated: " + strings.Join(urls, " ") + " . Each listed URL covers that path and everything under it, except a URL marked (exact), which covers only that path without a query string. The given target URL is one starting point among these, not the whole target: explore and test every approved path."
	}
	objective := " Objective: find vulnerabilities and back each one with actual request evidence. Test authorization first: IDOR (one account reading or using another account's objects by changing an object ID) and broken access control (reaching admin or other-role data and functions without that role). Then test other web vulnerability types the approved methods can reach, such as injection, sensitive data exposure and security misconfiguration. Endpoint discovery and mapping are a means, not the objective: the task is done only when the discovered attack surface has been tested for these vulnerability types, not when it has been mapped."
	// 시험했다고 볼 범위(판정 아님, IDOR 확정은 E③ 몫). 인증 없이 누구나 읽는 응답을 IDOR·접근통제 근거로 삼아
	// 목표를 일찍 끝내지 않게 한다(VAmPI 2차 회차: 인증 없이 읽히는 경로 관찰만으로 IDOR·접근통제 목표가 증명됨).
	objective += " Count authorization as tested only when the requests can show it: IDOR is tested when the same object on an endpoint that requires authentication is requested with each test account's session and the responses are compared; broken access control is tested when a function meant for another role is requested with a session that lacks that role and with no session. Reading data that any unauthenticated client can read does not by itself test IDOR or access control."
	if len(accountLabels) > 0 {
		objective += " The host logs in owned disposable test accounts (labels: " + strings.Join(accountLabels, ", ") + ") before diagnosis and gives Workers their sessions, so plan authorization intents that compare what each account can access."
	}
	report := " Report each potential vulnerability as an unconfirmed review candidate with its evidence; confirmation is decided separately from trusted evidence."
	return text + objective + report
}

// urlGoalAccountLabels는 실행에 결속된 시험 계정 라벨이다(이름만, 정렬). 결속 계정이 없으면 nil이다.
func urlGoalAccountLabels(auth *server.E2HTTPFileAuth) []string {
	if auth == nil {
		return nil
	}
	labels := make([]string, 0, len(auth.Accounts))
	for _, a := range auth.Accounts {
		labels = append(labels, a.Label)
	}
	slices.Sort(labels)
	return labels
}

// 준비 자원은 gateway가 만들어지기 전에도 별도로 검증·마감할 수 있다.
type urlPreparationRecovery struct {
	mu             sync.Mutex
	taskID         string
	closed         bool
	closeResources func(context.Context) error
}

func (r *urlPreparationRecovery) verify(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	if r.closeResources == nil || r.closeResources(ctx) != nil {
		return errPreparation
	}
	r.closed = true
	return nil
}

// 호출자는 runner.mu를 소유한다. manager/권위 DB를 한 번씩 닫고 실패는 확인될 때까지 보존한다.
func (r *urlGatewayRunner) closeStores() error {
	if r.launch.authorityDB != nil {
		if r.launch.authorityDB.Close() != nil {
			return errPreparation
		}
		r.launch.authorityDB = nil
	}
	if r.launch.manager != nil {
		if r.launch.manager.Close() != nil {
			return errPreparation
		}
		r.launch.manager = nil
	}
	return nil
}
func (r *urlGatewayRunner) forgetRecovery() {
	if r.backend == nil {
		return
	}
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	for id, runner := range r.backend.recovery {
		if runner == r {
			delete(r.backend.recovery, id)
		}
	}
}

// VerifyClosure는 Execute 반환 뒤 원래 Run 종료와 자원 마감을 모두 검증한다. 결과는 재작성하지 않는다.
func (b *urlPreparationRuntime) VerifyClosure(ctx context.Context, execution diagnosisprep.Execution) error {
	if ctx == nil || ctx.Err() != nil {
		return errPreparation
	}
	b.mu.RLock()
	runner := b.recovery[execution.Job.ID]
	preparation := b.preparationRecovery[execution.Job.ID]
	b.mu.RUnlock()
	if runner == nil {
		if preparation == nil || preparation.taskID != execution.Job.TaskID || preparation.verify(ctx) != nil {
			return errPreparation
		}
		b.mu.Lock()
		if b.preparationRecovery[execution.Job.ID] == preparation {
			delete(b.preparationRecovery, execution.Job.ID)
		}
		b.mu.Unlock()
		return nil
	}
	if runner.taskID != execution.Job.TaskID || runner.launch == nil || runner.launch.runtime == nil {
		return errPreparation
	}
	runner.mu.Lock()
	runner.closing = true // 아직 진입하지 않은 늦은 Run도 실행을 다시 시작하지 못한다.
	done, started := runner.runDone, runner.runStarted
	closeErr := runner.launch.runtime.Close(ctx)
	state, ok := runner.launch.runtime.(interface{ ResourcesClosed() bool })
	drained := ok && state.ResourcesClosed()
	// 원래 Run은 이 잠금 아래 done을 닫으므로 기다리기 전에 반드시 푼다.
	runner.mu.Unlock()
	if !drained {
		return errPreparation
	}
	if closeErr != nil {
		log.Print("진단 복구: 실행 결과 오류를 유지하며 닫힌 자원을 확인합니다")
	}
	if started {
		if done == nil {
			return errPreparation
		}
		select {
		case <-done:
		case <-ctx.Done():
			return errPreparation
		}
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.recoveryLive != nil && runner.recoveryLive.drain(ctx) != nil {
		return errPreparation
	}
	if runner.closeStores() != nil {
		return errPreparation
	}
	closure := diagnosis.Closure{}
	if runner.closed != nil {
		closure = *runner.closed
	}
	closure.DrainComplete = true
	// 늦은 gateway Close는 같은 마감 증거만 읽는다. 결과·저장 성공을 새로 만들지 않는다.
	runner.closed = &closure
	b.mu.Lock()
	delete(b.live, runner.taskID)
	if b.recovery[execution.Job.ID] == runner {
		delete(b.recovery, execution.Job.ID)
	}
	b.mu.Unlock()
	return nil
}
