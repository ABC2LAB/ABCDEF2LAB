/**
 * KG 조회 v1 — `GET /api/tasks/{id}/coverage-graph?view=kg-v1` (docs/contracts/kg-query-v1.md).
 *
 * 닫힌 파서다(§5.2 "v1의 알려진 필드·종류·상태 밖의 응답은 클라이언트가 계약 오류로 처리하고 그래프를 공개하지 않는다").
 * 서버가 만들 수 없는 값(모르는 칸·종류·상태, 어긋난 ID 공식, 풀리지 않는 참조, 정렬 위반, 상한 초과)을 받으면
 * 그래프 전체를 버리고 invalid 로 돌려준다 — 일부만 그리거나 예시로 채우지 않는다.
 *
 * 오류는 HTTP 상태로 먼저 나누고, kg 봉투(§5.4)의 code·retryable 은 본문에 있고 상태와 맞을 때만 쓴다(§11-3:
 * 제품 중계가 스스로 내는 오류는 writeErr 본문이라 kg 봉투가 아니다).
 */
import { ApiError, apiFetchLimitedJSON } from "./api";

export const KG_QUERY_VERSION = "abc2lab.kg-query/v1";

/** §5.3 상한. 하나라도 넘으면 서버는 413 이므로, 200 으로 넘어온 초과 응답은 계약 오류다. */
export const KG_LIMITS = {
  nodes: 2000,
  edges: 5000,
  provenance: 5000,
  issues: 5000,
  bytes: 16 * 1024 * 1024,
} as const;

export const ASSET_TYPES = ["root_domain", "subdomain", "ip", "app", "service", "endpoint"] as const;
export type AssetType = (typeof ASSET_TYPES)[number];

/** §4.1 안전 라벨 기본값(D02 채택). 서버 라벨이 이 모양이 아니면 URL·제목이 섞였을 수 있으니 받지 않는다. */
export const ASSET_TYPE_LABEL: Record<AssetType, string> = {
  root_domain: "루트 도메인",
  subdomain: "하위 도메인",
  ip: "IP",
  app: "애플리케이션",
  service: "서비스",
  endpoint: "엔드포인트",
};

export type Basis = "attached" | "scope" | "anchor";
export type ContainmentRule = "root_domain" | "service_domain" | "service_ip" | "endpoint_origin";
export const LINK_SOURCES = ["system", "manual", "api", "agent", "task", "company", "legacy", "unknown"] as const;
export const SCOPE_SOURCES = ["manual", "agent", "auto", "unknown"] as const;
export type LinkSource = (typeof LINK_SOURCES)[number];
export type ScopeSource = (typeof SCOPE_SOURCES)[number];

export type IssueCode =
  | "legacy_link_missing"
  | "membership_mismatch"
  | "source_reference_unverified"
  | "scope_kind_not_projected"
  | "containment_ambiguous"
  | "unknown_source";
/** §5.4 code 마다 state 가 하나로 정해져 있다. */
export const ISSUE_STATE: Record<IssueCode, "insufficient" | "conflict"> = {
  legacy_link_missing: "insufficient",
  membership_mismatch: "conflict",
  source_reference_unverified: "insufficient",
  scope_kind_not_projected: "insufficient",
  containment_ambiguous: "insufficient",
  unknown_source: "insufficient",
};

export type KgTask = { id: string; taskId: string; role: "current" | "source" };
export type KgAsset = { id: string; assetId: string; assetType: AssetType };

export type KgEdge =
  | { id: string; kind: "task_asset"; src: string; dst: string; basis: Basis; refs: string[] }
  | { id: string; kind: "contained_by"; src: string; dst: string; refs: string[] }
  | { id: string; kind: "source_task"; src: string; dst: string; refs: string[] };

export type KgObservation = {
  nodeId: string;
  kind: "intent" | "fact" | "finding";
  sourceState: string;
  claimedConfidence: "observed" | "inferred" | "unknown";
  authority: "model_claim" | "unverified_reference";
};

