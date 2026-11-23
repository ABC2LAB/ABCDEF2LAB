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

func TestDiagnosticTextMasksInlineAccountPairs(t *testing.T) {
	for _, tc := range []struct{ in, context string }{
		{"fixture1/synthetic-one, fixture2/synthetic-two 평문으로 노출됨", "평문으로 노출됨"},
		{"계정 비밀번호 fixture1(synthetic-one), fixture2(synthetic-two, admin=true) 노출", "admin=true"},
	} {
		got := DiagnosticText(tc.in)
		if strings.Contains(got, "synthetic-") || !strings.Contains(got, "자격정보 2건") || !strings.Contains(got, tc.context) {
			t.Errorf("보고서의 자격값 또는 관찰 근거 처리 실패: %q", got)
		}
	}
	if got := DiagnosticText("서비스 /users/v1 경로를 확인함"); got != "서비스 /users/v1 경로를 확인함" {
		t.Fatal("일반 경로를 자격값으로 처리했다")
	}
}

func TestCredentialTextMasksCredentialsButKeepsAddresses(t *testing.T) {
	in := "Set-Cookie: sid=synthetic-session-41c2; Path=/\nAuthorization: Bearer synthetic-bearer-41c2\n{\"email\":\"user@owned.test\",\"password\":\"synthetic-pass-41c2\"}\n" +
		"token=synthetic-token-41c2 eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJlLXN5bnRoZXRpYw next https://owned.test/read/item"
	out := CredentialText(in)
	for _, secret := range []string{"synthetic-session-41c2", "synthetic-bearer-41c2", "synthetic-pass-41c2", "synthetic-token-41c2", "c2lnbmF0dXJlLXN5bnRoZXRpYw"} {
		if strings.Contains(out, secret) {
			t.Fatalf("credential %s survived: %q", secret, out)
		}
	}
	for _, keep := range []string{"https://owned.test/read/item", "user@owned.test"} {
		if !strings.Contains(out, keep) {
			t.Fatalf("address %s was masked: %q", keep, out)
		}
	}
}
