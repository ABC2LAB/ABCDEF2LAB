package e3contract_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/Autumn-27/artex/e3contract"
)

// 계정 역할 칸(사용자 결정 2026-11-19, 계약 §4): 소유(A)·교차(B) 계정 라벨과 같은 권한 등급 확인은 선언에 넣어 동결한다.
func TestDeclarationAccountRolesValidate(t *testing.T) {
	if err := declaration().Validate(); err != nil {
		t.Fatalf("역할 있는 선언: %v", err)
	}
	none := declaration()
	none.OwnerAccountLabel, none.CrossAccountLabel, none.AccountsSamePrivilege = "", "", false
	if err := none.Validate(); err != nil {
		t.Fatalf("역할 없는 선언(꺼진 확정 단계로 저장): %v", err)
	}
	for name, c := range map[string]struct {
		edit  func(*e3contract.Declaration)
		field string
	}{
		"owner_only":         {func(d *e3contract.Declaration) { d.CrossAccountLabel = "" }, "cross_account_label"},
		"cross_only":         {func(d *e3contract.Declaration) { d.OwnerAccountLabel = "" }, "cross_account_label"},
		"same_account":       {func(d *e3contract.Declaration) { d.CrossAccountLabel = d.OwnerAccountLabel }, "cross_account_label"},
		"bad_owner":          {func(d *e3contract.Declaration) { d.OwnerAccountLabel = "tester a" }, "owner_account_label"},
		"bad_cross":          {func(d *e3contract.Declaration) { d.CrossAccountLabel = "../b" }, "cross_account_label"},
		"privilege_no_roles": {func(d *e3contract.Declaration) { d.OwnerAccountLabel, d.CrossAccountLabel = "", "" }, "accounts_same_privilege"},
	} {
		d := declaration()
		c.edit(&d)
		var fe *e3contract.DeclarationFieldError
		if err := d.Validate(); !errors.As(err, &fe) || fe.Field != c.field {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// 역할이 없는 선언의 정본 바이트는 칸을 넣기 전과 같다(이미 저장한 선언의 digest가 바뀌지 않는다). 역할이 있으면 정본에
// 들어가 digest가 달라진다.
func TestDeclarationAccountRolesCanonical(t *testing.T) {
	none := declaration()
	none.OwnerAccountLabel, none.CrossAccountLabel, none.AccountsSamePrivilege = "", "", false
	raw, err := none.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"owner_account_label", "cross_account_label", "accounts_same_privilege"} {
		if bytes.Contains(raw, []byte(key)) {
			t.Fatalf("역할 없는 선언 정본에 %s", key)
		}
	}
	with, err := declaration().CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"owner_account_label":"tester-a"`, `"cross_account_label":"tester-b"`, `"accounts_same_privilege":true`} {
		if !bytes.Contains(with, []byte(want)) {
			t.Fatalf("역할 있는 선언 정본에 %s 없음", want)
		}
	}
	// 같은 권한 등급 확인을 빼면 다른 정본이다(동결본 대조가 바뀐 확인을 잡는다).
	unconfirmed := declaration()
	unconfirmed.AccountsSamePrivilege = false
	other, err := unconfirmed.CanonicalBytes()
	if err != nil || bytes.Equal(other, with) {
		t.Fatal("등급 확인이 정본에 들어가지 않음")
	}
}

// 역할이 없거나, 입력의 A·B가 선언한 역할과 다르거나, 같은 권한 등급 확인이 없으면 확정 단계는 요청 없이 꺼진다.
func TestRunAccountRolesGate(t *testing.T) {
	cases := map[string]struct {
		edit   func(*e3contract.Declaration, *e3contract.Input)
		reason e3contract.DisabledReason
	}{
		"undeclared": {func(d *e3contract.Declaration, _ *e3contract.Input) {
			d.OwnerAccountLabel, d.CrossAccountLabel, d.AccountsSamePrivilege = "", "", false
		}, e3contract.DisabledAccountRolesUndeclared},
		"privilege_unconfirmed": {func(d *e3contract.Declaration, _ *e3contract.Input) { d.AccountsSamePrivilege = false }, e3contract.DisabledPrivilegeUnconfirmed},
		// 이름순 배정처럼 역할을 뒤집으면 선언과 맞지 않아 끈다.
		"swapped": {func(_ *e3contract.Declaration, in *e3contract.Input) { in.Owner, in.Cross = in.Cross, in.Owner }, e3contract.DisabledAccountsUnverified},
		"other_cross": {func(_ *e3contract.Declaration, in *e3contract.Input) {
			in.Cross = e3contract.Account{Label: "tester-admin", Authenticated: true}
		}, e3contract.DisabledAccountsUnverified},
		// 입력 계정이 둘 다 없으면 선언을 읽고 사유를 가른다.
		"no_accounts_roles_declared": {func(_ *e3contract.Declaration, in *e3contract.Input) {
			in.Owner, in.Cross = e3contract.Account{}, e3contract.Account{}
		}, e3contract.DisabledAccountsUnverified},
		"no_accounts_no_roles": {func(d *e3contract.Declaration, in *e3contract.Input) {
			d.OwnerAccountLabel, d.CrossAccountLabel, d.AccountsSamePrivilege = "", "", false
			in.Owner, in.Cross = e3contract.Account{}, e3contract.Account{}
		}, e3contract.DisabledAccountRolesUndeclared},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			d := declaration()
			c.edit(&d, &f.in)
			src, err := e3contract.Freeze(d, frozenRef)
			if err != nil {
				t.Fatal(err)
			}
			f.decls.Stored[frozenRef], f.in.Binding.Policy = d, src
			started := 0
			deps := f.deps()
			deps.Started = func() { started++ }
			res := e3contract.Run(context.Background(), deps, f.in)
			noConfirmed(t, res)
			if res.Status != e3contract.StatusDisabled || res.Reason != string(c.reason) || len(f.fake.Calls()) != 0 || started != 0 ||
				len(f.writes.Events()) != 0 {
				t.Fatalf("%+v calls=%d started=%d", res, len(f.fake.Calls()), started)
			}
		})
	}
}

// 계정 역할 라벨의 자격 모양 검사(CredentialField, 원문만 봄). 같은 표를 웹 화면 검사 시험(web/src/workbench/__tests__/
// credential-forms.test.ts LABEL_CASES)이 그대로 쓴다 — 한쪽을 바꾸면 다른 쪽도 맞춘다. 값은 모두 합성 문자열이다.
func TestCredentialFieldAccountLabels(t *testing.T) {
	cases := []struct {
		value    string
		rejected bool
	}{
		{"tester-a", false}, {"vampi-b", false}, {"tester-a.session", false}, {"sid:1", false}, {"tester_secret", false},
		{"token%3D1", false}, {"tester token", false},
		{"Cookie: a", true}, {"cookie:x", true}, {"Bearer abc", true}, {"Basic eA==", true}, {"token=1", true},
		{"token:abc", true}, {"pwd:1", true}, {"apikey:1", true}, {"Authorization: x", true}, {"password=1", true},
		{"session_id=1", true}, {"set-cookie=1", true}, {"eyJhbGciOi.eyJzdWIi.sig", true},
	}
	for _, c := range cases {
		for _, role := range []struct {
			field string
			set   func(*e3contract.Declaration, string)
		}{
			{"owner_account_label", func(d *e3contract.Declaration, v string) { d.OwnerAccountLabel = v }},
			{"cross_account_label", func(d *e3contract.Declaration, v string) { d.CrossAccountLabel = v }},
		} {
			d := declaration()
			d.OwnerAccountLabel, d.CrossAccountLabel = "tester-a", "tester-b"
			role.set(&d, c.value)
			want := ""
			if c.rejected {
				want = role.field
			}
			if got := e3contract.CredentialField(d); got != want {
				t.Errorf("%s=%q: 거부 칸 %q, 기대 %q", role.field, c.value, got, want)
			}
		}
	}
}
