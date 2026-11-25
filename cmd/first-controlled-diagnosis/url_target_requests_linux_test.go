package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/e2proxy"
)

// targetRequestEvents는 모델 시도(보냄·결과 불명·거부), 프로그램 로그인(인증 준비·세션 다시 받기·거부), 같은 시도의 중복
// 결과 행, 호출 닫힘·통제 행이 섞인 저널이다.
func targetRequestEvents() []e2proxy.Event {
	program := func(e e2proxy.Event, kind e2proxy.ProgramCallKind) e2proxy.Event {
		e.IntentID, e.Program = 0, kind
		if e.Decision == "allowed" {
			e.RuleRef = "auth-login"
		}
		return e
	}
	modelSent := liveEvent("result", "call-model-a", strings.Repeat("a", 32), 1, "allowed", e2proxy.Complete)
	return []e2proxy.Event{
		program(liveEvent("result", "call-login", strings.Repeat("1", 32), 1, "allowed", e2proxy.Complete), e2proxy.ProgramAuthPrep),
		program(liveEvent("result", "call-login-denied", strings.Repeat("2", 32), 1, "denied", e2proxy.PolicyDenied), e2proxy.ProgramAuthPrep),
		modelSent,
		modelSent,
		liveEvent("result", "call-model-b", strings.Repeat("b", 32), 1, "allowed", e2proxy.Unknown),
		liveEvent("result", "call-model-c", strings.Repeat("c", 32), 1, "denied", e2proxy.PolicyDenied),
		liveEvent("call_closed", "call-model-a", "", 0, "", e2proxy.Complete),
		program(liveEvent("result", "call-refresh", strings.Repeat("3", 32), 1, "allowed", e2proxy.Complete), e2proxy.ProgramRefreshSession),
		liveEvent("control", "", "", 0, "", e2proxy.Closed),
	}
}

// writtenAttempts는 저널에서 요청을 쓴 시도 수를 시험 쪽에서 따로 센다(서버 계산과 대조).
func writtenAttempts(events []e2proxy.Event) (model, login int) {
	seen := map[string]bool{}
	for _, e := range events {
		if e.Stage != "result" || seen[e.AttemptID] || !e.RequestWritten {
			continue
		}
		seen[e.AttemptID] = true
		if e.Program == "" {
			model++
		} else {
			login++
		}
	}
	return model, login
}

