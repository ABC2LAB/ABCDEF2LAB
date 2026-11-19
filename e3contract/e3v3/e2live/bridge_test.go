package e2live

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/e3contract/e3v3"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
)

func TestLiveEventConvertsV3ScopeAndObservation(t *testing.T) {
	complete := true
	policy := e3contract.PolicySource{Kind: e3contract.PolicyProductDeclaration, Ref: "declaration-1",
		DigestSHA256: strings.Repeat("a", 64), TargetApprovalRevision: "approval-1"}
	ev := e2proxy.Event{Index: 5, Stage: "result", Receipt: e2proxy.Receipt{
		Version: e2proxy.JournalVersionV3, Program: e2proxy.ProgramE3Identity,
		Scope:   transmit.Scope{RunRef: "run-1", TaskRef: "task-1", PolicyRef: "policy-1"},
		CallRef: "call-1", AttemptID: strings.Repeat("b", 32), Sequence: 1,
		PolicyRevision: "revision-1", RuleRef: "identity-rule", Method: "GET",
		Decision: "allowed", Reason: e2proxy.Complete, ForwardAttempted: true,
		RequestWritten: true, ResponseReceived: true, ResponseComplete: true,
		AuditComplete: true, StatusCode: 200, At: time.Now().UTC(),
		E3Audit: &e2proxy.E3Audit{Role: e3contract.RoleOwner, AccountLabel: "owner-a",
			AccountBindingRef: "acct-1", E3: e2proxy.E3Binding{JobRef: "job-1",
				GenerationRef: "generation-1", InstanceRef: "instance-1", Policy: policy},
			Normal: oracle.StateYes, RequestMarkerFree: &complete, BodyComplete: &complete,
			BodyDigestSHA256: strings.Repeat("c", 64), SubjectDigest: strings.Repeat("d", 64)},
	}}
	converted, err := liveEvent(ev)
	if err != nil || converted.Scope.RunRef != "run-1" || converted.E3.Policy != policy ||
		converted.SubjectDigest != ev.E3Audit.SubjectDigest || !converted.RequestMarkerFree {
		t.Fatalf("v3 사건 변환 실패: %v", err)
	}
	if _, err := converted.Observation(); err != nil {
		t.Fatalf("변환한 사건이 E③ 계약을 통과하지 못함: %v", err)
	}
	ev.E3Audit = nil
	if _, err := liveEvent(ev); !errors.Is(err, e3contract.ErrJournalIntegrity) {
		t.Fatalf("E③ 감사 필드 없는 사건 수락: %v", err)
	}
}

func TestNewLiveWithoutHostIsClosed(t *testing.T) {
	if _, err := NewLive(nil, nil, e3contract.RunBinding{}); !errors.Is(err, e2proxy.ErrE3JournalUnavailable) {
		t.Fatalf("Host 없는 연결을 허용: %v", err)
	}
	var _ e3v3.E2 = (*Live)(nil)
}

func TestLiveCanceledContextRemainsInterruption(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	live := &Live{}
	if _, err := live.Send(ctx, e3v3.Request{}); !errors.Is(err, context.Canceled) {
		t.Fatal("송신 전 중지를 결과 불명으로 바꿈", err)
	}
	if _, err := live.AccountBindingRef(ctx, e3contract.RunBinding{}, "owner-a"); !errors.Is(err, context.Canceled) {
		t.Fatal("결속 조회 중지를 감사 장애로 바꿈", err)
	}
	if _, err := live.Verified(ctx, e3contract.RunBinding{}, e3contract.CallRef{}); !errors.Is(err, context.Canceled) {
		t.Fatal("현재 head 조회 중지를 감사 장애로 바꿈", err)
	}
}
