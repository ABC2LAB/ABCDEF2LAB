import "@testing-library/jest-dom/vitest";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { ApiError, apiFetch } from "@/lib/diagnosis/api";
import { e2Attempt, e2Flow } from "@/lib/diagnosis/e2flow-fixtures";

import { resetUrl, useUrl } from "../live/urlDiagnosis";
import { type Shell, ShellContext, type View } from "../shell";
import { UrlE2FlowLive } from "../views/live/UrlE2FlowLive";

/** 전송 통제 흐름 화면은 보일 때(경로 e2flow)만 읽는다(1차 리허설 F5). */
function at(route: View = "e2flow") {
  const shell = {
    route,
    navSeq: 1,
    narrow: false,
    phone: false,
    reduce: false,
    inspOpen: false,
    railOpen: false,
    railNewAtRoute: false,
    goTo: () => undefined,
    openInsp: () => undefined,
    closeInsp: () => undefined,
    openRail: () => undefined,
    closeRail: () => undefined,
    foldRail: () => undefined,
    dashPick: () => undefined,
  } as Shell;
  return (
    <ShellContext.Provider value={shell}>
      <UrlE2FlowLive />
    </ShellContext.Provider>
  );
}
vi.mock("@/lib/diagnosis/api", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/api")>()),
  apiFetch: vi.fn(),
}));
beforeEach(() => {
  vi.resetAllMocks();
  resetUrl();
  useUrl.setState({ job: { task_id: "42" } as never });
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});
it("실제 task·16사유·10누계·시도와 가린 경로를 표시한다", async () => {
  vi.mocked(apiFetch).mockResolvedValue(
    e2Flow({ omitted_calls: 7, model_writes: { responded_2xx: 2, unverified: 1 } }),
  );
  render(at());
  expect(await screen.findByText("a".repeat(32))).toBeInTheDocument();
  expect(screen.getByText("GET /orders/{id}")).toBeInTheDocument();
  expect(screen.getByText(/이전 호출 7개 생략/)).toBeInTheDocument();
  expect(screen.getByText(/E③ 확정 단계의 호출·시도는 저장된 실행 기록에 포함됩니다/)).toBeInTheDocument();
  expect(screen.getByText(/2xx 응답 2건 · 결과 확인 필요 1건/)).toBeInTheDocument();
  expect(screen.queryByText(/예시 데이터/)).toBeNull();
  expect(screen.getByText(/완전 수신 확인/)).toBeInTheDocument();
  expect(vi.mocked(apiFetch).mock.calls[0][0]).toBe("/api/tasks/42/e2-flow");
});
it("more=true는 즉시 다음 index를 읽고 같은 시도를 교체한다", async () => {
  const first = e2Flow({ last_index: 10, more: true });
  first.calls[0].attempts = [e2Attempt({ index: 9, stage: "intent" })];
  const second = e2Flow({ last_index: 11 });
  second.calls[0].attempts = [e2Attempt({ index: 11, path_pattern: "" })];
  vi.mocked(apiFetch).mockResolvedValueOnce(first).mockResolvedValue(second);
  render(at());
  expect(await screen.findByText("GET 경로 없음")).toBeInTheDocument();
  expect(screen.getAllByText("a".repeat(32))).toHaveLength(1);
  expect(vi.mocked(apiFetch).mock.calls[1][0]).toBe("/api/tasks/42/e2-flow?after=10");
});
it("송신 뒤 감사 실패는 차단 성공으로 보이지 않는다", async () => {
  const f = e2Flow();
  f.calls[0].attempts = [e2Attempt({ decision: "denied", reason: "audit_failed", path_pattern: "" })];
  vi.mocked(apiFetch).mockResolvedValue(f);
  const { container } = render(at());
  await screen.findByText("a".repeat(32));
  expect(container.querySelector('[data-class="unknown"]')).not.toBeNull();
  expect(container.querySelector('[data-class="deny"]')).toBeNull();
});
it("저널 검증 실패는 이미 보인 사건도 제거한다", async () => {
  vi.useFakeTimers();
  vi.mocked(apiFetch)
    .mockResolvedValueOnce(e2Flow())
    .mockResolvedValue(e2Flow({ journal: "invalid" }));
  render(at());
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(screen.getByText("a".repeat(32))).toBeInTheDocument();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(2000);
  });
  expect(screen.getByRole("alert")).toHaveTextContent("감사 저널 검증 실패");
  expect(screen.queryByText("a".repeat(32))).toBeNull();
  expect(screen.queryByRole("heading", { name: "전송 통제 요약" })).toBeNull();
});
it("task 변경은 사건·커서를 초기화하고 늦은 이전 task 응답을 버린다", async () => {
  let release: (value: unknown) => void = () => {
    // 첫 요청에서 지연 응답으로 바꾼다.
  };
  vi.mocked(apiFetch).mockImplementation((path) =>
    String(path).includes("/42/")
      ? new Promise((resolve) => {
          release = resolve;
        })
      : (Promise.resolve(e2Flow({ calls: [] })) as never),
  );
  render(at());
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1));
  act(() => useUrl.setState({ job: { task_id: "43" } as never }));
  await screen.findByText(/기록된 호출이 없습니다/);
  await act(async () => release(e2Flow()));
  expect(screen.getByText(/기록된 호출이 없습니다/)).toBeInTheDocument();
  expect(screen.queryByText("a".repeat(32))).toBeNull();
  expect(vi.mocked(apiFetch).mock.calls[1][0]).toBe("/api/tasks/43/e2-flow");
});
it("실제 작업 없이는 호출이나 예시 데이터가 없다", () => {
  useUrl.setState({ job: null });
  render(at());
  expect(screen.getByText(/실제 진단 작업을 선택/)).toBeInTheDocument();
  expect(apiFetch).not.toHaveBeenCalled();
});

