//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/e3contract/e3v3"
	"github.com/Autumn-27/artex/server"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/budget"
)

// E② 실행 연결이 있으면 확정 단계는 늘 어댑터를 붙인다(Go 조건 2: 최종 이미지에서 not_wired가 나오지 않는다). 꺼진 회차와 몫이
// 0인 회차는 사유만 내고, 실제 브리지(표식 등록·Host 게시·E② 저널 사건)와 쓰기 기록을 하나도 만들지 않는다.

// e3TraceRecorder는 E② 실행이 남긴 저널 사건 수를 센다.
type e3TraceRecorder struct{ events atomic.Int64 }

func (r *e3TraceRecorder) Record(context.Context, e2proxy.Event) error {
	r.events.Add(1)
	return nil
}

// e3TraceExecution은 대상에 닿지 않는 실제 E② 실행이다(규칙 주소는 쓰지 않는 루프백). 확정 단계가 이 실행을 건드리면 저널
// 사건·호출 수가 바뀐다.
func e3TraceExecution(t *testing.T) (*e2proxy.Execution, *e3TraceRecorder) {
	t.Helper()
	now := time.Now()
	ledger, err := budget.New(budget.Limits{MaxApprovals: 1, MaxRequestsPerApproval: 4})
	if err != nil {
		t.Fatal(err)
	}
	grant := budget.Grant{Key: budget.Key{TaskRef: liveJob, BudgetRef: liveJob}, Binding: budget.Binding{PolicyRef: liveJob, ResetRef: "owned-reset"},
		MaxRequests: 4, ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Minute)}
	if ledger.Register(grant, now) != nil {
		t.Fatal("예산 준비 실패")
	}
	recorder := &e3TraceRecorder{}
	cfg := e2proxy.Config{ApprovalMode: e2proxy.ApprovalPreauthorizedScope,
		Scope:    transmit.Scope{TaskRef: liveJob, RunRef: liveJob, CaseRef: "owned-case", ResetRef: "owned-reset", PolicyRef: liveJob},
		IntentID: 1, OwnerRef: "ARTEX", PolicyRevision: "v1", AccountRef: "anonymous", SessionRef: "anonymous-session", ExpiresAt: grant.ValidUntil,
		Rules: []e2proxy.Rule{{Ref: "owned-rule", Origin: "http://127.0.0.1:9", ConnectIP: netip.MustParseAddr("127.0.0.1"),
			PathPrefix: "/", PathScopeRef: "owned-read", Methods: []string{"GET"}}},
		Budget: transmit.CumulativeBudget{Ledger: ledger, Key: grant.Key, Binding: grant.Binding}, Recorder: recorder,
		Limits: e2proxy.Limits{MaxAttempts: 4, MaxCalls: 2, MaxConcurrent: 1, MaxBodyBytes: 4096, RequestTimeout: time.Second,
			CallTimeout: time.Minute, MaxDenials: 4, MaxTransient: 4, MaxUnknown: 4}}
	execution, err := e2proxy.NewExecution(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return execution, recorder
}

// e3SpyBridge는 실제 브리지 대신 만들어진 횟수와 받은 호출을 센다.
type e3SpyBridge struct {
	created  *atomic.Int64
	binding  e3contract.RunBinding
	register error
	calls    atomic.Int64
	closed   atomic.Bool
}

func (b *e3SpyBridge) RegisterMarker(context.Context, e3contract.RunBinding, e3contract.Marker) error {
	b.calls.Add(1)
	return b.register
}
func (b *e3SpyBridge) Send(context.Context, e3v3.Request) (e3v3.Event, error) {
	b.calls.Add(1)
	return e3v3.Event{}, e2proxy.ErrE3JournalUnavailable
}
func (b *e3SpyBridge) AccountBindingRef(context.Context, e3contract.RunBinding, string) (string, error) {
	b.calls.Add(1)
	return "", e2proxy.ErrE3JournalUnavailable
}
func (b *e3SpyBridge) Verified(context.Context, e3contract.RunBinding, e3contract.CallRef) (e3v3.Event, error) {
	b.calls.Add(1)
	return e3v3.Event{}, e2proxy.ErrE3JournalUnavailable
}
func (b *e3SpyBridge) Close() { b.closed.Store(true) }

