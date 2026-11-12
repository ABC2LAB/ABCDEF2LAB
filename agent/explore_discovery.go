package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/Autumn-27/artex/diagnosisflow"
	actool "github.com/Autumn-27/norma/tool"
)

// exploreDiscoveryReporter는 탐색 단계의 발견 저장소다(diagnosisflow.Controller.ReportStatic).
type exploreDiscoveryReporter interface {
	ReportStatic(context.Context, []diagnosisflow.Discovery) (int, error)
}

// 보고 입력 크기 상한. 발견 32개 × (경로 2KiB + 이름 64개 × 64바이트) 안쪽이다.
const exploreDiscoveryInputLimit = 256 << 10

// newExploreDiscoveryTool은 탐색 에이전트의 발견 보고 도구다(탐색 실행기 결정안 §2-1-b·§2-3). 번들·문서·API 문서에서
// 찾은 엔드포인트를 메서드·경로·이름만으로 넘긴다. 저장 여부와 범위 검사는 프로그램(ReportStatic)이 정한다.
// 이 도구는 요청을 보내지 않는다.
func newExploreDiscoveryTool(reporter exploreDiscoveryReporter) actool.CoreTool {
	names := map[string]any{"type": "array", "maxItems": 32, "items": map[string]any{"type": "string", "maxLength": 64}}
	return actool.Build(actool.Spec{
		Name: "report_discovery",
		Description: "Report endpoint metadata of the approved target from scripts, pages, API documents or page runtime hooks. " +
			"Give only the method, the absolute path on this origin, and the names of query parameters and JSON body fields; never values. " +
			"Use source api_recon_static for static analysis or api_recon_runtime for page hooks. Runtime reports are still inferred; only host-owned boundary records establish observed requests. " +
			"The program re-checks the approved scope and drops anything outside it or malformed. Reported endpoints are unverified discoveries, not vulnerabilities. This tool sends no request.",
		Schema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"discoveries": map[string]any{"type": "array", "minItems": 1, "maxItems": 32, "items": map[string]any{
				"type": "object", "additionalProperties": false, "properties": map[string]any{
					"method":       map[string]any{"type": "string", "maxLength": 7},
					"path":         map[string]any{"type": "string", "maxLength": 128},
					"query_keys":   names,
					"body_keys":    names,
					"content_type": map[string]any{"type": "string", "maxLength": 129},
					"source":       map[string]any{"type": "string", "enum": []string{diagnosisflow.SourceAPIReconStatic, diagnosisflow.SourceAPIReconRuntime}},
					"transport":    map[string]any{"type": "string", "enum": []string{"http", "sse", "websocket"}},
				}, "required": []string{"method", "path"}}},
		}, "required": []string{"discoveries"}},
		Concurrent: func(json.RawMessage) bool { return false },
		Run: func(ctx context.Context, input json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			if reporter == nil {
				return actool.Errorf("discovery_report_unavailable"), nil
			}
			var in struct {
				Discoveries []diagnosisflow.Discovery `json:"discoveries"`
			}
			decoder := json.NewDecoder(bytes.NewReader(input))
			decoder.DisallowUnknownFields()
			if len(input) > exploreDiscoveryInputLimit || decoder.Decode(&in) != nil || len(in.Discoveries) == 0 {
				return actool.Errorf("discovery_report_input_invalid"), nil
			}
			if _, err := decoder.Token(); err != io.EOF {
				return actool.Errorf("discovery_report_input_invalid"), nil
			}
			stored, err := reporter.ReportStatic(ctx, in.Discoveries)
			switch {
			case errors.Is(err, diagnosisflow.ErrDenied):
				// 초기 탐색 단계가 아니거나 한 번에 너무 많다.
				return actool.Errorf("discovery_report_rejected"), nil
			case err != nil:
				return actool.Errorf("discovery_report_unavailable"), nil
			}
			return actool.Text(fmt.Sprintf("Stored %d of %d reported endpoints as unverified discoveries. The rest were outside the approved scope, malformed, or over the discovery limit.", stored, len(in.Discoveries))), nil
		},
	})
}
