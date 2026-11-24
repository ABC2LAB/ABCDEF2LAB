// 자산 지식 그래프(보고서 그래프 탭, GET /api/tasks/{id}/coverage-graph). 서버 간선만 그리고(추정 관계 없음), 어긋난 응답은
// 버리고 센다. 같은 부모 · 같은 종류가 FOLD_LIMIT 를 넘으면 '…'로 접고 누르면 FOLD_STEP 씩 펼친다. 배치는 결정적이다.

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/diagnosis/api", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/api")>()),
  apiFetch: vi.fn(),
}));

import { ApiError, apiFetch } from "@/lib/diagnosis/api";

import type { CoverageGraph } from "../live/api";
import {
  coverageGraphStats,
  coverageNodeLabel,
  FOLD_LIMIT,
  FOLD_STEP,
  layoutCoverageGraph,
  parseCoverageGraph,
  visibleCoverageGraph,
} from "../live/coverageGraph";
import { CoverageGraphSection } from "../views/live/coverageGraphView";

beforeEach(() => vi.resetAllMocks());
afterEach(cleanup);

type RawNode = CoverageGraph["nodes"][number] & { status_code?: number };

const node = (key: string, kind: string, extra: Partial<RawNode> = {}): RawNode => ({
  key,
  kind,
  label: extra.label ?? key,
  tested: false,
  in_scope: true,
  ...extra,
});

/** 서버 publicCoverageGraph 뒤 모양: 회사(범위 밖) → 루트 → 서브도메인 → 서비스 → 엔드포인트 n개(라벨은 전부 같은 origin). */
function sample(endpoints: number, tested = 0): CoverageGraph {
  const nodes: RawNode[] = [
    node("c:1", "company", { label: "회사 #1", in_scope: false }),
    node("r:example.test", "root_domain", { label: "example.test", in_scope: false }),
    node("a:2", "subdomain", { label: "app.example.test", asset_id: 2, tested: true }),
    node("a:3", "service", {
      label: "http://app.example.test:3000",
      asset_id: 3,
      port: 3000,
      status_code: 200,
      tested: true,
    }),
  ];
  const edges = [
    { src: "r:example.test", dst: "c:1" },
    { src: "a:2", dst: "r:example.test" },
    { src: "a:3", dst: "a:2" },
  ];
  for (let i = 0; i < endpoints; i++) {
    const key = `a:${100 + i}`;
    nodes.push(node(key, "endpoint", { label: "http://app.example.test:3000", asset_id: 100 + i, tested: i < tested }));
    edges.push({ src: key, dst: "a:3" });
  }
  return { nodes, edges };
}

function serve(body: unknown) {
  vi.mocked(apiFetch).mockImplementation(async () => {
    if (body instanceof Error) throw body;
    return body as never;
  });
}

describe("parseCoverageGraph — 응답은 데이터(절대 규칙 4)", () => {
  it("모르는 종류 · 중복 키 · 없는 노드 · 자기 간선 · 두 번째 부모 · 순환은 버리고 센다", () => {
    const raw = sample(2);
    raw.nodes.push(node("x:1", "database"), node("a:100", "endpoint"));
    raw.edges.push(
      { src: "a:100", dst: "zz" },
      { src: "a:101", dst: "a:101" },
      { src: "a:100", dst: "a:2" },
      { src: "c:1", dst: "a:100" },
    );
    const g = parseCoverageGraph(raw);
    expect(g.nodes.map((n) => n.key)).toEqual(["c:1", "r:example.test", "a:2", "a:3", "a:100", "a:101"]);
    expect(g.dropped).toEqual({ nodes: 2, edges: 4 });
    expect(g.edges).toHaveLength(5);
  });

  it("요약은 범위 안 노드만 세고, 검사함은 그 안에서만", () => {
    const g = parseCoverageGraph(sample(5, 2));
    // 범위 안 = 서브도메인 · 서비스 · 엔드포인트 5, 검사함 = 서브도메인 · 서비스 · 엔드포인트 2
    expect(coverageGraphStats(g)).toEqual({ inScope: 7, tested: 4 });
  });

  it("엔드포인트는 origin 이 아니라 자산 번호로 가른다, 서비스는 포트 · 상태 코드", () => {
    const g = parseCoverageGraph(sample(2));
    const by = new Map(g.nodes.map((n) => [n.key, n]));
    const label = (k: string) => {
      const n = by.get(k);
      if (!n) throw new Error(`no node ${k}`);
      return coverageNodeLabel(n);
    };
    expect(label("a:100")).toBe("엔드포인트 #100");
    expect(label("a:101")).toBe("엔드포인트 #101");
    expect(label("a:3")).toBe(":3000 · 200");
    expect(label("c:1")).toBe("회사 #1");
  });
});

