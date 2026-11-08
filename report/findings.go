package report

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/dlp"
)

// 취약점 페이지의 「내보내기」용 렌더링: 여러 건의 findings 테이블 행을 요약 Markdown,
// 개별 Markdown, 또는 CSV 로 렌더링한다. JSON 은 server 계층이 DTO 로 직접 직렬화하므로
// 여기서는 다루지 않는다.

// sortFindingsForExport 는 심각도 내림차순, 그다음 시각 역순으로 정렬한다. 요약 보고서의 그룹화와 동일하다.
func sortFindingsForExport(fs []*db.DBFinding) {
	sort.SliceStable(fs, func(i, j int) bool {
		ri, rj := sevRank[fs[i].Severity], sevRank[fs[j].Severity]
		if ri != rj {
			return ri < rj // sevRank 는 값이 작을수록 심각하다
		}
		return fs[i].CreatedAt.After(fs[j].CreatedAt)
	})
}

// findingTitle 은 취약점의 사람이 읽을 수 있는 제목을 고른다: 이름 → 유형 → 「미분류」.
func findingTitle(f *db.DBFinding) string {
	return nz(f.Name, nz(f.VulnClass, "미분류"))
}

// FindingsMarkdown 은 여러 건의 findings 를 하나의 요약 보고서로 통합한다(요약 + 심각도별
// 그룹화, 각 건은 유형/상태/소속 작업/증거/상세 보고서를 포함).
func FindingsMarkdown(fs []*db.DBFinding, generatedAt time.Time) string {
	items, err := ProjectFindings(fs)
	if err != nil || !validExportTime(generatedAt) {
		return blockedReport
	}
	sortFindingsForExport(items)

	var b strings.Builder
	b.WriteString("# 취약점 요약 보고서\n\n")
	b.WriteString(exportNotice)
	fmt.Fprintf(&b, "- **생성 시각**: %s\n", generatedAt.UTC().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "- **취약점 총계**: %d건\n\n", len(items))

	// 요약: 심각도별 집계.
	counts := map[string]int{}
	for _, f := range items {
		counts[f.Severity]++
	}
	b.WriteString("## 요약\n\n")
	b.WriteString("| 심각도 | 개수 |\n| --- | --- |\n")
	for _, s := range []struct{ key, label string }{
		{"critical", "심각"}, {"high", "높음"}, {"medium", "중간"}, {"low", "낮음"},
		{"info", "정보"}, {dlp.Redacted, "미분류"},
	} {
		fmt.Fprintf(&b, "| %s | %d |\n", s.label, counts[s.key])
	}
	b.WriteString("\n")

	if len(items) == 0 {
		b.WriteString("_일치하는 취약점이 없습니다._\n")
		return b.String()
	}

	b.WriteString("## 취약점 상세\n\n")
	for i, f := range items {
		fmt.Fprintf(&b, "### %d. [%s] %s\n\n", i+1, strings.ToUpper(nz(f.Severity, "info")), findingTitle(f))
		if f.VulnClass != "" {
			fmt.Fprintf(&b, "- **유형**: %s\n", f.VulnClass)
		}
		fmt.Fprintf(&b, "- **상태**: %s\n", nz(f.Status, "pending"))
		if desc := strings.TrimSpace(f.TaskDescription); desc != "" {
			fmt.Fprintf(&b, "- **소속 작업**: %s\n", desc)
		}
		fmt.Fprintf(&b, "- **발견 시각**: %s\n\n", f.CreatedAt.Format("2006-01-02 15:04:05"))
		if s := strings.TrimSpace(f.Summary); s != "" {
			fmt.Fprintf(&b, "%s\n\n", s)
		}
		if e := strings.TrimSpace(f.Evidence); e != "" {
			fmt.Fprintf(&b, "**증거:**\n\n```\n%s\n```\n\n", e)
		}
		if rep := strings.TrimSpace(f.Report); rep != "" {
			b.WriteString("**상세 보고서:**\n\n")
			b.WriteString(rep)
			b.WriteString("\n\n")
		}
		b.WriteString(findingTrafficMarkdown(f, false))
		b.WriteString("---\n\n")
	}
	return b.String()
}

