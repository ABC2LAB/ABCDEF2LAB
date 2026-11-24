// 제품 소유 · 접근 그래프(계약 docs/contracts/product-access-map.md §5, #877 G1~G3). 데이터는 제품 지도 응답(server-golden:
// 서버 assembleProductAccessMap 으로 뽑은 실제 JSON) 하나이고, 그래프는 그 응답을 다시 그린 것이다 — 노드 2 + N · 간선 2N,
// 간선과 칸이 1:1, 칸에 없는 관계 0, 자산 그래프와 잇지 않음. 간선 다섯 갈래의 모양이 서로 다르고 화살표는 확정 · 의심에만
// 있다(절대 규칙 5 — 판정 불가 · 판정 없음 · 공유 선언 · 소유 선언을 접근 성공처럼 그리지 않음). §5.5 시험 목록 그대로.

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/diagnosis/api", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/api")>()),
  apiFetch: vi.fn(),
}));
vi.mock("@/lib/diagnosis/kg-query", async (importOriginal) => {
  const real = await importOriginal<typeof import("@/lib/diagnosis/kg-query")>();
  return { ...real, getKgQuery: vi.fn() };
});

import { ApiError, apiFetch } from "@/lib/diagnosis/api";
import { getKgQuery, parseKgQuery } from "@/lib/diagnosis/kg-query";
import { kgCase } from "@/lib/diagnosis/kg-query-fixtures";

import {
  PRODUCT_MAP_ERROR_TEXT,
  type ProductAccessMap,
  type ProductMapCell,
  parseProductAccessMap,
} from "../live/productAccessMap";
import {
  PRODUCT_KG_EDGE_ORDER,
  PRODUCT_KG_LEGEND_HEAD,
  type ProductKgEdgeKind,
  productKgGraph,
} from "../live/productKgGraph";
import { ProductAccessMapGrid } from "../views/live/productAccessMapView";
import {
  PRODUCT_KG_LABEL_FONT,
  ProductKgGraphSection,
  productKgLayout,
  productKgStroke,
} from "../views/live/productKgGraphView";
import { ReportAssetGraph } from "../views/live/ReportAssetGraph";
import golden from "./product-access-map.server-golden.json";

type Json = Record<string, unknown>;
type Golden = Json & { task_id: string; cells: Json[]; resources: Json[] };
const cases = golden as unknown as Record<string, Golden>;
const clone = <T,>(v: T): T => JSON.parse(JSON.stringify(v)) as T;
const parse = (raw: Golden) => parseProductAccessMap(raw, raw.task_id);

beforeEach(() => vi.resetAllMocks());
afterEach(cleanup);

function serve(body: unknown) {
  vi.mocked(apiFetch).mockImplementation(async () => {
    if (body instanceof Error) throw body;
    return body as never;
  });
}

/** VAmPI 수용 실측 모양(§4 수용 결과): golden completed 에서 판정 불가 종류(note)를 뺀 것 — 종류 2(소유자 전용 1 · 공유 1). */
function vampiShape(): Golden {
  const raw = clone(cases.completed);
  raw.resources = raw.resources.filter((r) => r.resource_type !== "note");
  raw.cells = raw.cells.filter((c) => c.resource_type !== "note");
  raw.cells[1].evidence_seal = { status: "confirmed", reason: null };
  return raw;
}

/** 서버가 만드는 record_invalid 칸(판정 칸 없이 투영 오류만, server/product_access_map.go productMapCrossCell). */
function withRecordInvalid(): Golden {
  const raw = clone(cases.completed);
  Object.assign(raw.cells[1], {
    state: "unknown",
    verdict_id: null,
    record_status: null,
    oracle_value: null,
    checks: null,
    oracle_reason_codes: [],
    product_reason_codes: [],
    candidate_refs: [],
    observed_at: null,
    evidence_seal: null,
    projection_error: "record_invalid",
  });
  return raw;
}

const kinds = (raw: Golden) => productKgGraph(parse(raw)).edges.map((e) => e.kind);

