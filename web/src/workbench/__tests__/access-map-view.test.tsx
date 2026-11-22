import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError, apiFetch } from "@/lib/diagnosis/api";
import { running } from "@/lib/diagnosis/diagnosis-url-fixtures";
import { type AccessMap, getAccessMap } from "@/lib/diagnosis/kg";

import { resetUrl, useUrl } from "../live/urlDiagnosis";
import { type Shell, ShellContext, type View } from "../shell";
import { UrlAccessMapLive } from "../views/live/UrlAccessMapLive";

// 제품 판정 목록(#801)은 이 시험 범위가 아니다 — 제품 관리 중계가 아닌 서버처럼(404) 구획을 두지 않게 한다.
// vi.fn 이 아니어서 resetAllMocks 에도 그대로다
vi.mock("../live/productVerdicts", async (original) => {
  const real = await original<typeof import("../live/productVerdicts")>();
  const { ApiError: NotInstalled } = await import("@/lib/diagnosis/api");
  return {
    ...real,
    fetchProductVerdicts: async () => {
      throw new NotInstalled(404, "not found");
    },
  };
});
vi.mock("@/lib/diagnosis/api", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/api")>()),
  apiFetch: vi.fn(),
}));
// 실 getAccessMap 을 기본으로 쓰고, 매퍼가 만들 수 없는 불일치 지도 시험에서만 갈아 끼운다
vi.mock("@/lib/diagnosis/kg", async (original) => {
  const real = await original<typeof import("@/lib/diagnosis/kg")>();
  return { ...real, getAccessMap: vi.fn(real.getAccessMap) };
});

