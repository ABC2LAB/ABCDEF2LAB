// 자산 지식 그래프 (보고서 '자산 지식 그래프' 탭): 탐색이 실제로 관찰한 자산과 포함 관계(GET /api/tasks/{id}/coverage-graph).
// 회사 → 루트 도메인 → 서브도메인 · IP → 서비스 → 앱 · 엔드포인트. 서버 간선(child → parent)만 그리고, 관찰하지 않은 관계 ·
// 내부 구조는 추정해서 더하지 않는다. 화면 seam 은 이 파일의 순수 함수(검증 · 접힘 · 배치 · 라벨)뿐이라 레이아웃이 바뀌어도 그대로 둔다.
// 응답은 데이터다(절대 규칙 4): 모르는 종류 · 중복 키 · 없는 노드를 가리키는 간선은 버리고 버린 수를 화면에 알린다.

import type { CoverageGraph } from "./api";

export const COVERAGE_KINDS = ["company", "root_domain", "subdomain", "ip", "service", "app", "endpoint"] as const;
export type CoverageKind = (typeof COVERAGE_KINDS)[number];

export const COVERAGE_KIND_TEXT: Record<CoverageKind, string> = {
  company: "회사",
  root_domain: "루트 도메인",
  subdomain: "서브도메인",
  ip: "IP",
  service: "서비스",
  app: "앱",
  endpoint: "엔드포인트",
};

export type CgNode = {
  key: string;
  kind: CoverageKind;
  label: string;
  tested: boolean;
  inScope: boolean;
  assetId: number | null;
  port: number | null;
  statusCode: number | null;
  /** 서버가 준 origin(공개 처리 뒤 scheme://host[:port]). 검증을 못 넘으면 null */
  origin: string | null;
  /** 엔드포인트만: 가린 경로 구조(`/` 또는 `/{id}` 1~32개 + 끝 슬래시). 서버가 안 주거나 모양이 다르면 null */
  displayPath: string | null;
  /**
   * 엔드포인트만: 표시 경로의 상세 수준(coverage-graph-display-path.md). `exact` = 루트 `/` 그대로, `pattern_only` = 조각을
   * 가린 구조, `unavailable` = 표시할 경로가 기록되지 않음. 이전 서버(칸 없음) · 모르는 값 · 표시 경로와 어긋난 값이면 null
   */
  pathDetail: CgPathDetail | null;
};
export const CG_PATH_DETAILS = ["exact", "pattern_only", "unavailable"] as const;
export type CgPathDetail = (typeof CG_PATH_DETAILS)[number];
export type CgEdge = { src: string; dst: string };
export type CgGraph = {
  nodes: CgNode[];
  /** child → parent. 노드마다 부모 하나(첫 간선)만 둔다 — 포함 트리라 두 번째 부모는 그리지 않고 dropped 로 센다 */
  edges: CgEdge[];
  dropped: { nodes: number; edges: number };
  /** nodes · edges 가 배열이 아니다(응답 형식 오류) — 빈 그래프로 보이지 않고 형식 오류로 알린다 */
  malformed: boolean;
};

const KIND_SET: ReadonlySet<string> = new Set(COVERAGE_KINDS);
const isKind = (v: unknown): v is CoverageKind => typeof v === "string" && KIND_SET.has(v);
const posInt = (v: unknown): number | null => (typeof v === "number" && Number.isInteger(v) && v > 0 ? v : null);

