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

// 제목·유형은 보지 않고, 설명·조치·근거 셋을 본다.
func TestCandidateLanguageMixedFields(t *testing.T) {
	clean := "정상 한국어 문장입니다."
	mixed := "권한 검사가 " + hanStrict + "하지 않습니다."
	if candidateLanguageMixed(clean, clean, clean) {
		t.Fatal("한글만인 후보를 섞임으로 봄")
	}
	for i, fields := range [][3]string{{mixed, clean, clean}, {clean, mixed, clean}, {clean, clean, mixed}} {
		if !candidateLanguageMixed(fields[0], fields[1], fields[2]) {
			t.Fatalf("칸 %d의 섞임을 놓침", i)
		}
	}
}

func languageCandidateTool(t *testing.T) (*candidateMemoryStore, func(string) (bool, string)) {
	store, call, _, _ := languageCandidateToolState(t)
	return store, call
}

func languageCandidateToolState(t *testing.T) (*candidateMemoryStore, func(string) (bool, string), *e2HTTPToolState, *int) {
	t.Helper()
	execution, _, binding := e2WorkerFixture(t)
	state := &e2HTTPToolState{}
	result := e2runner.Result{ToolOutput: "합성 응답", Trusted: e2runner.TrustedResult{Execution: execution.SnapshotForIntent(binding.IntentID), Final: true, ProcessBashExited: true, ProcessDrained: true, ProxyDrained: true, Classification: "completed", Call: e2proxy.CallResult{IntentID: binding.IntentID, CallRef: "lang-call", Final: true, AuditComplete: true, Counts: e2proxy.Counts{Responses: 1, ForwardAttempted: 1}, Observations: []e2proxy.HTTPObservation{{Sequence: 1, AttemptRef: "lang-attempt", Method: "GET", Endpoint: "/documents", Decision: "allowed", StatusCode: 200, ResponseComplete: true, ForwardAttempted: true, AuditComplete: true, ObservedAt: time.Now().UTC()}}}}}
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
	in := map[string]any{"call_ref": "lang-call", "vulnclass": "IDOR", "name": name, "summary": summary, "remediation": remediation}
	if rationale != "" {
		in["suggested_severity"], in["severity_rationale"] = "high", rationale
	}
	raw, _ := json.Marshal(in)
	return string(raw)
}

// 처음 섞이면 저장하지 않고 다시 쓰게 한다(오류 문구는 모델이 읽는 영어). 다시 쓴 것이 깨끗하면 표시 없이 저장한다.
func TestCandidateLanguageRewriteOnce(t *testing.T) {
	store, call := languageCandidateTool(t)
	isErr, text := call(candidateInput("권한 검사가 "+hanStrict+"하지 않습니다.", "소유자 검사를 넣습니다.", ""))
	if !isErr || len(store.candidates) != 0 || !strings.Contains(text, "diagnostic_candidate_language_mixed") {
		t.Fatalf("첫 섞임: err=%v saved=%d %q", isErr, len(store.candidates), text)
	}
	for _, r := range candidateLanguageMixedError {
		if r > 0x7e {
			t.Fatalf("모델에게 돌려주는 다시 쓰기 요청에 ASCII 밖 글자 %q", r)
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

// 다시 써도 섞이면 '다른 언어 섞임' 표시를 붙여 저장한다(회차를 막지 않는다). 근거 문장도 본다.
func TestCandidateLanguageMixedSavedWithFlag(t *testing.T) {
	store, call := languageCandidateTool(t)
	mixedRationale := "다른 계정 문서가 " + hanPublic + "되어 영향이 큽니다."
	if isErr, _ := call(candidateInput("관찰 근거", "소유자 검사를 넣습니다.", mixedRationale)); !isErr {
		t.Fatal("근거 문장 섞임을 놓침")
	}
	if isErr, text := call(candidateInput("관찰 근거", "소유자 검사를 넣습니다"+kanaText, mixedRationale)); isErr {
		t.Fatalf("두 번째 섞임에서 저장하지 않음(회차를 막음): %q", text)
	}
	if len(store.candidates) != 1 {
		t.Fatalf("저장 수 %d", len(store.candidates))
	}
	c := store.candidates[0]
	if !c.LanguageMixed || c.LanguageRewrites != 1 || c.Validate() != nil || c.Verification != "manual_review_required" {
		t.Fatalf("섞임 표시: mixed=%v rewrites=%d valid=%v", c.LanguageMixed, c.LanguageRewrites, c.Validate())
	}
	raw, _ := json.Marshal(c)
	if !strings.Contains(string(raw), `"language_mixed":true`) || !strings.Contains(string(raw), `"language_rewrites":1`) {
		t.Fatalf("저장 칸: %s", raw)
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

// 다시 쓰기 요청은 후보마다 한 번이다. 관찰이 없는 call_ref는 언어를 보기 전에 거부한다(다시 쓰기 횟수를 쓰지 않음).
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
	if isErr, _ := call(mixed); isErr {
		t.Fatal("같은 call_ref 두 번째에 또 다시 쓰게 함")
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

// 같은 호출(call_ref)에서 이름이 다른 후보는 따로 다룬다(#896 동찬 P2). 섞인 A를 붙잡은 뒤 같은 호출의 다른 후보 B를
// 내도 A를 버리지 않고, B가 섞였으면 B도 한 번 다시 쓰게 한다. Worker 끝에 A·B 모두 남는다.
func TestCandidateLanguageSameCallRefDistinctCandidates(t *testing.T) {
	store, call, state, _ := languageCandidateToolState(t)
	mixed := "권한 검사가 " + hanStrict + "하지 않습니다."
	if isErr, _ := call(namedCandidateInput("후보 A", mixed, "", "")); !isErr {
		t.Fatal("A 첫 섞임")
	}
	if isErr, text := call(namedCandidateInput("후보 B", "다른 관찰입니다.", "", "")); isErr {
		t.Fatalf("깨끗한 B 거부: %q", text)
	}
	if isErr, text := call(namedCandidateInput("후보 C", mixed, "", "")); !isErr || !strings.Contains(text, "language_mixed") {
		t.Fatalf("같은 호출의 다른 후보 C를 다시 쓴 판으로 봄: %q", text)
	}
	if len(store.candidates) != 1 || store.candidates[0].Name != "후보 B" || store.candidates[0].LanguageRewrites != 0 {
		t.Fatalf("B만 저장돼야 함: %+v", store.candidates)
	}
	state.flushHeldCandidates(t.Context())
	got := map[string]bool{}
	for _, c := range store.candidates {
		got[c.Name] = c.LanguageMixed
	}
	if len(store.candidates) != 3 || !got["후보 A"] || got["후보 B"] || !got["후보 C"] {
		t.Fatalf("Worker 끝 저장: %v", got)
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
