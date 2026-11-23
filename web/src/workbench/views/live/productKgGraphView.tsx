"use client";

// 보고서 '자산 지식 그래프' 탭의 제품 소유 · 접근 그래프 (계약 docs/contracts/product-access-map.md §5, #877 G1~G3).
// 데이터는 제품 지도 응답 하나(getProductAccessMap)이고 그래프는 live/productKgGraph.ts 가 만든다. 왼쪽 계정 · 오른쪽 자원
// 종류의 이분 배치 inline SVG 다. 간선은 "검사한 조합"이고, 실선 화살표는 확정 · 의심 판정(judged)에만 쓴다 — 판정 불가 ·
// 판정 없음 · 공유 선언 · 소유 선언을 접근 성공처럼 그리지 않는다(절대 규칙 5, O1). 자산 그래프와 잇지 않는다(D06).
// 오류 · 대기 · 합계 칩 · 다시 읽기는 지도와 같은 틀(ProductMapBody · ProductMapSummary)을 쓴다(§5.3).
// ※ 운영자 전용: 보고서 내보내기 · 공유용 보고서 · 모델 입력에 넣지 않는다.

import { type CSSProperties, type KeyboardEvent, useId, useState } from "react";

import { cx } from "../../lib/cx";
import { dateTimeOf } from "../../live/map";
import type { ProductAccessMap, ProductMapState } from "../../live/productAccessMap";
import {
  PRODUCT_KG_EDGE_ORDER,
  PRODUCT_KG_LEGEND_HEAD,
  PRODUCT_KG_LEGEND_TEXT,
  type ProductKgEdge,
  type ProductKgEdgeKind,
  type ProductKgNode,
  productKgGraph,
} from "../../live/productKgGraph";
import { EVIDENCE_SEAL_TEXT } from "../../live/productVerdicts";
import { verdictReasonText } from "../../live/verdictReasons";
import s from "../../workbench.module.css";
import {
  notInstalled,
  ProductMapBody,
  ProductMapFootnote,
  ProductMapSummary,
  useProductAccessMap,
} from "./productAccessMapView";

// ---------- 배치 (viewBox 단위) ----------
const WIDTH = 640;
const PAD = 24;
const ROW = 64;
const NODE_W = 180;
const NODE_H = 40;
const LEFT_X = 16;
const RIGHT_X = WIDTH - 16 - NODE_W;
/** 간선 글 자리: 간선을 따라 이 비율만큼 간 곳(자원 종류 쪽에 가깝게 — 두 간선 글이 겹치지 않게). */
const LABEL_T = 0.62;
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

/** 보고서 그래프 탭 구획. 자산 그래프(kg-v1)와 따로 읽고 그린다 — 한쪽 실패가 다른 쪽을 가리지 않는다(§5.3). */
export function ProductKgGraphSection({ taskId, active }: { taskId: string | null; active: boolean }) {
  const poll = useProductAccessMap(taskId, active);
  if (!taskId) return null;
  if (poll.error && notInstalled(poll.error)) return null;
  return (
    <section className={s.isec} aria-label="제품 소유 · 접근 그래프">
      <h2>제품 소유 · 접근 그래프 (정책 선언 기준 · 수평)</h2>
      <p className={s.help}>
        정책 선언의 소유 · 교차 시험 계정과 자원 종류, 교차 계정 판정입니다. 위 자산 그래프와 출처가 달라 서로 잇지
        않습니다(경로가 비슷해도 대응 근거가 아닙니다). 계정 자격 · 표식 값은 없습니다.
      </p>
      <ProductMapBody poll={poll}>
        {(map, reload) => (
          <>
            <ProductMapSummary map={map} reload={reload} />
            <Graph map={map} />
            <ProductMapFootnote />
          </>
        )}
      </ProductMapBody>
    </section>
  );
}

