package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/remediation"
)

var ErrDiagnosticRemediation = errors.New("diagnostic_remediation_invalid")
var ErrDiagnosticRemediationStale = errors.New("diagnostic_remediation_source_changed")

const diagnosticRemediationOrigin = "diagnostic_remediation_v1"
const diagnosticRemediationByteLimit = 16 << 20

type DiagnosticRemediationQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
type DiagnosticRemediationRecord struct {
	ID        int64              `json:"id"`
	TaskID    int64              `json:"task_id"`
	EntryID   string             `json:"entry_id"`
	Source    remediation.Input  `json:"source"`
	Bundle    remediation.Bundle `json:"bundle"`
	CreatedAt time.Time          `json:"created_at"`
	Stale     bool               `json:"stale"`
}

func diagnosticEntryID(entry string) (string, int64, error) {
	parts := strings.Split(entry, "-")
	if len(parts) != 2 {
		return "", 0, ErrDiagnosticRemediation
	}
	if parts[0] != "candidate" && parts[0] != "finding" && parts[0] != "verdict" {
		return "", 0, ErrDiagnosticRemediation
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != parts[1] {
		return "", 0, ErrDiagnosticRemediation
	}
	return parts[0], id, nil
}
func diagnosticSourceHash(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func diagnosticVulnClass(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "idor", "bola":
		return "idor"
	case "sqli", "sql injection", "sql_injection":
		return "sql_injection"
	case "xss", "cross-site scripting":
		return "xss"
	case "ssrf":
		return "ssrf"
	case "security_headers", "security headers":
		return "security_headers"
	case "session_cookie", "session cookie":
		return "session_cookie"
	default:
		return "unknown"
	}
}
func diagnosticExecutionOutcome(s string) string {
	switch s {
	case "policy_not_executed", "unsupported", "forward_not_attempted", "isolation_unavailable":
		return "not_executed"
	case "result_unknown", "tool_failure", "transient_failure", "terminated":
		return "unknown"
	default:
		return "review"
	}
}

// LoadDiagnosticRemediationSource는 모델·HTTP 입력의 상태와 버전 대신 실제 저장 원본을 사용한다.
// 원문은 hash 계산에만 쓰고 생성기에 넘기지 않는다.
func LoadDiagnosticRemediationSource(ctx context.Context, q DiagnosticRemediationQueryer, taskID int64, entryID string) (remediation.Input, error) {
	in := remediation.Input{FindingID: entryID, Status: "review", VulnClass: "unknown"}
	kind, id, err := diagnosticEntryID(entryID)
	if err != nil || taskID <= 0 {
		return in, ErrDiagnosticRemediation
	}
	var raw string
	switch kind {
	case "candidate":
		err = q.QueryRowContext(ctx, `SELECT n.payload::text FROM tasks t JOIN exploration_nodes n ON n.exploration_id=t.exploration_id WHERE t.id=$1 AND t.deleted_at IS NULL AND n.id=$2 AND n.kind='fact' AND n.origin=$3 AND octet_length(n.payload::text)<=$4`, taskID, id, diagnosticCandidateOrigin, diagnosticRemediationByteLimit).Scan(&raw)
		if err != nil {
			return in, err
		}
		var p struct {
			Candidate DiagnosticCandidate `json:"diagnostic_candidate_v1"`
		}
		if json.Unmarshal([]byte(raw), &p) != nil || p.Candidate.TaskID != taskID || p.Candidate.Validate() != nil {
			return in, ErrDiagnosticRemediation
		}
		c := p.Candidate
		in.VulnClass = diagnosticVulnClass(c.VulnClass)
		in.Status = diagnosticExecutionOutcome(c.Classification)
		in.EvidenceVersion = strconv.FormatInt(c.EvidenceVersion, 10)
		in.EvidenceDigest = c.EvidenceDigest
		in.EvidenceCount = 1
		in.SourceDigest = diagnosticSourceHash([]any{taskID, entryID, json.RawMessage(raw)})
	case "finding":
		var evidenceVersion, reportVersion, linkedVersion int64
		var vulnclass, evidence, report, value string
		var manifest, manifestDigest, payload, payloadDigest []byte
		err = q.QueryRowContext(ctx, `SELECT to_jsonb(f)::text,f.evidence_version,f.report_evidence_version,f.vulnclass,f.evidence,COALESCE(f.report,''),COALESCE(l.evidence_version,0),COALESCE(v.oracle_value,''),COALESCE(m.canonical,''::bytea),COALESCE(m.digest,''::bytea),COALESCE(v.payload_canonical,''::bytea),COALESCE(v.payload_digest,''::bytea)
 FROM findings f JOIN tasks t ON t.id=f.task_id
 LEFT JOIN e3_verdict_links l ON l.finding_id=f.id AND l.task_id=f.task_id
 LEFT JOIN e3_verdict_issuances i ON i.id=l.issuance_id AND i.task_id=f.task_id
 LEFT JOIN e3_evidence_manifests m ON m.id=i.manifest_id AND m.task_id=f.task_id
 LEFT JOIN e3_oracle_verdicts v ON v.id=i.verdict_id
 WHERE f.task_id=$1 AND f.id=$2 AND t.deleted_at IS NULL AND octet_length(to_jsonb(f)::text)+COALESCE(octet_length(m.canonical),0)+COALESCE(octet_length(v.payload_canonical),0)<=$3`, taskID, id, diagnosticRemediationByteLimit).Scan(&raw, &evidenceVersion, &reportVersion, &vulnclass, &evidence, &report, &linkedVersion, &value, &manifest, &manifestDigest, &payload, &payloadDigest)
		if err != nil {
			return in, err
		}
		if evidenceVersion < 0 || reportVersion < -1 {
			return in, ErrDiagnosticRemediation
		}
		in.VulnClass = diagnosticVulnClass(vulnclass)
		in.EvidenceVersion = strconv.FormatInt(evidenceVersion, 10)
		in.EvidenceDigest = DiagnosticEvidenceDigest(evidence)
		if strings.TrimSpace(evidence) != "" {
			in.EvidenceCount = 1
		}
		if value != "" {
			mh, ph := sha256.Sum256(manifest), sha256.Sum256(payload)
			if !bytes.Equal(mh[:], manifestDigest) || !bytes.Equal(ph[:], payloadDigest) {
				return in, ErrDiagnosticRemediation
			}
			in.EvidenceDigest = hex.EncodeToString(manifestDigest)
			in.EvidenceCount = 1
			if value == "confirmed" && linkedVersion == evidenceVersion && (report == "" || reportVersion == evidenceVersion) {
				in.Status = "confirmed"
			}
		}
		in.SourceDigest = diagnosticSourceHash([]any{taskID, entryID, json.RawMessage(raw), linkedVersion, value, manifestDigest, payloadDigest})
	case "verdict":
		var version int64
		var value string
		var manifest, digest, payload, payloadDigest []byte
		err = q.QueryRowContext(ctx, `SELECT to_jsonb(i)::text,m.evidence_revision,v.oracle_value,m.canonical,m.digest,v.payload_canonical,v.payload_digest
 FROM e3_verdict_issuances i JOIN tasks t ON t.id=i.task_id JOIN e3_oracle_verdicts v ON v.id=i.verdict_id
 JOIN e3_evidence_manifests m ON m.id=i.manifest_id AND m.task_id=i.task_id
 LEFT JOIN e3_verdict_links l ON l.issuance_id=i.id
 WHERE i.task_id=$1 AND i.id=$2 AND t.deleted_at IS NULL AND l.id IS NULL AND octet_length(m.canonical)+octet_length(v.payload_canonical)<=$3`, taskID, id, diagnosticRemediationByteLimit).Scan(&raw, &version, &value, &manifest, &digest, &payload, &payloadDigest)
		if err != nil {
			return in, err
		}
		mh, ph := sha256.Sum256(manifest), sha256.Sum256(payload)
		if !bytes.Equal(mh[:], digest) || !bytes.Equal(ph[:], payloadDigest) || version <= 0 {
			return in, ErrDiagnosticRemediation
		}
		in.VulnClass = "idor"
		in.EvidenceVersion = strconv.FormatInt(version, 10)
		in.EvidenceDigest = hex.EncodeToString(digest)
		in.EvidenceCount = 1
		if value == "allowed" {
			in.Status = "negative"
		} else if value == "undecidable" {
			in.Status = "unknown"
		}
		in.SourceDigest = diagnosticSourceHash([]any{taskID, entryID, json.RawMessage(raw), value, digest, payloadDigest})
	}
	in.ReportVersion = "sha256:" + in.SourceDigest
	return in, nil
}

// SaveDiagnosticRemediation는 기존 보고서·E③ 판정·finding 상태를 수정하지 않는다.
// 명시적 사용자 기술 입력은 user_provided로만 수용하며 관측/모델 출처로 승격하지 않는다.
func (d *DB) SaveDiagnosticRemediation(ctx context.Context, taskID int64, entryID, expectedSourceDigest string, technologies []remediation.Technology) (DiagnosticRemediationRecord, error) {
	var out DiagnosticRemediationRecord
	kind, id, err := diagnosticEntryID(entryID)
	if err != nil || taskID <= 0 {
		return out, ErrDiagnosticRemediation
	}
	for _, t := range technologies {
		if t.Source != "user_provided" && t.Source != "unknown" {
			return out, ErrDiagnosticRemediation
		}
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if err := LockTaskEvidenceTx(tx, taskID); err != nil {
		return out, err
	}
	var expID int64
	if err := tx.QueryRowContext(ctx, `SELECT exploration_id FROM tasks WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, taskID).Scan(&expID); err != nil {
		return out, err
	}
	// 현재 원본을 잠근 뒤 버전을 재조회하여 생성 중 변경과 잘못된 고객/작업 귀속을 차단한다.
	switch kind {
	case "candidate":
		var locked int64
		err = tx.QueryRowContext(ctx, `SELECT id FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 AND origin=$3 FOR UPDATE`, id, expID, diagnosticCandidateOrigin).Scan(&locked)
	case "finding":
		var locked int64
		err = tx.QueryRowContext(ctx, `SELECT id FROM findings WHERE id=$1 AND task_id=$2 FOR UPDATE`, id, taskID).Scan(&locked)
	case "verdict":
		var locked int64
		err = tx.QueryRowContext(ctx, `SELECT id FROM e3_verdict_issuances WHERE id=$1 AND task_id=$2 FOR UPDATE`, id, taskID).Scan(&locked)
	}
	if err != nil {
		return out, err
	}
	source, err := LoadDiagnosticRemediationSource(ctx, tx, taskID, entryID)
	if err != nil {
		return out, err
	}
	if expectedSourceDigest == "" || source.SourceDigest != expectedSourceDigest {
		return out, ErrDiagnosticRemediationStale
	}
	source.Technologies = append([]remediation.Technology(nil), technologies...)
	bundle, err := remediation.Build(source)
	if err != nil {
		return out, err
	}
	source.Technologies = append([]remediation.Technology(nil), bundle.Technologies...)
	records, err := listDiagnosticRemediationRecords(ctx, tx, taskID, entryID, 1, false)
	if err != nil {
		return out, err
	}
	if len(records) == 1 && records[0].Bundle.InputDigest == bundle.InputDigest {
		return records[0], tx.Commit()
	}
	out = DiagnosticRemediationRecord{TaskID: taskID, EntryID: entryID, Source: source, Bundle: bundle, CreatedAt: time.Now().UTC()}
	raw, err := json.Marshal(map[string]any{"summary": "Stored remediation proposal; not applied", "diagnostic_remediation_v1": out})
	if err != nil || len(raw) > 256<<10 {
		return DiagnosticRemediationRecord{}, ErrDiagnosticRemediation
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO exploration_nodes(exploration_id,kind,payload,priority,state,origin) VALUES($1,'fact',$2,5,'confirmed',$3) RETURNING id`, expID, string(raw), diagnosticRemediationOrigin).Scan(&out.ID)
	if err != nil {
		return DiagnosticRemediationRecord{}, err
	}
	return out, tx.Commit()
}

func ListDiagnosticRemediationRecords(ctx context.Context, q DiagnosticRemediationQueryer, taskID int64, limit int) ([]DiagnosticRemediationRecord, error) {
	return listDiagnosticRemediationRecords(ctx, q, taskID, "", limit, true)
}
func listDiagnosticRemediationRecords(ctx context.Context, q DiagnosticRemediationQueryer, taskID int64, entryID string, limit int, checkCurrent bool) ([]DiagnosticRemediationRecord, error) {
	if taskID <= 0 || limit <= 0 || limit > MaxDiagnosticCandidates+1 {
		return nil, ErrDiagnosticRemediation
	}
	var count, totalBytes, maxBytes int64
	err := q.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(octet_length(payload::text)),0),COALESCE(max(octet_length(payload::text)),0) FROM (
 SELECT DISTINCT ON (n.payload->'diagnostic_remediation_v1'->>'entry_id') n.id,n.payload
 FROM exploration_nodes n JOIN tasks t ON t.exploration_id=n.exploration_id
 WHERE t.id=$1 AND t.deleted_at IS NULL AND n.origin=$2 AND n.kind='fact' AND ($3='' OR n.payload->'diagnostic_remediation_v1'->>'entry_id'=$3)
 ORDER BY n.payload->'diagnostic_remediation_v1'->>'entry_id',n.id DESC) latest`, taskID, diagnosticRemediationOrigin, entryID).Scan(&count, &totalBytes, &maxBytes)
	if err != nil {
		return nil, err
	}
	if count > int64(limit) || totalBytes > diagnosticRemediationByteLimit || maxBytes > 256<<10 {
		return nil, ErrDiagnosticRemediation
	}
	rows, err := q.QueryContext(ctx, `SELECT id,payload::text FROM (SELECT DISTINCT ON (n.payload->'diagnostic_remediation_v1'->>'entry_id') n.id,n.payload
 FROM exploration_nodes n JOIN tasks t ON t.exploration_id=n.exploration_id
 WHERE t.id=$1 AND t.deleted_at IS NULL AND n.origin=$2 AND n.kind='fact' AND ($3='' OR n.payload->'diagnostic_remediation_v1'->>'entry_id'=$3)
 ORDER BY n.payload->'diagnostic_remediation_v1'->>'entry_id',n.id DESC) latest ORDER BY id LIMIT $4`, taskID, diagnosticRemediationOrigin, entryID, limit)
	if err != nil {
		return nil, err
	}
	out := []DiagnosticRemediationRecord{}
	total := 0
	for rows.Next() {
		var id int64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		total += len(raw)
		if total > diagnosticRemediationByteLimit || len(raw) > 256<<10 {
			rows.Close()
			return nil, ErrDiagnosticRemediation
		}
		var p struct {
			Record DiagnosticRemediationRecord `json:"diagnostic_remediation_v1"`
		}
		if json.Unmarshal(raw, &p) != nil {
			rows.Close()
			return nil, ErrDiagnosticRemediation
		}
		r := p.Record
		if r.ID != 0 || r.TaskID != taskID || r.Stale || r.CreatedAt.IsZero() || r.EntryID != r.Source.FindingID || r.Bundle.FindingID != r.EntryID {
			rows.Close()
			return nil, ErrDiagnosticRemediation
		}
		expected, err := remediation.Build(r.Source)
		if err != nil {
			rows.Close()
			return nil, ErrDiagnosticRemediation
		}
		r.Source.Technologies = append([]remediation.Technology(nil), expected.Technologies...)
		if r.Bundle.RemediationVersion != remediation.GeneratorVersion {
			// 이전 생성기의 본문을 현재 유효한 작업 지시로 내보내지 않는다.
			// 자동 재생성/쓰기는 하지 않고 명시적 재생성을 기다린다.
			r.Bundle = remediation.Bundle{FindingID: r.EntryID}
			r.Stale = true
		} else {
			a, _ := json.Marshal(expected)
			b, _ := json.Marshal(r.Bundle)
			if !bytes.Equal(a, b) {
				rows.Close()
				return nil, ErrDiagnosticRemediation
			}
		}
		r.ID = id
		out = append(out, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if checkCurrent {
		for i := range out {
			current, err := LoadDiagnosticRemediationSource(ctx, q, taskID, out[i].EntryID)
			if errors.Is(err, sql.ErrNoRows) {
				out[i].Stale = true
				out[i].Bundle = remediation.Bundle{FindingID: out[i].EntryID}
				continue
			}
			if err != nil {
				return nil, err
			}
			current.Technologies = out[i].Source.Technologies
			out[i].Stale = out[i].Stale || remediation.IsStale(out[i].Bundle, current)
			if out[i].Stale {
				out[i].Bundle = remediation.Bundle{FindingID: out[i].EntryID}
			}
		}
	}
	return out, nil
}

// GetDiagnosticRemediationRecord는 복사 시 해당 결과의 최신 버전만 다시 검증한다.
func GetDiagnosticRemediationRecord(ctx context.Context, q DiagnosticRemediationQueryer, taskID int64, entryID string) (*DiagnosticRemediationRecord, error) {
	if _, _, err := diagnosticEntryID(entryID); err != nil {
		return nil, err
	}
	records, err := listDiagnosticRemediationRecords(ctx, q, taskID, entryID, 1, true)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	return &records[0], nil
}
