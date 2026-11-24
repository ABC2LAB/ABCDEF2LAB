package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/dlp"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/internal/diagnosticlog"
	actool "github.com/Autumn-27/norma/tool"
)

type e2DiagnosticObservation struct {
	Evidence       string
	At             time.Time
	Classification string
	Truncated      bool
}

func (s *e2HTTPToolState) observeDiagnostic(execution E2HTTPScope, intentID int64, command string, result e2runner.Result) {
	t := result.Trusted
	if t.Call.CallRef == "" || t.Call.IntentID != intentID || t.Execution.IntentID != intentID {
		return
	}
	// 모델이 증거 본문/버전/실행 상태를 채우지 못하도록 실제 도구 반환 직전에 복사한다.
	// 후보 근거는 저장 기록이라 모델에 준 비밀번호(model_visible_v1)까지 가린다.
	safeCommand := dlp.DiagnosticText(string(execution.RedactRecord([]byte(command))))
	result.ToolOutput = dlp.DiagnosticText(string(execution.RedactRecord([]byte(result.ToolOutput))))
	// JSON escaping 팽창까지 계산한다. 원문 전체를 보존했다고 주장하지 않고
	// 일부 관찰을 남겨 큰 출력만으로 후보가 사라지지 않게 한다.
	captureTruncated := false
	var raw []byte
	for {
		var err error
		raw, err = json.Marshal(struct {
			e2runner.Result
			Command          string `json:"command"`
			CaptureTruncated bool   `json:"capture_truncated"`
		}{result, safeCommand, captureTruncated})
		if err != nil {
			return
		}
		if len(raw) <= 256<<10 {
			break
		}
		captureTruncated = true
		result.Trusted.OutputTruncated = true
		if len(result.ToolOutput) > 1024 {
			result.ToolOutput = diagnosticUTF8Prefix(result.ToolOutput, len(result.ToolOutput)/2)
		} else if len(safeCommand) > 1024 {
			safeCommand = diagnosticUTF8Prefix(safeCommand, len(safeCommand)/2)
		} else {
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.observations == nil {
		s.observations = map[string]e2DiagnosticObservation{}
	}
	if len(s.observations) >= E2HTTPMaxTaskCalls {
		return
	}
	s.observations[t.Call.CallRef] = e2DiagnosticObservation{string(raw), time.Now().UTC(), t.Classification, result.Trusted.OutputTruncated}
}

type diagnosticCandidateStore interface {
	RecordDiagnosticCandidate(context.Context, db.DiagnosticCandidate) (int64, error)
}

type diagnosticCandidateFenceStore interface {
	RecordDiagnosticCandidateWithFence(context.Context, db.DiagnosticCandidate, func() (db.DiagnosticCandidateSaveFence, error)) (int64, error)
}

func newE2DiagnosticCandidateTool(store diagnosticCandidateStore, binding E2HTTPExecution, state *e2HTTPToolState, onSaved ...func()) actool.CoreTool {
	// 같은 워커 안의 중복 제출은 순차 처리하고 DB는 intent 잠금으로 중복을 제어한다.
	var mu sync.Mutex
	// languagePending은 한국어 칸 언어 섞임으로 다시 쓰기를 기다리는 후보다(e2_diagnostic_candidate_lang.go). 같은 호출·
	// 유형·증거 digest(candidateLanguagePendingKey)면 이름이 달라도 고친 판으로 본다 — 이름도 다시 쓰게 하므로 후보가 둘로
	// 갈리지 않게 한다. 같은 호출이라도 유형·증거가 다른 후보는 따로 기다린다. held는 붙잡아 둔 마지막 제출이고, 모델이 다시
	// 내지 않고 끝나면 Worker 끝에서 '다른 언어 섞임' 표시를 붙여 저장한다(후보를 잃지 않는다, flushHeldCandidates).
	// languageDone은 다시 쓰게 한 뒤 저장한 후보(저장소 기준 candidateLanguageKey)의 다시 쓰기 횟수다. 같은 후보를 다시
	// 내면 또 다시 쓰게 하지 않고 그 횟수로 저장한다.
	type languageWait struct {
		asks int
		held db.DiagnosticCandidate
	}
	languagePending := map[string]*languageWait{}
	languageDone := map[string]int{}
	saved := func(ctx context.Context, c db.DiagnosticCandidate, event func(name, status, reason, ref string)) (int64, bool) {
		var id int64
		var err error
		// Unsupported and legacy candidates keep their existing save semantics.
		if fenced, ok := store.(diagnosticCandidateFenceStore); ok && candidateverify.SupportedV2(c) {
			id, err = fenced.RecordDiagnosticCandidateWithFence(ctx, c, func() (db.DiagnosticCandidateSaveFence, error) {
				if binding.Runner == nil {
					return db.DiagnosticCandidateSaveFence{}, db.ErrDiagnosticCandidate
				}
				view := binding.Runner.SnapshotForIntent(c.IntentID)
				if !view.AuditComplete || view.IntentID != c.IntentID || view.Scope.TaskRef != strconv.FormatInt(c.TaskID, 10) || view.Scope.RunRef != c.RunRef || view.Counts.Attempts == 0 {
					return db.DiagnosticCandidateSaveFence{}, db.ErrDiagnosticCandidate
				}
				scope, marshalErr := json.Marshal(view.Scope)
				if marshalErr != nil {
					return db.DiagnosticCandidateSaveFence{}, marshalErr
				}
				return db.DiagnosticCandidateSaveFence{AttemptHighWater: view.Counts.Attempts, TaskRef: view.Scope.TaskRef, RunRef: view.Scope.RunRef, ScopeDigest: db.DiagnosticEvidenceDigest(string(scope)), PolicyRevision: view.PolicyRevision}, nil
			})
		} else {
			id, err = store.RecordDiagnosticCandidate(ctx, c)
		}
		if err != nil {
			event("candidate_saved", "failed", "candidate_store_failed", c.CallRef)
			return 0, false
		}
		event("candidate_saved", "manual_review_required", "", c.CallRef)
		for _, done := range onSaved {
			if done != nil {
				done()
			}
		}
		return id, true
	}
	if state != nil && store != nil {
		state.mu.Lock()
		state.candidateFlush = func(ctx context.Context) {
			mu.Lock()
			defer mu.Unlock()
			refs := make([]string, 0, len(languagePending))
			for ref := range languagePending {
				refs = append(refs, ref)
			}
			sort.Strings(refs)
			for _, ref := range refs {
				c := languagePending[ref].held
				delete(languagePending, ref)
				saved(ctx, c, func(name, status, reason, ref string) {
					diagnosticAgentEvent(ctx, diagnosticlog.Event{Event: name, Stage: "candidate_storage", Status: status, ReasonCode: reason, TaskID: binding.TaskID, RunRef: binding.RunRef, IntentID: binding.IntentID, CallRef: ref, Tool: "submit_diagnostic_candidate"})
				})
			}
		}
		state.mu.Unlock()
	}
	schema := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
		"suggested_severity": map[string]any{"type": "string", "enum": []string{"critical", "high", "medium", "low", "info"}},
		"severity_rationale": map[string]any{"type": "string", "maxLength": db.MaxSeverityRationaleBytes, "description": "A short single-line Korean explanation grounded in actual observed impact and known prerequisites. Keep it within 1,024 UTF-8 bytes (about 300 Korean characters); long explanations are truncated at a UTF-8 boundary with an explicit omission marker. No credentials or invented CVSS."},
		"related_call_refs":  map[string]any{"type": "array", "maxItems": 8, "items": map[string]any{"type": "string", "maxLength": 256}},
		"call_ref":           map[string]any{"type": "string", "maxLength": 256},
		"vulnclass":          map[string]any{"type": "string", "maxLength": 128},
		"verification_claim": map[string]any{"type": "string", "enum": []string{"html_nosniff_missing", "server_version_banner"}, "description": "Only for security_headers/html_nosniff_missing or information_disclosure/server_version_banner. Include a separately recorded approved GET related_call_ref for independent comparison. A software banner does not establish a vulnerable version. This is not confirmation."},
		"name":               map[string]any{"type": "string", "maxLength": 512},
		"summary":            map[string]any{"type": "string", "maxLength": 8192},
		"remediation":        map[string]any{"type": "string", "maxLength": 8192},
	}, "required": []string{"call_ref", "vulnclass", "name", "summary"}}
	// 제공자에게는 정상 문자열 스키마를 알리되, 새 선택 평가 두 칸만 잘못되어도 후보가 사라지지 않게 한다.
	// RawInput은 아래의 핵심/기존 필드 스키마·엄격 디코더·후보 결속 검사를 대신하지 않는다.
	coreProperties := map[string]any{}
	for key, value := range schema["properties"].(map[string]any) {
		if key != "suggested_severity" && key != "severity_rationale" {
			coreProperties[key] = value
		}
	}
	coreSchema := map[string]any{"type": "object", "properties": coreProperties, "required": []any{"call_ref", "vulnclass", "name", "summary"}}
	return actool.Build(actool.Spec{
		Name:        "submit_diagnostic_candidate",
		Description: "Preserve a potential vulnerability for manual review using an actual Bash call_ref from this intent. This tool cannot confirm a vulnerability, change approval, send requests, or invent evidence. For security_headers/html_nosniff_missing or information_disclosure/server_version_banner, compare the observed response with a normal alternative and include only separately recorded approved GET related_call_refs already available. A banner is not proof of a vulnerable software version. Display text must be Korean. Optionally suggest severity only from observed impact, resource sensitivity and known prerequisites in a completed HTTP observation; never infer a rating from the vulnerability class alone. Omit the estimate when evidence is insufficient. Estimates never confirm a finding.",
		Schema:      schema,
		RawInput:    true,
		Concurrent:  func(json.RawMessage) bool { return false },
		Run: func(ctx context.Context, input json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			event := func(name, status, reason, ref string) {
				diagnosticAgentEvent(ctx, diagnosticlog.Event{Event: name, Stage: "candidate_storage", Status: status, ReasonCode: reason, TaskID: binding.TaskID, RunRef: binding.RunRef, IntentID: binding.IntentID, CallRef: ref, Tool: "submit_diagnostic_candidate"})
			}
			event("candidate_submitted", "started", "", "")
			mu.Lock()
			defer mu.Unlock()
			if store == nil || state == nil || binding.Runner == nil || len(input) > 24<<10 {
				event("candidate_validated", "failed", "candidate_unavailable", "")
				return actool.Errorf("diagnostic_candidate_unavailable"), nil
			}
			execution := binding.Runner
			if execution == nil {
				event("candidate_validated", "failed", "candidate_unavailable", "")
				return actool.Errorf("diagnostic_candidate_unavailable"), nil
			}
			if actool.ValidateInput(coreSchema, input) != nil {
				event("candidate_validated", "failed", "candidate_input_invalid", "")
				return actool.Errorf("diagnostic_candidate_input_invalid"), nil
			}
			var in struct {
				SuggestedSeverity json.RawMessage `json:"suggested_severity"`
				SeverityRationale json.RawMessage `json:"severity_rationale"`
				RelatedCallRefs   []string        `json:"related_call_refs"`
				CallRef           string          `json:"call_ref"`
				VulnClass         string          `json:"vulnclass"`
				VerificationClaim string          `json:"verification_claim"`
				Name              string          `json:"name"`
				Summary           string          `json:"summary"`
				Remediation       string          `json:"remediation"`
			}
			decoder := json.NewDecoder(bytes.NewReader(input))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&in) != nil {
				event("candidate_validated", "failed", "candidate_input_invalid", "")
				return actool.Errorf("diagnostic_candidate_input_invalid"), nil
			}
			if _, err := decoder.Token(); err != io.EOF {
				event("candidate_validated", "failed", "candidate_input_invalid", "")
				return actool.Errorf("diagnostic_candidate_input_invalid"), nil
			}
			state.mu.Lock()
			observed, ok := state.observations[in.CallRef]
			var related []json.RawMessage
			seen := map[string]bool{in.CallRef: true}
			if len(in.RelatedCallRefs) > 8 {
				ok = false
			}
			sort.Strings(in.RelatedCallRefs)
			for _, ref := range in.RelatedCallRefs {
				other, found := state.observations[ref]
				if !found || seen[ref] {
					ok = false
					break
				}
				seen[ref] = true
				related = append(related, json.RawMessage(other.Evidence))
			}
			state.mu.Unlock()
			if !ok {
				event("candidate_validated", "failed", "candidate_observation_not_found", "")
				return actool.Errorf("diagnostic_candidate_observation_not_found"), nil
			}
			if len(related) > 0 {
				var envelope map[string]json.RawMessage
				if json.Unmarshal([]byte(observed.Evidence), &envelope) != nil {
					event("candidate_validated", "failed", "candidate_observation_invalid", "")
					return actool.Errorf("diagnostic_candidate_observation_invalid"), nil
				}
				rawRelated, err := json.Marshal(related)
				if err != nil {
					event("candidate_validated", "failed", "candidate_observation_invalid", "")
					return actool.Errorf("diagnostic_candidate_observation_invalid"), nil
				}
				envelope["related_calls"] = rawRelated
				combined, err := json.Marshal(envelope)
				if err != nil || len(combined) > 256<<10 {
					event("candidate_validated", "failed", "candidate_evidence_too_large", "")
					return actool.Errorf("diagnostic_candidate_evidence_too_large"), nil
				}
				observed.Evidence = string(combined)
			}
			redact := func(s string) string { return dlp.DiagnosticText(string(execution.RedactRecord([]byte(s)))) }
			candidate := db.DiagnosticCandidate{
				TaskID: binding.TaskID, IntentID: binding.IntentID, RunRef: binding.RunRef, CallRef: in.CallRef,
				VulnClass: redact(in.VulnClass), VerificationClaim: in.VerificationClaim, Name: redact(in.Name), Summary: redact(in.Summary), Remediation: redact(in.Remediation),
				Classification: observed.Classification, Verification: "manual_review_required", EvidenceVersion: 1,
				Evidence: observed.Evidence, EvidenceDigest: db.DiagnosticEvidenceDigest(observed.Evidence), ObservedAt: observed.At,
				OutputTruncated: observed.Truncated,
			}
			var severity, rationale string
			if json.Unmarshal(in.SuggestedSeverity, &severity) == nil && json.Unmarshal(in.SeverityRationale, &rationale) == nil {
				candidate.SuggestedSeverity, candidate.SeverityRationale, candidate.SeveritySource = severity, boundedSeverityRationale(redact(rationale)), db.WorkerEstimate
			}
			candidate = candidate.NormalizedSeverity()
			// 한국어 칸 언어 섞임은 저장될 글(가린 뒤)로 본다. 섞이면 붙잡아 두고 섞인 칸·조각을 짚어 다시 쓰게 하며(최대
			// db.MaxDiagnosticLanguageRewrites번), 그래도 섞이면 표시를 붙여 저장한다(회차·후보를 막지 않는다).
			findings := candidateLanguageFindings(candidate)
			mixed := len(findings) > 0
			pendingKey, doneKey := candidateLanguagePendingKey(candidate), candidateLanguageKey(candidate)
			asks, done := languageDone[doneKey]
			if wait := languagePending[pendingKey]; wait != nil {
				asks, done = wait.asks, false
			}
			if asks > 0 {
				candidate.LanguageMixed, candidate.LanguageRewrites = mixed, asks
			}
			if candidate.Validate() != nil {
				event("candidate_validated", "failed", "candidate_binding_invalid", "")
				return actool.Errorf("diagnostic_candidate_binding_invalid"), nil
			}
			if mixed && !done && asks < db.MaxDiagnosticLanguageRewrites {
				asks++
				candidate.LanguageMixed, candidate.LanguageRewrites = true, asks
				// 앞서 붙잡아 둔 제출은 버리고 이번 제출을 붙잡는다(끝까지 안 고쳐지면 가장 나중 판을 표시와 함께 저장).
				languagePending[pendingKey] = &languageWait{asks: asks, held: candidate}
				event("candidate_validated", "failed", "candidate_language_mixed", in.CallRef)
				return actool.Errorf(candidateLanguageRewriteRequest(findings, asks)), nil
			}
			event("candidate_validated", "succeeded", "", in.CallRef)
			id, ok := saved(ctx, candidate, event)
			if !ok {
				// 다시 쓴 제출을 저장하지 못하면 붙잡아 둔 첫 제출은 그대로 둔다(Worker 끝에 표시를 붙여 저장해 후보를 잃지 않는다).
				return actool.Errorf("diagnostic_candidate_store_failed"), nil
			}
			// 고친 판(또는 표시를 붙인 판)을 저장했으니 붙잡아 둔 제출은 버린다(이름이 바뀌었어도 같은 후보).
			delete(languagePending, pendingKey)
			if asks > 0 {
				languageDone[doneKey] = asks
			}
			return actool.Text("Unconfirmed candidate recorded: " + strconv.FormatInt(id, 10) + ". Manual review required; this is not an E3 verdict or confirmed finding."), nil
		},
	})
}

