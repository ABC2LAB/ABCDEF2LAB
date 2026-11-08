package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// Planner is the event-driven LLM planner (docs §4.3): each time the asset or
// exploration graph changes (debounced), it reads the exploration route, queries
// assets, judges whether the task goal is met, and emits 0..N exploration intents
// into the frontier. It is the sole intent generator.
type Planner struct {
	findingRecorder   FindingRecorder
	prov              llm.Provider
	model             string
	tx                *transcript.Store                      // raw LLM conversation persistence (nil = off)
	window            int                                    // context window in tokens (for compaction)
	windowFn          func() int                             // optional dynamic task-chain minimum
	maxTurns          int                                    // max agent turns per run (0 = unlimited)
	killWork          func(intentID int64) error             // engine callback to terminate a running work (nil = off)
	steerWork         func(intentID int64, msg string) error // engine callback to steer a running work mid-run (nil = off)
	proxyAddr         string                                 // recording proxy for WebFetch (empty = direct)
	proxyCACert       string                                 // recording proxy's CA cert path (HTTPS verify)
	webSearch         WebSearchOpts                          // web_search tool backend selection (off by default)
	workDir           string                                 // shared work dir (surfaced in prompt as artifact-output target)
	injectConstraints func() bool                            // resolver: inject task operation constraints into system prompt? (nil = yes)
	nonStreamingFn    func() bool                            // resolver: use non-streaming (Complete) path? (nil = streaming)
	noaEnabledFn      func() bool                            // resolver: use experimental noa compaction? (nil = off)
	maxTokensFn       func() int                             // resolver: per-reply output cap (nil/0 = send no cap)
	compactor         *Compactor                             // cold-node compaction (§7); nil = disabled

	// todos keeps ONE plan-scratchpad per task (keyed by exploration id) so the
	// planner's multi-step plan survives across wake-ups — each Plan() is a fresh
	// session, but the shared store lets it record a serial exploit chain once and
	// dispatch it step-by-step over rounds instead of front-loading it in parallel.
	todoMu sync.Mutex
	todos  map[int64]*actool.TodoStore
}

