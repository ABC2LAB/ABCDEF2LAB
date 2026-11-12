package report

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/artex/remediation"
)

const wordNS = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"

type diagnosticWord struct {
	images   [][]byte
	body     strings.Builder
	err      error
	replaced int
}

func wordText(value string) string {
	var out bytes.Buffer
	_ = xml.EscapeText(&out, []byte(value))
	return out.String()
}
func (w *diagnosticWord) paragraph(style, text string) {
	if !utf8.ValidString(text) {
		w.err = ErrExportBlocked
		return
	}
	text = strings.Map(func(r rune) rune {
		if (r < 32 && r != '\n' && r != '\r' && r != '\t') || r == 0xfffe || r == 0xffff {
			w.replaced++
			return '\uFFFD'
		}
		return r
	}, text)
	w.body.WriteString(`<w:p><w:pPr><w:pStyle w:val="` + style + `"/></w:pPr>`)
	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			w.body.WriteString(`<w:r><w:br/></w:r>`)
		}
		w.body.WriteString(`<w:r><w:t xml:space="preserve">` + wordText(line) + `</w:t></w:r>`)
	}
	w.body.WriteString(`</w:p>`)
}
func (w *diagnosticWord) table(rows [][]string) {
	w.body.WriteString(`<w:tbl><w:tblPr><w:tblStyle w:val="ReportTable"/><w:tblW w:w="0" w:type="auto"/><w:tblLayout w:type="fixed"/></w:tblPr>`)
	for i, row := range rows {
		w.body.WriteString(`<w:tr><w:trPr><w:cantSplit/>`)
		if i == 0 {
			w.body.WriteString(`<w:tblHeader/>`)
		}
		w.body.WriteString(`</w:trPr>`)
		for _, cell := range row {
			w.body.WriteString(`<w:tc><w:tcPr><w:tcW w:w="` + strconv.Itoa(9000/len(row)) + `" w:type="dxa"/>`)
			if i == 0 {
				w.body.WriteString(`<w:shd w:fill="E6EDF4"/>`)
			}
			w.body.WriteString(`</w:tcPr>`)
			w.paragraph("Normal", cell)
			w.body.WriteString(`</w:tc>`)
		}
		w.body.WriteString(`</w:tr>`)
	}
	w.body.WriteString(`</w:tbl>`)
}
func (w *diagnosticWord) lines(title string, values []string) {
	w.paragraph("Heading3", title)
	if len(values) == 0 {
		w.paragraph("Normal", "저장된 자료 없음. 실제 코드·승인 근거에서 추가 확인이 필요합니다.")
	}
	for _, v := range values {
		w.paragraph("Normal", "• "+v)
	}
}
func (w *diagnosticWord) chapter(title string) { w.paragraph("Heading1", title) }

