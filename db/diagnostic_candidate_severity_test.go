package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func observedSeverityCandidate(t *testing.T, task, intent int64) DiagnosticCandidate {
	t.Helper()
	c := syntheticDiagnosticCandidate(task, intent, "severity-call")
	var e map[string]any
	if json.Unmarshal([]byte(c.Evidence), &e) != nil {
		t.Fatal("합성 관찰 준비 실패")
	}
	trusted := e["trusted"].(map[string]any)
	for _, k := range []string{"final", "process_bash_exited", "process_drained", "proxy_drained"} {
		trusted[k] = true
	}
	call := trusted["call"].(map[string]any)
	call["final"], call["audit_complete"] = true, true
	call["counts"] = map[string]any{"responses": 1, "forward_attempted": 1, "unknown": 0}
	call["observations"] = []any{map[string]any{"sequence": 1, "attempt_ref": "severity-attempt", "method": "GET", "endpoint": "/documents", "rule_ref": "approved-rule", "decision": "allowed", "status_code": 200, "response_complete": true, "forward_attempted": true, "audit_complete": true, "observed_at": time.Now().UTC().Format(time.RFC3339Nano)}}
	raw, _ := json.Marshal(e)
	c.Evidence = string(raw)
	c.EvidenceDigest = DiagnosticEvidenceDigest(c.Evidence)
	c.SuggestedSeverity, c.SeverityRationale, c.SeveritySource = "high", "인증되지 않은 관찰에서 비공개 문서 내용이 노출되어 접근통제 영향을 검토해야 합니다.", WorkerEstimate
	return c
}