export type KgProvenance =
  | {
      id: string;
      kind: "task_asset_link";
      taskId: string;
      assetId: string;
      source: LinkSource;
      inherited: boolean;
      observation: KgObservation | null;
    }
  | { id: string; kind: "asset_task_ids"; taskId: string; assetId: string; inherited: boolean }
  | {
      id: string;
      kind: "task_scope";
      scopeId: string;
      taskId: string;
      assetId: string;
      source: ScopeSource;
      inherited: boolean;
    }
  | {
      id: string;
      kind: "exploration_anchor";
      nodeId: string;
      taskId: string;
      assetId: string;
      inherited: boolean;
      observation: KgObservation | null;
    }
  | { id: string; kind: "task_relation"; taskId: string; sourceTaskId: string }
  | { id: string; kind: "asset_fields"; assetId: string; parentAssetId: string; rule: ContainmentRule };

export type KgIssue = { code: IssueCode; state: "insufficient" | "conflict"; refs: string[] };

export type KgGraph = {
  taskId: string;
  /** scope.task_ids 순서 그대로: 현재 작업 → 직접 원천(ID 오름차순) */
  tasks: KgTask[];
  /** ID 오름차순 */
  assets: KgAsset[];
  edges: KgEdge[];
  provenance: KgProvenance[];
  issues: KgIssue[];
  /** v1 은 소유·진단·정책 출처를 읽지 않는다 — 셋 다 항상 not_requested(§5.1, §11-1 D05) */
  sections: { ownership: "not_requested"; diagnosis: "not_requested"; policySource: "not_requested" };
  /** v1 은 항상 null(§5.2 정책 출처 보완). 값 모양은 후속 판에서 정한다 */
  policySource: null;
};

// ---------- 형식 ----------

const DEC = /^[1-9][0-9]{0,18}$/;
const INT64_MAX = BigInt("9223372036854775807");
/** 선행 0 없는 양의 int64 십진 문자열(§5.2). JS Number 로 바꾸지 않는다. */
function isId(v: unknown): v is string {
  return typeof v === "string" && DEC.test(v) && BigInt(v) <= INT64_MAX;
}
function cmpId(a: string, b: string): number {
  const x = BigInt(a);
  const y = BigInt(b);
  return Number(x > y) - Number(x < y);
}
/** 관계·출처·문제 ID 는 ASCII 라 JS 문자열 비교가 UTF-8 바이트 사전순과 같다 — ASCII 가 아니면 형식 오류. */
const ASCII_ID = /^[\x21-\x7e]{1,256}$/;
function byteCmp(a: string, b: string): number {
  return Number(a > b) - Number(a < b);
}

class Invalid extends Error {}
function fail(why: string): never {
  throw new Invalid(why);
}

type Obj = Record<string, unknown>;
function obj(v: unknown, where: string): Obj {
  if (typeof v !== "object" || v === null || Array.isArray(v)) fail(`${where}: object`);
  return v as Obj;
}
/** 칸 집합이 정확히 이것이어야 한다(모르는 칸 = 계약 오류, 빠진 칸 = 계약 오류 — null 과 생략을 대용하지 않는다). */
function keys(o: Obj, want: readonly string[], where: string): void {
  const have = Object.keys(o);
  if (have.length !== want.length || !want.every((k) => Object.hasOwn(o, k))) fail(`${where}: keys`);
}
function list(v: unknown, where: string, max: number): unknown[] {
  if (!Array.isArray(v)) fail(`${where}: array`);
  if (v.length > max) fail(`${where}: limit`);
  return v;
}
function oneOf<T extends string>(v: unknown, allowed: readonly T[], where: string): T {
  if (typeof v !== "string" || !(allowed as readonly string[]).includes(v)) fail(`${where}: enum`);
  return v as T;
}
function id(v: unknown, where: string): string {
  if (!isId(v)) fail(`${where}: id`);
  return v;
}
function bool(v: unknown, where: string): boolean {
  if (typeof v !== "boolean") fail(`${where}: bool`);
  return v;
}
/** 엄격 오름차순(중복 없음)인지 */
function ascending<T>(xs: T[], cmp: (a: T, b: T) => number, where: string): void {
  for (let i = 1; i < xs.length; i++) if (cmp(xs[i - 1], xs[i]) >= 0) fail(`${where}: order`);
}
function refList(v: unknown, where: string, allowEmpty: boolean): string[] {
  const xs = list(v, where, KG_LIMITS.provenance).map((r) => {
    if (typeof r !== "string" || !ASCII_ID.test(r)) fail(`${where}: ref`);
    return r;
  });
  if (!allowEmpty && xs.length === 0) fail(`${where}: empty`);
  ascending(xs, byteCmp, where);
  return xs;
}

