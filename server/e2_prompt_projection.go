package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/e2prompt"
	"github.com/golang-jwt/jwt/v5"
)

const (
	e2PromptMaxAuthorizationBytes = 8192
	e2PromptMaxQueryBytes         = 256
	e2PromptQueryTimeout          = 5 * time.Second
	e2PromptMaxResponseBytes      = 8 << 20
)

// 설치 단일 관리자 인증을 사용한다. 쿼리 token·일반 Activity 상세 API를 사용하지 않는다.
func (s *Server) e2PromptProjection(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	auth := r.Header.Get("Authorization")
	if len(r.Header.Values("Authorization")) != 1 || len(auth) > e2PromptMaxAuthorizationBytes || !strings.HasPrefix(auth, "Bearer ") || !s.e2PromptAuthorized(strings.TrimPrefix(auth, "Bearer ")) {
		writeErr(w, 401, "인증이 필요합니다")
		return
	}
	query, queryErr := url.ParseQuery(r.URL.RawQuery)
	if queryErr != nil || len(r.URL.RawQuery) > e2PromptMaxQueryBytes || len(query) != 1 || len(query["run_ref"]) != 1 || !e2prompt.ValidRun(query.Get("run_ref")) || r.ContentLength != 0 {
		writeErr(w, 400, "평가 관측 요청 형식이 올바르지 않습니다")
		return
	}
	taskID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || taskID <= 0 || strconv.FormatInt(taskID, 10) != r.PathValue("id") {
		writeErr(w, 400, "작업 식별자가 올바르지 않습니다")
		return
	}
	task, ok := s.m.Task(r.PathValue("id"))
	if !ok || task.Store == nil || task.ExpID != task.Store.ID() {
		writeErr(w, 404, "작업을 확인할 수 없습니다")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), e2PromptQueryTimeout)
	defer cancel()
	projection, err := task.Store.E2PromptProjection(ctx, taskID, query.Get("run_ref"))
	if err != nil {
		writeErr(w, 503, "평가 관측을 확인할 수 없습니다")
		return
	}
	raw, err := json.Marshal(projection)
	if err != nil || len(raw) > e2PromptMaxResponseBytes {
		writeErr(w, 503, "평가 관측 범위를 확인할 수 없습니다")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

func (s *Server) e2PromptAuthorized(token string) bool {
	if !s.verifyToken(token) {
		return false
	}
	hash, err := s.cachedPassHash()
	if err != nil {
		return false
	}
	claims := &jwt.RegisteredClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(*jwt.Token) (any, error) { return s.signingKey(hash), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired(), jwt.WithSubject("ARTEX"))
	return err == nil && parsed.Valid
}

func promptProjectionRequestTask(r *http.Request) (int64, bool) {
	if r.Method != http.MethodGet || r.ContentLength != 0 || len(r.URL.RawQuery) > e2PromptMaxQueryBytes {
		return 0, false
	}
	tail, ok := strings.CutPrefix(r.URL.Path, "/api/tasks/")
	if !ok {
		return 0, false
	}
	parts := strings.Split(tail, "/")
	if len(parts) != 2 || parts[1] != "e2-prompt-projection" {
		return 0, false
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != parts[0] {
		return 0, false
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	return id, err == nil && len(query) == 1 && len(query["run_ref"]) == 1 && e2prompt.ValidRun(query.Get("run_ref"))
}
