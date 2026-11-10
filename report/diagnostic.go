package report

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/dlp"
	"github.com/Autumn-27/artex/remediation"
	"github.com/Autumn-27/artex/reportcapture"
	"github.com/Autumn-27/artex/reproduction"
	"html/template"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxDiagnosticReportBytes = 16 << 20

// DiagnosticReport는 선택한 task의 저장 결과만 표현한다. 판정과 처리 상태는 별개다.
type WorkflowApproval struct{ Method, Path, State, ExecutionState, Attempt string }
type DiagnosticModelRecord struct {
	Model, Worker string
	Records       int64
}
type DiagnosticReport struct {
	TaskTitle                                         string
	TaskCreatedAt                                     time.Time
	ApprovedScope                                     *diagnosisprep.Scope
	ModelRecords                                      []DiagnosticModelRecord
	ModelRecordState                                  string
	Workflow                                          *diagnosisflow.Snapshot
	WorkflowApprovals                                 []WorkflowApproval
	TaskID                                            int64
	GeneratedAt                                       time.Time
	Shared                                            bool
	Entries                                           []DiagnosticEntry
	Confirmed, Review, NotExecuted, Unknown, Negative int
	BySeverity, ByType                                map[string]int
}
type DiagnosticEntry struct {
	Steps                                                     []reproduction.Step
	StepsTruncated                                            bool
	ID, Kind, Run                                             string
	IntentID                                                  int64
	Title, VulnClass, Severity, Status, Verification, Outcome string
	Summary, Remediation, Evidence                            string
	EvidenceVersion                                           int64
	Digest                                                    string
	ObservedAt                                                time.Time
	Report                                                    string
	RemediationArtifactID                                     int64
	RemediationCreatedAt                                      time.Time
	RemediationStale                                          bool
	RemediationBundle                                         *remediation.Bundle
	RemediationSource                                         *remediation.Input
	Stale, Truncated                                          bool
}

// 자격정보 형식·연락처를 보수적으로 제거한다. 공유 사본은 자유문장 전체를 가린다.
// 임의의 비밀을 판별하는 오라클은 아니며 원문 공유 허가를 대신하지 않는다.
var diagnosticID = regexp.MustCompile(`^(candidate|finding|verdict)-[1-9][0-9]*$`)

func DiagnosticText(s string) string { return dlp.DiagnosticText(s) }

