package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
	acperm "github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

// compactIntents distills intents to {id, summary, state, asset_ids, parents,
// yields} so the planner sees both the direction and its LINEAGE — parents (the
// upstream nodes it derived from: facts/intents/findings) and yields (the facts/
// findings it produced) — without pulling full payloads. parentsOf/yieldsOf are
// built from the exploration edges in graph_overview.
func compactIntents(ns []*db.Node, parentsOf, yieldsOf map[int64][]int64) []map[string]any {
	out := make([]map[string]any, 0, len(ns))
	for _, n := range ns {
		var p map[string]any
		_ = json.Unmarshal(n.Payload, &p)
		m := map[string]any{"id": n.ID, "summary": p["summary"], "state": n.State}
		if n.Inherited {
			m["source_task_id"] = n.SourceTaskID
			m["inherited"] = true
		}
		// asset_ids is the structured "which assets this direction covers" signal for
		// dedup; fall back to legacy payload keys (target_ids plural, then target_id
		// single) so intents stored before the rename still surface their anchors.
		if tg, ok := p["asset_ids"]; ok && tg != nil {
			m["asset_ids"] = tg
		} else if tg, ok := p["target_ids"]; ok && tg != nil {
			m["asset_ids"] = tg
		} else if tg, ok := p["target_id"]; ok && tg != nil && tg != "" {
			m["asset_ids"] = []any{tg}
		}
		if ps := parentsOf[n.ID]; len(ps) > 0 {
			m["parents"] = ps // 上游：本意图派生自哪些节点（多个事实可共同产生一个意图）
		}
		if ys := yieldsOf[n.ID]; len(ys) > 0 {
			m["yields"] = ys // 下游：本意图产生了哪些事实/发现
		}
		out = append(out, m)
	}
	return out
}

// ToolSet exposes the PG-backed dual graph (asset + exploration) to an LLM agent.
// One ToolSet is created per planner/worker run; per-run signals live here.
type ToolSet struct {
	findingRecorder FindingRecorder
	as              *db.AssetStore   // asset store (optional; nil = asset tools not available)
	cs              *db.CompanyStore // company store (optional)
	ts              *db.ExplorationStore
	worker          string
	taskID          int64 // PG tasks.id; 0 when unknown (tests / orchestrator cross-task reads)
	// coverageDisabled mirrors tasks.coverage_enabled=false. Stored inverted so the
	// zero value (all existing ToolSet constructions) means ENABLED — matching the
	// DB default (true). When true: graphOverviewData drops the coverage block, the
	// auto-scope hook (insertAssets) is skipped, and add_task_scope/list_untested_assets
	// are filtered out of the agent's tool list. The scope field stays regardless.
	coverageDisabled bool
	// ownerNode is the exploration node that writes attach to: assets this run
	// touches get anchored to it as lineage/provenance (NOT visibility — the asset
	// graph is global and shared). Worker = its claimed intent; planner = begin root.
	ownerNode int64
	GoalMet   bool
	Reason    string
	writes    WriteCounts
	// killWork, if set, terminates a running work by intent id (engine callback,
	// wired by the planner). nil = the kill_work tool reports unavailable.
	killWork func(intentID int64) error
	// steerWork, if set, queues a mid-run course-correction for the work running an
	// intent id (engine callback, wired by the planner): the worker injects it before
	// its next tool call and re-plans, without being killed. nil = tool unavailable.
	steerWork func(intentID int64, msg string) error
	// enrich, if set, receives async auto-completion triggers (DNS resolve for a
	// domain, HTTP probe for a site). nil = no engine enrichment.
	enrich EnrichTrigger
	// notify, if set, wakes the task's planner after a graph change that should be
	// re-planned promptly (currently: a new hint). nil = no wake (the hint is still
	// stored and read on the next round triggered by other events). debounced.
	notify func()
	// notifyFinding, if set, wakes the task's planner when this run reports a finding,
	// carrying (intentID, summary) so the round can spell out which intent found what.
	// Wired for workers; nil elsewhere → falls back to notify (bare wake).
	notifyFinding func(intentID int64, summary string)
	// resumeTask, if set, revives the task after a graph change that should make a
	// stopped task run again (currently: set_goals adds a goal). It flips a terminal/
	// paused task back to running and (re)starts the engine loops — a plain notify()
	// can't, because the planner's terminal gate swallows wakes. Wired ONLY for the
	// main agent (human steering); nil for the goals decomposer and workers.
	resumeTask func()
	// notifyGoal, if set, wakes the planner AND records ONE "人新增了 N 个目标：…" trigger
	// for a whole set_goals call (batch-aware — one call, one trigger, not one per goal)
	// so the next round spells out the added goals (instead of the planner having to
	// spot new open goals in the overview). Wired ONLY for the main agent; nil for the
	// goals decomposer (round-0 has no running planner to inform) and workers → those
	// fall back to the bare notify.
	notifyGoal func(texts []string)
	// notifyHint, if set, wakes the planner AND records ONE "人新增了 N 条战略提示：…"
	// trigger for a whole add_hint call (batch-aware — one call, one trigger) so the next
	// round is told the round was fired by a new hint and spells the hint out, instead of
	// the planner having to spot it folded into the graph overview. Wired for the main
	// agent + cross-task orchestration; nil elsewhere → falls back to the bare notify.
	notifyHint func(texts []string)
}

// SetNotifyGoal wires the goal-add trigger callback (see ToolSet.notifyGoal). Set only
// by the main-agent chat, so runtime-added goals are announced to the planner by name.
func (t *ToolSet) SetNotifyGoal(fn func([]string)) { t.notifyGoal = fn }

// SetNotifyHint wires the hint-add trigger callback (see ToolSet.notifyHint). Set by
// the main-agent chat and cross-task orchestration, so a runtime-added hint fires a
// planner round announced by name instead of a bare wake.
func (t *ToolSet) SetNotifyHint(fn func([]string)) { t.notifyHint = fn }

// SetResumeTask wires the task-revive callback (see ToolSet.resumeTask). Set only by
// the main-agent chat, so runtime-added goals can pull a finished task back to running.
func (t *ToolSet) SetResumeTask(fn func()) { t.resumeTask = fn }

// SetNotify wires the planner-wake callback (see ToolSet.notify). Set by callers
// that hold the task handle (main-agent chat, cross-task orchestration).
func (t *ToolSet) SetNotify(fn func()) { t.notify = fn }

// SetNotifyFinding wires the finding-wake callback (see ToolSet.notifyFinding).
func (t *ToolSet) SetNotifyFinding(fn func(int64, string)) { t.notifyFinding = fn }

// EnrichTrigger is the enrichment engine seen from the tool layer (see package
// enrich). Kept as an interface here to avoid coupling agent → enrich.
type EnrichTrigger interface {
	ResolveDomain(id int64, host string)
	ProbeSite(id int64, url string)
}

// WriteCounts breaks down what a worker persisted this run, by node kind, so the
// engine can log an accurate "wrote back" summary instead of lumping assets and
// findings under "facts" (record_fact → Facts, insert_assets → Assets,
// report_finding → Findings; each element of a batch counts once).
type WriteCounts struct {
	Facts    int
	Assets   int
	Findings int
}

// Total is every node persisted this run, regardless of kind — the
// "explored but persisted nothing" signal (Total == 0).
func (w WriteCounts) Total() int { return w.Facts + w.Assets + w.Findings }

// String은 로그용으로 종류별 건수를 쓴다. 예: "사실 1, 자산 25, 취약점 0".
func (w WriteCounts) String() string {
	return fmt.Sprintf("사실 %d, 자산 %d, 취약점 %d", w.Facts, w.Assets, w.Findings)
}

// Writes reports what this run wrote back, split by node kind (so the engine can
// tell "explored but persisted nothing" apart from a completed intent, and log an
// honest breakdown instead of calling assets/findings "facts").
func (t *ToolSet) Writes() WriteCounts { return t.writes }

func NewToolSet(ts *db.ExplorationStore, worker string) *ToolSet {
	return &ToolSet{ts: ts, worker: worker}
}

// SetTaskID sets the PG task id on this ToolSet so that report_finding can
// dual-write to the standalone findings table (which survives task deletion).
func (t *ToolSet) SetTaskID(id int64) { t.taskID = id }

// SetCoverageEnabled records whether this task has the asset-coverage feature on
// (default enabled). Passing false makes graphOverviewData omit the coverage block
// and DropCoverageTools filter the two coverage-only tools out of the agent's tool
// list. It does NOT stop scope accumulation: insertAssets' auto-scope hook runs
// either way, because task_scope is the task's range boundary (the filter basis for
// asset queries), not merely a coverage denominator.
func (t *ToolSet) SetCoverageEnabled(enabled bool) { t.coverageDisabled = !enabled }

// CoverageDisabled reports whether the coverage feature is off for this task.
func (t *ToolSet) CoverageDisabled() bool { return t.coverageDisabled }

// coverageOnlyTools are the LLM tools that only make sense when asset coverage is
// on. When the feature is off they are filtered out of the agent's tool list so
// they neither pollute the prompt nor let the model build a disabled denominator.
// add_task_scope is deliberately NOT here: task_scope is the task's range boundary
// (the filter basis for asset queries), not merely a coverage denominator, so the
// agents that own范围定义 keep it either way — in lockstep with insertAssets'
// auto-scope hook, which also runs regardless of the switch.
var coverageOnlyTools = map[string]bool{"list_untested_assets": true}

// DropCoverageTools returns tools with the coverage-only ones removed when this
// task has the feature disabled; otherwise it returns tools unchanged.
func (t *ToolSet) DropCoverageTools(tools []actool.CoreTool) []actool.CoreTool {
	if !t.coverageDisabled {
		return tools
	}
	out := tools[:0:0]
	for _, tool := range tools {
		if coverageOnlyTools[tool.Name()] {
			continue
		}
		out = append(out, tool)
	}
	return out
}

// Cross-task reuse: exported accessors returning the per-task tool logic bound to
// THIS ToolSet's store. Host-side orchestration tools build a ToolSet for an
// arbitrary task, then Call these — so cross-task reads/hint reuse the exact
// same logic as the in-task tools. (readTool ignores ToolContext, so Call(…,nil)
// is safe; add_hint is a writeTool but also doesn't deref the context here.)
func (t *ToolSet) GraphOverviewTool() actool.CoreTool      { return t.graphOverview() }
func (t *ToolSet) ListFindingsTool() actool.CoreTool       { return t.listFindings() }
func (t *ToolSet) GetWorkerTraceTool() actool.CoreTool     { return t.getWorkerTrace() }
func (t *ToolSet) ListWorkerTracesTool() actool.CoreTool   { return t.listWorkerTraces() }
func (t *ToolSet) SearchWorkerTracesTool() actool.CoreTool { return t.searchAllWorkerTraces() }
func (t *ToolSet) NodeDetailTool() actool.CoreTool         { return t.nodeDetail() }
func (t *ToolSet) AddHintTool() actool.CoreTool            { return t.addHint() }

// SetEnrich wires the async enrichment engine (DNS/HTTP auto-completion).
func (t *ToolSet) SetEnrich(e EnrichTrigger) { t.enrich = e }

// SetOwnerNode sets the exploration node that writes anchor to (worker: its
// intent node; planner/main: the begin root). Assets created/referenced while
// ownerNode is set are anchored to it as lineage (not visibility).
func (t *ToolSet) SetOwnerNode(id int64) { t.ownerNode = id }

