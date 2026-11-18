//go:build linux

package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisprep"
	"golang.org/x/crypto/bcrypt"
)

type runListPublicBackend struct {
	inspection diagnosisprep.Inspection
	taskID     string
	calls      atomic.Int64
	running    chan struct{}
}

func (b *runListPublicBackend) Profiles(context.Context, string) ([]diagnosisprep.Profile, error) {
	return []diagnosisprep.Profile{{ID: "local", Label: "합성 프로필", ModelProfileRef: "local", ModelLabel: "합성 모델"}}, nil
}
func (b *runListPublicBackend) Inspect(context.Context, string, diagnosisprep.Input) (diagnosisprep.Inspection, error) {
	return b.inspection, nil
}
func (b *runListPublicBackend) Execute(ctx context.Context, _ diagnosisprep.Execution, publish diagnosisprep.Publish) error {
	b.calls.Add(1)
	u := diagnosisprep.Update{State: diagnosisprep.Running, Phase: diagnosisprep.ExecutionPhase, TaskID: b.taskID,
		PreparationStarted: true, Started: true, AuthorityState: diagnosisprep.AuthorityConsumed}
	if err := publish(u); err != nil {
		return err
	}
	close(b.running)
	<-ctx.Done()
	u.State, u.Phase, u.DrainComplete, u.ResultReady = diagnosisprep.Cancelled, diagnosisprep.Closure, true, true
	u.ReasonCode = "stop_requested"
	return publish(u)
}

