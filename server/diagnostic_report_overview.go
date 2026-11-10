package server

import (
	"context"
	"database/sql"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/report"
	"strconv"
)

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
	var readable bool
	if err := tx.QueryRowContext(ctx, `SELECT CASE WHEN to_regclass('llm_usage') IS NULL THEN false ELSE has_table_privilege(current_user,to_regclass('llm_usage'),'SELECT') END`).Scan(&readable); err != nil {
		return err
	}
	out.ModelRecordState = "storage_unavailable"
	if !readable {
		return nil
	}
	out.ModelRecordState = "not_recorded"
	rows, err := tx.QueryContext(ctx, `SELECT COALESCE(model,''),COALESCE(worker,''),count(*) FROM llm_usage WHERE task_id=$1 GROUP BY model,worker ORDER BY model,worker LIMIT 65`, strconv.FormatInt(task, 10))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var model, worker string
		var n int64
		if rows.Scan(&model, &worker, &n) != nil {
			return errDiagnosticReportScope
		}
		if len(out.ModelRecords) >= 64 {
			return errDiagnosticReportScope
		}
		out.ModelRecords = append(out.ModelRecords, report.DiagnosticModelRecord{Model: model, Worker: worker, Records: n})
		out.ModelRecordState = "recorded"
	}
	return rows.Err()
}