// ---------- 부분 파서 ----------

/** db/schema.sql 탐색 노드 state CHECK 와 같다. 원천의 intent 는 끝난 네 상태만 공개한다(§3.1-3). */
const FACT_STATES = ["confirmed", "dismissed", "origin"] as const;
const FINDING_STATES = ["confirmed", "dismissed"] as const;
const INTENT_DONE = ["done", "blocked", "exhausted", "stopped"] as const;
const INTENT_CURRENT = ["open", "running", "paused", ...INTENT_DONE] as const;

function observation(v: unknown, where: string, inherited: boolean): KgObservation | null {
  if (v === null) return null;
  const o = obj(v, where);
  keys(o, ["node_id", "kind", "source_state", "claimed_confidence", "authority"], where);
  const kind = oneOf(o.kind, ["intent", "fact", "finding"] as const, `${where}.kind`);
  const nodeId = id(o.node_id, `${where}.node_id`);
  let sourceState: string;
  if (kind === "fact") sourceState = oneOf(o.source_state, FACT_STATES, `${where}.source_state`);
  else if (kind === "finding") sourceState = oneOf(o.source_state, FINDING_STATES, `${where}.source_state`);
  else sourceState = oneOf(o.source_state, inherited ? INTENT_DONE : INTENT_CURRENT, `${where}.source_state`);
  const claimedConfidence = oneOf(o.claimed_confidence, ["observed", "inferred", "unknown"] as const, where);
  const authority = oneOf(o.authority, ["model_claim", "unverified_reference"] as const, where);
  // §4.3: fact 만 payload confidence 를 읽고 model_claim, 나머지는 unknown·unverified_reference
  if (
    kind === "fact"
      ? authority !== "model_claim"
      : claimedConfidence !== "unknown" || authority !== "unverified_reference"
  )
    fail(`${where}: authority`);
  return { nodeId, kind, sourceState, claimedConfidence, authority };
}

type Ctx = { current: string; sources: Set<string>; tasks: Set<string>; assets: Map<string, KgAsset> };

function inScopeTask(ctx: Ctx, t: string, where: string): boolean {
  if (!ctx.tasks.has(t)) fail(`${where}: task_id`);
  return ctx.sources.has(t);
}
function knownAsset(ctx: Ctx, a: string, where: string): void {
  if (!ctx.assets.has(a)) fail(`${where}: asset_id`);
}

const PROV_KEYS = ["id", "kind", "task_id", "asset_id", "source", "inherited", "observation"] as const;
/** 종류별로 더 붙는 칸(§4.3) */
const EXTRA_KEYS: Partial<Record<KgProvenance["kind"], string[]>> = {
  task_scope: ["scope_id"],
  task_relation: ["source_task_id"],
  asset_fields: ["parent_asset_id", "rule"],
};

