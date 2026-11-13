import "@testing-library/jest-dom/vitest";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { apiFetch } from "@/lib/diagnosis/api";
import { coverageJob, coverageSnapshot } from "@/lib/diagnosis/coverage-fixtures";
import { resetUrl, useUrl } from "../live/urlDiagnosis";
import { UrlCoverageLive } from "../views/live/UrlCoverageLive";
vi.mock("@/lib/diagnosis/api", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/api")>()),
  apiFetch: vi.fn(),
}));
beforeEach(() => {
  vi.resetAllMocks();
  resetUrl();
  useUrl.setState({ job: coverageJob });
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});
it("실제 workflow 끝점·방문·선택·관찰·출처·누계를 표시한다", async () => {
  vi.mocked(apiFetch).mockResolvedValue(coverageSnapshot());
  render(<UrlCoverageLive />);
  expect(await screen.findByText("GET /observed")).toBeInTheDocument();
  expect(screen.getByText(/브라우저 요청/)).toBeInTheDocument();
  expect(screen.getByText(/선택됨/)).toBeInTheDocument();
  expect(screen.getByText(/응답 본문 불완전/)).toBeInTheDocument();
  expect(screen.getByText(/관찰 갱신판 7/)).toBeInTheDocument();
  expect(screen.getByText(/방문과 HTTP 응답은 취약점 검사 완료나 E③ 판정을 뜻하지 않습니다/)).toBeInTheDocument();
  expect(screen.queryByText(/예시 데이터/)).toBeNull();
  expect(vi.mocked(apiFetch).mock.calls[0][0]).toBe(`/api/product/workflows/${coverageJob.id}`);
});
it("미시도를 점선으로 표시하고 안전·확정 색으로 승격하지 않는다", async () => {
  vi.mocked(apiFetch).mockResolvedValue(coverageSnapshot());
  const { container } = render(<UrlCoverageLive />);
  await screen.findByText("POST /unvisited");
  const untested = container.querySelector('[data-status="untested"]') as HTMLElement;
  expect(untested.style.background).toBe("transparent");
  expect(untested.style.borderStyle).toBe("dashed");
  expect(container.querySelector('[data-status="confirmed"]')).toBeNull();
});
it("조회 실패는 이전 관찰도 지우고 성공·목업으로 바꾸지 않는다", async () => {
  vi.useFakeTimers();
  vi.mocked(apiFetch).mockResolvedValueOnce(coverageSnapshot()).mockRejectedValue(new Error("fixed-failure"));
  render(<UrlCoverageLive />);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(screen.getByText("GET /observed")).toBeInTheDocument();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(2000);
  });
  expect(screen.getByText(/커버리지를 불러오지 못했습니다/)).toBeInTheDocument();
  expect(screen.queryByText("GET /observed")).toBeNull();
  expect(screen.queryByText(/예시 데이터/)).toBeNull();
});
it("누락된 정보는 방문·선택·출처·누계 불명으로 보인다", async () => {
  const s = coverageSnapshot();
  s.workflow.endpoints = [{ id: "missing" }] as never;
  delete (s.workflow as { counts?: unknown }).counts;
  vi.mocked(apiFetch).mockResolvedValue(s);
  render(<UrlCoverageLive />);
  await screen.findByText(/UNKNOWN 경로 불명/);
  expect(screen.getByText(/출처 불명/)).toBeInTheDocument();
  expect(screen.getByText(/방문 불명/)).toBeInTheDocument();
  expect(screen.getByText(/선택 불명/)).toBeInTheDocument();
});
it("회차 변경 뒤 늦은 응답을 새 회차에 그리지 않는다", async () => {
  let release: (v: unknown) => void = () => {};
  vi.mocked(apiFetch).mockImplementation((path) =>
    String(path).endsWith(coverageJob.id)
      ? new Promise((resolve) => {
          release = resolve;
        })
      : (Promise.reject(new Error("workflow_not_started")) as never),
  );
  render(<UrlCoverageLive />);
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1));
  act(() => useUrl.setState({ job: { ...coverageJob, id: "new-job", task_id: "43" } }));
  await screen.findByText(/커버리지를 불러오지 못했습니다/);
  await act(async () => release(coverageSnapshot()));
  expect(screen.queryByText("GET /observed")).toBeNull();
});
it("실제 workflow가 없으면 API·예시를 요청하지 않는다", () => {
  useUrl.setState({ job: null });
  render(<UrlCoverageLive />);
  expect(screen.getByText(/실제 워크플로 진단을 선택/)).toBeInTheDocument();
  expect(apiFetch).not.toHaveBeenCalled();
});
