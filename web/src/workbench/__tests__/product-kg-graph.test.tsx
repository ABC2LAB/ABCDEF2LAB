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

import { PRODUCT_MAP_ERROR_TEXT, parseProductAccessMap } from "../live/productAccessMap";
import {
  PRODUCT_KG_EDGE_ORDER,
  PRODUCT_KG_LEGEND_HEAD,
  type ProductKgEdgeKind,
  productKgGraph,
} from "../live/productKgGraph";
import { ProductAccessMapGrid } from "../views/live/productAccessMapView";
import { ProductKgGraphSection, productKgStroke } from "../views/live/productKgGraphView";
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

describe("ProductKgGraphSection — 그리기", () => {
  it("VAmPI 수용 모양: 노드 4 · 간선 4(소유 2 · 확정 1 · 공유 선언 1), 화살표는 확정 1뿐", async () => {
    const raw = vampiShape();
    serve(raw);
    render(<ProductKgGraphSection taskId={raw.task_id} active />);
    const svg = await screen.findByTestId("product-kg-graph");
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