/** 서버 응답 → 그릴 수 있는 그래프. 어긋난 노드 · 간선은 버리고 센다(빈 그래프로 대신하지 않음). */
export function parseCoverageGraph(raw: CoverageGraph): CgGraph {
  const nodes: CgNode[] = [];
  const seen = new Set<string>();
  let droppedNodes = 0;
  const malformed = !Array.isArray(raw.nodes) || !Array.isArray(raw.edges);
  for (const n of Array.isArray(raw.nodes) ? raw.nodes : []) {
    const key = (n as { key?: unknown } | null)?.key;
    if (!n || typeof key !== "string" || !key || seen.has(key) || !isKind(n.kind)) {
      droppedNodes++;
      continue;
    }
    seen.add(key);
    const displayPath = n.kind === "endpoint" ? displayPathOf(n.display_path) : null;
    nodes.push({
      key,
      kind: n.kind,
      label: typeof n.label === "string" ? n.label : "",
      tested: n.tested === true,
      inScope: n.in_scope === true,
      assetId: posInt(n.asset_id),
      port: posInt(n.port),
      statusCode: posInt((n as { status_code?: unknown }).status_code),
      origin: originOf(n.url),
      displayPath,
      pathDetail: n.kind === "endpoint" ? pathDetailOf(n.path_detail_state, displayPath) : null,
    });
  }
  const edges: CgEdge[] = [];
  const parent = new Map<string, string>();
  let droppedEdges = 0;
  for (const e of Array.isArray(raw.edges) ? raw.edges : []) {
    const src = (e as Partial<CgEdge> | null)?.src;
    const dst = (e as Partial<CgEdge> | null)?.dst;
    if (typeof src !== "string" || typeof dst !== "string" || src === dst || !seen.has(src) || !seen.has(dst)) {
      droppedEdges++;
      continue;
    }
    if (parent.has(src) || createsCycle(parent, src, dst)) {
      droppedEdges++;
      continue;
    }
    parent.set(src, dst);
    edges.push({ src, dst });
  }
  return { nodes, edges, dropped: { nodes: droppedNodes, edges: droppedEdges }, malformed };
}

/** 표시 경로 계약(coverage-graph-display-path.md): 루트 `/` 또는 1~32개의 `/{id}`와 선택적 끝 슬래시. 그 밖은 버린다. */
const DISPLAY_PATH = /^(?:\/|(?:\/\{id\}){1,32}\/?)$/;
function displayPathOf(v: unknown): string | null {
  return typeof v === "string" && DISPLAY_PATH.test(v) ? v : null;
}

/**
 * 상세 수준은 서버 계산(server/report_graph_display_path.go)과 같은 짝일 때만 받는다: `exact` ⇔ 표시 경로 `/`,
 * `pattern_only` ⇔ `/` 아닌 표시 경로, `unavailable` ⇔ 표시 경로 없음. 서버가 만들 수 없는 짝이면 상세 수준을 모르는 것으로
 * 둔다(null) — 표시 경로 자체는 위 검증대로 따로 보인다.
 */
function pathDetailOf(v: unknown, displayPath: string | null): CgPathDetail | null {
  if (v === "exact") return displayPath === "/" ? v : null;
  if (v === "pattern_only") return displayPath && displayPath !== "/" ? v : null;
  if (v === "unavailable") return displayPath === null ? v : null;
  return null;
}

/** origin 만 받는다: http(s), 자격 · 경로 · query · fragment 없음. 서버 공개 처리와 같은 모양이 아니면 보이지 않는다. */
function originOf(v: unknown): string | null {
  if (typeof v !== "string" || !v) return null;
  let u: URL;
  try {
    u = new URL(v);
  } catch {
    return null;
  }
  const plain = !u.username && !u.password && !u.search && !u.hash && u.pathname === "/";
  return (u.protocol === "http:" || u.protocol === "https:") && plain ? u.origin : null;
}

function createsCycle(parent: ReadonlyMap<string, string>, child: string, to: string): boolean {
  for (let at: string | undefined = to; at !== undefined; at = parent.get(at)) if (at === child) return true;
  return false;
}

/** 왼쪽 위 요약: 범위 안 노드 수 · 그중 검사한 수. 범위 밖(연결용 회색) 노드는 세지 않는다. */
export function coverageGraphStats(g: CgGraph): { inScope: number; tested: number } {
  const inScope = g.nodes.filter((n) => n.inScope);
  return { inScope: inScope.length, tested: inScope.filter((n) => n.tested).length };
}

/**
 * 노드 이름. 서비스는 포트 · 상태 코드다. 엔드포인트는 가린 표시 경로 + 자산 번호다 — 조각이 모두 `{id}`로 가려져
 * 같은 모양이 여럿일 수 있어(#904) 번호로 가른다. 표시 경로가 없으면(이전 서버 · 생략) 자산 번호만이다. 서버 공개 처리가
 * 엔드포인트 라벨을 origin 으로 덮어 서로 같기 때문에 라벨은 쓰지 않는다.
 */