// DiagnosticDOCX는 HTML과 동일한 검증·집계·DLP 투영을 사용한다. 대상·변환 API를 호출하지 않는다.
func DiagnosticDOCX(input DiagnosticReport) ([]byte, error) {
	in, err := PrepareDiagnostic(input)
	if err != nil {
		return nil, err
	}
	w := &diagnosticWord{}
	w.paragraph("Title", "웹 취약점 진단 결과보고서")
	w.table([][]string{{"문서 정보", "값"}, {"작업", fmt.Sprintf("#%d", in.TaskID)}, {"형식", "diagnostic-report/v2"}, {"생성 시각", in.GeneratedAt.UTC().Format(time.RFC3339)}, {"공개 범위", map[bool]string{true: "공유용 마스킹 사본", false: "인증된 관리자 내부 상세"}[in.Shared]}})
	w.paragraph("Normal", "이 보고서는 저장된 실행·증거·판정·조치 자료만 사용합니다. 보고서 생성은 새로운 진단이나 고객 코드 수정을 실행하지 않습니다.")
	w.chapter("목차")
	for _, t := range []string{"1장. 취약점 진단 개요", "2장. 진단 방법 및 수행 현황", "3장. 진단 결과 요약", "4장. 취약점별 상세 결과", "5장. 증적 부록"} {
		w.paragraph("Normal", t)
	}
	w.chapter("1장. 취약점 진단 개요")
	w.table([][]string{{"작업", in.TaskTitle}, {"작업 생성", in.TaskCreatedAt.UTC().Format(time.RFC3339)}})
	if scope := in.ApprovedScope; scope != nil {
		w.table([][]string{{"승인 범위", "저장 값"}, {"대상", scope.Origin}, {"경로", strings.Join(scope.PathPrefixes, ", ")}, {"메서드", strings.Join(scope.Methods, ", ")}, {"승인 방식", scope.ApprovalMode}, {"정책 만료", scope.ExpiresAt.UTC().Format(time.RFC3339)}, {"요청 / 모델 요청 상한", fmt.Sprintf("%d / %d", scope.Budget.Requests, scope.Budget.ModelRequests)}})
		if scope.Model != nil {
			w.paragraph("Normal", "승인 모델 정책: "+scope.Model.Model+" / "+scope.Model.Provider)
		}
	} else {
		w.paragraph("Normal", "저장된 승인 범위 상세 미확보 또는 공유용 비공개. 작업 목표는 승인 범위가 아닙니다.")
	}
	w.paragraph("Normal", "모델 사용량 기록 상태: "+in.ModelRecordState+". 기록 부재를 미실행으로 단정하지 않습니다.")
	if in.ModelRecordsOmitted > 0 {
		w.paragraph("Normal", fmt.Sprintf("추가 모델 사용량 묶음 %d개 생략. 저장된 원본 기록은 유지합니다.", in.ModelRecordsOmitted))
	}
	for _, r := range in.ModelRecords {
		w.paragraph("Normal", fmt.Sprintf("%s / %s · 사용량 기록 %d행", r.Model, r.Worker, r.Records))
	}
	w.paragraph("Heading2", "진단 목적·권한과 한계")
	w.paragraph("Normal", "승인된 실행에서 저장된 보안 관찰과 검증 결과를 개발자의 재현·조치·검증에 연결합니다. 선택 작업에 직접 속한 자료만 포함하며, 전체 웹사이트·모든 취약점의 검사 완료를 뜻하지 않습니다.")
	w.paragraph("Normal", "승인 대상·범위·정책·기간은 해당 실행 기록에서 확인해야 합니다. 저장되지 않은 대상 URL·실행 환경·모델·검사 유형·수행 인원을 추정하지 않습니다. 요청·로그인·취약점 재현·모델 completion을 보고서 생성 중 실행하지 않습니다.")
	w.chapter("2장. 진단 방법 및 수행 현황")
	if in.Workflow != nil {
		f := in.Workflow
		w.table([][]string{{"항목", "저장 값"}, {"실행 모드", f.Mode}, {"저장 단계", f.Stage}, {"최종 기록", f.UpdatedAt.UTC().Format(time.RFC3339)}, {"발견 / 실제 방문", fmt.Sprintf("%d / %d", f.Counts.Found, f.Counts.Visited)}, {"전송 시도 / 차단 / 불명", fmt.Sprintf("%d / %d / %d", f.Counts.Attempted, f.Counts.Blocked, f.Counts.Unknown)}})
		rows := [][]string{{"메서드", "경로", "방문 / 선택", "증거 call"}}
		for _, e := range f.Endpoints {
			rows = append(rows, []string{e.Method, e.Path, fmt.Sprintf("%t / %t", e.Visited, e.Selected), e.CallRef})
		}
		w.table(rows)
	} else {
		w.paragraph("Normal", "구조화된 탐색·Planner/Worker 수행 기록 미확보. 미검사 범위를 수행 완료로 표시하지 않습니다.")
	}
	if len(in.WorkflowApprovals) > 0 {
		rows := [][]string{{"메서드·경로", "승인 결정", "실행 상태"}}
		for _, a := range in.WorkflowApprovals {
			path := a.Path
			if in.Shared {
				path = "[공유용 경로 비공개]"
			}
			rows = append(rows, []string{a.Method + " " + path, a.State, a.ExecutionState})
		}
		w.table(rows)
	}
	w.chapter("3장. 진단 결과 요약")
	w.paragraph("Normal", "실행 결과: "+in.RunText+". 실행 결과는 취약점이 없다는 뜻이 아닙니다.")
	if in.SourceFailures > 0 {
		w.paragraph("Normal", fmt.Sprintf("근거 불러오기 실패: %d건. 해당 항목의 재현·캡처·프롬프트는 제공하지 않습니다.", in.SourceFailures))
	}
	w.table([][]string{{"확정", "검토 필요", "미실행", "판단 불가·결과 불명", "비취약"}, {strconv.Itoa(in.Confirmed), strconv.Itoa(in.Review), strconv.Itoa(in.NotExecuted), strconv.Itoa(in.Unknown), strconv.Itoa(in.Negative)}})
	w.paragraph("Normal", "확정 집계는 유효한 저장 E③ 판정만 포함합니다. HTTP 성공·AI 주장·후보·정상 종료는 확정 근거가 아닙니다. 검토 필요 후보는 확정 심각도·위험도 합계에 포함하지 않습니다.")
	keys := []string{}
	for k := range in.BySeverity {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	severity := [][]string{{"확정 결과 심각도", "건수"}}
	for _, k := range keys {
		severity = append(severity, []string{k, strconv.Itoa(in.BySeverity[k])})
	}
	w.table(severity)
	w.chapter("4장. 취약점별 상세 결과")
	for i, e := range in.Entries {
		style := "Heading2"
		if i > 0 {
			style = "EntryHeading"
		}
		w.paragraph(style, e.ID+" · "+e.Title)
		w.paragraph("Heading2", "① 판정과 영향")
		w.table([][]string{{"항목", "값"}, {"고유 ID / 유형", e.ID + " / " + e.VulnClass}, {"판정 / 검증", e.Outcome + " / " + e.Verification}, {"위험도 / 상태", e.Severity + " / " + e.Status}, {"실행 / intent", e.Run + " / " + strconv.FormatInt(e.IntentID, 10)}, {"증거 버전 / digest", strconv.FormatInt(e.EvidenceVersion, 10) + " / " + e.Digest}})
		if e.Stale {
			w.paragraph("Normal", "증거 버전 불일치: 오래된 자료를 확정으로 집계하지 않습니다.")
		}
		if e.Truncated {
			w.paragraph("Normal", "주의: 실제 도구 출력이 잘렸습니다. 완전한 재현 증거가 아닙니다.")
		}
		w.paragraph("Normal", e.Summary)
		w.paragraph("Normal", e.Report)
		w.paragraph("Heading2", "② 재현 절차 및 스크린샷")
		if e.SourceLoadFailed {
			w.paragraph("Normal", "근거 불러오기 실패. 현재 결속을 확인할 수 없어 재현 단계를 제공하지 않습니다.")
		}
		if e.StepsTruncated {
			w.paragraph("Normal", "관측 목록 잘림: 완전한 재현 절차가 아닙니다.")
		}
		if len(e.Steps) == 0 {
			w.paragraph("Normal", "구조화된 실제 재현 단계 미확보. 실행하지 않은 절차를 생성하지 않습니다.")
		}
		for _, step := range e.Steps {
			w.paragraph("Heading3", fmt.Sprintf("Step %d. %s", step.Number, step.Title))
			w.paragraph("Normal", step.Method+" "+step.Path)
			w.paragraph("Normal", "사전 조건: "+step.Prerequisites)
			w.paragraph("Normal", step.Observation)
			if step.Truncated {
				w.paragraph("Normal", "이 단계의 관측이 잘렸습니다.")
			}
			w.paragraph("Caption", "시각 "+step.ObservedAt+" · 실행 "+step.RunRef+" · 호출 "+step.CallRef+" · 근거 "+step.EvidenceRef+" · 단계 "+step.SourceStepID)
			if step.Capture.Status == "captured" {
				w.picture(step.Capture.PNG, step.Capture.Width, step.Capture.Height)
				w.paragraph("Caption", "실행 시각 "+step.Capture.CapturedAt.UTC().Format(time.RFC3339)+" · SHA256 "+step.Capture.Digest)
			} else {
				w.paragraph("Normal", "스크린샷 미확보 — 실제 요청·응답 및 실행 기록으로 증거 제공 · 상태 "+captureStatusLabel(step.Capture.Status))
			}
		}
		w.paragraph("Heading3", "저장된 실제 증거")
		w.paragraph("Code", e.Evidence)
		w.paragraph("Heading2", "③ 보안 조치")
		w.paragraph("Normal", e.Remediation)
		if b := e.RemediationBundle; b != nil {
			w.remediationContext(b)
			w.lines("수정해야 할 보안 조건", b.SecurityCondition)
			w.lines("즉시 완화책", b.ImmediateMitigation)
			w.lines("근본 수정", b.RootFix)
			w.lines("서버·배포 보안 설정", b.Configuration)
			w.lines("기술별 코드 조치", b.CodeExample)
			w.lines("유지할 정상 기능·적용 전제", b.Assumptions)
			w.lines("수정 후 재검증", b.Verification)
			w.lines("남은 위험·확인 사항", b.ResidualRisk)
			w.paragraph("Heading2", "④ AI 수정 작업용 프롬프트")
			w.paragraph("Normal", "기본 작업 방식: 실제 수정 실행형. 실행 범위는 전달받는 AI 세션의 권한을 따릅니다.")
			w.paragraph("Caption", "저장 조치 버전 "+b.RemediationVersion+" · 입력 "+b.InputDigest+" · 증거 버전 "+b.EvidenceVersion+" · 기술 digest "+b.TechnologyDigest)
			w.paragraph("Normal", "고객사의 실제 저장소·컴포넌트·배포 연결을 확인한 뒤 수정합니다. 이 보고서는 운영 재현·push·배포 권한을 부여하지 않습니다.")
			w.paragraph("Code", b.AIPrompt)
		} else {
			w.paragraph("Heading2", "④ AI 수정 작업용 프롬프트")
			text := "유효한 저장 조치·프롬프트 미확보. 내부 상세에서 명시적으로 생성하고 현재 근거를 확인하세요."
			if e.RemediationStale {
				text = "증거·조치·기술 정보 또는 생성기 버전 변경: 오래된 프롬프트는 출력하지 않습니다. 다시 생성하세요."
			}
			w.paragraph("Normal", text)
		}
		w.paragraph("Normal", "모델 제안과 검증된 원인은 별개입니다. 서버 소스 위치·실제 영향 확대는 확인되지 않았습니다. 수정 후 재검증은 별도 승인된 실행에서 수행하세요.")
	}
	if len(in.Entries) == 0 {
		w.paragraph("Normal", "저장된 결과 없음. 비취약 또는 검사 완료를 뜻하지 않습니다.")
	}
	w.chapter("5장. 증적 부록")
	w.paragraph("Normal", "각 항목의 실행·intent·증거 버전·digest·저장 조치 버전으로 보호 증적을 대조하세요. 누락·미실행·불명·미지원은 별도로 남깁니다. 내부 원문과 화면 증적은 인증된 저장소의 현재 권한·버전 검사 후 열람합니다.")
	w.paragraph("Normal", "새 대상 요청·로그인·재현·외부 문서 변환·모델 호출 없이 생성했습니다. 캡처 부재만으로 E③ 판정을 변경하지 않으며 스크린샷만으로 취약점을 확정하지 않습니다.")
	if w.replaced > 0 {
		w.paragraph("Normal", fmt.Sprintf("Word 출력에서 지원하지 않는 제어 문자 %d개를 U+FFFD로 대체했습니다. 원본 저장 증거는 변경하지 않았습니다.", w.replaced))
	}
	if w.err != nil || w.body.Len() > MaxDiagnosticReportBytes {
		return nil, ErrExportBlocked
	}
	var out bytes.Buffer
	z := zip.NewWriter(&out)
	files := []struct{ name, body string }{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Default Extension="png" ContentType="image/png"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/><Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/></Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="document" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`},
		{"word/_rels/document.xml.rels", `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="styles" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`},
		{"word/styles.xml", diagnosticWordStyles},
		{"word/document.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document xmlns:w="` + wordNS + `" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture"><w:body>` + w.body.String() + `<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1134" w:right="1134" w:bottom="1134" w:left="1134" w:header="567" w:footer="567"/></w:sectPr></w:body></w:document>`},
	}
	for i := range w.images {
		relationship := fmt.Sprintf(`<Relationship Id="capture%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/capture-%d.png"/>`, i+1, i+1)
		files[2].body = strings.Replace(files[2].body, "</Relationships>", relationship+"</Relationships>", 1)
	}
	for _, f := range files {
		writer, e := z.Create(f.name)
		if e != nil {
			return nil, e
		}
		if _, e = writer.Write([]byte(f.body)); e != nil {
			return nil, e
		}
	}
	for i, png := range w.images {
		f, e := z.Create(fmt.Sprintf("word/media/capture-%d.png", i+1))
		if e != nil {
			return nil, e
		}
		if _, e = f.Write(png); e != nil {
			return nil, e
		}
	}
	if err = z.Close(); err != nil {
		return nil, err
	}
	if out.Len() > MaxDiagnosticExportBytes {
		return nil, ErrExportBlocked
	}
	return out.Bytes(), nil
}

