package server

import (
	"context"
	"errors"
	"github.com/Autumn-27/artex/db"
	"github.com/golang-jwt/jwt/v5"
	"net/http"
	"strings"
	"time"
)

// 기존 requireAuth 내부의 추가 검사다. 쿠키·query 자격은 승인 API에서 받지 않는다.
func (s *Server) requestApprovalAccess(w http.ResponseWriter, r *http.Request, decision bool) (int64, string, bool) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") || len(header) <= 7 {
		writeErr(w, 401, authErrUnauthorized)
		return 0, "", false
	}
	hash, err := s.reloadPassHash()
	if err != nil {
		writeErr(w, 503, authErrDataSourceUnavailable)
		return 0, "", false
	}
	claims := &jwt.RegisteredClaims{}
	token, err := jwt.ParseWithClaims(strings.TrimPrefix(header, "Bearer "), claims, func(t *jwt.Token) (any, error) { return s.signingKey(hash), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())
	if err != nil || !token.Valid || claims.Subject != "ARTEX" {
		writeErr(w, 401, authErrTokenInvalid)
		return 0, "", false
	}
	id, ok := pathInt(r, "id")
	if !ok || id <= 0 {
		writeErr(w, 400, "작업 ID가 올바르지 않습니다")
		return 0, "", false
	}
	if s.requestApprovals == nil || s.m == nil || s.m.pg == nil {
		writeErr(w, 503, "요청별 승인 처리기가 연결되지 않았습니다")
		return 0, "", false
	}
	row, err := s.m.pg.GetTask(id)
	if err != nil || row == nil {
		writeErr(w, 403, "이 작업의 승인에 접근할 수 없습니다")
		return 0, "", false
	}
	s.requestApprovals.mu.Lock()
	binding, exists := s.requestApprovals.bindings[id]
	s.requestApprovals.mu.Unlock()
	if !exists || decision && (binding.stopRequested || binding.phase == "closed" || binding.phase == "failed" || binding.ctx.Err() != nil || db.IsTerminal(row.Status) || row.CompletedAt != nil) {
		writeErr(w, 403, "이 작업의 승인 권한이 없습니다")
		return 0, "", false
	}
	return id, claims.Subject, true
}
func (s *Server) requestApprovalList(w http.ResponseWriter, r *http.Request) {
	task, owner, ok := s.requestApprovalAccess(w, r, false)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, err := s.requestApprovals.list(ctx, task, owner)
	if err != nil {
		writeErr(w, 503, "승인 기록을 읽을 수 없습니다")
		return
	}
	writeJSON(w, 200, map[string]any{"approvals": rows, "run": s.requestApprovals.runState(task)})
}
func (s *Server) requestApprovalGet(w http.ResponseWriter, r *http.Request) {
	task, owner, ok := s.requestApprovalAccess(w, r, false)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	row, err := s.requestApprovals.get(ctx, task, owner, r.PathValue("approvalID"))
	if err != nil {
		writeErr(w, 404, "승인 기록을 찾을 수 없습니다")
		return
	}
	writeJSON(w, 200, row)
}
func (s *Server) requestApprovalDecide(w http.ResponseWriter, r *http.Request) {
	task, owner, ok := s.requestApprovalAccess(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Decision string `json:"decision"`
		SHA256   string `json:"request_sha256"`
	}
	if decode(r, &input) != nil || input.Decision != "allow" && input.Decision != "deny" || len(input.SHA256) != 64 {
		writeErr(w, 400, "승인 결정 형식이 올바르지 않습니다")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	row, err := s.requestApprovals.decide(ctx, task, owner, r.PathValue("approvalID"), input.SHA256, input.Decision)
	if errors.Is(err, db.ErrRequestApprovalConflict) {
		writeErr(w, 409, "이미 결정되었거나 만료·취소된 요청입니다")
		return
	}
	if err != nil {
		writeErr(w, 503, "승인 결정을 저장할 수 없습니다")
		return
	}
	writeJSON(w, 200, row)
}

// 정확한 승인 경로만 추가 허용하며 외곽 JWT 및 담당 task 검사는 유지한다.
func requestApprovalRoute(r *http.Request) bool {
	tail, ok := strings.CutPrefix(r.URL.Path, "/api/tasks/")
	if !ok {
		return false
	}
	parts := strings.Split(tail, "/")
	if len(parts) < 2 || !numericID.MatchString(parts[0]) || parts[1] != "request-approvals" {
		return false
	}
	if r.Method == http.MethodPost && len(parts) == 3 && parts[2] == "stop" {
		return true
	}
	if r.Method == http.MethodGet {
		return len(parts) == 2 || len(parts) == 3 && parts[2] != ""
	}
	return r.Method == http.MethodPost && len(parts) == 4 && parts[2] != "" && parts[3] == "decision"
}

// 현재 task에 결속된 E② 실행의 신규 송신만 중지한다. 재개 API는 열지 않는다.
func (s *Server) requestApprovalStop(w http.ResponseWriter, r *http.Request) {
	task, owner, ok := s.requestApprovalAccess(w, r, false)
	if !ok {
		return
	}
	if !s.requestApprovals.userStopped(task) {
		if _, _, allowed := s.requestApprovalAccess(w, r, true); !allowed {
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.requestApprovals.stop(ctx, task, owner); err != nil {
		if errors.Is(err, db.ErrRequestApprovalConflict) {
			writeErr(w, 409, "이미 종료된 실행은 중지할 수 없습니다")
			return
		}
		writeErr(w, 503, "실행 중지 기록을 확인할 수 없습니다")
		return
	}
	writeJSON(w, 200, map[string]any{"stopped": true, "run": s.requestApprovals.runState(task)})
}