// 템플릿 자동 이스케이프만 사용하며 링크·스크립트·외부 리소스를 만들지 않는다.
func PrepareDiagnostic(in DiagnosticReport) (DiagnosticReport, error) {
	if in.TaskID <= 0 || in.GeneratedAt.IsZero() || len(in.Entries) > MaxFindings {
		return DiagnosticReport{}, ErrExportBlocked
	}
	if in.Workflow != nil {
		if in.Workflow.Format != diagnosisflow.Version || in.Workflow.TaskID != strconv.FormatInt(in.TaskID, 10) || len(in.Workflow.Endpoints) > 32 || len(in.WorkflowApprovals) > 100 {
			return DiagnosticReport{}, ErrExportBlocked
		}
		raw, _ := json.Marshal(in.Workflow)
		var flow diagnosisflow.Snapshot
		if json.Unmarshal(raw, &flow) != nil {
			return DiagnosticReport{}, ErrExportBlocked
		}
		flow = diagnosisflow.Project(flow)
		if in.Shared {
			for i := range flow.Endpoints {
				flow.Endpoints[i].Path = "[공유용 경로 비공개]"
				flow.Endpoints[i].From = ""
				flow.Endpoints[i].Request = ""
				flow.Endpoints[i].Response = ""
			}
			flow.Plan = nil
			flow.Activity = nil
			flow.Events = nil
			flow.CurrentPath = ""
		}
		in.Workflow = &flow
	}
	if in.ApprovedScope != nil {
		scope := *in.ApprovedScope
		scope.Origin = DiagnosticText(scope.Origin)
		scope.ApprovalMode = DiagnosticText(scope.ApprovalMode)
		scope.PathPrefixes = append([]string{}, scope.PathPrefixes...)
		scope.Methods = append([]string{}, scope.Methods...)
		for i := range scope.PathPrefixes {
			scope.PathPrefixes[i] = DiagnosticText(scope.PathPrefixes[i])
		}
		for i := range scope.Methods {
			scope.Methods[i] = DiagnosticText(scope.Methods[i])
		}
		if scope.Model != nil {
			model := *scope.Model
			model.Model = DiagnosticText(model.Model)
			model.Provider = DiagnosticText(model.Provider)
			scope.Model = &model
		}
		in.ApprovedScope = &scope
	}
	in.TaskTitle = DiagnosticText(in.TaskTitle)
	records := append([]DiagnosticModelRecord{}, in.ModelRecords...)
	if len(records) > 64 {
		return DiagnosticReport{}, ErrExportBlocked
	}
	for i := range records {
		records[i].Model = DiagnosticText(records[i].Model)
		records[i].Worker = DiagnosticText(records[i].Worker)
	}
	in.ModelRecords = records
	if in.Shared {
		in.TaskTitle = "[REDACTED]"
		in.ApprovedScope = nil
		in.ModelRecords = nil
	}
	in.Confirmed, in.Review, in.NotExecuted, in.Unknown, in.Negative = 0, 0, 0, 0, 0
	in.BySeverity = map[string]int{}
	in.ByType = map[string]int{}
	entries := make([]DiagnosticEntry, len(in.Entries))
	copy(entries, in.Entries)
	in.Entries = entries
	seen := map[string]bool{}
	size := 0
	for i := range in.Entries {
		e := &in.Entries[i]
		if !diagnosticID.MatchString(e.ID) || seen[e.ID] || e.EvidenceVersion < 0 {
			return DiagnosticReport{}, ErrExportBlocked
		}
		seen[e.ID] = true
		if (e.Kind != "candidate" && e.Kind != "finding" && e.Kind != "verdict") || !strings.HasPrefix(e.ID, e.Kind+"-") || (e.Verification != "stored_verification" && e.Verification != "manual_review_required") {
			return DiagnosticReport{}, ErrExportBlocked
		}
		switch e.Outcome {
		case "confirmed":
			if e.Kind != "finding" || e.Verification != "stored_verification" || e.Stale {
				return DiagnosticReport{}, ErrExportBlocked
			}
			in.Confirmed++
		case "review":
			in.Review++
		case "not_executed":
			in.NotExecuted++
		case "unknown":
			in.Unknown++
		case "negative":
			in.Negative++
		default:
			return DiagnosticReport{}, ErrExportBlocked
		}
		for _, p := range []*string{&e.ID, &e.Kind, &e.Run, &e.Title, &e.VulnClass, &e.Severity, &e.Status, &e.Verification, &e.Summary, &e.Remediation, &e.Evidence, &e.Digest, &e.Report} {
			if !utf8.ValidString(*p) || strings.ContainsRune(*p, 0) {
				return DiagnosticReport{}, ErrExportBlocked
			}
			size += len(*p)
			if size > MaxDiagnosticReportBytes {
				return DiagnosticReport{}, ErrExportBlocked
			}
			*p = DiagnosticText(*p)
		}
		if e.Outcome == "confirmed" {
			sev := e.Severity
			if sev != "critical" && sev != "high" && sev != "medium" && sev != "low" && sev != "info" {
				sev = "미평가"
			}
			in.BySeverity[sev]++
			if in.Shared {
				in.ByType["[REDACTED]"]++
			} else {
				in.ByType[e.VulnClass]++
			}
		}
		if e.RemediationBundle != nil {
			if e.RemediationSource == nil || e.RemediationStale || e.RemediationBundle.FindingID != e.ID || e.RemediationBundle.EvidenceVersion != strconv.FormatInt(e.EvidenceVersion, 10) || remediation.IsStale(*e.RemediationBundle, *e.RemediationSource) {
				return DiagnosticReport{}, ErrExportBlocked
			}
			raw, err := json.Marshal(e.RemediationBundle)
			if err != nil {
				return DiagnosticReport{}, ErrExportBlocked
			}
			size += len(raw)
			if size > MaxDiagnosticReportBytes {
				return DiagnosticReport{}, ErrExportBlocked
			}
			// 저장본 결속을 먼저 확인한 뒤 출력 사본만 가린다. 해시·원본은 바꾸지 않는다.
			e.RemediationBundle, err = diagnosticBundleCopy(raw)
			if err != nil {
				return DiagnosticReport{}, ErrExportBlocked
			}
		}
		if len(e.Steps) > 32 {
			return DiagnosticReport{}, ErrExportBlocked
		}
		steps := append([]reproduction.Step{}, e.Steps...)
		e.Steps = steps
		for j := range steps {
			step := &e.Steps[j]
			if reproduction.Validate(*step, e.EvidenceVersion, e.Digest) != nil || step.TaskID != in.TaskID || step.EntryID != e.ID || step.Number != j+1 || step.RunRef != e.Run {
				return DiagnosticReport{}, ErrExportBlocked
			}
			if step.Capture.Status == "captured" {
				if step.Capture.Ref != "shot-"+step.BindingDigest[:24] || reportcapture.Verify(step.Capture) != nil {
					return DiagnosticReport{}, ErrExportBlocked
				}
				size += ((len(step.Capture.PNG) + 2) / 3) * 4
			} else if len(step.Capture.PNG) != 0 {
				return DiagnosticReport{}, ErrExportBlocked
			}
			for _, v := range []*string{&step.Title, &step.Method, &step.Path, &step.Prerequisites, &step.Observation} {
				*v = DiagnosticText(*v)
				size += len(*v)
			}
			if in.Shared {
				step.RunRef = "[REDACTED]"
				step.CallRef = "[REDACTED]"
				step.EvidenceRef = "[REDACTED]"
				step.EvidenceDigest = "[REDACTED]"
				step.BindingDigest = "[REDACTED]"
				step.SourceStepID = "[REDACTED]"
				step.Path = "[REDACTED]"
				step.Prerequisites = "[REDACTED]"
				step.Observation = "[REDACTED]"
				step.DependsOn = nil
				step.Capture = reproduction.Capture{Status: "shared_omitted"}
			}
		}
		if size > MaxDiagnosticReportBytes {
			return DiagnosticReport{}, ErrExportBlocked
		}
		if in.Shared {
			e.RemediationBundle = nil
			e.RemediationSource = nil
			e.Status = "[REDACTED]"
			if e.Severity != "critical" && e.Severity != "high" && e.Severity != "medium" && e.Severity != "low" && e.Severity != "info" {
				e.Severity = "미평가"
			}
			e.Title = "[REDACTED]"
			e.VulnClass = "[REDACTED]"
			e.Summary = "[REDACTED]"
			e.Remediation = "[REDACTED]"
			e.Evidence = "[REDACTED]"
			e.Report = "[REDACTED]"
			e.Run = "[REDACTED]"
			e.Digest = "[REDACTED]"
		}
	}
	return in, nil
}

