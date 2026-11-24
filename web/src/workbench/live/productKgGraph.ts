// 제품 소유 · 접근 그래프 투영 (계약 docs/contracts/product-access-map.md §5). 데이터는 제품 지도 응답(getProductAccessMap)
// 하나이고, 이 함수는 그 응답을 노드 · 간선으로 다시 그린다(G1: 서버 그래프 투영 없음). 노드 · 간선은 principals · resources ·
// cells 에서 1:1로만 나오고 칸에 없는 관계는 만들지 않는다. 자원 종류 ↔ 자산 간선은 만들지 않는다(kg-query-v1 D06).
// 간선은 "검사한 조합"이지 "접근함"이 아니다 — 관계를 주장하는 건 judged(확정 · 의심) 간선뿐이다(절대 규칙 5, O1).

import {
  PRODUCT_MAP_ERROR_TEXT,
  PRODUCT_MAP_STATE_TEXT,
  type ProductAccessMap,
  type ProductMapCell,
  type ProductMapResource,
  type ProductMapRole,
  productMapTally,
} from "./productAccessMap";

/** 간선 갈래(§5.2 표). 모양 · 화살표는 갈래마다 다르고 화살표는 judged 에만 있다. */
export type ProductKgEdgeKind = "judged" | "owner" | "undecidable" | "no_verdict" | "shared";
export const PRODUCT_KG_EDGE_ORDER: ProductKgEdgeKind[] = ["judged", "owner", "undecidable", "no_verdict", "shared"];

/** 범례 문구(§5.2). 첫 줄 + 다섯 갈래. */
export const PRODUCT_KG_LEGEND_HEAD = "간선 = 검사한 조합 · 실선 화살표만 교차 접근 판정(확정 · 의심)";
export const PRODUCT_KG_LEGEND_TEXT: Record<ProductKgEdgeKind, string> = {
  judged: "실선 화살표 — 취약 확정 / 취약 의심 판정",
  owner: "긴 점선 — 소유 계정(선언) · 판정 대상 아님",
  undecidable: "짧은 점선 — 판정 불가(저장 판정)",
  no_verdict: "옅은 점선 — 판정 기록 없음(사유 문구)",
  shared: "선 없음 — 공유 선언",
};

export type ProductKgPrincipalNode = { id: string; kind: "principal"; label: string; role: ProductMapRole };
export type ProductKgResourceNode = {
  id: string;
  kind: "resource";
  label: string;
  sharing: ProductMapResource["sharing"];
};
export type ProductKgNode = ProductKgPrincipalNode | ProductKgResourceNode;

export interface ProductKgEdge {
  id: string;
  kind: ProductKgEdgeKind;
  /** 계정 노드 id → 자원 종류 노드 id */
  src: string;
  dst: string;
  /** 간선 옆 글. 지도 칸과 같은 문구에서만 고른다(그래프만의 상태 · 문구 없음). */
  label: string;
  /** 이 간선이 그린 지도 칸 그대로(상세 · 사유 · 봉인 표시에 쓴다). */
  cell: ProductMapCell;
}

export interface ProductKgGraph {
  nodes: ProductKgNode[];
  edges: ProductKgEdge[];
}

const principalId = (role: ProductMapRole) => `p:${role}`;
const resourceId = (resourceType: string) => `r:${resourceType}`;

/** 칸 → 간선 갈래. 교차 칸은 지도 합계 칩과 같은 함수(productMapTally)로 정해 칩 건수와 간선 건수가 늘 맞는다. */
export function productKgEdgeKind(cell: ProductMapCell): ProductKgEdgeKind {
  if (cell.role === "owner") return "owner";
  switch (productMapTally(cell)) {
    case "confirmed":
    case "suspected":
      return "judged";
    case "na":
      return "shared";
    case "undecidable":
      return "undecidable";
    default:
      return "no_verdict";
  }
}

function edgeLabel(kind: ProductKgEdgeKind, cell: ProductMapCell): string {
  switch (kind) {
    case "judged":
      return PRODUCT_MAP_STATE_TEXT[cell.state];
    case "owner":
      return "소유 계정(선언)";
    case "undecidable":
      return cell.verdict ? `판정 불가 · 판정 ${cell.verdict.verdictId}` : PRODUCT_MAP_STATE_TEXT.unknown;
    case "no_verdict":
      // 원인을 추정하지 않고 지도의 투영 오류 문구를 그대로 쓴다. '미검사'라고 부르지 않는다(§5.2).
      return cell.projectionError ? PRODUCT_MAP_ERROR_TEXT[cell.projectionError] : PRODUCT_MAP_STATE_TEXT.unknown;
    case "shared":
      return "공유 선언";
  }
}

/** 제품 지도 응답 → 그래프. 노드 = 계정 2(owner → cross) + 자원 종류 N(동결본 순서), 간선 = 칸 2N(칸 순서). */
export function productKgGraph(map: ProductAccessMap): ProductKgGraph {
  const nodes: ProductKgNode[] = [
    { id: principalId("owner"), kind: "principal", label: map.owner, role: "owner" },
    { id: principalId("cross"), kind: "principal", label: map.cross, role: "cross" },
    ...map.resources.map(
      (r): ProductKgResourceNode => ({
        id: resourceId(r.resourceType),
        kind: "resource",
        label: r.resourceType,
        sharing: r.sharing,
      }),
    ),
  ];
  const edges = map.cells.map((cell): ProductKgEdge => {
    const kind = productKgEdgeKind(cell);
    return {
      id: `e:${cell.role}:${cell.resourceType}`,
      kind,
      src: principalId(cell.role),
      dst: resourceId(cell.resourceType),
      label: edgeLabel(kind, cell),
      cell,
    };
  });
  return { nodes, edges };
}
