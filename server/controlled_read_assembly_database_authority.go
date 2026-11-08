package server

import (
	"context"
	"database/sql"
)

// 별도 호스트 역할은 기존 epoch·키를 잠글 수 있지만 설치자/소유자 역할일 수 없다.
// FOR SHARE에 필요한 UPDATE는 지정 키 열로 제한하며 제품 코드는 해당 테이블을 수정하지 않는다.
func CheckFirstDiagnosisAuthorityDatabase(ctx context.Context, pool *sql.DB) error {
	if ctx == nil || ctx.Err() != nil || pool == nil || pool.Stats().MaxOpenConnections > 0 && pool.Stats().MaxOpenConnections < 2 {
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
	for _, r := range []struct{ name, column string }{{"audit_checkpoint_epochs", "store_ref"}, {"audit_key_registry_keys", "key_ref"}, {"audit_key_registry_history", "key_ref"}} {
		var valid bool
		err = tx.QueryRowContext(ctx, `SELECT c.relkind='r' AND NOT c.relrowsecurity AND n.nspname=$3 AND has_table_privilege(current_user,c.oid,'SELECT') AND has_column_privilege(current_user,c.oid,$2,'UPDATE') AND NOT has_table_privilege(current_user,c.oid,'INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER') AND NOT has_any_column_privilege(current_user,c.oid,'INSERT,REFERENCES') FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.oid=to_regclass($1)`, r.name, r.column, schema).Scan(&valid)
		if err != nil || !valid {
			return ErrControlledReadHostConfiguration
		}
		var excessive bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_attribute a WHERE a.attrelid=to_regclass($1) AND a.attnum>0 AND NOT a.attisdropped AND a.attname<>$2 AND has_column_privilege(current_user,a.attrelid,a.attnum,'UPDATE'))`, r.name, r.column).Scan(&excessive)
		if err != nil || excessive {
			return ErrControlledReadHostConfiguration
		}
	}
	for _, r := range []struct{ name, privileges string }{{"audit_checkpoint_history", "SELECT,INSERT"}, {"audit_checkpoint_heads", "SELECT,INSERT,UPDATE"}, {"audit_checkpoint_operations", "SELECT,INSERT"}} {
		for _, priv := range splitAssemblyPrivileges(r.privileges) {
			var valid bool
			if tx.QueryRowContext(ctx, `SELECT CASE WHEN to_regclass($1) IS NULL THEN false ELSE has_table_privilege(current_user,to_regclass($1),$2) END`, r.name, priv).Scan(&valid) != nil || !valid {
				return ErrControlledReadHostConfiguration
			}
		}
		var forbidden bool
		if tx.QueryRowContext(ctx, `SELECT has_table_privilege(current_user,to_regclass($1),'DELETE,TRUNCATE,REFERENCES,TRIGGER') OR has_any_column_privilege(current_user,to_regclass($1),'REFERENCES')`, r.name).Scan(&forbidden) != nil || forbidden {
			return ErrControlledReadHostConfiguration
		}
	}
	// 키 등록의 명령·receipt 저장 권한은 이 기동 어댑터에 필요하지 않다.
	var forbidden bool
	if tx.QueryRowContext(ctx, `SELECT CASE WHEN to_regclass('audit_key_registry_operations') IS NULL THEN true ELSE has_table_privilege(current_user,to_regclass('audit_key_registry_operations'),'INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER') OR has_any_column_privilege(current_user,to_regclass('audit_key_registry_operations'),'INSERT,UPDATE,REFERENCES') END`).Scan(&forbidden) != nil || forbidden {
		return ErrControlledReadHostConfiguration
	}
	if ctx.Err() != nil || tx.Commit() != nil {
		return ErrControlledReadHostConfiguration
	}
	return nil
}
