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
vi.mock("../live/runDelete", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../live/runDelete")>()),
  deleteURLRun: vi.fn(),
}));
vi.mock("../live/urlRunList", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../live/urlRunList")>()),
  loadUrlRuns: vi.fn(),
}));

import { ApiError } from "@/lib/diagnosis/api";
import { activeURLRun, type URLRunSummary } from "@/lib/diagnosis/diagnosis-runs";
import type { URLDiagnosisJob } from "@/lib/diagnosis/diagnosis-url";
import { catalog, handle, running } from "@/lib/diagnosis/diagnosis-url-fixtures";

import * as api from "../live/api";
import { LiveRailTasks } from "../live/rail";
import { deleteURLRun } from "../live/runDelete";
import { resetUrl, useUrl } from "../live/urlDiagnosis";
import { loadUrlRuns, useUrlRuns } from "../live/urlRunList";
import { type Shell, ShellContext, type View } from "../shell";
import { RunRow } from "../views/live/runRows";
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
  action_required: activeURLRun(job) || ["awaiting_approval", "failed", "unknown"].includes(job.state),
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
it.each([
  ["completed", "expired", "점검 완료", "승인 만료"],
  ["awaiting_approval", "not_revalidated", "승인 대기", "확인 필요"],
] as const)("%s 회차에 할 일이 있으면 목록에서 확인 신호를 보인다", (state, approval_status, pill, detail) => {
  const row: URLRunSummary = {
    ...projection(state === "completed" ? completed : running),
    state,
    approval_status,
    action_required: true,
  };
  render(
    <ShellContext.Provider value={shell("running")}>
      <RunRow row={row} />
    </ShellContext.Provider>,
  );
  const item = screen.getByRole("button", { name: /job-1/ });
  expect(item).toHaveTextContent(pill);
  expect(item).toHaveTextContent(detail);
  expect(item).toHaveTextContent("확인 필요");
});
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
    expect(region).toHaveTextContent("목록의 승인 표시는 실행 허가를 대신하지 않습니다.");
    expect(chip(region, "엔진 실행")).toHaveTextContent("1");
    fireEvent.click(chip(region, "상태 확인 필요"));
    expect(region.querySelectorAll("[data-url-run-id]")).toHaveLength(1);
    expect(within(region).getByRole("button", { name: /job-3/ })).toHaveTextContent("결과 불명");
    expect(region).not.toHaveTextContent("목록의 승인 표시는 실행 허가를 대신하지 않습니다.");
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
    // 패널 제목에 '발견 n건'을 적지 않는다(사용자 11/18) — 위험도 딱지 · 판정 줄이 말한다
    expect(within(detail).getByRole("heading", { level: 2 })).not.toHaveTextContent(/발견 \d+건/);
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
    // 일부만 마감한 회차는 패널에서도 완전한 점검으로 읽히지 않는다.
    await waitFor(() =>
      expect(screen.getByRole("complementary", { name: "선택한 점검" })).toHaveTextContent("실행 결과일부 완료"),
    );
    expect(screen.queryByRole("button", { name: /^발견 없음/ })).toBeNull();
    // 패널 머리 글엔 '끝난 점검'을 적지 않고 대상 주소만, 서버 작업 번호는 '작업 #n' — 목록 줄에도 같은 번호(사용자 11/18)
    const panel = screen.getByRole("complementary", { name: "선택한 점검" });
    expect(panel).not.toHaveTextContent("끝난 점검");
    expect(panel).not.toHaveTextContent("실제 task");
    expect(panel).toHaveTextContent(`작업#${completed.task_id}`);
    // 목록 줄은 이름(prep-…) 왼쪽에 번호만 — 아래 작은 글에 '작업 #n'을 따로 적지 않는다(사용자 11/18)
    // 목록 줄 이름은 '42. job-1'처럼 작업 번호 + 이름(사용자 11/18)
    const row = screen.getByRole("button", { name: new RegExp(`^작업\\s*${completed.task_id}\\.\\s*${completed.id}`) });
    expect(row).not.toHaveTextContent("작업 #");
    // 끝난 점검 줄 아래 글엔 단계(결과 저장) · 승인 상태 · 확인 필요를 적지 않는다(사용자 11/18)
    expect(row).not.toHaveTextContent(/결과 저장|승인|확인 필요/);
    // 목록 머리 수는 'n / n건'(조회 글 없이), 패널엔 다시 점검 안내 글이 없다
    expect(screen.getByText("1 / 1건")).toBeInTheDocument();
    expect(panel).not.toHaveTextContent("다시 점검은 별도");
    expect(screen.queryByText(/보존된 상태입니다/)).toBeNull();
    expect(screen.queryByText(/활성 회차|조회 \d+건/)).toBeNull();
  });
  it("계획을 실행하지 않은 완료 회차는 검사 안 함으로 표시한다", async () => {
    selectSaved({
      ...completed,
      outcome: "no_intents",
      intent_outcomes: { done: 0, incomplete: 0, stopped: 0, unknown: 0, policy_limited: 0 },
    });
    records(true);
    expect(screen.getByRole("complementary", { name: "선택한 점검" })).toHaveTextContent("실행 결과검사 안 함");
  });
  it("실행 결과가 없는 이전 형식 회차에는 실행 결과 줄을 만들지 않는다", () => {
    selectSaved({ ...completed, outcome: undefined, intent_outcomes: undefined });
    records(true);
    const panel = screen.getByRole("complementary", { name: "선택한 점검" });
    expect(within(panel).queryByText("실행 결과")).toBeNull();
  });
  it.each([
    [0, undefined, "검사 안 함 · 정책상 검증 안 됨 2"],
    [0, "budget_limited", "검사 안 함 · 정책상 검증 안 됨 2 · 예산 상한에서 마감"],
    [1, "budget_limited", "점검 완료 · 정책상 검증 안 됨 2 · 예산 상한에서 마감"],
  ] as const)("완료 회차의 검증 %d건과 마감 사유 %s를 함께 표시한다", (done, closing_reason, result) => {
    selectSaved({
      ...completed,
      outcome: "complete",
      closing_reason,
      intent_outcomes: { done, incomplete: 0, stopped: 0, unknown: 0, policy_limited: 2 },
    });
    records(true);
    const panel = screen.getByRole("complementary", { name: "선택한 점검" });
    expect(within(panel).getByText("실행 결과").nextElementSibling).toHaveTextContent(result);
  });
  it("시험 계정은 라벨 순서로 보이고 도움말 버튼으로 서버 라벨을 확인할 수 있다", async () => {
    vi.mocked(api.fetchRemediation).mockResolvedValue({ task_id: 42, entries: [] });
    selectSaved({ ...completed, account_auth: { shopper: "login_failed", "acct-a": "authenticated" } });
    records(true);
    const panel = screen.getByRole("complementary", { name: "선택한 점검" });
    const sec = within(panel).getByRole("heading", { name: "시험 계정 인증" }).parentElement as HTMLElement;
    expect(within(sec).getByText("인증됨")).toBeVisible();
    expect(within(sec).getByText("비인증 진행 · 로그인 실패")).toBeVisible();
    const accountA = within(sec).getByRole("button", { name: "계정 A 서버 라벨 보기" });
    expect(accountA).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(accountA);
    expect(accountA).toHaveAttribute("aria-expanded", "true");
    expect(within(sec).getByRole("note")).toHaveTextContent("서버 라벨: acct-a");
    fireEvent.keyDown(accountA, { key: "Escape" });
    expect(accountA).toHaveFocus();
    expect(within(sec).queryByRole("note")).toBeNull();
    fireEvent.click(within(sec).getByRole("button", { name: "계정 B 서버 라벨 보기" }));
    expect(within(sec).getByRole("note")).toHaveTextContent("서버 라벨: shopper");
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

describe("끝난 점검 삭제", () => {
  const panel = () => screen.getByRole("complementary", { name: "선택한 점검" });
  const openDelete = () => {
    vi.mocked(api.fetchRemediation).mockResolvedValue({ task_id: 42, entries: [] });
    selectSaved(completed);
    records(true);
    fireEvent.click(within(panel()).getByRole("button", { name: "삭제" }));
    return screen.getByRole("alertdialog", { name: "이 점검을 삭제할까요?" });
  };

  it("진행 중인 점검에는 삭제 버튼이 없다", () => {
    selectSaved(running);
    records(false);
    expect(within(panel()).queryByRole("button", { name: "삭제" })).toBeNull();
  });

  it("확인 상자는 버튼 줄 안이 아니라 바로 아래에 뜬다(사용자 11/23)", () => {
    vi.mocked(api.fetchRemediation).mockResolvedValue({ task_id: 42, entries: [] });
    selectSaved(completed);
    records(true);
    const opener = within(panel()).getByRole("button", { name: "삭제" });
    fireEvent.click(opener);
    const box = screen.getByRole("alertdialog", { name: "이 점검을 삭제할까요?" });
    const row = opener.parentElement as HTMLElement;
    expect(row.contains(box)).toBe(false);
    expect(row.nextElementSibling).toBe(box);
  });

  it("확인 상자에서 취소하면 보내지 않고 닫는다", () => {
    const box = openDelete();
    // 질문 한 줄만 둔다(사용자 11/23)
    expect(box).toHaveTextContent(/^이 점검을 삭제할까요\?삭제취소$/);
    fireEvent.click(within(box).getByRole("button", { name: "취소" }));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(deleteURLRun).not.toHaveBeenCalled();
  });

  it("확인하면 그 점검을 지우고, 지운 뒤에만 선택을 풀고 목록을 다시 읽는다", async () => {
    vi.mocked(deleteURLRun).mockResolvedValue(undefined);
    const box = openDelete();
    fireEvent.click(within(box).getByRole("button", { name: "삭제" }));
    await waitFor(() => expect(loadUrlRuns).toHaveBeenCalledWith(catalog.ownerContext));
    expect(deleteURLRun).toHaveBeenCalledWith(completed, catalog.ownerContext);
    expect(useUrl.getState().handle).toBeNull();
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });

  it.each([
    [new ApiError(404, "route_not_found"), "서버가 아직 점검 삭제를 지원하지 않습니다."],
    [new ApiError(409, "run_active"), "진행 중이거나 마감 중인 점검은 지울 수 없습니다."],
    [new ApiError(409, "revision_conflict"), "점검 상태가 바뀌었습니다. 다시 연 뒤 지워 주세요."],
    [new ApiError(401, "authentication_required"), "다시 로그인해 주세요."],
    // 결과를 모르는 실패는 '못 지웠다'로 말하지 않는다(규칙 5, #867 이경준 리뷰)
    [new ApiError(503, "unavailable"), "삭제됐는지 확인하지 못했습니다. 다시 누르면 확인합니다."],
    [new ApiError(0, "network"), "삭제됐는지 확인하지 못했습니다. 다시 누르면 확인합니다."],
    [new ApiError(422, "invalid_contract"), "삭제됐는지 확인하지 못했습니다. 다시 누르면 확인합니다."],
    [new ApiError(401, "authentication_changed"), "삭제됐는지 확인하지 못했습니다. 다시 누르면 확인합니다."],
    [new ApiError(500, "internal"), "삭제됐는지 확인하지 못했습니다. 다시 누르면 확인합니다."],
  ])("실패(%s)는 상자를 열어 둔 채 이유를 보이고 선택을 풀지 않는다", async (error, text) => {
    vi.mocked(deleteURLRun).mockRejectedValue(error);
    const box = openDelete();
    fireEvent.click(within(box).getByRole("button", { name: "삭제" }));
    expect(await within(box).findByRole("alert")).toHaveTextContent(text);
    expect(useUrl.getState().handle).not.toBeNull();
    expect(loadUrlRuns).not.toHaveBeenCalled();
  });

  it("이미 숨긴 회차(not_found)는 지워진 것으로 보고 닫은 뒤 목록을 다시 읽는다", async () => {
    vi.mocked(deleteURLRun).mockRejectedValue(new ApiError(404, "not_found"));
    const box = openDelete();
    fireEvent.click(within(box).getByRole("button", { name: "삭제" }));
    await waitFor(() => expect(loadUrlRuns).toHaveBeenCalledWith(catalog.ownerContext));
    expect(useUrl.getState().handle).toBeNull();
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });
});
