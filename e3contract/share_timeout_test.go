package e3contract_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/e3contract/e3fake"
	"github.com/Autumn-27/artex/oracle"
)

// 확정 요청은 모두 회차의 확정 요청 전용 시간 상한을 가져간다(사용자 결정 2026-11-18 (나)).
func TestRunPassesConfirmationRequestTimeout(t *testing.T) {
	f := newFixture(t)
	f.in.RequestTimeout = 7 * time.Second
	f.run(t)
	calls := f.fake.Calls()
	if len(calls) == 0 {
		t.Fatal("요청 없음")
	}
	for _, c := range calls {
		if c.Timeout != 7*time.Second {
			t.Fatalf("%+v: 확정 요청 시간 상한이 안 넘어감", c.Key)
		}
	}
}

// 확정 요청이 시간 상한으로 끊기면 결과 불명이고 판정은 UNDECIDABLE(product_request_timeout)이다. 다시 보내지 않는다.
func TestRunConfirmationRequestTimeoutIsUndecidable(t *testing.T) {
	for name, k := range map[string]e3fake.Key{
		"create":     key(e3contract.PurposeCreate, e3contract.RoleOwner, "note"),
		"owner read": key(e3contract.PurposeRead, e3contract.RoleOwner, "note"),
		"cross read": key(e3contract.PurposeRead, e3contract.RoleCross, "note"),
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.fake.Behaviors[k] = e3fake.Behavior{TimedOut: true}
			res := f.run(t)
			if v := verdictOf(t, res, "note"); v.Value != oracle.Undecidable || !hasReason(v, e3contract.ReasonRequestTimeout) {
				t.Fatalf("note: %+v", v)
			}
			if n := countCalls(f.fake.Calls(), func(c e3fake.Key) bool { return c == k }); n != 1 {
				t.Fatalf("끊긴 요청 %d건(다시 보내지 않음)", n)
			}
			// 다른 종류는 그대로 확정된다(시간 초과는 그 요청만의 결과 불명).
			if v := verdictOf(t, res, "secret"); v.Value != oracle.Confirmed {
				t.Fatalf("secret: %+v", v)
			}
		})
	}
}

// 생성 요청이 시간 초과되면 객체가 생겼을 수 있다: 판정은 UNDECIDABLE이고 쓰기 기록(ID 모름)이 남는다. 그 기록을 가진
// 대상은 리셋 확인 전까지 다음 회차 확정 단계를 시작하지 않는다(사용자 결정 2026-11-18).
func TestRunCreateTimeoutLeavesWriteAndBlocksNextRun(t *testing.T) {
	f := newFixture(t)
	f.fake.Behaviors[key(e3contract.PurposeCreate, e3contract.RoleOwner, "note")] = e3fake.Behavior{TimedOut: true}
	res := f.run(t)
	if v := verdictOf(t, res, "note"); v.Value != oracle.Undecidable || !hasReason(v, e3contract.ReasonRequestTimeout) {
		t.Fatalf("note: %+v", v)
	}
	var note, secret *e3contract.ConfirmationWrite
	for i := range res.Writes {
		switch res.Writes[i].ResourceType {
		case "note":
			note = &res.Writes[i]
		case "secret":
			secret = &res.Writes[i]
		}
	}
	if len(res.Writes) != 2 || note == nil || note.Outcome != e3contract.WriteUnknown || note.ObjectIDDigest != "" || note.Ref.CallRef == "" ||
		secret == nil || secret.Outcome != e3contract.WriteCreated || secret.ObjectIDDigest == "" {
		t.Fatalf("쓰기 기록: %+v", res.Writes)
	}
	// 다음 회차: 쓰기 기록이 있고 리셋 확인이 없으면 시작 거부. ID 모르는 객체는 잔존을 읽을 수 없어 리셋 확인에만 기댄다.
	next := newFixture(t)
	next.reset = e3fake.Reset{State: e3contract.ResetState{PreviousWrites: len(res.Writes) > 0, Residue: e3contract.ResidueAbsent}}
	refused := next.run(t)
	if refused.Status != e3contract.StatusStartRefused || refused.Reason != string(e3contract.RefuseResetConfirmationMissing) ||
		refused.Requests != 0 || len(next.fake.Calls()) != 0 {
		t.Fatalf("리셋 확인 전 시작: %+v", refused)
	}
	next.reset.State.ResetConfirmed = true
	if started := next.run(t); started.Status != e3contract.StatusCompleted {
		t.Fatalf("리셋 확인 뒤 시작 안 됨: %+v", started)
	}
}

