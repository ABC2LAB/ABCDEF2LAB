package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/diagnosis"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/internal/pathscope"
	"github.com/Autumn-27/artex/report"
)

const (
	followupSuffix        = "/remediation-followup"
	followupTimeout       = 15 * time.Second
	maxFollowupQueryBytes = 8 << 10
	maxFollowupEditBytes  = 8 << 10
	maxObservedHTTPStatus = 599
)

type followupQuery struct {
	source, current, general int64
	entries                  map[string]bool
}

// 빈 선택은 전체다. 사용자가 명시한 잘못된 선택을 전체로 확대하지 않는다.
func parseFollowup(r *http.Request) (followupQuery, bool) {
	var q followupQuery
	id, ok := strings.CutPrefix(r.URL.Path, "/api/tasks/")
	id, suffix := strings.CutSuffix(id, followupSuffix)
	source, okID := verdictDetailID(id)
	q.source = source
	if !ok || !suffix || !okID || r.URL.RawPath != "" || len(r.URL.RawQuery) > maxFollowupQueryBytes {
		return q, false
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return q, false
	}
	q.entries = map[string]bool{}
	for k, v := range values {
		switch k {
		case "entry":
			for _, e := range v {
				kind, n, found := strings.Cut(e, "-")
				if _, valid := verdictDetailID(n); !found || !valid || (kind != "finding" && kind != "candidate" && kind != "verdict") || q.entries[e] {
					return q, false
				}
				q.entries[e] = true
			}
		case "current_task", "general_task":
			if len(v) != 1 {
				return q, false
			}
			n, valid := verdictDetailID(v[0])
			if !valid || n <= q.source {
				return q, false
			}
			if k == "current_task" {
				q.current = n
			} else {
				q.general = n
			}
		default:
			return q, false
		}
	}
	return q, true
}

type followupScope struct {
	State     string    `json:"state"`
	Paths     []string  `json:"paths"`
	Methods   []string  `json:"methods"`
	Requests  int64     `json:"request_budget"`
	ExpiresAt time.Time `json:"expires_at"`
}
type followupItem struct {
	Entry            string            `json:"entry_id"`
	Record           string            `json:"record_id"`
	Title            string            `json:"title"`
	Summary          string            `json:"original_observation"`
	Criteria         []string          `json:"criteria"`
	Digest           string            `json:"original_digest,omitempty"`
	EvidenceDigest   string            `json:"original_evidence_digest,omitempty"`
	RootFix          []string          `json:"requested_changes,omitempty"`
	OriginalRequests []followupRequest `json:"original_requests,omitempty"`
	Result           string            `json:"result"`
	Reason           string            `json:"reason,omitempty"`
	Review           *followupReview   `json:"review,omitempty"`
}
type followupRequest struct {
	Method    string `json:"method"`
	Path      string `json:"path"`
	VulnClass string `json:"vuln_class,omitempty"`
}

type followupEvidence struct {
	followupRequest
	ID         string `json:"step_id"`
	Digest     string `json:"binding_digest"`
	Number     int    `json:"number"`
	Status     int    `json:"http_status"`
	Reviewable bool   `json:"reviewable"`
	comparable bool
}
type followupFinding struct {
	Entry       string             `json:"entry_id"`
	Title       string             `json:"title"`
	Outcome     string             `json:"outcome"`
	Steps       []followupEvidence `json:"observations,omitempty"`
	SealWarning string             `json:"seal_warning,omitempty"`
}
type followupRun struct {
	Task           string            `json:"task_id,omitempty"`
	State          string            `json:"state"`
	Outcome        string            `json:"run_outcome,omitempty"`
	Started        bool              `json:"started"`
	Ready          bool              `json:"result_ready"`
	ResultState    string            `json:"results_state"`
	ExcludedPaths  *string           `json:"confirmed_excluded_paths"`
	SourceFailures int               `json:"unresolved_entries"`
	Findings       []followupFinding `json:"findings,omitempty"`
}
type followupDraft struct {
	Source string `json:"source_task"`
	Target string `json:"target_id"`
	URL    string `json:"target_url"`
	// nil과 빈 문자열을 구별해 '제외 없음'을 확인한 이전 회차를 보존한다.
	Exclusions *string             `json:"previous_excluded_paths"`
	Scope      followupScope       `json:"current_scope"`
	Items      []followupItem      `json:"items"`
	Current    followupRun         `json:"current"`
	General    followupRun         `json:"general"`
	Additional []followupReference `json:"additional"`
}

