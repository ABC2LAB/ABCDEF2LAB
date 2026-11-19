package e3contract_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/e3contract/e3fake"
	"github.com/Autumn-27/artex/oracle"
)

// 기록하고 나서 보내기(사용자 결정 2026-11-19): 생성 요청마다 먼저 쓰기를 "ID 모름"으로 남기고(Reserve), 그 뒤에만
// 보낸다. 응답을 받으면 결과로 갱신한다(Settle).
func TestRunReservesWriteBeforeCreate(t *testing.T) {
	f := newFixture(t)
	res := f.run(t)
	if res.Status != e3contract.StatusCompleted {
		t.Fatalf("%+v", res)
	}
	events := f.writes.Events()
	// 확정할 종류(note·secret)마다 Reserve → (생성) → Settle(created) 순서다.
	if len(events) != 4 {
		t.Fatalf("쓰기 기록 호출 %d건: %+v", len(events), events)
	}
	for i, typ := range []string{"note", "secret"} {
		reserve, settle := events[2*i], events[2*i+1]
		if !reserve.Reserve || reserve.Type != typ || reserve.SentBefore != i {
			t.Fatalf("%s 예약이 생성 요청 전이 아님: %+v", typ, reserve)
		}
		if settle.Reserve || settle.Write.ResourceType != typ || settle.Write.Outcome != e3contract.WriteCreated ||
			settle.Write.ObjectIDDigest == "" || settle.SentBefore != i+1 {
			t.Fatalf("%s 갱신: %+v", typ, settle)
		}
	}
	if len(res.Writes) != 2 {
		t.Fatalf("결과 쓰기 %+v", res.Writes)
	}
}

// 기록을 남기지 못하면 생성 요청을 보내지 않고 그 종류는 UNDECIDABLE이다. 다른 종류는 그대로 진행한다.
func TestRunWriteReserveFailureSendsNothing(t *testing.T) {
	f := newFixture(t)
	f.writes.ReserveErr = map[string]error{"note": errors.New("저널 쓰기 실패")}
	res := f.run(t)
	if v := verdictOf(t, res, "note"); v.Value != oracle.Undecidable || !hasReason(v, e3contract.ReasonWriteRecordUnavailable) {
		t.Fatalf("note: %+v", v)
	}
	if n := countCalls(f.fake.Calls(), func(k e3fake.Key) bool { return k.Type == "note" }); n != 0 {
		t.Fatalf("기록 없이 note 요청 %d건", n)
	}
	for _, w := range res.Writes {
		if w.ResourceType == "note" {
			t.Fatalf("보내지 않은 생성이 결과 쓰기에 남음: %+v", res.Writes)
		}
	}
	if v := verdictOf(t, res, "secret"); v.Value != oracle.Confirmed {
		t.Fatalf("다른 종류가 멈춤: %+v", v)
	}
	// 모든 종류가 기록을 남기지 못하면 생성 요청은 0건이다(신원 요청만).
	f = newFixture(t)
	f.writes.ReserveErr = map[string]error{"": errors.New("저널 쓰기 실패")}
	res = f.run(t)
	noConfirmed(t, res)
	if n := countCalls(f.fake.Calls(), func(k e3fake.Key) bool { return k.Purpose == e3contract.PurposeCreate }); n != 0 || len(res.Writes) != 0 {
		t.Fatalf("생성 %d건, 쓰기 %+v", n, res.Writes)
	}
}

// 보낸 뒤 갱신이 실패해도 판정은 이어 간다. 미리 남긴 기록(ID 모름)이 남아 다음 회차를 막는다(제품 쪽 시험이 확인).
func TestRunWriteSettleFailureKeepsReservation(t *testing.T) {
	f := newFixture(t)
	f.writes.SettleErr = map[string]error{"note": errors.New("저널 쓰기 실패")}
	res := f.run(t)
	if v := verdictOf(t, res, "note"); v.Value != oracle.Confirmed {
		t.Fatalf("note: %+v", v)
	}
	reserved, settled := 0, 0
	for _, e := range f.writes.Events() {
		if e.Type != "note" {
			continue
		}
		if e.Reserve {
			reserved++
		} else {
			settled++
		}
	}
	if reserved != 1 || settled != 0 {
		t.Fatalf("note 예약 %d·갱신 %d", reserved, settled)
	}
}

// 결과 불명 생성은 갱신하지 않고(이미 ID 모름), 보내지 않은 것이 확실한 생성은 not_sent로 기록을 거둔다.
func TestRunWriteSettleOutcomes(t *testing.T) {
	for name, c := range map[string]struct {
		be      e3fake.Behavior
		settled e3contract.WriteOutcome
	}{
		"timeout": {e3fake.Behavior{TimedOut: true}, ""},
		"error":   {e3fake.Behavior{Err: errors.New("연결 끊김")}, ""},
		"no_id":   {e3fake.Behavior{NoObjectID: true}, ""},
		"denied":  {e3fake.Behavior{NotSent: e3contract.NotSentPolicyDenied}, e3contract.WriteNotSent},
		"budget":  {e3fake.Behavior{NotSent: e3contract.NotSentBudgetExhausted}, e3contract.WriteNotSent},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.fake.Behaviors[key(e3contract.PurposeCreate, e3contract.RoleOwner, "note")] = c.be
			f.run(t)
			var got []e3fake.WriteEvent
			for _, e := range f.writes.Events() {
				if e.Type == "note" && !e.Reserve {
					got = append(got, e)
				}
			}
			if c.settled == "" {
				if len(got) != 0 {
					t.Fatalf("결과 불명 생성을 갱신함: %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].Write.Outcome != c.settled || got[0].Write.ObjectIDDigest != "" {
				t.Fatalf("갱신: %+v", got)
			}
		})
	}
}

