//go:build linux

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/oracle"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// 실제 첫 진단 생성자/감독자/Handler와 분리된 LOGIN으로 저장 경로를 검증한다.
// 모델과 대상 실행은 시작하지 않으며 fixture 외부 네트워크를 사용하지 않는다.
func TestFirstDiagnosisReportRoutesOwnedPostgres(t *testing.T) {
	f := newControlledLocalDatabaseFixture(t)
	ctx := f.Context
	installer := &db.DB{DB: f.Installer}
	var role string
	if f.Runtime.QueryRowContext(ctx, "SELECT current_user").Scan(&role) != nil {
		t.Fatal("runtime identity")
	}
	ident := pgx.Identifier{role}.Sanitize()
	exec := func(query string) {
		t.Helper()
		if _, err := f.Installer.ExecContext(ctx, query); err != nil {
			t.Fatal("owned report privilege setup")
		}
	}
	// 일반 HTTP 보고서는 판정 발급권한을 요구하지 않는다.
	exec("REVOKE INSERT ON e3_evidence_manifests,e3_oracle_verdicts,e3_verdict_issuances,e3_verdict_links FROM " + ident)
	exec("REVOKE UPDATE(id) ON e3_evidence_manifests FROM " + ident)
	check := func(t *testing.T, want bool) {
		t.Helper()
		err := CheckFirstDiagnosisHTTPRuntimeDatabase(ctx, f.Runtime)
		if (err == nil) != want {
			t.Fatalf("runtime report permissions accepted=%v want=%v", err == nil, want)
		}
	}
	check(t, true)
	if CheckFirstDiagnosisRuntimeDatabase(ctx, f.Runtime) == nil {
		t.Fatal("general report role gained registered verdict privileges")
	}
	for _, tc := range []struct{ name, apply, restore string }{
		{"missing-asset-lock", "REVOKE UPDATE(id) ON assets FROM ", "GRANT UPDATE(id) ON assets TO "},
		{"missing-asset-link-lock", "REVOKE UPDATE(task_id) ON task_asset_links FROM ", "GRANT UPDATE(task_id) ON task_asset_links TO "},
		{"missing-anchor-lock", "REVOKE UPDATE(node_id) ON exploration_anchors FROM ", "GRANT UPDATE(node_id) ON exploration_anchors TO "},
		{"extra-asset-update", "GRANT UPDATE(type) ON assets TO ", "REVOKE UPDATE(type) ON assets FROM "},
		{"extra-link-update", "GRANT UPDATE(asset_id) ON task_asset_links TO ", "REVOKE UPDATE(asset_id) ON task_asset_links FROM "},
		{"extra-anchor-update", "GRANT UPDATE(asset_id) ON exploration_anchors TO ", "REVOKE UPDATE(asset_id) ON exploration_anchors FROM "},
		{"missing-archive-read", "REVOKE SELECT ON task_archives FROM ", "GRANT SELECT ON task_archives TO "},
		{"extra-issuance-column", "GRANT UPDATE(input_digest) ON e3_verdict_issuances TO ", "REVOKE UPDATE(input_digest) ON e3_verdict_issuances FROM "},
		{"missing-evidence-read", "REVOKE SELECT ON e3_evidence_manifests FROM ", "GRANT SELECT ON e3_evidence_manifests TO "},
		{"missing-verdict-read", "REVOKE SELECT ON e3_oracle_verdicts FROM ", "GRANT SELECT ON e3_oracle_verdicts TO "},
		{"missing-issuance-read", "REVOKE SELECT ON e3_verdict_issuances FROM ", "GRANT SELECT ON e3_verdict_issuances TO "},
		{"missing-links-read", "REVOKE SELECT ON e3_verdict_links FROM ", "GRANT SELECT ON e3_verdict_links TO "},
		{"missing-issuance-lock", "REVOKE UPDATE(id) ON e3_verdict_issuances FROM ", "GRANT UPDATE(id) ON e3_verdict_issuances TO "},
		{"broad-issuance-update", "GRANT UPDATE ON e3_verdict_issuances TO ", "REVOKE UPDATE ON e3_verdict_issuances FROM "},
	} {
		t.Run(tc.name, func(t *testing.T) { exec(tc.apply + ident); defer exec(tc.restore + ident); check(t, false) })
	}
	// PostgreSQL은 table UPDATE 회수 때 column UPDATE도 회수할 수 있으므로 명시 복구한다.
	exec("GRANT UPDATE(id) ON e3_verdict_issuances TO " + ident)
	check(t, true)
	t.Run("immutable-trigger-required", func(t *testing.T) {
		exec("ALTER TABLE e3_verdict_issuances DISABLE TRIGGER USER")
		defer exec("ALTER TABLE e3_verdict_issuances ENABLE TRIGGER USER")
		check(t, false)
	})
	if CheckFirstDiagnosisHTTPRuntimeDatabase(ctx, f.Installer) == nil {
		t.Fatal("installer bypass accepted")
	}
	for _, query := range []string{"UPDATE e3_verdict_issuances SET input_digest=input_digest WHERE false", "INSERT INTO e3_oracle_verdicts DEFAULT VALUES", "UPDATE audit_checkpoint_epochs SET generation=generation WHERE false", "CREATE TEMP TABLE report_forbidden(id int)"} {
		if _, err := f.Runtime.ExecContext(ctx, query); err == nil {
			t.Fatal("forbidden runtime SQL accepted")
		}
	}
	tx, err := f.Runtime.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, "SELECT id FROM e3_verdict_issuances WHERE false FOR UPDATE"); err != nil {
		t.Fatal("issuance lock unavailable")
	}
	_ = tx.Rollback()

	t.Run("actual-verdict-row-lock-and-immutable", func(t *testing.T) {
		fixture := diagnosticE3FixtureFor(t, installer, oracle.Suspected, true)
		verdict, err := installer.RecordControlledFinding(ctx, fixture.prepared, fixture.markers)
		if err != nil || verdict.IssuanceID <= 0 || verdict.FindingID != 0 {
			t.Fatal("unlinked verdict setup", err)
		}
		runtimeDB := &db.DB{DB: f.Runtime}
		entry := fmt.Sprintf("verdict-%d", verdict.IssuanceID)
		source, err := db.LoadDiagnosticRemediationSource(ctx, runtimeDB, fixture.taskID, entry)
		if err != nil {
			t.Fatal("actual verdict source", err)
		}
		record, err := runtimeDB.SaveDiagnosticRemediation(ctx, fixture.taskID, entry, source.SourceDigest, nil)
		if err != nil || record.ID <= 0 || record.Bundle.AIPrompt == "" {
			t.Fatal("actual verdict remediation with id-only lock", err)
		}
		_, err = f.Runtime.ExecContext(ctx, "UPDATE e3_verdict_issuances SET id=id WHERE id=$1", verdict.IssuanceID)
		var guarded *pgconn.PgError
		if !errors.As(err, &guarded) || guarded.Code != "23514" || guarded.Message != "e3_immutable_record" {
			t.Fatal("immutable guard did not reject actual row update")
		}
	})
	manager, err := NewFirstDiagnosisManager(ctx, t.TempDir(), f.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	task, err := manager.CreateTask("Owned report route fixture", "Read stored evidence without target execution", nil, 60, 60)
	if err != nil {
		t.Fatal(err)
	}
	firstDiagnosisSupervisorConstructorClose(t, manager, installer, task, func(runtime *FirstDiagnosisHTTPRuntime) {
		checkFirstDiagnosisStoredReportFlow(t, runtime, task, installer)
	})
}

