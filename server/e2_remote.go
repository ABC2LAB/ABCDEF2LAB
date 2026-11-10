package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

// 엔진이 eval-e2의 실행 자원을 부르는 통로다. 원문 HTTP 요청은 받지 않고 정해진 연산만 받는다.
const (
	E2RemoteFormat = "abc2lab.e2-remote/v1"
	e2RemotePath   = "/v1/e2"
	// 토큰은 추측 불가능해야 한다. 러너가 회차마다 32바이트 난수의 hex로 만든다.
	e2RemoteMinTokenBytes = 32
	e2RemoteMaxTokenBytes = 512
	// 명령·출력 상한보다 넉넉하게 잡는다. 넘는 요청은 잘라서 받지 않고 거절한다.
	e2RemoteMaxBodyBytes  = 8 << 20
	e2RemoteHeaderTimeout = 5 * time.Second
)

const (
	e2RemoteDescribeOp = "describe"
	e2RemoteAuthorize  = "authorize"
	e2RemoteAcquireOp  = "acquire"
	e2RemoteRegister   = "register_intent"
	e2RemoteRunOp      = "run"
	e2RemoteViewOp     = "view"
	e2RemoteRedactOp   = "redact"
	e2RemoteDrainOp    = "drain_intent"
	e2RemoteStopOp     = "stop"
)

const (
	e2RemoteRunUnavailable = "unavailable"
	e2RemoteRunFailed      = "failed"
)

type e2RemoteRequest struct {
	Format        string `json:"format"`
	Op            string `json:"op"`
	TaskID        string `json:"task_id,omitempty"`
	TaskNumber    int64  `json:"task_number,omitempty"`
	ExplorationID int64  `json:"exploration_id,omitempty"`
	IntentID      int64  `json:"intent_id,omitempty"`
	Command       string `json:"command,omitempty"`
	Data          []byte `json:"data,omitempty"`
}

func (r e2RemoteRequest) identity() E2HTTPRequest {
	return E2HTTPRequest{TaskID: r.TaskID, TaskIDNumber: r.TaskNumber, ExplorationID: r.ExplorationID, IntentID: r.IntentID}
}

type e2RemoteDescribe struct {
	PolicyFingerprintSHA256 string                           `json:"policy_fingerprint_sha256"`
	PolicyConfigSHA256      string                           `json:"policy_config_sha256"`
	HeaderMode              e2proxy.HeaderMode               `json:"header_mode"`
	Evaluation              *e2proxy.EvaluationCallerContext `json:"evaluation,omitempty"`
	RunRef                  string                           `json:"run_ref"`
	PolicyRef               string                           `json:"policy_ref"`
	CaseRef                 string                           `json:"case_ref"`
	PolicyRevision          string                           `json:"policy_revision"`
	BudgetRef               string                           `json:"budget_ref"`
	MaxWorkerTurns          int                              `json:"max_worker_turns"`
	MaxToolCalls            int                              `json:"max_tool_calls"`
	WorkerTimeoutMS         int64                            `json:"worker_timeout_ms"`
	MaxPlannerRounds        uint64                           `json:"max_planner_rounds"`
	MaxIntentRuns           uint64                           `json:"max_intent_runs"`
	DrainTimeoutMS          int64                            `json:"drain_timeout_ms"`
}

type e2RemoteView struct {
	Snapshot        e2proxy.ExecutionView `json:"snapshot"`
	Intent          e2proxy.ExecutionView `json:"intent"`
	HasIntent       bool                  `json:"has_intent"`
	Policy          e2proxy.PolicyView    `json:"policy"`
	BudgetRemaining uint64                `json:"budget_remaining"`
	BudgetKnown     bool                  `json:"budget_known"`
	Calls           []e2proxy.CallResult  `json:"calls"`
}

type e2RemoteResponse struct {
	Format   string            `json:"format"`
	Code     string            `json:"code,omitempty"`
	Describe *e2RemoteDescribe `json:"describe,omitempty"`
	View     *e2RemoteView     `json:"view,omitempty"`
	Result   *e2runner.Result  `json:"result,omitempty"`
	Data     []byte            `json:"data,omitempty"`
	// Run은 명령이 실패해도 Result를 돌려준다. 오류는 내용 없이 종류만 전한다.
	RunError string `json:"run_error,omitempty"`
}

