/**
 * KG 조회 v1 계약 사례(docs/contracts/kg-query-v1.md §7) — 시험 전용.
 * E01 은 §5.1 전체 응답이고, E02~E05·E07 은 E01 에 대한 **최상위 필드 교체**다(중첩 객체를 merge 하지 않는다).
 * 계약 설명용 합성 ID 이며 실제 진단 결과가 아니다. 화면 코드에서 import 하지 않는다(예시 fallback 금지, §5.4).
 */

type Json = Record<string, unknown>;

export const KG_E01: Json = {
  version: "abc2lab.kg-query/v1",
  task_id: "41",
  scope: { kind: "task_context", task_ids: ["41"], source_depth: 1, execution_scope: null },
  sections: {
    assets: "complete",
    provenance: "complete",
    ownership: "not_requested",
    diagnosis: "not_requested",
    policy_source: "not_requested",
  },
  policy_source: null,
  nodes: [
    { id: "t:41", kind: "task", task_id: "41", label: "작업 #41", role: "current" },
    {
      id: "a:101",
      kind: "asset",
      asset_id: "101",
      asset_type: "endpoint",
      label: "엔드포인트 #101",
      context_state: "included",
      observation_state: "unknown",
    },
  ],
  edges: [
    {
      id: "task_asset:t:41:a:101:attached",
      kind: "task_asset",
      src: "t:41",
      dst: "a:101",
      basis: "attached",
      state: "stored",
      provenance_refs: ["link:41:101"],
    },
  ],
  provenance: [
    {
      id: "link:41:101",
      kind: "task_asset_link",
      task_id: "41",
      asset_id: "101",
      source: "system",
      inherited: false,
      observation: null,
    },
  ],
  issues: [],
  page: { mode: "complete", has_more: false, next_cursor: null },
};

export const KG_E02: Json = {
  provenance: [
    {
      id: "link:41:101",
      kind: "task_asset_link",
      task_id: "41",
      asset_id: "101",
      source: "manual",
      inherited: false,
      observation: null,
    },
  ],
};

export const KG_E03: Json = {
  edges: [
    {
      id: "task_asset:t:41:a:101:anchor",
      kind: "task_asset",
      src: "t:41",
      dst: "a:101",
      basis: "anchor",
      state: "stored",
      provenance_refs: ["anchor:901:101"],
    },
    {
      id: "task_asset:t:41:a:101:attached",
      kind: "task_asset",
      src: "t:41",
      dst: "a:101",
      basis: "attached",
      state: "stored",
      provenance_refs: ["link:41:101"],
    },
  ],
  provenance: [
    {
      id: "anchor:901:101",
      kind: "exploration_anchor",
      task_id: "41",
      asset_id: "101",
      source: null,
      inherited: false,
      observation: {
        node_id: "901",
        kind: "fact",
        source_state: "confirmed",
        claimed_confidence: "observed",
        authority: "model_claim",
      },
    },
    {
      id: "link:41:101",
      kind: "task_asset_link",
      task_id: "41",
      asset_id: "101",
      source: "agent",
      inherited: false,
      observation: null,
    },
  ],
};

export const KG_E04: Json = {
  scope: { kind: "task_context", task_ids: ["41", "40"], source_depth: 1, execution_scope: null },
  nodes: [
    { id: "t:41", kind: "task", task_id: "41", label: "작업 #41", role: "current" },
    { id: "t:40", kind: "task", task_id: "40", label: "작업 #40", role: "source" },
    {
      id: "a:101",
      kind: "asset",
      asset_id: "101",
      asset_type: "endpoint",
      label: "엔드포인트 #101",
      context_state: "included",
      observation_state: "unknown",
    },
  ],
  edges: [
    {
      id: "source_task:t:41:t:40",
      kind: "source_task",
      src: "t:41",
      dst: "t:40",
      state: "stored",
      provenance_refs: ["relation:41:40"],
    },
    {
      id: "task_asset:t:40:a:101:attached",
      kind: "task_asset",
      src: "t:40",
      dst: "a:101",
      basis: "attached",
      state: "stored",
      provenance_refs: ["link:40:101"],
    },
  ],
  provenance: [
    {
      id: "link:40:101",
      kind: "task_asset_link",
      task_id: "40",
      asset_id: "101",
      source: "manual",
      inherited: true,
      observation: null,
    },
    {
      id: "relation:41:40",
      kind: "task_relation",
      task_id: "41",
      source_task_id: "40",
      asset_id: null,
      source: null,
      inherited: false,
      observation: null,
    },
  ],
};

