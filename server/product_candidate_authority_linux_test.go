//go:build linux

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisprep"
)

type candidateReadAuthorityFixture struct {
	e2HTTPPolicyAuthorityFixture
	binding FirstDiagnosisHTTPApproval
	revoked bool
	checks  int
}

func (a *candidateReadAuthorityFixture) approval() FirstDiagnosisHTTPApproval { return a.binding }
func (a *candidateReadAuthorityFixture) revoke()                              { a.revoked = true }
func (a *candidateReadAuthorityFixture) close() error                         { return nil }
func (a *candidateReadAuthorityFixture) CheckRoot(ctx context.Context) error {
	a.checks++
	if a.revoked {
		return errors.New("synthetic authority revoked")
	}
	return ctx.Err()
}

func TestProductCandidateRuntimeAuthorityOwnedPostgres(t *testing.T) {
	fixture := newControlledLocalDatabaseFixture(t)
	installer := &db.DB{DB: fixture.Installer}
	task, err := installer.CreateTask("합성 후보 조회", "", nil, 3600, 30)
	if err != nil {
		t.Fatal(err)
	}
	const passwordHash = "synthetic-candidate-authority-hash"
	if err := installer.SetSetting(authPassKey, passwordHash); err != nil {
		t.Fatal(err)
	}
	s := &Server{
		m:      &Manager{pg: &db.DB{DB: fixture.Runtime}},
		jwtKey: []byte("synthetic-candidate-authority-key"),
	}
	token, err := signJWT(s.signingKey(passwordHash))
	if err != nil {
		t.Fatal(err)
	}
	taskRef := strconv.FormatInt(task.ID, 10)
	job := &diagnosisprep.Job{ID: "synthetic-candidate-job", TaskID: taskRef, Scope: &diagnosisprep.Scope{TargetRef: "synthetic-target"}}
	authority := &candidateReadAuthorityFixture{
		binding: FirstDiagnosisHTTPApproval{TaskID: task.ID, ExplorationID: task.ExplorationID},
	}
	runtime := &FirstDiagnosisHTTPRuntime{
		server: s,
		host: &FirstDiagnosisHTTPHost{
			authority: authority,
			runtime: &E2HTTPRuntime{options: E2HTTPRuntimeOptions{
				Expected: E2HTTPExpectedBinding{RunRef: "synthetic-candidate-run"},
			}},
		},
	}
	for _, tc := range []struct {
		name, wantError                 string
		revoked, revokeDuringCheck, end bool
		status, checks, scopeChecks     int
	}{
		{name: "live", status: 200, checks: 2, scopeChecks: 1},
		{name: "revoked_before", revoked: true, status: 503, checks: 1, wantError: "candidate_read_unavailable"},
		{name: "revoked_during_scope_check", revokeDuringCheck: true, status: 503, checks: 2, scopeChecks: 1, wantError: "candidate_read_unavailable"},
		{name: "admission_stopped", end: true, status: 409, wantError: "candidate_run_not_active"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authority.revoked, authority.checks = tc.revoked, 0
			runtime.admissionStopped.Store(tc.end)
			scopeChecks := 0
			handler := runtime.ProductCandidateHandler(job, 1, func(context.Context) int {
				scopeChecks++
				if tc.revokeDuringCheck {
					authority.revoke()
				}
				return 0
			})
			req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+taskRef+productCandidateSuffix, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set(productCandidateProof, candidateProof(job, task.ExplorationID, 1))
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, req)
			if out.Code != tc.status || authority.checks != tc.checks || scopeChecks != tc.scopeChecks {
				t.Fatalf("status=%d, authority checks=%d, scope checks=%d", out.Code, authority.checks, scopeChecks)
			}
			if tc.wantError == "" {
				var page productCandidatePage
				if err := json.Unmarshal(out.Body.Bytes(), &page); err != nil || !page.valid(taskRef) {
					t.Fatal("정상 후보 페이지 계약 불일치")
				}
				return
			}
			var body map[string]string
			if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil || len(body) != 1 || body["error"] != tc.wantError ||
				out.Header().Get(productCandidateProof) != "" || out.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("거절 응답의 형식 또는 노출 경계 불일치")
			}
		})
	}
}

func TestProductCandidatePageAcceptsShortByteTruncation(t *testing.T) {
	items := []productCandidateItem{{ID: "17", State: "manual_review_required", ObservedAt: time.Now().UTC()}}
	truncated, returned := true, 1
	page := productCandidatePage{TaskID: "1", Items: items, Returned: &returned, Truncated: &truncated}
	if !page.valid("1") {
		t.Fatal("바이트 상한에서 잘린 짧은 페이지를 거부함")
	}
	page.Total = &returned
	if page.valid("1") {
		t.Fatal("잘린 페이지의 전체 건수를 확정값으로 받음")
	}
}