// wire는 확정 단계에 E② 실행 연결(handle)과 브리지 생성기를 붙인다. created는 브리지를 만든 횟수다.
func wireE3(c *productE3Confirmation, exec *e2proxy.Execution, accounts server.E2HTTPAccountBindings, create func(*e2proxy.Execution, server.E2HTTPAccountBindings, e3contract.RunBinding) (productE3Bridge, error)) {
	c.handle = func(string) (*e2proxy.Execution, server.E2HTTPAccountBindings, string) { return exec, accounts, "" }
	c.newBridge = create
}

func countingBridge(created *atomic.Int64) func(*e2proxy.Execution, server.E2HTTPAccountBindings, e3contract.RunBinding) (productE3Bridge, error) {
	return func(_ *e2proxy.Execution, _ server.E2HTTPAccountBindings, binding e3contract.RunBinding) (productE3Bridge, error) {
		created.Add(1)
		return &e3SpyBridge{created: created, binding: binding, register: errors.New("spy refuses marker")}, nil
	}
}

// 선언 없는 회차·몫이 0인 회차·쓰기 동의 없는 회차는 연결이 있어도 사유만 내고, E② 저널·호출과 쓰기 기록이 어댑터를 붙이기
// 전과 같다. 실제 브리지는 만들지 않는다. 연결이 정말 없을 때만 not_wired다.
func TestProductE3WiredDisabledLeavesNoTrace(t *testing.T) {
	zeroShare := func(p *server.E2HTTPFilePolicy) {
		requests, timeoutMS := uint64(0), int64(0)
		p.Limits.E3ConfirmationRequests, p.Limits.E3ConfirmationTimeoutMS = &requests, &timeoutMS
	}
	for name, tc := range map[string]struct {
		declare bool
		policy  func(*server.E2HTTPFilePolicy)
		change  func(*productE3Confirmation)
		wired   bool
		status  e3contract.ConfirmationStatus
		reason  string
	}{
		"wired-without-declaration": {declare: false, wired: true, status: e3contract.StatusDisabled, reason: string(e3contract.DisabledNoDeclaration)},
		"wired-without-write-consent": {declare: true, wired: true, change: func(c *productE3Confirmation) { c.target.WriteConsent = "" },
			status: e3contract.StatusDisabled, reason: string(e3contract.DisabledWriteNotConsented)},
		// 몫에 든 종류가 없으면 표식·신원 요청 없이 종류마다 UNDECIDABLE(예산)이다.
		"wired-zero-share":             {declare: true, policy: zeroShare, wired: true, status: e3contract.StatusCompleted},
		"unwired-without-declaration":  {declare: false, wired: false, status: e3contract.StatusDisabled, reason: string(e3contract.DisabledNotWired)},
		"unwired-with-declaration-too": {declare: true, wired: false, status: e3contract.StatusDisabled, reason: string(e3contract.DisabledNotWired)},
	} {
		t.Run(name, func(t *testing.T) {
			f := newE3HookFixture(t, tc.declare, tc.policy)
			c := f.confirmation(nil, e3HookAccounts(), nil)
			if tc.change != nil {
				tc.change(c)
			}
			exec, recorder := e3TraceExecution(t)
			var created atomic.Int64
			if tc.wired {
				wireE3(c, exec, nil, countingBridge(&created))
			}
			eventsBefore, callsBefore := recorder.events.Load(), len(exec.Calls())
			begins := 0
			if err := c.run(t.Context(), "9", func() { begins++ }); err != nil {
				t.Fatal(err)
			}
			res, raw := readE3Result(t, f.runs)
			if res.Status != tc.status || res.Reason != tc.reason || res.Requests != 0 || len(res.Writes) != 0 {
				t.Fatalf("결과: %s", raw)
			}
			if created.Load() != 0 || begins != 0 {
				t.Fatalf("꺼진·몫 0 회차가 브리지를 만들거나(%d) 시작 신호를 보냄(%d)", created.Load(), begins)
			}
			if recorder.events.Load() != eventsBefore || len(exec.Calls()) != callsBefore {
				t.Fatalf("E② 저널 사건·호출이 바뀜: 사건 %d→%d 호출 %d→%d", eventsBefore, recorder.events.Load(), callsBefore, len(exec.Calls()))
			}
			if st, err := f.r.resetConfirmation("ARTEX", f.target.ID); err != nil || st.ResetRequired || st.LastWritesJob != "" {
				t.Fatalf("쓰기 기록이 남음: %+v %v", st, err)
			}
		})
	}
}

