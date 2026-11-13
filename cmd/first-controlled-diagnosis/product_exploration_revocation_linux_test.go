//go:build linux

package main

import (
	"context"
	"errors"
	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/exploration"
	"github.com/Autumn-27/artex/exploreboundary"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func revocationHost(t *testing.T) (hostFixture, *productRuntime) {
	t.Helper()
	f := newHostFixture(t)
	registry, _ := targetTestRegistry(t)
	f.rec.Principal = "ARTEX"
	f.rec.ReadonlyBasis = "Owned test fixture: approved GET/HEAD do not change state."
	f.rec.Target.Addresses = []string{"127.0.0.1"}
	f.rec.Target.ApprovedAt = time.Now().Add(-time.Minute)
	f.rec.Target.Requests = 5
	pending := productTargetCopy(f.rec)
	pending.Target.State = "pending"
	pending.Target.Revision = 1
	// 네트워크 경계용 소유 fixture의 승인 스냅샷이다. DNS 소유 증명 경로를 대신하는 제품 기능이 아니다.
	if registry.append(pending) != nil || registry.append(f.rec) != nil {
		t.Fatal("소유 대상 저널 fixture 준비 실패")
	}
	return f, &productRuntime{targets: registry, exploration: f.host, active: map[string]map[string]context.CancelFunc{}}
}

func TestProductRevokeClosesActiveExplorationAndPreservesPrivateObservation(t *testing.T) {
	f, p := revocationHost(t)
	host, err := f.host.factory(f.rec, p.targets.current, f.digest, nil, 4096)(t.Context(), f.binding, &agent.Worker{})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := host.Session.Boundary.Open(t.Context(), f.binding)
	if err != nil {
		t.Fatal(err)
	}
	response, err := host.Fetch(t.Context(), f.target+"/read")
	if err != nil || f.hits.Load() != 1 {
		t.Fatal("정상 승인 요청 양성 대조 실패")
	}
	own, cancelOwn := context.WithCancel(t.Context())
	defer cancelOwn()
	other, cancelOther := context.WithCancel(t.Context())
	defer cancelOther()
	p.active[f.rec.Target.ID] = map[string]context.CancelFunc{"own": cancelOwn}
	p.active["other-target"] = map[string]context.CancelFunc{"other": cancelOther}
	revoked, err := p.revoke("ARTEX", f.rec.Target.ID)
	if err != nil || revoked.State != "revoked" || revoked.Revision != f.rec.Target.Revision+1 || own.Err() == nil || other.Err() != nil {
		t.Fatal("대상별 철회·독립 실행 취소 결속 실패")
	}
	// wrapper의 Status가 대신 마감해 주기 전에 원격 경계 자체가 이미 닫혔는지 본다.
	rawReader := exploreboundary.ControlReader{Client: f.host.control, Binding: f.binding, Flow: f.flow}
	remote, remoteErr := rawReader.Status(t.Context())
	if remoteErr != nil || remote.Active || remote.ClosedReason != exploration.TargetEnded {
		t.Fatal("revoke 반환 전에 경계 close를 보내지 않음")
	}
	state, err := lease.Status(t.Context())
	if err != nil || state.Active || state.ClosedReason != exploration.TargetEnded || state.ActiveConnections != 0 || state.TargetRequests != 1 {
		t.Fatal("철회 반환 전에 실제 경계가 마감되지 않음")
	}
	before := f.hits.Load()
	for i := 0; i < 3; i++ {
		result, fetchErr := host.Fetch(t.Context(), f.target+"/read")
		if fetchErr == nil && (result.Seq != 0 || result.Reason != string(exploration.TargetEnded)) {
			t.Fatal("철회 뒤 탐색 마감 사유 누락")
		}
	}
	if f.hits.Load() != before {
		t.Fatal("철회 뒤 대상에 추가 도달")
	}
	observation, err := host.Reader.ReadObservation(t.Context(), f.flow, response.Seq)
	if err != nil || observation.Seq != response.Seq {
		t.Fatal("철회로 유효한 private 관측이 사라짐")
	}
	if _, err = host.Session.Boundary.Open(t.Context(), f.binding); err == nil {
		t.Fatal("철회된 동일 판 재사용")
	}
}

func TestProductExplorationStatusObservesAuthorityEndAndNeverReopens(t *testing.T) {
	f := newHostFixture(t)
	host, err := f.host.factory(f.rec, f.current, f.digest, nil, 4096)(t.Context(), f.binding, &agent.Worker{})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := host.Session.Boundary.Open(t.Context(), f.binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = host.Fetch(t.Context(), f.target+"/read"); err != nil {
		t.Fatal("승인 조회 실패")
	}
	f.ended.Store(true)
	state, err := lease.Status(t.Context())
	if err != nil || state.Active || state.ClosedReason != exploration.TargetEnded {
		t.Fatal("실행 중 권한 종료가 경계에 전달되지 않음")
	}
	f.ended.Store(false)
	if state, err = lease.Status(t.Context()); err != nil || state.Active {
		t.Fatal("재승인이 옛 탐색 세션을 다시 엶")
	}
	result, fetchErr := host.Fetch(t.Context(), f.target+"/read")
	if fetchErr == nil && (result.Seq != 0 || result.Reason != string(exploration.TargetEnded)) || f.hits.Load() != 1 {
		t.Fatal("옛 판에서 대상 재송신")
	}
}

func TestProductRevokeCloseFailureRemainsUnknownAfterCleanupRecovery(t *testing.T) {
	f, p := revocationHost(t)
	host, err := f.host.factory(f.rec, p.targets.current, f.digest, nil, 4096)(t.Context(), f.binding, &agent.Worker{})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := host.Session.Boundary.Open(t.Context(), f.binding)
	if err != nil {
		t.Fatal(err)
	}
	own, cancel := context.WithCancel(t.Context())
	defer cancel()
	p.active[f.rec.Target.ID] = map[string]context.CancelFunc{"own": cancel}
	socket, away := f.policy.ControlSocket, f.policy.ControlSocket+".away"
	if os.Rename(socket, away) != nil {
		t.Fatal("제어 장애 fixture 준비 실패")
	}
	defer os.Rename(away, socket)
	revoked, err := p.revoke("ARTEX", f.rec.Target.ID)
	if err == nil || err.Error() != "target_revocation_close_failed" || revoked.State != "revoked" || own.Err() == nil {
		t.Fatal("마감 실패를 성공으로 바꾸거나 취소를 누락")
	}
	if os.Rename(away, socket) != nil {
		t.Fatal("제어 채널 복구 실패")
	}
	state, err := lease.Close(t.Context(), exploration.Completed)
	if err == nil || state.Active || state.ActiveConnections != 0 || state.ClosedReason != exploration.TargetEnded {
		t.Fatal("정리 복구가 옛 불명 진단 예산 증명을 다시 엶")
	}
	if _, err = lease.Status(t.Context()); err == nil {
		t.Fatal("불명 마감이 유효 상태로 승격됨")
	}
}

func TestProductExplorationRevocationAndSendRace(t *testing.T) {
	f, p := revocationHost(t)
	host, err := f.host.factory(f.rec, p.targets.current, f.digest, nil, 4096)(t.Context(), f.binding, &agent.Worker{})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := host.Session.Boundary.Open(t.Context(), f.binding)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	var attempts atomic.Int32
	start := make(chan struct{})
	for i := 0; i < 4; i++ {
		group.Add(1)
		go func() { defer group.Done(); <-start; attempts.Add(1); _, _ = host.Fetch(t.Context(), f.target+"/read") }()
	}
	close(start)
	if _, err = p.revoke("ARTEX", f.rec.Target.ID); err != nil {
		t.Fatal("경합 철회 마감 실패")
	}
	after := f.hits.Load()
	group.Wait()
	if attempts.Load() != 4 || f.hits.Load() != after {
		t.Fatal("철회 마감 뒤 늦은 대상 송신")
	}
	state, err := lease.Status(t.Context())
	if err != nil || state.Active || state.ActiveConnections != 0 || state.TargetRequests > f.binding.Limits.MaxRequests {
		t.Fatal("경합 마감·요청 몫 불일치")
	}
}

func TestProductExplorationRejectsLateRevocationDuringOpen(t *testing.T) {
	f := newHostFixture(t)
	var reads atomic.Int32
	current := func(rec productTargetRecord) error {
		if reads.Add(1) >= 3 {
			return errors.New("registry changed")
		}
		return nil
	}
	host, err := f.host.factory(f.rec, current, f.digest, nil, 4096)(t.Context(), f.binding, &agent.Worker{})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := host.Session.Boundary.Open(t.Context(), f.binding)
	if err == nil || lease == nil || f.hits.Load() != 0 {
		t.Fatal("열기 중 승인 변경을 놓침")
	}
	state, closeErr := lease.Close(t.Context(), exploration.Completed)
	if closeErr != nil || state.Active || state.ClosedReason != exploration.BoundaryUnavailable {
		t.Fatal("열기 중 변경이 소유 경계에 남음")
	}
}
