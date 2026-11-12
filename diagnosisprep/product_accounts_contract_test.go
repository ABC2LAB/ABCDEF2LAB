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

func TestProductLoginAndCredentialRequestValidation(t *testing.T) {
	id := "target-abc"
	login := TargetLogin{Method: "POST", Path: "/read/login", Format: "json", UsernameField: "email", PasswordField: "password", Cookies: []string{"session"}}
	cred := AccountCredential{Username: "seeded-user", Password: "seeded-pass"}
	ok := []ProductRequest{
		{Operation: "target_login", TargetID: id},
		{Operation: "target_login_set", TargetID: id, Login: &login},
		{Operation: "account_credential_set", TargetID: id, Account: &AccountRequest{Label: "tester-a"}, Credential: &cred},
	}
	for _, r := range ok {
		r := r
		if !validProductRequest(&r) {
			t.Errorf("valid %s rejected", r.Operation)
		}
	}
	empty := AccountCredential{Username: "seeded-user"}
	badLogin := login
	badLogin.Method = "GET"
	bad := map[string]ProductRequest{
		"login read with body":       {Operation: "target_login", TargetID: id, Login: &login},
		"login set without body":     {Operation: "target_login_set", TargetID: id},
		"login set invalid":          {Operation: "target_login_set", TargetID: id, Login: &badLogin},
		"login with credential":      {Operation: "target_login_set", TargetID: id, Login: &login, Credential: &cred},
		"credential without account": {Operation: "account_credential_set", TargetID: id, Credential: &cred},
		"credential empty password":  {Operation: "account_credential_set", TargetID: id, Account: &AccountRequest{Label: "tester-a"}, Credential: &empty},
		"credential on register":     {Operation: "account_register", TargetID: id, Account: &AccountRequest{Label: "tester-a"}, Credential: &cred},
		"credential on target op":    {Operation: "approve", TargetID: id, Credential: &cred},
		"login on accounts":          {Operation: "accounts", TargetID: id, Login: &login},
	}
	for name, r := range bad {
		r := r
		if validProductRequest(&r) {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestProductLoginResponseValidation(t *testing.T) {
	request := Request{Version: Version, RequestID: "product-control", Principal: "ARTEX", Command: ProductControl, Product: &ProductRequest{Operation: "target_login", TargetID: "target-abc"}}
	good := TargetLogin{Method: "POST", Path: "/read/login", Format: "json", UsernameField: "email", PasswordField: "password", Cookies: []string{"session"}}
	if err := ValidateResponse(request, Response{Version: Version, RequestID: "product-control", Product: &ProductResponse{Login: &good}}); err != nil {
		t.Fatal("valid login rejected:", err)
	}
	bad := good
	bad.Header = "Authorization"
	if ValidateResponse(request, Response{Version: Version, RequestID: "product-control", Product: &ProductResponse{Login: &bad}}) == nil {
		t.Fatal("invalid login response accepted")
	}
}

func TestProductAccountStatementOnlyOnList(t *testing.T) {
	respond := func(op string, statement string) error {
		p := &ProductRequest{Operation: op, TargetID: "target-abc"}
		if op == "account_register" {
			p.Account = &AccountRequest{Label: "tester-a"}
		}
		request := Request{Version: Version, RequestID: "product-control", Principal: "ARTEX", Command: ProductControl, Product: p}
		return ValidateResponse(request, Response{Version: Version, RequestID: "product-control", Product: &ProductResponse{AccountStatement: statement}})
	}
	if err := respond("accounts", "이 계정은 시험 계정이다."); err != nil {
		t.Fatal("statement on list rejected:", err)
	}
	if respond("account_register", "이 계정은 시험 계정이다.") == nil || respond("accounts", "line\nbreak") == nil {
		t.Fatal("statement outside the list or with control characters accepted")
	}
}
