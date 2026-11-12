package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2runner"
)

func TestE2HTTPResultObserverWithholdsOutputAndPrivateFailure(t *testing.T) {
	result := completedE2WorkerResult()
	result.ToolOutput = "private fixture tool output"
	before := result.Trusted
	calls := 0
	got, err := observeE2HTTPBoundResult(context.Background(), func(_ context.Context, observed e2runner.Result) error {
		calls++
		if observed.ToolOutput != "" || !reflect.DeepEqual(observed.Trusted, before) {
			t.Fatal("observer received raw output or changed trusted binding")
		}
		return errors.New("private observer failure fixture")
	}, result, e2runner.ErrUnavailable)
	if calls != 1 || !errors.Is(err, ErrE2HTTPIncomplete) || !errors.Is(err, e2runner.ErrUnavailable) || strings.Contains(err.Error(), "private observer failure") || !reflect.DeepEqual(got, result) {
		t.Fatal("observer failure was lost, disclosed, or changed the actual runner result")
	}
}

func TestE2HTTPResultObserverAbsenceKeepsExistingResult(t *testing.T) {
	result := completedE2WorkerResult()
	got, err := observeE2HTTPBoundResult(context.Background(), nil, result, e2runner.ErrUnavailable)
	if !reflect.DeepEqual(got, result) || !errors.Is(err, e2runner.ErrUnavailable) || errors.Is(err, ErrE2HTTPIncomplete) {
		t.Fatal("absent observer changed runner semantics")
	}
}

// C09: 관찰자가 이 호출의 신뢰 결과 대조 실패만 알리면 intent 단위 결과 불명이다. 다른 관찰 실패와 섞이면 지금처럼
// 마감 미완료다.
func TestE2HTTPResultObserverUntrustedIsIntentUnknown(t *testing.T) {
	result := completedE2WorkerResult()
	_, err := observeE2HTTPBoundResult(context.Background(), func(context.Context, e2runner.Result) error { return ErrE2HTTPResultUntrusted }, result, nil)
	if !errors.Is(err, ErrE2HTTPResultUntrusted) || errors.Is(err, ErrE2HTTPIncomplete) {
		t.Fatal("untrusted call result was not reported as intent-level", err)
	}
	mixed := errors.Join(errors.New("private host failure fixture"), ErrE2HTTPResultUntrusted)
	_, err = observeE2HTTPBoundResult(context.Background(), func(context.Context, e2runner.Result) error { return mixed }, result, nil)
	if !errors.Is(err, ErrE2HTTPIncomplete) || errors.Is(err, ErrE2HTTPResultUntrusted) {
		t.Fatal("mixed observer failure was narrowed to an intent outcome", err)
	}
}

// C09: 대조 실패를 받은 Bash는 그 intent를 결과 불명으로 멈추고, 다음 명령은 E②로 보내지 않는다.
func TestE2HTTPBashUntrustedResultHaltsIntentAsUnknown(t *testing.T) {
	execution, _, _ := e2WorkerFixture(t)
	state := &e2HTTPToolState{}
	calls := 0
	tool := e2WorkerBashTool(t, execution, func(context.Context, string) (e2runner.Result, error) {
		calls++
		result := completedE2WorkerResult()
		result.Trusted.Execution = execution.SnapshotForIntent(22)
		return result, ErrE2HTTPResultUntrusted
	}, time.Second, state)
	first, err := tool.Call(context.Background(), json.RawMessage(`{"command":"printf first"}`), nil)
	state.mu.Lock()
	halted, unknown := state.halted, state.unknown
	state.mu.Unlock()
	if err != nil || !first.IsError || !halted || !unknown || !strings.Contains(first.Flatten(), `"unknown"`) {
		t.Fatalf("untrusted result did not halt the intent as unknown: err=%v halted=%v unknown=%v %s", err, halted, unknown, first.Flatten())
	}
	if _, err = tool.Call(context.Background(), json.RawMessage(`{"command":"printf second"}`), nil); err != nil || calls != 1 {
		t.Fatalf("intent with an untrusted result sent again: calls=%d err=%v", calls, err)
	}
}