function provenance(v: unknown, i: number, ctx: Ctx): KgProvenance {
  const where = `provenance[${i}]`;
  const o = obj(v, where);
  const kind = oneOf(
    o.kind,
    ["task_asset_link", "asset_task_ids", "task_scope", "exploration_anchor", "task_relation", "asset_fields"] as const,
    `${where}.kind`,
  );
  const extra = EXTRA_KEYS[kind] ?? [];
  keys(o, [...PROV_KEYS, ...extra], where);
  const pid = o.id;
  if (typeof pid !== "string" || !ASCII_ID.test(pid)) fail(`${where}.id`);
  const inherited = bool(o.inherited, `${where}.inherited`);

  if (kind === "task_relation") {
    const taskId = id(o.task_id, `${where}.task_id`);
    const sourceTaskId = id(o.source_task_id, `${where}.source_task_id`);
    if (taskId !== ctx.current || !ctx.sources.has(sourceTaskId)) fail(`${where}: relation`);
    if (o.asset_id !== null || o.source !== null || o.observation !== null || inherited) fail(`${where}: nulls`);
    if (pid !== `relation:${taskId}:${sourceTaskId}`) fail(`${where}.id`);
    return { id: pid, kind, taskId, sourceTaskId };
  }
  if (kind === "asset_fields") {
    const assetId = id(o.asset_id, `${where}.asset_id`);
    const parentAssetId = id(o.parent_asset_id, `${where}.parent_asset_id`);
    knownAsset(ctx, assetId, where);
    knownAsset(ctx, parentAssetId, where);
    const rule = oneOf(o.rule, ["root_domain", "service_domain", "service_ip", "endpoint_origin"] as const, where);
    if (o.task_id !== null || o.source !== null || o.observation !== null || inherited) fail(`${where}: nulls`);
    if (pid !== `fields:${assetId}:${parentAssetId}:${rule}`) fail(`${where}.id`);
    // §4.1 규칙마다 자식·부모 유형이 정해져 있다 — 이걸로 포함 관계가 순환할 수 없다
    const pair: Record<ContainmentRule, [AssetType, AssetType]> = {
      root_domain: ["subdomain", "root_domain"],
      service_domain: ["service", "subdomain"],
      service_ip: ["service", "ip"],
      endpoint_origin: ["endpoint", "service"],
    };
    const [child, parent] = pair[rule];
    if (ctx.assets.get(assetId)?.assetType !== child || ctx.assets.get(parentAssetId)?.assetType !== parent)
      fail(`${where}: rule types`);
    return { id: pid, kind, assetId, parentAssetId, rule };
  }

  const taskId = id(o.task_id, `${where}.task_id`);
  const assetId = id(o.asset_id, `${where}.asset_id`);
  knownAsset(ctx, assetId, where);
  // §4.3: 출처를 가진 task 가 직접 원천이면 inherited=true
  if (inScopeTask(ctx, taskId, where) !== inherited) fail(`${where}: inherited`);

  if (kind === "task_asset_link") {
    if (pid !== `link:${taskId}:${assetId}`) fail(`${where}.id`);
    const source = oneOf(o.source, LINK_SOURCES, `${where}.source`);
    return {
      id: pid,
      kind,
      taskId,
      assetId,
      source,
      inherited,
      observation: observation(o.observation, `${where}.observation`, inherited),
    };
  }
  if (kind === "asset_task_ids") {
    if (pid !== `array:${taskId}:${assetId}`) fail(`${where}.id`);
    if (o.source !== "unknown" || o.observation !== null) fail(`${where}: array source`);
    return { id: pid, kind, taskId, assetId, inherited };
  }
  if (kind === "task_scope") {
    const scopeId = id(o.scope_id, `${where}.scope_id`);
    if (pid !== `scope:${scopeId}:${assetId}`) fail(`${where}.id`);
    const source = oneOf(o.source, SCOPE_SOURCES, `${where}.source`);
    if (o.observation !== null) fail(`${where}.observation`);
    return { id: pid, kind, scopeId, taskId, assetId, source, inherited };
  }
  // exploration_anchor
  const m = /^anchor:([^:]+):([^:]+)$/.exec(pid);
  if (!m || !isId(m[1]) || m[2] !== assetId) fail(`${where}.id`);
  if (o.source !== null) fail(`${where}.source`);
  const obs = observation(o.observation, `${where}.observation`, inherited);
  if (obs && obs.nodeId !== m[1]) fail(`${where}: anchor node`);
  return { id: pid, kind, nodeId: m[1], taskId, assetId, inherited, observation: obs };
}