export function coverageNodeLabel(n: CgNode): string {
  if (n.kind === "service") {
    const parts = [n.port ? `:${n.port}` : "", n.statusCode ? String(n.statusCode) : ""].filter(Boolean);
    return parts.length ? parts.join(" · ") : n.label || assetName(n);
  }
  if (n.kind === "endpoint") {
    if (!n.displayPath) return assetName(n);
    return n.assetId ? `${n.displayPath} · #${n.assetId}` : n.displayPath;
  }
  // 공개할 수 없는 루트 도메인은 서버가 key 를 해시로 바꾸고 라벨을 '자산 #0'으로 둔다 — 번호처럼 보이지 않게
  if (!n.assetId && HIDDEN_LABEL.test(n.label)) return `${COVERAGE_KIND_TEXT[n.kind]}(이름 비공개)`;
  return n.label || assetName(n);
}

const HIDDEN_LABEL = /^자산 #0$/;

/**
 * 노드 상세의 경로 줄. 표시 경로는 저장된 URL의 구조일 뿐이라 요청 · 진단 근거가 아니라고 함께 적는다. 상세 수준을 모르면
 * (이전 서버) 표시 경로가 있을 때만 예전 문구를 쓰고, 없으면 줄을 두지 않는다 — 기록이 없다고 단정하지 않는다.
 */
export function coveragePathDetailLine(n: CgNode): string | null {
  if (n.kind !== "endpoint") return null;
  if (n.pathDetail === "unavailable") return "상세 경로 미기록 — 이 자산은 표시할 경로가 기록되지 않았습니다";
  if (!n.displayPath) return null;
  if (n.pathDetail === "exact") return `표시 경로 ${n.displayPath} — 루트 경로 그대로입니다(요청 · 진단 근거 아님)`;
  return `표시 경로 ${n.displayPath} — 경로 조각을 가린 구조입니다(요청 · 진단 근거 아님)`;
}

/** 종류 이름을 붙인 이름(읽기 · 상세용). 이름이 이미 종류로 시작하면('엔드포인트 #100') 두 번 붙이지 않는다. */
export function coverageNodeTitle(n: CgNode): string {
  const kind = COVERAGE_KIND_TEXT[n.kind];
  const name = coverageNodeLabel(n);
  return name.startsWith(kind) ? name : `${kind} ${name}`;
}

function assetName(n: CgNode): string {
  return n.assetId ? `${COVERAGE_KIND_TEXT[n.kind]} #${n.assetId}` : COVERAGE_KIND_TEXT[n.kind];
}

// ---------- 접힘: 부모마다 같은 종류 자식이 많으면 앞 FOLD_LIMIT 개만, 나머지는 '…' 노드 하나로 ----------

export const FOLD_LIMIT = 20;
export const FOLD_STEP = 20;

export type VisAsset = { fold: false; key: string; node: CgNode };
export type VisFold = {
  fold: true;
  key: string;
  groupId: string;
  /** null = 최상위(부모 없는) 묶음 — 간선 없이 둔다 */
  parentKey: string | null;
  kind: CoverageKind;
  hidden: number;
};
export type VisNode = VisAsset | VisFold;
/** 최상위 접힘 묶음 id 앞머리. 노드 key 는 서버가 'a:' · 'c:' · 'r:' 로 시작해 겹치지 않는다 */
const ROOT_GROUP = "root";
export type VisGraph = { nodes: VisNode[]; edges: CgEdge[] };

/** 검사한 것 먼저, 그다음 이름 순 — 접힐 때 검사한 자산이 보이는 쪽에 남는다. */
function childOrder(a: CgNode, b: CgNode): number {
  if (a.tested !== b.tested) return a.tested ? -1 : 1;
  return coverageNodeLabel(a).localeCompare(coverageNodeLabel(b)) || a.key.localeCompare(b.key);
}

