package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/notify"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/taskmode"
)

//go:embed e3_finding_schema.sql
var e3FindingSchemaSQL string

var (
	ErrControlledFindingInput    = errors.New("controlled_finding_input_invalid")
	ErrControlledFindingMode     = errors.New("controlled_finding_mode_rejected")
	ErrControlledFindingConflict = errors.New("controlled_finding_save_conflict")
	ErrControlledFindingStore    = errors.New("controlled_finding_store_unavailable")
	ErrControlledFindingGeneric  = errors.New("controlled_finding_requires_verified_path")
)

type ControlledFindingResult struct {
	ManifestID int64               `json:"manifest_id,string"`
	VerdictID  int64               `json:"verdict_id,string"`
	IssuanceID int64               `json:"issuance_id,string"`
	FindingID  int64               `json:"finding_id,string,omitempty"`
	NodeID     int64               `json:"finding_node_id,string,omitempty"`
	Value      oracle.VerdictValue `json:"oracle_value"`
	Created    bool                `json:"created"`
}

type controlledFindingStoreError struct{ cause error }

func (e controlledFindingStoreError) Error() string { return ErrControlledFindingStore.Error() }
func (e controlledFindingStoreError) Unwrap() error { return e.cause }
func (e controlledFindingStoreError) Is(target error) bool {
	return target == ErrControlledFindingStore
}
func (e controlledFindingStoreError) GoString() string {
	return "db.controlledFindingStoreError{cause:[REDACTED]}"
}

