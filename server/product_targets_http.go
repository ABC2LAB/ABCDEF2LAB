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
		if len(parts) < 1 || len(parts) > 2 {
			http.NotFound(w, r)
			return
		}
		request.TargetID = parts[0]
		if len(parts) == 1 && r.Method == "GET" {
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
		case "target_not_found":
			status = 404
		}
	}
	if response.Product.Reason != "" {
		writeJSON(w, status, map[string]string{"error": response.Product.Reason})
		return
	}
	writeJSON(w, status, response.Product)
}
