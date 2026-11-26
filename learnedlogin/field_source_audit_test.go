package learnedlogin

import (
	"encoding/json"
	"strings"
	"testing"
)

func sourceRow(stage string) Row {
	r := succeededRow(stage+"-1", "acct-a")
	r.Stage = stage
	r.FieldSources = []RowFieldSource{{Destination: "_csrf", SourceMethod: "GET", SourcePath: "/api/csrf", SourceType: SourceJSONField,
		SourceField: "csrfToken", SourceRef: "call_p/attempt_2", SourceStatus: 200}}
	return r
}

func TestFieldSourceSelectionAndAudit(t *testing.T) {
	r := sourceRow(StageDiagnosis)
	sel := Select([]Row{r}, run, []string{"acct-a"})["acct-a"]
	if sel.Reason != "" || sel.Learned.Method.Schema != MethodSchemaV2 || sel.Learned.Method.PrepPath() != "/api/csrf" {
		t.Fatalf("v2 선택 %+v", sel)
	}
	lookup := func(call, attempt string) (AuditedSend, bool) {
		return AuditedSend{CallRef: call, AttemptID: attempt, RunRef: run, ModelCall: true, Method: "GET", Allowed: true,
			RequestWritten: true, ResponseReceived: true, ResponseComplete: true, StatusCode: 200, AuditComplete: true}, true
	}
	if reason := VerifyFieldSources(r, run, nil, lookup); reason != "" {
		t.Fatal(reason)
	}
	if reason := VerifyFieldSources(r, run, nil, nil); reason != ReasonTransportUnverified {
		t.Fatal("감사 없는 출처를 허용함")
	}
	bad := r
	bad.FieldSources = append([]RowFieldSource(nil), r.FieldSources...)
	bad.FieldSources[0].SourceStatus = 201
	if reason := VerifyFieldSources(bad, run, nil, lookup); reason != ReasonTransportUnverified {
		t.Fatal("다른 상태를 허용함")
	}
	encoded, _ := json.Marshal(r)
	if _, _, err := ParseRows(append(encoded, '\n')); err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(encoded), `"source_path":"/api/csrf"`, `"source_path":"/api/csrf","source_path":"/other"`, 1)
	if _, _, err := ParseRows([]byte(broken + "\n")); err == nil {
		t.Fatal("중첩 출처 키 중복을 허용함")
	}
}

func TestExplorationCSRFSourceRequiresIndependentGET(t *testing.T) {
	r := sourceRow(StageExploration)
	r.TransportRef, r.FieldSources[0].SourceRef = "17", "16"
	e := explorationEvidenceFixture()
	e.RunRef = r.RunRef
	e.Sends = append(e.Sends, ExplorationSend{Seq: 16, Method: "GET", Path: "/api/csrf", Status: 200,
		BoundaryForwarded: true, ApprovedTarget: true, ObservationForwarded: true, ResponseReceived: true})
	if reason := VerifyFieldSources(r, run, &e, nil); reason != "" {
		t.Fatal(reason)
	}
	e.Sends[1].Path = "/other"
	if reason := VerifyFieldSources(r, run, &e, nil); reason != ReasonTransportUnverified {
		t.Fatal("다른 발급 경로를 허용함")
	}
}
