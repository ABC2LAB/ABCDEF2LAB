package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/Autumn-27/artex/internal/diagnosticlog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// DiagnosticCandidate는 확정 finding이 아닌, 실제 실행 관찰에 연결된 검토 대상이다.
// 기존 fact 저장소를 사용하며 triage/오라클 판정 상태를 재정의하지 않는다.
type DiagnosticCandidate struct {
	SuggestedSeverity string    `json:"suggested_severity,omitempty"`
	SeverityRationale string    `json:"severity_rationale,omitempty"`
	SeveritySource    string    `json:"severity_source,omitempty"`
	ID                int64     `json:"id"`
	TaskID            int64     `json:"task_id"`
	IntentID          int64     `json:"intent_id"`
	RunRef            string    `json:"run_ref"`
	CallRef           string    `json:"call_ref"`
	VulnClass         string    `json:"vulnclass"`
	VerificationClaim string    `json:"verification_claim,omitempty"`
	Name              string    `json:"name"`
	Summary           string    `json:"summary"`
	Remediation       string    `json:"remediation"`
	Classification    string    `json:"classification"`
	Verification      string    `json:"verification"`
	EvidenceVersion   int64     `json:"evidence_version"`
	EvidenceDigest    string    `json:"evidence_digest"`
	ObservedAt        time.Time `json:"observed_at"`
	Evidence          string    `json:"evidence"`
	OutputTruncated   bool      `json:"output_truncated"`
	// LanguageMixed는 모델이 쓴 이름·설명·조치·근거에 한자·가나가 섞인 채 저장됐다는 표시다('다른 언어 섞임').
	// LanguageRewrites는 그 때문에 다시 쓰게 한 횟수(0~MaxDiagnosticLanguageRewrites)다(2026-11-24,
	// agent/e2_diagnostic_candidate_lang.go). 둘 다 0이면 칸을 쓰지 않는다.
	LanguageMixed    bool `json:"language_mixed,omitempty"`
	LanguageRewrites int  `json:"language_rewrites,omitempty"`
}

var ErrDiagnosticCandidate = errors.New("diagnostic_candidate_invalid")

// MaxDiagnosticLanguageRewrites는 한국어 칸 언어 섞임으로 한 후보를 다시 쓰게 하는 최대 횟수다(2026-11-24 사용자 결정).
const MaxDiagnosticLanguageRewrites = 2

const diagnosticCandidateOrigin = "e2_diagnostic_candidate_v1"
const MaxDiagnosticCandidates = 10000

func DiagnosticEvidenceDigest(evidence string) string {
	sum := sha256.Sum256([]byte(evidence))
	return hex.EncodeToString(sum[:])
}

