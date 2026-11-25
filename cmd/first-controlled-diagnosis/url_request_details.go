package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
)

// 작업대 요청 상세와 전체 기록(2026-11-25 사용자 지시, 재상님 요구사항). 검증한 E② 저널만 읽는다. 저널에는 헤더·본문·
// 크기·경로 원문이 없으므로(저장하지 않음) 그 칸은 이유 코드로 내린다. 경로는 시도별 화면용 경로 틀(가린 값)이다.
// 저널 형식은 바꾸지 않는다(합성 확인 기준선·동결, 사용자 결정). 크기·헤더 이름은 측정 뒤 후보다(개발 계획).

const (
	e2RequestsViewFormat = "abc2lab.e2-flow-requests/v1"
	e2RequestViewFormat  = "abc2lab.e2-flow-request/v1"
	// 전체 기록 한 쪽의 기본·최대 줄 수.
	e2RequestsDefaultLimit = 100
	e2RequestsMaxLimit     = 200
)

// 요청 줄의 종류. 모델 진단 요청은 흐름 Event와 같은 request다.
const (
	e2RequestKindModel        = "request"
	e2RequestKindConfirmation = "confirmation"
	e2RequestKindUnclassified = "unclassified"
)

// 상세 칸·조회 실패의 이유 코드.
const (
	// e2FieldNotStored는 헤더·본문처럼 E② 저널이 저장하지 않는 칸이다.
	e2FieldNotStored = "not_stored"
	// e2RequestNotFound는 이 회차 저널에 그 호출·시도가 없다는 뜻이다.
	e2RequestNotFound = "not_found"
	// e2RequestNotJournaled는 이 회차에 E② 저널이 아직 없다는 뜻이다(실행 전이거나 전송 통제를 거치지 않은 회차).
	e2RequestNotJournaled = "not_journaled"
	// e2RequestJournalInvalid는 저널이 있지만 실행 감사 키로 검증하지 못했다는 뜻이다(내용을 보이지 않는다).
	e2RequestJournalInvalid = "journal_invalid"
)

// 전체 기록·상세의 화면 이름(liveDataView 값).
const (
	e2RequestsView = "e2-flow-requests"
	e2RequestView  = "e2-flow-request"
)

// e2RequestRow는 전체 기록의 줄이다. 흐름 Event 줄과 같은 모양(시각·종류·메서드·경로·코드·식별자)이다.
type e2RequestRow struct {
	At        time.Time `json:"at"`
	Kind      string    `json:"kind"`
	Method    string    `json:"method,omitempty"`
	Path      string    `json:"path,omitempty"`
	Status    int       `json:"status,omitempty"`
	CallRef   string    `json:"call_ref"`
	AttemptID string    `json:"attempt_id"`
	Index     uint64    `json:"index"`
}

type e2RequestsPage struct {
	Format string         `json:"format"`
	Rows   []e2RequestRow `json:"rows"`
	// Total은 이 회차 저널에서 요청을 보낸 시도 전체 수다(e2-flow target_requests의 model+login+confirmation+unclassified와
	// 같다. 탐색 요청은 E② 저널에 없어 여기 없다).
	Total int `json:"total"`
	// NextCursor는 다음 쪽을 받을 cursor다. 마지막 쪽이면 없다.
	NextCursor *uint64 `json:"next_cursor,omitempty"`
}

type e2RequestField struct {
	State string `json:"state"`
}

type e2RequestDetail struct {
	Format            string         `json:"format"`
	CallRef           string         `json:"call_ref"`
	AttemptID         string         `json:"attempt_id"`
	Kind              string         `json:"kind"`
	Index             uint64         `json:"index"`
	At                time.Time      `json:"at"`
	Method            string         `json:"method,omitempty"`
	PathPattern       string         `json:"path_pattern,omitempty"`
	RuleRef           string         `json:"rule_ref,omitempty"`
	Decision          string         `json:"decision"`
	Reason            string         `json:"reason"`
	StatusCode        int            `json:"status_code,omitempty"`
	ForwardAttempted  bool           `json:"forward_attempted"`
	RequestWritten    bool           `json:"request_written"`
	ResponseReceived  bool           `json:"response_received"`
	ResponseComplete  bool           `json:"response_complete"`
	ResponseTruncated bool           `json:"response_truncated"`
	RequestHeaders    e2RequestField `json:"request_headers"`
	RequestBody       e2RequestField `json:"request_body"`
	ResponseHeaders   e2RequestField `json:"response_headers"`
	ResponseBody      e2RequestField `json:"response_body"`
}

