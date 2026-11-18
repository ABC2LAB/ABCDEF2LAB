// 진행 중인 점검 · 끝난 점검 화면 (시안 v54, 점검 기록을 둘로 나눔): 레일에서 점검을 누르면 알맞은 화면으로 오고,
// 화면마다 따로 필터가 있다. 줄은 이 브라우저가 이어 보는 진단 하나뿐이고, 필터는 서버 값
// (진단 상태 · 탐색 단계 · 요청 승인 · 팀 lib 판정)으로만 나눈다.

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../live/api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../live/api")>()),
  fetchRequestApprovals: vi.fn(),
  fetchRemediation: vi.fn(),
}));

import { ApiError } from "@/lib/diagnosis/api";
import type { URLRunSummary } from "@/lib/diagnosis/diagnosis-runs";
import type { URLDiagnosisJob } from "@/lib/diagnosis/diagnosis-url";
import { catalog, handle, running } from "@/lib/diagnosis/diagnosis-url-fixtures";

import * as api from "../live/api";
import { LiveRailTasks } from "../live/rail";
import { resetUrl, useUrl } from "../live/urlDiagnosis";
import { useUrlRuns } from "../live/urlRunList";
import { type Shell, ShellContext, type View } from "../shell";
import { recDoneKinds, recRunKind, UrlFinishedLive, UrlRunningLive } from "../views/live/urlRecords";

const noop = () => undefined;
const shell = (route: View, over: Partial<Shell> = {}): Shell => ({
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
  ...over,
});

const completed = {
  ...running,
  revision: 7,
  state: "completed" as const,
  phase: "report" as const,
  drain_complete: true,
  result_ready: true,
  outcome: "complete" as const,
};
const zero = { confirmed: 0, review: 0, not_executed: 0, unknown: 0, negative: 0 };

