package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2config"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/server"
)

// candidateDatabase uses the installed, protected runtime role. No browser
// field selects a DSN, model, target, or database role.
func (b *urlPreparationRuntime) candidateDatabase(ctx context.Context) (*sql.DB, error) {
	if b == nil || b.product == nil || b.config.Product == nil || len(b.config.Product.Models) == 0 || ctx == nil || ctx.Err() != nil {
		return nil, errPreparation
	}
	if _, _, err := b.installation(); err != nil {
		return nil, errPreparation
	}
	_, launch, _, _, err := b.product.model(b.config.Product.Models[0].ID)
	if err != nil || launch.RuntimeDSNFile == "" {
		return nil, errPreparation
	}
	raw, pin, err := urlReadPin(launch.RuntimeDSNFile, 16<<10, false)
	clear(raw)
	if err != nil {
		return nil, errPreparation
	}
	pool, err := urlOpenDatabase(ctx, b.config, pin)
	if err != nil {
		return nil, errPreparation
	}
	if checkLaunchDatabaseRole(ctx, pool, launch) != nil {
		pool.Close()
		return nil, errPreparation
	}
	return pool, nil
}

// Only an already approved, immutable request can enter the background queue.
// The worker repeats every check before allocating a fresh task or authority.
func (b *urlPreparationRuntime) scheduleCandidateVerification(ctx context.Context, idText, digest string) (string, error) {
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != idText || b == nil || b.ctx == nil || b.ctx.Err() != nil {
		return "", errPreparation
	}
	pool, err := b.candidateDatabase(ctx)
	if err != nil {
		return "", errPreparation
	}
	defer pool.Close()
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return "", errPreparation
	}
	defer tx.Rollback()
	if _, err := server.ReadApprovedCandidatePlan(ctx, tx, id, digest); err != nil {
		return "", errPreparation
	}
	if tx.Commit() != nil {
		return "", errPreparation
	}
	b.mu.Lock()
	if b.ctx.Err() != nil {
		b.mu.Unlock()
		return "", errPreparation
	}
	if b.candidateWorkers == nil {
		b.candidateWorkers = map[int64]context.CancelFunc{}
	}
	if _, running := b.candidateWorkers[id]; running {
		b.mu.Unlock()
		return "scheduled", nil
	}
	bounded, cancel := context.WithTimeout(b.ctx, 90*time.Second)
	b.candidateWorkers[id] = cancel
	b.candidateWG.Add(1)
	b.mu.Unlock()
	go func() {
		defer b.candidateWG.Done()
		defer func() { b.mu.Lock(); delete(b.candidateWorkers, id); b.mu.Unlock() }()
		defer cancel()
		_ = b.executeCandidateVerification(bounded, id, digest)
	}()
	return "scheduled", nil
}

func (b *urlPreparationRuntime) waitCandidateWorkers(ctx context.Context) error {
	if b == nil || ctx == nil {
		return errPreparation
	}
	done := make(chan struct{})
	go func() { b.candidateWG.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return errors.Join(errPreparation, ctx.Err())
	}
}

