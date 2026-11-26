"use client";

// 보고서 '자산 지식 그래프' 탭의 자산 관계 그래프 — 탐색이 관찰한 자산과 포함 관계(GET /api/tasks/{id}/coverage-graph).
// 데이터 · 접힘 · 배치는 live/coverageGraph.ts 순수 함수이고, 여기는 inline SVG 그리기와 조작(끌기 · 휠 확대 · 펼치기)만 한다
// (새 의존성 없음). 같은 부모 아래 같은 종류가 FOLD_LIMIT 개를 넘으면 '…' 노드로 접어 노드가 많아도 배치 · 글이 읽히게 둔다.
// '검사함'은 탐색 · 점검이 그 자산을 다뤘다는 뜻이지 취약점이 없다는 뜻이 아니다(절대 규칙 5) — 정상색(초록)을 쓰지 않는다.

import {
  type CSSProperties,
  type KeyboardEvent,
  type ReactNode,
  type PointerEvent as ReactPointerEvent,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";

import { ApiError } from "@/lib/diagnosis/api";

import { cx } from "../../lib/cx";
import { fetchCoverageGraph } from "../../live/api";
import {
  type CgGraph,
  type CgNode,
  COVERAGE_KIND_TEXT,
  COVERAGE_KINDS,
  type CoverageKind,
  coverageGraphStats,
  coverageNodeLabel,
  coverageNodeTitle,
  coveragePathDetailLine,
  FOLD_LIMIT,
  FOLD_STEP,
  layoutCoverageGraph,
  type Point,
  parseCoverageGraph,
  type VisNode,
  visibleCoverageGraph,
} from "../../live/coverageGraph";
import { usePoll } from "../../live/usePoll";
import { Icon } from "../../ui/icons";
import s from "../../workbench.module.css";
import { KgAssetDetail, KgOverview, type KgState, useKgQuery } from "./kgAssetDetail";

/** 종류 색(옛 ARTEX 자산 그래프와 같은 갈래). 위험도 색(--crit 등)과 섞이지 않게 따로 둔다. */
const KIND_COLOR: Record<CoverageKind, string> = {
  company: "#64748b",
  root_domain: "#6366f1",
  subdomain: "#3b82f6",
  ip: "#0891b2",
  service: "#f59e0b",
  app: "#d946ef",
  endpoint: "#f43f5e",
};
const KIND_RADIUS: Record<CoverageKind, number> = {
  company: 17,
  root_domain: 15,
  subdomain: 13,
  ip: 12,
  service: 12,
  app: 10,
  endpoint: 9,
};
const FOLD_RADIUS = 10;
/** SVG 좌표계 크기. 화면 폭에 맞춰 늘고 줄며, 확대 · 이동은 이 좌표 위에서 한다. */
const VIEW_W = 1000;
const VIEW_H = 620;
const FIT_PAD = 60;
const ZOOM_MIN = 0.15;
const ZOOM_MAX = 4;
const WHEEL_RATE = 0.0015;
/** +/− 버튼 한 번의 배율 */
const ZOOM_STEP = 1.25;
/** 끌기로 보는 최소 이동(px). 이보다 적게 움직이면 누름으로 본다. */
const DRAG_SLOP = 4;
/** 엔드포인트 이름은 확대했거나 수가 적을 때만 늘 보인다(많으면 겹쳐 읽히지 않음). 고르거나 올리면 늘 보인다. */
const ENDPOINT_LABEL_ZOOM = 0.9;
const ENDPOINT_LABEL_MAX = 40;
const LABEL_MAX = 26;

type View = { x: number; y: number; k: number };

/** 보고서 '자산 지식 그래프' 탭 몸통 — 자산 관계 그래프에 KG 조회 v1 포함 근거 · 출처를 붙여 한 구획으로 그린다. */
export function AssetGraphTab({ taskId, active }: { taskId: string; active: boolean }) {
  const kg = useKgQuery(taskId, active);
  return <CoverageGraphSection taskId={taskId} active={active} kg={kg} />;
}

/**
 * 그래프 탭 구획. 탭이 보일 때만 읽고, 탭에 다시 들어오거나 [다시 읽기]를 누르면 다시 읽는다(주기 조회 없음).
 * kg(KG 조회 v1)를 주면 노드 상세에 그 자산의 포함 근거 · 출처를, 그래프 아래에 요약 한 줄을 붙인다(kgAssetDetail).
 */
export function CoverageGraphSection({ taskId, active, kg }: { taskId: string | null; active: boolean; kg?: KgState }) {
  const poll = usePoll(() => fetchCoverageGraph(taskId as string), [taskId], { enabled: active && !!taskId });
  const graph = useMemo(() => (poll.data ? parseCoverageGraph(poll.data) : null), [poll.data]);
  if (!taskId) return null;
  const reload = (
    <button className={cx(s.btn, s.sm)} type="button" onClick={poll.reload}>
      다시 읽기
    </button>
  );
  let body: ReactNode;
  if (poll.error) {
    body = (
      <div className={cx(s.note, s.warn)} role="alert">
        <Icon name="alert" />
        <span className={s["lv-grow"]}>{failText(poll.error)}</span>
        {reload}
      </div>
    );
  } else if (graph) {
    body = <Graph graph={graph} onReload={poll.reload} kg={kg} />;
  } else if (poll.fetchedAt !== null) {
    // 503 → null: 저장된 자산 그래프를 지금 확인할 수 없다(자산 저장소 꺼짐 · 조회 실패). 빈 그래프로 대신하지 않는다
    body = (
      <div className={s.note} role="note">
        <Icon name="info" />
        <span className={s["lv-grow"]}>
          저장된 자산 그래프를 지금 확인할 수 없습니다(자산 저장소 꺼짐 또는 조회 실패).
        </span>
        {reload}
      </div>
    );
  } else {
    body = (
      <p className={s.empty} role="status">
        자산 그래프를 불러오는 중…
      </p>
    );
  }
  return (
    <section className={s.isec} aria-label="자산 관계 그래프">
      <h2>자산 관계 그래프</h2>
      <p className={s.help}>
        탐색이 실제로 관찰한 자산과 포함 관계입니다(회사 → 도메인 → 서비스 → 엔드포인트). 관찰하지 않은 관계나 내부
        구조는 그리지 않습니다.
      </p>
      {body}
      {kg ? <KgOverview kg={kg} graphAssetIds={graphAssetIds(graph)} /> : null}
    </section>
  );
}

/** 지금 그래프에 있는 자산 번호(그래프를 못 읽었거나 형식 오류면 빈 집합 — 그 KG 자산은 그래프에서 고를 수 없다) */
function graphAssetIds(graph: CgGraph | null): ReadonlySet<string> {
  if (!graph || graph.malformed) return new Set();
  return new Set(graph.nodes.flatMap((n) => (n.assetId ? [String(n.assetId)] : [])));
}

function failText(error: unknown): string {
  const status = error instanceof ApiError ? error.status : 0;
  const text: Record<number, string> = {
    0: "자산 그래프를 읽지 못했습니다(연결 실패).",
    401: "자산 그래프를 읽지 못했습니다 · 다시 로그인해 주세요.",
    403: "이 점검의 자산 그래프를 볼 권한이 없거나 승인이 바뀌었습니다.",
    404: "이 점검을 찾지 못해 자산 그래프를 읽지 못했습니다.",
    409: "자산 그래프의 조회 문맥이 바뀌었습니다. 다시 읽어 주세요.",
  };
  return text[status] ?? `자산 그래프를 읽지 못했습니다 (HTTP ${status}).`;
}

type NodeStatus = "out" | "tested" | "untested";
const STATUS_TEXT: Record<NodeStatus, string> = { out: "범위 밖", tested: "검사함", untested: "미검사" };
function nodeStatus(n: CgNode): NodeStatus {
  if (!n.inScope) return "out";
  return n.tested ? "tested" : "untested";
}

function trunc(v: string): string {
  return v.length > LABEL_MAX ? `${v.slice(0, LABEL_MAX - 1)}…` : v;
}

/** 배치 좌표 전체가 VIEW 안에 들어오는 이동 · 배율. */
function fitView(points: Iterable<Point>): View {
  let minX = Number.POSITIVE_INFINITY;
  let minY = Number.POSITIVE_INFINITY;
  let maxX = Number.NEGATIVE_INFINITY;
  let maxY = Number.NEGATIVE_INFINITY;
  for (const p of points) {
    minX = Math.min(minX, p.x);
    minY = Math.min(minY, p.y);
    maxX = Math.max(maxX, p.x);
    maxY = Math.max(maxY, p.y);
  }
  if (!Number.isFinite(minX)) return { x: VIEW_W / 2, y: VIEW_H / 2, k: 1 };
  const w = Math.max(maxX - minX, 1);
  const h = Math.max(maxY - minY, 1);
  const k = Math.min(
    ZOOM_MAX,
    Math.max(ZOOM_MIN, Math.min((VIEW_W - FIT_PAD * 2) / w, (VIEW_H - FIT_PAD * 2) / h, 1.4)),
  );
  return { x: VIEW_W / 2 - ((minX + maxX) / 2) * k, y: VIEW_H / 2 - ((minY + maxY) / 2) * k, k };
}

type Gesture =
  | { mode: "node"; key: string; startX: number; startY: number; moved: boolean }
  | { mode: "pan"; startX: number; startY: number; view: View; moved: boolean };

/**
 * 응답 상태(형식 오류 · 버린 수 · 빈 그래프)와 그림을 나눈다. 그림(Canvas)은 노드가 있을 때만 붙고 내용이 바뀌면 새로 붙는다 —
 * 빈 그래프 뒤 노드가 들어와도 휠 리스너 · 화면 맞춤이 새 svg 에 다시 잡힌다(#902 리뷰 이동찬 · 윤재상).
 */
function Graph({ graph, onReload, kg }: { graph: CgGraph; onReload: () => void; kg?: KgState }) {
  const reload = (
    <button className={cx(s.btn, s.sm)} type="button" onClick={onReload}>
      다시 읽기
    </button>
  );
  if (graph.malformed) {
    return (
      <div className={cx(s.note, s.warn)} role="alert">
        <Icon name="alert" />
        <span className={s["lv-grow"]}>자산 그래프 응답 형식을 확인하지 못해 표시하지 않습니다.</span>
        {reload}
      </div>
    );
  }
  const shape = `${graph.nodes.length}|${graph.edges.length}|${graph.nodes.at(0)?.key ?? ""}|${graph.nodes.at(-1)?.key ?? ""}`;
  return (
    <>
      {graph.dropped.nodes + graph.dropped.edges ? (
        <p className={cx(s.note, s.warn)} role="note">
          <Icon name="alert" />
          <span>{`응답 형식이 맞지 않는 노드 ${graph.dropped.nodes} · 간선 ${graph.dropped.edges}건은 그리지 않았습니다`}</span>
        </p>
      ) : null}
      {graph.nodes.length === 0 ? (
        <div className={s.note} role="note">
          <Icon name="info" />
          <span className={s["lv-grow"]}>
            탐색이 관찰해 저장한 자산이 아직 없습니다. 취약점이 없다는 뜻이 아니라 그릴 관찰 기록이 없다는 뜻입니다.
          </span>
          {reload}
        </div>
      ) : (
        <Canvas key={shape} graph={graph} onReload={onReload} kg={kg} />
      )}
    </>
  );
}

function Canvas({ graph, onReload, kg }: { graph: CgGraph; onReload: () => void; kg?: KgState }) {
  const [expanded, setExpanded] = useState<ReadonlyMap<string, number>>(() => new Map());
  const [picked, setPicked] = useState<string | null>(null);
  const [hover, setHover] = useState<string | null>(null);
  const [moved, setMoved] = useState<ReadonlyMap<string, Point>>(() => new Map());
  const vis = useMemo(() => visibleCoverageGraph(graph, expanded), [graph, expanded]);
  // 펼칠 때는 앞 배치(끌어 옮긴 자리 포함)를 이어받아 보이던 노드가 튀지 않게 한다
  const prev = useRef<ReadonlyMap<string, Point> | null>(null);
  const movedRef = useRef(moved);
  movedRef.current = moved;
  const layout = useMemo(() => {
    const seed = prev.current ? new Map([...prev.current, ...movedRef.current]) : undefined;
    return layoutCoverageGraph(vis, seed);
  }, [vis]);
  useEffect(() => {
    prev.current = layout;
  }, [layout]);
  const [view, setView] = useState<View>(() => fitView(layout.values()));
  const svgRef = useRef<SVGSVGElement>(null);
  const gesture = useRef<Gesture | null>(null);
  const stats = coverageGraphStats(graph);

  const at = useCallback((key: string): Point => moved.get(key) ?? layout.get(key) ?? { x: 0, y: 0 }, [moved, layout]);
  const endpointCount = vis.nodes.filter((n) => !n.fold && n.node.kind === "endpoint").length;
  const showEndpointLabels = view.k >= ENDPOINT_LABEL_ZOOM || endpointCount <= ENDPOINT_LABEL_MAX;

  /** 화면 좌표 → SVG viewBox 좌표. 측정할 수 없으면(시험 환경) 비율 1로 본다. */
  const toSvg = useCallback((clientX: number, clientY: number): Point => {
    const el = svgRef.current;
    const ctm = el?.getScreenCTM?.();
    if (!el || !ctm) return { x: clientX, y: clientY };
    const p = new DOMPoint(clientX, clientY).matrixTransform(ctm.inverse());
    return { x: p.x, y: p.y };
  }, []);

  // Ctrl · ⌘ + 휠 확대는 기본 동작(브라우저 확대)을 막아야 해서 passive 가 아닌 리스너로 단다(React onWheel 은 passive).
  // Canvas 는 svg 와 함께 붙고 떨어지므로 mount 때 한 번 달면 된다
  useEffect(() => {
    const el = svgRef.current;
    if (!el) return;
    const onWheel = (ev: WheelEvent) => {
      // 그냥 휠은 페이지 스크롤로 둔다 — 그래프 위를 지나가며 내릴 때 확대로 가로채지 않게(#902 리뷰 윤재상)
      if (!ev.ctrlKey && !ev.metaKey) return;
      ev.preventDefault();
      const p = toSvg(ev.clientX, ev.clientY);
      setView((v) => {
        const k = Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, v.k * Math.exp(-ev.deltaY * WHEEL_RATE)));
        return { k, x: p.x - ((p.x - v.x) * k) / v.k, y: p.y - ((p.y - v.y) * k) / v.k };
      });
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => el.removeEventListener("wheel", onWheel);
  }, [toSvg]);

  const activate = (n: VisNode) => {
    if (n.fold) {
      setExpanded((m) => new Map(m).set(n.groupId, (m.get(n.groupId) ?? FOLD_LIMIT) + FOLD_STEP));
      return;
    }
    setPicked((k) => (k === n.key ? null : n.key));
  };

  const onNodeDown = (key: string) => (ev: ReactPointerEvent) => {
    ev.stopPropagation();
    svgRef.current?.setPointerCapture?.(ev.pointerId);
    gesture.current = { mode: "node", key, startX: ev.clientX, startY: ev.clientY, moved: false };
  };
  const onDown = (ev: ReactPointerEvent) => {
    svgRef.current?.setPointerCapture?.(ev.pointerId);
    gesture.current = { mode: "pan", startX: ev.clientX, startY: ev.clientY, view, moved: false };
  };
  const onMove = (ev: ReactPointerEvent) => {
    const g = gesture.current;
    if (!g) return;
    if (!g.moved && Math.hypot(ev.clientX - g.startX, ev.clientY - g.startY) < DRAG_SLOP) return;
    g.moved = true;
    if (g.mode === "node") {
      const p = toSvg(ev.clientX, ev.clientY);
      setMoved((m) => new Map(m).set(g.key, { x: (p.x - view.x) / view.k, y: (p.y - view.y) / view.k }));
    } else {
      const a = toSvg(g.startX, g.startY);
      const b = toSvg(ev.clientX, ev.clientY);
      setView({ ...g.view, x: g.view.x + b.x - a.x, y: g.view.y + b.y - a.y });
    }
  };
  const onUp = () => {
    const g = gesture.current;
    gesture.current = null;
    if (!g || g.moved) return;
    if (g.mode === "pan") setPicked(null);
    else {
      const n = vis.nodes.find((x) => x.key === g.key);
      if (n) activate(n);
    }
  };
  const onKey = (n: VisNode) => (ev: KeyboardEvent) => {
    if (ev.key === "Enter" || ev.key === " ") {
      ev.preventDefault();
      activate(n);
    }
  };

  const chosen = picked ? graph.nodes.find((n) => n.key === picked) : undefined;
  const zoomBy = (factor: number) =>
    setView((v) => {
      const k = Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, v.k * factor));
      const cx0 = VIEW_W / 2;
      const cy0 = VIEW_H / 2;
      return { k, x: cx0 - ((cx0 - v.x) * k) / v.k, y: cy0 - ((cy0 - v.y) * k) / v.k };
    });

  return (
    <>
      <div style={{ position: "relative", border: "1px solid var(--rule)", borderRadius: 10, background: "var(--bg)" }}>
        <Legend
          stats={stats}
          onReload={onReload}
          onFit={() => setView(fitView(vis.nodes.map((n) => at(n.key))))}
          onZoom={zoomBy}
        />
        {/* biome-ignore lint/a11y/useSemanticElements: SVG 그림이라 fieldset 을 둘 수 없다 — 노드(버튼)를 묶는 group 이다 */}
        <svg
          ref={svgRef}
          viewBox={`0 0 ${VIEW_W} ${VIEW_H}`}
          width="100%"
          style={{ display: "block", height: VIEW_H, touchAction: "none", cursor: "grab", userSelect: "none" }}
          role="group"
          aria-label="자산 관계 그래프"
          data-testid="coverage-graph"
          onPointerDown={onDown}
          onPointerMove={onMove}
          onPointerUp={onUp}
          onPointerCancel={() => {
            gesture.current = null;
          }}
        >
          <g transform={`translate(${view.x} ${view.y}) scale(${view.k})`}>
            {vis.edges.map((e) => {
              const a = at(e.src);
              const b = at(e.dst);
              return (
                <line
                  key={`${e.src}>${e.dst}`}
                  x1={a.x}
                  y1={a.y}
                  x2={b.x}
                  y2={b.y}
                  style={{ stroke: "var(--rule-2)" }}
                  strokeWidth={1.2 / view.k}
                />
              );
            })}
            {vis.nodes.map((n) => (
              <Node
                key={n.key}
                n={n}
                p={at(n.key)}
                k={view.k}
                selected={n.key === picked}
                labelled={
                  n.fold || n.node.kind !== "endpoint" || showEndpointLabels || n.key === hover || n.key === picked
                }
                onDown={onNodeDown(n.key)}
                onKey={onKey(n)}
                onHover={(on) => setHover(on ? n.key : null)}
              />
            ))}
          </g>
        </svg>
      </div>
      <Detail graph={graph} node={chosen ?? null} kg={kg} />
    </>
  );
}

