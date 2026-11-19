package db

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// ErrSealCheckInput은 봉인 확인 기록 칸이 계약 모양이 아닌 것이다.
var ErrSealCheckInput = errors.New("db: e3 seal check input invalid")

// maxSealChecks는 한 회차 봉인 확인 결과 수의 상한이다(E② Host가 회차마다 내주는 근거 체크포인트 상한과 같다).
const maxSealChecks = 128

// SealCheck는 근거 체크포인트 하나의 봉인 포함 결과다.
type SealCheck struct {
	Sequence uint64 `json:"sequence"`
	MAC      string `json:"mac"`
	Included bool   `json:"included"`
}

// SealCheckRecord는 회차 하나의 봉인 확인 기록이다(E1 계약 §6). Head는 신뢰 저장소가 인증한 sealed head다.
type SealCheckRecord struct {
	TaskID     int64
	RunRef     string
	Head       ProductCheckpoint
	Checks     []SealCheck
	RecordedAt time.Time
}

func validSealCheckRecord(r SealCheckRecord) bool {
	if r.TaskID <= 0 || !validProductRefs(r.RunRef, r.Head.JournalID) || r.Head.Sequence > 1<<62 || !validLowerHex64(r.Head.MAC) ||
		r.Checks == nil || len(r.Checks) > maxSealChecks {
		return false
	}
	seen := map[uint64]bool{}
	for _, c := range r.Checks {
		if c.Sequence == 0 || c.Sequence > r.Head.Sequence && c.Included || !validLowerHex64(c.MAC) || seen[c.Sequence] {
			return false
		}
		seen[c.Sequence] = true
	}
	return true
}

// RecordSealCheck는 회차의 봉인 확인을 한 번만 INSERT한다. 같은 회차에 이미 같은 기록이 있으면 그대로 두고, 다른 기록이면
// 오류다(바꾸지 않는다).
func (d *DB) RecordSealCheck(ctx context.Context, r SealCheckRecord) error {
	if d == nil || ctx == nil || !validSealCheckRecord(r) {
		return ErrSealCheckInput
	}
	checks, err := json.Marshal(r.Checks)
	if err != nil {
		return ErrSealCheckInput
	}
	res, err := d.ExecContext(ctx, `INSERT INTO e3_seal_checks(task_id,run_ref,journal_id,head_sequence,head_mac,checks)
VALUES($1,$2,$3,$4,$5,$6::jsonb) ON CONFLICT (task_id,run_ref) DO NOTHING`, r.TaskID, r.RunRef, r.Head.JournalID, int64(r.Head.Sequence), r.Head.MAC, string(checks))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 1 {
		return err
	}
	existing, ok, err := d.SealCheck(ctx, r.TaskID, r.RunRef)
	if err != nil {
		return err
	}
	if !ok || existing.Head != r.Head || !equalSealChecks(existing.Checks, r.Checks) {
		return ErrSealCheckInput
	}
	return nil
}

func equalSealChecks(a, b []SealCheck) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// SealCheck는 회차의 봉인 확인 기록을 읽는다. 없으면 ok=false다. 저장된 칸이 계약 모양이 아니면 오류다.
func (d *DB) SealCheck(ctx context.Context, taskID int64, runRef string) (SealCheckRecord, bool, error) {
	if d == nil {
		return SealCheckRecord{}, false, ErrSealCheckInput
	}
	return ReadSealCheck(ctx, d.DB, taskID, runRef)
}

// ErrSealCheckStored는 저장된 봉인 확인 기록이 계약 모양이 아닌 것이다(읽는 쪽은 그 회차를 봉인 확인 실패로 본다).
var ErrSealCheckStored = errors.New("db: stored e3 seal check invalid")

// ReadSealCheck는 열린 연결·트랜잭션(읽기 스냅샷)에서 봉인 확인 기록을 읽는다.
func ReadSealCheck(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, taskID int64, runRef string) (SealCheckRecord, bool, error) {
	if ctx == nil || q == nil {
		return SealCheckRecord{}, false, ErrSealCheckInput
	}
	var r SealCheckRecord
	var sequence int64
	var raw []byte
	err := q.QueryRowContext(ctx, `SELECT task_id,run_ref,journal_id,head_sequence,head_mac,checks::text,recorded_at FROM e3_seal_checks WHERE task_id=$1 AND run_ref=$2`,
		taskID, runRef).Scan(&r.TaskID, &r.RunRef, &r.Head.JournalID, &sequence, &r.Head.MAC, &raw, &r.RecordedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SealCheckRecord{}, false, nil
	}
	if err != nil {
		return SealCheckRecord{}, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if sequence < 0 || decoder.Decode(&r.Checks) != nil {
		return SealCheckRecord{}, false, ErrSealCheckStored
	}
	r.Head.Sequence = uint64(sequence)
	if !validSealCheckRecord(r) {
		return SealCheckRecord{}, false, ErrSealCheckStored
	}
	return r, true, nil
}