func TestDiagnosticCandidateSeverityConservativeNormalization(t *testing.T) {
	good := observedSeverityCandidate(t, 1, 2)
	if n := good.NormalizedSeverity(); n.SuggestedSeverity != "high" || n.Verification != "manual_review_required" || n.EvidenceDigest != good.EvidenceDigest {
		t.Fatal("평가 보존 또는 판정 분리 실패")
	}
	tests := map[string]func(*DiagnosticCandidate){
		"legacy":           func(c *DiagnosticCandidate) { c.SuggestedSeverity = "" },
		"enum":             func(c *DiagnosticCandidate) { c.SuggestedSeverity = "HIGH" },
		"no rationale":     func(c *DiagnosticCandidate) { c.SeverityRationale = "" },
		"short":            func(c *DiagnosticCandidate) { c.SeverityRationale = "높음" },
		"not Korean":       func(c *DiagnosticCandidate) { c.SeverityRationale = "No verified impact is available" },
		"multiline":        func(c *DiagnosticCandidate) { c.SeverityRationale += "\n추측" },
		"large":            func(c *DiagnosticCandidate) { c.SeverityRationale = strings.Repeat("문", 400) },
		"source claim":     func(c *DiagnosticCandidate) { c.SeveritySource = "confirmed" },
		"digest":           func(c *DiagnosticCandidate) { c.Evidence += " " },
		"scope":            func(c *DiagnosticCandidate) { c.TaskID++ },
		"output truncated": func(c *DiagnosticCandidate) { c.OutputTruncated = true },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			c := good
			mutate(&c)
			n := c.NormalizedSeverity()
			if n.SuggestedSeverity != "" || n.SeveritySource != "" || n.SeverityRationale != "" {
				t.Fatal("부족한 근거의 평가를 수용함")
			}
			if n.Evidence != c.Evidence || n.Name != c.Name {
				t.Fatal("평가 거부가 후보를 바꿈")
			}
		})
	}
	for _, status := range []string{"policy_not_executed", "result_unknown", "unsupported", "partial_policy_denial", "tool_failure"} {
		t.Run(status, func(t *testing.T) {
			c := good
			c.Classification = status
			var evidence map[string]any
			json.Unmarshal([]byte(c.Evidence), &evidence)
			evidence["trusted"].(map[string]any)["classification"] = status
			raw, _ := json.Marshal(evidence)
			c.Evidence = string(raw)
			c.EvidenceDigest = DiagnosticEvidenceDigest(c.Evidence)
			if c.NormalizedSeverity().SuggestedSeverity != "" {
				t.Fatal("실행 상태를 평가로 승격함")
			}
		})
	}
	for _, field := range []string{"final", "process_bash_exited", "process_drained", "proxy_drained"} {
		t.Run(field, func(t *testing.T) {
			c := good
			var e map[string]any
			json.Unmarshal([]byte(c.Evidence), &e)
			e["trusted"].(map[string]any)[field] = false
			raw, _ := json.Marshal(e)
			c.Evidence = string(raw)
			c.EvidenceDigest = DiagnosticEvidenceDigest(c.Evidence)
			if c.NormalizedSeverity().SuggestedSeverity != "" {
				t.Fatal("마감 없는 평가")
			}
		})
	}
	for _, field := range []string{"final", "audit_complete", "observations", "counts"} {
		t.Run("call-"+field, func(t *testing.T) {
			c := good
			var e map[string]any
			json.Unmarshal([]byte(c.Evidence), &e)
			delete(e["trusted"].(map[string]any)["call"].(map[string]any), field)
			raw, _ := json.Marshal(e)
			c.Evidence = string(raw)
			c.EvidenceDigest = DiagnosticEvidenceDigest(c.Evidence)
			if c.NormalizedSeverity().SuggestedSeverity != "" {
				t.Fatal("HTTP·감사 근거 없는 평가")
			}
		})
	}
	for _, flag := range []string{"observations_truncated", "response_truncated", "withheld"} {
		t.Run(flag, func(t *testing.T) {
			c := good
			var evidence map[string]any
			json.Unmarshal([]byte(c.Evidence), &evidence)
			call := evidence["trusted"].(map[string]any)["call"].(map[string]any)
			switch flag {
			case "observations_truncated":
				call[flag] = true
			case "withheld":
				call["counts"].(map[string]any)[flag] = float64(1)
			default:
				observation := call["observations"].([]any)[0].(map[string]any)
				copy := map[string]any{}
				for k, v := range observation {
					copy[k] = v
				}
				copy[flag] = true
				copy["sequence"] = float64(2)
				call["observations"] = append(call["observations"].([]any), copy)
			}
			raw, _ := json.Marshal(evidence)
			c.Evidence = string(raw)
			c.EvidenceDigest = DiagnosticEvidenceDigest(c.Evidence)
			if c.NormalizedSeverity().SuggestedSeverity != "" {
				t.Fatal("일부 응답의 잘림·보류를 완전한 관찰로 평가함")
			}
		})
	}

	// 평가 근거 글은 가리지 않는다(절대규칙 2, 2026-11-26 DLP 폐기). 우리 제어 비밀 · E③ 표식은 후보 저장 때 RedactRecord 가 맡는다.
	c := good
	c.SeverityRationale += " Authorization: Bearer synthetic-severity-secret"
	n := c.NormalizedSeverity()
	if !strings.Contains(n.SeverityRationale, "Authorization: Bearer synthetic-severity-secret") {
		t.Fatal("평가 근거 대상 값을 가림")
	}
}

func TestRelatedCallGateDropsEstimate(t *testing.T) {
	for _, condition := range []string{"completed", "observations_truncated", "response_truncated", "unknown", "unclosed"} {
		t.Run(condition, func(t *testing.T) {
			c := observedSeverityCandidate(t, 1, 2)
			related := observedSeverityCandidate(t, 1, 2)
			var other, evidence map[string]any
			if json.Unmarshal([]byte(related.Evidence), &other) != nil || json.Unmarshal([]byte(c.Evidence), &evidence) != nil {
				t.Fatal("합성 관찰 준비 실패")
			}
			trusted := other["trusted"].(map[string]any)
			call := trusted["call"].(map[string]any)
			call["call_ref"] = "severity-related"
			switch condition {
			case "observations_truncated":
				call[condition] = true
			case "response_truncated":
				call["observations"].([]any)[0].(map[string]any)[condition] = true
			case "unknown":
				call["counts"].(map[string]any)[condition] = float64(1)
			case "unclosed":
				trusted["proxy_drained"] = false
			}
			evidence["related_calls"] = []any{other}
			raw, err := json.Marshal(evidence)
			if err != nil {
				t.Fatal(err)
			}
			c.Evidence = string(raw)
			c.EvidenceDigest = DiagnosticEvidenceDigest(c.Evidence)
			if err := c.Validate(); err != nil {
				t.Fatal("관련 호출을 넣은 후보의 기본 결속이 잘못됨", err)
			}
			n := c.NormalizedSeverity()
			if condition == "completed" {
				if n.SuggestedSeverity != "high" || n.SeveritySource != WorkerEstimate || n.SeverityRationale != c.SeverityRationale {
					t.Fatal("양성 대조: 정상 관련 호출의 평가를 버림")
				}
			} else if n.SuggestedSeverity != "" || n.SeveritySource != "" || n.SeverityRationale != "" {
				t.Fatal("관련 호출의 불완전한 근거로 평가를 남김")
			}
			if n.Evidence != c.Evidence || n.EvidenceDigest != c.EvidenceDigest || n.Verification != "manual_review_required" {
				t.Fatal("평가 정규화가 후보·증거를 바꿈")
			}
		})
	}
}

