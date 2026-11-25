package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2config"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/server"
	"github.com/Autumn-27/artex/transmit"
)

const candidateLLMEmptySourcesDigest = "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"

func candidateLLMRuntimeSeconds(planSeconds int, installedSeconds, workerTimeoutMS int64) int {
	if planSeconds < 1 || installedSeconds < 1 {
		return 0
	}
	seconds := min(planSeconds, int(installedSeconds))
	if workerTimeoutMS > 0 {
		seconds = min(seconds, int(workerTimeoutMS/1000))
	}
	return seconds
}

func candidateLLMReadRule(ref, origin, connectIP, spki string, action server.CandidateLLMPlanAction) server.E2HTTPFileRule {
	return server.E2HTTPFileRule{Ref: ref, Origin: origin, ConnectIP: connectIP,
		PathPrefix: action.ExactPath, PathMatch: "exact", PathScopeRef: ref, Methods: []string{action.Method},
		ApprovalDisplay: server.RequestApprovalDisplay{QueryKeys: []string{}, JSONFields: []string{}},
		TLSSPKISHA256:   spki}
}

func (b *urlPreparationRuntime) scheduleCandidateLLMExecution(ctx context.Context, idText, digest string) (string, error) {
	id, err := strconv.ParseInt(idText, 10, 64)
	if b == nil || b.ctx == nil || b.ctx.Err() != nil || err != nil || id <= 0 || strconv.FormatInt(id, 10) != idText {
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
	if _, err := server.ReadApprovedCandidateLLMPlan(ctx, tx, id, digest); err != nil || tx.Commit() != nil {
		return "", errPreparation
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ctx.Err() != nil {
		return "", errPreparation
	}
	if b.candidateWorkers == nil {
		b.candidateWorkers = map[int64]context.CancelFunc{}
	}
	if _, running := b.candidateWorkers[id]; running {
		return "scheduled", nil
	}
	bounded, cancel := context.WithTimeout(b.ctx, 270*time.Second)
	b.candidateWorkers[id] = cancel
	b.candidateWG.Add(1)
	go func() {
		defer b.candidateWG.Done()
		defer func() { b.mu.Lock(); delete(b.candidateWorkers, id); b.mu.Unlock() }()
		defer cancel()
		if err := b.executeCandidateLLMVerification(bounded, id, digest); err != nil {
			log.Printf("[candidate-llm-execution] request %d ended without sealed evidence", id)
			return
		}
		if err := b.executeCandidateLLMAssessment(bounded, id); err != nil {
			log.Printf("[candidate-llm-assessment] request %d ended without a verified verdict", id)
		}
	}()
	return "scheduled", nil
}

func (b *urlPreparationRuntime) executeCandidateLLMVerification(ctx context.Context, requestID int64, digest string) (resultErr error) {
	phase := "context"
	pool, err := b.candidateDatabase(ctx)
	if err != nil {
		return errPreparation
	}
	defer pool.Close()
	defer func() {
		if resultErr == nil {
			return
		}
		log.Printf("[candidate-llm-execution] request %d failed at %s", requestID, phase)
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(b.ctx), 10*time.Second)
		defer cancel()
		state, reason := "failed", "candidate_execution_unavailable"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			state, reason = "expired", "candidate_execution_timeout"
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			state, reason = "cancelled", "candidate_execution_cancelled"
		}
		if candidateLLMTerminalEvent(cleanup, pool, requestID, state, reason) != nil {
			log.Printf("[candidate-llm-execution] request %d failure record unavailable", requestID)
		}
	}()
	var originalTaskID int64
	var evidenceDigest string
	if pool.QueryRowContext(ctx, `SELECT task_id,original_evidence_digest FROM diagnostic_candidate_verification_requests
WHERE id=$1 AND method='llm/v1'`, requestID).Scan(&originalTaskID, &evidenceDigest) != nil {
		return errPreparation
	}
	ownerReq := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "candidate-llm-execution-owner", Command: diagnosisprep.AuthorizeTask,
		Principal: "ARTEX", TaskID: strconv.FormatInt(originalTaskID, 10)}
	ownerResp, err := b.endpoint.Call(ctx, ownerReq)
	if err != nil || diagnosisprep.ValidateResponse(ownerReq, ownerResp) != nil || ownerResp.Authorized == nil || !*ownerResp.Authorized ||
		ownerResp.Job == nil || ownerResp.Job.Scope == nil || ownerResp.Job.TaskID != ownerReq.TaskID {
		return errPreparation
	}
	l, err := b.load("ARTEX", ownerResp.Job.Input)
	if err != nil || l == nil {
		return errPreparation
	}
	defer l.clearMaterials()
	if l.productTarget == nil || l.productRegistry == nil || l.policy.Auth != nil || l.policy.HeaderMode != e2proxy.HeaderPreserve ||
		len(l.productAccounts) != 0 || l.externalTestAccounts {
		return errPreparation
	}
	ledger, ok := b.endpoint.(interface {
		ReserveCandidateBudget(context.Context, string, int64, string) error
		FinishCandidateBudget(int64)
	})
	if !ok || ledger.ReserveCandidateBudget(ctx, ownerReq.TaskID, requestID, evidenceDigest) != nil {
		return errPreparation
	}
	defer ledger.FinishCandidateBudget(requestID)
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return errPreparation
	}
	defer tx.Rollback()
	var approvalEventID int64
	if tx.QueryRowContext(ctx, `SELECT e.id FROM diagnostic_candidate_verification_requests q
JOIN diagnostic_candidate_verification_events e ON e.request_id=q.id AND e.step=5 AND e.process_state='approved'
WHERE q.id=$1 FOR UPDATE OF q`, requestID).Scan(&approvalEventID) != nil {
		return errPreparation
	}
	plan, err := server.ReadApprovedCandidateLLMPlan(ctx, tx, requestID, digest)
	if err != nil || plan.TaskID != originalTaskID || ownerResp.Job.Scope.TargetRef != plan.TargetID || l.productTarget.Target.ID != plan.TargetID {
		return errPreparation
	}
	currentTarget, _, err := b.product.targets.view("ARTEX", plan.TargetID)
	if err != nil || currentTarget == nil || !server.CandidateLLMPlanStillApproved(time.Now().UTC(), plan, ownerResp.Job.Input, *ownerResp.Job.Scope, *currentTarget) || l.current() != nil {
		return errPreparation
	}
	l.policy.Workflow = nil
	l.policy.Auth = nil
	l.policy.E3WriteConsent, l.policy.E3Resettable = false, false
	l.policy.HeaderMode = e2proxy.HeaderPreserve
	l.policy.ApprovalMode = e2proxy.ApprovalPreauthorizedScope
	l.policy.CaseRef = digest
	l.policy.Rules = nil
	for i, action := range plan.Actions {
		ref := fmt.Sprintf("candidate-llm-%d", i+1)
		l.policy.Rules = append(l.policy.Rules, candidateLLMReadRule(ref, plan.Origin, l.productTarget.SelectedIP,
			l.productTarget.Target.TLSSPKISHA256, action))
	}
	count := len(plan.Actions)
	if l.profile.Budget.Requests < int64(count) || l.profile.Budget.DurationSeconds < 1 ||
		l.policy.MaxRequests < uint64(count) || l.policy.Limits.MaxAttempts < uint64(count) ||
		l.policy.Limits.MaxCalls < count || l.policy.Runtime.MaxToolCalls < count {
		return errPreparation
	}
	runtimeSeconds := candidateLLMRuntimeSeconds(plan.DurationSeconds, l.profile.Budget.DurationSeconds, l.policy.Runtime.WorkerTimeoutMS)
	if runtimeSeconds < 1 {
		return errPreparation
	}
	l.policy.MaxRequests = uint64(count)
	l.policy.Limits.MaxAttempts = uint64(count)
	l.policy.Limits.MaxCalls = count
	l.policy.Limits.MaxConcurrent = 1
	l.policy.Runtime.MaxToolCalls = count
	l.policy.Runtime.MaxIntentRuns = 1
	l.profile.Budget.Requests = int64(count)
	l.profile.Budget.ModelRequests, l.profile.Budget.InputTokens, l.profile.Budget.OutputTokens, l.profile.Budget.CostUSDMicros = 0, 0, 0, 0
	l.profile.Budget.DurationSeconds = int64(runtimeSeconds)
	if l.profile.ValidUntil.After(plan.ExpiresAt) {
		l.profile.ValidUntil = plan.ExpiresAt
	}
	jobID := fmt.Sprintf("candidate-llm-%d-%s", requestID, digest[:16])
	runRef := "run-" + jobID
	hard, cancel := context.WithTimeout(ctx, time.Duration(runtimeSeconds)*time.Second)
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
	prepared, err := server.PrepareFirstDiagnosisTask(hard, runtimePool, "선택 비 IDOR 후보 추가 검증", "승인된 읽기 행동만 실행", l.launch.ModelProfileID, runtimeSeconds)
	if err != nil {
		runtimePool.Close()
		return errPreparation
	}
	issuedScope := transmit.Scope{TaskRef: strconv.FormatInt(prepared.TaskID, 10), RunRef: runRef,
		CaseRef: digest, ResetRef: l.policy.ResetRef, PolicyRef: "policy-" + jobID}
	scopeRaw, err := json.Marshal(issuedScope)
	if err != nil {
		runtimePool.Close()
		return errPreparation
	}
	scopeDigest := db.DiagnosticEvidenceDigest(string(scopeRaw))
	_, err = tx.ExecContext(hard, `INSERT INTO diagnostic_candidate_verification_executions
(request_id,followup_task_id,approval_event_id,plan_digest,run_ref,scope_digest) VALUES($1,$2,$3,$4,$5,$6)`, requestID, prepared.TaskID, approvalEventID, digest, runRef, scopeDigest)
	if err == nil {
		_, err = tx.ExecContext(hard, `INSERT INTO diagnostic_candidate_verification_events
(request_id,step,process_state,reason,sources,sources_digest,rule_version,execution_ref)
VALUES($1,6,'running','execution_started','[]'::json,$2,'selected/llm-v1',$3)`, requestID, candidateLLMEmptySourcesDigest, runRef)
	}
	if err != nil || tx.Commit() != nil {
		_, _ = runtimePool.ExecContext(context.WithoutCancel(ctx), `UPDATE tasks SET status='failed' WHERE id=$1 AND paused AND first_run_at IS NULL`, prepared.TaskID)
		runtimePool.Close()
		return errPreparation
	}
	phase = "authority_issue"
	launch, err := b.issueSelectedCandidate(l, jobID, prepared)
	if err != nil {
		runtimePool.Close()
		return errPreparation
	}
	phase = "authority_validation"
	rawPolicy, _, err := urlReadPin(launch.HTTP.PolicyFile, 256<<10, false)
	var issued server.E2HTTPFilePolicy
	if err != nil || e2config.StrictJSON(rawPolicy, &issued) != nil || len(issued.Rules) != len(plan.Actions) ||
		issued.CaseRef != digest || issued.RunRef != issuedScope.RunRef || issued.ResetRef != issuedScope.ResetRef ||
		issued.PolicyRef != issuedScope.PolicyRef || issued.Auth != nil || issued.E3WriteConsent || issued.E3Resettable ||
		issued.Limits.E3ConfirmationRequests == nil || *issued.Limits.E3ConfirmationRequests != 0 {
		clear(rawPolicy)
		runtimePool.Close()
		return errPreparation
	}
	for i, action := range plan.Actions {
		if issued.Rules[i].Purpose != "" || issued.Rules[i].Credentials != nil || issued.Rules[i].PathMatch != "exact" || issued.Rules[i].PathPrefix != action.ExactPath ||
			len(issued.Rules[i].Methods) != 1 || issued.Rules[i].Methods[0] != action.Method {
			clear(rawPolicy)
			runtimePool.Close()
			return errPreparation
		}
	}
	clear(rawPolicy)
	phase = "manager"
	manager, err := server.NewFirstDiagnosisManager(hard, launch.DataDir, runtimePool)
	if err != nil {
		runtimePool.Close()
		return errPreparation
	}
	defer manager.Close()
	checkCurrent := func(call context.Context, current server.CandidateLLMPlan) error {
		if l.current() != nil || call == nil || call.Err() != nil {
			return errPreparation
		}
		fresh, err := b.endpoint.Call(call, ownerReq)
		if err != nil || diagnosisprep.ValidateResponse(ownerReq, fresh) != nil || fresh.Authorized == nil || !*fresh.Authorized ||
			fresh.Job == nil || fresh.Job.Scope == nil || fresh.Job.TaskID != ownerReq.TaskID {
			return errPreparation
		}
		target, _, err := b.product.targets.view("ARTEX", current.TargetID)
		if err != nil || target == nil || !server.CandidateLLMPlanStillApproved(time.Now(), current, fresh.Job.Input, *fresh.Job.Scope, *target) {
			return errPreparation
		}
		var latest string
		if pool.QueryRowContext(call, `SELECT process_state FROM diagnostic_candidate_verification_events WHERE request_id=$1 ORDER BY step DESC LIMIT 1`, requestID).Scan(&latest) != nil || latest != "running" {
			return errPreparation
		}
		return nil
	}
	phase = "supervisor"
	guard, err := server.NewCandidateLLMExecutionSupervisor(hard, runtimePool, requestID, prepared.TaskID, digest, checkCurrent)
	if err != nil {
		return errPreparation
	}
	options := server.FirstDiagnosisHTTPStartOptions{Host: server.FirstDiagnosisHTTPHostConfig{PolicyFile: launch.HTTP.PolicyFile,
		AuthorityFile: launch.HTTP.AuthorityFile, AuthorityRoot: launch.HTTP.AuthorityRoot}, SkillDir: launch.SkillDir, DataDir: launch.DataDir, KeyDir: launch.KeyDir,
		LocalLLMManifestPath: launch.LocalLLMManifestPath, ModelExecutionMode: launch.ModelExecutionMode, OpenRouterManifestPath: launch.OpenRouterManifestPath,
		Supervisor: guard, Hooks: server.FirstDiagnosisHTTPHooks{HostSocket: launch.E2HostSocket, CheckAuthority: func() error { return checkCurrent(hard, plan) }},
		CleanupTimeout: time.Duration(launch.ShutdownTimeoutSeconds) * time.Second}
	phase = "e2_execution"
	observations, err := server.ExecuteSelectedCandidateActions(hard, manager, options, prepared.TaskID, runRef, plan)
	if err != nil {
		return errPreparation
	}
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer stop()
	phase = "evidence_commit"
	if err := server.CompleteCandidateLLMEvidence(finish, pool, requestID, prepared.TaskID, digest, runRef, observations); err != nil {
		return errPreparation
	}
	return nil
}

