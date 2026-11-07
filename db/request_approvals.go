package db

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var ErrRequestApprovalConflict = errors.New("request_approval_conflict")

const requestApprovalSchemaSQL = `CREATE TABLE IF NOT EXISTS request_approvals (
 id TEXT PRIMARY KEY,
 task_id BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
 owner_ref TEXT NOT NULL,
 version BIGINT NOT NULL DEFAULT 1,
 expires_at TIMESTAMPTZ NOT NULL,
 record JSONB NOT NULL
);
CREATE INDEX IF NOT EXISTS request_approvals_task_idx ON request_approvals(task_id);`

// 원문 요청은 저장하지 않는다. InputPreview는 신뢰 호스트의 공개 필드만 포함한다.
type RequestApproval struct {
	ID             string            `json:"id"`
	TaskID         int64             `json:"task_id"`
	OwnerRef       string            `json:"-"`
	RunRef         string            `json:"run_ref"`
	IntentID       int64             `json:"intent_id"`
	CallRef        string            `json:"call_ref"`
	AttemptID      string            `json:"attempt_id"`
	PolicyRevision string            `json:"policy_revision"`
	AccountRef     string            `json:"account_ref"`
	SessionRef     string            `json:"session_ref"`
	RequestSHA256  string            `json:"request_sha256"`
	Method         string            `json:"method"`
	TargetOrigin   string            `json:"target_origin"`
	Path           string            `json:"path"`
	InputPreview   map[string]string `json:"input_preview"`
	BodyBytes      int               `json:"body_bytes"`
	CreatedAt      time.Time         `json:"created_at"`
	ExpiresAt      time.Time         `json:"expires_at"`
	State          string            `json:"state"`
	DecidedBy      string            `json:"decided_by,omitempty"`
	DecidedAt      *time.Time        `json:"decided_at,omitempty"`
	Consumed       bool              `json:"consumed"`
	ExecutionState string            `json:"execution_state"`
	Reason         string            `json:"reason,omitempty"`
	Live           bool              `json:"live"`
	CanDecide      bool              `json:"can_decide"`
	Version        int64             `json:"-"`
}

func (d *DB) InsertRequestApproval(ctx context.Context, a RequestApproval) error {
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = d.ExecContext(ctx, `INSERT INTO request_approvals(id,task_id,owner_ref,expires_at,record) VALUES($1,$2,$3,$4,$5)`, a.ID, a.TaskID, a.OwnerRef, a.ExpiresAt, raw)
	return err
}
func scanRequestApproval(row interface{ Scan(...any) error }) (RequestApproval, error) {
	var a RequestApproval
	var raw []byte
	var owner string
	var version int64
	if err := row.Scan(&raw, &owner, &version); err != nil {
		return a, err
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return a, err
	}
	a.OwnerRef = owner
	a.Version = version
	return a, nil
}
func (d *DB) GetRequestApproval(ctx context.Context, task int64, owner, id string) (RequestApproval, error) {
	return scanRequestApproval(d.QueryRowContext(ctx, `SELECT record,owner_ref,version FROM request_approvals WHERE task_id=$1 AND owner_ref=$2 AND id=$3`, task, owner, id))
}
func (d *DB) ListRequestApprovals(ctx context.Context, task int64, owner string) ([]RequestApproval, error) {
	rows, err := d.QueryContext(ctx, `SELECT record,owner_ref,version FROM request_approvals WHERE task_id=$1 AND owner_ref=$2 ORDER BY (record->>'created_at') DESC LIMIT 200`, task, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RequestApproval{}
	for rows.Next() {
		a, e := scanRequestApproval(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// version CAS와 DB 시계를 함께 사용해 중복 결정·만료 후 소비를 거부한다.
func (d *DB) SwapRequestApproval(ctx context.Context, a RequestApproval, requireFresh bool) error {
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	result, err := d.ExecContext(ctx, `UPDATE request_approvals SET record=$1,version=version+1 WHERE id=$2 AND task_id=$3 AND owner_ref=$4 AND version=$5 AND (NOT $6 OR expires_at>NOW())`, raw, a.ID, a.TaskID, a.OwnerRef, a.Version, requireFresh)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrRequestApprovalConflict
	}
	return nil
}