type followupReference struct {
	Task  string `json:"task_id"`
	Entry string `json:"entry_id"`
}

func followupOriginals(data report.DiagnosticReport, source *diagnosisprep.Job, reason string) []followupItem {
	items := []followupItem{}
	for _, e := range data.Entries {
		if e.RemediationArtifactID <= 0 {
			continue
		}
		item := followupItem{Entry: e.ID, Record: strconv.FormatInt(e.RemediationArtifactID, 10), Title: report.DiagnosticText(e.Title),
			Summary: report.DiagnosticText(e.Summary), Result: "inconclusive", Reason: reason}
		if e.RemediationBundle == nil || e.RemediationStale || e.SourceLoadFailed || e.Stale || e.Kind == "candidate" && e.Run != "run-"+source.ID {
			item.Reason = "original_evidence_unavailable"
		} else {
			item.Criteria = e.RemediationBundle.Verification
			item.RootFix = e.RemediationBundle.RootFix
			item.EvidenceDigest = e.RemediationBundle.EvidenceDigest
			item.Digest = e.RemediationBundle.InputDigest
			for _, step := range followupSteps(e) {
				if step.comparable && !slices.Contains(item.OriginalRequests, step.followupRequest) {
					item.OriginalRequests = append(item.OriginalRequests, step.followupRequest)
				}
			}
		}
		items = append(items, item)
	}
	return items
}

// 이미 저장된 후보의 HTTP 관찰과 검증된 재현 단계가 같은 호출·시도를 가리키는 경우만 사람이 연결할 수 있다.
func followupSteps(e report.DiagnosticEntry) []followupEvidence {
	if e.Kind != "candidate" || e.SourceLoadFailed || e.Stale || e.Truncated {
		return nil
	}
	type callEvidence struct {
		Trusted struct {
			Call e2proxy.CallResult `json:"call"`
		} `json:"trusted"`
	}
	var envelope struct {
		callEvidence
		Related []callEvidence `json:"related_calls"`
	}
	if json.Unmarshal([]byte(e.Evidence), &envelope) != nil {
		return nil
	}
	type key struct{ call, step string }
	observed := map[key]e2proxy.HTTPObservation{}
	for _, v := range append(envelope.Related, envelope.callEvidence) {
		call := v.Trusted.Call
		for _, o := range call.Observations {
			if o.Decision == "allowed" && o.ResponseComplete && o.ForwardAttempted && o.AuditComplete && !o.ResponseTruncated && o.StatusCode >= http.StatusOK && o.StatusCode <= maxObservedHTTPStatus {
				observed[key{call.CallRef, fmt.Sprintf("attempt-%d", o.Sequence)}] = o
			}
		}
	}
	class := strings.ToLower(strings.TrimSpace(e.VulnClass))
	if class == "unknown" || class != report.DiagnosticText(class) || strings.ContainsAny(class, "{}[]") {
		class = ""
	}
	var result []followupEvidence
	for _, s := range e.Steps {
		o, ok := observed[key{s.CallRef, s.SourceStepID}]
		if !ok || s.Truncated || s.Method == "" || s.Method != o.Method {
			continue
		}
		displayPath := report.DiagnosticText(s.Path)
		_, pathOK := pathscope.Parse(s.Path)
		// 접두 뒤 경로나 보호된 조각을 가린 표시값은 서로 다른 끝점을 가리킬 수 있다.
		comparable := class != "" && pathOK && strings.HasPrefix(s.Path, "/") &&
			!strings.ContainsAny(s.Path, "{}[]*;") && displayPath == s.Path && s.Path == o.Endpoint
		result = append(result, followupEvidence{ID: s.ID, Digest: s.BindingDigest, Number: s.Number, Status: o.StatusCode,
			followupRequest: followupRequest{Method: s.Method, Path: displayPath, VulnClass: class}, comparable: comparable})
	}
	return result
}