const diagnosticWordStyles = `<?xml version="1.0" encoding="UTF-8"?><w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="Malgun Gothic" w:hAnsi="Malgun Gothic" w:eastAsia="맑은 고딕"/><w:sz w:val="20"/><w:lang w:val="ko-KR" w:eastAsia="ko-KR"/></w:rPr></w:rPrDefault><w:pPrDefault><w:pPr><w:spacing w:after="120" w:line="300" w:lineRule="auto"/></w:pPr></w:pPrDefault></w:docDefaults><w:style w:type="paragraph" w:styleId="Normal" w:default="1"><w:name w:val="Normal"/></w:style><w:style w:type="paragraph" w:styleId="Title"><w:name w:val="Title"/><w:basedOn w:val="Normal"/><w:pPr><w:jc w:val="center"/><w:spacing w:before="2400" w:after="1200"/></w:pPr><w:rPr><w:b/><w:color w:val="173A5E"/><w:sz w:val="44"/></w:rPr></w:style><w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:basedOn w:val="Normal"/><w:pPr><w:pageBreakBefore/><w:keepNext/><w:outlineLvl w:val="0"/><w:spacing w:after="300"/></w:pPr><w:rPr><w:b/><w:sz w:val="32"/><w:color w:val="173A5E"/></w:rPr></w:style><w:style w:type="paragraph" w:styleId="Heading2"><w:name w:val="heading 2"/><w:basedOn w:val="Normal"/><w:pPr><w:keepNext/><w:outlineLvl w:val="1"/><w:spacing w:before="240" w:after="160"/></w:pPr><w:rPr><w:b/><w:sz w:val="26"/><w:color w:val="254F76"/></w:rPr></w:style><w:style w:type="paragraph" w:styleId="EntryHeading"><w:name w:val="Entry Heading"/><w:basedOn w:val="Heading2"/><w:pPr><w:pageBreakBefore/><w:keepNext/></w:pPr></w:style><w:style w:type="paragraph" w:styleId="Heading3"><w:name w:val="heading 3"/><w:basedOn w:val="Normal"/><w:pPr><w:keepNext/><w:outlineLvl w:val="2"/></w:pPr><w:rPr><w:b/><w:sz w:val="22"/></w:rPr></w:style><w:style w:type="paragraph" w:styleId="Code"><w:name w:val="Code"/><w:basedOn w:val="Normal"/><w:pPr><w:shd w:fill="F1F4F7"/><w:spacing w:line="240"/></w:pPr><w:rPr><w:rFonts w:ascii="Consolas" w:hAnsi="Consolas" w:eastAsia="맑은 고딕"/><w:sz w:val="18"/></w:rPr></w:style><w:style w:type="paragraph" w:styleId="Caption"><w:name w:val="caption"/><w:basedOn w:val="Normal"/><w:rPr><w:sz w:val="17"/><w:color w:val="526478"/></w:rPr></w:style><w:style w:type="table" w:styleId="ReportTable"><w:name w:val="Report Table"/><w:tblPr><w:tblBorders><w:top w:val="single" w:sz="4" w:color="A8B8C8"/><w:left w:val="single" w:sz="4" w:color="A8B8C8"/><w:bottom w:val="single" w:sz="4" w:color="A8B8C8"/><w:right w:val="single" w:sz="4" w:color="A8B8C8"/><w:insideH w:val="single" w:sz="4" w:color="A8B8C8"/><w:insideV w:val="single" w:sz="4" w:color="A8B8C8"/></w:tblBorders><w:tblCellMar><w:top w:w="80" w:type="dxa"/><w:left w:w="100" w:type="dxa"/><w:bottom w:w="80" w:type="dxa"/><w:right w:w="100" w:type="dxa"/></w:tblCellMar></w:tblPr></w:style></w:styles>`

