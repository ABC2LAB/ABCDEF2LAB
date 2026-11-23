package server

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/report"
)

// 보고서의 확정 단계 판정 구역(2026-11-23 사용자 결정, #875 발견 1). 제품 판정의 진위 확인은 준비 서비스 IPC가 필요해 제품
// 관리 중계만 할 수 있다. 그래서 중계가 판정 목록 API와 같은 확인(readProductVerdictList)을 거친 목록을 내부 헤더로 보고서
// 생성 쪽에 넘긴다. 중계는 실행 서버로 보내는 요청을 새로 만들어 밖에서 온 같은 이름 헤더를 넘기지 않는다. 그래도 보고서
// 쪽은 저장된 제품 판정 행과 판정 번호·값·자원 종류를 다시 대조해, 저장되지 않았거나 다른 판정을 확정으로 세지 않는다.

var errReportProductVerdicts = errors.New("report product verdicts unavailable")

const (
	reportProductVerdictsHeader = "X-ABC2LAB-Report-Product-Verdicts"
	// 헤더 값(base64url JSON) 상한. 넘으면 뒤 행부터 빼고 전체 수를 남겨 보고서가 'n건 중 m건 표시'를 적는다.
	maxReportProductVerdictsHeaderBytes = 8 << 10

	reportProductVerdictsOK          = "ok"
	reportProductVerdictsUnavailable = "unavailable"
)

type reportProductVerdictsWire struct {
	State string                     `json:"state"`
	Total int                        `json:"total"`
	Items []reportProductVerdictWire `json:"items"`
}

type reportProductVerdictWire struct {
	VerdictID     string   `json:"verdict_id"`
	Verified      bool     `json:"verified"`
	ResourceType  string   `json:"resource_type,omitempty"`
	OracleValue   string   `json:"oracle_value,omitempty"`
	ReasonCodes   []string `json:"reason_codes,omitempty"`
	EvidenceCalls int      `json:"evidence_calls,omitempty"`
	Seal          string   `json:"seal,omitempty"`
	ObservedAt    string   `json:"observed_at,omitempty"`
}

