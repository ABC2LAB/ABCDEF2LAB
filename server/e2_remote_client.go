package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

// 조회가 실패했을 때의 상태 이름이다. 실행이 닫혔다는 뜻이 아니다.
const e2RemoteViewUnavailable = "unavailable"

// e2RemoteClient는 엔진 쪽에서 eval-e2의 실행 자원을 부른다.
type e2RemoteClient struct {
	url     string
	token   string
	http    *http.Client
	timeout time.Duration
}

func (*e2RemoteClient) String() string   { return "server.e2RemoteClient[protected]" }
func (*e2RemoteClient) GoString() string { return "server.e2RemoteClient[protected]" }

func newE2RemoteClient(baseURL, tokenPath string, timeout time.Duration) (*e2RemoteClient, error) {
	if baseURL == "" || timeout <= 0 {
		return nil, ErrE2Control
	}
	token, err := readE2RemoteToken(tokenPath)
	if err != nil {
		return nil, err
	}
	defer clear(token)
	// 환경의 프록시를 타면 E② 호출이 기록 프록시로 샌다. 리다이렉트도 따르지 않는다.
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DisableCompression: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &e2RemoteClient{url: baseURL + e2RemotePath, token: string(token), http: client, timeout: timeout}, nil
}

func (c *e2RemoteClient) call(ctx context.Context, request e2RemoteRequest) (e2RemoteResponse, error) {
	request.Format = E2RemoteFormat
	body, err := json.Marshal(request)
	if err != nil {
		return e2RemoteResponse{}, ErrE2Control
	}
	outbound, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return e2RemoteResponse{}, ErrE2Control
	}
	outbound.Header.Set("Authorization", "Bearer "+c.token)
	outbound.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(outbound)
	if err != nil {
		return e2RemoteResponse{}, ErrE2Control
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, e2RemoteMaxBodyBytes+1))
	if err != nil || len(raw) > e2RemoteMaxBodyBytes || response.StatusCode != http.StatusOK {
		return e2RemoteResponse{}, ErrE2Control
	}
	var parsed e2RemoteResponse
	if json.Unmarshal(raw, &parsed) != nil || parsed.Format != E2RemoteFormat || parsed.Code != "" {
		return e2RemoteResponse{}, ErrE2Control
	}
	return parsed, nil
}

// 시한이 있는 읽기 호출이다. 실행 호출은 호출자의 시한을 그대로 쓴다.
func (c *e2RemoteClient) bounded(parent context.Context, request e2RemoteRequest) (e2RemoteResponse, error) {
	ctx, cancel := context.WithTimeout(parent, c.timeout)
	defer cancel()
	return c.call(ctx, request)
}

func e2RemoteIdentity(op string, q E2HTTPRequest) e2RemoteRequest {
	return e2RemoteRequest{Op: op, TaskID: q.TaskID, TaskNumber: q.TaskIDNumber, ExplorationID: q.ExplorationID, IntentID: q.IntentID}
}

func (c *e2RemoteClient) describe(ctx context.Context) (e2RemoteDescribe, error) {
	response, err := c.bounded(ctx, e2RemoteRequest{Op: e2RemoteDescribeOp})
	if err != nil || response.Describe == nil {
		return e2RemoteDescribe{}, ErrE2Control
	}
	return *response.Describe, nil
}

func (c *e2RemoteClient) authorize(ctx context.Context, q E2HTTPRequest) error {
	_, err := c.bounded(ctx, e2RemoteIdentity(e2RemoteAuthorize, q))
	return err
}

// 실행 자원은 E²가 소유한다. 엔진이 들고 있는 건 부르는 길뿐이라 Release할 것이 없다.
func (c *e2RemoteClient) acquire(ctx context.Context, q E2HTTPRequest) (E2HTTPRunOptions, error) {
	// 원격 E②는 탐색 몫을 뺀 예산 등록을 아직 받지 않는다. 몫이 있으면 회차 합계를 넘지 않게 거부한다(P1-E).
	if q.ExplorationRequests != 0 || q.ExplorationUnknown {
		return E2HTTPRunOptions{}, ErrE2HTTPHost
	}
	if _, err := c.bounded(ctx, e2RemoteIdentity(e2RemoteAcquireOp, q)); err != nil {
		return E2HTTPRunOptions{}, err
	}
	// 조회는 task가 취소된 뒤 마감 단계에서도 돌아야 한다. 각 호출은 자체 시한으로 끊긴다.
	run := &e2RemoteRun{client: c, ctx: context.WithoutCancel(ctx), q: q}
	return E2HTTPRunOptions{Runner: run, Release: func(context.Context) error { return nil }}, nil
}

type e2RemoteRun struct {
	client *e2RemoteClient
	ctx    context.Context
	q      E2HTTPRequest
	mu     sync.Mutex
	// Only acknowledged results received on this authenticated RPC enter here.
	// The producer verifies its durable control event before replying. A local
	// callback cannot invent a receipt, and each receipt can be consumed once.
	notSent map[string]e2proxy.Receipt
}

func (r *e2RemoteRun) identity(op string, intent int64) e2RemoteRequest {
	q := r.q
	q.IntentID = intent
	return e2RemoteIdentity(op, q)
}

