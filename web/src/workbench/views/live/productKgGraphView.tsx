"use client";

// 접근통제 지도의 그래프 보기 — 제품 소유 · 접근 그래프 (계약 docs/contracts/product-access-map.md §5, #877 G1~G3).
// 격자 보기와 같은 제품 지도 응답을 다르게 그린 것뿐이라 보고서 '자산 지식 그래프' 탭이 아니라 접근통제 지도 구획에 둔다(11/24).
// 데이터는 제품 지도 응답 하나(getProductAccessMap)이고 그래프는 live/productKgGraph.ts 가 만든다. 왼쪽 계정 · 오른쪽 자원
// 종류의 이분 배치 inline SVG 다. 간선은 "검사한 조합"이고, 실선 화살표는 확정 · 의심 판정(judged)에만 쓴다 — 판정 불가 ·
// 판정 없음 · 공유 선언 · 소유 선언을 접근 성공처럼 그리지 않는다(절대 규칙 5, O1). 자산 그래프와 잇지 않는다(D06).
// 오류 · 대기 · 합계 칩 · 다시 읽기는 지도와 같은 틀(ProductMapBody · ProductMapSummary)을 쓴다(§5.3).
// ※ 운영자 전용: 보고서 내보내기 · 공유용 보고서 · 모델 입력에 넣지 않는다.

import { type CSSProperties, type KeyboardEvent, type ReactNode, useId, useState } from "react";

import { cx } from "../../lib/cx";
import { dateTimeOf } from "../../live/map";
import type { ProductAccessMap, ProductMapState } from "../../live/productAccessMap";
import {
  PRODUCT_KG_EDGE_ORDER,
  PRODUCT_KG_LEGEND_HEAD,
  PRODUCT_KG_LEGEND_TEXT,
  type ProductKgEdge,
  type ProductKgEdgeKind,
  type ProductKgGraph,
  type ProductKgPrincipalNode,
  type ProductKgResourceNode,
  productKgGraph,
} from "../../live/productKgGraph";
import { EVIDENCE_SEAL_TEXT } from "../../live/productVerdicts";
import { verdictReasonText } from "../../live/verdictReasons";
import s from "../../workbench.module.css";
import { ProductMapFootnote, ProductMapSummary } from "./productAccessMapView";

// ---------- 배치 (px, 축소 없이 그대로 그린다) ----------
// #893 이동찬 P2(2차): 폭 926 그래프를 패널에 맞춰 줄이면 1280px 창(그래프 자리 약 604px)에서 11px 글이 약 7.6px이 됐다.
// 그래서 (1) 간선 글을 자원 종류 상자 **안**에 A · B 두 줄로 넣어 전체 폭을 600 이하로 줄이고, (2) SVG 를 viewBox 크기 그대로
// 그린다(width = PRODUCT_KG_WIDTH). 더 좁은 자리에서는 바깥 overflowX 로 가로 스크롤되고 글자는 줄지 않는다.
const PAD = 16;
const LEFT_X = 12;
/** 계정 상자 */
const NODE_W = 130;
const NODE_H = 40;
/** 계정 상자와 자원 종류 상자 사이(간선이 지나가는 자리). 간선 글은 여기 두지 않는다 — 다른 간선이 글 위를 지나간다. */
const EDGE_SPAN = 118;
/** 자원 종류 상자: 이름 · 선언 한 줄 + 간선 글 A · B 두 줄. 폭은 가장 긴 간선 글("B · " + 가장 긴 투영 오류 문구)이 들어가는 폭. */
const RES_W = 328;
const RES_H = 62;
const RIGHT_X = LEFT_X + NODE_W + EDGE_SPAN;
/** 자원 종류 한 줄 높이(상자 + 줄 사이). */
const ROW = 74;
/** 계정 두 상자가 붙지 않게 하는 최소 줄 수(계정은 전체 높이의 1/3 · 2/3 자리). */
const MIN_ROWS = 3;
/** 상자 안 글 자리(상자 위에서부터의 기준선). 소유 · 교차 간선은 자기 글 줄 높이에서 상자에 닿는다. */
const INSET = 10;
const NAME_BASE = 18;
const OWNER_LABEL_BASE = 37;
const CROSS_LABEL_BASE = 54;
const OWNER_END = 33;
const CROSS_END = 50;
export const PRODUCT_KG_WIDTH = RIGHT_X + RES_W + LEFT_X;
export const PRODUCT_KG_LABEL_FONT = 11;
/** 노드 글 최대 글자 수(넘으면 줄이고 title 에 전체). 자원 종류 이름은 최대 64자다. */
const NODE_TEXT_MAX = 22;

