// 보고서 '자산 지식 그래프' 탭의 화면 모델과 문구 (KG 조회 v1 — docs/contracts/kg-query-v1.md).
// 데이터 모양은 lib/diagnosis/kg-query.ts(닫힌 파서)가 정하고, 여기는 화면에 맞게 묶기만 한다.
// - 포함(basis)과 관측 출처를 따로 보인다(D03). 원천 작업의 포함은 '읽기 전용 상속'으로 표시한다.
// - 소유·정책 출처는 v1 이 조회하지 않으므로 '미조회' 문구만 보인다(D05). '없음'·'허용'·'안전'으로 칠하지 않는다.
// - 그래프 조회가 실패하면 소유·정책 출처 칸을 그리지 않는다(§11-1, v1 실패는 소유 문구로 보이지 않음).

import type {
  Basis,
  ContainmentRule,
  IssueCode,
  KgAsset,
  KgFailure,
  KgGraph,
  KgObservation,
  KgProvenance,
  LinkSource,
  ScopeSource,
} from "@/lib/diagnosis/kg-query";
import { ASSET_TYPE_LABEL } from "@/lib/diagnosis/kg-query";

export const BASIS_TEXT: Record<Basis, string> = {
  attached: "직접 연결",
  scope: "범위 일치",
  anchor: "탐색 앵커",
};
/** 포함 근거의 뜻 — 어느 것도 대상 전송 허가·검사 완료가 아니다(§3.1, E05) */
export const BASIS_HELP: Record<Basis, string> = {
  attached: "작업에 저장된 연결입니다.",
  scope: "작업 범위 조건과 저장 자산 필드가 일치합니다. 전송 허가나 검사 완료가 아닙니다.",
  anchor: "탐색 기록이 이 자산을 가리킵니다. 기록 내용이 참이라는 뜻은 아닙니다.",
};

export const LINK_SOURCE_TEXT: Record<LinkSource, string> = {
  system: "시스템",
  manual: "수동 등록",
  api: "API 등록",
  agent: "에이전트",
  task: "작업 생성",
  company: "회사 연결",
  legacy: "이전 기록",
  unknown: "미등록 값",
};
export const SCOPE_SOURCE_TEXT: Record<ScopeSource, string> = {
  manual: "수동",
  agent: "에이전트",
  auto: "자동",
  unknown: "미등록 값",
};
export const RULE_TEXT: Record<ContainmentRule, string> = {
  root_domain: "루트 도메인 일치",
  service_domain: "서비스 도메인 일치",
  service_ip: "서비스 IP 일치",
  endpoint_origin: "같은 origin",
};

const OBS_KIND_TEXT: Record<KgObservation["kind"], string> = { intent: "의도", fact: "사실", finding: "발견" };
const CONFIDENCE_TEXT: Record<KgObservation["claimedConfidence"], string> = {
  observed: "관측",
  inferred: "추론",
  unknown: "미상",
};
/** 관측 참조는 근거가 아니라 참조다 — 모델 주장·검증 안 된 참조임을 같이 적는다(§4.3) */
export function observationText(o: KgObservation): string {
  const head = `${OBS_KIND_TEXT[o.kind]} #${o.nodeId} · 상태 ${o.sourceState}`;
  return o.authority === "model_claim"
    ? `${head} · 모델 주장(${CONFIDENCE_TEXT[o.claimedConfidence]}, 검증 안 됨)`
    : `${head} · 검증 안 된 참조`;
}

export const ISSUE_TEXT: Record<IssueCode, string> = {
  legacy_link_missing: "작업 배열에는 연결이 있으나 상세 출처 행이 없습니다.",
  membership_mismatch: "연결 행과 작업 배열이 어긋나 그 연결은 포함 근거로 쓰지 않았습니다.",
  source_reference_unverified: "관측 참조를 검증하지 못해 관측을 비워 두었습니다.",
  scope_kind_not_projected: "키워드 범위는 자산을 추정하지 않습니다.",
  containment_ambiguous: "부모 후보가 여럿이라 포함 관계를 그리지 않았습니다.",
  unknown_source: "등록되지 않은 출처 값이라 '미등록 값'으로 표시합니다.",
};
export const ISSUE_STATE_TEXT = { insufficient: "근거 부족", conflict: "상충" } as const;

/** D05 · 정책 출처 문구(§11-1). v1 응답에서는 항상 '미조회'다. */
export const OWNERSHIP_NOT_REQUESTED = "소유 미조회 — 이 그래프는 소유를 조회하지 않습니다";
export const POLICY_SOURCE_NOT_REQUESTED = "정책 출처 미조회 — 이 그래프는 회차 정책 출처를 조회하지 않습니다";
export const OBSERVATION_UNKNOWN = "관측 미확인 — 이 그래프는 자산의 실제 관측을 판정하지 않습니다";

/** 그래프 조회 실패 문구(오류 code 별). 소유 문구를 쓰지 않는다(§11-1). */
export const FAILURE_TEXT: Record<KgFailure, string> = {
  invalid_request: "조회 요청 형식이 맞지 않습니다.",
  unsupported: "서버가 이 조회를 받지 않았습니다. 아직 KG 조회 v1을 제공하지 않는 서버일 수 있습니다.",
  auth: "로그인이 필요합니다.",
  forbidden: "이 점검이나 직접 원천 작업을 읽을 권한을 확인하지 못했습니다.",
  not_found: "점검을 찾지 못했습니다.",
  method: "허용되지 않은 조회 방식입니다.",
  scope_unavailable: "이 점검의 작업 문맥을 하나로 확인할 수 없습니다.",
  too_large:
    "그래프가 조회 상한(노드 2,000 · 관계 5,000 · 출처 5,000 · 문제 5,000 · 16MiB)을 넘었습니다. 잘라서 보이지 않습니다.",
  store_unavailable: "자산 저장소를 지금 읽지 못했습니다.",
  offline: "엔진에 연결하지 못했습니다.",
  contract: "응답이 KG 조회 v1 계약과 달라 그리지 않습니다.",
  failed: "그래프를 불러오지 못했습니다.",
};