// On restart, a bound execution is never replayed: its old lease and actual
// send outcome are unknown. An approved proposal without an execution binding
// may be scheduled once more because no task or E² authority was issued yet.
func (b *urlPreparationRuntime) recoverCandidateVerifications(ctx context.Context) error {
	if b == nil || b.product == nil {
		return nil
	}
	pool, err := b.candidateDatabase(ctx)
	if err != nil {
		return errPreparation
	}
	defer pool.Close()
	rows, err := pool.QueryContext(ctx, `SELECT q.id,q.task_id,p.plan_digest,e.process_state,x.followup_task_id,t.exploration_id
FROM diagnostic_candidate_verification_requests q
JOIN diagnostic_candidate_verification_plans p ON p.request_id=q.id
JOIN LATERAL (SELECT process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true
LEFT JOIN diagnostic_candidate_verification_executions x ON x.request_id=q.id AND x.plan_digest=p.plan_digest
LEFT JOIN tasks t ON t.id=x.followup_task_id AND t.deleted_at IS NULL
WHERE q.method='active/v1' AND e.process_state IN ('approved','running') ORDER BY q.id LIMIT 1025`)
	if err != nil {
		return errPreparation
	}
	type pending struct {
		id, originalTask  int64
		digest, state     string
		task, exploration sql.NullInt64
	}
	var items []pending
	for rows.Next() {
		var item pending
		if rows.Scan(&item.id, &item.originalTask, &item.digest, &item.state, &item.task, &item.exploration) != nil {
			rows.Close()
			return errPreparation
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(items) > 1024 {
		return errPreparation
	}
	for _, item := range items {
		if item.state == "running" && (!item.task.Valid || !item.exploration.Valid || item.exploration.Int64 <= 0) {
			return errPreparation
		}
		if item.state == "running" || item.task.Valid {
			if item.task.Valid && item.exploration.Valid {
				if _, err := server.ReleaseInterruptedFirstDiagnosisTasks(ctx, pool, item.originalTask, []server.FirstDiagnosisBoundTask{{TaskID: item.task.Int64, ExplorationID: item.exploration.Int64}}); err != nil {
					return errPreparation
				}
			}
			if candidateTerminalEvent(ctx, pool, item.id, "failed", "execution_interrupted") != nil {
				return errPreparation
			}
			continue
		}
		if _, err := b.scheduleCandidateVerification(ctx, strconv.FormatInt(item.id, 10), item.digest); err != nil {
			// The approved immutable plan is retained for an explicit retry.
			log.Printf("[candidate-verification] request %d recovery schedule unavailable", item.id)
		}
	}
	return nil
}

func candidateTerminalEvent(ctx context.Context, pool *sql.DB, requestID int64, state, reason string) error {
	if ctx == nil || pool == nil || requestID <= 0 || reason == "" || state != "failed" && state != "cancelled" && state != "expired" {
		return errPreparation
	}
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return errPreparation
	}
	defer tx.Rollback()
	var step int
	var previousState, runRef string
	err = tx.QueryRowContext(ctx, `SELECT e.step,e.process_state,COALESCE(x.run_ref,'') FROM diagnostic_candidate_verification_requests q
 JOIN LATERAL (SELECT step,process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true
 LEFT JOIN diagnostic_candidate_verification_executions x ON x.request_id=q.id WHERE q.id=$1 FOR UPDATE OF q`, requestID).Scan(&step, &previousState, &runRef)
	if err != nil {
		return errPreparation
	}
	if previousState != "approved" && previousState != "running" {
		return tx.Commit()
	}
	emptySources := []byte("[]")
	sum := sha256.Sum256(emptySources)
	_, err = tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_verification_events(request_id,step,process_state,reason,sources,sources_digest,rule_version,execution_ref)
VALUES($1,$2,$3,$4,$5,$6,'candidate-active/v1',$7)`, requestID, step+1, state, reason, emptySources, hex.EncodeToString(sum[:]), runRef)
	if err != nil || tx.Commit() != nil {
		return errPreparation
	}
	return nil
}

// Cancellation is recorded before the worker context is stopped. Every E²
// admission checks the latest event, so a late result cannot become confirmed.
func (b *urlPreparationRuntime) cancelCandidateVerification(ctx context.Context, idText, digest string) (string, error) {
	id, err := strconv.ParseInt(idText, 10, 64)
	decoded, digestErr := hex.DecodeString(digest)
	if b == nil || err != nil || id <= 0 || strconv.FormatInt(id, 10) != idText || digestErr != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != digest {
		return "", errPreparation
	}
	pool, err := b.candidateDatabase(ctx)
	if err != nil {
		return "", errPreparation
	}
	defer pool.Close()
	var state, storedDigest string
	err = pool.QueryRowContext(ctx, `SELECT e.process_state,p.plan_digest FROM diagnostic_candidate_verification_requests q
JOIN diagnostic_candidate_verification_plans p ON p.request_id=q.id
JOIN LATERAL (SELECT process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true
WHERE q.id=$1 AND q.method='active/v1'`, id).Scan(&state, &storedDigest)
	if err != nil || storedDigest != digest {
		return "", errPreparation
	}
	if state == "cancelled" || state == "expired" || state == "failed" || state == "completed" || state == "rejected" {
		return state, nil
	}
	if state != "approved" && state != "running" {
		return "", errPreparation
	}
	if candidateTerminalEvent(ctx, pool, id, "cancelled", "user_cancelled") != nil {
		return "", errPreparation
	}
	b.mu.RLock()
	cancel := b.candidateWorkers[id]
	b.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
	return "cancelled", nil
}

func (b *urlPreparationRuntime) executeCandidateVerification(ctx context.Context, requestID int64, requestedDigest string) (resultErr error) {
	pool, err := b.candidateDatabase(ctx)
	if err != nil {
		return errPreparation
	}
	defer pool.Close()
	defer func() {
		if resultErr != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(b.ctx), 10*time.Second)
			state, reason := "failed", "candidate_execution_unavailable"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				state, reason = "expired", "candidate_execution_timeout"
			} else if errors.Is(ctx.Err(), context.Canceled) {
				state, reason = "cancelled", "candidate_execution_cancelled"
			}
			if candidateTerminalEvent(cleanup, pool, requestID, state, reason) != nil {
				log.Printf("[candidate-verification] request %d failure record unavailable", requestID)
			}
			cancel()
		}
	}()
	var originalTaskID int64
	if pool.QueryRowContext(ctx, `SELECT task_id FROM diagnostic_candidate_verification_requests WHERE id=$1 AND method='active/v1'`, requestID).Scan(&originalTaskID) != nil {
		return errPreparation
	}
	ownerReq := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "candidate-execution-owner", Command: diagnosisprep.AuthorizeTask, Principal: "ARTEX", TaskID: strconv.FormatInt(originalTaskID, 10)}
	ownerResp, err := b.endpoint.Call(ctx, ownerReq)
	if err != nil || diagnosisprep.ValidateResponse(ownerReq, ownerResp) != nil || ownerResp.Authorized == nil || !*ownerResp.Authorized || ownerResp.Job == nil || ownerResp.Job.Scope == nil || ownerResp.Job.TaskID != ownerReq.TaskID {
		return errPreparation
	}
	l, err := b.load("ARTEX", ownerResp.Job.Input)
	if err != nil || l == nil {
		return errPreparation
	}
	defer l.clearMaterials()
	if l.productTarget == nil || l.productRegistry == nil || l.policy.Auth != nil || l.policy.HeaderMode != e2proxy.HeaderPreserve || len(l.productAccounts) != 0 || l.externalTestAccounts {
		return errPreparation
	}
	// The existing issuance path can add E③ rules for active declarations.
	// Until a separate candidate-only authority contract is reviewed, refuse
	// that case rather than relaxing the current E③ host or target pinning.
	if _, active := l.productRegistry.activeDeclaration(*l.productTarget); active {
		return errPreparation
	}
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return errPreparation
	}
	defer tx.Rollback()
	var approvalEventID int64
	if tx.QueryRowContext(ctx, `SELECT e.id FROM diagnostic_candidate_verification_requests q
 JOIN diagnostic_candidate_verification_events e ON e.request_id=q.id AND e.step=2 AND e.process_state='approved'
 WHERE q.id=$1 FOR UPDATE OF q`, requestID).Scan(&approvalEventID) != nil {
		return errPreparation
	}
	plan, err := server.ReadApprovedCandidatePlan(ctx, tx, requestID, requestedDigest)
	if err != nil || plan.TaskID != originalTaskID || ownerResp.Job.Scope.TargetRef != plan.TargetID || l.productTarget.Target.ID != plan.TargetID {
		return errPreparation
	}
	currentTarget, _, err := b.product.targets.view("ARTEX", plan.TargetID)
	if err != nil || currentTarget == nil || !server.CandidatePlanStillApproved(time.Now().UTC(), plan, ownerResp.Job.Input, *ownerResp.Job.Scope, *currentTarget) || l.current() != nil {
		return errPreparation
	}
	// Collapse the frozen policy to exactly one approved, static GET. No
	// credential, E③, model, query or general diagnosis rule is inherited.
	l.policy.Workflow = nil
	l.policy.Auth = nil
	l.policy.E3WriteConsent, l.policy.E3Resettable = false, false
	l.policy.HeaderMode = e2proxy.HeaderPreserve
	l.policy.ApprovalMode = e2proxy.ApprovalPreauthorizedScope
	l.policy.CaseRef = requestedDigest
	l.policy.Rules = []server.E2HTTPFileRule{{Ref: "candidate-exact", Origin: plan.Origin, ConnectIP: l.productTarget.SelectedIP,
		PathPrefix: plan.ExactPath, PathMatch: "exact", PathScopeRef: "candidate-exact", Methods: []string{"GET"}, TLSSPKISHA256: l.productTarget.Target.TLSSPKISHA256}}
	l.policy.MaxRequests = 2
	l.policy.Limits.MaxAttempts = 2
	l.policy.Limits.MaxCalls = 2
	l.policy.Limits.MaxConcurrent = 1
	l.policy.Runtime.MaxToolCalls = 2
	l.policy.Runtime.MaxIntentRuns = 1
	l.profile.Budget.Requests = 2
	l.profile.Budget.ModelRequests = 0
	l.profile.Budget.InputTokens = 0
	l.profile.Budget.OutputTokens = 0
	l.profile.Budget.CostUSDMicros = 0
	l.profile.Budget.DurationSeconds = 60
	if l.profile.ValidUntil.After(plan.ExpiresAt) {
		l.profile.ValidUntil = plan.ExpiresAt
	}
	jobID := fmt.Sprintf("candidate-%d-%s", requestID, requestedDigest[:16])
	runRef := "run-" + jobID
	hard, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	release, err := b.product.attach(*l.productTarget, jobID, cancel)
	if err != nil {
		return errPreparation
	}
	defer release()
	if l.hold() != nil {
		return errPreparation
	}
	defer l.releaseInputs()
	if l.prepareTargetConnection(hard, b.resolver, jobID) != nil || l.current() != nil {
		return errPreparation
	}
	dsnPin, err := l.databasePin()
	if err != nil {
		return errPreparation
	}
	runtimePool, err := urlOpenDatabase(hard, b.config, dsnPin)
	if err != nil || checkLaunchDatabaseRole(hard, runtimePool, l.launch) != nil {
		if runtimePool != nil {
			runtimePool.Close()
		}
		return errPreparation
	}
	prepared, err := server.PrepareFirstDiagnosisTask(hard, runtimePool, "선택 후보 추가 검증", "승인된 정확한 GET 관찰 두 건", l.launch.ModelProfileID, 60)
	if err != nil {
		runtimePool.Close()
		return errPreparation
	}
	_, err = tx.ExecContext(hard, `INSERT INTO diagnostic_candidate_verification_executions(request_id,followup_task_id,approval_event_id,plan_digest,run_ref) VALUES($1,$2,$3,$4,$5)`, requestID, prepared.TaskID, approvalEventID, requestedDigest, runRef)
	if err == nil {
		emptySources := []byte("[]")
		sum := sha256.Sum256(emptySources)
		_, err = tx.ExecContext(hard, `INSERT INTO diagnostic_candidate_verification_events(request_id,step,process_state,reason,sources,sources_digest,rule_version,execution_ref)
VALUES($1,3,'running','execution_started',$2,$3,'candidate-active/v1',$4)`, requestID, emptySources, hex.EncodeToString(sum[:]), runRef)
	}
	if err != nil || tx.Commit() != nil {
		_, _ = runtimePool.ExecContext(context.WithoutCancel(ctx), `UPDATE tasks SET status='failed' WHERE id=$1 AND paused AND first_run_at IS NULL`, prepared.TaskID)
		runtimePool.Close()
		return errPreparation
	}
	launch, err := b.issue(l, jobID, prepared)
	if err != nil {
		runtimePool.Close()
		return errPreparation
	}
	// Fail closed if a concurrent declaration change added E③ rules during
	// issuance. A candidate run must have exactly the one planned GET rule.
	rawPolicy, _, err := urlReadPin(launch.HTTP.PolicyFile, 256<<10, false)
	var issued server.E2HTTPFilePolicy
	if err != nil || e2config.StrictJSON(rawPolicy, &issued) != nil || len(issued.Rules) != 1 || issued.Rules[0].PathMatch != "exact" || issued.Rules[0].PathPrefix != plan.ExactPath || issued.CaseRef != requestedDigest || issued.Auth != nil {
		clear(rawPolicy)
		runtimePool.Close()
		return errPreparation
	}
	clear(rawPolicy)
	manager, err := server.NewFirstDiagnosisManager(hard, launch.DataDir, runtimePool)
	if err != nil {
		runtimePool.Close()
		return errPreparation
	}
	defer manager.Close()
	checkCurrent := func(call context.Context, current server.CandidateActivePlan) error {
		if l.current() != nil {
			return errPreparation
		}
		target, _, err := b.product.targets.view("ARTEX", current.TargetID)
		if err != nil || target == nil || !server.CandidatePlanStillApproved(time.Now(), current, ownerResp.Job.Input, *ownerResp.Job.Scope, *target) {
			return errPreparation
		}
		var latest string
		if pool.QueryRowContext(call, `SELECT process_state FROM diagnostic_candidate_verification_events WHERE request_id=$1 ORDER BY step DESC LIMIT 1`, requestID).Scan(&latest) != nil || latest != "running" {
			return errPreparation
		}
		return nil
	}
	guard, err := server.NewCandidateExecutionSupervisor(hard, runtimePool, requestID, prepared.TaskID, requestedDigest, checkCurrent)
	if err != nil {
		return errPreparation
	}
	options := server.FirstDiagnosisHTTPStartOptions{Host: server.FirstDiagnosisHTTPHostConfig{PolicyFile: launch.HTTP.PolicyFile, AuthorityFile: launch.HTTP.AuthorityFile, AuthorityRoot: launch.HTTP.AuthorityRoot},
		SkillDir: launch.SkillDir, DataDir: launch.DataDir, KeyDir: launch.KeyDir,
		LocalLLMManifestPath: launch.LocalLLMManifestPath, ModelExecutionMode: launch.ModelExecutionMode, OpenRouterManifestPath: launch.OpenRouterManifestPath,
		Supervisor: guard, Hooks: server.FirstDiagnosisHTTPHooks{HostSocket: launch.E2HostSocket, CheckAuthority: func() error { return checkCurrent(hard, plan) }},
		CleanupTimeout: time.Duration(launch.ShutdownTimeoutSeconds) * time.Second}
	observations, err := server.ExecuteSelectedCandidateGET(hard, manager, options, prepared.TaskID, runRef, plan.Origin+plan.ExactPath)
	if err != nil {
		return errPreparation
	}
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer stop()
	if _, err := server.CompleteCandidateActiveVerification(finish, pool, requestID, prepared.TaskID, requestedDigest, runRef, observations); err != nil {
		return errPreparation
	}
	return nil
}
