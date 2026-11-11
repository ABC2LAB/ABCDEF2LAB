package db

import (
	"context"
	"encoding/json"
)

// DiagnosticReportCandidate preserves a bad row's identity without disclosing or
// trusting its payload. Execution/standard candidate reads retain strict refusal.
type DiagnosticReportCandidate struct {
	Candidate   DiagnosticCandidate
	Unavailable bool
}

func ListDiagnosticReportCandidates(ctx context.Context, q DiagnosticCandidateQueryer, exploration, task int64, limit int) ([]DiagnosticReportCandidate, error) {
	if task <= 0 || exploration <= 0 || limit <= 0 || limit > MaxDiagnosticCandidates+1 {
		return nil, ErrDiagnosticCandidate
	}
	rows, err := q.QueryContext(ctx, `SELECT n.id,CASE WHEN octet_length(n.payload::text)<=$5 THEN n.payload ELSE NULL END FROM exploration_nodes n JOIN tasks t ON t.exploration_id=n.exploration_id WHERE t.id=$1 AND n.exploration_id=$2 AND n.origin=$3 AND n.kind='fact' ORDER BY n.id LIMIT $4`, task, exploration, diagnosticCandidateOrigin, limit, 16<<20)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DiagnosticReportCandidate{}
	total := 0
	for rows.Next() {
		var id int64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var p struct {
			Candidate DiagnosticCandidate `json:"diagnostic_candidate_v1"`
		}
		invalid := len(raw) == 0 || json.Unmarshal(raw, &p) != nil || p.Candidate.ID != 0 || p.Candidate.TaskID != task || p.Candidate.Validate() != nil
		c := p.Candidate
		if invalid {
			c = DiagnosticCandidate{TaskID: task}
		}
		c.ID = id
		if !invalid {
			total += len(raw)
			if total > 16<<20 {
				return nil, ErrDiagnosticCandidate
			}
		}
		out = append(out, DiagnosticReportCandidate{Candidate: c, Unavailable: invalid})
	}
	return out, rows.Err()
}
