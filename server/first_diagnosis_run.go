package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const diagnosisRunOrigin = "first_diagnosis_run_v1"

type diagnosisRunRecord struct {
	Version int                     `json:"version"`
	TaskID  int64                   `json:"task_id"`
	SavedAt time.Time               `json:"saved_at"`
	Run     requestApprovalRunState `json:"run"`
}

// PersistRunOutcome는 drain 뒤의 감독자 상태만 저장한다. 조회 서버는 이 상태로 실행 권한을 복구하지 않는다.
func (s *FirstDiagnosisSupervisor) PersistRunOutcome(ctx context.Context, closureErr error) error {
	if s == nil || ctx == nil || ctx.Err() != nil {
		return ErrFirstDiagnosisOutcome
	}
	s.mu.Lock()
	started, running, registered, task, server := s.runStarted, s.running, s.registered, s.runTask, s.server
	s.mu.Unlock()
	if !started || registered {
		return nil
	}
	if running || server == nil || server.requestApprovals == nil || s.m == nil || s.m.pg == nil {
		return ErrFirstDiagnosisOutcome
	}
	state := server.requestApprovals.runState(mustFirstDiagnosisTaskNumber(task))
	// 실행 실패 자체는 자원 마감 실패와 구별한다.
	if closureErr != nil {
		state.Phase, state.ClosureFailed, state.DrainComplete = "failed", true, false
	}
	return saveDiagnosisRun(ctx, s.m.pg.DB, mustFirstDiagnosisTaskNumber(task), state)
}

func validDiagnosisRun(state requestApprovalRunState) bool {
	if state.RunRef == "" || (state.Phase != "closed" && state.Phase != "failed") {
		return false
	}
	switch state.Outcome {
	case "completed", "failed", "cancelled":
	default:
		return false
	}
	if state.Phase == "closed" && (!state.DrainComplete || state.ClosureFailed) {
		return false
	}
	if state.Outcome == "completed" && state.Phase != "closed" && !state.ClosureFailed {
		return false
	}
	if state.ClosureFailed && (state.Phase != "failed" || state.DrainComplete) {
		return false
	}
	return true
}

func saveDiagnosisRun(ctx context.Context, pool *sql.DB, task int64, state requestApprovalRunState) error {
	if ctx == nil || pool == nil || task <= 0 || !validDiagnosisRun(state) {
		return ErrFirstDiagnosisOutcome
	}
	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		return ErrFirstDiagnosisOutcome
	}
	defer tx.Rollback()
	var exploration int64
	// 동일 task의 중복 종료 호출은 같은 기록 하나로 수렴하며 다른 결과로 덮어쓰지 않는다.
	if tx.QueryRowContext(ctx, `SELECT exploration_id FROM tasks WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, task).Scan(&exploration) != nil {
		return ErrFirstDiagnosisOutcome
	}
	var existing []byte
	err = tx.QueryRowContext(ctx, `SELECT payload FROM exploration_nodes WHERE exploration_id=$1 AND kind='fact' AND state='confirmed' AND origin=$2 AND payload->>'task_id'=$3 ORDER BY id DESC LIMIT 1`, exploration, diagnosisRunOrigin, strconv.FormatInt(task, 10)).Scan(&existing)
	if err == nil {
		var old diagnosisRunRecord
		a, marshalErr := json.Marshal(state)
		if marshalErr != nil {
			return ErrFirstDiagnosisOutcome
		}
		if json.Unmarshal(existing, &old) != nil || old.Version != 1 || old.TaskID != task || old.SavedAt.IsZero() || !validDiagnosisRun(old.Run) {
			return ErrFirstDiagnosisOutcome
		}
		b, marshalErr := json.Marshal(old.Run)
		if marshalErr != nil {
			return ErrFirstDiagnosisOutcome
		}
		if !bytes.Equal(a, b) {
			return ErrFirstDiagnosisOutcome
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ErrFirstDiagnosisOutcome
	}
	payload, err := json.Marshal(diagnosisRunRecord{Version: 1, TaskID: task, SavedAt: time.Now().UTC(), Run: state})
	if err != nil {
		return ErrFirstDiagnosisOutcome
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO exploration_nodes(exploration_id,kind,state,origin,payload) VALUES($1,'fact','confirmed',$2,$3::jsonb)`, exploration, diagnosisRunOrigin, string(payload)); err != nil {
		return ErrFirstDiagnosisOutcome
	}
	if tx.Commit() != nil {
		return ErrFirstDiagnosisOutcome
	}
	return nil
}

func diagnosisRunRequestTask(r *http.Request) (int64, bool) {
	if r.Method != http.MethodGet || r.URL.RawQuery != "" {
		return 0, false
	}
	tail, ok := strings.CutPrefix(r.URL.Path, "/api/tasks/")
	if !ok {
		return 0, false
	}
	raw, ok := strings.CutSuffix(tail, "/diagnosis-run")
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	return id, err == nil && id > 0 && strconv.FormatInt(id, 10) == raw
}

func (s *Server) diagnosisRun(w http.ResponseWriter, r *http.Request) {
	s.firstModelMu.RLock()
	var task int64
	if s.firstModel != nil {
		task = s.firstModel.TaskNumber
	}
	s.firstModelMu.RUnlock()
	s.serveDiagnosisRun(w, r, task)
}

func (s *Server) serveDiagnosisRun(w http.ResponseWriter, r *http.Request, boundTask int64) {
	// 일반 인증 미들웨어의 쿠키·query token fallback을 이 경로에서 허용하지 않는다.
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") || len(header) <= 7 {
		writeErr(w, 401, authErrUnauthorized)
		return
	}
	hash, err := s.reloadPassHash()
	if err != nil {
		writeErr(w, 503, authErrDataSourceUnavailable)
		return
	}
	claims := &jwt.RegisteredClaims{}
	token, err := jwt.ParseWithClaims(strings.TrimPrefix(header, "Bearer "), claims, func(t *jwt.Token) (any, error) { return s.signingKey(hash), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())
	if err != nil || !token.Valid || claims.Subject != "ARTEX" {
		writeErr(w, 401, authErrTokenInvalid)
		return
	}
	task, ok := diagnosisRunRequestTask(r)
	if !ok || task != boundTask || boundTask <= 0 {
		writeErr(w, 403, "선택한 작업의 실행 결과만 사용할 수 있습니다")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var raw []byte
	err = s.m.pg.QueryRowContext(ctx, `SELECT n.payload FROM tasks t JOIN exploration_nodes n ON n.exploration_id=t.exploration_id WHERE t.id=$1 AND t.deleted_at IS NULL AND n.kind='fact' AND n.state='confirmed' AND n.origin=$2 AND n.payload->>'task_id'=$3 ORDER BY n.id DESC LIMIT 1`, task, diagnosisRunOrigin, strconv.FormatInt(task, 10)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, 404, "저장된 실행 종료 결과가 없습니다")
		return
	}
	var result diagnosisRunRecord
	if err != nil || json.Unmarshal(raw, &result) != nil || result.Version != 1 || result.TaskID != task || !validDiagnosisRun(result.Run) || result.SavedAt.IsZero() {
		writeErr(w, 503, "저장된 실행 종료 결과를 확인할 수 없습니다")
		return
	}
	writeJSON(w, 200, result)
}