func NewPlanner(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *Planner {
	return &Planner{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns, todos: map[int64]*actool.TodoStore{}}
}

func (p *Planner) SetCompactionWindowResolver(fn func() int) { p.windowFn = fn }

// SetCompactor wires the cold-node compactor (cold-digest §7). Called each
// planner wake-up to advance the round counter, maintain cold stamps, and
// (off the hot path) fold cold nodes into digests. nil = feature disabled.
func (p *Planner) SetCompactor(c *Compactor) { p.compactor = c }

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (p *Planner) SetNonStreaming(fn func() bool) { p.nonStreamingFn = fn }

func (p *Planner) nonStreaming() bool { return p.nonStreamingFn != nil && p.nonStreamingFn() }

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (p *Planner) SetNoaEnabled(fn func() bool) { p.noaEnabledFn = fn }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (p *Planner) SetMaxTokens(fn func() int) { p.maxTokensFn = fn }

func (p *Planner) maxTokens() int {
	if p.maxTokensFn == nil {
		return 0
	}
	return p.maxTokensFn()
}

func (p *Planner) compactionWindow() int {
	if p.windowFn != nil {
		return p.windowFn()
	}
	return p.window
}

// SetProxy points the planner's WebFetch at the recording proxy plus the CA cert
// it trusts to verify HTTPS through it (empty addr = direct).
func (p *Planner) SetProxy(addr, caCert string) { p.proxyAddr, p.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for the planner (off by default).
func (p *Planner) SetWebSearch(o WebSearchOpts) { p.webSearch = o }

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the planner system prompt. Read per round so the
// settings toggle takes effect without rebuilding the agent. nil = inject (default).
func (p *Planner) SetConstraintInject(fn func() bool) { p.injectConstraints = fn }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (p *Planner) wantConstraints() bool { return p.injectConstraints == nil || p.injectConstraints() }

// todoFor returns the task's persistent planning todo store, creating it on first
// use. Shared across all of this task's planner wake-ups.
func (p *Planner) todoFor(expID int64) *actool.TodoStore {
	p.todoMu.Lock()
	defer p.todoMu.Unlock()
	s := p.todos[expID]
	if s == nil {
		s = actool.NewTodoStore()
		p.todos[expID] = s
	}
	return s
}

// SetKillWork wires the engine's per-work terminate callback so the planner's
// kill_work tool can stop a single running worker.
func (p *Planner) SetKillWork(fn func(intentID int64) error) { p.killWork = fn }

// SetSteerWork wires the engine's per-work steering callback so the planner's
// steer_work tool can inject a mid-run course-correction into a running worker.
func (p *Planner) SetSteerWork(fn func(intentID int64, msg string) error) { p.steerWork = fn }

// renderPlannerTodos formats the persistent planning todo for injection into the
// wake-up prompt (empty when there are no todos yet — first wake-up).
func renderPlannerTodos(items []actool.Todo) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[Your planning todos (kept across wake-ups, written by you last round)]:\n")
	for _, it := range items {
		mark := map[actool.TodoStatus]string{actool.TodoPending: "☐", actool.TodoInProgress: "▶", actool.TodoCompleted: "✔"}[it.Status]
		if mark == "" {
			mark = "☐"
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", mark, it.Content))
	}
	b.WriteString("Proceed from these: dispatch intents only for the next step whose [prerequisite steps are complete or whose required fact exists]; update the list with TodoWrite (mark steps satisfied by facts as completed). Don't re-dispatch steps already pending or in_progress on the list.")
	return b.String()
}

// TriggerEvent describes what concretely caused this planning round to fire, so
// the planner looks first at the actual change instead of re-scanning the whole
// overview. Kind:
//
//	"done"    — a worker finished intent IntentID (its output conclusion is fetched).
//	"finding" — a worker reported a finding on intent IntentID (Detail = 摘要).
//	"goal"    — the human (via 主 agent 的 set_goals) added one OR MORE goals in a
//	            single call (Goals = 本次新增的目标文本，1+ 条；set_goals 支持批量).
//	"goal_deleted" — the human deleted a goal from 总览的目标管理 (Detail = 被删目标文本).
//	"goal_edited"  — the human edited a goal from 总览的目标管理 (OldGoal→NewGoal 文本).
//	"cancelled" — the human deleted intent IntentID (Detail = 删除原因). The intent is
//	            stopped (not deleted) and the reason is attached to it as a fact.
type TriggerEvent struct {
	Kind     string
	IntentID int64
	Detail   string
	Summary  string   // Kind=="cancelled" 专用：删除前捕获的意图摘要（真删除后节点已不存在，无法再查）
	Goals    []string // Kind=="goal" 专用：本次 set_goals 新增的目标文本（1 条或多条）
	OldGoal  string   // Kind=="goal_edited" 专用：修改前的目标文本
	NewGoal  string   // Kind=="goal_edited" 专用：修改后的目标文本
	Hints    []string // Kind=="hint" 专用：本次 add_hint 新增的提示文本（1 条或多条）
}

// renderTriggers spells out the change(s) that fired this round: for a finished
// worker — which intent + its output conclusion; for a finding — which intent +
// what was found. Empty for time/heartbeat wakes. Reads the store (best-effort;
// a blank field never blocks the round).
func renderTriggers(ts *db.ExplorationStore, evs []TriggerEvent) string {
	if len(evs) == 0 || ts == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[Actual changes that triggered this round (look here first, then decide whether to add directions)]:")
	for _, ev := range evs {
		switch ev.Kind {
		case "goal":
			if len(ev.Goals) == 1 {
				b.WriteString(fmt.Sprintf("\n- The human (main agent) added a goal: %s. It is a new goal to reach; add exploration directions for it (if no matching intent exists yet).", ev.Goals[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- The human (main agent) added %d goals: %s. All are new goals to reach; add exploration directions for each goal without a matching intent.", len(ev.Goals), strings.Join(ev.Goals, "; ")))
			}
		case "hint":
			if len(ev.Hints) == 1 {
				b.WriteString(fmt.Sprintf("\n- The human (main agent) added a strategic hint: %s. It is attached to the exploration graph; adjust or add exploration directions accordingly (if no matching intent exists yet).", ev.Hints[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- The human (main agent) added %d strategic hints: %s. All are attached to the exploration graph; adjust or add exploration directions for each.", len(ev.Hints), strings.Join(ev.Hints, "; ")))
			}
		case "goal_deleted":
			b.WriteString(fmt.Sprintf("\n- The human deleted this goal: %s. It has been removed; rejudge the remaining goals and directions (no more intents for it).", ev.Detail))
		case "goal_edited":
			b.WriteString(fmt.Sprintf("\n- The human changed a goal from \"%s\" to \"%s\". Adjust exploration directions to the new goal (stop dispatching old directions that no longer apply).", ev.OldGoal, ev.NewGoal))
		case "finding":
			b.WriteString(fmt.Sprintf("\n- The worker of intent #%d (%s) reported a finding: %s", ev.IntentID, intentSummary(ts, ev.IntentID), ev.Detail))
		case "cancelled":
			// 意图内容优先用删除时捕获的 Summary（真删除后节点已不存在，intentSummary 查不到）。
			sm := ev.Summary
			if sm == "" {
				sm = intentSummary(ts, ev.IntentID)
			}
			b.WriteString(fmt.Sprintf("\n- The user deleted intent #%d. Intent content: %s. Reason for deletion: %s. The intent is deleted (it won't run); replan accordingly.", ev.IntentID, sm, ev.Detail))
		default: // "done"
			b.WriteString(fmt.Sprintf("\n- The worker of intent #%d (%s) finished. Output conclusion: %s", ev.IntentID, intentSummary(ts, ev.IntentID), workerOutput(ts, ev.IntentID)))
			if fids := factIDsYielded(ts, ev.IntentID); fids != "" {
				b.WriteString(fmt.Sprintf("; fact ids newly produced by this intent: %s ", fids))
			}
		}
	}
	b.WriteString("\n(For full details, use node_detail / get_worker_output / list_findings.)")
	return b.String()
}

// factIDsYielded lists the fact ids an intent produced this run as "#12、#15", so the
// planner can jump straight to the round's incremental facts. Empty (best-effort) when
// the intent yielded no facts or the lookup fails.
func factIDsYielded(ts *db.ExplorationStore, id int64) string {
	ids, err := ts.FactsYielded(id)
	if err != nil || len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, fid := range ids {
		parts[i] = fmt.Sprintf("#%d", fid)
	}
	return strings.Join(parts, ", ")
}

// intentSummary reads an intent node's one-line summary (best-effort, "?" on miss).
func intentSummary(ts *db.ExplorationStore, id int64) string {
	n, err := ts.GetNode(id)
	if err != nil || n == nil {
		return "?"
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if s, ok := p["summary"].(string); ok && s != "" {
			return s
		}
	}
	return "?"
}

// workerOutput returns the finished worker's conclusion for an intent — the last
// 'result' (else 'text') activity's full detail, truncated. Same source get_worker_output uses.
func workerOutput(ts *db.ExplorationStore, id int64) string {
	acts, _, err := ts.ActivityList(&id, 0, 1000)
	if err != nil {
		return "(failed to fetch output)"
	}
	var pick *db.Activity
	for i := range acts {
		if acts[i].Kind == "result" {
			pick = &acts[i]
		} else if acts[i].Kind == "text" && pick == nil {
			pick = &acts[i]
		}
	}
	if pick == nil {
		return "(this work has no output record yet)"
	}
	out, _ := ts.ActivityDetail(pick.ID)
	if out == "" {
		out = pick.Summary
	}
	return truncOutput(out, 800)
}

// truncOutput caps a worker-output blob so the trigger context doesn't bloat the
// system prompt every round; full text is one get_worker_output call away.
func truncOutput(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " ... (truncated; see get_worker_output for the full text)"
}

// renderGraphOverview folds the pre-computed graph_overview snapshot into the
// wake-up prompt so the planner starts each round with the full situation in
// hand — saving the round-trip it would otherwise spend calling the tool. It is
// the exact same JSON graph_overview would return; deeper detail is still one
// tool call away (node_detail / list_facts / …).
func renderGraphOverview(data map[string]any) string {
	b, err := json.Marshal(data)
	if err != nil {
		return "" // fall back to the model calling graph_overview itself
	}
	return "\n\n[Situation this round (graph_overview prefetched, same as the result of calling that tool; call node_detail, list_facts and so on when you need detail)]:\n" + string(b)
}

// plannerDefaultTmpl is the built-in EDITABLE body (段 [A]) of the planner prompt,
// seeded into agent_prompts. Goal is a {{.Goal}} template var; the 中间产物输出规约
// tail is code-owned (artifactSpec) and appended by plannerSystem after rendering.
const plannerDefaultTmpl = `You are the "planner" of an authorized penetration testing system on a cybersecurity platform. You are woken often (every time the graph changes). Your job: read the situation → judge the goals → add exploration intents **only when there is a genuinely uncovered new direction**. You are a planner, not an executor: everything you produce this round is either [creating or clarifying an intent] or [judging a goal]. Never do the work inside plan.

Task goal: {{.Goal}}

**How many intents this round (think about this first)**:
- **Hard floor (highest priority)**: if [the goal isn't met] and [there is no open or running intent] (frontier_open=0 and running_intents empty), you [must] produce at least one intent that moves toward the goal this round. With no running work to wait for and no queued direction, zero intents means the task stalls. Even if all known directions appear only in recent_done, open a new one or continue one based on the done/exhausted/blocked rules below.
- Outside the hard floor, **zero intents is a normal result but needs a valid reason** (not a "fewer is safer" default): (1) **Already covered**: every direction you can think of is handled by an intent still open or running (regenerating an existing intent in other words is a serious error). (2) **Waiting on a dependency**: the next step depends on output of running work that hasn't appeared yet (dispatching now would leave the downstream spinning without its prerequisite; wait for the next wake-up after the graph updates).
- Conversely: if there is a new direction that is [uncovered and doesn't depend on running work], or the goal isn't met and untested surfaces remain in scope, dispatch it. Don't treat zero intents as a lazy default.

**Decision flow for each wake-up**:

1. **The full situation is attached below this prompt** (it is the graph_overview result; no need to call it): task (original title + goal/root node), asset counts, goals + status, open/running/recent_done intents, sites_without_endpoints (sites without endpoints, hinting at directions to explore), facts (count of exploration facts, separate from vulnerabilities), recent_facts ({id,summary,confidence?}).
   - **Scope**: exploration nodes (goals/intents/facts/findings) are for this task only; **the asset graph is shared globally** (one copy across tasks; asset counts are global within scope, not specific to this task). Ignore assets unrelated to this task.
   - **Lineage**: each intent has parents (upstream: which facts or intents it derives from) and yields (downstream: which facts or findings it produced); each recent_facts item has from_intent. Use these to understand which facts came from which direction and whether they can combine into a new direction.
   - **Negative or doubtful observations** (such as "port closed" or "not injectable" in recent_facts) are worker observations, not verdicts. Before accepting one, check the evidence with node_detail(id). Treat the direction as closed for now only if the evidence is solid, confidence=observed and the means are exhausted. If evidence is missing, it is only "looks like" or "probed once", or confidence=inferred, treat it as [not yet established]; if it is in scope and no other intent covers it, by default dispatch one recheck intent to confirm or refute it (**recheck the same negative direction at most once**; if it is still negative with reasonable evidence, accept the conclusion and don't dispatch again).
   - **Call for deeper detail only when needed**: list_facts (paged, newest first, default 20, q filters, before pages, returns total/has_more), list_findings (all vulnerabilities), node_detail(id) (full evidence and detail; lists and recent_facts only give summaries), list_assets (pull: q search, type/company_id/task_id filters, paging, or id/ids directly), asset_neighbors. Assets are shared globally; don't pull everything by default.

2. **Judge goals (core duty)**: the goals field has goals and status. For an unmet goal already proven by a finding or fact, call prove_goal(goal_id, evidence_id, reason) to mark it met. **When the one you mark is the last unmet goal, the system automatically marks the whole task complete**: completion is driven only by individual prove_goal calls; there is no other "complete all" action.
   - ⚠️ **Check quantified acceptance (never mark early)**: when a goal has a quantified condition (coverage X%, N flags, obtaining some privilege), you [must] check the measured values in the graph_overview above (coverage.pct, findings_total and so on) before prove_goal. If the condition is unmet, [do not] call prove_goal; dispatch intents to close the gap instead. Don't mark it met early because it is "mostly done" or "the core is achieved". Example: the goal requires 100% coverage and coverage.pct=40% → not met, keep dispatching coverage intents.

3. **(Optional, only at the start, very light) Probe to understand**: only when the graph has almost no facts (recent_facts basically empty, the task just started) and the situation alone can't make the initial intent concrete, use Bash or similar for a very small read-only probe of the target (such as 1-2 curl calls for the home page or fingerprint). **The only legitimate output is a more precise intent description**: never a finding, verification or exploitation of a vulnerability, and never enumerated endpoints, directories or parameters (those are worker jobs; write them as intents). Three hard limits:
   - The graph already has worker-produced facts (facts>0 or recent_facts not empty) → [do not] probe yourself. Base all judgment on existing facts; your output this round is either "dispatch new intents" or "finish". To dig into a lead, dispatch an intent to a worker; don't curl it yourself.
   - Even at the start, probe at most 3 times and stop, only to make the initial intent clear. As soon as you find yourself "investigating deeply" rather than "quickly setting direction" (enumerating endpoints or directories, trying ids one by one, decoding chains, repeatedly probing the same endpoint, any injection, privilege or vulnerability test: all heavy worker jobs), stop and write an intent.
   - If existing facts and the situation are enough to judge, don't probe at all.

4. **Decide which new directions to add**: **"restraint" here only means [don't duplicate existing intents], not "dispatch as little as possible"**. While the goal is unmet, the default question is "what deeper, harder, uncovered approaches can still get us closer to the goal", not "can we wrap up". An intent is [an open exploration direction] (not a fixed type or menu). Choose directions from known facts, assets and goals, and compare each against open + running + recent_done:
   - Already covered by open/running → don't create (it is being handled).
   - Appears in recent_done → **first check that intent's state (each has one) to see how it stopped, then decide**:
     · **done (ran to completion)**: covered → don't re-dispatch as is. Whether it is a dead end depends on the conclusions of the facts it yielded, not on state. Re-dispatch only when there is a [material new mechanism] (new fact, asset, parameter or clearly different approach), and state in the summary how it differs. Rewording or "trying again might work" doesn't count; don't retry.
     · **exhausted (budget ran out midway, only partial write-back) / blocked (model or network failure, barely explored)**: both ended early with incomplete information. First use get_worker_trace / get_worker_output to see how far it got and where it stuck, then pick: close to a breakthrough but cut off by budget → dispatch "continue from last progress"; purely external failure and didn't run (often blocked) → re-dispatch the same direction; stuck at the same place each time → change approach or direction. Always decide from actual progress in the trace, not from state itself.
   - A brand-new direction with no covering intent → create.
   - All known directions covered by intents still open/running → don't create; finish (there is running or queued work; let it advance). But if only recent_done covers them, nothing is open/running and the goal is unmet → per the hard floor above, you must open a new one or continue one.
   - **Depth over coverage**: coverage is a floor and an acceptance item, not the exploration goal itself. After finding a high-value entry (one that may lead to RCE, privilege escalation or data exfiltration), first dispatch intents to [drive that path deep], rather than spreading shallow tests across assets to level out coverage.
   - **Keep routes diverse; don't converge too early**: while the goal is unmet, if existing intents crowd one route or entry and there is an [essentially different] uncovered direction (another entry surface, another asset class, another exploit chain), add that diverging direction first rather than synonymous intents on the same line (judge by substance, not wording). If that direction is already covered, still don't create. Ideally 2-3 routes with different mechanisms run side by side (such as "via the upload chain" and "via auth bypass"), and resources concentrate only after one produces evidence of [approaching the goal]. **But diversity always yields to the [operational constraints] at the top**: never create intents for entry surfaces, ports, hosts or operations excluded by constraints, even if essentially different.

   **Serial exploit chains: dispatch step by step, don't split into parallel.** For a strongly dependent serial chain (1→2→3, each step needs the actual output of the previous step): don't dispatch all in parallel at once (the downstream can't get a prerequisite that doesn't exist yet and will only repeat or spin). Record the whole chain as todos with TodoWrite (one per step), dispatch only the step whose prerequisite is met this round (usually the first), and after it produces a fact, dispatch the next step at the next wake-up (the prompt carries the todo list) and mark satisfied ones completed. Don't split "the same thing" into two intents ("confirm the trigger point" and "trigger the trigger point" are one step). Use parallel intents only for [parallel, independent] dimensions (such as enumerating several unrelated endpoints).

5. **Submit**: use add_intent [once] to submit the selected new directions in a batch (intents array, at most the 4 highest-value ones; don't call it repeatedly one by one):
   - **summary**: one natural-language sentence describing the direction (full target address + what to do + why), without fixed categories. Deduplication mainly compares this against existing intents.
   - **asset_ids**: ids of target assets this direction tests or attacks (pass them whenever possible; 0, 1 or many, from list_assets). If the direction centers on concrete assets (site, endpoint, parameter, host), always pass them: they are used for coverage dedup and to connect the asset chain. Pass all of them when spanning several assets. Leave empty only for purely global recon with no concrete asset.
   - **parent_ids**: upstream nodes this direction was synthesized from (optional; 0, 1 or many). Pass all of them when several facts combine into one intent, and pass the id when derived from an upstream intent or finding. Leave empty for a brand-new top-level direction.

Don't duplicate and don't pad; but while the goal is unmet and there are uncovered, deeper approaches, dispatch them. Be concise, focused and efficient.`

func plannerSystem(ctx context.Context, goal, dataDir, workDir string) string {
	body := renderSystem(ctx, "planner", plannerDefaultTmpl, PlannerVars{Goal: goal, DataDir: dataDir, Now: nowStr()})
	return body + artifactSpec(workDir) + langDirective(resolveOutputLang(ctx))
}

// Plan runs one planning round. emit, if non-nil, receives the planner's execution
// steps (so users can see how it reads the situation and judges goals — the
// planner is the intent generator and was previously a black box). Returns whether
// the planner judged the goal met.
// triggers carries the concrete change(s) that fired this round — worker(s) done
// and/or finding(s) reported (may be several — the engine debounces a burst; empty
// for time/heartbeat wakes). They are spelled out at the top of the prompt so the
// planner looks first at the actual change (which intent, its output/finding).
func (p *Planner) Plan(ctx context.Context, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, goal string, triggers []TriggerEvent, emit func(db.Activity)) (met bool, reason string, err error) {
	// cold-digest §2.3/§7: advance this task's planner-round counter, maintain the
	// cold_since_round stamps, and (if a threshold is hit) kick off background
	// compaction. Synchronous part is cheap (a few queries); the LLM compaction
	// runs in a detached goroutine so it never adds latency to this round.
	if !isE2HTTPPlanning(ctx) {
		p.compactor.OnPlannerRound(ctx, ts)
	}
	tsx := NewToolSet(ts, "planner")
	tsx.SetFindingRecorder(p.findingRecorder)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetTaskID(taskID)
	tsx.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
	tsx.killWork = p.killWork   // enable kill_work tool (nil = unavailable)
	tsx.steerWork = p.steerWork // enable steer_work tool (nil = unavailable)
	if origin, _ := ts.OriginFactID(); origin > 0 {
		tsx.SetOwnerNode(origin) // planner-side anchors default to the task root (origin fact)
	}
	// 领域工具 + 基础默认工具集（Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash）
	// 资产覆盖度功能关闭时剔除 add_task_scope/list_untested_assets（不入 prompt）。
	base := tsx.DropCoverageTools(tsx.PlannerTools())
	if !isE2HTTPPlanning(ctx) {
		base = append(base, actool.DefaultTools()...)
	}
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts)})
	var tools []actool.CoreTool
	var def DeferredInfo
	cleanup := func() {}
	if isE2HTTPPlanning(ctx) {
		tools = e2HTTPPlannerTools(base)
	} else {
		tools, def, cleanup = AugmentTools(ctx, "planner", base)
	}
	defer cleanup()
	// 关键态势（刚完成的意图 + 预取的完整图）改放【本轮 user 输入】(见下方 input)，system
	// 只留静态规划正文。move-out 让 system 每轮稳定、更利于缓存；代价是若单轮变长，态势可能
	// 被 compaction 压缩（planner 单轮通常短，风险低）。situational 会拼进下方 input。
	situational := renderTriggers(ts, triggers) + renderGraphOverview(tsx.graphOverviewData(ctx))
	// 任务级 deadline / 终局模式(经 ctx 注入,见 taskclock.go)。终局那一轮把任务超时
	// planner 收尾词作为【本轮操作指令】拼进本轮 user 输入(随 situational),让它只做最后
	// 目标判定、不产新意图。
	tc := taskClockFrom(ctx)
	if tc.Final {
		situational += "\n\n[Task final wrap-up (special instruction for this round; overrides the regular planning flow above)]: " + resolveTaskTimeoutWrapup(ctx, "planner")
	}
	// 本任务的工作目录 <workDir>/tasks/<taskID>，先建好。
	taskDir := ensureRunDir(p.workDir, taskID, 0)
	ctx = intercept.WithReviewContext(ctx, taskDir, intercept.ReviewBackground{})
	sysBody := plannerSystem(ctx, goal, p.workDir, taskDir)
	if p.wantConstraints() {
		sysBody += constraintBlock(ts) // 操作约束(若有)注入系统提示,框定探索边界
	}
	system, boundary := deferredSystem(sysBody, def)
	// planner 无自身墙钟预算;有 deadline 时把 MaxDuration 夹逼到剩余,让在跑的规划轮在
	// 任务到点时进收尾(因超时→任务超时词,因步数→per-run 词)。
	maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, 0)
	settle := wrapupSettlement(ctx, "planner", nil)
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask(ctx, "planner", nil, clamped)
	}
	opts := agentcore.Options{
		Provider:        p.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		EnableWebFetch:  true, // 走记录代理留痕；载入代理 CA 验证 MITM 重签的 HTTPS 证书
		WebFetchProxy:   p.proxyAddr,
		WebFetchCACert:  p.proxyCACert,
		// 联网搜索(可选)。ddgs 无需 key；brave-free 需 BraveKey；tavily 需 TavilyKey。
		// WebSearchProxy 是独立出口代理(http/https/socks5)，与记录流量的 MITM 代理无关；空则直连。
		EnableWebSearch:       p.webSearch.Enabled,
		WebSearchBackend:      p.webSearch.Backend,
		BraveSearchAPIKey:     p.webSearch.BraveKey,
		TavilySearchAPIKey:    p.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: p.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  p.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel:   p.webSearch.DeepSeekModel,
		WebSearchProxy:        p.webSearch.Proxy,
		BashEnv:               proxyEnv(p.proxyAddr, p.proxyCACert), // Bash 子命令默认走代理+信任 CA
		WorkingDir:            taskDir,                              // 本任务工作目录 <workDir>/tasks/<taskID>
		ToolOutputDir:         cmdOutDir(taskDir),
		MaxTurns:              p.maxTurns, // 0 = unlimited (configurable in agent management)
		MaxDuration:           maxDur,     // 0=不限;有 deadline 时=距 deadline 剩余
		Compaction:            compactionConfig(p.compactionWindow()),
		// 跨唤醒共享的规划待办：让串行链在多轮之间保留（session 是新的，store 不是）。
		Todos: p.todoFor(ts.ID()),
		// 命中【本轮】步数预算→ SDK 跑收尾:把本轮已想清楚的结论落地(该派的 add_intent、
		// 能证的 prove_goal、串行链记 TodoWrite),而非停止规划——planner 之后仍会被反复唤醒。
		// clamped(被任务 deadline 夹逼)时改用 PromptByReason(见 wrapupSettlementForTask)。
		Settlement:   settle,
		NonStreaming: p.nonStreaming(), // 该 profile 选非流式时走 Provider.Complete
		MaxTokens:    p.maxTokens(),    // 0 = 不发上限,由服务端默认值决定
	}
	if isE2HTTPPlanning(ctx) {
		if e2Err := constrainE2HTTPPlannerOptions(ctx, &opts); e2Err != nil {
			return false, "e2_http_planning_blocked", e2Err
		}
	}

	if p.tx != nil && !isE2HTTPPlanning(ctx) { // E2 never resumes a legacy planner transcript
		opts.Transcript = p.tx
		opts.SessionID = fmt.Sprintf("exp%d-planner", ts.ID())
	}
	// 实验功能:开启后由 noa 接管上下文压缩(归档集中在 <workDir>/noa/<SessionID> 下,持久)。
	noaSession := fmt.Sprintf("exp%d-planner", ts.ID())
	if !isE2HTTPPlanning(ctx) {
		enableNoa(&opts, p.noaEnabledFn, p.workDir, noaSession, noaWarn(noaSession))
	}
	// 态势（刚完成的意图 + 完整图）现在拼进本轮 user 输入（见下方 input）。user 里还有
	// 指令 + 跨唤醒待办（todo 是模型自己的规划便签，可再生，放 user 即可）。
	// 开场白按「本轮有无具体变动」分两种：有变动 → 指向下方【实际变动】块；无变动
	// (心跳定时巡检 / hint / 恢复等) → 别谎称"图发生了变化",转而提示顺带复查在跑意图。
	lead := "There are concrete changes (see [Actual changes that triggered this round] below). Plan the next step from them: "
	if len(triggers) == 0 {
		lead = "This round is a wake-up from a **scheduled check (heartbeat) with no concrete change signal**; the graph may have no new changes. Also review running intents: use steer_work to correct ones with no progress for a long time or that have drifted, and kill_work to stop ones whose direction is wrong. Then judge the goals and decide whether to add directions: "
		// 心跳/无变动唤醒时,若全图已无任何 open 或 running 意图 → 探索已停摆(没 worker 在跑、
		// 也没排队方向)。明确告知 planner 并强制其本轮补出新方向,别只复查在跑意图后空转一轮。
		if active, err := ts.HasActiveIntent(); err == nil && !active {
			lead = "This round is a **scheduled check (heartbeat)** wake-up and there are **no open or running intents**: no worker is running and no direction is queued, so exploration has stalled. You **must** produce one or more new intents this round that move toward the goal and **don't duplicate** existing intents in the graph (zero intents is not allowed). First judge from the situation below whether the goal is met; if it isn't, add directions immediately: "
		}
	}
	todoText := ""
	if opts.Todos != nil {
		todoText = renderPlannerTodos(opts.Todos.List())
	}
	input := lead + situational + "\n\nFrom the situation above, judge the goals. When a goal is [truly met] (the target outcome is obtained or the target vulnerability is confirmed), mark it with prove_goal one by one. **Hard floor: if a goal is not met and there are no open or running intents (frontier_open=0 and running_intents empty), you must produce at least one intent moving toward the goal this round. With no running work to wait for and no queued direction, zero intents means the task stalls. You may produce no new intents only when open or running intents are advancing or the goal is met.**" +
		todoText
	if isE2HTTPPlanning(ctx) {
		input = "Use the current graph and trusted worker status to plan within the existing approval. Zero new intents is valid when policy, evidence, or the remaining budget prevents useful work. A new intent does not reset call, retry, approval, expiry, revocation, or task limits.\n" + situational
	}
	// MaxDuration 现在会在墙钟到点打断在跑工具并就地进收尾(在活 ctx 上),单轮卡死不再
	// 绕过收尾,无需外部硬 ctx 兜底。ctx 只承载 pause / kill / shutdown。
	_, _, err = captureRun(ctx, opts, input,
		func(r db.Activity) {
			if emit != nil {
				r.Worker = "planner" // planner activity has no intent_id (it generates them)
				emit(r)
			}
		})
	return tsx.GoalMet, tsx.Reason, err
}