/** 판정 간선 색: 지도 칸과 같은 workbench 토큰(확정 · 의심). */
const JUDGED_COLOR: Partial<Record<ProductMapState, string>> = { confirmed: "var(--crit)", suspected: "var(--mid)" };

type Stroke = { stroke: string; width: number; dash: string | null; opacity: number; arrow: boolean };

/**
 * 간선 갈래별 선 모양(§5.2 표). 다섯 갈래가 서로 다른 모양이고 화살표는 judged 뿐이다. shared 는 선을 그리지 않는다
 * (stroke none — 간선 자리 · 글만 있다).
 */
export function productKgStroke(kind: ProductKgEdgeKind, state: ProductMapState): Stroke {
  switch (kind) {
    case "judged":
      return { stroke: JUDGED_COLOR[state] ?? "var(--crit)", width: 2.5, dash: null, opacity: 1, arrow: true };
    case "owner":
      return { stroke: "var(--ink-3)", width: 1.5, dash: "9 5", opacity: 1, arrow: false };
    case "undecidable":
      return { stroke: "var(--und)", width: 1.5, dash: "3 3", opacity: 1, arrow: false };
    case "no_verdict":
      return { stroke: "var(--ink-3)", width: 1, dash: "1 5", opacity: 0.45, arrow: false };
    case "shared":
      return { stroke: "none", width: 0, dash: null, opacity: 1, arrow: false };
  }
}

const short = (text: string) => (text.length > NODE_TEXT_MAX ? `${text.slice(0, NODE_TEXT_MAX - 1)}…` : text);

type Point = { x: number; y: number };
export interface ProductKgEdgeLayout {
  edge: ProductKgEdge;
  x1: number;
  y1: number;
  x2: number;
  y2: number;
  /** 글 왼쪽 끝(text-anchor start) · 기준선 */
  lx: number;
  ly: number;
  text: string;
}
export interface ProductKgLayout {
  width: number;
  height: number;
  /** 계정 상자: (왼쪽 x, 가운데 y). 크기 nodeW × nodeH */
  nodes: Map<string, Point>;
  /** 자원 종류 상자: (왼쪽 x, 위 y). 크기 resW × resH. 이름 기준선은 위 + nameBase */
  boxes: Map<string, Point>;
  edges: ProductKgEdgeLayout[];
  nodeW: number;
  nodeH: number;
  resW: number;
  resH: number;
  nameBase: number;
}

/** 간선 글: 어느 계정 간선인지 A · B 를 붙인다(같은 자리에 두 글이 있으므로). 상태 · 문구는 간선 글 그대로다. */
export const productKgEdgeText = (edge: ProductKgEdge) => `${edge.cell.role === "owner" ? "A" : "B"} · ${edge.label}`;

