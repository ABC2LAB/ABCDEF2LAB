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
	// description이 있으면 원본 도구의 동작은 그대로 두고 모델에 보이는 설명만 진단 판으로 바꾼다(DecorateTool).
	// 원본 설명이 진단 경로에서 틀린 약속을 하는 도구에만 쓴다.
	description string
}

// toolProfile은 한 실행 단계에서 모델에 보일 수 있는 도구 목록이다(루프 복원 결정안 §1).
// 원본 루프의 도구 목록에서 이 목록으로 고르고, 목록 밖 도구가 섞이면 시작 전에 거부한다.
// 출력 순서는 항목 순서다. 프롬프트 관측의 도구 digest가 순서에 묶여 있어 순서도 계약이다.
type toolProfile struct {
	name    string
	entries []toolProfileEntry
}

// 진단 Worker 프로필. 복원 단계마다 이 목록을 바꾼다(루프 복원 결정안 §2·§7).
// 그래프 도구 순서는 원본 WorkerTools 순서다. report_finding(확정 기록)은 아직 넣지 않는다. insert_assets는 승인
// 대상 origin 안의 자산만 받는 E② 판으로만 채운다(C24, 루프 복원 결정안 §9-5). 승인 origin을 모르는 실행에는 없다.
var diagnosisWorkerProfile = toolProfile{name: "diagnosis_worker", entries: []toolProfileEntry{
	{name: "Bash", source: bound, required: true},
	// 원본 WebFetch의 E② 판. 같은 Bash 실행기로 가져온다(결정안 §2).
	{name: "WebFetch", source: bound},
	// 보관소 안만 보는 파일 도구(C29, 작업 공간 결정안 §2-4). 러너가 보관소를 쓸 때만 채워진다.
	{name: "Read", source: bound},
	{name: "Write", source: bound},
	{name: "Edit", source: bound},
	{name: "LS", source: bound},
	{name: "list_findings", source: fromBase},
	{name: "record_fact", source: fromBase},
	{name: "insert_assets", source: bound},
	{name: "list_assets", source: fromBase},
	{name: "search_all_worker_traces", source: fromBase},
	{name: "get_worker_trace", source: fromBase},
	{name: "node_detail", source: fromBase},
	{name: "submit_diagnostic_candidate", source: bound},
	// 시험 계정 세션이 있는 실행에만 채운다(C14 refresh_session). 세션 없는 실행(평가 포함)의 도구 목록은 그대로다.
	{name: "refresh_session", source: bound},
}}

// 진단 Planner 프로필. 순서는 PlannerTools 순서다. Planner에는 대상으로 보내는 도구가 없다.
var diagnosisPlannerProfile = toolProfile{name: "diagnosis_planner", entries: []toolProfileEntry{
	{name: "graph_overview", source: fromBase}, {name: "list_findings", source: fromBase},
	{name: "list_facts", source: fromBase}, {name: "node_detail", source: fromBase},
	{name: "expand_digest", source: fromBase}, {name: "get_worker_output", source: fromBase},
	{name: "get_worker_trace", source: fromBase}, {name: "search_all_worker_traces", source: fromBase},
	{name: "list_goals", source: fromBase}, {name: "add_intent", source: fromBase},
	// 목표 도구(C26). goal_met은 원본처럼 기본 바인딩이 없어 운영자가 묶을 때만 보인다.
	{name: "prove_goal", source: fromBase}, {name: "goal_met", source: fromBase},
	// 작업 제어(C27). Planner가 Worker 실행 중에도 돌므로 원본처럼 실행 중 작업을 멈추거나 방향을 고친다. kill_work로
	// 진행 중 E② 호출이 끊기면 그 intent만 결과 불명으로 끝난다(C01 뒤, 결과 불명 송신은 다시 보내지 않음).
	{name: "kill_work", source: fromBase}, {name: "steer_work", source: fromBase},
	{name: "list_companies", source: fromBase}, {name: "list_assets", source: fromBase},
	// add_task_scope는 커버리지 분모만 바꾸고 E② 승인 범위는 바꾸지 않는다(진단 경로는 task_scope를 승인 근거로
	// 읽지 않음). 원본 설명은 이 도구를 "인가 경계"라 부르고 넓히라고 권해서, 그대로 두면 모델이 승인 밖 호스트로
	// intent를 만들고 거부 상한(C01 전에는 실행 정지)에 닿을 수 있다. 그래서 설명만 진단 판으로 바꾼다.
	// add_company_scope는 제품 DB 역할이 companies·company_scope를 읽기만 해서 넣어도 늘 실패하므로
	// insert_assets와 같이 DB 역할 결정(열린 항목 5) 뒤에 넣는다.
	{name: "add_task_scope", source: fromBase, description: diagnosisTaskScopeDescription},
	{name: "list_untested_assets", source: fromBase},
}}

const diagnosisTaskScopeDescription = "Add test scope to [this task]. In this diagnosis run, scope only widens the asset-coverage denominator and asset membership. " +
	"It does NOT change which hosts, ports, paths or methods diagnostic requests may reach: the approved diagnosis policy decides that, and requests outside it are refused " +
	"(refusals count toward the run's denial limit and can stop the run). Do not create intents for hosts, ports or paths outside the approved target just because you added them here.\n" +
	"kind supports: company (all assets under a company) / root_domain (a whole root domain, including all subdomains) / subdomain (one exact subdomain) / ip / cidr / icp / keyword.\n" +
	"value: for company pass the company name or id (the company must already exist); for root_domain/subdomain pass a domain; for ip/cidr pass an IP or network; for icp/keyword pass a filing number or company keyword.\n" +
	"Always give reason to state the basis (auditable). For several entries use the entries array."

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
			if entry.description != "" {
				tool = DecorateTool(tool, entry.description, nil)
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
