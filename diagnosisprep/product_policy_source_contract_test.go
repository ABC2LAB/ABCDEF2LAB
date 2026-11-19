package diagnosisprep

import (
	"strings"
	"testing"
)

// policy_source는 회차 하나의 정책 동결본 digest 조회다(판정 상세 조회 OD06). 회차 참조만 받고, 응답은 digest만 싣는다.
func TestProductPolicySourceRequestValidation(t *testing.T) {
	if r := (ProductRequest{Operation: "policy_source", JobID: "job-1"}); !validProductRequest(&r) {
		t.Fatal("valid policy_source rejected")
	}
	for name, r := range map[string]ProductRequest{
		"no job":        {Operation: "policy_source"},
		"bad job":       {Operation: "policy_source", JobID: "../job"},
		"with target":   {Operation: "policy_source", JobID: "job-1", TargetID: "target-1"},
		"with profile":  {Operation: "policy_source", JobID: "job-1", ProfileID: "local"},
		"with command":  {Operation: "policy_source", JobID: "job-1", CommandKey: "key-1"},
		"with revision": {Operation: "policy_source", JobID: "job-1", ExpectedRevision: 2},
		"with selected": {Operation: "policy_source", JobID: "job-1", SelectedIDs: []string{"1"}},
		"with url":      {Operation: "policy_source", JobID: "job-1", TargetURL: "https://owned.test/"},
		"with account":  {Operation: "policy_source", JobID: "job-1", Account: &AccountRequest{Label: "tester-a"}},
		"with reset":    {Operation: "policy_source", JobID: "job-1", Reset: &ResetRequest{StatementConfirmed: true}},
		"with decl":     {Operation: "policy_source", JobID: "job-1", Declaration: declarationContractInput()},
		"registration":  {Operation: "policy_source", JobID: "job-1", Registration: &TargetRegistration{}},
	} {
		r := r
		if validProductRequest(&r) {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestProductPolicySourceResponseValidation(t *testing.T) {
	req := Request{Version: Version, RequestID: "product-control", Principal: "ARTEX", Command: ProductControl,
		Product: &ProductRequest{Operation: "policy_source", JobID: "job-1"}}
	other := Request{Version: Version, RequestID: "product-control", Principal: "ARTEX", Command: ProductControl,
		Product: &ProductRequest{Operation: "declaration", TargetID: "target-1"}}
	respond := func(r Request, p *ProductResponse) error {
		return ValidateResponse(r, Response{Version: Version, RequestID: "product-control", Product: p})
	}
	digest := strings.Repeat("a", 64)
	none := &ProductConfirmationState{}
	for name, p := range map[string]*ProductResponse{
		"present":   {PolicySource: &ProductPolicySource{Present: true, SHA256: digest}, Confirmation: none},
		"absent":    {PolicySource: &ProductPolicySource{}, Confirmation: none},
		"not found": {Reason: "not_found"},
		// 제품 판정 목록 결정 2·3: 동결본의 계정 라벨 둘과 확정 단계 상태·사유.
		"labels": {PolicySource: &ProductPolicySource{Present: true, SHA256: digest, OwnerAccountLabel: "tester-a", CrossAccountLabel: "tester-b"},
			Confirmation: &ProductConfirmationState{Present: true, Status: "disabled", Reason: "declaration_missing"}},
		"completed":  {PolicySource: &ProductPolicySource{Present: true, SHA256: digest}, Confirmation: &ProductConfirmationState{Present: true, Status: "completed"}},
		"unreadable": {PolicySource: &ProductPolicySource{}, Confirmation: &ProductConfirmationState{Present: true, Status: ConfirmationUnreadable}},
	} {
		if err := respond(req, p); err != nil {
			t.Fatalf("%s rejected: %v", name, err)
		}
	}
	for name, c := range map[string]struct {
		r Request
		p *ProductResponse
	}{
		"empty":                    {req, &ProductResponse{}},
		"present no digest":        {req, &ProductResponse{PolicySource: &ProductPolicySource{Present: true}, Confirmation: none}},
		"absent digest":            {req, &ProductResponse{PolicySource: &ProductPolicySource{SHA256: digest}, Confirmation: none}},
		"upper digest":             {req, &ProductResponse{PolicySource: &ProductPolicySource{Present: true, SHA256: strings.Repeat("A", 64)}, Confirmation: none}},
		"short digest":             {req, &ProductResponse{PolicySource: &ProductPolicySource{Present: true, SHA256: digest[:63]}, Confirmation: none}},
		"with reason":              {req, &ProductResponse{Reason: "not_found", PolicySource: &ProductPolicySource{}}},
		"on other op":              {other, &ProductResponse{PolicySource: &ProductPolicySource{}}},
		"no confirmation":          {req, &ProductResponse{PolicySource: &ProductPolicySource{}}},
		"confirmation only":        {req, &ProductResponse{Confirmation: none}},
		"confirmation with reason": {req, &ProductResponse{Reason: "not_found", Confirmation: none}},
		"confirmation other op":    {other, &ProductResponse{Confirmation: none}},
		"one label":                {req, &ProductResponse{PolicySource: &ProductPolicySource{Present: true, SHA256: digest, OwnerAccountLabel: "tester-a"}, Confirmation: none}},
		"same labels":              {req, &ProductResponse{PolicySource: &ProductPolicySource{Present: true, SHA256: digest, OwnerAccountLabel: "tester-a", CrossAccountLabel: "tester-a"}, Confirmation: none}},
		"bad label":                {req, &ProductResponse{PolicySource: &ProductPolicySource{Present: true, SHA256: digest, OwnerAccountLabel: "Tester A", CrossAccountLabel: "tester-b"}, Confirmation: none}},
		"labels while absent":      {req, &ProductResponse{PolicySource: &ProductPolicySource{OwnerAccountLabel: "tester-a", CrossAccountLabel: "tester-b"}, Confirmation: none}},
		"unknown status":           {req, &ProductResponse{PolicySource: &ProductPolicySource{}, Confirmation: &ProductConfirmationState{Present: true, Status: "maybe"}}},
		"status while absent":      {req, &ProductResponse{PolicySource: &ProductPolicySource{}, Confirmation: &ProductConfirmationState{Status: "completed"}}},
		"unreadable with reason":   {req, &ProductResponse{PolicySource: &ProductPolicySource{}, Confirmation: &ProductConfirmationState{Present: true, Status: ConfirmationUnreadable, Reason: "x"}}},
		"bad reason":               {req, &ProductResponse{PolicySource: &ProductPolicySource{}, Confirmation: &ProductConfirmationState{Present: true, Status: "disabled", Reason: "Not Wired"}}},
	} {
		if respond(c.r, c.p) == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