// 실제 설치 인증 → URL v2 → 보호 Unix IPC → 영속 Job → 저장된 task 활동/보고서를 통과한다.
// 실행 backend만 합성이며 실제 모델·Bash·대상 송신 또는 Workbench 브라우저 수용으로 계산하지 않는다.
func TestRunListPublicEntryReconnectStopAndRestartOwnedPostgres(t *testing.T) {
	f := newControlledLocalDatabaseFixture(t)
	installer := &db.DB{DB: f.Installer}
	task, err := installer.CreateTaskWithOptions("저장 조회 시험", "합성 결과 조회", db.TaskCreateOptions{Name: "다시 찾은 합성 점검"})
	if err != nil {
		t.Fatal(err)
	}
	defer installer.DeleteTask(task.ID)
	taskID := strconv.FormatInt(task.ID, 10)
	store := installer.Exploration(task.ExplorationID)
	activityID, err := store.AppendActivity(db.Activity{Worker: "planner", Kind: "observation", Summary: "저장된 합성 관찰"})
	if err != nil {
		t.Fatal(err)
	}
	runtimeServer := &Server{m: &Manager{pg: installer, tasks: map[string]*Task{taskID: {ID: taskID, Store: store}}}}
	runtimeMux := http.NewServeMux()
	runtimeMux.HandleFunc("GET /api/exploration/activity", runtimeServer.activity)
	runtimeMux.HandleFunc("GET /api/report/diagnostic", runtimeServer.diagnosticReport)
	runtimeAPI := httptest.NewServer(runtimeMux)
	defer runtimeAPI.Close()

	password := "synthetic-run-list-password"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil || installer.SetSetting(authPassKey, string(hash)) != nil {
		t.Fatal("합성 인증 준비 실패")
	}
	keyDir, dataDir := firstDiagnosisPrivateTemp(t), firstDiagnosisPrivateTemp(t)
	if os.WriteFile(filepath.Join(keyDir, jwtKeyFilename), []byte("synthetic-owned-run-list-jwt-material"), 0600) != nil {
		t.Fatal("합성 인증키 준비 실패")
	}
	root, err := os.MkdirTemp("", "jrl-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	budget := diagnosisprep.Budget{Requests: 4, ModelRequests: 3, InputTokens: 30, OutputTokens: 20, DurationSeconds: 60}
	backend := &runListPublicBackend{taskID: taskID, running: make(chan struct{}), inspection: diagnosisprep.Inspection{
		Scope: &diagnosisprep.Scope{TargetRef: "owned-target", ScopeRef: "owned-scope", Origin: "https://owned.example",
			PathPrefixes: []string{"/"}, Methods: []string{"GET"}, QueryPolicy: "any", BodyPolicy: "bounded", MaxBodyBytes: 1024,
			ApprovalMode: "preauthorized_scope", AttestationRef: "owned-proof", Budget: budget, ExpiresAt: time.Now().UTC().Add(time.Hour)},
		InstallationDigest: strings.Repeat("a", 64), DelegationRef: "owned-delegation", MaxRuns: 4,
		AggregateBudget: diagnosisprep.Budget{Requests: 16, ModelRequests: 12, InputTokens: 120, OutputTokens: 80, DurationSeconds: 240}}}
	service, err := diagnosisprep.New(t.Context(), root, backend)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { service.Shutdown(t.Context()) }()
	socket := filepath.Join(root, "prepare.sock")
	transport, err := diagnosisprep.ListenUnix(t.Context(), socket, service)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { transport.Close(t.Context()) }()
	client, err := diagnosisprep.NewUnixClient(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { client.Close() }()
	newAPI := func() *httptest.Server {
		t.Helper()
		handler, err := NewURLDiagnosisManagementHandler(t.Context(), f.Runtime, keyDir, dataDir, client, runtimeAPI.URL)
		if err != nil {
			t.Fatal(err)
		}
		return httptest.NewServer(handler)
	}
	api := newAPI()
	defer func() { api.Close() }()
	request := func(method, path, body, key, token string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, api.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		response, err := api.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, raw
	}
	login := func() string {
		t.Helper()
		code, raw := request("POST", "/api/auth/login", `{"username":"ABC2LAB","password":"`+password+`"}`, "", "")
		var value struct{ Token string }
		if code != 200 || json.Unmarshal(raw, &value) != nil || value.Token == "" {
			t.Fatal("실제 설치 로그인 실패", code)
		}
		return value.Token
	}
	token := login()
	base := "/api/diagnoses/v2"
	code, raw := request("POST", base+"/drafts", `{"version":"diagnosis-api.v2","input":{"target_url":"https://owned.example/","profile_id":"local","display_name":"다시 찾은 점검"}}`, "private-create-key", token)
	var draft struct{ Draft *diagnosisprep.Job }
	if code != 201 || json.Unmarshal(raw, &draft) != nil || draft.Draft == nil {
		t.Fatal("초안 생성 실패", code)
	}
	if strings.Contains(string(raw), `"started_at"`) || strings.Contains(string(raw), `"finished_at"`) {
		t.Fatal("시작 전 응답에 미확인 시각을 게시했습니다")
	}
	id := draft.Draft.ID
	mutation := func(revision uint64) string {
		body, _ := json.Marshal(map[string]any{"version": "diagnosis-api.v2", "expected_revision": revision})
		return string(body)
	}
	code, _ = request("POST", base+"/drafts/"+id+"/start", mutation(draft.Draft.Revision), "private-start-key", token)
	if code != 200 {
		t.Fatal("시작 실패", code)
	}
	select {
	case <-backend.running:
	case <-time.After(3 * time.Second):
		t.Fatal("합성 실행 진입 미확인")
	}
	api.Close()
	api = newAPI() // 새 HTTP 세션은 Job ID·멱등 키 없이 목록부터 재발견한다.
	token = login()
	code, raw = request("GET", base+"/runs", "", "", token)
	var list struct{ Page *diagnosisprep.RunPage }
	if code != 200 || json.Unmarshal(raw, &list) != nil || list.Page == nil || len(list.Page.Runs) != 1 {
		t.Fatal("새 세션 목록 복구 실패", code)
	}
	run := list.Page.Runs[0]
	if run.ID != id || run.TaskID != taskID || run.State != diagnosisprep.Running || !run.ActionRequired || backend.calls.Load() != 1 {
		t.Fatal("기존 실행 연결·재실행 방지 실패")
	}
	if run.StartedAt == nil || run.StartedAt.IsZero() || run.StartedAt.Location() != time.UTC || run.FinishedAt != nil || strings.Contains(string(raw), `"finished_at"`) {
		t.Fatal("실제 제품 목록의 시작 시각·마감 전 생략 계약 위반")
	}
	startedAt := *run.StartedAt
	code, raw = request("GET", "/api/exploration/activity?task="+run.TaskID, "", "", token)
	var activities struct {
		Items []struct {
			Seq     int64  `json:"seq"`
			Summary string `json:"summary"`
		}
	}
	if code != 200 || json.Unmarshal(raw, &activities) != nil || len(activities.Items) != 1 || activities.Items[0].Seq != activityID || activities.Items[0].Summary != "진단 활동 기록" || strings.Contains(string(raw), "저장된 합성 관찰") {
		t.Fatal("실제 저장 활동 재연결 실패", code)
	}
	code, raw = request("GET", base+"/drafts/"+run.ID+"/status", "", "", token)
	if code != 200 || json.Unmarshal(raw, &draft) != nil {
		t.Fatal("재발견한 회차 상태 조회 실패")
	}
	if draft.Draft.StartedAt == nil || !draft.Draft.StartedAt.Equal(startedAt) || draft.Draft.FinishedAt != nil {
		t.Fatal("제품 상태 조회와 목록의 확인 시각이 다릅니다")
	}
	code, _ = request("POST", base+"/drafts/"+run.ID+"/stop", mutation(draft.Draft.Revision), "", token)
	if code != 200 {
		t.Fatal("재발견한 회차 중지 실패", code)
	}
	if err := service.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := transport.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	service, err = diagnosisprep.New(t.Context(), root, backend)
	if err != nil {
		t.Fatal(err)
	}
	transport, err = diagnosisprep.ListenUnix(t.Context(), socket, service)
	if err != nil {
		t.Fatal(err)
	}
	client, err = diagnosisprep.NewUnixClient(socket)
	if err != nil {
		t.Fatal(err)
	}
	api.Close()
	api = newAPI()
	token = login()
	code, raw = request("GET", base+"/runs", "", "", token)
	if code != 200 || json.Unmarshal(raw, &list) != nil || len(list.Page.Runs) != 1 || list.Page.Runs[0].State != diagnosisprep.Cancelled || !list.Page.Runs[0].ReportAvailable || backend.calls.Load() != 1 {
		t.Fatal("서비스 재시작 뒤 상태·결과 보존 또는 재실행 방지 실패", code)
	}
	closed := list.Page.Runs[0]
	if closed.StartedAt == nil || closed.FinishedAt == nil || !closed.StartedAt.Equal(startedAt) || closed.FinishedAt.Location() != time.UTC || closed.FinishedAt.Before(startedAt) {
		t.Fatal("재시작한 제품 목록의 시작·소요 근거가 없습니다")
	}
	code, raw = request("GET", base+"/drafts/"+closed.ID+"/status", "", "", token)
	if code != 200 || json.Unmarshal(raw, &draft) != nil || draft.Draft.StartedAt == nil || draft.Draft.FinishedAt == nil ||
		!draft.Draft.StartedAt.Equal(*closed.StartedAt) || !draft.Draft.FinishedAt.Equal(*closed.FinishedAt) {
		t.Fatal("재시작 뒤 보고서 재연결에 쓰는 확인 시각이 달라졌습니다")
	}
	code, raw = request("GET", "/api/report/diagnostic?task="+list.Page.Runs[0].TaskID+"&audience=internal", "", "", token)
	if code != 200 || !strings.Contains(string(raw), "다시 찾은 합성 점검") {
		t.Fatal("재시작 뒤 실제 저장 보고서 재연결 실패", code)
	}
	code, raw = request("GET", "/api/report/diagnostic?task="+list.Page.Runs[0].TaskID, "", "", token)
	if code != 200 || strings.Contains(string(raw), "다시 찾은 합성 점검") {
		t.Fatal("기존 공유 보고서의 자유문장 비공개 계약 변경", code)
	}
	if code, _ := request("GET", base+"/runs", "", "", ""); code != 401 {
		t.Fatal("미인증 목록 공개")
	}
}