describe("productKgGraph — 투영", () => {
  it("golden 6가지: 노드 2 + N · 간선 2N, 간선과 칸이 순서대로 1:1, 칸에 없는 간선 0", () => {
    expect(Object.keys(cases)).toHaveLength(6);
    for (const [name, raw] of Object.entries(cases)) {
      const map = parse(raw);
      const g = productKgGraph(map);
      const n = map.resources.length;
      expect(g.nodes, name).toHaveLength(2 + n);
      expect(g.edges, name).toHaveLength(2 * n);
      g.edges.forEach((e, i) => {
        expect(e.cell, `${name} ${i}`).toBe(map.cells[i]);
        expect(e.src, name).toBe(`p:${map.cells[i].role}`);
        expect(e.dst, name).toBe(`r:${map.cells[i].resourceType}`);
      });
      const ids = new Set(g.nodes.map((x) => x.id));
      expect(
        g.edges.every((e) => ids.has(e.src) && ids.has(e.dst)),
        name,
      ).toBe(true);
      expect(new Set(g.edges.map((e) => e.id)).size, name).toBe(g.edges.length);
    }
  });

  it("자원 종류 노드에 자산 ID · 자산 노드가 없다(자산 그래프와 잇지 않음, D06)", () => {
    const g = productKgGraph(parse(cases.completed));
    for (const n of g.nodes) {
      expect(Object.keys(n).sort()).toEqual(
        n.kind === "principal" ? ["id", "kind", "label", "role"] : ["id", "kind", "label", "sharing"],
      );
      expect(n.id).toMatch(/^[pr]:/);
    }
  });

  it("갈래: 소유 칸 = owner, 확정 · 의심 = judged, 저장 판정 불가 = undecidable, 판정 칸 없음 = no_verdict, 공유 = shared", () => {
    expect(kinds(cases.completed)).toEqual(["owner", "judged", "owner", "undecidable", "owner", "shared"]);
    expect(kinds(cases.interrupted_suspected)).toEqual(["owner", "no_verdict", "owner", "judged", "owner", "shared"]);
    // na_evidence_mismatch · record_missing_after_attempt 는 판정 칸이 없어 no_verdict(서버가 판정 칸을 채우지 않음)
    expect(kinds(cases.completed_skip_mismatch)).toEqual([
      "owner",
      "no_verdict",
      "owner",
      "judged",
      "owner",
      "no_verdict",
    ]);
    // record_hidden 칸도 판정 칸 없음, 같은 응답의 저장 판정 불가는 undecidable — 두 갈래를 섞지 않는다
    expect(kinds(cases.hidden)).toEqual(["owner", "no_verdict", "owner", "undecidable", "owner", "shared"]);
    // record_invalid 도 판정 칸이 없어 no_verdict(#877 윤재상 P2)
    expect(kinds(withRecordInvalid())[1]).toBe("no_verdict");
  });

  it("간선 글은 지도와 같은 문구에서만 — 소유는 중립, 판정 불가 두 갈래는 다른 글", () => {
    const g = productKgGraph(parse(cases.hidden));
    expect(g.edges[0].label).toBe("소유 계정(선언)");
    expect(g.edges[1].label).toBe(PRODUCT_MAP_ERROR_TEXT.record_hidden);
    expect(g.edges[3].label).toMatch(/^판정 불가 · 판정 \d+$/);
    expect(g.edges[5].label).toBe("공유 선언");
    expect(g.edges.map((e) => e.label).join(" ")).not.toMatch(/성공|정상|통과|미검사/);
    expect(productKgGraph(parse(withRecordInvalid())).edges[1].label).toBe(PRODUCT_MAP_ERROR_TEXT.record_invalid);
  });
});

