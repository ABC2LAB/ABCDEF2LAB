//go:build linux

package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/transmit"
)

const e2RemoteTestToken = "0123456789abcdef0123456789abcdef0123456789abcdef"

var e2RemoteTestRequest = E2HTTPRequest{TaskID: "11", TaskIDNumber: 11, ExplorationID: 23, IntentID: 17}

func writeE2RemoteToken(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "engine-token")
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

type e2RemoteFixture struct {
	backend  *E2StandaloneBackend
	server   *e2RemoteServer
	client   *e2RemoteClient
	target   *httptest.Server
	received *atomic.Int64
}

func startE2RemoteFixture(t *testing.T) e2RemoteFixture {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("비특권 Linux 격리 fixture")
	}
	received := &atomic.Int64{}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { received.Add(1); _, _ = w.Write([]byte("fixture")) }))
	t.Cleanup(target.Close)
	backend, _, _ := standaloneFixture(t, target.URL, true)
	if err := backend.BindReset(t.Context(), strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	server, err := startE2RemoteServer(ctx, "127.0.0.1:0", []byte(e2RemoteTestToken), backend)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closing, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if err := server.close(closing); err != nil {
			t.Error(err)
		}
	})
	client, err := newE2RemoteClient("http://"+server.addr, writeE2RemoteToken(t, e2RemoteTestToken), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return e2RemoteFixture{backend, server, client, target, received}
}

func (f e2RemoteFixture) acquire(t *testing.T) *e2RemoteRun {
	t.Helper()
	lease, err := f.client.acquire(context.Background(), e2RemoteTestRequest)
	if err != nil {
		t.Fatal("실행 자원 획득 실패:", err)
	}
	if lease.Release == nil || lease.Runner == nil {
		t.Fatal("불완전한 lease")
	}
	run := lease.Runner.(*e2RemoteRun)
	if err := run.RegisterIntent(context.Background(), e2RemoteTestRequest.IntentID); err != nil {
		t.Fatal(err)
	}
	return run
}

func (f e2RemoteFixture) curl(path string) string {
	return fmt.Sprintf("curl --silent --max-time 2 '%s%s' >/dev/null", f.target.URL, path)
}

func TestE2RemoteRunsThroughTheBackend(t *testing.T) {
	f := startE2RemoteFixture(t)
	run := f.acquire(t)
	ctx := context.Background()
	if !run.HasIntent(17) || run.HasIntent(99) {
		t.Fatal("intent 등록 상태가 틀림")
	}
	if view := run.SnapshotForIntent(17); view.State != "active" || view.Version != e2proxy.Version || view.Scope.TaskRef != "11" {
		t.Fatalf("intent 조회: %+v", view)
	}
	if remaining, err := run.BudgetRemaining(); err != nil || remaining == 0 {
		t.Fatal("예산 조회", remaining, err)
	}
	if run.Policy().BudgetRef == "" {
		t.Fatal("정책 조회가 비어 있음")
	}
	result, err := run.RunForIntent(ctx, 17, f.curl("/allowed"))
	if err != nil || !result.Trusted.Final || f.received.Load() != 1 {
		t.Fatalf("허용 요청: err=%v final=%v received=%d", err, result.Trusted.Final, f.received.Load())
	}
	if _, err := run.RunForIntent(ctx, 17, f.curl("/denied")); err != nil && !errors.Is(err, e2runner.ErrUnavailable) {
		t.Fatal("차단 요청의 실행 오류 종류:", err)
	}
	if f.received.Load() != 1 {
		t.Fatal("범위 밖 요청이 대상에 도달", f.received.Load())
	}
	failed, err := run.RunForIntent(ctx, 17, "exit 3")
	if !errors.Is(err, e2runner.ErrUnavailable) || !failed.Trusted.Final || failed.Trusted.ExitCode != 3 {
		t.Fatalf("0이 아닌 종료: err=%v final=%v code=%d", err, failed.Trusted.Final, failed.Trusted.ExitCode)
	}
	if _, err := run.RunForIntent(ctx, 18, "true"); !errors.Is(err, ErrE2Control) || errors.Is(err, e2runner.ErrUnavailable) {
		t.Fatal("등록하지 않은 intent가 실행됨:", err)
	}
	if calls := run.Calls(); len(calls) != 3 {
		t.Fatal("호출 기록 수", len(calls))
	}
	if string(run.RedactOutput([]byte("plain"))) != "plain" {
		t.Fatal("가림 호출 실패")
	}
	if err := run.DrainIntent(ctx, 17); err != nil {
		t.Fatal(err)
	}
	if err := run.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if state := run.Snapshot().State; state == "active" {
		t.Fatal("중지 뒤에도 활성 상태")
	}
}

