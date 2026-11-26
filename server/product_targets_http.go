package server

import (
	"context"
	"encoding/json"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e3contract"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
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
		} else if len(parts) == 2 && parts[1] == "reset-confirmation" {
			// GET: 확정 단계 쓰기와 리셋 확인 상태(진술 문장 포함). POST {"statement_confirmed": true, "writes_ref": "<본 기록 참조>"}:
			// 리셋 진술을 남김. 그 사이 기록이 바뀌었으면 409 reset_record_changed.
			switch r.Method {
			case "GET":
				request.Operation = "reset_confirmation"
			case "POST":
				var body diagnosisprep.ResetRequest
				if !productAccountBody(w, r, &body) || !body.StatementConfirmed {
					writeJSON(w, 400, map[string]string{"error": "statement_required"})
					return
				}
				// 동의는 화면이 본 쓰기 기록(last_writes_job)에 묶인다. 없으면 무엇에 동의했는지 모른다.
				if !diagnosisprep.ValidResetWritesRef(body.WritesRef) {
					writeJSON(w, 400, map[string]string{"error": "invalid_reset_request"})
					return
				}
				request.Operation = "reset_confirm"
				request.Reset = &body
			default:
				writeJSON(w, 405, map[string]string{"error": "method_not_allowed"})
				return
			}
		} else if len(parts) == 2 && parts[1] == "declaration" {
			if !productDeclarationRequest(w, r, request) {
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

// productAccountRequest는 대상 계정 경로(C14 결정 1·2)를 제품 제어 요청으로 바꾼다.
//
//	GET  /api/product/targets/{id}/accounts                   목록(라벨·등록자·등록 시각·자격 등록 여부)
//	POST /api/product/targets/{id}/accounts                   {"label": "..."} 등록
//	PUT  /api/product/targets/{id}/accounts/{label}/credential {"username","password"} 자격 등록(값은 응답·목록에 없음)
//	DELETE /api/product/targets/{id}/accounts/{label}/credential (본문 없음 또는 {}) 그 계정만 자격 철회(결속 사본도 지움)
//
// 계정 분류 폐지(사용자 결정 2026-11-26, docs/contracts/model-login-attempts.md §0): 시험 계정 표시(mark-test)·해제(unmark)
// 경로는 없앴다(404). 프로그램은 계정을 분류하지 않고, 자격이 저장된 등록 계정을 회차에 결속한다.
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
	case len(parts) == 4 && r.Method == "DELETE" && parts[3] == "credential":
		raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 128))
		if !diagnosisprep.ValidAccountLabel(parts[2]) || e != nil || len(raw) > 0 && string(raw) != "{}" {
			writeJSON(w, 400, map[string]string{"error": "invalid_account_request"})
			return false
		}
		request.Operation = "account_credential_clear"
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

// maxDeclarationBodyBytes는 정책 선언 저장 요청 본문 상한이다. 선언 인코딩 상한(e3contract.MaxDeclarationEncodedBytes)에
// 대상 판·감싸는 칸의 여유를 더한 값이고, 준비 프로세스 IPC 한 프레임(64KiB) 안이다.
const maxDeclarationBodyBytes = e3contract.MaxDeclarationEncodedBytes + 1<<10

// declarationServerFields는 서버가 채우는 선언 칸이다. 화면이 보내면 그 칸 이름으로 거부한다.
var declarationServerFields = map[string]bool{"version": true, "target_ref": true, "target_approval_revision": true,
	"declared_by": true, "declared_at": true}

