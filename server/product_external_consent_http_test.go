package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
)

func consentCall(t *testing.T, e *releaseEndpoint, method, target, body, contentType string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	productExternalConsentHTTP(w, r, "ARTEX", e)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// 설치 단위 외부 전송 동의 경로(사용자 결정 2026-11-26): 조회와 한 번 동의만 받고, 그 밖의 모양은 준비 서비스에 보내지 않는다.
func TestProductExternalConsentRouteRefusals(t *testing.T) {
	e := &releaseEndpoint{reply: func(diagnosisprep.Request) (diagnosisprep.Response, error) {
		t.Fatal("refused request reached the preparation service")
		return diagnosisprep.Response{}, nil
	}}
	cases := []struct {
		name, method, target, body, media string
		status                            int
		code                              string
	}{
		{"get with query", "GET", productExternalConsentPath + "?x=1", "", "", 400, "invalid_consent_request"},
		{"post without statement", "POST", productExternalConsentPath, `{"statement_confirmed":false}`, "application/json", 409, "statement_required"},
		{"post empty object", "POST", productExternalConsentPath, `{}`, "application/json", 409, "statement_required"},
		{"post unknown field", "POST", productExternalConsentPath, `{"statement_confirmed":true,"source":"install"}`, "application/json", 400, "invalid_consent_request"},
		{"post not json", "POST", productExternalConsentPath, `statement_confirmed=true`, "application/x-www-form-urlencoded", 400, "invalid_consent_request"},
		{"post too large", "POST", productExternalConsentPath, `{"statement_confirmed":true,"x":"` + strings.Repeat("a", productExternalConsentBodyBytes) + `"}`, "application/json", 400, "invalid_consent_request"},
		{"post with query", "POST", productExternalConsentPath + "?x=1", `{"statement_confirmed":true}`, "application/json", 400, "invalid_consent_request"},
		{"put", "PUT", productExternalConsentPath, `{"statement_confirmed":true}`, "application/json", 405, "method_not_allowed"},
	}
	for _, c := range cases {
		status, out := consentCall(t, e, c.method, c.target, c.body, c.media)
		if status != c.status || out["error"] != c.code {
			t.Errorf("%s: %d %v", c.name, status, out)
		}
	}
}

func TestProductExternalConsentRouteCallsPreparation(t *testing.T) {
	at := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	e := &releaseEndpoint{reply: func(r diagnosisprep.Request) (diagnosisprep.Response, error) {
		out := diagnosisprep.Response{Version: diagnosisprep.Version, RequestID: r.RequestID, Product: &diagnosisprep.ProductResponse{}}
		switch r.Product.Operation {
		case "external_consent":
			out.Product.ExternalConsent = &diagnosisprep.ProductExternalConsent{Required: true, Statement: "문장"}
		case "external_consent_record":
			out.Product.ExternalConsent = &diagnosisprep.ProductExternalConsent{Required: true, Statement: "문장", Consented: true, Source: "admin", ConsentedBy: "ARTEX", ConsentedAt: &at}
		}
		return out, nil
	}}
	status, out := consentCall(t, e, "GET", productExternalConsentPath, "", "")
	consent, _ := out["external_consent"].(map[string]any)
	if status != 200 || consent["required"] != true || consent["consented"] != false || len(out) != 1 {
		t.Fatalf("read: %d %v", status, out)
	}
	status, out = consentCall(t, e, "POST", productExternalConsentPath, `{"statement_confirmed":true}`, "application/json")
	consent, _ = out["external_consent"].(map[string]any)
	if status != 200 || consent["consented"] != true || consent["source"] != "admin" {
		t.Fatalf("record: %d %v", status, out)
	}
	if len(e.requests) != 2 || e.requests[0].Product.Operation != "external_consent" || e.requests[1].Product.Operation != "external_consent_record" ||
		e.requests[1].Product.ExternalConsent == nil || !e.requests[1].Product.ExternalConsent.StatementConfirmed || e.requests[1].Principal != "ARTEX" {
		t.Fatalf("preparation requests: %+v", e.requests)
	}
	// 외부 모델이 없는 설치의 동의는 그 사유로 409다.
	e.reply = func(r diagnosisprep.Request) (diagnosisprep.Response, error) {
		return diagnosisprep.Response{Version: diagnosisprep.Version, RequestID: r.RequestID, Product: &diagnosisprep.ProductResponse{Reason: "external_transfer_not_installed"}}, nil
	}
	if status, out = consentCall(t, e, "POST", productExternalConsentPath, `{"statement_confirmed":true}`, "application/json"); status != 409 || out["error"] != "external_transfer_not_installed" {
		t.Fatalf("not installed: %d %v", status, out)
	}
}
