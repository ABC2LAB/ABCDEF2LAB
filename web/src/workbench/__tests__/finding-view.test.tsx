import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { URLDiagnosisJob } from "@/lib/diagnosis/diagnosis-url";
import { running } from "@/lib/diagnosis/diagnosis-url-fixtures";

vi.mock("../live/api", async (original) => ({
  ...(await original<typeof import("../live/api")>()),
  fetchRemediation: vi.fn(),
}));

import { ApiError } from "@/lib/diagnosis/api";

import * as api from "../live/api";
import { clearFindingPick, pickFinding, useFindingPick } from "../live/findingPick";
import { resetUrl, useUrl } from "../live/urlDiagnosis";
import { type Shell, ShellContext, type View } from "../shell";
import { UrlFindingLive } from "../views/live/UrlFindingLive";

const a = vi.mocked(api);
const noop = () => undefined;
const completed: URLDiagnosisJob = {
  ...running,
  state: "completed",
  phase: "report",
  drain_complete: true,
  result_ready: true,
};
const other: URLDiagnosisJob = { ...completed, id: "job-2", task_id: "43" };
function entry(n: number): api.Remediation["entries"][number] {
  return {
    entry_id: `candidate-${n}`,
    title: `후보 ${n}`,
    outcome: "review",
    status: "completed",
    verification: "manual_review_required",
    evidence_digest: "e".repeat(64),
    report_version: "1",
    run: "run-1",
    intent_id: 7,
    observed_at: "2026-11-09T12:10:00Z",
    evidence_version: 1,
    source_digest: "d".repeat(64),
    summary: "다른 계정 응답 후보",
    evidence: "",
    report: "",
    remediation: "",
    available: true,
    unavailable_reason: "",
  };
}
function mount(goTo = vi.fn<(v: View) => void>()) {
  const shell: Shell = {
    route: "finding",
    navSeq: 1,
    narrow: false,
    phone: false,
    reduce: false,
    inspOpen: false,
    railOpen: false,
    railNewAtRoute: false,
    goTo,
    openInsp: noop,
    closeInsp: noop,
    openRail: noop,
    closeRail: noop,
    foldRail: noop,
    dashPick: noop,
  };
  render(
    <ShellContext.Provider value={shell}>
      <UrlFindingLive />
    </ShellContext.Provider>,
  );
  return goTo;
}

beforeEach(() => {
  vi.resetAllMocks();
  resetUrl();
  clearFindingPick();
  useUrl.setState({ job: completed, verified: true });
});
afterEach(cleanup);

describe("UrlFindingLive (단독 발견 상세 · 저장 결과)", () => {
  it("보고서에서 고른 발견을 보고서 상세 페이지와 같은 짜임으로 그리고, 예시는 없다", async () => {
    a.fetchRemediation.mockResolvedValue({ task_id: 42, entries: [entry(1), { ...entry(2), vuln_class: "idor" }] });
    pickFinding("42", "candidate-2");
    const goTo = mount();
    const page = await screen.findByRole("region", { name: "발견 상세 페이지" });
    expect(a.fetchRemediation).toHaveBeenCalledWith("42", "internal");
    expect(screen.getByRole("heading", { level: 2, name: "후보 2" })).toBeInTheDocument();
    expect(page).toHaveTextContent(/유형idor/);
    expect(screen.queryByText(/예시/)).toBeNull();
    // E③ 원문 판정 · 다계정 비교 칸은 두지 않는다(백엔드 후속)
    expect(screen.queryByText(/다계정|교차 계정 비교/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "발견 목록으로" }));
    expect(goTo).toHaveBeenCalledWith("report");
  });

  it("고른 발견이 없으면 부르지 않고 보고서로 안내한다", () => {
    const goTo = mount();
    expect(screen.getByText(/보고서 발견 목록에서 발견을 고르고/)).toBeInTheDocument();
    expect(a.fetchRemediation).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "보고서 발견 목록으로" }));
    expect(goTo).toHaveBeenCalledWith("report");
  });

  it("다른 task 에서 고른 발견은 열지 않는다", () => {
    pickFinding("41", "candidate-2");
    mount();
    expect(screen.getByText(/보고서 발견 목록에서 발견을 고르고/)).toBeInTheDocument();
    expect(a.fetchRemediation).not.toHaveBeenCalled();
  });

  it("고른 발견이 저장 결과에 없으면 다른 발견으로 대신하지 않는다", async () => {
    a.fetchRemediation.mockResolvedValue({ task_id: 42, entries: [entry(1)] });
    pickFinding("42", "candidate-9");
    mount();
    expect(await screen.findByText(/고른 발견이 지금 저장 결과에 없습니다/)).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "발견 상세 페이지" })).toBeNull();
  });

  it.each([403, 404, 503])("저장 결과를 못 읽으면(HTTP %s) 상세를 그리지 않는다", async (status) => {
    a.fetchRemediation.mockRejectedValue(new ApiError(status, "x"));
    pickFinding("42", "candidate-1");
    mount();
    expect(await screen.findByRole("button", { name: /다시/ })).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "발견 상세 페이지" })).toBeNull();
  });

  it("Job 을 바꾸면 고른 발견을 지우고 이전 회차 상세를 보이지 않는다", async () => {
    a.fetchRemediation.mockResolvedValue({ task_id: 42, entries: [entry(1)] });
    pickFinding("42", "candidate-1");
    mount();
    await screen.findByRole("region", { name: "발견 상세 페이지" });
    act(() => useUrl.setState({ job: other }));
    expect(useFindingPick.getState()).toEqual({ task: null, entry: null });
    expect(screen.queryByRole("region", { name: "발견 상세 페이지" })).toBeNull();
    expect(screen.getByText(/보고서 발견 목록에서 발견을 고르고/)).toBeInTheDocument();
  });

  it("Job 이 빠지면(권한 철회 · 재로그인) 상세를 지우고 예시로 채우지 않는다", async () => {
    a.fetchRemediation.mockResolvedValue({ task_id: 42, entries: [entry(1)] });
    pickFinding("42", "candidate-1");
    mount();
    await screen.findByRole("region", { name: "발견 상세 페이지" });
    act(() => useUrl.setState({ job: null }));
    expect(screen.getByText("실제 진단 작업을 선택하세요.")).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "발견 상세 페이지" })).toBeNull();
    expect(screen.queryByText(/예시/)).toBeNull();
  });

  it("결과 저장 전 회차는 읽지 않는다", () => {
    useUrl.setState({ job: { ...completed, state: "running", result_ready: false } });
    pickFinding("42", "candidate-1");
    mount();
    expect(screen.getByText(/결과가 저장되면 발견 상세를 볼 수 있습니다/)).toBeInTheDocument();
    expect(a.fetchRemediation).not.toHaveBeenCalled();
  });
});
