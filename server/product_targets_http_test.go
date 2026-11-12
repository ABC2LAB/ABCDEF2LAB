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