// anchorOwner records a lineage edge from this run's owner node to an asset
// (no-op if unset). Provenance only — the asset graph is global and shared, so
// this no longer affects which assets a task can read.
func (t *ToolSet) anchorOwner(assetID int64) {
	if t.ts != nil && t.ownerNode > 0 && assetID > 0 {
		_ = t.ts.Anchor(t.ownerNode, assetID)
	}
}

// pid parses an id that may arrive as a JSON number or string ("" / 0 → 0).
func pid(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		return v
	}
	return 0
}

// pidList parses a list of ids (number|string), dropping zeros/invalids.
func pidList(raw []json.RawMessage) []int64 {
	var out []int64
	for _, r := range raw {
		if v := pid(r); v > 0 {
			out = append(out, v)
		}
	}
	return out
}

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}
func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func intp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func idp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func readTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		ReadOnly:    func(json.RawMessage) bool { return true },
		Concurrent:  func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, acperm.Context) acperm.Decision { return acperm.Allowed() },
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

func writeTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		Permissions: func(context.Context, json.RawMessage, acperm.Context) acperm.Decision { return acperm.Allowed() },
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

// readExpTool / writeExpTool build a domain tool whose handler dereferences the
// task-bound ExplorationStore. Two ToolSets carry a nil store: the catalog's
// seed-only shell (never called) and the server-level one behind buildDomainReg,
// which the tools table can bind to ANY agent — including ones that never run
// inside a task (auto/pentest/reporter/自定义 agent/旁路提问). Refusing there
// keeps a mis-bound tool a bad tool call; without the guard it was a nil deref,
// and tool handlers run on the harness's own goroutine, so the panic is out of
// reach of every recover() in the server and kills the whole process.
func (t *ToolSet) readExpTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return readTool(name, desc, schema, t.needExploration(name, run))
}

func (t *ToolSet) writeExpTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return writeTool(name, desc, schema, t.needExploration(name, run))
}

// needExploration wraps a handler so it only runs with an exploration store.
// Tools that degrade more usefully than "unavailable" (report_finding points at
// add_task_hint, set_goals/set_constraints at the task itself) keep their own
// bespoke guard instead.
func (t *ToolSet) needExploration(name string, run func(context.Context, json.RawMessage) (actool.Result, error)) func(context.Context, json.RawMessage) (actool.Result, error) {
	return func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
		if t.ts == nil {
			return actool.Errorf(name + " requires task context (exploration graph): the current agent isn't running inside a task, so the task's exploration graph can't be fetched and this tool is unavailable. Use it inside a task, or switch to a cross-task read tool that takes task_id (get_task_node_detail / list_task_findings / get_task_graph and so on)."), nil
		}
		return run(ctx, in)
	}
}

func jsonResult(v any) (actool.Result, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}
	return actool.Text(string(b)), nil
}

// --- read tools (planner + worker) ---

func (t *ToolSet) graphOverview() actool.CoreTool {
	return t.readExpTool("graph_overview",
		"(Exploration chain graph) Distilled summary of the exploration situation: asset counts, sites without endpoints, frontier, findings, hints (strategic hints from humans or the main agent, which must be factored in when generating intents). Call it first when planning.",
		obj(map[string]any{}),
		func(ctx context.Context, _ json.RawMessage) (actool.Result, error) {
			return jsonResult(t.graphOverviewData(ctx))
		})
}

// graphOverviewData computes the distilled situational snapshot shared by the
// graph_overview tool and the planner's wake-up prompt (which pre-injects it so
// the model needn't spend a turn calling the tool — every plan round starts with
// an empty context and always needs this first).
func (t *ToolSet) graphOverviewData(ctx context.Context) map[string]any {
	out := map[string]any{}
	// goals summary folded in so the planner needn't call list_goals each round.
	goals, _ := t.ts.ListByKind(db.KindGoal, 100)
	gsum := make([]map[string]any, 0, len(goals))
	for _, g := range goals {
		var p map[string]any
		_ = json.Unmarshal(g.Payload, &p)
		gsum = append(gsum, map[string]any{"id": g.ID, "state": g.State, "text": p["text"]})
	}
	out["goals"] = gsum
	// hints: 人类/主 agent 通过 add_hint 挂上图的战略提示；folded in so the
	// planner reads them every round when generating intents (否则只写不读).
	hints, _ := t.ts.ListByKind(db.KindHint, 50)
	hsum := make([]map[string]any, 0, len(hints))
	for _, h := range hints {
		var p map[string]any
		_ = json.Unmarshal(h.Payload, &p)
		hint := map[string]any{"id": h.ID, "state": h.State, "text": p["text"]}
		if findingTrafficBindingEnabled(ctx) && p["traffic_refs"] != nil {
			hint["traffic_refs"] = p["traffic_refs"]
		}
		hsum = append(hsum, hint)
	}
	out["hints"] = hsum
	// lineage from the exploration edges: an intent's parents (what it
	// derived_from — possibly several facts combined) and its yields (the
	// facts/findings it produced). factFrom maps a fact → the intent that
	// produced it. This is the relationship layer the flat lists lacked.
	edges, _ := t.ts.Edges(5000)
	parentsOf := map[int64][]int64{}
	yieldsOf := map[int64][]int64{}
	factFrom := map[int64]int64{}
	for _, e := range edges {
		switch e.Rel {
		case db.RelDerivedFrom, db.RelSpawns: // upstream: derived_from (fact/finding/intent→intent) or spawns (origin fact→goal, legacy begin→intent)
			parentsOf[e.To] = append(parentsOf[e.To], e.From)
		case db.RelYields: // intent --yields--> fact/finding
			yieldsOf[e.From] = append(yieldsOf[e.From], e.To)
			factFrom[e.To] = e.From
		}
	}
	// cold-digest §6: members folded into an active digest are shown via cold_digests
	// (below), not the flat recent_* lists. `covered` maps member id → its digest id.
	// §6 render-time revival check: a covered member that has become hot again (a new
	// intent derived from it) must reappear this round — so `hidden` folds a member out
	// only when it is covered AND still cold.
	covered, _ := t.ts.CoveredMembers()
	// Render-time hot set (ancestor of a live intent / fact under a live intent).
	// §6 revival check: a covered member that revived (now hot) must NOT stay folded
	// — hidden() only folds a member out when it is covered AND still cold. Computed
	// every round (cheap for real graph sizes); nil map degrades safely.
	var hotAtRender map[int64]bool
	if cg, _, err := loadColdGraph(t.ts); err == nil {
		hotAtRender = cg.hotSet()
	}
	hidden := func(id int64) bool { _, c := covered[id]; return c && !hotAtRender[id] }
	const openIntentsCap = 30
	fr, _ := t.ts.Frontier(openIntentsCap) // priority DESC, id ASC —— 优先级最高的前 N 条；真实总数见 frontier_open
	out["open_intents"] = compactIntents(fr, parentsOf, yieldsOf)
	all, _ := t.ts.ListByKind(db.KindIntent, 300)
	var running, recentDone []*db.Node
	for _, n := range all {
		switch n.State {
		case "running":
			running = append(running, n)
		case "done", "blocked", "exhausted":
			if hidden(n.ID) {
				continue // in a cold_digest and still cold — shown via cold_digests (§6.2)
			}
			recentDone = append(recentDone, n) // 最新在前（all 按 id 降序）；折叠的已剔除，输出时截最新 N
		}
	}
	out["running_intents"] = compactIntents(running, parentsOf, yieldsOf)
	// done_intents_total：已结束意图（done/blocked/exhausted）总数，与 recent_done_intents
	// 平行命名——后者只是它的最新窗口截断视图。两键并排即自描述："看到的是 N/总数"，
	// 让 planner 去重时别把"没显示"当成"没派过"，无需在提示词里另行解释。
	if dt, err := t.ts.CountFinishedIntents(); err == nil {
		out["done_intents_total"] = dt
	}
	// frontier_open：开放意图真实总数（open_intents 只是其中优先级最高的前 N 条截断视图）。
	if fo, err := t.ts.CountOpenIntents(); err == nil {
		out["frontier_open"] = fo
	} else {
		out["frontier_open"] = len(fr)
	}
	// findings (confirmed vulns) and facts (worker exploration results) are
	// now distinct node kinds. recent_facts surfaces fact summaries (esp.
	// negative results) so the planner sees them in one call; full content
	// via node_detail(id).
	vulnNodes, _ := t.ts.ListByKind(db.KindFinding, 1000)
	factNodes, _ := t.ts.ListByKind(db.KindFact, 1000) // newest first
	out["findings_total"] = len(vulnNodes)             // 确认漏洞总数（目标判定看它）；明细见 finding_list（最新一窗）
	out["facts"] = len(factNodes)                      // 探索事实/结论数（含否定结论）
	// findings 是任务里最高价值的产物 → 概览带最新一窗（≤10 条，vulnNodes 已按 id 降序即最新在前），
	// 让 planner 每轮判目标时一眼看到最近确认的漏洞；全量/更早的用 list_findings 取。
	// 每条只留 {id, summary, from_intent?}：from_intent 是产生本漏洞的意图。
	// evidence/assets/vulnclass/severity/state 等仍可用 list_findings / node_detail(id) 取。
	const findingListCap = 10
	findingList := make([]map[string]any, 0, findingListCap)
	for _, n := range vulnNodes {
		if len(findingList) >= findingListCap {
			break
		}
		var fp map[string]any
		_ = json.Unmarshal(n.Payload, &fp)
		m := map[string]any{"id": n.ID, "summary": fp["summary"]}
		if from := factFrom[n.ID]; from > 0 {
			m["from_intent"] = from // 本漏洞由哪个意图产生
		}
		findingList = append(findingList, m)
	}
	out["finding_list"] = findingList
	// recent_facts：非折叠事实里最新的一窗（≤N，factNodes 按 id 降序即最新在前）。已折进
	// digest 且仍冷的（hidden）走 cold_digests，不在此重复。每条 {id, summary, from_intent?,
	// confidence?}；evidence 等详情用 node_detail(id)。更早的用 list_facts 翻。
	const recentFactsCap = 20
	recentFacts := make([]map[string]any, 0, recentFactsCap)
	for _, n := range factNodes {
		if len(recentFacts) >= recentFactsCap {
			break
		}
		if hidden(n.ID) {
			continue // 已折进 digest 且仍冷 —— 见 cold_digests
		}
		m := compactNode(n)
		if from := factFrom[n.ID]; from > 0 {
			m["from_intent"] = from // 本事实由哪个意图产生
		}
		// confidence 带进概览：让规划者一眼看出哪条结论只是 inferred（尤其否定结论
		// 别当铁案）；evidence 较长，留给 node_detail(id)。
		var fp map[string]any
		if json.Unmarshal(n.Payload, &fp) == nil {
			if c, ok := fp["confidence"].(string); ok && c != "" {
				m["confidence"] = c
			}
		}
		recentFacts = append(recentFacts, m)
	}
	out["recent_facts"] = recentFacts
	// recent_done_intents：非折叠的已结束意图里最新的一窗（≤N，recentDone 已按 id 降序）。
	// 更早的看 done_intents_total 计数 + node_detail(id)。
	const recentDoneCap = 12
	if len(recentDone) > recentDoneCap {
		recentDone = recentDone[:recentDoneCap]
	}
	out["recent_done_intents"] = compactIntents(recentDone, parentsOf, yieldsOf)
	// cold-digest §6.1: 折叠冷区的 digest body，按最新成员时间降序取前 N；被截的更旧 digest
	// 只给裸 id（仍可 expand_digest 展开），避免冷区唯一出口被无限拉长。
	const coldDigestsCap = 15
	if cds, more := coldDigestsRecent(t.ts, coldDigestsCap); len(cds) > 0 {
		out["cold_digests"] = cds // [{id, body, member_count}] —— 直接读 body (§6.1)
		if len(more) > 0 {
			out["cold_digests_more"] = more // 被截断的更旧 digest 的 id；用 expand_digest(id) 展开
		}
	}
	// the original task (root) so the planner always has it, not just the
	// decomposed goals.
	if description, goal, err := t.ts.Root(); err == nil {
		out["task"] = map[string]any{"description": description, "goal": goal}
	}
	// Direct source tasks are a live, read-only blackboard view. Keep their
	// summaries in a separate field so their intents never enter this task's
	// frontier or get mistaken for locally claimable work.
	out["related_tasks"] = t.relatedTaskOverviews()
	// coverage：粗略的资产测试覆盖度参考——范围(task_scope)内的资产里，被 fact 碰过的
	// 占比 + by_type(按类型的 总数/已测)。要看未测的具体资产由 agent 按需调 list_untested_assets 自行判断。仅任务上下文有。
	// 资产覆盖度功能关闭时(coverageDisabled)：只保留 host_count(目标主机数的感知信息)，
	// 丢弃 denominator/tested/pct/by_type/note 等覆盖度度量，避免污染上下文、也不诱导
	// 已隐藏的 add_task_scope/list_untested_assets。
	if t.as != nil && t.ts != nil && t.taskID > 0 {
		{
			m := map[string]any{}
			if !t.coverageDisabled {
				if cov, err := t.as.TaskCoverageWithSources(t.taskID); err == nil {
					m["denominator"] = cov.Denominator
					m["tested"] = cov.Tested
					m["by_type"] = cov.ByType
					m["note"] = "coverage is asset test coverage (including endpoints and other related assets), a rough estimate for reference only: it includes the scope and fact anchors of the current task and directly linked tasks; linked scope is read-only. Container-type assets and large enumerations push it low, so don't conclude from it that testing is finished. Use add_task_scope to extend this task's scope and list_untested_assets to see untested assets [usually don't call list_untested_assets; just advance according to the task]."
					if cov.Denominator == 0 {
						m["pct"] = nil
						m["status"] = "scope not anchored"
					} else {
						m["pct"] = cov.Pct
					}
				}
			}
			if hosts, err := t.as.HostsByTaskWithSources(t.taskID); err == nil {
				// 只给主机总数，不再把 host 列表平铺进 graph_overview（大范围任务里那是每轮
				// 都重复携带的大量字符串，对规划决策价值有限）；具体主机按需 list_assets 查。
				m["host_count"] = len(hosts)
			}
			if len(m) > 0 {
				out["coverage"] = m
			}
		}
	}
	return out
}

