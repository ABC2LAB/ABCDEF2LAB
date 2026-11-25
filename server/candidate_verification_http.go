package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisapi"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/report"
)

const candidateVerificationPrefix = "/api/product/tasks/"
const candidateVerificationHistoryLimit = 100

var candidateVerificationKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{7,127}$`)
var candidateVerificationDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)
var candidateVerificationReason = regexp.MustCompile(`^[a-z][a-z0-9_]{0,127}$`)

func validStoredAutomaticCandidateAssessment(candidate db.DiagnosticCandidate, assessment report.CandidateAssessment) bool {
	if !candidateVerificationReason.MatchString(assessment.Reason) || assessment.EvidenceDigest != candidate.EvidenceDigest {
		return false
	}
	switch assessment.Status {
	case candidateverify.Confirmed, candidateverify.Negative, candidateverify.Insufficient, candidateverify.Unsupported:
	default:
		return false
	}
	return candidateverify.ValidateStoredV2(candidate, candidateverify.Result{Status: assessment.Status, Reason: assessment.Reason, Attempts: assessment.AttemptRefs, Sources: assessment.Sources})
}

type candidateVerificationRoute struct{ task, candidate, before int64 }

func parseCandidateVerificationRoute(r *http.Request) (candidateVerificationRoute, bool) {
	if r.URL.RawPath != "" || r.URL.ForceQuery || !strings.HasPrefix(r.URL.Path, candidateVerificationPrefix) {
		return candidateVerificationRoute{}, false
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, candidateVerificationPrefix), "/")
	if len(parts) != 4 || parts[1] != "candidates" || parts[3] != "verifications" {
		return candidateVerificationRoute{}, false
	}
	task, e1 := strconv.ParseInt(parts[0], 10, 64)
	candidate, e2 := strconv.ParseInt(parts[2], 10, 64)
	if e1 != nil || e2 != nil || task <= 0 || candidate <= 0 ||
		strconv.FormatInt(task, 10) != parts[0] || strconv.FormatInt(candidate, 10) != parts[2] {
		return candidateVerificationRoute{}, false
	}
	var before int64
	if r.URL.RawQuery != "" {
		query, queryErr := url.ParseQuery(r.URL.RawQuery)
		if queryErr != nil || r.Method != http.MethodGet || len(query) != 1 || len(query["before"]) != 1 {
			return candidateVerificationRoute{}, false
		}
		value := query.Get("before")
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != value {
			return candidateVerificationRoute{}, false
		}
		before = parsed
	}
	return candidateVerificationRoute{task: task, candidate: candidate, before: before}, true
}

type candidateVerificationAutomatic struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type candidateVerificationEvent struct {
	State         string          `json:"process_state"`
	Verdict       *string         `json:"verdict"`
	Reason        string          `json:"reason"`
	RuleVersion   string          `json:"rule_version"`
	Sources       json.RawMessage `json:"sources"`
	SourcesDigest string          `json:"sources_digest"`
	ExecutionRef  string          `json:"execution_ref,omitempty"`
	ApprovalRef   string          `json:"approval_ref,omitempty"`
	At            time.Time       `json:"at"`
}

// 후속 이력에는 이미 종료 기록에 있는 관찰 본문 projection을 복제하지 않는다.
// 원본 종료 기록을 다시 대조할 수 있는 비민감 참조와 digest만 보존한다.
type candidateVerificationSourceRef struct {
	CallRef           string `json:"call_ref"`
	AttemptRef        string `json:"attempt_ref"`
	ObservationDigest string `json:"observation_digest"`
}

func candidateVerificationSourceRefs(sources []candidateverify.SourceV2) []candidateVerificationSourceRef {
	out := make([]candidateVerificationSourceRef, 0, len(sources))
	for _, source := range sources {
		out = append(out, candidateVerificationSourceRef{CallRef: source.CallRef, AttemptRef: source.Observation.AttemptRef, ObservationDigest: source.Digest})
	}
	return out
}

type candidateVerificationAttempt struct {
	ID              string                     `json:"id"`
	CandidateID     string                     `json:"candidate_id"`
	RequestKey      string                     `json:"request_id"`
	Method          string                     `json:"method"`
	OriginalDigest  string                     `json:"original_evidence_digest"`
	OriginalVersion int64                      `json:"original_evidence_version"`
	OriginalRunRef  string                     `json:"original_run_ref"`
	RequestedAt     time.Time                  `json:"requested_at"`
	Latest          candidateVerificationEvent `json:"latest"`
}

type candidateVerificationPage struct {
	TaskID         string                         `json:"task_id"`
	CandidateID    string                         `json:"candidate_id"`
	EvidenceDigest string                         `json:"evidence_digest"`
	Automatic      candidateVerificationAutomatic `json:"automatic"`
	StoredEvidence candidateVerificationStored    `json:"stored_evidence"`
	Verifications  []candidateVerificationAttempt `json:"verifications"`
	Truncated      bool                           `json:"truncated"`
	NextBefore     string                         `json:"next_before,omitempty"`
}

type candidateVerificationStored struct {
	State   string `json:"state"`
	Count   int    `json:"observation_count"`
	Verdict string `json:"verdict,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

