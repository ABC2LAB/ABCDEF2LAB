package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

// 한국어 칸 언어 섞임 검사(2026-11-24). 시험 입력의 한자·가나는 \u 이스케이프로 쓴다(엔진 소스에 한자를 두지 않는다).
const (
	hanStrict = "严格" // 중국어 '엄격'
	hanPublic = "公开" // 중국어 '공개'
	kanaText  = "です" // 일본어 히라가나
	kataText  = "テスト"
)

func TestMixedForeignScript(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       bool
	}{
		{"한글만", "비공개 문서가 다른 계정 응답에 노출되었습니다.", false},
		{"영문 섞인 한국어", "GET /api/users/2 응답에 email 칸이 보입니다.", false},
		{"빈 칸", "", false},
		{"한자 낱말", "권한 검사가 " + hanStrict + "하지 않습니다.", true},
		{"히라가나", "노출되었습니다" + kanaText, true},
		{"가타카나", "응답 " + kataText + " 확인", true},
		{"따옴표 인용 안", `응답 본문에 "` + hanPublic + `" 문구가 있습니다.`, false},
		{"둥근 따옴표 인용 안", "응답에 “" + hanPublic + "”가 보입니다.", false},
		{"낫표 인용 안", "응답에 「" + kanaText + "」가 보입니다.", false},
		{"인라인 코드", "응답 `msg=" + hanStrict + "` 를 확인했습니다.", false},
		{"코드 블록", "재현:\n```\ncurl -s /x # " + hanStrict + "\n```\n끝.", false},
		{"URL", "https://target.example/" + hanPublic + "/page 에서 확인했습니다.", false},
		{"경로", "요청 /files/" + hanPublic + " 응답을 확인했습니다.", false},
		{"인용 밖에도 섞임", `"` + hanPublic + `" 문구가 ` + hanStrict + `하게 보입니다.`, true},
	} {
		if got := mixedForeignScript(tc.text); got != tc.want {
			t.Errorf("%s: mixedForeignScript(%q) = %v, want %v", tc.name, tc.text, got, tc.want)
		}
	}
}

// 유형은 보지 않고, 이름·설명·조치·근거 넷을 본다. 섞인 칸마다 조각을 뽑는다.
func TestCandidateLanguageFindingsFields(t *testing.T) {
	clean := "정상 한국어 문장입니다."
	mixed := "권한 검사가 " + hanStrict + "하지 않습니다."
	base := db.DiagnosticCandidate{VulnClass: "IDOR" + hanStrict, Name: clean, Summary: clean, Remediation: clean, SeverityRationale: clean}
	if got := candidateLanguageFindings(base); len(got) != 0 {
		t.Fatalf("한글만인 후보(유형의 한자는 안 봄)를 섞임으로 봄: %+v", got)
	}
	for _, tc := range []struct {
		field string
		set   func(*db.DiagnosticCandidate)
	}{
		{"name", func(c *db.DiagnosticCandidate) { c.Name = mixed }},
		{"summary", func(c *db.DiagnosticCandidate) { c.Summary = mixed }},
		{"remediation", func(c *db.DiagnosticCandidate) { c.Remediation = mixed }},
		{"severity_rationale", func(c *db.DiagnosticCandidate) { c.SeverityRationale = mixed }},
	} {
		c := base
		tc.set(&c)
		got := candidateLanguageFindings(c)
		if len(got) != 1 || got[0].Field != tc.field || len(got[0].Fragments) != 1 || got[0].Fragments[0] != hanStrict {
			t.Fatalf("%s 칸 섞임: %+v", tc.field, got)
		}
	}
}