// 중지 기록(RunStopped)도 같은 연결을 쓴다. 연결이 있고 선언이 없으면 declaration_missing, 돌 수 있었으면 interrupted이고,
// 브리지·E② 사건은 0이다.
func TestProductE3WiredStoppedRecordsActualReason(t *testing.T) {
	for name, tc := range map[string]struct {
		declare bool
		status  e3contract.ConfirmationStatus
		reason  string
	}{
		"without-declaration": {false, e3contract.StatusDisabled, string(e3contract.DisabledNoDeclaration)},
		"would-run":           {true, e3contract.StatusInterrupted, ""},
	} {
		t.Run(name, func(t *testing.T) {
			f := newE3HookFixture(t, tc.declare, nil)
			c := f.confirmation(nil, e3HookAccounts(), nil)
			exec, recorder := e3TraceExecution(t)
			var created atomic.Int64
			wireE3(c, exec, nil, countingBridge(&created))
			if err := c.stopped(t.Context(), "9"); err != nil {
				t.Fatal(err)
			}
			res, raw := readE3Result(t, f.runs)
			if res.Status != tc.status || res.Reason != tc.reason || res.Requests != 0 || created.Load() != 0 || recorder.events.Load() != 0 {
				t.Fatalf("중지 기록: %s 브리지 %d 사건 %d", raw, created.Load(), recorder.events.Load())
			}
		})
	}
}

// 실제 제품에서 중지 기록 훅은 실행이 끝난 뒤 불려 연결 사유가 "실행 끝남"(lease_missing 등)이다. 연결이 있던 판이면
// not_wired가 아니라 실제 상태를 남긴다. 실행기·계정 저장소가 없는 사유와 연결 자체가 없는 판은 그대로 not_wired다.
func TestProductE3StoppedAfterExecutionEnded(t *testing.T) {
	ended := func(reason string) func(string) (*e2proxy.Execution, server.E2HTTPAccountBindings, string) {
		return func(string) (*e2proxy.Execution, server.E2HTTPAccountBindings, string) { return nil, nil, reason }
	}
	for name, tc := range map[string]struct {
		handle  func(string) (*e2proxy.Execution, server.E2HTTPAccountBindings, string)
		bridge  bool
		declare bool
		status  e3contract.ConfirmationStatus
		reason  string
	}{
		"lease-missing-would-run":      {ended(server.E3HandleLeaseMissing), true, true, e3contract.StatusStartRefused, string(e3contract.RefuseE2ExecutionUnavailable)},
		"task-fenced-would-run":        {ended(server.E3HandleTaskFenced), true, true, e3contract.StatusInterrupted, ""},
		"execution-stopped-would-run":  {ended(server.E3HandleExecutionStopped), true, true, e3contract.StatusInterrupted, ""},
		"task-missing-would-run":       {ended(server.E3HandleTaskMissing), true, true, e3contract.StatusInterrupted, ""},
		"task-mismatch-would-run":      {ended(server.E3HandleTaskMismatch), true, true, e3contract.StatusInterrupted, ""},
		"lease-missing-no-declaration": {ended(server.E3HandleLeaseMissing), true, false, e3contract.StatusDisabled, string(e3contract.DisabledNoDeclaration)},
		"runtime-closing-would-run":    {ended(server.E3HandleRuntimeClosing), true, true, e3contract.StatusInterrupted, ""},
		"runtime-closed-would-run":     {ended(server.E3HandleRuntimeClosed), true, true, e3contract.StatusInterrupted, ""},
		"runner-missing":               {ended(server.E3HandleRunnerMissing), true, true, e3contract.StatusDisabled, string(e3contract.DisabledNotWired)},
		"lease-invalid":                {ended(server.E3HandleLeaseInvalid), true, true, e3contract.StatusDisabled, string(e3contract.DisabledNotWired)},
		"runner-not-local":             {ended(server.E3HandleRunnerNotLocal), true, true, e3contract.StatusDisabled, string(e3contract.DisabledNotWired)},
		"accounts-missing":             {ended(server.E3HandleAccountsMissing), true, true, e3contract.StatusDisabled, string(e3contract.DisabledNotWired)},
		"runtime-unavailable":          {ended(server.E3HandleRuntimeUnavailable), true, true, e3contract.StatusDisabled, string(e3contract.DisabledNotWired)},
		"handle-not-configured":        {nil, true, true, e3contract.StatusDisabled, string(e3contract.DisabledNotWired)},
		"bridge-not-configured":        {ended(server.E3HandleLeaseMissing), false, true, e3contract.StatusDisabled, string(e3contract.DisabledNotWired)},
	} {
		t.Run(name, func(t *testing.T) {
			f := newE3HookFixture(t, tc.declare, nil)
			c := f.confirmation(nil, e3HookAccounts(), nil)
			var created atomic.Int64
			c.handle, c.newBridge = tc.handle, nil
			if tc.bridge {
				c.newBridge = countingBridge(&created)
			}
			if err := c.stopped(t.Context(), "9"); err != nil {
				t.Fatal(err)
			}
			res, raw := readE3Result(t, f.runs)
			if res.Status != tc.status || res.Reason != tc.reason || res.Requests != 0 || created.Load() != 0 {
				t.Fatalf("중지 기록: %s 브리지 %d", raw, created.Load())
			}
		})
	}
}

