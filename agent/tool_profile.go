package agent

import (
	actool "github.com/Autumn-27/norma/tool"
)

// toolSource는 프로필 항목을 어디서 채우는지 정한다.
type toolSource int

const (
	// fromBase: 원본 도구 목록(WorkerTools·PlannerTools·기본 도구)에서 같은 이름의 첫 도구를 고른다.
	fromBase toolSource = iota
	// bound: 대상으로 요청을 보내거나 E② 결속이 필요한 도구다. 호출자가 넘긴 E② 판으로만 채우고,
	// 원본 목록에 같은 이름이 있어도(호스트 Bash 등) 절대 그쪽을 고르지 않는다.
	bound
)

type toolProfileEntry struct {
	name     string
	source   toolSource
	required bool // bound 항목이 비면 루프를 시작하지 않는다.
}

// toolProfile은 한 실행 단계에서 모델에 보일 수 있는 도구 목록이다(루프 복원 결정안 §1).
// 원본 루프의 도구 목록에서 이 목록으로 고르고, 목록 밖 도구가 섞이면 시작 전에 거부한다.
// 출력 순서는 항목 순서다. 프롬프트 관측의 도구 digest가 순서에 묶여 있어 순서도 계약이다.
type toolProfile struct {
	name    string
	entries []toolProfileEntry
}

// 진단 Worker 프로필. 복원 단계마다 이 목록을 바꾼다(루프 복원 결정안 §2·§7).
// 그래프 도구 순서는 원본 WorkerTools 순서다. report_finding(확정 기록)과 insert_assets(제품 DB 역할이 assets 쓰기를
// 허용하지 않음)는 아직 넣지 않는다.
var diagnosisWorkerProfile = toolProfile{name: "diagnosis_worker", entries: []toolProfileEntry{
	{name: "Bash", source: bound, required: true},
	{name: "list_findings", source: fromBase},
	{name: "record_fact", source: fromBase},
	{name: "list_assets", source: fromBase},
	{name: "search_all_worker_traces", source: fromBase},
	{name: "get_worker_trace", source: fromBase},
	{name: "node_detail", source: fromBase},
	{name: "submit_diagnostic_candidate", source: bound},
}}

// 진단 Planner 프로필. 순서는 PlannerTools 순서다. Planner에는 대상으로 보내는 도구가 없다.
var diagnosisPlannerProfile = toolProfile{name: "diagnosis_planner", entries: []toolProfileEntry{
	{name: "graph_overview", source: fromBase}, {name: "list_findings", source: fromBase},
	{name: "list_facts", source: fromBase}, {name: "node_detail", source: fromBase},
	{name: "expand_digest", source: fromBase}, {name: "get_worker_output", source: fromBase},
	{name: "get_worker_trace", source: fromBase}, {name: "search_all_worker_traces", source: fromBase},
	{name: "list_goals", source: fromBase}, {name: "add_intent", source: fromBase},
	{name: "list_companies", source: fromBase}, {name: "list_assets", source: fromBase},
	{name: "list_untested_assets", source: fromBase},
}}

// selectTools는 원본 목록 base와 E② 판 boundTools로 프로필 도구를 만든다. wrap은 base에서 고른 도구에만 씌운다.
// 프로필에 없는 이름을 boundTools로 넘기거나 필수 bound 항목이 비면 설정 오류로 본다.
func (p toolProfile) selectTools(base []actool.CoreTool, boundTools map[string]actool.CoreTool, wrap func(actool.CoreTool) actool.CoreTool) ([]actool.CoreTool, error) {
	known := make(map[string]toolSource, len(p.entries))
	for _, entry := range p.entries {
		known[entry.name] = entry.source
	}
	for name, tool := range boundTools {
		if source, ok := known[name]; !ok || source != bound || tool == nil || isNilCoreTool(tool) || tool.Name() != name {
			return nil, ErrE2HTTPBinding
		}
	}
	out := make([]actool.CoreTool, 0, len(p.entries))
	for _, entry := range p.entries {
		if entry.source == bound {
			tool := boundTools[entry.name]
			if tool == nil {
				if entry.required {
					return nil, ErrE2HTTPBinding
				}
				continue
			}
			out = append(out, tool)
			continue
		}
		for _, tool := range base {
			if tool == nil || isNilCoreTool(tool) || tool.Name() != entry.name {
				continue
			}
			if wrap != nil {
				tool = wrap(tool)
			}
			out = append(out, tool)
			break
		}
	}
	return out, nil
}

// check는 최종 도구 목록(진단 기록 래퍼를 씌운 뒤)이 프로필 안인지 본다. 이름이 프로필 밖이거나 겹치거나
// MCP 도구면 거부한다. MCP는 이름이 프로필과 같아도 진단 단계에 들어가지 않는다(탐색 실행기 결정안 §2-2).
func (p toolProfile) check(tools []actool.CoreTool) error {
	if e2HTTPHasMCPTool(tools) {
		return ErrE2HTTPBinding
	}
	allowed := make(map[string]bool, len(p.entries))
	for _, entry := range p.entries {
		allowed[entry.name] = true
	}
	seen := make(map[string]bool, len(tools))
	for _, tool := range tools {
		if tool == nil || isNilCoreTool(tool) || !allowed[tool.Name()] || seen[tool.Name()] {
			return ErrE2HTTPBinding
		}
		seen[tool.Name()] = true
	}
	return nil
}
