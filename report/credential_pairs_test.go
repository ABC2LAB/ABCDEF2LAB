package report

import (
	"strings"
	"testing"
)

// 11/23 VAmPI 회차 내부용 보고서에 남은 꼴(값은 합성): 따옴표 없는 계정 목록. 값만 가리고 이름·경로·주소는 남긴다.
func TestMaskCredentialPairsShapes(t *testing.T) {
	cases := []struct {
		name, in string
		hidden   []string
		kept     []string
		count    int
	}{
		{"slash list", "디버그 엔드포인트가 계정 정보를 반환합니다. alpha/s3cretA, beta/s3cretB, root/s3cretA 가 평문으로 노출됩니다.",
			[]string{"s3cretA", "s3cretB"}, []string{"alpha/", "beta/", "root/", "평문"}, 3},
		{"paren list", "노출된 계정과 비밀번호: alpha(s3cretA), beta(s3cretB), root(s3cretA, admin=true).",
			[]string{"s3cretA", "s3cretB"}, []string{"alpha(", "beta(", "root(", "admin=true"}, 3},
		{"colon list", "credentials leaked: alpha:s3cretA beta:s3cretB",
			[]string{"s3cretA", "s3cretB"}, []string{"alpha:", "beta:"}, 2},
		{"paths are not pairs", "비밀번호 목록은 GET /users/v1/_debug 와 /books/v1 응답에 있습니다.",
			nil, []string{"/users/v1/_debug", "/books/v1"}, 0},
		{"no credential word", "목록: alpha/s3cretA, beta(s3cretB)",
			nil, []string{"alpha/s3cretA", "beta(s3cretB)"}, 0},
		{"time and scheme", "비밀번호 교체는 10:30에 했고 문서는 https 로 받았습니다.",
			nil, []string{"10:30"}, 0},
		{"key form counted once", "password=fixture-pass 로 로그인합니다.",
			[]string{"fixture-pass"}, nil, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := 0
			out := diagnosticTextCounted(tc.in, &n)
			for _, h := range tc.hidden {
				if strings.Contains(out, h) {
					t.Fatalf("값이 남음 %q: %q", h, out)
				}
			}
			for _, k := range tc.kept {
				if !strings.Contains(out, k) {
					t.Fatalf("남아야 할 글이 사라짐 %q: %q", k, out)
				}
			}
			if n != tc.count {
				t.Fatalf("건수 %d, 기대 %d: %q", n, tc.count, out)
			}
		})
	}
	// 주소·이메일 가림은 자격 값이 아니라 세지 않는다.
	n := 0
	diagnosticTextCounted("문서 https://example.invalid/a 와 user@example.invalid", &n)
	if n != 0 {
		t.Fatalf("주소·이메일을 자격 값으로 셈: %d", n)
	}
}

// 보고서는 값 대신 "노출됨 n건"만 남긴다. 내부용·공유용, HTML·DOCX가 같은 건수를 쓴다.
func TestDiagnosticReportShowsExposedCredentialCount(t *testing.T) {
	in := diagnosticFixture()
	in.Entries[1].Summary = "디버그 엔드포인트가 alpha/s3cretA, beta/s3cretB 를 평문으로 노출합니다."
	in.Entries[2].Evidence = "노출된 계정 비밀번호: gamma(s3cretC)"
	for _, shared := range []bool{false, true} {
		in.Shared = shared
		prepared, err := PrepareDiagnostic(in)
		if err != nil {
			t.Fatal(err)
		}
		if prepared.ExposedCredentialValues != 3 {
			t.Fatalf("shared=%v 건수 %d", shared, prepared.ExposedCredentialValues)
		}
		body, err := DiagnosticHTML(in)
		if err != nil {
			t.Fatal(err)
		}
		s := string(body)
		for _, secret := range []string{"s3cretA", "s3cretB", "s3cretC"} {
			if strings.Contains(s, secret) {
				t.Fatalf("shared=%v 보고서에 값이 남음", shared)
			}
		}
		if !strings.Contains(s, "노출됨 3건") {
			t.Fatalf("shared=%v 노출 건수 표시 없음", shared)
		}
		docx, err := DiagnosticDOCX(in)
		if err != nil || len(docx) == 0 {
			t.Fatal("DOCX 실패", err)
		}
	}
}
