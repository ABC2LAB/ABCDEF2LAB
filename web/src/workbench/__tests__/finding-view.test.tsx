import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
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
function shellAt(route: View, goTo: (v: View) => void): Shell {
  return {
    route,
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
}
function mount(goTo = vi.fn<(v: View) => void>()) {
  const view = (route: View) => (
    <ShellContext.Provider value={shellAt(route, goTo)}>
      <UrlFindingLive />
    </ShellContext.Provider>
  );
  const r = render(view("finding"));
  return Object.assign(goTo, { show: (route: View) => r.rerender(view(route)) });
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
    const page = await screen.findByRole("region", { name: "취약점 상세 페이지" });
    expect(a.fetchRemediation).toHaveBeenCalledWith("42", "internal");
    expect(screen.getByRole("heading", { level: 2, name: "후보 2" })).toBeInTheDocument();
    expect(page).toHaveTextContent(/유형idor/);
    expect(screen.queryByText(/예시/)).toBeNull();
    // E③ 원문 판정 · 다계정 비교 칸은 두지 않는다(백엔드 후속)
    expect(screen.queryByText(/다계정|교차 계정 비교/)).toBeNull();
    // 목록으로 갈 때 공용 선택을 지운다(남으면 보고서가 같은 상세를 다시 연다)
    fireEvent.click(screen.getByRole("button", { name: "취약점 목록으로 돌아가기" }));
    expect(goTo).toHaveBeenCalledWith("report");
    expect(useFindingPick.getState()).toEqual({ task: null, entry: null });
  });

  it("같은 점검의 상세에 다시 들어오면 새 응답 전엔 직전 결과를 보이지 않는다", async () => {
    a.fetchRemediation.mockResolvedValueOnce({ task_id: 42, entries: [entry(1)] });
    pickFinding("42", "candidate-1");
    const view = mount();
    await screen.findByRole("region", { name: "취약점 상세 페이지" });
    act(() => view.show("report"));
    let answer: (v: api.Remediation) => void = noop;
    a.fetchRemediation.mockReturnValueOnce(
      new Promise((r) => {
        answer = r;
      }),
    );
    act(() => view.show("finding"));
    expect(screen.getByText("불러오는 중…")).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "취약점 상세 페이지" })).toBeNull();
    expect(screen.queryByRole("button", { name: /보안 조치/ })).toBeNull();
    await act(async () => answer({ task_id: 42, entries: [{ ...entry(1), title: "후보 1 새 결과" }] }));
    expect(screen.getByRole("heading", { level: 2, name: "후보 1 새 결과" })).toBeInTheDocument();
    expect(a.fetchRemediation).toHaveBeenCalledTimes(2);
  });

  it("앞선 성공과 다시 들어온 때가 같은 밀리초여도 직전 상세를 보이지 않는다", async () => {
    const now = vi.spyOn(Date, "now").mockReturnValue(1_800_000_000_000); // 시계를 고정
    a.fetchRemediation.mockResolvedValueOnce({ task_id: 42, entries: [entry(1)] });
    pickFinding("42", "candidate-1");
    const view = mount();
    await screen.findByRole("region", { name: "취약점 상세 페이지" });
    act(() => view.show("report"));
    a.fetchRemediation.mockReturnValueOnce(new Promise(() => undefined));
    act(() => view.show("finding"));
    expect(screen.getByText("불러오는 중…")).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "취약점 상세 페이지" })).toBeNull();
    expect(screen.queryByRole("button", { name: /보안 조치/ })).toBeNull();
    now.mockRestore();
  });

  it("직전 진입의 읽기 실패(403)도 다시 들어온 뒤 새 응답 전엔 보이지 않는다", async () => {
    a.fetchRemediation.mockRejectedValueOnce(new ApiError(403, "x"));
    pickFinding("42", "candidate-1");
    const view = mount();
    expect(await screen.findByRole("button", { name: /다시/ })).toBeInTheDocument();
    act(() => view.show("report"));
    let answer: (v: api.Remediation) => void = noop;
    a.fetchRemediation.mockReturnValueOnce(
      new Promise((r) => {
        answer = r;
      }),
    );
    act(() => view.show("finding"));
    expect(screen.getByText("불러오는 중…")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /다시/ })).toBeNull();
    await act(async () => answer({ task_id: 42, entries: [entry(1)] }));
    expect(screen.getByRole("region", { name: "취약점 상세 페이지" })).toBeInTheDocument();
  });

  it("읽기 실패 뒤 '다시 시도'는 같은 진입에서 다시 읽는다", async () => {
    a.fetchRemediation
      .mockRejectedValueOnce(new ApiError(503, "x"))
      .mockResolvedValueOnce({ task_id: 42, entries: [entry(1)] });
    pickFinding("42", "candidate-1");
    mount();
    fireEvent.click(await screen.findByRole("button", { name: /다시/ }));
    expect(await screen.findByRole("region", { name: "취약점 상세 페이지" })).toBeInTheDocument();
    expect(a.fetchRemediation).toHaveBeenCalledTimes(2);
  });

  it("'증거 / PoC' 칸 맨 밑 'PoC 복사'는 단계 · 요청 · 응답 증거를 화면에 보이는 그대로 복사한다", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
    const step = {
      task_id: 42,
      entry_id: "candidate-1",
      id: `step-${"b".repeat(24)}`,
      number: 1,
      title: "계정 B로 계정 A 주문 조회",
      source_step_id: "cross-read",
      run_ref: "run-1",
      call_ref: "",
      evidence_ref: "cross-proof",
      evidence_version: 1,
      evidence_digest: "e".repeat(64),
      binding_digest: "b".repeat(64),
      observed_at: "2026-11-09T12:10:00Z",
      method: "GET",
      path: "/api/orders/2",
      prerequisites: "시험 계정 A · B",
      observation: "200 응답에 계정 A 주문",
      truncated: false,
      capture: { status: "not_captured" as const },
    };
    a.fetchRemediation.mockResolvedValue({
      task_id: 42,
      entries: [
        {
          ...entry(1),
          steps: [step],
          evidence: JSON.stringify({ status: 200, headers: { Authorization: "Bearer synthetic-poc-token" } }),
        },
      ],
    });
    pickFinding("42", "candidate-1");
    mount();
    const page = await screen.findByRole("region", { name: "취약점 상세 페이지" });
    fireEvent.click(within(page).getByRole("button", { name: "PoC 복사" }));
    expect(await within(page).findByText("PoC 를 복사했습니다.")).toBeInTheDocument();
    const text = writeText.mock.calls[0][0] as string;
    expect(text).toMatch(/^저장된 근거 — 후보 1\n\[위험도 미평가 · 검토 필요\] ID candidate-1/);
    expect(text).toContain("이 기록은 취약점 확정이 아닙니다.");
    expect(text).toContain(
      "요청 1 · 계정 B로 계정 A 주문 조회\nGET /api/orders/2\n사전 조건: 시험 계정 A · B\n관찰: 200 응답에 계정 A 주문",
    );
    expect(text).toContain("요청 시도 · 응답 관찰:");
    // 값은 가리지 않는다(DLP 폐기, CLAUDE.md 절대규칙 2) — 스크린샷 안내 글은 없다
    expect(text).toContain("synthetic-poc-token");
    expect(text).not.toContain("스크린샷");
    vi.unstubAllGlobals();
  });

  it("고른 발견이 없으면 부르지 않고 보고서로 안내한다", () => {
    const goTo = mount();
    expect(screen.getByText(/보고서 취약점 목록에서 취약점을 고르고/)).toBeInTheDocument();
    expect(a.fetchRemediation).not.toHaveBeenCalled();
    pickFinding("41", "candidate-1"); // 다른 task 에서 고른 것도 목록으로 가며 지운다
    fireEvent.click(screen.getByRole("button", { name: "취약점 목록으로 돌아가기" }));
    expect(goTo).toHaveBeenCalledWith("report");
    expect(useFindingPick.getState()).toEqual({ task: null, entry: null });
  });

  it("다른 task 에서 고른 발견은 열지 않는다", () => {
    pickFinding("41", "candidate-2");
    mount();
    expect(screen.getByText(/보고서 취약점 목록에서 취약점을 고르고/)).toBeInTheDocument();
    expect(a.fetchRemediation).not.toHaveBeenCalled();
  });

  it("고른 발견이 저장 결과에 없으면 다른 발견으로 대신하지 않는다", async () => {
    a.fetchRemediation.mockResolvedValue({ task_id: 42, entries: [entry(1)] });
    pickFinding("42", "candidate-9");
    mount();
    expect(await screen.findByText(/고른 취약점이 지금 저장 결과에 없습니다/)).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "취약점 상세 페이지" })).toBeNull();
  });

  it.each([403, 404, 503])("저장 결과를 못 읽으면(HTTP %s) 상세를 그리지 않는다", async (status) => {
    a.fetchRemediation.mockRejectedValue(new ApiError(status, "x"));
    pickFinding("42", "candidate-1");
    mount();
    expect(await screen.findByRole("button", { name: /다시/ })).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "취약점 상세 페이지" })).toBeNull();
  });

  it("Job 을 바꾸면 고른 발견을 지우고 이전 회차 상세를 보이지 않는다", async () => {
    a.fetchRemediation.mockResolvedValue({ task_id: 42, entries: [entry(1)] });
    pickFinding("42", "candidate-1");
    mount();
    await screen.findByRole("region", { name: "취약점 상세 페이지" });
    act(() => useUrl.setState({ job: other }));
    expect(useFindingPick.getState()).toEqual({ task: null, entry: null });
    expect(screen.queryByRole("region", { name: "취약점 상세 페이지" })).toBeNull();
    expect(screen.getByText(/보고서 취약점 목록에서 취약점을 고르고/)).toBeInTheDocument();
  });

  it("Job 이 빠지면(권한 철회 · 재로그인) 상세를 지우고 예시로 채우지 않는다", async () => {
    a.fetchRemediation.mockResolvedValue({ task_id: 42, entries: [entry(1)] });
    pickFinding("42", "candidate-1");
    mount();
    await screen.findByRole("region", { name: "취약점 상세 페이지" });
    act(() => useUrl.setState({ job: null }));
    expect(screen.getByText("실제 진단 작업을 선택하세요.")).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "취약점 상세 페이지" })).toBeNull();
    expect(screen.queryByText(/예시/)).toBeNull();
  });

  it("결과 저장 전 회차는 읽지 않는다", () => {
    useUrl.setState({ job: { ...completed, state: "running", result_ready: false } });
    pickFinding("42", "candidate-1");
    mount();
    expect(screen.getByText(/결과가 저장되면 취약점 상세를 볼 수 있습니다/)).toBeInTheDocument();
    expect(a.fetchRemediation).not.toHaveBeenCalled();
  });
});
