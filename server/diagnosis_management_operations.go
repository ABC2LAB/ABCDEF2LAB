package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func canonicalNatural(s string) bool {
	n, err := strconv.ParseInt(s, 10, 64)
	return err == nil && n >= 0 && strconv.FormatInt(n, 10) == s
}

// 명시적 task 없이는 Manager.ResolveTask의 현재 작업 기본값에 도달하지 않는다.
func managementOperationsTask(r *http.Request) (string, bool) {
	if firstDiagnosisControlRoute(r) && r.URL.RawQuery == "" {
		tail := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/tasks/"), "/control")
		return tail, canonicalNatural(tail) && tail != "0"
	}
	if r.Method != http.MethodGet {
		return "", false
	}
	allowed := map[string]bool{"task": true}
	switch r.URL.Path {
	case "/api/exploration/activity", "/api/exploration/activity/stream":
		allowed["since"], allowed["limit"] = true, true
	case "/api/exploration/activity/history":
		allowed["session"], allowed["before"], allowed["limit"] = true, true, true
	default:
		seq, ok := strings.CutPrefix(r.URL.Path, "/api/exploration/activity/")
		if !ok || !canonicalNatural(seq) || seq == "0" {
			return "", false
		}
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", false
	}
	for key, values := range q {
		if !allowed[key] || len(values) != 1 {
			return "", false
		}
		if key != "session" && !canonicalNatural(values[0]) {
			return "", false
		}
	}
	// 다른 task에서 상속한 의도 세션은 공개하지 않는다. main/plan만 조회한다.
	if s := q.Get("session"); s != "" && s != "main" && s != "plan" {
		return "", false
	}
	task := q.Get("task")
	return task, task != "" && task != "0" && canonicalNatural(task)
}

// 원문 명령·모델 텍스트·도구 인자·상세·metadata를 버리고 고정 활동 표시만 전송한다.
func publicActivity(raw json.RawMessage) (map[string]any, bool) {
	var a struct {
		Seq          int64  `json:"seq"`
		TS           string `json:"ts"`
		IsError      bool   `json:"is_error"`
		IntentID     string `json:"intent_id"`
		Worker       string `json:"worker"`
		Inherited    bool   `json:"inherited"`
		SourceTaskID string `json:"source_task_id"`
	}
	if json.Unmarshal(raw, &a) != nil || a.Seq <= 0 || a.Inherited || a.SourceTaskID != "" {
		return nil, false
	}
	if _, err := time.Parse(time.RFC3339Nano, a.TS); err != nil {
		a.TS = ""
	}
	if !canonicalNatural(a.IntentID) {
		a.IntentID = ""
	}
	if a.Worker != "planner" && a.Worker != "mainagent" && a.Worker != "worker" {
		a.Worker = "worker"
	}
	return map[string]any{"seq": a.Seq, "ts": a.TS, "intent_id": a.IntentID, "worker": a.Worker, "kind": "status", "is_error": a.IsError, "summary": "진단 활동 기록", "detail": ""}, true
}

func (b *diagnosisManagementBridge) servePublicActivity(w http.ResponseWriter, r *http.Request, response *http.Response, owner, task string) {
	if response.StatusCode != http.StatusOK {
		writeErr(w, response.StatusCode, "활동 조회 결과를 확인할 수 없습니다")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/api/exploration/activity/stream" {
		flusher, ok := w.(http.Flusher)
		if !ok || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
			writeErr(w, 502, "활동 스트림을 확인할 수 없습니다")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 4096), managementBridgeRequestLimit)
		for scanner.Scan() {
			current, err := b.authenticate(r)
			if err != nil || current != owner || !b.authorized(r.Context(), current, task) {
				return
			}
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				if a, ok := publicActivity(json.RawMessage(strings.TrimPrefix(line, "data: "))); ok {
					raw, _ := json.Marshal(a)
					fmt.Fprintf(w, "id: %v\ndata: %s\n\n", a["seq"], raw)
					flusher.Flush()
				}
			} else if strings.HasPrefix(line, ":") {
				_, _ = io.WriteString(w, ": ping\n\n")
				flusher.Flush()
			}
		}
		return
	}
	var page struct {
		Items    []json.RawMessage `json:"items"`
		Cursor   int64             `json:"cursor"`
		Snapshot int64             `json:"snapshot_cursor"`
		Earliest int64             `json:"earliest_cursor"`
		More     bool              `json:"has_more"`
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, managementBridgeResponseLimit+1))
	if err != nil || len(raw) > managementBridgeResponseLimit || json.Unmarshal(raw, &page) != nil {
		writeErr(w, 502, "활동 응답을 확인할 수 없습니다")
		return
	}
	if r.URL.Path != "/api/exploration/activity" && r.URL.Path != "/api/exploration/activity/history" {
		writeJSON(w, 200, map[string]string{"detail": "원문 상세는 공개 활동 API에 포함하지 않습니다"})
		return
	}
	items := []map[string]any{}
	for _, item := range page.Items {
		if a, ok := publicActivity(item); ok {
			items = append(items, a)
		}
	}
	if r.URL.Path == "/api/exploration/activity/history" {
		writeJSON(w, 200, map[string]any{"items": items, "snapshot_cursor": page.Snapshot, "earliest_cursor": page.Earliest, "has_more": page.More})
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "cursor": page.Cursor})
}