// 조각은 한자·가나가 이어진 부분이고, 인용 안은 빼며, 같은 조각은 한 번, 칸마다·전체 개수와 길이에 상한이 있다.
func TestForeignScriptFragments(t *testing.T) {
	han := func(i int) string { return string(rune(0x4e00 + i)) }
	text := "가" + han(1) + han(2) + "나 " + kanaText + " 다 " + han(1) + han(2) + ` "` + hanPublic + `" 라`
	got := foreignScriptFragments(text, 10)
	if strings.Join(got, "|") != han(1)+han(2)+"|"+kanaText {
		t.Fatalf("조각: %q", got)
	}
	// 가타카나 장음 부호는 낱말을 끊지 않는다.
	if got := foreignScriptFragments("응답 "+kataText+"ー"+kataText+" 확인", 10); len(got) != 1 {
		t.Fatalf("장음 부호에서 끊김: %q", got)
	}
	var many []string
	for i := range 5 {
		many = append(many, han(10+i))
	}
	if got := foreignScriptFragments(strings.Join(many, " 그리고 "), languageFragmentsPerField); len(got) != languageFragmentsPerField {
		t.Fatalf("칸 상한: %d", len(got))
	}
	long := ""
	for i := range languageFragmentRunes + 4 {
		long += han(20 + i)
	}
	if got := foreignScriptFragments("앞 "+long+" 뒤", 10); len(got) != 1 || len([]rune(got[0])) != languageFragmentRunes+3 || !strings.HasSuffix(got[0], "...") {
		t.Fatalf("길이 상한: %q", got)
	}
	five := strings.Join(many, " 그리고 ")
	c := db.DiagnosticCandidate{Name: five, Summary: five, Remediation: five, SeverityRationale: five}
	findings := candidateLanguageFindings(c)
	total := 0
	for _, f := range findings {
		total += len(f.Fragments)
	}
	if len(findings) != 4 || total != languageFragmentsTotal || len(findings[0].Fragments) != languageFragmentsPerField ||
		len(findings[1].Fragments) != languageFragmentsPerField || len(findings[2].Fragments) != 0 || len(findings[3].Fragments) != 0 {
		t.Fatalf("전체 상한(칸은 모두 남김): %+v", findings)
	}
	request := candidateLanguageRewriteRequest(findings, 1)
	if !strings.Contains(request, "remediation (more fragments omitted)") || !strings.Contains(request, "severity_rationale (more fragments omitted)") {
		t.Fatalf("상한 넘은 칸 표시: %q", request)
	}
}

func languageCandidateTool(t *testing.T) (*candidateMemoryStore, func(string) (bool, string)) {
	store, call, _, _ := languageCandidateToolState(t)
	return store, call
}

func languageCandidateToolState(t *testing.T) (*candidateMemoryStore, func(string) (bool, string), *e2HTTPToolState, *int) {
	t.Helper()
	return languageCandidateToolStateWith(t, "합성 응답")
}

// languageCandidateToolStateWith는 대상 응답(도구 출력)을 정해 관찰을 하나 남긴 진단 후보 도구다.
func languageCandidateToolStateWith(t *testing.T, targetOutput string) (*candidateMemoryStore, func(string) (bool, string), *e2HTTPToolState, *int) {
	t.Helper()
	execution, _, binding := e2WorkerFixture(t)
	state := &e2HTTPToolState{}
	result := e2runner.Result{ToolOutput: targetOutput, Trusted: e2runner.TrustedResult{Execution: execution.SnapshotForIntent(binding.IntentID), Final: true, ProcessBashExited: true, ProcessDrained: true, ProxyDrained: true, Classification: "completed", Call: e2proxy.CallResult{IntentID: binding.IntentID, CallRef: "lang-call", Final: true, AuditComplete: true, Counts: e2proxy.Counts{Responses: 1, ForwardAttempted: 1}, Observations: []e2proxy.HTTPObservation{{Sequence: 1, AttemptRef: "lang-attempt", Method: "GET", Endpoint: "/documents", Decision: "allowed", StatusCode: 200, ResponseComplete: true, ForwardAttempted: true, AuditComplete: true, ObservedAt: time.Now().UTC()}}}}}
	state.observeDiagnostic(execution, binding.IntentID, "synthetic fixture", result)
	store := &candidateMemoryStore{}
	writes := 0
	tool := newE2DiagnosticCandidateTool(store, binding, state, func() { writes++ })
	return store, func(input string) (bool, string) {
		r, err := tool.Call(t.Context(), json.RawMessage(input), nil)
		if err != nil {
			t.Fatal(err)
		}
		return r.IsError, r.Flatten()
	}, state, &writes
}

