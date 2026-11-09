package diagnosisapi

import (
	"testing"

	"github.com/Autumn-27/artex/diagnosis"
)

func TestStartedDraftCannotBeEditedOrRevalidated(t *testing.T) {
	for _, action := range []string{"edit", "validate"} {
		t.Run(action, func(t *testing.T) {
			h, reg, g := setup(t)
			d := create(t, h)
			reg.approved = true
			if code, data := command(t, h, d, "validate", ""); code != 200 || resultDraft(t, data).State != "prepared" {
				t.Fatal("not prepared", code, data)
			}
			if code, data := command(t, h, d, "start", "start"); code != 202 || resultDraft(t, data).State != "running" {
				t.Fatal("not running", code, data)
			}
			method, path := "POST", "/drafts/"+d.ID+"/validate"
			body := requestBody{Version: Version, ExpectedRevision: d.Revision}
			if action == "edit" {
				method, path, body.Configuration = "PATCH", "/drafts/"+d.ID, &testConfiguration
			}
			code, data := request(t, h, method, path, "alice", "", body)
			if code != 409 || string(data["error"]) != `"already_started"` || len(g.calls) != 2 {
				t.Fatal("started draft changed", code, data, g.calls)
			}
			code, data = command(t, h, d, "stop", "")
			if code != 202 || resultDraft(t, data).State != "stopping" || len(g.calls) != 3 || g.calls[2] != diagnosis.Stop {
				t.Fatal("running task lost stop control", code, data, g.calls)
			}
		})
	}
}

func TestValidateRejectsAlreadyStartedGateway(t *testing.T) {
	for _, state := range []diagnosis.State{diagnosis.Running, diagnosis.Prepared} {
		t.Run(string(state), func(t *testing.T) {
			h, reg, g := setup(t)
			d := create(t, h)
			reg.approved = true
			g.state = state
			g.snapshot = func(s *diagnosis.Snapshot) { s.Started = true }
			code, data := command(t, h, d, "validate", "")
			wantCode, wantError := 409, `"not_prepared"`
			if state == diagnosis.Prepared {
				wantCode, wantError = 503, `"execution_service_unavailable"`
			}
			if code != wantCode || string(data["error"]) != wantError || len(g.calls) != 1 || g.calls[0] != diagnosis.Validate || h.BoundTask(t.Context(), "alice", "42") {
				t.Fatal("started Gateway accepted as preparation", code, data, g.calls)
			}
			code, data = command(t, h, d, "start", "start")
			if code != 409 || string(data["error"]) != `"approval_required"` || len(g.calls) != 1 {
				t.Fatal("unprepared Gateway started", code, data, g.calls)
			}
		})
	}
}

func TestValidateRejectsMismatchedApprovalFields(t *testing.T) {
	for name, change := range map[string]func(*Binding){
		"principal": func(b *Binding) { b.Principal = "bob" },
		"draft":     func(b *Binding) { b.DraftID = "other-draft" },
		"revision":  func(b *Binding) { b.Revision++ },
		"digest":    func(b *Binding) { b.SourceDigest = "changed" },
	} {
		t.Run(name, func(t *testing.T) {
			h, reg, g := setup(t)
			d := create(t, h)
			reg.approved = true
			reg.transform = change
			code, data := command(t, h, d, "validate", "")
			if code != 409 || string(data["error"]) != `"approval_binding_invalid"` || len(g.calls) != 0 {
				t.Fatal("mismatched approval reached Gateway", code, data, g.calls)
			}
		})
	}
}

func TestStartRequiresIdempotencyKeyAfterValidation(t *testing.T) {
	h, reg, g := setup(t)
	d := create(t, h)
	reg.approved = true
	if code, data := command(t, h, d, "validate", ""); code != 200 || resultDraft(t, data).State != "prepared" {
		t.Fatal("not prepared", code, data)
	}
	code, data := command(t, h, d, "start", "")
	if code != 400 || string(data["error"]) != `"idempotency_key_required"` || len(g.calls) != 1 || g.calls[0] != diagnosis.Validate {
		t.Fatal("keyless start reached Gateway", code, data, g.calls)
	}
}

func TestCreateKeyCannotSelectAnotherRegisteredConfiguration(t *testing.T) {
	h, reg, g := setup(t)
	other := testConfiguration
	other.TargetRef = "other-owned-target"
	reg.profiles = []Profile{{ID: "first", Label: "첫 설정", Configuration: testConfiguration}, {ID: "second", Label: "다른 설정", Configuration: other}}
	d := create(t, h)
	code, data := request(t, h, "POST", "/drafts", "alice", "create-1", requestBody{Version: Version, Configuration: &other})
	if code != 409 || string(data["error"]) != `"idempotency_conflict"` || len(g.calls) != 0 {
		t.Fatal("receipt reused for other configuration", code, data, g.calls)
	}
	code, data = request(t, h, "GET", "/drafts", "alice", "create-1", nil)
	stored := resultDraft(t, data)
	if code != 200 || stored.ID != d.ID || stored.Revision != d.Revision || stored.SourceDigest != d.SourceDigest || stored.Configuration != d.Configuration {
		t.Fatal("original receipt changed", code, stored)
	}
}

func TestManagementRejectsUnsupportedRequestRoutes(t *testing.T) {
	h, _, g := setup(t)
	d := create(t, h)
	for _, tc := range []struct {
		method, path string
		status       int
		errorCode    string
	}{
		{"GET", "/catalog?extra=1", 400, "invalid_request"},
		{"GET", "/catalog%25", 400, "invalid_request"},
		{"GET", "/drafts/" + d.ID + "/status/extra", 404, "route_not_found"},
		{"GET", "/drafts/bad!ref/status", 404, "route_not_found"},
		{"PATCH", "/drafts/" + d.ID + "/start", 405, "method_not_allowed"},
		{"GET", "/drafts/" + d.ID + "/start", 405, "method_not_allowed"},
		{"POST", "/drafts/" + d.ID + "/unknown", 405, "method_not_allowed"},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			code, data := request(t, h, tc.method, tc.path, "alice", "start", requestBody{Version: Version, ExpectedRevision: d.Revision})
			if code != tc.status || string(data["error"]) != `"`+tc.errorCode+`"` || len(g.calls) != 0 {
				t.Fatal("unsupported route accepted", code, data, g.calls)
			}
		})
	}
}
