package compensation

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const (
	testOrigin = "http://target.example:3000"
	testPost   = "/api/things"
)

func testRules() Rules {
	return Rules{WeakPaths: []string{testPost}, TableByPath: map[string]string{testPost: "Things"}}
}

func post(status int, location, bodyID string) Record {
	return Record{AttemptID: "a1", Sequence: 1, SessionRef: "s1", Method: "POST", Origin: testOrigin, RawTarget: testPost,
		Outcome: OutcomeResponseReceived, Status: status, Location: location, BodyID: bodyID}
}

func TestClassify(t *testing.T) {
	put := post(201, "/api/things/7", "")
	put.Method = "PUT"
	patch, del := put, put
	patch.Method, del.Method = "PATCH", "DELETE"
	withQuery := post(200, "", "7")
	withQuery.RawTarget = testPost + "?a=1#frag"
	absolute := post(200, "", "7")
	absolute.RawTarget = testOrigin + testPost
	otherAbsolute := post(200, "", "7")
	otherAbsolute.RawTarget = "http://other.example:3000" + testPost
	badTarget := post(200, "", "7")
	badTarget.RawTarget = "/api/../things"
	unknown, attempted, denied, notSent, future := post(201, "/api/things/7", "7"), post(201, "/api/things/7", "7"), post(201, "", ""), post(201, "", ""), post(201, "/api/things/7", "7")
	unknown.Outcome, attempted.Outcome, denied.Outcome, notSent.Outcome, future.Outcome = OutcomeUnknown, OutcomeSendAttempted, OutcomePolicyDenied, OutcomeNotSent, Outcome("later")

	cases := []struct {
		id     string
		record Record
		class  Class
		path   string
	}{
		{"CP01 201 같은 origin 절대", post(201, testOrigin+"/api/things/7", ""), ClassCreateStrong, "/api/things/7"},
		{"CP01 201 상대 경로", post(201, "/api/things/7", ""), ClassCreateStrong, "/api/things/7"},
		{"CP01 201 상대 조각", post(201, "things/7", ""), ClassCreateStrong, "/api/things/7"},
		{"CP01 201 쿼리는 뺌", post(201, "/api/things/7?x=1#f", ""), ClassCreateStrong, "/api/things/7"},
		{"CP02 201 다른 origin", post(201, "http://other.example:3000/api/things/7", ""), ClassRecordOnly, ""},
		{"CP02 201 다른 scheme", post(201, "https://target.example:3000/api/things/7", ""), ClassRecordOnly, ""},
		{"CP02 201 다른 포트", post(201, "http://target.example:3001/api/things/7", ""), ClassRecordOnly, ""},
		{"CP02 201 파싱 불가", post(201, "http://[bad", ""), ClassRecordOnly, ""},
		{"CP02 201 다른 origin이면 body_id로", post(201, "http://other.example/x/1", "7"), ClassCreateWeak, "/api/things/7"},
		{"Location 경로 이동", post(201, "/api/things/../users/7", ""), ClassRecordOnly, ""},
		{"Location 인코딩 구분자", post(201, "/api/things%2f7", ""), ClassRecordOnly, ""},
		{"Location 다른 컬렉션", post(201, "/api/users/7", ""), ClassRecordOnly, ""},
		{"Location 더 깊은 경로", post(201, "/api/things/7/items/1", ""), ClassRecordOnly, ""},
		{"Location 점 하나", post(201, "./7", ""), ClassRecordOnly, ""},
		{"Location 풀면 맞아 보이는 점 구간", post(201, "/api/other/../things/7", ""), ClassRecordOnly, ""},
		{"Location 끝 슬래시", post(201, "/api/things/", ""), ClassRecordOnly, ""},
		{"CP03 200 body_id 경로 허용", post(200, "", "7"), ClassCreateWeak, "/api/things/7"},
		{"CP03b 쿼리·fragment 뺌", withQuery, ClassCreateWeak, "/api/things/7"},
		{"절대 형식 같은 origin", absolute, ClassCreateWeak, "/api/things/7"},
		{"절대 형식 다른 origin", otherAbsolute, ClassRecordOnly, ""},
		{"대상 경로 이동", badTarget, ClassRecordOnly, ""},
		{"CP05 200 신호 없음", post(200, "", ""), ClassRecordOnly, ""},
		{"200인데 Location만", post(200, "/api/things/7", ""), ClassRecordOnly, ""},
		{"CP06 PUT", put, ClassRecordOnly, ""},
		{"CP06 PATCH", patch, ClassRecordOnly, ""},
		{"CP06 DELETE", del, ClassRecordOnly, ""},
		{"CP07 outcome_unknown", unknown, ClassRecordOnly, ""},
		{"CP07 send_attempted", attempted, ClassRecordOnly, ""},
		{"모르는 outcome", future, ClassRecordOnly, ""},
		{"CP08 policy_denied", denied, ClassNone, ""},
		{"CP08 not_sent", notSent, ClassNone, ""},
		{"CP09 body_id 슬래시", post(200, "", "7/8"), ClassRecordOnly, ""},
		{"CP09 body_id 점 둘", post(200, "", ".."), ClassRecordOnly, ""},
		{"CP09 body_id 퍼센트", post(200, "", "7%2f8"), ClassRecordOnly, ""},
		{"CP26 응답 불완전(신호 없음)", post(201, "", ""), ClassRecordOnly, ""},
		{"500", post(500, "/api/things/7", "7"), ClassRecordOnly, ""},
		{"199", post(199, "/api/things/7", "7"), ClassRecordOnly, ""},
		{"300", post(300, "/api/things/7", "7"), ClassRecordOnly, ""},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			got := Classify(c.record, testRules())
			if got.Class != c.class || got.DeletePath != c.path {
				t.Fatalf("got %s %q, want %s %q", got.Class, got.DeletePath, c.class, c.path)
			}
		})
	}
	t.Run("CP04 경로 허용 안 됨", func(t *testing.T) {
		rules := testRules()
		rules.WeakPaths = []string{"/api/others"}
		if got := Classify(post(200, "", "7"), rules); got.Class != ClassRecordOnly {
			t.Fatalf("got %s", got.Class)
		}
	})
}