func DiagnosticHTML(in DiagnosticReport) ([]byte, error) {
	var err error
	in, err = PrepareDiagnostic(in)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := diagnosticTemplate.Execute(&out, in); err != nil {
		return nil, errors.New("diagnostic report unavailable")
	}
	if out.Len() > MaxDiagnosticReportBytes {
		return nil, ErrExportBlocked
	}
	return out.Bytes(), nil
}

// 닫힌 JSON 스키마의 중첩 문자열까지 검사해 새 자유필드도 백스톱에서 빠지지 않게 한다.
func diagnosticBundleCopy(raw []byte) (*remediation.Bundle, error) {
	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	var redact func(any) (any, error)
	redact = func(value any) (any, error) {
		switch v := value.(type) {
		case string:
			if !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
				return nil, ErrExportBlocked
			}
			return DiagnosticText(v), nil
		case []any:
			for i := range v {
				item, err := redact(v[i])
				if err != nil {
					return nil, err
				}
				v[i] = item
			}
		case map[string]any:
			for key := range v {
				item, err := redact(v[key])
				if err != nil {
					return nil, err
				}
				v[key] = item
			}
		}
		return value, nil
	}
	data, err := redact(data)
	if err != nil {
		return nil, err
	}
	clean, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	var bundle remediation.Bundle
	if err := json.Unmarshal(clean, &bundle); err != nil {
		return nil, err
	}
	return &bundle, nil
}