func (w *diagnosticWord) picture(png []byte, width, height int) {
	n := len(w.images) + 1
	w.images = append(w.images, append([]byte(nil), png...))
	cx, cy := int64(width)*9525, int64(height)*9525
	if cx > 5_500_000 {
		cy = cy * 5_500_000 / cx
		cx = 5_500_000
	}
	if cy > 7_000_000 {
		cx = cx * 7_000_000 / cy
		cy = 7_000_000
	}
	fmt.Fprintf(&w.body, `<w:p><w:r><w:drawing><wp:inline distT="0" distB="0" distL="0" distR="0"><wp:extent cx="%d" cy="%d"/><wp:docPr id="%d" name="capture-%d" descr="실행 당시 수집한 비민감 영역"/><wp:cNvGraphicFramePr/><a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/picture"><pic:pic><pic:nvPicPr><pic:cNvPr id="%d" name="capture-%d.png"/><pic:cNvPicPr/></pic:nvPicPr><pic:blipFill><a:blip r:embed="capture%d"/><a:stretch><a:fillRect/></a:stretch></pic:blipFill><pic:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="%d" cy="%d"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></pic:spPr></pic:pic></a:graphicData></a:graphic></wp:inline></w:drawing></w:r></w:p>`, cx, cy, n, n, n, n, n, cx, cy)
}

