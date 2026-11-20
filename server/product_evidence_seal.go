package server

import (
	"context"
	"errors"

	"github.com/Autumn-27/artex/db"
)

// 제품 판정의 봉인 확인 표시(docs/contracts/e3-product-verdict-store.md §7). 판정 행은 바꾸지 않고, 회차의 봉인 확인 기록
// (e3_seal_checks)을 판정의 근거 체크포인트와 대조해 세 단계로 보인다.
const (
	productEvidenceSealConfirmed         = "confirmed"
	productEvidenceSealUnconfirmed       = "unconfirmed"
	productEvidenceSealInclusionFailed   = "inclusion_failed"
	productEvidenceSealNotRecorded       = "seal_not_recorded"
	productEvidenceSealCheckpointMissing = "evidence_checkpoint_missing"
)

// productEvidenceSeal은 행 하나의 봉인 확인 표시다. reason은 unconfirmed일 때만 있다.
type productEvidenceSeal struct {
	Status string  `json:"status"`
	Reason *string `json:"reason"`
}

// productSealCheck는 회차 하나의 봉인 확인 기록이다. record가 nil이면 기록이 없다. invalid는 저장된 기록이 계약 모양이
// 아니다(위조로 보고 그 회차 근거를 포함 실패로 둔다).
type productSealCheck struct {
	record  *db.SealCheckRecord
	invalid bool
}

type productSealChecks map[string]productSealCheck

// loadProductSealChecks는 판정 행들의 회차마다 봉인 확인 기록을 같은 읽기 스냅샷에서 읽는다. 읽기 오류는 응답 전체를
// 실패시킨다(다른 행 값을 추측해 채우지 않음).
func loadProductSealChecks(ctx context.Context, q reportReadQuery, taskID int64, runRefs []string) (productSealChecks, error) {
	out := productSealChecks{}
	for _, run := range runRefs {
		if _, done := out[run]; done || run == "" {
			continue
		}
		record, ok, err := db.ReadSealCheck(ctx, q, taskID, run)
		switch {
		case errors.Is(err, db.ErrSealCheckStored):
			out[run] = productSealCheck{invalid: true}
		case err != nil:
			return nil, err
		case ok:
			out[run] = productSealCheck{record: &record}
		default:
			out[run] = productSealCheck{}
		}
	}
	return out, nil
}

// productEvidenceSealFor는 검증을 통과한 제품 판정의 봉인 확인 표시와, 그것이 진위 실패(포함 실패)인지를 돌려준다. 근거
// 호출이 없는 판정은 봉인 확인 대상이 아니다(nil).
func productEvidenceSealFor(v verdictDetailVerified, seals productSealChecks) (*productEvidenceSeal, bool) {
	if v.evidence == 0 {
		return nil, false
	}
	unconfirmed := func(reason string) (*productEvidenceSeal, bool) {
		return &productEvidenceSeal{Status: productEvidenceSealUnconfirmed, Reason: &reason}, false
	}
	failed := &productEvidenceSeal{Status: productEvidenceSealInclusionFailed}
	if v.checkpoint == nil {
		return unconfirmed(productEvidenceSealCheckpointMissing)
	}
	seal := seals[v.runRef]
	if seal.invalid {
		return failed, true
	}
	if seal.record == nil {
		return unconfirmed(productEvidenceSealNotRecorded)
	}
	if seal.record.Head.JournalID != v.checkpoint.JournalID {
		return failed, true
	}
	for _, c := range seal.record.Checks {
		if c.Sequence == v.checkpoint.Sequence && c.MAC == v.checkpoint.MAC {
			if c.Included {
				return &productEvidenceSeal{Status: productEvidenceSealConfirmed}, false
			}
			return failed, true
		}
	}
	// Host는 내준 근거를 모두 확인 목록에 남긴다. 목록에 없는 근거는 Host가 내준 것이 아니다.
	return failed, true
}

// productVerdictRunRefs는 행들의 회차 참조다(발급 칸 execution_ref).
func productVerdictRunRefs(rows []verdictDetailRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.issExecution.Valid {
			out = append(out, r.issExecution.String)
		}
	}
	return out
}