// 도구 호출 상한이 있어 별도 fixture로 잰다.
func TestE2RemoteSandboxCannotReachTheChannelOrNest(t *testing.T) {
	f := startE2RemoteFixture(t)
	run := f.acquire(t)
	ctx := context.Background()
	// 샌드박스는 빈 netns라 같은 프로세스의 엔진 통로에 닿으면 안 된다. HTTP로 닿으면 401이어도 curl은 rc=0이다.
	reach, err := run.RunForIntent(ctx, 17, fmt.Sprintf("curl --noproxy '*' --silent --max-time 2 -o /dev/null http://%s%s; echo rc=$?", f.server.addr, e2RemotePath))
	if err != nil || !strings.Contains(reach.ToolOutput, "rc=7") {
		t.Fatalf("샌드박스가 엔진 통로에 닿음: err=%v output=%q", err, reach.ToolOutput)
	}
	// 없는 명령(rc=127)을 거절로 착각하지 않게 정확한 값을 본다.
	nested, err := run.RunForIntent(ctx, 17, "unshare -U true; echo rc=$?")
	if err != nil || !strings.Contains(nested.ToolOutput, "rc=1") {
		t.Fatalf("샌드박스 안에서 namespace를 다시 만듦: err=%v output=%q", err, nested.ToolOutput)
	}
}

func TestE2RemoteRefusesBadCalls(t *testing.T) {
	f := startE2RemoteFixture(t)
	url := "http://" + f.server.addr
	body := `{"format":"` + E2RemoteFormat + `","op":"describe"}`
	for name, tc := range map[string]struct {
		method, path, token, body string
		status                    int
	}{
		"정상 대조":     {http.MethodPost, e2RemotePath, e2RemoteTestToken, body, http.StatusOK},
		"GET":       {http.MethodGet, e2RemotePath, e2RemoteTestToken, "", http.StatusNotFound},
		"다른 경로":     {http.MethodPost, "/v1/other", e2RemoteTestToken, body, http.StatusNotFound},
		"토큰 없음":     {http.MethodPost, e2RemotePath, "", body, http.StatusUnauthorized},
		"접두사 없는 토큰": {http.MethodPost, e2RemotePath, "\x00raw", body, http.StatusUnauthorized},
		"다른 토큰":     {http.MethodPost, e2RemotePath, strings.Repeat("x", 48), body, http.StatusUnauthorized},
		"모르는 필드":    {http.MethodPost, e2RemotePath, e2RemoteTestToken, `{"format":"` + E2RemoteFormat + `","op":"describe","x":1}`, http.StatusBadRequest},
		"다른 형식":     {http.MethodPost, e2RemotePath, e2RemoteTestToken, `{"format":"other","op":"describe"}`, http.StatusBadRequest},
		"본문 초과":     {http.MethodPost, e2RemotePath, e2RemoteTestToken, `{"format":"` + E2RemoteFormat + `","op":"redact","command":"` + strings.Repeat("a", e2RemoteMaxBodyBytes) + `"}`, http.StatusBadRequest},
		"모르는 연산":    {http.MethodPost, e2RemotePath, e2RemoteTestToken, `{"format":"` + E2RemoteFormat + `","op":"shell"}`, http.StatusConflict},
		"획득 전 조회":   {http.MethodPost, e2RemotePath, e2RemoteTestToken, `{"format":"` + E2RemoteFormat + `","op":"view"}`, http.StatusConflict},
	} {
		t.Run(name, func(t *testing.T) {
			request, err := http.NewRequest(tc.method, url+tc.path, strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case tc.token == "\x00raw":
				request.Header.Set("Authorization", e2RemoteTestToken)
			case tc.token != "":
				request.Header.Set("Authorization", "Bearer "+tc.token)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != tc.status {
				t.Fatalf("status=%d", response.StatusCode)
			}
		})
	}
	other := e2RemoteTestRequest
	other.TaskID, other.TaskIDNumber = "12", 12
	if _, err := f.client.acquire(context.Background(), other); err == nil {
		t.Fatal("승인되지 않은 task가 실행 자원을 얻음")
	}
	if f.received.Load() != 0 {
		t.Fatal("거절된 호출이 대상에 도달")
	}
}

func TestE2RemoteClientFailsClosed(t *testing.T) {
	client, err := newE2RemoteClient("http://127.0.0.1:1", writeE2RemoteToken(t, e2RemoteTestToken), 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	run := &e2RemoteRun{client: client, ctx: ctx, q: e2RemoteTestRequest}
	if run.SnapshotForIntent(17).State == "active" || run.Snapshot().State == "active" || run.HasIntent(17) {
		t.Fatal("연결 실패가 활성으로 보임")
	}
	if run.RedactOutput([]byte("secret")) != nil {
		t.Fatal("가리지 못한 원문을 돌려줌")
	}
	if _, err := run.BudgetRemaining(); err == nil {
		t.Fatal("예산 조회 실패를 숨김")
	}
	if len(run.Calls()) != 0 || run.Policy().BudgetRef != "" {
		t.Fatal("연결 실패인데 값이 있음")
	}
	if _, err := run.RunForIntent(ctx, 17, "true"); !errors.Is(err, ErrE2Control) || errors.Is(err, e2runner.ErrUnavailable) {
		t.Fatal("연결 실패의 실행 오류 종류:", err)
	}
	if run.RegisterIntent(ctx, 17) == nil || run.Stop(ctx) == nil || run.DrainIntent(ctx, 17) == nil {
		t.Fatal("연결 실패를 숨김")
	}
	if _, err := client.acquire(ctx, e2RemoteTestRequest); err == nil {
		t.Fatal("연결 실패인데 lease를 줌")
	}
	if client.authorize(ctx, e2RemoteTestRequest) == nil {
		t.Fatal("연결 실패인데 승인")
	}
	if _, err := client.describe(ctx); err == nil {
		t.Fatal("연결 실패인데 설명을 줌")
	}
}

func TestE2RemoteClientMapsRunErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		runError string
		want     error
	}{
		"성공":      {"", nil},
		"0 아닌 종료": {e2RemoteRunUnavailable, e2runner.ErrUnavailable},
		"실패":      {e2RemoteRunFailed, ErrE2Control},
		"모르는 값":   {"other", ErrE2Control},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				e2RemoteWrite(w, http.StatusOK, e2RemoteResponse{Result: &e2runner.Result{ToolOutput: "out"}, RunError: tc.runError})
			}))
			defer server.Close()
			client, err := newE2RemoteClient(server.URL, writeE2RemoteToken(t, e2RemoteTestToken), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			run := &e2RemoteRun{client: client, ctx: context.Background(), q: e2RemoteTestRequest}
			result, err := run.RunForIntent(context.Background(), 17, "true")
			if !errors.Is(err, tc.want) || result.ToolOutput != "out" {
				t.Fatalf("err=%v output=%q", err, result.ToolOutput)
			}
		})
	}
	if e2RemoteRunError(nil) != "" || e2RemoteRunError(e2runner.ErrUnavailable) != e2RemoteRunUnavailable || e2RemoteRunError(errors.New("x")) != e2RemoteRunFailed {
		t.Fatal("실행 오류 분류가 틀림")
	}
}

