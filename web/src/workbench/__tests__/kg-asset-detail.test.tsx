import "@testing-library/jest-dom/vitest";

import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

// 자산 지식 그래프 탭: 자산 관계 그래프(coverage-graph)에 KG 조회 v1 포함 근거 · 출처를 붙인다(kg-query-v1.md §11).
// 그래프 응답은 apiFetch 로, KG 응답은 실 파서를 그대로 두고 getKgQuery 만 바꿔 §7 계약 사례를 통과시켜 만든다.
vi.mock("@/lib/diagnosis/api", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/api")>()),
  apiFetch: vi.fn(),
}));
vi.mock("@/lib/diagnosis/kg-query", async (importOriginal) => {
  const real = await importOriginal<typeof import("@/lib/diagnosis/kg-query")>();
  return { ...real, getKgQuery: vi.fn() };
});

import { apiFetch } from "@/lib/diagnosis/api";
import { getKgQuery, type KgLoad, parseKgQuery } from "@/lib/diagnosis/kg-query";
import { KG_E03, KG_E04, kgCase, kgTreeCase } from "@/lib/diagnosis/kg-query-fixtures";

import { AssetGraphTab } from "../views/live/coverageGraphView";

const g = vi.mocked(getKgQuery);
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});

/** 실 파서를 통과한 응답. taskId 를 주면 E01 의 현재 작업 41 을 그 ID 로 바꾼다(노드·관계·출처 ID 공식까지). */
function ready(raw: Record<string, unknown>, taskId?: string): KgLoad {
  const body = taskId ? JSON.parse(JSON.stringify(raw).replace(/\b41\b/g, taskId)) : raw;
  const graph = parseKgQuery(body);
  if (!graph) throw new Error("fixture rejected");
  return { state: "ready", graph };
}
function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

type RawNode = { key: string; kind: string; label: string; tested: boolean; in_scope: boolean; asset_id?: number };
const node = (key: string, kind: string, asset_id?: number): RawNode => ({
  key,
  kind,
  label: key,
  tested: false,
  in_scope: true,
  ...(asset_id ? { asset_id } : {}),
});
function serveGraph(nodes: RawNode[], edges: { src: string; dst: string }[] = []) {
  vi.mocked(apiFetch).mockImplementation(async () => ({ nodes, edges }) as never);
}
/** E01 · E03 · E04 의 엔드포인트 자산 101 하나 + 자산 번호 없는 노드 하나 + KG 에 없는 자산 900 */
const serveE01 = () =>
  serveGraph(
    [node("x:svc", "service"), node("a:101", "endpoint", 101), node("a:900", "endpoint", 900)],
    [
      { src: "a:101", dst: "x:svc" },
      { src: "a:900", dst: "x:svc" },
    ],
  );
/** 포함 트리 사례의 자산 1~4(자산 5는 그래프에 없음) */
const serveTree = () =>
  serveGraph(
    [node("a:1", "root_domain", 1), node("a:2", "subdomain", 2), node("a:3", "service", 3), node("a:4", "endpoint", 4)],
    [
      { src: "a:2", dst: "a:1" },
      { src: "a:3", dst: "a:2" },
      { src: "a:4", dst: "a:3" },
    ],
  );

async function pick(name: RegExp) {
  const svg = await screen.findByTestId("coverage-graph");
  fireEvent.keyDown(within(svg).getByRole("button", { name }), { key: "Enter" });
  return screen.getByTestId("coverage-graph-detail");
}

