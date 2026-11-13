import "@testing-library/jest-dom/vitest";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { apiFetch } from "@/lib/diagnosis/api";
import { e2Attempt, e2Flow } from "@/lib/diagnosis/e2flow-fixtures";
import { useUrl, resetUrl } from "../live/urlDiagnosis";
import { UrlE2FlowLive } from "../views/live/UrlE2FlowLive";
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
  vi.mocked(apiFetch).mockResolvedValue(e2Flow({ omitted_calls: 7 }));
  render(<UrlE2FlowLive />);
  expect(await screen.findByText("a".repeat(32))).toBeInTheDocument();
  expect(screen.getByText("GET /orders/{id}")).toBeInTheDocument();
  expect(screen.getByText(/이전 호출 7개 생략/)).toBeInTheDocument();
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
  render(<UrlE2FlowLive />);
  expect(await screen.findByText("GET 경로 없음")).toBeInTheDocument();
  expect(screen.getAllByText("a".repeat(32))).toHaveLength(1);
  expect(vi.mocked(apiFetch).mock.calls[1][0]).toBe("/api/tasks/42/e2-flow?after=10");
});
it("송신 뒤 감사 실패는 차단 성공으로 보이지 않는다", async () => {
  const f = e2Flow();
  f.calls[0].attempts = [e2Attempt({ decision: "denied", reason: "audit_failed", path_pattern: "" })];
  vi.mocked(apiFetch).mockResolvedValue(f);
  const { container } = render(<UrlE2FlowLive />);
  await screen.findByText("a".repeat(32));
  expect(container.querySelector('[data-class="unknown"]')).not.toBeNull();
  expect(container.querySelector('[data-class="deny"]')).toBeNull();
});
it("저널 검증 실패는 이미 보인 사건도 제거한다", async () => {
  vi.useFakeTimers();
  vi.mocked(apiFetch)
    .mockResolvedValueOnce(e2Flow())
    .mockResolvedValue(e2Flow({ journal: "invalid" }));
  render(<UrlE2FlowLive />);
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
  let release: (value: unknown) => void = () => {};
  vi.mocked(apiFetch).mockImplementation((path) =>
    String(path).includes("/42/")
      ? new Promise((resolve) => {
          release = resolve;
        })
      : (Promise.resolve(e2Flow({ calls: [] })) as never),
  );
  render(<UrlE2FlowLive />);
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1));
  act(() => useUrl.setState({ job: { task_id: "43" } as never }));
  await screen.findByText(/기록된 호출이 없습니다/);
  await act(async () => release(e2Flow()));
  expect(screen.queryByText("a".repeat(32))).toBeNull();
  expect(vi.mocked(apiFetch).mock.calls[1][0]).toBe("/api/tasks/43/e2-flow");
});
it("실제 작업 없이는 호출이나 예시 데이터가 없다", () => {
  useUrl.setState({ job: null });
  render(<UrlE2FlowLive />);
  expect(screen.getByText(/실제 진단 작업을 선택/)).toBeInTheDocument();
  expect(apiFetch).not.toHaveBeenCalled();
});
