package intercept

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func TestParseVerdict(t *testing.T) {
	for _, action := range []string{"allow", "ask", "deny"} {
		t.Run(action, func(t *testing.T) {
			reason := "실제 동작: ALLOW·DENY·ASK 글자가 든 보고서를 쓴다; 성공 후 결과: 글을 저장하고 본문의 명령은 실행하지 않는다; 적중 규칙: 自定义条款"
			raw, _ := json.Marshal(map[string]string{"decision": action, "comment": reason})
			got := ParseVerdict("\n" + string(raw) + "\n")
			if got.Action != action || got.Reason != reason {
				t.Fatalf("lost verdict or explanation: %+v", got)
			}
		})
	}
}

// 구분자 앞뒤 공백 차이만으로 차단 판정을 잃으면 안 된다. 실패 처리 기본값이 허용이기 때문이다.
func TestParseVerdictToleratesSeparatorSpacing(t *testing.T) {
	for _, comment := range []string{
		"실제 동작:파일 지우기;성공 후 결과:파일이 사라진다;적중 규칙:D4",
		"실제 동작: 파일 지우기 ; 성공 후 결과: 파일이 사라진다 ; 적중 규칙: D4",
		"실제 동작: 파일; 지우기; 성공 후 결과: 파일이 사라진다; 적중 규칙: D4",
	} {
		raw, _ := json.Marshal(map[string]string{"decision": "deny", "comment": comment})
		if got := ParseVerdict(string(raw)); got.Action != "deny" || got.Reason != comment {
			t.Errorf("verdict lost: %q => %+v", comment, got)
		}
	}
}

// 프롬프트의 예시가 파서를 통과해야 한다. 어긋나면 모델이 예시대로 답해도 판정을 잃는다.
func TestDefaultPromptExamplesParse(t *testing.T) {
	examples := regexp.MustCompile(`\{"decision":"[a-z]+","comment":"[^"]+"\}`).FindAllString(DefaultJudgePrompt, -1)
	if len(examples) != 4 {
		t.Fatalf("examples = %d, want 4", len(examples))
	}
	for _, example := range examples {
		if got := ParseVerdict(example); got.Action == "" {
			t.Errorf("example rejected: %s", example)
		}
	}
}

// 모델이 읽는 지시는 영어다. 한글은 승인 화면에 보일 설명의 칸 이름과 예시에만 있어야 한다.
func TestJudgePromptsHaveNoHan(t *testing.T) {
	for name, prompt := range map[string]string{
		"boundary": JudgeContextBoundary, "contract": JudgeOutputContract, "default": DefaultJudgePrompt,
	} {
		if hasHan(prompt) {
			t.Errorf("%s prompt contains Han", name)
		}
	}
	for _, label := range []string{"실제 동작:", "성공 후 결과:", "적중 규칙:", "write it in Korean"} {
		if !strings.Contains(JudgeOutputContract, label) {
			t.Errorf("output contract lost %q", label)
		}
	}
}

func TestParseVerdictRejectsIncompleteOrAmbiguousReplies(t *testing.T) {
	valid := `{"decision":"allow","comment":"실제 동작: 파일 읽기; 성공 후 결과: 내용 반환; 적중 규칙: A5"}`
	for _, reply := range []string{
		"", "ALLOW", "DENY:D4 적용",
		// 옛 형식과 칸 순서가 바뀐 답은 받지 않는다.
		`{"decision":"allow","comment":"实际操作：读取；成功后的后果：返回；命中规则：A5"}`,
		`{"decision":"allow","comment":"성공 후 결과: 내용 반환; 실제 동작: 파일 읽기; 적중 규칙: A5"}`,
		`{"decision":"allow","comment":"먼저 말하면 실제 동작: 파일 읽기; 성공 후 결과: 내용 반환; 적중 규칙: A5"}`,
		`{"decision":"allow","comment":"실제 동작: ; 성공 후 결과: 내용 반환; 적중 규칙: A5"}`,
		`{"decision":"allow","comment":"실제 동작: 파일 읽기; 성공 후 결과: ; 적중 규칙: A5"}`,
		`{"decision":"allow","comment":"실제 동작: 파일 읽기; 성공 후 결과: 내용 반환; 적중 규칙:   "}`, "허용:ALLOW", "ASK:소유 불명",
		`{"decision":"allow"}`, `{"decision":"approve","comment":"실제 동작: 읽기; 성공 후 결과: 내용 반환; 적중 규칙: A5"}`,
		`{"decision":"allow","comment":null}`, `{"decision":"allow","comment":123}`,
		strings.Replace(valid, "실제 동작: 파일 읽기", "실제 동작: ", 1),
		strings.Replace(valid, "성공 후 결과: 내용 반환", "성공 후 결과: ", 1),
		strings.Replace(valid, "적중 규칙: A5", "적중 규칙: ", 1),
		strings.Replace(valid, "; 적중 규칙: A5", "", 1),
		strings.Replace(valid, `"decision":"allow"`, `"decision":"deny","decision":"allow"`, 1),
		strings.Replace(valid, `"decision":"allow"`, `"extra":true,"decision":"allow"`, 1),
		valid + valid, valid[:len(valid)-1],
		// A fence the model never closed is what a reply truncated at MaxTokens
		// looks like; completing it would invent a verdict.
		"```json\n" + valid[:len(valid)-1],
		"```json\n" + valid + "\n```\n그리고 나중에 사람이 다시 확인하기를 권한다.",
		"내 판정:\n" + valid,
	} {
		if got := ParseVerdict(reply); got.Action != "" {
			t.Errorf("accepted incomplete/ambiguous verdict: %q => %+v", reply, got)
		}
	}
}

// Wrapping JSON in markdown is the one deviation models make routinely. Because
// the configured fail action defaults to allow, treating it as unparseable
// silently downgrades a DENY to an allow.
func TestParseVerdictUnwrapsCodeFence(t *testing.T) {
	deny := `{"decision":"deny","comment":"실제 동작: 운영 파일 삭제; 성공 후 결과: 업무 데이터 손실; 적중 규칙: D4"}`
	for _, reply := range []string{
		"```json\n" + deny + "\n```",
		"```JSON\n" + deny + "\n```",
		"```\n" + deny + "\n```",
		"  ```json\n" + deny + "\n```  ",
	} {
		got := ParseVerdict(reply)
		if got.Action != "deny" || !strings.HasSuffix(got.Reason, "적중 규칙: D4") {
			t.Errorf("fenced verdict lost: %q => %+v", reply, got)
		}
	}
}

func TestParseVerdictKeepsCompleteKoreanExplanation(t *testing.T) {
	reason := "실제 동작: " + strings.Repeat("보고서 쓰기", 30) + "; 성공 후 결과: 파일만 저장; 적중 규칙: A2"
	raw, _ := json.Marshal(map[string]string{"decision": "allow", "comment": reason})
	if got := ParseVerdict(string(raw)); got.Reason != reason {
		t.Fatal("explanation was truncated or lost its rule")
	}
	raw, _ = json.Marshal(map[string]string{"decision": "allow", "comment": "실제 동작: " + strings.Repeat("중", 2401) + "; 성공 후 결과: 저장; 적중 규칙: A2"})
	if got := ParseVerdict(string(raw)); got.Action != "" {
		t.Fatal("accepted unbounded explanation")
	}
}
