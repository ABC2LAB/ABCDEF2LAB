package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/llmpolicy"
	"github.com/Autumn-27/artex/report"
)

// readDiagnosticRun은 저장된 실행 종료 기록의 상태·실행 결과를 보고서와 같은 스냅샷에서 읽는다(실행 결과 상태 값
// 계약 §5-5). 기록이 없으면 nil이다. 기록 형식이 맞지 않으면 상태 없는 값을 돌려 보고서에 "확인할 수 없음"으로 쓴다
// (완료·발견 없음으로 바꾸지 않는다).
func readDiagnosticRun(ctx context.Context, tx *sql.Tx, task int64) (*report.DiagnosticRun, error) {
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT n.payload FROM tasks t JOIN exploration_nodes n ON n.exploration_id=t.exploration_id WHERE t.id=$1 AND t.deleted_at IS NULL AND n.kind='fact' AND n.state='confirmed' AND n.origin=$2 AND n.payload->>'task_id'=$3 ORDER BY n.id DESC LIMIT 1`, task, diagnosisRunOrigin, strconv.FormatInt(task, 10)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var record diagnosisRunRecord
	if json.Unmarshal(raw, &record) != nil || record.Version != 1 || record.TaskID != task || record.SavedAt.IsZero() || !validDiagnosisRun(record.Run) || !validDiagnosisRunResult(record.Run, record.Result) {
		return &report.DiagnosticRun{}, nil
	}
	state := record.Run.Outcome
	if record.Run.ClosureFailed {
		state = report.DiagnosticRunClosureFailed
	}
	run := &report.DiagnosticRun{State: state, RunRef: record.Run.RunRef, Result: record.Result}
	if record.Verification != nil && record.Verification.Version == "passive/v1" {
		run.VerificationVersion = record.Verification.Version
		run.VerificationSourceUnavailable = record.Verification.SourceUnavailable
		for _, row := range record.Verification.Candidates {
			run.CandidateAssessments = append(run.CandidateAssessments, report.CandidateAssessment{CandidateID: row.CandidateID, EvidenceDigest: row.EvidenceDigest, Status: row.Result.Status, Reason: row.Result.Reason, AttemptRefs: append([]string(nil), row.Result.Attempts...)})
		}
	}
	return run, nil
}

// 저장된 준비·실행 정산을 출처별로 표시한다. 두 단계를 합산하거나 미확정 비용을 0으로 단정하지 않는다.
func readDiagnosticModelAccounting(ctx context.Context, tx *sql.Tx, task int64) ([]report.DiagnosticModelAccounting, error) {
	preparation, err := readDiagnosisPreparationUsage(ctx, tx, task)
	if err != nil {
		return nil, err
	}
	project := func(phase, phaseText string, usage llmpolicy.OpenRouterUsage) (report.DiagnosticModelAccounting, error) {
		if usage.Requests <= 0 || usage.Errors < 0 || usage.Errors > usage.Requests || usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.CostMicros < 0 {
			return report.DiagnosticModelAccounting{}, ErrFirstDiagnosisOutcome
		}
		costText := "0 또는 미확인"
		if usage.CostMicros > 0 {
			costText = fmt.Sprintf("$%d.%06d", usage.CostMicros/1_000_000, usage.CostMicros%1_000_000)
		}
		return report.DiagnosticModelAccounting{Phase: phase, PhaseText: phaseText, Requests: usage.Requests, Errors: usage.Errors,
			InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, CostMicros: usage.CostMicros, CostText: costText}, nil
	}
	out := make([]report.DiagnosticModelAccounting, 0, len(preparation)+1)
	for _, entry := range preparation {
		result := "실패"
		if entry.Succeeded {
			result = "성공"
		}
		phaseText := fmt.Sprintf("준비 (%s · 준비 작업 %s)", entry.SavedAt.UTC().Format(time.RFC3339Nano), result)
		row, err := project("preparation", phaseText, entry.Usage)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT n.payload FROM tasks t JOIN exploration_nodes n ON n.exploration_id=t.exploration_id WHERE t.id=$1 AND t.deleted_at IS NULL AND n.kind='fact' AND n.state='confirmed' AND n.origin=$2 AND n.payload->>'task_id'=$3 ORDER BY n.id DESC LIMIT 1`, task, diagnosisRunOrigin, strconv.FormatInt(task, 10)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	var record diagnosisRunRecord
	if json.Unmarshal(raw, &record) != nil || record.Version != 1 || record.TaskID != task || record.SavedAt.IsZero() || !validDiagnosisRun(record.Run) || !validDiagnosisRunResult(record.Run, record.Result) {
		return nil, ErrFirstDiagnosisOutcome
	}
	if record.Model != nil && record.Model.Requests == 0 && (record.Model.Errors != 0 || record.Model.InputTokens != 0 || record.Model.OutputTokens != 0 || record.Model.CostMicros != 0) {
		return nil, ErrFirstDiagnosisOutcome
	}
	if record.Model != nil && record.Model.Requests < 0 {
		return nil, ErrFirstDiagnosisOutcome
	}
	if record.Model != nil && record.Model.Requests > 0 {
		row, err := project("execution", "진단", *record.Model)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}

type diagnosticScopeContext struct{}
type diagnosticScopeValue struct {
	task  string
	scope diagnosisprep.Scope
}

// 비공개 preparation의 AuthorizeTask 결과만 이 문맥을 설정한다. HTTP 입력에서는 받지 않는다.
func WithDiagnosticReportScope(ctx context.Context, task string, scope diagnosisprep.Scope) context.Context {
	scope.PathPrefixes = append([]string(nil), scope.PathPrefixes...)
	scope.Methods = append([]string(nil), scope.Methods...)
	if scope.Model != nil {
		model := *scope.Model
		scope.Model = &model
	}
	return context.WithValue(ctx, diagnosticScopeContext{}, diagnosticScopeValue{task: task, scope: scope})
}
func readDiagnosticOverview(ctx context.Context, tx *sql.Tx, task int64, out *report.DiagnosticReport) error {
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(name,''),created_at FROM tasks WHERE id=$1 AND deleted_at IS NULL`, task).Scan(&out.TaskTitle, &out.TaskCreatedAt); err != nil {
		return err
	}
	if v, ok := ctx.Value(diagnosticScopeContext{}).(diagnosticScopeValue); ok && v.task == strconv.FormatInt(task, 10) {
		scope := v.scope
		out.ApprovedScope = &scope
	}
	run, err := readDiagnosticRun(ctx, tx, task)
	if err != nil {
		return err
	}
	out.Run = run
	out.ModelAccounting, err = readDiagnosticModelAccounting(ctx, tx, task)
	if err != nil {
		return err
	}
	var readable bool
	if err := tx.QueryRowContext(ctx, `SELECT CASE WHEN to_regclass('llm_usage') IS NULL THEN false ELSE has_table_privilege(current_user,to_regclass('llm_usage'),'SELECT') END`).Scan(&readable); err != nil {
		return err
	}
	out.ModelRecordState = "storage_unavailable"
	if !readable {
		return nil
	}
	out.ModelRecordState = "not_recorded"
	rows, err := tx.QueryContext(ctx, `SELECT COALESCE(model,''),COALESCE(worker,''),count(*),count(*) OVER() FROM llm_usage WHERE task_id=$1 GROUP BY model,worker ORDER BY model,worker LIMIT $2`, strconv.FormatInt(task, 10), report.MaxDiagnosticModelRecords)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var model, worker string
		var n, total int64
		if err := rows.Scan(&model, &worker, &n, &total); err != nil {
			return err
		}
		if total > report.MaxDiagnosticModelRecords {
			out.ModelRecordsOmitted = total - report.MaxDiagnosticModelRecords
		}
		out.ModelRecords = append(out.ModelRecords, report.DiagnosticModelRecord{Model: model, Worker: worker, Records: n})
		out.ModelRecordState = "recorded"
	}
	return rows.Err()
}