// 파일을 그대로 쓴다. 다른 사용자가 읽을 수 있거나 짧은 토큰은 받지 않는다.
func readE2RemoteToken(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > e2RemoteMaxTokenBytes {
		return nil, ErrE2Control
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, ErrE2Control
	}
	token := []byte(strings.TrimSpace(string(raw)))
	clear(raw)
	if len(token) < e2RemoteMinTokenBytes {
		clear(token)
		return nil, ErrE2Control
	}
	return token, nil
}

type e2RemoteServer struct {
	server *http.Server
	addr   string
	done   chan error
}

func startE2RemoteServer(ctx context.Context, listen string, token []byte, backend *E2StandaloneBackend) (*e2RemoteServer, error) {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", listen)
	if err != nil {
		return nil, ErrE2Control
	}
	digest := sha256.Sum256(token)
	handler := &e2RemoteHandler{tokenDigest: digest, backend: backend}
	s := &e2RemoteServer{server: &http.Server{Handler: handler, ReadHeaderTimeout: e2RemoteHeaderTimeout, BaseContext: func(net.Listener) context.Context { return ctx }}, addr: listener.Addr().String(), done: make(chan error, 1)}
	go func() { s.done <- s.server.Serve(listener) }()
	return s, nil
}

// 호출자 없이 만든 서비스에는 서버가 없다.
func (s *e2RemoteServer) close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	shutdownErr := s.server.Shutdown(ctx)
	if shutdownErr != nil {
		shutdownErr = errors.Join(shutdownErr, s.server.Close())
	}
	select {
	case err := <-s.done:
		if !errors.Is(err, http.ErrServerClosed) {
			return errors.Join(shutdownErr, err)
		}
	case <-ctx.Done():
		return errors.Join(shutdownErr, ctx.Err())
	}
	return shutdownErr
}

type e2RemoteHandler struct {
	tokenDigest [32]byte
	backend     *E2StandaloneBackend
}

func (h *e2RemoteHandler) authorized(r *http.Request) bool {
	given, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || given == "" {
		return false
	}
	digest := sha256.Sum256([]byte(given))
	return subtle.ConstantTimeCompare(digest[:], h.tokenDigest[:]) == 1
}

func e2RemoteWrite(w http.ResponseWriter, status int, response e2RemoteResponse) {
	response.Format = E2RemoteFormat
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

func (h *e2RemoteHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != e2RemotePath || r.Method != http.MethodPost {
		e2RemoteWrite(w, http.StatusNotFound, e2RemoteResponse{Code: "not_found"})
		return
	}
	if !h.authorized(r) {
		e2RemoteWrite(w, http.StatusUnauthorized, e2RemoteResponse{Code: "unauthorized"})
		return
	}
	var request e2RemoteRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, e2RemoteMaxBodyBytes))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || request.Format != E2RemoteFormat {
		e2RemoteWrite(w, http.StatusBadRequest, e2RemoteResponse{Code: "bad_request"})
		return
	}
	response, err := h.dispatch(r.Context(), request)
	if err != nil {
		e2RemoteWrite(w, http.StatusConflict, e2RemoteResponse{Code: "refused"})
		return
	}
	e2RemoteWrite(w, http.StatusOK, response)
}

// 0이 아닌 종료 코드도 Runner는 ErrUnavailable로 돌려준다. 이것과 진짜 실패를 구분해서 넘긴다.
func e2RemoteRunError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, e2runner.ErrUnavailable):
		return e2RemoteRunUnavailable
	}
	return e2RemoteRunFailed
}

func (h *e2RemoteHandler) dispatch(ctx context.Context, request e2RemoteRequest) (e2RemoteResponse, error) {
	b := h.backend
	switch request.Op {
	case e2RemoteDescribeOp:
		described, err := b.remoteDescribe(ctx)
		return e2RemoteResponse{Describe: &described}, err
	case e2RemoteAuthorize:
		return e2RemoteResponse{}, b.remoteAuthorize(ctx, request.identity())
	case e2RemoteAcquireOp:
		return e2RemoteResponse{}, b.remoteAcquire(ctx, request.identity())
	case e2RemoteRegister:
		return e2RemoteResponse{}, b.remoteRegister(ctx, request.IntentID)
	case e2RemoteRunOp:
		result, err := b.Execute(ctx, request.identity(), request.Command)
		return e2RemoteResponse{Result: &result, RunError: e2RemoteRunError(err)}, nil
	case e2RemoteViewOp:
		view, err := b.remoteView(ctx, request.IntentID)
		return e2RemoteResponse{View: &view}, err
	case e2RemoteRedactOp:
		data, err := b.remoteRedact(ctx, request.Data)
		return e2RemoteResponse{Data: data}, err
	case e2RemoteDrainOp:
		return e2RemoteResponse{}, b.remoteDrain(ctx, request.IntentID)
	case e2RemoteStopOp:
		return e2RemoteResponse{}, b.remoteStop(ctx)
	}
	return e2RemoteResponse{}, ErrE2Control
}