func (c DiagnosticCandidate) Validate() error {
	if c.TaskID <= 0 || c.IntentID <= 0 || c.RunRef == "" || c.CallRef == "" ||
		c.Verification != "manual_review_required" || c.EvidenceVersion != 1 ||
		c.ObservedAt.IsZero() || c.ObservedAt.Year() < 1 || c.ObservedAt.Year() > 9999 ||
		c.EvidenceDigest != DiagnosticEvidenceDigest(c.Evidence) {
		return ErrDiagnosticCandidate
	}
	for _, s := range []string{c.RunRef, c.CallRef, c.VulnClass, c.Name, c.Summary, c.Remediation, c.Evidence} {
		if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
			return ErrDiagnosticCandidate
		}
	}
	if len(c.RunRef) > 256 || len(c.CallRef) > 256 || strings.TrimSpace(c.Name) == "" ||
		len(c.Name) > 512 || strings.TrimSpace(c.VulnClass) == "" || len(c.VulnClass) > 128 ||
		len(c.Summary) > 8192 || len(c.Remediation) > 8192 || len(c.Evidence) > 256<<10 {
		return ErrDiagnosticCandidate
	}
	if c.VerificationClaim != "" && c.VerificationClaim != "html_nosniff_missing" && c.VerificationClaim != "server_version_banner" {
		return ErrDiagnosticCandidate
	}
	// 섞임 표시는 한 번 이상 다시 쓰게 한 뒤에만 붙는다.
	if c.LanguageRewrites < 0 || c.LanguageRewrites > MaxDiagnosticLanguageRewrites || c.LanguageMixed && c.LanguageRewrites < 1 {
		return ErrDiagnosticCandidate
	}
	switch c.Classification {
	case "completed", "policy_not_executed", "partial_policy_denial", "unsupported", "partial_unsupported",
		"result_unknown", "tool_failure", "transient_failure", "forward_not_attempted", "terminated", "isolation_unavailable":
	default:
		return ErrDiagnosticCandidate
	}
	var evidence struct {
		Related []json.RawMessage `json:"related_calls"`
		Trusted struct {
			Classification string `json:"classification"`
			Execution      struct {
				Scope struct {
					TaskRef string `json:"TaskRef"`
					RunRef  string `json:"RunRef"`
				} `json:"scope"`
				IntentID int64 `json:"intent_id"`
			} `json:"execution"`
			Call struct {
				CallRef  string `json:"call_ref"`
				IntentID int64  `json:"intent_id"`
			} `json:"call"`
			OutputTruncated bool `json:"output_truncated"`
		} `json:"trusted"`
	}
	if json.Unmarshal([]byte(c.Evidence), &evidence) != nil ||
		evidence.Trusted.Classification != c.Classification ||
		evidence.Trusted.Execution.Scope.TaskRef != strconv.FormatInt(c.TaskID, 10) ||
		evidence.Trusted.Execution.Scope.RunRef != c.RunRef ||
		evidence.Trusted.Execution.IntentID != c.IntentID ||
		evidence.Trusted.Call.CallRef != c.CallRef ||
		evidence.Trusted.Call.IntentID != c.IntentID ||
		evidence.Trusted.OutputTruncated != c.OutputTruncated {
		return ErrDiagnosticCandidate
	}
	if len(evidence.Related) > 8 {
		return ErrDiagnosticCandidate
	}
	seen := map[string]bool{c.CallRef: true}
	for _, raw := range evidence.Related {
		var other struct {
			Related []json.RawMessage `json:"related_calls"`
			Trusted struct {
				Classification  string `json:"classification"`
				OutputTruncated bool   `json:"output_truncated"`
				Call            struct {
					CallRef string `json:"call_ref"`
				} `json:"call"`
			} `json:"trusted"`
		}
		if json.Unmarshal(raw, &other) != nil || len(other.Related) > 0 || seen[other.Trusted.Call.CallRef] {
			return ErrDiagnosticCandidate
		}
		seen[other.Trusted.Call.CallRef] = true
		copy := c
		copy.Evidence = string(raw)
		copy.EvidenceDigest = DiagnosticEvidenceDigest(copy.Evidence)
		copy.CallRef = other.Trusted.Call.CallRef
		copy.Classification = other.Trusted.Classification
		copy.OutputTruncated = other.Trusted.OutputTruncated
		if copy.Validate() != nil {
			return ErrDiagnosticCandidate
		}
	}
	return nil
}