/** 그래프 배치(순수 함수 — 시험이 글 상자 겹침 · 폭을 잰다). 왼쪽 계정 · 오른쪽 자원 종류(동결본 순서). */
export function productKgLayout(graph: ProductKgGraph): ProductKgLayout {
  const resources = graph.nodes.filter((n) => n.kind === "resource");
  const rows = Math.max(resources.length, MIN_ROWS);
  const height = PAD * 2 + ROW * rows;
  const nodes = new Map<string, Point>();
  // 계정은 위 · 아래 1/3 자리(owner 위, cross 아래), 자원 종류는 한 줄씩
  nodes.set("p:owner", { x: LEFT_X, y: PAD + (ROW * rows) / 3 });
  nodes.set("p:cross", { x: LEFT_X, y: PAD + (ROW * rows * 2) / 3 });
  const boxes = new Map<string, Point>();
  resources.forEach((n, i) => {
    boxes.set(n.id, { x: RIGHT_X, y: PAD + ROW * i + (ROW - RES_H) / 2 });
  });
  const edges: ProductKgEdgeLayout[] = [];
  for (const edge of graph.edges) {
    const from = nodes.get(edge.src);
    const box = boxes.get(edge.dst);
    if (!from || !box) continue;
    const owner = edge.cell.role === "owner";
    edges.push({
      edge,
      x1: from.x + NODE_W,
      y1: from.y,
      x2: box.x,
      y2: box.y + (owner ? OWNER_END : CROSS_END),
      lx: box.x + INSET,
      ly: box.y + (owner ? OWNER_LABEL_BASE : CROSS_LABEL_BASE),
      text: productKgEdgeText(edge),
    });
  }
  return {
    width: PRODUCT_KG_WIDTH,
    height,
    nodes,
    boxes,
    edges,
    nodeW: NODE_W,
    nodeH: NODE_H,
    resW: RES_W,
    resH: RES_H,
    nameBase: NAME_BASE,
  };
}

/** 그래프 보기: 지도 위 공통 줄 + 이분 그래프 + 공통 고지. 접근통제 지도 구획(productAccessMapSection)이 격자 보기와 바꿔 쓴다. */
export function ProductMapGraph({ map, reload }: { map: ProductAccessMap; reload: ReactNode }) {
  return (
    <>
      <ProductMapSummary map={map} reload={reload} />
      <Graph map={map} />
      <ProductMapFootnote />
    </>
  );
}

function Graph({ map }: { map: ProductAccessMap }) {
  const graph = productKgGraph(map);
  const [picked, setPicked] = useState<string | null>(null);
  const uid = useId().replace(/:/g, "");
  const layout = productKgLayout(graph);
  const height = layout.height;
  const selected = graph.edges.find((e) => e.id === picked) ?? null;
  const pick = (id: string) => setPicked(id);
  const onKey = (id: string) => (ev: KeyboardEvent) => {
    if (ev.key === "Enter" || ev.key === " ") {
      ev.preventDefault();
      pick(id);
    }
  };

  return (
    <>
      <div style={{ overflowX: "auto", marginTop: 10 }}>
        {/* biome-ignore lint/a11y/useSemanticElements: SVG 그림이라 fieldset 을 둘 수 없다 — 간선(버튼)을 묶는 group 이다 */}
        <svg
          viewBox={`0 0 ${layout.width} ${height}`}
          width={layout.width}
          height={height}
          style={{ display: "block", flex: "none" }}
          role="group"
          aria-label="계정 × 자원 종류 제품 소유 · 접근 그래프"
          data-testid="product-kg-graph"
        >
          <defs>
            {(["confirmed", "suspected"] as const).map((st) => (
              <marker
                key={st}
                id={`${uid}-arrow-${st}`}
                viewBox="0 0 10 10"
                refX="10"
                refY="5"
                markerWidth="7"
                markerHeight="7"
                orient="auto-start-reverse"
              >
                <path d="M 0 0 L 10 5 L 0 10 z" style={{ fill: JUDGED_COLOR[st] }} />
              </marker>
            ))}
          </defs>
          {/* 상자를 먼저 그린다 — 간선 글이 자원 종류 상자 안에 들어가므로 글이 상자 위에 와야 한다 */}
          {graph.nodes.map((n) =>
            n.kind === "principal" ? (
              <Principal key={n.id} node={n} at={layout.nodes.get(n.id)} />
            ) : (
              <Resource key={n.id} node={n} at={layout.boxes.get(n.id)} />
            ),
          )}
          {layout.edges.map((l) => (
            <Edge
              key={l.edge.id}
              at={l}
              marker={`${uid}-arrow-${l.edge.cell.state}`}
              selected={l.edge.id === picked}
              onPick={() => pick(l.edge.id)}
              onKey={onKey(l.edge.id)}
            />
          ))}
        </svg>
      </div>
      <Legend uid={uid} />
      <Detail edge={selected} map={map} />
    </>
  );
}