export const KG_E05: Json = {
  edges: [
    {
      id: "task_asset:t:41:a:101:scope",
      kind: "task_asset",
      src: "t:41",
      dst: "a:101",
      basis: "scope",
      state: "derived",
      provenance_refs: ["scope:20:101"],
    },
  ],
  provenance: [
    {
      id: "scope:20:101",
      kind: "task_scope",
      scope_id: "20",
      task_id: "41",
      asset_id: "101",
      source: "manual",
      inherited: false,
      observation: null,
    },
  ],
};

export const KG_E07: Json = {
  sections: {
    assets: "complete",
    provenance: "complete",
    ownership: "not_requested",
    diagnosis: "not_requested",
    policy_source: "not_requested",
  },
};

/** §7 규칙: E01 의 최상위 필드만 교체한다. 깊은 복사라 시험이 서로의 fixture 를 바꾸지 않는다. */
export function kgCase(patch: Json = {}): Json {
  return structuredClone({ ...KG_E01, ...patch });
}

/**
 * 포함 트리 사례(합성): 루트 도메인 → 하위 도메인 → 서비스 → 엔드포인트, 모두 범위 일치.
 * §4.1 네 규칙 중 root_domain·service_domain·endpoint_origin 과 containment_ambiguous issue 를 쓴다.
 */
export function kgTreeCase(): Json {
  const asset = (id: string, type: string, label: string) => ({
    id: `a:${id}`,
    kind: "asset",
    asset_id: id,
    asset_type: type,
    label: `${label} #${id}`,
    context_state: "included",
    observation_state: "unknown",
  });
  const scopeEdge = (a: string) => ({
    id: `task_asset:t:41:a:${a}:scope`,
    kind: "task_asset",
    src: "t:41",
    dst: `a:${a}`,
    basis: "scope",
    state: "derived",
    provenance_refs: [`scope:20:${a}`],
  });
  const scopeProv = (a: string) => ({
    id: `scope:20:${a}`,
    kind: "task_scope",
    scope_id: "20",
    task_id: "41",
    asset_id: a,
    source: "manual",
    inherited: false,
    observation: null,
  });
  const contained = (c: string, p: string, rule: string) => ({
    edge: {
      id: `contained_by:a:${c}:a:${p}`,
      kind: "contained_by",
      src: `a:${c}`,
      dst: `a:${p}`,
      basis: "asset_fields",
      state: "derived",
      provenance_refs: [`fields:${c}:${p}:${rule}`],
    },
    prov: {
      id: `fields:${c}:${p}:${rule}`,
      kind: "asset_fields",
      task_id: null,
      asset_id: c,
      parent_asset_id: p,
      rule,
      source: null,
      inherited: false,
      observation: null,
    },
  });
  const ids = ["1", "2", "3", "4", "5"];
  const c = [
    contained("2", "1", "root_domain"),
    contained("3", "2", "service_domain"),
    contained("4", "3", "endpoint_origin"),
  ];
  const edges = [...ids.map(scopeEdge), ...c.map((x) => x.edge)].sort((x, y) => (x.id < y.id ? -1 : 1));
  const provenance = [...ids.map(scopeProv), ...c.map((x) => x.prov)].sort((x, y) => (x.id < y.id ? -1 : 1));
  return kgCase({
    nodes: [
      { id: "t:41", kind: "task", task_id: "41", label: "작업 #41", role: "current" },
      asset("1", "root_domain", "루트 도메인"),
      asset("2", "subdomain", "하위 도메인"),
      asset("3", "service", "서비스"),
      asset("4", "endpoint", "엔드포인트"),
      asset("5", "endpoint", "엔드포인트"),
    ],
    edges,
    provenance,
    issues: [{ code: "containment_ambiguous", state: "insufficient", refs: ["a:5"] }],
  });
}