// RecordDiagnosticCandidate는 신뢰된 E② 어댑터 전용이며 모델의 확정/증거 본문을 받지 않는다.
// task/intent 귀속 확인과 fact/edge 삽입을 같은 트랜잭션으로 처리한다.
func (s *ExplorationStore) RecordDiagnosticCandidate(ctx context.Context, c DiagnosticCandidate) (int64, error) {
	c = c.NormalizedSeverity()
	if ctx == nil || c.ID != 0 || c.Validate() != nil {
		return 0, ErrDiagnosticCandidate
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var kind string
	err = tx.QueryRowContext(ctx, `SELECT n.kind FROM tasks t JOIN exploration_nodes n ON n.exploration_id=t.exploration_id WHERE t.id=$1 AND t.exploration_id=$2 AND n.id=$3 FOR UPDATE OF n`, c.TaskID, s.expID, c.IntentID).Scan(&kind)
	if err != nil {
		return 0, err
	}
	if kind != KindIntent {
		return 0, ErrDiagnosticCandidate
	}
	// 같은 call/type/name 제출은 기존 후보를 반환한다. 다른 관찰과 이름만으로 합치지 않는다.
	var existing int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM exploration_nodes WHERE exploration_id=$1 AND origin=$2 AND payload->'diagnostic_candidate_v1'->>'run_ref'=$3 AND payload->'diagnostic_candidate_v1'->>'call_ref'=$4 AND payload->'diagnostic_candidate_v1'->>'vulnclass'=$5 AND payload->'diagnostic_candidate_v1'->>'name'=$6 AND payload->'diagnostic_candidate_v1'->>'intent_id'=$7 AND payload->'diagnostic_candidate_v1'->>'evidence_digest'=$8 AND COALESCE(payload->'diagnostic_candidate_v1'->>'verification_claim','')=$9 ORDER BY id LIMIT 1`, s.expID, diagnosticCandidateOrigin, c.RunRef, c.CallRef, c.VulnClass, c.Name, strconv.FormatInt(c.IntentID, 10), c.EvidenceDigest, c.VerificationClaim).Scan(&existing)
	if err == nil {

		err := tx.Commit()
		if err == nil {
			_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "db", Event: "candidate_duplicate", Stage: "candidate_storage", Status: "manual_review_required", TaskID: c.TaskID, RunRef: c.RunRef, IntentID: c.IntentID, CallRef: c.CallRef, CandidateID: existing})
		}
		return existing, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	// fact의 confirmed는 관찰 존재를 뜻한다. 후보 확정 여부는 별도 verification으로 고정한다.
	raw, err := json.Marshal(map[string]any{"summary": "Unconfirmed diagnostic candidate; manual review required", "diagnostic_candidate_v1": c})
	if err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO exploration_nodes(exploration_id,kind,payload,priority,state,origin) VALUES($1,'fact',$2,5,'confirmed',$3) RETURNING id`, s.expID, string(raw), diagnosticCandidateOrigin).Scan(&id)
	if err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO exploration_edges(exploration_id,src_id,rel,dst_id) VALUES($1,$2,'yields',$3)`, s.expID, c.IntentID, id)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// ListDiagnosticCandidates는 연결 task를 합치지 않고 선택한 회차만 읽는다.
// limit+1 사용은 호출자의 책임이며 잘못된/변경된 증거는 부분 성공 대신 오류다.
func (s *ExplorationStore) ListDiagnosticCandidates(taskID int64, runRef string, limit int) ([]DiagnosticCandidate, error) {
	return ListDiagnosticCandidatesQuery(context.Background(), s.db.DB, s.expID, taskID, runRef, limit)
}

type DiagnosticCandidateQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// ReadDiagnosticCandidate는 작업 하나의 후보 하나를 같은 읽기 트랜잭션에서 읽는다(판정 상세 조회 candidate selector).
// 없거나 다른 작업·탐색의 노드면 ok=false다. 노드는 있는데 저장 형식·작업 결속이 어긋나면 ErrDiagnosticCandidate다.
func ReadDiagnosticCandidate(ctx context.Context, q DiagnosticCandidateQueryer, explorationID, taskID, id int64) (DiagnosticCandidate, bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT n.payload FROM exploration_nodes n JOIN tasks t ON t.exploration_id=n.exploration_id WHERE n.id=$1 AND t.id=$2 AND n.exploration_id=$3 AND n.origin=$4 AND n.kind='fact' LIMIT 2`,
		id, taskID, explorationID, diagnosticCandidateOrigin)
	if err != nil {
		return DiagnosticCandidate{}, false, err
	}
	defer rows.Close()
	var raw []byte
	found := 0
	for rows.Next() {
		found++
		if found > 1 {
			return DiagnosticCandidate{}, false, ErrDiagnosticCandidate
		}
		if err := rows.Scan(&raw); err != nil {
			return DiagnosticCandidate{}, false, err
		}
	}
	if err := rows.Err(); err != nil {
		return DiagnosticCandidate{}, false, err
	}
	if found == 0 {
		return DiagnosticCandidate{}, false, nil
	}
	c, err := decodeDiagnosticCandidate(id, raw, taskID, "")
	if err != nil {
		return DiagnosticCandidate{}, false, ErrDiagnosticCandidate
	}
	return c, true, nil
}