func followupReport(ctx context.Context, pool *sql.DB, job *diagnosisprep.Job, endpoint diagnosisprep.Endpoint, owner string) (report.DiagnosticReport, error) {
	id, _ := verdictDetailID(job.TaskID)
	bind := diagnosticProductBinding{taskID: job.TaskID, jobID: job.ID, reader: productPolicySourceReader{endpoint: endpoint, principal: owner, jobID: job.ID}}
	ctx = context.WithValue(ctx, diagnosticProductContextKey{}, bind)
	return loadDiagnosticSnapshot(ctx, pool, id, func(tx *sql.Tx) (report.DiagnosticReport, error) {
		return readDiagnosticEntries(ctx, tx, id, time.Now(), false, true)
	})
}

func followupObservations(job *diagnosisprep.Job, data report.DiagnosticReport, scope followupScope) followupRun {
	if job == nil {
		return followupRun{State: "not_linked", ResultState: "not_linked"}
	}
	run := followupRun{Task: job.TaskID, State: string(job.State), Outcome: job.Outcome, Started: job.Started, Ready: job.ResultReady}
	if job.Input.ExclusionsReviewed {
		run.ExcludedPaths = &job.Input.ExcludedPaths
	}
	if !job.ResultReady {
		switch job.State {
		case diagnosisprep.Failed, diagnosisprep.Cancelled, diagnosisprep.Unknown, diagnosisprep.Completed:
			run.ResultState = "unavailable"
		default:
			run.ResultState = "pending"
		}
		return run
	}
	run.SourceFailures = data.SourceFailures + data.ProductUnreadable + data.ProductUnverified + data.ProductSourceUnavailable + data.ProductUndecidable + data.ProductSuspected
	for _, e := range data.Entries {
		if e.Kind == "candidate" && e.Run != "run-"+job.ID && !e.SourceLoadFailed {
			run.SourceFailures++
			continue
		}
		if !e.SourceLoadFailed && (e.Stale || e.Outcome == "unknown" || e.Outcome == "not_executed") {
			run.SourceFailures++
		}
		if e.SourceLoadFailed || e.Stale || (e.Outcome != "confirmed" && e.Outcome != "review") {
			continue
		}
		steps := followupSteps(e)
		for i := range steps {
			steps[i].Reviewable = canReviewObservation(steps[i], job, scope)
		}
		run.Findings = append(run.Findings, followupFinding{Entry: e.ID, Title: report.DiagnosticText(e.Title), Outcome: e.Outcome, Steps: steps})
	}
	for _, p := range data.ProductConfirmed {
		run.Findings = append(run.Findings, followupFinding{Entry: "product-verdict-" + p.VerdictID, Title: report.DiagnosticText(p.ResourceType), Outcome: "confirmed", SealWarning: p.SealUnconfirmedReason})
	}
	switch {
	case !job.Started || job.Outcome == diagnosis.OutcomeNoIntents:
		run.ResultState = "not_checked"
	case job.State == diagnosisprep.Completed && job.Outcome == diagnosis.OutcomeComplete && run.SourceFailures == 0:
		run.ResultState = "available"
	default:
		run.ResultState = "partial"
	}
	return run
}

