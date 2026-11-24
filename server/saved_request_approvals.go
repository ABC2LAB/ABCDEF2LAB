package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisflow"
)

const maxSavedRequestApprovals = 1000

// 저장된 회차는 호출자가 전달한 참조가 아니라 작업에 결속된 워크플로에서 찾는다.
func savedApprovalRequestTask(r *http.Request) (int64, bool) {
	if r.Method != http.MethodGet || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery {
		return 0, false
	}
	tail, ok := strings.CutPrefix(r.URL.Path, "/api/tasks/")
	if !ok {
		return 0, false
	}
	parts := strings.Split(tail, "/")
	if len(parts) != 2 && len(parts) != 3 || parts[1] != "saved-request-approvals" ||
		len(parts) == 3 && !managementApprovalRef.MatchString(parts[2]) {
		return 0, false
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	return id, err == nil && id > 0 && strconv.FormatInt(id, 10) == parts[0]
}

// SavedApproval은 대상 주소·경로·본문·계정·세션·결정 기능을 포함하지 않는다.
type SavedApproval struct {
	ID             string     `json:"id"`
	AttemptRef     string     `json:"attempt_ref"`
	Method         string     `json:"method"`
	State          string     `json:"state"`
	ExecutionState string     `json:"execution_state"`
	CreatedAt      time.Time  `json:"created_at"`
	ExpiresAt      time.Time  `json:"expires_at"`
	DecidedAt      *time.Time `json:"decided_at,omitempty"`
	Live           bool       `json:"live"`
	CanDecide      bool       `json:"can_decide"`
}

func projectSavedApproval(a db.RequestApproval, task int64, run string) (SavedApproval, bool) {
	if a.TaskID != task || a.RunRef != run || a.ID == "" || a.AttemptID != a.ID ||
		!managementApprovalRef.MatchString(a.ID) || a.CreatedAt.IsZero() || a.ExpiresAt.IsZero() {
		return SavedApproval{}, false
	}
	switch a.Method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
	default:
		return SavedApproval{}, false
	}
	switch a.State {
	case "pending", "allowed", "denied", "expired", "cancelled":
	default:
		return SavedApproval{}, false
	}
	switch a.ExecutionState {
	case "not_sent", "awaiting_dispatch", "dispatch_pending", "completed", "unknown":
	default:
		return SavedApproval{}, false
	}
	return SavedApproval{ID: a.ID, AttemptRef: a.AttemptID, Method: a.Method, State: a.State,
		ExecutionState: a.ExecutionState, CreatedAt: a.CreatedAt, ExpiresAt: a.ExpiresAt, DecidedAt: a.DecidedAt}, true
}

func storedApprovalRun(ctx context.Context, pool *sql.DB, task int64) (string, error) {
	var raw []byte
	var jobID string
	err := pool.QueryRowContext(ctx, `SELECT job_id,record FROM diagnosis_workflows WHERE task_id=$1 AND owner_ref='ARTEX' ORDER BY job_id LIMIT 1`, task).Scan(&jobID, &raw)
	if err != nil {
		return "", err
	}
	var record diagnosisflow.Record
	if json.Unmarshal(raw, &record) != nil || record.Owner != "ARTEX" || record.Snapshot.JobID != jobID ||
		record.Snapshot.TaskID != strconv.FormatInt(task, 10) || record.Snapshot.Format != diagnosisflow.Version ||
		!managementApprovalRef.MatchString(record.Snapshot.RunRef) {
		return "", errDiagnosticReportScope
	}
	return record.Snapshot.RunRef, nil
}

func (s *Server) serveSavedRequestApprovals(w http.ResponseWriter, r *http.Request) {
	task, ok := savedApprovalRequestTask(r)
	if !ok || s.m == nil || s.m.pg == nil || s.m.pg.DB == nil {
		writeErr(w, http.StatusForbidden, "저장 승인 이력의 작업 결속을 확인할 수 없습니다")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	run, err := storedApprovalRun(ctx, s.m.pg.DB, task)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "이 작업에 저장된 승인 이력이 없습니다")
		return
	}
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "저장 승인 이력을 확인할 수 없습니다")
		return
	}
	id := r.PathValue("approvalID")
	if id != "" {
		var raw []byte
		err = s.m.pg.QueryRowContext(ctx, `SELECT record FROM request_approvals WHERE task_id=$1 AND owner_ref='ARTEX' AND id=$2 AND record->>'run_ref'=$3`, task, id, run).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "승인 기록을 찾을 수 없습니다")
			return
		}
		var a db.RequestApproval
		if err != nil || json.Unmarshal(raw, &a) != nil {
			writeErr(w, http.StatusServiceUnavailable, "승인 기록을 읽을 수 없습니다")
			return
		}
		v, valid := projectSavedApproval(a, task, run)
		if !valid || v.ID != id {
			writeErr(w, http.StatusServiceUnavailable, "승인 기록의 결속을 확인할 수 없습니다")
			return
		}
		writeJSON(w, http.StatusOK, v)
		return
	}
	rows, err := s.m.pg.QueryContext(ctx, `SELECT id,record FROM request_approvals WHERE task_id=$1 AND owner_ref='ARTEX' AND record->>'run_ref'=$2 ORDER BY id LIMIT $3`, task, run, maxSavedRequestApprovals+1)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "승인 이력을 읽을 수 없습니다")
		return
	}
	defer rows.Close()
	out := make([]SavedApproval, 0)
	for rows.Next() {
		var raw []byte
		var storedID string
		var a db.RequestApproval
		if rows.Scan(&storedID, &raw) != nil || json.Unmarshal(raw, &a) != nil {
			writeErr(w, http.StatusServiceUnavailable, "승인 이력을 읽을 수 없습니다")
			return
		}
		v, valid := projectSavedApproval(a, task, run)
		if !valid || v.ID != storedID || len(out) >= maxSavedRequestApprovals {
			writeErr(w, http.StatusServiceUnavailable, "승인 이력의 범위를 확인할 수 없습니다")
			return
		}
		out = append(out, v)
	}
	if rows.Err() != nil {
		writeErr(w, http.StatusServiceUnavailable, "승인 이력을 읽을 수 없습니다")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run_ref": run, "approvals": out})
}
