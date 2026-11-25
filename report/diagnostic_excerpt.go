package report

import (
	"strings"
	"unicode"

	"github.com/Autumn-27/artex/dlp"
)

const diagnosticExportMask = "***"
const diagnosticExcerptRunes = 240

func prepareDiagnosticExport(in DiagnosticReport) (DiagnosticReport, error) {
	out, err := PrepareDiagnostic(in)
	if err != nil {
		return DiagnosticReport{}, err
	}
	// DLP와 증거 결속 검증을 마친 사본의 표시 값만 줄인다.
	for i := range out.Entries {
		entry := &out.Entries[i]
		entry.Evidence = diagnosticExcerpt(entry.Evidence)
		entry.Report = diagnosticExcerpt(entry.Report)
		for j := range entry.Steps {
			step := &entry.Steps[j]
			step.Observation = diagnosticExcerpt(step.Observation)
		}
	}
	return out, nil
}

func diagnosticExcerpt(value string) string {
	value = strings.ReplaceAll(value, dlp.Redacted, diagnosticExportMask)
	count := 0
	for offset := range value {
		if count == diagnosticExcerptRunes {
			return strings.TrimRightFunc(value[:offset], unicode.IsSpace) + "…"
		}
		count++
	}
	return value
}
