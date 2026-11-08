package report

import (
	"bytes"
	"errors"
	"github.com/Autumn-27/artex/dlp"
	"html/template"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxDiagnosticReportBytes = 16 << 20

// DiagnosticReport는 선택한 task의 저장 결과만 표현한다. 판정과 처리 상태는 별개다.
type DiagnosticReport struct {
	TaskID                                            int64
	GeneratedAt                                       time.Time
	Shared                                            bool
	Entries                                           []DiagnosticEntry
	Confirmed, Review, NotExecuted, Unknown, Negative int
	BySeverity, ByType                                map[string]int
}
type DiagnosticEntry struct {
	ID, Kind, Run                                             string
	IntentID                                                  int64
	Title, VulnClass, Severity, Status, Verification, Outcome string
	Summary, Remediation, Evidence                            string
	EvidenceVersion                                           int64
	Digest                                                    string
	ObservedAt                                                time.Time
	Report                                                    string
	Stale, Truncated                                          bool
}

// 자격정보 형식·연락처를 보수적으로 제거한다. 공유 사본은 자유문장 전체를 가린다.
// 임의의 비밀을 판별하는 오라클은 아니며 원문 공유 허가를 대신하지 않는다.
var diagnosticID = regexp.MustCompile(`^(candidate|finding|verdict)-[1-9][0-9]*$`)

func DiagnosticText(s string) string { return dlp.DiagnosticText(s) }

// 템플릿 자동 이스케이프만 사용하며 링크·스크립트·외부 리소스를 만들지 않는다.
func DiagnosticHTML(in DiagnosticReport) ([]byte, error) {
	if in.TaskID <= 0 || in.GeneratedAt.IsZero() || len(in.Entries) > MaxFindings {
		return nil, ErrExportBlocked
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
			return nil, ErrExportBlocked
		}
		seen[e.ID] = true
		if (e.Kind != "candidate" && e.Kind != "finding" && e.Kind != "verdict") || !strings.HasPrefix(e.ID, e.Kind+"-") || (e.Verification != "stored_verification" && e.Verification != "manual_review_required") {
			return nil, ErrExportBlocked
		}
		switch e.Outcome {
		case "confirmed":
			if e.Kind != "finding" || e.Verification != "stored_verification" || e.Stale {
				return nil, ErrExportBlocked
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
			return nil, ErrExportBlocked
		}
		for _, p := range []*string{&e.ID, &e.Kind, &e.Run, &e.Title, &e.VulnClass, &e.Severity, &e.Status, &e.Verification, &e.Summary, &e.Remediation, &e.Evidence, &e.Digest, &e.Report} {
			if !utf8.ValidString(*p) || strings.ContainsRune(*p, 0) {
				return nil, ErrExportBlocked
			}
			size += len(*p)
			if size > MaxDiagnosticReportBytes {
				return nil, ErrExportBlocked
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
		if in.Shared {
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
	var out bytes.Buffer
	if err := diagnosticTemplate.Execute(&out, in); err != nil {
		return nil, errors.New("diagnostic report unavailable")
	}
	if out.Len() > MaxDiagnosticReportBytes {
		return nil, ErrExportBlocked
	}
	return out.Bytes(), nil
}

var diagnosticTemplate = template.Must(template.New("diagnostic").Parse(`<!doctype html>
<html lang="ko"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'">
<title>진단 종합 보고서</title><style>
body{font:16px/1.65 system-ui,sans-serif;max-width:1080px;margin:40px auto;padding:0 24px;color:#17283a;background:#f5f7fa}
h1{font-size:32px}h2{margin-top:36px;border-bottom:2px solid #b8cadd;padding-bottom:8px}
article{background:white;border:1px solid #ccd7e4;border-radius:8px;padding:24px;margin:20px 0;break-inside:avoid}
pre{white-space:pre-wrap;overflow-wrap:anywhere;background:#eef2f6;padding:16px;border-radius:4px;font:14px/1.65 monospace}
table{width:100%;border-collapse:collapse;background:white}th,td{border:1px solid #ccd7e4;padding:12px;text-align:left}
dt{font-weight:700}dd{margin-left:0;margin-bottom:8px;overflow-wrap:anywhere}
@media print{body{background:white;margin:0;max-width:none}article{break-inside:auto}h2{break-after:avoid}}
</style></head><body>
<h1>진단 종합 보고서</h1><p>작업 #{{.TaskID}} · 전체 저장 회차 · 생성 {{.GeneratedAt}}</p>
{{if .Shared}}<p>공유용 마스킹 사본. 자유문장과 원문 증거는 포함하지 않습니다.</p>{{else}}<p>인증된 관리자용 상세 사본. 자격정보 형식을 가렸습니다. 외부 공유 전 별도 검토가 필요합니다.</p>{{end}}
<h2>진단 범위와 한계</h2><p>선택한 작업에 직접 속한 저장 결과만 포함합니다. 연결된 다른 작업·전체 자산·모든 검사항목의 검사 완료를 의미하지 않습니다.
실행 허용 범위·계정 준비·대상·기간은 각 실행 기록에서 확인해야 합니다. 기록되지 않은 관찰, 캡처, 원인이나 검증 성공을 생성하지 않습니다.
새 대상 요청·로그인·재현·외부 리소스 조회 없이 저장 자료만 사용했습니다.</p>
<h2>관리자 요약</h2>
<table><thead><tr><th>확정</th><th>검토 필요</th><th>미실행</th><th>오류·결과 불명</th><th>비취약 판정</th></tr></thead>
<tbody><tr><td>{{.Confirmed}}</td><td>{{.Review}}</td><td>{{.NotExecuted}}</td><td>{{.Unknown}}</td><td>{{.Negative}}</td></tr></tbody></table>
<p>실행 completed는 취약점 확정이 아닙니다. 후보는 실제 관찰을 가진 검토 대상이며 자동 검증 또는 권한 있는 사람의 근거 있는 판단이 더 필요합니다.
심각도·처리 상태·판정·실행 결과를 따로 표시합니다. 유사 제목끼리 합치지 않으며 원래 ID를 유지합니다.</p>
<h3>확정 결과 심각도별 집계</h3><ul>{{range $key, $count := .BySeverity}}<li>{{$key}}: {{$count}}</li>{{end}}</ul>
<h3>확정 결과 유형별 집계</h3><ul>{{range $key, $count := .ByType}}<li>{{$key}}: {{$count}}</li>{{end}}</ul>
<h2>결과별 상세</h2>
{{range .Entries}}<article id="{{.ID}}"><h3>{{.ID}} · {{.Title}}</h3>
<dl><dt>유형</dt><dd>{{.VulnClass}}</dd><dt>결과 구분</dt><dd>{{.Outcome}}</dd>
<dt>검증 방법</dt><dd>{{.Verification}}</dd><dt>심각도</dt><dd>{{.Severity}}</dd><dt>처리/실행 상태</dt><dd>{{.Status}}</dd>
<dt>회차 / intent</dt><dd>{{if .Run}}{{.Run}}{{else}}회차 미기록{{end}} / {{if .IntentID}}{{.IntentID}}{{else}}intent 미기록{{end}}</dd><dt>관찰 시각</dt><dd>{{if .ObservedAt.IsZero}}미기록{{else}}{{.ObservedAt}}{{end}}</dd>
<dt>증거 버전 / digest</dt><dd>{{.EvidenceVersion}} / {{.Digest}}</dd></dl>
{{if .Stale}}<p>주의: 증거와 상세 보고서 버전이 달라 확정 집계에서 제외했습니다. 갱신이 필요합니다.</p>{{end}}
{{if .Truncated}}<p>주의: 실제 도구 출력이 잘렸습니다. 완전한 재현 증거가 아닙니다.</p>{{end}}
<h4>관찰과 주장</h4><pre>{{if .Summary}}{{.Summary}}{{else}}추가 설명 미기록{{end}}</pre><h4>저장된 상세 설명</h4><pre>{{if .Report}}{{.Report}}{{else}}별도 상세 보고서 미작성{{end}}</pre>
<h4>실제 관찰 증거</h4><pre>{{if .Evidence}}{{.Evidence}}{{else}}추가 증거 본문 미기록{{end}}</pre>
<p>위 자료는 저장된 도구 출력 또는 증거 설명입니다. 실제 웹페이지 캡처가 아니며, 요청·응답 전문이나 실행 단계가 없으면 재현 완료로 해석하지 마세요.
스크린샷 부재는 결과 누락 사유가 아닙니다. 임의 HTML과 외부 링크는 실행되지 않습니다.</p>
<h4>수정 권고 · 후속 검증</h4><pre>{{if .Remediation}}{{.Remediation}}{{else}}유형·관찰·대상 기술에 맞는 수정 방안 검토가 필요합니다.{{end}}</pre>
<p>모델 제안과 검증된 원인은 별개입니다. 서버 소스 위치·실제 영향 확대는 확인되지 않았습니다.
수정 후 재검증은 이 보고서 생성으로 수행되지 않습니다. 별도 승인된 실행에서 사전 조건·예상 결과·실제 결과를 대조하세요.</p>
</article>{{else}}<p>선택 범위에 저장된 결과가 없습니다. 안전 또는 검사 완료를 뜻하지 않습니다.</p>{{end}}
<h2>증적 부록</h2><p>이 파일은 증거 텍스트를 자체 포함하며 숨은 첨부 링크가 없습니다. 비밀값·원문 요청/응답·브라우저 캡처는 별도 보호 저장소에서 권한을 확인하여 열람해야 합니다.
자동 검증기가 지원하지 않는 유형은 확정으로 승격하지 않습니다. 실제 모델 탐지 성능은 이 출력 시험과 별도입니다.</p></body></html>`))