// requestKind는 시도의 귀속이다. 프로그램 로그인은 프로그램 호출 이름(auth_prep·refresh_session)을 그대로 쓴다.
func requestKind(e e2proxy.Event) string {
	switch e.Program {
	case "":
		if e.E3Audit != nil {
			return e2RequestKindConfirmation
		}
		return e2RequestKindModel
	case e2proxy.ProgramAuthPrep, e2proxy.ProgramRefreshSession:
		return string(e.Program)
	case e2proxy.ProgramE3Identity, e2proxy.ProgramE3Create, e2proxy.ProgramE3Read:
		return e2RequestKindConfirmation
	}
	// 이 판이 모르는 프로그램 호출 종류는 다른 종류로 바꾸지 않는다(e2-flow target_requests의 unclassified).
	return e2RequestKindUnclassified
}

// sentRequests는 시도마다 마지막 결과 행 하나를 남기고, 요청을 보낸 시도만 그 행의 저널 번호(Index) 오름차순으로 준다.
// 쪽 넘김 cursor가 이 번호라서 순서도 같은 번호로 정한다. 같은 시도의 결과 행이 다른 시도 사이에 끼어 있어도(A·B·A) 줄이
// 빠지거나 두 번 나오지 않는다(#931 이동찬 P2).
func sentRequests(events []e2proxy.Event) []e2proxy.Event {
	last := map[string]e2proxy.Event{}
	for _, e := range events {
		if e.Stage == "result" && e.AttemptID != "" {
			last[e.AttemptID] = e
		}
	}
	out := make([]e2proxy.Event, 0, len(last))
	for _, e := range last {
		if e.RequestWritten {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

func e2RequestsPageOf(events []e2proxy.Event, patterns map[string]string, cursor uint64, limit int) e2RequestsPage {
	sent := sentRequests(events)
	page := e2RequestsPage{Format: e2RequestsViewFormat, Rows: []e2RequestRow{}, Total: len(sent)}
	for _, e := range sent {
		if e.Index <= cursor {
			continue
		}
		if len(page.Rows) == limit {
			next := page.Rows[len(page.Rows)-1].Index
			page.NextCursor = &next
			break
		}
		page.Rows = append(page.Rows, e2RequestRow{At: e.At, Kind: requestKind(e), Method: e.Method, Path: patterns[e.AttemptID], Status: e.StatusCode, CallRef: e.CallRef, AttemptID: e.AttemptID, Index: e.Index})
	}
	return page
}

// e2RequestDetailOf는 그 호출·시도의 마지막 결과 행(없으면 마지막 의도 행)으로 상세를 만든다.
func e2RequestDetailOf(events []e2proxy.Event, patterns map[string]string, call, attempt string) (e2RequestDetail, bool) {
	var found *e2proxy.Event
	for i := range events {
		e := events[i]
		if e.CallRef != call || e.AttemptID != attempt || e.Stage != "result" && e.Stage != "intent" {
			continue
		}
		if found == nil || e.Stage == "result" || found.Stage != "result" {
			found = &events[i]
		}
	}
	if found == nil {
		return e2RequestDetail{}, false
	}
	e := *found
	notStored := e2RequestField{State: e2FieldNotStored}
	return e2RequestDetail{Format: e2RequestViewFormat, CallRef: e.CallRef, AttemptID: e.AttemptID, Kind: requestKind(e), Index: e.Index, At: e.At,
		Method: e.Method, PathPattern: patterns[e.AttemptID], RuleRef: e.RuleRef, Decision: e.Decision, Reason: string(e.Reason), StatusCode: e.StatusCode,
		ForwardAttempted: e.ForwardAttempted, RequestWritten: e.RequestWritten, ResponseReceived: e.ResponseReceived, ResponseComplete: e.ResponseComplete,
		ResponseTruncated: e.ResponseTruncated, RequestHeaders: notStored, RequestBody: notStored, ResponseHeaders: notStored, ResponseBody: notStored}, true
}

// e2RequestsQuery는 전체 기록의 cursor·limit을 읽는다. 다른 키·중복·형식이 틀린 값은 거부한다.
func e2RequestsQuery(raw string) (cursor uint64, limit int, ok bool) {
	query, err := url.ParseQuery(raw)
	if err != nil {
		return 0, 0, false
	}
	limit = e2RequestsDefaultLimit
	for key, values := range query {
		if len(values) != 1 {
			return 0, 0, false
		}
		n, err := strconv.ParseUint(values[0], 10, 64)
		if err != nil || strconv.FormatUint(n, 10) != values[0] {
			return 0, 0, false
		}
		switch key {
		case "cursor":
			cursor = n
		case "limit":
			if n < 1 || n > e2RequestsMaxLimit {
				return 0, 0, false
			}
			limit = int(n)
		default:
			return 0, 0, false
		}
	}
	return cursor, limit, true
}

// e2RequestPath는 상세 경로의 호출·시도 식별자다(/api/tasks/{id}/e2-flow/requests/{call_ref}/{attempt_id}).
func e2RequestPath(path string) (call, attempt string, ok bool) {
	tail, ok := strings.CutPrefix(path, "/api/tasks/")
	if !ok {
		return "", "", false
	}
	parts := strings.Split(tail, "/")
	if len(parts) != 5 || parts[1] != "e2-flow" || parts[2] != "requests" || !managementReference.MatchString(parts[3]) || !attemptRefValid(parts[4]) {
		return "", "", false
	}
	return parts[3], parts[4], true
}

func writeE2RequestError(w http.ResponseWriter, status int, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{"reason": reason}); err != nil {
		log.Print("[first-diagnosis] 요청 상세 오류 응답 쓰기 실패")
	}
}

// serveE2Requests는 전체 기록(view e2-flow-requests)과 요청 상세(view e2-flow-request)다. 실행 결속·정책 확인은 e2-flow와 같다.
// 끝난 점검도 회차 디렉터리가 있는 동안 읽힌다.
func serveE2Requests(w http.ResponseWriter, r *http.Request, b *runBinding, view string) {
	var cursor uint64
	limit := 0
	var call, attempt string
	ok := false
	if view == e2RequestsView {
		cursor, limit, ok = e2RequestsQuery(r.URL.RawQuery)
	} else {
		call, attempt, ok = e2RequestPath(r.URL.Path)
		ok = ok && r.URL.RawQuery == ""
	}
	if !ok {
		http.Error(w, "조회 조건이 맞지 않습니다", http.StatusBadRequest)
		return
	}
	events, verified, err := e2FlowEvents(r, b)
	switch {
	case errors.Is(err, errRunNotBound):
		writeE2RequestError(w, http.StatusNotFound, e2RequestNotJournaled)
		return
	case err == nil && !verified:
		writeE2RequestError(w, http.StatusConflict, e2RequestJournalInvalid)
		return
	case err != nil:
		http.Error(w, "전송 통제 기록을 읽지 못했습니다", http.StatusServiceUnavailable)
		return
	}
	patterns := attemptPatterns(b)
	var out any
	if view == e2RequestsView {
		out = e2RequestsPageOf(events, patterns, cursor, limit)
	} else {
		detail, found := e2RequestDetailOf(events, patterns, call, attempt)
		if !found {
			writeE2RequestError(w, http.StatusNotFound, e2RequestNotFound)
			return
		}
		out = detail
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		logLiveWriteError(r, view, err)
	}
}
