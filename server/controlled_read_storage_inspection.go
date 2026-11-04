package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"
)

const controlledStorageInspectionTimeout = 3 * time.Second

var ErrControlledReadStorageInspection = errors.New("controlled_read_storage_inspection_unavailable")

// ControlledReadStorageReport contains catalog facts on one inspected pool
// connection. It grants no permission and has no aggregate ready/success flag.
type ControlledReadStorageReport struct {
	ResolutionBasis string                          `json:"resolution_basis"`
	Relations       []ControlledReadStorageRelation `json:"relations"`
	Unassessed      []string                        `json:"unassessed"`
}

type ControlledReadStorageRelation struct {
	Name string `json:"name"`
	// A same-name catalog entry can exist outside the usable search_path.
	NameObservedInCatalog bool                             `json:"name_observed_in_catalog"`
	Resolved              bool                             `json:"resolved"`
	OrdinaryTable         *bool                            `json:"ordinary_table,omitempty"`
	Columns               []ControlledReadStorageColumn    `json:"columns,omitempty"`
	TablePrivileges       []ControlledReadStoragePrivilege `json:"table_privileges,omitempty"`
}

type ControlledReadStorageColumn struct {
	Name               string `json:"name"`
	Present            bool   `json:"present"`
	BuiltinTypeMatches bool   `json:"builtin_type_matches"`
}

type ControlledReadStoragePrivilege struct {
	Privilege string `json:"privilege"`
	Granted   bool   `json:"granted"`
}

type storageInspectionFailure string

func (e storageInspectionFailure) Error() string {
	return ErrControlledReadStorageInspection.Error() + ":" + string(e)
}
func (e storageInspectionFailure) Unwrap() error { return ErrControlledReadStorageInspection }

func storageInspectionError(ctx context.Context, stage string) error {
	if ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return storageInspectionFailure("deadline")
	}
	if ctx != nil && ctx.Err() != nil {
		return storageInspectionFailure("cancelled")
	}
	return storageInspectionFailure(stage)
}

// Keep diagnostics useful without retaining driver errors, which may contain
// SQL, schema names, credentials or server messages. Only a validated SQLSTATE
// and a fixed call-site stage enter the log; public errors remain unchanged.
func storageInspectionDriverError(ctx context.Context, stage string, cause error) error {
	state := "unavailable"
	var coded interface{ SQLState() string }
	if errors.As(cause, &coded) {
		candidate := coded.SQLState()
		valid := len(candidate) == 5
		for i := 0; i < len(candidate); i++ {
			if !(candidate[i] >= '0' && candidate[i] <= '9' || candidate[i] >= 'A' && candidate[i] <= 'Z') {
				valid = false
			}
		}
		if valid {
			state = candidate
		}
	}
	log.Printf("[controlled-read] storage_inspection_failed stage=%s sqlstate=%s", stage, state)
	return storageInspectionError(ctx, stage)
}

type storageInspectionObject struct{ name, columns string }

// The inventory covers direct CPD/revision and initial E3 SQL plus the base
// relations those queries use. Defaults/constraints/triggers/indirect writes are
// deliberately not inferred from a matching name and built-in type OID.
func storageInspectionObjects() []storageInspectionObject {
	return []storageInspectionObject{
		{"audit_checkpoint_epochs", "store_ref:text epoch_ref:text generation:int8 anchor_digest:bytea"},
		{"audit_checkpoint_history", "store_ref:text run_ref:text revision:int8 epoch_ref:text generation:int8 anchor_digest:bytea publisher_ref:text phase:text checkpoint:jsonb recorded_at:timestamptz"},
		{"audit_checkpoint_heads", "store_ref:text run_ref:text epoch_ref:text journal_id:text revision:int8"},
		{"audit_checkpoint_operations", "store_ref:text epoch_ref:text operation_ref:text request_digest:bytea receipt_ref:text run_ref:text revision:int8 committed_at:timestamptz"},
		{"evidence_revision_initials", "revision:int8 execution_ref:text purpose:text task_id:int8 exploration_id:int8 intent_id:int8 mode_version:int8 binding:bytea input_digest:bytea evidence_digest:bytea issued_at:timestamptz"},
		{"evidence_revision_operations", "operation_ref:text revision:int8 request_digest:bytea recorded_at:timestamptz"},
		{"e3_evidence_manifests", "id:int8 task_id:int8 execution_ref:text evidence_revision:int8 canonical:bytea digest:bytea"},
		{"e3_oracle_verdicts", "id:int8 record_version:int2 payload_canonical:bytea payload:jsonb payload_digest:bytea oracle_value:text"},
		{"e3_verdict_issuances", "id:int8 verdict_id:int8 manifest_id:int8 task_id:int8 exploration_id:int8 intent_id:int8 mode_version:int8 purpose:text execution_ref:text case_ref:text resource_ref:text eval_run_id:text model_tag:text reset_generation:text kg_snapshot_ref:text kg_policy_version:text kg_snapshot_digest:bytea harness_generation_ref:text target_instance_ref:text operation_key:bytea input_digest:bytea binding_canonical:bytea"},
		{"e3_verdict_links", "id:int8 issuance_id:int8 task_id:int8 finding_id:int8 purpose:text evidence_version:int8"},
		{"tasks", "id:int8 exploration_id:int8 deleted_at:timestamptz"},
		{"task_archives", "task_id:int8 state:text"},
		{"explorations", "id:int8"},
		{"task_control_modes", "task_id:int8 mode:text version:int8"},
		{"exploration_nodes", "id:int8 exploration_id:int8 kind:text payload:jsonb priority:int4 state:text origin:text"},
		{"exploration_edges", "exploration_id:int8 src_id:int8 rel:text dst_id:int8"},
		{"findings", "id:int8 task_id:int8 node_id:int8 vulnclass:text name:text severity:text summary:text evidence:text worker:text asset_ids:jsonb status:text evidence_version:int8"},
	}
}