describe("visibleCoverageGraph — 접힘", () => {
  it(`같은 부모 · 같은 종류가 ${FOLD_LIMIT}개를 넘으면 나머지를 '…' 하나로, 펼치면 ${FOLD_STEP}개씩`, () => {
    const g = parseCoverageGraph(sample(45, 3));
    const v = visibleCoverageGraph(g, new Map());
    const eps = v.nodes.filter((n) => !n.fold && n.node.kind === "endpoint");
    const folds = v.nodes.filter((n) => n.fold);
    expect(eps).toHaveLength(FOLD_LIMIT);
    expect(folds).toEqual([expect.objectContaining({ kind: "endpoint", hidden: 45 - FOLD_LIMIT, parentKey: "a:3" })]);
    // 검사한 엔드포인트가 접히지 않는 쪽에 남는다
    expect(eps.slice(0, 3).every((n) => !n.fold && n.node.tested)).toBe(true);
    // 간선 = 보이는 노드 사이만(접힘 노드 → 부모 포함)
    const keys = new Set(v.nodes.map((n) => n.key));
    expect(v.edges.every((e) => keys.has(e.src) && keys.has(e.dst))).toBe(true);
    const fold = folds[0];
    if (!fold.fold) throw new Error("unreachable");
    const more = visibleCoverageGraph(g, new Map([[fold.groupId, FOLD_LIMIT + FOLD_STEP]]));
    expect(more.nodes.filter((n) => !n.fold && n.node.kind === "endpoint")).toHaveLength(40);
    const all = visibleCoverageGraph(g, new Map([[fold.groupId, FOLD_LIMIT + FOLD_STEP * 2]]));
    expect(all.nodes.filter((n) => n.fold)).toHaveLength(0);
  });

  it("접힌 자식의 자손은 보이지 않는다", () => {
    const raw = sample(0);
    for (let i = 0; i < FOLD_LIMIT + 1; i++) {
      raw.nodes.push(node(`s:${i}`, "service", { asset_id: 500 + i, label: `s${String(i).padStart(2, "0")}` }));
      raw.edges.push({ src: `s:${i}`, dst: "a:2" });
    }
    // 이름 순 맨 뒤 서비스(접힐 쪽) 아래 엔드포인트
    raw.nodes.push(node("e:x", "endpoint", { asset_id: 900 }));
    raw.edges.push({ src: "e:x", dst: `s:${FOLD_LIMIT}` });
    const v = visibleCoverageGraph(parseCoverageGraph(raw), new Map());
    const keys = v.nodes.map((n) => n.key);
    expect(keys).not.toContain(`s:${FOLD_LIMIT}`);
    expect(keys).not.toContain("e:x");
  });
});

describe("layoutCoverageGraph — 결정적 · 겹치지 않음", () => {
  it("같은 입력은 같은 좌표, 모두 유한하고 노드끼리 붙지 않는다", () => {
    const v = visibleCoverageGraph(parseCoverageGraph(sample(30)), new Map());
    const a = layoutCoverageGraph(v);
    const b = layoutCoverageGraph(v);
    expect([...a.entries()]).toEqual([...b.entries()]);
    const pts = [...a.values()];
    expect(pts.every((p) => Number.isFinite(p.x) && Number.isFinite(p.y))).toBe(true);
    let min = Number.POSITIVE_INFINITY;
    for (let i = 0; i < pts.length; i++)
      for (let j = i + 1; j < pts.length; j++)
        min = Math.min(min, Math.hypot(pts[i].x - pts[j].x, pts[i].y - pts[j].y));
    expect(min).toBeGreaterThan(12);
  });

  it("노드가 많아도(1천 개 펼침) 끝나고 좌표가 유한하다", () => {
    const g = parseCoverageGraph(sample(1000));
    const v = visibleCoverageGraph(g, new Map([["a:3::endpoint", 1000]]));
    expect(v.nodes).toHaveLength(1004);
    const pos = layoutCoverageGraph(v);
    expect([...pos.values()].every((p) => Number.isFinite(p.x) && Number.isFinite(p.y))).toBe(true);
  });
});

