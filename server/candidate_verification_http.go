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
	Plan            *candidateActivePlan       `json:"plan,omitempty"`
	PlanDigest      string                     `json:"plan_digest,omitempty"`
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
		Method         string `json:"method,omitempty"`
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
		if input.Method == "" {
			input.Method = "stored/v1"
		}
		if input.Method != "stored/v1" && input.Method != "active/v1" {
			candidateVerificationError(w, 400, "invalid_verification_method")
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
		var createErr error
		if input.Method == "active/v1" {
			if response.Job.Scope == nil {
				candidateVerificationError(w, 409, "verification_scope_denied")
				return
			}
			targetRequest := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "candidate-verification-target", Command: diagnosisprep.ProductControl, Principal: owner,
				Product: &diagnosisprep.ProductRequest{Operation: "target", TargetID: response.Job.Scope.TargetRef}}
			targetResponse, targetErr := endpoint.Call(ctx, targetRequest)
			if targetErr != nil || diagnosisprep.ValidateResponse(targetRequest, targetResponse) != nil || targetResponse.Error != "" || targetResponse.Product == nil || targetResponse.Product.Target == nil || targetResponse.Product.Reason != "" {
				candidateVerificationError(w, 503, "verification_target_unavailable")
				return
			}
			plan, digest, planErr := makeCandidateActivePlan(time.Now().UTC(), candidate, *automatic, response.Job.Input, *response.Job.Scope, *targetResponse.Product.Target)
			if planErr != nil {
				candidateVerificationError(w, 409, "candidate_active_plan_unavailable")
				return
			}
			createErr = createCandidateActiveVerification(ctx, tx, route, candidate, owner, input.RequestID, *automatic, plan, digest)
		} else {
			createErr = createCandidateVerification(ctx, tx, route, candidate, owner, input.RequestID, *automatic)
		}
		if err := createErr; err != nil {
			if errors.Is(err, errCandidateVerificationPending) {
				candidateVerificationError(w, 409, "verification_already_pending")
			} else if errors.Is(err, errCandidateVerificationConflict) {
				candidateVerificationError(w, 409, "verification_request_conflict")
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
var errCandidateVerificationConflict = errors.New("candidate_verification_conflict")

func createCandidateActiveVerification(ctx context.Context, tx *sql.Tx, route candidateVerificationRoute, candidate db.DiagnosticCandidate, owner, requestKey string, automatic report.CandidateAssessment, plan candidateActivePlan, digest string) error {
	if automatic.Status != candidateverify.Insufficient || plan.TaskID != route.task || plan.CandidateID != route.candidate || plan.EvidenceDigest != candidate.EvidenceDigest || plan.OriginalRunRef != candidate.RunRef {
		return errCandidateVerificationConflict
	}
	var existingMethod string
	err := tx.QueryRowContext(ctx, `SELECT method FROM diagnostic_candidate_verification_requests WHERE task_id=$1 AND candidate_id=$2 AND requester=$3 AND request_key=$4`, route.task, route.candidate, owner, requestKey).Scan(&existingMethod)
	if err == nil {
		if existingMethod != "active/v1" {
			return errCandidateVerificationConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// Expired proposals grant no authority and must not block a fresh,
	// separately reviewed plan for this candidate.
	_, err = tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_verification_events(request_id,step,process_state,reason,sources,sources_digest,rule_version)
SELECT q.id,2,'expired','plan_expired','[]'::json,'4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945','candidate-active/v1'
FROM diagnostic_candidate_verification_requests q
JOIN diagnostic_candidate_verification_plans p ON p.request_id=q.id
JOIN LATERAL (SELECT step,process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true
WHERE q.task_id=$1 AND q.candidate_id=$2 AND q.method='active/v1' AND e.step=1 AND e.process_state='awaiting_approval' AND p.expires_at<clock_timestamp()`, route.task, route.candidate)
	if err != nil {
		return err
	}
	var pending int64
	err = tx.QueryRowContext(ctx, `SELECT q.id FROM diagnostic_candidate_verification_requests q JOIN LATERAL (SELECT process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true WHERE q.task_id=$1 AND q.candidate_id=$2 AND e.process_state IN ('checking_stored_evidence','awaiting_approval','approved','running') LIMIT 1`, route.task, route.candidate).Scan(&pending)
	if err == nil {
		return errCandidateVerificationPending
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	raw, err := json.Marshal(plan)
	if err != nil || len(raw) > 4096 {
		return errCandidateVerificationConflict
	}
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO diagnostic_candidate_verification_requests(task_id,candidate_id,original_run_ref,original_evidence_version,original_evidence_digest,requester,request_key,method) VALUES($1,$2,$3,$4,$5,$6,$7,'active/v1') RETURNING id`, route.task, route.candidate, candidate.RunRef, candidate.EvidenceVersion, candidate.EvidenceDigest, owner, requestKey).Scan(&id)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_verification_plans(request_id,plan,plan_digest,target_id,target_revision,expires_at) VALUES($1,$2::jsonb,$3,$4,$5,$6)`, id, raw, digest, plan.TargetID, plan.TargetRevision, plan.ExpiresAt)
	if err != nil {
		return err
	}
	emptySources := []byte("[]")
	sum := sha256.Sum256(emptySources)
	_, err = tx.ExecContext(ctx, `INSERT INTO diagnostic_candidate_verification_events(request_id,step,process_state,reason,sources,sources_digest,rule_version) VALUES($1,1,'awaiting_approval','independent_observation_missing',$2,$3,'candidate-active/v1')`, id, emptySources, hex.EncodeToString(sum[:]))
	return err
}

func createCandidateVerification(ctx context.Context, tx *sql.Tx, route candidateVerificationRoute, candidate db.DiagnosticCandidate, owner, requestKey string, automatic report.CandidateAssessment) error {
	var existing int64
	var existingMethod string
	err := tx.QueryRowContext(ctx, `SELECT id,method FROM diagnostic_candidate_verification_requests WHERE task_id=$1 AND candidate_id=$2 AND requester=$3 AND request_key=$4`, route.task, route.candidate, owner, requestKey).Scan(&existing, &existingMethod)
	if err == nil {
		if existingMethod != "stored/v1" {
			return errCandidateVerificationConflict
		}
		return nil // 같은 요청의 반복은 저장 이력을 늘리지 않는다.
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var pending int64
	err = tx.QueryRowContext(ctx, `SELECT q.id FROM diagnostic_candidate_verification_requests q JOIN LATERAL (SELECT process_state FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true WHERE q.task_id=$1 AND q.candidate_id=$2 AND e.process_state IN ('checking_stored_evidence','awaiting_approval','approved','running') LIMIT 1`, route.task, route.candidate).Scan(&pending)
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
	rows, err := tx.QueryContext(ctx, `SELECT q.id,q.candidate_id,q.request_key,q.method,q.original_evidence_digest,q.original_evidence_version,q.original_run_ref,q.requested_at,e.step,e.process_state,e.verdict,e.reason,e.sources,e.sources_digest,e.rule_version,e.execution_ref,e.approval_ref,e.event_at,p.plan,p.plan_digest,d.decision,d.plan_digest,x.followup_task_id,x.run_ref FROM diagnostic_candidate_verification_requests q LEFT JOIN LATERAL (SELECT * FROM diagnostic_candidate_verification_events WHERE request_id=q.id ORDER BY step DESC LIMIT 1) e ON true LEFT JOIN diagnostic_candidate_verification_plans p ON p.request_id=q.id LEFT JOIN diagnostic_candidate_verification_decisions d ON d.request_id=q.id LEFT JOIN diagnostic_candidate_verification_executions x ON x.request_id=q.id AND x.plan_digest=p.plan_digest WHERE q.task_id=$1 AND q.candidate_id=$2 AND ($3::bigint=0 OR q.id<$3) ORDER BY q.id DESC LIMIT $4`, route.task, route.candidate, route.before, candidateVerificationHistoryLimit+1)
	if err != nil {
		return nil, false, err
	}
	type attemptAndBinding struct {
		row     candidateVerificationAttempt
		step    int
		binding candidateVerificationBinding
	}
	pending := make([]attemptAndBinding, 0)
	for rows.Next() {
		row, step, binding, err := scanCandidateVerificationAttempt(rows)
		if err != nil {
			rows.Close()
			return nil, false, err
		}
		pending = append(pending, attemptAndBinding{row, step, binding})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, false, err
	}
	items := make([]candidateVerificationAttempt, 0, len(pending))
	for _, pendingRow := range pending {
		if validateCandidateVerificationAttempt(pendingRow.row, pendingRow.step, pendingRow.binding, candidate, automatic) != nil ||
			validateCandidateActiveEvidence(ctx, tx, pendingRow.row, pendingRow.binding, candidate) != nil {
			return nil, false, db.ErrDiagnosticCandidate
		}
		items = append(items, pendingRow.row)
	}
	truncated := len(items) > candidateVerificationHistoryLimit
	if truncated {
		items = items[:candidateVerificationHistoryLimit]
	}
	return items, truncated, nil
}

type candidateVerificationScanner interface{ Scan(...any) error }

type candidateVerificationBinding struct {
	Decision, DecisionDigest sql.NullString
	FollowupTask             sql.NullInt64
	RunRef                   sql.NullString
}

func scanCandidateVerificationAttempt(scanner candidateVerificationScanner) (candidateVerificationAttempt, int, candidateVerificationBinding, error) {
	var id int64
	var candidateID int64
	var step int
	var verdict sql.NullString
	var planJSON []byte
	var planDigest sql.NullString
	var binding candidateVerificationBinding
	var row candidateVerificationAttempt
	err := scanner.Scan(&id, &candidateID, &row.RequestKey, &row.Method, &row.OriginalDigest, &row.OriginalVersion, &row.OriginalRunRef, &row.RequestedAt, &step, &row.Latest.State, &verdict, &row.Latest.Reason, &row.Latest.Sources, &row.Latest.SourcesDigest, &row.Latest.RuleVersion, &row.Latest.ExecutionRef, &row.Latest.ApprovalRef, &row.Latest.At, &planJSON, &planDigest, &binding.Decision, &binding.DecisionDigest, &binding.FollowupTask, &binding.RunRef)
	if err != nil {
		return row, 0, binding, err
	}
	row.ID = strconv.FormatInt(id, 10)
	row.CandidateID = strconv.FormatInt(candidateID, 10)
	if verdict.Valid {
		row.Latest.Verdict = &verdict.String
	}
	if planDigest.Valid {
		row.PlanDigest = planDigest.String
		var plan candidateActivePlan
		if strictJSON(planJSON, &plan) != nil {
			return row, 0, binding, db.ErrDiagnosticCandidate
		}
		row.Plan = &plan
	} else if len(planJSON) != 0 {
		return row, 0, binding, db.ErrDiagnosticCandidate
	}
	return row, step, binding, nil
}

func validateCandidateVerificationAttempt(row candidateVerificationAttempt, step int, binding candidateVerificationBinding, candidate db.DiagnosticCandidate, automatic report.CandidateAssessment) error {
	if row.ID == "" || row.CandidateID != strconv.FormatInt(candidate.ID, 10) || !candidateVerificationKey.MatchString(row.RequestKey) || row.RequestedAt.IsZero() || row.Latest.At.IsZero() || row.Latest.At.Before(row.RequestedAt) ||
		row.OriginalDigest != candidate.EvidenceDigest || row.OriginalVersion != candidate.EvidenceVersion || row.OriginalRunRef != candidate.RunRef || !candidateVerificationDigest.MatchString(row.Latest.SourcesDigest) || len(row.Latest.Sources) > 16<<10 {
		return db.ErrDiagnosticCandidate
	}
	sum := sha256.Sum256(row.Latest.Sources)
	if row.Latest.SourcesDigest != hex.EncodeToString(sum[:]) {
		return db.ErrDiagnosticCandidate
	}
	if row.Method == "active/v1" {
		return validateCandidateActiveAttempt(row, step, binding, candidate)
	}
	if row.Method != "stored/v1" || step != 1 || row.Plan != nil || row.PlanDigest != "" || binding.Decision.Valid || binding.FollowupTask.Valid ||
		row.Latest.RuleVersion != "passive/v2" || row.Latest.ExecutionRef != "" || row.Latest.ApprovalRef != "" {
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

func validateCandidateActiveAttempt(row candidateVerificationAttempt, step int, binding candidateVerificationBinding, candidate db.DiagnosticCandidate) error {
	if row.Plan == nil || row.Plan.Version != "candidate-active/v1" || row.Plan.TaskID != candidate.TaskID || row.Plan.CandidateID != candidate.ID ||
		row.Plan.EvidenceDigest != candidate.EvidenceDigest || row.Plan.OriginalRunRef != candidate.RunRef || row.Plan.Method != "GET" ||
		row.Plan.MaxRequests != 2 || row.Plan.DurationSeconds != 60 || row.Plan.ModelCostMicros != 0 ||
		row.Plan.TargetID == "" || row.Plan.TargetRevision == 0 || row.Plan.Origin == "" || row.Plan.ExactPath == "" ||
		!row.Plan.NeedsNewEvidence || !candidateVerificationDigest.MatchString(row.PlanDigest) || row.Latest.RuleVersion != "candidate-active/v1" {
		return db.ErrDiagnosticCandidate
	}
	digest, err := candidatePlanDigest(*row.Plan)
	if err != nil || digest != row.PlanDigest {
		return db.ErrDiagnosticCandidate
	}
	switch step {
	case 1:
		if row.Latest.State != "awaiting_approval" || binding.Decision.Valid || binding.FollowupTask.Valid || row.Latest.Verdict != nil || row.Latest.ExecutionRef != "" || row.Latest.ApprovalRef != "" {
			return db.ErrDiagnosticCandidate
		}
	case 2:
		if row.Latest.Verdict != nil || row.Latest.ExecutionRef != "" {
			return db.ErrDiagnosticCandidate
		}
		if row.Latest.State == "expired" || row.Latest.State == "cancelled" {
			if binding.Decision.Valid || row.Latest.ApprovalRef != "" || row.Latest.State == "expired" && row.Latest.At.Before(row.Plan.ExpiresAt) {
				return db.ErrDiagnosticCandidate
			}
		} else if !binding.Decision.Valid || binding.DecisionDigest.String != row.PlanDigest || binding.Decision.String != row.Latest.State ||
			row.Latest.State != "approved" && row.Latest.State != "rejected" || row.Latest.State == "approved" && row.Latest.ApprovalRef == "" ||
			row.Latest.State == "rejected" && row.Latest.ApprovalRef != "" {
			return db.ErrDiagnosticCandidate
		}
	case 3, 4:
		if !binding.Decision.Valid || binding.Decision.String != "approved" || binding.DecisionDigest.String != row.PlanDigest || row.Latest.State == "completed" && row.Latest.Verdict == nil || row.Latest.State != "completed" && row.Latest.Verdict != nil {
			return db.ErrDiagnosticCandidate
		}
		if step == 3 && row.Latest.State != "running" && row.Latest.State != "failed" && row.Latest.State != "cancelled" && row.Latest.State != "expired" ||
			step == 4 && row.Latest.State != "completed" && row.Latest.State != "failed" && row.Latest.State != "cancelled" && row.Latest.State != "expired" {
			return db.ErrDiagnosticCandidate
		}
		if binding.FollowupTask.Valid {
			if row.Latest.ExecutionRef == "" || binding.RunRef.String != row.Latest.ExecutionRef || binding.FollowupTask.Int64 == candidate.TaskID {
				return db.ErrDiagnosticCandidate
			}
		} else if row.Latest.ExecutionRef != "" || row.Latest.State == "running" || row.Latest.State == "completed" {
			return db.ErrDiagnosticCandidate
		}
	default:
		return db.ErrDiagnosticCandidate
	}
	return nil
}