// 닫힌 연결은 부르면 늘 실패한다(중지 기록은 부르지 않는다).
func TestProductEndedE3ConnectionAlwaysFails(t *testing.T) {
	var c productEndedE3Connection
	ctx := t.Context()
	if c.RegisterMarker(ctx, e3contract.RunBinding{}, e3contract.Marker{}) == nil {
		t.Fatal("표식 등록")
	}
	if _, err := c.Identity(ctx, e3contract.IdentityRequest{}); err == nil {
		t.Fatal("신원")
	}
	if _, err := c.Create(ctx, e3contract.CreateRequest{}); err == nil {
		t.Fatal("생성")
	}
	if _, err := c.Read(ctx, e3contract.ReadRequest{}); err == nil {
		t.Fatal("읽기")
	}
	if _, err := c.Sealed(ctx, e3contract.RunBinding{}, e3contract.CallRef{}); err == nil {
		t.Fatal("봉인 사건")
	}
}

// e3ReloginAccounts는 계정 결속 조회에 확정 전 다시 로그인을 더한 가짜다. 결과 코드를 고정해 돌려주고 부른 라벨을 센다.
type e3ReloginAccounts struct {
	result string
	labels []string
}

func (a *e3ReloginAccounts) AccountBindingRef(string, string) (string, bool) { return "", false }
func (a *e3ReloginAccounts) BoundSession(string, string, string) (http.Header, error) {
	return nil, server.ErrE2HTTPAccountBinding
}
func (a *e3ReloginAccounts) Relogin(_ context.Context, label string) string {
	a.labels = append(a.labels, label)
	return a.result
}

type e3PlainAccounts struct{}

func (e3PlainAccounts) AccountBindingRef(string, string) (string, bool) { return "", false }
func (e3PlainAccounts) BoundSession(string, string, string) (http.Header, error) {
	return nil, server.ErrE2HTTPAccountBinding
}

// 사용자 결정 2026-11-20 (가): 연결이 있는 확정 단계는 계정 저장소의 다시 로그인을 붙인다(보낸 결과만 몫으로 셈). 다시
// 로그인 경로가 없는 저장소면 붙이지 않는다.
func TestProductE3WiredRelogin(t *testing.T) {
	for name, tc := range map[string]struct {
		accounts server.E2HTTPAccountBindings
		relogin  bool
		results  map[string]int
	}{
		"relogin-authenticated": {&e3ReloginAccounts{result: server.E2HTTPAuthAuthenticated}, true, map[string]int{"tester-a": 1}},
		"relogin-not-sent":      {&e3ReloginAccounts{result: server.E2HTTPRefreshCooldown}, true, map[string]int{"tester-a": 0}},
		"no-relogin-path":       {e3PlainAccounts{}, false, nil},
	} {
		t.Run(name, func(t *testing.T) {
			f := newE3HookFixture(t, true, nil)
			c := f.confirmation(nil, e3HookAccounts(), nil)
			exec, _ := e3TraceExecution(t)
			var created atomic.Int64
			wireE3(c, exec, tc.accounts, countingBridge(&created))
			deps, release, _ := c.wiredDeps("9")
			defer release()
			if (deps.Relogin != nil) != tc.relogin {
				t.Fatalf("다시 로그인 연결: %v", deps.Relogin != nil)
			}
			for label, want := range tc.results {
				if got := deps.Relogin(t.Context(), label); got != want {
					t.Fatalf("%s: 보냄=%v", label, got)
				}
			}
			if r, ok := tc.accounts.(*e3ReloginAccounts); ok && len(r.labels) != len(tc.results) {
				t.Fatalf("다시 로그인 호출 %v", r.labels)
			}
		})
	}
	// 연결이 없으면 다시 로그인도 없다.
	f := newE3HookFixture(t, true, nil)
	c := f.confirmation(nil, e3HookAccounts(), nil)
	c.handle = nil
	deps, release, _ := c.wiredDeps("9")
	defer release()
	if deps.Relogin != nil {
		t.Fatal("연결 없는 판에 다시 로그인이 붙음")
	}
}