func candidateInput(summary, remediation, rationale string) string {
	return namedCandidateInput("합성 후보", summary, remediation, rationale)
}

func namedCandidateInput(name, summary, remediation, rationale string) string {
	return classedCandidateInput("IDOR", name, summary, remediation, rationale)
}

func classedCandidateInput(vulnclass, name, summary, remediation, rationale string) string {
	in := map[string]any{"call_ref": "lang-call", "vulnclass": vulnclass, "name": name, "summary": summary, "remediation": remediation}
	if rationale != "" {
		in["suggested_severity"], in["severity_rationale"] = "high", rationale
	}
	raw, _ := json.Marshal(in)
	return string(raw)
}

// 처음 섞이면 저장하지 않고, 섞인 칸과 조각을 짚어 다시 쓰게 한다(고정 문구는 모델이 읽는 영어). 다시 쓴 것이 깨끗하면
// 표시 없이 저장하고 다시 쓰기 횟수 1을 남긴다.
func TestCandidateLanguageRewriteRequestNamesFields(t *testing.T) {
	store, call := languageCandidateTool(t)
	isErr, text := call(candidateInput("권한 검사가 "+hanStrict+"하지 않습니다.", "소유자 검사를 "+kanaText+" 넣습니다.", ""))
	if !isErr || len(store.candidates) != 0 || !strings.Contains(text, "diagnostic_candidate_language_mixed") {
		t.Fatalf("첫 섞임: err=%v saved=%d %q", isErr, len(store.candidates), text)
	}
	for _, want := range []string{`summary: "` + hanStrict + `"`, `remediation: "` + kanaText + `"`, "do not translate", "wrap it in double quotes", "Rewrite request 1 of 2"} {
		if !strings.Contains(text, want) {
			t.Fatalf("다시 쓰기 요청에 %q 없음: %q", want, text)
		}
	}
	if strings.Contains(text, "name:") || strings.Contains(text, "severity_rationale:") {
		t.Fatalf("섞이지 않은 칸을 짚음: %q", text)
	}
	for _, fixed := range []string{candidateLanguageMixedError, candidateLanguageRewriteInstruction} {
		for _, r := range fixed {
			if r > 0x7e {
				t.Fatalf("모델에게 돌려주는 고정 문구에 ASCII 밖 글자 %q", r)
			}
		}
	}
	if isErr, text = call(candidateInput("권한 검사가 엄격하지 않습니다.", "소유자 검사를 넣습니다.", "")); isErr {
		t.Fatalf("다시 쓴 후보 거부: %q", text)
	}
	c := store.candidates[0]
	if c.LanguageMixed || c.LanguageRewrites != 1 || c.Validate() != nil {
		t.Fatalf("다시 쓴 후보: mixed=%v rewrites=%d valid=%v", c.LanguageMixed, c.LanguageRewrites, c.Validate())
	}
}

// 두 번째 제출도 섞이면 한 번 더 다시 쓰게 하고, 그 뒤 깨끗하면 표시 없이 횟수 2로 저장한다.
func TestCandidateLanguageRewriteTwiceThenClean(t *testing.T) {
	store, call := languageCandidateTool(t)
	if isErr, _ := call(candidateInput("권한 검사가 "+hanStrict+"하지 않습니다.", "", "")); !isErr {
		t.Fatal("첫 섞임")
	}
	isErr, text := call(candidateInput("권한 검사가 "+hanPublic+"하지 않습니다.", "", ""))
	if !isErr || !strings.Contains(text, "Rewrite request 2 of 2") || !strings.Contains(text, `"`+hanPublic+`"`) || len(store.candidates) != 0 {
		t.Fatalf("두 번째 섞임에 두 번째 요청이 아님: %q", text)
	}
	if isErr, text = call(candidateInput("권한 검사가 엄격하지 않습니다.", "", "")); isErr {
		t.Fatal(text)
	}
	if len(store.candidates) != 1 || store.candidates[0].LanguageMixed || store.candidates[0].LanguageRewrites != 2 {
		t.Fatalf("두 번 다시 쓴 뒤 저장: %+v", store.candidates)
	}
}

