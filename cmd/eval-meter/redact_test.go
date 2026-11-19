package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/dlp"
)

// redact-text는 제품과 같은 자격 가림(dlp.CredentialText)을 줄마다 적용한다. 자격이 아닌 내용은 그대로 둔다.
func TestRedactTextUsesTheProductCredentialRules(t *testing.T) {
	in := []string{
		"pip install requests",
		"curl -H 'Authorization: Bearer abc.def.ghi' http://app.eval.test:8080/x",
		"export TOKEN=s3cr3tvalue; npx thing",
		"",
	}
	raw, _ := json.Marshal(in)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-role", "redact-text"}, bytes.NewReader(raw), &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d %s", code, stderr.String())
	}
	var out []string
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil || len(out) != len(in) {
		t.Fatalf("output %q", stdout.String())
	}
	for i := range in {
		if out[i] != dlp.CredentialText(in[i]) {
			t.Fatalf("line %d: %q", i, out[i])
		}
	}
	// 자격이 없는 줄은 그대로, 인증 헤더 줄은 제품 규칙대로 줄 끝까지, 토큰 키=값은 값만 가린다
	if out[0] != in[0] || strings.Contains(out[1], "abc.def.ghi") || strings.Contains(out[2], "s3cr3tvalue") ||
		!strings.Contains(out[2], "npx thing") {
		t.Fatalf("redaction %q", out)
	}
}

// 러너는 명령 원문 전체(여러 줄)를 한 항목으로 넘기고 가린 뒤에 줄을 고른다(#736 리뷰). 줄바꿈이 든 따옴표 값도 한 항목
// 안에서는 통째로 가려져, 가린 뒤 어느 줄을 골라도 값 조각이 남지 않는다.
func TestRedactTextRedactsAQuotedValueAcrossLines(t *testing.T) {
	in := []string{"npx run --token=\"synthfirstpart\nsynthsecondpart\" && echo done"}
	raw, _ := json.Marshal(in)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-role", "redact-text"}, bytes.NewReader(raw), &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d %s", code, stderr.String())
	}
	var out []string
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil || len(out) != 1 {
		t.Fatalf("output %q", stdout.String())
	}
	if strings.Contains(out[0], "synthfirstpart") || strings.Contains(out[0], "synthsecondpart") || !strings.Contains(out[0], "npx run") {
		t.Fatalf("redaction %q", out[0])
	}
	// 줄로 나눈 뒤 가리면 첫 줄의 값 앞부분이 남는다(옛 순서의 결함 재현)
	if first := dlp.CredentialText(strings.SplitN(in[0], "\n", 2)[0]); !strings.Contains(first, "synthfirstpart") {
		t.Fatalf("split-first premise changed: %q", first)
	}
}

func TestRedactTextRejectsMalformedInput(t *testing.T) {
	long := strings.Repeat("a", maxRedactLineBytes+1)
	many, _ := json.Marshal(make([]string, maxRedactLines+1))
	longRaw, _ := json.Marshal([]string{long})
	for name, input := range map[string]string{
		"not array": `{"a":1}`, "null": `null`, "numbers": `[1]`, "trailing": `["a"] ["b"]`, "too many": string(many),
		"too long": string(longRaw), "empty": ``,
	} {
		var stderr bytes.Buffer
		if code := run([]string{"-role", "redact-text"}, strings.NewReader(input), &bytes.Buffer{}, &stderr); code != 2 ||
			!strings.HasPrefix(stderr.String(), invalidInput) {
			t.Fatalf("%s: exit %d stderr %q", name, code, stderr.String())
		}
	}
	var stderr bytes.Buffer
	if code := run([]string{"-role", "redact-text", "-listen", "127.0.0.1:0"}, strings.NewReader(`[]`), &bytes.Buffer{}, &stderr); code != 2 {
		t.Fatalf("listen flag accepted: %d", code)
	}
}