/** expanded: 접힘 묶음(groupId)마다 지금 보이는 수. 없으면 FOLD_LIMIT. 접힌 자식의 자손도 보이지 않는다. */
export function visibleCoverageGraph(g: CgGraph, expanded: ReadonlyMap<string, number>): VisGraph {
  const byKey = new Map(g.nodes.map((n) => [n.key, n]));
  const children = new Map<string, CgNode[]>();
  const hasParent = new Set<string>();
  for (const e of g.edges) {
    hasParent.add(e.src);
    const child = byKey.get(e.src);
    if (!child) continue;
    const list = children.get(e.dst);
    if (list) list.push(child);
    else children.set(e.dst, [child]);
  }
  const nodes: VisNode[] = [];
  const edges: CgEdge[] = [];
  // 부모 없는 노드(서버 anchor 로 바로 들어온 엔드포인트 등)도 종류별 FOLD_LIMIT 개로 접는다(#902 리뷰)
  const queue: CgNode[] = [];
  const roots = new Map<CoverageKind, CgNode[]>();
  for (const n of g.nodes) {
    if (hasParent.has(n.key)) continue;
    const list = roots.get(n.kind);
    if (list) list.push(n);
    else roots.set(n.kind, [n]);
  }
  for (const kind of COVERAGE_KINDS) {
    const list = roots.get(kind);
    if (!list) continue;
    list.sort(childOrder);
    const groupId = `${ROOT_GROUP}::${kind}`;
    const shown = expanded.get(groupId) ?? FOLD_LIMIT;
    for (const n of list.slice(0, shown)) {
      nodes.push({ fold: false, key: n.key, node: n });
      queue.push(n);
    }
    const hidden = list.length - Math.min(shown, list.length);
    if (hidden > 0) nodes.push({ fold: true, key: `fold:${groupId}`, groupId, parentKey: null, kind, hidden });
  }
  for (let i = 0; i < queue.length; i++) {
    const parent = queue[i];
    const groups = new Map<CoverageKind, CgNode[]>();
    for (const c of children.get(parent.key) ?? []) {
      const list = groups.get(c.kind);
      if (list) list.push(c);
      else groups.set(c.kind, [c]);
    }
    for (const kind of COVERAGE_KINDS) {
      const list = groups.get(kind);
      if (!list) continue;
      list.sort(childOrder);
      const groupId = `${parent.key}::${kind}`;
      const shown = expanded.get(groupId) ?? FOLD_LIMIT;
      for (const c of list.slice(0, shown)) {
        nodes.push({ fold: false, key: c.key, node: c });
        edges.push({ src: c.key, dst: parent.key });
        queue.push(c);
      }
      const hidden = list.length - Math.min(shown, list.length);
      if (hidden > 0) {
        const key = `fold:${groupId}`;
        nodes.push({ fold: true, key, groupId, parentKey: parent.key, kind, hidden });
        edges.push({ src: key, dst: parent.key });
      }
    }
  }
  return { nodes, edges };
}

// ---------- 배치: 방사형 트리로 시작해 짧게 힘 배치(결정적 — 같은 응답은 같은 그림) ----------

export type Point = { x: number; y: number };

/** 깊이 하나의 반지름(px). 첫 배치의 고리 간격이다. */
const RING = 150;
/** 간선 쉼 길이(px): 잎(엔드포인트 · 앱 · 접힘)은 부모 가까이, 나머지는 한 고리만큼. */
const REST_LEAF = 70;
const REST_INNER = 120;
/** 밀어내기 거리 상한(px). 이보다 먼 쌍은 보지 않아 격자 칸 이웃만 살핀다 — 노드가 많아도 거의 선형. */
const CUTOFF = 140;
const REPULSE = 2600;
const SPRING = 0.06;
const GRAVITY = 0.004;
/** 앞 배치 자리로 당기는 힘. 밀어내기보다 커야 보이던 노드가 제자리를 지킨다. */
const ANCHOR = 0.2;
/** 전체 계산량 상한: 반복 수 × 노드 수 ≈ 이 값. 노드가 많으면 반복을 줄여 화면이 멈추지 않게 한다. */
const WORK_BUDGET = 40000;
const MIN_ITER = 40;
const MAX_ITER = 220;