// 두 번 다시 써도 섞이면 '다른 언어 섞임' 표시를 붙여 저장한다(회차를 막지 않는다). 근거 문장도 본다.
func TestCandidateLanguageMixedSavedWithFlag(t *testing.T) {
	store, call := languageCandidateTool(t)
	mixedRationale := "다른 계정 문서가 " + hanPublic + "되어 영향이 큽니다."
	for i := 1; i <= db.MaxDiagnosticLanguageRewrites; i++ {
		isErr, text := call(candidateInput("관찰 근거", "소유자 검사를 넣습니다.", mixedRationale))
		if !isErr || !strings.Contains(text, "severity_rationale") || len(store.candidates) != 0 {
			t.Fatalf("%d번째 섞임: %q", i, text)
		}
	}
	if isErr, text := call(candidateInput("관찰 근거", "소유자 검사를 넣습니다"+kanaText, mixedRationale)); isErr {
		t.Fatalf("세 번째 섞임에서 저장하지 않음(회차를 막음): %q", text)
	}
	if len(store.candidates) != 1 {
		t.Fatalf("저장 수 %d", len(store.candidates))
	}
	c := store.candidates[0]
	if !c.LanguageMixed || c.LanguageRewrites != 2 || c.Validate() != nil || c.Verification != "manual_review_required" {
		t.Fatalf("섞임 표시: mixed=%v rewrites=%d valid=%v", c.LanguageMixed, c.LanguageRewrites, c.Validate())
	}
	raw, _ := json.Marshal(c)
	if !strings.Contains(string(raw), `"language_mixed":true`) || !strings.Contains(string(raw), `"language_rewrites":2`) {
		t.Fatalf("저장 칸: %s", raw)
	}
	// 저장이 끝난 같은 후보를 또 내면 다시 쓰게 하지 않고 앞의 횟수로 저장한다(같은 후보에 요청이 끝없이 쌓이지 않음).
	if isErr, text := call(candidateInput("관찰 근거", "소유자 검사를 넣습니다"+kanaText, mixedRationale)); isErr {
		t.Fatalf("저장한 후보를 다시 냈는데 또 다시 쓰게 함: %q", text)
	}
	if len(store.candidates) != 2 || !store.candidates[1].LanguageMixed || store.candidates[1].LanguageRewrites != 2 {
		t.Fatalf("같은 후보 다시 냄: %+v", store.candidates)
	}
}

// 두 번 다시 쓰게 했는데 모델이 다시 내지 않고 끝나면, 가장 나중에 붙잡은 제출을 횟수 2·표시와 함께 저장한다.
func TestCandidateLanguageHeldLatestSavedAfterTwoAsks(t *testing.T) {
	store, call, state, _ := languageCandidateToolState(t)
	if isErr, _ := call(candidateInput("첫 판 "+hanStrict, "", "")); !isErr {
		t.Fatal("첫 섞임")
	}
	if isErr, _ := call(candidateInput("둘째 판 "+hanPublic, "", "")); !isErr {
		t.Fatal("둘째 섞임")
	}
	state.flushHeldCandidates(t.Context())
	if len(store.candidates) != 1 {
		t.Fatalf("저장 %d건", len(store.candidates))
	}
	c := store.candidates[0]
	if !strings.HasPrefix(c.Summary, "둘째 판") || !c.LanguageMixed || c.LanguageRewrites != 2 || c.Validate() != nil {
		t.Fatalf("붙잡은 후보: %q mixed=%v rewrites=%d", c.Summary, c.LanguageMixed, c.LanguageRewrites)
	}
}