// 쓰기 기록은 보냈거나 보냈을 수 있는 생성만 남긴다. 보내지 않은 것이 확실한 생성은 남기지 않는다.
func TestRunWritesOnlyForSentCreates(t *testing.T) {
	for name, c := range map[string]struct {
		be      e3fake.Behavior
		outcome e3contract.WriteOutcome // 빈 값이면 쓰기 기록 없음
		ref     bool
	}{
		"created":    {e3fake.Behavior{}, e3contract.WriteCreated, true},
		"incomplete": {e3fake.Behavior{Incomplete: true}, e3contract.WriteUnknown, true},
		"error":      {e3fake.Behavior{Err: errors.New("연결 끊김")}, e3contract.WriteUnknown, false},
		"no_id":      {e3fake.Behavior{NoObjectID: true}, e3contract.WriteUnknown, true},
		"not_normal": {e3fake.Behavior{Status: 500, Normal: oracle.StateNo}, e3contract.WriteUnknown, true},
		"denied":     {e3fake.Behavior{NotSent: e3contract.NotSentPolicyDenied}, "", false},
		"approval":   {e3fake.Behavior{NotSent: e3contract.NotSentApprovalDenied}, "", false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.fake.Behaviors[key(e3contract.PurposeCreate, e3contract.RoleOwner, "note")] = c.be
			res := f.run(t)
			var got *e3contract.ConfirmationWrite
			for i := range res.Writes {
				if res.Writes[i].ResourceType == "note" {
					got = &res.Writes[i]
				}
			}
			if c.outcome == "" {
				if got != nil {
					t.Fatalf("보내지 않은 생성의 쓰기 기록: %+v", got)
				}
				return
			}
			if got == nil || got.Outcome != c.outcome || (got.Ref.CallRef != "") != c.ref || (got.ObjectIDDigest != "") != (c.outcome == e3contract.WriteCreated) {
				t.Fatalf("쓰기 기록: %+v", got)
			}
		})
	}
}

// 진위 실패로 판정을 버려도, 이미 보낸 생성의 쓰기 기록은 남는다(대상에는 객체가 생겼다).
func TestRunIntegrityFailureKeepsWrites(t *testing.T) {
	f := newFixture(t)
	f.fake.Tamper = func(c e3fake.Call, o *e3contract.Observation) {
		if c.Key == key(e3contract.PurposeRead, e3contract.RoleCross, "note") {
			o.StatusCode = 299
		}
	}
	res := f.run(t)
	if res.Status != e3contract.StatusIntegrityFailed || len(res.Verdicts) != 0 {
		t.Fatalf("진위 실패 아님: %+v", res)
	}
	if len(res.Writes) != 1 || res.Writes[0].ResourceType != "note" || res.Writes[0].Outcome != e3contract.WriteCreated {
		t.Fatalf("진위 실패 뒤 쓰기 기록 없음: %+v", res.Writes)
	}
}