func TestDiagnosticCandidateSeverityPostgresIdempotencyLegacyAndStale(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTask("합성 평가 저장", "저장 조회 시험", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	store := d.Exploration(task.ExplorationID)
	intent, err := store.AddIntent(map[string]any{"summary": "합성 평가"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	c := observedSeverityCandidate(t, task.ID, intent)
	id, err := store.RecordDiagnosticCandidate(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	changed := c
	changed.SuggestedSeverity = "critical"
	again, err := store.RecordDiagnosticCandidate(t.Context(), changed)
	if err != nil || again != id {
		t.Fatal("중복 평가 덮어쓰기", err)
	}
	rows, err := store.ListDiagnosticCandidates(task.ID, "", 10)
	if err != nil || len(rows) != 1 || rows[0].SuggestedSeverity != "high" || rows[0].Verification != "manual_review_required" {
		t.Fatal("DB 평가 왕복 실패", err)
	}
	ref := fmt.Sprintf("candidate-%d", id)
	before, err := LoadDiagnosticRemediationSource(context.Background(), d, task.ID, ref)
	if err != nil {
		t.Fatal(err)
	}
	record, err := d.SaveDiagnosticRemediation(t.Context(), task.ID, ref, before.SourceDigest, nil)
	if err != nil {
		t.Fatal("조치 저장 실패", err)
	}

	if _, err = d.Exec(`UPDATE exploration_nodes SET payload=jsonb_set(payload,'{diagnostic_candidate_v1,severity_rationale}',to_jsonb('관측된 비공개 문서에 대한 접근통제 영향의 추가 설명입니다.'::text)) WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	after, err := LoadDiagnosticRemediationSource(t.Context(), d, task.ID, ref)
	if err != nil || before.SourceDigest == after.SourceDigest || before.EvidenceDigest != after.EvidenceDigest {
		t.Fatal("평가 변경의 원본 지문·증거 분리 실패", err)
	}
	records, err := ListDiagnosticRemediationRecords(t.Context(), d, task.ID, 10)
	if err != nil || len(records) != 1 || records[0].ID != record.ID || !records[0].Stale {
		t.Fatal("평가 변경 뒤 조치 stale 미탐지", err)
	}

	if _, err = d.Exec(`UPDATE exploration_nodes SET payload=jsonb_set(payload,'{diagnostic_candidate_v1}',(payload->'diagnostic_candidate_v1')-'suggested_severity'-'severity_rationale'-'severity_source') WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	rows, err = store.ListDiagnosticCandidates(task.ID, "", 10)
	if err != nil || len(rows) != 1 || rows[0].SuggestedSeverity != "" {
		t.Fatal("구버전 호환 실패", err)
	}
}

// 위험도 근거는 모델이 쓴 저장 기록이라 자격 모양 글도 가리지 않는다(절대규칙 2, 2026-11-26 DLP 폐기). 근거 형식 규칙(한국어 ·
// 한 줄 · 길이)은 그대로다.
func TestDiagnosticCandidateSeverityRationaleKeepsTargetValues(t *testing.T) {
	c := observedSeverityCandidate(t, 1, 2)
	c.SeverityRationale = "응답 본문에 password=synthetic-pass-77 이 그대로 보여 다른 계정 접근 영향이 큼"
	n := c.NormalizedSeverity()
	if n.SuggestedSeverity != "high" || n.SeverityRationale != c.SeverityRationale {
		t.Fatalf("근거를 가림: %q", n.SeverityRationale)
	}
}