// reportProductVerdictsUnavailableHeader는 중계가 판정 목록을 읽지 못했을 때의 헤더 값이다.
func reportProductVerdictsUnavailableHeader() string {
	raw, _ := json.Marshal(reportProductVerdictsWire{State: reportProductVerdictsUnavailable, Items: []reportProductVerdictWire{}})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// encodeReportProductVerdicts는 진위 확인을 마친 판정 목록을 헤더 값으로 만든다. record_status가 verified가 아닌 행
// (진위 실패·당시 원본 미확인)은 값 없이 확인 실패로 싣는다. 크기 상한을 넘으면 뒤 행부터 뺀다.
func encodeReportProductVerdicts(env productVerdictListEnvelope) string {
	wire := reportProductVerdictsWire{State: reportProductVerdictsOK, Total: len(env.Items), Items: make([]reportProductVerdictWire, 0, len(env.Items))}
	for _, item := range env.Items {
		w := reportProductVerdictWire{}
		if item.VerdictID != nil {
			w.VerdictID = *item.VerdictID
		}
		if item.RecordStatus == productVerdictVerified && item.VerdictID != nil && item.ResourceType != nil && item.OracleValue != nil && item.ObservedAt != nil {
			w.Verified = true
			w.ResourceType, w.OracleValue, w.ObservedAt = *item.ResourceType, *item.OracleValue, *item.ObservedAt
			w.ReasonCodes = append(append([]string{}, item.OracleReasonCodes...), item.ProductReasonCodes...)
			if item.EvidenceCalls != nil {
				w.EvidenceCalls = *item.EvidenceCalls
			}
			if item.EvidenceSeal != nil {
				w.Seal = item.EvidenceSeal.Status
			}
		}
		wire.Items = append(wire.Items, w)
	}
	for {
		raw, err := json.Marshal(wire)
		if err != nil {
			return reportProductVerdictsUnavailableHeader()
		}
		value := base64.RawURLEncoding.EncodeToString(raw)
		if len(value) <= maxReportProductVerdictsHeaderBytes || len(wire.Items) == 0 {
			return value
		}
		wire.Items = wire.Items[:len(wire.Items)-1]
	}
}

// reportProductVerdicts는 중계가 보고서 요청에 붙일 헤더 값이다. 작업 결속·판정 목록을 읽지 못하면 '읽지 못함'이다.
// 이미 작업 권한을 확인한 요청에서만 부른다.
func (b *diagnosisManagementBridge) reportProductVerdicts(ctx context.Context, owner, taskRef string) string {
	task, err := strconv.ParseInt(taskRef, 10, 64)
	if err != nil || task <= 0 || b.candidateJob == nil || b.candidateEndpoint == nil || b.readPool == nil {
		return reportProductVerdictsUnavailableHeader()
	}
	readCtx, cancel := context.WithTimeout(ctx, verdictDetailReadTimeout)
	defer cancel()
	job, err := b.candidateJob(readCtx, owner, taskRef)
	if err != nil || job == nil || job.TaskID != taskRef || job.ID == "" {
		log.Print(logT("[report] 확정 단계 판정 목록 결속을 읽지 못함"))
		return reportProductVerdictsUnavailableHeader()
	}
	reader := productPolicySourceReader{endpoint: b.candidateEndpoint, principal: owner, jobID: job.ID}
	env, status, _ := readProductVerdictList(readCtx, b.readPool, task, job.ID, reader)
	if status != http.StatusOK {
		log.Printf(logT("[report] 확정 단계 판정 목록을 읽지 못함 상태=%d"), status)
		return reportProductVerdictsUnavailableHeader()
	}
	return encodeReportProductVerdicts(env)
}

// reportProductVerdictsFromRequest는 보고서 생성 쪽에서 헤더를 읽어 구역 입력을 만든다. 헤더가 없으면 구역을 그리지 않는다
// (제품 중계를 거치지 않은 보고서). 헤더가 깨졌거나 저장 행을 읽지 못하면 '읽지 못함'이다.
func reportProductVerdictsFromRequest(ctx context.Context, pool reportProductVerdictStore, r *http.Request, taskID int64) report.ProductVerdicts {
	values := r.Header.Values(reportProductVerdictsHeader)
	if len(values) == 0 {
		return report.ProductVerdicts{}
	}
	unavailable := report.ProductVerdicts{Provided: true, Unavailable: true}
	wire, ok := decodeReportProductVerdicts(values)
	if !ok {
		log.Print(logT("[report] 확정 단계 판정 헤더 모양이 맞지 않음"))
		return unavailable
	}
	if wire.State == reportProductVerdictsUnavailable {
		return unavailable
	}
	stored, err := storedProductVerdicts(ctx, pool, taskID)
	if err != nil {
		log.Print(logT("[report] 저장 제품 판정을 읽지 못함"))
		return unavailable
	}
	return crossCheckReportProductVerdicts(wire, stored)
}

// crossCheckReportProductVerdicts는 중계가 넘긴 행을 저장 판정과 대조해 구역 입력을 만든다.
func crossCheckReportProductVerdicts(wire reportProductVerdictsWire, stored map[string]storedProductVerdict) report.ProductVerdicts {
	out := report.ProductVerdicts{Provided: true, Total: wire.Total, Items: make([]report.ProductVerdictEntry, 0, len(wire.Items))}
	for _, w := range wire.Items {
		e := report.ProductVerdictEntry{VerdictID: w.VerdictID}
		at, atErr := time.Parse(time.RFC3339Nano, w.ObservedAt)
		row, found := stored[w.VerdictID]
		// 저장 행과 번호·값·자원 종류가 같을 때만 확인된 판정으로 둔다. 다르면 확인 실패(확정에 안 셈)다.
		if w.Verified && atErr == nil && found && row.oracleValue == w.OracleValue && row.resourceType == w.ResourceType {
			e.Verified = true
			e.ResourceType, e.OracleValue, e.ReasonCodes = w.ResourceType, w.OracleValue, w.ReasonCodes
			e.EvidenceCalls, e.Seal, e.ObservedAt = w.EvidenceCalls, w.Seal, at.UTC()
		}
		out.Items = append(out.Items, e)
	}
	return out
}

func decodeReportProductVerdicts(values []string) (reportProductVerdictsWire, bool) {
	var wire reportProductVerdictsWire
	if len(values) != 1 || len(values[0]) > maxReportProductVerdictsHeaderBytes {
		return wire, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(values[0])
	if err != nil || strictJSON(raw, &wire) != nil {
		return wire, false
	}
	if wire.State != reportProductVerdictsOK && wire.State != reportProductVerdictsUnavailable {
		return wire, false
	}
	if wire.State == reportProductVerdictsUnavailable && (wire.Total != 0 || len(wire.Items) != 0) {
		return wire, false
	}
	if wire.Total < len(wire.Items) || wire.Total > report.MaxDiagnosticProductVerdicts {
		return wire, false
	}
	return wire, true
}

// reportProductVerdictStore는 저장 제품 판정 행을 읽을 곳이다(*db.DB·*sql.DB).
type reportProductVerdictStore interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

type storedProductVerdict struct{ oracleValue, resourceType string }

// storedProductVerdicts는 작업의 제품 판정 행을 판정 번호로 묶는다. 값은 판정 기록(payload)의 자원 종류·판정 값이다.
func storedProductVerdicts(ctx context.Context, pool reportProductVerdictStore, taskID int64) (map[string]storedProductVerdict, error) {
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, rows, status, _ := loadProductVerdictRows(ctx, tx, taskID)
	if status != http.StatusOK {
		return nil, errReportProductVerdicts
	}
	out := map[string]storedProductVerdict{}
	for _, r := range rows {
		if !r.verdictID.Valid || !r.oracleValue.Valid {
			continue
		}
		var payload struct {
			ResourceType string `json:"resource_type"`
			OracleValue  string `json:"oracle_value"`
		}
		// 판정 값은 저장 칸과 판정 기록 정본이 같을 때만 쓴다.
		if json.Unmarshal(r.payload, &payload) != nil || payload.OracleValue != r.oracleValue.String {
			continue
		}
		out[strconv.FormatInt(r.verdictID.Int64, 10)] = storedProductVerdict{oracleValue: payload.OracleValue, resourceType: payload.ResourceType}
	}
	return out, tx.Commit()
}