/** 후보 한 건 (팀 diagnosticOutcome 이 review 로 분류하는 모양). */
function candidate(n: number): api.Remediation["entries"][number] {
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

/** 칩 (이름은 글 + 개수). */
const chip = (region: HTMLElement, text: string) =>
  within(region).getByRole("button", { name: new RegExp(`^${text}\\s*(\\d+|—)$`) });

beforeEach(() => {
  vi.resetAllMocks();
  resetUrl({ kind: "v2", catalog });
});
afterEach(() => cleanup());

describe("진행 중 · 끝난 점검 묶음 계산", () => {
  it("진행 중: 승인 대기 → 범위 확인 대기 → 점검 중, 확인 못 했거나 어느 것도 아니면 null", () => {
    expect(recRunKind("running", true, "diagnosing", 0)).toBe("run");
    expect(recRunKind("preparing", true, null, 0)).toBe("run");
    expect(recRunKind("awaiting_approval", true, null, 0)).toBe("wait");
    expect(recRunKind("running", true, "diagnosing", 2)).toBe("wait");
    expect(recRunKind("running", true, "awaiting_selection", 0)).toBe("scope");
    expect(recRunKind("running", false, "diagnosing", 0)).toBeNull();
    expect(recRunKind("unknown", true, null, 0)).toBeNull();
    expect(recRunKind("ready", true, null, 0)).toBeNull();
  });

  it("끝난 점검: 끝까지 마친(실행 마감, completed) 점검만 팀 lib 판정 집계로 나눈다 — 발견 없음은 정상 대조 결과가 있을 때만", () => {
    expect(recDoneKinds("completed", null, "complete")).toEqual([]);
    expect(recDoneKinds("completed", { ...zero, negative: 3 }, "complete")).toEqual(["clean"]);
    // 계획한 작업 일부가 끝나지 않은 실행(partial)은 정상 대조 결과만 있어도 발견 없음이 아니다 (절대규칙 5, #440)
    expect(recDoneKinds("completed", { ...zero, negative: 3 }, "partial")).toEqual([]);
    expect(recDoneKinds("completed", { ...zero, negative: 3 })).toEqual([]);
    // 발견 · 검토 · 판단 불가 묶음은 실행 결과와 상관없이 판정 기록 그대로
    expect(recDoneKinds("completed", { ...zero, confirmed: 1, negative: 3 }, "partial")).toEqual(["hit"]);
    expect(recDoneKinds("completed", { ...zero, unknown: 1 }, "partial")).toEqual(["und"]);
    expect(recDoneKinds("completed", { ...zero, confirmed: 1 })).toEqual(["hit"]);
    expect(recDoneKinds("completed", { ...zero, review: 2 })).toEqual(["review"]);
    expect(recDoneKinds("completed", { ...zero, not_executed: 1 })).toEqual(["und"]);
    expect(recDoneKinds("completed", { ...zero, confirmed: 1, unknown: 1 })).toEqual(["hit", "und"]);
    // 판정 기록이 하나도 없으면 '검사했고 정상' 의 근거가 아니다 (절대규칙 5)
    expect(recDoneKinds("completed", zero)).toEqual([]);
    // 중지 · 실패는 보고서가 있어도 나누지 않는다 (끝까지 보지 않음)
    expect(recDoneKinds("cancelled", { ...zero, negative: 3 })).toEqual([]);
    expect(recDoneKinds("failed", { ...zero, confirmed: 1 })).toEqual([]);
  });
});

// 목록에는 판정이 없으므로 상태 필터만 사용한다. 판정은 선택한 회차의 실제 보고서에 둔다.
const projection = (job: URLDiagnosisJob): URLRunSummary => ({
  id: job.id,
  revision: job.revision,
  state: job.state,
  phase: job.phase,
  task_id: job.task_id,
  started: job.started,
  drain_complete: job.drain_complete,
  result_ready: job.result_ready,
  report_available: job.result_ready && !!job.task_id,
  approval_status: "not_revalidated",
  action_required: true,
});
function savedRows(jobs: URLDiagnosisJob[]) {
  useUrlRuns.setState({
    owner: catalog.ownerContext,
    page: { snapshot: "c".repeat(64), limit: 25, more: false, runs: jobs.map(projection) },
  });
}
function selectSaved(job: URLDiagnosisJob) {
  savedRows([job]);
  useUrl.setState({ handle, job, selectedID: job.id, verified: true });
}
function records(finished = false) {
  return render(
    <ShellContext.Provider value={shell(finished ? "finished" : "running")}>
      {finished ? <UrlFinishedLive /> : <UrlRunningLive />}
    </ShellContext.Provider>,
  );
}
describe("서버 보존 목록의 상태 필터와 선택한 보고서", () => {
  it("여러 서버 회차를 상태로 거르고 목록에 없는 URL·진행률·판정을 만들지 않는다", () => {
    savedRows([
      running,
      { ...running, id: "job-2", state: "preparing", started: false },
      { ...running, id: "job-3", state: "unknown" },
    ]);
    records();
    const region = screen.getByRole("region", { name: "진행 중인 점검 목록" });
    expect(region.querySelectorAll("[data-url-run-id]")).toHaveLength(3);
    expect(chip(region, "엔진 실행")).toHaveTextContent("1");
    fireEvent.click(chip(region, "상태 확인 필요"));
    expect(region.querySelectorAll("[data-url-run-id]")).toHaveLength(1);
    expect(within(region).getByRole("button", { name: /job-3/ })).toHaveTextContent("결과 불명");
    expect(region).not.toHaveTextContent(/target\.example|%|취약점 없음/);
    expect(api.fetchRemediation).not.toHaveBeenCalled();
  });
  it.each([
    "completed",
    "failed",
    "cancelled",
  ] as const)("%s도 서버 목록에 남고 보고서를 자동 판정하지 않는다", (state) => {
    savedRows([{ ...completed, state }]);
    records(true);
    expect(screen.getByRole("button", { name: /job-1/ })).toBeInTheDocument();
    expect(api.fetchRemediation).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: /^발견 없음/ })).toBeNull();
  });
  it("선택한 완료 회차의 저장 보고서만 조회하고 상세에서 후보를 표시한다", async () => {
    vi.mocked(api.fetchRemediation).mockResolvedValue({ task_id: 42, entries: [candidate(1)] });
    selectSaved(completed);
    records(true);
    await waitFor(() => expect(api.fetchRemediation).toHaveBeenCalledWith("42", "internal"));
    const detail = screen.getByRole("complementary", { name: "선택한 점검" });
    await waitFor(() => expect(detail).toHaveTextContent("검토 필요"));
    const row = screen.getByRole("button", { name: /job-1/ });
    expect(row).not.toHaveTextContent(/발견 1|취약점/);
  });
  it.each(["cancelled", "completed"] as const)("%s의 판정 0건은 취약점 없음이 아니다", async (state) => {
    vi.mocked(api.fetchRemediation).mockResolvedValue({ task_id: 42, entries: [] });
    selectSaved({ ...completed, state });
    records(true);
    await waitFor(() => expect(api.fetchRemediation).toHaveBeenCalled());
    expect(screen.getByRole("complementary", { name: "선택한 점검" })).not.toHaveTextContent("취약점 없음");
  });
  it("부분 완료 회차의 정상 대조를 안전 판정으로 승격하지 않는다", async () => {
    vi.mocked(api.fetchRemediation).mockResolvedValue({ task_id: 42, entries: [] });
    selectSaved({
      ...completed,
      outcome: "partial",
      intent_outcomes: { done: 1, incomplete: 1, stopped: 0, unknown: 0, policy_limited: 0 },
    });
    records(true);
    // 오른쪽 패널 실행 결과는 일부만 마감한 회차도 '점검 완료'만 적는다(사용자 11/18 결정 — 남은 작업 · 결과 불명은 점검 진행
    // 상태 카드에 남는다). 그래도 정상 대조를 '발견 없음'으로 올리지는 않는다(절대규칙 5)
    await waitFor(() =>
      expect(screen.getByRole("complementary", { name: "선택한 점검" })).toHaveTextContent("실행 결과점검 완료"),
    );
    expect(screen.queryByRole("button", { name: /^발견 없음/ })).toBeNull();
    // 패널 머리 글엔 '끝난 점검'을 적지 않고 대상 주소만, 서버 작업 번호는 '작업 #n' — 목록 줄에도 같은 번호(사용자 11/18)
    const panel = screen.getByRole("complementary", { name: "선택한 점검" });
    expect(panel).not.toHaveTextContent("끝난 점검");
    expect(panel).not.toHaveTextContent("실제 task");
    expect(panel).toHaveTextContent(`작업#${completed.task_id}`);
    // 목록 줄은 이름(prep-…) 왼쪽에 번호만 — 아래 작은 글에 '작업 #n'을 따로 적지 않는다(사용자 11/18)
    const row = screen.getByRole("button", { name: new RegExp(`^작업\\s*${completed.task_id}\\s*${completed.id}`) });
    expect(row).not.toHaveTextContent("작업 #");
  });
  it("결과 저장 미확인 회차는 보고서 조회를 하지 않는다", () => {
    selectSaved({ ...completed, result_ready: false });
    records(true);
    expect(api.fetchRemediation).not.toHaveBeenCalled();
  });
  it("보고서 서비스 장애는 빈 결과로 위장하지 않는다", async () => {
    vi.mocked(api.fetchRemediation).mockRejectedValue(new ApiError(503, "unavailable"));
    selectSaved(completed);
    records(true);
    expect(await screen.findByRole("alert")).toHaveTextContent("보고서 조회 실패 (HTTP 503)");
    expect(screen.queryByText("취약점 없음")).toBeNull();
  });
  it("목록이 검증된 빈 페이지일 때에만 빈 안내를 표시한다", () => {
    savedRows([]);
    records();
    expect(screen.getByText("이 상태의 점검이 없습니다.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /새 점검/ })).toHaveAttribute("href", "#new");
  });
  it("활성 회차와 완료 회차의 목록을 구별한다", () => {
    savedRows([running, { ...completed, id: "done-1" }]);
    records(true);
    expect(screen.queryByRole("button", { name: /job-1/ })).toBeNull();
    expect(screen.getByRole("button", { name: /done-1/ })).toBeInTheDocument();
  });
});

