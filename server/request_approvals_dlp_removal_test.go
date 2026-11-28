package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Autumn-27/artex/e2proxy"
)

// 승인 미리보기 가림 걷기(민준 결정 2026-11-27, 절대규칙 2 DLP 폐기): 경로 · 질의 · 본문을 다 보이고, 이름에 비밀 낱말이 든
// 칸도 빼지 않으며, 호스트의 공개 지정(ApprovalDisplay)과 '표시 제한' 문장을 쓰지 않는다. 남는 가림은 실행의 저장 기록
// 가림(RedactRecord: 우리 제어 비밀 · E③ 표식)뿐이다. 긴 값은 조용히 빼지 않고 앞부분과 잘린 바이트 수를 남긴다.

// approvalTestRedact는 실행의 RedactRecord 대신 쓰는 합성 가림이다(제어 비밀 한 값).
func approvalTestRedact(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte("synthetic-control-secret"), []byte("[REDACTED]"))
}

func approvalTestProposal(rawURL string, body []byte) e2proxy.RequestApprovalProposal {
	return e2proxy.RequestApprovalProposal{Method: "POST", URL: rawURL, Body: body, Redact: approvalTestRedact}
}

func TestApprovalPreviewShowsWholeRequest(t *testing.T) {
	body := []byte(`{"username":"tester-a","password":"seeded-pass-77","session_token":"target-issued-session","profile":{"role":"admin"},"tags":["a","b"]}`)
	p := approvalTestProposal("https://owned.test/api/users/token=abc123/eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.c2ln?api_key=k-77&page=2", body)
	// 공개 지정 없는 규칙(지금 제품 규칙과 같음)
	origin, path, preview, err := approvalPreview(p)
	if err != nil || origin != "https://owned.test" || path != "/api/users/token=abc123/eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.c2ln" {
		t.Fatalf("경로를 가림: %q %q %v", origin, path, err)
	}
	want := map[string]string{
		"query.api_key":      `["k-77"]`,
		"query.page":         `["2"]`,
		"body.username":      `"tester-a"`,
		"body.password":      `"seeded-pass-77"`,
		"body.session_token": `"target-issued-session"`,
		"body.profile":       `{"role":"admin"}`,
		"body.tags":          `["a","b"]`,
	}
	if fmt.Sprint(preview) != fmt.Sprint(want) {
		t.Fatalf("미리보기:\n got %v\nwant %v", preview, want)
	}
	// JSON 이 아닌 본문은 통째로 한 칸이다
	_, _, form, err := approvalPreview(approvalTestProposal("https://owned.test/login", []byte("user=tester-a&password=seeded-pass-77")))
	if err != nil || form["body"] != "user=tester-a&password=seeded-pass-77" || len(form) != 1 {
		t.Fatalf("폼 본문: %v %v", form, err)
	}
	// 질의 · 본문이 없으면 빈 미리보기다(안내 문장 없음)
	_, _, empty, err := approvalPreview(approvalTestProposal("https://owned.test/api/users/7", nil))
	if err != nil || len(empty) != 0 {
		t.Fatalf("빈 요청 미리보기: %v %v", empty, err)
	}
}

func TestApprovalPreviewKeepsOnlyControlSecretMasking(t *testing.T) {
	body := []byte(`{"note":"synthetic-control-secret","synthetic-control-secret":"x"}`)
	p := approvalTestProposal("https://owned.test/a/synthetic-control-secret?q=synthetic-control-secret&synthetic-control-secret=1", body)
	_, path, preview, err := approvalPreview(p)
	raw, _ := json.Marshal(map[string]any{"path": path, "preview": preview})
	if err != nil || strings.Contains(string(raw), "synthetic-control-secret") || !strings.Contains(path, "[REDACTED]") {
		t.Fatalf("제어 비밀이 미리보기에 남음: %s %v", raw, err)
	}
	// 가림 함수가 없는 제안은 받지 않는다(가림 없이 남기지 않는다)
	p.Redact = nil
	if _, _, _, err := approvalPreview(p); err == nil {
		t.Fatal("가림 함수 없는 제안을 받음")
	}
}

