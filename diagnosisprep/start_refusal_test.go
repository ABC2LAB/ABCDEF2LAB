package diagnosisprep

import (
	"encoding/json"
	"testing"
)

// busyReasonFor는 해제 판단(view)만 보고 사유를 고른다. 끝 상태를 게시했지만 실행이 아직 반환되지 않은 작업(job_running)은
// 마감 중이고, 해제 기준에 닿은 중지는 해제 가능이다.
func TestBusyReasonForReleaseView(t *testing.T) {
	for _, tc := range []struct {
		view ReleaseView
		want string
	}{
		{ReleaseView{Refusal: ReleaseJobRunning, JobState: Running}, BusyRunActive},
		{ReleaseView{Refusal: ReleaseJobRunning, JobState: Preparing}, BusyRunActive},
		{ReleaseView{Refusal: ReleaseJobRunning, JobState: Completed}, BusyRunStopping},
		{ReleaseView{Refusal: ReleaseJobRunning, JobState: Failed}, BusyRunStopping},
		{ReleaseView{Refusal: ReleaseNotQuiet, JobState: Stopping}, BusyRunStopping},
		{ReleaseView{Refusal: ReleaseUnsupported, JobState: Stopping}, BusyRunStopping},
		{ReleaseView{Refusal: ReleaseNotQuiet, JobState: Unknown}, BusyClosureUnconfirmed},
		{ReleaseView{Refusal: ReleaseUnsupported, JobState: Unknown}, BusyRecoveryUnsupported},
		{ReleaseView{Eligible: true, JobState: Stopping}, BusyReleaseAvailable},
		{ReleaseView{Eligible: true, JobState: Unknown}, BusyReleaseAvailable},
		{ReleaseView{Refusal: ReleasePending, JobState: Unknown}, BusyReleasePendingRestart},
		{ReleaseView{Refusal: ReleaseOtherActive, JobState: Running}, BusyMultipleBlockers},
		{ReleaseView{Refusal: ReleaseNotStuck}, ""},
	} {
		if got := busyReasonFor(tc.view); got != tc.want {
			t.Fatalf("%+v: got %q want %q", tc.view, got, tc.want)
		}
		if tc.want != "" && !ValidBusyReason(tc.want) {
			t.Fatal("closed list missing", tc.want)
		}
	}
}

// 보호 IPC의 엄격한 JSON 디코더도 새 칸을 읽고, 구 서비스의 busy 응답은 계속 받는다.
func TestBusyReasonStrictIPCDecode(t *testing.T) {
	req := Request{Version: Version, RequestID: "request", Principal: "owner", Command: Start,
		JobID: "prep-a", IdempotencyKey: "start-a", ExpectedRevision: 1}
	for _, reason := range []string{BusyRunActive, ""} {
		raw, err := json.Marshal(Response{Version: Version, RequestID: req.RequestID, Error: "busy", BusyReason: reason})
		if err != nil {
			t.Fatal(err)
		}
		var decoded Response
		if err := strictDecode(raw, &decoded); err != nil {
			t.Fatal("엄격한 IPC 디코딩 실패:", err)
		}
		if err := ValidateResponse(req, decoded); err != nil || decoded.BusyReason != reason {
			t.Fatalf("IPC 응답 계약: reason=%q decoded=%#v err=%v", reason, decoded, err)
		}
	}
}
