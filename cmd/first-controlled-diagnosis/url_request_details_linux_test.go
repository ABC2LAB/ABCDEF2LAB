package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/pathmask"
	"github.com/Autumn-27/artex/server"
)

func requestDetailsGet(t *testing.T, runsDir, path string) *httptest.ResponseRecorder {
	t.Helper()
	view := liveDataView(strings.SplitN(path, "?", 2)[0])
	if view == "" {
		t.Fatalf("요청 상세 경로가 아님: %s", path)
	}
	w := httptest.NewRecorder()
	serveLiveData(w, httptest.NewRequest(http.MethodGet, path, nil), runsDir, liveJob, view)
	return w
}

func requestsFixture(t *testing.T) string {
	t.Helper()
	runsDir, policy := liveRunFixture(t, func(add func(e2proxy.Event)) {
		for _, e := range targetRequestEvents() {
			add(e)
		}
	})
	// 시도별 화면용 경로 틀(가린 값)을 붙인다. 원문 경로는 상세에 나오지 않는다.
	rows := []server.E2AttemptPatternRow{
		{AttemptID: strings.Repeat("a", 32), PathPattern: "/api/users/{id}", DisplayVersion: 2},
		{AttemptID: strings.Repeat("b", 32), PathPattern: pathmask.LongPath, DisplayVersion: 2},
	}
	var raw []byte
	for _, row := range rows {
		line, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		raw = append(append(raw, line...), '\n')
	}
	if err := os.WriteFile(filepath.Join(policy.StateDir, server.E2AttemptPatternsFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return runsDir
}

// 전체 기록은 쪽 단위로 보낸 요청만 저널 순서대로 한 번씩 주고, 건수는 e2-flow 대상 요청 내역(저널 몫)과 같다.
func TestE2RequestsPagesMatchFlowTargetRequests(t *testing.T) {
	runsDir := requestsFixture(t)
	flow := targetRequestsGet(t, runsDir, nil)
	journalSent := flow.Model + flow.Login + flow.Confirmation + flow.Unclassified
	var rows []e2RequestRow
	path := "/api/tasks/9/e2-flow/requests?limit=2"
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("쪽이 끝나지 않음")
		}
		w := requestDetailsGet(t, runsDir, path)
		var page e2RequestsPage
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Format != e2RequestsViewFormat || len(page.Rows) > 2 {
			t.Fatalf("전체 기록 쪽 %d: %s", w.Code, w.Body.String())
		}
		if page.Total != journalSent {
			t.Fatalf("전체 기록 건수 %d, e2-flow 저널 몫 %d", page.Total, journalSent)
		}
		rows = append(rows, page.Rows...)
		if page.NextCursor == nil {
			break
		}
		path = "/api/tasks/9/e2-flow/requests?limit=2&cursor=" + strings.TrimSpace(string(must(json.Marshal(*page.NextCursor))))
	}
	if len(rows) != journalSent {
		t.Fatalf("쪽을 다 모은 줄 %d개, 기대 %d", len(rows), journalSent)
	}
	kinds := map[string]int{}
	seen := map[string]bool{}
	for i, row := range rows {
		if row.CallRef == "" || !attemptRefValid(row.AttemptID) || seen[row.AttemptID] || i > 0 && row.Index <= rows[i-1].Index || row.At.IsZero() {
			t.Fatalf("줄 %d 모양 %+v", i, row)
		}
		seen[row.AttemptID] = true
		if (row.Path == "" || row.Path == pathmask.LongPath) && row.PathDetailState != server.PathDetailUnavailable {
			t.Fatalf("경로 기록이 없는 줄의 상태: %+v", row)
		}
		kinds[row.Kind]++
	}
	if kinds[e2RequestKindModel] != flow.Model || kinds[string(e2proxy.ProgramAuthPrep)]+kinds[string(e2proxy.ProgramRefreshSession)] != flow.Login || kinds[string(e2proxy.ProgramAuthPrep)] != 1 {
		t.Fatalf("줄 종류 %+v, e2-flow %+v", kinds, flow)
	}
	if rows[1].Path != "/api/users/{id}" || rows[1].PathDetailState != server.PathDetailPatternOnly || rows[1].Kind != e2RequestKindModel || rows[1].Status != 200 {
		t.Fatalf("모델 줄 경로 틀·코드 %+v", rows[1])
	}
	for _, bad := range []string{"?limit=0", "?limit=201", "?cursor=x", "?after=1", "?cursor=1&cursor=2", "?limit=01"} {
		if w := requestDetailsGet(t, runsDir, "/api/tasks/9/e2-flow/requests"+bad); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", bad, w.Code)
		}
	}
}

func must(raw []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return raw
}