func remediationSourceLabel(source string) string {
	switch source {
	case "observed":
		return "실행 관측"
	case "inferred":
		return "모델·분석기 추정"
	case "user_provided":
		return "사용자 제공·수동 검토"
	default:
		return "미확인"
	}
}
func remediationKnown(value string) bool {
	return value != "" && value != "unknown" && !strings.HasPrefix(value, "미확인")
}
func remediationContextConflicts(c remediation.Context) []string {
	if c.Source != "user_provided" {
		return nil
	}
	labels := []string{"영향 컴포넌트", "엔드포인트 경로 템플릿", "재현 사전 조건", "실제 관찰의 비민감 요약", "유지할 정상 동작", "차단할 취약 동작"}
	values := []string{c.Component, c.Endpoint, c.Prerequisites, c.Observation, c.ExpectedBehavior, c.ProhibitedBehavior}
	conflicts := []string{}
	for i, value := range values {
		if !remediationKnown(value) {
			continue
		}
		for _, o := range c.Observations {
			original := []string{o.Component, o.Endpoint, o.Prerequisites, o.Observation, o.ExpectedBehavior, o.ProhibitedBehavior}[i]
			if remediationKnown(original) && original != value {
				conflicts = append(conflicts, labels[i])
				break
			}
		}
	}
	return conflicts
}
func remediationTechnologyConflicts(technologies []remediation.Technology) []string {
	groups := map[string]map[string]bool{}
	order := []string{}
	for _, t := range technologies {
		if !remediationKnown(t.Component) || !remediationKnown(t.Name) {
			continue
		}
		key := t.Component + " / " + t.ComponentRole + " / " + t.Kind
		if groups[key] == nil {
			groups[key] = map[string]bool{}
			order = append(order, key)
		}
		value := t.Name
		if remediationKnown(t.Version) {
			value += " " + t.Version
		}
		groups[key][value] = true
	}
	conflicts := []string{}
	for _, key := range order {
		if len(groups[key]) > 1 {
			conflicts = append(conflicts, key)
		}
	}
	return conflicts
}

