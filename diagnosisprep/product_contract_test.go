package diagnosisprep

import (
	"encoding/json"
	"testing"
)

func TestProductControlCannotBeSmuggledIntoDiagnosisCommands(t *testing.T) {
	r := Request{Version: Version, RequestID: "test-product-control", Principal: "ARTEX", Command: ProductControl, Product: &ProductRequest{Operation: "targets"}}
	if ValidateRequest(r) != nil {
		t.Fatal("valid product intent rejected")
	}
	for _, cmd := range []Command{Catalog, Create, Validate, Start, Lookup, Status, Stop, AuthorizeTask} {
		bad := r
		bad.Command = cmd
		if ValidateRequest(bad) == nil {
			t.Fatalf("product control accepted as %s", cmd)
		}
	}
	for _, bad := range []ProductRequest{{Operation: "targets", TargetURL: "https://owned.example/"}, {Operation: "approve", TargetID: "target-a", Registration: &TargetRegistration{}}, {Operation: "credentials", ProfileID: "profile-a", TargetID: "target-a"}, {Operation: "execute"}} {
		r.Product = &bad
		if ValidateRequest(r) == nil {
			t.Fatal("ambiguous product intent accepted")
		}
	}
}

func TestProductTargetSummaryHasNoNullCollectionsOnPrivateWire(t *testing.T) {
	r := Request{Version: Version, RequestID: "targets-summary", Principal: "ARTEX", Command: ProductControl, Product: &ProductRequest{Operation: "targets"}}
	response := Response{Version: Version, RequestID: r.RequestID, Product: &ProductResponse{Targets: []ProductTarget{{ID: "target-a", Origin: "https://owned.example", State: "approved", Revision: 2, Requests: 4, Port: 443}}}}
	raw, e := json.Marshal(response)
	if e != nil {
		t.Fatal(e)
	}
	var decoded Response
	if strictDecode(raw, &decoded) != nil || ValidateResponse(r, decoded) != nil {
		t.Fatal("summary cannot cross strict private IPC")
	}
}
