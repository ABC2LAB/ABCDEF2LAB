package server

import (
	"github.com/Autumn-27/artex/diagnosisprep"
	"io"
	"mime"
	"net/http"
	"strings"
)

// 제품 workflow 명령은 공개 task 실행 API로 우회하지 않고 인증된 IPC만 사용한다.
func productWorkflowHTTP(w http.ResponseWriter, r *http.Request, owner string, endpoint diagnosisprep.Endpoint) {
	if owner != "ARTEX" {
		writeJSON(w, 403, map[string]string{"error": "forbidden"})
		return
	}
	if r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery || strings.ContainsAny(r.URL.Path, "%\\") {
		writeJSON(w, 400, map[string]string{"error": "invalid_workflow_request"})
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/product/workflows/"), "/")
	if len(parts) < 1 || len(parts) > 2 || !managementApprovalRef.MatchString(parts[0]) {
		writeJSON(w, 400, map[string]string{"error": "invalid_workflow_request"})
		return
	}
	request := &diagnosisprep.ProductRequest{Operation: "workflow_status", JobID: parts[0]}
	if len(parts) == 1 {
		if r.Method != "GET" {
			writeJSON(w, 405, map[string]string{"error": "method_not_allowed"})
			return
		}
	} else {
		if r.Method != "POST" || parts[1] != "selection" && parts[1] != "pause" && parts[1] != "resume" {
			writeJSON(w, 405, map[string]string{"error": "method_not_allowed"})
			return
		}
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		raw, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
		var body struct {
			Version          string   `json:"version"`
			ExpectedRevision uint64   `json:"expected_revision"`
			SelectedIDs      []string `json:"selected_ids,omitempty"`
		}
		key := r.Header.Get("Idempotency-Key")
		if err != nil || media != "application/json" || readErr != nil || strictJSON(raw, &body) != nil || body.Version != "abc2lab.workbench-flow/v1" || body.ExpectedRevision == 0 || len(r.Header.Values("Idempotency-Key")) != 1 || !managementApprovalRef.MatchString(key) || len(body.SelectedIDs) > 64 || parts[1] != "selection" && len(body.SelectedIDs) != 0 {
			writeJSON(w, 400, map[string]string{"error": "invalid_workflow_request"})
			return
		}
		request.Operation = "workflow_" + parts[1]
		request.CommandKey = key
		request.ExpectedRevision = body.ExpectedRevision
		request.SelectedIDs = body.SelectedIDs
	}
	callProductControl(w, r, owner, endpoint, request)
}
