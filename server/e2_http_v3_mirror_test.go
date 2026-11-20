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

func TestV3MirrorKeepsE3EventsOutOfLegacyJournal(t *testing.T) {
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
	host := &v3RecorderFixture{}
	mirror := e2HTTPV3MirrorRecorder{host: host, legacy: journal}
	event := e2proxy.Event{Stage: "result", Receipt: e2proxy.Receipt{
		Version: e2proxy.JournalVersionV3, Program: e2proxy.ProgramE3Identity,
	}}
	if err := mirror.Record(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if len(host.events) != 1 || journal.Checkpoint().Sequence != 0 {
		t.Fatal("E③ 사건이 Host 또는 v2 화면 저널의 잘못된 경로로 감")
	}
}

// E③ 사건은 Host에만 남고, 그 뒤 일반 사건(정지 control 포함)은 사본의 다음 순번으로 이어진다. Host 순번을 그대로 쓰면
// 사본 연속 순번 검사가 거부해 실행이 감사 실패로 래치됐다(실제 제품 합성 A/B 회차 S01, 11/20).
func TestV3MirrorRenumbersLegacyAfterE3Events(t *testing.T) {
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
	host := &v3RecorderFixture{}
	mirror := e2HTTPV3MirrorRecorder{host: host, legacy: journal}
	scope := transmit.Scope{RunRef: "run-mirror", TaskRef: "task-mirror", CaseRef: "case-mirror", ResetRef: "reset-mirror", PolicyRef: "policy-mirror"}
	at := time.Unix(1_700_000_000, 0).UTC()
	worker := e2proxy.Event{Index: 1, Stage: "result", Receipt: e2proxy.Receipt{
		Version: e2proxy.JournalVersionV3, IntentID: 1, Scope: scope, PolicyRevision: "revision-mirror",
		CallRef: "call-mirror", AttemptID: "0123456789abcdef0123456789abcdef", Sequence: 1,
		Method: "GET", Decision: "denied", Reason: e2proxy.PolicyDenied, NotSent: true,
		AuditComplete: true, At: at,
	}}
	control := e2proxy.Event{Index: 4, Stage: "control", Receipt: e2proxy.Receipt{
		Version: e2proxy.JournalVersionV3, Scope: scope, PolicyRevision: "revision-mirror",
		Reason: e2proxy.Closed, NotSent: true, AuditComplete: true, At: at,
	}}
	for _, event := range []e2proxy.Event{worker,
		{Index: 2, Stage: "result", Receipt: e2proxy.Receipt{Version: e2proxy.JournalVersionV3, Program: e2proxy.ProgramE3Identity}},
		{Index: 3, Stage: "call_closed", Receipt: e2proxy.Receipt{Version: e2proxy.JournalVersionV3, Program: e2proxy.ProgramE3Identity}},
		control} {
		if err := mirror.Record(t.Context(), event); err != nil {
			t.Fatalf("index %d 기록 실패: %v", event.Index, err)
		}
	}
	if len(host.events) != 4 || host.events[3].Index != 4 || journal.Checkpoint().Sequence != 2 {
		t.Fatalf("Host %d건·사본 순번 %d", len(host.events), journal.Checkpoint().Sequence)
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
	if err != nil || len(events) != 2 || events[0].Index != 1 || events[1].Index != 2 || events[1].Stage != "control" {
		t.Fatal("사본 검증·순번 불일치", err)
	}
}