function node(v: unknown, i: number): KgTask | KgAsset {
  const where = `nodes[${i}]`;
  const o = obj(v, where);
  if (o.kind === "task") {
    keys(o, ["id", "kind", "task_id", "label", "role"], where);
    const taskId = id(o.task_id, `${where}.task_id`);
    const role = oneOf(o.role, ["current", "source"] as const, `${where}.role`);
    if (o.id !== `t:${taskId}` || o.label !== `작업 #${taskId}`) fail(`${where}: id/label`);
    return { id: o.id, taskId, role };
  }
  if (o.kind === "asset") {
    keys(o, ["id", "kind", "asset_id", "asset_type", "label", "context_state", "observation_state"], where);
    const assetId = id(o.asset_id, `${where}.asset_id`);
    const assetType = oneOf(o.asset_type, ASSET_TYPES, `${where}.asset_type`);
    if (o.id !== `a:${assetId}` || o.label !== `${ASSET_TYPE_LABEL[assetType]} #${assetId}`) fail(`${where}: id/label`);
    // §4.2: 기본 v1 은 included·unknown 만 낸다(observed 는 후속 어댑터 계약 전까지 비활성)
    if (o.context_state !== "included" || o.observation_state !== "unknown") fail(`${where}: state`);
    return { id: o.id, assetId, assetType };
  }
  fail(`${where}.kind`);
}

function edge(v: unknown, i: number, nodeIds: Set<string>): KgEdge {
  const where = `edges[${i}]`;
  const o = obj(v, where);
  const kind = oneOf(o.kind, ["task_asset", "contained_by", "source_task"] as const, `${where}.kind`);
  keys(
    o,
    kind === "source_task"
      ? ["id", "kind", "src", "dst", "state", "provenance_refs"]
      : ["id", "kind", "src", "dst", "basis", "state", "provenance_refs"],
    where,
  );
  const { src, dst } = o;
  if (typeof src !== "string" || typeof dst !== "string" || !nodeIds.has(src) || !nodeIds.has(dst))
    fail(`${where}: endpoints`);
  const refs = refList(o.provenance_refs, `${where}.provenance_refs`, false);
  if (kind === "task_asset") {
    const basis = oneOf(o.basis, ["attached", "scope", "anchor"] as const, `${where}.basis`);
    if (!src.startsWith("t:") || !dst.startsWith("a:")) fail(`${where}: endpoint kinds`);
    // §5.2: anchor·attached 는 stored, scope 는 derived
    if (o.state !== (basis === "scope" ? "derived" : "stored")) fail(`${where}.state`);
    if (o.id !== `task_asset:${src}:${dst}:${basis}`) fail(`${where}.id`);
    return { id: o.id, kind, src, dst, basis, refs };
  }
  if (kind === "contained_by") {
    if (o.basis !== "asset_fields" || o.state !== "derived") fail(`${where}: basis/state`);
    if (!src.startsWith("a:") || !dst.startsWith("a:")) fail(`${where}: endpoint kinds`);
    if (o.id !== `contained_by:${src}:${dst}`) fail(`${where}.id`);
    return { id: o.id, kind, src, dst, refs };
  }
  if (o.state !== "stored" || !src.startsWith("t:") || !dst.startsWith("t:")) fail(`${where}: source_task`);
  if (o.id !== `source_task:${src}:${dst}`) fail(`${where}.id`);
  return { id: o.id, kind, src, dst, refs };
}

