package diagnosisapi

import (
	"context"
	"testing"

	"github.com/Autumn-27/artex/diagnosisprep"
)

func TestURLAPIBoundTaskRejectsExplicitDeniedReceipt(t *testing.T) {
	endpoint := &urlTestEndpoint{reply: func(req diagnosisprep.Request) (diagnosisprep.Response, error) {
		allowed := false
		response := diagnosisprep.Response{
			Version: diagnosisprep.Version, RequestID: req.RequestID, Authorized: &allowed,
		}
		if err := diagnosisprep.ValidateResponse(req, response); err != nil {
			t.Fatalf("denial fixture is not a valid service response: %v", err)
		}
		return response, nil
	}}
	if urlTestHandler(t, endpoint).BoundTask(context.Background(), "owner", "42") {
		t.Fatal("explicitly unauthorized task was accepted")
	}
	if len(endpoint.calls) != 1 {
		t.Fatalf("ownership calls = %d, want 1", len(endpoint.calls))
	}
	req := endpoint.calls[0]
	if req.Command != diagnosisprep.AuthorizeTask || req.Principal != "owner" || req.TaskID != "42" {
		t.Fatalf("wrong ownership request: %+v", req)
	}
}