function Node({
  n,
  p,
  k,
  selected,
  labelled,
  onDown,
  onKey,
  onHover,
}: {
  n: VisNode;
  p: Point;
  k: number;
  selected: boolean;
  labelled: boolean;
  onDown: (ev: ReactPointerEvent) => void;
  onKey: (ev: KeyboardEvent) => void;
  onHover: (on: boolean) => void;
}) {
  // 확대해도 글 · 테두리가 같은 크기로 보이게 배율로 나눈다(노드 원만 함께 커진다)
  const fs = 11 / Math.max(k, ZOOM_MIN);
  if (n.fold) {
    const label = `${COVERAGE_KIND_TEXT[n.kind]} ${n.hidden}개 더`;
    return (
      // biome-ignore lint/a11y/useSemanticElements: SVG 안이라 button 요소를 둘 수 없다
      <g
        role="button"
        tabIndex={0}
        aria-label={`${label} · 누르면 ${Math.min(FOLD_STEP, n.hidden)}개 펼침`}
        data-fold={n.kind}
        transform={`translate(${p.x} ${p.y})`}
        style={{ cursor: "pointer" }}
        onPointerDown={onDown}
        onKeyDown={onKey}
      >
        <circle r={FOLD_RADIUS} style={{ fill: "var(--well)", stroke: "var(--ink-3)" }} strokeDasharray="3 2" />
        <text textAnchor="middle" dy="0.35em" style={{ fill: "var(--ink-2)", fontSize: 11 }}>
          …
        </text>
        <text y={FOLD_RADIUS + fs * 1.3} textAnchor="middle" style={{ fill: "var(--ink-3)", fontSize: fs }}>
          {label}
        </text>
      </g>
    );
  }
  const node = n.node;
  const r = KIND_RADIUS[node.kind];
  const name = coverageNodeLabel(node);
  const title = coverageNodeTitle(node);
  const status = nodeStatus(node);
  const state = STATUS_TEXT[status];
  return (
    // biome-ignore lint/a11y/useSemanticElements: SVG 안이라 button 요소를 둘 수 없다
    <g
      role="button"
      tabIndex={0}
      aria-label={`${title} · ${state}`}
      aria-pressed={selected}
      data-node={node.kind}
      data-state={status}
      transform={`translate(${p.x} ${p.y})`}
      style={{ cursor: "pointer" }}
      onPointerDown={onDown}
      onKeyDown={onKey}
      onPointerEnter={() => onHover(true)}
      onPointerLeave={() => onHover(false)}
    >
      <title>{`${title} · ${state}`}</title>
      {node.tested && node.inScope ? (
        <circle r={r + 3.5} style={{ fill: "none", stroke: "var(--accent)" }} strokeWidth={2.5} />
      ) : null}
      {node.inScope ? (
        <circle r={r} style={{ fill: KIND_COLOR[node.kind], opacity: node.tested ? 1 : 0.45 }} />
      ) : (
        <circle r={r} style={{ fill: "var(--well)", stroke: "var(--ink-3)" }} strokeDasharray="3 2" />
      )}
      {selected ? <circle r={r + 7} style={{ fill: "none", stroke: "var(--ink)" }} strokeWidth={1.5 / k} /> : null}
      {labelled ? (
        <text y={r + fs * 1.3} textAnchor="middle" style={{ fill: "var(--ink-2)", fontSize: fs }}>
          {trunc(name)}
        </text>
      ) : null}
    </g>
  );
}

