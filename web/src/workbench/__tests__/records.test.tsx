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
import { activeURLRun, type URLRunSummary } from "@/lib/diagnosis/diagnosis-runs";
import type { URLDiagnosisJob } from "@/lib/diagnosis/diagnosis-url";
import { catalog, handle, running } from "@/lib/diagnosis/diagnosis-url-fixtures";

import * as api from "../live/api";
import { useHiddenRuns } from "../live/hiddenRuns";
import { LiveRailTasks } from "../live/rail";
import { resetUrl, useUrl } from "../live/urlDiagnosis";
import { urlStateText } from "../live/urlMap";
import { useUrlRuns } from "../live/urlRunList";
import { type Shell, ShellContext, type View } from "../shell";
import { RunRow } from "../views/live/runRows";
import { UrlDashboardLive } from "../views/live/UrlDashboardLive";
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
// 끝난 점검은 승인 상태 · '확인 필요'를 붙이지 않는다(사용자 11/24: 대상 승인 시간이 지나면 서버가 끝난 점검까지 만료 ·
// 확인 필요로 내려 '점검 완료 · 승인 만료'로 보였다 — 결과는 그대로 유효)
it.each(["completed", "cancelled", "failed"] as const)("끝난(%s) 점검은 승인이 만료돼도 상태만 보인다", (state) => {
  const row: URLRunSummary = {
    ...projection(completed),
    state,
    approval_status: "expired",
    action_required: true,
  };
  render(
    <ShellContext.Provider value={shell("finished")}>
      <RunRow row={row} />
    </ShellContext.Provider>,
  );
  const item = screen.getByRole("button", { name: /job-1/ });
  expect(item).toHaveTextContent(urlStateText(state));
  // 점검 완료 딱지는 초록(ok), 중지 · 실패는 회색
  const pill = within(item).getByText(urlStateText(state));
  if (state === "completed") expect(pill.className).toMatch(/\bok\b|_ok_/);
  else expect(pill.className).not.toMatch(/\bok\b|_ok_|wait/);
  expect(item).not.toHaveTextContent("승인 만료");
  expect(item).not.toHaveTextContent("확인 필요");
});
it.each([
  ["running", "expired", "엔진 실행 중", "승인 만료"],
  ["awaiting_approval", "not_revalidated", "승인 대기", "확인 필요"],
] as const)("%s 회차에 할 일이 있으면 목록에서 확인 신호를 보인다", (state, approval_status, pill, detail) => {
  const row: URLRunSummary = {
    ...projection(running),
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
  // #914 인계: 끝난 회차에 account_auth 가 없으면 '인증 준비 기록 없음'(로그인 성공 · 실패로 단정하지 않음)
  it("끝난 회차에 인증 준비 기록이 없으면 그렇게 적는다", () => {
    vi.mocked(api.fetchRemediation).mockResolvedValue({ task_id: 42, entries: [] });
    selectSaved({ ...completed });
    records(true);
    const panel = screen.getByRole("complementary", { name: "선택한 점검" });
    const sec = within(panel).getByRole("heading", { name: "시험 계정 인증" }).parentElement as HTMLElement;
    expect(sec).toHaveTextContent("이 회차에는 인증 준비 기록이 없습니다.");
    expect(sec).not.toHaveTextContent(/인증됨|로그인 실패/);
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
      <ShellContext.Provider value={shell("declaration", { goTo })}>
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

it("이름을 붙인 점검 줄에는 서버 ID(prep-…)를 보이지 않고 상태 낱말은 따로 읽힌다(사용자 11/23)", () => {
  const row: URLRunSummary = { ...projection(running), display_name: "test1" };
  render(
    <ShellContext.Provider value={shell("running")}>
      <RunRow row={row} />
    </ShellContext.Provider>,
  );
  const button = screen.getByRole("button", { name: /test1/ });
  expect(button).not.toHaveTextContent(running.id);
  const status = button.querySelector("span[class*='r-s']") as HTMLElement;
  expect(status.children.length).toBeGreaterThan(1);
});

it("이미 열어 둔 점검을 다시 누르면 상태를 다시 읽지 않는다(깜빡임, 사용자 11/23)", async () => {
  vi.mocked(api.fetchRemediation).mockResolvedValue({ task_id: 42, entries: [] });
  selectSaved(completed);
  records(true);
  const epoch = useUrl.getState().selectionEpoch;
  fireEvent.click(
    within(screen.getByRole("region", { name: "끝난 점검 목록" })).getByRole("button", { name: /job-1/ }),
  );
  expect(useUrl.getState().selectionEpoch).toBe(epoch);
  expect(useUrl.getState().job?.id).toBe(completed.id);
  expect(within(screen.getByRole("complementary", { name: "선택한 점검" })).queryByRole("status")).toBeNull();
});

it("점검을 고른 뒤 상태를 읽는 동안 '목록에서 점검을 선택' 안내 대신 불러오는 중만 둔다(깜빡임, 사용자 11/23)", () => {
  savedRows([completed]);
  useUrl.setState({ selectedID: completed.id, busy: true, handle: null, job: null });
  records(true);
  const detail = screen.getByRole("complementary", { name: "선택한 점검" });
  expect(within(detail).getByRole("status")).toHaveTextContent("불러오는 중…");
  expect(detail).not.toHaveTextContent("목록에서 점검을 선택");
});

describe("끝난 점검 삭제 (화면 목록에서만, 기록은 그대로, 2026-11-23 #867)", () => {
  const panel = () => screen.getByRole("complementary", { name: "선택한 점검" });
  const list = () => screen.getByRole("region", { name: "끝난 점검 목록" });
  const hiddenToggle = () => screen.getByRole("button", { name: /^삭제한 점검/ });
  beforeEach(() => {
    window.localStorage.clear();
    useHiddenRuns.setState({ owner: null, ids: [], showing: false });
    vi.mocked(api.fetchRemediation).mockResolvedValue({ task_id: 42, entries: [] });
  });
  const openHide = () => {
    selectSaved(completed);
    records(true);
    const opener = within(panel()).getByRole("button", { name: "삭제" });
    fireEvent.click(opener);
    return { opener, box: screen.getByRole("alertdialog", { name: "이 점검을 삭제할까요?" }) };
  };

  it("진행 중인 점검에는 삭제 버튼이 없다", () => {
    selectSaved(running);
    records(false);
    expect(within(panel()).queryByRole("button", { name: "삭제" })).toBeNull();
  });

  it("처음 버튼은 확인 상자를 켜고 끄고, 상자는 버튼 줄 바로 아래에 질문 한 줄만 둔다", () => {
    const { opener, box } = openHide();
    const row = opener.parentElement as HTMLElement;
    expect(row.contains(box)).toBe(false);
    expect(row.nextElementSibling).toBe(box);
    expect(box).toHaveTextContent(/^이 점검을 삭제할까요\?삭제취소$/);
    fireEvent.click(opener);
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(opener).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(opener);
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "취소" }));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(within(list()).getByRole("button", { name: /job-1/ })).toBeInTheDocument();
  });

  it("삭제하면 서버에 보내지 않고 기본 목록에서만 빠지고, 건수 옆 '삭제한 점검'에서 다시 열어 보고서를 읽고 되돌린다", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    const { box } = openHide();
    fireEvent.click(within(box).getByRole("button", { name: "삭제" }));
    // 서버에는 아무것도 보내지 않는다(기록은 서버에 그대로)
    expect(fetchSpy).not.toHaveBeenCalled();
    fetchSpy.mockRestore();
    expect(within(list()).queryByRole("button", { name: /job-1/ })).toBeNull();
    expect(useUrl.getState().handle).toBeNull();
    expect(JSON.parse(window.localStorage.getItem(`abc2lab-hidden-runs:${catalog.ownerContext}`) ?? "[]")).toEqual([
      completed.id,
    ]);
    // '삭제한 점검'은 오른쪽 건수 바로 옆에 붙는다(사용자 11/23)
    const toggle = hiddenToggle();
    expect(toggle).toHaveTextContent("삭제한 점검 1");
    expect(toggle.parentElement).toHaveTextContent(/^0 \/ 0건삭제한 점검 1$/);
    fireEvent.click(toggle);
    expect(hiddenToggle()).toHaveAttribute("aria-pressed", "true");
    expect(within(list()).getByRole("button", { name: /job-1/ })).toBeInTheDocument();
    // 점검을 고르면 화면이 다시 그려져도(byMode key) '삭제한 점검' 목록에 그대로 남는다
    cleanup();
    records(true);
    expect(hiddenToggle()).toHaveAttribute("aria-pressed", "true");
    expect(within(list()).getByRole("button", { name: /job-1/ })).toBeInTheDocument();
    vi.mocked(api.fetchRemediation).mockClear();
    selectSaved(completed);
    await waitFor(() => expect(api.fetchRemediation).toHaveBeenCalledWith("42", "internal"));
    fireEvent.click(within(panel()).getByRole("button", { name: "목록에 다시 보이기" }));
    expect(window.localStorage.getItem(`abc2lab-hidden-runs:${catalog.ownerContext}`)).toBe("[]");
    // 기본 목록으로 돌아가면 다시 보인다
    fireEvent.click(hiddenToggle());
    expect(within(list()).getByRole("button", { name: /job-1/ })).toBeInTheDocument();
    expect(within(panel()).getByRole("button", { name: "삭제" })).toBeInTheDocument();
  });

  it("브라우저 저장소를 못 써도 이 탭에서는 숨긴다", () => {
    const setItem = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    const { box } = openHide();
    fireEvent.click(within(box).getByRole("button", { name: "삭제" }));
    expect(within(list()).queryByRole("button", { name: /job-1/ })).toBeNull();
    setItem.mockRestore();
  });

  it("대시보드의 끝난 점검 목록에서도 숨긴 점검을 뺀다", () => {
    savedRows([completed]);
    window.localStorage.setItem(`abc2lab-hidden-runs:${catalog.ownerContext}`, JSON.stringify([completed.id]));
    render(
      <ShellContext.Provider value={shell("dashboard")}>
        <UrlDashboardLive />
      </ShellContext.Provider>,
    );
    const done = screen.getByRole("heading", { name: "보존된 끝난 점검" }).closest("section") as HTMLElement;
    expect(done).toHaveTextContent("0건");
    expect(within(done).queryByRole("button", { name: /job-1/ })).toBeNull();
  });

  it("레일의 이어 보지 않는 끝난 점검 수에서 숨긴 점검을 뺀다", () => {
    savedRows([completed]);
    window.localStorage.setItem(`abc2lab-hidden-runs:${catalog.ownerContext}`, JSON.stringify([completed.id]));
    render(
      <ShellContext.Provider value={shell("dashboard")}>
        <LiveRailTasks />
      </ShellContext.Provider>,
    );
    expect(screen.queryByText(/이어 보지 않는 끝난 점검/)).toBeNull();
  });
});