func candidateVerificationError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

// 후보별 저장 관찰 재평가는 제품 관리 API에서만 받는다. 대상 HTTP와 모델을 호출하지 않는다.
func candidateVerificationHTTP(w http.ResponseWriter, r *http.Request, pool *sql.DB, authenticate diagnosisapi.Authenticate, endpoint diagnosisprep.Endpoint) {
	route, ok := parseCandidateVerificationRoute(r)
	if !ok || (r.Method != http.MethodGet && r.Method != http.MethodPost) {
		candidateVerificationError(w, 400, "invalid_verification_request")
		return
	}
	if r.Method == http.MethodGet && (r.ContentLength != 0 || len(r.TransferEncoding) != 0) {
		candidateVerificationError(w, 400, "invalid_verification_request")
		return
	}
	owner, err := authenticate(r)
	if err != nil || owner == "" {
		candidateVerificationError(w, 401, "authentication_required")
		return
	}
	if pool == nil || endpoint == nil {
		candidateVerificationError(w, 503, "verification_storage_unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	request := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "candidate-verification-owner", Command: diagnosisprep.AuthorizeTask, Principal: owner, TaskID: strconv.FormatInt(route.task, 10)}
	response, err := endpoint.Call(ctx, request)
	if err != nil || diagnosisprep.ValidateResponse(request, response) != nil {
		candidateVerificationError(w, 503, "verification_owner_unavailable")
		return
	}
	if response.Authorized == nil || !*response.Authorized || response.Job == nil || response.Job.TaskID != request.TaskID {
		candidateVerificationError(w, 403, "verification_scope_denied")
		return
	}
	var input struct {
		RequestID      string `json:"request_id"`
		EvidenceDigest string `json:"evidence_digest"`
	}
	if r.Method == http.MethodPost {
		media, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if mediaErr != nil || media != "application/json" {
			candidateVerificationError(w, 400, "invalid_verification_request")
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
		if err != nil || strictJSON(raw, &input) != nil || !candidateVerificationKey.MatchString(input.RequestID) || !candidateVerificationDigest.MatchString(input.EvidenceDigest) {
			candidateVerificationError(w, 400, "invalid_verification_request")
			return
		}
		current, err := authenticate(r)
		if err != nil || current != owner {
			candidateVerificationError(w, 401, "authentication_required")
			return
		}
	}
	isolation := sql.LevelRepeatableRead
	if r.Method == http.MethodPost {
		// 같은 task의 FOR UPDATE 뒤 새 snapshot에서 선행 요청을 봐야 한다.
		isolation = sql.LevelReadCommitted
	}
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{Isolation: isolation, ReadOnly: r.Method == http.MethodGet})
	if err != nil {
		candidateVerificationError(w, 503, "verification_storage_unavailable")
		return
	}
	defer tx.Rollback()
	var exploration int64
	query := `SELECT exploration_id FROM tasks WHERE id=$1 AND deleted_at IS NULL`
	if r.Method == http.MethodPost {
		query += ` FOR UPDATE`
	}
	err = tx.QueryRowContext(ctx, query, route.task).Scan(&exploration)
	if errors.Is(err, sql.ErrNoRows) {
		candidateVerificationError(w, 404, "task_not_found")
		return
	}
	if err != nil || exploration <= 0 {
		candidateVerificationError(w, 503, "verification_storage_unavailable")
		return
	}
	candidate, found, err := db.ReadDiagnosticCandidateWithFence(ctx, tx, exploration, route.task, route.candidate)
	if err != nil {
		candidateVerificationError(w, 503, "candidate_integrity_failed")
		return
	}
	if !found {
		candidateVerificationError(w, 404, "candidate_not_found")
		return
	}
	run, err := readDiagnosticRun(ctx, tx, route.task)
	if err != nil {
		candidateVerificationError(w, 503, "verification_run_invalid")
		return
	}
	if run == nil || run.State != report.DiagnosticRunCompleted || run.VerificationVersion != diagnosisVerificationV2 || run.RunRef != candidate.RunRef {
		candidateVerificationError(w, 409, "diagnosis_not_completed")
		return
	}
	var automatic *report.CandidateAssessment
	for i := range run.CandidateAssessments {
		if run.CandidateAssessments[i].CandidateID == candidate.ID {
			if automatic != nil {
				candidateVerificationError(w, 503, "verification_run_invalid")
				return
			}
			automatic = &run.CandidateAssessments[i]
		}
	}
	if automatic == nil || !validStoredAutomaticCandidateAssessment(candidate, *automatic) {
		candidateVerificationError(w, 503, "verification_evidence_invalid")
		return
	}
	page := candidateVerificationPage{TaskID: strconv.FormatInt(route.task, 10), CandidateID: strconv.FormatInt(candidate.ID, 10), EvidenceDigest: candidate.EvidenceDigest,
		Automatic: candidateVerificationAutomatic{Status: automatic.Status, Reason: automatic.Reason}}
	page.StoredEvidence.State = "not_stored"
	if run.VerificationObservationsStored {
		observations, readErr := readCandidateObservations(ctx, tx, route.task, run.RunRef)
		if readErr != nil || len(observations) != run.VerificationObservationsCount {
			candidateVerificationError(w, 503, "verification_evidence_invalid")
			return
		}
		digest, digestErr := candidateObservationsDigest(observations)
		if digestErr != nil || digest != run.VerificationObservationsDigest {
			candidateVerificationError(w, 503, "verification_evidence_invalid")
			return
		}
		page.StoredEvidence.Count = len(observations)
		if len(observations) != 0 {
			page.StoredEvidence.State = "stored"
		}
		if len(observations) != 0 && candidateverify.SupportedV2(candidate) {
			review := candidateverify.AssessStoredV2(candidate, observations)
			page.StoredEvidence.State, page.StoredEvidence.Verdict, page.StoredEvidence.Reason = "checked", review.Status, review.Reason
			// The same closed run cannot gain a new confirmation by rereading its
			// unchanged observations. A disagreement means storage/provenance is
			// incomplete or corrupt; keep the automatic record untouched.
			if review.Status != automatic.Status || review.Reason != automatic.Reason ||
				!reflect.DeepEqual(review.Attempts, automatic.AttemptRefs) || !reflect.DeepEqual(review.Sources, automatic.Sources) {
				candidateVerificationError(w, 503, "verification_evidence_invalid")
				return
			}
		}
	}
	if r.Method == http.MethodPost {
		if input.EvidenceDigest != candidate.EvidenceDigest {
			candidateVerificationError(w, 409, "candidate_evidence_changed")
			return
		}
		if err := createCandidateVerification(ctx, tx, route, candidate, owner, input.RequestID, *automatic); err != nil {
			if errors.Is(err, errCandidateVerificationPending) {
				candidateVerificationError(w, 409, "verification_already_pending")
			} else {
				candidateVerificationError(w, 503, "verification_storage_unavailable")
			}
			return
		}
	}
	page.Verifications, page.Truncated, err = listCandidateVerifications(ctx, tx, route, candidate, *automatic)
	if err != nil {
		candidateVerificationError(w, 503, "verification_storage_unavailable")
		return
	}
	if page.Truncated && len(page.Verifications) != 0 {
		page.NextBefore = page.Verifications[len(page.Verifications)-1].ID
	}
	current, err := authenticate(r)
	if err != nil || current != owner {
		candidateVerificationError(w, 401, "authentication_required")
		return
	}
	recheck, err := endpoint.Call(ctx, request)
	if err != nil || diagnosisprep.ValidateResponse(request, recheck) != nil {
		candidateVerificationError(w, 503, "verification_owner_unavailable")
		return
	}
	if recheck.Authorized == nil || !*recheck.Authorized || recheck.Job == nil || recheck.Job.TaskID != request.TaskID {
		candidateVerificationError(w, 403, "verification_scope_denied")
		return
	}
	if tx.Commit() != nil {
		candidateVerificationError(w, 503, "verification_storage_unavailable")
		return
	}
	writeJSON(w, 200, page)
}

var errCandidateVerificationPending = errors.New("candidate_verification_pending")

func createCandidateVerification(ctx context.Context, tx *sql.Tx, route candidateVerificationRoute, candidate db.DiagnosticCandidate, owner, requestKey string, automatic report.CandidateAssessment) error {
	var existing int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM diagnostic_candidate_verification_requests WHERE task_id=$1 AND candidate_id=$2 AND requester=$3 AND request_key=$4`, route.task, route.candidate, owner, requestKey).Scan(&existing)
	if err == nil {
		return nil // 같은 요청의 반복은 저장 이력을 늘리지 않는다.
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var pending int64
	err = tx.QueryRowContext(ctx, `SELECT q.id FROM diagnostic_candidate_verification_requests q JOIN LATERAL (SELECT process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true WHERE q.task_id=$1 AND q.candidate_id=$2 AND e.process_state IN ('checking_stored_evidence','awaiting_approval','running') LIMIT 1`, route.task, route.candidate).Scan(&pending)
	if err == nil {
		return errCandidateVerificationPending
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	raw, err := json.Marshal(candidateVerificationSourceRefs(automatic.Sources))
	if err != nil || len(raw) > 16<<10 {
		return db.ErrDiagnosticCandidate
	}
	sum := sha256.Sum256(raw)
	sourcesDigest := hex.EncodeToString(sum[:])
	// 저장 근거가 부족해도 이 read-only 검토는 완료된다. 실제 승인 대기는
	// 후보 전용 E② 계획과 새 실행 권한이 발급된 뒤에만 표시할 수 있다.
	err = tx.QueryRowContext(ctx, `SELECT q.id FROM diagnostic_candidate_verification_requests q JOIN diagnostic_candidate_verification_events e ON e.request_id=q.id AND e.step=1 WHERE q.task_id=$1 AND q.candidate_id=$2 AND q.original_evidence_digest=$3 AND e.process_state='completed' AND e.rule_version='passive/v2' AND e.sources_digest=$4 AND e.verdict=$5 LIMIT 1`, route.task, route.candidate, candidate.EvidenceDigest, sourcesDigest, automatic.Status).Scan(&existing)
	if err == nil {
		return nil // 동일한 저장 근거를 새 판정으로 중복 기록하지 않는다.
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO diagnostic_candidate_verification_requests(task_id,candidate_id,original_run_ref,original_evidence_version,original_evidence_digest,requester,request_key,method) VALUES($1,$2,$3,$4,$5,$6,$7,'stored/v1') RETURNING id`, route.task, route.candidate, candidate.RunRef, candidate.EvidenceVersion, candidate.EvidenceDigest, owner, requestKey).Scan(&id)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_verification_events(request_id,step,process_state,verdict,reason,sources,sources_digest,rule_version) VALUES($1,1,'completed',$2,$3,$4,$5,'passive/v2')`, id, automatic.Status, automatic.Reason, raw, sourcesDigest)
	return err
}

func listCandidateVerifications(ctx context.Context, tx *sql.Tx, route candidateVerificationRoute, candidate db.DiagnosticCandidate, automatic report.CandidateAssessment) ([]candidateVerificationAttempt, bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT q.id,q.candidate_id,q.request_key,q.method,q.original_evidence_digest,q.original_evidence_version,q.original_run_ref,q.requested_at,e.step,e.process_state,e.verdict,e.reason,e.sources,e.sources_digest,e.rule_version,e.execution_ref,e.approval_ref,e.event_at FROM diagnostic_candidate_verification_requests q LEFT JOIN LATERAL (SELECT * FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true WHERE q.task_id=$1 AND q.candidate_id=$2 AND ($3::bigint=0 OR q.id<$3) ORDER BY q.id DESC LIMIT $4`, route.task, route.candidate, route.before, candidateVerificationHistoryLimit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	items := make([]candidateVerificationAttempt, 0)
	for rows.Next() {
		row, step, err := scanCandidateVerificationAttempt(rows)
		if err != nil {
			return nil, false, err
		}
		if validateCandidateVerificationAttempt(row, step, candidate, automatic) != nil {
			return nil, false, db.ErrDiagnosticCandidate
		}
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(items) > candidateVerificationHistoryLimit
	if truncated {
		items = items[:candidateVerificationHistoryLimit]
	}
	return items, truncated, nil
}

type candidateVerificationScanner interface{ Scan(...any) error }

func scanCandidateVerificationAttempt(scanner candidateVerificationScanner) (candidateVerificationAttempt, int, error) {
	var id int64
	var candidateID int64
	var step int
	var verdict sql.NullString
	var row candidateVerificationAttempt
	err := scanner.Scan(&id, &candidateID, &row.RequestKey, &row.Method, &row.OriginalDigest, &row.OriginalVersion, &row.OriginalRunRef, &row.RequestedAt, &step, &row.Latest.State, &verdict, &row.Latest.Reason, &row.Latest.Sources, &row.Latest.SourcesDigest, &row.Latest.RuleVersion, &row.Latest.ExecutionRef, &row.Latest.ApprovalRef, &row.Latest.At)
	if err != nil {
		return row, 0, err
	}
	row.ID = strconv.FormatInt(id, 10)
	row.CandidateID = strconv.FormatInt(candidateID, 10)
	if verdict.Valid {
		row.Latest.Verdict = &verdict.String
	}
	return row, step, nil
}

func validateCandidateVerificationAttempt(row candidateVerificationAttempt, step int, candidate db.DiagnosticCandidate, automatic report.CandidateAssessment) error {
	if row.ID == "" || row.CandidateID != strconv.FormatInt(candidate.ID, 10) || !candidateVerificationKey.MatchString(row.RequestKey) || row.RequestedAt.IsZero() || row.Latest.At.IsZero() || row.Latest.At.Before(row.RequestedAt) ||
		row.OriginalDigest != candidate.EvidenceDigest || row.OriginalVersion != candidate.EvidenceVersion || row.OriginalRunRef != candidate.RunRef || row.Method != "stored/v1" || step != 1 || row.Latest.RuleVersion != "passive/v2" || row.Latest.ExecutionRef != "" || row.Latest.ApprovalRef != "" || !candidateVerificationDigest.MatchString(row.Latest.SourcesDigest) || len(row.Latest.Sources) > 16<<10 {
		return db.ErrDiagnosticCandidate
	}
	sum := sha256.Sum256(row.Latest.Sources)
	if row.Latest.SourcesDigest != hex.EncodeToString(sum[:]) {
		return db.ErrDiagnosticCandidate
	}
	var sources []candidateVerificationSourceRef
	if json.Unmarshal(row.Latest.Sources, &sources) != nil || !reflect.DeepEqual(sources, candidateVerificationSourceRefs(automatic.Sources)) || row.Latest.Reason != automatic.Reason {
		return db.ErrDiagnosticCandidate
	}
	if row.Latest.State != "completed" || row.Latest.Verdict == nil || *row.Latest.Verdict != automatic.Status {
		return db.ErrDiagnosticCandidate
	}
	return nil
}
