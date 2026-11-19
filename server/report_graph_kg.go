package server

// KG 조회 v1 선택자와 오류 봉투(docs/contracts/kg-query-v1.md §3·§5.4).
// 경로는 기존 coverage-graph를 그대로 쓰고 query `view=kg-v1` 하나로만 고른다(D01). 무인자 응답은 바뀌지 않는다.

import (
	"net/http"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// kgV1Query는 받는 query의 정확한 원문이다. 다른 query·중복 키·인코딩 변형은 400이다.
const kgV1Query = "view=kg-v1"

// reportGraphKGView는 정규 v1 조회인지다(메서드·경로·task 형식까지 reportGraphRequestTask가 본다).
func reportGraphKGView(r *http.Request) bool {
	_, ok := reportGraphRequestTask(r)
	return ok && r.URL.RawQuery == kgV1Query
}

// kgRequested는 오류를 kg 봉투로 낼지다. coverage-graph에 query가 붙은 요청은 v1을 뜻한 것으로 본다 —
// 무인자 조회는 query를 받은 적이 없으므로 기존 클라이언트의 오류 본문은 바뀌지 않는다.
func kgRequested(r *http.Request) bool {
	return strings.HasSuffix(r.URL.Path, "/coverage-graph") && (r.URL.RawQuery != "" || r.URL.ForceQuery)
}

type kgErrorCode struct {
	Code      string `json:"code"`
	Retryable bool   `json:"retryable"`
}

// kgErrors는 §5.4 HTTP ↔ 고정 code 표다. retryable은 재조회 안내이지 진단 재실행 권한이 아니다.
var kgErrors = map[int]kgErrorCode{
	http.StatusBadRequest:            {"kg_request_invalid", false},
	http.StatusUnauthorized:          {"kg_auth_required", false},
	http.StatusForbidden:             {"kg_task_forbidden", false},
	http.StatusNotFound:              {"kg_task_not_found", false},
	http.StatusMethodNotAllowed:      {"kg_method_not_allowed", false},
	http.StatusConflict:              {"kg_scope_unavailable", false},
	http.StatusRequestEntityTooLarge: {"kg_graph_too_large", false},
	http.StatusServiceUnavailable:    {"kg_store_unavailable", true},
}

// writeKGError는 SQL·경로·토큰 등 상세 없이 고정 code만 낸다. 표에 없는 상태는 503으로 닫는다.
func writeKGError(w http.ResponseWriter, status int) {
	e, ok := kgErrors[status]
	if !ok {
		status, e = http.StatusServiceUnavailable, kgErrors[http.StatusServiceUnavailable]
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]any{"version": db.KGQueryVersion, "error": e})
}

func kgContextTasks(b reportReadBinding) []db.KGContextTask {
	out := make([]db.KGContextTask, 0, len(b.Tasks))
	for _, t := range b.Tasks {
		out = append(out, db.KGContextTask{ID: t.ID, Exploration: t.Exploration})
	}
	return out
}