// ---------- 화면 모델 ----------

/** 자산 하나를 포함한 작업과 근거. inherited=true 면 직접 원천 작업의 포함(읽기 전용 상속). */
export type Inclusion = { taskId: string; inherited: boolean; bases: Basis[] };

export type AssetRow = {
  asset: KgAsset;
  label: string;
  inclusions: Inclusion[];
  /** 포함 관계 규칙(부모가 있을 때만) */
  rule: ContainmentRule | null;
  children: AssetRow[];
};

export type KgView = {
  currentTaskId: string;
  sourceTaskIds: string[];
  roots: AssetRow[];
  /** 자산 ID → 그 자산을 가리키는 출처(자산 필드 파생은 자식 쪽) */
  provenanceByAsset: Map<string, KgProvenance[]>;
  /** 화면 참조 ID(노드·출처) → 사람이 읽는 이름 */
  refLabel: (ref: string) => string;
  counts: { assets: number; provenance: number; issues: number };
};

const BASIS_ORDER: Basis[] = ["attached", "scope", "anchor"];

export function assetLabel(a: KgAsset): string {
  return `${ASSET_TYPE_LABEL[a.assetType]} #${a.assetId}`;
}

export function buildKgView(g: KgGraph): KgView {
  const current = g.tasks[0].taskId;
  const sources = new Set(g.tasks.slice(1).map((t) => t.taskId));
  const rows = new Map<string, AssetRow>();
  for (const a of g.assets)
    rows.set(a.id, { asset: a, label: assetLabel(a), inclusions: [], rule: null, children: [] });

  const fieldsById = new Map<string, ContainmentRule>();
  for (const p of g.provenance) if (p.kind === "asset_fields") fieldsById.set(p.id, p.rule);

  const hasParent = new Set<string>();
  for (const e of g.edges) {
    if (e.kind === "task_asset") {
      const row = rows.get(e.dst);
      if (!row) continue;
      const taskId = e.src.slice(2);
      let inc = row.inclusions.find((x) => x.taskId === taskId);
      if (!inc) {
        inc = { taskId, inherited: sources.has(taskId), bases: [] };
        row.inclusions.push(inc);
      }
      if (!inc.bases.includes(e.basis)) inc.bases.push(e.basis);
    } else if (e.kind === "contained_by") {
      const child = rows.get(e.src);
      const parent = rows.get(e.dst);
      if (!child || !parent) continue;
      parent.children.push(child);
      child.rule = fieldsById.get(e.refs[0]) ?? null;
      hasParent.add(e.src);
    }
  }
  // 현재 작업 먼저, 그다음 원천 작업(ID 오름차순 — scope.task_ids 순서). 근거는 고정 순서.
  const taskOrder = new Map(g.tasks.map((t, i) => [t.taskId, i]));
  for (const row of rows.values()) {
    row.inclusions.sort((x, y) => (taskOrder.get(x.taskId) ?? 0) - (taskOrder.get(y.taskId) ?? 0));
    for (const inc of row.inclusions) inc.bases.sort((x, y) => BASIS_ORDER.indexOf(x) - BASIS_ORDER.indexOf(y));
  }
  // 노드가 ID 오름차순이라 자식도 그 순서로 쌓인다
  const roots = g.assets.filter((a) => !hasParent.has(a.id)).map((a) => rows.get(a.id) as AssetRow);

  const provenanceByAsset = new Map<string, KgProvenance[]>();
  for (const p of g.provenance) {
    if (p.kind === "task_relation") continue;
    const key = p.assetId;
    const xs = provenanceByAsset.get(key) ?? [];
    xs.push(p);
    provenanceByAsset.set(key, xs);
  }

  const labels = new Map<string, string>();
  for (const t of g.tasks) labels.set(t.id, `작업 #${t.taskId}`);
  for (const a of g.assets) labels.set(a.id, assetLabel(a));
  for (const p of g.provenance) labels.set(p.id, provenanceTitle(p));

  return {
    currentTaskId: current,
    sourceTaskIds: [...sources],
    roots,
    provenanceByAsset,
    refLabel: (ref) => labels.get(ref) ?? ref,
    counts: { assets: g.assets.length, provenance: g.provenance.length, issues: g.issues.length },
  };
}

/** 출처 한 줄 제목. 원문 summary·URL·payload 는 v1 에 없으므로 종류·ID·허용 값만 쓴다. */
export function provenanceTitle(p: KgProvenance): string {
  switch (p.kind) {
    case "task_asset_link":
      return `작업 #${p.taskId} 연결 · ${LINK_SOURCE_TEXT[p.source]}`;
    case "asset_task_ids":
      return `작업 #${p.taskId} 연결 · 출처 행 없음`;
    case "task_scope":
      return `작업 #${p.taskId} 범위 #${p.scopeId} · ${SCOPE_SOURCE_TEXT[p.source]}`;
    case "exploration_anchor":
      return `작업 #${p.taskId} 탐색 앵커 #${p.nodeId}`;
    case "task_relation":
      return `작업 #${p.taskId} ← 직접 원천 작업 #${p.sourceTaskId}`;
    case "asset_fields":
      return `자산 필드 파생 · ${RULE_TEXT[p.rule]} (부모 #${p.parentAssetId})`;
  }
}
