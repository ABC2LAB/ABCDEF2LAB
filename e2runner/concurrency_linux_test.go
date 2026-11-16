//go:build linux

package e2runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
)

type bashOutcome struct {
	result Result
	err    error
}

func startBash(r *Runner, ctx context.Context, intent int64, command string) <-chan bashOutcome {
	done := make(chan bashOutcome, 1)
	go func() { result, err := r.RunForIntent(ctx, intent, command); done <- bashOutcome{result, err} }()
	return done
}
func awaitBash(t *testing.T, done <-chan bashOutcome) bashOutcome {
	t.Helper()
	select {
	case out := <-done:
		return out
	case <-time.After(8 * time.Second):
		t.Fatal("Bash 호출 마감 시간 초과")
		return bashOutcome{}
	}
}
func awaitSignal[T any](t *testing.T, signal <-chan T) T {
	t.Helper()
	select {
	case value := <-signal:
		return value
	case <-time.After(8 * time.Second):
		t.Fatal("실제 수신 동기화 시간 초과")
		var zero T
		return zero
	}
}
func parallelRunnerFixture(t *testing.T, target string, slots, httpLimit int, recorder func(e2proxy.Recorder) e2proxy.Recorder, cleanup bool) *runnerFixture {
	t.Helper()
	base := runnerTestDirectory(t)
	helper := runnerTestHelper(t, base)
	f := newRunnerFixtureWithLimits(t, base, helper, target, nil, recorder, cleanup, func(c *Config) { c.BashSlots = slots; c.PersistWorkspace = true }, func(c *e2proxy.Config) { c.Limits.MaxConcurrent = httpLimit })
	for _, id := range []int64{2, 3, 4} {
		if err := f.execution.RegisterIntent(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// 수신자가 응답을 보류한 동안 서로 다른 실제 namespace Bash가 동시에 대상에 도달한다.
func TestRunnerParallelIntentSlotsAndAudit(t *testing.T) {
	for _, slots := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(slots), func(t *testing.T) {
			entered := make(chan string, 4)
			release := make(chan struct{})
			var once sync.Once
			var active, peak atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := active.Add(1)
				defer active.Add(-1)
				for old := peak.Load(); n > old; old = peak.Load() {
					if peak.CompareAndSwap(old, n) {
						break
					}
				}
				entered <- r.URL.Path
				select {
				case <-release:
					io.WriteString(w, "observed")
				case <-r.Context().Done():
				}
			}))
			defer target.Close()
			defer once.Do(func() { close(release) })
			var eventMu sync.Mutex
			var events []e2proxy.Event
			f := parallelRunnerFixture(t, target.URL, slots, slots, func(next e2proxy.Recorder) e2proxy.Recorder {
				return eventWitness{next: next, mu: &eventMu, events: &events}
			}, true)
			done := make([]<-chan bashOutcome, slots)
			for i := range slots {
				done[i] = startBash(f.runner, t.Context(), int64(i+1), fmt.Sprintf("printf intent-%d > /work/own; curl -fsS --max-time 7 '%s/allowed/%d'", i+1, target.URL, i+1))
			}
			paths := map[string]bool{}
			for range slots {
				paths[awaitSignal(t, entered)] = true
			}
			if len(paths) != slots || peak.Load() != int64(slots) || len(f.runner.callGate) != slots {
				t.Fatal("승인된 Bash 슬롯의 실제 중첩이 확인되지 않음")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
			extra := awaitBash(t, startBash(f.runner, ctx, 4, "printf must-not-start"))
			cancel()
			if !errors.Is(extra.err, ErrUnavailable) || extra.result.Trusted.Call.CallRef != "" || len(f.execution.Calls()) != slots {
				t.Fatal("초과 슬롯 호출이 실행·예산을 획득함")
			}
			once.Do(func() { close(release) })
			refs := map[string]int64{}
			for i, ch := range done {
				out := awaitBash(t, ch)
				c := out.result.Trusted.Call
				if out.err != nil || !out.result.Trusted.Final || !c.AuditComplete || c.Counts.ForwardAttempted != 1 || c.Counts.Responses != 1 || out.result.Trusted.Execution.IntentID != int64(i+1) {
					t.Fatalf("호출별 실제 결과·감사 불일치: %v", out.err)
				}
				refs[c.CallRef] = int64(i + 1)
			}
			eventMu.Lock()
			defer eventMu.Unlock()
			closed := map[string]bool{}
			forwarded := map[string]bool{}
			for _, e := range events {
				if e.CallRef == "" {
					continue
				}
				if refs[e.CallRef] != e.IntentID {
					t.Fatal("다른 intent의 감사 결속 혼입")
				}
				if e.Stage == "call_closed" {
					closed[e.CallRef] = true
				}
				if e.ForwardAttempted {
					forwarded[e.CallRef] = true
				}
			}
			if len(refs) != slots || len(closed) != slots || len(forwarded) != slots {
				t.Fatal("실제 송신·마감 감사가 누락됨")
			}
			remaining, err := f.ledger.Remaining(f.budgetKey, f.budgetBinding, time.Now())
			if err != nil || remaining != uint64(30-slots) {
				t.Fatal("병렬 호출에서 누적 예산 불일치")
			}
		})
	}
}

