package server

import (
	"context"
	"github.com/Autumn-27/artex/diagnosisprep"
	"io"
	"mime"
	"net/http"
	"strings"
)

// The public API passes authenticated operator intent to private preparation.
// It cannot resolve targets, approve ownership, choose keys, or mint run authority.
func productTargetsHTTP(w http.ResponseWriter, r *http.Request, owner string, endpoint diagnosisprep.Endpoint) {
	if owner != "ARTEX" || endpoint == nil {
		writeJSON(w, 403, map[string]string{"error": "operator_required"})
		return
	}
	request := &diagnosisprep.ProductRequest{}
	if r.URL.Path == "/api/product/targets/resolve" && r.Method == "GET" {
		q := r.URL.Query()
		if len(q) != 1 || len(q["url"]) != 1 {
			writeJSON(w, 400, map[string]string{"error": "invalid_target_lookup"})
			return
		}
		request.Operation = "resolve"
		request.TargetURL = q.Get("url")
		callProductControl(w, r, owner, endpoint, request)
		return
	}
	tail := strings.TrimPrefix(r.URL.Path, "/api/product/targets")
	if tail == "" {
		switch r.Method {
		case "GET":
			request.Operation = "targets"
		case "POST":
			media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
			raw, re := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
			defer clear(raw)
			var spec diagnosisprep.TargetRegistration
			if e != nil || media != "application/json" || re != nil || strictJSON(raw, &spec) != nil {
				writeJSON(w, 400, map[string]string{"error": "invalid_target_registration"})
				return
			}
			request.Operation = "register"
			request.Registration = &spec
		default:
			writeJSON(w, 405, map[string]string{"error": "method_not_allowed"})
			return
		}
	} else {
		parts := strings.Split(strings.TrimPrefix(tail, "/"), "/")
		if len(parts) < 1 || len(parts) > 4 {
			http.NotFound(w, r)
			return
		}
		request.TargetID = parts[0]
		if len(parts) >= 2 && parts[1] == "accounts" {
			if !productAccountRequest(w, r, parts, request) {
				return
			}
		} else if len(parts) == 2 && parts[1] == "login" {
			switch r.Method {
			case "GET":
				request.Operation = "target_login"
			case "PUT":
				var login diagnosisprep.TargetLogin
				if !productAccountBody(w, r, &login) || !diagnosisprep.ValidTargetLogin(login) {
					writeJSON(w, 400, map[string]string{"error": "invalid_target_login"})
					return
				}
				request.Operation = "target_login_set"
				request.Login = &login
			default:
				writeJSON(w, 405, map[string]string{"error": "method_not_allowed"})
				return
			}
		} else if len(parts) > 2 {
			http.NotFound(w, r)
			return
		} else if len(parts) == 1 && r.Method == "GET" {
			request.Operation = "target"
		} else if len(parts) == 2 && r.Method == "POST" && (parts[1] == "approve" || parts[1] == "revoke") {
			request.Operation = parts[1]
			raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 128))
			if e != nil || len(raw) > 0 && string(raw) != "{}" {
				writeJSON(w, 400, map[string]string{"error": "invalid_target_operation"})
				return
			}
		} else {
			http.NotFound(w, r)
			return
		}
	}
	callProductControl(w, r, owner, endpoint, request)
	if request.Credential != nil {
		request.Credential.Username, request.Credential.Password = "", ""
	}
}