it("실제 allowed+complete intent는 응답 받음이나 송신 없음으로 표시하지 않는다", async () => {
  const f = e2Flow();
  f.counts = {
    attempts: 0,
    allowed: 0,
    denied: 0,
    unsupported: 0,
    forward_attempted: 0,
    responses: 0,
    unknown: 0,
    transient: 0,
    withheld: 0,
    calls: 1,
  };
  f.calls[0].attempts = [
    e2Attempt({
      stage: "intent",
      reason: "complete",
      decision: "allowed",
      forward_attempted: false,
      request_written: false,
      response_received: false,
      response_complete: false,
      status_code: 0,
    }),
  ];
  vi.mocked(apiFetch).mockResolvedValue(f);
  const { container } = render(at());
  await screen.findByText("a".repeat(32));
  expect(screen.getByText(/송신 전 감사\(intent\)만 있습니다/)).toBeInTheDocument();
  expect(container.querySelector('[data-class="allow"]')).toBeNull();
  expect(screen.queryByText(/사유 응답 받음/)).toBeNull();
  expect(screen.queryByText(/요청 쓰기 없음/)).toBeNull();
});
it("실제 연결 시도 true인 TCP/TLS 실패를 송신 실패로 표시한다", async () => {
  const f = e2Flow();
  f.counts = { ...f.counts, responses: 0, transient: 1 };
  f.calls[0].attempts = [
    e2Attempt({
      reason: "transport_failed",
      forward_attempted: true,
      request_written: false,
      response_received: false,
      response_complete: false,
      status_code: 0,
    }),
  ];
  vi.mocked(apiFetch).mockResolvedValue(f);
  const { container } = render(at());
  await screen.findByText("a".repeat(32));
  expect(container.querySelector('[data-class="transient"]')).not.toBeNull();
});
it("more인데 커서가 늘지 않으면 반복을 중단하고 이전 사건을 지운다", async () => {
  vi.useFakeTimers();
  vi.mocked(apiFetch)
    .mockResolvedValueOnce(e2Flow())
    .mockResolvedValueOnce(e2Flow({ more: true }))
    .mockResolvedValue(e2Flow({ last_index: 3, calls: [] }));
  render(at());
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(screen.getByText("a".repeat(32))).toBeInTheDocument();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(2000);
  });
  expect(screen.getByRole("alert")).toHaveTextContent("확인하지 못했습니다");
  expect(screen.queryByText("a".repeat(32))).toBeNull();
  expect(apiFetch).toHaveBeenCalledTimes(2);
});
it("조회 실패 후 이전 사건·커서를 버리고 첫 페이지로 재조회한다", async () => {
  vi.useFakeTimers();
  vi.mocked(apiFetch)
    .mockResolvedValueOnce(e2Flow())
    .mockRejectedValueOnce(new Error("fixed-failure"))
    .mockResolvedValue(e2Flow({ last_index: 3, calls: [] }));
  render(at());
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(2000);
  });
  expect(screen.queryByText("a".repeat(32))).toBeNull();
  expect(screen.getByRole("alert")).toBeInTheDocument();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(2000);
  });
  expect(vi.mocked(apiFetch).mock.calls[2][0]).toBe("/api/tasks/42/e2-flow");
  expect(screen.queryByText("a".repeat(32))).toBeNull();
});
it("task가 바뀐 렌더부터 이전 사건을 숨긴다", async () => {
  vi.mocked(apiFetch)
    .mockResolvedValueOnce(e2Flow())
    .mockImplementation(
      () =>
        new Promise(() => {
          // 재조회 응답을 대기 상태로 유지한다.
        }),
    );
  render(at());
  await screen.findByText("a".repeat(32));
  act(() => useUrl.setState({ job: { task_id: "43" } as never }));
  expect(screen.queryByText("a".repeat(32))).toBeNull();
});

