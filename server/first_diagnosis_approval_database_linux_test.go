//go:build linux

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// 설치자가 공급한 권한을 실제 LOGIN으로 검사하며 승인 대기 중의 첫 SQL까지 미루지 않는다.
func TestFirstDiagnosisHTTPApprovalPrivilegesOwnedPostgres(t *testing.T) {
	f := newControlledLocalDatabaseFixture(t)
	var role string
	if err := f.Runtime.QueryRowContext(f.Context, "SELECT current_user").Scan(&role); err != nil {
		t.Fatal("실행 역할 식별 실패")
	}
	ident := pgx.Identifier{role}.Sanitize()
	if err := CheckFirstDiagnosisHTTPRuntimeDatabase(f.Context, f.Runtime); err != nil {
		t.Fatal("최소 승인 권한의 일반 HTTP 기동 거부", err)
	}
	for _, tc := range []struct{ privilege, query string }{
		{"SELECT", "SELECT id FROM request_approvals LIMIT 0"},
		{"INSERT", "INSERT INTO request_approvals(id,task_id,owner_ref,expires_at,record) SELECT '',0,'',NOW(),'{}'::jsonb WHERE false"},
		{"UPDATE", "UPDATE request_approvals SET version=version WHERE false"},
	} {
		t.Run(tc.privilege, func(t *testing.T) {
			if _, err := f.Installer.ExecContext(f.Context, "REVOKE "+tc.privilege+" ON request_approvals FROM "+ident); err != nil {
				t.Fatal("소유 시험 승인 권한 회수 실패")
			}
			t.Cleanup(func() {
				if _, err := f.Installer.ExecContext(context.Background(), "GRANT "+tc.privilege+" ON request_approvals TO "+ident); err != nil {
					t.Error("소유 시험 승인 권한 복구 실패")
				}
			})
			if err := CheckFirstDiagnosisHTTPRuntimeDatabase(f.Context, f.Runtime); !errors.Is(err, ErrControlledReadHostConfiguration) {
				t.Fatal("승인 저장 권한 누락을 기동에서 거부하지 않음")
			}
			_, err := f.Runtime.ExecContext(f.Context, tc.query)
			var denied *pgconn.PgError
			if !errors.As(err, &denied) || denied.Code != "42501" {
				t.Fatal("독립 실행 LOGIN의 승인 SQL 권한 경계가 없음")
			}
			// 등록 GET은 별도 권위 경로이며 요청별 승인 테이블을 공통 조건으로 만들지 않는다.
			if err := CheckFirstDiagnosisRuntimeDatabase(f.Context, f.Runtime); err != nil {
				t.Fatal("일반 HTTP 승인 권한을 등록 GET에 강제함", err)
			}
		})
	}
}
