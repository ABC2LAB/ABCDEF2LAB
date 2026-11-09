package server

import (
	"context"
	"database/sql"
	"strings"
)

// CheckFirstDiagnosisRuntimeDatabase는 설치자 권한과 런타임 권한의 실제 분리를 검사한다.
// 현재 연결의 카탈로그를 읽을 뿐 role, schema, 키 또는 epoch를 만들지 않는다.
func CheckFirstDiagnosisRuntimeDatabase(ctx context.Context, pool *sql.DB) error {
	return checkFirstDiagnosisRuntimeDatabase(ctx, pool, true)
}

// 일반 HTTP도 저장 보고서를 읽지만 등록 조회의 KG·E③ 판정 INSERT 권한은 요구하지 않는다.
func CheckFirstDiagnosisHTTPRuntimeDatabase(ctx context.Context, pool *sql.DB) error {
	return checkFirstDiagnosisRuntimeDatabase(ctx, pool, false)
}

// 저장보고서 경로는 실행·모델·판정 발급 권한 없이 기존 결과와 조치 저장만 사용한다.
func CheckFirstDiagnosisReportDatabase(ctx context.Context, pool *sql.DB) error {
	return checkFirstDiagnosisDatabase(ctx, pool, false, true)
}
func checkFirstDiagnosisRuntimeDatabase(ctx context.Context, pool *sql.DB, registered bool) error {
	return checkFirstDiagnosisDatabase(ctx, pool, registered, false)
}
func checkFirstDiagnosisDatabase(ctx context.Context, pool *sql.DB, registered, reportOnly bool) error {
	if ctx == nil || ctx.Err() != nil || pool == nil {
		return ErrControlledReadHostConfiguration
	}
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return ErrControlledReadHostConfiguration
	}
	defer tx.Rollback()
	schema, err := checkFirstDiagnosisRoleCatalog(ctx, tx)
	if err != nil {
		return err
	}
	var unsafe bool
	for _, name := range []string{"audit_checkpoint_epochs", "audit_key_registry_keys", "audit_key_registry_history", "audit_key_registry_operations"} {
		var exists bool
		err = tx.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists)
		if err != nil {
			return ErrControlledReadHostConfiguration
		}
		if !exists {
			if registered {
				return ErrControlledReadHostConfiguration
			}
			continue
		}
		err = tx.QueryRowContext(ctx, `SELECT has_table_privilege(current_user,to_regclass($1),'INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER') OR has_any_column_privilege(current_user,to_regclass($1),'INSERT,UPDATE,REFERENCES')`, name).Scan(&unsafe)
		if err != nil || unsafe {
			return ErrControlledReadHostConfiguration
		}
	}
	// 관찰 결과와 판정 저장에 필요한 권한만 명시적으로 확인한다.
	required := []struct{ name, privileges string }{
		{"tasks", "SELECT,INSERT,UPDATE"}, {"explorations", "SELECT,INSERT,UPDATE"}, {"exploration_nodes", "SELECT,INSERT,UPDATE"},
		{"task_llm_profiles", "SELECT,INSERT,UPDATE"}, {"activity", "SELECT,INSERT"}, {"exploration_edges", "SELECT,INSERT"}, {"findings", "SELECT,INSERT,UPDATE"},
		{"task_archives", "SELECT"}, {"llm_profiles", "SELECT"}, {"intercept_rules", "SELECT"}, {"settings", "SELECT"}, {"agents", "SELECT"}, {"agent_prompts", "SELECT"}, {"task_categories", "SELECT"}, {"task_relations", "SELECT"}, {"task_scope", "SELECT,INSERT"}, {"task_constraints", "SELECT,INSERT"},
		// 원본 목표 분해·Planner의 실제 자산 조회/참조 경로에 필요한 읽기와 anchor만 허용한다.
		{"assets", "SELECT"}, {"companies", "SELECT"}, {"company_scope", "SELECT"}, {"task_asset_links", "SELECT"},
		{"asset_intercept_rules", "SELECT"}, {"task_intercept_rules", "SELECT"}, {"exploration_anchors", "SELECT,INSERT"},
		// 보고서·조치의 기존 조회는 판정 유무와 관계없이 이 테이블을 함께 읽는다.
		{"e3_evidence_manifests", "SELECT"}, {"e3_oracle_verdicts", "SELECT"},
		{"e3_verdict_issuances", "SELECT"}, {"e3_verdict_links", "SELECT"},
	}
	// 일반 HTTP의 확인·중지 조회는 승인 행 유무와 무관하게 저장소를 사용한다.
	// 설치 누락을 첫 요청의 감사 실패로 미루지 않고 기동 전에 거부한다.
	if !registered && !reportOnly {
		required = append(required, struct{ name, privileges string }{"request_approvals", "SELECT,INSERT,UPDATE"})
	}
	if registered {
		required = append(required, []struct{ name, privileges string }{{"audit_checkpoint_epochs", "SELECT"}, {"task_control_modes", "SELECT,INSERT,UPDATE"}, {"traffic_evidence_snapshots", "SELECT,INSERT"}, {"finding_traffic_bindings", "SELECT,INSERT"},
			{"e3_evidence_manifests", "SELECT,INSERT"}, {"e3_oracle_verdicts", "SELECT,INSERT"},
			{"e3_verdict_issuances", "SELECT,INSERT"}, {"e3_verdict_links", "SELECT,INSERT"},
			{"evidence_revision_initials", "SELECT,INSERT"}, {"evidence_revision_operations", "SELECT,INSERT"},
			{"audit_checkpoint_history", "SELECT"}, {"audit_checkpoint_heads", "SELECT"}, {"audit_checkpoint_operations", "SELECT"}}...)
	}
	if reportOnly {
		required = []struct{ name, privileges string }{
			{"tasks", "SELECT"}, {"task_archives", "SELECT"}, {"findings", "SELECT"},
			{"task_control_modes", "SELECT"},
			{"exploration_nodes", "SELECT,INSERT"}, {"settings", "SELECT"},
			{"assets", "SELECT"}, {"task_asset_links", "SELECT"}, {"exploration_anchors", "SELECT"},
			{"e3_evidence_manifests", "SELECT"}, {"e3_oracle_verdicts", "SELECT"},
			{"e3_verdict_issuances", "SELECT"}, {"e3_verdict_links", "SELECT"},
		}
	}
	for _, relation := range required {
		var ordinary bool
		if tx.QueryRowContext(ctx, `SELECT c.relkind IN ('r','p') AND NOT c.relrowsecurity AND n.nspname=$2 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.oid=to_regclass($1)`, relation.name, schema).Scan(&ordinary) != nil || !ordinary {
			return ErrControlledReadHostConfiguration
		}

		// 권한 목록을 한 번에 전달하면 OR이므로 각 권한을 따로 검사한다.
		for _, privilege := range splitAssemblyPrivileges(relation.privileges) {
			var granted bool
			err = tx.QueryRowContext(ctx, `SELECT CASE WHEN to_regclass($1) IS NULL THEN false ELSE has_table_privilege(current_user,to_regclass($1),$2) END`, relation.name, privilege).Scan(&granted)
			if err != nil || !granted {
				return ErrControlledReadHostConfiguration
			}
		}
		if strings.Contains(relation.privileges, "INSERT") {
			var sequenceDenied bool
			err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_class s JOIN pg_depend d ON d.objid=s.oid
   WHERE s.relkind='S' AND d.refobjid=to_regclass($1) AND d.deptype IN ('a','i') AND NOT has_sequence_privilege(current_user,s.oid,'USAGE'))`, relation.name).Scan(&sequenceDenied)
			if err != nil || sequenceDenied {
				return ErrControlledReadHostConfiguration
			}
		}
	}

	// scoped 자산 SQL이 호출하는 기존 helper의 실제 실행 권한도 기동 전에 확인한다.
	var assetHelper bool
	if !reportOnly && (tx.QueryRowContext(ctx, `SELECT CASE WHEN to_regprocedure('try_inet(text)') IS NULL THEN false ELSE has_function_privilege(current_user,to_regprocedure('try_inet(text)'),'EXECUTE') END`).Scan(&assetHelper) != nil || !assetHelper) {
		return ErrControlledReadHostConfiguration
	}
	if reportOnly {
		// 기존 runtime의 넓은 쓰기를 새로 요구하지 않고 원본 id의 행 잠금만 확인한다.
		for _, name := range []string{"tasks", "exploration_nodes", "findings"} {
			var lockable bool
			if tx.QueryRowContext(ctx, `SELECT has_column_privilege(current_user,to_regclass($1),'id','UPDATE')`, name).Scan(&lockable) != nil || !lockable {
				return ErrControlledReadHostConfiguration
			}
		}
	}
	// 조치 원본·자산 결속을 잠그는 데 필요한 열만 허용한다. 제품은 이 열을 갱신하지 않는다.
	lockColumns := []struct {
		name, column string
		immutable    bool
	}{
		{"e3_verdict_issuances", "id", true},
		{"assets", "id", false},
		{"task_asset_links", "task_id", false},
		{"exploration_anchors", "node_id", false},
	}
	if registered {
		lockColumns = append(lockColumns, struct {
			name, column string
			immutable    bool
		}{"e3_evidence_manifests", "id", true})
	}
	for _, relation := range lockColumns {
		name := relation.name
		var lockable bool
		if tx.QueryRowContext(ctx, `SELECT has_column_privilege(current_user,to_regclass($1),$2,'UPDATE') AND NOT has_table_privilege(current_user,to_regclass($1),'UPDATE,DELETE,TRUNCATE,TRIGGER')
 AND NOT EXISTS(SELECT 1 FROM pg_attribute a WHERE a.attrelid=to_regclass($1) AND a.attnum>0 AND NOT a.attisdropped AND a.attname<>$2 AND has_column_privilege(current_user,a.attrelid,a.attnum,'UPDATE'))`, name, relation.column).Scan(&lockable) != nil || !lockable {
			return ErrControlledReadHostConfiguration
		}
		if !relation.immutable {
			continue
		}
		// 기존 immutable trigger를 실제로 유지한 경우만 행 잠금용 id 열 권한을 수용한다.
		var protected bool
		if tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_trigger t JOIN pg_proc p ON p.oid=t.tgfoid JOIN pg_namespace n ON n.oid=p.pronamespace JOIN pg_language l ON l.oid=p.prolang
    WHERE t.tgrelid=to_regclass($1) AND NOT t.tgisinternal AND t.tgenabled IN ('O','A') AND (t.tgtype::int & 27)=27 AND p.proname='guard_e3_immutable' AND n.nspname=$2 AND NOT p.prosecdef AND l.lanname='plpgsql'
    AND regexp_replace(p.prosrc,'[[:space:]]','','g')=$3)`, name, schema, "BEGINRAISEEXCEPTION'e3_immutable_record'USINGERRCODE='23514';END;").Scan(&protected) != nil || !protected {
			return ErrControlledReadHostConfiguration
		}
	}
	if ctx.Err() != nil || tx.Commit() != nil {
		return ErrControlledReadHostConfiguration
	}
	return nil
}

