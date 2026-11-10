package dlp

import (
	"strings"
	"testing"
)

func TestDiagnosticTextKnownCredentialShapes(t *testing.T) {
	for _, input := range []string{
		"Authorization: Bearer synthetic-auth",
		"Set-Cookie: session=synthetic-cookie; HttpOnly",
		"password=synthetic-password",
		`{"access_token":"synthetic-token"}`,
		"https://user:synthetic-password@example.invalid/path?key=synthetic-token",
		"fixture@example.invalid",
		"-----BEGIN PRIVATE KEY-----\nsynthetic-private\n-----END PRIVATE KEY-----",
	} {
		got := DiagnosticText(input)
		if strings.Contains(got, "synthetic-") || strings.Contains(got, "fixture@example.invalid") {
			t.Fatalf("known secret not protected: %q", got)
		}
	}
	input := "SQLi 후보: 실제 응답 오류 차이. 요청 1/응답 2. 추가 검토 필요."
	if got := DiagnosticText(input); got != input {
		t.Fatal("ordinary diagnostic text lost")
	}
}

func TestDiagnosticTextCredentialDelimiters(t *testing.T) {
	cases := []struct {
		input   string
		secrets []string
	}{
		{"Cookie: first=one; second=secret-two", []string{"one", "secret-two"}},
		{"Set-Cookie: first=one; second=secret-two, third=secret-three", []string{"one", "secret-two", "secret-three"}},
		{"Authorization: Digest username=private-user, nonce=private-nonce", []string{"private-user", "private-nonce"}},
		{"Cookie: first=private-one\r\n second=private-two\r\nobserved response", []string{"private-one", "private-two"}},
		{`password="private-a;private-b,private-c"`, []string{"private-a", "private-b", "private-c"}},
		{`password='private-a;private-b,private-c'`, []string{"private-a", "private-b", "private-c"}},
		{`{"password":"private-a;private-b","token":"private-c,private-d","api_key":"private-e","summary":"ordinary"}`, []string{"private-a", "private-b", "private-c", "private-d", "private-e"}},
		{`{"password":"private-a\";private-b","client_secret":"private-c"}`, []string{"private-a", "private-b", "private-c"}},
	}
	for _, tc := range cases {
		got := DiagnosticText(tc.input)
		for _, secret := range tc.secrets {
			if strings.Contains(got, secret) {
				t.Errorf("credential suffix leaked for input shape: %q", got)
			}
		}
	}
	if got := DiagnosticText("Cookie: first=private-one; second=private-two\nobserved response"); !strings.Contains(got, "observed response") {
		t.Fatal("separate ordinary observation removed")
	}
}