describe("선 모양(§5.2 표)", () => {
  const shape = (k: ProductKgEdgeKind) => {
    const st = productKgStroke(k, "confirmed");
    return JSON.stringify([st.stroke === "none", st.dash, st.opacity, st.arrow]);
  };

  it("다섯 갈래의 모양이 서로 다르고 화살표는 judged 뿐", () => {
    const shapes = PRODUCT_KG_EDGE_ORDER.map(shape);
    expect(new Set(shapes).size).toBe(5);
    for (const k of PRODUCT_KG_EDGE_ORDER) expect(productKgStroke(k, "confirmed").arrow, k).toBe(k === "judged");
    // 실선(점선 없음 · 선 있음)은 judged 뿐
    for (const k of PRODUCT_KG_EDGE_ORDER) {
      const st = productKgStroke(k, "suspected");
      expect(st.stroke !== "none" && st.dash === null, k).toBe(k === "judged");
    }
    for (const k of ["no_verdict", "shared", "owner"] as const) expect(shape("undecidable")).not.toBe(shape(k));
  });
});

/**
 * 자원 종류 n개 지도(배치 시험용, 파서를 거치지 않는다). 교차 칸은 다섯 갈래를 돌아가며 쓰고, 가장 긴 투영 오류 문구
 * (na_evidence_mismatch · record_missing_after_attempt)를 넣어 글 너비가 가장 넓은 경우를 만든다.
 */
function wideMap(n: number): ProductAccessMap {
  const resources = Array.from({ length: n }, (_, i) => ({
    resourceType: `type-${String(i + 1).padStart(2, "0")}`,
    sharing: (i % 5 === 4 ? "shared" : "owner_only") as "shared" | "owner_only",
    confirmable: i % 5 !== 4,
  }));
  const verdict = (value: "confirmed" | "suspected" | "undecidable") => ({
    verdictId: "1",
    oracleValue: value,
    checks: { policy: "yes" as const, identity: "yes" as const, data: "yes" as const },
    oracleReasonCodes: [],
    productReasonCodes: [],
    candidateRefs: [],
    observedAt: "2026-11-23T10:38:07.000000Z",
    evidenceSeal: null,
  });
  const cells: ProductMapCell[] = [];
  resources.forEach((r, i) => {
    const base = { resourceType: r.resourceType, naBasis: null, verdict: null, projectionError: null };
    cells.push({ ...base, principal: "tester-a", role: "owner", state: "owner_control", applicable: null });
    const cross = { ...base, principal: "tester-b", role: "cross" as const, applicable: true };
    const pick = i % 5;
    if (pick === 0) cells.push({ ...cross, state: "confirmed", verdict: verdict("confirmed") });
    else if (pick === 1) cells.push({ ...cross, state: "unknown", verdict: verdict("undecidable") });
    else if (pick === 2) cells.push({ ...cross, state: "unknown", projectionError: "na_evidence_mismatch" });
    else if (pick === 3) cells.push({ ...cross, state: "unknown", projectionError: "record_missing_after_attempt" });
    else cells.push({ ...cross, state: "na", applicable: false, naBasis: "declared_shared" });
  });
  return {
    taskId: "7",
    policySource: { ref: "declaration-job", digest: "0".repeat(64) },
    confirmation: { status: "completed", reason: null },
    owner: "tester-a",
    cross: "tester-b",
    resources,
    hiddenRecords: 0,
    cells,
  };
}

type Box = { x0: number; x1: number; y0: number; y1: number };
/** 글 상자 어림(text-anchor start): 한글 등 비ASCII 는 글자 크기 1배, ASCII 는 0.6배 폭, 높이는 기준선 위 1배 · 아래 0.3배. */
function labelBox(text: string, lx: number, ly: number): Box {
  const f = PRODUCT_KG_LABEL_FONT;
  const w = [...text].reduce((sum, ch) => sum + (ch.charCodeAt(0) < 128 ? 0.6 * f : f), 0);
  return { x0: lx, x1: lx + w, y0: ly - f, y1: ly + 0.3 * f };
}
const overlap = (a: Box, b: Box) => a.x0 < b.x1 && b.x0 < a.x1 && a.y0 < b.y1 && b.y0 < a.y1;

