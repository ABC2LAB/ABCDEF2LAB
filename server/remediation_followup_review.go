package server

import (
	"context"
	"database/sql"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"
)

// 판정은 조회 권한을 확인한 담당자가 당시 조건과 기준을 직접 대조한 결과다.
type followupReview struct {
	EvidenceEntry string    `json:"evidence_entry_id,omitempty"`
	StepID        string    `json:"step_id,omitempty"`
	BindingDigest string    `json:"binding_digest,omitempty"`
	Reviewer      string    `json:"reviewer"`
	Revision      int64     `json:"revision"`
	ReviewedAt    time.Time `json:"reviewed_at"`
}

type followupEdit struct {
	Entry            string `json:"entry_id"`
	Record           string `json:"record_id"`
	OriginalDigest   string `json:"original_digest"`
	EvidenceEntry    string `json:"evidence_entry_id,omitempty"`
	StepID           string `json:"step_id,omitempty"`
	BindingDigest    string `json:"binding_digest,omitempty"`
	Outcome          string `json:"outcome"`
	Reason           string `json:"reason,omitempty"`
	Comparable       bool   `json:"conditions_comparable"`
	CriteriaMet      bool   `json:"all_criteria_met"`
	ExpectedRevision *int64 `json:"expected_revision"`
}

func readFollowupEdit(w http.ResponseWriter, r *http.Request) *followupEdit {
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		return nil
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
	var edit followupEdit
	if err != nil || strictJSON(raw, &edit) != nil || edit.ExpectedRevision == nil || *edit.ExpectedRevision < 0 {
		return nil
	}
	switch edit.Outcome {
	case "fixed", "reproduced":
		if !edit.Comparable || edit.CriteriaMet != (edit.Outcome == "fixed") || edit.Reason != "" || edit.StepID == "" {
			return nil
		}
	case "inconclusive":
		if edit.CriteriaMet {
			return nil
		}
		switch edit.Reason {
		case "insufficient_evidence", "outside_scope", "conditions_changed", "access_unavailable":
		default:
			return nil
		}
	default:
		return nil
	}
	if edit.Entry == "" || edit.Record == "" || edit.OriginalDigest == "" || ((edit.EvidenceEntry == "") != (edit.StepID == "")) || ((edit.BindingDigest == "") != (edit.StepID == "")) {
		return nil
	}
	return &edit
}

type followupBinding struct{ entry, step, digest string }

func (run followupRun) bindings() map[followupBinding]bool {
	out := map[followupBinding]bool{}
	for _, f := range run.Findings {
		for _, s := range f.Steps {
			out[followupBinding{f.Entry, s.ID, s.Digest}] = s.Reviewable
		}
	}
	return out
}

