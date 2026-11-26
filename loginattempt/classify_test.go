package loginattempt

import (
	"bytes"
	"mime/multipart"
	"strings"
	"testing"
)

var testAccounts = []Account{
	{Label: "tester-a", Username: "user1@owned.test", Password: "alpha-pass-1"},
	{Label: "tester-b", Username: "user2", Password: "bravo-pass-2"},
}

func TestClassifyBodyLoginShapes(t *testing.T) {
	for name, tc := range map[string]struct {
		ct, body                string
		login                   bool
		idField, pwField, label string
		pair                    bool
		format                  string
	}{
		"form name rule":        {"application/x-www-form-urlencoded", "userName=user2&password=bravo-pass-2&_csrf=x", true, "userName", "password", "tester-b", true, FormatForm},
		"form wrong password":   {"application/x-www-form-urlencoded", "email=user1%40owned.test&password=nope", true, "email", "password", "tester-a", false, FormatForm},
		"json nested":           {"application/json", `{"user":{"email":"user1@owned.test","password":"alpha-pass-1"}}`, true, "user.email", "user.password", "tester-a", true, FormatJSON},
		"json camel":            {"application/json", `{"loginId":"someone","loginPassword":"x"}`, true, "", "loginPassword", "", false, FormatJSON},
		"registered value only": {"application/json", `{"handle":"user2","secret":"bravo-pass-2"}`, true, "handle", "secret", "tester-b", true, FormatJSON},
		"password change":       {"application/json", `{"oldPassword":"alpha-pass-1","newPassword":"other-pass"}`, false, "", "", "", false, ""},
		"signup confirm":        {"application/x-www-form-urlencoded", "username=new&password=a&passwordConfirm=a", false, "", "", "", false, ""},
		"no password":           {"application/json", `{"q":"search"}`, false, "", "", "", false, ""},
		"array body":            {"application/json", `[{"password":"x"}]`, false, "", "", "", false, ""},
		"text body":             {"text/plain", "password=alpha-pass-1", false, "", "", "", false, ""},
		"bad escape":            {"application/x-www-form-urlencoded", "user=%zz&password=x", false, "", "", "", false, ""},
		"id full name":          {"application/json", `{"id":"user2","pwd":"bravo-pass-2"}`, true, "id", "pwd", "tester-b", true, FormatJSON},
		"id end piece ignored":  {"application/json", `{"csrf_id":"zzz","pass":"x"}`, true, "", "pass", "", false, FormatJSON},
	} {
		t.Run(name, func(t *testing.T) {
			c, ok := Classify("POST", tc.ct, []byte(tc.body), testAccounts, nil)
			if ok != tc.login {
				t.Fatalf("login=%v", ok)
			}
			if !ok {
				return
			}
			if c.IdentifierField != tc.idField || c.PasswordField != tc.pwField || c.AccountLabel != tc.label || c.Pair != tc.pair || c.Format != tc.format {
				t.Fatalf("id=%q pw=%q label=%q pair=%v format=%q", c.IdentifierField, c.PasswordField, c.AccountLabel, c.Pair, c.Format)
			}
		})
	}
}

// 아이디 칸은 규칙 순서가 먼저다: 등록 아이디와 값이 같은 칸이 이름 규칙 칸보다 앞선다. 앞 응답에 그대로 있던 값(CSRF 토큰)은
// 아이디로 고르지 않고 fields_from_response로 남긴다.
func TestClassifyIdentifierOrderAndResponseFields(t *testing.T) {
	body := `{"email":"decoy@x.test","account_name":"user2","token":"csrf-token-1234567","password":"bravo-pass-2"}`
	c, ok := Classify("POST", "application/json", []byte(body), testAccounts, func(v string) bool { return v == "csrf-token-1234567" })
	if !ok || c.IdentifierField != "account_name" || c.AccountLabel != "tester-b" {
		t.Fatalf("id=%q label=%q", c.IdentifierField, c.AccountLabel)
	}
	if strings.Join(c.FieldsFromResponse, ",") != "token" || strings.Join(c.OtherFields, ",") != "email,token" {
		t.Fatalf("from=%v other=%v", c.FieldsFromResponse, c.OtherFields)
	}
	csrfOnly := `{"login":"csrf-token-1234567","password":"x"}`
	c, ok = Classify("POST", "application/json", []byte(csrfOnly), testAccounts, func(v string) bool { return v == "csrf-token-1234567" })
	if !ok || c.IdentifierField != "" {
		t.Fatalf("응답에서 받은 값을 아이디로 고름: %q", c.IdentifierField)
	}
}

func TestClassifyMultipartAndMethods(t *testing.T) {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	w.WriteField("username", "user2")
	w.WriteField("password", "bravo-pass-2")
	w.Close()
	c, ok := Classify("POST", w.FormDataContentType(), b.Bytes(), testAccounts, nil)
	if !ok || c.Format != FormatMultipart || !c.Pair {
		t.Fatal("multipart 로그인을 못 가림")
	}
	if _, ok := Classify("PUT", w.FormDataContentType(), b.Bytes(), testAccounts, nil); ok {
		t.Fatal("POST가 아닌 요청을 로그인으로 봄")
	}
	var f bytes.Buffer
	fw := multipart.NewWriter(&f)
	fw.WriteField("username", "user2")
	fw.WriteField("password", "bravo-pass-2")
	part, _ := fw.CreateFormFile("avatar", "a.png")
	part.Write([]byte("png"))
	fw.Close()
	if _, ok := Classify("POST", fw.FormDataContentType(), f.Bytes(), testAccounts, nil); ok {
		t.Fatal("파일 부분이 있는 본문을 로그인으로 봄")
	}
}

func TestEndPiece(t *testing.T) {
	for in, want := range map[string]string{"user[password]": "password", "loginPassword": "password", "user.pwd": "pwd", "old-pass": "pass", "PASSWORD": "password", "csrf_id": "id", "": ""} {
		if got := endPiece(in); got != want {
			t.Fatalf("%q → %q, want %q", in, got, want)
		}
	}
}

func TestAccountValuesDoNotFormat(t *testing.T) {
	c, _ := Classify("POST", "application/json", []byte(`{"username":"user2","password":"bravo-pass-2"}`), testAccounts, nil)
	for _, s := range []string{testAccounts[1].String(), c.String()} {
		if strings.Contains(s, "bravo-pass-2") {
			t.Fatal("값이 문자열로 나옴")
		}
	}
}
