/**
 * 보고서 자산 그래프: 엔진 coverage-graph(평평한 노드 목록) → 도메인 → 서비스 → 경로 묶음 → 엔드포인트.
 * 엔진에는 "경로 묶음" 단계가 없어서 경로 첫 마디(/api, /admin …)로 화면에서 나눈다.
 * 엔진 엔드포인트 노드에는 메서드가 없다(label = URL) — 같은 URL의 다른 메서드는 같은 경로로 두 줄 보인다.
 */
import type { EngineCoverageGraph, EngineCoverageNode, EngineFinding } from "./engine";

/** ok = 검사함, wait = 미검사, out = 범위 밖 */
export type AgStatus = "ok" | "wait" | "out";
export type AgEndpoint = { path: string; status: AgStatus; findings: EngineFinding[] };
export type AgGroup = { key: string; name: string; eps: AgEndpoint[] };
export type AgBlock = {
  /** host:port — 블록 식별자이자 서비스 노드 이름 */
  service: string;
  domain: string;
  /** 서비스 전체에 걸린 발견(응답 헤더 · 쿠키처럼 엔드포인트 하나에 묶이지 않는 것) */
  siteFindings: EngineFinding[];
  groups: AgGroup[];
};
export type AgStats = { inScope: number; tested: number; out: number };
export type AssetGraph = {
  blocks: AgBlock[];
  /** 그래프의 어느 노드에도 연결하지 못한 발견 */
  loose: EngineFinding[];
  stats: AgStats;
};

const API_SEG = new Set(["api", "rest", "graphql"]);
const GROUP_NAME: Record<string, string> = { "/": "페이지", api: "API", rest: "API", graphql: "API", admin: "관리자" };

type Loc = { service: string; domain: string; path: string };

/** URL → host:port + 경로(쿼리는 키만 남김: ?q=apple → ?q=) */
function locate(raw: string): Loc | null {
  let u: URL;
  try {
    u = new URL(raw);
  } catch {
    return null;
  }
  if (!u.hostname) return null;
  const port = u.port || (u.protocol === "https:" ? "443" : "80");
  const keys = [...new Set(u.searchParams.keys())];
  const query = keys.length ? `?${keys.map((k) => `${k}=`).join("&")}` : "";
  // URL 은 {id} 같은 템플릿 괄호를 %7B…%7D 로 바꾼다 — 화면에는 원래 모양으로
  let path = u.pathname || "/";
  try {
    path = decodeURI(path);
  } catch {
    /* 잘못된 % 인코딩은 그대로 둔다 */
  }
  return { service: `${u.hostname}:${port}`, domain: u.hostname, path: path + query };
}