const isLeafKind = (n: VisNode) => n.fold || n.node.kind === "endpoint" || n.node.kind === "app";

/** 노드 key → 좌표(원점 중심). 같은 VisGraph 면 늘 같은 결과다(난수 없음). */
export function layoutCoverageGraph(vis: VisGraph, seed?: ReadonlyMap<string, Point>): Map<string, Point> {
  const pos = seed?.size ? seededStart(vis, seed) : radialStart(vis);
  const keys = vis.nodes.map((n) => n.key);
  const n = keys.length;
  if (n < 2) return pos;
  const index = new Map(keys.map((k, i) => [k, i]));
  const xs = new Float64Array(n);
  const ys = new Float64Array(n);
  keys.forEach((k, i) => {
    const p = pos.get(k) as Point;
    xs[i] = p.x;
    ys[i] = p.y;
  });
  const leaf = vis.nodes.map(isLeafKind);
  // 이미 그려 둔 노드는 제자리로 당긴다 — 펼치기 · 다시 배치 때 보이던 노드가 튀지 않게(#902 리뷰)
  const anchors = keys.map((k) => seed?.get(k) ?? null);
  const springs = vis.edges.map((e) => {
    const a = index.get(e.src) as number;
    return [a, index.get(e.dst) as number, leaf[a] ? REST_LEAF : REST_INNER] as const;
  });
  const iterations = Math.max(MIN_ITER, Math.min(MAX_ITER, Math.round(WORK_BUDGET / n)));
  const fx = new Float64Array(n);
  const fy = new Float64Array(n);
  for (let it = 0; it < iterations; it++) {
    const temp = 1 - it / iterations; // 식힘: 처음엔 크게, 끝엔 작게 움직인다
    fx.fill(0);
    fy.fill(0);
    // 밀어내기: CUTOFF 칸 격자에 넣고 이웃 9칸만 본다
    const grid = new Map<string, number[]>();
    for (let i = 0; i < n; i++) {
      const cell = `${Math.floor(xs[i] / CUTOFF)},${Math.floor(ys[i] / CUTOFF)}`;
      const list = grid.get(cell);
      if (list) list.push(i);
      else grid.set(cell, [i]);
    }
    for (let i = 0; i < n; i++) {
      const cx = Math.floor(xs[i] / CUTOFF);
      const cy = Math.floor(ys[i] / CUTOFF);
      for (let dx = -1; dx <= 1; dx++) {
        for (let dy = -1; dy <= 1; dy++) {
          for (const j of grid.get(`${cx + dx},${cy + dy}`) ?? []) {
            if (j <= i) continue;
            let ddx = xs[i] - xs[j];
            let ddy = ys[i] - ys[j];
            let d2 = ddx * ddx + ddy * ddy;
            if (d2 > CUTOFF * CUTOFF) continue;
            if (d2 < 1) {
              // 같은 자리: 번호로 정한 방향으로 떼어 낸다(결정적)
              ddx = ((i - j) % 3) + 0.5;
              ddy = ((i + j) % 3) - 1;
              d2 = ddx * ddx + ddy * ddy;
            }
            const d = Math.sqrt(d2);
            const f = REPULSE / d2;
            fx[i] += (ddx / d) * f;
            fy[i] += (ddy / d) * f;
            fx[j] -= (ddx / d) * f;
            fy[j] -= (ddy / d) * f;
          }
        }
      }
    }
    for (const [a, b, rest] of springs) {
      const ddx = xs[b] - xs[a];
      const ddy = ys[b] - ys[a];
      const d = Math.sqrt(ddx * ddx + ddy * ddy) || 1;
      const f = SPRING * (d - rest);
      fx[a] += (ddx / d) * f;
      fy[a] += (ddy / d) * f;
      fx[b] -= (ddx / d) * f;
      fy[b] -= (ddy / d) * f;
    }
    const maxStep = 30 * temp + 1;
    for (let i = 0; i < n; i++) {
      const anchor = anchors[i];
      if (anchor) {
        fx[i] += (anchor.x - xs[i]) * ANCHOR;
        fy[i] += (anchor.y - ys[i]) * ANCHOR;
      } else {
        fx[i] -= xs[i] * GRAVITY;
        fy[i] -= ys[i] * GRAVITY;
      }
      const m = Math.sqrt(fx[i] * fx[i] + fy[i] * fy[i]);
      const s = m > maxStep ? maxStep / m : 1;
      xs[i] += fx[i] * s;
      ys[i] += fy[i] * s;
    }
  }
  const out = new Map<string, Point>();
  keys.forEach((k, i) => {
    out.set(k, { x: xs[i], y: ys[i] });
  });
  return out;
}

