import "@testing-library/jest-dom/vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { apiFetch } from "@/lib/diagnosis/api";
import { coverageJob, coverageSnapshot } from "@/lib/diagnosis/coverage-fixtures";
import { resetUrl, useUrl } from "../live/urlDiagnosis";
import type { WorkflowSnapshot } from "../live/workflow";
import { UrlCoverageLive } from "../views/live/UrlCoverageLive";
vi.mock("@/lib/diagnosis/api", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/api")>()),
  apiFetch: vi.fn(),
}));
function publish(snapshot = coverageSnapshot().workflow) {
  useUrl.setState({
    job: coverageJob,
    flow: snapshot as unknown as WorkflowSnapshot,
    flowVerified: true,
    flowError: "",
    flowUncertain: false,
  });
}
beforeEach(() => {
  vi.resetAllMocks();
  resetUrl();
  publish();
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});
it("공유 workflow의 끝점·방문·선택·관찰·출처·누계를 추가 조회 없이 표시한다", () => {
  render(<UrlCoverageLive />);
  expect(screen.getByText("GET /observed")).toBeInTheDocument();
  expect(screen.getByText(/브라우저 요청/)).toBeInTheDocument();
  expect(screen.getByText(/선택됨/)).toBeInTheDocument();
  expect(screen.getByText(/응답 본문 불완전/)).toBeInTheDocument();
  expect(screen.getByText(/관찰 갱신판 7/)).toBeInTheDocument();
  expect(screen.queryByText(/예시 데이터/)).toBeNull();
  expect(apiFetch).not.toHaveBeenCalled();
});
it("미시도 점선과 관찰 실패를 구분하고 안전·확정으로 올리지 않는다", () => {
  const s = coverageSnapshot().workflow;
  Object.assign(s.endpoints[2], { visited: false, observation_error: "fetch_failed" });
  publish(s);
  const { container } = render(<UrlCoverageLive />);
  const untested = container.querySelector('[data-status="untested"]') as HTMLElement;
  expect(untested.style.background).toBe("transparent");
  expect(untested.style.borderStyle).toBe("dashed");
  expect(screen.getByText("가져오기 실패")).toBeInTheDocument();
  expect(container.querySelector('[data-status="undecidable"]')).not.toBeNull();
  expect(container.querySelector('[data-status="confirmed"]')).toBeNull();
});
it("공유 조회 실패 시 이전 관찰을 숨기고 자체 폴링/목업으로 바꾸지 않는다", () => {
  render(<UrlCoverageLive />);
  expect(screen.getByText("GET /observed")).toBeInTheDocument();
  act(() => useUrl.setState({ flowVerified: false, flowError: "fixed-unavailable" }));
  expect(screen.getByText(/커버리지를 불러오지 못했습니다/)).toBeInTheDocument();
  expect(screen.queryByText("GET /observed")).toBeNull();
  expect(apiFetch).not.toHaveBeenCalled();
});
it("방문·선택·출처·누계 누락은 불명/미시도로 표시한다", () => {
  const s = coverageSnapshot().workflow;
  s.endpoints = [{ id: "missing" }] as never;
  delete (s as { counts?: unknown }).counts;
  publish(s);
  render(<UrlCoverageLive />);
  expect(screen.getByText(/UNKNOWN 경로 불명/)).toBeInTheDocument();
  expect(screen.getByText(/출처 불명/)).toBeInTheDocument();
  expect(screen.getByText(/방문 불명/)).toBeInTheDocument();
  expect(screen.getByText(/선택 불명/)).toBeInTheDocument();
});
it("job 전환과 늦은 이전 flow를 같은 렌더에서 차단한다", () => {
  render(<UrlCoverageLive />);
  expect(screen.getByText("GET /observed")).toBeInTheDocument();
  act(() => useUrl.setState({ job: { ...coverageJob, id: "new-job", task_id: "43" } }));
  expect(screen.queryByText("GET /observed")).toBeNull();
  act(() => useUrl.setState({ flow: coverageSnapshot().workflow as unknown as WorkflowSnapshot, flowVerified: true }));
  expect(screen.queryByText("GET /observed")).toBeNull();
});
it("같은 binding의 job 객체가 갱신돼도 조회를 취소하거나 다시 시작하지 않는다", async () => {
  vi.useFakeTimers();
  render(<UrlCoverageLive />);
  for (let i = 0; i < 5; i++) {
    await act(async () => {
      useUrl.setState({ job: { ...coverageJob, revision: coverageJob.revision + i } });
      await vi.advanceTimersByTimeAsync(2000);
    });
  }
  expect(screen.getByText("GET /observed")).toBeInTheDocument();
  expect(apiFetch).not.toHaveBeenCalled();
});
it("서버 누계 불일치는 숨기지 않고 경고한다", () => {
  const s = coverageSnapshot().workflow;
  s.counts.found = 0;
  publish(s);
  render(<UrlCoverageLive />);
  expect(screen.getByRole("alert")).toHaveTextContent("집계 불일치");
});
it("내보내기·공유 버튼을 제공하지 않는다", () => {
  render(<UrlCoverageLive />);
  for (const el of [...screen.queryAllByRole("button"), ...screen.queryAllByRole("link")])
    expect(el.textContent).not.toMatch(/내보내기|공유|export/i);
});
it("실제 workflow가 없으면 API나 예시를 만들지 않는다", () => {
  useUrl.setState({ job: null, flow: null, flowVerified: false });
  render(<UrlCoverageLive />);
  expect(screen.getByText(/실제 워크플로 진단을 선택/)).toBeInTheDocument();
  expect(apiFetch).not.toHaveBeenCalled();
});
