package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisprep"
	"golang.org/x/crypto/bcrypt"
)

type operationsEndpoint struct{ revoked atomic.Bool }

func (e *operationsEndpoint) Call(_ context.Context, r diagnosisprep.Request) (diagnosisprep.Response, error) {
	out := diagnosisprep.Response{Version: diagnosisprep.Version, RequestID: r.RequestID}
	allowed := r.Command == diagnosisprep.AuthorizeTask && r.Principal == "ARTEX" && r.TaskID == "42" && !e.revoked.Load()
	out.Authorized = &allowed
	if allowed {
		out.Job = &diagnosisprep.Job{ID: "owned-job", Revision: 1, SourceDigest: strings.Repeat("a", 64), Input: diagnosisprep.Input{TargetURL: "https://owned.example/", ProfileID: "local"}, State: diagnosisprep.Unknown, Phase: diagnosisprep.Planning, TaskID: "42", PreparationStarted: true, AuthorityState: diagnosisprep.AuthorityNotIssued}
	}
	return out, nil
}

// 실제 설치 로그인 → 공개 관리 mux → 영속 작업 권한 조회 → HTTP 실행 중계.
// 실행 수신기는 합성이며 실제 모델·엔진 수용으로 계산하지 않는다.
func TestURLManagementOperationsPublicEntryOwnedPostgres(t *testing.T) {
	f := newControlledLocalDatabaseFixture(t)
	installer := &db.DB{DB: f.Installer}
	hash, _ := bcrypt.GenerateFromPassword([]byte("owned-fixture-password"), bcrypt.MinCost)
	if installer.SetSetting(authPassKey, string(hash)) != nil {
		t.Fatal("인증 준비 실패")
	}
	keyDir, dataDir := firstDiagnosisPrivateTemp(t), firstDiagnosisPrivateTemp(t)
	if os.WriteFile(filepath.Join(keyDir, jwtKeyFilename), []byte("synthetic-owned-url-jwt-material"), 0600) != nil {
		t.Fatal("키 준비 실패")
	}
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Method == "POST" {
			var body struct{ Action string }
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Action != "pause" && body.Action != "resume" {
				t.Error("잘못된 제어 전달")
			}
			writeJSON(w, 200, map[string]any{"id": "42", "status": "paused"})
			return
		}
		activity := `{"seq":7,"ts":"2026-11-13T00:00:00Z","worker":"planner","kind":"tool","summary":"secret-command","detail":"secret-session","metadata":{"password":"secret-password"}}`
		if strings.HasSuffix(r.URL.Path, "/stream") {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\n", activity)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		fmt.Fprintf(w, `{"items":[%s],"cursor":7,"snapshot_cursor":7,"earliest_cursor":7,"has_more":false}`, activity)
	}))
	defer upstream.Close()
	endpoint := &operationsEndpoint{}
	handler, err := NewURLDiagnosisManagementHandler(t.Context(), f.Runtime, keyDir, dataDir, endpoint, upstream.URL)
	if err != nil {
		t.Fatal("실제 관리 handler 조립 실패")
	}
	api := httptest.NewServer(handler)
	defer api.Close()
	login, _ := http.Post(api.URL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"ABC2LAB","password":"owned-fixture-password"}`))
	var session struct{ Token string }
	if login == nil || json.NewDecoder(login.Body).Decode(&session) != nil || session.Token == "" {
		t.Fatal("로그인 실패")
	}
	login.Body.Close()
	request := func(method, path, body, token string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, api.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, e := api.Client().Do(req)
		if e != nil {
			t.Fatal("제품 HTTP 요청 실패")
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	for _, action := range []string{"pause", "resume"} {
		if code, _ := request("POST", "/api/tasks/42/control", `{"action":"`+action+`"}`, session.Token); code != 200 {
			t.Fatalf("공개 제어 실패: %d", code)
		}
	}
	for _, path := range []string{"/api/exploration/activity?task=42", "/api/exploration/activity/history?task=42&session=plan", "/api/exploration/activity/7?task=42"} {
		code, raw := request("GET", path, "", session.Token)
		if code != 200 || strings.Contains(raw, "secret-") {
			t.Fatalf("활동 결속·비밀 보호 실패: %d", code)
		}
	}
	before := hits.Load()
	for _, test := range []struct{ method, path, body, token string }{
		{"POST", "/api/tasks/43/control", `{"action":"pause"}`, session.Token},
		{"POST", "/api/tasks/42/control", `{"action":"rerun"}`, session.Token},
		{"POST", "/api/tasks/42/control", `{"action":"pause","command":"secret"}`, session.Token},
		{"POST", "/api/tasks/42/control", `{"action":"pause"} {}`, session.Token},
		{"POST", "/api/tasks/42/chat", `{}`, session.Token},
		{"GET", "/api/exploration/activity", "", session.Token},
		{"GET", "/api/exploration/activity?task=42&task=43", "", session.Token},
		{"GET", "/api/exploration/activity?task=43", "", session.Token},
		{"GET", "/api/exploration/activity?task=42&intent=99", "", session.Token},
		{"GET", "/api/exploration/activity?task=42", "", ""},
	} {
		code, _ := request(test.method, test.path, test.body, test.token)
		if code < 400 {
			t.Error("금지 요청 허용")
		}
	}
	if hits.Load() != before {
		t.Fatal("금지 요청이 실행 수신기에 도달")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", api.URL+"/api/exploration/activity/stream?task=42&since=0", nil)
	req.Header.Set("Authorization", "Bearer "+session.Token)
	resp, err := api.Client().Do(req)
	if err != nil {
		t.Fatal("활동 스트림 첫 헤더 실패")
	}
	scanner := bufio.NewScanner(resp.Body)
	received := false
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data:") {
			if strings.Contains(scanner.Text(), "secret-") {
				t.Fatal("스트림 원문 유출")
			}
			received = true
			break
		}
	}
	resp.Body.Close()
	cancel()
	if !received {
		t.Fatal("스트림이 종료 전 활동을 전달하지 않음")
	}
	endpoint.revoked.Store(true)
	before = hits.Load()
	if code, _ := request("POST", "/api/tasks/42/control", `{"action":"resume"}`, session.Token); code != 403 || hits.Load() != before {
		t.Fatal("회수 후 제어 전달")
	}
}
