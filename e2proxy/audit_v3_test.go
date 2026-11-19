package e2proxy

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

func journalV3Event() Event {
	event := journalTestEvent()
	event.Version = JournalVersionV3
	return event
}

func TestJournalV3DurableRoundTrip(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux 전용 저널 파일 생성")
	}
	ctx := t.Context()
	dir := journalTestDirectory(t)
	event := journalV3Event()
	for _, badRef := range []string{"", "https://example.invalid", "key\nref"} {
		if _, err := CreateJournalV3(dir, "run.journal", journalTestKey, event.Scope, badRef); !errors.Is(err, ErrJournalConfig) {
			t.Fatal("잘못된 키 참조로 저널을 만들었습니다", err)
		}
	}
	keyRef := "audit-key-1"
	key := bytes.Clone(journalTestKey)
	j, err := CreateJournalV3(dir, "run.journal", key, event.Scope, keyRef)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	clear(key)
	path := filepath.Join(dir, "run.journal")
	read := func() []byte {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	initial := j.Checkpoint()
	data := read()
	summary, err := VerifyJournal(ctx, bytes.NewReader(data), journalTestKey, initial)
	if err != nil || summary.Checkpoint != initial || initial.Scope != event.Scope || summary.Events != 0 ||
		!bytes.Contains(data, []byte(`"KeyRef":"audit-key-1"`)) {
		t.Fatal("헤더의 결속을 검증하지 못했습니다", err)
	}

	otherScope := event
	otherScope.Scope.ResetRef = "other-reset"
	legacy := event
	legacy.Version = JournalVersion
	unavailableProgram := event
	unavailableProgram.IntentID, unavailableProgram.Program = 0, ProgramE3Identity
	for _, bad := range []Event{otherScope, legacy, unavailableProgram} {
		if err := j.Record(ctx, bad); !errors.Is(err, ErrJournalEvent) || j.Checkpoint() != initial || !bytes.Equal(data, read()) {
			t.Fatal("잇기 전 검증 실패가 기록에 반영됐습니다", err)
		}
	}
	intent := event
	intent.Stage = "intent"
	intent.ForwardAttempted, intent.RequestWritten = false, false
	intent.ResponseReceived, intent.ResponseComplete, intent.StatusCode = false, false, 0
	event.Index = 2
	for _, row := range []Event{intent, event} {
		if err := j.Record(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	head := j.Checkpoint()
	rows, summary, err := VerifyJournalEvents(ctx, bytes.NewReader(read()), journalTestKey, head)
	if err != nil || summary.Checkpoint != head || !slices.Equal(rows, []Event{intent, event}) {
		t.Fatal("검증된 v3 행을 저장 순서대로 읽지 못했습니다", err)
	}
}

func TestJournalV3RequiresMatchingCheckpoint(t *testing.T) {
	ctx := t.Context()
	w := newJournalTestWriter()
	event := journalV3Event()
	j, err := newJournalWithHeader(w, journalTestKey, journalHeader{
		Version: JournalVersionV3, Scope: event.Scope, KeyRef: "audit-ref", CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	var prior JournalCheckpoint
	for range 2 {
		prior = j.Checkpoint()
		if err := j.Record(ctx, event); err != nil {
			t.Fatal(err)
		}
		event.Index++
		event.Sequence++
		event.AttemptID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	}
	data, head := w.bytes(), j.Checkpoint()
	lines := bytes.SplitAfter(data, []byte("\n"))
	withRows := func(order ...int) []byte {
		data := bytes.Clone(lines[0])
		for _, row := range order {
			data = append(data, lines[row]...)
		}
		return data
	}
	wrongScope, wrongID := head, head
	wrongScope.Scope.TaskRef = "other-task"
	wrongID.JournalID = "00112233445566778899aabbccddeeff"
	for _, tc := range []struct {
		name string
		data []byte
		head []JournalCheckpoint
	}{
		{"no-checkpoint", data, nil},
		{"foreign-task", data, []JournalCheckpoint{wrongScope}},
		{"foreign-journal", data, []JournalCheckpoint{wrongID}},
		{"header-key-tamper", bytes.Replace(data, []byte(`"KeyRef":"audit-ref"`), []byte(`"KeyRef":"other-audit-ref"`), 1), []JournalCheckpoint{head}},
		{"field-tamper", bytes.Replace(data, []byte(`"status_code":200`), []byte(`"status_code":201`), 1), []JournalCheckpoint{head}},
		{"duplicate-row", withRows(1, 1, 2), []JournalCheckpoint{head}},
		{"remove-middle", withRows(2), []JournalCheckpoint{head}},
		{"remove-tail", withRows(1), []JournalCheckpoint{head}},
		{"extra-tail", data, []JournalCheckpoint{prior}},
		{"reverse-rows", withRows(2, 1), []JournalCheckpoint{head}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, summary, err := VerifyJournalEvents(t.Context(), bytes.NewReader(tc.data), journalTestKey, tc.head...)
			if !errors.Is(err, ErrJournalIntegrity) || rows != nil || summary != (JournalSummary{}) {
				t.Fatal("무결성 검증 실패에서 기록을 반환했습니다", err)
			}
		})
	}
}

func TestJournalV3ByteLimitRefusesAppend(t *testing.T) {
	w := newJournalTestWriter()
	event := journalV3Event()
	j, err := newJournalWithHeader(w, journalTestKey, journalHeader{
		Version: JournalVersionV3, Scope: event.Scope, KeyRef: "audit-ref", CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	initial, size := j.Checkpoint(), w.length()
	j.size = journalMaxBytes
	if err := j.Record(t.Context(), event); !errors.Is(err, ErrJournalLimit) || j.Checkpoint() != initial || w.length() != size {
		t.Fatal("검증 크기 상한을 넘는 행을 기록했습니다", err)
	}
}

func TestJournalLegacyRejectsAuthenticatedV3HeaderFields(t *testing.T) {
	for _, version := range []string{VersionV1, JournalVersion} {
		header := journalHeader{Version: version, JournalID: "00112233445566778899aabbccddeeff",
			CreatedAt: time.Now().UTC(), Scope: journalTestEvent().Scope, KeyRef: "audit-ref"}
		mac, err := journalMAC(journalTestKey, version, "header", header)
		if err != nil {
			t.Fatal(err)
		}
		line, err := encodeJournalLine(journalLine{Kind: "header", Header: &header, MAC: mac})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyJournal(t.Context(), bytes.NewReader(line), journalTestKey); !errors.Is(err, ErrJournalIntegrity) {
			t.Fatal("구버전 저널에서 v3 헤더 필드를 읽었습니다", version, err)
		}
	}
}

func TestJournalV3SyncFailureCannotAdvanceCheckpoint(t *testing.T) {
	w := newJournalTestWriter()
	event := journalV3Event()
	j, err := newJournalWithHeader(w, journalTestKey, journalHeader{
		Version: JournalVersionV3, Scope: event.Scope, KeyRef: "audit-ref", CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	initial := j.Checkpoint()
	w.failSync = true
	if err := j.Record(t.Context(), event); !errors.Is(err, ErrJournalStorage) || j.Checkpoint() != initial {
		t.Fatal("Sync 실패가 체크포인트를 전진시켰습니다", err)
	}
	w.failSync = false
	if err := j.Record(t.Context(), event); !errors.Is(err, ErrJournalFailed) {
		t.Fatal("실패한 저널이 후속 기록을 받았습니다", err)
	}
	if rows, _, err := VerifyJournalEvents(t.Context(), bytes.NewReader(w.bytes()), journalTestKey, initial); !errors.Is(err, ErrJournalIntegrity) || rows != nil {
		t.Fatal("Sync 확인을 받지 못한 행을 검증된 사실로 반환했습니다", err)
	}
}

func TestJournalV3CanceledVerificationReturnsNoEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if rows, _, err := VerifyJournalEvents(ctx, bytes.NewReader(nil), journalTestKey, JournalCheckpoint{Version: JournalVersionV3}); !errors.Is(err, context.Canceled) || rows != nil {
		t.Fatal("취소한 읽기가 이벤트를 반환했습니다", err)
	}
}
