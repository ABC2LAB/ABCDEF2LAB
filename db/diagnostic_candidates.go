package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// DiagnosticCandidate는 확정 finding이 아닌, 실제 실행 관찰에 연결된 검토 대상이다.
// 기존 fact 저장소를 사용하며 triage/오라클 판정 상태를 재정의하지 않는다.
type DiagnosticCandidate struct {
	ID              int64     `json:"id"`
	TaskID          int64     `json:"task_id"`
	IntentID        int64     `json:"intent_id"`
	RunRef          string    `json:"run_ref"`
	CallRef         string    `json:"call_ref"`
	VulnClass       string    `json:"vulnclass"`
	Name            string    `json:"name"`
	Summary         string    `json:"summary"`
	Remediation     string    `json:"remediation"`
	Classification  string    `json:"classification"`
	Verification    string    `json:"verification"`
	EvidenceVersion int64     `json:"evidence_version"`
	EvidenceDigest  string    `json:"evidence_digest"`
	ObservedAt      time.Time `json:"observed_at"`
	Evidence        string    `json:"evidence"`
	OutputTruncated bool      `json:"output_truncated"`
}

var ErrDiagnosticCandidate = errors.New("diagnostic_candidate_invalid")

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
	err = tx.QueryRowContext(ctx, `SELECT id FROM exploration_nodes WHERE exploration_id=$1 AND origin=$2 AND payload->'diagnostic_candidate_v1'->>'run_ref'=$3 AND payload->'diagnostic_candidate_v1'->>'call_ref'=$4 AND payload->'diagnostic_candidate_v1'->>'vulnclass'=$5 AND payload->'diagnostic_candidate_v1'->>'name'=$6 AND payload->'diagnostic_candidate_v1'->>'intent_id'=$7 AND payload->'diagnostic_candidate_v1'->>'evidence_digest'=$8 ORDER BY id LIMIT 1`, s.expID, diagnosticCandidateOrigin, c.RunRef, c.CallRef, c.VulnClass, c.Name, strconv.FormatInt(c.IntentID, 10), c.EvidenceDigest).Scan(&existing)
	if err == nil {
		return existing, tx.Commit()
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

// ListDiagnosticCandidatesQuery는 보고서의 동일 repeatable-read 트랜잭션에 참여한다.
func ListDiagnosticCandidatesQuery(ctx context.Context, q DiagnosticCandidateQueryer, explorationID, taskID int64, runRef string, limit int) ([]DiagnosticCandidate, error) {
	if taskID <= 0 || limit <= 0 || limit > MaxDiagnosticCandidates+1 {
		return nil, ErrDiagnosticCandidate
	}
	rows, err := q.QueryContext(ctx, `SELECT n.id,n.payload FROM exploration_nodes n JOIN tasks t ON t.exploration_id=n.exploration_id WHERE t.id=$1 AND n.exploration_id=$2 AND n.origin=$3 AND n.kind='fact' AND ($4='' OR n.payload->'diagnostic_candidate_v1'->>'run_ref'=$4) ORDER BY n.id LIMIT $5`, taskID, explorationID, diagnosticCandidateOrigin, runRef, limit)
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
		if totalBytes > 16<<20 {
			return nil, ErrDiagnosticCandidate
		}
		var payload struct {
			Candidate DiagnosticCandidate `json:"diagnostic_candidate_v1"`
		}
		if json.Unmarshal(raw, &payload) != nil {
			return nil, ErrDiagnosticCandidate
		}
		c := payload.Candidate
		if c.ID != 0 || c.TaskID != taskID || (runRef != "" && c.RunRef != runRef) || c.Validate() != nil {
			return nil, ErrDiagnosticCandidate
		}
		c.ID = id
		out = append(out, c)
	}
	return out, rows.Err()
}