func measured(tables map[string]TableDiff) StateDiff {
	return StateDiff{Measured: true, Tables: tables}
}

func TestConfirmKeepsDeletesBehindStateEvidence(t *testing.T) {
	added7 := measured(map[string]TableDiff{"Things": {Added: []int64{7}, Changed: []int64{}}})
	cases := []struct {
		id     string
		record Record
		rules  Rules
		diff   StateDiff
		action Action
		reason Reason
		rowID  int64
	}{
		{"새 자원은 지움", post(201, "/api/things/7", ""), testRules(), added7, ActionDelete, ReasonAddedSinceStart, 7},
		{"약한 신호도 근거 있으면 지움", post(200, "", "7"), testRules(), added7, ActionDelete, ReasonAddedSinceStart, 7},
		{"CP17 경로→테이블 설정 없음", post(201, "/api/things/7", ""), Rules{}, added7, ActionUndecided, ReasonNoTable, 0},
		{"CP18 상태 차이 못 잼", post(201, "/api/things/7", ""), testRules(), StateDiff{}, ActionUndecided, ReasonDiffUnavailable, 0},
		{"CP20 before에 없음", post(201, "/api/things/8", ""), testRules(), added7, ActionUndecided, ReasonNotInAdded, 8},
		{"CP21 upsert", post(200, "", "9"), testRules(), measured(map[string]TableDiff{"Things": {Added: []int64{7}, Changed: []int64{9}}}), ActionRecordOnly, ReasonExistingChanged, 9},
		{"CP22 added_ids null", post(201, "/api/things/7", ""), testRules(), measured(map[string]TableDiff{"Things": {}}), ActionUndecided, ReasonAddedUnknown, 7},
		{"CP22 테이블 없음", post(201, "/api/things/7", ""), testRules(), measured(map[string]TableDiff{}), ActionUndecided, ReasonAddedUnknown, 7},
		{"CP23 id 정수 아님", post(200, "", "abc"), testRules(), added7, ActionUndecided, ReasonNotInteger, 0},
		{"id 0", post(200, "", "0"), testRules(), measured(map[string]TableDiff{"Things": {Added: []int64{0}}}), ActionUndecided, ReasonNotInteger, 0},
		{"id 범위 초과", post(200, "", "9999999999999999999"), testRules(), added7, ActionUndecided, ReasonNotInteger, 0},
		{"신호 없음", post(200, "", ""), testRules(), added7, ActionRecordOnly, ReasonNoSignal, 0},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			steps, err := Build([]Record{c.record}, c.rules, c.diff)
			if err != nil || len(steps) != 1 {
				t.Fatalf("steps=%d err=%v", len(steps), err)
			}
			step := steps[0]
			if step.Action != c.action || step.Reason != c.reason || step.ID != c.rowID {
				t.Fatalf("got %s %s %d, want %s %s %d", step.Action, step.Reason, step.ID, c.action, c.reason, c.rowID)
			}
			if (step.DeletePath != "") != (c.action == ActionDelete) {
				t.Fatalf("삭제가 아닌데 경로가 있거나 삭제인데 경로가 없음: %q", step.DeletePath)
			}
		})
	}
}

func TestBuildOrdersByReverseSequenceAndSkipsUntouched(t *testing.T) {
	first, second, third, denied := post(201, "/api/things/1", ""), post(201, "/api/things/2", ""), post(201, "/api/things/3", ""), post(201, "", "")
	first.AttemptID, first.Sequence = "a1", 1
	second.AttemptID, second.Sequence = "a2", 5
	third.AttemptID, third.Sequence = "a3", 3
	denied.AttemptID, denied.Sequence, denied.Outcome = "a4", 9, OutcomePolicyDenied
	steps, err := Build([]Record{first, second, third, denied}, testRules(),
		measured(map[string]TableDiff{"Things": {Added: []int64{1, 2, 3}}}))
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, step := range steps {
		got = append(got, step.AttemptID)
	}
	if strings.Join(got, ",") != "a2,a3,a1" {
		t.Fatalf("CP10 순서 %v", got)
	}
}

