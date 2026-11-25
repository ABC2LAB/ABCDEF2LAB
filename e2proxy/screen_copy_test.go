package e2proxy

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Autumn-27/artex/transmit"
)

func screenTestScope() transmit.Scope {
	return transmit.Scope{RunRef: "run-screen", TaskRef: "task-screen", CaseRef: "case-screen", ResetRef: "reset-screen", PolicyRef: "policy-screen"}
}

// screenTestE3Result는 Host v3 저널에 남는 모양의 확정 단계 결과 행이다(E3Audit·메서드 포함, 대상 응답 완료).
func screenTestE3Result(index uint64) Event {
	return Event{Index: index, Stage: "result", Receipt: Receipt{
		E3Audit: &E3Audit{AccountLabel: "tester-a", ResourceType: "book"}, Program: ProgramE3Create, Version: JournalVersionV3,
		Scope: screenTestScope(), CallRef: "call-e3-create", CommandRef: "command-e3", AttemptID: "0123456789abcdef0123456789abcdef",
		PolicyRevision: "revision-screen", RuleRef: "e3-create-rule", Sequence: 1, Method: "POST", Decision: "allowed", Reason: Complete,
		ForwardAttempted: true, RequestWritten: true, ResponseReceived: true, ResponseComplete: true, AuditComplete: true,
		StatusCode: 201, At: time.Unix(1_700_000_000, 0).UTC(),
	}}
}

func screenTestJournal(t *testing.T, create func(string, string, []byte) (*Journal, error)) (*Journal, string, []byte) {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{0x5a}, 32)
	j, err := create(directory, "events.jsonl", key)
	if err != nil {
		t.Fatal(err)
	}
	return j, filepath.Join(directory, "events.jsonl"), key
}

// 화면 사본만 확정 단계 투영을 받고, 투영은 E3Audit·메서드·명령 참조 없이 식별자·종류·결정·송신 단계·상태 코드를 남긴다.
// 일반 v2 저널과 일반 검증은 확정 행을 계속 거부한다(v3·평가 검증 그대로, 2026-11-25 이동찬 결정).
func TestScreenJournalAcceptsOnlyE3Projection(t *testing.T) {
	projected := E3ScreenProjection(screenTestE3Result(7))
	projected.Version, projected.Index = Version, 1
	if projected.E3Audit != nil || projected.Method != "" || projected.CommandRef != "" || projected.Program != ProgramE3Create ||
		projected.CallRef != "call-e3-create" || projected.AttemptID != "0123456789abcdef0123456789abcdef" || projected.Decision != "allowed" ||
		!projected.RequestWritten || projected.StatusCode != 201 || !projected.ResponseComplete {
		t.Fatalf("투영 칸 %+v", projected.Receipt)
	}
	closed := E3ScreenProjection(Event{Index: 8, Stage: "call_closed", Receipt: Receipt{Program: ProgramE3Create, Version: JournalVersionV3,
		Scope: screenTestScope(), CallRef: "call-e3-create", PolicyRevision: "revision-screen", Reason: Complete, AuditComplete: true,
		At: time.Unix(1_700_000_001, 0).UTC()}})
	closed.Version, closed.Index = Version, 2

	screen, path, key := screenTestJournal(t, CreateScreenJournal)
	for _, bad := range []func(*Event){
		func(e *Event) { e.E3Audit = &E3Audit{AccountLabel: "tester-a"} },
		func(e *Event) { e.Method = "POST" },
		func(e *Event) { e.CommandRef = "command-e3" },
		func(e *Event) { e.IntentID = 3 },
	} {
		e := projected
		bad(&e)
		if screen.Record(t.Context(), e) == nil {
			t.Fatalf("화면 사본이 투영 아닌 확정 행을 받음 %+v", e.Receipt)
		}
	}
	for _, e := range []Event{projected, closed} {
		if err := screen.Record(t.Context(), e); err != nil {
			t.Fatalf("화면 사본이 확정 투영을 거부: %v", err)
		}
	}
	if err := screen.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	events, _, err := VerifyScreenJournalEvents(t.Context(), bytes.NewReader(raw), key)
	if err != nil || len(events) != 2 || events[0].Program != ProgramE3Create || events[0].E3Audit != nil || events[1].Stage != "call_closed" {
		t.Fatalf("화면 사본 검증 %v %+v", err, events)
	}
	if _, _, err := VerifyJournalEvents(t.Context(), bytes.NewReader(raw), key); err == nil {
		t.Fatal("일반 검증이 확정 행이 있는 사본을 받음")
	}

	plain, _, _ := screenTestJournal(t, CreateJournal)
	defer plain.Close()
	if plain.Record(t.Context(), projected) == nil {
		t.Fatal("일반 v2 저널이 확정 행을 받음")
	}
}