function Legend({
  stats,
  onReload,
  onFit,
  onZoom,
}: {
  stats: { inScope: number; tested: number };
  onReload: () => void;
  onFit: () => void;
  onZoom: (factor: number) => void;
}) {
  const dot = (style: CSSProperties) => (
    <span style={{ display: "inline-block", width: 10, height: 10, borderRadius: 999, ...style }} />
  );
  return (
    <div
      style={{
        position: "absolute",
        top: 10,
        left: 10,
        maxWidth: 320,
        padding: "10px 12px",
        border: "1px solid var(--rule)",
        borderRadius: 8,
        background: "var(--bg)",
        boxShadow: "var(--shadow)",
        fontSize: 12,
        display: "flex",
        flexDirection: "column",
        gap: 8,
      }}
      data-testid="coverage-graph-legend"
    >
      <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
        <span className={s["lv-grow"]} data-testid="coverage-graph-stats">
          범위 안 <b>{stats.inScope}</b> · 검사함 <b>{stats.tested}</b>
        </span>
        <button className={cx(s.btn, s.sm)} type="button" onClick={() => onZoom(ZOOM_STEP)} aria-label="확대">
          +
        </button>
        <button className={cx(s.btn, s.sm)} type="button" onClick={() => onZoom(1 / ZOOM_STEP)} aria-label="축소">
          −
        </button>
        <button className={cx(s.btn, s.sm)} type="button" onClick={onFit}>
          맞추기
        </button>
        <button className={cx(s.btn, s.sm)} type="button" onClick={onReload}>
          다시 읽기
        </button>
      </div>
      <div style={{ display: "flex", flexWrap: "wrap", gap: "4px 10px" }}>
        {COVERAGE_KINDS.map((k) => (
          <span key={k} style={{ display: "inline-flex", alignItems: "center", gap: 4 }} data-legend={k}>
            {dot({ background: KIND_COLOR[k] })}
            {COVERAGE_KIND_TEXT[k]}
          </span>
        ))}
      </div>
      <div style={{ display: "flex", flexWrap: "wrap", gap: "4px 10px", color: "var(--ink-2)" }}>
        <span style={{ display: "inline-flex", alignItems: "center", gap: 4 }}>
          {dot({ background: "var(--ink-3)", boxShadow: "0 0 0 2px var(--bg), 0 0 0 4px var(--accent)" })}
          검사함(테두리)
        </span>
        <span style={{ display: "inline-flex", alignItems: "center", gap: 4 }}>
          {dot({ background: "var(--ink-3)", opacity: 0.45 })}
          미검사(옅게)
        </span>
        <span style={{ display: "inline-flex", alignItems: "center", gap: 4 }}>
          {dot({ background: "var(--well)", border: "1px dashed var(--ink-3)" })}
          범위 밖
        </span>
      </div>
      <p className={s.help} style={{ margin: 0 }}>
        노드를 끌어 옮기고, 빈 곳을 끌면 화면이 움직입니다. 확대 · 축소는 +/− 버튼이나 Ctrl(⌘)+휠입니다. 점선 '…'는 접힌
        노드이며 누르면 {FOLD_STEP}개씩 더 펼칩니다.
      </p>
    </div>
  );
}

