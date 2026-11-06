package db

import (
	"fmt"
	"testing"
	"time"
)

// 이전 실행이 남긴 추론 행은 목록·페이지·상세 어느 경로로도 나가지 않아야 한다. 다른 행과 사용량은 그대로다.
func TestActivityReadsHideOldThinkingRows(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("thinking-reads", "goal")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	store := d.Exploration(expID)

	appendRow := func(kind string) int64 {
		id, appendErr := store.AppendActivity(Activity{Worker: "w", Kind: kind, Summary: kind, Detail: kind + "-detail"})
		if appendErr != nil {
			t.Fatal(appendErr)
		}
		return id
	}
	textID := appendRow("text")
	thinkingID := appendRow("thinking")
	usageID := appendRow("usage")

	// 의도 하나로 좁힌 조회도 같아야 한다.
	intentID, err := store.AddIntent(map[string]any{"summary": "thinking-reads"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if _, appendErr := store.AppendActivity(Activity{NodeID: &intentID, Worker: "w", Kind: "thinking", Summary: "x", Detail: "x"}); appendErr != nil {
		t.Fatal(appendErr)
	}
	intentTextID, appendErr := store.AppendActivity(Activity{NodeID: &intentID, Worker: "w", Kind: "text", Summary: "y", Detail: "y"})
	if appendErr != nil {
		t.Fatal(appendErr)
	}
	byIntent, _, err := store.ActivityList(&intentID, 0, 10)
	if err != nil || len(byIntent) != 1 || byIntent[0].ID != intentTextID {
		t.Fatalf("ActivityList by intent: %+v err=%v", byIntent, err)
	}

	list, cursor, err := store.ActivityList(nil, 0, 10)
	if err != nil || len(list) != 3 || list[0].ID != textID || list[1].ID != usageID || list[2].ID != intentTextID || cursor != intentTextID {
		t.Fatalf("ActivityList: %+v cursor=%d err=%v", list, cursor, err)
	}
	page, more, err := store.ActivityPage(ActivitySessionFilter{}, 0, 10)
	if err != nil || more || len(page) != 3 {
		t.Fatalf("ActivityPage: %+v more=%v err=%v", page, more, err)
	}
	if detail, detailErr := store.ActivityDetail(thinkingID); detailErr != nil || detail != "" {
		t.Fatalf("ActivityDetail thinking: %q err=%v", detail, detailErr)
	}
	if detail, detailErr := store.ActivityDetail(textID); detailErr != nil || detail != "text-detail" {
		t.Fatalf("ActivityDetail text: %q err=%v", detail, detailErr)
	}
	if detail, detailErr := store.ActivityDetailWithSources(thinkingID); detailErr != nil || detail != "" {
		t.Fatalf("ActivityDetailWithSources thinking: %q err=%v", detail, detailErr)
	}

	conv, err := d.CreateConversation("mainagent", fmt.Sprintf("thinking-reads-%d", time.Now().UnixNano()), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM conversations WHERE id=$1`, conv.ID)
	appendConv := func(kind string) int64 {
		id, appendErr := d.AppendConvActivity(conv.ID, Activity{Worker: "w", Kind: kind, Summary: kind, Detail: kind + "-detail"})
		if appendErr != nil {
			t.Fatal(appendErr)
		}
		return id
	}
	convText := appendConv("text")
	convThinking := appendConv("thinking")
	convList, convCursor, err := d.ConvActivityList(conv.ID, 0, 10)
	if err != nil || len(convList) != 1 || convList[0].ID != convText || convCursor != convText {
		t.Fatalf("ConvActivityList: %+v cursor=%d err=%v", convList, convCursor, err)
	}
	convPage, convMore, err := d.ConvActivityPage(conv.ID, 0, 10)
	if err != nil || convMore || len(convPage) != 1 {
		t.Fatalf("ConvActivityPage: %+v more=%v err=%v", convPage, convMore, err)
	}
	if detail, detailErr := d.ConvActivityDetail(conv.ID, convThinking); detailErr != nil || detail != "" {
		t.Fatalf("ConvActivityDetail thinking: %q err=%v", detail, detailErr)
	}
}
