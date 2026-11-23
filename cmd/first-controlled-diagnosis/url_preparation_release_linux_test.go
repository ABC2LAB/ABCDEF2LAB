//go:build linux

package main

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/diagnosisprep"
)

// 운영자 해제 전에 그 작업의 탐색 경계 세션을 실제 경계에서 닫고, 닫힌 뒤에는 대상에 닿지 않는다. 다른 작업의 세션은
// 닫지 않고 거절한다. 재시작한 준비 서비스는 세션 참조 없이 열린 세션이 없음을 확인한다.
func TestReleaseClosesOnlyThatJobsExplorationSession(t *testing.T) {
	f := newHostFixture(t)
	if closed, err := f.host.closeForRelease(t.Context(), f.binding.JobID); err != nil || closed {
		t.Fatalf("세션이 없을 때: %v %v", closed, err)
	}
	if err := f.host.clearForRelease(t.Context()); err != nil {
		t.Fatalf("세션이 없는데 확인 실패: %v", err)
	}
	host, err := f.host.factory(f.rec, f.current, f.digest, nil, 4096, &productTargetConnection{IP: f.rec.SelectedIP, SPKI: f.rec.Target.TLSSPKISHA256, jobID: f.binding.JobID})(t.Context(), f.binding, &agent.Worker{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.Session.Boundary.Open(t.Context(), f.binding); err != nil {
		t.Fatal(err)
	}
	if err := f.host.clearForRelease(t.Context()); !errors.Is(err, errExplorationOpen) {
		t.Fatalf("열린 세션을 놓침: %v", err)
	}
	if _, err := f.host.closeForRelease(t.Context(), "other-job"); !errors.Is(err, diagnosisprep.ErrExplorationBusy) {
		t.Fatalf("다른 작업의 세션을 닫으려 함: %v", err)
	}
	if err := f.host.clearForRelease(t.Context()); err == nil {
		t.Fatal("다른 작업 거절이 세션을 닫음")
	}
	closed, err := f.host.closeForRelease(t.Context(), f.binding.JobID)
	if err != nil || !closed {
		t.Fatalf("그 작업의 세션을 닫지 못함: %v %v", closed, err)
	}
	if err := f.host.clearForRelease(t.Context()); err != nil {
		t.Fatalf("닫은 뒤 확인 실패: %v", err)
	}
	before := f.hits.Load()
	if _, err := host.Fetch(t.Context(), f.target+"/read"); err == nil && f.hits.Load() != before {
		t.Fatal("닫힌 세션으로 대상에 닿음")
	}
	if f.hits.Load() != before {
		t.Fatal("닫힌 세션으로 대상에 닿음")
	}
}

func TestReleaseExplorationUnreachableIsNotClear(t *testing.T) {
	h := &productExplorationHost{}
	h.control.Path = t.TempDir() + "/missing.sock"
	h.control.Timeout = time.Second
	if err := h.clearForRelease(t.Context()); err == nil {
		t.Fatal("경계에 닿지 못했는데 세션 없음으로 봄")
	}
	if _, err := h.closeForRelease(t.Context(), "job-1"); err == nil {
		t.Fatal("경계에 닿지 못했는데 닫힘으로 봄")
	}
}

func TestReleaseQuietSetting(t *testing.T) {
	if d, err := releaseQuiet(""); err != nil || d != diagnosisprep.DefaultReleaseQuiet {
		t.Fatalf("기본값: %v %v", d, err)
	}
	if d, err := releaseQuiet("900"); err != nil || d != 900*time.Second {
		t.Fatalf("하한: %v %v", d, err)
	}
	for _, raw := range []string{"899", "600", "-1", "abc", "1200.5", "86401"} {
		if _, err := releaseQuiet(raw); err == nil {
			t.Fatalf("잘못된 기준을 받음: %s", raw)
		}
	}
}

type interfaceList struct {
	mu     sync.Mutex
	ifaces []net.Interface
	err    error
}

func (l *interfaceList) list() ([]net.Interface, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]net.Interface(nil), l.ifaces...), l.err
}
func (l *interfaceList) set(ifaces []net.Interface, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ifaces, l.err = ifaces, err
}

var (
	loopback = net.Interface{Name: "lo", Flags: net.FlagUp | net.FlagLoopback}
	eth0     = net.Interface{Name: "eth0", Flags: net.FlagUp}
)

func closedWithin(ch <-chan struct{}, d time.Duration) bool {
	select {
	case <-ch:
		return true
	case <-time.After(d):
		return false
	}
}

// 준비 컨테이너가 재시작하면 같이 쓰던 netns에 lo만 남는다(실측). 그때만 종료 신호를 낸다.
func TestSharedNetworkWatchFiresWhenOnlyLoopbackRemains(t *testing.T) {
	l := &interfaceList{ifaces: []net.Interface{loopback, eth0}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lost := watchSharedNetwork(ctx, 5*time.Millisecond, l.list)
	if closedWithin(lost, 50*time.Millisecond) {
		t.Fatal("인터페이스가 있는데 종료 신호")
	}
	l.set(nil, errors.New("일시 오류"))
	if closedWithin(lost, 50*time.Millisecond) {
		t.Fatal("인터페이스를 못 읽은 회차로 종료")
	}
	l.set([]net.Interface{loopback}, nil)
	if !closedWithin(lost, time.Second) {
		t.Fatal("lo만 남았는데 종료하지 않음")
	}
}

func TestSharedNetworkWatchStaysOffWithoutNetwork(t *testing.T) {
	l := &interfaceList{ifaces: []net.Interface{loopback}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if closedWithin(watchSharedNetwork(ctx, 5*time.Millisecond, l.list), 50*time.Millisecond) {
		t.Fatal("처음부터 망이 없는 배치에서 종료 신호")
	}
}

type countingEndpoint struct{ calls int }

func (e *countingEndpoint) Call(_ context.Context, r diagnosisprep.Request) (diagnosisprep.Response, error) {
	e.calls++
	return diagnosisprep.Response{Version: diagnosisprep.Version, RequestID: r.RequestID}, nil
}

// 비공개 IPC에서도 해제 명령은 제품 관리자만 받는다(API가 막아도 한 번 더).
func TestProductEndpointReleaseRequiresOperator(t *testing.T) {
	base := &countingEndpoint{}
	e := productPreparationEndpoint{base: base}
	for _, r := range []diagnosisprep.Request{
		{Version: diagnosisprep.Version, RequestID: "r1", Principal: "viewer", Command: diagnosisprep.ReleaseStatus},
		{Version: diagnosisprep.Version, RequestID: "r2", Principal: "viewer", Command: diagnosisprep.Release, JobID: "prep-a", ExpectedRevision: 2, Release: &diagnosisprep.ReleaseRequest{Reason: "x"}},
	} {
		out, err := e.Call(t.Context(), r)
		if err != nil || out.Error != "forbidden" {
			t.Fatalf("관리자가 아닌 해제 명령을 받음: %#v %v", out, err)
		}
	}
	if base.calls != 0 {
		t.Fatal("관리자가 아닌 해제 명령을 준비 서비스로 넘김")
	}
	if _, err := e.Call(t.Context(), diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "r3", Principal: "ARTEX", Command: diagnosisprep.ReleaseStatus}); err != nil || base.calls != 1 {
		t.Fatal("관리자의 해제 상태 조회를 넘기지 않음")
	}
}
