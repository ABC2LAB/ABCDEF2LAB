import "@testing-library/jest-dom/vitest";

import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

// 실 파서는 그대로 쓰고 getKgQuery 만 바꾼다 — 응답은 §7 계약 사례를 실 파서에 통과시켜 만든다.
vi.mock("@/lib/diagnosis/kg-query", async (importOriginal) => {
  const real = await importOriginal<typeof import("@/lib/diagnosis/kg-query")>();
  return { ...real, getKgQuery: vi.fn() };
});

import { getKgQuery, type KgLoad, parseKgQuery } from "@/lib/diagnosis/kg-query";
import { KG_E03, KG_E04, kgCase, kgTreeCase } from "@/lib/diagnosis/kg-query-fixtures";

import { ReportAssetGraph } from "../views/live/ReportAssetGraph";

const g = vi.mocked(getKgQuery);
afterEach(() => {
  cleanup();
  g.mockReset();
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

describe("ReportAssetGraph — 그리기", () => {
  it("E01: 자산·포함 근거와 소유·정책 출처 '미조회' 문구를 보이고 '없음·허용·안전'으로 칠하지 않는다", async () => {
    g.mockResolvedValue(ready(kgCase()));
    const { container } = render(<ReportAssetGraph taskId="41" active />);
    expect(await screen.findByRole("button", { name: /엔드포인트 #101/ })).toBeInTheDocument();
    expect(screen.getByText("직접 연결")).toBeInTheDocument();
    expect(screen.getByText(/소유 미조회 — 이 그래프는 소유를 조회하지 않습니다/)).toBeInTheDocument();
    expect(screen.getByText(/정책 출처 미조회/, { selector: "span.help, [class*=help]" })).toBeInTheDocument();
    expect(screen.getByText(/관측 미확인/, { selector: "[class*=help]" })).toBeInTheDocument();
    expect(container.textContent ?? "").not.toMatch(/소유 없음|접근 허용|안전함|소유 미확인|소유 조회 실패/);
    expect(g).toHaveBeenCalledWith("41", expect.any(AbortSignal));
  });

  it("포함 트리: 자식이 부모 아래에 들어가고, 고른 자산의 출처(자산 필드 파생 규칙)가 보인다", async () => {
    g.mockResolvedValue(ready(kgTreeCase()));
    render(<ReportAssetGraph taskId="41" active />);
    const ep = await screen.findByRole("button", { name: /엔드포인트 #4/ });
    expect(screen.getByRole("button", { name: /루트 도메인 #1/ }).closest("li")).toContainElement(ep);
    fireEvent.click(ep);
    expect(ep).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("heading", { name: "출처 · 엔드포인트 #4" })).toBeInTheDocument();
    expect(screen.getByText(/자산 필드 파생 · 같은 origin/)).toBeInTheDocument();
    expect(screen.getByText(/작업 #41 범위 #20 · 수동/)).toBeInTheDocument();
    // 문제: 애매한 부모는 관계를 그리지 않고 문제로 보인다
    expect(screen.getByText(/부모 후보가 여럿이라/)).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "문제 1" })).toBeInTheDocument();
  });

  it("E04: 직접 원천 작업과 그 포함을 '읽기 전용 상속'으로 표시한다", async () => {
    g.mockResolvedValue(ready(kgCase(KG_E04)));
    render(<ReportAssetGraph taskId="41" active />);
    expect(await screen.findByText(/직접 원천 작업 #40/)).toBeInTheDocument();
    expect(screen.getAllByText("읽기 전용 상속").length).toBeGreaterThanOrEqual(3); // 원천 목록 · 트리 포함 · 출처
    expect(screen.getByText("작업 #40 연결 · 수동 등록")).toBeInTheDocument();
  });

  it("E03: 관측 참조는 모델 주장·검증 안 됨으로만 보인다", async () => {
    g.mockResolvedValue(ready(kgCase(KG_E03)));
    render(<ReportAssetGraph taskId="41" active />);
    expect(await screen.findByText(/사실 #901 · 상태 confirmed · 모델 주장\(관측, 검증 안 됨\)/)).toBeInTheDocument();
    expect(screen.getByText("탐색 앵커")).toBeInTheDocument();
  });

  it("정상 빈 결과는 '검사 결과가 아님'을 같이 적는다", async () => {
    g.mockResolvedValue(ready(kgCase({ nodes: [(kgCase().nodes as unknown[])[0]], edges: [], provenance: [] })));
    render(<ReportAssetGraph taskId="41" active />);
    expect(await screen.findByText(/포함된 자산이 없습니다. 검사 결과가 아니라/)).toBeInTheDocument();
  });
});

describe("ReportAssetGraph — 실패(§11-1: 그래프 조회 실패로만, 소유·정책 출처 칸 없음)", () => {
  it("413 은 상한 문구, 다시 읽기 없음, 소유·정책 출처 칸 없음", async () => {
    g.mockResolvedValue({ state: "error", failure: "too_large", retryable: false });
    render(<ReportAssetGraph taskId="41" active />);
    expect(await screen.findByRole("alert")).toHaveTextContent(/그래프 조회 실패 · 그래프가 조회 상한/);
    expect(screen.queryByRole("button", { name: "다시 읽기" })).toBeNull();
    expect(screen.queryByText(/소유 미조회|정책 출처 미조회/)).toBeNull();
  });

  it("503 은 다시 읽기로 같은 task 를 다시 부른다", async () => {
    g.mockResolvedValueOnce({ state: "error", failure: "store_unavailable", retryable: true });
    g.mockResolvedValueOnce(ready(kgCase()));
    render(<ReportAssetGraph taskId="41" active />);
    fireEvent.click(await screen.findByRole("button", { name: "다시 읽기" }));
    expect(await screen.findByRole("button", { name: /엔드포인트 #101/ })).toBeInTheDocument();
    expect(g).toHaveBeenCalledTimes(2);
  });

  it("예상 밖 예외도 그래프 조회 실패로 그린다", async () => {
    g.mockRejectedValue(new Error("boom"));
    render(<ReportAssetGraph taskId="41" active />);
    expect(await screen.findByRole("alert")).toHaveTextContent("그래프 조회 실패");
  });
});

describe("ReportAssetGraph — 조회 시점과 경쟁", () => {
  it("탭이 보이지 않으면 부르지 않고, 보이면 부른다", async () => {
    g.mockResolvedValue(ready(kgCase()));
    const { rerender } = render(<ReportAssetGraph taskId="41" active={false} />);
    expect(g).not.toHaveBeenCalled();
    rerender(<ReportAssetGraph taskId="41" active />);
    expect(await screen.findByRole("button", { name: /엔드포인트 #101/ })).toBeInTheDocument();
    expect(g).toHaveBeenCalledTimes(1);
  });

  it("Job 전환: A 응답이 B 로 바꾼 뒤 늦게 와도 그리지 않는다", async () => {
    const a = deferred<KgLoad>();
    const b = deferred<KgLoad>();
    g.mockImplementation((task) => (task === "41" ? a.promise : b.promise));
    const { rerender } = render(<ReportAssetGraph taskId="41" active />);
    rerender(<ReportAssetGraph taskId="42" active />);
    const [, signalA] = g.mock.calls[0];
    expect(signalA?.aborted).toBe(true);
    await act(async () => a.resolve(ready(kgCase())));
    expect(screen.queryByRole("button", { name: /엔드포인트 #101/ })).toBeNull();
    expect(screen.getByRole("status")).toHaveTextContent("불러오는 중");
    await act(async () => b.resolve(ready(kgCase(), "42")));
    expect(screen.getByText(/작업 #42 · 자산 1/)).toBeInTheDocument();
  });

  it("다시 들어오면 이전 그래프를 지우고 새로 읽는다 — 새 조회 실패를 이전 그래프로 가리지 않는다(§9 Q16)", async () => {
    g.mockResolvedValueOnce(ready(kgCase()));
    const { rerender } = render(<ReportAssetGraph taskId="41" active />);
    expect(await screen.findByRole("button", { name: /엔드포인트 #101/ })).toBeInTheDocument();
    const later = deferred<KgLoad>();
    g.mockReturnValueOnce(later.promise);
    rerender(<ReportAssetGraph taskId="41" active={false} />);
    rerender(<ReportAssetGraph taskId="41" active />);
    expect(screen.queryByRole("button", { name: /엔드포인트 #101/ })).toBeNull();
    await act(async () => later.resolve({ state: "error", failure: "forbidden", retryable: false }));
    expect(screen.getByRole("alert")).toHaveTextContent(/권한을 확인하지 못했습니다/);
    expect(screen.queryByRole("button", { name: /엔드포인트 #101/ })).toBeNull();
  });

  it("A→B→A: 처음 A 의 고른 자산이 아니라 이번 조회의 첫 자산을 고른다", async () => {
    g.mockImplementation(async (task) => (task === "41" ? ready(kgTreeCase()) : ready(kgCase(), "42")));
    const { rerender } = render(<ReportAssetGraph taskId="41" active />);
    fireEvent.click(await screen.findByRole("button", { name: /엔드포인트 #4/ }));
    rerender(<ReportAssetGraph taskId="42" active />);
    await screen.findByText(/작업 #42 · 자산 1/);
    rerender(<ReportAssetGraph taskId="41" active />);
    const root = await screen.findByRole("button", { name: /루트 도메인 #1/ });
    expect(root).toHaveAttribute("aria-pressed", "true");
  });
});