describe("자산 지식 그래프 — 노드 상세에 포함 근거 · 출처", () => {
  it("E01: 노드를 고르면 그 자산의 포함 근거 딱지와 출처가 상세에 나온다", async () => {
    serveE01();
    g.mockResolvedValue(ready(kgCase()));
    render(<AssetGraphTab taskId="41" active />);
    await screen.findByText(/포함 자산 1 · 출처/);
    const detail = await pick(/#101/);
    const kg = within(detail).getByTestId("kg-asset-detail");
    expect(within(kg).getByText("직접 연결")).toBeInTheDocument();
    expect(kg).toHaveTextContent("작업 #41");
    expect(kg).toHaveTextContent(/출처 \d/);
    expect(g).toHaveBeenCalledWith("41", expect.any(AbortSignal));
  });

  it("미조회 세 칸은 짧은 딱지로 늘 보이고, D05 문구는 (?) 안에 그대로 둔다. '없음 · 허용 · 안전'으로 칠하지 않는다", async () => {
    serveE01();
    g.mockResolvedValue(ready(kgCase()));
    const { container } = render(<AssetGraphTab taskId="41" active />);
    const summary = await screen.findByRole("region", { name: "포함 근거 요약" });
    expect(within(summary).getByText("소유 미조회", { selector: "span" })).toBeInTheDocument();
    expect(within(summary).getByText("정책 출처 미조회", { selector: "span" })).toBeInTheDocument();
    expect(within(summary).getByText("관측 미확인", { selector: "span" })).toBeInTheDocument();
    const tip = within(summary).getByRole("button", { name: "조회하지 않은 칸" });
    const note = document.getElementById(tip.getAttribute("aria-controls") ?? "");
    expect(note).not.toBeVisible();
    fireEvent.click(tip);
    expect(note).toBeVisible();
    expect(note).toHaveTextContent("소유 미조회 — 이 그래프는 소유를 조회하지 않습니다");
    expect(note).toHaveTextContent("정책 출처 미조회 — 이 그래프는 회차 정책 출처를 조회하지 않습니다");
    expect(note).toHaveTextContent("관측 미확인 — 이 그래프는 자산의 실제 관측을 판정하지 않습니다");
    expect(container.textContent ?? "").not.toMatch(/소유 없음|접근 허용|안전함|소유 미확인|소유 조회 실패/);
  });

  it("포함 트리: 자산 필드 파생 규칙 · 범위 출처가 고른 노드에 붙고, 문제는 그래프 아래 경고 상자에 한 번", async () => {
    serveTree();
    g.mockResolvedValue(ready(kgTreeCase()));
    render(<AssetGraphTab taskId="41" active />);
    await screen.findByText(/포함 자산 5/);
    const detail = await pick(/#4/);
    const kg = within(detail).getByTestId("kg-asset-detail");
    expect(within(kg).getByText("범위 일치")).toBeInTheDocument();
    expect(kg).toHaveTextContent("포함 관계 같은 origin(저장 필드 파생)");
    expect(kg).toHaveTextContent("자산 필드 파생 · 같은 origin");
    expect(kg).toHaveTextContent("작업 #41 범위 #20 · 수동");
    const issues = screen.getByRole("note", { name: "문제" });
    expect(issues).toHaveTextContent("문제 1");
    expect(issues).toHaveTextContent(/부모 후보가 여럿이라/);
  });

  it("그래프에 없는 포함 자산은 접어 둔 목록에서 근거 · 출처를 본다(트리 대신)", async () => {
    serveTree();
    g.mockResolvedValue(ready(kgTreeCase()));
    render(<AssetGraphTab taskId="41" active />);
    const off = await screen.findByText("그래프에서 고를 수 없는 포함 자산 1");
    const list = screen.getByRole("list", { name: "그래프에 없는 포함 자산", hidden: true });
    expect(off.closest("details")).not.toHaveAttribute("open");
    expect(list).toHaveTextContent("#5");
    expect(list).toHaveTextContent("범위 일치");
    // 옛 글 목록(포함 자산 트리)은 없다
    expect(screen.queryByRole("region", { name: "포함 자산" })).toBeNull();
  });

  it("E04: 직접 원천 작업과 그 포함 · 출처를 '읽기 전용 상속'으로 표시한다", async () => {
    serveE01();
    g.mockResolvedValue(ready(kgCase(KG_E04)));
    render(<AssetGraphTab taskId="41" active />);
    expect(await screen.findByText("직접 원천 작업 #40")).toBeInTheDocument();
    const kg = within(await pick(/#101/)).getByTestId("kg-asset-detail");
    expect(kg).toHaveTextContent("작업 #40 연결 · 수동 등록");
    expect(within(kg).getAllByText("읽기 전용 상속").length).toBeGreaterThanOrEqual(2); // 포함 · 출처
  });

  it("E03: 관측 참조는 모델 주장 · 검증 안 됨으로만 보인다", async () => {
    serveE01();
    g.mockResolvedValue(ready(kgCase(KG_E03)));
    render(<AssetGraphTab taskId="41" active />);
    await screen.findByText(/포함 자산 1/);
    const kg = within(await pick(/#101/)).getByTestId("kg-asset-detail");
    expect(kg).toHaveTextContent(/사실 #901 · 상태 confirmed · 모델 주장\(관측, 검증 안 됨\)/);
    expect(within(kg).getByText("탐색 앵커")).toBeInTheDocument();
  });

  it("자산 번호가 없는 노드 · KG 에 없는 자산은 그 뜻대로 적는다(근거 없음 ≠ 검사 결과)", async () => {
    serveE01();
    g.mockResolvedValue(ready(kgCase()));
    render(<AssetGraphTab taskId="41" active />);
    await screen.findByText(/포함 자산 1/);
    expect(await pick(/서비스/)).toHaveTextContent("자산 번호가 없는 노드라 포함 근거와 이을 수 없습니다");
    expect(await pick(/#900/)).toHaveTextContent("포함 자산이 아닙니다(포함 근거 없음 — 검사 결과가 아닙니다)");
  });

  it("정상 빈 결과는 '검사 결과가 아님'을 같이 적는다", async () => {
    serveE01();
    g.mockResolvedValue(ready(kgCase({ nodes: [(kgCase().nodes as unknown[])[0]], edges: [], provenance: [] })));
    render(<AssetGraphTab taskId="41" active />);
    expect(await screen.findByText(/포함된 자산이 없습니다. 검사 결과가 아니라/)).toBeInTheDocument();
  });
});

describe("자산 지식 그래프 — KG 조회 실패(§11-1: 조회 실패로만, 미조회 칸 없음)", () => {
  it("413 은 상한 문구 · 다시 읽기 없음 · 미조회 칸 없음, 그래프는 그대로 그린다", async () => {
    serveE01();
    g.mockResolvedValue({ state: "error", failure: "too_large", retryable: false });
    render(<AssetGraphTab taskId="41" active />);
    expect(await screen.findByRole("alert")).toHaveTextContent(/포함 근거 조회 실패 · 그래프가 조회 상한/);
    expect(screen.getAllByRole("button", { name: "다시 읽기" }).length).toBeGreaterThan(0); // 그래프 쪽 다시 읽기는 있다
    expect(screen.getByRole("alert").querySelector("button")).toBeNull();
    expect(screen.queryByText(/소유 미조회|정책 출처 미조회/)).toBeNull();
    const detail = await pick(/#101/);
    expect(detail).toHaveTextContent("포함 근거 조회 실패");
    expect(screen.getAllByRole("alert")).toHaveLength(1);
  });

  it("503 은 다시 읽기로 같은 task 의 KG 만 다시 부른다", async () => {
    serveE01();
    g.mockResolvedValueOnce({ state: "error", failure: "store_unavailable", retryable: true });
    g.mockResolvedValueOnce(ready(kgCase()));
    render(<AssetGraphTab taskId="41" active />);
    const alert = await screen.findByRole("alert");
    fireEvent.click(within(alert).getByRole("button", { name: "다시 읽기" }));
    expect(await screen.findByText(/포함 자산 1/)).toBeInTheDocument();
    expect(g).toHaveBeenCalledTimes(2);
    expect(apiFetch).toHaveBeenCalledTimes(1);
  });

  it("예상 밖 예외도 포함 근거 조회 실패로 그린다", async () => {
    serveE01();
    g.mockRejectedValue(new Error("boom"));
    render(<AssetGraphTab taskId="41" active />);
    expect(await screen.findByRole("alert")).toHaveTextContent("포함 근거 조회 실패");
  });
});

describe("자산 지식 그래프 — 조회 시점과 경쟁", () => {
  it("탭이 보이지 않으면 부르지 않고, 보이면 부른다", async () => {
    serveE01();
    g.mockResolvedValue(ready(kgCase()));
    const { rerender } = render(<AssetGraphTab taskId="41" active={false} />);
    expect(g).not.toHaveBeenCalled();
    rerender(<AssetGraphTab taskId="41" active />);
    expect(await screen.findByText(/포함 자산 1/)).toBeInTheDocument();
    expect(g).toHaveBeenCalledTimes(1);
  });

  it("Job 전환: A 응답이 B 로 바꾼 뒤 늦게 와도 쓰지 않는다", async () => {
    serveE01();
    const a = deferred<KgLoad>();
    const b = deferred<KgLoad>();
    g.mockImplementation((task) => (task === "41" ? a.promise : b.promise));
    const { rerender } = render(<AssetGraphTab taskId="41" active />);
    rerender(<AssetGraphTab taskId="42" active />);
    const [, signalA] = g.mock.calls[0];
    expect(signalA?.aborted).toBe(true);
    await act(async () => a.resolve(ready(kgCase())));
    expect(screen.queryByText(/작업 #41 · 포함 자산/)).toBeNull();
    expect(screen.getByText(/KG 조회 v1\)를 불러오는 중/)).toBeInTheDocument();
    await act(async () => b.resolve(ready(kgCase(), "42")));
    expect(screen.getByText(/작업 #42 · 포함 자산 1/)).toBeInTheDocument();
  });

  it("다시 들어오면 이전 근거를 지우고 새로 읽는다 — 새 조회 실패를 이전 근거로 가리지 않는다(§9 Q16)", async () => {
    serveE01();
    g.mockResolvedValueOnce(ready(kgCase()));
    const { rerender } = render(<AssetGraphTab taskId="41" active />);
    expect(await screen.findByText(/포함 자산 1/)).toBeInTheDocument();
    const later = deferred<KgLoad>();
    g.mockReturnValueOnce(later.promise);
    rerender(<AssetGraphTab taskId="41" active={false} />);
    rerender(<AssetGraphTab taskId="41" active />);
    expect(screen.queryByText(/포함 자산 1/)).toBeNull();
    await act(async () => later.resolve({ state: "error", failure: "forbidden", retryable: false }));
    expect(screen.getByRole("alert")).toHaveTextContent(/권한을 확인하지 못했습니다/);
    expect(screen.queryByText(/포함 자산 1/)).toBeNull();
  });
});