describe("CoverageGraphSection — 그리기", () => {
  it("서버 간선만 그리고, 요약 · 범례 · 접힘 노드를 보인다", async () => {
    serve(sample(25, 2));
    render(<CoverageGraphSection taskId="1" active />);
    const svg = await screen.findByTestId("coverage-graph");
    expect(apiFetch).toHaveBeenCalledWith("/api/tasks/1/coverage-graph");
    expect(svg.querySelectorAll("[data-node]")).toHaveLength(4 + FOLD_LIMIT);
    expect(svg.querySelectorAll("line")).toHaveLength(3 + FOLD_LIMIT + 1);
    expect(svg.querySelectorAll("[data-node][data-state='out']")).toHaveLength(2);
    expect(screen.getByTestId("coverage-graph-stats")).toHaveTextContent("범위 안 27 · 검사함 4");
    const legend = screen.getByTestId("coverage-graph-legend");
    expect(legend.querySelectorAll("[data-legend]")).toHaveLength(7);
    expect(within(svg).getByRole("button", { name: /엔드포인트 5개 더/ })).toBeInTheDocument();
  });

  it("'…'를 누르면 펼치고(다시 읽지 않음), 노드를 고르면 상세를 본다", async () => {
    serve(sample(25));
    render(<CoverageGraphSection taskId="1" active />);
    const svg = await screen.findByTestId("coverage-graph");
    const fold = within(svg).getByRole("button", { name: /엔드포인트 5개 더/ });
    fireEvent.pointerDown(fold, { pointerId: 1, clientX: 10, clientY: 10 });
    fireEvent.pointerUp(svg, { pointerId: 1, clientX: 10, clientY: 10 });
    expect(svg.querySelectorAll("[data-node='endpoint']")).toHaveLength(25);
    expect(svg.querySelectorAll("[data-fold]")).toHaveLength(0);
    expect(apiFetch).toHaveBeenCalledTimes(1);

    const service = within(svg).getByRole("button", { name: /서비스 :3000 · 200/ });
    fireEvent.keyDown(service, { key: "Enter" });
    const detail = screen.getByTestId("coverage-graph-detail");
    expect(detail).toHaveTextContent("서비스 · :3000 · 200");
    expect(detail).toHaveTextContent("속한 자산: 서브도메인 · app.example.test");
    expect(detail).toHaveTextContent("직접 포함한 자산 25");
    expect(detail).toHaveTextContent("취약점이 없다는 뜻은 아닙니다");
  });

  it("어긋난 응답은 그린 만큼 그리고 버린 수를 알린다", async () => {
    const raw = sample(1);
    raw.edges.push({ src: "a:100", dst: "nope" });
    serve(raw);
    render(<CoverageGraphSection taskId="1" active />);
    expect(await screen.findByRole("note")).toHaveTextContent("노드 0 · 간선 1건은 그리지 않았습니다");
  });

  it("빈 그래프 · 503 · 오류를 서로 다른 글로 — 빈 그림으로 대신하지 않는다", async () => {
    serve({ nodes: [], edges: [] });
    const a = render(<CoverageGraphSection taskId="1" active />);
    expect(await screen.findByText(/관찰해 저장한 자산이 아직 없습니다/)).toBeInTheDocument();
    a.unmount();
    serve(new ApiError(503, "저장된 자산 그래프를 확인할 수 없습니다"));
    const b = render(<CoverageGraphSection taskId="1" active />);
    expect(await screen.findByRole("note")).toHaveTextContent("지금 확인할 수 없습니다");
    b.unmount();
    serve(new ApiError(409, "자산 그래프의 조회 문맥이 변경되었습니다"));
    render(<CoverageGraphSection taskId="1" active />);
    expect(await screen.findByRole("alert")).toHaveTextContent("조회 문맥이 바뀌었습니다");
    expect(screen.queryByTestId("coverage-graph")).toBeNull();
  });

  it("탭이 안 보이거나 작업이 없으면 부르지 않는다", () => {
    serve(sample(1));
    render(<CoverageGraphSection taskId="1" active={false} />);
    render(<CoverageGraphSection taskId={null} active />);
    expect(apiFetch).not.toHaveBeenCalled();
  });
});