func TestApprovalPreviewTruncatesWithoutDropping(t *testing.T) {
	long := strings.Repeat("가", 200) // 600 바이트
	p := approvalTestProposal("https://owned.test/a?q="+long, []byte(`{"bio":"`+long+`"}`))
	_, _, preview, err := approvalPreview(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"query.q", "body.bio"} {
		v, ok := preview[key]
		if !ok {
			t.Fatalf("%s 를 뺌", key)
		}
		if !utf8.ValidString(v) || !strings.Contains(v, "바이트 잘림") || len(v) > approvalPreviewValueBytes+64 {
			t.Fatalf("%s 잘림 표시: %q", key, v)
		}
	}
	// 잘린 바이트 수가 맞다: 질의 값 JSON(["가…"]) 길이 - 남긴 앞부분
	v := preview["query.q"]
	kept := v[:strings.Index(v, "…")]
	full, _ := json.Marshal([]string{long})
	if !strings.HasSuffix(v, fmt.Sprintf("(뒤 %d바이트 잘림)", len(full)-len(kept))) || !strings.HasPrefix(string(full), kept) {
		t.Fatalf("잘린 바이트 수: %q", v)
	}
	// 칸이 상한보다 많으면 앞 칸만 남기고 남은 칸 수를 적는다(조용히 빼지 않는다)
	fields := map[string]int{}
	for i := 0; i < approvalPreviewEntries+5; i++ {
		fields[fmt.Sprintf("f%03d", i)] = i
	}
	many, _ := json.Marshal(fields)
	_, _, preview, err = approvalPreview(approvalTestProposal("https://owned.test/a", many))
	if err != nil || len(preview) != approvalPreviewEntries+1 || preview[approvalPreviewOmittedKey] != "칸 5개 더 있음" {
		t.Fatalf("칸 상한: %d %q %v", len(preview), preview[approvalPreviewOmittedKey], err)
	}
}

// 같은 이름의 최상위 칸이 여러 번 있으면 모두 원문 순서대로 남긴다. map으로 풀면 앞 칸이 사라져 대상 파서에 따라 실제로 쓰일
// 값이 숨는다(#990 이동찬 P1).
func TestApprovalPreviewKeepsDuplicateBodyNames(t *testing.T) {
	_, _, preview, err := approvalPreview(approvalTestProposal("https://owned.test/transfer", []byte(`{"amount":100000,"to":"b","amount":1}`)))
	if err != nil {
		t.Fatal(err)
	}
	if preview["body.amount"] != "100000" || preview["body.amount (2번째)"] != "1" || preview["body.to"] != `"b"` || len(preview) != 3 {
		t.Fatalf("같은 이름 칸: %q", preview)
	}
	// 객체 뒤에 다른 값이 붙었거나 객체가 아니면 본문 원문 한 칸이다(풀린 앞 객체만 보이지 않게).
	for _, body := range []string{`{"a":1} {"a":2}`, `{"a":1`, `[1,2]`, `null`} {
		_, _, preview, err := approvalPreview(approvalTestProposal("https://owned.test/x", []byte(body)))
		if err != nil || preview["body"] != body || len(preview) != 1 {
			t.Fatalf("%s → %q %v", body, preview, err)
		}
	}
}

// 256바이트로 자른 이름이나 가린 뒤 이름이 같아져도 한 칸이 다른 칸을 덮지 않는다(#990 이동찬 P2).
func TestApprovalPreviewKeysStayDistinct(t *testing.T) {
	prefix := strings.Repeat("p", approvalPreviewValueBytes+40)
	_, _, preview, err := approvalPreview(approvalTestProposal("https://owned.test/q?"+prefix+"a=first&"+prefix+"b=second", nil))
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]bool{}
	for _, v := range preview {
		values[v] = true
	}
	if len(preview) != 2 || !values[`["first"]`] || !values[`["second"]`] {
		t.Fatalf("잘린 이름이 겹쳐 칸이 줄어듦: %d칸", len(preview))
	}
	_, _, preview, err = approvalPreview(approvalTestProposal("https://owned.test/x", []byte(`{"synthetic-control-secret":1,"[REDACTED]":2}`)))
	if err != nil || len(preview) != 2 || preview["body.[REDACTED]"] != "1" || preview["body.[REDACTED] (2번째)"] != "2" {
		t.Fatalf("가린 뒤 이름이 겹쳐 칸이 줄어듦: %q %v", preview, err)
	}
	for k, v := range preview {
		if strings.Contains(k+v, "synthetic-control-secret") {
			t.Fatal("제어 비밀이 남음")
		}
	}
}

// 질의를 칸으로 풀지 못하면 풀린 칸만 보이지 않게 원문 한 칸으로 싣는다.
func TestApprovalPreviewShowsUnparsableQueryWhole(t *testing.T) {
	_, _, preview, err := approvalPreview(approvalTestProposal("https://owned.test/q?a=%zz&b=1", nil))
	if err != nil || preview["query"] != "a=%zz&b=1" || len(preview) != 1 {
		t.Fatalf("못 푸는 질의: %q %v", preview, err)
	}
}