// 진단 당시 허용된 요청도 현재 승인에서 빠질 수 있다. 정확 일치 경로는 당시 query 없는
// 경로로 승인된 경우에만 확인한다. 관찰에는 query가 보존되지 않는다.
func canReviewObservation(step followupEvidence, job *diagnosisprep.Job, scope followupScope) bool {
	if !step.comparable || job.Scope == nil || !job.Started || job.State != diagnosisprep.Completed || (scope.State != "review_required" && scope.State != "changed") {
		return false
	}
	if !strings.HasPrefix(step.Path, "/") || !slices.Contains(scope.Methods, step.Method) || !slices.Contains(job.Scope.Methods, step.Method) {
		return false
	}
	if !slices.ContainsFunc(job.Scope.PathPrefixes, func(p string) bool { return pathscope.Allows(step.Path, false, p) }) {
		return false
	}
	if job.Input.ExcludedPaths != "" && pathscope.ExcludedAny(step.Path, strings.Split(job.Input.ExcludedPaths, "\n")) {
		return false
	}
	for _, p := range scope.Paths {
		if pathscope.Allows(step.Path, true, p) || p == "="+step.Path && slices.Contains(job.Scope.PathPrefixes, p) {
			return true
		}
	}
	return false
}

func (b *diagnosisManagementBridge) followupScope(ctx context.Context, owner string, source *diagnosisprep.Job) (followupScope, error) {
	s := source.Scope
	req := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "followup-target", Principal: owner, Command: diagnosisprep.ProductControl, Product: &diagnosisprep.ProductRequest{Operation: "target", TargetID: s.TargetRef}}
	resp, err := b.candidateEndpoint.Call(ctx, req)
	if err != nil || diagnosisprep.ValidateResponse(req, resp) != nil || resp.Error != "" || resp.Product == nil {
		return followupScope{}, diagnosisprep.ErrUnavailable
	}
	t := resp.Product.Target
	if resp.Product.Reason == "target_not_found" {
		return followupScope{State: "unavailable"}, nil
	}
	if t == nil || t.ID != s.TargetRef || t.Origin != s.Origin {
		return followupScope{}, diagnosisprep.ErrUnavailable
	}
	state := t.State
	if state == "approved" {
		switch {
		case !time.Now().Before(t.ExpiresAt):
			state = "expired"
		case s.ScopeRef != "scope-"+t.ID || !slices.Equal(s.PathPrefixes, t.PathPrefixes) || s.Budget.Requests != t.Requests || s.ExpiresAt.After(t.ExpiresAt) || slices.ContainsFunc(s.Methods, func(m string) bool { return !slices.Contains(t.Methods, m) }):
			state = "changed"
		default:
			state = "review_required"
		}
	}
	return followupScope{State: state, Paths: t.PathPrefixes, Methods: t.Methods, Requests: t.Requests, ExpiresAt: t.ExpiresAt}, nil
}

// 원문 오류에는 쿼리·경로 등이 섞일 수 있으므로 고정된 단계와 사유만 기록한다.
func logFollowupFailure(stage string, err error) {
	reason := "unavailable"
	var coded interface{ SQLState() string }
	if errors.As(err, &coded) {
		switch coded.SQLState() {
		case "42P01":
			reason = "table_missing"
		case "42501":
			reason = "permission_denied"
		}
	}
	log.Printf("[이행점검] 처리 실패 단계=%s 사유=%s", stage, reason)
}

func followupError(w http.ResponseWriter, code int) {
	writeErr(w, code, map[int]string{400: "이행점검 입력을 확인하세요.", 401: "인증이 필요합니다.", 403: "선택한 보고서의 조회 권한을 확인할 수 없습니다.", 404: "선택한 조치 기록이 없습니다.", 405: "지원하지 않는 이행점검 요청입니다.", 409: "보고서의 대상·버전·근거가 일치하지 않습니다. 다시 조회하세요.", 422: "저장된 보고서의 크기나 근거를 확인하세요.", 503: "이행점검 기록을 조회할 수 없습니다."}[code])
}

