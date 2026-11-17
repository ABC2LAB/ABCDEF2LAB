package diagnosisprep

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

const MaxRunPageSize = 50
const DefaultRunPageSize = 25
const MaxRunPageBytes = 24 << 10
const MaxRunCursorBytes = 512

type RunListRequest struct {
	Limit  int    `json:"limit,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

// RunSummary는 보호 Job의 비민감 projection이다. 기록하지 않은 시작·종료 시각이나 원문 URL을 만들지 않는다.
// State는 저장된 실행 상태이고 ApprovalStatus는 별도다. 목록 자체는 재개·송신 권위가 아니다.
type RunSummary struct {
	ID              string `json:"id"`
	Revision        uint64 `json:"revision"`
	DisplayName     string `json:"display_name,omitempty"`
	State           State  `json:"state"`
	Phase           Phase  `json:"phase"`
	ReasonCode      string `json:"reason_code,omitempty"`
	TaskID          string `json:"task_id,omitempty"`
	Started         bool   `json:"started"`
	DrainComplete   bool   `json:"drain_complete"`
	ResultReady     bool   `json:"result_ready"`
	ReportAvailable bool   `json:"report_available"`
	ApprovalStatus  string `json:"approval_status"`
	ActionRequired  bool   `json:"action_required"`
}
type RunPage struct {
	Runs       []RunSummary `json:"runs"`
	Snapshot   string       `json:"snapshot"`
	Limit      int          `json:"limit"`
	More       bool         `json:"more"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

// RunApprovalReader는 기존 승인 기록의 현재 상태만 읽는다. DNS·HTTP·모델·권위 발급·저널 변경을 하지 않는다.
// 구현이 없거나 상태를 확정할 수 없으면 not_revalidated다. 이 응답도 실행 허가는 아니다.
type RunApprovalReader interface {
	ReadRunApprovals(principal string, jobs []Job) map[string]string
}

type runCursor struct {
	Snapshot string `json:"snapshot"`
	After    string `json:"after"`
	Limit    int    `json:"limit"`
}

func decodeRunCursor(value string) (runCursor, bool) {
	var cursor runCursor
	if len(value) == 0 || len(value) > MaxRunCursorBytes || strings.ContainsAny(value, "=+/") {
		return cursor, false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != value || strictDecode(raw, &cursor) != nil || !digestPattern.MatchString(cursor.Snapshot) || !reference.MatchString(cursor.After) || cursor.Limit < 1 || cursor.Limit > MaxRunPageSize {
		return runCursor{}, false
	}
	return cursor, true
}
func encodeRunCursor(cursor runCursor) string {
	raw, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(raw)
}
func validRunListRequest(r *RunListRequest) bool {
	if r == nil || r.Limit < 0 || r.Limit > MaxRunPageSize {
		return false
	}
	if r.Cursor != "" {
		c, ok := decodeRunCursor(r.Cursor)
		return ok && (r.Limit == 0 || r.Limit == c.Limit)
	}
	return true
}
func validApprovalStatus(s string) bool {
	switch s {
	case "not_revalidated", "expired", "revoked", "changed", "unavailable":
		return true
	}
	return false
}
func runActive(s State) bool { return s == Preparing || s == Running || s == Stopping }
func runBefore(a, b RunSummary) bool {
	if runActive(a.State) != runActive(b.State) {
		return runActive(a.State)
	}
	return a.ID > b.ID
}
func validRunSummary(run RunSummary) bool {
	// 상태/단계 조합은 원본 Job을 검사한 서비스가 만든다. 공개 projection도 고정된 상태·단계만 받는다.
	switch run.State {
	case AwaitingApproval, Ready, Preparing, Running, Stopping, Completed, Cancelled, Failed, Unknown:
	default:
		return false
	}
	switch run.Phase {
	case DraftPhase, ScopeCheck, TaskCreation, Planning, Authority, Gateway, ExecutionPhase, Closure, Report:
	default:
		return false
	}
	if run.Started && run.TaskID == "" || run.ResultReady && run.TaskID == "" {
		return false
	}
	// 기존 Job의 known fact 계약을 projection에서도 보존한다. 결과 불명을 완료로 바꾸지 않는다.
	switch run.State {
	case AwaitingApproval, Ready:
		if run.Started || run.DrainComplete || run.ResultReady || run.TaskID != "" {
			return false
		}
	case Preparing:
		if run.Started {
			return false
		}
	case Running:
		if !run.Started || run.DrainComplete || run.ResultReady {
			return false
		}
	case Stopping:
		if run.ResultReady {
			return false
		}
	case Completed:
		if !run.Started || !run.DrainComplete || !run.ResultReady {
			return false
		}
	case Cancelled, Failed:
		if !run.DrainComplete || run.Started && !run.ResultReady {
			return false
		}
	}
	return reference.MatchString(run.ID) && run.Revision > 0 && (run.DisplayName == "" || validText(run.DisplayName, 160)) && (run.ReasonCode == "" || reasonPattern.MatchString(run.ReasonCode)) && (run.TaskID == "" || validTask(run.TaskID)) && validApprovalStatus(run.ApprovalStatus) && run.ReportAvailable == (run.ResultReady && run.TaskID != "") && (!runActive(run.State) || run.ActionRequired)
}
func validRunPage(req Request, page *RunPage) bool {
	if page == nil || page.Runs == nil || len(page.Runs) > MaxRunPageSize || !digestPattern.MatchString(page.Snapshot) || page.Limit < 1 || page.Limit > MaxRunPageSize || len(page.Runs) > page.Limit || page.More != (page.NextCursor != "") {
		return false
	}
	raw, err := json.Marshal(page)
	if err != nil || len(raw) > MaxRunPageBytes {
		return false
	}
	if req.Runs.Limit != 0 && req.Runs.Limit != page.Limit {
		return false
	}
	cursor, hasCursor := decodeRunCursor(req.Runs.Cursor)
	if req.Runs.Cursor != "" && (!hasCursor || cursor.Snapshot != page.Snapshot || cursor.Limit != page.Limit) {
		return false
	}
	for i, run := range page.Runs {
		if !validRunSummary(run) || i > 0 && !runBefore(page.Runs[i-1], run) || hasCursor && run.ID == cursor.After {
			return false
		}
	}
	if page.More {
		next, ok := decodeRunCursor(page.NextCursor)
		if !ok || len(page.Runs) == 0 || next.After != page.Runs[len(page.Runs)-1].ID || next.Snapshot != page.Snapshot || next.Limit != page.Limit {
			return false
		}
	}
	return true
}