function Detail({ graph, node, kg }: { graph: CgGraph; node: CgNode | null; kg?: KgState }) {
  if (!node) {
    return (
      <p className={s.help} data-testid="coverage-graph-detail">
        {kg
          ? "노드를 고르면 종류 · 범위 · 검사 여부와 연결, 포함 근거 · 출처를 봅니다."
          : "노드를 고르면 종류 · 범위 · 검사 여부와 연결을 봅니다."}
      </p>
    );
  }
  const parentKey = graph.edges.find((e) => e.src === node.key)?.dst;
  const parent = parentKey ? graph.nodes.find((n) => n.key === parentKey) : undefined;
  const children = graph.edges.filter((e) => e.dst === node.key).length;
  const pathLine = coveragePathDetailLine(node);
  return (
    <section className={s.isec} aria-label="고른 자산" data-testid="coverage-graph-detail">
      <h3>{coverageNodeTitle(node)}</h3>
      <ul className={s.tml}>
        <li>{node.inScope ? "점검 범위 안" : "범위 밖(연결용)"}</li>
        <li>{node.tested ? "검사함 — 탐색 · 점검이 다룬 자산입니다(취약점이 없다는 뜻은 아닙니다)" : "미검사"}</li>
        {node.assetId ? <li>{`자산 #${node.assetId}`}</li> : null}
        {node.origin ? <li>{`origin ${node.origin}`}</li> : null}
        {pathLine ? <li>{pathLine}</li> : null}
        {node.port ? <li>{`포트 ${node.port}`}</li> : null}
        {node.statusCode ? <li>{`관찰한 상태 코드 ${node.statusCode}`}</li> : null}
        <li>{parent ? `속한 자산: ${coverageNodeTitle(parent)}` : "최상위 자산"}</li>
        <li>{`직접 포함한 자산 ${children}`}</li>
      </ul>
      {kg ? <KgAssetDetail kg={kg} assetId={node.assetId} /> : null}
    </section>
  );
}
