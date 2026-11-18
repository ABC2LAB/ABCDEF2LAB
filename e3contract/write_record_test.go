package e3contract_test

import (
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