const boxStyle: CSSProperties = { fill: "var(--well)", stroke: "var(--rule-2)" };
const nameStyle: CSSProperties = { fill: "var(--ink)", fontSize: 13, fontWeight: 600 };
const subStyle: CSSProperties = { fill: "var(--ink-3)", fontSize: 11, fontWeight: 400 };

function Principal({ node, at }: { node: ProductKgPrincipalNode; at: Point | undefined }) {
  if (!at) return null;
  const sub = node.role === "owner" ? "소유(A)" : "교차(B)";
  return (
    <g data-node={node.kind} data-node-id={node.id}>
      <title>{`${node.label} · ${sub}`}</title>
      <rect x={at.x} y={at.y - NODE_H / 2} width={NODE_W} height={NODE_H} rx={8} style={boxStyle} />
      <text x={at.x + NODE_W / 2} y={at.y - 3} textAnchor="middle" style={nameStyle}>
        {short(node.label)}
      </text>
      <text x={at.x + NODE_W / 2} y={at.y + 13} textAnchor="middle" style={subStyle}>
        {sub}
      </text>
    </g>
  );
}

/** 자원 종류 상자: 첫 줄은 이름 · 선언, 아래 두 줄은 그 종류에 닿는 간선 글(A · B — Edge 가 그린다). */
function Resource({ node, at }: { node: ProductKgResourceNode; at: Point | undefined }) {
  if (!at) return null;
  const sub = node.sharing === "shared" ? "공유 선언" : "소유자 전용 선언";
  return (
    <g data-node={node.kind} data-node-id={node.id}>
      <title>{`${node.label} · ${sub}`}</title>
      <rect x={at.x} y={at.y} width={RES_W} height={RES_H} rx={8} style={boxStyle} />
      <text x={at.x + INSET} y={at.y + NAME_BASE} style={{ ...nameStyle, fontFamily: "var(--mono, monospace)" }}>
        {short(node.label)}
        <tspan dx={8} style={{ ...subStyle, fontFamily: "inherit" }}>
          {sub}
        </tspan>
      </text>
    </g>
  );
}

function Edge({
  at,
  marker,
  selected,
  onPick,
  onKey,
}: {
  at: ProductKgEdgeLayout;
  marker: string;
  selected: boolean;
  onPick: () => void;
  onKey: (ev: KeyboardEvent) => void;
}) {
  const { edge, x1, y1, x2, y2, lx, ly } = at;
  const st = productKgStroke(edge.kind, edge.cell.state);
  const who = edge.cell.role === "owner" ? "소유(A)" : "교차(B)";
  return (
    // biome-ignore lint/a11y/useSemanticElements: SVG 안이라 <button> 을 둘 수 없다 — role · tabIndex · 키 처리로 대신한다
    <g
      role="button"
      tabIndex={0}
      aria-pressed={selected}
      aria-label={`${edge.cell.principal}(${who}) → ${edge.cell.resourceType}: ${edge.label}`}
      data-edge={edge.kind}
      data-state={edge.cell.state}
      data-edge-id={edge.id}
      onClick={onPick}
      onFocus={onPick}
      onKeyDown={onKey}
      style={{ cursor: "pointer", outline: "none" }}
    >
      {/* 고르기 쉬운 투명한 넓은 선(모양과 상관없음) */}
      <line x1={x1} y1={y1} x2={x2} y2={y2} style={{ stroke: "transparent", strokeWidth: 14 }} />
      {st.stroke !== "none" ? (
        <line
          x1={x1}
          y1={y1}
          x2={x2}
          y2={y2}
          data-line=""
          style={{ stroke: st.stroke, strokeWidth: selected ? st.width + 1.5 : st.width, opacity: st.opacity }}
          strokeDasharray={st.dash ?? undefined}
          markerEnd={st.arrow ? `url(#${marker})` : undefined}
        />
      ) : null}
      <text
        x={lx}
        y={ly}
        textAnchor="start"
        data-label=""
        style={{
          fontSize: PRODUCT_KG_LABEL_FONT,
          fontWeight: edge.kind === "judged" ? 700 : 400,
          fill: edge.kind === "judged" ? st.stroke : "var(--ink-3)",
          textDecoration: selected ? "underline" : undefined,
        }}
      >
        {at.text}
      </text>
    </g>
  );
}