// 실제 브리지 생성기는 실행·계정 저장소 없이는 브리지를 만들지 않는다.
func TestNewProductE3BridgeNeedsExecution(t *testing.T) {
	if _, err := newProductE3Bridge(nil, nil, e3contract.RunBinding{JobRef: "job-1", RunRef: "run-1"}); !errors.Is(err, e2proxy.ErrE3JournalUnavailable) {
		t.Fatal("실행·계정 저장소 없이 브리지를 만듦")
	}
}

// E② 연결을 받지 못하면 not_wired이고, 어느 조건인지 고정 사유 코드를 서버 로그에 남긴다(값 없음).
func TestProductE3UnwiredLogsHandleReason(t *testing.T) {
	var buf bytes.Buffer
	prev, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(flags) })
	for name, handle := range map[string]func(string) (*e2proxy.Execution, server.E2HTTPAccountBindings, string){
		server.E3HandleTaskFenced: func(string) (*e2proxy.Execution, server.E2HTTPAccountBindings, string) {
			return nil, nil, server.E3HandleTaskFenced
		},
		"execution_missing":     func(string) (*e2proxy.Execution, server.E2HTTPAccountBindings, string) { return nil, nil, "" },
		"handle_not_configured": nil,
		server.E3HandleExecutionStopped: func(string) (*e2proxy.Execution, server.E2HTTPAccountBindings, string) {
			return nil, nil, server.E3HandleExecutionStopped
		},
	} {
		t.Run(name, func(t *testing.T) {
			buf.Reset()
			f := newE3HookFixture(t, false, nil)
			c := f.confirmation(nil, e3HookAccounts(), nil)
			c.handle = handle
			if err := c.run(t.Context(), "9", func() {}); err != nil {
				t.Fatal(err)
			}
			res, raw := readE3Result(t, f.runs)
			if res.Status != e3contract.StatusDisabled || res.Reason != string(e3contract.DisabledNotWired) {
				t.Fatalf("결과: %s", raw)
			}
			if !strings.Contains(buf.String(), "task 9 확정 단계에 E② 연결 없음: 사유="+name) {
				t.Fatalf("사유 로그 없음: %q", buf.String())
			}
		})
	}
}

// 연결이 있고 확정 단계가 돌 수 있으면 표식 등록에서 브리지를 만든다. 브리지가 표식을 거부하면 start_refused다(요청 0).
func TestProductE3WiredRunBuildsBridgeAtMarker(t *testing.T) {
	f := newE3HookFixture(t, true, nil)
	c := f.confirmation(nil, e3HookAccounts(), nil)
	exec, _ := e3TraceExecution(t)
	var created atomic.Int64
	wireE3(c, exec, nil, countingBridge(&created))
	begins := 0
	if err := c.run(t.Context(), "9", func() { begins++ }); err != nil {
		t.Fatal(err)
	}
	res, raw := readE3Result(t, f.runs)
	if res.Status != e3contract.StatusStartRefused || res.Reason != string(e3contract.RefuseMarkerRegistrationFailed) || res.Requests != 0 ||
		created.Load() != 1 || begins != 1 {
		t.Fatalf("결과: %s 브리지 %d 시작 %d", raw, created.Load(), begins)
	}
}