// InspectControlledReadStorage is trusted Go diagnostics only. It does not
// register/resume a task, acquire execution/key authority, migrate, read payload
// rows or modify application data. No startup/HTTP/CLI path calls it implicitly.
// Relation resolution uses this transaction connection's actual search_path;
// the result does not attest other pool connections, a trusted path or future
// query success. Unresolved does not mean absent or permission denied.
func (s *Server) InspectControlledReadStorage(ctx context.Context) (out ControlledReadStorageReport, err error) {
	if ctx == nil || s == nil || s.m == nil || s.m.pg == nil || s.m.pg.DB == nil {
		return out, storageInspectionFailure("configuration")
	}
	ctx, cancel := context.WithTimeout(ctx, controlledStorageInspectionTimeout)
	defer cancel()
	if ctx.Err() != nil {
		return out, storageInspectionError(ctx, "transaction")
	}
	tx, err := s.m.pg.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return out, storageInspectionDriverError(ctx, "transaction", err)
	}
	defer func() {
		rollbackErr := tx.Rollback()
		if err == nil && rollbackErr != nil {
			out, err = ControlledReadStorageReport{}, storageInspectionDriverError(ctx, "rollback", rollbackErr)
		}
	}()
	out.ResolutionBasis = "inspected_connection_search_path"
	out.Unassessed = []string{"execution_authority", "host_root_epoch_key_trust", "constraints_defaults_indexes_triggers", "column_privileges_rls_security_definer", "sequence_access", "type_modifiers_domains_semantics", "search_path_trust_and_other_connections", "mode_archive_and_other_helper_dependencies", "indirect_notification_storage", "durability_and_future_execution"}
	for _, spec := range storageInspectionObjects() {
		fact, queryErr := inspectStorageRelation(ctx, tx, spec)
		if queryErr != nil {
			return ControlledReadStorageReport{}, queryErr
		}
		out.Relations = append(out.Relations, fact)
	}
	if ctx.Err() != nil {
		return ControlledReadStorageReport{}, storageInspectionError(ctx, "complete")
	}
	return out, nil
}

const storageInspectionRelationSQL = `WITH resolved AS (
 SELECT pg_catalog.to_regclass($1)::pg_catalog.oid AS oid
), classified AS (
 SELECT r.oid,c.relkind,
 (c.relkind::pg_catalog.text OPERATOR(pg_catalog.=) 'r'::pg_catalog.text OR
  c.relkind::pg_catalog.text OPERATOR(pg_catalog.=) 'p'::pg_catalog.text OR
  c.relkind::pg_catalog.text OPERATOR(pg_catalog.=) 'v'::pg_catalog.text OR
  c.relkind::pg_catalog.text OPERATOR(pg_catalog.=) 'm'::pg_catalog.text OR
  c.relkind::pg_catalog.text OPERATOR(pg_catalog.=) 'f'::pg_catalog.text) AS table_acl_kind
 FROM resolved r LEFT JOIN pg_catalog.pg_class c ON c.oid OPERATOR(pg_catalog.=) r.oid
)
SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_class all_names WHERE all_names.relname::pg_catalog.text OPERATOR(pg_catalog.=) $1::pg_catalog.text),
 c.oid IS NOT NULL, c.oid,
 CASE WHEN c.oid IS NULL THEN NULL ELSE c.relkind::pg_catalog.text OPERATOR(pg_catalog.=) 'r'::pg_catalog.text END,
 CASE WHEN c.table_acl_kind THEN pg_catalog.has_table_privilege(c.oid,'SELECT') ELSE NULL END,
 CASE WHEN c.table_acl_kind THEN pg_catalog.has_table_privilege(c.oid,'INSERT') ELSE NULL END,
 CASE WHEN c.table_acl_kind THEN pg_catalog.has_table_privilege(c.oid,'UPDATE') ELSE NULL END
FROM classified c`

