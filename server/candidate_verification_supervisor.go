package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"sync"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/report"
	"github.com/Autumn-27/artex/transmit"
)

var ErrCandidateExecutionBinding = errors.New("candidate_execution_binding_invalid")

// CandidateExecutionSupervisor is a bounded, model-free E² admission guard.
// It binds each lease to a DB-approved plan and a distinct prepared task. The
// callback must recheck protected target state, pin, exclusions and expiry.
type CandidateExecutionSupervisor struct {
	mu             sync.Mutex
	ctx            context.Context
	pool           *sql.DB
	requestID      int64
	followupTaskID int64
	planDigest     string
	current        func(context.Context, CandidateActivePlan) error
	server         *Server
}

func NewCandidateExecutionSupervisor(ctx context.Context, pool *sql.DB, requestID, followupTaskID int64, planDigest string, current func(context.Context, CandidateActivePlan) error) (*CandidateExecutionSupervisor, error) {
	if ctx == nil || ctx.Err() != nil || pool == nil || requestID <= 0 || followupTaskID <= 0 || !candidateVerificationDigest.MatchString(planDigest) || current == nil {
		return nil, ErrCandidateExecutionBinding
	}
	return &CandidateExecutionSupervisor{ctx: ctx, pool: pool, requestID: requestID, followupTaskID: followupTaskID, planDigest: planDigest, current: current}, nil
}

