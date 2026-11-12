package diagnosisprep

import (
	"testing"
	"time"
)

func TestProductAccountRequestValidation(t *testing.T) {
	id := "target-abc"
	ok := []ProductRequest{
		{Operation: "accounts", TargetID: id},
		{Operation: "account_register", TargetID: id, Account: &AccountRequest{Label: "tester-a"}},
		{Operation: "account_mark_test", TargetID: id, Account: &AccountRequest{Label: "tester-a", StatementConfirmed: true}},
		{Operation: "account_unmark", TargetID: id, Account: &AccountRequest{Label: "tester-a"}},
	}
	for _, r := range ok {
		r := r
		if !validProductRequest(&r) {
			t.Errorf("valid %s rejected", r.Operation)
		}
	}
	bad := map[string]ProductRequest{
		"list with account":       {Operation: "accounts", TargetID: id, Account: &AccountRequest{Label: "tester-a"}},
		"register without target": {Operation: "account_register", Account: &AccountRequest{Label: "tester-a"}},
		"register bad label":      {Operation: "account_register", TargetID: id, Account: &AccountRequest{Label: "user@example.test"}},
		"register with statement": {Operation: "account_register", TargetID: id, Account: &AccountRequest{Label: "tester-a", StatementConfirmed: true}},
		"mark without statement":  {Operation: "account_mark_test", TargetID: id, Account: &AccountRequest{Label: "tester-a"}},
		"unmark with statement":   {Operation: "account_unmark", TargetID: id, Account: &AccountRequest{Label: "tester-a", StatementConfirmed: true}},
		"unknown account op":      {Operation: "account_delete", TargetID: id, Account: &AccountRequest{Label: "tester-a"}},
		"account on target op":    {Operation: "approve", TargetID: id, Account: &AccountRequest{Label: "tester-a"}},
		"account with job":        {Operation: "accounts", TargetID: id, JobID: "job-1"},
	}
	for name, r := range bad {
		r := r
		if validProductRequest(&r) {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestProductAccountResponseValidation(t *testing.T) {
	now := time.Now()
	good := ProductAccount{Label: "tester-a", Classification: "real", RegisteredBy: "ARTEX", RegisteredAt: now}
	test := ProductAccount{Label: "tester-b", Classification: "test", RegisteredBy: "ARTEX", RegisteredAt: now, ClassifiedBy: "ARTEX", ClassifiedAt: now, MarkedRevision: 2}
	request := Request{Version: Version, RequestID: "product-control", Principal: "ARTEX", Command: ProductControl, Product: &ProductRequest{Operation: "accounts", TargetID: "target-abc"}}
	respond := func(p *ProductResponse) error {
		return ValidateResponse(request, Response{Version: Version, RequestID: "product-control", Product: p})
	}
	if err := respond(&ProductResponse{Accounts: []ProductAccount{good, test}}); err != nil {
		t.Fatal("valid accounts rejected:", err)
	}
	for name, a := range map[string]ProductAccount{
		"unknown classification": {Label: "tester-a", Classification: "trusted", RegisteredBy: "ARTEX", RegisteredAt: now},
		"test without marker":    {Label: "tester-a", Classification: "test", RegisteredBy: "ARTEX", RegisteredAt: now},
		"test with reason":       {Label: "tester-a", Classification: "test", Reason: "manual_unmark", RegisteredBy: "ARTEX", RegisteredAt: now, ClassifiedBy: "ARTEX", ClassifiedAt: now, MarkedRevision: 2},
		"unknown reason":         {Label: "tester-a", Classification: "real", Reason: "expired", RegisteredBy: "ARTEX", RegisteredAt: now},
		"username label":         {Label: "user@example.test", Classification: "real", RegisteredBy: "ARTEX", RegisteredAt: now},
		"no registrar":           {Label: "tester-a", Classification: "real", RegisteredAt: now},
	} {
		if respond(&ProductResponse{Accounts: []ProductAccount{a}}) == nil || respond(&ProductResponse{Account: &a}) == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if respond(&ProductResponse{Accounts: []ProductAccount{good, good}}) == nil {
		t.Error("duplicate labels accepted")
	}
	many := make([]ProductAccount, MaxTargetAccounts+1)
	for i := range many {
		many[i] = good
		many[i].Label = "tester-" + string(rune('a'+i))
	}
	if respond(&ProductResponse{Accounts: many}) == nil {
		t.Error("over capacity list accepted")
	}
}