func (b *diagnosisManagementBridge) serveRemediationFollowup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		w.Header().Set("Allow", "GET, PUT")
		followupError(w, 405)
		return
	}
	q, ok := parseFollowup(r)
	if !ok || r.Method == http.MethodGet && (r.ContentLength != 0 || len(r.TransferEncoding) > 0) {
		followupError(w, 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), followupTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	owner, err := b.authenticate(r)
	if errors.Is(err, errManagementAuthenticationUnavailable) {
		followupError(w, 503)
		return
	}
	if err != nil || owner == "" {
		followupError(w, 401)
		return
	}
	if b.readPool == nil || b.candidateJob == nil || b.candidateEndpoint == nil {
		followupError(w, 503)
		return
	}
	var edit *followupEdit
	if r.Method == http.MethodPut {
		edit = readFollowupEdit(w, r)
		if edit == nil || q.current == 0 || q.general != 0 || len(q.entries) != 0 {
			followupError(w, 400)
			return
		}
	}
	jobs := map[int64]*diagnosisprep.Job{}
	for _, id := range []int64{q.source, q.current, q.general} {
		if id == 0 || jobs[id] != nil {
			continue
		}
		ref := strconv.FormatInt(id, 10)
		j, err := b.candidateJob(ctx, owner, ref)
		if err != nil {
			logFollowupFailure("job_authorize", err)
			followupError(w, http.StatusServiceUnavailable)
			return
		}
		if j == nil {
			followupError(w, 403)
			return
		}
		if j.TaskID != ref || j.ID == "" || j.Scope == nil || j.Scope.TargetRef == "" {
			followupError(w, 409)
			return
		}
		if original := jobs[q.source]; original != nil && (j.Scope.TargetRef != original.Scope.TargetRef || j.Scope.Origin != original.Scope.Origin) {
			followupError(w, 409)
			return
		}
		jobs[id] = j
	}
	source := jobs[q.source]
	if !source.ResultReady {
		followupError(w, 409)
		return
	}
	scope, err := b.followupScope(ctx, owner, source)
	if err != nil {
		logFollowupFailure("target_approval", err)
		followupError(w, http.StatusServiceUnavailable)
		return
	}
	reports := map[int64]report.DiagnosticReport{}
	for id, j := range jobs {
		if !j.ResultReady {
			continue
		}
		data, err := followupReport(ctx, b.readPool, j, b.candidateEndpoint, owner)
		if err != nil {
			code := diagnosticReadFailureCode(err)
			if code == http.StatusServiceUnavailable {
				logFollowupFailure("report_read", err)
			}
			followupError(w, code)
			return
		}
		reports[id] = data
	}
	page := followupDraft{Source: source.TaskID, Target: source.Scope.TargetRef, URL: source.Input.TargetURL, Scope: scope,
		Current: followupObservations(jobs[q.current], reports[q.current], scope), General: followupObservations(jobs[q.general], reports[q.general], scope)}
	page.Items = followupOriginals(reports[q.source], source, page.unreviewedReason())
	if source.Input.ExclusionsReviewed {
		page.Exclusions = &source.Input.ExcludedPaths
	}
	if q.current != 0 {
		if edit != nil {
			if code := saveFollowupReview(ctx, b.readPool, q, page, owner, *edit); code != http.StatusOK {
				followupError(w, code)
				return
			}
		}
		if err := applyFollowupReviews(ctx, b.readPool, q, &page); err != nil {
			logFollowupFailure("review_read", err)
			followupError(w, http.StatusServiceUnavailable)
			return
		}
	}
	page.partition(q.entries)
	if len(q.entries) > 0 && len(q.entries) != len(page.Items) {
		followupError(w, 404)
		return
	}
	writeRemediationJSON(w, page)
}

func (p followupDraft) unreviewedReason() string {
	if p.Scope.State != "review_required" && p.Scope.State != "changed" {
		return "scope_" + p.Scope.State
	}
	switch p.Current.ResultState {
	case "not_linked", "not_checked":
		return "not_checked"
	case "pending":
		return "result_pending"
	case "unavailable":
		return "result_unavailable"
	}
	bindings := p.Current.bindings()
	for _, step := range bindings {
		if step.Reviewable {
			return "no_linked_review"
		}
	}
	if p.Scope.State == "changed" || len(bindings) > 0 {
		return "outside_scope"
	}
	return "insufficient_evidence"
}