type eventWitness struct {
	next   e2proxy.Recorder
	mu     *sync.Mutex
	events *[]e2proxy.Event
}

func (w eventWitness) Record(ctx context.Context, event e2proxy.Event) error {
	if err := w.next.Record(ctx, event); err != nil {
		return err
	}
	w.mu.Lock()
	*w.events = append(*w.events, event)
	w.mu.Unlock()
	return nil
}

// 같은 intent 대기는 전체 슬롯을 차지하지 않고, 최종 저장 뒤에만 이전 파일을 읽는다.
func TestRunnerParallelSameIntentSerializesWorkspace(t *testing.T) {
	entered := make(chan string, 4)
	release := make(chan struct{})
	var once sync.Once
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- r.URL.Path
		if r.URL.Path == "/allowed/first" {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		io.WriteString(w, "ok")
	}))
	defer target.Close()
	defer once.Do(func() { close(release) })
	f := parallelRunnerFixture(t, target.URL, 3, 8, nil, true)
	first := startBash(f.runner, t.Context(), 1, "curl -fsS --max-time 7 '"+target.URL+"/allowed/first' >/dev/null; printf alpha > /work/state")
	if awaitSignal(t, entered) != "/allowed/first" {
		t.Fatal("첫 호출 결속 실패")
	}
	queuedCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	resume := make(chan struct{})
	queued := &observedDoneContext{Context: queuedCtx, observed: make(chan struct{}, 1), resume: resume}
	second := startBash(f.runner, queued, 1, "cat /work/state; printf beta >> /work/state; curl -fsS '"+target.URL+"/allowed/second' >/dev/null")
	awaitSignal(t, queued.observed)
	close(resume)
	others := make([]<-chan bashOutcome, 2)
	for i := range 2 {
		others[i] = startBash(f.runner, t.Context(), int64(i+2), "test ! -e /work/state && curl -fsS '"+target.URL+fmt.Sprintf("/allowed/other%d'", i))
	}
	for range 2 {
		if p := awaitSignal(t, entered); p == "/allowed/second" {
			t.Fatal("같은 intent의 호출이 첫 호출과 중첩됨")
		}
	}
	for _, ch := range others {
		if out := awaitBash(t, ch); out.err != nil {
			t.Fatal("다른 intent 작업 공간 격리 실패", out.err)
		}
	}
	if err := f.runner.WriteWorkspaceFile(1, "state", []byte("overwrite")); !errors.Is(err, ErrWorkspaceBusy) {
		t.Fatal("실행 중 파일 변경이 허용됨")
	}
	once.Do(func() { close(release) })
	if out := awaitBash(t, first); out.err != nil || out.result.Workspace != "saved" {
		t.Fatal("첫 호출 저장 실패", out.err)
	}
	if awaitSignal(t, entered) != "/allowed/second" {
		t.Fatal("후속 호출 순서 불일치")
	}
	out := awaitBash(t, second)
	if out.err != nil || out.result.ToolOutput != "alpha" {
		t.Fatal("직렬 후속 호출이 최종 보관본을 보지 못함", out.err)
	}
	data, err := f.runner.WorkspaceFile(1, "state")
	if err != nil || string(data) != "alphabeta" {
		t.Fatal("동일 intent 보관 파일 손실")
	}
}