it("숨겨진 채 붙어 있을 때는 e2-flow를 부르지 않는다(1차 리허설 F5: 2초마다 404가 수백 건 쌓였다)", async () => {
  vi.mocked(apiFetch).mockResolvedValue(e2Flow());
  const view = render(at("dashboard"));
  await act(async () => {
    await Promise.resolve();
  });
  expect(apiFetch).not.toHaveBeenCalled();
  view.rerender(at("e2flow"));
  expect(await screen.findByText("a".repeat(32))).toBeInTheDocument();
});

it("첫 진단 의도 전 404는 실패가 아니라 '진단 요청 전'으로 보이고 10초 뒤 다시 본다", async () => {
  vi.useFakeTimers();
  vi.mocked(apiFetch)
    .mockRejectedValueOnce(new ApiError(404, "전송 통제 기록이 아직 없습니다"))
    .mockResolvedValue(e2Flow());
  render(at());
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(screen.getByRole("status")).toHaveTextContent(/진단 요청 전 · 아직 전송 통제 기록이 없습니다/);
  expect(screen.queryByRole("alert")).toBeNull();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(2000);
  });
  expect(apiFetch).toHaveBeenCalledTimes(1); // 2초 간격이 아니라 10초 뒤에
  await act(async () => {
    await vi.advanceTimersByTimeAsync(8000);
  });
  expect(apiFetch).toHaveBeenCalledTimes(2);
  expect(screen.getByText("a".repeat(32))).toBeInTheDocument();
});

it("404가 아닌 실패는 지금처럼 실패로 보인다", async () => {
  vi.mocked(apiFetch).mockRejectedValue(new ApiError(503, "x"));
  render(at());
  expect(await screen.findByRole("alert")).toHaveTextContent(/전송 통제 흐름을 확인하지 못했습니다/);
});

it("저널 없음(none) 응답도 '진단 요청 전'으로 보이고 10초 뒤 다시 본다", async () => {
  vi.useFakeTimers();
  const none = {
    ...e2Flow(),
    journal: "none",
    last_index: 0,
    calls: [],
    counts: Object.fromEntries(Object.keys(e2Flow().counts).map((k) => [k, 0])),
  };
  vi.mocked(apiFetch).mockResolvedValueOnce(none).mockResolvedValue(e2Flow());
  render(at());
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(screen.getByRole("status")).toHaveTextContent(/진단 요청 전 · 아직 전송 통제 기록이 없습니다/);
  expect(screen.queryByRole("alert")).toBeNull();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(2000);
  });
  expect(apiFetch).toHaveBeenCalledTimes(1);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(8000);
  });
  expect(apiFetch).toHaveBeenCalledTimes(2);
  expect(screen.getByText("a".repeat(32))).toBeInTheDocument();
});