// RecordControlledFinding persists one host-prepared initial verdict. Only
// confirmed creates a finding. Non-confirmed verdicts remain structured records.
// It never sends HTTP or re-runs VerifyRead, and generic write paths do not call it.
func (d *DB) RecordControlledFinding(ctx context.Context, prepared e3record.Prepared, markers TaskModeMarkerReader) (out ControlledFindingResult, err error) {
	snapshot, valid := prepared.Snapshot()
	if ctx == nil || ctx.Err() != nil || !valid || markers == nil || d == nil || d.DB == nil {
		return out, ErrControlledFindingInput
	}
	err = d.WithEvidenceTx(ctx, func(tx *sql.Tx) error {
		if err := LockTaskEvidenceTx(tx, snapshot.TaskID); err != nil {
			return err
		}
		decision, modeErr := taskmode.Resolve(ctx, snapshot.TaskID, TaskModeReader(tx, markers))
		if modeErr != nil {
			return modeErr
		}
		if decision.Route() != taskmode.Controlled {
			return ErrControlledFindingMode
		}
		var modeVersion int64
		if err := tx.QueryRowContext(ctx, `SELECT version FROM task_control_modes WHERE task_id=$1`, snapshot.TaskID).Scan(&modeVersion); err != nil {
			return err
		}
		if modeVersion != int64(snapshot.Source.ModeVersion) {
			return ErrControlledFindingConflict
		}
		var intentKind string
		err := tx.QueryRowContext(ctx, `SELECT n.kind FROM tasks t JOIN exploration_nodes n ON n.exploration_id=t.exploration_id
WHERE t.id=$1 AND t.exploration_id=$2 AND n.id=$3 FOR UPDATE OF n`,
			snapshot.TaskID, snapshot.Source.ExplorationID, snapshot.Source.Expected.IntentID).Scan(&intentKind)
		if err != nil {
			return err
		}
		if intentKind != "intent" {
			return ErrControlledFindingInput
		}
		existing, found, err := readControlledOperation(ctx, tx, snapshot)
		if err != nil {
			return err
		}
		if found {
			out = existing
			return nil
		}
		var manifestID int64
		_, err = tx.ExecContext(ctx, `INSERT INTO e3_evidence_manifests(task_id,execution_ref,evidence_revision,canonical,digest)
VALUES($1,$2,$3,$4,$5) ON CONFLICT(task_id,execution_ref,evidence_revision) DO NOTHING`,
			snapshot.TaskID, snapshot.Source.Expected.Scope.RunRef, int64(snapshot.Source.EvidenceRevision),
			snapshot.Manifest, snapshot.ManifestDigest[:])
		if err != nil {
			return err
		}
		var manifest, digest []byte
		err = tx.QueryRowContext(ctx, `SELECT id,canonical,digest FROM e3_evidence_manifests
WHERE task_id=$1 AND execution_ref=$2 AND evidence_revision=$3 FOR UPDATE`,
			snapshot.TaskID, snapshot.Source.Expected.Scope.RunRef, int64(snapshot.Source.EvidenceRevision)).Scan(&manifestID, &manifest, &digest)
		if err != nil {
			return err
		}
		if !bytes.Equal(manifest, snapshot.Manifest) || !bytes.Equal(digest, snapshot.ManifestDigest[:]) {
			return ErrControlledFindingConflict
		}
		out.ManifestID, out.Value = manifestID, snapshot.Value
		err = tx.QueryRowContext(ctx, `INSERT INTO e3_oracle_verdicts(record_version,payload_canonical,payload,payload_digest,oracle_value)
VALUES(1,$1,$2::jsonb,$3,$4) RETURNING id`, snapshot.Canonical, string(snapshot.Canonical),
			snapshot.PayloadDigest[:], string(snapshot.Value)).Scan(&out.VerdictID)
		if err != nil {
			return err
		}
		source := snapshot.Source
		err = tx.QueryRowContext(ctx, `INSERT INTO e3_verdict_issuances(
verdict_id,manifest_id,task_id,exploration_id,intent_id,mode_version,purpose,execution_ref,case_ref,resource_ref,
eval_run_id,model_tag,reset_generation,kg_snapshot_ref,kg_policy_version,kg_snapshot_digest,harness_generation_ref,
target_instance_ref,operation_key,input_digest,binding_canonical)
VALUES($1,$2,$3,$4,$5,$6,'initial',$7,$8,$9,NULLIF($10,''),$11,$12,$13,$14,$15,$16,$17,$18,$19,$20) RETURNING id`,
			out.VerdictID, out.ManifestID, snapshot.TaskID, source.ExplorationID, source.Expected.IntentID,
			int64(source.ModeVersion), source.Expected.Scope.RunRef, source.Expected.Scope.CaseRef, source.Expected.ResourceRef,
			source.EvalRunID, source.ModelTag, source.ResetGeneration, source.KGSnapshotRef,
			source.Expected.KGPolicyVersion, source.KGSnapshotDigest[:], source.GenerationRef, source.InstanceRef,
			snapshot.OperationKey[:], snapshot.InputDigest[:], snapshot.Binding).Scan(&out.IssuanceID)
		if err != nil {
			return err
		}
		if snapshot.Value == oracle.Confirmed {
			if err := insertControlledFinding(ctx, tx, snapshot, &out); err != nil {
				return err
			}
		}
		out.Created = true
		return nil
	})
	if err != nil {
		out = ControlledFindingResult{}
		if errors.Is(err, ErrControlledFindingInput) || errors.Is(err, ErrControlledFindingMode) ||
			errors.Is(err, ErrControlledFindingConflict) {
			return out, err
		}
		return out, controlledFindingStoreError{err}
	}
	return out, nil
}