func (w *diagnosticWord) remediationContext(b *remediation.Bundle) {
	c := b.Context
	w.paragraph("Heading3", "수정 작업의 독립 맥락")
	w.table([][]string{{"맥락", "저장 값"}, {"출처", remediationSourceLabel(c.Source)}, {"컴포넌트 / 역할", c.Component + " / " + c.ComponentRole}, {"기능·엔드포인트", c.Endpoint}, {"재현 사전 조건", c.Prerequisites}, {"관찰", c.Observation}, {"유지할 정상 동작", c.ExpectedBehavior}, {"차단할 취약 동작", c.ProhibitedBehavior}})
	w.paragraph("Normal", "사용자 제공 요약은 자동 검증된 관찰이 아닙니다. 미확인 항목은 저장소와 승인된 근거에서 먼저 확인하세요.")
	if conflicts := remediationContextConflicts(c); len(conflicts) > 0 {
		w.paragraph("Normal", "사용자 보완과 원본 상충 가능·대조 필요: "+strings.Join(conflicts, ", "))
	}
	if c.ObservationsTruncated {
		w.paragraph("Normal", "관측 또는 기술 목록이 잘렸습니다. 전체 단계 연결은 미확인입니다.")
	}
	for _, o := range c.Observations {
		w.paragraph("Heading3", "보존된 원본 맥락 · "+remediationSourceLabel(o.Source))
		w.paragraph("Caption", "근거 "+o.EvidenceRef+" · 관측 시각 "+o.ObservedAt+" · 실행 "+o.RunID+" · 호출 "+o.CallID+" · 단계 "+o.StepID+" · 선행 단계 "+strings.Join(o.DependsOn, ", "))
		w.table([][]string{{"컴포넌트 / 역할", o.Component + " / " + o.ComponentRole}, {"기능·엔드포인트", o.Endpoint}, {"재현 전제", o.Prerequisites}, {"관측·추정 내용", o.Observation}, {"유지할 정상 동작", o.ExpectedBehavior}, {"차단할 취약 동작", o.ProhibitedBehavior}})
		if o.Truncated {
			w.paragraph("Normal", "이 관측 출력 또는 단계가 잘렸습니다. 완전한 재현으로 해석하지 마세요.")
		}
	}
	if len(b.MissingContextFields) > 0 {
		w.paragraph("Normal", "보완이 필요한 맥락: "+strings.Join(b.MissingContextFields, ", ")+". 수정 전에 확인하세요.")
	}
	for _, tech := range b.Technologies {
		w.paragraph("Normal", fmt.Sprintf("%s: %s %s · 컴포넌트 %s / %s · 출처 %s · 확인 수준 %s · 근거 %s · 관측 시각 %s", tech.Kind, tech.Name, tech.Version, tech.Component, tech.ComponentRole, remediationSourceLabel(tech.Source), tech.Confidence, tech.EvidenceRef, tech.ObservedAt))
	}
	w.paragraph("Normal", "출처와 확인 수준은 별개입니다. 사용자 제공·수동 검토는 프로그램의 실행 관측 또는 취약점 확정이 아닙니다.")
	if conflicts := remediationTechnologyConflicts(b.Technologies); len(conflicts) > 0 {
		w.paragraph("Normal", "기술 상충 가능·대조 필요: "+strings.Join(conflicts, ", ")+". 여러 기술이 함께 쓰이는지 확인하세요.")
	}
}
