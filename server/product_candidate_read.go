package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisprep"
)

const productCandidateSuffix = "/diagnostic-candidates"
const productCandidateProof = "X-ABC2LAB-Candidate-Read-Scope"
const productCandidateBytes = 64 << 10

// ProductCandidatePath는 mux의 경로 정리 전에 전용 조회 경로를 분기한다.
func ProductCandidatePath(r *http.Request) bool {
	for part := range strings.SplitSeq(r.URL.Path, "/") {
		if part == "diagnostic-candidates" {
			return true
		}
	}
	return false
}

func ProductCandidateRequestTask(r *http.Request) (int64, bool) {
	if r.Method != http.MethodGet || r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" ||
		r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		return 0, false
	}
	tail, ok := strings.CutPrefix(r.URL.Path, "/api/tasks/")
	id, pathOK := strings.CutSuffix(tail, productCandidateSuffix)
	task, err := strconv.ParseInt(id, 10, 64)
	return task, ok && pathOK && err == nil && task > 0 && strconv.FormatInt(task, 10) == id
}

func ProductCandidateJobActive(job *diagnosisprep.Job) bool {
	return job.Scope != nil && job.Started && !job.DrainComplete && !job.ResultReady &&
		(job.State == diagnosisprep.Running || job.State == diagnosisprep.Stopping)
}

type productCandidateItem struct {
	ID                string    `json:"candidate_id"`
	State             string    `json:"state"`
	ObservedAt        time.Time `json:"observed_at"`
	SuggestedSeverity string    `json:"suggested_severity,omitempty"`
}

type productCandidatePage struct {
	TaskID string                 `json:"task_id"`
	Items  []productCandidateItem `json:"items"`
	// 포인터로 전송 필드 누락과 실제 0을 구별한다.
	Returned  *int  `json:"returned"`
	Total     *int  `json:"candidate_total"`
	Truncated *bool `json:"truncated"`
}

func (p productCandidatePage) valid(task string) bool {
	if p.TaskID != task || p.Items == nil || p.Returned == nil || *p.Returned != len(p.Items) ||
		len(p.Items) > db.LiveDiagnosticCandidateLimit || p.Truncated == nil {
		return false
	}
	if *p.Truncated {
		if p.Total != nil || len(p.Items) != db.LiveDiagnosticCandidateLimit {
			return false
		}
	} else if p.Total == nil || *p.Total != len(p.Items) {
		return false
	}
	var previous int64
	for i, item := range p.Items {
		id, err := strconv.ParseInt(item.ID, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != item.ID || i > 0 && id >= previous ||
			item.State != "manual_review_required" || item.ObservedAt.IsZero() {
			return false
		}
		switch item.SuggestedSeverity {
		case "", "critical", "high", "medium", "low", "info":
		default:
			return false
		}
		previous = id
	}
	return true
}

func productCandidateError(w http.ResponseWriter, status int) {
	code := map[int]string{
		400: "invalid_candidate_request", 401: "authentication_required", 403: "candidate_scope_denied",
		404: "task_not_found", 409: "candidate_run_not_active", 503: "candidate_read_unavailable",
	}[status]
	if code == "" {
		status, code = 502, "candidate_response_invalid"
	}
	writeJSON(w, status, map[string]string{"error": code})
}

func candidateExploration(ctx context.Context, q reportReadQuery, task int64) (int64, int) {
	var exploration int64
	err := q.QueryRowContext(ctx, `SELECT exploration_id FROM tasks WHERE id=$1 AND deleted_at IS NULL`, task).Scan(&exploration)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, http.StatusNotFound
	}
	if err != nil || exploration <= 0 {
		return 0, http.StatusServiceUnavailable
	}
	return exploration, 0
}

func candidateProof(job *diagnosisprep.Job, exp int64, targetRevision uint64) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\n%s\n%s\n%d\n%s\n%d", job.ID, job.TaskID, job.SourceDigest, exp, job.Scope.TargetRef, targetRevision)))
	return hex.EncodeToString(digest[:])
}

func (b *diagnosisManagementBridge) liveCandidateBinding(r *http.Request, task int64) (string, int) {
	owner, err := b.authenticate(r)
	if errors.Is(err, errManagementAuthenticationUnavailable) {
		return "", 503
	}
	if err != nil || owner == "" {
		return "", 401
	}
	if b.candidateJob == nil || b.candidateEndpoint == nil || b.readPool == nil {
		return "", 503
	}
	job, err := b.candidateJob(r.Context(), owner, strconv.FormatInt(task, 10))
	if err != nil {
		return "", 503
	}
	if job == nil {
		return "", 403
	}
	exp, status := candidateExploration(r.Context(), b.readPool, task)
	if status != 0 {
		return "", status
	}
	if !ProductCandidateJobActive(job) {
		return "", 409
	}
	// 로컬 준비 서비스의 등록 기록을 읽으며 대상 연결을 다시 검사하지 않는다.
	req := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "candidate-scope", Principal: owner, Command: diagnosisprep.ProductControl,
		Product: &diagnosisprep.ProductRequest{Operation: "target", TargetID: job.Scope.TargetRef}}
	response, err := b.candidateEndpoint.Call(r.Context(), req)
	if err != nil || diagnosisprep.ValidateResponse(req, response) != nil {
		return "", 503
	}
	if response.Error == "not_found" || response.Error == "forbidden" {
		return "", 403
	}
	if response.Error != "" || response.Product == nil {
		return "", 503
	}
	if response.Product.Reason == "target_not_found" {
		return "", 403
	}
	if response.Product.Reason != "" || response.Product.Target == nil {
		return "", 503
	}
	target := response.Product.Target
	now := time.Now()
	if target.ID != job.Scope.TargetRef || target.Origin != job.Scope.Origin || target.Revision == 0 ||
		target.State != "approved" || !now.Before(target.ExpiresAt) || !now.Before(job.Scope.ExpiresAt) {
		return "", 403
	}
	return candidateProof(job, exp, target.Revision), 0
}