// 미리 남긴 쓰기 기록을 거두는(not_sent) 것은 E²가 대상으로 연결을 시도하기 전에 거부한 경우뿐이다: 정책 거부·승인 거절·
// 승인 시간 초과·확정 몫 소진, 또는 회차가 이미 멈춰 생성 요청을 부르지 않은 경우. 연결 뒤 실패·결과 불명(E² 오류, 보냈지만
// 응답 미완료, 시간 초과, ID 없음)과 사유를 알 수 없는 sent=false(E² C02의 Unknown·TransportFailed를 잘못 옮긴 경우)는 거두지
// 않는다. 기록은 ID 모름으로 남아 리셋 확인 대상이다.
func TestRunWriteWithdrawOnlyBeforeConnect(t *testing.T) {
	for name, c := range map[string]struct {
		be       e3fake.Behavior
		withdraw bool
	}{
		"policy_denied":    {e3fake.Behavior{NotSent: e3contract.NotSentPolicyDenied}, true},
		"approval_denied":  {e3fake.Behavior{NotSent: e3contract.NotSentApprovalDenied}, true},
		"approval_timeout": {e3fake.Behavior{NotSent: e3contract.NotSentApprovalTimeout}, true},
		"budget_exhausted": {e3fake.Behavior{NotSent: e3contract.NotSentBudgetExhausted}, true},
		"transport_error":  {e3fake.Behavior{Err: errors.New("연결 뒤 끊김")}, false},
		"sent_incomplete":  {e3fake.Behavior{Incomplete: true}, false},
		"sent_timed_out":   {e3fake.Behavior{TimedOut: true}, false},
		"no_object_id":     {e3fake.Behavior{NoObjectID: true}, false},
		"not_normal":       {e3fake.Behavior{Status: 500, Normal: oracle.StateNo}, false},
		// sent=false인데 연결 전 거부 사유가 아니면 관찰 계약 위반이다(진위 실패). 보냈을 수 있으니 거두지 않는다.
		"not_sent_transport_failed": {e3fake.Behavior{NotSent: "transport_failed"}, false},
		"not_sent_unknown":          {e3fake.Behavior{NotSent: "unknown"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.fake.Behaviors[key(e3contract.PurposeCreate, e3contract.RoleOwner, "note")] = c.be
			res := f.run(t)
			reserved, withdrawn, settled := 0, 0, 0
			for _, e := range f.writes.Events() {
				if e.Type != "note" {
					continue
				}
				switch {
				case e.Reserve:
					reserved++
				case e.Write.Outcome == e3contract.WriteNotSent:
					withdrawn++
				default:
					settled++
				}
			}
			if reserved != 1 || settled != 0 {
				t.Fatalf("note 예약 %d·갱신 %d", reserved, settled)
			}
			if c.withdraw != (withdrawn == 1) || withdrawn > 1 {
				t.Fatalf("거둠 %d건(기대 %v), 결과 %+v", withdrawn, c.withdraw, res.Writes)
			}
			// 거두지 않은 생성은 결과 쓰기에도 ID 모름으로 남는다(진위 실패로 판정을 버려도 쓰기는 남김, 계약 §7.2).
			var note []e3contract.ConfirmationWrite
			for _, w := range res.Writes {
				if w.ResourceType == "note" {
					note = append(note, w)
				}
			}
			if c.withdraw && len(note) != 0 || !c.withdraw && (len(note) != 1 || note[0].Outcome != e3contract.WriteUnknown) {
				t.Fatalf("결과 쓰기: %+v", res.Writes)
			}
		})
	}
	// 예약 뒤 생성 요청 전에 회차가 멈췄으면 보내지 않은 것이 확실하다. 예약은 거둔다(멈춘 문맥과 관계없이 갱신한다).
	t.Run("stopped_before_call", func(t *testing.T) {
		f := newFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		deps := f.deps()
		deps.Writes = cancelOnReserve{Writes: f.writes, cancel: cancel}
		e3contract.Run(ctx, deps, f.in)
		creates := 0
		for _, call := range f.fake.Calls() {
			if call.Key.Purpose == e3contract.PurposeCreate {
				creates++
			}
		}
		var events []e3fake.WriteEvent
		for _, e := range f.writes.Events() {
			if e.Type == "note" {
				events = append(events, e)
			}
		}
		if creates != 0 || len(events) != 2 || !events[0].Reserve || events[1].Write.Outcome != e3contract.WriteNotSent {
			t.Fatalf("생성 %d, 쓰기 기록 %+v", creates, events)
		}
	})
}

// cancelOnReserve는 예약을 남긴 직후 회차를 멈춘다(예약과 생성 요청 사이의 중지).
type cancelOnReserve struct {
	*e3fake.Writes
	cancel context.CancelFunc
}

func (c cancelOnReserve) Reserve(ctx context.Context, b e3contract.RunBinding, typ string) error {
	err := c.Writes.Reserve(ctx, b, typ)
	c.cancel()
	return err
}
