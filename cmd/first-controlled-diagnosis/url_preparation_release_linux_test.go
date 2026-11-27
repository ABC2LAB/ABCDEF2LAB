//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/exploration"
	"github.com/Autumn-27/artex/exploreboundary"
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
	host, err := f.host.factory(f.rec, f.current, f.digest, nil, 4096, &productTargetConnection{IP: f.rec.SelectedIP, SPKI: f.rec.Target.TLSSPKISHA256, jobID: f.binding.JobID}, productExplorationLogin(nil))(t.Context(), f.binding, &agent.Worker{})
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

// 준비 컨테이너가 재시작하면 같이 쓰던 netns에 lo만 남는다(실측). 그때 준비 서비스가 다시 응답할 때까지 기다렸다가 종료
// 신호를 낸다(먼저 종료하면 Docker가 시작에 실패하고 다시 띄우지 않는다). 시간을 재지 않는다.
func TestSharedNetworkWatchWaitsForPreparationBeforeExit(t *testing.T) {
	l := &interfaceList{ifaces: []net.Interface{loopback, eth0}}
	var mu sync.Mutex
	back := false
	ready := func(context.Context) bool { mu.Lock(); defer mu.Unlock(); return back }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lost := watchSharedNetwork(ctx, 5*time.Millisecond, l.list, ready)
	if closedWithin(lost, 50*time.Millisecond) {
		t.Fatal("인터페이스가 있는데 종료 신호")
	}
	l.set(nil, errors.New("일시 오류"))
	if closedWithin(lost, 50*time.Millisecond) {
		t.Fatal("인터페이스를 못 읽은 회차로 종료")
	}
	l.set([]net.Interface{loopback}, nil)
	if closedWithin(lost, 100*time.Millisecond) {
		t.Fatal("준비 서비스가 돌아오기 전에 종료(재시작이 실패해 멈춤)")
	}
	mu.Lock()
	back = true
	mu.Unlock()
	if !closedWithin(lost, time.Second) {
		t.Fatal("준비 서비스가 돌아왔는데 종료하지 않음")
	}
}

func TestSharedNetworkWatchStaysOffWithoutNetwork(t *testing.T) {
	l := &interfaceList{ifaces: []net.Interface{loopback}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if closedWithin(watchSharedNetwork(ctx, 5*time.Millisecond, l.list, func(context.Context) bool { return true }), 50*time.Millisecond) {
		t.Fatal("처음부터 망이 없는 배치에서 종료 신호")
	}
}

// workbench는 옛 netns의 loopback으로 api health를 본다: ready면 준비 서비스가 돌아온 것, api가 사라져 연결이 거부되면
// api가 이미 (준비 서비스가 돌아온 뒤) 종료한 것이다. 503·다른 오류는 아직이다.
func TestAPIRejoinReady(t *testing.T) {
	state := http.StatusServiceUnavailable
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		code := state
		mu.Unlock()
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"ready":` + map[bool]string{true: "true", false: "false"}[code == http.StatusOK] + `}`))
	}))
	client := &http.Client{Timeout: time.Second}
	check := apiRejoinReady(client, srv.URL)
	if check(t.Context()) {
		t.Fatal("준비 서비스가 안 돌아왔는데(503) 준비됨")
	}
	mu.Lock()
	state = http.StatusOK
	mu.Unlock()
	if !check(t.Context()) {
		t.Fatal("api가 ready인데 준비 안 됨")
	}
	srv.Close() // api가 종료해 연결이 거부됨
	if !check(t.Context()) {
		t.Fatal("api가 이미 종료(연결 거부)했는데 기다림")
	}
	if apiRejoinReady(client, "http://192.0.2.1:9")(func() context.Context {
		c, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		t.Cleanup(cancel)
		return c
	}()) {
		t.Fatal("응답 없음(시간 초과)을 준비됨으로 봄")
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

// stubBoundary는 제어 소켓에 줄 단위로 답한다. 닫기는 성공이라고 답해도 그 뒤 current가 여전히 열린 세션을 보이면
// 해제는 닫힘을 확인하지 못한 것이다.
func stubBoundary(t *testing.T, reply func(exploreboundary.ControlRequest) exploreboundary.ControlResponse) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "jr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "c.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			var in exploreboundary.ControlRequest
			if json.NewDecoder(conn).Decode(&in) == nil {
				_ = json.NewEncoder(conn).Encode(reply(in))
			}
			conn.Close()
		}
	}()
	return path
}

func TestReleaseRequiresSessionGoneAfterClose(t *testing.T) {
	f := newHostFixture(t)
	open := exploration.State{Binding: f.binding, Active: true}
	path := stubBoundary(t, func(in exploreboundary.ControlRequest) exploreboundary.ControlResponse {
		if in.Op == exploreboundary.OpClose {
			closed := exploration.State{Binding: f.binding, ClosedReason: exploration.Cancelled}
			return exploreboundary.ControlResponse{OK: true, State: &closed}
		}
		return exploreboundary.ControlResponse{OK: true, State: &open} // 닫았다고 답한 뒤에도 열려 있음
	})
	h := &productExplorationHost{}
	h.control.Path, h.control.Timeout = path, time.Second
	if _, err := h.closeForRelease(t.Context(), f.binding.JobID); !errors.Is(err, errExplorationOpen) {
		t.Fatalf("닫기 응답만 믿고 해제를 진행함: %v", err)
	}
}