function Graph({ map }: { map: ProductAccessMap }) {
  const graph = productKgGraph(map);
  const [picked, setPicked] = useState<string | null>(null);
  const uid = useId().replace(/:/g, "");
  const resources = graph.nodes.filter((n) => n.kind === "resource");
  const rows = Math.max(resources.length, 2);
  const height = PAD * 2 + ROW * rows;
  const pos = new Map<string, { x: number; y: number }>();
  // 계정은 위 · 아래 1/3 자리(owner 위, cross 아래), 자원 종류는 동결본 순서대로 한 줄씩
  pos.set("p:owner", { x: LEFT_X, y: PAD + (ROW * rows) / 3 });
  pos.set("p:cross", { x: LEFT_X, y: PAD + (ROW * rows * 2) / 3 });
  resources.forEach((n, i) => {
    pos.set(n.id, { x: RIGHT_X, y: PAD + ROW * i + ROW / 2 });
  });
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
          viewBox={`0 0 ${WIDTH} ${height}`}
          width="100%"
          style={{ maxWidth: WIDTH, minWidth: 480, display: "block" }}
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
          {graph.edges.map((e) => (
            <Edge
              key={e.id}
              edge={e}
              from={pos.get(e.src)}
              to={pos.get(e.dst)}
              marker={`${uid}-arrow-${e.cell.state}`}
              selected={e.id === picked}
              onPick={() => pick(e.id)}
              onKey={onKey(e.id)}
            />
          ))}
          {graph.nodes.map((n) => (
            <Node key={n.id} node={n} at={pos.get(n.id)} />
          ))}
        </svg>
      </div>
      <Legend uid={uid} />
      <Detail edge={selected} map={map} />
    </>
  );
}

function nodeSubText(node: ProductKgNode): string {
  if (node.kind === "principal") return node.role === "owner" ? "소유(A)" : "교차(B)";
  return node.sharing === "shared" ? "공유 선언" : "소유자 전용 선언";
}

function Node({ node, at }: { node: ProductKgNode; at: { x: number; y: number } | undefined }) {
  if (!at) return null;
  const sub = nodeSubText(node);
  const textStyle: CSSProperties = { fill: "var(--ink)", fontSize: 13, fontWeight: 600 };
  return (
    <g data-node={node.kind} data-node-id={node.id}>
      <title>{`${node.label} · ${sub}`}</title>
      <rect
        x={at.x}
        y={at.y - NODE_H / 2}
        width={NODE_W}
        height={NODE_H}
        rx={8}
        style={{ fill: "var(--well)", stroke: "var(--rule-2)" }}
      />
      <text
        x={at.x + NODE_W / 2}
        y={at.y - 3}
        textAnchor="middle"
        style={{ ...textStyle, fontFamily: node.kind === "resource" ? "var(--mono, monospace)" : undefined }}
      >
        {short(node.label)}
      </text>
      <text x={at.x + NODE_W / 2} y={at.y + 13} textAnchor="middle" style={{ fill: "var(--ink-3)", fontSize: 11 }}>
        {sub}
      </text>
    </g>
  );
}

function Edge({
  edge,
  from,
  to,
  marker,
  selected,
  onPick,
  onKey,
}: {
  edge: ProductKgEdge;
  from: { x: number; y: number } | undefined;
  to: { x: number; y: number } | undefined;
  marker: string;
  selected: boolean;
  onPick: () => void;
  onKey: (ev: KeyboardEvent) => void;
}) {
  if (!from || !to) return null;
  const x1 = from.x + NODE_W;
  const y1 = from.y;
  const x2 = to.x;
  const y2 = to.y;
  const st = productKgStroke(edge.kind, edge.cell.state);
  const lx = x1 + (x2 - x1) * LABEL_T;
  const ly = y1 + (y2 - y1) * LABEL_T + (edge.cell.role === "owner" ? -6 : 14);
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
        textAnchor="middle"
        style={{
          fontSize: 11,
          fontWeight: edge.kind === "judged" ? 700 : 400,
          fill: edge.kind === "judged" ? st.stroke : "var(--ink-3)",
          textDecoration: selected ? "underline" : undefined,
        }}
      >
        {edge.label}
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
