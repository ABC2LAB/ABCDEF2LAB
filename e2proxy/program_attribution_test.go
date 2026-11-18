package e2proxy

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestWorkerCallCannotUseProgramEntry(t *testing.T) {
	cfg := authConfig(t, "http://127.0.0.1:1")
	e, worker := coreOpen(t, cfg)
	before := e.SnapshotForIntent(1)
	extractorCalled := false
	_, receipt, err := worker.AuthPrep(t.Context(), coreRequest(t, "POST", cfg.Rules[0].Origin+"/login"),
		func(*http.Response, []byte) (http.Header, error) {
			extractorCalled = true
			return nil, nil
		})
	if !errors.Is(err, ErrConfig) || receipt != (Receipt{}) || extractorCalled {
		t.Fatal("Worker가 프로그램 전송 입구에서 요청을 시작했습니다")
	}
	after := e.SnapshotForIntent(1)
	if after.Counts != before.Counts || after.ProgramCounts != before.ProgramCounts || after.IntentCounts != before.IntentCounts {
		t.Fatal("실행을 거부한 요청이 호출 집계를 변경했습니다")
	}
}

func TestProgramCallAttributionIsExplicitAndExclusive(t *testing.T) {
	cfg := coreConfig(t, "http://127.0.0.1:1", &coreRecorder{}, 2)
	cfg.Limits.MaxCalls = 2
	e, err := NewExecution(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{0, -1, 2} {
		want := ErrConfig
		if id == 2 {
			want = ErrClosed
		}
		if _, err := e.BeginCallForIntent(t.Context(), id, "invalid-worker"); !errors.Is(err, want) {
			t.Fatal("등록 intent 검사 우회", id)
		}
	}
	for _, kind := range []ProgramCallKind{"", "unknown"} {
		if _, err := e.BeginProgramCall(t.Context(), kind, "invalid-program"); !errors.Is(err, ErrConfig) {
			t.Fatal("미등록 프로그램 종류 허용")
		}
	}
	for _, kind := range []ProgramCallKind{ProgramAuthPrep, ProgramRefreshSession} {
		c, err := e.BeginProgramCall(t.Context(), kind, "program-call")
		if err != nil {
			t.Fatal(err)
		}
		if c.Result().IntentID != 0 || c.Result().Program != kind {
			t.Fatal("프로그램에 Worker 귀속을 합성함")
		}
		if _, _, err := c.Do(t.Context(), coreRequest(t, "GET", cfg.Rules[0].Origin+"/allowed")); !errors.Is(err, ErrConfig) {
			t.Fatal("프로그램 호출이 Worker 전송 API를 사용함")
		}
		if _, err := c.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.BeginCallForIntent(t.Context(), 1, "over-budget-worker"); !errors.Is(err, ErrClosed) {
		t.Fatal("프로그램 호출이 전체 호출 상한을 우회함")
	}
	v := e.SnapshotForIntent(1)
	if v.Counts.Calls != 2 || v.Counts.Attempts != 0 || v.ProgramCounts.Calls != 2 || v.ProgramCounts.Attempts != 0 || v.IntentCounts != (Counts{}) {
		t.Fatal("프로그램/Worker 전체 호출 집계 불일치")
	}
}

func TestProgramJournalAttributionCannotMasqueradeAsIntent(t *testing.T) {
	event := journalTestEvent()
	event.IntentID, event.Program = 0, ProgramAuthPrep
	if !validJournalEvent(event, Version) {
		t.Fatal("새 프로그램 감사 귀속 거부")
	}
	for _, change := range []func(*Event){
		func(e *Event) { e.IntentID = 1 },
		func(e *Event) { e.IntentID = -1 },
		func(e *Event) { e.Program = "" },
		func(e *Event) { e.Program = "unknown" },
		func(e *Event) { e.Version = VersionV1 },
		func(e *Event) { e.Stage = "control"; e.AttemptID = ""; e.Sequence = 0; e.Decision = "" },
	} {
		changed := event
		change(&changed)
		if validJournalEvent(changed, changed.Version) {
			t.Fatal("잘못된 프로그램 감사 귀속 허용")
		}
	}
}
