package db

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// 저장·모델 경로는 영어 그대로, 화면 경계만 한국어여야 한다. 위임받은(terminal) 조회도 metadata 를 실어야 화면에서 바꿀 수 있다.
func TestActivityDisplayTextBoundary(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("display-text", "goal")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	store := d.Exploration(expID)
	intentID, err := store.AddIntent(map[string]any{"summary": "display"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	display := DisplayMetadata("화면 요약", "화면 상세")
	id, err := store.AppendActivity(Activity{NodeID: &intentID, Worker: "w", Kind: "result", Summary: "model summary", Detail: "model detail", Metadata: display})
	if err != nil {
		t.Fatal(err)
	}
	plainID, err := store.AppendActivity(Activity{NodeID: &intentID, Worker: "w", Kind: "result", Summary: "plain summary", Detail: "plain detail"})
	if err != nil {
		t.Fatal(err)
	}

	list, _, err := store.ActivityList(&intentID, 0, 10)
	if err != nil || len(list) != 2 {
		t.Fatalf("ActivityList: %+v err=%v", list, err)
	}
	if list[0].Summary != "model summary" {
		t.Fatalf("stored summary must stay the model text: %q", list[0].Summary)
	}
	if shown := list[0].ForDisplay(); shown.Summary != "화면 요약" || shown.Metadata != nil {
		t.Fatalf("display summary: %+v", shown)
	}
	if shown := list[1].ForDisplay(); shown.Summary != "plain summary" {
		t.Fatalf("row without display text changed: %+v", shown)
	}
	if detail, detailErr := store.ActivityDetail(id); detailErr != nil || detail != "model detail" {
		t.Fatalf("ActivityDetail: %q err=%v", detail, detailErr)
	}
	for _, read := range []struct {
		name string
		fn   func(int64) (string, error)
	}{{"ActivityDisplayDetail", store.ActivityDisplayDetail}, {"ActivityDisplayDetailWithSources", store.ActivityDisplayDetailWithSources}} {
		if detail, detailErr := read.fn(id); detailErr != nil || detail != "화면 상세" {
			t.Fatalf("%s: %q err=%v", read.name, detail, detailErr)
		}
		if detail, detailErr := read.fn(plainID); detailErr != nil || detail != "plain detail" {
			t.Fatalf("%s plain: %q err=%v", read.name, detail, detailErr)
		}
	}

	if err := store.SetIntentState(intentID, "done"); err != nil {
		t.Fatal(err)
	}
	terminal, _, err := store.ActivityListForTerminalIntent(intentID, 0, 10)
	if err != nil || len(terminal) != 2 || terminal[0].ForDisplay().Summary != "화면 요약" {
		t.Fatalf("ActivityListForTerminalIntent: %+v err=%v", terminal, err)
	}
	page, _, err := store.ActivityPageForTerminalIntent(intentID, 0, 10)
	if err != nil || len(page) != 2 || page[0].ForDisplay().Summary != "화면 요약" {
		t.Fatalf("ActivityPageForTerminalIntent: %+v err=%v", page, err)
	}
	byID, err := store.ActivityByIDsForTerminalIntents([]int64{id})
	if err != nil || len(byID) != 1 || byID[0].Detail != "model detail" || byID[0].ForDisplay().Detail != "화면 상세" {
		t.Fatalf("ActivityByIDsForTerminalIntents: %+v err=%v", byID, err)
	}

	conv, err := d.CreateConversation("mainagent", fmt.Sprintf("display-text-%d", time.Now().UnixNano()), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM conversations WHERE id=$1`, conv.ID)
	convID, err := d.AppendConvActivity(conv.ID, Activity{Kind: "result", Summary: "model summary", Detail: "model detail", Metadata: display})
	if err != nil {
		t.Fatal(err)
	}
	convList, _, err := d.ConvActivityList(conv.ID, 0, 10)
	if err != nil || len(convList) != 1 || convList[0].Summary != "화면 요약" {
		t.Fatalf("ConvActivityList: %+v err=%v", convList, err)
	}
	if detail, detailErr := d.ConvActivityDetail(conv.ID, convID); detailErr != nil || detail != "화면 상세" {
		t.Fatalf("ConvActivityDetail: %q err=%v", detail, detailErr)
	}
}

func TestForDisplayLeavesOtherRowsAlone(t *testing.T) {
	other := json.RawMessage(`{"llm_transition":{"from":"a"}}`)
	for _, a := range []Activity{
		{Kind: "llm_switch", Summary: "s", Metadata: DisplayMetadata("x", "y")},
		{Kind: "result", Summary: "s", Metadata: other},
		{Kind: "result", Summary: "s", Metadata: json.RawMessage(`{`)},
		{Kind: "result", Summary: "s"},
	} {
		if got := a.ForDisplay(); got.Summary != "s" || len(got.Metadata) != len(a.Metadata) {
			t.Errorf("row changed: %+v -> %+v", a, got)
		}
	}
	// 목록 행은 상세가 비어 있다. 화면용 상세로 채우면 지연 로딩 계약이 깨진다.
	if got := (Activity{Kind: "result", Summary: "s", Metadata: DisplayMetadata("x", "y")}).ForDisplay(); got.Detail != "" {
		t.Errorf("list row detail must stay empty: %q", got.Detail)
	}
}
