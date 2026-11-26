// 자산 지식 그래프(보고서 그래프 탭, GET /api/tasks/{id}/coverage-graph). 서버 간선만 그리고(추정 관계 없음), 어긋난 응답은
// 버리고 센다. 같은 부모 · 같은 종류가 FOLD_LIMIT 를 넘으면 '…'로 접고 누르면 FOLD_STEP 씩 펼친다. 배치는 결정적이다.

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/diagnosis/api", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/api")>()),
  apiFetch: vi.fn(),
}));

import { ApiError, apiFetch } from "@/lib/diagnosis/api";

import type { CoverageGraph } from "../live/api";
import {
  type CgNode,
  coverageGraphStats,
  coverageNodeLabel,
  coverageNodeTitle,
  coveragePathDetailLine,
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

// #904 서버 합성 응답(docs/contracts/fixtures/coverage-graph-display-path.json · coverage-graph-legacy.json)의 엔드포인트 부분 사본.
// #904 머지 뒤에는 그 파일을 바로 읽어도 된다.
const DISPLAY_FIXTURE: CoverageGraph = {
  nodes: [
    node("c:1", "company", { label: "회사 #1", in_scope: false }),
    node("a:14", "service", {
      label: "https://api.example.test",
      asset_id: 14,
      url: "https://api.example.test",
      port: 443,
      status_code: 200,
    }),
    node("a:21", "endpoint", {
      label: "https://api.example.test",
      asset_id: 21,
      url: "https://api.example.test",
      tested: true,
      display_path: "/{id}/{id}",
      path_detail_state: "pattern_only",
    }),
    node("a:22", "endpoint", {
      label: "https://api.example.test",
      asset_id: 22,
      url: "https://api.example.test",
      tested: true,
      display_path: "/{id}/{id}/{id}",
      path_detail_state: "pattern_only",
    }),
    node("a:23", "endpoint", {
      label: "https://api.example.test",
      asset_id: 23,
      url: "https://api.example.test",
      display_path: "/{id}/{id}/{id}",
      path_detail_state: "pattern_only",
    }),
    node("a:24", "endpoint", {
      label: "https://api.example.test",
      asset_id: 24,
      url: "https://api.example.test",
      display_path: "/",
      path_detail_state: "exact",
    }),
    node("a:25", "endpoint", {
      label: "https://api.example.test",
      asset_id: 25,
      url: "https://api.example.test",
      path_detail_state: "unavailable",
    }),
  ],
  edges: [{ src: "a:14", dst: "c:1" }, ...[21, 22, 23, 24, 25].map((id) => ({ src: `a:${id}`, dst: "a:14" }))],
};
const LEGACY_FIXTURE: CoverageGraph = {
  nodes: [
    node("a:14", "service", {
      label: "https://api.example.test",
      asset_id: 14,
      url: "https://api.example.test",
      port: 443,
      status_code: 200,
    }),
    node("a:22", "endpoint", {
      label: "https://api.example.test",
      asset_id: 22,
      url: "https://api.example.test",
      tested: true,
    }),
  ],
  edges: [{ src: "a:22", dst: "a:14" }],
};

describe("표시 경로(display_path, #904)", () => {
  const labels = (raw: CoverageGraph) => {
    const g = parseCoverageGraph(raw);
    return Object.fromEntries(g.nodes.map((n) => [n.key, coverageNodeLabel(n)]));
  };

  it("엔드포인트는 가린 경로 + 자산 번호 — 같은 모양도 key · 번호로 갈린다, 없으면 자산 번호만", () => {
    const g = parseCoverageGraph(DISPLAY_FIXTURE);
    expect(g.dropped).toEqual({ nodes: 0, edges: 0 });
    expect(labels(DISPLAY_FIXTURE)).toMatchObject({
      "a:21": "/{id}/{id} · #21",
      "a:22": "/{id}/{id}/{id} · #22",
      "a:23": "/{id}/{id}/{id} · #23",
      "a:24": "/ · #24",
      "a:25": "엔드포인트 #25",
    });
    const by = new Map(g.nodes.map((n) => [n.key, n]));
    expect(by.get("a:21")?.origin).toBe("https://api.example.test");
    expect(by.get("a:22")?.displayPath).toBe(by.get("a:23")?.displayPath);
  });

  it("이전 응답(칸 없음)도 그대로 읽는다", () => {
    const g = parseCoverageGraph(LEGACY_FIXTURE);
    expect(g.dropped).toEqual({ nodes: 0, edges: 0 });
    expect(labels(LEGACY_FIXTURE)).toEqual({ "a:14": ":443 · 200", "a:22": "엔드포인트 #22" });
  });

  it("계약 밖 모양 · 엔드포인트 아닌 노드의 칸 · 경로 붙은 origin 은 쓰지 않는다", () => {
    const bad = ["/api/users", "", "/{id}//{id}", "{id}", "/{ID}", `${"/{id}".repeat(33)}`, "/{id}/?q=1"];
    const raw: CoverageGraph = {
      nodes: [
        ...bad.map((p, i) => node(`e:${i}`, "endpoint", { asset_id: 300 + i, display_path: p })),
        node("e:ok", "endpoint", { asset_id: 400, display_path: `${"/{id}".repeat(32)}/` }),
        node("s:1", "service", { asset_id: 500, display_path: "/{id}", url: "https://user:pw@api.example.test/x?y=1" }),
      ],
      edges: [],
    };
    const g = parseCoverageGraph(raw);
    for (const n of g.nodes.filter((x) => x.key.startsWith("e:") && x.key !== "e:ok"))
      expect(n.displayPath, n.key).toBeNull();
    expect(g.nodes.find((n) => n.key === "e:ok")?.displayPath).toBe(`${"/{id}".repeat(32)}/`);
    const svc = g.nodes.find((n) => n.key === "s:1");
    expect(svc?.displayPath).toBeNull();
    expect(svc?.origin).toBeNull();
  });

  it("상세에 origin 과 표시 경로를 따로 보인다", async () => {
    serve(DISPLAY_FIXTURE);
    render(<CoverageGraphSection taskId="1" active />);
    const svg = await screen.findByTestId("coverage-graph");
    fireEvent.keyDown(within(svg).getByRole("button", { name: /엔드포인트 \/\{id\}\/\{id\} · #21/ }), { key: "Enter" });
    const detail = screen.getByTestId("coverage-graph-detail");
    expect(detail).toHaveTextContent("origin https://api.example.test");
    expect(detail).toHaveTextContent("표시 경로 /{id}/{id}");
    expect(detail).toHaveTextContent("요청 · 진단 근거 아님");
  });
});

describe("표시 경로 상세 수준(path_detail_state, #945)", () => {
  const nodes = (raw: CoverageGraph) => new Map(parseCoverageGraph(raw).nodes.map((n) => [n.key, n]));
  const line = (by: Map<string, CgNode>, key: string) => {
    const n = by.get(key);
    if (!n) throw new Error(`no node ${key}`);
    return coveragePathDetailLine(n);
  };

  it("서버 합성 응답의 세 값을 읽고 상세 줄을 고른다", () => {
    const by = nodes(DISPLAY_FIXTURE);
    expect(by.get("a:21")?.pathDetail).toBe("pattern_only");
    expect(by.get("a:24")?.pathDetail).toBe("exact");
    expect(by.get("a:25")?.pathDetail).toBe("unavailable");
    expect(line(by, "a:21")).toBe("표시 경로 /{id}/{id} — 경로 조각을 가린 구조입니다(요청 · 진단 근거 아님)");
    expect(line(by, "a:24")).toBe("표시 경로 / — 루트 경로 그대로입니다(요청 · 진단 근거 아님)");
    expect(line(by, "a:25")).toBe("상세 경로 미기록 — 이 자산은 표시할 경로가 기록되지 않았습니다");
    expect(line(by, "a:14")).toBeNull();
  });

  it("이전 응답(칸 없음)은 상세 수준을 모름 — 표시 경로가 없으면 '미기록'으로 단정하지 않는다", () => {
    const by = nodes(LEGACY_FIXTURE);
    expect(by.get("a:22")?.pathDetail).toBeNull();
    expect(line(by, "a:22")).toBeNull();
    const old = nodes({ nodes: [node("e:1", "endpoint", { asset_id: 1, display_path: "/{id}" })], edges: [] });
    expect(old.get("e:1")?.pathDetail).toBeNull();
    expect(line(old, "e:1")).toBe("표시 경로 /{id} — 경로 조각을 가린 구조입니다(요청 · 진단 근거 아님)");
  });

  it("서버가 만들 수 없는 짝 · 모르는 값 · 엔드포인트 밖 칸은 상세 수준으로 쓰지 않는다", () => {
    const by = nodes({
      nodes: [
        node("e:1", "endpoint", { asset_id: 1, display_path: "/{id}", path_detail_state: "exact" }),
        node("e:2", "endpoint", { asset_id: 2, display_path: "/", path_detail_state: "pattern_only" }),
        node("e:3", "endpoint", { asset_id: 3, display_path: "/{id}", path_detail_state: "unavailable" }),
        node("e:4", "endpoint", { asset_id: 4, path_detail_state: "pattern_only" }),
        node("e:5", "endpoint", { asset_id: 5, path_detail_state: "exact" }),
        node("e:6", "endpoint", { asset_id: 6, display_path: "/{id}", path_detail_state: "PATTERN_ONLY" }),
        node("e:7", "endpoint", { asset_id: 7, display_path: "/api/users", path_detail_state: "pattern_only" }),
        node("s:1", "service", { asset_id: 8, path_detail_state: "unavailable" }),
      ],
      edges: [],
    });
    for (const k of ["e:1", "e:2", "e:3", "e:4", "e:5", "e:6", "e:7", "s:1"])
      expect(by.get(k)?.pathDetail, k).toBeNull();
    // 어긋난 짝이어도 검증을 넘은 표시 경로는 예전 문구로 보이고, 표시 경로가 없으면 줄을 두지 않는다
    expect(line(by, "e:3")).toBe("표시 경로 /{id} — 경로 조각을 가린 구조입니다(요청 · 진단 근거 아님)");
    expect(line(by, "e:4")).toBeNull();
    expect(line(by, "s:1")).toBeNull();
  });

  it("상세에 '상세 경로 미기록'을 보인다", async () => {
    serve(DISPLAY_FIXTURE);
    render(<CoverageGraphSection taskId="1" active />);
    const svg = await screen.findByTestId("coverage-graph");
    fireEvent.keyDown(within(svg).getByRole("button", { name: /엔드포인트 #25/ }), { key: "Enter" });
    const detail = screen.getByTestId("coverage-graph-detail");
    expect(detail).toHaveTextContent("상세 경로 미기록");
    expect(detail).not.toHaveTextContent("표시 경로");
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
    expect(detail).toHaveTextContent("서비스 :3000 · 200");
    expect(detail).toHaveTextContent("속한 자산: 서브도메인 app.example.test");
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

describe("#902 리뷰 반영", () => {
  const scaleOf = (svg: HTMLElement) => {
    const m = /scale\(([0-9.]+)\)/.exec(svg.querySelector("g")?.getAttribute("transform") ?? "");
    return m ? Number(m[1]) : Number.NaN;
  };

  it("빈 그래프 뒤 다시 읽어 노드가 생기면 Ctrl+휠 확대가 붙고 화면 맞춤도 새로 한다(이동찬 · 윤재상 재현)", async () => {
    vi.mocked(apiFetch)
      .mockResolvedValueOnce({ nodes: [], edges: [] } as never)
      .mockResolvedValueOnce(sample(3) as never);
    render(<CoverageGraphSection taskId="1" active />);
    const empty = await screen.findByText(/관찰해 저장한 자산이 아직 없습니다/);
    // 빈 그래프에도 다시 읽기가 있다
    fireEvent.click(within(empty.closest("[role='note']") as HTMLElement).getByRole("button", { name: "다시 읽기" }));
    const svg = await screen.findByTestId("coverage-graph");
    const before = scaleOf(svg);
    // 빈 배치(가운데 · 배율 1)에 멈추지 않고 새 노드에 맞춘다
    expect(svg.querySelector("g")?.getAttribute("transform")).not.toBe("translate(500 310) scale(1)");
    const ev = new WheelEvent("wheel", { deltaY: -200, ctrlKey: true, bubbles: true, cancelable: true });
    svg.dispatchEvent(ev);
    expect(ev.defaultPrevented).toBe(true);
    await waitFor(() => expect(scaleOf(svg)).toBeGreaterThan(before));
  });

  it("그냥 휠은 페이지 스크롤로 둔다(가로채지 않음), +/− 버튼으로도 확대 · 축소한다", async () => {
    serve(sample(3));
    render(<CoverageGraphSection taskId="1" active />);
    const svg = await screen.findByTestId("coverage-graph");
    const before = scaleOf(svg);
    const plain = new WheelEvent("wheel", { deltaY: -200, bubbles: true, cancelable: true });
    svg.dispatchEvent(plain);
    expect(plain.defaultPrevented).toBe(false);
    expect(scaleOf(svg)).toBe(before);
    fireEvent.click(screen.getByRole("button", { name: "확대" }));
    expect(scaleOf(svg)).toBeGreaterThan(before);
    fireEvent.click(screen.getByRole("button", { name: "축소" }));
    fireEvent.click(screen.getByRole("button", { name: "축소" }));
    expect(scaleOf(svg)).toBeLessThan(before);
  });

  it("nodes · edges 가 배열이 아니면 빈 그래프가 아니라 형식 오류로 알린다", async () => {
    expect(parseCoverageGraph({ nodes: {}, edges: [] } as unknown as CoverageGraph).malformed).toBe(true);
    serve({ nodes: {}, edges: [] });
    render(<CoverageGraphSection taskId="1" active />);
    expect(await screen.findByRole("alert")).toHaveTextContent("응답 형식을 확인하지 못해");
    expect(screen.queryByText(/관찰해 저장한 자산이 아직 없습니다/)).toBeNull();
  });

  it("부모 없는 노드도 종류별로 접는다(서버 anchor 로 바로 들어온 엔드포인트 500개)", () => {
    const raw: CoverageGraph = {
      nodes: Array.from({ length: 500 }, (_, i) => node(`a:${1000 + i}`, "endpoint", { asset_id: 1000 + i })),
      edges: [],
    };
    const v = visibleCoverageGraph(parseCoverageGraph(raw), new Map());
    expect(v.nodes.filter((n) => !n.fold)).toHaveLength(FOLD_LIMIT);
    expect(v.nodes.filter((n) => n.fold)).toEqual([
      expect.objectContaining({ hidden: 500 - FOLD_LIMIT, parentKey: null }),
    ]);
    expect(v.edges).toHaveLength(0);
  });

  it("종류 이름을 두 번 붙이지 않고, 공개 못 하는 루트('자산 #0')는 이름 비공개로", () => {
    const g = parseCoverageGraph({
      nodes: [
        node("a:100", "endpoint", { asset_id: 100 }),
        node("r:0123abcd", "root_domain", { label: "자산 #0", in_scope: false }),
      ],
      edges: [],
    });
    expect(g.nodes.map(coverageNodeTitle)).toEqual(["엔드포인트 #100", "루트 도메인(이름 비공개)"]);
  });

  it("'…'를 펼쳐도 보이던 노드는 거의 제자리다(앞 배치를 이어받음)", () => {
    const g = parseCoverageGraph(sample(45));
    const v1 = visibleCoverageGraph(g, new Map());
    const l1 = layoutCoverageGraph(v1);
    const v2 = visibleCoverageGraph(g, new Map([["a:3::endpoint", FOLD_LIMIT + FOLD_STEP]]));
    const l2 = layoutCoverageGraph(v2, l1);
    const shared = v1.nodes.filter((n) => !n.fold).map((n) => n.key);
    const moves = shared.map((k) => {
      const a = l1.get(k) as { x: number; y: number };
      const b = l2.get(k) as { x: number; y: number };
      return Math.hypot(a.x - b.x, a.y - b.y);
    });
    const avg = moves.reduce((x, y) => x + y, 0) / moves.length;
    // 리뷰 실측(이어받기 전): 평균 106px · 최대 253px
    expect(avg).toBeLessThan(40);
    // 같은 입력이면 같은 결과(결정적)
    expect([...layoutCoverageGraph(v2, l1).entries()]).toEqual([...l2.entries()]);
  });
});