// 몫에 들지 않은 종류는 요청을 보내지 않고 UNDECIDABLE(예산)이다. 하나도 안 들면 표식·신원 요청도 보내지 않는다.
func TestRunAdmittedTypes(t *testing.T) {
	f := newFixture(t)
	f.in.AdmittedTypes = 1
	res := f.run(t)
	if res.Status != e3contract.StatusCompleted || len(res.Verdicts) != 2 {
		t.Fatalf("%+v", res)
	}
	if v := verdictOf(t, res, "note"); v.Value != oracle.Confirmed {
		t.Fatalf("몫에 든 종류: %+v", v)
	}
	if v := verdictOf(t, res, "secret"); v.Value != oracle.Undecidable || !hasReason(v, e3contract.ReasonConfirmationBudgetExhaust) || len(v.Evidence) != 0 {
		t.Fatalf("몫 밖 종류: %+v", v)
	}
	if n := countCalls(f.fake.Calls(), func(k e3fake.Key) bool { return k.Type == "secret" }); n != 0 {
		t.Fatalf("몫 밖 종류에 요청 %d건", n)
	}

	none := newFixture(t)
	none.in.AdmittedTypes = 0
	res = none.run(t)
	if res.Status != e3contract.StatusCompleted || res.Requests != 0 || len(none.fake.Calls()) != 0 || len(res.Verdicts) != 2 || len(res.Writes) != 0 {
		t.Fatalf("몫 0인데 요청: %+v calls=%d", res, len(none.fake.Calls()))
	}
	for _, v := range res.Verdicts {
		if v.Value != oracle.Undecidable || !hasReason(v, e3contract.ReasonConfirmationBudgetExhaust) {
			t.Fatalf("몫 0: %+v", v)
		}
	}
	if len(none.fake.Sinks()) != 0 && len(e3contract.LeakingSinks(testMarker(t), none.fake.Sinks())) != 0 {
		t.Fatal("몫 0 회차에 표식 흔적")
	}

	for name, change := range map[string]func(*e3contract.Input){
		"no timeout":       func(in *e3contract.Input) { in.RequestTimeout = 0 },
		"negative timeout": func(in *e3contract.Input) { in.RequestTimeout = -time.Second },
		"negative types":   func(in *e3contract.Input) { in.AdmittedTypes = -1 },
	} {
		g := newFixture(t)
		change(&g.in)
		if res := g.run(t); res.Status != e3contract.StatusDisabled || res.Reason != string(e3contract.DisabledShareUnavailable) || res.Requests != 0 {
			t.Fatalf("%s: %+v", name, res)
		}
	}
}

// 시간 초과 표시는 보낸 뒤 결과 불명일 때만 맞다. 그 밖의 조합은 관찰 진위 실패다.
func TestObservationTimedOutShape(t *testing.T) {
	for name, tamper := range map[string]func(o *e3contract.Observation){
		"complete": func(o *e3contract.Observation) { o.TimedOut = true },
	} {
		f := newFixture(t)
		f.fake.TamperBoth = func(c e3fake.Call, o *e3contract.Observation) {
			if c.Key == key(e3contract.PurposeRead, e3contract.RoleOwner, "note") {
				tamper(o)
			}
		}
		if res := f.run(t); res.Status != e3contract.StatusIntegrityFailed || res.Reason != string(e3contract.IntegrityObservationInvalid) {
			t.Fatalf("%s: %+v", name, res)
		}
	}
	notSent := newFixture(t)
	notSent.fake.Behaviors[key(e3contract.PurposeRead, e3contract.RoleOwner, "note")] = e3fake.Behavior{NotSent: e3contract.NotSentPolicyDenied}
	notSent.fake.TamperBoth = func(c e3fake.Call, o *e3contract.Observation) {
		if c.Key == key(e3contract.PurposeRead, e3contract.RoleOwner, "note") {
			o.TimedOut = true
		}
	}
	if res := notSent.run(t); res.Status != e3contract.StatusIntegrityFailed {
		t.Fatalf("보내지 않았는데 시간 초과: %+v", res)
	}
}

// 회차가 중지돼 부르지도 않은 생성은 쓰기 기록에 없다(보내지 않은 것이 확실하다).
func TestRunInterruptedBeforeCreateLeavesNoWrite(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.fake.TamperBoth = func(c e3fake.Call, _ *e3contract.Observation) {
		if c.Key == key(e3contract.PurposeRead, e3contract.RoleCross, "note") {
			cancel()
		}
	}
	res := e3contract.Run(ctx, f.deps(), f.in)
	if res.Status != e3contract.StatusInterrupted {
		t.Fatalf("%+v", res)
	}
	if n := countCalls(f.fake.Calls(), func(k e3fake.Key) bool { return k.Type == "secret" }); n != 0 {
		t.Fatalf("중지 뒤 secret 요청 %d건", n)
	}
	if len(res.Writes) != 1 || res.Writes[0].ResourceType != "note" {
		t.Fatalf("부르지 않은 생성이 쓰기 기록에 들어감: %+v", res.Writes)
	}
}
