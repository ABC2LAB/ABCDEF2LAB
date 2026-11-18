//go:build linux

package main

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/Autumn-27/artex/server"
)

// checkE3ShareIssued는 실제로 발급한 실행 정책의 E③ 확정 단계 몫을 본다(사용자 결정 2026-11-18). 이 판은 정책 선언을
// 실행에 연결하지 않아 확정 단계가 꺼져 있으므로 몫은 0이고, 0도 늘 적는다. 모델 단계 요청 상한은 설치 템플릿 그대로다.
func checkE3ShareIssued(t *testing.T, raw []byte, template server.E2HTTPFilePolicy) {
	t.Helper()
	var policy server.E2HTTPFilePolicy
	if json.Unmarshal(raw, &policy) != nil {
		t.Fatal("실행 정책 JSON")
	}
	l := policy.Limits
	if l.E3ConfirmationRequests == nil || *l.E3ConfirmationRequests != 0 || l.E3ConfirmationTimeoutMS == nil || *l.E3ConfirmationTimeoutMS != 0 {
		t.Fatal("꺼진 회차의 확정 몫이 0으로 적히지 않음")
	}
	if policy.MaxRequests != template.MaxRequests || template.Limits.E3ConfirmationRequests != nil || template.Limits.E3ConfirmationTimeoutMS != nil {
		t.Fatal("모델 단계 요청 상한이 바뀌었거나 공유 템플릿에 몫 칸이 섞임")
	}
}

// checkE3ShareDocument는 검증을 통과하는 실행 정책 문서(raw, 몫 칸 없음)에 몫 칸을 넣어 문서 검사·지문을 본다.
func checkE3ShareDocument(t *testing.T, raw []byte) {
	t.Helper()
	// 요청 상한을 제품 기본(400)으로 맞춘 같은 문서에서 본다(시험 설치의 작은 상한이면 1/4 몫이 0이 된다).
	const runRequests = 400
	edit := func(change func(limits map[string]any)) []byte {
		var doc map[string]any
		if json.Unmarshal(raw, &doc) != nil {
			t.Fatal("실행 정책 JSON")
		}
		doc["max_requests"] = runRequests
		change(doc["limits"].(map[string]any))
		out, _ := json.Marshal(doc)
		return out
	}
	raw = edit(func(map[string]any) {})
	_, absent, err := server.ValidateE2HTTPRunPolicyDocument(raw)
	if err != nil {
		t.Fatal(err)
	}
	// 칸이 없는 정책(이전 회차)도 받지만 0과 지문이 다르다: 러너가 "칸 없음"과 0을 구분한다.
	zeroDoc := edit(func(l map[string]any) { l["e3_confirmation_requests"] = 0; l["e3_confirmation_timeout_ms"] = 0 })
	_, zero, err := server.ValidateE2HTTPRunPolicyDocument(zeroDoc)
	if err != nil || zero == absent {
		t.Fatal("확정 몫 칸 없음과 0의 지문이 같음", err)
	}
	quarter := uint64(runRequests / server.E3ConfirmationShareDivisor)
	_, positive, err := server.ValidateE2HTTPRunPolicyDocument(edit(func(l map[string]any) {
		l["e3_confirmation_requests"] = quarter
		l["e3_confirmation_timeout_ms"] = 1000
	}))
	if err != nil || positive == zero || positive == absent {
		t.Fatal("1/4 이하 확정 몫 거부 또는 지문 구분 실패", err)
	}
	for name, change := range map[string]func(l map[string]any){
		"requests only":      func(l map[string]any) { l["e3_confirmation_requests"] = 0 },
		"timeout only":       func(l map[string]any) { l["e3_confirmation_timeout_ms"] = 0 },
		"requests zero time": func(l map[string]any) { l["e3_confirmation_requests"] = 0; l["e3_confirmation_timeout_ms"] = 1000 },
		"time zero requests": func(l map[string]any) { l["e3_confirmation_requests"] = 1; l["e3_confirmation_timeout_ms"] = 0 },
		"negative time":      func(l map[string]any) { l["e3_confirmation_requests"] = 1; l["e3_confirmation_timeout_ms"] = -1 },
		"over quarter": func(l map[string]any) {
			l["e3_confirmation_requests"] = quarter + 1
			l["e3_confirmation_timeout_ms"] = 1000
		},
		"null requests": func(l map[string]any) { l["e3_confirmation_requests"] = nil; l["e3_confirmation_timeout_ms"] = 0 },
	} {
		if _, _, err := server.ValidateE2HTTPRunPolicyDocument(edit(change)); err == nil {
			t.Fatal("잘못된 확정 몫 수용:", name)
		}
	}
}

// 통제 정책 화면 투영은 확정 몫 칸을 실행 정책 그대로 싣는다: 칸 없는 이전 회차는 빠지고, 0(꺼짐)은 0으로 나온다.
func TestControlPolicyViewE3ConfirmationShare(t *testing.T) {
	runsDir, _ := liveRunFixture(t, nil)
	_, out := liveGet(t, runsDir, "/api/tasks/9/control-policy")
	limits := out["limits"].(map[string]any)
	if _, ok := limits["e3_confirmation_requests"]; ok {
		t.Fatal("칸 없는 이전 회차에 확정 몫을 채움")
	}
	if _, ok := limits["e3_confirmation_timeout_ms"]; ok {
		t.Fatal("칸 없는 이전 회차에 확정 시간 몫을 채움")
	}
	runsDir, _ = liveRunFixtureWith(t, nil, func(p *server.E2HTTPFilePolicy) {
		requests, timeout := uint64(0), int64(0)
		p.Limits.E3ConfirmationRequests, p.Limits.E3ConfirmationTimeoutMS = &requests, &timeout
	})
	w, out := liveGet(t, runsDir, "/api/tasks/9/control-policy")
	limits, _ = out["limits"].(map[string]any)
	if w.Code != 200 || limits == nil || limits["e3_confirmation_requests"] != float64(0) || limits["e3_confirmation_timeout_ms"] != float64(0) {
		t.Fatal("꺼진 회차의 확정 몫 0이 화면 투영에서 빠짐", w.Code, w.Body.String())
	}
}

// e3OffViewFixture는 E③ 확정 단계가 꺼진 제품 회차의 control-policy 보기 응답이다(회차 시각만 자리표시). 평가 러너 시험
// (scripts/eval/tests/test_runner_split_execute.py)이 같은 파일을 러너 checked_control_policy·check_official_e3_share에
// 넣는다. 제품 보기와 러너 검사를 묶는 고리다(#716 이동찬 리뷰).
const e3OffViewFixture = "testdata/control-policy-view-e3-off.json"

func TestControlPolicyViewE3OffMatchesRunnerFixture(t *testing.T) {
	runsDir, _ := liveRunFixtureWith(t, nil, func(p *server.E2HTTPFilePolicy) {
		*p = withE3ConfirmationShare(*p, int64(urlMaxRunSeconds), 0) // 실행 발급(issue)과 같은 함수
	})
	w, got := liveGet(t, runsDir, "/api/tasks/9/control-policy")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	raw, err := os.ReadFile(e3OffViewFixture)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	for _, view := range []map[string]any{got, want} {
		view["valid_from"], view["valid_until"] = "<회차 시각>", "<회차 시각>"
	}
	if !reflect.DeepEqual(got, want) {
		body, _ := json.MarshalIndent(got, "", "  ")
		t.Fatalf("제품 control-policy 보기가 러너 고정 파일과 다름:\n%s", body)
	}
}