// 호출 전에 실행 보호 값·DLP를 먼저 가린다. 설명의 잘림은 HTTP 관찰의 잘림이 아니다.
func boundedSeverityRationale(rationale string) string {
	rationale = strings.TrimSpace(rationale)
	if len(rationale) <= db.MaxSeverityRationaleBytes {
		return rationale
	}
	for _, r := range rationale {
		if unicode.IsControl(r) {
			return ""
		}
	}
	const suffix = " … [근거 일부 생략]"
	prefix := diagnosticUTF8Prefix(rationale, db.MaxSeverityRationaleBytes-len(suffix))
	hangul := false
	for _, r := range prefix {
		hangul = hangul || unicode.In(r, unicode.Hangul)
	}
	// 안내 문장의 한글이 원래 근거의 형식 검사를 대신하지 않게 한다.
	if !hangul || !utf8.ValidString(prefix) || utf8.RuneCountInString(prefix) < 8 {
		return ""
	}
	return prefix + suffix
}

func diagnosticUTF8Prefix(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// 저장 증거는 최대 256KiB다. 모델의 상세 조회가 원문으로 문맥을 채우지 않게 한다.
func diagnosticNodeResult(n *db.Node) (actool.Result, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(n.Payload, &payload); err != nil {
		return actool.Errorf("node_payload_invalid"), nil
	}
	raw, ok := payload["diagnostic_candidate_v1"]
	if !ok {
		return jsonResult(n)
	}
	var candidate map[string]json.RawMessage
	if err := json.Unmarshal(raw, &candidate); err != nil || candidate == nil {
		return actool.Errorf("diagnostic_candidate_payload_invalid"), nil
	}
	delete(candidate, "evidence")
	candidate["evidence_omitted"] = json.RawMessage(`true`)
	var err error
	payload["diagnostic_candidate_v1"], err = json.Marshal(candidate)
	if err != nil {
		return actool.Errorf("diagnostic_candidate_payload_invalid"), nil
	}
	copy := *n
	copy.Payload, err = json.Marshal(payload)
	if err != nil {
		return actool.Errorf("node_payload_invalid"), nil
	}
	return jsonResult(&copy)
}