/** 관계의 출처 참조가 그 관계와 같은 끝점·종류의 출처로 풀리는지(§5.2 "끝점·provenance_refs 는 응답 안에서 해소") */
function checkEdgeRefs(e: KgEdge, prov: Map<string, KgProvenance>, where: string): void {
  for (const r of e.refs) {
    const p = prov.get(r);
    if (!p) fail(`${where}: dangling ref`);
    let ok = false;
    if (e.kind === "task_asset") {
      const t = e.src.slice(2);
      const a = e.dst.slice(2);
      const same = "taskId" in p && "assetId" in p && p.taskId === t && p.assetId === a;
      if (e.basis === "attached") ok = same && (p.kind === "task_asset_link" || p.kind === "asset_task_ids");
      else if (e.basis === "scope") ok = same && p.kind === "task_scope";
      else ok = same && p.kind === "exploration_anchor";
    } else if (e.kind === "contained_by") {
      ok = p.kind === "asset_fields" && `a:${p.assetId}` === e.src && `a:${p.parentAssetId}` === e.dst;
    } else {
      ok = p.kind === "task_relation" && `t:${p.taskId}` === e.src && `t:${p.sourceTaskId}` === e.dst;
    }
    if (!ok) fail(`${where}: ref kind`);
  }
}

function issue(v: unknown, i: number, refable: Set<string>): KgIssue {
  const where = `issues[${i}]`;
  const o = obj(v, where);
  keys(o, ["code", "state", "refs"], where);
  const code = oneOf(o.code, Object.keys(ISSUE_STATE) as IssueCode[], `${where}.code`);
  if (o.state !== ISSUE_STATE[code]) fail(`${where}.state`);
  const refs = refList(o.refs, `${where}.refs`, true);
  for (const r of refs) if (!refable.has(r)) fail(`${where}: dangling ref`);
  return { code, state: ISSUE_STATE[code], refs };
}
function cmpIssue(a: KgIssue, b: KgIssue): number {
  const c = byteCmp(a.code, b.code) || byteCmp(a.state, b.state);
  if (c) return c;
  for (let i = 0; i < Math.min(a.refs.length, b.refs.length); i++) {
    const r = byteCmp(a.refs[i], b.refs[i]);
    if (r) return r;
  }
  return a.refs.length - b.refs.length;
}

/** v1 응답 → 그래프. 계약 밖이면 null(이유는 개발 중 확인용으로만 던지고 화면에는 내보내지 않는다). */
export function parseKgQuery(raw: unknown): KgGraph | null {
  try {
    return parseOrThrow(raw);
  } catch (err) {
    if (err instanceof Invalid) return null;
    throw err;
  }
}

/** 시험용: 어디서 거부했는지 본다. 화면 코드는 parseKgQuery 를 쓴다. */
export function kgRejectReason(raw: unknown): string | null {
  try {
    parseOrThrow(raw);
    return null;
  } catch (err) {
    if (err instanceof Invalid) return err.message;
    throw err;
  }
}

