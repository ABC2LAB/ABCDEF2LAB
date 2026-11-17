package diagnosisapi

import (
	"encoding/json"
	"github.com/Autumn-27/artex/diagnosisprep"
	"net/http"
	"net/url"
	"strconv"
)

func (h *URLHandler) serveRuns(w http.ResponseWriter, r *http.Request, owner string) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		urlFail(w, 405, "method_not_allowed")
		return
	}
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 || len(r.Header.Values("Idempotency-Key")) != 0 || r.URL.ForceQuery || len(r.URL.RawQuery) > diagnosisprep.MaxRunCursorBytes+64 {
		urlFail(w, 400, "invalid_request")
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) > 2 {
		urlFail(w, 400, "invalid_request")
		return
	}
	input := &diagnosisprep.RunListRequest{}
	for key, values := range query {
		if len(values) != 1 || values[0] == "" {
			urlFail(w, 400, "invalid_request")
			return
		}
		switch key {
		case "limit":
			number, e := strconv.Atoi(values[0])
			if e != nil || number < 1 || number > diagnosisprep.MaxRunPageSize || strconv.Itoa(number) != values[0] {
				urlFail(w, 400, "invalid_request")
				return
			}
			input.Limit = number
		case "cursor":
			input.Cursor = values[0]
		default:
			urlFail(w, 400, "invalid_request")
			return
		}
	}
	currentOwner, err := h.authenticate(r)
	if err != nil || currentOwner != owner {
		urlFail(w, 401, "authentication_required")
		return
	}
	req := diagnosisprep.Request{Version: diagnosisprep.Version, Principal: owner, Command: diagnosisprep.ListRuns, Runs: input}
	// 형식 오류를 서비스 장애로 보이지 않고 송신 전에 거부한다.
	validation := req
	validation.RequestID = "validate-list"
	if diagnosisprep.ValidateRequest(validation) != nil {
		urlFail(w, 400, "invalid_request")
		return
	}
	response, err := h.call(r.Context(), req)
	if err != nil {
		urlFail(w, 503, "preparation_service_unavailable")
		return
	}
	if response.Error != "" {
		status, known := urlErrorStatus(response.Error)
		if !known {
			status = 503
		}
		urlFail(w, status, response.Error)
		return
	}
	result := struct {
		Version      string                 `json:"version"`
		OwnerContext string                 `json:"owner_context"`
		Page         *diagnosisprep.RunPage `json:"page"`
	}{URLVersion, urlOwnerContext(owner), response.RunPage}
	raw, err := json.Marshal(result)
	if err != nil || len(raw) > diagnosisprep.MaxRunPageBytes+1024 {
		urlFail(w, 503, "preparation_service_unavailable")
		return
	}
	writeJSON(w, 200, result)
}