func saveFollowupReview(ctx context.Context, pool *sql.DB, q followupQuery, page followupDraft, owner string, edit followupEdit) int {
	valid := false
	for _, i := range page.Items {
		if i.Entry == edit.Entry && i.Record == edit.Record && i.Digest == edit.OriginalDigest && len(i.Criteria) > 0 {
			valid = true
			break
		}
	}
	if !valid || !page.Current.Ready {
		return http.StatusConflict
	}
	if edit.StepID != "" {
		reviewable, exists := page.Current.bindings()[followupBinding{edit.EvidenceEntry, edit.StepID, edit.BindingDigest}]
		if !exists || edit.Outcome != "inconclusive" && !reviewable {
			return http.StatusConflict
		}
	}
	record, _ := verdictDetailID(edit.Record)
	args := []any{q.source, q.current, record, edit.OriginalDigest, edit.EvidenceEntry, edit.StepID, edit.BindingDigest, edit.Outcome, edit.Reason, owner}
	var result sql.Result
	var err error
	if *edit.ExpectedRevision == 0 {
		result, err = pool.ExecContext(ctx, `INSERT INTO remediation_followup_reviews
			(source_task,current_task,record_id,original_digest,evidence_entry,step_id,binding_digest,outcome,reason,reviewer)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING`, args...)
	} else {
		result, err = pool.ExecContext(ctx, `UPDATE remediation_followup_reviews SET original_digest=$4,evidence_entry=$5,step_id=$6,
			binding_digest=$7,outcome=$8,reason=$9,reviewer=$10,revision=revision+1,reviewed_at=now()
			WHERE source_task=$1 AND current_task=$2 AND record_id=$3 AND revision=$11`, append(args, *edit.ExpectedRevision)...)
	}
	if err != nil {
		return http.StatusServiceUnavailable
	}
	n, err := result.RowsAffected()
	if err != nil {
		return http.StatusServiceUnavailable
	}
	if n == 1 {
		return http.StatusOK
	}
	// 응답을 잃고 같은 요청을 다시 보낸 경우만 성공으로 돌려준다.
	var replay bool
	err = pool.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM remediation_followup_reviews
		WHERE source_task=$1 AND current_task=$2 AND record_id=$3 AND original_digest=$4 AND evidence_entry=$5
		AND step_id=$6 AND binding_digest=$7 AND outcome=$8 AND reason=$9 AND reviewer=$10 AND revision-1=$11)`, append(args, *edit.ExpectedRevision)...).Scan(&replay)
	if err != nil {
		return http.StatusServiceUnavailable
	}
	if replay {
		return http.StatusOK
	}
	return http.StatusConflict
}

func applyFollowupReviews(ctx context.Context, pool *sql.DB, q followupQuery, page *followupDraft) error {
	if len(page.Items) == 0 {
		return nil
	}
	byRecord := map[int64]*followupItem{}
	ids := make([]int64, 0, len(page.Items))
	for i := range page.Items {
		id, _ := strconv.ParseInt(page.Items[i].Record, 10, 64)
		ids = append(ids, id)
		byRecord[id] = &page.Items[i]
	}
	rows, err := pool.QueryContext(ctx, `SELECT record_id,original_digest,evidence_entry,step_id,binding_digest,outcome,reason,reviewer,revision,reviewed_at
		FROM remediation_followup_reviews WHERE source_task=$1 AND current_task=$2 AND record_id=ANY($3::bigint[])`, q.source, q.current, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	valid := page.Current.bindings()
	for rows.Next() {
		var id int64
		var digest, outcome, reason string
		var r followupReview
		if err := rows.Scan(&id, &digest, &r.EvidenceEntry, &r.StepID, &r.BindingDigest, &outcome, &reason, &r.Reviewer, &r.Revision, &r.ReviewedAt); err != nil {
			return err
		}
		item := byRecord[id]
		_, exists := valid[followupBinding{r.EvidenceEntry, r.StepID, r.BindingDigest}]
		if digest == "" || digest != item.Digest || (r.StepID == "" && outcome != "inconclusive") || (r.StepID != "" && !exists) {
			item.Reason = "review_evidence_changed"
			item.Review = &followupReview{Revision: r.Revision, Reviewer: r.Reviewer, ReviewedAt: r.ReviewedAt}
			continue
		}
		item.Review = &r
		item.Result = outcome
		item.Reason = reason
	}
	return rows.Err()
}

// 일부 원래 조치만 표시해도 다른 조치에 연결된 관찰을 추가 발견으로 세지 않는다.
func (p *followupDraft) partition(selected map[string]bool) {
	linked := map[followupReference]bool{}
	visible := make([]followupItem, 0, len(p.Items))
	for _, i := range p.Items {
		if i.Review != nil && i.Review.EvidenceEntry != "" {
			linked[followupReference{p.Current.Task, i.Review.EvidenceEntry}] = true
		}
		if len(selected) == 0 || selected[i.Entry] {
			visible = append(visible, i)
		}
	}
	p.Items = visible
	p.Additional = []followupReference{}
	for _, run := range []*followupRun{&p.Current, &p.General} {
		for _, f := range run.Findings {
			ref := followupReference{run.Task, f.Entry}
			if !linked[ref] {
				p.Additional = append(p.Additional, ref)
				linked[ref] = true
			}
		}
	}
}
