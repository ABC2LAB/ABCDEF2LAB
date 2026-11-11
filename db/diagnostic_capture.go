package db

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"github.com/Autumn-27/artex/reportcapture"
	"github.com/Autumn-27/artex/reproduction"
	"strconv"
)

//go:embed diagnostic_capture_v1.sql
var diagnosticCaptureSchemaSQL string
var ErrDiagnosticCapture = errors.New("diagnostic_capture_binding_invalid")

// 공개 업로드 API는 없다. 인가된 실행기의 사적 캡처 hook만 이 함수를 사용한다.
func (d *DB) SaveDiagnosticCapture(ctx context.Context, a reportcapture.Asset) error {
	b, c, mask, valid := a.Snapshot()
	if !valid {
		return ErrDiagnosticCapture
	}
	if ctx == nil || ctx.Err() != nil || b.TaskID <= 0 || b.Step.TaskID != b.TaskID || b.Step.EntryID != b.EntryID || reproduction.Validate(b.Step, b.Step.EvidenceVersion, b.Step.EvidenceDigest) != nil {
		return ErrDiagnosticCapture
	}
	tx, e := d.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT id FROM tasks WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, b.TaskID); e != nil {
		return e
	}
	source, e := LoadDiagnosticRemediationSource(ctx, tx, b.TaskID, b.EntryID)
	if errors.Is(e, sql.ErrNoRows) {
		return ErrDiagnosticCapture
	}
	if e != nil {
		return e
	}
	matched := false
	if b.Step.EvidenceDigest != source.EvidenceDigest {
		return ErrDiagnosticCapture
	}
	for _, step := range reproduction.FromStored(b.TaskID, b.EntryID, b.Step.EvidenceVersion, source.EvidenceDigest, source.Context) {
		if step.BindingDigest == b.Step.BindingDigest && step.ID == b.Step.ID && step.RunRef == b.Step.RunRef && step.CallRef == b.Step.CallRef && source.EvidenceVersion == strconv.FormatInt(b.Step.EvidenceVersion, 10) {
			matched = true
		}
	}
	if !matched {
		return ErrDiagnosticCapture
	}
	if c.Status == "captured" {
		if mask != "approved_regions/v1" || reportcapture.Verify(c) != nil || c.Ref != "shot-"+b.Step.BindingDigest[:24] {
			return ErrDiagnosticCapture
		}
	} else if (c.Status != "not_supported" && c.Status != "failed") || len(c.PNG) != 0 || mask != "" {
		return ErrDiagnosticCapture
	}
	png := c.PNG
	if png == nil {
		png = []byte{}
	}
	var at any
	if !c.CapturedAt.IsZero() {
		at = c.CapturedAt
	}
	result, e := tx.ExecContext(ctx, `INSERT INTO diagnostic_captures_v1(task_id,entry_id,step_id,binding_digest,evidence_version,evidence_digest,run_ref,call_ref,capture_ref,state,png,image_digest,width,height,captured_at,mask_policy) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16) ON CONFLICT(task_id,entry_id,step_id,binding_digest) DO UPDATE SET
 capture_ref=EXCLUDED.capture_ref,state=EXCLUDED.state,png=EXCLUDED.png,image_digest=EXCLUDED.image_digest,
 width=EXCLUDED.width,height=EXCLUDED.height,captured_at=EXCLUDED.captured_at,mask_policy=EXCLUDED.mask_policy
 WHERE diagnostic_captures_v1.state<>'captured' AND EXCLUDED.state='captured'`, b.TaskID, b.EntryID, b.Step.ID, b.Step.BindingDigest, b.Step.EvidenceVersion, b.Step.EvidenceDigest, b.Step.RunRef, b.Step.CallRef, c.Ref, c.Status, png, c.Digest, c.Width, c.Height, at, mask)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		if c.Status != "captured" {
			var state string
			if err := tx.QueryRowContext(ctx, `SELECT state FROM diagnostic_captures_v1 WHERE task_id=$1 AND entry_id=$2 AND step_id=$3 AND binding_digest=$4`, b.TaskID, b.EntryID, b.Step.ID, b.Step.BindingDigest).Scan(&state); err != nil {
				return err
			}
			if state != "captured" {
				return tx.Commit()
			}
		}
		return ErrDiagnosticCapture
	}
	return tx.Commit()
}
func ReadDiagnosticCapture(ctx context.Context, q DiagnosticRemediationQueryer, task int64, entry string, step reproduction.Step) (reproduction.Capture, error) {
	return readDiagnosticCapture(ctx, q, task, entry, step, true)
}

// 메타데이터 조회는 PNG blob을 가져오지 않는다. 명시적 이미지 조회에서 바이트를 검증한다.
func ReadDiagnosticCaptureMetadata(ctx context.Context, q DiagnosticRemediationQueryer, task int64, entry string, step reproduction.Step) (reproduction.Capture, int64, error) {
	c, err := readDiagnosticCapture(ctx, q, task, entry, step, false)
	if err != nil {
		return c, 0, err
	}
	var n int64
	if c.Status == "captured" {
		err = q.QueryRowContext(ctx, `SELECT octet_length(png) FROM diagnostic_captures_v1 WHERE task_id=$1 AND entry_id=$2 AND step_id=$3 AND binding_digest=$4`, task, entry, step.ID, step.BindingDigest).Scan(&n)
	}
	return c, n, err
}
func readDiagnosticCapture(ctx context.Context, q DiagnosticRemediationQueryer, task int64, entry string, step reproduction.Step, pixels bool) (reproduction.Capture, error) {
	var c reproduction.Capture
	if reproduction.Validate(step, step.EvidenceVersion, step.EvidenceDigest) != nil {
		return c, ErrDiagnosticCapture
	}
	var at sql.NullTime
	var policy, run, call, digest string
	var version int64
	e := q.QueryRowContext(ctx, `SELECT state,capture_ref,image_digest,captured_at,width,height,CASE WHEN $5 AND octet_length(png)<=$6 THEN png ELSE NULL END,mask_policy,run_ref,call_ref,evidence_version,evidence_digest FROM diagnostic_captures_v1 WHERE task_id=$1 AND entry_id=$2 AND step_id=$3 AND binding_digest=$4`, task, entry, step.ID, step.BindingDigest, pixels, reportcapture.MaxPNGBytes).Scan(&c.Status, &c.Ref, &c.Digest, &at, &c.Width, &c.Height, &c.PNG, &policy, &run, &call, &version, &digest)
	if e == sql.ErrNoRows {
		return reproduction.Capture{Status: "not_captured"}, nil
	}
	if e != nil {
		return c, e
	}
	if version != step.EvidenceVersion || digest != step.EvidenceDigest || run != step.RunRef || call != step.CallRef {
		return c, ErrDiagnosticCapture
	}
	if at.Valid {
		c.CapturedAt = at.Time
	}
	if c.Status == "captured" && (policy != "approved_regions/v1" || c.Ref != "shot-"+step.BindingDigest[:24] || (pixels && reportcapture.Verify(c) != nil)) {
		return c, ErrDiagnosticCapture
	}
	return c, nil
}