func checkFirstDiagnosisStoredReportFlow(t *testing.T, runtime *FirstDiagnosisHTTPRuntime, task *Task, installer *db.DB) {
	t.Helper()
	s := runtime.server
	taskID := mustFirstDiagnosisTaskNumber(task.ID)
	candidate := diagnosticServerCandidate(t, s, taskID, "completed")
	entry := fmt.Sprintf("candidate-%d", candidate)
	var token string
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		runtime.Handler().ServeHTTP(w, r)
		return w
	}
	base := "/api/report/diagnostic/remediation?task=" + task.ID
	if got := request("GET", base+"&audience=internal", ""); got.Code != 401 {
		t.Fatal("anonymous report admitted")
	}
	login := request("POST", "/api/auth/login", `{"username":"ABC2LAB","password":"synthetic-owned-constructor-fixture"}`)
	var session struct {
		Token string `json:"token"`
	}
	if login.Code != 200 || json.Unmarshal(login.Body.Bytes(), &session) != nil || session.Token == "" {
		t.Fatal("existing login failed")
	}
	token = session.Token
	list := func(internal bool) diagnosticRemediationEntryDTO {
		t.Helper()
		path := base
		if internal {
			path += "&audience=internal"
		}
		w := request("GET", path, "")
		var result struct {
			Entries []diagnosticRemediationEntryDTO `json:"entries"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
			t.Fatalf("runtime list status=%d", w.Code)
		}
		for _, e := range result.Entries {
			if e.EntryID == entry {
				return e
			}
		}
		t.Fatal("stored candidate missing")
		return diagnosticRemediationEntryDTO{}
	}

	var intentID int64
	if err := s.m.pg.QueryRow("SELECT (payload->'diagnostic_candidate_v1'->>'intent_id')::bigint FROM exploration_nodes WHERE id=$1", candidate).Scan(&intentID); err != nil {
		t.Fatal("candidate intent binding", err)
	}
	asset, err := installer.Assets().UpsertHTTPService(db.UpsertHTTPServiceReq{URL: fmt.Sprintf("http://owned-report-%d.invalid", taskID), TaskID: taskID, Technologies: []string{"Phoenix"}})
	if err != nil {
		t.Fatal("owned fingerprint installation", err)
	}
	if err = task.Store.Anchor(intentID, asset); err != nil {
		t.Fatal("owned candidate anchor", err)
	}
	source := list(true)
	unsafeBody, _ := json.Marshal(map[string]any{"entry_id": entry, "expected_source_digest": source.SourceDigest})
	var before int
	if s.m.pg.QueryRow("SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1", task.ExpID).Scan(&before) != nil {
		t.Fatal("report row count")
	}
	for _, mode := range []string{"cross-origin-cookie", "query-token", "bearer-text-plain", "empty-bearer"} {
		r := httptest.NewRequest("POST", base+"&audience=internal", strings.NewReader(string(unsafeBody)))
		r.Header.Set("Content-Type", "application/json")
		switch mode {
		case "cross-origin-cookie":
			r.AddCookie(&http.Cookie{Name: "artex_token", Value: token})
			r.Header.Set("Origin", "http://untrusted.example")
			r.Header.Set("Content-Type", "text/plain")
		case "query-token":
			r.URL.RawQuery += "&token=" + token
		case "bearer-text-plain":
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("Content-Type", "text/plain")
		case "empty-bearer":
			r.Header.Set("Authorization", "Bearer ")
		}
		w := httptest.NewRecorder()
		runtime.Handler().ServeHTTP(w, r)
		if w.Code != 403 && !(mode == "empty-bearer" && w.Code == 401) {
			t.Fatalf("unsafe POST mode=%s status=%d", mode, w.Code)
		}
	}
	var after int
	if s.m.pg.QueryRow("SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1", task.ExpID).Scan(&after) != nil || after != before {
		t.Fatal("unsafe POST wrote remediation")
	}

	if !source.Available || source.SourceDigest == "" {
		t.Fatal("runtime source unavailable")
	}
	raw, _ := json.Marshal(map[string]any{"entry_id": entry, "expected_source_digest": source.SourceDigest})
	create := request("POST", base+"&audience=internal", string(raw))
	var record diagnosticRemediationDTO
	if create.Code != 200 || json.Unmarshal(create.Body.Bytes(), &record) != nil || record.Bundle == nil {
		t.Fatalf("runtime generation status=%d", create.Code)
	}

	linked := false
	for _, technology := range record.Bundle.Technologies {
		if technology.Name == "Phoenix" && technology.Source == "inferred" && technology.Component == fmt.Sprintf("asset-%d", asset) {
			linked = true
		}
	}
	if !linked {
		t.Fatal("linked asset fingerprint missing from runtime generated prompt")
	}
	var unchanged bool
	if installer.QueryRow("SELECT technologies=ARRAY['Phoenix'] AND EXISTS(SELECT 1 FROM task_asset_links WHERE task_id=$1 AND asset_id=$2) AND EXISTS(SELECT 1 FROM exploration_anchors WHERE node_id=$3 AND asset_id=$2) FROM assets WHERE id=$2", taskID, asset, intentID).Scan(&unchanged) != nil || !unchanged {
		t.Fatal("report generation changed asset source")
	}
	stored := list(true)
	if stored.Record == nil || stored.Record.ID != record.ID || stored.Record.Bundle == nil || stored.Record.Bundle.AIPrompt != record.Bundle.AIPrompt {
		t.Fatal("stored report differs")
	}
	copied := request("GET", fmt.Sprintf("/api/report/diagnostic/remediation/prompt?task=%s&audience=internal&entry=%s&record=%d&digest=%s", task.ID, entry, record.ID, record.Bundle.InputDigest), "")
	var result struct {
		Prompt string `json:"ai_prompt"`
	}
	if copied.Code != 200 || json.Unmarshal(copied.Body.Bytes(), &result) != nil || result.Prompt != record.Bundle.AIPrompt {
		t.Fatal("copy differs")
	}
	report := request("GET", "/api/report/diagnostic?task="+task.ID+"&audience=internal&download=1", "")
	if report.Code != 200 || report.Header().Get("Content-Disposition") == "" || !strings.Contains(html.UnescapeString(report.Body.String()), record.Bundle.AIPrompt) {
		t.Fatal("report export differs")
	}
	shared := list(false)
	if shared.Record == nil || shared.Record.Bundle != nil || shared.SourceDigest != "" {
		t.Fatal("shared details exposed")
	}
	exported := request("GET", "/api/report/diagnostic?task="+task.ID+"&download=1", "")
	if exported.Code != 200 || strings.Contains(exported.Body.String(), record.Bundle.InputDigest) {
		t.Fatal("shared export exposed")
	}
	for _, tc := range []struct {
		method, path, body string
		code               int
	}{
		{"POST", base, string(raw), 403},
		{"GET", fmt.Sprintf("/api/report/diagnostic/remediation/prompt?task=%s&entry=%s&record=%d&digest=%s", task.ID, entry, record.ID, record.Bundle.InputDigest), "", 403},
		{"GET", "/api/report/diagnostic?task=999999999", "", 403},
		{"POST", "/api/report/diagnostic/remediation?task=999999999&audience=internal", string(raw), 403},
		{"GET", base + "&task=" + task.ID, "", 403},
		{"POST", "/api/tasks/" + task.ID + "/resume", "{}", 403},
		{"POST", "/api/chat?task=" + task.ID, "{}", 403},
		{"GET", "/api/update/check", "", 403},
	} {
		if got := request(tc.method, tc.path, tc.body); got.Code != tc.code {
			t.Fatalf("boundary status=%d want=%d path=%s", got.Code, tc.code, tc.path)
		}
	}
	if _, err := installer.ExecContext(context.Background(), "UPDATE assets SET technologies=ARRAY['ChangedTech'] WHERE id=$1", asset); err != nil {
		t.Fatal(err)
	}

	stale := list(true)
	if stale.Record == nil || !stale.Record.Stale || stale.Record.Bundle != nil {
		t.Fatal("stale runtime prompt exposed")
	}
	if got := request("POST", base+"&audience=internal", string(raw)); got.Code != 409 {
		t.Fatal("stale source accepted")
	}
	if got := request("GET", fmt.Sprintf("/api/report/diagnostic/remediation/prompt?task=%s&audience=internal&entry=%s&record=%d&digest=%s", task.ID, entry, record.ID, record.Bundle.InputDigest), ""); got.Code != 409 {
		t.Fatal("stale copy accepted")
	}
}