/**
 * 작업대 1280px 창에서 보고서 그래프 자리의 폭(#893 이동찬 P2 2차: 왼쪽 236 · 오른쪽 상세 384 · 본문 좌우 패딩 56을 뺀 약
 * 604px). 그래프는 축소하지 않고 그대로 그리므로 이 폭 안에 들어가야 1280px 창에서 가로 스크롤 없이 11px 글이 11px로 보인다.
 */
const GRAPH_SLOT_AT_1280 = 604;

describe("배치 — 간선 글이 겹치지 않고 글자가 줄지 않음(#893 이동찬 P2)", () => {
  for (const [name, map] of [
    ["VAmPI 모양(종류 2)", parse(vampiShape())],
    ["golden completed(종류 3)", parse(cases.completed)],
    ["상한(종류 16)", wideMap(16)],
  ] as const) {
    it(`${name}: 글끼리 · 글과 계정 상자가 겹치지 않고, 글은 그 간선의 자원 종류 상자 안(이름 줄 아래)에 있다`, () => {
      const layout = productKgLayout(productKgGraph(map));
      expect(layout.width).toBeLessThanOrEqual(GRAPH_SLOT_AT_1280);
      const boxes = layout.edges.map((l) => labelBox(l.text, l.lx, l.ly));
      for (let i = 0; i < boxes.length; i++)
        for (let j = i + 1; j < boxes.length; j++)
          expect(overlap(boxes[i], boxes[j]), `${layout.edges[i].text} / ${layout.edges[j].text}`).toBe(false);
      const principalBoxes = [...layout.nodes.values()].map(
        (p): Box => ({ x0: p.x, x1: p.x + layout.nodeW, y0: p.y - layout.nodeH / 2, y1: p.y + layout.nodeH / 2 }),
      );
      for (const [i, b] of boxes.entries())
        for (const n of principalBoxes) expect(overlap(b, n), layout.edges[i].text).toBe(false);
      const resourceBoxes = [...layout.boxes.values()].map(
        (p): Box => ({ x0: p.x, x1: p.x + layout.resW, y0: p.y, y1: p.y + layout.resH }),
      );
      for (let i = 0; i < resourceBoxes.length; i++)
        for (let j = i + 1; j < resourceBoxes.length; j++)
          expect(overlap(resourceBoxes[i], resourceBoxes[j])).toBe(false);
      for (const [i, l] of layout.edges.entries()) {
        const box = layout.boxes.get(l.edge.dst);
        if (!box) throw new Error(`상자 없음 ${l.edge.dst}`);
        const b = boxes[i];
        // 자기 자원 종류 상자 안 — 다른 간선이 지나가는 자리(상자 왼쪽)에 글을 두지 않는다
        expect(b.x0 >= box.x && b.x1 <= box.x + layout.resW, `${l.text} 가로`).toBe(true);
        expect(b.y0 >= box.y && b.y1 <= box.y + layout.resH, `${l.text} 세로`).toBe(true);
        // 이름 · 선언 줄과 겹치지 않는다(이름 기준선 아래)
        expect(b.y0 >= box.y + layout.nameBase, `${l.text} 이름 줄`).toBe(true);
        // 간선은 자기 글 줄 높이에서 상자 왼쪽에 닿는다
        expect(l.x2).toBe(box.x);
        expect(Math.abs(l.y2 - (b.y0 + b.y1) / 2) <= PRODUCT_KG_LABEL_FONT, l.text).toBe(true);
        expect(l.text.startsWith(l.edge.cell.role === "owner" ? "A · " : "B · "), l.text).toBe(true);
      }
    });
  }
});

