package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/loginattempt"
	"github.com/Autumn-27/artex/server"
)

// maxRunLoginAttempts는 작업대가 읽는 로그인 시도 기록 파일의 상한이다(표시용 패턴 파일과 같은 크기).
const maxRunLoginAttempts = maxRunAttemptPatterns

// 화면에 상세 사유로 주는 로그인 거절(docs/contracts/workbench-live-data.md §4.2). 로그인 시도 판정기(loginattempt)가 보내기
// 전에 막은 고정 사유만이다. 다른 경계(범위·메서드·예산 등)가 막아 판정기가 not_sent로 남긴 행은 여기 없다.
var workbenchLoginRefusals = map[string]bool{
	loginattempt.RefusalExhausted:          true,
	loginattempt.RefusalHistoryUnavailable: true,
	loginattempt.RefusalPending:            true,
	loginattempt.RefusalContextInUse:       true,
}

// loginRefusals는 이 회차 진단 단계 로그인 시도 기록에서 판정기가 막은 행을 E② 시도 참조(call_ref/attempt_id)별로 모은다.
// 결속은 행의 transport_ref(판정기가 그 E② 영수증의 call_ref·attempt_id로 남긴 값)와의 정확한 일치뿐이다. 경로·시각으로
// 잇지 않는다. 같은 참조가 두 번 이상 나오면 어느 쪽인지 모르므로 뺀다. 파일이 없거나 못 읽으면 빈 결과다(사유를 만들지 않음).
// 기록은 감사 기록이 아니어서, 검증된 E② 사건 중 모델의 POST 거부와 일치하는 참조만 받는다.
func loginRefusals(b *runBinding, events []e2proxy.Event) map[string]string {
	out := map[string]string{}
	runRef := eventsRunRef(events)
	if runRef == "" {
		return out
	}
	eligible := map[string]bool{}
	for _, e := range events {
		if e.Stage == "result" && e.Program == "" && e.Method == http.MethodPost && e.Decision == "denied" &&
			e.Reason == e2proxy.PolicyDenied && !e.ForwardAttempted {
			eligible[e.CallRef+"/"+e.AttemptID] = true
		}
	}
	if len(eligible) == 0 {
		return out
	}
	raw, err := readRunFile(b.root, "state/"+server.E2HTTPLoginAttemptsFile, maxRunLoginAttempts)
	if err != nil {
		return out
	}
	ambiguous := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		var row loginattempt.Row
		// 판정기는 행을 json.Marshal로 쓴다(빈 목록이 null). 서버 판독기(readLoginAttemptRows)와 같이 읽되 모르는 칸은 거부한다.
		decoder := json.NewDecoder(strings.NewReader(line))
		decoder.DisallowUnknownFields()
		if line == "" || decoder.Decode(&row) != nil || decoder.Decode(new(any)) != io.EOF ||
			row.Schema != loginattempt.Schema || row.RunRef != runRef ||
			row.Stage != loginattempt.StageDiagnosis || row.Actor != loginattempt.ActorModel || row.Method != http.MethodPost ||
			row.Transport != loginattempt.TransportNotSent || row.Outcome != loginattempt.OutcomeNotSent || !workbenchLoginRefusals[row.Refusal] {
			continue
		}
		call, attempt, ok := strings.Cut(row.TransportRef, "/")
		if !ok || !managementReference.MatchString(call) || !attemptRefValid(attempt) || !eligible[row.TransportRef] {
			continue
		}
		if _, seen := out[row.TransportRef]; seen || ambiguous[row.TransportRef] {
			delete(out, row.TransportRef)
			ambiguous[row.TransportRef] = true
			continue
		}
		out[row.TransportRef] = row.Refusal
	}
	return out
}

// eventsRunRef는 검증한 E② 사건의 회차 참조다. 사건마다 다르면(있을 수 없는 저널) 비운다.
func eventsRunRef(events []e2proxy.Event) string {
	ref := ""
	for _, e := range events {
		if e.Scope.RunRef == "" {
			continue
		}
		if ref != "" && ref != e.Scope.RunRef {
			return ""
		}
		ref = e.Scope.RunRef
	}
	return ref
}

// loginRefusalFor는 E② 시도가 실제로 보내지 않은 정책 거부(policy_denied)일 때만 그 시도의 로그인 거절 사유를 준다.
func loginRefusalFor(refusals map[string]string, callRef, attemptID, decision string, reason e2proxy.Reason, forwarded bool) string {
	if decision != "denied" || reason != e2proxy.PolicyDenied || forwarded {
		return ""
	}
	return refusals[callRef+"/"+attemptID]
}

// withLoginRefusals는 흐름 시도 행에 로그인 거절 사유를 붙인다(결과 행만).
func withLoginRefusals(view e2FlowView, refusals map[string]string) e2FlowView {
	if len(refusals) == 0 {
		return view
	}
	for i := range view.Calls {
		for j := range view.Calls[i].Attempts {
			a := &view.Calls[i].Attempts[j]
			if a.Stage == "result" {
				a.LoginRefusal = loginRefusalFor(refusals, view.Calls[i].CallRef, a.AttemptID, a.Decision, e2proxy.Reason(a.Reason), a.ForwardAttempted)
			}
		}
	}
	return view
}
