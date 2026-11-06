package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

const findingIDGuidance = "\n\n**Vulnerability ID convention**: finding_id is the separate vulnerability record ID; finding_node_id is the exploration node ID. The id in list_findings / list_task_findings / node_detail / get_task_node_detail remains the exploration node ID; read the separate number from finding_id in the same response. get_finding_traffic / bind_finding_traffic use the separate finding_id. The finding_id parameter of the older update_finding_report still takes finding_node_id. Don't use the number on the first line of report_finding for evidence tools, and don't guess other numbers after an ID error."

// The server supplies the persisted setting. A missing setting/host is off.
// Consulted at assembly and again on writes so an already-running session
// cannot keep binding after the user switches the feature off.
func findingTrafficBindingEnabled(ctx context.Context) bool {
	h := RuntimeHooksFrom(ctx).FindingTrafficBindingEnabled
	return h != nil && h()
}

// Applied after ToolResolve: user descriptions and prompts remain intact, while
// all actual reporters (including Planner and custom chat agents) see the same
// API contract. Disabled/unbound tools are never reintroduced here.
func findingWorkflowTools(ctx context.Context, agentKey string, tools []actool.CoreTool) ([]actool.CoreTool, string) {
	if !findingTrafficBindingEnabled(ctx) {
		out := make([]actool.CoreTool, 0, len(tools))
		for _, tool := range tools {
			if tool.Name() == "bind_finding_traffic" {
				continue
			}
			if agentKey == "reporter" && (tool.Name() == "traffic_search" || tool.Name() == "traffic_get" || tool.Name() == "traffic_blob") {
				continue
			}
			switch tool.Name() {
			case "report_finding", "add_hint", "add_task_hint":
				// Work on a copy: toggling back on must restore the original schema.
				raw, _ := json.Marshal(tool.InputSchema())
				var schema map[string]any
				if json.Unmarshal(raw, &schema) == nil {
					stripTrafficParameters(schema)
					tool = DecorateTool(tool, tool.Description(), schema)
				}
			}
			out = append(out, tool)
		}
		return out, ""
	}
	out := append([]actool.CoreTool(nil), tools...)
	has := map[string]bool{}
	for i, tool := range out {
		has[tool.Name()] = true
		note := ""
		switch tool.Name() {
		case "report_finding":
			note = "\nBy default, the report Agent checks and binds traffic before writing the report. The reporter keeps the verification commands, key output, any real traffic IDs and their purposes in evidence, so the report Agent can verify them against the execution record; there's no need to look up extra packets for binding. Explicit immediate binding is still supported: traffic_refs or evidence_hint_id can submit verified references, the latter reading the structured references of the specified hint in this task; if any is invalid, the whole report fails. TCP or no-packet cases don't need these optional parameters. The returned finding_id and finding_node_id refer to the separate record and the exploration node respectively."
		case "add_hint", "add_task_hint":
			note = "\nWhen handing over a confirmed vulnerability, keep the ID, purpose, note and order of verified traffic in the traffic_refs of the matching hint (at the top level for a single hint, in the matching hints element for a batch), and state in text the specific vulnerability it proves. The caller must not hand over only text and drop existing traffic references. Unverified candidates must not be passed as evidence."
		case "get_finding_traffic", "bind_finding_traffic", "list_findings", "list_task_findings", "node_detail", "get_task_node_detail", "update_finding_report":
			note = findingIDGuidance
		}
		if note != "" {
			out[i] = DecorateTool(tool, tool.Description()+note, tool.InputSchema())
		}
	}
	guidance := ""
	if has["report_finding"] || has["add_task_hint"] || has["add_hint"] {
		guidance = "\n\n**Traffic evidence handoff (optional)**: by default, automatic binding is done by the report Agent after the vulnerability is stored and before the report is written. The reporter should keep the verification commands, key output, any real traffic IDs and their purposes in evidence, and pass intent_id when in a task, so the report Agent can trace them; there's no need to look up extra packets for binding. When Auto or Planner reports on someone's behalf, don't drop the references the executor already has. add_hint / add_task_hint can hand over with traffic_refs; explicit immediate binding is still supported through the traffic_refs / evidence_hint_id of report_finding. For TCP or no-packet cases, register normally; don't guess IDs, and don't re-probe just to capture packets."
		if has["add_task_hint"] && !has["add_hint"] {
			guidance += "\nWhen a platform chat has no task context, don't call report_finding directly; hand over to the existing relevant task through add_task_hint so the task's Agent registers it, and check the result with list_task_findings."
		}
		if has["prove_goal"] || has["goal_met"] {
			guidance += "\nBefore judging a goal complete, first finish reporting or handing over the evidence already available in this run. While the evidence handoff is unfinished, don't end the task or cancel Workers just because the vulnerability text is registered; no-packet cases don't require waiting or forcing a capture."
		}
	}
	if has["update_finding_report"] && has["bind_finding_traffic"] && has["get_finding_traffic"] {
		guidance += "\n\n**Automatic traffic linking before the report (on)**: you are responsible for checking and binding traffic for the vulnerability that triggered this run, then writing the report. First get the explicit finding_id and finding_node_id from the JSON returned by report_finding or from get_task_node_detail / list_task_findings. Read the vulnerability details, the execution record of the matching intent and the existing evidence list, preferring the real IDs handed over by the reporter. If this verification is HTTP and the traffic tools are available, filter candidates with traffic_search, then verify one by one with traffic_get that each request and response really supports the vulnerability; domain and time are only for filtering and don't prove ownership. Link the confirmed evidence in reproduction order with bind_finding_traffic(finding_id, traffic_refs), choosing baseline / proof / verification / supporting and stating the purpose. Operate only on this vulnerability; don't create duplicate vulnerabilities or probe the target again. After a successful binding, call get_finding_traffic again to get the latest version, read the bodies you need, then pass the version actually read as evidence_version to update_finding_report (its finding_id parameter still takes finding_node_id). Existing bindings don't need to be appended again. For TCP, uncaptured traffic, unavailable tools or no exact match, skip automatic binding, write the report normally from text and command evidence and state why; never guess just to fill in traffic. Don't claim success when binding fails; keep the existing evidence and explain in the report why it wasn't bound."
	}
	if guidance != "" || has["get_finding_traffic"] || has["update_finding_report"] {
		guidance += findingIDGuidance
	}
	return out, guidance
}

