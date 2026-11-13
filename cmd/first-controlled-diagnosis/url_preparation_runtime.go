package main

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"reflect"
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
		release, e := b.product.attach(*l.productTarget, execution.Job.ID, func() { stopTarget(server.ErrFirstDiagnosisTargetEnded) })
		if e != nil {
			return knownFailure("target_approval_changed", true)
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
	if l.dnsCheck(ctx, b.resolver) != nil {
		return knownFailure("target_dns_mismatch", true)
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
		modelBudget, err = server.NewFirstDiagnosisOpenRouterBudget(l.profile.Budget, l.profile.InputTokensPerCall, l.profile.OutputTokensPerCall, l.launch.OpenRouterManifestPath, observe)
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
	goal := urlDiagnosisGoal(execution.Job.Input.TargetURL, execution.Job.Input.WorkflowMode, execution.Job.Scope.Methods)
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
		finishPrepare()
		_ = pool.Close()
		return knownFailure("preparation_host_failed", true)
	}
	supervisor, err := server.NewFirstDiagnosisSupervisor(prepareCtx, manager)
	if err != nil {
		finishPrepare()
		manager.Close()
		return knownFailure("preparation_host_failed", true)
	}
	options := l.launch.modelOptions()
	options.Budget = modelBudget
	preparation, err := server.NewFirstDiagnosisPreparationServerWithModel(prepareCtx, manager, l.launch.SkillDir, l.launch.DataDir, l.launch.KeyDir, options)
	if err != nil || supervisor.Attach(preparation) != nil {
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
	b.mu.Lock()
	b.live[taskID] = newURLLiveHandler(d.runtime.Handler())
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

// urlDiagnosisGoal은 작업 목표 문장을 승인 기록의 메서드로 만든다(루프 복원 결정안 §3, C25). 모델이 정한 값이 아니다.
// 읽기 메서드만 승인됐으면 읽기 전용이라고 적고, 쓰기 메서드가 승인된 범위(건별 확인 흐름)에서는 그 메서드를 밝히고
// 요청마다 운영자 확인을 받아야 E②가 보낸다고 적는다. 실제 허용·차단은 E②가 정한다.
func urlDiagnosisGoal(target, workflowMode string, methods []string) string {
	var writes []string
	for _, m := range methods {
		if m != "GET" && m != "HEAD" {
			writes = append(writes, m)
		}
	}
	scope := "Inspect the approved read-only HTTPS target " + target + " through the enforced E2 boundary. Approved methods: " + strings.Join(methods, ", ") + "."
	if len(writes) > 0 {
		scope = "Inspect the approved HTTPS target " + target + " through the enforced E2 boundary. Approved methods: " + strings.Join(methods, ", ") + ". Write methods (" + strings.Join(writes, ", ") + ") are approved only per request: E2 holds each such request until the operator confirms it. Never invent that confirmation and never resend a request whose result is unknown."
	}
	if workflowMode == "" {
		if len(writes) == 0 {
			return scope + " Collect observations and manual-review candidates only. Do not request state changes or other origins."
		}
		return scope + " Collect observations and manual-review candidates only. Do not request other origins."
	}
	flow := " Workbench workflow mode: " + workflowMode + ". First prepare an initial discovery intent. The host will perform bounded discovery in the protected Worker. After discovery, use the server-provided exact selected endpoints for deep diagnostic intents."
	if len(writes) == 0 {
		flow += " State-changing requests require separately granted scope and per-request confirmation; never invent that permission."
	}
	return scope + flow + " Collect only supported observations and unconfirmed review candidates. Do not request other origins."
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