func (b *diagnosisManagementBridge) serveProductCandidates(w http.ResponseWriter, r *http.Request) {
	task, ok := ProductCandidateRequestTask(r)
	if !ok {
		productCandidateError(w, 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	proof, status := b.liveCandidateBinding(r, task)
	if status != 0 {
		productCandidateError(w, status)
		return
	}
	target := *b.origin
	target.Path = r.URL.Path
	out, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		productCandidateError(w, 503)
		return
	}
	out.Header.Set("Authorization", r.Header.Get("Authorization"))
	out.Header.Set(productCandidateProof, proof)
	response, err := b.client.Do(out)
	if err != nil {
		productCandidateError(w, 503)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		productCandidateError(w, response.StatusCode)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, productCandidateBytes+1))
	var page productCandidatePage
	headers := response.Header.Values(productCandidateProof)
	if err != nil || len(raw) > productCandidateBytes || len(headers) != 1 || headers[0] != proof ||
		strictJSON(raw, &page) != nil || !page.valid(strconv.FormatInt(task, 10)) {
		productCandidateError(w, 502)
		return
	}
	current, status := b.liveCandidateBinding(r, task)
	if status != 0 {
		productCandidateError(w, status)
		return
	}
	if current != proof {
		productCandidateError(w, 403)
		return
	}
	for i := range page.Items {
		page.Items[i].ObservedAt = page.Items[i].ObservedAt.UTC()
	}
	writeJSON(w, 200, page)
}

// ProductCandidateHandler는 이미 시작한 URL 제품 실행에 직접 결속된 내부 조회다.
func (r *FirstDiagnosisHTTPRuntime) ProductCandidateHandler(job *diagnosisprep.Job, targetRevision uint64, authorize func(context.Context) int) http.Handler {
	task, exp := r.host.ExpectedTask()
	return r.server.productCandidateHandler(task, exp, r.host.runtime.options.Expected.RunRef, job, targetRevision, func(ctx context.Context) int {
		if r.admissionStopped.Load() {
			return 409
		}
		return authorize(ctx)
	})
}

func (s *Server) productCandidateHandler(boundTask string, exp int64, run string, job *diagnosisprep.Job, targetRevision uint64, authorize func(context.Context) int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		task, ok := ProductCandidateRequestTask(r)
		if !ok {
			productCandidateError(w, 400)
			return
		}
		expected := candidateProof(job, exp, targetRevision)
		proof := r.Header.Values(productCandidateProof)
		if boundTask != job.TaskID || boundTask != strconv.FormatInt(task, 10) || len(proof) != 1 || proof[0] != expected {
			productCandidateError(w, 403)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		if !s.authenticateReportRead(w, r) {
			return
		}
		pool := s.m.pg.DB
		page, status := loadLiveProductCandidates(ctx, pool, task, exp, run)
		if status != 0 {
			productCandidateError(w, status)
			return
		}
		if !s.authenticateReportRead(w, r) {
			return
		}
		current, status := candidateExploration(ctx, pool, task)
		if status != 0 {
			productCandidateError(w, status)
			return
		}
		if current != exp {
			productCandidateError(w, 403)
			return
		}
		if status := authorize(ctx); status != 0 {
			productCandidateError(w, status)
			return
		}
		w.Header().Set(productCandidateProof, expected)
		writeJSON(w, 200, page)
	})
}

func loadLiveProductCandidates(ctx context.Context, pool *sql.DB, task, exp int64, run string) (productCandidatePage, int) {
	page := productCandidatePage{TaskID: strconv.FormatInt(task, 10), Items: []productCandidateItem{}}
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return page, 503
	}
	defer tx.Rollback()
	current, status := candidateExploration(ctx, tx, task)
	if status != 0 {
		return page, status
	}
	if current != exp {
		return page, 403
	}
	rows, err := db.ListLiveDiagnosticCandidates(ctx, tx, exp, task, run)
	if err != nil || tx.Commit() != nil {
		return page, 503
	}
	truncated := len(rows) > db.LiveDiagnosticCandidateLimit
	if truncated {
		rows = rows[:db.LiveDiagnosticCandidateLimit]
	}
	for _, row := range rows {
		page.Items = append(page.Items, productCandidateItem{
			ID: strconv.FormatInt(row.ID, 10), State: row.Verification,
			ObservedAt: row.ObservedAt.UTC(), SuggestedSeverity: row.SuggestedSeverity,
		})
	}
	returned := len(page.Items)
	page.Returned, page.Truncated = &returned, &truncated
	if !truncated {
		page.Total = &returned
	}
	return page, 0
}
