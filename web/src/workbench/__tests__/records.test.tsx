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

import { catalog, handle, running } from "@/lib/diagnosis/diagnosis-url-fixtures";

import * as api from "../live/api";
import { LiveRailTasks } from "../live/rail";
import { resetUrl, useUrl } from "../live/urlDiagnosis";
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

  it("끝난 점검: 실행 마감된 점검만 팀 lib 판정 집계로 나눈다 — 발견 없음은 정상 대조 결과가 있을 때만", () => {
    expect(recDoneKinds("completed", null)).toEqual([]);
    expect(recDoneKinds("completed", { ...zero, negative: 3 })).toEqual(["clean"]);
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

describe("진행 중인 점검 · 끝난 점검 화면", () => {
  it("진행 중 칸의 필터로 거르고, 레일에서 다시 오면 가려진 칸 필터만 푼다", () => {
    useUrl.setState({ handle, job: running, verified: true });
    const { rerender } = render(
      <ShellContext.Provider value={shell("running")}>
        <UrlRunningLive />
      </ShellContext.Provider>,
    );
    const run = screen.getByRole("region", { name: "진행 중인 점검 목록" });
    expect(within(run).getByRole("button", { name: /target\.example/ })).toBeInTheDocument();
    expect(chip(run, "전체")).toHaveAttribute("aria-pressed", "true");
    expect(chip(run, "점검 중")).toHaveTextContent("1");
    expect(chip(run, "승인 대기")).toHaveTextContent("0");
    // 범위 확인 대기는 그런 점검이 있을 때만
    expect(within(run).queryByRole("button", { name: /범위 확인 대기/ })).toBeNull();

    fireEvent.click(chip(run, "승인 대기"));
    expect(within(run).queryByRole("button", { name: /target\.example/ })).toBeNull();
    expect(within(run).getByText("이 상태의 점검이 없습니다.")).toBeInTheDocument();
    expect(within(run).getByText("0 / 1건")).toBeInTheDocument();
    // 목록에 가려졌으면 오른쪽 상세도 비운다 (목록과 상세가 어긋나지 않게)
    const detail = screen.getByRole("complementary", { name: "선택한 점검" });
    expect(within(detail).getByText("고른 점검이 없습니다.")).toBeInTheDocument();

    // 레일에서 이 화면으로 다시 오면(라우팅 순번이 오름) 가려진 칸의 필터가 풀린다
    rerender(
      <ShellContext.Provider value={shell("running", { navSeq: 2 })}>
        <UrlRunningLive />
      </ShellContext.Provider>,
    );
    expect(chip(run, "전체")).toHaveAttribute("aria-pressed", "true");
    expect(within(run).getByRole("button", { name: /target\.example/ })).toBeInTheDocument();
  });

  it("끝난 점검은 보고서 저장 뒤 팀 lib 판정으로 거르고, 상세에 판정 집계를 보인다", async () => {
    vi.mocked(api.fetchRemediation).mockResolvedValue({ task_id: 42, entries: [candidate(1)] });
    useUrl.setState({ handle, job: completed, verified: true });
    render(
      <ShellContext.Provider value={shell("finished")}>
        <UrlFinishedLive />
      </ShellContext.Provider>,
    );
    const done = screen.getByRole("region", { name: "끝난 점검 목록" });
    await waitFor(() => expect(chip(done, "검토 필요")).toHaveTextContent("1"));
    expect(api.fetchRemediation).toHaveBeenCalledWith("42", "internal");
    expect(chip(done, "발견 있음")).toHaveTextContent("0");
    expect(chip(done, "발견 없음")).toHaveTextContent("0");
    expect(chip(done, "판단 불가")).toHaveTextContent("0");

    fireEvent.click(chip(done, "발견 있음"));
    expect(within(done).getByText("이 결과의 끝난 점검이 없습니다.")).toBeInTheDocument();
    fireEvent.click(chip(done, "검토 필요"));
    expect(within(done).getByRole("button", { name: /target\.example/ })).toBeInTheDocument();

    const detail = screen.getByRole("complementary", { name: "선택한 점검" });
    expect(within(detail).getByText("확정 0 · 검토 필요 1 · 미실행 0 · 결과 불명 0")).toBeInTheDocument();
    expect(within(detail).getByRole("button", { name: "이 주소로 다시 점검" })).toBeInTheDocument();
  });

  it("결과 저장이 확인된 중지 점검도 결과로 나누지 않고 '전체' 에만 보인다 (판정 0건이어도 발견 없음 아님)", async () => {
    vi.mocked(api.fetchRemediation).mockResolvedValue({ task_id: 42, entries: [] });
    useUrl.setState({ handle, job: { ...completed, state: "cancelled" }, verified: true });
    render(
      <ShellContext.Provider value={shell("finished")}>
        <UrlFinishedLive />
      </ShellContext.Provider>,
    );
    const done = screen.getByRole("region", { name: "끝난 점검 목록" });
    expect(within(done).getByRole("button", { name: /target\.example/ })).toBeInTheDocument();
    for (const c of ["발견 있음", "발견 없음", "판단 불가"]) expect(chip(done, c)).toHaveTextContent("0");
    expect(within(done).getByRole("note")).toHaveTextContent(
      "중지 · 실패한 점검은 끝까지 보지 않아 결과로 나누지 않고",
    );
    fireEvent.click(chip(done, "발견 없음"));
    expect(within(done).queryByRole("button", { name: /target\.example/ })).toBeNull();
  });

  it("실행 마감된 점검이라도 판정 기록이 0건이면 발견 없음으로 나누지 않는다", async () => {
    vi.mocked(api.fetchRemediation).mockResolvedValue({ task_id: 42, entries: [] });
    useUrl.setState({ handle, job: completed, verified: true });
    render(
      <ShellContext.Provider value={shell("finished")}>
        <UrlFinishedLive />
      </ShellContext.Provider>,
    );
    const done = screen.getByRole("region", { name: "끝난 점검 목록" });
    await waitFor(() => expect(within(done).getByRole("note")).toHaveTextContent("정상 대조 결과가 없어"));
    expect(chip(done, "발견 없음")).toHaveTextContent("0");
    expect(chip(done, "전체")).toHaveTextContent("1");
  });

  it("보고서를 못 읽은 끝난 점검(결과 저장 미확인)은 판정을 부르지 않고 '전체' 에만 보인다", () => {
    useUrl.setState({ handle, job: { ...completed, state: "cancelled", result_ready: false }, verified: true });
    render(
      <ShellContext.Provider value={shell("finished")}>
        <UrlFinishedLive />
      </ShellContext.Provider>,
    );
    const done = screen.getByRole("region", { name: "끝난 점검 목록" });
    expect(within(done).getByRole("button", { name: /target\.example/ })).toBeInTheDocument();
    expect(chip(done, "발견 없음")).toHaveTextContent("0");
    expect(api.fetchRemediation).not.toHaveBeenCalled();
  });

  it("요청 승인 대기는 승인 대기 칩에 들고, 상세에서 대시보드의 승인 요청으로 간다", async () => {
    const job = { ...running, input: { ...running.input, workflow_mode: "user_approval_v1" as const } };
    vi.mocked(api.fetchRequestApprovals).mockResolvedValue({
      run: {
        run_ref: "run-1",
        phase: "running",
        outcome: "",
        stop_requested: false,
        context_cancelled: false,
        drain_complete: false,
        closure_failed: false,
      },
      approvals: [
        {
          id: "ap-1",
          task_id: 42,
          run_ref: "run-1",
          intent_id: 1,
          call_ref: "call-1",
          attempt_id: "attempt-1",
          policy_revision: "rev-1",
          account_ref: "a",
          session_ref: "s",
          request_sha256: "d".repeat(64),
          method: "PUT",
          target_origin: "https://target.example",
          path: "/api/users/2",
          input_preview: {},
          body_bytes: 0,
          created_at: "2026-11-11T01:02:03Z",
          expires_at: "2099-01-01T00:00:00Z",
          state: "pending",
          consumed: false,
          execution_state: "not_sent",
          live: true,
          can_decide: true,
        },
      ],
    });
    useUrl.setState({ handle, job, verified: true });
    const goTo = vi.fn<(v: View) => void>();
    render(
      <ShellContext.Provider value={shell("running", { goTo })}>
        <UrlRunningLive />
      </ShellContext.Provider>,
    );
    const run = screen.getByRole("region", { name: "진행 중인 점검 목록" });
    await waitFor(() => expect(chip(run, "승인 대기")).toHaveTextContent("1"));
    expect(chip(run, "점검 중")).toHaveTextContent("0");
    const detail = screen.getByRole("complementary", { name: "선택한 점검" });
    // 승인 대기 종류(보낼 요청)와 가장 빠른 만료를 같이 보인다
    expect(within(detail).getByText(/^보낼 요청 1건 · 가장 빠른 만료 \d\d:\d\d:\d\d$/)).toBeInTheDocument();
    fireEvent.click(within(detail).getByRole("button", { name: "승인 요청 보기" }));
    expect(goTo).toHaveBeenCalledWith("dashboard");
  });

  it("보고서를 읽는 중에 레일로 오면 해제를 보류했다가, 늦게 온 보고서로 가려지면 한 번만 끝난 점검 필터를 푼다", async () => {
    // 보고서 응답을 늦춘다
    let answer: (v: api.Remediation) => void = () => undefined;
    vi.mocked(api.fetchRemediation).mockImplementation(
      () =>
        new Promise<api.Remediation>((resolve) => {
          answer = resolve;
        }),
    );
    useUrl.setState({ handle, job: completed, verified: true });
    const view = (navSeq: number) => (
      <ShellContext.Provider value={shell("finished", { navSeq })}>
        <UrlFinishedLive />
      </ShellContext.Provider>
    );
    const { rerender } = render(view(1));
    const done = screen.getByRole("region", { name: "끝난 점검 목록" });
    await waitFor(() => expect(api.fetchRemediation).toHaveBeenCalledTimes(1));
    expect(chip(done, "발견 없음")).toHaveTextContent("—");
    // 이전에 고른 필터('발견 없음')를 둔 채, 보고서를 읽는 중에 레일로 다시 들어온다
    fireEvent.click(chip(done, "발견 없음"));
    rerender(view(2));
    expect(chip(done, "발견 없음")).toHaveAttribute("aria-pressed", "true");
    // 후보가 있는 보고서가 늦게 오면: 지금 점검은 '검토 필요' 라 '발견 없음' 에 가려진다 → 필터를 푼다
    answer({ task_id: 42, entries: [candidate(1)] });
    await waitFor(() => expect(chip(done, "전체")).toHaveAttribute("aria-pressed", "true"));
    expect(within(done).getByRole("button", { name: /target\.example/ })).toBeInTheDocument();
    expect(chip(done, "검토 필요")).toHaveTextContent("1");
    // 그 뒤 사용자가 직접 고른 필터는 다시 풀지 않는다
    fireEvent.click(chip(done, "발견 없음"));
    rerender(view(2));
    expect(chip(done, "발견 없음")).toHaveAttribute("aria-pressed", "true");
    expect(within(done).queryByRole("button", { name: /target\.example/ })).toBeNull();
  });

  it("보고서를 읽는 중에 레일로 왔어도, 그 사이 사용자가 끝난 점검 필터를 직접 고르면 보류한 해제를 버린다", async () => {
    let answer: (v: api.Remediation) => void = () => undefined;
    vi.mocked(api.fetchRemediation).mockImplementation(
      () =>
        new Promise<api.Remediation>((resolve) => {
          answer = resolve;
        }),
    );
    useUrl.setState({ handle, job: completed, verified: true });
    const view = (navSeq: number) => (
      <ShellContext.Provider value={shell("finished", { navSeq })}>
        <UrlFinishedLive />
      </ShellContext.Provider>
    );
    const { rerender } = render(view(1));
    const done = screen.getByRole("region", { name: "끝난 점검 목록" });
    await waitFor(() => expect(api.fetchRemediation).toHaveBeenCalledTimes(1));
    rerender(view(2));
    fireEvent.click(chip(done, "발견 있음"));
    answer({ task_id: 42, entries: [candidate(1)] });
    await waitFor(() => expect(chip(done, "검토 필요")).toHaveTextContent("1"));
    expect(chip(done, "발견 있음")).toHaveAttribute("aria-pressed", "true");
    expect(within(done).getByText("이 결과의 끝난 점검이 없습니다.")).toBeInTheDocument();
  });

  it("보류 중에 보고서 조회가 실패하면 보류를 풀고 '전체' 로 돌린다 (칩 개수는 모름 '—' 그대로)", async () => {
    let fail: (e: unknown) => void = () => undefined;
    vi.mocked(api.fetchRemediation).mockImplementation(
      () =>
        new Promise<api.Remediation>((_resolve, reject) => {
          fail = reject;
        }),
    );
    useUrl.setState({ handle, job: completed, verified: true });
    const view = (navSeq: number) => (
      <ShellContext.Provider value={shell("finished", { navSeq })}>
        <UrlFinishedLive />
      </ShellContext.Provider>
    );
    const { rerender } = render(view(1));
    const done = screen.getByRole("region", { name: "끝난 점검 목록" });
    await waitFor(() => expect(api.fetchRemediation).toHaveBeenCalledTimes(1));
    fireEvent.click(chip(done, "발견 없음"));
    rerender(view(2));
    fail(new Error("보고서 조회 실패"));
    await waitFor(() => expect(chip(done, "전체")).toHaveAttribute("aria-pressed", "true"));
    expect(within(done).getByRole("button", { name: /target\.example/ })).toBeInTheDocument();
    expect(chip(done, "발견 없음")).toHaveTextContent("—");
    expect(within(done).getByRole("note")).toHaveTextContent("보고서를 읽지 못해 결과로 나누지 않고");
  });

  it("이어 보는 점검이 없으면 두 화면 모두 빈 안내와 새 점검 링크", () => {
    const { unmount } = render(
      <ShellContext.Provider value={shell("running")}>
        <UrlRunningLive />
      </ShellContext.Provider>,
    );
    const run = screen.getByRole("region", { name: "진행 중인 점검 목록" });
    expect(within(run).getByText("진행 중인 점검이 없습니다.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "새 점검" })).toHaveAttribute("href", "#new");
    unmount();
    render(
      <ShellContext.Provider value={shell("finished")}>
        <UrlFinishedLive />
      </ShellContext.Provider>,
    );
    const done = screen.getByRole("region", { name: "끝난 점검 목록" });
    expect(within(done).getByText("끝난 점검이 없습니다.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "새 점검" })).toHaveAttribute("href", "#new");
  });

  it("진행 중인 점검은 진행 중 점검만, 끝난 점검은 끝난 점검만 보인다", () => {
    useUrl.setState({ handle, job: running, verified: true });
    const { unmount } = render(
      <ShellContext.Provider value={shell("finished")}>
        <UrlFinishedLive />
      </ShellContext.Provider>,
    );
    const done = screen.getByRole("region", { name: "끝난 점검 목록" });
    expect(within(done).queryByRole("button", { name: /target\.example/ })).toBeNull();
    expect(within(done).getByText("끝난 점검이 없습니다.")).toBeInTheDocument();
    unmount();
    useUrl.setState({ handle, job: { ...completed, state: "cancelled", result_ready: false }, verified: true });
    render(
      <ShellContext.Provider value={shell("running")}>
        <UrlRunningLive />
      </ShellContext.Provider>,
    );
    const run = screen.getByRole("region", { name: "진행 중인 점검 목록" });
    expect(within(run).queryByRole("button", { name: /target\.example/ })).toBeNull();
    expect(within(run).getByText("진행 중인 점검이 없습니다.")).toBeInTheDocument();
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
