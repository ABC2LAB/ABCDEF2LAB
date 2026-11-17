package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/transmit"
)

type severityNoHTTP struct{ calls int }

func (n *severityNoHTTP) RoundTrip(*http.Request) (*http.Response, error) {
	n.calls++
	return nil, fmt.Errorf("보고서 읽기는 외부 HTTP를 호출하지 않아야 합니다")
}

func TestCandidateSeverityStoredAPIHTMLDOCXOwnedPostgres(t *testing.T) {
	s, legacy, req := trafficEvidenceServer(t)
	stored, err := s.m.pg.GetFinding(legacy.FindingID)
	if err != nil || stored.TaskID == nil {
		t.Fatal(err)
	}
	taskID := *stored.TaskID
	task := s.m.ResolveTask(strconv.FormatInt(taskID, 10))
	ctx := t.Context()
	ids := map[string]string{}
	for _, tc := range []struct {
		name, status, severity string
		truncated, broken      bool
	}{
		{"높음", "completed", "high", false, false}, {"심각", "completed", "critical", false, false}, {"정보성", "completed", "info", false, false},
		{"미실행", "policy_not_executed", "high", false, false}, {"불명", "result_unknown", "critical", false, false},
		{"생략", "completed", "", false, false}, {"잘못된 등급", "completed", "urgent", false, false},
		{"잘림", "completed", "high", true, false}, {"훼손", "completed", "high", false, true},
	} {
		intent, err := task.Store.AddIntent(map[string]any{"summary": "합성 심각도 관찰"}, 5, nil, "test")
		if err != nil {
			t.Fatal(err)
		}
		run, call := "severity-run", fmt.Sprintf("severity-%d", intent)
		result := e2runner.Result{ToolOutput: "합성 응답 본문", Trusted: e2runner.TrustedResult{Execution: e2proxy.ExecutionView{Scope: transmit.Scope{TaskRef: fmt.Sprint(taskID), RunRef: run}, IntentID: intent}, Classification: tc.status, Final: true, ProcessBashExited: true, ProcessDrained: true, ProxyDrained: true, OutputTruncated: tc.truncated, Call: e2proxy.CallResult{IntentID: intent, CallRef: call, Final: true, AuditComplete: true, Counts: e2proxy.Counts{Responses: 1, ForwardAttempted: 1}, Observations: []e2proxy.HTTPObservation{{Sequence: 1, AttemptRef: "owned-attempt", RuleRef: "owned-rule", Method: "GET", Endpoint: "/documents", Decision: "allowed", StatusCode: 200, ResponseComplete: true, ForwardAttempted: true, AuditComplete: true, ObservedAt: time.Now().UTC()}}}}}
		raw, _ := json.Marshal(result)
		candidate := db.DiagnosticCandidate{TaskID: taskID, IntentID: intent, RunRef: run, CallRef: call, Name: "합성 " + tc.name, VulnClass: "IDOR", Summary: "검토 대상 관찰", Classification: tc.status, Verification: "manual_review_required", EvidenceVersion: 1, Evidence: string(raw), EvidenceDigest: db.DiagnosticEvidenceDigest(string(raw)), ObservedAt: time.Now().UTC(), OutputTruncated: tc.truncated, SuggestedSeverity: tc.severity, SeveritySource: db.WorkerEstimate, SeverityRationale: "비공개 문서가 다른 접근 문맥의 응답에 포함되어 권한 영향을 검토해야 합니다."}
		id, err := task.Store.RecordDiagnosticCandidate(ctx, candidate)
		if err != nil {
			t.Fatal(err)
		}
		ids[tc.name] = fmt.Sprintf("candidate-%d", id)
		if tc.broken {
			if _, err = s.m.pg.Exec(`UPDATE exploration_nodes SET payload=jsonb_set(payload,'{diagnostic_candidate_v1,evidence_digest}',to_jsonb('invalid'::text)) WHERE id=$1`, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	guard := &severityNoHTTP{}
	previous := http.DefaultTransport
	http.DefaultTransport = guard
	defer func() { http.DefaultTransport = previous }()
	base := fmt.Sprintf("/api/report/diagnostic/remediation?task=%d", taskID)
	internal := req("GET", base+"&audience=internal", "")
	if internal.Code != 200 {
		t.Fatal("내부 API 실패", internal.Code)
	}
	var data struct {
		Entries []diagnosticRemediationEntryDTO `json:"entries"`
	}
	if json.Unmarshal(internal.Body.Bytes(), &data) != nil {
		t.Fatal("응답 JSON 실패")
	}
	byID := map[string]diagnosticRemediationEntryDTO{}
	for _, entry := range data.Entries {
		byID[entry.EntryID] = entry
		if entry.Outcome == "confirmed" {
			t.Fatal("예상 평가로 확정됨")
		}
	}
	for name, id := range ids {
		e := byID[id]
		if name == "높음" || name == "심각" || name == "정보성" {
			if e.SeveritySource != db.WorkerEstimate || e.Severity == "미평가" || e.Outcome != "review" || e.Verification != "manual_review_required" {
				t.Fatal("내부 예상 평가 소실", name)
			}
		} else if e.Severity != "미평가" || e.SeveritySource != "" || e.SeverityRationale != "" {
			t.Fatal("불충분한 후보 평가", name)
		}
	}
	shared := req("GET", base, "")
	if shared.Code != 200 || strings.Contains(shared.Body.String(), "severity_source") || strings.Contains(shared.Body.String(), "severity_rationale") {
		t.Fatal("공유 DTO 평가 노출")
	}
	path := fmt.Sprintf("/api/report/diagnostic?task=%d", taskID)
	html := req("GET", path+"&audience=internal", "")
	if html.Code != 200 || !strings.Contains(html.Body.String(), "예상 높음") || !strings.Contains(html.Body.String(), "예상 심각") || !strings.Contains(html.Body.String(), "예상 정보성") {
		t.Fatal("HTML 의미 불일치", html.Code)
	}
	docx := req("GET", path+"&audience=internal&format=docx", "")
	if docx.Code != 200 || !strings.Contains(diagnosticWordText(t, wordParts(t, docx.Body.Bytes())["word/document.xml"]), "예상 높음") {
		t.Fatal("DOCX 의미 불일치", docx.Code)
	}
	for _, format := range []string{"html", "docx"} {
		response := req("GET", path+"&format="+format, "")
		if response.Code != 200 {
			t.Fatal(response.Code)
		}
		text := response.Body.String()
		if format == "docx" {
			text = diagnosticWordText(t, wordParts(t, response.Body.Bytes())["word/document.xml"])
		}
		if strings.Contains(text, "예상 높음") || strings.Contains(text, "비공개 문서가 다른") {
			t.Fatal("공유 보고서 평가 노출")
		}
	}
	if guard.calls != 0 {
		t.Fatal("보고서 조회가 대상·모델 HTTP를 호출함")
	}
}