/**
 * 이어 배치: 앞 배치에 있던 노드는 그 자리에서, 새 노드는 부모 둘레(번호로 정한 각도, 결정적)에서 시작한다.
 * 부모도 새 노드면 부모가 먼저 놓인 뒤 놓이도록 간선 순서(부모 → 자식)대로 채운다. 부모 없는 새 노드는 방사형 자리다.
 */
function seededStart(vis: VisGraph, seed: ReadonlyMap<string, Point>): Map<string, Point> {
  const radial = radialStart(vis);
  const parent = new Map(vis.edges.map((e) => [e.src, e.dst]));
  const pos = new Map<string, Point>();
  const place = (key: string, depth: number): Point => {
    const done = pos.get(key);
    if (done) return done;
    const known = seed.get(key);
    const up = parent.get(key);
    let p: Point;
    if (known) p = known;
    else if (up && depth < COVERAGE_KINDS.length + 1) {
      const base = place(up, depth + 1);
      const angle = ((hashKey(key) % 360) * Math.PI) / 180;
      p = { x: base.x + REST_LEAF * Math.cos(angle), y: base.y + REST_LEAF * Math.sin(angle) };
    } else p = radial.get(key) ?? { x: 0, y: 0 };
    pos.set(key, p);
    return p;
  };
  for (const n of vis.nodes) place(n.key, 0);
  return pos;
}

/** 문자열 → 0 이상 정수(각도 고르기용, 결정적). */
function hashKey(key: string): number {
  let h = 0;
  for (let i = 0; i < key.length; i++) h = (h * 31 + key.charCodeAt(i)) >>> 0;
  return h;
}

/** 첫 배치: 뿌리들을 가운데, 자식은 잎 수에 비례한 각도 몫 안에 깊이별 고리로 둔다. */
function radialStart(vis: VisGraph): Map<string, Point> {
  const children = new Map<string, string[]>();
  const hasParent = new Set<string>();
  for (const e of vis.edges) {
    hasParent.add(e.src);
    const list = children.get(e.dst);
    if (list) list.push(e.src);
    else children.set(e.dst, [e.src]);
  }
  const leaves = new Map<string, number>();
  const count = (k: string): number => {
    const kids = children.get(k) ?? [];
    const v = kids.length ? kids.reduce((sum, c) => sum + count(c), 0) : 1;
    leaves.set(k, v);
    return v;
  };
  const roots = vis.nodes.filter((n) => !hasParent.has(n.key)).map((n) => n.key);
  const total = roots.reduce((sum, r) => sum + count(r), 0) || 1;
  const pos = new Map<string, Point>();
  const place = (k: string, depth: number, from: number, to: number) => {
    const mid = (from + to) / 2;
    const r = depth * RING;
    pos.set(k, { x: r * Math.cos(mid), y: r * Math.sin(mid) });
    let at = from;
    for (const c of children.get(k) ?? []) {
      const span = ((to - from) * (leaves.get(c) ?? 1)) / (leaves.get(k) ?? 1);
      place(c, depth + 1, at, at + span);
      at += span;
    }
  };
  // 뿌리가 여럿이면 각자 몫의 각도에서 첫 고리에 둔다(하나면 가운데)
  let at = 0;
  for (const r of roots) {
    const span = (2 * Math.PI * (leaves.get(r) ?? 1)) / total;
    place(r, roots.length > 1 ? 1 : 0, at, at + span);
    at += span;
  }
  return pos;
}