// ListDiagnosticCandidatesQuery는 보고서의 동일 repeatable-read 트랜잭션에 참여한다.
func ListDiagnosticCandidatesQuery(ctx context.Context, q DiagnosticCandidateQueryer, explorationID, taskID int64, runRef string, limit int) ([]DiagnosticCandidate, error) {
	if taskID <= 0 || limit <= 0 || limit > MaxDiagnosticCandidates+1 {
		return nil, ErrDiagnosticCandidate
	}
	rows, err := q.QueryContext(ctx, `SELECT n.id,n.payload FROM exploration_nodes n JOIN tasks t ON t.exploration_id=n.exploration_id WHERE t.id=$1 AND n.exploration_id=$2 AND n.origin=$3 AND n.kind='fact' AND ($4='' OR n.payload->'diagnostic_candidate_v1'->>'run_ref'=$4) ORDER BY n.id LIMIT $5`, taskID, explorationID, diagnosticCandidateOrigin, runRef, limit)
	return readDiagnosticCandidateRows(rows, err, taskID, runRef)
}

const LiveDiagnosticCandidateLimit = 200
const diagnosticCandidateReadBytes = 16 << 20
const liveDiagnosticCandidateRowBytes = 2 << 20

// ListLiveDiagnosticCandidates는 최근 후보를 행과 바이트 상한까지 읽고, 제외된 행이 있는지 반환한다.
func ListLiveDiagnosticCandidates(ctx context.Context, q DiagnosticCandidateQueryer, explorationID, taskID int64, runRef string) ([]DiagnosticCandidate, bool, error) {
	if taskID <= 0 || explorationID <= 0 || runRef == "" {
		return nil, false, ErrDiagnosticCandidate
	}
	// 크기를 먼저 계산하되 상한 밖의 행은 payload나 intent 연결을 읽기 결과에 넣지 않는다.
	// 행 제한을 넘긴 payload에는 상한만큼의 예산을 계산하고, 반환 범위 안에 있으면 검증에서 거부한다.
	rows, err := q.QueryContext(ctx, `
WITH recent AS MATERIALIZED (
  SELECT n.id
  FROM exploration_nodes n
  WHERE n.exploration_id=$2 AND n.origin=$3
    AND EXISTS (SELECT 1 FROM tasks t WHERE t.id=$1 AND t.exploration_id=$2 AND t.deleted_at IS NULL)
    AND COALESCE(NULLIF(n.payload->'diagnostic_candidate_v1'->>'run_ref',''),$4)=$4
  ORDER BY n.id DESC LIMIT ($5::int + 1)
), sized AS MATERIALIZED (
  SELECT n.id, octet_length(n.payload::text) AS bytes
  FROM (SELECT id FROM recent ORDER BY id DESC LIMIT $5) r
  JOIN exploration_nodes n ON n.id=r.id
), visible AS MATERIALIZED (
  SELECT id, bytes FROM (
    SELECT id, bytes, SUM(LEAST(bytes,$6)) OVER (ORDER BY id DESC ROWS UNBOUNDED PRECEDING) AS running_bytes
    FROM sized
  ) budgeted WHERE running_bytes <= $7
)
SELECT n.id, CASE WHEN n.kind='fact' AND n.state='confirmed' AND v.bytes <= $6
  AND EXISTS (SELECT 1 FROM exploration_nodes src
    JOIN exploration_edges e ON e.src_id=src.id AND e.exploration_id=src.exploration_id
    WHERE src.exploration_id=n.exploration_id AND src.kind='intent' AND e.rel='yields'
      AND e.dst_id=n.id AND src.id::text=n.payload->'diagnostic_candidate_v1'->>'intent_id')
  THEN n.payload::text END, FALSE AS truncated
FROM visible v JOIN exploration_nodes n ON n.id=v.id
UNION ALL
SELECT 0, NULL, TRUE
WHERE (SELECT count(*) FROM recent) > (SELECT count(*) FROM visible)
ORDER BY truncated, id DESC`, taskID, explorationID, diagnosticCandidateOrigin, runRef, LiveDiagnosticCandidateLimit, liveDiagnosticCandidateRowBytes, diagnosticCandidateReadBytes)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []DiagnosticCandidate{}
	var truncated bool
	for rows.Next() {
		var id int64
		var raw []byte
		if err := rows.Scan(&id, &raw, &truncated); err != nil {
			return nil, false, err
		}
		if truncated {
			continue
		}
		c, err := decodeDiagnosticCandidate(id, raw, taskID, runRef)
		if err != nil {
			return nil, false, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return out, truncated, nil
}

func readDiagnosticCandidateRows(rows *sql.Rows, err error, taskID int64, runRef string) ([]DiagnosticCandidate, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DiagnosticCandidate{}
	totalBytes := 0
	for rows.Next() {
		var id int64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		totalBytes += len(raw)
		if totalBytes > diagnosticCandidateReadBytes {
			return nil, ErrDiagnosticCandidate
		}
		c, err := decodeDiagnosticCandidate(id, raw, taskID, runRef)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func decodeDiagnosticCandidate(id int64, raw []byte, taskID int64, runRef string) (DiagnosticCandidate, error) {
	var payload struct {
		Candidate DiagnosticCandidate `json:"diagnostic_candidate_v1"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return DiagnosticCandidate{}, ErrDiagnosticCandidate
	}
	c := payload.Candidate
	if c.ID != 0 || c.TaskID != taskID || (runRef != "" && c.RunRef != runRef) || c.Validate() != nil {
		return DiagnosticCandidate{}, ErrDiagnosticCandidate
	}
	c = c.NormalizedSeverity()
	c.ID = id
	return c, nil
}

// CountDiagnosticCandidateLanguage는 한 실행(run_ref)의 진단 후보 가운데 한국어 칸 언어 섞임 때문에 다시 쓰게 한 횟수의
// 합과 '다른 언어 섞임' 표시가 붙은 후보 수다(실행 기록 칸, 2026-11-24). 저장되지 않은 다시 쓰기 요청은 세지 않는다.
func CountDiagnosticCandidateLanguage(ctx context.Context, q DiagnosticCandidateQueryer, explorationID int64, runRef string) (rewrites, mixed int, err error) {
	if explorationID <= 0 || runRef == "" {
		return 0, 0, ErrDiagnosticCandidate
	}
	// 다시 쓰기 횟수는 저장 검사(Validate)를 거친 0~2만 있으므로 그 값만 더한다(형 변환 실패로 집계가 깨지지 않게).
	rows, err := q.QueryContext(ctx, `SELECT COALESCE(sum(CASE n.payload->'diagnostic_candidate_v1'->>'language_rewrites' WHEN '1' THEN 1 WHEN '2' THEN 2 ELSE 0 END),0),
  COALESCE(sum(CASE WHEN n.payload->'diagnostic_candidate_v1'->>'language_mixed'='true' THEN 1 ELSE 0 END),0)
FROM exploration_nodes n
WHERE n.exploration_id=$1 AND n.origin=$2 AND n.kind='fact' AND n.payload->'diagnostic_candidate_v1'->>'run_ref'=$3`, explorationID, diagnosticCandidateOrigin, runRef)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	if !rows.Next() {
		return 0, 0, errors.Join(ErrDiagnosticCandidate, rows.Err())
	}
	if err := rows.Scan(&rewrites, &mixed); err != nil {
		return 0, 0, err
	}
	return rewrites, mixed, rows.Err()
}
