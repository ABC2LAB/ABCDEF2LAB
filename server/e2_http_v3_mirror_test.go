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