function Legend({ uid }: { uid: string }) {
  return (
    <div className={s.help} data-testid="product-kg-legend" style={{ marginTop: 8 }}>
      <div>{PRODUCT_KG_LEGEND_HEAD}</div>
      <ul style={{ listStyle: "none", padding: 0, margin: "4px 0 0", display: "flex", flexWrap: "wrap", gap: 12 }}>
        {PRODUCT_KG_EDGE_ORDER.map((kind) => {
          const st = productKgStroke(kind, "confirmed");
          return (
            <li key={kind} data-legend={kind} style={{ display: "inline-flex", alignItems: "center", gap: 6 }}>
              <svg width="36" height="10" viewBox="0 0 36 10" aria-hidden="true">
                {st.stroke !== "none" ? (
                  <line
                    x1="1"
                    y1="5"
                    x2="33"
                    y2="5"
                    style={{ stroke: st.stroke, strokeWidth: st.width, opacity: st.opacity }}
                    strokeDasharray={st.dash ?? undefined}
                    markerEnd={st.arrow ? `url(#${uid}-arrow-confirmed)` : undefined}
                  />
                ) : null}
              </svg>
              <span>{PRODUCT_KG_LEGEND_TEXT[kind]}</span>
            </li>
          );
        })}
      </ul>
    </div>
  );
}

/** 고른 간선의 칸 내용. 지도 칸과 같은 allowlist(판정 번호 · 관측 시각 · 사유 · 봉인 미확인 · 투영 오류)만 보인다. */
function Detail({ edge, map }: { edge: ProductKgEdge | null; map: ProductAccessMap }) {
  if (!edge)
    return (
      <p className={s.help} data-testid="product-kg-edge-detail">
        간선을 고르면(클릭 · 키보드) 그 칸의 판정을 보입니다.
      </p>
    );
  const c = edge.cell;
  const v = c.verdict;
  const reasons = v ? [...v.oracleReasonCodes, ...v.productReasonCodes] : [];
  const who = c.role === "owner" ? `${map.owner} · 소유(A)` : `${map.cross} · 교차(B)`;
  return (
    <div className={cx(s.note)} role="status" data-testid="product-kg-edge-detail" data-edge={edge.kind}>
      <span className={s["lv-grow"]}>
        <b>{`${who} → ${c.resourceType}`}</b>
        {` · ${edge.label}`}
        {edge.kind === "owner" ? " · 선언이 정한 소유 계정입니다. 소유 계정 조회 결과가 아닙니다." : null}
        {v ? ` · 판정 ${v.verdictId} · ${dateTimeOf(v.observedAt)}` : null}
        {reasons.length ? ` · 사유: ${reasons.map(verdictReasonText).join(" · ")}` : null}
        {v?.evidenceSeal?.status === "unconfirmed" ? ` · ${EVIDENCE_SEAL_TEXT.unconfirmed}` : null}
      </span>
    </div>
  );
}
