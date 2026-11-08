package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// 데이터 소스 오류를 대조군으로 써 뒤쪽 JWT 거부가 Bearer 전용 가드를 가리지 않게 한다.
func TestDiagnosisRunRejectsNonBearerBeforeStorage(t *testing.T) {
	pool, err := sql.Open("pgx", "postgres:///synthetic-unused")
	if err != nil {
		t.Fatal(err)
	}
	if err = pool.Close(); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: &Manager{pg: &db.DB{DB: pool}}}
	for _, mode := range []string{"missing", "cookie", "query", "empty-bearer", "basic", "bearer-storage-control"} {
		t.Run(mode, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/tasks/7/diagnosis-run", nil)
			switch mode {
			case "cookie":
				r.AddCookie(&http.Cookie{Name: "artex_token", Value: "synthetic-session"})
			case "query":
				r.URL.RawQuery = "token=synthetic-session"
			case "empty-bearer":
				r.Header.Set("Authorization", "Bearer ")
			case "basic":
				r.Header.Set("Authorization", "Basic synthetic-session")
			case "bearer-storage-control":
				r.Header.Set("Authorization", "Bearer synthetic-session")
			}
			w := httptest.NewRecorder()
			s.serveDiagnosisRun(w, r, 7)
			want := http.StatusUnauthorized
			if mode == "bearer-storage-control" {
				want = http.StatusServiceUnavailable
			}
			if w.Code != want {
				t.Fatalf("authentication/storage ordering: status=%d want=%d", w.Code, want)
			}
		})
	}
}

func TestDiagnosisRunPersistsClosureFailureOwnedPostgres(t *testing.T) {
	pg := approvalTestDB(t)
	for _, failed := range []bool{false, true} {
		name := "complete"
		if failed {
			name = "closure-failed"
		}
		t.Run(name, func(t *testing.T) {
			task, err := pg.CreateTask("synthetic run outcome", "fixture", nil, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := pg.DeleteTask(task.ID); err != nil {
					t.Error(err)
				}
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			service := newRequestApprovalService(nil)
			if err = service.admitRun(ctx, task.ID, "synthetic-run", cancel); err != nil {
				t.Fatal(err)
			}
			service.runStarted(task.ID)
			if service.runFinished(task.ID, nil, false) {
				t.Fatal("unexpected stop")
			}
			supervisor := &FirstDiagnosisSupervisor{m: &Manager{pg: pg}, server: &Server{requestApprovals: service}, runStarted: true, runTask: strconv.FormatInt(task.ID, 10)}
			var closureErr error
			if failed {
				closureErr = errors.New("synthetic close failure")
			}
			if err = supervisor.PersistRunOutcome(t.Context(), closureErr); err != nil {
				t.Fatal(err)
			}
			var raw []byte
			if err = pg.QueryRowContext(t.Context(), `SELECT payload FROM exploration_nodes WHERE exploration_id=$1 AND origin=$2`, task.ExplorationID, diagnosisRunOrigin).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var stored diagnosisRunRecord
			if err = json.Unmarshal(raw, &stored); err != nil {
				t.Fatal(err)
			}
			wantPhase := "closed"
			if failed {
				wantPhase = "failed"
			}
			if stored.TaskID != task.ID || stored.Run.Phase != wantPhase || stored.Run.ClosureFailed != failed || stored.Run.DrainComplete == failed || stored.Run.Outcome != "completed" {
				t.Fatalf("closure outcome misreported: %+v", stored.Run)
			}
		})
	}
}
