package e2proxy

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
)

func TestExecutionV3AuditSelectionKeepsProgramSendBlocked(t *testing.T) {
	target := httptest.NewServer(nil)
	defer target.Close()
	recorder := &coreRecorder{}
	config := coreConfig(t, target.URL, recorder, 1)
	config.AuditVersion = JournalVersionV3
	execution, call := coreOpen(t, config)
	if _, receipt, err := call.Do(context.Background(), coreRequest(t, "POST", target.URL+"/allowed")); err == nil ||
		receipt.ForwardAttempted {
		t.Fatal("미승인 POST 송신")
	}
	if len(recorder.events) == 0 {
		t.Fatal("v3 사건 누락")
	}
	for _, event := range recorder.events {
		if event.Version != JournalVersionV3 || !validJournalRecordEvent(event, JournalVersionV3) {
			t.Fatal("v3 사건이 잘못 기록됨")
		}
	}
	if _, err := execution.BeginProgramCall(context.Background(), ProgramE3Create, "program-ref"); !errors.Is(err, ErrE3JournalUnavailable) {
		t.Fatal("Host 연결만으로 E③ 송신이 열림", err)
	}
	config.AuditVersion = "unrecognized"
	if _, err := NewExecution(context.Background(), config); !errors.Is(err, ErrConfig) {
		t.Fatal("알 수 없는 감사 판 수용", err)
	}
}
