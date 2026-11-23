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
	types := []ProductDeclaredType{{Type: "note", Sharing: "owner_only", Confirmable: true}, {Type: "post", Sharing: "shared"}}
	skipped := []ProductSkippedType{{ResourceType: "post", Reason: "not_owner_only"}}
	full := make([]ProductDeclaredType, 16)
	over := make([]ProductDeclaredType, 17)
	manySkipped := make([]ProductSkippedType, 17)
	for i := range over {
		name := "t" + string(rune('a'+i))
		over[i] = ProductDeclaredType{Type: name, Sharing: "shared"}
		manySkipped[i] = ProductSkippedType{ResourceType: name, Reason: "not_owner_only"}
	}
	copy(full, over)
	source := func(types ...ProductDeclaredType) *ProductPolicySource {
		return &ProductPolicySource{Present: true, SHA256: digest, Types: types}
	}
	completed := func(skipped ...ProductSkippedType) *ProductConfirmationState {
		return &ProductConfirmationState{Present: true, Status: "completed", Skipped: skipped}
	}
	for name, p := range map[string]*ProductResponse{
		"present":   {PolicySource: source(types...), Confirmation: none},
		"absent":    {PolicySource: &ProductPolicySource{}, Confirmation: none},
		"not found": {Reason: "not_found"},
		// 제품 판정 목록 결정 2·3: 동결본의 계정 라벨 둘과 확정 단계 상태·사유.
		"labels": {PolicySource: &ProductPolicySource{Present: true, SHA256: digest, OwnerAccountLabel: "tester-a", CrossAccountLabel: "tester-b", Types: types},
			Confirmation: &ProductConfirmationState{Present: true, Status: "disabled", Reason: "declaration_missing"}},
		"completed": {PolicySource: source(types...), Confirmation: &ProductConfirmationState{Present: true, Status: "completed"}},
		// 제품 접근통제 지도 §1: 동결본 종류 요약(상한 16)과 확정 단계가 건너뛴 종류.
		"types max":   {PolicySource: source(full...), Confirmation: none},
		"skipped":     {PolicySource: source(types...), Confirmation: completed(skipped...)},
		"skipped max": {PolicySource: source(types...), Confirmation: completed(manySkipped[:16]...)},
		"unreadable":  {PolicySource: &ProductPolicySource{}, Confirmation: &ProductConfirmationState{Present: true, Status: ConfirmationUnreadable}},
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
		"upper digest":             {req, &ProductResponse{PolicySource: &ProductPolicySource{Present: true, SHA256: strings.Repeat("A", 64), Types: types}, Confirmation: none}},
		"short digest":             {req, &ProductResponse{PolicySource: &ProductPolicySource{Present: true, SHA256: digest[:63], Types: types}, Confirmation: none}},
		"with reason":              {req, &ProductResponse{Reason: "not_found", PolicySource: &ProductPolicySource{}}},
		"on other op":              {other, &ProductResponse{PolicySource: &ProductPolicySource{}}},
		"no confirmation":          {req, &ProductResponse{PolicySource: &ProductPolicySource{}}},
		"confirmation only":        {req, &ProductResponse{Confirmation: none}},
		"confirmation with reason": {req, &ProductResponse{Reason: "not_found", Confirmation: none}},
		"confirmation other op":    {other, &ProductResponse{Confirmation: none}},
		"one label":                {req, &ProductResponse{PolicySource: &ProductPolicySource{Present: true, SHA256: digest, OwnerAccountLabel: "tester-a", Types: types}, Confirmation: none}},
		"same labels":              {req, &ProductResponse{PolicySource: &ProductPolicySource{Present: true, SHA256: digest, OwnerAccountLabel: "tester-a", CrossAccountLabel: "tester-a", Types: types}, Confirmation: none}},
		"bad label":                {req, &ProductResponse{PolicySource: &ProductPolicySource{Present: true, SHA256: digest, OwnerAccountLabel: "Tester A", CrossAccountLabel: "tester-b", Types: types}, Confirmation: none}},
		"labels while absent":      {req, &ProductResponse{PolicySource: &ProductPolicySource{OwnerAccountLabel: "tester-a", CrossAccountLabel: "tester-b"}, Confirmation: none}},
		"unknown status":           {req, &ProductResponse{PolicySource: &ProductPolicySource{}, Confirmation: &ProductConfirmationState{Present: true, Status: "maybe"}}},
		"status while absent":      {req, &ProductResponse{PolicySource: &ProductPolicySource{}, Confirmation: &ProductConfirmationState{Status: "completed"}}},
		"unreadable with reason":   {req, &ProductResponse{PolicySource: &ProductPolicySource{}, Confirmation: &ProductConfirmationState{Present: true, Status: ConfirmationUnreadable, Reason: "x"}}},
		"bad reason":               {req, &ProductResponse{PolicySource: &ProductPolicySource{}, Confirmation: &ProductConfirmationState{Present: true, Status: "disabled", Reason: "Not Wired"}}},
		// 종류 요약: present=true면 1개 이상 16개 이하, type 엄격 오름차순, sharing은 두 값, confirmable ⇔ owner_only(두 방향).
		"present no types":         {req, &ProductResponse{PolicySource: source(), Confirmation: none}},
		"types while absent":       {req, &ProductResponse{PolicySource: &ProductPolicySource{Types: types}, Confirmation: none}},
		"types over max":           {req, &ProductResponse{PolicySource: source(over...), Confirmation: none}},
		"types unsorted":           {req, &ProductResponse{PolicySource: source(types[1], types[0]), Confirmation: none}},
		"types duplicate":          {req, &ProductResponse{PolicySource: source(types[0], types[0]), Confirmation: none}},
		"type bad name":            {req, &ProductResponse{PolicySource: source(ProductDeclaredType{Type: "Note", Sharing: "shared"}), Confirmation: none}},
		"type empty name":          {req, &ProductResponse{PolicySource: source(ProductDeclaredType{Sharing: "shared"}), Confirmation: none}},
		"type bad sharing":         {req, &ProductResponse{PolicySource: source(ProductDeclaredType{Type: "note", Sharing: "private"}), Confirmation: none}},
		"type no sharing":          {req, &ProductResponse{PolicySource: source(ProductDeclaredType{Type: "note"}), Confirmation: none}},
		"owner_only unconfirmable": {req, &ProductResponse{PolicySource: source(ProductDeclaredType{Type: "note", Sharing: "owner_only"}), Confirmation: none}},
		"shared confirmable":       {req, &ProductResponse{PolicySource: source(ProductDeclaredType{Type: "post", Sharing: "shared", Confirmable: true}), Confirmation: none}},
		// 건너뛴 종류: 파일이 없거나 unreadable이면 비어 있고, 16개 이하·종류 이름 모양·중복 없음·사유 코드 모양이다.
		"skipped while absent": {req, &ProductResponse{PolicySource: source(types...), Confirmation: &ProductConfirmationState{Skipped: skipped}}},
		"skipped unreadable":   {req, &ProductResponse{PolicySource: source(types...), Confirmation: &ProductConfirmationState{Present: true, Status: ConfirmationUnreadable, Skipped: skipped}}},
		"skipped over max":     {req, &ProductResponse{PolicySource: source(types...), Confirmation: completed(manySkipped...)}},
		"skipped duplicate":    {req, &ProductResponse{PolicySource: source(types...), Confirmation: completed(skipped[0], skipped[0])}},
		"skipped bad type":     {req, &ProductResponse{PolicySource: source(types...), Confirmation: completed(ProductSkippedType{ResourceType: "Post", Reason: "not_owner_only"})}},
		"skipped bad reason":   {req, &ProductResponse{PolicySource: source(types...), Confirmation: completed(ProductSkippedType{ResourceType: "post", Reason: "Not Owner"})}},
		"skipped no reason":    {req, &ProductResponse{PolicySource: source(types...), Confirmation: completed(ProductSkippedType{ResourceType: "post"})}},
	} {
		if respond(c.r, c.p) == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
