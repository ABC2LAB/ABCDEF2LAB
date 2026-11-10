//go:build linux

package server

import (
	"context"
	"github.com/jackc/pgx/v5"
	"testing"
)

func TestE2PromptProjectionPrivilegesOwnedPostgres(t *testing.T) {
	f := newControlledLocalDatabaseFixture(t)
	var role string
	if f.Runtime.QueryRowContext(f.Context, "SELECT current_user").Scan(&role) != nil {
		t.Fatal("runtime identity")
	}
	ident := pgx.Identifier{role}.Sanitize()
	for _, table := range []string{"e2_prompt_runs", "e2_prompt_observations"} {
		for _, privilege := range []string{"SELECT", "INSERT", "UPDATE"} {
			t.Run(table+privilege, func(t *testing.T) {
				if err := CheckFirstDiagnosisHTTPRuntimeDatabase(f.Context, f.Runtime); err != nil {
					t.Fatal("valid projection installation rejected")
				}
				if _, err := f.Installer.ExecContext(f.Context, "REVOKE "+privilege+" ON "+table+" FROM "+ident); err != nil {
					t.Fatal("revoke fixture privilege")
				}
				defer func() {
					if _, err := f.Installer.ExecContext(context.Background(), "GRANT "+privilege+" ON "+table+" TO "+ident); err != nil {
						t.Error("restore fixture privilege")
					}
				}()
				if CheckFirstDiagnosisHTTPRuntimeDatabase(f.Context, f.Runtime) == nil {
					t.Fatal("missing projection privilege admitted")
				}
				if err := CheckFirstDiagnosisRuntimeDatabase(f.Context, f.Runtime); err != nil {
					t.Fatal("registered GET unexpectedly requires projection privileges")
				}
			})
		}
	}
}
