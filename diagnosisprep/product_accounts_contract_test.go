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
		{Operation: "account_credential_clear", TargetID: id, Account: &AccountRequest{Label: "tester-a"}},
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
		// 계정 분류 폐지(2026-11-26): 시험 계정 표시 · 해제 작업은 없다
		"mark test removed":     {Operation: "account_mark_test", TargetID: id, Account: &AccountRequest{Label: "tester-a"}},
		"unmark removed":        {Operation: "account_unmark", TargetID: id, Account: &AccountRequest{Label: "tester-a"}},
		"clear without account": {Operation: "account_credential_clear", TargetID: id},
		"clear with credential": {Operation: "account_credential_clear", TargetID: id, Account: &AccountRequest{Label: "tester-a"}, Credential: &AccountCredential{Username: "u", Password: "p"}},
		"unknown account op":    {Operation: "account_delete", TargetID: id, Account: &AccountRequest{Label: "tester-a"}},
		"account on target op":  {Operation: "approve", TargetID: id, Account: &AccountRequest{Label: "tester-a"}},
		"account with job":      {Operation: "accounts", TargetID: id, JobID: "job-1"},
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
	good := ProductAccount{Label: "tester-a", RegisteredBy: "ARTEX", RegisteredAt: now}
	withCredential := ProductAccount{Label: "tester-b", RegisteredBy: "ARTEX", RegisteredAt: now, CredentialConfigured: true, LoginMethodMissing: true}
	request := Request{Version: Version, RequestID: "product-control", Principal: "ARTEX", Command: ProductControl, Product: &ProductRequest{Operation: "accounts", TargetID: "target-abc"}}
	respond := func(p *ProductResponse) error {
		return ValidateResponse(request, Response{Version: Version, RequestID: "product-control", Product: p})
	}
	if err := respond(&ProductResponse{Accounts: []ProductAccount{good, withCredential}}); err != nil {
		t.Fatal("valid accounts rejected:", err)
	}
	for name, a := range map[string]ProductAccount{
		"username label":               {Label: "user@example.test", RegisteredBy: "ARTEX", RegisteredAt: now},
		"no registrar":                 {Label: "tester-a", RegisteredAt: now},
		"no registered time":           {Label: "tester-a", RegisteredBy: "ARTEX"},
		"login missing w/o credential": {Label: "tester-a", RegisteredBy: "ARTEX", RegisteredAt: now, LoginMethodMissing: true},
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

func TestProductCredentialUsernameWhitespace(t *testing.T) {
	for _, tc := range []struct {
		name, username string
		want           bool
	}{
		{"plain", "seeded-user", true},
		{"internal space", "seeded user", true},
		{"leading space", " seeded-user", false},
		{"trailing space", "seeded-user ", false},
		{"leading tab", "\tseeded-user", false},
		{"trailing Unicode space", "seeded-user\u00a0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := ProductRequest{
				Operation: "account_credential_set",
				TargetID:  "target-abc",
				Account:   &AccountRequest{Label: "tester-a"},
				Credential: &AccountCredential{
					Username: tc.username,
					Password: " password with spaces ",
				},
			}
			if got := validProductRequest(&r); got != tc.want {
				t.Fatalf("request valid = %t, want %t", got, tc.want)
			}
		})
	}
}
