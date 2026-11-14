package diagnosisprep

import (
	"testing"

	"github.com/Autumn-27/artex/diagnosisflow"
)

// 대상 등록의 승인 요청 수는 1부터 코드 상한(MaxTargetRequests)까지 받는다(C32, 2026-11-14 100에서 올림).
func TestTargetRegistrationRequestsUpToCodeCap(t *testing.T) {
	for requests, ok := range map[int64]bool{1: true, 100: true, 101: true, 1000: true, 1001: false, 0: false} {
		r := &ProductRequest{Operation: "register", Registration: &TargetRegistration{URL: "https://owned.example/", ReadonlyBasis: "Owned disposable practice target, read-only.", Requests: requests, ValidHours: 4, PathPrefixes: []string{"/"}, Methods: []string{"GET", "HEAD"}}}
		if validProductRequest(r) != ok {
			t.Fatalf("requests=%d: valid=%v", requests, !ok)
		}
	}
}

// 워크플로 프로파일의 요청 한도도 같은 코드 상한까지 받는다. 1000은 받고 1001은 거부한다(값 자체를 고정).
func TestWorkflowProfileRequestLimitUpToCodeCap(t *testing.T) {
	r := Request{Version: Version, RequestID: "r", Principal: "owner", Command: Catalog}
	flow := &diagnosisflow.Limits{Format: diagnosisflow.Version, MaxPages: 8, MaxEndpoints: 32, MaxResponseBytes: 65536}
	for limit, ok := range map[int64]bool{1: true, 101: true, 1000: true, 1001: false, 0: false} {
		response := Response{Version: Version, RequestID: "r", Profiles: []Profile{{ID: "product-model-local", Label: "fixture", ModelProfileRef: "model-local", ModelLabel: "fixture", Workflow: flow, WorkflowRequestLimit: limit}}}
		if (ValidateResponse(r, response) == nil) != ok {
			t.Fatalf("workflow_request_limit=%d: ok=%v", limit, !ok)
		}
	}
}

// 회차당 요청 상한은 워크플로가 없는 제품 프로필에도 실린다(화면이 그 값으로 대상을 등록). 있으면 1부터 코드 상한까지만
// 받고, 없으면(0) 다른 실행 모드 프로필이라 받는다. 워크플로 프로필에는 반드시 있어야 한다.
func TestProfileRequestLimitWithoutWorkflow(t *testing.T) {
	r := Request{Version: Version, RequestID: "r", Principal: "owner", Command: Catalog}
	for limit, ok := range map[int64]bool{0: true, 1: true, 400: true, 1000: true, 1001: false, -1: false} {
		response := Response{Version: Version, RequestID: "r", Profiles: []Profile{{ID: "product-model-local", Label: "fixture", ModelProfileRef: "model-local", ModelLabel: "fixture", WorkflowRequestLimit: limit}}}
		if (ValidateResponse(r, response) == nil) != ok {
			t.Fatalf("request limit %d without workflow: ok=%v", limit, !ok)
		}
	}
}
