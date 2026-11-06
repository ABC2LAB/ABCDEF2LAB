package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// 옛 추론 행은 화면용 metadata가 있어도 direct·source·인증 HTTP 조회에서 숨긴다.
func TestActivityDetailHTTPHidesLegacyThinking(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("전용 PostgreSQL 필요: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	source, err := m.CreateTaskWithOptions("thinking detail source", "fixture", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	current, err := m.CreateTaskWithOptions("thinking detail current", "fixture", db.TaskCreateOptions{SourceTaskIDs: []int64{mustTaskID(t, source.ID)}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = m.DeleteTask(current.ID, DeleteTaskOptions{})
		_, _ = m.DeleteTask(source.ID, DeleteTaskOptions{})
	})
	s := newTestServer(t, context.Background(), m, t.TempDir(), t.TempDir(), t.TempDir())
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	fetch := func(id int64) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/exploration/activity/%d?task=%s", id, current.ID), nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var body struct {
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Detail
	}
	for _, task := range []*Task{current, source} {
		intent, err := task.Store.AddIntent(map[string]any{"summary": "fixture"}, 1, nil, "planner")
		if err != nil {
			t.Fatal(err)
		}
		if err := task.Store.SetIntentState(intent, "done"); err != nil {
			t.Fatal(err)
		}
		for _, metadata := range []json.RawMessage{nil, db.DisplayMetadata("추론 표시", "숨길 표시 상세")} {
			id, err := task.Store.AppendActivity(db.Activity{NodeID: &intent, Worker: "worker", Kind: "thinking", Detail: "synthetic private reasoning", Metadata: metadata})
			if err != nil {
				t.Fatal(err)
			}
			if got, err := task.Store.ActivityDisplayDetail(id); err != nil || got != "" {
				t.Fatalf("direct thinking=%q err=%v", got, err)
			}
			if got, err := current.Store.ActivityDisplayDetailWithSources(id); err != nil || got != "" {
				t.Fatalf("source thinking=%q err=%v", got, err)
			}
			if got := fetch(id); got != "" {
				t.Fatalf("HTTP thinking=%q", got)
			}
		}
		id, err := task.Store.AppendActivity(db.Activity{NodeID: &intent, Worker: "worker", Kind: "result", Detail: "model result", Metadata: db.DisplayMetadata("결과", "공개 결과")})
		if err != nil {
			t.Fatal(err)
		}
		if got := fetch(id); got != "공개 결과" {
			t.Fatalf("HTTP result=%q", got)
		}
	}
}