// productDeclarationRequest는 대상 정책 선언 경로를 제품 제어 요청으로 바꾼다(E③ 확정 경로 계약 §3).
//
//	GET /api/product/targets/{id}/declaration  조회(state none·active·invalid)
//	PUT /api/product/targets/{id}/declaration  {"target_revision": N, "declaration": {"identity": …, "types": […],
//	    "owner_account_label"?, "cross_account_label"?, "accounts_same_privilege"?}} 통째 저장
//
// 거부한 칸의 값은 응답·로그에 넣지 않는다.
func productDeclarationRequest(w http.ResponseWriter, r *http.Request, request *diagnosisprep.ProductRequest) bool {
	switch r.Method {
	case "GET":
		request.Operation = "declaration"
		return true
	case "PUT":
	default:
		writeJSON(w, 405, map[string]string{"error": "method_not_allowed"})
		return false
	}
	invalid := func(field string) bool {
		writeJSON(w, 400, map[string]string{"error": "invalid_declaration", "field": field})
		return false
	}
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	raw, re := io.ReadAll(http.MaxBytesReader(w, r.Body, maxDeclarationBodyBytes))
	defer clear(raw)
	if e != nil || media != "application/json" || re != nil {
		return invalid("body")
	}
	var top struct {
		TargetRevision uint64                     `json:"target_revision"`
		Declaration    map[string]json.RawMessage `json:"declaration"`
	}
	if strictJSON(raw, &top) != nil || top.Declaration == nil {
		return invalid("body")
	}
	if top.TargetRevision == 0 {
		return invalid("target_revision")
	}
	for key := range top.Declaration {
		if declarationServerFields[key] {
			return invalid(key)
		}
		switch key {
		// 확정 단계 계정 역할(계약 §4)은 선택 칸이다. 모양·짝 검사는 준비 프로세스(e3contract.Declaration.Validate)가 칸 이름으로 한다.
		case "identity", "types", "owner_account_label", "cross_account_label", "accounts_same_privilege":
		default:
			return invalid("declaration")
		}
	}
	inner, err := json.Marshal(top.Declaration)
	var in diagnosisprep.DeclarationInput
	if err != nil || strictJSON(inner, &in) != nil {
		return invalid("declaration")
	}
	if len(in.Types) == 0 || len(in.Types) > e3contract.MaxDeclarationTypes {
		return invalid("types")
	}
	// 공유 종류는 소유자 전용 필드를 보내지 않을 수 있다. 준비 프로세스 IPC는 null을 받지 않으므로 빈 목록으로 둔다.
	for i := range in.Types {
		if in.Types[i].OwnerOnlyFields == nil {
			in.Types[i].OwnerOnlyFields = []string{}
		}
	}
	request.Operation = "declaration_put"
	request.ExpectedRevision = top.TargetRevision
	request.Declaration = &in
	return true
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
		case "invalid_declaration":
			// 선언 형식 오류는 칸 이름만 돌려준다(값 없음). 대상 판 결속 실패는 409다.
			writeJSON(w, 400, map[string]string{"error": "invalid_declaration", "field": response.Product.Field})
			return
		}
	}
	if response.Product.Reason != "" {
		writeJSON(w, status, map[string]string{"error": response.Product.Reason})
		return
	}
	if control.Operation == "declaration" || control.Operation == "declaration_put" {
		writeJSON(w, status, publicDeclaration(response.Product.Declaration))
		return
	}
	writeJSON(w, status, response.Product)
}

// productDeclarationView는 정책 선언 조회·저장의 공개 응답이다. 선언이 없으면 declaration은 null이고 시각·판 칸은 없다.
type productDeclarationView struct {
	State          string                  `json:"state"`
	InvalidReason  string                  `json:"invalid_reason,omitempty"`
	TargetRevision uint64                  `json:"target_revision,omitempty"`
	DeclaredBy     string                  `json:"declared_by,omitempty"`
	DeclaredAt     *time.Time              `json:"declared_at,omitempty"`
	Declaration    *e3contract.Declaration `json:"declaration"`
}

func publicDeclaration(d *diagnosisprep.ProductDeclaration) productDeclarationView {
	if d == nil {
		return productDeclarationView{State: diagnosisprep.DeclarationNone}
	}
	v := productDeclarationView{State: d.State, InvalidReason: d.InvalidReason, TargetRevision: d.TargetRevision,
		DeclaredBy: d.DeclaredBy, Declaration: d.Declaration}
	if !d.DeclaredAt.IsZero() {
		at := d.DeclaredAt
		v.DeclaredAt = &at
	}
	return v
}
