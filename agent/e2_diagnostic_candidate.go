package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/dlp"
	"github.com/Autumn-27/artex/e2proxy"
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

func (s *e2HTTPToolState) observeDiagnostic(execution *e2proxy.Execution, intentID int64, command string, result e2runner.Result) {
	t := result.Trusted
	if t.Call.CallRef == "" || t.Call.IntentID != intentID || t.Execution.IntentID != intentID {
		return
	}
	// 모델이 증거 본문/버전/실행 상태를 채우지 못하도록 실제 도구 반환 직전에 복사한다.
	safeCommand := dlp.DiagnosticText(string(execution.RedactOutput([]byte(command))))
	result.ToolOutput = dlp.DiagnosticText(string(execution.RedactOutput([]byte(result.ToolOutput))))
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

func newE2DiagnosticCandidateTool(store diagnosticCandidateStore, binding E2HTTPExecution, state *e2HTTPToolState, onSaved ...func()) actool.CoreTool {
	// 같은 워커 안의 중복 제출은 순차 처리하고 DB는 intent 잠금으로 중복을 제어한다.
	var mu sync.Mutex
	return actool.Build(actool.Spec{
		Name:        "submit_diagnostic_candidate",
		Description: "Preserve a potential vulnerability for manual review using an actual Bash call_ref from this intent. This tool cannot confirm a vulnerability, change approval, send requests, or invent evidence. Display text must be Korean.",
		Schema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"related_call_refs": map[string]any{"type": "array", "maxItems": 8, "items": map[string]any{"type": "string", "maxLength": 256}},
			"call_ref":          map[string]any{"type": "string", "maxLength": 256},
			"vulnclass":         map[string]any{"type": "string", "maxLength": 128},
			"name":              map[string]any{"type": "string", "maxLength": 512},
			"summary":           map[string]any{"type": "string", "maxLength": 8192},
			"remediation":       map[string]any{"type": "string", "maxLength": 8192},
		}, "required": []string{"call_ref", "vulnclass", "name", "summary"}},
		Concurrent: func(json.RawMessage) bool { return false },
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
			execution := binding.Runner.Execution()
			if execution == nil {
				event("candidate_validated", "failed", "candidate_unavailable", "")
				return actool.Errorf("diagnostic_candidate_unavailable"), nil
			}
			var in struct {
				RelatedCallRefs []string `json:"related_call_refs"`
				CallRef         string   `json:"call_ref"`
				VulnClass       string   `json:"vulnclass"`
				Name            string   `json:"name"`
				Summary         string   `json:"summary"`
				Remediation     string   `json:"remediation"`
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
			redact := func(s string) string { return dlp.DiagnosticText(string(execution.RedactOutput([]byte(s)))) }
			candidate := db.DiagnosticCandidate{
				TaskID: binding.TaskID, IntentID: binding.IntentID, RunRef: binding.RunRef, CallRef: in.CallRef,
				VulnClass: redact(in.VulnClass), Name: redact(in.Name), Summary: redact(in.Summary), Remediation: redact(in.Remediation),
				Classification: observed.Classification, Verification: "manual_review_required", EvidenceVersion: 1,
				Evidence: observed.Evidence, EvidenceDigest: db.DiagnosticEvidenceDigest(observed.Evidence), ObservedAt: observed.At,
				OutputTruncated: observed.Truncated,
			}
			if candidate.Validate() != nil {
				event("candidate_validated", "failed", "candidate_binding_invalid", "")
				return actool.Errorf("diagnostic_candidate_binding_invalid"), nil
			}
			event("candidate_validated", "succeeded", "", in.CallRef)
			id, err := store.RecordDiagnosticCandidate(ctx, candidate)
			if err != nil {
				event("candidate_saved", "failed", "candidate_store_failed", in.CallRef)
				return actool.Errorf("diagnostic_candidate_store_failed"), nil
			}
			event("candidate_saved", "manual_review_required", "", in.CallRef)
			for _, saved := range onSaved {
				if saved != nil {
					saved()
				}
			}
			return actool.Text("Unconfirmed candidate recorded: " + strconv.FormatInt(id, 10) + ". Manual review required; this is not an E3 verdict or confirmed finding."), nil
		},
	})
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