// 사용자 결정 2026-11-20 분기점 2: 모델 단계가 E② 실행을 한 번도 잡지 않고 스스로 끝나 확정 단계 훅이 lease_missing을 보면
// not_wired가 아니라 실행 없음으로 기록한다(요청 0, 브리지 0). 꺼진 회차(공식 측정 조건)는 실제 사유(declaration_missing)이고
// Go 조건 2가 이 경로에 걸리지 않게 고정한다. 켜진 회차는 e2_execution_unavailable이다(2026-11-22 F12).
func TestProductE3RunWithoutExecution(t *testing.T) {
	for name, tc := range map[string]struct {
		declare bool
		status  e3contract.ConfirmationStatus
		reason  string
	}{
		"disabled-no-declaration": {false, e3contract.StatusDisabled, string(e3contract.DisabledNoDeclaration)},
		"enabled-no-execution":    {true, e3contract.StatusStartRefused, string(e3contract.RefuseE2ExecutionUnavailable)},
	} {
		t.Run(name, func(t *testing.T) {
			f := newE3HookFixture(t, tc.declare, nil)
			c := f.confirmation(nil, e3HookAccounts(), nil)
			var created atomic.Int64
			c.handle = func(string) (*e2proxy.Execution, server.E2HTTPAccountBindings, string) {
				return nil, nil, server.E3HandleLeaseMissing
			}
			c.newBridge = countingBridge(&created)
			begun := 0
			if err := c.run(t.Context(), "9", func() { begun++ }); err != nil {
				t.Fatal(err)
			}
			res, raw := readE3Result(t, f.runs)
			if res.Status != tc.status || res.Reason != tc.reason || res.Requests != 0 || created.Load() != 0 || begun != 0 {
				t.Fatalf("실행 없음 기록: %s 브리지 %d 시작 %d", raw, created.Load(), begun)
			}
		})
	}
	// 실행 없음이 아닌 닫힘 사유는 확정 단계 훅에서 그대로 not_wired다(확정 단계 전에 E②가 닫힌 결함을 숨기지 않는다).
	f := newE3HookFixture(t, true, nil)
	c := f.confirmation(nil, e3HookAccounts(), nil)
	c.handle = func(string) (*e2proxy.Execution, server.E2HTTPAccountBindings, string) {
		return nil, nil, server.E3HandleTaskFenced
	}
	if err := c.run(t.Context(), "9", func() {}); err != nil {
		t.Fatal(err)
	}
	if res, raw := readE3Result(t, f.runs); res.Status != e3contract.StatusDisabled || res.Reason != string(e3contract.DisabledNotWired) {
		t.Fatalf("확정 단계 전에 닫힌 E②: %s", raw)
	}
}

// 리허설 F11·F12: 같은 설치의 두 번째 점검이 진단 의도를 하나도 실행하지 못하고 실패로 끝나 중지 기록 훅이 lease_missing을
// 보면, 인증 준비를 안 해 계정 인증 결과가 비어 있어도 test_accounts_unavailable이 아니라 실행 없음(e2_execution_unavailable)이다.
// 실행 끝남 사유 중 lease_missing만 실행 없음이다(그 밖의 끝남은 지금처럼 interrupted).
func TestProductE3StoppedWithoutExecutionNotMaskedByAccounts(t *testing.T) {
	for name, tc := range map[string]struct {
		reason string
		status e3contract.ConfirmationStatus
		want   string
	}{
		"lease-missing": {server.E3HandleLeaseMissing, e3contract.StatusStartRefused, string(e3contract.RefuseE2ExecutionUnavailable)},
		"task-fenced":   {server.E3HandleTaskFenced, e3contract.StatusDisabled, string(e3contract.DisabledAccountsUnverified)},
	} {
		t.Run(name, func(t *testing.T) {
			f := newE3HookFixture(t, true, nil)
			c := f.confirmation(nil, map[string]string{}, nil)
			var created atomic.Int64
			reason := tc.reason
			c.handle = func(string) (*e2proxy.Execution, server.E2HTTPAccountBindings, string) { return nil, nil, reason }
			c.newBridge = countingBridge(&created)
			if err := c.stopped(t.Context(), "9"); err != nil {
				t.Fatal(err)
			}
			res, raw := readE3Result(t, f.runs)
			if res.Status != tc.status || res.Reason != tc.want || res.Requests != 0 || created.Load() != 0 {
				t.Fatalf("중지 기록: %s 브리지 %d", raw, created.Load())
			}
		})
	}
}