func readControlledOperation(ctx context.Context, tx *sql.Tx, s e3record.Snapshot) (out ControlledFindingResult, found bool, err error) {
	var digest []byte
	var findingID, nodeID sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT i.id,i.manifest_id,i.verdict_id,i.input_digest,v.oracle_value,l.finding_id,f.node_id
FROM e3_verdict_issuances i JOIN e3_oracle_verdicts v ON v.id=i.verdict_id
LEFT JOIN e3_verdict_links l ON l.issuance_id=i.id LEFT JOIN findings f ON f.id=l.finding_id
WHERE i.operation_key=$1 FOR UPDATE OF i`, s.OperationKey[:]).
		Scan(&out.IssuanceID, &out.ManifestID, &out.VerdictID, &digest, &out.Value, &findingID, &nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	if !bytes.Equal(digest, s.InputDigest[:]) {
		return out, false, ErrControlledFindingConflict
	}
	if out.Value == oracle.Confirmed && (!findingID.Valid || !nodeID.Valid) {
		return out, false, ErrControlledFindingConflict
	}
	out.FindingID, out.NodeID = findingID.Int64, nodeID.Int64
	return out, true, nil
}

func insertControlledFinding(ctx context.Context, tx *sql.Tx, s e3record.Snapshot, out *ControlledFindingResult) error {
	report, err := controlledFindingReport(s, *out)
	if err != nil {
		return err
	}
	const name = "Horizontal IDOR verified read"
	evidence := fmt.Sprintf("E3 verdict:%d issuance:%d manifest:%d", out.VerdictID, out.IssuanceID, out.ManifestID)
	payload, err := json.Marshal(map[string]any{"vulnclass": "IDOR", "name": name, "severity": s.Source.Severity,
		"summary":  "A controlled read produced a bound CONFIRMED oracle verdict.",
		"evidence": map[string]any{"by": "e3", "poc": evidence}, "e3_verdict_id": out.VerdictID, "e3_issuance_id": out.IssuanceID})
	if err != nil {
		return err
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO exploration_nodes(exploration_id,kind,payload,priority,state,origin)
VALUES($1,'finding',$2,9,'confirmed','e3') RETURNING id`, s.Source.ExplorationID, string(payload)).Scan(&out.NodeID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO exploration_edges(exploration_id,src_id,rel,dst_id) VALUES($1,$2,$3,$4)`,
		s.Source.ExplorationID, s.Source.Expected.IntentID, RelYields, out.NodeID)
	if err != nil {
		return err
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO findings(task_id,node_id,vulnclass,name,severity,summary,evidence,worker,asset_ids,status,evidence_version,report,report_evidence_version)
VALUES($1,$2,'IDOR',$3,$4,$5,$6,'e3','[]'::jsonb,'pending',1,$7,1) RETURNING id`,
		s.TaskID, out.NodeID, name, s.Source.Severity,
		"A controlled read produced a bound CONFIRMED oracle verdict.", evidence, report).Scan(&out.FindingID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO e3_verdict_links(issuance_id,task_id,finding_id,purpose,evidence_version)
VALUES($1,$2,$3,'initial',1)`, out.IssuanceID, s.TaskID, out.FindingID)
	if err != nil {
		return err
	}
	RecordNotificationEventTx(ctx, tx, notify.EventFindingCreated, out.FindingID, notify.Snapshot{
		Kind: notify.EventFindingCreated, FindingID: out.FindingID, TaskID: s.TaskID, VulnClass: "IDOR",
		Name: name, Severity: s.Source.Severity, Summary: "Bound CONFIRMED controlled read.", AssetIDs: []int64{},
	})
	return nil
}

// ControlledFindingRecord reads persisted immutable bytes; it does not re-run
// a request or claim that a historical KG snapshot is still available.
func (d *DB) ControlledFindingRecord(ctx context.Context, findingID int64) ([]byte, error) {
	var payload, digest []byte
	err := d.QueryRowContext(ctx, `SELECT v.payload_canonical,v.payload_digest FROM e3_verdict_links l
JOIN e3_verdict_issuances i ON i.id=l.issuance_id JOIN e3_oracle_verdicts v ON v.id=i.verdict_id
WHERE l.finding_id=$1`, findingID).Scan(&payload, &digest)
	if err != nil {
		return nil, controlledFindingStoreError{err}
	}
	actual := sha256.Sum256(payload)
	if !bytes.Equal(actual[:], digest) {
		return nil, ErrControlledFindingConflict
	}
	return payload, nil
}