describe("레일 → 진행 중인 점검 · 끝난 점검", () => {
  it("진행 중 점검 줄을 누르면 진행 중인 점검으로 가고, 그 화면을 보는 동안 현재 표시를 단다", () => {
    useUrl.setState({ handle, job: running, verified: true });
    const goTo = vi.fn<(v: View) => void>();
    const { rerender } = render(
      <ShellContext.Provider value={shell("dashboard", { goTo })}>
        <LiveRailTasks />
      </ShellContext.Provider>,
    );
    const row = screen.getByRole("button", { name: /target\.example/ });
    expect(row).not.toHaveAttribute("aria-current");
    fireEvent.click(row);
    expect(goTo).toHaveBeenCalledWith("running");
    rerender(
      <ShellContext.Provider value={shell("running", { goTo })}>
        <LiveRailTasks />
      </ShellContext.Provider>,
    );
    expect(screen.getByRole("button", { name: /target\.example/ })).toHaveAttribute("aria-current", "true");
    // 끝난 점검 화면은 이 점검의 목록이 아니다
    rerender(
      <ShellContext.Provider value={shell("finished", { goTo })}>
        <LiveRailTasks />
      </ShellContext.Provider>,
    );
    expect(screen.getByRole("button", { name: /target\.example/ })).not.toHaveAttribute("aria-current");
  });

  it("끝난 점검 줄을 누르면 끝난 점검으로 간다", () => {
    useUrl.setState({ handle, job: { ...completed, state: "cancelled", result_ready: false }, verified: true });
    const goTo = vi.fn<(v: View) => void>();
    render(
      <ShellContext.Provider value={shell("finished", { goTo })}>
        <LiveRailTasks />
      </ShellContext.Provider>,
    );
    const row = screen.getByRole("button", { name: /target\.example/ });
    expect(row).toHaveAttribute("aria-current", "true");
    fireEvent.click(row);
    expect(goTo).toHaveBeenCalledWith("finished");
  });
});