describe("ProductKgGraphSection — 그리기", () => {
  it("VAmPI 수용 모양: 노드 4 · 간선 4(소유 2 · 확정 1 · 공유 선언 1), 화살표는 확정 1뿐", async () => {
    const raw = vampiShape();
    serve(raw);
    render(<ProductKgGraphSection taskId={raw.task_id} active />);
    const svg = await screen.findByTestId("product-kg-graph");
    // 축소하지 않는다: 그리는 크기 = 배치 크기(#893 이동찬 P2 2차 — 패널에 맞춰 줄이면 11px 글이 7.6px이 됐다)
    const layout = productKgLayout(productKgGraph(parse(raw)));
    expect(svg.getAttribute("width")).toBe(String(layout.width));
    expect(svg.getAttribute("height")).toBe(String(layout.height));
    expect(svg.getAttribute("viewBox")).toBe(`0 0 ${layout.width} ${layout.height}`);
    expect(svg.querySelectorAll("[data-node]")).toHaveLength(4);
    const edges = [...svg.querySelectorAll("[data-edge]")];
    expect(edges.map((e) => e.getAttribute("data-edge"))).toEqual(["owner", "judged", "owner", "shared"]);
    const arrows = svg.querySelectorAll("line[marker-end]");
    expect(arrows).toHaveLength(1);
    expect(arrows[0].closest("[data-edge]")?.getAttribute("data-state")).toBe("confirmed");
    // 공유 선언은 선 없이 글만
    const shared = svg.querySelector("[data-edge='shared']");
    expect(shared?.querySelector("line[data-line]")).toBeNull();
    expect(shared).toHaveTextContent("공유 선언");
    // 지도와 같은 합계 칩(교차 칸만)
    const counts = screen.getByTestId("product-map-counts");
    expect([...counts.querySelectorAll("[data-tally]")].map((el) => el.getAttribute("data-tally"))).toEqual([
      "confirmed",
      "na",
    ]);
  });

  it("그린 선의 모양이 갈래마다 다르다(점선 · 불투명도 · 화살표), 범례에 다섯 갈래와 첫 줄", async () => {
    // golden 두 개로 다섯 갈래를 모두 그린다
    serve(cases.completed);
    const a = render(<ProductKgGraphSection taskId={cases.completed.task_id} active />);
    await screen.findByTestId("product-kg-graph");
    const seen = new Map<string, string>();
    const collect = () => {
      for (const g of document.querySelectorAll("[data-edge]")) {
        if (g.closest("[data-testid='product-kg-edge-detail']")) continue;
        const line = g.querySelector("line[data-line]");
        const sig = line
          ? JSON.stringify([
              line.getAttribute("stroke-dasharray"),
              (line as SVGElement).style.opacity,
              line.hasAttribute("marker-end"),
            ])
          : "no-line";
        const kind = g.getAttribute("data-edge") as string;
        if (seen.has(kind)) expect(seen.get(kind), kind).toBe(sig);
        seen.set(kind, sig);
      }
    };
    collect();
    const legend = screen.getByTestId("product-kg-legend");
    expect(legend).toHaveTextContent(PRODUCT_KG_LEGEND_HEAD);
    expect([...legend.querySelectorAll("[data-legend]")].map((el) => el.getAttribute("data-legend"))).toEqual(
      PRODUCT_KG_EDGE_ORDER,
    );
    a.unmount();
    serve(cases.interrupted_suspected);
    render(<ProductKgGraphSection taskId={cases.interrupted_suspected.task_id} active />);
    await screen.findByTestId("product-kg-graph");
    collect();
    expect([...seen.keys()].sort()).toEqual([...PRODUCT_KG_EDGE_ORDER].sort());
    expect(new Set(seen.values()).size).toBe(5);
    expect(JSON.parse(seen.get("judged") as string)[2]).toBe(true);
    for (const k of ["owner", "undecidable", "no_verdict"]) expect(JSON.parse(seen.get(k) as string)[2], k).toBe(false);
  });

  it("간선을 고르면 그 칸의 판정 번호 · 사유 · 봉인 미확인을, 소유 간선은 '조회 결과 아님'을 보인다", async () => {
    serve(cases.completed);
    render(<ProductKgGraphSection taskId={cases.completed.task_id} active />);
    const svg = await screen.findByTestId("product-kg-graph");
    const detail = () => screen.getByTestId("product-kg-edge-detail");
    expect(detail()).toHaveTextContent("간선을 고르면");
    fireEvent.click(within(svg).getByRole("button", { name: /tester-b\(교차\(B\)\) → document: 취약 확정/ }));
    expect(detail()).toHaveTextContent(/판정 177/);
    expect(detail()).toHaveTextContent("봉인 미확인");
    expect(detail()).toHaveTextContent("정책상 교차 계정 접근 금지");
    // 키보드로도 고른다
    const owner = within(svg).getByRole("button", { name: /tester-a\(소유\(A\)\) → document/ });
    fireEvent.keyDown(owner, { key: "Enter" });
    expect(detail()).toHaveTextContent("소유 계정 조회 결과가 아닙니다");
    expect(detail()).not.toHaveTextContent(/성공|정상/);
  });

  it("오류 · 대기는 지도와 같은 글: 409 사유 안내 · 404 구획 없음 · 확정 단계 전 안내", async () => {
    serve(new ApiError(409, "product_access_map_no_declaration"));
    const a = render(<ProductKgGraphSection taskId="2" active />);
    const note = await screen.findByRole("note");
    a.unmount();
    serve(new ApiError(409, "product_access_map_no_declaration"));
    const m = render(<ProductAccessMapGrid taskId="2" active />);
    expect((await screen.findByRole("note")).textContent).toBe(note.textContent);
    m.unmount();
    serve(new ApiError(404, "not found"));
    const b = render(<ProductKgGraphSection taskId="2" active />);
    await vi.waitFor(() => expect(apiFetch).toHaveBeenCalled());
    await vi.waitFor(() => expect(b.container).toBeEmptyDOMElement());
    b.unmount();
    serve(cases.pending);
    render(<ProductKgGraphSection taskId={cases.pending.task_id} active />);
    expect(await screen.findByTestId("product-map-pending")).toHaveTextContent("확정 단계가 끝나면");
    expect(screen.queryByTestId("product-kg-graph")).toBeNull();
  });

  it("탭이 안 보이거나 작업이 없으면 부르지 않는다", () => {
    serve(cases.completed);
    render(<ProductKgGraphSection taskId="2" active={false} />);
    render(<ProductKgGraphSection taskId={null} active />);
    expect(apiFetch).not.toHaveBeenCalled();
  });
});