func (b *E2StandaloneBackend) remoteDescribe(ctx context.Context) (e2RemoteDescribe, error) {
	if b.check(ctx) != nil {
		return e2RemoteDescribe{}, ErrE2Control
	}
	f, expected := b.factory, b.factory.Expected
	return e2RemoteDescribe{PolicyFingerprintSHA256: f.PolicyFingerprintSHA256, PolicyConfigSHA256: f.PolicyConfigSHA256, HeaderMode: f.HeaderMode, Evaluation: f.Evaluation,
		RunRef: expected.RunRef, PolicyRef: expected.PolicyRef, CaseRef: expected.CaseRef, PolicyRevision: expected.PolicyRevision, BudgetRef: expected.BudgetRef,
		MaxWorkerTurns: f.MaxWorkerTurns, MaxToolCalls: f.MaxToolCalls, WorkerTimeoutMS: f.WorkerTimeout.Milliseconds(), MaxPlannerRounds: f.MaxPlannerRounds, MaxIntentRuns: f.MaxIntentRuns, DrainTimeoutMS: f.DrainTimeout.Milliseconds()}, nil
}

func (b *E2StandaloneBackend) remoteAuthorize(ctx context.Context, q E2HTTPRequest) error {
	if b.check(ctx) != nil {
		return ErrE2Control
	}
	if _, err := b.resetBinding(); err != nil {
		return err
	}
	return b.factory.AuthorizeIntent(ctx, q)
}

func (b *E2StandaloneBackend) remoteAcquire(ctx context.Context, q E2HTTPRequest) error {
	b.op.Lock()
	defer b.op.Unlock()
	if b.check(ctx) != nil {
		return ErrE2Control
	}
	lease, err := b.acquireLocked(ctx, q)
	if err != nil {
		return err
	}
	if lease.Execution == nil || lease.Execution.Snapshot().Scope.TaskRef != q.TaskID {
		return ErrE2Control
	}
	return nil
}

// 실행 자원이 없으면 거절한다. 읽기 호출이 자원을 만들게 하지 않는다.
func (b *E2StandaloneBackend) remoteLease(ctx context.Context) (*E2HTTPRunOptions, error) {
	b.op.Lock()
	defer b.op.Unlock()
	if ctx == nil || ctx.Err() != nil || b.lease == nil || b.lease.Execution == nil || b.lease.Runner == nil {
		return nil, ErrE2Control
	}
	return b.lease, nil
}

func (b *E2StandaloneBackend) remoteRegister(ctx context.Context, intent int64) error {
	lease, err := b.remoteLease(ctx)
	if err != nil {
		return err
	}
	return lease.Execution.RegisterIntent(ctx, intent)
}

func (b *E2StandaloneBackend) remoteView(ctx context.Context, intent int64) (e2RemoteView, error) {
	lease, err := b.remoteLease(ctx)
	if err != nil {
		return e2RemoteView{}, err
	}
	execution := lease.Execution
	remaining, budgetErr := execution.BudgetRemaining()
	view := e2RemoteView{Snapshot: execution.Snapshot(), Policy: execution.Policy(), BudgetRemaining: remaining, BudgetKnown: budgetErr == nil, Calls: execution.Calls()}
	if intent > 0 {
		view.Intent, view.HasIntent = execution.SnapshotForIntent(intent), execution.HasIntent(intent)
	}
	return view, nil
}

func (b *E2StandaloneBackend) remoteRedact(ctx context.Context, data []byte) ([]byte, error) {
	lease, err := b.remoteLease(ctx)
	if err != nil {
		return nil, err
	}
	return lease.Execution.RedactOutput(data), nil
}

func (b *E2StandaloneBackend) remoteDrain(ctx context.Context, intent int64) error {
	lease, err := b.remoteLease(ctx)
	if err != nil {
		return err
	}
	return lease.Runner.DrainIntent(ctx, intent)
}

func (b *E2StandaloneBackend) remoteStop(ctx context.Context) error {
	lease, err := b.remoteLease(ctx)
	if err != nil {
		return err
	}
	return lease.Execution.Stop(ctx)
}