func (s *CandidateExecutionSupervisor) Attach(server *Server) error {
	if s == nil || server == nil || server.m == nil || server.m.pg == nil || server.m.pg.DB != s.pool || !server.firstDiagnosis || server.firstDiagnosisPreparation {
		return ErrCandidateExecutionBinding
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil || s.server != nil {
		return ErrCandidateExecutionBinding
	}
	s.server = server
	return nil
}

func (s *CandidateExecutionSupervisor) check(ctx context.Context, scope transmit.Scope, intentID int64, paused, requireCurrent bool) error {
	if s == nil || ctx == nil || ctx.Err() != nil || requireCurrent && s.ctx.Err() != nil || scope.TaskRef != strconv.FormatInt(s.followupTaskID, 10) ||
		scope.CaseRef != s.planDigest || scope.RunRef == "" || intentID < 0 {
		return ErrCandidateExecutionBinding
	}
	s.mu.Lock()
	attached := s.server != nil
	s.mu.Unlock()
	if !attached {
		return ErrCandidateExecutionBinding
	}
	tx, err := s.pool.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return ErrCandidateExecutionBinding
	}
	defer tx.Rollback()
	var originalTask, candidateID, followupTask, explorationID int64
	var originalDigest, originalRun, runRef, storedPlanDigest, method, decision, state, latestState, taskStatus string
	var planJSON []byte
	var taskPaused, queued, fresh bool
	err = tx.QueryRowContext(ctx, `SELECT q.task_id,q.candidate_id,q.original_evidence_digest,q.original_run_ref,q.method,
 p.plan,p.plan_digest,d.decision,e.process_state,latest.process_state,x.followup_task_id,x.run_ref,
 t.exploration_id,t.paused,t.queued,t.status,t.deleted_at IS NULL AND t.completed_at IS NULL
 FROM diagnostic_candidate_verification_requests q
 JOIN diagnostic_candidate_verification_plans p ON p.request_id=q.id
 JOIN diagnostic_candidate_verification_decisions d ON d.request_id=q.id AND d.plan_digest=p.plan_digest
 JOIN diagnostic_candidate_verification_events e ON e.request_id=q.id AND e.step=2
 JOIN LATERAL (SELECT process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) latest ON true
 JOIN diagnostic_candidate_verification_executions x ON x.request_id=q.id AND x.approval_event_id=e.id AND x.plan_digest=p.plan_digest
 JOIN tasks t ON t.id=x.followup_task_id
 WHERE q.id=$1`, s.requestID).Scan(&originalTask, &candidateID, &originalDigest, &originalRun, &method,
		&planJSON, &storedPlanDigest, &decision, &state, &latestState, &followupTask, &runRef,
		&explorationID, &taskPaused, &queued, &taskStatus, &fresh)
	if err != nil || method != "active/v1" || decision != "approved" || state != "approved" || followupTask != s.followupTaskID ||
		storedPlanDigest != s.planDigest || runRef != scope.RunRef || !fresh || queued || taskPaused != paused || originalTask == followupTask || db.IsTerminal(taskStatus) ||
		requireCurrent && latestState != "running" {
		return ErrCandidateExecutionBinding
	}
	var plan candidateActivePlan
	if strictJSON(planJSON, &plan) != nil || plan.TaskID != originalTask || plan.CandidateID != candidateID || plan.EvidenceDigest != originalDigest ||
		plan.OriginalRunRef != originalRun || plan.Method != "GET" || plan.MaxRequests != 2 || plan.ModelCostMicros != 0 {
		return ErrCandidateExecutionBinding
	}
	digest, err := candidatePlanDigest(plan)
	if err != nil || digest != s.planDigest {
		return ErrCandidateExecutionBinding
	}
	if requireCurrent && s.current(ctx, plan) != nil {
		return ErrCandidateExecutionBinding
	}
	var originalExploration int64
	if tx.QueryRowContext(ctx, `SELECT exploration_id FROM tasks WHERE id=$1 AND deleted_at IS NULL`, originalTask).Scan(&originalExploration) != nil {
		return ErrCandidateExecutionBinding
	}
	candidate, found, err := db.ReadDiagnosticCandidateWithFence(ctx, tx, originalExploration, originalTask, candidateID)
	if err != nil || !found || candidate.EvidenceDigest != originalDigest || candidate.RunRef != originalRun || !candidatePlanMatchesEvidence(plan, candidate) {
		return ErrCandidateExecutionBinding
	}
	run, err := readDiagnosticRun(ctx, tx, originalTask)
	if err != nil || run == nil || run.State != report.DiagnosticRunCompleted || run.RunRef != originalRun || run.VerificationVersion != diagnosisVerificationV2 {
		return ErrCandidateExecutionBinding
	}
	insufficient := false
	for _, a := range run.CandidateAssessments {
		if a.CandidateID == candidateID && a.Status == candidateverify.Insufficient && validStoredAutomaticCandidateAssessment(candidate, a) {
			insufficient = true
		}
	}
	if !insufficient {
		return ErrCandidateExecutionBinding
	}
	if intentID > 0 {
		var payload json.RawMessage
		var state string
		if tx.QueryRowContext(ctx, `SELECT payload,state FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 AND kind='intent'`, intentID, explorationID).Scan(&payload, &state) != nil || state != "open" && state != "running" {
			return ErrCandidateExecutionBinding
		}
	}
	if tx.Commit() != nil {
		return ErrCandidateExecutionBinding
	}
	return nil
}

func (s *CandidateExecutionSupervisor) AuthorizeExecution(ctx context.Context, scope transmit.Scope, intentID int64) error {
	if intentID <= 0 {
		return ErrCandidateExecutionBinding
	}
	return s.check(ctx, scope, intentID, false, true)
}

func (s *CandidateExecutionSupervisor) CheckTaskQuiescent(ctx context.Context, taskID int64) error {
	if s == nil || taskID != s.followupTaskID {
		return ErrCandidateExecutionBinding
	}
	return s.check(ctx, transmit.Scope{TaskRef: strconv.FormatInt(taskID, 10), CaseRef: s.planDigest, RunRef: s.runRef(ctx)}, 0, true, false)
}

func (s *CandidateExecutionSupervisor) runRef(ctx context.Context) string {
	if s == nil || s.pool == nil || ctx == nil {
		return ""
	}
	var ref string
	_ = s.pool.QueryRowContext(ctx, `SELECT run_ref FROM diagnostic_candidate_verification_executions WHERE request_id=$1 AND followup_task_id=$2`, s.requestID, s.followupTaskID).Scan(&ref)
	return ref
}
