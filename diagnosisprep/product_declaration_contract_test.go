package diagnosisprep

import (
	"testing"
	"time"

	"github.com/Autumn-27/artex/e3contract"
)

func declarationContractInput() *DeclarationInput {
	return &DeclarationInput{Identity: e3contract.IdentitySpec{Path: "/app/me", SubjectIDPath: "id"},
		Types: []e3contract.TypeDeclaration{{Type: "post", Sharing: e3contract.SharingShared, OwnerOnlyFields: []string{}}}}
}

func TestProductDeclarationRequestValidation(t *testing.T) {
	id := "target-abc"
	ok := []ProductRequest{
		{Operation: "declaration", TargetID: id},
		{Operation: "declaration_put", TargetID: id, ExpectedRevision: 3, Declaration: declarationContractInput()},
	}
	for _, r := range ok {
		r := r
		if !validProductRequest(&r) {
			t.Errorf("valid %s rejected", r.Operation)
		}
	}
	many := declarationContractInput()
	for len(many.Types) <= e3contract.MaxDeclarationTypes {
		many.Types = append(many.Types, many.Types[0])
	}
	bad := map[string]ProductRequest{
		"get with body":         {Operation: "declaration", TargetID: id, Declaration: declarationContractInput()},
		"get with revision":     {Operation: "declaration", TargetID: id, ExpectedRevision: 3},
		"put without revision":  {Operation: "declaration_put", TargetID: id, Declaration: declarationContractInput()},
		"put without body":      {Operation: "declaration_put", TargetID: id, ExpectedRevision: 3},
		"put without target":    {Operation: "declaration_put", ExpectedRevision: 3, Declaration: declarationContractInput()},
		"put too many types":    {Operation: "declaration_put", TargetID: id, ExpectedRevision: 3, Declaration: many},
		"put with account":      {Operation: "declaration_put", TargetID: id, ExpectedRevision: 3, Declaration: declarationContractInput(), Account: &AccountRequest{Label: "tester-a"}},
		"put with job":          {Operation: "declaration_put", TargetID: id, ExpectedRevision: 3, Declaration: declarationContractInput(), JobID: "job-1"},
		"unknown op":            {Operation: "declaration_delete", TargetID: id},
		"declaration on target": {Operation: "approve", TargetID: id, Declaration: declarationContractInput()},
		"declaration on accts":  {Operation: "accounts", TargetID: id, Declaration: declarationContractInput()},
		"declaration on login":  {Operation: "target_login", TargetID: id, Declaration: declarationContractInput()},
	}
	for name, r := range bad {
		r := r
		if validProductRequest(&r) {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestProductDeclarationResponseValidation(t *testing.T) {
	now := time.Date(2026, 11, 18, 6, 0, 0, 0, time.UTC)
	decl := e3contract.Declaration{Version: e3contract.DeclarationVersion, TargetRef: "target-abc", TargetApprovalRevision: "3",
		DeclaredBy: "ARTEX", DeclaredAt: now, Identity: e3contract.IdentitySpec{Path: "/app/me", SubjectIDPath: "id"},
		Types: []e3contract.TypeDeclaration{{Type: "post", Sharing: e3contract.SharingShared, OwnerOnlyFields: []string{}}}}
	put := Request{Version: Version, RequestID: "product-control", Principal: "ARTEX", Command: ProductControl,
		Product: &ProductRequest{Operation: "declaration_put", TargetID: "target-abc", ExpectedRevision: 3, Declaration: declarationContractInput()}}
	get := Request{Version: Version, RequestID: "product-control", Principal: "ARTEX", Command: ProductControl,
		Product: &ProductRequest{Operation: "declaration", TargetID: "target-abc"}}
	accounts := Request{Version: Version, RequestID: "product-control", Principal: "ARTEX", Command: ProductControl,
		Product: &ProductRequest{Operation: "accounts", TargetID: "target-abc"}}
	respond := func(r Request, p *ProductResponse) error {
		return ValidateResponse(r, Response{Version: Version, RequestID: "product-control", Product: p})
	}
	active := ProductDeclaration{State: DeclarationActive, TargetRevision: 3, DeclaredBy: "ARTEX", DeclaredAt: now, Declaration: &decl}
	invalid := active
	invalid.State, invalid.InvalidReason = DeclarationInvalid, "target_reapproved"
	for name, p := range map[string]*ProductResponse{
		"none":    {Declaration: &ProductDeclaration{State: DeclarationNone}},
		"active":  {Declaration: &active},
		"invalid": {Declaration: &invalid},
	} {
		if err := respond(get, p); err != nil {
			t.Fatalf("%s rejected: %v", name, err)
		}
	}
	if err := respond(put, &ProductResponse{Reason: "invalid_declaration", Field: "types[0].create.body_template"}); err != nil {
		t.Fatalf("field rejected: %v", err)
	}
	other := now.Add(time.Second)
	for name, c := range map[string]struct {
		r Request
		p *ProductResponse
	}{
		"declaration on accounts": {accounts, &ProductResponse{Declaration: &active}},
		"none with body":          {get, &ProductResponse{Declaration: &ProductDeclaration{State: DeclarationNone, Declaration: &decl}}},
		"active with reason":      {get, &ProductResponse{Declaration: &ProductDeclaration{State: DeclarationActive, InvalidReason: "target_expired", TargetRevision: 3, DeclaredBy: "ARTEX", DeclaredAt: now, Declaration: &decl}}},
		"unknown reason":          {get, &ProductResponse{Declaration: &ProductDeclaration{State: DeclarationInvalid, InvalidReason: "deleted", TargetRevision: 3, DeclaredBy: "ARTEX", DeclaredAt: now, Declaration: &decl}}},
		"declared_at mismatch":    {get, &ProductResponse{Declaration: &ProductDeclaration{State: DeclarationActive, TargetRevision: 3, DeclaredBy: "ARTEX", DeclaredAt: other, Declaration: &decl}}},
		"unknown state":           {get, &ProductResponse{Declaration: &ProductDeclaration{State: "draft"}}},
		"field on get":            {get, &ProductResponse{Reason: "invalid_declaration", Field: "types"}},
		"field without reason":    {put, &ProductResponse{Field: "types"}},
		"field with value":        {put, &ProductResponse{Reason: "invalid_declaration", Field: "password=hunter2"}},
		"declaration with reason": {put, &ProductResponse{Reason: "target_not_approved", Declaration: &active}},
	} {
		if respond(c.r, c.p) == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