func inheritedMap(m map[string]any, sourceTaskID int64) map[string]any {
	m["source_task_id"] = sourceTaskID
	m["inherited"] = true
	return m
}

const (
	relatedOverviewTotalTextRunes      = 48_000
	relatedOverviewMaxTextPerSource    = 8_000
	relatedOverviewMaxGoalsPerSource   = 8
	relatedOverviewMaxHintsPerSource   = 6
	relatedOverviewMaxFactsPerSource   = 12
	relatedOverviewMaxFindingsPerTask  = 6
	relatedOverviewMaxIntentsPerTask   = 8
	relatedOverviewMaxScopePerSource   = 12
	relatedOverviewMaxDigestsPerSource = 6
)

// overviewTextBudget bounds inherited prompt text while preserving a fair slice
// for every direct source. Full evidence remains available through the on-demand
// read tools, so truncation here does not discard persisted blackboard data.
type overviewTextBudget struct {
	remaining int
	truncated bool
}

func relatedOverviewBudgetForSources(sourceCount int) int {
	if sourceCount <= 0 {
		return 0
	}
	if sourceCount > db.MaxTaskSourceCount {
		sourceCount = db.MaxTaskSourceCount
	}
	perSource := relatedOverviewTotalTextRunes / sourceCount
	if perSource > relatedOverviewMaxTextPerSource {
		perSource = relatedOverviewMaxTextPerSource
	}
	return perSource
}

func (b *overviewTextBudget) take(value any, fieldLimit int) string {
	var text string
	switch value := value.(type) {
	case string:
		text = strings.TrimSpace(value)
	case nil:
		return ""
	default:
		text = strings.TrimSpace(fmt.Sprint(value))
	}
	if text == "" {
		return ""
	}
	if b.remaining <= 0 || fieldLimit <= 0 {
		b.truncated = true
		return ""
	}
	runes := []rune(text)
	limit := fieldLimit
	if limit > b.remaining {
		limit = b.remaining
	}
	if len(runes) > limit {
		b.truncated = true
		if limit == 1 {
			text = "…"
		} else {
			text = string(runes[:limit-1]) + "…"
		}
		runes = []rune(text)
	}
	b.remaining -= len(runes)
	return text
}

func recentTerminalIntents(store *db.ExplorationStore, limit int) []*db.Node {
	if limit <= 0 {
		return []*db.Node{}
	}
	const batch = 300
	cursor := int64(0)
	out := make([]*db.Node, 0, limit)
	for len(out) < limit {
		page, more, err := store.ListByKindPage(db.KindIntent, cursor, batch)
		if err != nil || len(page) == 0 {
			break
		}
		for _, intent := range page {
			switch intent.State {
			case "done", "blocked", "exhausted", "stopped":
				out = append(out, intent)
			}
			if len(out) >= limit {
				break
			}
		}
		if !more {
			break
		}
		cursor = page[len(page)-1].ID
	}
	return out
}

// relatedTaskOverviews distills persistent blackboard state from direct source
// tasks. It intentionally reads each source's local store methods, never its own
// related sources, so inheritance is one level only.
func (t *ToolSet) relatedTaskOverviews() []map[string]any {
	sources, err := t.ts.DirectSourceStores()
	if err != nil {
		return []map[string]any{}
	}
	if len(sources) > db.MaxTaskSourceCount {
		sources = sources[:db.MaxTaskSourceCount]
	}
	perSourceTextBudget := relatedOverviewBudgetForSources(len(sources))
	out := make([]map[string]any, 0, len(sources))
	for _, source := range sources {
		ts := source.Store
		// §2 cross-task: render the source task's OWN folded view — fold out the
		// members it has already folded, and surface its cold_digests read-only.
		hidden := hiddenMembersFor(ts)
		budget := overviewTextBudget{remaining: perSourceTextBudget}
		item := map[string]any{
			"source_task_id": source.Task.TaskID,
			"inherited":      true,
			"task": map[string]any{
				"description": budget.take(source.Task.Description, 800),
				"goal":        budget.take(source.Task.Goal, 800),
				"status":      source.Task.Status,
			},
		}
		stats, statsErr := ts.Stats()

		edges, _ := ts.Edges(5000)
		parentsOf := map[int64][]int64{}
		yieldsOf := map[int64][]int64{}
		factFrom := map[int64]int64{}
		for _, edge := range edges {
			switch edge.Rel {
			case db.RelDerivedFrom, db.RelSpawns:
				parentsOf[edge.To] = append(parentsOf[edge.To], edge.From)
			case db.RelYields:
				yieldsOf[edge.From] = append(yieldsOf[edge.From], edge.To)
				factFrom[edge.To] = edge.From
			}
		}

		goals, _ := ts.ListByKind(db.KindGoal, relatedOverviewMaxGoalsPerSource)
		goalSummary := make([]map[string]any, 0, len(goals))
		for _, goal := range goals {
			var payload map[string]any
			_ = json.Unmarshal(goal.Payload, &payload)
			goalSummary = append(goalSummary, inheritedMap(map[string]any{
				"id": goal.ID, "state": goal.State, "text": budget.take(payload["text"], 400),
			}, source.Task.TaskID))
		}
		item["goals"] = goalSummary

		hints, _ := ts.ListByKind(db.KindHint, relatedOverviewMaxHintsPerSource)
		hintSummary := make([]map[string]any, 0, len(hints))
		for _, hint := range hints {
			var payload map[string]any
			_ = json.Unmarshal(hint.Payload, &payload)
			hintSummary = append(hintSummary, inheritedMap(map[string]any{
				"id": hint.ID, "state": hint.State, "text": budget.take(payload["text"], 400),
			}, source.Task.TaskID))
		}
		item["hints"] = hintSummary

		facts, _ := ts.ListByKind(db.KindFact, relatedOverviewMaxFactsPerSource)
		findings, _ := ts.ListByKind(db.KindFinding, relatedOverviewMaxFindingsPerTask)
		intentNodes, _ := ts.ListByKind(db.KindIntent, 300)
		terminalIntent := make(map[int64]bool, len(intentNodes))
		for _, intent := range intentNodes {
			terminalIntent[intent.ID] = inheritedIntentSummaryState(intent.State)
		}
		item["facts"] = len(facts)
		item["findings"] = len(findings)
		if statsErr == nil {
			item["facts"] = stats[db.KindFact]
			item["findings"] = stats[db.KindFinding]
			if stats[db.KindGoal] > len(goals) || stats[db.KindHint] > len(hints) ||
				stats[db.KindFact] > len(facts) || stats[db.KindFinding] > len(findings) {
				budget.truncated = true
			}
		}
		recentFindings := make([]map[string]any, 0, len(findings))
		for _, finding := range findings {
			entry := inheritedMap(compactFinding(finding), source.Task.TaskID)
			entry["summary"] = budget.take(entry["summary"], 400)
			recentFindings = append(recentFindings, entry)
		}
		item["recent_findings"] = recentFindings
		recentFacts := make([]map[string]any, 0, len(facts))
		for _, fact := range facts {
			if hidden(fact.ID) {
				continue // folded into this source's cold_digests — shown there (§2/§6.2)
			}
			m := inheritedMap(compactNode(fact), source.Task.TaskID)
			m["summary"] = budget.take(m["summary"], 400)
			if from := factFrom[fact.ID]; from > 0 && terminalIntent[from] {
				m["from_intent"] = from
			}
			var payload map[string]any
			if json.Unmarshal(fact.Payload, &payload) == nil {
				if confidence, ok := payload["confidence"].(string); ok && confidence != "" {
					m["confidence"] = confidence
				}
			}
			recentFacts = append(recentFacts, m)
		}
		item["recent_facts"] = recentFacts

		recentDoneRaw := recentTerminalIntents(ts, relatedOverviewMaxIntentsPerTask)
		recentDone := recentDoneRaw[:0] // in-place filter: drop this source's folded intents (§2)
		for _, intent := range recentDoneRaw {
			if hidden(intent.ID) {
				continue
			}
			recentDone = append(recentDone, intent)
		}
		for _, intent := range recentDone {
			intent.Inherited = true
			intent.SourceTaskID = source.Task.TaskID
		}
		intentResults := compactIntents(recentDone, parentsOf, yieldsOf)
		for i, intent := range recentDone {
			intentResults[i]["summary"] = budget.take(intentResults[i]["summary"], 400)
			acts, _, err := ts.ActivityPageForTerminalIntent(intent.ID, 0, 20)
			if err != nil {
				continue
			}
			var resultSummary, textFallback string
			for _, activity := range acts {
				switch activity.Kind {
				case "result":
					resultSummary = activity.Summary
				case "text":
					textFallback = activity.Summary
				}
			}
			if resultSummary == "" {
				resultSummary = textFallback
			}
			if resultSummary != "" {
				intentResults[i]["result_summary"] = budget.take(resultSummary, 800)
			}
		}
		item["recent_intent_results"] = intentResults
		// §2 cross-task: the source task's folded cold region, read-only, newest-member
		// first & capped like the current task's. Members (and overflow digests) are
		// resolvable via expand_digest(id)/node_detail(id), which search source tasks.
		if cds, more := coldDigestsRecent(ts, relatedOverviewMaxDigestsPerSource); len(cds) > 0 {
			for _, cd := range cds {
				cd["inherited"] = true
				cd["source_task_id"] = source.Task.TaskID
			}
			item["cold_digests"] = cds
			if len(more) > 0 {
				item["cold_digests_more"] = more // 被截断的更旧 digest 的 id；expand_digest(id) 展开
			}
		}
		if statsErr == nil {
			item["node_stats"] = stats
		}

		if t.as != nil {
			if scopeRows, err := t.as.ListTaskScope(source.Task.TaskID); err == nil && len(scopeRows) > 0 {
				scopeCount := len(scopeRows)
				if len(scopeRows) > relatedOverviewMaxScopePerSource {
					scopeRows = scopeRows[:relatedOverviewMaxScopePerSource]
					budget.truncated = true
				}
				scope := make([]map[string]any, 0, len(scopeRows))
				for _, row := range scopeRows {
					entry := map[string]any{"kind": row.Kind, "source": budget.take(row.Source, 300)}
					switch {
					case row.Domain != "":
						entry["value"] = budget.take(row.Domain, 400)
					case row.Net != "":
						entry["value"] = budget.take(row.Net, 400)
					case row.Value != "":
						entry["value"] = budget.take(row.Value, 400)
					case row.CompanyID != nil:
						entry["company_id"] = *row.CompanyID
					}
					scope = append(scope, entry)
				}
				item["asset_scope"] = scope
				item["asset_scope_count"] = scopeCount
			}
			if coverage, err := t.as.TaskCoverage(source.Task.TaskID, source.Task.ExplorationID); err == nil {
				item["asset_coverage"] = map[string]any{
					"denominator": coverage.Denominator,
					"tested":      coverage.Tested,
					"pct":         coverage.Pct,
					"by_type":     coverage.ByType,
				}
			}
		}
		if budget.truncated {
			item["summary_truncated"] = true
		}
		out = append(out, item)
	}
	return out
}

