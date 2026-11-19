package e2proxy

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

func TestJournalRejectsE3ProgramEvenWithValidMAC(t *testing.T) {
	for _, version := range []string{VersionV1, Version, JournalVersionV3} {
		t.Run(version, func(t *testing.T) {
			first := journalTestEvent()
			first.Version = version
			header := journalHeader{Version: version, CreatedAt: first.At}
			if version == JournalVersionV3 {
				header.Scope, header.KeyRef = first.Scope, "fixture-key"
			}
			w := newJournalTestWriter()
			journal, err := newJournalWithHeader(w, journalTestKey, header)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = journal.Close() })
			if err := journal.Record(t.Context(), first); err != nil {
				t.Fatal(err)
			}
			prefix, head := w.bytes(), journal.Checkpoint()
			second := first
			second.Index, second.Sequence = 2, 2
			second.AttemptID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

			// 검증기가 저널 헤더와 앞 행뿐 아니라 마지막 행의 순번과 MAC까지 맞는 입력을 보도록
			// 시험에서 마지막 행을 만든다. 정상 쓰기 경로는 잘못된 귀속을 먼저 거부해야 한다.
			appendSigned := func(t *testing.T, event Event) ([]byte, JournalCheckpoint) {
				t.Helper()
				record := journalRecord{Index: event.Index, Previous: head.MAC, Event: event}
				mac, err := journalMAC(journalTestKey, version, "event", record)
				if err != nil {
					t.Fatal(err)
				}
				line, err := encodeJournalLine(journalLine{Kind: "event", Record: &record, MAC: mac})
				if err != nil {
					t.Fatal(err)
				}
				expected := head
				expected.Sequence, expected.MAC = record.Index, mac
				return append(bytes.Clone(prefix), line...), expected
			}
			valid, expected := appendSigned(t, second)
			rows, summary, err := VerifyJournalEvents(t.Context(), bytes.NewReader(valid), journalTestKey, expected)
			if err != nil || len(rows) != 2 || summary.Checkpoint != expected {
				t.Fatal("정상 대조 저널이 검증되지 않았습니다", err)
			}

			for _, program := range []ProgramCallKind{ProgramE3Identity, ProgramE3Create, ProgramE3Read} {
				for _, intentID := range []int64{0, first.IntentID} {
					t.Run(fmt.Sprintf("%s/intent-%d", program, intentID), func(t *testing.T) {
						untrusted := second
						untrusted.Program, untrusted.IntentID = program, intentID
						if err := journal.Record(t.Context(), untrusted); !errors.Is(err, ErrJournalEvent) ||
							journal.Checkpoint() != head || !bytes.Equal(w.bytes(), prefix) {
							t.Fatal("지원하지 않는 프로그램이 기록에 반영됐습니다", err)
						}
						data, checkpoint := appendSigned(t, untrusted)
						rows, summary, err := VerifyJournalEvents(t.Context(), bytes.NewReader(data), journalTestKey, checkpoint)
						if !errors.Is(err, ErrJournalIntegrity) || rows != nil || summary != (JournalSummary{}) {
							t.Fatal("지원하지 않는 프로그램 행이 있는 저널에서 검증 결과를 반환했습니다", err)
						}
					})
				}
			}
		})
	}
}
