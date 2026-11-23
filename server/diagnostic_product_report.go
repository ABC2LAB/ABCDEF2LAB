package server

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/report"
)

type diagnosticProductContextKey struct{}

type diagnosticProductBinding struct {
	taskID string
	jobID  string
	reader productVerdictContextReader
}

// WithDiagnosticProductReport is called after the preparation service has authorized the task.
func WithDiagnosticProductReport(ctx context.Context, taskID, jobID string, endpoint diagnosisprep.Endpoint) context.Context {
	return context.WithValue(ctx, diagnosticProductContextKey{}, diagnosticProductBinding{
		taskID: taskID, jobID: jobID, reader: productPolicySourceReader{endpoint: endpoint, principal: "ARTEX", jobID: jobID},
	})
}

func readDiagnosticProductVerdicts(ctx context.Context, tx *sql.Tx, taskID int64, out *report.DiagnosticReport) error {
	b, ok := ctx.Value(diagnosticProductContextKey{}).(diagnosticProductBinding)
	if !ok || b.taskID != strconv.FormatInt(taskID, 10) || b.jobID == "" {
		return nil
	}
	exploration, rows, status, _ := loadProductVerdictRows(ctx, tx, taskID)
	if status != http.StatusOK {
		return errDiagnosticReportScope
	}
	if len(out.Entries)+len(rows) > report.MaxFindings {
		return report.ErrExportBlocked
	}
	if len(rows) == 0 {
		return nil
	}
	seals, err := loadProductSealChecks(ctx, tx, taskID, productVerdictRunRefs(rows))
	if err != nil {
		return err
	}
	got, err := b.reader.read(ctx)
	if err != nil {
		out.ProductUnreadable = len(rows)
		return nil
	}
	// 판정 목록과 동일한 결속·동결본·봉인 검증을 통과한 행의 결과를 표시한다.
	list, status, _ := assembleProductVerdictList(taskID, exploration, b.jobID, rows, got, seals)
	if status != http.StatusOK {
		return errDiagnosticReportScope
	}
	for _, row := range list.Items {
		if row.RecordStatus != productVerdictVerified || row.VerdictID == nil || row.OracleValue == nil {
			out.ProductUnverified++
			continue
		}
		switch oracle.VerdictValue(*row.OracleValue) {
		case oracle.Confirmed:
			confirmation := report.DiagnosticProductConfirmation{
				VerdictID: *row.VerdictID, ResourceType: *row.ResourceType, EvidenceCalls: *row.EvidenceCalls,
			}
			if seal := row.EvidenceSeal; seal != nil && seal.Status == productEvidenceSealUnconfirmed && seal.Reason != nil {
				confirmation.SealUnconfirmedReason = *seal.Reason
			}
			out.ProductConfirmed = append(out.ProductConfirmed, confirmation)
		case oracle.Suspected:
			out.ProductSuspected++
		case oracle.Undecidable:
			out.ProductUndecidable++
		}
	}
	return nil
}