function parseOrThrow(raw: unknown): KgGraph {
  const o = obj(raw, "response");
  keys(
    o,
    ["version", "task_id", "scope", "sections", "policy_source", "nodes", "edges", "provenance", "issues", "page"],
    "response",
  );
  if (o.version !== KG_QUERY_VERSION) fail("version");
  const taskId = id(o.task_id, "task_id");

  const scope = obj(o.scope, "scope");
  keys(scope, ["kind", "task_ids", "source_depth", "execution_scope"], "scope");
  if (scope.kind !== "task_context" || scope.source_depth !== 1 || scope.execution_scope !== null) fail("scope");
  const taskIds = list(scope.task_ids, "scope.task_ids", KG_LIMITS.nodes).map((t, i) => id(t, `scope.task_ids[${i}]`));
  if (taskIds[0] !== taskId) fail("scope.task_ids: current");
  ascending(taskIds.slice(1), cmpId, "scope.task_ids");
  if (taskIds.slice(1).includes(taskId)) fail("scope.task_ids: duplicate");

  const sections = obj(o.sections, "sections");
  keys(sections, ["assets", "provenance", "ownership", "diagnosis", "policy_source"], "sections");
  if (sections.assets !== "complete" || sections.provenance !== "complete") fail("sections: complete");
  if (sections.ownership !== "not_requested" || sections.diagnosis !== "not_requested") fail("sections: not_requested");
  if (sections.policy_source !== "not_requested" || o.policy_source !== null) fail("policy_source");

  const page = obj(o.page, "page");
  keys(page, ["mode", "has_more", "next_cursor"], "page");
  if (page.mode !== "complete" || page.has_more !== false || page.next_cursor !== null) fail("page");

  // 노드: 현재 작업 → 직접 원천(scope.task_ids 순서) → 자산(ID 오름차순)
  const nodes = list(o.nodes, "nodes", KG_LIMITS.nodes).map(node);
  const tasks = nodes.filter((n): n is KgTask => "taskId" in n);
  const assets = nodes.filter((n): n is KgAsset => "assetId" in n);
  if (nodes.findIndex((n) => "assetId" in n) !== -1 && nodes.findIndex((n) => "assetId" in n) !== tasks.length)
    fail("nodes: task first");
  if (tasks.length !== taskIds.length || tasks.some((t, i) => t.taskId !== taskIds[i])) fail("nodes: tasks ≠ scope");
  if (tasks[0].role !== "current" || tasks.slice(1).some((t) => t.role !== "source")) fail("nodes: roles");
  ascending(
    assets.map((a) => a.assetId),
    cmpId,
    "nodes: assets",
  );
  const ctx: Ctx = {
    current: taskId,
    sources: new Set(taskIds.slice(1)),
    tasks: new Set(taskIds),
    assets: new Map(assets.map((a) => [a.assetId, a])),
  };
  const nodeIds = new Set(nodes.map((n) => n.id));

  const provenanceList = list(o.provenance, "provenance", KG_LIMITS.provenance).map((p, i) => provenance(p, i, ctx));
  ascending(provenanceList, (a, b) => byteCmp(a.id, b.id), "provenance");
  const provById = new Map(provenanceList.map((p) => [p.id, p]));

  const edges = list(o.edges, "edges", KG_LIMITS.edges).map((e, i) => edge(e, i, nodeIds));
  ascending(edges, (a, b) => byteCmp(a.id, b.id), "edges");
  edges.forEach((e, i) => {
    checkEdgeRefs(e, provById, `edges[${i}]`);
  });

  // 직접 원천마다 source_task 관계 하나, 자산마다 포함 근거(task_asset) 하나 이상, 자식마다 부모 하나 이하
  for (const s of ctx.sources)
    if (edges.filter((e) => e.kind === "source_task" && e.dst === `t:${s}`).length !== 1) fail("edges: source_task");
  for (const e of edges) if (e.kind === "source_task" && e.src !== `t:${taskId}`) fail("edges: source_task src");
  for (const a of assets)
    if (!edges.some((e) => e.kind === "task_asset" && e.dst === a.id)) fail("edges: orphan asset");
  const parents = new Set<string>();
  for (const e of edges)
    if (e.kind === "contained_by") {
      if (parents.has(e.src)) fail("edges: two parents");
      parents.add(e.src);
    }

  const refable = new Set([...nodeIds, ...provById.keys()]);
  const issues = list(o.issues, "issues", KG_LIMITS.issues).map((x, i) => issue(x, i, refable));
  ascending(issues, cmpIssue, "issues");

  return {
    taskId,
    tasks,
    assets,
    edges,
    provenance: provenanceList,
    issues,
    sections: { ownership: "not_requested", diagnosis: "not_requested", policySource: "not_requested" },
    policySource: null,
  };
}