func TestRunnerParallelQueuedLifecycle(t *testing.T) {
	for _, action := range []string{"cancel", "revoke", "drain", "close"} {
		t.Run(action, func(t *testing.T) {
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			var once sync.Once
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				entered <- struct{}{}
				select {
				case <-release:
				case <-r.Context().Done():
				}
			}))
			defer target.Close()
			defer once.Do(func() { close(release) })
			f := parallelRunnerFixture(t, target.URL, 1, 3, nil, true)
			first := startBash(f.runner, t.Context(), 1, "curl -fsS --max-time 7 '"+target.URL+"/allowed'")
			awaitSignal(t, entered)
			base, cancel := context.WithCancel(t.Context())
			defer cancel()
			resume := make(chan struct{})
			queued := &observedDoneContext{Context: base, observed: make(chan struct{}, 1), resume: resume}
			done := startBash(f.runner, queued, 2, "printf must-not-start")
			awaitSignal(t, queued.observed)
			ctx, finish := context.WithTimeout(t.Context(), 5*time.Second)
			defer finish()
			switch action {
			case "cancel":
				cancel()
			case "revoke":
				if err := f.execution.Revoke(ctx); err != nil {
					t.Fatal(err)
				}
			case "drain":
				if err := f.runner.Drain(ctx); err != nil {
					t.Fatal(err)
				}
			case "close":
				if err := f.runner.Close(ctx); err != nil {
					t.Fatal(err)
				}
			}
			close(resume)
			if out := awaitBash(t, done); !errors.Is(out.err, ErrUnavailable) || out.result.Trusted.Call.CallRef != "" {
				t.Fatal("취소·철회·마감 전 대기 호출이 새로 시작됨", out.err)
			}
			once.Do(func() { close(release) })
			awaitBash(t, first)
			if len(f.execution.Calls()) != 1 {
				t.Fatal("대기 호출이 실행 횟수를 소비함")
			}
			if action == "drain" {
				if out := awaitBash(t, startBash(f.runner, t.Context(), 3, "printf fresh")); out.err != nil {
					t.Fatal("마감 후 새 세대 호출 차단", out.err)
				}
			}
		})
	}
}

func TestRunnerParallelDrainOneIntent(t *testing.T) {
	entered := make(chan string, 2)
	release := make(chan struct{})
	var once sync.Once
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- r.URL.Path
		select {
		case <-release:
			io.WriteString(w, "alive")
		case <-r.Context().Done():
		}
	}))
	defer target.Close()
	defer once.Do(func() { close(release) })
	f := parallelRunnerFixture(t, target.URL, 3, 3, nil, true)
	a := startBash(f.runner, t.Context(), 1, "curl -fsS --max-time 7 '"+target.URL+"/allowed/a'")
	b := startBash(f.runner, t.Context(), 2, "curl -fsS --max-time 7 '"+target.URL+"/allowed/b'")
	awaitSignal(t, entered)
	awaitSignal(t, entered)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := f.runner.DrainIntent(ctx, 1); err != nil {
		t.Fatal(err)
	}
	out := awaitBash(t, a)
	if !out.result.Trusted.Final {
		t.Fatal("대상 intent 마감 실패")
	}
	select {
	case <-b:
		t.Fatal("다른 실행 중 intent를 함께 중지함")
	default:
	}
	if len(f.runner.callGate) != 1 {
		t.Fatal("다른 intent 슬롯을 반환하거나 종료 슬롯을 보유함")
	}
	once.Do(func() { close(release) })
	if out := awaitBash(t, b); out.err != nil || out.result.ToolOutput != "alive" {
		t.Fatal("다른 intent가 정상 완료되지 않음", out.err)
	}
}

