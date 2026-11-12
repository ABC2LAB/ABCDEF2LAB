package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/report"
	"strconv"
)

func readWorkflowReport(ctx context.Context, tx *sql.Tx, taskID int64, out *report.DiagnosticReport) error {
	var exists bool
	if tx.QueryRowContext(ctx, `SELECT to_regclass('diagnosis_workflows') IS NOT NULL`).Scan(&exists) != nil {
		return errDiagnosticReportScope
	}
	if !exists {
		return nil
	}
	var raw []byte
	var recordBytes int
	err := tx.QueryRowContext(ctx, `SELECT CASE WHEN octet_length(record::text)<=131072 THEN record ELSE NULL END,octet_length(record::text) FROM diagnosis_workflows WHERE task_id=$1 AND owner_ref='ARTEX' ORDER BY job_id LIMIT 1`, taskID).Scan(&raw, &recordBytes)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil || recordBytes > 131072 {
		return errDiagnosticReportScope
	}
	var record diagnosisflow.Record
	if json.Unmarshal(raw, &record) != nil || record.Owner != "ARTEX" || record.Snapshot.TaskID != strconv.FormatInt(taskID, 10) || record.Snapshot.Format != diagnosisflow.Version {
		return errDiagnosticReportScope
	}
	flow := diagnosisflow.Project(record.Snapshot)
	out.Workflow = &flow
	rows, err := tx.QueryContext(ctx, `SELECT COALESCE(record->>'method',''),COALESCE(record->>'path',''),COALESCE(record->>'state',''),COALESCE(record->>'execution_state',''),COALESCE(record->>'attempt_id','') FROM request_approvals WHERE task_id=$1 AND owner_ref='ARTEX' AND record->>'run_ref'=$2 ORDER BY id LIMIT 100`, taskID, flow.RunRef)
	if err != nil {
		return errDiagnosticReportScope
	}
	defer rows.Close()
	for rows.Next() {
		var v report.WorkflowApproval
		if rows.Scan(&v.Method, &v.Path, &v.State, &v.ExecutionState, &v.Attempt) != nil {
			return errDiagnosticReportScope
		}
		v.Path = report.DiagnosticText(v.Path)
		out.WorkflowApprovals = append(out.WorkflowApprovals, v)
	}
	return rows.Err()
}