func candidateLLMTerminalEvent(ctx context.Context, pool *sql.DB, requestID int64, state, reason string) error {
	if ctx == nil || pool == nil || requestID <= 0 || state != "failed" && state != "cancelled" && state != "expired" {
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
LEFT JOIN diagnostic_candidate_verification_executions x ON x.request_id=q.id
WHERE q.id=$1 AND q.method='llm/v1' FOR UPDATE OF q`, requestID).Scan(&step, &previousState, &runRef)
	if err != nil {
		return errPreparation
	}
	if previousState != "approved" && previousState != "running" && previousState != "evidence_ready" && previousState != "assessing" {
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_verification_events
(request_id,step,process_state,reason,sources,sources_digest,rule_version,execution_ref)
VALUES($1,$2,$3,$4,'[]'::json,$5,'selected/llm-v1',$6)`, requestID, step+1, state, reason, candidateLLMEmptySourcesDigest, runRef); err != nil {
		return errPreparation
	}
	return tx.Commit()
}

// Persist revocation before interrupting the worker; E² admissions inspect
// the latest event and reject every later target send.
func (b *urlPreparationRuntime) cancelCandidateLLMExecution(ctx context.Context, idText, digest string) (string, error) {
	id, err := strconv.ParseInt(idText, 10, 64)
	if b == nil || err != nil || id <= 0 || strconv.FormatInt(id, 10) != idText || len(digest) != 64 {
		return "", errPreparation
	}
	pool, err := b.candidateDatabase(ctx)
	if err != nil {
		return "", errPreparation
	}
	defer pool.Close()
	var storedDigest, state string
	if pool.QueryRowContext(ctx, `SELECT p.plan_digest,e.process_state FROM diagnostic_candidate_verification_requests q
JOIN diagnostic_candidate_verification_plans p ON p.request_id=q.id
JOIN LATERAL (SELECT process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true
WHERE q.id=$1 AND q.method='llm/v1'`, id).Scan(&storedDigest, &state) != nil || storedDigest != digest {
		return "", errPreparation
	}
	if state == "cancelled" || state == "expired" || state == "failed" || state == "completed" || state == "rejected" {
		return state, nil
	}
	if state != "approved" && state != "running" && state != "evidence_ready" && state != "assessing" {
		return "", errPreparation
	}
	if candidateLLMTerminalEvent(ctx, pool, id, "cancelled", "candidate_execution_cancelled") != nil {
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

// No bound E² execution is replayed after a restart. Its send outcome may be
// unknown; release the interrupted task and retain its immutable audit trail.
func (b *urlPreparationRuntime) recoverCandidateLLMExecutions(ctx context.Context, pool *sql.DB) error {
	rows, err := pool.QueryContext(ctx, `SELECT q.id,q.task_id,p.plan_digest,p.expires_at,e.process_state,x.followup_task_id,t.exploration_id
FROM diagnostic_candidate_verification_requests q
JOIN diagnostic_candidate_verification_plans p ON p.request_id=q.id
JOIN LATERAL (SELECT process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true
LEFT JOIN diagnostic_candidate_verification_executions x ON x.request_id=q.id AND x.plan_digest=p.plan_digest
LEFT JOIN tasks t ON t.id=x.followup_task_id AND t.deleted_at IS NULL
	WHERE q.method='llm/v1' AND e.process_state IN ('approved','running','evidence_ready','assessing') ORDER BY q.id LIMIT 1025`)
	if err != nil {
		return errPreparation
	}
	type pending struct {
		id, originalTask  int64
		digest, state     string
		expires           time.Time
		task, exploration sql.NullInt64
	}
	var items []pending
	for rows.Next() {
		var item pending
		if rows.Scan(&item.id, &item.originalTask, &item.digest, &item.expires, &item.state, &item.task, &item.exploration) != nil {
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
		if item.state == "assessing" {
			if candidateLLMTerminalEvent(ctx, pool, item.id, "failed", "final_model_interrupted") != nil {
				return errPreparation
			}
			continue
		}
		if item.state == "evidence_ready" {
			if !item.task.Valid || !item.exploration.Valid || item.exploration.Int64 <= 0 {
				return errPreparation
			}
			if _, err := b.scheduleCandidateLLMAssessment(ctx, item.id); err != nil {
				log.Printf("[candidate-llm-assessment] request %d recovery schedule unavailable", item.id)
			}
			continue
		}
		if item.state == "running" && (!item.task.Valid || !item.exploration.Valid || item.exploration.Int64 <= 0) {
			return errPreparation
		}
		if item.task.Valid {
			if !item.exploration.Valid {
				return errPreparation
			}
			if _, err := server.ReleaseInterruptedFirstDiagnosisTasks(ctx, pool, item.originalTask,
				[]server.FirstDiagnosisBoundTask{{TaskID: item.task.Int64, ExplorationID: item.exploration.Int64}}); err != nil {
				return errPreparation
			}
			if candidateLLMTerminalEvent(ctx, pool, item.id, "failed", "candidate_execution_unavailable") != nil {
				return errPreparation
			}
			continue
		}
		if !time.Now().Before(item.expires) {
			if candidateLLMTerminalEvent(ctx, pool, item.id, "expired", "candidate_execution_timeout") != nil {
				return errPreparation
			}
			continue
		}
		if _, err := b.scheduleCandidateLLMExecution(ctx, strconv.FormatInt(item.id, 10), item.digest); err != nil {
			log.Printf("[candidate-llm-execution] request %d recovery schedule unavailable", item.id)
		}
	}
	return nil
}
