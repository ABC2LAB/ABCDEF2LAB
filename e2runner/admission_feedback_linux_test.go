//go:build linux

package e2runner

// PR #598 리뷰용 재현 시험(추적 안 함).

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
)

// 슬롯 대기 중 취소된 호출이 intent 잠금을 돌려주지 않으면 그 intent는 다시 실행되지 않는다.
// 지금 시험 묶음은 이 반환(<-intentGate)을 지워도 통과한다(변이 M3 생존).
func TestRunnerParallelSlotWaitFailureReleasesIntentGate(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
		io.WriteString(w, "ok")
	}))
	defer target.Close()
	defer once.Do(func() { close(release) })
	f := parallelRunnerFixture(t, target.URL, 1, 3, nil, true)
	first := startBash(f.runner, t.Context(), 1, "curl -fsS --max-time 7 '"+target.URL+"/allowed/hold'")
	awaitSignal(t, entered)
	// intent 2는 intent 잠금을 얻고 전체 슬롯을 기다리다 시간 초과로 빠진다.
	wait, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	if out := awaitBash(t, startBash(f.runner, wait, 2, "printf queued")); !errors.Is(out.err, ErrUnavailable) || !AdmissionNotSent(out.result) || !f.execution.HasAdmissionNotSent(*out.result.Trusted.AdmissionNotSent) {
		t.Fatal("대기 호출이 실행됨", out.err)
	}
	cancel()
	if calls := f.execution.Calls(); len(calls) != 1 || calls[0].IntentID != 1 || f.execution.SnapshotForIntent(2).IntentCounts != (e2proxy.Counts{}) {
		t.Fatal("대기 만료가 호출·전송·결과 불명 건수에 섞임")
	}
	once.Do(func() { close(release) })
	if out := awaitBash(t, first); out.err != nil {
		t.Fatal("첫 호출 실패", out.err)
	}
	ctx, done := context.WithTimeout(t.Context(), 3*time.Second)
	defer done()
	if out := awaitBash(t, startBash(f.runner, ctx, 2, "printf again")); out.err != nil || out.result.ToolOutput != "again" {
		t.Fatal("슬롯 대기 실패 뒤 같은 intent가 다시 실행되지 않음(intent 잠금 누수)", out.err)
	}
}

// 마감 불명 호출이 슬롯을 계속 쥔 동안 전역 Drain 장벽이 생기면, 슬롯을 기다리던 다른 intent는 슬롯이 풀리기 전에
// 바로 빠져야 한다(PR 설명: "전체/intent drain은 기존 대기 세대를 무효화"). 기존 대기 시험은 대기자를 select 밖에서
// 멈춘 뒤 신호를 먼저 주므로 select 안에서 막힌 대기자의 깨움(admissionEpoch case)을 검사하지 않는다.
func TestRunnerParallelBlockedWaiterWokenByDrainWhileSlotRetained(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer target.Close()
	f := parallelRunnerFixture(t, target.URL, 1, 3, nil, false)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := startBash(f.runner, ctx, 1, "sleep 20")
	state := waitForRunningCall(t, f.runner)
	defer func() {
		if state.process != nil {
			_ = state.process.Close()
		}
	}()
	if err := state.listener.SetDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if out := awaitBash(t, done); !errors.Is(out.err, ErrIncomplete) || len(f.runner.callGate) != 1 {
		t.Fatal("마감 불명 슬롯 보유 준비 실패", out.err)
	}
	waitCtx, stop := context.WithTimeout(t.Context(), 6*time.Second)
	defer stop()
	queued := startBash(f.runner, waitCtx, 2, "printf must-not-start")
	time.Sleep(300 * time.Millisecond) // 대기자가 select 안에서 슬롯을 기다리는 중
	short, c := context.WithTimeout(t.Context(), 100*time.Millisecond)
	if f.runner.Drain(short) == nil {
		t.Fatal("마감 불명 drain 성공 처리")
	}
	c()
	select {
	case out := <-queued:
		if !errors.Is(out.err, ErrUnavailable) || !AdmissionNotSent(out.result) {
			t.Fatal("대기자가 잘못 실행됨", out.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("drain 장벽 뒤에도 대기자가 슬롯 반환까지 계속 기다림")
	}
}

// Even without a caller deadline, the command clock includes slot waiting.
func TestRunnerParallelCommandDeadlineIncludesQueue(t *testing.T) {
	f := parallelRunnerFixture(t, "http://127.0.0.1", 1, 3, nil, true)
	f.runner.cfg.CommandTimeout = 60 * time.Millisecond
	f.runner.callGate <- struct{}{} // Keep a slot unavailable without starting a process.
	defer func() { <-f.runner.callGate }()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	out := awaitBash(t, startBash(f.runner, ctx, 2, "printf must-not-start"))
	if ctx.Err() != nil || !errors.Is(out.err, ErrUnavailable) || !AdmissionNotSent(out.result) || len(f.execution.Calls()) != 0 {
		t.Fatal("호출자 기한 대신 명령 기한으로 대기 만료를 확정하지 못함")
	}
}
