package main

import (
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
)

func TestRunListReadsRevocationWithoutBindingOrSend(t *testing.T) {
	registry, _, target := accountTestTarget(t, true)
	backend := &urlPreparationRuntime{product: &productRuntime{targets: registry}}
	job := diagnosisprep.Job{ID: "owned-job", Scope: &diagnosisprep.Scope{TargetRef: target.ID, ScopeRef: "scope-" + target.ID,
		PathPrefixes: append([]string(nil), target.PathPrefixes...), Methods: []string{"GET"},
		Budget: diagnosisprep.Budget{Requests: target.Requests}, ExpiresAt: target.ExpiresAt.Add(-time.Minute)}}
	if got := backend.ReadRunApprovals("ARTEX", []diagnosisprep.Job{job})[job.ID]; got != "not_revalidated" {
		t.Fatal("조회가 현재 실행 승인으로 승격되거나 기존 범위를 잃음", got)
	}
	changed := job
	scope := *job.Scope
	changed.Scope = &scope
	changed.Scope.PathPrefixes = []string{"/other"}
	if got := backend.ReadRunApprovals("ARTEX", []diagnosisprep.Job{changed})[job.ID]; got != "changed" {
		t.Fatal("범위 변경 미표시", got)
	}
	if got := backend.ReadRunApprovals("other", []diagnosisprep.Job{job})[job.ID]; got != "unavailable" {
		t.Fatal("다른 주체에 승인 상태 공개", got)
	}
	if _, err := registry.revoke("ARTEX", target.ID); err != nil {
		t.Fatal(err)
	}
	if got := backend.ReadRunApprovals("ARTEX", []diagnosisprep.Job{job})[job.ID]; got != "revoked" {
		t.Fatal("철회 상태 미표시", got)
	}
	// 상태 조회는 승인 기록만 사용한다. DNS·TLS·모델·binding 생성 경로는 호출하지 않는다.
	if len(registry.records) != 1 {
		t.Fatal("목록 조회가 대상을 생성함")
	}
}