const storageInspectionColumnsSQL = `SELECT w.name, a.attnum IS NOT NULL,
 COALESCE(a.atttypid OPERATOR(pg_catalog.=) pg_catalog.to_regtype(w.builtin)::pg_catalog.oid, false)
FROM pg_catalog.jsonb_to_recordset($2::pg_catalog.jsonb) AS w(name pg_catalog.text,builtin pg_catalog.text,ordinal pg_catalog.int4)
LEFT JOIN pg_catalog.pg_attribute a ON a.attrelid OPERATOR(pg_catalog.=) $1::pg_catalog.oid
 AND a.attname::pg_catalog.text OPERATOR(pg_catalog.=) w.name
 AND a.attnum OPERATOR(pg_catalog.>) 0::pg_catalog.int2 AND NOT a.attisdropped
ORDER BY w.ordinal USING OPERATOR(pg_catalog.<)`

func inspectStorageRelation(ctx context.Context, tx *sql.Tx, spec storageInspectionObject) (fact ControlledReadStorageRelation, err error) {
	fact.Name = spec.name
	var oid sql.NullInt64
	var ordinary, selectAccess, insertAccess, updateAccess sql.NullBool
	if err := tx.QueryRowContext(ctx, storageInspectionRelationSQL, spec.name).Scan(&fact.NameObservedInCatalog, &fact.Resolved, &oid, &ordinary, &selectAccess, &insertAccess, &updateAccess); err != nil {
		return ControlledReadStorageRelation{}, storageInspectionDriverError(ctx, "catalog", err)
	}
	if !fact.Resolved {
		return fact, nil
	}
	if !oid.Valid || !ordinary.Valid {
		return ControlledReadStorageRelation{}, storageInspectionFailure("catalog")
	}
	fact.OrdinaryTable = &ordinary.Bool
	for i, access := range []sql.NullBool{selectAccess, insertAccess, updateAccess} {
		if access.Valid {
			fact.TablePrivileges = append(fact.TablePrivileges, ControlledReadStoragePrivilege{Privilege: []string{"SELECT", "INSERT", "UPDATE"}[i], Granted: access.Bool})
		}
	}
	type columnSpec struct {
		Name    string `json:"name"`
		Builtin string `json:"builtin"`
		Ordinal int    `json:"ordinal"`
	}
	var columns []columnSpec
	for i, entry := range strings.Fields(spec.columns) {
		pair := strings.SplitN(entry, ":", 2)
		columns = append(columns, columnSpec{pair[0], "pg_catalog." + pair[1], i})
	}
	encoded, marshalErr := json.Marshal(columns)
	if marshalErr != nil {
		return ControlledReadStorageRelation{}, storageInspectionFailure("columns")
	}
	rows, queryErr := tx.QueryContext(ctx, storageInspectionColumnsSQL, oid.Int64, encoded)
	if queryErr != nil {
		return ControlledReadStorageRelation{}, storageInspectionDriverError(ctx, "columns", queryErr)
	}
	defer func() {
		if closeErr := rows.Close(); err == nil && closeErr != nil {
			fact, err = ControlledReadStorageRelation{}, storageInspectionDriverError(ctx, "columns_close", closeErr)
		}
	}()
	for rows.Next() {
		var column ControlledReadStorageColumn
		if scanErr := rows.Scan(&column.Name, &column.Present, &column.BuiltinTypeMatches); scanErr != nil {
			return ControlledReadStorageRelation{}, storageInspectionDriverError(ctx, "columns", scanErr)
		}
		fact.Columns = append(fact.Columns, column)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return ControlledReadStorageRelation{}, storageInspectionDriverError(ctx, "columns", rowsErr)
	}
	if len(fact.Columns) != len(columns) {
		return ControlledReadStorageRelation{}, storageInspectionError(ctx, "columns")
	}
	return fact, nil
}