// 상세는 저널 칸만 주고 헤더·본문은 저장하지 않음으로 내린다. 못 읽는 경우는 이유 코드로 나눈다.
func TestE2RequestDetailShowsJournalFieldsAndReasons(t *testing.T) {
	runsDir := requestsFixture(t)
	w := requestDetailsGet(t, runsDir, "/api/tasks/9/e2-flow/requests/call-model-a/"+strings.Repeat("a", 32))
	var detail e2RequestDetail
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &detail) != nil {
		t.Fatalf("상세 %d: %s", w.Code, w.Body.String())
	}
	notStored := e2RequestField{State: e2FieldNotStored}
	if detail.Format != e2RequestViewFormat || detail.Kind != e2RequestKindModel || detail.Method != "POST" || detail.PathPattern != "/api/users/{id}" || detail.PathDetailState != server.PathDetailPatternOnly || detail.RuleRef != "target-path-a" ||
		detail.Decision != "allowed" || detail.Reason != string(e2proxy.Complete) || detail.StatusCode != 200 || !detail.RequestWritten || !detail.ResponseReceived || !detail.ResponseComplete ||
		detail.RequestHeaders != notStored || detail.RequestBody != notStored || detail.ResponseHeaders != notStored || detail.ResponseBody != notStored {
		t.Fatalf("상세 칸 %+v", detail)
	}
	// 거부된 로그인 시도도 저널에 있으면 상세가 열린다(보내지 않았음이 칸에 보인다).
	detail = e2RequestDetail{}
	w = requestDetailsGet(t, runsDir, "/api/tasks/9/e2-flow/requests/call-login-denied/"+strings.Repeat("2", 32))
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &detail) != nil || detail.Kind != string(e2proxy.ProgramAuthPrep) || detail.RequestWritten || detail.Decision != "denied" ||
		detail.PathPattern != "" || detail.PathDetailState != server.PathDetailUnavailable {
		t.Fatalf("거부 시도 상세 %d %+v", w.Code, detail)
	}
	detail = e2RequestDetail{}
	w = requestDetailsGet(t, runsDir, "/api/tasks/9/e2-flow/requests/call-model-b/"+strings.Repeat("b", 32))
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &detail) != nil ||
		detail.PathPattern != pathmask.LongPath || detail.PathDetailState != server.PathDetailUnavailable {
		t.Fatalf("길이 상한 시도 상세 %d %+v", w.Code, detail)
	}
	reason := func(w *httptest.ResponseRecorder) string {
		var out map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out["reason"]
	}
	if w := requestDetailsGet(t, runsDir, "/api/tasks/9/e2-flow/requests/call-model-a/"+strings.Repeat("f", 32)); w.Code != http.StatusNotFound || reason(w) != e2RequestNotFound {
		t.Fatalf("없는 시도 %d %s", w.Code, w.Body.String())
	}
	if w := requestDetailsGet(t, runsDir, "/api/tasks/9/e2-flow/requests/call-other/"+strings.Repeat("a", 32)); w.Code != http.StatusNotFound || reason(w) != e2RequestNotFound {
		t.Fatalf("다른 호출의 시도 %d %s", w.Code, w.Body.String())
	}
	if w := requestDetailsGet(t, runsDir, "/api/tasks/9/e2-flow/requests/call-model-a/"+strings.Repeat("a", 32)+"?limit=1"); w.Code != http.StatusBadRequest {
		t.Fatalf("상세 조회 조건 %d", w.Code)
	}
	// 저널이 아직 없는 회차(실행 전)는 저널에 안 남음, 검증하지 못한 저널은 따로 이유를 준다.
	empty, _ := liveRunFixture(t, nil)
	for _, path := range []string{"/api/tasks/9/e2-flow/requests", "/api/tasks/9/e2-flow/requests/call-model-a/" + strings.Repeat("a", 32)} {
		if w := requestDetailsGet(t, empty, path); w.Code != http.StatusNotFound || reason(w) != e2RequestNotJournaled {
			t.Fatalf("저널 없음 %s %d %s", path, w.Code, w.Body.String())
		}
	}
	journal := filepath.Join(runsDir, liveJob, "state", "events.jsonl")
	raw, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal, []byte(strings.Replace(string(raw), "call-model-a", "call-model-z", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if w := requestDetailsGet(t, runsDir, "/api/tasks/9/e2-flow/requests"); w.Code != http.StatusConflict || reason(w) != e2RequestJournalInvalid {
		t.Fatalf("바뀐 저널 %d %s", w.Code, w.Body.String())
	}
}

// 같은 시도의 결과 행이 다른 시도 사이에 끼어도(A(1)·B(2)·A(3)) 쪽 넘김이 줄을 빠뜨리거나 두 번 주지 않는다. 줄은 시도마다
// 마지막 결과 행의 저널 번호 순서다(#931 이동찬 P2).
func TestE2RequestsPagesInterleavedDuplicateResults(t *testing.T) {
	a := liveEvent("result", "call-a", strings.Repeat("a", 32), 1, "allowed", e2proxy.Complete)
	b := liveEvent("result", "call-b", strings.Repeat("b", 32), 1, "allowed", e2proxy.Complete)
	c := liveEvent("result", "call-c", strings.Repeat("c", 32), 1, "allowed", e2proxy.Complete)
	runsDir, _ := liveRunFixture(t, func(add func(e2proxy.Event)) {
		for _, e := range []e2proxy.Event{a, b, a, c, b} {
			add(e)
		}
	})
	var got []string
	path := "/api/tasks/9/e2-flow/requests?limit=1"
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("쪽이 끝나지 않음")
		}
		w := requestDetailsGet(t, runsDir, path)
		var page e2RequestsPage
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Total != 3 {
			t.Fatalf("쪽 %d: %s", w.Code, w.Body.String())
		}
		for _, row := range page.Rows {
			got = append(got, row.CallRef)
		}
		if page.NextCursor == nil {
			break
		}
		path = "/api/tasks/9/e2-flow/requests?limit=1&cursor=" + string(must(json.Marshal(*page.NextCursor)))
	}
	// 마지막 결과 행 번호: A=3, C=4, B=5.
	if strings.Join(got, ",") != "call-a,call-c,call-b" {
		t.Fatalf("쪽을 이은 줄 %v", got)
	}
}