const f = vi.mocked(apiFetch);
const other = { ...running, id: "job-2", task_id: "43" };
const cell = (principal: string, resource: string, state: string, over: Record<string, unknown> = {}) => ({
  principal,
  owner_ref: "owner",
  resource_ref: resource,
  resource_label: resource,
  state,
  oracle_value: null,
  applicable: true,
  record_id: null,
  case_ref: null,
  observed_at: null,
  reason_codes: [],
  canary_fields: [],
  projection_error: null,
  // 서버(access_map.go)가 늘 내는 신호 9개
  policy_check: "unknown",
  identity_check: "unknown",
  data_check: "unknown",
  same_resource: "unknown",
  owner_normal_data: "unknown",
  cross_normal_data: "unknown",
  nontrivial_data_match: "unknown",
  reflection_excluded: "unknown",
  template_excluded: "unknown",
  ...over,
});
/** product-report-graph-read.md 지도 DTO 모양의 합성 응답(비민감). */
const apiMap = (task = 42, over: Record<string, unknown> = {}) => ({
  task_id: task,
  run_ref: `execution-${task}`,
  reset_generation: "reset-1",
  axis: "horizontal",
  policy_version: `kg-fixture/v1:map-snapshot:${"a".repeat(64)}`,
  cells: [
    cell("reader", "document:one", "confirmed", { oracle_value: "confirmed" }),
    cell("reader", "document:two", "suspected"),
    cell("writer", "document:one", "allow-ok"),
    cell("writer", "document:two", "unknown", { projection_error: "observation_unavailable" }),
  ],
  ...over,
});
const noop = () => undefined;
function shellAt(route: View): Shell {
  return {
    route,
    navSeq: 1,
    narrow: false,
    phone: false,
    reduce: false,
    inspOpen: false,
    railOpen: false,
    railNewAtRoute: false,
    goTo: noop,
    openInsp: noop,
    closeInsp: noop,
    openRail: noop,
    closeRail: noop,
    foldRail: noop,
    dashPick: noop,
  };
}
/** Workbench 처럼 ShellContext 안에서 그린다(route=accessmap 일 때만 읽는다). */
function show(route: View = "accessmap") {
  const view = (r: View) => (
    <ShellContext.Provider value={shellAt(r)}>
      <UrlAccessMapLive />
    </ShellContext.Provider>
  );
  const out = render(view(route));
  return { ...out, at: (r: View) => out.rerender(view(r)) };
}
function deferred() {
  let resolve: (v: unknown) => void = () => undefined;
  const promise = new Promise<unknown>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

beforeEach(() => {
  vi.resetAllMocks();
  resetUrl();
  useUrl.setState({ job: running, verified: true });
});
afterEach(cleanup);

describe("UrlAccessMapLive (현재 task · 예시 없음)", () => {
  it("현재 task 의 지도를 읽어 수평 판정 · 격자를 그리고, 예시 배너는 없다", async () => {
    f.mockResolvedValue(apiMap());
    show();
    expect(screen.getByText(/운영자 전용/)).toBeInTheDocument();
    const grid = await screen.findByRole("table", { name: /격자/ });
    expect(f).toHaveBeenCalledWith("/api/tasks/42/access-map", expect.objectContaining({ cache: "no-store" }));
    expect(screen.queryByText(/예시/)).toBeNull();
    expect(screen.getByRole("heading", { name: "수평(교차 계정) 판정" })).toBeInTheDocument();
    expect(within(grid).getAllByText("취약 확정").length).toBeGreaterThan(0);
    expect(within(grid).getAllByText("취약 의심").length).toBeGreaterThan(0);
    expect(screen.getByText(/정상 차단/)).toHaveTextContent("산출하지 않습니다");
  });

  it("확정과 의심은 색으로 구분한다(§4.2(3) 축소 화면 오인 방지)", async () => {
    f.mockResolvedValue(apiMap());
    show();
    const grid = await screen.findByRole("table", { name: /격자/ });
    const vuln = within(grid).getAllByText("취약 확정")[0].closest("[data-decision]") as HTMLElement;
    const suspected = within(grid).getAllByText("취약 의심")[0].closest("[data-decision]") as HTMLElement;
    expect(vuln).toHaveAttribute("data-decision", "vuln");
    expect(suspected).toHaveAttribute("data-decision", "suspected");
    expect(vuln.style.background).not.toBe("");
    expect(vuln.style.background).not.toBe(suspected.style.background);
  });

  it("격자 밖 칸(projection 불일치)은 숨기지 않고 fail-closed 경고로 띄운다(§4.2(2)·N4)", async () => {
    const bad: AccessMap = {
      policyVersion: "test",
      mock: false,
      principals: [{ ref: "A", role: "", axis: "H" }],
      resources: [{ id: "r1", label: "r1", ownerRef: "A" }],
      cells: [
        { principalRef: "A", resourceId: "r1", decision: "vuln" },
        { principalRef: "Z", resourceId: "r1", decision: "vuln" }, // Z 는 신원 격자 밖
      ],
    };
    vi.mocked(getAccessMap).mockResolvedValueOnce(bad);
    show();
    await screen.findByRole("table", { name: /격자/ });
    expect(screen.getByText(/지도 데이터 불일치/)).toBeInTheDocument();
  });

  it.each([
    ["403 권한 거부", 403, /지도를 볼 권한을 확인하지 못했습니다/],
    ["404 task 없음", 404, /task 를 찾지 못했습니다/],
    ["502 중계 실패", 502, /정상 데이터로 보지 않습니다/],
    ["503 장애", 503, /불러오지 못했습니다 \(HTTP 503\)/],
  ])("%s 이면 빈 격자 · 예시로 바꾸지 않고 이유만 보인다", async (_, status, text) => {
    f.mockRejectedValue(new ApiError(status, "x"));
    show();
    expect(await screen.findByRole("alert")).toHaveTextContent(text);
    expect(screen.queryByRole("table", { name: /격자/ })).toBeNull();
    expect(screen.queryByRole("heading", { name: "수평(교차 계정) 판정" })).toBeNull();
    expect(screen.queryByText(/예시/)).toBeNull();
  });

  it("응답 task_id 가 선택한 task 와 다르면 그리지 않는다", async () => {
    f.mockResolvedValue(apiMap(41));
    show();
    expect(await screen.findByRole("alert")).toHaveTextContent(/다른 점검의 지도/);
    expect(screen.queryByRole("table", { name: /격자/ })).toBeNull();
  });

  it("다시 읽기는 같은 task 를 다시 부르고, 성공하면 오류를 지운다", async () => {
    f.mockRejectedValueOnce(new ApiError(503, "x")).mockResolvedValueOnce(apiMap());
    show();
    fireEvent.click(await screen.findByRole("button", { name: "다시 읽기" }));
    expect(await screen.findByRole("table", { name: /격자/ })).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(f.mock.calls.map(([p]) => p)).toEqual(["/api/tasks/42/access-map", "/api/tasks/42/access-map"]);
  });

  it("Job 을 바꾸면 이전 격자를 바로 버리고, 늦게 온 이전 응답도 그리지 않는다", async () => {
    const late = deferred();
    f.mockReturnValueOnce(late.promise as never).mockResolvedValueOnce(
      apiMap(43, { cells: [cell("guest", "document:43", "suspected")] }),
    );
    show();
    act(() => useUrl.setState((st) => ({ job: other, selectionEpoch: st.selectionEpoch + 1 })));
    expect(await screen.findByText("document:43")).toBeInTheDocument();
    expect((f.mock.calls[0][1] as RequestInit).signal?.aborted).toBe(true);
    await act(async () => late.resolve(apiMap(42)));
    expect(screen.queryByText("document:one")).toBeNull();
    expect(screen.getByText("document:43")).toBeInTheDocument();
  });

  it("새 Job 의 응답을 기다리는 동안 이전 격자를 보이지 않는다", async () => {
    f.mockResolvedValueOnce(apiMap()).mockReturnValueOnce(new Promise(() => undefined) as never);
    show();
    await screen.findByRole("table", { name: /격자/ });
    act(() => useUrl.setState((st) => ({ job: other, selectionEpoch: st.selectionEpoch + 1 })));
    expect(screen.getByText("불러오는 중…")).toBeInTheDocument();
    expect(screen.queryByRole("table", { name: /격자/ })).toBeNull();
  });

  it("Job 이 빠지면(권한 철회 · 재로그인) 격자를 지우고 예시로 채우지 않는다", async () => {
    f.mockResolvedValue(apiMap());
    show();
    await screen.findByRole("table", { name: /격자/ });
    act(() => useUrl.setState({ job: null }));
    expect(screen.getByText("실제 진단 작업을 선택하세요.")).toBeInTheDocument();
    expect(screen.queryByRole("table", { name: /격자/ })).toBeNull();
    expect(screen.queryByText(/예시/)).toBeNull();
    expect(f).toHaveBeenCalledTimes(1);
  });

  it("같은 점검을 다시 고르면(A → B → A) 새 응답 전엔 지난 A 격자를 보이지 않는다", async () => {
    f.mockResolvedValueOnce(apiMap()) // 처음 A
      .mockReturnValueOnce(new Promise(() => undefined) as never) // B (대기)
      .mockReturnValueOnce(new Promise(() => undefined) as never); // 다시 A (대기)
    show();
    await screen.findByRole("table", { name: /격자/ });
    act(() => useUrl.setState((st) => ({ job: other, selectionEpoch: st.selectionEpoch + 1 })));
    act(() => useUrl.setState((st) => ({ job: running, selectionEpoch: st.selectionEpoch + 1 })));
    expect(screen.getByText("불러오는 중…")).toBeInTheDocument();
    expect(screen.queryByRole("table", { name: /격자/ })).toBeNull();
    expect(f.mock.calls.map(([p]) => p)).toEqual([
      "/api/tasks/42/access-map",
      "/api/tasks/43/access-map",
      "/api/tasks/42/access-map",
    ]);
  });

  it("409(지도 범위 없음)는 오류 경고가 아니라 안내로 보이고 빈 격자를 만들지 않는다(1차 리허설 F7)", async () => {
    f.mockRejectedValue(new ApiError(409, "x"));
    show();
    expect(await screen.findByText(/평가 KG 접근통제 지도 범위가 없습니다/)).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.queryByRole("table", { name: /격자/ })).toBeNull();
    expect(screen.queryByText(/예시/)).toBeNull();
  });

  it("처음 409 뒤 같은 점검의 상태가 바뀌면 열린 화면에서 다시 읽는다", async () => {
    f.mockRejectedValueOnce(new ApiError(409, "x")).mockResolvedValueOnce(apiMap());
    show();
    expect(await screen.findByText(/평가 KG 접근통제 지도 범위가 없습니다/)).toBeInTheDocument();
    act(() => useUrl.setState({ job: { ...running, revision: running.revision + 1 } }));
    expect(await screen.findByRole("table", { name: /격자/ })).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("같은 선택에서 지도 화면에 다시 들어오면 새 응답 전엔 이전 격자를 보이지 않는다", async () => {
    f.mockResolvedValueOnce(apiMap());
    const v = show();
    await screen.findByRole("table", { name: /격자/ });
    act(() => v.at("report"));
    const next = deferred();
    f.mockReturnValueOnce(next.promise as never);
    act(() => v.at("accessmap"));
    expect(screen.getByText("불러오는 중…")).toBeInTheDocument();
    expect(screen.queryByRole("table", { name: /격자/ })).toBeNull();
    await act(async () => next.resolve(apiMap(42, { cells: [] })));
    expect(screen.queryByText("불러오는 중…")).toBeNull();
    expect(screen.getByText("집계할 수평 판정이 없습니다.")).toBeInTheDocument();
    expect(f).toHaveBeenCalledTimes(2);
  });

  it("같은 점검의 revision 이 오르면 새 응답 전엔 이전 격자를 보이지 않는다", async () => {
    f.mockResolvedValueOnce(apiMap());
    show();
    await screen.findByRole("table", { name: /격자/ });
    const next = deferred();
    f.mockReturnValueOnce(next.promise as never);
    act(() => useUrl.setState({ job: { ...running, revision: running.revision + 1 } }));
    expect(screen.getByText("불러오는 중…")).toBeInTheDocument();
    expect(screen.queryByRole("table", { name: /격자/ })).toBeNull();
    await act(async () => next.resolve(apiMap()));
    expect(await screen.findByRole("table", { name: /격자/ })).toBeInTheDocument();
    expect(f).toHaveBeenCalledTimes(2);
  });

  it.each([
    ["Job 이 비었다가(상태 조회 실패)", { job: null }],
    ["서버 확인이 풀렸다가", { verified: false }],
  ])("%s 같은 task · revision 으로 돌아오면 새 응답 전엔 이전 격자를 보이지 않는다", async (_n, gone) => {
    f.mockResolvedValueOnce(apiMap());
    show();
    await screen.findByRole("table", { name: /격자/ });
    act(() => useUrl.setState(gone));
    expect(screen.queryByRole("table", { name: /격자/ })).toBeNull();
    const next = deferred();
    f.mockReturnValueOnce(next.promise as never);
    act(() => useUrl.setState({ job: running, verified: true })); // 같은 선택 · task · revision
    expect(screen.getByText("불러오는 중…")).toBeInTheDocument();
    expect(screen.queryByRole("table", { name: /격자/ })).toBeNull();
    await act(async () => next.resolve(apiMap()));
    expect(await screen.findByRole("table", { name: /격자/ })).toBeInTheDocument();
    expect(f).toHaveBeenCalledTimes(2);
  });

  it("다시 읽기를 누르면 새 응답 전엔 지난 오류를 보이지 않는다", async () => {
    f.mockRejectedValueOnce(new ApiError(503, "x"));
    show();
    const retry = await screen.findByRole("button", { name: "다시 읽기" });
    const next = deferred();
    f.mockReturnValueOnce(next.promise as never);
    fireEvent.click(retry);
    expect(screen.getByText("불러오는 중…")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
    await act(async () => next.resolve(apiMap()));
    expect(await screen.findByRole("table", { name: /격자/ })).toBeInTheDocument();
  });

  it("화면이 숨겨져 있으면 읽지 않고, 열리면 읽는다", async () => {
    f.mockResolvedValue(apiMap());
    const v = show("report");
    expect(f).not.toHaveBeenCalled();
    act(() => v.at("accessmap"));
    expect(await screen.findByRole("table", { name: /격자/ })).toBeInTheDocument();
    expect(f).toHaveBeenCalledTimes(1);
  });

  it("서버 확인 전 Job 은 읽지 않는다", () => {
    useUrl.setState({ verified: false });
    show();
    expect(f).not.toHaveBeenCalled();
    expect(screen.queryByRole("table", { name: /격자/ })).toBeNull();
  });

  it("§4: 내보내기·공유·export 버튼을 두지 않는다", async () => {
    f.mockResolvedValue(apiMap());
    show();
    await screen.findByRole("table", { name: /격자/ });
    for (const el of [...screen.queryAllByRole("button"), ...screen.queryAllByRole("link")]) {
      expect(el.textContent ?? "").not.toMatch(/내보내기|공유|export/i);
    }
  });
});