func targetRequestsFixture(t *testing.T, record *explorationRequestsRecord, raw string) string {
	t.Helper()
	runsDir, policy := liveRunFixture(t, func(add func(e2proxy.Event)) {
		for _, e := range targetRequestEvents() {
			add(e)
		}
	})
	switch {
	case record != nil:
		if err := writeExplorationRequests(policy.StateDir, *record); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(filepath.Join(policy.StateDir, explorationRequestsFile))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("탐색 요청 수 기록 권한 %v err=%v", info, err)
		}
	case raw != "":
		if err := os.WriteFile(filepath.Join(policy.StateDir, explorationRequestsFile), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return runsDir
}

func targetRequestsGet(t *testing.T, runsDir string, live liveExploration) e2FlowTargetRequests {
	t.Helper()
	w := httptest.NewRecorder()
	serveLiveDataWith(w, httptest.NewRequest(http.MethodGet, "/api/tasks/9/e2-flow", nil), runsDir, liveJob, "e2-flow", live)
	var out e2FlowView
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Journal != "verified" || out.TargetRequests == nil {
		t.Fatalf("e2-flow 대상 요청 내역 없음: %d %s", w.Code, w.Body.String())
	}
	return *out.TargetRequests
}

// 모델·로그인 수는 검증한 저널에서 요청을 쓴 시도 수와 같고, 탐색 수는 경계가 확정한 기록과 같으며, 합은 둘을 더한 값이다.
func TestE2FlowTargetRequestsMatchJournalAndExplorationRecord(t *testing.T) {
	model, login := writtenAttempts(targetRequestEvents())
	if model != 2 || login != 2 {
		t.Fatalf("시험 저널 구성이 바뀜 model=%d login=%d", model, login)
	}
	closed := explorationKnown(explorationClosed, 7)
	got := targetRequestsGet(t, targetRequestsFixture(t, &closed, ""), nil)
	if got.Model != model || got.Login != login || got.Confirmation != 0 || got.ExplorationState != explorationClosed ||
		got.Exploration == nil || *got.Exploration != 7 || got.Total == nil || *got.Total != uint64(model+login)+7 {
		t.Fatalf("대상 요청 내역 %+v", got)
	}
}

func TestE2FlowTargetRequestsExplorationStates(t *testing.T) {
	model, login := writtenAttempts(targetRequestEvents())
	written := uint64(model + login)
	pending, notRun, unknown := explorationKnown(explorationPending, 0), explorationKnown(explorationNotRun, 0), explorationRequestsRecord{Format: explorationRequestsFormat, State: explorationUnknown}
	running := explorationRequestsRecord{Format: explorationRequestsFormat, State: explorationRunning}
	liveThree := func(ctx context.Context, job string) (uint64, bool) { return 3, job == liveJob && ctx.Err() == nil }
	noLive := func(context.Context, string) (uint64, bool) { return 0, false }
	for name, c := range map[string]struct {
		record      *explorationRequestsRecord
		raw         string
		live        liveExploration
		state       string
		exploration int64 // -1이면 모름(null)
	}{
		"no-exploration-stage": {state: explorationNone, exploration: 0},
		"pending":              {record: &pending, state: explorationPending, exploration: 0},
		"not-run":              {record: &notRun, state: explorationNotRun, exploration: 0},
		"running-live":         {record: &running, live: liveThree, state: explorationRunning, exploration: 3},
		"running-no-boundary":  {record: &running, live: noLive, state: explorationRunning, exploration: -1},
		"running-no-live-func": {record: &running, state: explorationRunning, exploration: -1},
		"unknown":              {record: &unknown, state: explorationUnknown, exploration: -1},
		"broken-record":        {raw: `{"format":"abc2lab.exploration-requests/v1","state":"closed"}`, state: explorationUnknown, exploration: -1},
	} {
		got := targetRequestsGet(t, targetRequestsFixture(t, c.record, c.raw), c.live)
		if got.Model != model || got.Login != login || got.ExplorationState != c.state {
			t.Fatalf("%s: %+v", name, got)
		}
		if c.exploration < 0 {
			if got.Exploration != nil || got.Total != nil {
				t.Fatalf("%s: 모르는 탐색 수를 숫자로 보임 %+v", name, got)
			}
			continue
		}
		if got.Exploration == nil || *got.Exploration != uint64(c.exploration) || got.Total == nil || *got.Total != written+uint64(c.exploration) {
			t.Fatalf("%s: %+v", name, got)
		}
	}
}

func TestExplorationResultRecordKeepsOnlyBoundaryConfirmedCounts(t *testing.T) {
	for _, c := range []struct {
		notRun, known bool
		requests      uint64
		state         string
		count         int64
	}{
		{false, true, 9, explorationClosed, 9},
		{true, true, 0, explorationNotRun, 0},
		{false, true, 0, explorationClosed, 0},
		{false, false, 5, explorationUnknown, -1},
		{true, false, 0, explorationUnknown, -1},
	} {
		r := explorationResultRecord(c.notRun, c.known, c.requests)
		if !r.valid() || r.State != c.state || (c.count < 0) != (r.TargetRequests == nil) || c.count >= 0 && *r.TargetRequests != uint64(c.count) {
			t.Fatalf("%+v -> %+v", c, r)
		}
	}
	three := uint64(3)
	for _, bad := range []explorationRequestsRecord{
		{Format: "other", State: explorationClosed, TargetRequests: &three},
		{Format: explorationRequestsFormat, State: explorationRunning, TargetRequests: &three},
		{Format: explorationRequestsFormat, State: explorationPending, TargetRequests: &three},
		{Format: explorationRequestsFormat, State: explorationClosed},
		{Format: explorationRequestsFormat, State: explorationNone},
	} {
		if writeExplorationRequests(t.TempDir(), bad) == nil {
			t.Fatalf("잘못된 기록을 씀 %+v", bad)
		}
	}
}