func splitAssemblyPrivileges(value string) []string {
	var result []string
	start := 0
	for i := 0; i <= len(value); i++ {
		if i == len(value) || value[i] == ',' {
			result = append(result, value[start:i])
			start = i + 1
		}
	}
	return result
}

func checkFirstDiagnosisRoleCatalog(ctx context.Context, tx *sql.Tx) (string, error) {
	var err error
	var unsafe bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles r
 WHERE (r.rolname=current_user OR r.rolname=session_user OR pg_has_role(current_user,r.oid,'MEMBER') OR pg_has_role(session_user,r.oid,'MEMBER'))
 AND (r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolbypassrls OR r.rolname IN ('pg_read_all_data','pg_write_all_data','pg_execute_server_program','pg_read_server_files','pg_write_server_files','pg_signal_backend','pg_checkpoint','pg_create_subscription','pg_monitor','pg_read_all_settings','pg_read_all_stats','pg_stat_scan_tables','pg_use_reserved_connections')))`).Scan(&unsafe)
	if err != nil || unsafe {
		return "", ErrControlledReadHostConfiguration
	}
	var schema string
	err = tx.QueryRowContext(ctx, `SELECT current_schema() WHERE current_setting('server_version_num')::int BETWEEN 160000 AND 169999`).Scan(&schema)
	if err != nil || schema == "" || schema == "pg_catalog" || schema == "information_schema" {
		return "", ErrControlledReadHostConfiguration
	}
	err = tx.QueryRowContext(ctx, `SELECT current_setting('session_replication_role')<>'origin' OR has_parameter_privilege(current_user,'session_replication_role','SET') OR cardinality(array_remove(current_schemas(false),'pg_catalog'))<>1 OR pg_my_temp_schema()<>0 OR has_database_privilege(current_user,current_database(),'CREATE')
 OR has_database_privilege(current_user,current_database(),'TEMP')
 OR has_schema_privilege(current_user,$1,'CREATE')
 OR EXISTS(SELECT 1 FROM pg_namespace n WHERE n.nspname=$1 AND pg_has_role(current_user,n.nspowner,'MEMBER'))
 OR EXISTS(SELECT 1 FROM pg_database d WHERE d.datname=current_database() AND pg_has_role(current_user,d.datdba,'MEMBER'))
 OR EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND pg_has_role(current_user,c.relowner,'MEMBER'))
 OR EXISTS(SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=$1 AND (pg_has_role(current_user,p.proowner,'MEMBER') OR p.prosecdef AND has_function_privilege(current_user,p.oid,'EXECUTE')))`, schema).Scan(&unsafe)
	if err != nil || unsafe {
		return "", ErrControlledReadHostConfiguration
	}
	return schema, nil
}
