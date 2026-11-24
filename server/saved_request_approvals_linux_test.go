//go:build linux

package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

func TestSavedRequestApprovalsReadOnlyPostgres(t *testing.T) {
	f := newControlledLocalDatabaseFixture(t)
	store := &db.DB{DB: f.Installer}
	if err := diagnosisflow.Migrate(t.Context(), f.Installer); err != nil {
		t.Fatal(err)
	}
	var role string
	if err := f.Runtime.QueryRow("SELECT current_user").Scan(&role); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Installer.Exec("GRANT SELECT ON diagnosis_workflows TO " + pgx.Identifier{role}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if err := CheckFirstDiagnosisReportDatabase(t.Context(), f.Runtime); err != nil {
		t.Fatal("report-only runtime role rejected", err)
	}
	task, err := store.CreateTask("approval history", "synthetic", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.CreateTask("other approval history", "synthetic", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	run := "synthetic-run-a"
	flow := diagnosisflow.Record{Owner: "ARTEX", Snapshot: diagnosisflow.Snapshot{
		Format: diagnosisflow.Version, JobID: "synthetic-job", TaskID: fmt.Sprint(task.ID), RunRef: run, Revision: 1,
	}}
	if err := (diagnosisflow.Postgres{DB: f.Installer}).Create(t.Context(), flow); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, tc := range []struct {
		id, run, state, execution string
		task                      int64
	}{
		{"synthetic-allow", run, "allowed", "completed", task.ID},
		{"synthetic-deny", run, "denied", "not_sent", task.ID},
		{"synthetic-expire", run, "expired", "not_sent", task.ID},
		{"synthetic-cancel", run, "cancelled", "not_sent", task.ID},
		{"synthetic-unknown", run, "allowed", "unknown", task.ID},
		{"synthetic-other-run", "synthetic-run-b", "allowed", "completed", task.ID},
		{"synthetic-other-task", run, "allowed", "completed", other.ID},
	} {
		a := db.RequestApproval{ID: tc.id, AttemptID: tc.id, TaskID: tc.task, OwnerRef: "ARTEX", RunRef: tc.run,
			Method: "POST", Path: "/private/synthetic-secret", InputPreview: map[string]string{"body": "synthetic-secret"},
			CreatedAt: now, ExpiresAt: now.Add(time.Minute), State: tc.state, ExecutionState: tc.execution}
		if err := store.InsertRequestApproval(t.Context(), a); err != nil {
			t.Fatal(err)
		}
	}
	password := "synthetic-report-password"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil || store.SetSetting(authPassKey, string(hash)) != nil {
		t.Fatal("report auth fixture")
	}
	keyDir, dataDir := t.TempDir(), t.TempDir()
	if os.Chmod(keyDir, 0700) != nil || os.Chmod(dataDir, 0700) != nil {
		t.Fatal("private report directories")
	}
	if err := os.WriteFile(filepath.Join(keyDir, jwtKeyFilename), []byte("synthetic-saved-approval-key-material"), 0600); err != nil {
		t.Fatal(err)
	}
	makeHandler := func() http.Handler {
		t.Helper()
		h, err := NewFirstDiagnosisReportHandler(t.Context(), f.Runtime, keyDir, dataDir, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	handler := makeHandler()
	var token string
	request := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	base := fmt.Sprintf("/api/tasks/%d/saved-request-approvals", task.ID)
	if w := request("GET", base); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized list=%d", w.Code)
	}
	login := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(fmt.Sprintf(`{"username":"ABC2LAB","password":%q}`, password)))
	login.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, login)
	var session struct {
		Token string `json:"token"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &session) != nil || session.Token == "" {
		t.Fatal("report login")
	}
	token = session.Token
	checkList := func() {
		t.Helper()
		w := request("GET", base)
		var got struct {
			RunRef    string          `json:"run_ref"`
			Approvals []SavedApproval `json:"approvals"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.RunRef != run || len(got.Approvals) != 5 {
			t.Fatalf("saved approvals status=%d count=%d", w.Code, len(got.Approvals))
		}
		if bytes.Contains(w.Body.Bytes(), []byte("synthetic-secret")) || bytes.Contains(w.Body.Bytes(), []byte("/private/")) {
			t.Fatal("saved approval leaked request details")
		}
		for _, row := range got.Approvals {
			if row.Live || row.CanDecide || row.AttemptRef != row.ID {
				t.Fatal("saved approval gained decision capability")
			}
		}
	}
	checkList()
	for _, status := range []string{"done", "failed", "timeout"} {
		if _, err := store.ExecContext(t.Context(), `UPDATE tasks SET status=$1 WHERE id=$2`, status, task.ID); err != nil {
			t.Fatal(err)
		}
		checkList()
	}
	if w := request("GET", base+"/synthetic-allow"); w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`"execution_state":"completed"`)) {
		t.Fatalf("saved detail=%d", w.Code)
	}
	if w := request("GET", base+"/synthetic-unknown"); w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`"execution_state":"unknown"`)) {
		t.Fatalf("unknown send detail=%d", w.Code)
	}
	for _, path := range []string{base + "/synthetic-other-run", base + "/synthetic-other-task"} {
		if w := request("GET", path); w.Code != http.StatusNotFound {
			t.Fatalf("foreign approval exposed: %d", w.Code)
		}
	}
	if w := request("GET", fmt.Sprintf("/api/tasks/%d/saved-request-approvals", other.ID)); w.Code != http.StatusForbidden {
		t.Fatalf("foreign task exposed: %d", w.Code)
	}
	if w := request("POST", base+"/synthetic-deny"); w.Code == http.StatusOK {
		t.Fatal("saved approval accepted mutation")
	}
	handler = makeHandler() // Stored history survives service restart.
	checkList()
	missing, err := NewFirstDiagnosisReportHandler(t.Context(), f.Runtime, keyDir, dataDir, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	handler = missing
	if w := request("GET", fmt.Sprintf("/api/tasks/%d/saved-request-approvals", other.ID)); w.Code != http.StatusNotFound {
		t.Fatalf("missing history treated as success or storage failure: %d", w.Code)
	}
	handler = makeHandler()
	if _, err := store.ExecContext(t.Context(), `UPDATE request_approvals SET record=jsonb_set(record,'{id}',to_jsonb('synthetic-mismatch'::text)) WHERE id='synthetic-deny'`); err != nil {
		t.Fatal(err)
	}
	if w := request("GET", base); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("invalid stored binding returned as history: %d", w.Code)
	}
}
