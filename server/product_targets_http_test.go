package server

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/diagnosisprep"
)

type productControlRecorder struct {
	requests []diagnosisprep.Request
	reason   string
}

func (p *productControlRecorder) Call(_ context.Context, r diagnosisprep.Request) (diagnosisprep.Response, error) {
	p.requests = append(p.requests, r)
	return diagnosisprep.Response{Version: diagnosisprep.Version, RequestID: r.RequestID, Product: &diagnosisprep.ProductResponse{Reason: p.reason}}, nil
}
func TestProductTargetHTTPOnlyPassesBoundedOperatorIntent(t *testing.T) {
	p := &productControlRecorder{}
	invoke := func(method, path, body, owner string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		productTargetsHTTP(w, r, owner, p)
		return w
	}
	body := `{"url":"https://owned.example/read","path_prefixes":["/read"],"methods":["GET","HEAD"],"readonly_basis":"Owned read-only product listing approved by operator","requests":4,"valid_hours":4}`
	if invoke("POST", "/api/product/targets", body, "viewer").Code != 403 || len(p.requests) != 0 {
		t.Fatal("nonoperator reached preparation")
	}
	if invoke("POST", "/api/product/targets", body, "ARTEX").Code != 200 || len(p.requests) != 1 || p.requests[0].Product.Operation != "register" || p.requests[0].Principal != "ARTEX" {
		t.Fatal("registration intent lost binding")
	}
	for _, bad := range []string{strings.TrimSuffix(body, "}") + `,"approved":true}`, strings.Replace(body, `"GET"`, `"POST"`, 1), strings.TrimSuffix(body, "}") + `,"connect_ip":"127.0.0.1"}`} {
		before := len(p.requests)
		if invoke("POST", "/api/product/targets", bad, "ARTEX").Code != 400 || len(p.requests) != before {
			t.Fatal("browser forged scope reached preparation")
		}
	}
	id := "target-" + strings.Repeat("a", 32)
	p.reason = "ownership_not_verified"
	if w := invoke("POST", "/api/product/targets/"+id+"/approve", "{}", "ARTEX"); w.Code != 409 || !strings.Contains(w.Body.String(), p.reason) {
		t.Fatal("ownership denial not propagated")
	}
	p.reason = "target_address_denied"
	if invoke("POST", "/api/product/targets/"+id+"/approve", "{}", "ARTEX").Code != 403 {
		t.Fatal("protected address denial not propagated")
	}
	before := len(p.requests)
	if invoke("POST", "/api/product/targets/"+id+"/approve", `{"confirmed":true}`, "ARTEX").Code != 400 || len(p.requests) != before {
		t.Fatal("checkbox expanded authority")
	}
	if invoke("GET", "/api/product/targets/resolve?url=https%3A%2F%2Fowned.example%2Fread&url=https%3A%2F%2Fother.example", "", "ARTEX").Code != 400 {
		t.Fatal("ambiguous lookup accepted")
	}
}

// 대상 계정 경로(C14)는 라벨·진술 동의만 넘기고, 자격 값이나 다른 칸은 준비 프로세스로 보내지 않는다.
func TestProductAccountHTTPPassesOnlyLabelAndStatement(t *testing.T) {
	p := &productControlRecorder{}
	id := "target-" + strings.Repeat("b", 32)
	base := "/api/product/targets/" + id + "/accounts"
	invoke := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		productTargetsHTTP(w, r, "ARTEX", p)
		return w
	}
	last := func() *diagnosisprep.ProductRequest { return p.requests[len(p.requests)-1].Product }
	if invoke("GET", base, "").Code != 200 || last().Operation != "accounts" || last().TargetID != id || last().Account != nil {
		t.Fatal("account list not forwarded")
	}
	if invoke("POST", base, `{"label":"tester-a"}`).Code != 200 || last().Operation != "account_register" || last().Account.Label != "tester-a" || last().Account.StatementConfirmed {
		t.Fatal("account registration not forwarded")
	}
	if invoke("POST", base+"/tester-a/mark-test", `{"statement_confirmed":true}`).Code != 200 || last().Operation != "account_mark_test" || !last().Account.StatementConfirmed {
		t.Fatal("test mark not forwarded")
	}
	if invoke("POST", base+"/tester-a/unmark", "{}").Code != 200 || last().Operation != "account_unmark" || last().Account.Label != "tester-a" {
		t.Fatal("unmark not forwarded")
	}
	before := len(p.requests)
	for name, c := range map[string][3]string{
		"password field":      {"POST", base, `{"label":"tester-a","password":"seeded-secret"}`},
		"username label":      {"POST", base, `{"label":"user@example.test"}`},
		"classification":      {"POST", base, `{"label":"tester-a","classification":"test"}`},
		"statement missing":   {"POST", base + "/tester-a/mark-test", `{}`},
		"statement false":     {"POST", base + "/tester-a/mark-test", `{"statement_confirmed":false}`},
		"mark extra field":    {"POST", base + "/tester-a/mark-test", `{"statement_confirmed":true,"revision":9}`},
		"unmark body":         {"POST", base + "/tester-a/unmark", `{"reason":"x"}`},
		"bad label path":      {"POST", base + "/Tester/unmark", "{}"},
		"unknown action":      {"POST", base + "/tester-a/delete", "{}"},
		"get one account":     {"GET", base + "/tester-a", ""},
		"approve with suffix": {"POST", "/api/product/targets/" + id + "/approve/x", "{}"},
	} {
		if w := invoke(c[0], c[1], c[2]); w.Code < 400 || len(p.requests) != before {
			t.Errorf("%s reached preparation (code %d)", name, w.Code)
		}
	}
	p.reason = "account_not_found"
	if invoke("POST", base+"/tester-z/unmark", "{}").Code != 404 {
		t.Fatal("missing account not mapped to 404")
	}
}