// 한글만이거나 인용·코드·URL 안에만 한자·가나가 있으면 다시 쓰게 하지 않고 칸도 쓰지 않는다(이전 저장 바이트와 같음).
func TestCandidateLanguageCleanUntouched(t *testing.T) {
	store, call := languageCandidateTool(t)
	summary := `응답 본문에 "` + hanPublic + `" 문구와 ` + "`" + kanaText + "`" + ` 값이 보입니다. https://t.example/` + hanStrict
	if isErr, text := call(candidateInput(summary, "소유자 검사를 넣습니다.", "")); isErr {
		t.Fatalf("인용 안 글자로 다시 쓰게 함: %q", text)
	}
	c := store.candidates[0]
	raw, _ := json.Marshal(c)
	if c.LanguageMixed || c.LanguageRewrites != 0 || strings.Contains(string(raw), "language_") {
		t.Fatalf("깨끗한 후보에 언어 칸: %s", raw)
	}
}

// 다시 쓰기 요청은 후보마다 최대 두 번이다. 관찰이 없는 call_ref는 언어를 보기 전에 거부한다(다시 쓰기 횟수를 쓰지 않음).
func TestCandidateLanguageRewritePerCallRef(t *testing.T) {
	store, call := languageCandidateTool(t)
	mixed := candidateInput("권한 검사가 "+hanStrict+"하지 않습니다.", "", "")
	other := strings.Replace(mixed, `"lang-call"`, `"invented-call"`, 1)
	if isErr, text := call(other); !isErr || !strings.Contains(text, "observation_not_found") {
		t.Fatalf("관찰 없는 call_ref: %q", text)
	}
	if isErr, text := call(mixed); !isErr || !strings.Contains(text, "language_mixed") {
		t.Fatalf("첫 섞임(지어낸 call_ref가 다시 쓰기 몫을 쓰면 안 됨): %q", text)
	}
	if isErr, text := call(mixed); !isErr || !strings.Contains(text, "2 of 2") {
		t.Fatalf("같은 call_ref 두 번째: %q", text)
	}
	if isErr, _ := call(mixed); isErr {
		t.Fatal("같은 call_ref 세 번째에 또 다시 쓰게 함")
	}
	if len(store.candidates) != 1 || !store.candidates[0].LanguageMixed {
		t.Fatalf("저장 수 %d", len(store.candidates))
	}
}

// 다시 쓰라고 했는데 모델이 다시 내지 않고 Worker가 끝나면, 붙잡아 둔 첫 제출을 표시를 붙여 저장한다(후보를 잃지 않음).
// 실행 문맥이 이미 끝났어도 저장하고, 쓰기 수에 들어가며, 두 번 저장하지 않는다.
func TestCandidateLanguageHeldSavedAtWorkerEnd(t *testing.T) {
	store, call, state, writes := languageCandidateToolState(t)
	if isErr, _ := call(candidateInput("권한 검사가 "+hanStrict+"하지 않습니다.", "", "")); !isErr || len(store.candidates) != 0 {
		t.Fatal("첫 섞임에서 바로 저장함")
	}
	ended, cancel := context.WithCancel(t.Context())
	cancel()
	state.flushHeldCandidates(ended)
	if len(store.candidates) != 1 || *writes != 1 {
		t.Fatalf("붙잡은 후보 저장: %d건, 쓰기 %d", len(store.candidates), *writes)
	}
	c := store.candidates[0]
	if !c.LanguageMixed || c.LanguageRewrites != 1 || c.Validate() != nil || c.CallRef != "lang-call" {
		t.Fatalf("붙잡은 후보: mixed=%v rewrites=%d valid=%v", c.LanguageMixed, c.LanguageRewrites, c.Validate())
	}
	state.flushHeldCandidates(t.Context())
	if len(store.candidates) != 1 {
		t.Fatal("붙잡은 후보를 두 번 저장함")
	}
}

