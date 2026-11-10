package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkflowHTTPBindsOperatorCommandAndRejectsMalformedInput(t *testing.T) {
	p := &productControlRecorder{}
	invoke := func(method, path, body, key, owner string) int {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		productWorkflowHTTP(w, r, owner, p)
		return w.Code
	}
	body := `{"version":"abc2lab.workbench-flow/v1","expected_revision":9,"selected_ids":["endpoint-owned"]}`
	if invoke("POST", "/api/product/workflows/prep-owned/selection", body, "command-owned", "ARTEX") != 200 || len(p.requests) != 1 {
		t.Fatal("valid operator selection did not reach private IPC")
	}
	r := p.requests[0]
	if r.Principal != "ARTEX" || r.Product.JobID != "prep-owned" || r.Product.ExpectedRevision != 9 || r.Product.CommandKey != "command-owned" || r.Product.SelectedIDs[0] != "endpoint-owned" {
		t.Fatal("selection binding lost")
	}
	for _, tc := range []struct{ method, path, body, key, owner string }{
		{"POST", "/api/product/workflows/prep-owned/selection", body, "command-owned", "viewer"},
		{"POST", "/api/product/workflows/prep-owned/selection", body, "", "ARTEX"},
		{"POST", "/api/product/workflows/prep-owned/pause", body, "command-owned", "ARTEX"},
		{"GET", "/api/product/workflows/prep-owned?task=other", "", "", "ARTEX"},
		{"POST", "/api/product/workflows/prep-owned/resume", strings.Replace(body, `"expected_revision":9`, `"expected_revision":0`, 1), "command-owned", "ARTEX"},
		{"POST", "/api/product/workflows/prep-owned/selection", strings.TrimSuffix(body, "}") + `,"origin":"https://other.example"}`, "command-owned", "ARTEX"},
	} {
		before := len(p.requests)
		if invoke(tc.method, tc.path, tc.body, tc.key, tc.owner) < 400 || len(p.requests) != before {
			t.Fatal("malformed or unauthorized command reached IPC", tc.path)
		}
	}
}
