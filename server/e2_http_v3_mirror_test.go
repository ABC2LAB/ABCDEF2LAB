package server

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/transmit"
)

type v3RecorderFixture struct {
	events []e2proxy.Event
	fail   bool
}

func (r *v3RecorderFixture) Record(_ context.Context, event e2proxy.Event) error {
	if r.fail {
		return errors.New("synthetic Host ACK failure")
	}
	r.events = append(r.events, event)
	return nil
}

func TestV3MirrorRequiresHostACKBeforeLegacyProjection(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{0x53}, 32)
	journal, err := e2proxy.CreateJournal(directory, "events.jsonl", key)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	host := &v3RecorderFixture{fail: true}
	mirror := e2HTTPV3MirrorRecorder{host: host, legacy: journal}
	scope := transmit.Scope{RunRef: "run-mirror", TaskRef: "task-mirror", CaseRef: "case-mirror", ResetRef: "reset-mirror", PolicyRef: "policy-mirror"}
	event := e2proxy.Event{Index: 1, Stage: "result", Receipt: e2proxy.Receipt{
		Version: e2proxy.JournalVersionV3, IntentID: 1, Scope: scope, PolicyRevision: "revision-mirror",
		CallRef: "call-mirror", AttemptID: "0123456789abcdef0123456789abcdef", Sequence: 1,
		Method: "GET", Decision: "denied", Reason: e2proxy.PolicyDenied, NotSent: true,
		AuditComplete: true, At: time.Unix(1_700_000_000, 0).UTC(),
	}}
	if mirror.Record(t.Context(), event) == nil {
		t.Fatal("Host ACK 실패를 성공 처리")
	}
	if journal.Checkpoint().Sequence != 0 {
		t.Fatal("Host ACK 없는 사건이 화면 저널에 표시됨")
	}
	host.fail = false
	if err := mirror.Record(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if journal.Checkpoint().Sequence != 1 || len(host.events) != 1 || host.events[0].Version != e2proxy.JournalVersionV3 {
		t.Fatal("Host와 화면 투영 순서 불일치")
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(filepath.Join(directory, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	events, _, err := e2proxy.VerifyJournalEvents(t.Context(), file, key)
	if err != nil || len(events) != 1 || events[0].Version != e2proxy.Version {
		t.Fatal("기존 화면용 v2 투영 검증 실패", err)
	}
}

// mirrorE3Result는 Host v3 저널에 남는 모양의 확정 단계 결과 행이다(E3Audit·메서드 포함).
func mirrorE3Result(index uint64, scope transmit.Scope, at time.Time) e2proxy.Event {
	return e2proxy.Event{Index: index, Stage: "result", Receipt: e2proxy.Receipt{
		E3Audit: &e2proxy.E3Audit{AccountLabel: "tester-a", ResourceType: "book"}, Program: e2proxy.ProgramE3Identity,
		Version: e2proxy.JournalVersionV3, Scope: scope, PolicyRevision: "revision-mirror", CallRef: "call-e3",
		AttemptID: "fedcba9876543210fedcba9876543210", Sequence: 1, Method: "GET", RuleRef: "e3-identity-rule",
		Decision: "allowed", Reason: e2proxy.Complete, ForwardAttempted: true, RequestWritten: true, ResponseReceived: true,
		ResponseComplete: true, AuditComplete: true, StatusCode: 200, At: at,
	}}
}

// 확정 단계 사건은 Host에는 원본 그대로, 화면 사본에는 E3Audit·메서드를 뺀 투영으로 사본 자체 순번에 남는다. 사본에 없으면
// 작업대 보낸 요청 총계·요청 목록에서 확정 요청이 빠졌다(2026-11-25 화면 회차, 사용자·이동찬 결정).
// 앞뒤 일반 사건(정지 control 포함)도 사본의 다음 순번으로 이어진다(Host 순번을 그대로 쓰면 사본 연속 순번 검사가 거부, 11/20).
func TestV3MirrorProjectsE3EventsIntoScreenCopy(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{0x53}, 32)
	journal, err := e2proxy.CreateScreenJournal(directory, "events.jsonl", key)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	host := &v3RecorderFixture{}
	mirror := e2HTTPV3MirrorRecorder{host: host, legacy: journal}
	scope := transmit.Scope{RunRef: "run-mirror", TaskRef: "task-mirror", CaseRef: "case-mirror", ResetRef: "reset-mirror", PolicyRef: "policy-mirror"}
	at := time.Unix(1_700_000_000, 0).UTC()
	worker := e2proxy.Event{Index: 5, Stage: "result", Receipt: e2proxy.Receipt{
		Version: e2proxy.JournalVersionV3, IntentID: 1, Scope: scope, PolicyRevision: "revision-mirror",
		CallRef: "call-mirror", AttemptID: "0123456789abcdef0123456789abcdef", Sequence: 1,
		Method: "GET", Decision: "denied", Reason: e2proxy.PolicyDenied, NotSent: true,
		AuditComplete: true, At: at,
	}}
	e3 := mirrorE3Result(6, scope, at)
	e3Closed := e2proxy.Event{Index: 7, Stage: "call_closed", Receipt: e2proxy.Receipt{Program: e2proxy.ProgramE3Identity,
		Version: e2proxy.JournalVersionV3, Scope: scope, PolicyRevision: "revision-mirror", CallRef: "call-e3",
		Reason: e2proxy.Complete, AuditComplete: true, At: at}}
	control := e2proxy.Event{Index: 9, Stage: "control", Receipt: e2proxy.Receipt{
		Version: e2proxy.JournalVersionV3, Scope: scope, PolicyRevision: "revision-mirror",
		Reason: e2proxy.Closed, NotSent: true, AuditComplete: true, At: at,
	}}
	for _, event := range []e2proxy.Event{worker, e3, e3Closed, control} {
		if err := mirror.Record(t.Context(), event); err != nil {
			t.Fatalf("index %d 기록 실패: %v", event.Index, err)
		}
	}
	if len(host.events) != 4 || host.events[1].E3Audit == nil || host.events[1].Method != "GET" || journal.Checkpoint().Sequence != 4 {
		t.Fatalf("Host %d건(원본 유지 %v)·사본 순번 %d", len(host.events), host.events[1].E3Audit != nil, journal.Checkpoint().Sequence)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(directory, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	events, _, err := e2proxy.VerifyScreenJournalEvents(t.Context(), bytes.NewReader(raw), key)
	if err != nil || len(events) != 4 {
		t.Fatal("사본 검증 실패", err)
	}
	for i, e := range events {
		if e.Index != uint64(i+1) || e.Version != e2proxy.Version {
			t.Fatalf("사본 순번·판 %d: %+v", i, e)
		}
	}
	got := events[1]
	if got.Program != e2proxy.ProgramE3Identity || got.E3Audit != nil || got.Method != "" || got.CallRef != "call-e3" ||
		got.AttemptID != e3.AttemptID || got.Decision != "allowed" || !got.RequestWritten || got.StatusCode != 200 || events[2].Stage != "call_closed" {
		t.Fatalf("확정 투영 %+v", got.Receipt)
	}
	if _, _, err := e2proxy.VerifyJournalEvents(t.Context(), bytes.NewReader(raw), key); err == nil {
		t.Fatal("일반 검증이 확정 행이 있는 사본을 받음(v3·평가 쪽 검증은 그대로여야 함)")
	}
}

// 공용 관찰기는 확정 사건을 일반 observe(평가 관찰기 포함)에 넘기지 않는다. 승인 이력 갱신용 observeE3만 받는다.
func TestJournalObserverRoutesE3EventsOnlyToE3Observer(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	scope := transmit.Scope{RunRef: "run-mirror", TaskRef: "task-mirror", CaseRef: "case-mirror", ResetRef: "reset-mirror", PolicyRef: "policy-mirror"}
	worker := e2proxy.Event{Index: 1, Stage: "result", Receipt: e2proxy.Receipt{IntentID: 1, Scope: scope, AttemptID: "0123456789abcdef0123456789abcdef"}}
	e3 := mirrorE3Result(2, scope, at)
	for _, withE3 := range []bool{true, false} {
		var general, confirmation []e2proxy.Event
		observer := e2HTTPJournalObserver{next: &v3RecorderFixture{}, observe: func(_ context.Context, e e2proxy.Event) error {
			general = append(general, e)
			return nil
		}}
		if withE3 {
			observer.observeE3 = func(_ context.Context, e e2proxy.Event) { confirmation = append(confirmation, e) }
		}
		for _, e := range []e2proxy.Event{worker, e3} {
			if err := observer.Record(t.Context(), e); err != nil {
				t.Fatal(err)
			}
		}
		if len(general) != 1 || general[0].Program != "" {
			t.Fatalf("일반 관찰기가 확정 사건을 받음: %d건", len(general))
		}
		if withE3 && (len(confirmation) != 1 || confirmation[0].Program != e2proxy.ProgramE3Identity) || !withE3 && len(confirmation) != 0 {
			t.Fatalf("확정 관찰 %d건(연결 %v)", len(confirmation), withE3)
		}
	}
}