// SingleFindingMarkdown 은 개별 취약점을 독립된 Markdown 한 건으로 렌더링한다(「취약점 하나당 파일 하나」 묶음에 사용).
func SingleFindingMarkdown(f *db.DBFinding, generatedAt time.Time) string {
	items, err := ProjectFindings([]*db.DBFinding{f})
	if err != nil || !validExportTime(generatedAt) {
		return blockedReport
	}
	f = items[0]
	var b strings.Builder
	fmt.Fprintf(&b, "# [%s] %s\n\n", strings.ToUpper(nz(f.Severity, "info")), findingTitle(f))
	b.WriteString(exportNotice)
	if f.VulnClass != "" {
		fmt.Fprintf(&b, "- **유형**: %s\n", f.VulnClass)
	}
	fmt.Fprintf(&b, "- **심각도**: %s\n", nz(f.Severity, "info"))
	fmt.Fprintf(&b, "- **상태**: %s\n", nz(f.Status, "pending"))
	if desc := strings.TrimSpace(f.TaskDescription); desc != "" {
		fmt.Fprintf(&b, "- **소속 작업**: %s\n", desc)
	}
	fmt.Fprintf(&b, "- **발견 시각**: %s\n", f.CreatedAt.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "- **생성 시각**: %s\n\n", generatedAt.UTC().Format("2006-01-02 15:04:05"))
	if s := strings.TrimSpace(f.Summary); s != "" {
		fmt.Fprintf(&b, "## 개요\n\n%s\n\n", s)
	}
	if e := strings.TrimSpace(f.Evidence); e != "" {
		fmt.Fprintf(&b, "## 증거\n\n```\n%s\n```\n\n", e)
	}
	if rep := strings.TrimSpace(f.Report); rep != "" {
		b.WriteString("## 상세 보고서\n\n")
		b.WriteString(rep)
		b.WriteString("\n")
	}
	b.WriteString(findingTrafficMarkdown(f, true))
	return b.String()
}

// FindingFilename uses only fixed severity labels and an internal numeric ID;
// sanitizing a raw title would still leak its letters into the ZIP directory.
func FindingFilename(f *db.DBFinding) string {
	if f == nil || f.ID < 0 {
		return "finding.md"
	}
	sev := "unknown"
	if s := exportSeverity(f.Severity); s != dlp.Redacted {
		sev = s
	}
	return fmt.Sprintf("%s_finding_%d.md", sev, f.ID)
}

// FindingsCSV 는 여러 건의 findings 를 CSV 로 렌더링한다(UTF-8 BOM 포함, Excel 이 한글을
// 올바르게 인식하도록). 긴 report/evidence 전문은 담지 않고 요약 성격의 필드만 넣는다.
// 전문이 필요하면 Markdown/JSON 으로 내보낸다.
func FindingsCSV(fs []*db.DBFinding) []byte {
	items, err := ProjectFindings(fs)
	if err != nil {
		return []byte(blockedReport)
	}
	sortFindingsForExport(items)

	var buf bytes.Buffer
	buf.WriteString("\xEF\xBB\xBF") // UTF-8 BOM
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"ID", "이름", "유형", "심각도", "상태", "소속 작업", "발견 시각", "개요", "트래픽 증거 개수", "트래픽 증거 ID"})
	for _, f := range items {
		_ = w.Write([]string{
			fmt.Sprintf("%d", f.ID),
			findingTitle(f),
			f.VulnClass,
			nz(f.Severity, "info"),
			nz(f.Status, "pending"),
			f.TaskDescription,
			f.CreatedAt.Format("2006-01-02 15:04:05"),
			strings.TrimSpace(f.Summary),
			fmt.Sprint(len(f.TrafficBindings)), findingTrafficIDs(f),
		})
	}
	w.Flush()
	return buf.Bytes()
}

func findingTrafficIDs(f *db.DBFinding) string {
	ids := make([]string, 0, len(f.TrafficBindings))
	for _, b := range f.TrafficBindings {
		ids = append(ids, fmt.Sprint(b.ID))
	}
	return strings.Join(ids, ",")
}

func findingTrafficMarkdown(f *db.DBFinding, attachments bool) string {
	stale := f.Report != "" && f.EvidenceVersion != f.ReportEvidenceVersion
	if len(f.TrafficBindings) == 0 && !stale {
		return ""
	}
	var out strings.Builder
	out.WriteString("\n## 관련 트래픽 증거\n\n")
	fmt.Fprintf(&out, "증거 버전: %d, 바인딩 개수: %d.\n\n", f.EvidenceVersion, len(f.TrafficBindings))
	if stale {
		out.WriteString("증거가 변경되어 상세 보고서를 갱신해야 합니다.\n\n")
	}
	for i, b := range f.TrafficBindings {
		fmt.Fprintf(&out, "%d. **증거 #%d · %s**: `%s %s`, 상태 코드 %d\n", i+1, b.ID, b.Role, b.Snapshot.Method, strings.ReplaceAll(b.Snapshot.URL, "`", "%60"), b.Snapshot.Status)
		if b.Note != "" {
			fmt.Fprintf(&out, "   %s\n", strings.ReplaceAll(b.Note, "\n", "\n   "))
		}
		if attachments {
			fmt.Fprintf(&out, "   [요청 메시지](evidence/%d/%d/request.http) · [응답 메시지](evidence/%d/%d/response.http)\n", f.ID, b.ID, f.ID, b.ID)
		}
	}
	out.WriteString("\n")
	return out.String()
}