// 다시 낸 후보를 저장했으면 붙잡아 둔 첫 제출은 버린다(같은 후보가 두 번 저장되지 않음).
func TestCandidateLanguageResubmitDropsHeld(t *testing.T) {
	store, call, state, _ := languageCandidateToolState(t)
	if isErr, _ := call(candidateInput("권한 검사가 "+hanStrict+"하지 않습니다.", "", "")); !isErr {
		t.Fatal("첫 섞임")
	}
	if isErr, text := call(candidateInput("권한 검사가 엄격하지 않습니다.", "", "")); isErr {
		t.Fatal(text)
	}
	state.flushHeldCandidates(t.Context())
	if len(store.candidates) != 1 || store.candidates[0].LanguageMixed {
		t.Fatalf("다시 낸 뒤 저장 %d건", len(store.candidates))
	}
}

// Worker 세션 끝(runE2HTTPSession)이 붙잡아 둔 후보를 저장한다: 모델이 섞인 후보를 한 번 내고 다시 내지 않은 채 끝난다.
func TestE2HTTPSessionSavesHeldCandidateAtEnd(t *testing.T) {
	execution, _, binding := e2WorkerFixture(t)
	state := &e2HTTPToolState{}
	result := e2runner.Result{ToolOutput: "합성 응답", Trusted: e2runner.TrustedResult{Execution: execution.SnapshotForIntent(binding.IntentID), Final: true, ProcessBashExited: true, ProcessDrained: true, ProxyDrained: true, Classification: "completed", Call: e2proxy.CallResult{IntentID: binding.IntentID, CallRef: "lang-call", Final: true, AuditComplete: true, Counts: e2proxy.Counts{Responses: 1, ForwardAttempted: 1}, Observations: []e2proxy.HTTPObservation{{Sequence: 1, AttemptRef: "lang-attempt", Method: "GET", Endpoint: "/documents", Decision: "allowed", StatusCode: 200, ResponseComplete: true, ForwardAttempted: true, AuditComplete: true, ObservedAt: time.Now().UTC()}}}}}
	state.observeDiagnostic(execution, binding.IntentID, "synthetic fixture", result)
	store := &candidateMemoryStore{}
	tool := newE2DiagnosticCandidateTool(store, binding, state)
	turn := 0
	provider := captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
		turn++
		events := []llm.StreamEvent{{Type: llm.SETextDelta, Text: "done"}, {Type: llm.SEMessageDelta, StopReason: "end_turn"}}
		if turn == 1 {
			events = []llm.StreamEvent{
				{Type: llm.SEToolUseStart, ToolID: "candidate-call", ToolName: tool.Name()},
				{Type: llm.SEToolInputJSON, Text: candidateInput("권한 검사가 "+hanStrict+"하지 않습니다.", "", "")},
				{Type: llm.SEMessageDelta, StopReason: "tool_use"},
			}
		}
		for _, event := range events {
			if !yield(event, nil) {
				return
			}
		}
	}}
	opts := agentcore.Options{
		Provider: provider, Tools: []actool.CoreTool{&diagnosticTool{CoreTool: tool}},
		PermissionMode: permission.ModeBypass, DisableBackgroundTasks: true, MaxTurns: 3, MaxDuration: 3 * time.Second,
	}
	w := &Worker{}
	_, _, _ = w.runE2HTTPSession(t.Context(), "worker-lang", &db.Node{ID: binding.IntentID}, nil, opts, "synthetic", execution, state, nil)
	if turn != 2 {
		t.Fatalf("합성 세션 왕복 %d", turn)
	}
	if len(store.candidates) != 1 || !store.candidates[0].LanguageMixed || store.candidates[0].LanguageRewrites != 1 {
		t.Fatalf("세션 끝에 붙잡은 후보를 저장하지 않음: %d건", len(store.candidates))
	}
}