func TestBuildPlansOneDeletePerResource(t *testing.T) {
	first, second, other := post(201, "/api/things/7", ""), post(201, "/api/things/7", ""), post(201, "/api/things/8", "")
	first.AttemptID, first.Sequence = "a1", 1
	second.AttemptID, second.Sequence = "a2", 2
	other.AttemptID, other.Sequence = "a3", 3
	rules := testRules()
	rules.TableByPath["/api/others"] = "Others"
	otherTable := post(201, "/api/others/7", "")
	otherTable.AttemptID, otherTable.Sequence, otherTable.RawTarget = "a4", 4, "/api/others"
	steps, err := Build([]Record{first, second, other, otherTable}, rules,
		measured(map[string]TableDiff{"Things": {Added: []int64{7, 8}}, "Others": {Added: []int64{7}}}))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Step{}
	for _, step := range steps {
		got[step.AttemptID] = step
	}
	if got["a2"].Action != ActionDelete || got["a2"].DeletePath != "/api/things/7" {
		t.Fatalf("나중 시도가 지워야 함: %+v", got["a2"])
	}
	if merged := got["a1"]; merged.Action != ActionMerged || merged.Reason != ReasonSameResource || merged.DeletePath != "" || merged.ID != 7 {
		t.Fatalf("앞 시도는 합쳐야 함: %+v", merged)
	}
	// 번호가 같아도 테이블이 다르면 다른 자원이다.
	if got["a3"].Action != ActionDelete || got["a4"].Action != ActionDelete {
		t.Fatalf("다른 자원을 합침: %+v %+v", got["a3"], got["a4"])
	}
}

func TestBuildRejectsAmbiguousAttempts(t *testing.T) {
	blank, one, sameAttempt, sameSequence := post(201, "", ""), post(201, "", ""), post(201, "", ""), post(201, "", "")
	blank.AttemptID = ""
	sameAttempt.Sequence = 2
	sameSequence.AttemptID = "a2"
	for name, records := range map[string][]Record{"빈 ID": {blank}, "ID 중복": {one, sameAttempt}, "순번 중복": {one, sameSequence}} {
		t.Run(name, func(t *testing.T) {
			if steps, err := Build(records, testRules(), StateDiff{}); !errors.Is(err, ErrInvalidRecords) || steps != nil {
				t.Fatalf("steps=%v err=%v", steps, err)
			}
		})
	}
}

func TestUnidentifiedWriteStillNamesItsTable(t *testing.T) {
	// 놓침 판정이 "그 테이블에 id를 못 정한 쓰기가 있었다"를 알아야 한다(CP25). 결과 불명 POST도 행을 만들었을 수 있다.
	unknown := post(0, "", "")
	unknown.Outcome = OutcomeUnknown
	for name, record := range map[string]Record{"신호 없는 200": post(200, "", ""), "결과 불명": unknown, "500": post(500, "", "")} {
		t.Run(name, func(t *testing.T) {
			steps, err := Build([]Record{record}, testRules(), measured(map[string]TableDiff{"Things": {Added: []int64{}}}))
			if err != nil || len(steps) != 1 {
				t.Fatalf("steps=%d err=%v", len(steps), err)
			}
			if steps[0].Table != "Things" || steps[0].Action != ActionRecordOnly || steps[0].Shared().ID != nil {
				t.Fatalf("got %+v", steps[0])
			}
		})
	}
}

func TestSharedStepCarriesNoRawValues(t *testing.T) {
	// CP19: 공유 기록에는 경로·Location·본문 id 원문과 세션 참조가 없다.
	record := post(201, "/api/secret-looking-path/7", "")
	record.RawTarget = "/api/secret-looking-path?token=synthetic-query-value"
	rules := Rules{TableByPath: map[string]string{"/api/secret-looking-path": "Things"}}
	steps, err := Build([]Record{record}, rules, measured(map[string]TableDiff{"Things": {Added: []int64{7}}}))
	if err != nil {
		t.Fatal(err)
	}
	if steps[0].Action != ActionDelete || steps[0].DeletePath != "/api/secret-looking-path/7" {
		t.Fatalf("got %+v", steps[0])
	}
	raw, err := json.Marshal(steps[0].Shared())
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"secret-looking-path", "synthetic-query-value", "/api", "s1", testOrigin} {
		if strings.Contains(string(raw), value) {
			t.Fatalf("공유 기록에 %q: %s", value, raw)
		}
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 7 || decoded["id"] != float64(7) || decoded["table"] != "Things" {
		t.Fatalf("got %s", raw)
	}
}