// 보관소 잠금으로 마지막 저장을 멈춰, process 종료만으로 슬롯을 조기 반환하지 않는지 검사한다.
func TestRunnerParallelSlotHeldThroughWorkspaceCommit(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { entered <- struct{}{}; <-release }))
	defer target.Close()
	defer once.Do(func() { close(release) })
	f := parallelRunnerFixture(t, target.URL, 2, 3, nil, true)
	done := startBash(f.runner, t.Context(), 1, "printf alpha > /work/state; curl -fsS '"+target.URL+"/allowed'")
	awaitSignal(t, entered)
	state := waitForRunningCall(t, f.runner)
	f.runner.workspaceMu.Lock()
	locked := true
	defer func() {
		if locked {
			f.runner.workspaceMu.Unlock()
		}
	}()
	once.Do(func() { close(release) })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if result, err := state.process.Wait(ctx); err != nil || !result.Drained {
		t.Fatal("실제 namespace 마감 실패", err)
	}
	// cleanupGate를 실제로 잡은 뒤에만 검사한다. 프로세스 종료 시각만으로 저장 완료를 추정하지 않는다.
	deadline := time.After(5 * time.Second)
	for len(state.cleanupGate) == 0 {
		select {
		case <-deadline:
			t.Fatal("최종 cleanup 미진입")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if len(f.runner.callGate) != 1 {
		t.Fatal("보관소 최종 저장 전에 슬롯 반환")
	}
	f.runner.mu.Lock()
	n := len(f.runner.active)
	f.runner.mu.Unlock()
	if n != 1 {
		t.Fatal("보관소 저장 전에 drain 대상에서 제거됨")
	}
	queuedCtx, c := context.WithTimeout(t.Context(), 80*time.Millisecond)
	defer c()
	if out := awaitBash(t, startBash(f.runner, queuedCtx, 1, "printf overwrite")); !errors.Is(out.err, ErrUnavailable) {
		t.Fatal("저장 중 동일 intent 재진입")
	}
	f.runner.workspaceMu.Unlock()
	locked = false
	if out := awaitBash(t, done); out.err != nil || out.result.Workspace != "saved" {
		t.Fatal("최종 저장 실패", out.err)
	}
	if len(f.runner.callGate) != 0 {
		t.Fatal("최종 저장 후 슬롯 누수")
	}
}

func TestRunnerParallelWorkspaceStoreFailureAndInitFailure(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer target.Close()
	f := parallelRunnerFixture(t, target.URL, 3, 3, nil, true)
	// 원자 교체 목적지를 비어 있지 않은 디렉터리로 만들어 이전 보관소를 파괴하지 못하게 한다.
	path := f.runner.workspacePath(1)
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	// 처음 load는 빈 상태여야 하므로, 실행을 시작한 뒤 receiver에서 목적지를 바꾸는 대신
	// 서로 다른 intent의 검증 불가 보관본으로 초기화 실패를 먼저 대조한다.
	out := awaitBash(t, startBash(f.runner, t.Context(), 1, "printf must-not-run"))
	if !errors.Is(out.err, ErrUnavailable) || out.result.Trusted.Classification != "isolation_unavailable" || len(f.runner.callGate) != 0 {
		t.Fatal("초기화 실패 뒤 슬롯 회수 오류")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { entered <- struct{}{}; <-release }))
	defer receiver.Close()
	defer once.Do(func() { close(release) })
	g := parallelRunnerFixture(t, receiver.URL, 3, 3, nil, true)
	done := startBash(g.runner, t.Context(), 1, "printf alpha > /work/state; curl -fsS '"+receiver.URL+"/allowed'")
	awaitSignal(t, entered)
	path = g.runner.workspacePath(1)
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "block"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	out = awaitBash(t, done)
	if out.err != nil || out.result.Workspace != "not_saved" || !out.result.Trusted.Final || len(g.runner.callGate) != 0 {
		t.Fatal("저장 실패와 실행 마감을 혼동함", out.err)
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if out := awaitBash(t, startBash(g.runner, t.Context(), 1, "test ! -e /work/state")); out.err != nil {
		t.Fatal("실패한 보관본이 다음 호출에 혼입됨")
	}
}

func TestRunnerParallelCleanupFailuresRetainSlots(t *testing.T) {
	for _, kind := range []string{"audit", "proxy"} {
		t.Run(kind, func(t *testing.T) {
			target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			defer target.Close()
			var decorate func(e2proxy.Recorder) e2proxy.Recorder
			if kind == "audit" {
				decorate = func(next e2proxy.Recorder) e2proxy.Recorder {
					return runnerStageFailureRecorder{next: next, stage: "call_closed"}
				}
			}
			f := parallelRunnerFixture(t, target.URL, 3, 3, decorate, false)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			command := "printf done"
			if kind == "proxy" {
				command = "sleep 20"
			}
			done := startBash(f.runner, ctx, 1, command)
			state := waitForRunningCall(t, f.runner)
			defer func() {
				if state.process != nil {
					if err := state.process.Close(); err != nil {
						t.Error("소유 시험 프로세스 마감 실패")
					}
				}
			}()
			if kind == "proxy" {
				if err := state.listener.SetDeadline(time.Now().Add(-time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			out := awaitBash(t, done)
			if !errors.Is(out.err, ErrIncomplete) || out.result.Trusted.Final || !out.result.Trusted.ProcessDrained || len(f.runner.callGate) != 1 {
				t.Fatal("미확인 마감으로 슬롯이 조기 반환됨", out.err)
			}
			f.runner.mu.Lock()
			n := len(f.runner.active)
			gate := f.runner.intentGates[1]
			f.runner.mu.Unlock()
			if n != 1 || len(gate) != 1 {
				t.Fatal("미확인 마감의 intent 소유권 상실")
			}
			wait, c := context.WithTimeout(t.Context(), 80*time.Millisecond)
			defer c()
			if out := awaitBash(t, startBash(f.runner, wait, 1, "printf overwrite")); !errors.Is(out.err, ErrUnavailable) {
				t.Fatal("마감 미확인 intent 재진입")
			}
			// 전체 drain이 실패하면 여분 슬롯이 있어도 다른 새 실행을 열지 않는다.
			drain, c := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer c()
			if f.runner.Drain(drain) == nil {
				t.Fatal("미확인 마감 drain을 성공 처리함")
			}
			if out := awaitBash(t, startBash(f.runner, t.Context(), 2, "printf bypass")); !errors.Is(out.err, ErrUnavailable) || len(f.execution.Calls()) != 1 {
				t.Fatal("마감 미확인 상태에서 다른 intent가 우회함")
			}
		})
	}
}

func TestRunnerParallelDetachedChildrenAndTimeout(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprint(timeout), func(t *testing.T) {
			entered := make(chan string, 4)
			releaseParent := make(chan struct{})
			releaseOther := make(chan struct{})
			var parentOnce, otherOnce sync.Once
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				entered <- r.URL.Path
				release := releaseParent
				if r.URL.Path == "/allowed/other" {
					release = releaseOther
				}
				select {
				case <-release:
				case <-r.Context().Done():
				}
			}))
			defer target.Close()
			defer parentOnce.Do(func() { close(releaseParent) })
			defer otherOnce.Do(func() { close(releaseOther) })
			f := parallelRunnerFixture(t, target.URL, 2, 3, nil, true)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if timeout {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(t.Context(), time.Second)
				defer stop()
			}
			command := "setsid sh -c \"curl -fsS --max-time 7 '" + target.URL + "/allowed/child'\" >/tmp/child 2>&1 & curl -fsS --max-time 7 '" + target.URL + "/allowed/parent'"
			a := startBash(f.runner, ctx, 1, command)
			b := startBash(f.runner, t.Context(), 2, "curl -fsS --max-time 7 '"+target.URL+"/allowed/other'")
			paths := map[string]bool{}
			for range 3 {
				paths[awaitSignal(t, entered)] = true
			}
			if !paths["/allowed/child"] || !paths["/allowed/parent"] || !paths["/allowed/other"] {
				t.Fatal("분리 자식·병렬 intent의 실제 수신 미확인")
			}
			if !timeout {
				parentOnce.Do(func() { close(releaseParent) })
			}
			out := awaitBash(t, a)
			if !out.result.Trusted.Final || !out.result.Trusted.ProcessDrained || !out.result.Trusted.ProxyDrained {
				t.Fatal("분리 자식 종료 전에 슬롯 마감")
			}
			if timeout && out.result.Trusted.Termination != TerminationTimeout {
				t.Fatal("명령 시간 초과 분류 상실")
			}
			select {
			case <-b:
				t.Fatal("한 intent 종료가 다른 intent를 중지함")
			default:
			}
			if len(f.runner.callGate) != 1 {
				t.Fatal("분리 자식 정리 뒤 슬롯 수 오류")
			}
			if out := awaitBash(t, startBash(f.runner, t.Context(), 3, "printf next")); out.err != nil {
				t.Fatal("마감된 슬롯 재사용 실패")
			}
			otherOnce.Do(func() { close(releaseOther) })
			if out := awaitBash(t, b); out.err != nil {
				t.Fatal("다른 intent 정상 마감 실패")
			}
		})
	}
}