func inheritedIntentSummaryState(state string) bool {
	switch state {
	case "done", "blocked", "exhausted", "stopped":
		return true
	default:
		return false
	}
}

// compactNode distills any exploration node to id + summary + state, dropping the
// big detail/evidence (fetch that on demand via node_detail).
func compactNode(n *db.Node) map[string]any {
	var p map[string]any
	_ = json.Unmarshal(n.Payload, &p)
	m := map[string]any{"id": n.ID, "state": n.State, "summary": p["summary"]}
	if n.Inherited {
		inheritedMap(m, n.SourceTaskID)
	}
	return m
}

// compactFinding is compactNode plus the vuln-specific vulnclass/severity.
func compactFinding(n *db.Node) map[string]any {
	var p map[string]any
	_ = json.Unmarshal(n.Payload, &p)
	m := map[string]any{"id": n.ID, "state": n.State, "summary": p["summary"]}
	if n.Inherited {
		inheritedMap(m, n.SourceTaskID)
	}
	if vc, ok := p["vulnclass"]; ok && vc != nil && vc != "" {
		m["vulnclass"] = vc
	}
	if sv, ok := p["severity"]; ok && sv != nil && sv != "" {
		m["severity"] = sv
	}
	return m
}

func (t *ToolSet) listFindings() actool.CoreTool {
	return t.readExpTool("list_findings", "List [confirmed vulnerabilities] of this task and directly linked tasks (compact: id+task_id+intent_id+vulnclass+severity+summary+state). Linked-task entries carry source_task_id/inherited=true and are read-only. This covers vulnerabilities only; use list_facts for ordinary exploration facts and node_detail(id) for details.",
		obj(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			f, _ := t.ts.ListByKindWithSources(db.KindFinding, 500)
			if err := t.ts.PopulateFindingTrafficIDs(f); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			intentOf, _ := t.ts.FindingIntentsWithSources() // finding id -> 产生它的 intent id
			taskID := t.taskID
			if taskID <= 0 {
				taskID, _ = t.ts.TaskID()
			}
			out := make([]map[string]any, 0, len(f))
			for _, n := range f {
				m := compactFinding(n)
				if n.FindingID > 0 {
					m["finding_id"], m["finding_node_id"], m["traffic_count"] = n.FindingID, n.ID, n.TrafficCount
				}
				if n.Inherited {
					m["task_id"] = n.SourceTaskID
				} else {
					m["task_id"] = taskID
				}
				if iid, ok := intentOf[n.ID]; ok {
					m["intent_id"] = iid
				}
				out = append(out, m)
			}
			return jsonResult(out)
		})
}

// factsPageSize is the default page size for list_facts. Facts pile up on long
// tasks; returning all of them at once (the old behaviour) could blow up the
// context, so default to the newest page and let the agent page/filter for more.
const factsPageSize = 20

