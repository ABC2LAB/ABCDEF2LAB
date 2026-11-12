package main

import (
	"context"
	"database/sql"
	"errors"
	"io"
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
	ctx      context.Context
	path     string
	config   urlPreparationInstallation
	endpoint diagnosisprep.Endpoint
	resolver *net.Resolver // nil이면 시스템 resolver. 시험은 전역 DNS를 변경하지 않는다.
	mu       sync.RWMutex
	live     map[string]*urlLiveHandler
	reports  map[string]*savedReportService
	product  *productRuntime
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
		return b.product.load(principal, input, reportOnly, id)
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
	}
	var diagnostics *launchDiagnostics
	check := func() error {
		if diagnostics != nil && diagnostics.ready() != nil {
			return errPreparation
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return l.current()
	}
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
		if diagnostics.logger.Err() != nil || u.State == diagnosisprep.Completed && (code != 0 || diagnostics.logger.Incomplete()) {
			u.State = diagnosisprep.Unknown
			u.ReasonCode = "diagnostic_logging_incomplete"
		}
	}
	p.publish = func(u diagnosisprep.Update) error {
		switch u.State {
		case diagnosisprep.Completed, diagnosisprep.Failed, diagnosisprep.Cancelled, diagnosisprep.Unknown:
			u = settleProductWorkflow(parent, flow, u, finishDiagnostics)
		}
		return publish(u)
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
		watchURLPreparation(hard, watchDone, diagnostics.logger.Failed(), ticker.C, l.current, cancel, stopTarget)
	}()
	var lastModelRequest int64
	observe := func(usage diagnosisprep.ModelUsage) error {
		if usage.Requests > lastModelRequest {
			// 대상 호출 admission은 예약 전에 검사한다. 마감 usage는 종료 뒤에도 저장한다.
			if l.productTarget == nil && check() != nil {
				return errPreparation
			}
			lastModelRequest = usage.Requests
			if usage.Requests == 1 {
				if diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "cli", Event: "model_request_started", Stage: "model_call", Status: "started", Count: 1, DurationMS: time.Since(startedAt).Milliseconds()}) != nil {
					return errPreparation
				}
			}
		}
		return p.set(func(u *diagnosisprep.Update) { copy := usage; u.ModelUsage = &copy })
	}
	var modelBudget *server.FirstDiagnosisModelBudget
	if l.launch.ModelExecutionMode == "openrouter" {
		modelBudget, err = server.NewFirstDiagnosisOpenRouterBudget(l.profile.Budget, l.profile.InputTokensPerCall, l.profile.OutputTokensPerCall, l.launch.OpenRouterManifestPath, observe)
	} else {
		modelBudget, err = server.NewFirstDiagnosisModelBudget(l.profile.Budget, l.profile.InputTokensPerCall, l.profile.OutputTokensPerCall, observe)
	}
	if err != nil {
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
		if pool.QueryRowContext(ctx, `SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND kind='intent' AND state='open'`, actual.ExplorationID).Scan(&intents) != nil || intents < 1 || intents > 128 {
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
	if closeErr == nil {
		manager.Close()
	}
	if closeErr != nil || saveErr != nil {
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
	runner := &urlGatewayRunner{backend: b, launch: d, config: launch, loaded: l, taskID: taskID}
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
	if diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "cli", Event: "stage_finished", Stage: "admission", Status: "succeeded", DurationMS: time.Since(startedAt).Milliseconds()}) != nil {
		cancel()
		return unknown("diagnostic_logging_incomplete", false)
	}
	diagnostics.next("execution", "execution_failed")
	response, err := gateway.Call(ctx, diagnosis.Request{Version: diagnosis.Version, RequestID: "url-start", Command: diagnosis.Start, TaskID: taskID, PreparationRef: execution.Job.ID, IdempotencyKey: execution.Job.ID})
	if err != nil || response.Snapshot == nil {
		return unknown("gateway_start_unconfirmed", false)
	}
	if p.set(func(u *diagnosisprep.Update) { u.State = diagnosisprep.Running; u.Started = response.Snapshot.Started }) != nil {
		cancel()
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
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
			return p.set(func(u *diagnosisprep.Update) {
				u.Phase = diagnosisprep.Report
				u.State = diagnosisprep.Cancelled
				u.ModelUsage = &usage
				u.Started = true
				u.DrainComplete = state.DrainComplete
				u.ResultReady = state.ResultReady
				if !state.DrainComplete || !state.ResultReady {
					u.State = diagnosisprep.Unknown
					u.ReasonCode = "execution_closure_unconfirmed"
				}
				if !usage.UsageKnown {
					u.State = diagnosisprep.Unknown
					u.ReasonCode = "model_usage_unknown"
				}
			})
		case <-ticker.C:
			status, e := gateway.Call(ctx, diagnosis.Request{Version: diagnosis.Version, RequestID: "url-status", Command: diagnosis.Status, TaskID: taskID, PreparationRef: execution.Job.ID})
			if e != nil || status.Snapshot == nil {
				// A target stop may already be draining model usage. A failed
				// status read with that cancelled context must not hard-cancel it.
				cancelURLStatusFailure(ctx, cancel)
				continue
			}
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
	backend *urlPreparationRuntime
	launch  *launchedDiagnosis
	config  launchConfig
	loaded  *urlLoadedProfile
	taskID  string
	mu      sync.Mutex
	closed  *diagnosis.Closure
	runErr  error
}

func (r *urlGatewayRunner) Run(ctx context.Context) error {
	err := r.launch.supervisor.Run(ctx, r.taskID, 0, "")
	r.mu.Lock()
	r.runErr = err
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
func (r *urlGatewayRunner) Close(ctx context.Context) (diagnosis.Closure, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
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
	stored, saveErr := r.launch.supervisor.PersistRunOutcomeStored(ctx, closureErr)
	result := diagnosis.Closure{DrainComplete: drained, ResultReady: stored && saveErr == nil}
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
		r.launch.manager.Close()
		r.launch.manager = nil
	}
	r.closed = &result
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
		loaded, loadErr := b.loadForReport("ARTEX", auth.Job.Input, true, targetRef)
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

// Execute keeps audit watching active during a normal target drain.
func watchURLPreparation(hard context.Context, done, auditFailed <-chan struct{}, ticks <-chan time.Time, current func() error, cancel context.CancelFunc, stopTarget context.CancelCauseFunc) {
	for {
		select {
		case <-done:
			return
		case <-hard.Done():
			return
		case <-auditFailed:
			cancel()
			return
		case <-ticks:
			if err := current(); err != nil {
				if errors.Is(err, server.ErrFirstDiagnosisTargetEnded) {
					stopTarget(err)
					continue
				}
				cancel()
				return
			}
		}
	}
}

// A failed read after a soft target stop must preserve an active usage drain.
func cancelURLStatusFailure(ctx context.Context, cancel context.CancelFunc) {
	if ctx.Err() == nil {
		cancel()
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