/** 발견 자산 라벨("GET /api/orders/1", 전체 URL, 경로) → 경로. 경로를 못 뽑으면 "" */
function labelPath(label: string): string {
  const s = label.trim().replace(/^[A-Z]+\s+/, "");
  let loc: Loc | null = null;
  if (/^https?:\/\//i.test(s)) loc = locate(s);
  else if (s.startsWith("/")) loc = locate(`http://x${s}`);
  return loc ? loc.path : "";
}

function serviceKeyOf(n: EngineCoverageNode): string | null {
  const loc = n.url ? locate(n.url) : null;
  if (loc) return loc.service;
  const host = n.domain ? n.domain : n.ip;
  return host && n.port ? `${host}:${n.port}` : null;
}

function firstSeg(path: string): string {
  return path.match(/^\/([^/?#]+)/)?.[1] ?? "";
}

function statusOf(n: EngineCoverageNode): AgStatus {
  if (!n.in_scope) return "out";
  return n.tested ? "ok" : "wait";
}

export function buildAssetGraph(graph: EngineCoverageGraph, findings: EngineFinding[]): AssetGraph {
  const blocks = new Map<string, AgBlock & { eps: (AgEndpoint & { assetId?: number })[] }>();
  const serviceAsset = new Map<string, AgBlock>();
  const block = (service: string, domain: string) => {
    let b = blocks.get(service);
    if (!b) {
      b = { service, domain, siteFindings: [], groups: [], eps: [] };
      blocks.set(service, b);
    }
    return b;
  };

  for (const n of graph.nodes) {
    if (n.kind === "endpoint") {
      const loc = n.url ? locate(n.url) : null;
      if (!loc) continue;
      block(loc.service, loc.domain).eps.push({
        path: loc.path,
        status: statusOf(n),
        findings: [],
        assetId: n.asset_id,
      });
    } else if (n.kind === "service") {
      const key = serviceKeyOf(n);
      if (!key) continue;
      const b = block(key, key.replace(/:\d+$/, ""));
      if (n.asset_id !== undefined) serviceAsset.set(String(n.asset_id), b);
    }
  }

  // 경로 묶음: API·관리자 접두사이거나 두 개 이상 모인 첫 마디만 따로, 나머지는 "/"(페이지)
  for (const b of blocks.values()) {
    const count = new Map<string, number>();
    for (const e of b.eps) count.set(firstSeg(e.path), (count.get(firstSeg(e.path)) ?? 0) + 1);
    const groups = new Map<string, AgGroup>();
    for (const e of [...b.eps].sort((x, y) => x.path.localeCompare(y.path))) {
      const seg = firstSeg(e.path);
      const own = seg && (API_SEG.has(seg) || seg === "admin" || (count.get(seg) ?? 0) >= 2);
      const key = own ? `/${seg}` : "/";
      let g = groups.get(key);
      if (!g) {
        g = { key, name: GROUP_NAME[own ? seg : "/"] ?? "", eps: [] };
        groups.set(key, g);
      }
      g.eps.push(e);
    }
    // "/"(페이지)가 맨 앞, 나머지는 경로 순
    const rank = (g: AgGroup) => (g.key === "/" ? "" : g.key);
    b.groups = [...groups.values()].sort((x, y) => rank(x).localeCompare(rank(y)));
  }

  // 발견 → 엔드포인트(자산 id, 없으면 경로) · 서비스 · 연결 못 함
  const all = [...blocks.values()];
  const byAsset = new Map<string, AgEndpoint>();
  const byPath = new Map<string, AgEndpoint>();
  for (const b of all)
    for (const e of b.eps) {
      if (e.assetId !== undefined) byAsset.set(String(e.assetId), e);
      if (!byPath.has(e.path)) byPath.set(e.path, e);
    }
  const loose: EngineFinding[] = [];
  for (const f of findings) {
    let placed = false;
    for (const a of f.assets ?? []) {
      // 자산 id 가 우선(엔드포인트 → 서비스), id 로 못 찾을 때만 라벨의 경로로 엔드포인트를 찾는다
      const site = serviceAsset.get(a.id);
      const ep = byAsset.get(a.id) ?? (site || a.type === "service" ? undefined : byPath.get(labelPath(a.label)));
      const into = ep?.findings ?? site?.siteFindings;
      if (!into) continue;
      if (!into.includes(f)) into.push(f);
      placed = true;
    }
    if (!placed) loose.push(f);
  }

  const stats: AgStats = { inScope: 0, tested: 0, out: 0 };
  for (const b of all)
    for (const e of b.eps) {
      if (e.status === "out") stats.out += 1;
      else {
        stats.inScope += 1;
        if (e.status === "ok") stats.tested += 1;
      }
    }

  // 엔드포인트가 많은 서비스부터
  const out = all
    .sort((x, y) => y.eps.length - x.eps.length || x.service.localeCompare(y.service))
    .map(({ eps: _eps, ...b }) => ({
      ...b,
      groups: b.groups.map((g) => ({
        ...g,
        eps: g.eps.map(({ path, status, findings: fs }) => ({ path, status, findings: fs })),
      })),
    }));
  return { blocks: out, loose, stats };
}