func (t *ToolSet) listFacts() actool.CoreTool {
	return t.readExpTool("list_facts", "List [exploration facts/conclusions] of this task and directly linked tasks page by page, newest first (compact: id+summary+state; overly long summaries are truncated, use node_detail(id) for the full text). All parameters are optional: limit (default 20, max 100), before (cursor: pass the next_before returned by the previous page to get an older page; omitted or 0 = newest page), q (filter by summary keyword). Returns {facts, total, has_more, next_before}: total is the count after filtering; when has_more=true, continue paging with next_before. Linked-task entries carry source_task_id/inherited=true and are read-only. For vulnerabilities see list_findings.",
		obj(map[string]any{
			"limit":  intp("Number of entries to return, default 20, max 100"),
			"before": intp("Paging cursor: return only older facts with id less than this value; omitted or 0 = newest page"),
			"q":      str("Filter by fact summary keyword (case-insensitive); omitted = no filter"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Limit  int    `json:"limit"`
				Before int64  `json:"before"`
				Q      string `json:"q"`
			}
			_ = json.Unmarshal(in, &a)
			limit := a.Limit
			if limit <= 0 {
				limit = factsPageSize
			}
			if limit > 100 {
				limit = 100
			}
			f, hasMore, total, err := t.ts.ListByKindPageWithSources(db.KindFact, a.Before, limit, strings.TrimSpace(a.Q))
			if err != nil {
				return actool.Result{}, err
			}
			out := make([]map[string]any, 0, len(f))
			for _, n := range f {
				out = append(out, compactFact(n))
			}
			res := map[string]any{"facts": out, "total": total, "has_more": hasMore}
			if hasMore && len(f) > 0 {
				res["next_before"] = f[len(f)-1].ID // 传回它取下一页(更旧的)
			}
			return jsonResult(res)
		})
}

// factSummaryMax caps a fact summary in list_facts output. Facts carry one-line
// conclusions, but nothing enforces brevity; a runaway summary must not bloat a
// whole page. Full text stays available via node_detail(id).
const factSummaryMax = 160

// compactFact is compactNode with the summary rune-capped for list_facts, so a
// page of facts stays bounded regardless of how long any single summary grew.
func compactFact(n *db.Node) map[string]any {
	m := compactNode(n)
	if s, ok := m["summary"].(string); ok && len([]rune(s)) > factSummaryMax {
		m["summary"] = string([]rune(s)[:factSummaryMax]) + "…"
		m["summary_truncated"] = true
	}
	return m
}

func (t *ToolSet) nodeDetail() actool.CoreTool {
	return t.readExpTool("node_detail", "Get the full content of an [exploration graph node] of this task or a directly linked task by id. Inherited nodes carry source_task_id/inherited=true and are read-only. Only exploration node ids returned by list_facts/list_findings/graph_overview are accepted; for assets use list_assets/asset_neighbors.",
		obj(map[string]any{"id": idp("Exploration graph node id (not an asset id)")}, "id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				ID json.RawMessage `json:"id"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.ID)
			if id <= 0 {
				return actool.Errorf("id is required"), nil
			}
			n, err := t.ts.GetNodeWithSources(id)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if n == nil {
				return actool.Errorf(fmt.Sprintf("Exploration node %d not found. If you meant to look up an asset, use list_assets / asset_neighbors (assets and exploration nodes are separate id spaces; an asset id can't be passed to node_detail).", id)), nil
			}
			if err := t.ts.PopulateFindingTrafficIDs([]*db.Node{n}); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(n) // full payload incl. detail / evidence, plus explicit finding IDs
		})
}

// --- planner write tools ---

// intentItem 是 add_intent 批量/单条的一条探索方向。
type intentItem struct {
	Summary   string            `json:"summary"`
	AssetIDs  []json.RawMessage `json:"asset_ids"`
	ParentIDs []json.RawMessage `json:"parent_ids"`
	Priority  int               `json:"priority"`
}

// addOneIntent 创建一条意图节点并连上游血缘，返回 id。
// 约束：意图只能锚在已确认知识上——每个 parent_id 必须是已存在的 fact/finding
// 节点（不能挂在别的意图/目标/提示上）。顶层全新方向留空 parent_ids，兜底连 origin fact。
// 这样"每个意图都连到 fact 节点、且是发现驱动而非凭空规划"从创建路径上被强制。
func (t *ToolSet) addOneIntent(it intentItem) (int64, error) {
	if strings.TrimSpace(it.Summary) == "" {
		return 0, fmt.Errorf("summary must not be empty")
	}
	// 先校验锚点（建节点前，避免坏锚点留下孤儿意图）。
	parents := pidList(it.ParentIDs)
	for _, pidv := range parents {
		n, err := t.ts.GetNodeWithSources(pidv)
		if err != nil || n == nil {
			return 0, fmt.Errorf("parent_id %d doesn't exist in this task or a directly linked task: parent_ids must be ids of existing [fact/finding] nodes; for a brand-new top-level direction leave parent_ids empty", pidv)
		}
		if n.Kind != db.KindFact && n.Kind != db.KindFinding {
			return 0, fmt.Errorf("parent_id %d is a %q node and can't be an intent anchor: an intent can only be anchored on a confirmed [fact/finding], not on an intent, goal or hint; for a brand-new top-level direction leave parent_ids empty", pidv, n.Kind)
		}
	}
	priority := it.Priority
	if priority == 0 {
		priority = 5
	}
	anchors := pidList(it.AssetIDs)
	// 资产拦截：意图绑定的资产若命中系统资产拦截规则，则禁止下发该意图。
	if t.as != nil && len(anchors) > 0 {
		hits, err := t.as.CheckAssetsIntercept(t.taskID, anchors)
		if err != nil {
			return 0, fmt.Errorf("asset intercept check failed: %w", err)
		}
		if len(hits) > 0 {
			var b strings.Builder
			fmt.Fprintf(&b, "Assets bound to intent \"%s\" failed the test scope check; stop testing the related assets:", it.Summary)
			for _, h := range hits {
				fmt.Fprintf(&b, "\n - %s", h.Describe())
			}
			return 0, fmt.Errorf("%s", b.String())
		}
	}
	payload := map[string]any{"summary": it.Summary}
	if len(anchors) > 0 {
		payload["asset_ids"] = anchors
	}
	id, err := t.ts.AddIntent(payload, priority, anchors, "planner")
	if err != nil {
		return 0, err
	}
	// upstream lineage: link each (validated) fact/finding parent → this intent, so
	// "multiple facts combine into one new intent" is expressible.
	for _, parent := range parents {
		_ = t.ts.Link(parent, db.RelDerivedFrom, id)
	}
	// a top-level intent (no explicit parent) connects to the origin fact, so every
	// intent still traces back to a fact node — at task start the only fact is the
	// origin, and the first intents derive from it.
	if len(parents) == 0 {
		if origin, _ := t.ts.OriginFactID(); origin > 0 {
			_ = t.ts.Link(origin, db.RelDerivedFrom, id)
		}
	}
	return id, nil
}

func (t *ToolSet) addIntent() actool.CoreTool {
	return t.writeExpTool("add_intent", "Create [exploration directions], write them to the frontier and connect them to the exploration chain. An intent is an open exploration direction, not a fixed type: use summary to describe freely in one sentence what to explore, verify or exploit.\n"+
		"Prefer: batch. Put the new directions selected in one round in the intents array and submit them at once (saves round trips compared with calling one by one). Returns an ids array with the same length and order as intents (failed items have id=0; see errors for details). For a single direction, omit intents and give a top-level summary directly.",
		obj(map[string]any{
			"intents":    map[string]any{"type": "array", "description": "[Prefer this] Array of exploration directions to add, processed in order. Each element has the same fields as the top-level fields below (summary/asset_ids/parent_ids/priority). The returned ids have the same length and order as this array.", "items": map[string]any{"type": "object"}},
			"summary":    str("[Single] One sentence describing this exploration direction: what to do + why. A clearly written direction is enough; it doesn't depend on asset ids."),
			"asset_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "[Target asset ids] this direction tests or attacks (**pass them whenever possible**; 0, 1 or many; these are asset ids returned by list_assets, not exploration node ids): which assets (sites, endpoints, parameters, hosts and so on) this exploration direction targets. If the direction centers on concrete assets, always pass them: this is the structured marker of \"which targets this exploration hits\", used for coverage dedup and to connect the intent to the asset chain. Leave empty only for purely global recon with genuinely no concrete target asset."},
			"parent_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Upstream anchor ids (optional; 0, 1 or many): which [confirmed facts/findings] this direction was synthesized from. **Only ids of existing fact/finding nodes are allowed, not intents, goals or hints**: an intent must be anchored on confirmed knowledge, driven by discoveries rather than planned out of thin air. Pass several when several facts together produce one new intent; leave empty for a brand-new top-level recon direction (it is attached automatically to the task's starting origin fact)."},
			"priority":   intp("Priority 0-10, default 5"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Intents    []intentItem `json:"intents"`
				intentItem              // 单条模式：顶层 summary/asset_ids/parent_ids/priority
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Intents) > 0
			items := a.Intents
			if !batch {
				items = []intentItem{a.intentItem}
			}

			ids := make([]int64, len(items))
			errs := map[string]string{}
			createdAny := false
			for i, it := range items {
				id, err := t.addOneIntent(it)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
				createdAny = true
			}

			// 人经主 agent 直投意图 → 若任务已 done（无 open 目标的 goalless 分支），把它
			// 拉回 running，worker 才能领这条意图执行。resumeTask 仅由主 agent 的 Chat 接入
			// (SetResumeTask)；planner 的 ToolSet 为 nil，故 planner 自己调 add_intent 时此段
			// no-op，不影响其正常产意图。意图节点已在上面建好(open)，复活时不会被误判抽干。
			if createdAny && t.resumeTask != nil {
				t.resumeTask()
			}

			if !batch { // 单条：保持原返回
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("intent created: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

func (t *ToolSet) listGoals() actool.CoreTool {
	return t.readExpTool("list_goals", "List this task's goal nodes and their states (open/met), used to judge whether they are met.",
		obj(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			g, _ := t.ts.ListByKind(db.KindGoal, 100)
			return jsonResult(g)
		})
}

func (t *ToolSet) proveGoal() actool.CoreTool {
	return t.writeExpTool("prove_goal", "Call this when you judge that a finding or fact proves a goal is met: links the evidence node to the goal node and marks the goal met.",
		obj(map[string]any{
			"goal_id":     idp("Goal node id"),
			"evidence_id": idp("Id of the finding or fact node that proves it"),
			"reason":      str("Why this evidence satisfies the goal"),
		}, "goal_id", "evidence_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				GoalID     json.RawMessage `json:"goal_id"`
				EvidenceID json.RawMessage `json:"evidence_id"`
				Reason     string          `json:"reason"`
			}
			_ = json.Unmarshal(in, &a)
			goal, ev := pid(a.GoalID), pid(a.EvidenceID)
			if goal == 0 || ev == 0 {
				return actool.Errorf("goal_id and evidence_id are required"), nil
			}
			goalNode, err := t.ts.GetNode(goal)
			if err != nil || goalNode == nil || goalNode.Kind != db.KindGoal {
				return actool.Errorf("goal_id must be a goal node of this task (goals of linked tasks are read-only)"), nil
			}
			evidenceNode, err := t.ts.GetNodeWithSources(ev)
			if err != nil || evidenceNode == nil || (evidenceNode.Kind != db.KindFact && evidenceNode.Kind != db.KindFinding) {
				return actool.Errorf("evidence_id must be a fact or vulnerability node of this task or a directly linked task"), nil
			}
			_ = t.ts.Link(ev, db.RelProves, goal)
			_ = t.ts.SetNodeState(goal, "met")
			// 每标记一个目标 met，就检查本任务是否【所有目标】都已 met；若是，自动判定
			// 任务完成（置 GoalMet），无需再依赖模型显式调 goal_met。
			if goals, err := t.ts.ListByKind(db.KindGoal, 1000); err == nil && len(goals) > 0 {
				allMet := true
				for _, g := range goals {
					if g.State != "met" {
						allMet = false
						break
					}
				}
				if allMet {
					t.GoalMet = true
					t.Reason = fmt.Sprintf("목표 %d개가 모두 met 상태다(마지막 트리거: goal %d)", len(goals), goal)
					return actool.Text(fmt.Sprintf("goal %d marked met; all goals of this task are met, and the task is automatically judged complete", goal)), nil
				}
			}
			return actool.Text(fmt.Sprintf("goal %d marked met", goal)), nil
		})
}

func (t *ToolSet) goalMet() actool.CoreTool {
	return writeTool("goal_met", "[End the whole task immediately]: call only when you have confirmed that [all of the task's goals are genuinely met and the whole task is wrapped up] (note this means the task is complete [as a whole]; meeting just one goal, one flag or one vulnerability [doesn't count]; in that case just mark that goal with prove_goal). Warning: this is not for \"ending this planning round\": if there is no new intent to dispatch this round or you are waiting on worker output, [just end this round; don't call this tool] (zero intents is perfectly normal). For normal judgment, prefer proving goals one by one with prove_goal; goal_met is only a means of bypassing per-goal proof and wrapping up from the global view.",
		obj(map[string]any{"reason": str("Reason the goal is met (must be evidence that the goals are genuinely met, not a reason for ending the round such as \"no new direction this round\")")}, "reason"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct{ Reason string }
			_ = json.Unmarshal(in, &a)
			t.GoalMet = true
			t.Reason = a.Reason
			return actool.Text("acknowledged: goal marked met"), nil
		})
}

// --- worker write tools ---

func (t *ToolSet) addFinding() actool.CoreTool {
	return writeTool("report_finding", "Record a confirmed vulnerability, using evidence to provide verifiable proof such as command output and logs. In task context, pass the current intent_id. The returned finding_id is the separate vulnerability record ID, and finding_node_id is the exploration node ID (the first line keeps that node number).", obj(map[string]any{
		"vulnclass": str("Vulnerability class"), "name": str("Vulnerability name"), "severity": str("critical|high|medium|low"), "summary": str("Finding summary"),
		"intent_id": idp("Intent id of the current task"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Ids of affected assets"},
		"evidence":         str("Evidence or PoC text"),
		"evidence_hint_id": idp("Optional: ID of the hint node in this task for this vulnerability; its structured traffic_refs are carried over automatically. It can't reference an inherited hint or a hint for another vulnerability"),
		"traffic_refs": map[string]any{"type": "array", "description": "Optional. For HTTP/HTTPS vulnerabilities, first search and verify one by one that each request and response really supports the vulnerability conclusion, then fill in the real IDs in reproduction order. For TCP and other non-HTTP vulnerabilities, or when traffic wasn't captured or no exact record can be found, omit it or pass []; this doesn't block reporting, and you can explain why in evidence and provide other verifiable proof. Don't guess IDs, infer association from domain or time, or re-probe just to capture packets. Roles: baseline = normal control, proof = proof of the vulnerability, verification = additional verification, supporting = supporting evidence.",
			"items": obj(map[string]any{"traffic_id": str("Real traffic ID returned by traffic_search"), "role": map[string]any{"type": "string", "enum": []string{"baseline", "proof", "verification", "supporting"}}, "note": str("How this traffic supports the vulnerability conclusion")}, "traffic_id")},
	}, "vulnclass", "severity", "summary"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
		var a struct {
			VulnClass, Name, Severity, Summary, Evidence string
			IntentID                                     json.RawMessage   `json:"intent_id"`
			AssetIDs                                     []json.RawMessage `json:"asset_ids"`
			TrafficRefs                                  []db.TrafficRef   `json:"traffic_refs"`
			EvidenceHintID                               json.RawMessage   `json:"evidence_hint_id"`
		}
		if err := json.Unmarshal(in, &a); err != nil {
			return actool.Errorf(err.Error()), nil
		}
		if t.ts == nil {
			return actool.Errorf("report_finding requires task context. In a platform chat, hand the vulnerability over to the relevant task with add_task_hint, carrying any existing traffic_refs in the hint, and the task's Agent will register it. A registered vulnerability can be bound later with bind_finding_traffic."), nil
		}
		// Auto-binding off: ignore the evidence params instead of rejecting the call.
		// stripTrafficParameters already removes them from the advertised schema, but
		// models routinely emit fields anyway — failing here would discard a confirmed
		// finding over a stray parameter.
		// 아래 성공 경로는 evidence_status "not_bound"와, 자동 바인딩이 꺼져 있으니 페이지에서
		// 수동으로 연결하라는 안내를 돌려준다. 호출자에게는 이 정보면 충분하다.
		if !findingTrafficBindingEnabled(ctx) {
			a.TrafficRefs, a.EvidenceHintID = nil, nil
		}
		if len(a.EvidenceHintID) > 0 && pid(a.EvidenceHintID) <= 0 {
			return actool.Errorf("evidence_hint_id must be a valid hint node ID; omit it when there is no handoff hint"), nil
		}
		refs, err := t.findingRefsFromHint(pid(a.EvidenceHintID), a.TrafficRefs)
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		input := db.RecordFindingInput{TaskID: t.taskID, ExplorationID: t.ts.ID(), IntentID: pid(a.IntentID), VulnClass: a.VulnClass, Name: a.Name, Severity: a.Severity, Summary: a.Summary, Evidence: a.Evidence, Worker: t.worker, AssetIDs: pidList(a.AssetIDs)}
		var recorded *db.RecordedFinding
		if t.findingRecorder != nil {
			recorded, err = t.findingRecorder.Record(ctx, input, refs)
		} else if len(refs) > 0 {
			return actool.Errorf("Traffic evidence storage is unavailable; the vulnerability was not registered"), nil
		} else {
			recorded, err = t.ts.RecordFinding(ctx, input)
		}
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		if t.notifyFinding != nil {
			iid := input.IntentID
			if iid <= 0 {
				iid = t.ownerNode
			}
			t.notifyFinding(iid, a.Summary)
		} else if t.notify != nil {
			t.notify()
		}
		t.writes.Findings++
		// Keep the first line's node-ID contract for existing reporter triggers.
		for i := range recorded.Traffic.Bindings {
			recorded.Traffic.Bindings[i].Snapshot.ReqHead = ""
			recorded.Traffic.Bindings[i].Snapshot.RespHead = ""
		}
		result := struct {
			*db.RecordedFinding
			EvidenceStatus string `json:"evidence_status"`
			EvidenceNote   string `json:"evidence_note,omitempty"`
		}{RecordedFinding: recorded, EvidenceStatus: "bound"}
		if len(recorded.Traffic.Bindings) == 0 {
			result.EvidenceStatus = "not_bound"
			result.EvidenceNote = "Vulnerability saved; no traffic bound. TCP or no-packet cases can continue normally. If you already have verified HTTP traffic, bind it later with bind_finding_traffic if available or on the vulnerability page, then complete the evidence handoff. Don't create the vulnerability again."
			if !findingTrafficBindingEnabled(ctx) {
				result.EvidenceNote = "Vulnerability saved. Agent automatic traffic binding is turned off; traffic can be linked manually on the page."
			}
		}
		raw, _ := json.Marshal(result)
		return actool.Text(fmt.Sprintf("finding recorded: %d\n%s", recorded.NodeID, raw)), nil
	})
}

// recordFact writes a general exploration RESULT/conclusion (not a vuln, not a
// new asset) into the EXPLORATION graph, chained to the intent that produced it.
// This is the home for observations and — importantly — negative results
// ("port closed", "param not injectable", "no login found"). Such conclusions
// must NOT be stuffed into the asset graph via upsert_asset.
// factItem 是 record_fact 批量/单条的一条事实。
type factItem struct {
	Summary    string            `json:"summary"`
	Detail     string            `json:"detail"`
	Evidence   string            `json:"evidence"`   // 一行关键证据（命令+关键输出行），支撑结论、便于事后核对
	Confidence string            `json:"confidence"` // observed（直接看到）| inferred（据现象推断）
	IntentID   json.RawMessage   `json:"intent_id"`
	AssetIDs   []json.RawMessage `json:"asset_ids"`
}

// recordOneFact 写一条 fact 节点并连到意图（intent→yields→fact）。defaultIntent 为
// 批量时的默认意图（本条未给 intent_id 时用）。
func (t *ToolSet) recordOneFact(it factItem, defaultIntent int64) (int64, error) {
	if strings.TrimSpace(it.Summary) == "" {
		return 0, fmt.Errorf("summary must not be empty")
	}
	payload := map[string]any{"summary": it.Summary}
	if it.Detail != "" {
		payload["detail"] = it.Detail
	}
	if e := strings.TrimSpace(it.Evidence); e != "" {
		payload["evidence"] = e
	}
	if c := strings.TrimSpace(it.Confidence); c != "" {
		payload["confidence"] = c
	}
	intent := pid(it.IntentID)
	if intent <= 0 {
		intent = defaultIntent
	}
	if intent > 0 {
		node, err := t.ts.GetNode(intent)
		if err != nil || node == nil || node.Kind != db.KindIntent {
			return 0, fmt.Errorf("intent_id must be an intent of this task (intents of linked tasks are read-only)")
		}
	}
	// a fact is its OWN node kind (distinct from a vuln finding).
	id, err := t.ts.AddNode(db.KindFact, payload, 5, "confirmed", t.worker, pidList(it.AssetIDs))
	if err != nil {
		return 0, err
	}
	if intent > 0 {
		_ = t.ts.Link(intent, db.RelYields, id) // chain: intent -> fact
	}
	t.writes.Facts++
	return id, nil
}

func (t *ToolSet) recordFact() actool.CoreTool {
	return t.writeExpTool("record_fact", "Write exploration [facts/conclusions] to the exploration graph, connected to the intent that produced them (intent_id). Use it to record exploration results, including [positive conclusions] such as fingerprints and enumeration, and [negative conclusions] such as 'port closed', 'parameter not injectable' or 'no login entry found'.\n"+
		"Warning: combine several observations from one exploration into [one fact]; don't split them. Whatever can merge into one fact should be expressed as one fact: summary = a one-sentence summary of this conclusion, detail = the related details (may include several specific items). Example: fingerprint intent -> one fact {summary:'identified the tech stack and response characteristics of site X', detail:'nginx 1.25 / Vue3 / 200 / title=.. / body_len=..'}, not one fact each for status code, fingerprint and title. One intent usually yields one fact; splitting too finely makes the graph grow without limit.\n"+
		"Prefer the facts array when writing several [mutually different] conclusions at once (each may omit intent_id, which defaults to the top-level intent_id). Returns an ids array with the same length and order as facts.\n"+
		"Warning: write only conclusions you [actually saw] in tool output; don't make things up. evidence and confidence exist to keep inaccurate conclusions from polluting the graph:\n"+
		"  - evidence = [one line] of key evidence supporting this conclusion (the command plus the one or two output lines that best prove it), **keep it short**: details already go in detail, so don't paste large chunks of output here.\n"+
		"  - confidence = observed (seen directly in output) | inferred (deduced from behavior).\n"+
		"  - **Negative conclusions** (not injectable, port closed, no entry found and so on): write only \"observation + tentative reading\": state what you actually saw, and leave whether to drop the direction to the planner, who weighs the whole picture. Always give evidence. If the means aren't exhausted or the evidence is weak (including probing only once or \"looks like\"), mark inferred; mark observed only when the means are truly exhausted and you saw it directly.",
		obj(map[string]any{
			"facts":      map[string]any{"type": "array", "description": "[Use when there are several different conclusions] Array of facts; each element has the same fields as the top-level fields below (summary/detail/evidence/confidence/intent_id/asset_ids); if intent_id is omitted, the top-level intent_id is used. The returned ids have the same length and order as this array.", "items": map[string]any{"type": "object"}},
			"summary":    str("[One-sentence summary] of this exploration's conclusion (it condenses detail)"),
			"intent_id":  idp("Id of the intent that produced this fact (the intent you received; in a batch, it is the default for each item)"),
			"detail":     str("Related details of this fact: write all the observations from this exploration here"),
			"evidence":   str("[One line] of key evidence: the command plus the one or two output lines that best prove the conclusion. Keep it short; don't paste large chunks of output (details go in detail)."),
			"confidence": str("observed (seen directly in output) | inferred (deduced from behavior). Always mark negative conclusions truthfully."),
			"asset_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Related asset ids (optional; 0, 1 or many): which assets this fact involves"},
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Facts    []factItem `json:"facts"`
				factItem            // 单条模式 + 批量默认 intent_id
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Facts) > 0
			items := a.Facts
			if !batch {
				items = []factItem{a.factItem}
			}
			defaultIntent := pid(a.factItem.IntentID) // 顶层 intent_id = 批量默认

			ids := make([]int64, len(items))
			errs := map[string]string{}
			for i, it := range items {
				id, err := t.recordOneFact(it, defaultIntent)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
			}

			if !batch { // 单条：保持原返回
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("fact recorded: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

type hintItem struct {
	Text        string            `json:"text"`
	AssetIDs    []json.RawMessage `json:"asset_ids"`
	TrafficRefs []db.TrafficRef   `json:"traffic_refs"`
}

// addOneHint 挂一条 hint 节点(active/human)到探索图,可锚定资产,返回 id。
func (t *ToolSet) addOneHint(ctx context.Context, it hintItem) (int64, error) {
	if len(it.TrafficRefs) > 0 && !findingTrafficBindingEnabled(ctx) {
		return 0, fmt.Errorf("Agent automatic traffic binding is turned off, so the hint carrying traffic_refs was not saved; turn it on in system settings, or hand over text only")
	}
	if strings.TrimSpace(it.Text) == "" {
		return 0, fmt.Errorf("text must not be empty")
	}
	var anchors []int64
	for _, raw := range it.AssetIDs {
		if tid := pid(raw); tid > 0 {
			anchors = append(anchors, tid)
		}
	}
	refs, err := db.NormalizeTrafficRefs(it.TrafficRefs)
	if err != nil {
		return 0, err
	}
	payload := map[string]any{"text": it.Text}
	if len(refs) > 0 {
		payload["traffic_refs"] = refs
	}
	// 唤醒 planner 不在此处逐条做——由 addHint 在整批写完后统一触发一次（带上提示文本），
	// 避免一次 add_hint 多条提示逐条刷屏 planner 的触发行。
	return t.ts.AddNode(db.KindHint, payload, 0, "active", "human", anchors)
}

type goalItem struct {
	Text      string `json:"text"`
	VulnClass string `json:"vulnclass"`
}

// addOneGoal 挂一条 goal 节点(open)到探索图:连到任务根(origin fact,rel spawns)。
// origin 取 t.worker(缺省 system):goals 拆解器写入的记 "goals"、主 agent 运行时记
// "human"。唤醒 planner 由 setGoals 在整批写完后统一做(见下),这里只负责落库。
func (t *ToolSet) addOneGoal(it goalItem) (int64, error) {
	text := strings.TrimSpace(it.Text)
	if text == "" {
		return 0, fmt.Errorf("text must not be empty")
	}
	payload := map[string]any{"text": text}
	if vc := strings.TrimSpace(it.VulnClass); vc != "" {
		payload["vulnclass"] = vc
	}
	origin := t.worker
	if origin == "" {
		origin = "system"
	}
	id, err := t.ts.AddNode(db.KindGoal, payload, 0, "open", origin, nil)
	if err != nil {
		return 0, err
	}
	if of, _ := t.ts.OriginFactID(); of > 0 && id > 0 {
		_ = t.ts.Link(of, db.RelSpawns, id) // goals descend from the task root (origin fact)
	}
	return id, nil
}

// setGoals 给【本任务】新增探索目标(goal 节点)。既是目标拆解器的提交工具,也是主
// agent 运行时补目标的工具——同一个受管工具,可在 web 端改描述/schema、按 agent 绑定。
func (t *ToolSet) setGoals() actool.CoreTool {
	return writeTool("set_goals",
		"Add exploration goals to [this task]. A goal = a final deliverable or verifiable result, not an attack step or recon action.\n"+
			"Prefer: batch. Put several goals in the goals array and submit them at once; returns ids with the same length and order (failed items have id=0; see errors for details). For a single goal, omit goals and give a top-level text directly.\n"+
			"vulnclass is optional: the matching vulnerability class (such as SQLi/IDOR); leave it empty for business logic goals. Whether a goal is met is judged and marked met by the system; this tool only adds goals.",
		obj(map[string]any{
			"goals":     map[string]any{"type": "array", "description": "[Prefer this] Array of goals to add, processed in order. Each element: text (required, one independently verifiable final goal) + vulnclass (optional). The returned ids have the same length and order as this array.", "items": map[string]any{"type": "object"}},
			"text":      str("[Single] One independently verifiable final goal"),
			"vulnclass": str("[Single] Matching vulnerability class (if clear), such as SQLi/IDOR; may be empty for business logic goals"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("set_goals is not enabled: ExplorationStore is not initialized"), nil
			}
			var a struct {
				Goals    []goalItem `json:"goals"`
				goalItem            // 单条模式:顶层 text/vulnclass
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Goals) > 0
			items := a.Goals
			if !batch {
				items = []goalItem{a.goalItem}
			}

			ids := make([]int64, len(items))
			errs := map[string]string{}
			var addedTexts []string
			for i, it := range items {
				id, err := t.addOneGoal(it)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
				addedTexts = append(addedTexts, strings.TrimSpace(it.Text))
			}
			if len(addedTexts) > 0 {
				// 唤醒 planner(整批一次)。优先 notifyGoal:一次 set_goals 记一条「人新增了
				// N 个目标:…」触发,不逐条刷屏;拆解器/worker 无此回调 → 退回纯 notify(拆解器
				// round-0 连 notify 也没接,即无操作,因为此时 planner 尚未启动)。
				switch {
				case t.notifyGoal != nil:
					t.notifyGoal(addedTexts)
				case t.notify != nil:
					t.notify()
				}
				// 主 agent 运行时新增目标 → 把已完成/暂停的任务拉回 running 继续跑(终态门会
				// 吞掉普通 notify,必须显式复活)。仅 mainagent 接了此回调;拆解器/worker 为 nil。
				if t.resumeTask != nil {
					t.resumeTask()
				}
			}

			if !batch { // 单条:保持原返回
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("goal added: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

type constraintItem struct {
	Text string `json:"text"`
	Type string `json:"type"` // allow | deny
}

// addOneConstraint 落一条操作约束到 task_constraints。origin 取 t.worker(缺省 system):
// 拆解器写 "goals"、主 agent 写 "human"。
func (t *ToolSet) addOneConstraint(it constraintItem) (int64, error) {
	text := strings.TrimSpace(it.Text)
	if text == "" {
		return 0, fmt.Errorf("text must not be empty")
	}
	kind := strings.TrimSpace(strings.ToLower(it.Type))
	if kind == "" {
		kind = "deny" // 默认按禁止处理:未标注类型时更保守
	}
	if kind != "allow" && kind != "deny" {
		return 0, fmt.Errorf("type must be allow or deny")
	}
	return t.ts.AddConstraint(kind, text, t.worker)
}

// setConstraints 给【本任务】新增操作约束(allow=允许做什么 / deny=禁止做什么)。既是目标
// 拆解器 round-0 抽约束的提交工具,也是主 agent 运行时补约束的工具——同一受管工具,可在 web
// 端改描述/schema、按 agent 绑定。约束会被注入 planner/worker 的系统提示以约束探索边界。
func (t *ToolSet) setConstraints() actool.CoreTool {
	return writeTool("set_constraints",
		"Add operational constraints to [this task] to frame the exploration boundary: type=allow (operations allowed) or deny (operations forbidden).\n"+
			"A constraint = a rule about [which operations may or may not be done] (such as 'test only the current port, don't scan other ports', 'no write operations on the production database', 'passive recon only'); it isn't a goal or an attack step.\n"+
			"Prefer: batch. Put several in the constraints array and submit them at once; returns ids with the same length and order (failed items have id=0; see errors for details). For a single constraint, omit constraints and give top-level text/type directly.\n"+
			"Register only constraints [explicitly written] in the task goal or description; don't invent any. When unsure of the type, use deny (more conservative).",
		obj(map[string]any{
			"constraints": map[string]any{"type": "array", "description": "[Prefer this] Array of constraints to add, processed in order. Each element: text (required, one constraint) + type (allow|deny). The returned ids have the same length and order as this array.", "items": map[string]any{"type": "object"}},
			"text":        str("[Single] Content of one operational constraint"),
			"type":        str("[Single] allow (allowed) or deny (forbidden); treated as deny when omitted"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("set_constraints is not enabled: ExplorationStore is not initialized"), nil
			}
			var a struct {
				Constraints    []constraintItem `json:"constraints"`
				constraintItem                  // 单条模式:顶层 text/type
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Constraints) > 0
			items := a.Constraints
			if !batch {
				items = []constraintItem{a.constraintItem}
			}
			ids := make([]int64, len(items))
			errs := map[string]string{}
			for i, it := range items {
				id, err := t.addOneConstraint(it)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
			}
			if !batch { // 单条:保持简单返回
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("constraint added: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

func (t *ToolSet) addHint() actool.CoreTool {
	return t.writeExpTool("add_hint", "Attach a strategic hint from a human or the main agent to the exploration graph; the planner reads it the next time it generates intents.\n"+
		"Prefer: batch. Put several hints in the hints array and submit them at once (saves round trips compared with calling one by one). Returns an ids array with the same length and order as hints (failed items have id=0; see errors for details). For a single hint, omit hints and give a top-level text directly.",
		obj(map[string]any{
			"hints":        map[string]any{"type": "array", "description": "[Prefer this] Array of hints to add, processed in order. Each element has the same fields as the top-level fields below (text/asset_ids/traffic_refs). The returned ids have the same length and order as this array.", "items": obj(map[string]any{"text": str("Hint text"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, "traffic_refs": HintTrafficSchema()})},
			"text":         str("[Single] Hint text, such as 'focus on post-auth endpoints'"),
			"traffic_refs": HintTrafficSchema(),
			"asset_ids":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Anchored asset ids (optional; 0, 1 or many)"},
		}),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Hints    []hintItem `json:"hints"`
				hintItem            // 单条模式：顶层 text/asset_ids
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Hints) > 0
			items := a.Hints
			if !batch {
				items = []hintItem{a.hintItem}
			}

			ids := make([]int64, len(items))
			errs := map[string]string{}
			var addedTexts []string
			for i, it := range items {
				id, err := t.addOneHint(ctx, it)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
				addedTexts = append(addedTexts, strings.TrimSpace(it.Text))
			}
			if len(addedTexts) > 0 {
				// 唤醒 planner（整批一次）。优先 notifyHint：一次 add_hint 记一条「人新增了
				// N 条战略提示：…」触发，让 planner 明确"本轮由新增 hint 触发"并看到提示内容；
				// 未接该回调时退回纯 notify（bare wake，hint 仍折在图里供其自行读取）。
				switch {
				case t.notifyHint != nil:
					t.notifyHint(addedTexts)
				case t.notify != nil:
					t.notify()
				}
			}

			if !batch { // 单条：保持原返回
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("hint added: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

// killWorkTool lets the planner terminate a single running work (by intent id).
func (t *ToolSet) killWorkTool() actool.CoreTool {
	return t.writeExpTool("kill_work", "Terminate a running intent (work). Use it to stop off-track or pointless exploration; a terminated intent is marked stopped and isn't claimed again automatically. First use get_worker_output to see what it is doing, then decide.",
		obj(map[string]any{"intent_id": idp("Id of the intent to terminate (= work handle)")}, "intent_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.killWork == nil {
				return actool.Errorf("kill_work is currently unavailable"), nil
			}
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id is required"), nil
			}
			node, err := t.ts.GetNode(id)
			if err != nil || node == nil || node.Kind != db.KindIntent {
				return actool.Errorf("intent_id must be an intent of this task (intents of linked tasks are read-only)"), nil
			}
			if err := t.killWork(id); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(fmt.Sprintf("Sent a termination signal to the work of intent %d", id)), nil
		})
}

// steerWorkTool lets the planner inject a mid-run course-correction into a running
// work WITHOUT killing it: the message reaches the worker before its next tool call,
// which re-plans its next step (already-gathered context is kept). For in-intent
// nudges ("停做 X、聚焦 Y"); if the whole direction is wrong use kill_work + a new intent.
func (t *ToolSet) steerWorkTool() actool.CoreTool {
	return t.writeExpTool("steer_work", "Inject a course-correction instruction into a running intent (work) in real time, without interrupting it or losing progress: the worker receives your instruction before its next action and adjusts accordingly. Use it for [within-intent] corrections such as 'stop going down X, focus on Y'; if the whole direction is wrong, use kill_work instead and issue a new intent. It is recommended to first use get_worker_output to see what it is doing.",
		obj(map[string]any{
			"intent_id": idp("Id of the intent to correct (= work handle)"),
			"message":   str("Course-correction instruction for the worker, stating clearly what to stop and what to turn to"),
		}, "intent_id", "message"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.steerWork == nil {
				return actool.Errorf("steer_work is currently unavailable"), nil
			}
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
				Message  string          `json:"message"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id is required"), nil
			}
			node, err := t.ts.GetNode(id)
			if err != nil || node == nil || node.Kind != db.KindIntent {
				return actool.Errorf("intent_id must be an intent of this task (intents of linked tasks are read-only)"), nil
			}
			if strings.TrimSpace(a.Message) == "" {
				return actool.Errorf("message is required"), nil
			}
			if err := t.steerWork(id, a.Message); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(fmt.Sprintf("Injected a course-correction instruction into the work of intent %d (takes effect at the next step)", id)), nil
		})
}

// getWorkerOutput returns a work's final (or截至中止时的) conclusion text by intent id.
func (t *ToolSet) getWorkerOutput() actool.CoreTool {
	return t.readExpTool("get_worker_output", "Get the final output conclusion of an intent (work) of this task or a directly linked task. Linked-task results carry source_task_id/inherited=true and are read-only. A normal finish returns its summary; a terminated (stopped) or abnormal work returns its last output up to the point it stopped.",
		obj(map[string]any{"intent_id": idp("Intent id (= work handle)")}, "intent_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id is required"), nil
			}
			intentNode, err := t.ts.GetNodeWithSources(id)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if intentNode == nil || intentNode.Kind != db.KindIntent {
				return actool.Errorf("intent_id doesn't belong to this task or its directly linked tasks"), nil
			}
			acts, _, err := t.ts.ActivityListWithSources(id, 0, 1000)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			var chosen, fallback *db.Activity
			for i := range acts {
				switch acts[i].Kind {
				case "result":
					chosen = &acts[i]
					fallback = &acts[i]
				case "text":
					fallback = &acts[i]
				}
			}
			pick := chosen
			if pick == nil {
				pick = fallback
			}
			if pick == nil {
				if intentNode.Inherited {
					return jsonResult(inheritedMap(map[string]any{
						"intent_id": id, "final_text": "(this work has no output yet)",
					}, intentNode.SourceTaskID))
				}
				return actool.Text("(this work has no output yet)"), nil
			}
			detail, _ := t.ts.ActivityDetailWithSources(pick.ID)
			if detail == "" {
				detail = pick.Summary
			}
			result := map[string]any{
				"intent_id": id, "final_text": detail,
				"summary": pick.Summary, "is_error": pick.IsError,
			}
			if intentNode.Inherited {
				inheritedMap(result, intentNode.SourceTaskID)
			}
			return jsonResult(result)
		})
}

// traceSteps renders summary-only trace rows, re-truncating each summary to 100
// chars — the stored summary is capped at 200 for the UI transcript; the trace
// tools want it tighter since a whole work's step list is many rows.
func traceSteps(acts []db.Activity) []map[string]any {
	steps := make([]map[string]any, 0, len(acts))
	for i := range acts {
		step := map[string]any{
			"step_id": acts[i].ID, "kind": acts[i].Kind, "tool": acts[i].Tool,
			"is_error": acts[i].IsError, "summary": firstLine(acts[i].Summary, 100),
		}
		if acts[i].Inherited {
			inheritedMap(step, acts[i].SourceTaskID)
		}
		steps = append(steps, step)
	}
	return steps
}

// getWorkerTrace exposes a work's execution PROCESS (not just its final output):
// list step summaries, keyword-search within one work, or pull full detail of a
// few specific steps. Thinking steps are excluded everywhere.
func (t *ToolSet) getWorkerTrace() actool.CoreTool {
	return t.readExpTool("get_worker_trace",
		"View the [execution process] of an intent (work) (unlike get_worker_output, which gives only the final conclusion). Three usages:\n"+
			"(1) intent_id only -> returns a summary stream of each step of that work (summary <= 100 chars, with step_id; only an outline of actions, without full output);\n"+
			"(2) intent_id + q -> returns only summaries of steps matching the keyword (searches both summaries and full output; still gives only summary, use (3) to see the content);\n"+
			"(3) intent_id + step_ids -> returns the full content (detail) of those steps; at most 5 per call, and beyond that only the first 5 are returned, with the rest reported in notice/omitted_step_ids.\n"+
			"Typical flow: first use (1) or (2) to locate the step_id of a suspicious step, then use (3) to get its full output. Thinking steps are excluded. Historical traces of directly linked tasks are supported; their results carry source_task_id/inherited=true and are read-only.",
		obj(map[string]any{
			"intent_id": idp("Intent id (= work handle)"),
			"q":         str("Keyword: return only steps whose summary or full output matches it (optional; mutually exclusive with step_ids)"),
			"step_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "step_id values to get full content for (from (1) or (2); at most 5 per call; if more are passed, only the first 5 are returned and the rest are listed in omitted_step_ids)"},
			"limit":     intp("Return limit for the summary stream or search (optional)"),
		}, "intent_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				IntentID json.RawMessage   `json:"intent_id"`
				Q        string            `json:"q"`
				StepIDs  []json.RawMessage `json:"step_ids"`
				Limit    int               `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id is required"), nil
			}
			intentNode, nodeErr := t.ts.GetNodeWithSources(id)
			if nodeErr != nil {
				return actool.Errorf(nodeErr.Error()), nil
			}
			if intentNode == nil || intentNode.Kind != db.KindIntent {
				return actool.Errorf("intent_id doesn't belong to this task or its directly linked tasks"), nil
			}
			// ③ detail drill-down by step ids, thinking excluded by the store.
			if len(a.StepIDs) > 0 {
				// Dedup + drop invalid ids first so garbage/duplicates don't eat into
				// the per-call cap. detail is returned in full (untruncated), so the
				// cap bounds one tool result; over the cap we serve the first N and
				// tell the model exactly which ids were deferred, instead of erroring
				// and forcing it to re-plan the call.
				const maxStepIDs = 5
				var ids []int64
				seen := make(map[int64]bool)
				for _, raw := range a.StepIDs {
					if v := pid(raw); v > 0 && !seen[v] {
						seen[v] = true
						ids = append(ids, v)
					}
				}
				var omitted []int64
				if len(ids) > maxStepIDs {
					omitted = append(omitted, ids[maxStepIDs:]...)
					ids = ids[:maxStepIDs]
				}
				acts, err := t.ts.ActivityByIDsWithSources(ids)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				steps := make([]map[string]any, 0, len(acts))
				for i := range acts {
					if acts[i].NodeID == nil || *acts[i].NodeID != id || acts[i].Inherited != intentNode.Inherited ||
						(acts[i].Inherited && acts[i].SourceTaskID != intentNode.SourceTaskID) {
						continue
					}
					step := map[string]any{
						"step_id": acts[i].ID, "kind": acts[i].Kind, "tool": acts[i].Tool,
						"is_error": acts[i].IsError, "detail": acts[i].Detail,
					}
					if acts[i].Inherited {
						inheritedMap(step, acts[i].SourceTaskID)
					}
					steps = append(steps, step)
				}
				result := map[string]any{"intent_id": id, "steps": steps, "returned_step_ids": ids}
				if len(omitted) > 0 {
					// returned_step_ids/omitted_step_ids let the model decide programmatically
					// whether another call is worth it; the notice states the same in prose.
					result["omitted_step_ids"] = omitted
					result["notice"] = fmt.Sprintf(
						"Full content is limited to %d steps per call. This call returned the first %d (%v); the %d not fetched are %v. "+
							"If this content is enough to locate the issue, there's no need to fetch the remaining steps; if you really need to continue, call again with these step_id values.",
						maxStepIDs, len(ids), ids, len(omitted), omitted)
				}
				if intentNode.Inherited {
					inheritedMap(result, intentNode.SourceTaskID)
				}
				return jsonResult(result)
			}
			// ①/② summary stream, optionally keyword-filtered; 100-char summaries.
			var acts []db.Activity
			var err error
			if strings.TrimSpace(a.Q) != "" {
				acts, err = t.ts.ActivityTraceSearchWithSources(id, a.Q, a.Limit)
			} else {
				acts, err = t.ts.ActivityTraceWithSources(id, a.Limit)
			}
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			result := map[string]any{"intent_id": id, "steps": traceSteps(acts)}
			if intentNode.Inherited {
				inheritedMap(result, intentNode.SourceTaskID)
			}
			return jsonResult(result)
		})
}

// searchAllWorkerTraces keyword-searches EVERY work's process in this task — for
// finding what a worker saw but never wrote back as a fact. Returns only matching
// summaries (≤100 chars), each tagged with its intent_id for follow-up drill-down.
func (t *ToolSet) searchAllWorkerTraces() actool.CoreTool {
	return t.readExpTool("search_all_worker_traces",
		"[Usually not recommended, because the system already provides most of the information] Search by keyword (q) in [the execution process of other works in this task], to recover something a worker saw but didn't write into a fact (a path, token, error and so on). "+
			"Steps of your own intent are excluded automatically (they are already in your context). "+
			"Returns only summaries of matching steps (summary <= 100 chars), each with intent_id; then use get_worker_trace(intent_id, step_ids=[...]) to get the full content.",
		obj(map[string]any{
			"q":     str("Keyword (searched in summaries and full output of all work steps)"),
			"limit": intp("Return limit, default 100 (optional)"),
		}, "q"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Q     string `json:"q"`
				Limit int    `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			if strings.TrimSpace(a.Q) == "" {
				return actool.Errorf("q is required"), nil
			}
			// 排除调用者自身这条意图的步骤（worker 的自有 trace 已在其上下文里）。
			acts, err := t.ts.ActivityTraceSearchAllWithSources(t.ownerNode, a.Q, a.Limit)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			hits := make([]map[string]any, 0, len(acts))
			for i := range acts {
				var intent int64
				if acts[i].NodeID != nil {
					intent = *acts[i].NodeID
				}
				hit := map[string]any{
					"intent_id": intent, "step_id": acts[i].ID, "worker": acts[i].Worker,
					"kind": acts[i].Kind, "tool": acts[i].Tool, "is_error": acts[i].IsError,
					"summary": firstLine(acts[i].Summary, 100),
				}
				if acts[i].Inherited {
					inheritedMap(hit, acts[i].SourceTaskID)
				}
				hits = append(hits, hit)
			}
			return jsonResult(map[string]any{"query": a.Q, "hits": hits})
		})
}

// listWorkerTraces gives a worker (which has no graph_overview and can't see the
// intent graph) a lightweight index of the works in this task — intent_id +
// one-line summary + state — so it can DISCOVER which works to inspect via
// get_worker_trace. Without this a worker only knows intent_ids that come back
// from search_all_worker_traces hits. Excludes still-open intents (not yet run →
// no process to inspect).
func (t *ToolSet) listWorkerTraces() actool.CoreTool {
	return t.readExpTool("list_worker_traces",
		"[Usually not recommended, because the system already provides most of the information] List an index of [works (intents) that have already run] in this task: intent_id + one-sentence direction (summary) + state. "+
			"You (the worker) can't see the exploration graph; use this to discover which works are worth reading, then use get_worker_trace(intent_id) to see their steps and get_worker_trace(intent_id, step_ids=[...]) to get details. "+
			"Lists only executed ones (running/done/exhausted/blocked/stopped), not open ones that haven't run yet. Note: your task boundary is still the one intent you received; read other works only to reuse observations and avoid duplicate effort.",
		obj(map[string]any{
			"q":     str("Filter by summary keyword (optional)"),
			"limit": intp("Return limit, default 50 (optional)"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Q     string `json:"q"`
				Limit int    `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			limit := a.Limit
			if limit <= 0 {
				limit = 50
			}
			all, err := t.ts.ListByKindWithSources(db.KindIntent, 500)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			q := strings.ToLower(strings.TrimSpace(a.Q))
			out := make([]map[string]any, 0, limit)
			for _, n := range all {
				if n.Inherited && n.State == "running" {
					continue
				}
				switch n.State {
				case "running", "done", "exhausted", "blocked", "stopped": // has run → has a process
				default:
					continue
				}
				var p map[string]any
				_ = json.Unmarshal(n.Payload, &p)
				summary, _ := p["summary"].(string)
				if q != "" && !strings.Contains(strings.ToLower(summary), q) {
					continue
				}
				item := map[string]any{"intent_id": n.ID, "summary": summary, "state": n.State}
				if n.Inherited {
					inheritedMap(item, n.SourceTaskID)
				}
				out = append(out, item)
				if len(out) >= limit {
					break
				}
			}
			return jsonResult(map[string]any{"works": out})
		})
}

// PlannerTools is the read + intent-generation + goal-judgement tool set.
func (t *ToolSet) PlannerTools() []actool.CoreTool {
	return []actool.CoreTool{
		t.graphOverview(), t.listFindings(), t.listFacts(), t.nodeDetail(),
		// cold-digest §6.1: restore folded cold nodes (digest body → members → detail).
		t.expandDigest(),
		t.getWorkerOutput(), t.getWorkerTrace(), t.searchAllWorkerTraces(), t.listGoals(), t.addIntent(), t.proveGoal(), t.goalMet(),
		t.killWorkTool(), t.steerWorkTool(),
		// report_finding：规划态势研判时若自身已确证漏洞，可直接登记（与 worker 同工具）。
		t.addFinding(),
		// list_companies：查看企业列表 + scope + 资产数（拿 company_id / 理解归属范围）。
		t.listCompanies(),
		// list_assets：规划时按 DSL 检索全资产库（配合 list_untested_assets 的"范围内未测"视角，
		// 补上"按域名/指纹/端口/状态码等条件在整库里查"的能力）。
		t.listAssets(),
		// add_company_scope：规划时可把域名/IP/CIDR/ICP/关键词纳入某公司的资产范围（自动认领命中资产）。
		t.addCompanyScope(),
		// add_task_scope：主动把整根域/整公司/某子域/IP 纳入本任务测试范围(覆盖度分母)。
		t.addTaskScope(),
		// list_untested_assets：按需查本任务范围内未测资产(类型+分页)，自行决定补测。
		t.listUntestedAssets(),
	}
}