// ---------- 조회 ----------

/**
 * 조회 실패 종류. HTTP 상태로 먼저 나눈다(§11-3). unsupported 는 kg 봉투 없는 400 —
 * 아직 v1 을 모르는 서버가 query 를 거부한 경우일 수 있어 "요청 형식 오류"와 따로 둔다.
 */
export type KgFailure =
  | "invalid_request"
  | "unsupported"
  | "auth"
  | "forbidden"
  | "not_found"
  | "method"
  | "scope_unavailable"
  | "too_large"
  | "store_unavailable"
  | "offline"
  | "contract"
  | "failed";

export type KgLoad = { state: "ready"; graph: KgGraph } | { state: "error"; failure: KgFailure; retryable: boolean };

/** §5.4 HTTP ↔ 고정 code. 본문 code 가 상태와 맞을 때만 그 retryable 을 쓴다. */
const KG_CODE_BY_STATUS: Record<number, [string, KgFailure, boolean]> = {
  400: ["kg_request_invalid", "invalid_request", false],
  401: ["kg_auth_required", "auth", false],
  403: ["kg_task_forbidden", "forbidden", false],
  404: ["kg_task_not_found", "not_found", false],
  405: ["kg_method_not_allowed", "method", false],
  409: ["kg_scope_unavailable", "scope_unavailable", false],
  413: ["kg_graph_too_large", "too_large", false],
  503: ["kg_store_unavailable", "store_unavailable", true],
};

export function kgFailureOf(err: unknown): { failure: KgFailure; retryable: boolean } {
  if (err instanceof TypeError) return { failure: "offline", retryable: true };
  if (!(err instanceof ApiError)) return { failure: "failed", retryable: true };
  // apiFetchLimitedJSON 이 낸 수신 상한·JSON 손상 — 200 이지만 계약 밖
  if (err.status === 422 && (err.message === "response_limit" || err.message === "invalid_contract"))
    return { failure: "contract", retryable: false };
  if (err.status === 0 || err.status === 502 || err.status === 504) return { failure: "offline", retryable: true };
  const known = KG_CODE_BY_STATUS[err.status];
  if (!known) return { failure: "failed", retryable: true };
  const [code, failure, retryable] = known;
  const enveloped = err.code === code && typeof err.retryable === "boolean";
  // 봉투 없는 400 은 v1 을 모르는 서버(기존 query 거부)일 수 있다
  if (err.status === 400 && !enveloped) return { failure: "unsupported", retryable: false };
  // 중계가 스스로 낸 오류(writeErr)는 봉투가 없다 — 상태 기본값을 쓴다
  return { failure, retryable: enveloped ? (err.retryable as boolean) : retryable };
}

export function kgQueryPath(taskId: string): string {
  return `/api/tasks/${encodeURIComponent(taskId)}/coverage-graph?view=kg-v1`;
}

/** 이 task 의 KG v1 그래프. 실패는 던지지 않고 KgLoad 로 돌려준다(화면이 그래프 조회 실패로 그린다). */
export async function getKgQuery(taskId: string, signal?: AbortSignal): Promise<KgLoad> {
  if (!isId(taskId)) return { state: "error", failure: "invalid_request", retryable: false };
  let raw: unknown;
  try {
    raw = await apiFetchLimitedJSON(kgQueryPath(taskId), KG_LIMITS.bytes, { signal, cache: "no-store" });
  } catch (err) {
    if (err instanceof DOMException && err.name === "AbortError") throw err;
    return { state: "error", ...kgFailureOf(err) };
  }
  const graph = parseKgQuery(raw);
  // 다른 task 의 그래프가 오면 받지 않는다
  if (!graph || graph.taskId !== taskId) return { state: "error", failure: "contract", retryable: false };
  return { state: "ready", graph };
}