// 이름도 다시 쓰게 한다(2026-11-24 사용자 결정). 다시 쓰기를 기다리는 동안 같은 호출·유형·증거 digest면 이름이 달라도 고친
// 판으로 보고, 붙잡아 둔 첫 제출을 버린 채 고친 것만 저장한다(후보가 둘로 갈리지 않음). 저장한 뒤 같은 호출·유형의 새 후보는
// 새로 다시 쓰기를 기다린다.
func TestCandidateLanguageRenamedRewriteReplacesHeld(t *testing.T) {
	store, call, state, _ := languageCandidateToolState(t)
	isErr, text := call(namedCandidateInput("문서 "+hanPublic+" 노출", "다른 계정 문서가 보입니다.", "", ""))
	if !isErr || !strings.Contains(text, `name: "`+hanPublic+`"`) {
		t.Fatalf("이름 섞임을 짚지 않음: %q", text)
	}
	if isErr, text = call(namedCandidateInput("문서 공개 노출", "다른 계정 문서가 보입니다.", "", "")); isErr {
		t.Fatalf("이름을 고친 판 거부: %q", text)
	}
	state.flushHeldCandidates(t.Context())
	if len(store.candidates) != 1 || store.candidates[0].Name != "문서 공개 노출" || store.candidates[0].LanguageMixed || store.candidates[0].LanguageRewrites != 1 {
		t.Fatalf("고친 판만 저장돼야 함: %+v", store.candidates)
	}
	if isErr, text = call(namedCandidateInput("다른 후보", "권한 검사가 "+hanStrict+"하지 않습니다.", "", "")); !isErr || !strings.Contains(text, "1 of 2") {
		t.Fatalf("저장 뒤 같은 호출의 새 후보가 앞 횟수를 이어받음: %q", text)
	}
}

// 같은 호출이라도 유형이 다르면 다른 후보다(#896 동찬 P2). 섞인 A를 붙잡은 동안 유형이 다른 B를 내도 A를 버리지 않는다.
func TestCandidateLanguageSameCallRefOtherClassKept(t *testing.T) {
	store, call, state, _ := languageCandidateToolState(t)
	if isErr, _ := call(classedCandidateInput("IDOR", "후보 A", "권한 검사가 "+hanStrict+"하지 않습니다.", "", "")); !isErr {
		t.Fatal("A 첫 섞임")
	}
	if isErr, text := call(classedCandidateInput("InfoDisclosure", "후보 B", "다른 관찰입니다.", "", "")); isErr {
		t.Fatalf("깨끗한 B 거부: %q", text)
	}
	if len(store.candidates) != 1 || store.candidates[0].Name != "후보 B" || store.candidates[0].LanguageRewrites != 0 {
		t.Fatalf("B만 저장돼야 함: %+v", store.candidates)
	}
	state.flushHeldCandidates(t.Context())
	got := map[string]bool{}
	for _, c := range store.candidates {
		got[c.Name] = c.LanguageMixed
	}
	if len(store.candidates) != 2 || !got["후보 A"] || got["후보 B"] {
		t.Fatalf("Worker 끝 저장: %v", got)
	}
}

// 중국어·일본어 대상: 대상 응답 글을 따옴표 없이 쓰면 그 조각을 짚어 다시 쓰게 하고, 요청은 번역하지 말고 따옴표로 감싸라고
// 한다. 따옴표로 감싼 판은 섞임이 아니고, 인용한 대상 글은 번역되지 않은 채 남으며, 저장되는 증거(대상 응답)는 관찰 그대로다.
func TestCandidateLanguageTargetTextQuotedNotTranslated(t *testing.T) {
	for _, tc := range []struct{ name, target string }{
		{"중국어 대상", "用户不存在"},       // 중국어 '사용자 없음'
		{"일본어 대상", "ユーザーが存在しません"}, // 일본어 '사용자가 없음'
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, call, state, _ := languageCandidateToolStateWith(t, `{"message":"`+tc.target+`"}`)
			observed := state.observations["lang-call"].Evidence
			if !strings.Contains(observed, tc.target) {
				t.Fatalf("관찰 증거에 대상 글 없음")
			}
			isErr, text := call(candidateInput("다른 계정으로 조회하면 "+tc.target+" 메시지가 나옵니다.", "", ""))
			if !isErr || !strings.Contains(text, `summary: "`+tc.target+`"`) || !strings.Contains(text, "do not translate") || !strings.Contains(text, "wrap it in double quotes") {
				t.Fatalf("대상 글 조각·번역 금지 요청: %q", text)
			}
			quoted := `다른 계정으로 조회하면 "` + tc.target + `" 메시지가 나옵니다.`
			if isErr, text = call(candidateInput(quoted, "", "")); isErr {
				t.Fatalf("따옴표로 감싼 판 거부: %q", text)
			}
			state.flushHeldCandidates(t.Context())
			if len(store.candidates) != 1 {
				t.Fatalf("저장 %d건", len(store.candidates))
			}
			c := store.candidates[0]
			if c.LanguageMixed || c.LanguageRewrites != 1 || c.Summary != quoted || c.Evidence != observed || c.EvidenceDigest != db.DiagnosticEvidenceDigest(observed) {
				t.Fatalf("인용·증거가 바뀜: mixed=%v rewrites=%d summary=%q evidence_same=%v", c.LanguageMixed, c.LanguageRewrites, c.Summary, c.Evidence == observed)
			}
		})
	}
}