func TestRunnerBashSlotConfigurationBounds(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer target.Close()
	f := parallelRunnerFixture(t, target.URL, 1, 3, nil, true)
	for _, slots := range []int{-1, 4} {
		cfg := f.cfg
		cfg.BashSlots = slots
		if _, err := New(cfg); !errors.Is(err, ErrConfig) {
			t.Fatal("승인 상한 밖 Bash 슬롯 수용")
		}
	}
	cfg := f.cfg
	cfg.BashSlots = 0
	r, err := New(cfg)
	if err != nil || cap(r.callGate) != 1 {
		t.Fatal("누락한 슬롯 설정이 기본 확대됨")
	}
}

// 각 Bash가 여러 HTTP 요청을 내도 회차 예산은 하나다. 33개 제안 중 실제 대상 수신은 승인 30개뿐이다.
func TestRunnerParallelCallsCannotResetRequestBudget(t *testing.T) {
	entered := make(chan struct{}, 3)
	release := make(chan struct{})
	var once sync.Once
	var seen sync.Map
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if _, loaded := seen.LoadOrStore(r.URL.Path, true); !loaded {
			entered <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		io.WriteString(w, "observed")
	}))
	defer target.Close()
	defer once.Do(func() { close(release) })
	f := parallelRunnerFixture(t, target.URL, 3, 3, nil, true)
	results := make([]<-chan bashOutcome, 3)
	for i := range 3 {
		results[i] = startBash(f.runner, t.Context(), int64(i+1), fmt.Sprintf("for n in {1..11}; do curl -sS --max-time 2 '%s/allowed/%d' >/dev/null 2>&1; done; exit 0", target.URL, i+1))
	}
	for range 3 {
		awaitSignal(t, entered)
	}
	once.Do(func() { close(release) })
	var forwarded, responses uint64
	for _, done := range results {
		out := awaitBash(t, done)
		if !out.result.Trusted.Final || !out.result.Trusted.Call.AuditComplete {
			t.Fatal("예산 소진 뒤 호출 감사 마감 실패")
		}
		forwarded += out.result.Trusted.Call.Counts.ForwardAttempted
		responses += out.result.Trusted.Call.Counts.Responses
	}
	remaining, err := f.execution.BudgetRemaining()
	if err != nil || remaining != 0 || hits.Load() != 30 || forwarded != 30 || responses != 30 {
		t.Fatalf("병렬 호출의 공유 예산 우회: 수신=%d 송신=%d 응답=%d 잔여=%d", hits.Load(), forwarded, responses, remaining)
	}
}