var diagnosticTemplate = template.Must(template.New("diagnostic").Funcs(template.FuncMap{
	"sourceLabel": remediationSourceLabel,
	"captureURI": func(step reproduction.Step) template.URL {
		if reportcapture.Verify(step.Capture) != nil {
			return ""
		}
		return template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(step.Capture.PNG))
	},
	"join":                strings.Join,
	"contextConflicts":    remediationContextConflicts,
	"technologyConflicts": remediationTechnologyConflicts,
}).Parse(`<!doctype html>
<html lang="ko"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src data:; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'">
<title>진단 종합 보고서</title><style>
body{font:16px/1.65 system-ui,sans-serif;max-width:1080px;margin:40px auto;padding:0 24px;color:#17283a;background:#f5f7fa}
h1{font-size:32px}h2{margin-top:36px;border-bottom:2px solid #b8cadd;padding-bottom:8px}
article{background:white;border:1px solid #ccd7e4;border-radius:8px;padding:24px;margin:20px 0;break-inside:avoid}
pre{white-space:pre-wrap;overflow-wrap:anywhere;background:#eef2f6;padding:16px;border-radius:4px;font:14px/1.65 monospace}
table{width:100%;border-collapse:collapse;background:white}th,td{border:1px solid #ccd7e4;padding:12px;text-align:left}
dt{font-weight:700}dd{margin-left:0;margin-bottom:8px;overflow-wrap:anywhere}
@media print{body{background:white;margin:0;max-width:none}article{break-inside:auto}h2{break-after:avoid}}
</style></head><body>
<h1>웹 취약점 진단 결과보고서</h1><p>진단 종합 보고서 · 문서 형식 diagnostic-report/v2</p><h2>목차</h2><ol><li>취약점 진단 개요</li><li>진단 방법 및 수행 현황</li><li>진단 결과 요약</li><li>취약점별 상세 결과</li><li>증적 부록</li></ol><p>작업 #{{.TaskID}} · 전체 저장 회차 · 생성 {{.GeneratedAt}}</p>
{{if .Shared}}<p>공유용 마스킹 사본. 자유문장과 원문 증거는 포함하지 않습니다.</p>{{else}}<p>인증된 관리자용 상세 사본. 자격정보 형식을 가렸습니다. 외부 공유 전 별도 검토가 필요합니다.</p>{{end}}
<h2>1장. 취약점 진단 개요</h2><p>작업 {{.TaskTitle}} · 작업 생성 {{.TaskCreatedAt}}</p>{{with .ApprovedScope}}<h3>해당 회차의 저장 승인 범위</h3><dl><dt>대상</dt><dd>{{.Origin}}</dd><dt>경로·메서드</dt><dd>{{join .PathPrefixes ", "}} · {{join .Methods ", "}}</dd><dt>승인 방식·만료</dt><dd>{{.ApprovalMode}} · {{.ExpiresAt}}</dd><dt>요청·모델 요청 상한</dt><dd>{{.Budget.Requests}} · {{.Budget.ModelRequests}}</dd>{{with .Model}}<dt>승인 모델 정책</dt><dd>{{.Model}} · {{.Provider}}</dd>{{end}}</dl>{{else}}<p>저장된 승인 범위 상세 미확보 또는 공유용 비공개. 작업 목표를 승인 범위로 간주하지 않습니다.</p>{{end}}<h3>저장된 모델·실행 정보</h3><p>사용량 기록 상태 {{.ModelRecordState}}. 기록 부재를 미실행으로 단정하지 않습니다.</p><ul>{{range .ModelRecords}}<li>{{.Model}} · {{.Worker}} · 사용량 기록 {{.Records}}행</li>{{end}}</ul><h3>진단 범위와 한계</h3><p>선택한 작업에 직접 속한 저장 결과만 포함합니다. 연결된 다른 작업·전체 자산·모든 검사항목의 검사 완료를 의미하지 않습니다.
실행 허용 범위·계정 준비·대상·기간은 각 실행 기록에서 확인해야 합니다. 기록되지 않은 관찰, 캡처, 원인이나 검증 성공을 생성하지 않습니다.
새 대상 요청·로그인·재현·외부 리소스 조회 없이 저장 자료만 사용했습니다.</p>
<h2>2장. 진단 방법 및 수행 현황</h2><p>아래 내용은 실제 저장한 실행·탐색 기록입니다. 목록에 없는 검사나 환경·모델은 수행·확인한 것으로 추정하지 않습니다.</p>{{with .Workflow}}<h3>실제 탐색·선택 기록</h3><p>모드 {{.Mode}} · 저장 단계 {{.Stage}} · 마지막 저장 {{.UpdatedAt}}. 이 기록은 재실행 권한 또는 현재 실행 중임의 증거가 아닙니다.</p><p>발견 {{.Counts.Found}} · 응답 관찰 {{.Counts.Visited}} · 요청 쓰기 {{.Counts.Sent}} · 전송 시도 {{.Counts.Attempted}} · 차단 {{.Counts.Blocked}} · 불명 {{.Counts.Unknown}}</p><table><thead><tr><th>메서드</th><th>경로</th><th>실제 방문</th><th>선택</th><th>응답</th><th>증거 call</th></tr></thead><tbody>{{range .Endpoints}}<tr><td>{{.Method}}</td><td>{{.Path}}</td><td>{{.Visited}}</td><td>{{.Selected}}</td><td>{{.Status}}</td><td>{{.CallRef}}</td></tr>{{end}}</tbody></table><p>제한된 정적 탐색입니다. 미방문·미선택 항목을 검사 완료로 표시하지 않습니다. 엔드포인트 선택과 개별 송신 승인은 별개입니다.</p>{{end}}
{{if .WorkflowApprovals}}<h3>저장된 요청 승인 이력</h3><table><thead><tr><th>메서드</th><th>경로</th><th>결정</th><th>전송 결과</th><th>attempt</th></tr></thead><tbody>{{range .WorkflowApprovals}}<tr><td>{{.Method}}</td><td>{{if $.Shared}}[공유용 경로 비공개]{{else}}{{.Path}}{{end}}</td><td>{{.State}}</td><td>{{.ExecutionState}}</td><td>{{.Attempt}}</td></tr>{{end}}</tbody></table>{{end}}
<h2>3장. 진단 결과 요약</h2><h3>관리자 요약</h3>
<table><thead><tr><th>확정</th><th>검토 필요</th><th>미실행</th><th>오류·결과 불명</th><th>비취약 판정</th></tr></thead>
<tbody><tr><td>{{.Confirmed}}</td><td>{{.Review}}</td><td>{{.NotExecuted}}</td><td>{{.Unknown}}</td><td>{{.Negative}}</td></tr></tbody></table>
<p>실행 completed는 취약점 확정이 아닙니다. 후보는 실제 관찰을 가진 검토 대상이며 자동 검증 또는 권한 있는 사람의 근거 있는 판단이 더 필요합니다.
심각도·처리 상태·판정·실행 결과를 따로 표시합니다. 유사 제목끼리 합치지 않으며 원래 ID를 유지합니다.</p>
<h3>확정 결과 심각도별 집계</h3><ul>{{range $key, $count := .BySeverity}}<li>{{$key}}: {{$count}}</li>{{end}}</ul>
<h3>확정 결과 유형별 집계</h3><ul>{{range $key, $count := .ByType}}<li>{{$key}}: {{$count}}</li>{{end}}</ul>
<h2>4장. 취약점별 상세 결과</h2><p>확정 항목과 검토 필요·미실행·불명·비취약 항목은 결과 구분을 기준으로 분리해서 검토하세요.</p>
{{range .Entries}}<article id="{{.ID}}"><h3>{{.ID}} · {{.Title}}</h3>
<dl><dt>유형</dt><dd>{{.VulnClass}}</dd><dt>결과 구분</dt><dd>{{.Outcome}}</dd>
<dt>검증 방법</dt><dd>{{.Verification}}</dd><dt>심각도</dt><dd>{{.Severity}}</dd><dt>처리/실행 상태</dt><dd>{{.Status}}</dd>
<dt>회차 / intent</dt><dd>{{if .Run}}{{.Run}}{{else}}회차 미기록{{end}} / {{if .IntentID}}{{.IntentID}}{{else}}intent 미기록{{end}}</dd><dt>관찰 시각</dt><dd>{{if .ObservedAt.IsZero}}미기록{{else}}{{.ObservedAt}}{{end}}</dd>
<dt>증거 버전 / digest</dt><dd>{{.EvidenceVersion}} / {{.Digest}}</dd></dl>
{{if .Stale}}<p>주의: 증거와 상세 보고서 버전이 달라 확정 집계에서 제외했습니다. 갱신이 필요합니다.</p>{{end}}
{{if .Truncated}}<p>주의: 실제 도구 출력이 잘렸습니다. 완전한 재현 증거가 아닙니다.</p>{{end}}
<h4>① 확인된 취약점 · 판정과 영향</h4><h5>관찰과 주장</h5><pre>{{if .Summary}}{{.Summary}}{{else}}추가 설명 미기록{{end}}</pre><h4>저장된 상세 설명</h4><pre>{{if .Report}}{{.Report}}{{else}}별도 상세 보고서 미작성{{end}}</pre>
<h4>② 재현 절차 및 스크린샷</h4>
{{if .StepsTruncated}}<p>실행 관측 목록이 잘렸습니다. 전체 재현 절차로 해석하지 마세요.</p>{{end}}
{{range .Steps}}<section><h5>Step {{.Number}}. {{.Title}}</h5><p>{{.Method}} {{if .Path}}{{.Path}}{{else}}비민감 경로 미기록{{end}}</p><p>사전 조건: {{.Prerequisites}}</p><pre>{{.Observation}}</pre><p>시각 {{.ObservedAt}} · 원본 단계 {{.SourceStepID}} · 실행 {{.RunRef}} · 호출 {{.CallRef}} · 근거 {{.EvidenceRef}} · 증거 {{.EvidenceVersion}} / {{.EvidenceDigest}}</p>{{if eq .Capture.Status "captured"}}<figure><img src="{{captureURI .}}" alt="실행 당시 수집한 비민감 영역 캡처" style="max-width:100%;height:auto"><figcaption>캡처 {{.Capture.CapturedAt}} · SHA256 {{.Capture.Digest}}</figcaption></figure>{{else}}<p>스크린샷 미확보 — 실제 요청·응답 및 실행 기록으로 증거 제공 · 상태 {{.Capture.Status}}</p>{{end}}</section>{{else}}<p>구조화된 실제 재현 단계 미확보. 아래 저장 근거를 확인하세요. 실행하지 않은 절차를 생성하지 않습니다.</p>{{end}}
<h4>실제 관찰 증거</h4><pre>{{if .Evidence}}{{.Evidence}}{{else}}추가 증거 본문 미기록{{end}}</pre>
<p>위 자료는 저장된 도구 출력 또는 증거 설명입니다. 실제 웹페이지 캡처가 아니며, 요청·응답 전문이나 실행 단계가 없으면 재현 완료로 해석하지 마세요.
스크린샷 부재는 결과 누락 사유가 아닙니다. 임의 HTML과 외부 링크는 실행되지 않습니다.</p>
<h4>③ 보안 조치</h4><h5>저장된 수정 제안 · 후속 검증</h5><pre>{{if .Remediation}}{{.Remediation}}{{else}}유형·관찰·대상 기술에 맞는 수정 방안 검토가 필요합니다.{{end}}</pre>
{{if .RemediationStale}}<p>저장 조치와 프롬프트의 근거 또는 생성기 버전이 바뀌었습니다. 오래된 프롬프트는 출력하지 않습니다. 다시 생성하세요.</p>{{end}}
{{with .RemediationBundle}}
<h4>기술 맞춤형 보안 설정·코드 조치</h4>
<p>조치 버전 {{.RemediationVersion}} · 입력 {{.InputDigest}} · 보고서 {{.ReportVersion}} · 증거 버전 {{.EvidenceVersion}} · 기술 정보 {{.TechnologyDigest}}</p>
<h5>수정 작업의 독립 맥락</h5><p>출처 {{sourceLabel .Context.Source}} · 컴포넌트 {{.Context.Component}} · 역할 {{.Context.ComponentRole}}</p><dl><dt>기능·엔드포인트</dt><dd>{{.Context.Endpoint}}</dd><dt>재현 사전 조건</dt><dd>{{.Context.Prerequisites}}</dd><dt>실제 관찰의 비민감 요약</dt><dd>{{.Context.Observation}}</dd><dt>유지할 정상 동작</dt><dd>{{.Context.ExpectedBehavior}}</dd><dt>차단할 취약 동작</dt><dd>{{.Context.ProhibitedBehavior}}</dd></dl><p>사용자 제공 요약은 자동 검증된 관찰이 아닙니다. 미확인 항목은 저장소와 승인된 근거에서 먼저 확인하세요.</p>
{{with contextConflicts .Context}}<p>사용자 보완과 원본 상충 가능·대조 필요: {{join . ", "}}</p>{{end}}
{{if .Context.ObservationsTruncated}}<p>관측 또는 기술 목록이 잘렸습니다. 전체 단계 연결은 미확인입니다.</p>{{end}}
{{range .Context.Observations}}<h5>보존된 원본 맥락 · {{sourceLabel .Source}}</h5>
<p>근거 {{.EvidenceRef}} · 관측 시각 {{.ObservedAt}} · 실행 {{.RunID}} · 호출 {{.CallID}} · 단계 {{.StepID}} · 선행 단계 {{join .DependsOn ", "}}</p>
<dl><dt>컴포넌트 / 역할</dt><dd>{{.Component}} / {{.ComponentRole}}</dd><dt>기능·엔드포인트</dt><dd>{{.Endpoint}}</dd>
<dt>재현 전제</dt><dd>{{.Prerequisites}}</dd><dt>관측·추정 내용</dt><dd>{{.Observation}}</dd>
<dt>유지할 정상 동작</dt><dd>{{.ExpectedBehavior}}</dd><dt>차단할 취약 동작</dt><dd>{{.ProhibitedBehavior}}</dd></dl>
{{if .Truncated}}<p>이 관측 출력 또는 단계가 잘렸습니다. 완전한 재현으로 해석하지 마세요.</p>{{end}}{{end}}
{{with .MissingContextFields}}<p>보완이 필요한 맥락: {{join . ", "}}. 수정 전에 확인하세요.</p>{{end}}
<ul>{{range .Technologies}}<li>{{.Kind}}: {{.Name}} {{.Version}} · 컴포넌트 {{.Component}} / {{.ComponentRole}} · 출처 {{sourceLabel .Source}} · 확인 수준 {{.Confidence}} · 근거 {{.EvidenceRef}} · 관측 시각 {{.ObservedAt}}</li>{{else}}<li>기술 스택 미확인. 실제 저장소에서 추가 확인하세요.</li>{{end}}</ul>
<p>출처와 확인 수준은 별개입니다. 사용자 제공·수동 검토는 프로그램의 실행 관측 또는 취약점 확정이 아닙니다.</p>
{{with technologyConflicts .Technologies}}<p>기술 상충 가능·대조 필요: {{join . ", "}}. 여러 기술이 함께 쓰이는지 확인하세요.</p>{{end}}
<h5>수정해야 할 보안 조건</h5><ul>{{range .SecurityCondition}}<li>{{.}}</li>{{end}}</ul>
<h5>즉시 완화책</h5><ul>{{range .ImmediateMitigation}}<li>{{.}}</li>{{end}}</ul>
<h5>근본 수정</h5><ul>{{range .RootFix}}<li>{{.}}</li>{{end}}</ul>
<h5>설정 변경</h5><ul>{{range .Configuration}}<li>{{.}}</li>{{end}}</ul>
<h5>조건부 코드 예시</h5>{{range .CodeExample}}<pre>{{.}}</pre>{{end}}
<h5>적용 전제·정상 기능 영향</h5><ul>{{range .Assumptions}}<li>{{.}}</li>{{end}}</ul>
<h5>수정 후 검증·회귀 시험</h5><ul>{{range .Verification}}<li>{{.}}</li>{{end}}</ul>
<h5>남은 위험·확인 사항</h5><ul>{{range .ResidualRisk}}<li>{{.}}</li>{{end}}</ul>
<h4>④ AI 수정 작업용 프롬프트</h4>
<p>기본 작업 방식: 실제 수정 실행형. 저장소 확인 후 실제 코드·설정 수정과 시험을 수행하도록 지시하는 프롬프트입니다. 실행 범위는 전달받는 AI 세션의 권한을 따릅니다.</p><pre>{{.AIPrompt}}</pre>
<p>생성·복사 자체는 고객 코드를 실행하거나 수정하지 않습니다. 근거·권한 부족, 구현 불일치와 운영 작업은 별도 확인이 필요합니다.
고객이 검토만 요청한 세션의 제한을 따르며, 원격 push·PR·배포·운영 재현 권한은 이 보고서가 부여하지 않습니다.</p>
{{else}}{{if not .RemediationStale}}<p>이 출력에 유효한 저장 조치·AI 프롬프트가 없습니다. 내부 상세에서 명시적으로 생성하거나 현재 버전을 확인하세요.</p>{{end}}{{end}}
<p>모델 제안과 검증된 원인은 별개입니다. 서버 소스 위치·실제 영향 확대는 확인되지 않았습니다.
수정 후 재검증은 이 보고서 생성으로 수행되지 않습니다. 별도 승인된 실행에서 사전 조건·예상 결과·실제 결과를 대조하세요.</p>
</article>{{else}}<p>선택 범위에 저장된 결과가 없습니다. 안전 또는 검사 완료를 뜻하지 않습니다.</p>{{end}}
<h2>5장. 증적 부록</h2><p>이 파일은 증거 텍스트를 자체 포함하며 숨은 첨부 링크가 없습니다. 비밀값·원문 요청/응답·브라우저 캡처는 별도 보호 저장소에서 권한을 확인하여 열람해야 합니다.
자동 검증기가 지원하지 않는 유형은 확정으로 승격하지 않습니다. 실제 모델 탐지 성능은 이 출력 시험과 별도입니다.</p></body></html>`))