// failingCandidateStore는 fail이 참인 동안 저장에 실패한다(DB 오류·문맥 취소).
type failingCandidateStore struct {
	candidateMemoryStore
	fail bool
}

func (s *failingCandidateStore) RecordDiagnosticCandidate(ctx context.Context, c db.DiagnosticCandidate) (int64, error) {
	if s.fail {
		return 0, context.Canceled
	}
	return s.candidateMemoryStore.RecordDiagnosticCandidate(ctx, c)
}

// 다시 쓴 제출의 저장이 실패하면 붙잡아 둔 첫 제출을 버리지 않는다(#896 재상 봇 P3). Worker 끝에 표시를 붙여 저장한다.
func TestCandidateLanguageHeldSurvivesFailedResubmitSave(t *testing.T) {
	execution, _, binding := e2WorkerFixture(t)
	state := &e2HTTPToolState{}
	result := e2runner.Result{ToolOutput: "합성 응답", Trusted: e2runner.TrustedResult{Execution: execution.SnapshotForIntent(binding.IntentID), Final: true, ProcessBashExited: true, ProcessDrained: true, ProxyDrained: true, Classification: "completed", Call: e2proxy.CallResult{IntentID: binding.IntentID, CallRef: "lang-call", Final: true, AuditComplete: true, Counts: e2proxy.Counts{Responses: 1, ForwardAttempted: 1}, Observations: []e2proxy.HTTPObservation{{Sequence: 1, AttemptRef: "lang-attempt", Method: "GET", Endpoint: "/documents", Decision: "allowed", StatusCode: 200, ResponseComplete: true, ForwardAttempted: true, AuditComplete: true, ObservedAt: time.Now().UTC()}}}}}
	state.observeDiagnostic(execution, binding.IntentID, "synthetic fixture", result)
	store := &failingCandidateStore{}
	tool := newE2DiagnosticCandidateTool(store, binding, state)
	call := func(input string) (bool, string) {
		r, err := tool.Call(t.Context(), json.RawMessage(input), nil)
		if err != nil {
			t.Fatal(err)
		}
		return r.IsError, r.Flatten()
	}
	if isErr, _ := call(candidateInput("권한 검사가 "+hanStrict+"하지 않습니다.", "", "")); !isErr {
		t.Fatal("첫 섞임")
	}
	store.fail = true
	if isErr, text := call(candidateInput("권한 검사가 엄격하지 않습니다.", "", "")); !isErr || !strings.Contains(text, "store_failed") {
		t.Fatalf("저장 실패가 오류로 안 돌아옴: %q", text)
	}
	store.fail = false
	state.flushHeldCandidates(t.Context())
	if len(store.candidates) != 1 || !store.candidates[0].LanguageMixed || store.candidates[0].LanguageRewrites != 1 {
		t.Fatalf("저장 실패 뒤 붙잡은 첫 제출을 잃음: %d건", len(store.candidates))
	}
}
