package server

import (
	"context"
	"fmt"
	"log"
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

// WithDiagnosticProductReport는 준비 서비스에서 작업 조회를 허가한 뒤 제품 보고서 결속을 설정한다.
func WithDiagnosticProductReport(ctx context.Context, taskID, jobID string, endpoint diagnosisprep.Endpoint) context.Context {
	return context.WithValue(ctx, diagnosticProductContextKey{}, diagnosticProductBinding{
		taskID: taskID, jobID: jobID, reader: productPolicySourceReader{endpoint: endpoint, principal: "ARTEX", jobID: jobID},
	})
}

type diagnosticProductSnapshot struct {
	binding     diagnosticProductBinding
	exploration int64
	rows        []verdictDetailRow
	seals       productSealChecks
}

func diagnosticProductReadError(stage string, status int, code string) error {
	log.Printf(logT("[diagnostic-report] 제품 판정 조회 실패 단계=%s 상태=%d 코드=%s"), stage, status, code)
	if status == http.StatusUnprocessableEntity {
		return report.ErrExportBlocked
	}
	return fmt.Errorf("제품 판정 조회 실패: %s (%d)", code, status)
}

func loadDiagnosticProductSnapshot(ctx context.Context, tx reportReadQuery, taskID int64, entries int) (*diagnosticProductSnapshot, error) {
	b, ok := ctx.Value(diagnosticProductContextKey{}).(diagnosticProductBinding)
	if !ok || b.taskID != strconv.FormatInt(taskID, 10) || b.jobID == "" {
		return nil, nil
	}
	exploration, rows, status, code := loadProductVerdictRows(ctx, tx, taskID)
	if status != http.StatusOK {
		return nil, diagnosticProductReadError("load", status, code)
	}
	if entries+len(rows) > report.MaxFindings {
		return nil, report.ErrExportBlocked
	}
	if len(rows) == 0 {
		return nil, nil
	}
	seals, err := loadProductSealChecks(ctx, tx, taskID, productVerdictRunRefs(rows))
	if err != nil {
		return nil, diagnosticProductReadError("seal-checks", http.StatusServiceUnavailable, productVerdictsUnavailable)
	}
	return &diagnosticProductSnapshot{binding: b, exploration: exploration, rows: rows, seals: seals}, nil
}

// addToReport는 DB 스냅샷을 닫은 뒤 당시 정책 원본을 조회한다.
func (s *diagnosticProductSnapshot) addToReport(ctx context.Context, out *report.DiagnosticReport) error {
	if s == nil {
		return nil
	}
	got, err := s.binding.reader.read(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.Printf(logT("[diagnostic-report] 제품 판정 정책 원본 조회 실패 작업=%d 코드=policy_source_unavailable"), out.TaskID)
		out.ProductUnreadable = len(s.rows)
		return nil
	}
	// 판정 목록과 동일한 결속·동결본·봉인 검증을 통과한 행의 결과를 표시한다.
	list, status, code := assembleProductVerdictList(out.TaskID, s.exploration, s.binding.jobID, s.rows, got, s.seals)
	if status != http.StatusOK {
		return diagnosticProductReadError("assemble", status, code)
	}
	for _, row := range list.Items {
		if row.RecordStatus == verdictDetailPolicySourceMissing {
			out.ProductSourceUnavailable++
			continue
		}
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
