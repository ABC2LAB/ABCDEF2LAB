package e2proxy

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

// v1Journal은 C16 전 판으로 쓴 기록을 만든다. 판 문자열과 MAC 도메인만 v1이고 나머지 규칙은 같다.
func v1Journal(t *testing.T, events ...Event) []byte {
	t.Helper()
	header := journalHeader{Version: VersionV1, JournalID: "00112233445566778899aabbccddeeff", CreatedAt: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)}
	mac, err := journalMAC(journalTestKey, VersionV1, "header", header)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	line, err := encodeJournalLine(journalLine{Kind: "header", Header: &header, MAC: mac})
	if err != nil {
		t.Fatal(err)
	}
	out.Write(line)
	previous := mac
	for i, event := range events {
		event.Index = uint64(i + 1)
		record := journalRecord{Index: event.Index, Previous: previous, Event: event}
		if previous, err = journalMAC(journalTestKey, VersionV1, "event", record); err != nil {
			t.Fatal(err)
		}
		if line, err = encodeJournalLine(journalLine{Kind: "event", Record: &record, MAC: previous}); err != nil {
			t.Fatal(err)
		}
		out.Write(line)
	}
	return out.Bytes()
}

func v1Event() Event {
	e := journalTestEvent()
	e.Version = VersionV1
	return e
}

// 저장된 v1 기록은 v1 규칙으로 계속 검증한다. 새 기록은 v2로만 쓴다.
func TestJournalVerifiesStoredV1Records(t *testing.T) {
	summary, err := VerifyJournal(context.Background(), bytes.NewReader(v1Journal(t, v1Event())), journalTestKey)
	if err != nil || summary.Events != 1 || summary.Checkpoint.Version != VersionV1 {
		t.Fatalf("stored v1 journal rejected: %+v %v", summary, err)
	}
	w := newJournalTestWriter()
	j, err := newJournal(w, journalTestKey, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Record(context.Background(), journalTestEvent()); err != nil {
		t.Fatal(err)
	}
	summary, err = VerifyJournal(context.Background(), bytes.NewReader(w.bytes()), journalTestKey)
	if err != nil || summary.Checkpoint.Version != Version || Version != "e2-http-v2" {
		t.Fatalf("new journal is not v2: %+v %v", summary, err)
	}
}

// v1 행에는 withheld·response_truncated가 없다. 판이 섞인 기록(v1 머리 + v2 행, v2 MAC 도메인으로 쓴 v1 머리)도 거부한다.
func TestJournalV1RulesRejectV2Fields(t *testing.T) {
	withheld := v1Event()
	withheld.Reason, withheld.ResponseComplete = Withheld, false
	truncated := v1Event()
	truncated.ResponseTruncated = true
	mixed := journalTestEvent()
	for name, data := range map[string][]byte{
		"v1 withheld":  v1Journal(t, withheld),
		"v1 truncated": v1Journal(t, truncated),
		"v2 row in v1": v1Journal(t, mixed),
	} {
		if _, err := VerifyJournal(context.Background(), bytes.NewReader(data), journalTestKey); !errors.Is(err, ErrJournalIntegrity) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// 머리를 v1이라 적고 v2 도메인으로 MAC을 만든 기록은 인증되지 않는다.
	header := journalHeader{Version: VersionV1, JournalID: "00112233445566778899aabbccddeeff", CreatedAt: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)}
	mac, _ := journalMAC(journalTestKey, Version, "header", header)
	line, _ := encodeJournalLine(journalLine{Kind: "header", Header: &header, MAC: mac})
	if _, err := VerifyJournal(context.Background(), bytes.NewReader(line), journalTestKey); !errors.Is(err, ErrJournalIntegrity) {
		t.Fatalf("cross-version MAC accepted: %v", err)
	}
}

// v2 보류 행: 허용·결과 단계·응답 수신·완전 응답 아님. 자른 응답 표시는 보류에만 붙는다.
func TestJournalV2AcceptsWithheldShape(t *testing.T) {
	for name, change := range map[string]func(*Event){
		"withheld":  func(e *Event) { e.Reason, e.ResponseComplete = Withheld, false },
		"truncated": func(e *Event) { e.Reason, e.ResponseComplete, e.ResponseTruncated = Withheld, false, true },
	} {
		w := newJournalTestWriter()
		j, err := newJournal(w, journalTestKey, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		event := journalTestEvent()
		change(&event)
		if err := j.Record(context.Background(), event); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if summary, err := VerifyJournal(context.Background(), bytes.NewReader(w.bytes()), journalTestKey); err != nil || summary.Events != 1 {
			t.Fatalf("%s: %+v %v", name, summary, err)
		}
	}
}

// call_closed 행은 call의 마지막 사유를 요약으로 싣는다. 보류로 끝난 call도 닫는 기록을 남겨야 한다(v2).
func TestJournalCallClosedMayCarryWithheld(t *testing.T) {
	w := newJournalTestWriter()
	j, err := newJournal(w, journalTestKey, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	e := journalTestEvent()
	e.Stage, e.AttemptID, e.Sequence, e.Decision, e.Method, e.RuleRef = "call_closed", "", 0, "", "", ""
	e.Reason, e.ForwardAttempted, e.RequestWritten, e.ResponseReceived, e.ResponseComplete, e.StatusCode = Withheld, false, false, false, false, 0
	if err := j.Record(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	e.Index, e.ResponseTruncated = 2, true
	if err := j.Record(context.Background(), e); !errors.Is(err, ErrJournalEvent) {
		t.Fatalf("truncation mark on a call summary accepted: %v", err)
	}
}