func (r *e2RemoteRun) view(intent int64) (e2RemoteView, bool) {
	response, err := r.client.bounded(r.ctx, r.identity(e2RemoteViewOp, intent))
	if err != nil || response.View == nil {
		return e2RemoteView{}, false
	}
	return *response.View, true
}

// 조회가 안 되면 활성 상태로 보이면 안 된다. Worker는 active가 아니면 실행하지 않는다.
func (r *e2RemoteRun) SnapshotForIntent(id int64) e2proxy.ExecutionView {
	view, ok := r.view(id)
	if !ok {
		return e2proxy.ExecutionView{State: e2RemoteViewUnavailable}
	}
	return view.Intent
}

func (r *e2RemoteRun) HasIntent(id int64) bool {
	view, ok := r.view(id)
	return ok && view.HasIntent
}

func (r *e2RemoteRun) ProgramCalls() map[string]bool {
	view, ok := r.view(0)
	if !ok {
		return nil
	}
	return view.ProgramCalls
}

// 가리지 못한 원문을 돌려주면 안 되므로 실패하면 비운다.
func (r *e2RemoteRun) RedactOutput(data []byte) []byte {
	return r.redact(e2RemoteRedactOp, data)
}

// RedactRecord는 저장 기록용 가림이다. 실패하면 비우는 것은 RedactOutput과 같다.
func (r *e2RemoteRun) RedactRecord(data []byte) []byte {
	return r.redact(e2RemoteRedactRecordOp, data)
}

func (r *e2RemoteRun) redact(op string, data []byte) []byte {
	request := r.identity(op, 0)
	request.Data = data
	response, err := r.client.bounded(r.ctx, request)
	if err != nil {
		return nil
	}
	return response.Data
}

func (r *e2RemoteRun) RunForIntent(ctx context.Context, id int64, command string) (e2runner.Result, error) {
	request := r.identity(e2RemoteRunOp, id)
	request.Command = command
	// 명령 시간 예산(timeout_ms)과 흐르기 시작한 시각은 Host 쪽 러너가 쓴다: 지난 시간을 빼고 승인 대기 동안만 멈춘다
	// (#898 발견 3, #910 이동찬 리뷰). 1ms 미만은 싣지 않는다(러너 기본값).
	if budget, start, ok := e2runner.CommandBudget(ctx); ok && budget.Milliseconds() > 0 {
		request.TimeoutMS, request.BudgetStartUnixMS = budget.Milliseconds(), start.UnixMilli()
	}
	response, err := r.client.call(ctx, request)
	if err != nil || response.Result == nil {
		return e2runner.Result{}, ErrE2Control
	}
	switch response.RunError {
	case "":
		return *response.Result, nil
	case e2RemoteRunUnavailable:
		if e2runner.AdmissionNotSent(*response.Result) && response.Result.Trusted.Execution.Scope.TaskRef == r.q.TaskID && response.Result.Trusted.Execution.IntentID == id {
			receipt := *response.Result.Trusted.AdmissionNotSent
			r.mu.Lock()
			if r.notSent == nil {
				r.notSent = make(map[string]e2proxy.Receipt)
			}
			r.notSent[receipt.CommandRef] = receipt
			r.mu.Unlock()
		}
		return *response.Result, e2runner.ErrUnavailable
	}
	return *response.Result, ErrE2Control
}

func (r *e2RemoteRun) takeAdmissionNotSent(receipt e2proxy.Receipt) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	ack, ok := r.notSent[receipt.CommandRef]
	if !ok || ack != receipt {
		return false
	}
	delete(r.notSent, receipt.CommandRef)
	return true
}

func (r *e2RemoteRun) Snapshot() e2proxy.ExecutionView {
	view, ok := r.view(0)
	if !ok {
		return e2proxy.ExecutionView{State: e2RemoteViewUnavailable}
	}
	return view.Snapshot
}

func (r *e2RemoteRun) Policy() e2proxy.PolicyView {
	view, _ := r.view(0)
	return view.Policy
}

func (r *e2RemoteRun) BudgetRemaining() (uint64, error) {
	view, ok := r.view(0)
	if !ok || !view.BudgetKnown {
		return 0, ErrE2Control
	}
	return view.BudgetRemaining, nil
}

func (r *e2RemoteRun) RegisterIntent(ctx context.Context, id int64) error {
	_, err := r.client.bounded(ctx, r.identity(e2RemoteRegister, id))
	return err
}

func (r *e2RemoteRun) Calls() []e2proxy.CallResult {
	view, _ := r.view(0)
	return view.Calls
}

// 러너가 먼저 닫은 실행은 이미 끝났다. 다시 멈추려다 실패하면 프롬프트 봉인까지 가지 못한다.
func (r *e2RemoteRun) Stop(ctx context.Context) error {
	view, ok := r.view(0)
	if !ok {
		return ErrE2Control
	}
	if view.Snapshot.State != "active" {
		return nil
	}
	_, err := r.client.bounded(ctx, r.identity(e2RemoteStopOp, 0))
	return err
}

func (r *e2RemoteRun) DrainIntent(ctx context.Context, id int64) error {
	_, err := r.client.call(ctx, r.identity(e2RemoteDrainOp, id))
	return err
}

// 실행 자원의 마감은 러너의 close 요청으로 E²가 한다. 엔진이 먼저 닫으면 마감 순서가 어긋난다.
func (r *e2RemoteRun) Close(context.Context) error { return nil }