func stripTrafficParameters(schema map[string]any) {
	props, _ := schema["properties"].(map[string]any)
	delete(props, "traffic_refs")
	delete(props, "evidence_hint_id")
	if required, ok := schema["required"].([]any); ok {
		kept := required[:0]
		for _, key := range required {
			if key != "traffic_refs" && key != "evidence_hint_id" {
				kept = append(kept, key)
			}
		}
		schema["required"] = kept
	}
	if hints, ok := props["hints"].(map[string]any); ok {
		if items, ok := hints["items"].(map[string]any); ok {
			stripTrafficParameters(items)
		}
	}
}

// HintTrafficSchema is shared by the task-local and cross-task hint tools.
func HintTrafficSchema() map[string]any {
	return map[string]any{"type": "array", "description": "Optional: verified traffic references matching the specific vulnerability in this hint, keeping their order; after the handoff, report_finding can pass evidence_hint_id to carry these references.", "items": obj(map[string]any{"traffic_id": str("Real traffic ID"), "role": str("baseline / proof / verification / supporting"), "note": str("What conclusion this traffic supports")}, "traffic_id")}
}

func (t *ToolSet) findingRefsFromHint(hintID int64, explicit []db.TrafficRef) ([]db.TrafficRef, error) {
	if hintID <= 0 {
		return db.NormalizeTrafficRefs(explicit)
	}
	n, err := t.ts.GetNode(hintID) // local store only: inherited hints cannot supply evidence
	if err != nil {
		return nil, err
	}
	if n == nil || n.Kind != db.KindHint {
		return nil, fmt.Errorf("evidence_hint_id=%d must be a hint node of this task (inherited hints can't be used directly for binding)", hintID)
	}
	var payload struct {
		Refs []db.TrafficRef `json:"traffic_refs"`
	}
	if err := json.Unmarshal(n.Payload, &payload); err != nil {
		return nil, err
	}
	return db.NormalizeTrafficRefs(append(append([]db.TrafficRef{}, explicit...), payload.Refs...))
}
