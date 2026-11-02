// Package report renders a deliverable Markdown report from the exploration
// graph: confirmed findings (with severity/evidence) (docs §15 P5).
package report

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/dlp"
)

// Input bundles what the report needs.
type Input struct {
	Title       string
	Goal        string
	GeneratedAt time.Time
	AssetCounts map[string]int
	Findings    []*db.Node
}

type findingView struct {
	NodeID    int64
	FindingID int64
	VulnClass string
	Name      string
	Severity  string
	Summary   string
	PoC       string
}

func parseFinding(n *db.Node, policy *dlp.Policy) (findingView, error) {
	if n == nil || n.ID < 0 || n.FindingID < 0 {
		return findingView{}, ErrExportBlocked
	}
	safe, err := policy.Project(context.Background(), n.Payload)
	if err != nil {
		return findingView{}, ErrExportBlocked
	}
	var p struct {
		VulnClass string `json:"vulnclass"`
		Name      string `json:"name"`
		Severity  string `json:"severity"`
		Summary   string `json:"summary"`
		Evidence  string `json:"evidence"`
	}
	if err := json.Unmarshal(safe.Bytes(), &p); err != nil {
		return findingView{}, ErrExportBlocked
	}
	return findingView{NodeID: n.ID, FindingID: n.FindingID, VulnClass: p.VulnClass, Name: p.Name, Severity: exportSeverity(p.Severity), Summary: p.Summary, PoC: p.Evidence}, nil
}

var sevRank = map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3, "info": 4, "": 5, dlp.Redacted: 5}

// Markdown renders the report.
func Markdown(in Input) string {
	if len(in.Findings) > MaxFindings || !validExportTime(in.GeneratedAt) {
		return blockedReport
	}
	counts, err := exportAssetCounts(in.AssetCounts)
	if err != nil {
		return blockedReport
	}
	policy, err := dlp.New(dlp.Limits{
		MaxInputBytes: 128 << 10, MaxOutputBytes: 4 << 10, MaxDepth: 16, MaxObjectFields: 32, MaxArrayItems: 256,
	}, map[string]dlp.Rule{
		"vulnclass": dlp.Redact(), "name": dlp.Redact(), "summary": dlp.Redact(), "evidence": dlp.Redact(),
		"severity": dlp.Enum("critical", "high", "medium", "low", "info", ""),
	})
	if err != nil {
		return blockedReport
	}
	fs := make([]findingView, 0, len(in.Findings))
	inputBytes := 0
	for _, n := range in.Findings {
		if n == nil || len(n.Payload) > maxGraphBytes-inputBytes {
			return blockedReport
		}
		inputBytes += len(n.Payload)
		f, err := parseFinding(n, policy)
		if err != nil {
			return blockedReport
		}
		fs = append(fs, f)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# 침투 테스트 보고서: %s\n\n", nz(maskText(in.Title), "이름 없는 작업"))
	b.WriteString(exportNotice)
	fmt.Fprintf(&b, "- **작업 목표**: %s\n", nz(maskText(in.Goal), "(지정되지 않음)"))
	fmt.Fprintf(&b, "- **생성 시각**: %s\n\n", in.GeneratedAt.UTC().Format("2006-01-02 15:04:05"))

	// summary
	fmt.Fprintf(&b, "## 요약\n\n")
	fmt.Fprintf(&b, "- 확인된 취약점: **%d**건\n", len(in.Findings))
	fmt.Fprintf(&b, "- 자산: ")
	var types []string
	for t := range counts {
		types = append(types, t)
	}
	sort.Strings(types)
	for i, t := range types {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s %d", t, counts[t])
	}
	b.WriteString("\n\n")

	// findings
	fmt.Fprintf(&b, "## 취약점\n\n")
	if len(in.Findings) == 0 {
		b.WriteString("_이번에 확인된 취약점이 없습니다._\n\n")
	} else {
		sort.SliceStable(fs, func(i, j int) bool { return sevRank[fs[i].Severity] < sevRank[fs[j].Severity] })
		for i, f := range fs {
			fmt.Fprintf(&b, "### %d. [%s] %s\n\n", i+1, strings.ToUpper(nz(f.Severity, "info")), nz(f.Name, nz(f.VulnClass, "미분류")))
			fmt.Fprintf(&b, "- **판정 노드**: #%d\n", f.NodeID)
			if f.FindingID > 0 {
				fmt.Fprintf(&b, "- **취약점 참조**: #%d\n", f.FindingID)
			}
			b.WriteString("\n")
			fmt.Fprintf(&b, "%s\n\n", nz(f.Summary, ""))
			if f.PoC != "" {
				fmt.Fprintf(&b, "**PoC / 증거:**\n\n```\n%s\n```\n\n", f.PoC)
			}
		}
	}

	return b.String()
}

func nz(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}