func TestE2RemoteClientRejectsBadResponses(t *testing.T) {
	valid := func(extra string) string { return `{"format":"` + E2RemoteFormat + `"` + extra + `}` }
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"형식은 맞는 500": {http.StatusInternalServerError, valid(`,"describe":{}`)},
		"다른 형식":      {http.StatusOK, `{"format":"other","describe":{}}`},
		"오류 코드":      {http.StatusOK, valid(`,"code":"refused","describe":{}`)},
		"본문 초과":      {http.StatusOK, valid(`,"describe":{}`) + strings.Repeat(" ", e2RemoteMaxBodyBytes)},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client, err := newE2RemoteClient(server.URL, writeE2RemoteToken(t, e2RemoteTestToken), 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.describe(context.Background()); err == nil {
				t.Fatal("잘못된 응답을 받음")
			}
		})
	}
}

func TestE2RemoteClientReportsUnknownBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		e2RemoteWrite(w, http.StatusOK, e2RemoteResponse{View: &e2RemoteView{BudgetRemaining: 9, BudgetKnown: false}})
	}))
	defer server.Close()
	client, err := newE2RemoteClient(server.URL, writeE2RemoteToken(t, e2RemoteTestToken), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	run := &e2RemoteRun{client: client, ctx: context.Background(), q: e2RemoteTestRequest}
	if _, err := run.BudgetRemaining(); err == nil {
		t.Fatal("모르는 예산을 값으로 돌려줌")
	}
}

// 환경 프록시를 타면 실행 요청이 기록 프록시로 샌다. loopback은 프록시를 안 타서 동작으로는 못 잰다.
func TestE2RemoteClientIgnoresEnvironmentProxy(t *testing.T) {
	client, err := newE2RemoteClient("http://127.0.0.1:1", writeE2RemoteToken(t, e2RemoteTestToken), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if client.http.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("환경 프록시 사용")
	}
}