describe("보고서 그래프 탭 — 자산 그래프와 서로 가리지 않음(§5.3)", () => {
  const Tab = ({ taskId }: { taskId: string }) => (
    <>
      <ReportAssetGraph taskId={taskId} active />
      <ProductKgGraphSection taskId={taskId} active />
    </>
  );

  it("자산 그래프(kg-v1) 실패 · 제품 그래프 성공: 둘 다 제 상태대로", async () => {
    const raw = clone(cases.completed);
    raw.task_id = "41";
    serve(raw);
    vi.mocked(getKgQuery).mockResolvedValue({ state: "error", failure: "failed", retryable: true });
    render(<Tab taskId="41" />);
    expect(await screen.findByTestId("product-kg-graph")).toBeInTheDocument();
    expect(await screen.findByText(/그래프 조회 실패/)).toBeInTheDocument();
  });

  it("자산 그래프 성공 · 제품 그래프 503: 자산 그래프는 그대로, 제품 쪽만 경고", async () => {
    const graph = parseKgQuery(kgCase());
    if (!graph) throw new Error("fixture rejected");
    vi.mocked(getKgQuery).mockResolvedValue({ state: "ready", graph });
    serve(new ApiError(503, "product_access_map_unavailable"));
    render(<Tab taskId="41" />);
    expect(await screen.findByRole("button", { name: /엔드포인트 #101/ })).toBeInTheDocument();
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("제품 지도 서비스가 응답하지 않거나");
    expect(screen.queryByTestId("product-kg-graph")).toBeNull();
  });
});