// productAccountRequest는 대상 계정 경로(C14 결정 1·2)를 제품 제어 요청으로 바꾼다. 자격 값은 받지 않는다.
//
//	GET  /api/product/targets/{id}/accounts                  목록(분류·사유·등록자·표시자)
//	POST /api/product/targets/{id}/accounts                  {"label": "..."} 등록(분류 real)
//	POST /api/product/targets/{id}/accounts/{label}/mark-test {"statement_confirmed": true} 시험 계정 표시(대상 승인자)
//	POST /api/product/targets/{id}/accounts/{label}/unmark    {} 표시 해제
func productAccountRequest(w http.ResponseWriter, r *http.Request, parts []string, request *diagnosisprep.ProductRequest) bool {
	switch {
	case len(parts) == 2 && r.Method == "GET":
		request.Operation = "accounts"
		return true
	case len(parts) == 2 && r.Method == "POST":
		var body struct {
			Label string `json:"label"`
		}
		if !productAccountBody(w, r, &body) || !diagnosisprep.ValidAccountLabel(body.Label) {
			writeJSON(w, 400, map[string]string{"error": "invalid_account_request"})
			return false
		}
		request.Operation = "account_register"
		request.Account = &diagnosisprep.AccountRequest{Label: body.Label}
		return true
	case len(parts) == 4 && r.Method == "POST" && parts[3] == "mark-test":
		var body struct {
			StatementConfirmed bool `json:"statement_confirmed"`
		}
		if !diagnosisprep.ValidAccountLabel(parts[2]) || !productAccountBody(w, r, &body) {
			writeJSON(w, 400, map[string]string{"error": "invalid_account_request"})
			return false
		}
		if !body.StatementConfirmed {
			writeJSON(w, 409, map[string]string{"error": "statement_required"})
			return false
		}
		request.Operation = "account_mark_test"
		request.Account = &diagnosisprep.AccountRequest{Label: parts[2], StatementConfirmed: true}
		return true
	case len(parts) == 4 && r.Method == "PUT" && parts[3] == "credential":
		// 자격 값은 준비 프로세스로만 넘긴다. 읽은 본문 바이트는 바로 지우고, 요청 구조체의 값 참조는 넘긴 뒤 버린다
		// (Go 문자열은 덮어쓸 수 없어 참조만 끊는다). 응답·로그에는 넣지 않는다.
		media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		raw, re := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<10))
		var cred diagnosisprep.AccountCredential
		ok := diagnosisprep.ValidAccountLabel(parts[2]) && e == nil && media == "application/json" && re == nil && strictJSON(raw, &cred) == nil && diagnosisprep.ValidAccountCredential(cred)
		clear(raw)
		if !ok {
			cred = diagnosisprep.AccountCredential{}
			writeJSON(w, 400, map[string]string{"error": "invalid_account_credential"})
			return false
		}
		request.Operation = "account_credential_set"
		request.Account = &diagnosisprep.AccountRequest{Label: parts[2]}
		request.Credential = &cred
		return true
	case len(parts) == 4 && r.Method == "POST" && parts[3] == "unmark":
		raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 128))
		if !diagnosisprep.ValidAccountLabel(parts[2]) || e != nil || len(raw) > 0 && string(raw) != "{}" {
			writeJSON(w, 400, map[string]string{"error": "invalid_account_request"})
			return false
		}
		request.Operation = "account_unmark"
		request.Account = &diagnosisprep.AccountRequest{Label: parts[2]}
		return true
	}
	http.NotFound(w, r)
	return false
}

func productAccountBody(w http.ResponseWriter, r *http.Request, out any) bool {
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	raw, re := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<10))
	return e == nil && media == "application/json" && re == nil && strictJSON(raw, out) == nil
}

func callProductControl(w http.ResponseWriter, r *http.Request, owner string, endpoint diagnosisprep.Endpoint, control *diagnosisprep.ProductRequest) {
	request := diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "product-control", Principal: owner, Command: diagnosisprep.ProductControl, Product: control}
	if diagnosisprep.ValidateRequest(request) != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid_product_request"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 9_000_000_000)
	defer cancel()
	response, e := endpoint.Call(ctx, request)
	if e != nil || response.Error != "" || diagnosisprep.ValidateResponse(request, response) != nil {
		writeJSON(w, 503, map[string]string{"error": "product_control_unavailable"})
		return
	}
	status := 200
	if response.Product.Reason != "" {
		status = 409
		switch response.Product.Reason {
		case "target_address_denied", "target_port_denied", "operator_required":
			status = 403
		case "target_not_found", "account_not_found":
			status = 404
		}
	}
	if response.Product.Reason != "" {
		writeJSON(w, status, map[string]string{"error": response.Product.Reason})
		return
	}
	writeJSON(w, status, response.Product)
}