func TestE2RemoteClientDoesNotFollowRedirects(t *testing.T) {
	var followed atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			followed.Add(1)
		}
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, err := newE2RemoteClient(server.URL, writeE2RemoteToken(t, e2RemoteTestToken), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.describe(context.Background()); err == nil || followed.Load() != 0 {
		t.Fatal("리다이렉트를 따라감", err, followed.Load())
	}
}

func TestReadE2RemoteToken(t *testing.T) {
	dir := t.TempDir()
	write := func(name, value string, mode os.FileMode) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(value), mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if token, err := readE2RemoteToken(write("ok", e2RemoteTestToken+"\n", 0600)); err != nil || string(token) != e2RemoteTestToken {
		t.Fatal("정상 토큰 거부", err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(filepath.Join(dir, "ok"), link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"파이프":    fifo,
		"짧음":     write("short", strings.Repeat("a", e2RemoteMinTokenBytes-1), 0600),
		"다른 사용자": write("wide", e2RemoteTestToken, 0640),
		"너무 김":   write("long", strings.Repeat("a", e2RemoteMaxTokenBytes+1), 0600),
		"링크":     link,
		"디렉터리":   dir,
		"없음":     filepath.Join(dir, "missing"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := readE2RemoteToken(path); err == nil {
				t.Fatal("거부해야 하는 토큰을 받음")
			}
		})
	}
}

func TestE2EvalTaskBindingKeepsTheFirstTask(t *testing.T) {
	binding := &e2EvalTaskBinding{}
	ctx := context.Background()
	for name, tc := range map[string]struct {
		scope  transmit.Scope
		intent int64
	}{"intent 0": {transmit.Scope{TaskRef: "11"}, 0}, "task 없음": {transmit.Scope{}, 17}} {
		if binding.AuthorizeExecution(ctx, tc.scope, tc.intent) == nil {
			t.Fatal(name, "수용")
		}
	}
	if err := binding.AuthorizeExecution(ctx, transmit.Scope{TaskRef: "11"}, 17); err != nil {
		t.Fatal("첫 task 거부:", err)
	}
	if err := binding.AuthorizeExecution(ctx, transmit.Scope{TaskRef: "11"}, 18); err != nil {
		t.Fatal("같은 task의 다른 intent 거부:", err)
	}
	if binding.AuthorizeExecution(ctx, transmit.Scope{TaskRef: "12"}, 17) == nil {
		t.Fatal("다른 task 수용")
	}
}

func TestE2EvalServiceOpensTheEngineChannelOnlyWithAToken(t *testing.T) {
	without := startE2EvalFixture(t)
	if without.service.remote != nil {
		t.Fatal("토큰 없이 엔진 호출 경로가 열림")
	}
	without.close(t)

	run := e2ProducedRunFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tokenPath := writeE2RemoteToken(t, e2RemoteTestToken)
	service, err := StartE2EvalService(ctx, E2EvalServiceOptions{ConfigPath: run.configPath, IOTimeout: time.Second, CloseTimeout: 5 * time.Second, EngineTokenPath: tokenPath})
	if err != nil {
		t.Fatal("토큰이 있는 서비스 기동 실패:", err)
	}
	address := service.remote.addr
	client, err := newE2RemoteClient("http://"+address, tokenPath, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	described, err := client.describe(ctx)
	if err != nil || described.PolicyFingerprintSHA256 != run.expectedFingerprint || described.RunRef == "" || described.MaxToolCalls <= 0 || described.WorkerTimeoutMS <= 0 {
		t.Fatalf("describe: %+v err=%v", described, err)
	}
	if _, ok := service.backend.options.Supervisor.(*e2EvalTaskBinding); !ok {
		t.Fatal("토큰이 있는데 task 고정이 없음")
	}
	closing, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if err := service.Close(closing); err != nil {
		t.Fatal(err)
	}
	if conn, err := net.DialTimeout("tcp", address, time.Second); err == nil {
		_ = conn.Close()
		t.Fatal("서비스 종료 뒤에도 엔진 호출 경로가 열림")
	}
}

func TestE2EvalServiceRejectsABadEngineToken(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("비특권 Linux 제어 fixture")
	}
	run := e2ProducedRunFixture(t)
	_, err := StartE2EvalService(context.Background(), E2EvalServiceOptions{ConfigPath: run.configPath, IOTimeout: time.Second, CloseTimeout: time.Second, EngineTokenPath: writeE2RemoteToken(t, "short")})
	if !errors.Is(err, ErrE2Control) || !strings.Contains(err.Error(), ": engine_token") {
		t.Fatal(err)
	}
}
