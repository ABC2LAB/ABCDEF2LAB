// 점검 기록 화면 (시안 v27 ~ v30): 레일에서 점검을 누르면 오고, 진행 중 · 끝난 점검 칸마다 따로 필터가 있다.
// 줄은 이 브라우저가 이어 보는 진단 하나뿐이고, 필터는 서버 값(진단 상태 · 탐색 단계 · 요청 승인 · 팀 lib 판정)으로만 나눈다.

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
import { recDoneKinds, recRunKind, UrlRecordsLive } from "../views/live/UrlRecordsLive";

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

describe("점검 기록 묶음 계산", () => {
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

  it("끝난 점검: 팀 lib 판정 집계 그대로 — 확정 · 검토 필요 · 판단 불가(미실행 · 결과 불명), 넷 다 0 이면 발견 없음", () => {
    expect(recDoneKinds(null)).toEqual([]);
    expect(recDoneKinds({ ...zero, negative: 3 })).toEqual(["clean"]);
    expect(recDoneKinds({ ...zero, confirmed: 1 })).toEqual(["hit"]);
    expect(recDoneKinds({ ...zero, review: 2 })).toEqual(["review"]);
    expect(recDoneKinds({ ...zero, not_executed: 1 })).toEqual(["und"]);
    expect(recDoneKinds({ ...zero, confirmed: 1, unknown: 1 })).toEqual(["hit", "und"]);
  });
});

describe("점검 기록 화면", () => {
  it("진행 중 칸의 필터로 거르고, 레일에서 다시 오면 가려진 칸 필터만 푼다", () => {
    useUrl.setState({ handle, job: running, verified: true });
    const { rerender } = render(
      <ShellContext.Provider value={shell("records")}>
        <UrlRecordsLive />
      </ShellContext.Provider>,
    );
    const run = screen.getByRole("region", { name: "진행 중" });
    expect(within(run).getByRole("button", { name: /target\.example/ })).toBeInTheDocument();
    expect(chip(run, "전체")).toHaveAttribute("aria-pressed", "true");
    expect(chip(run, "점검 중")).toHaveTextContent("1");
    expect(chip(run, "승인 대기")).toHaveTextContent("0");
    // 범위 확인 대기는 그런 점검이 있을 때만
    expect(within(run).queryByRole("button", { name: /범위 확인 대기/ })).toBeNull();
    expect(screen.getByText("끝난 점검이 없습니다.")).toBeInTheDocument();

    fireEvent.click(chip(run, "승인 대기"));
    expect(within(run).queryByRole("button", { name: /target\.example/ })).toBeNull();
    expect(within(run).getByText("이 상태의 점검이 없습니다.")).toBeInTheDocument();
    expect(within(run).getByText("0 / 1건")).toBeInTheDocument();

    // 레일에서 이 화면으로 다시 오면(라우팅 순번이 오름) 가려진 칸의 필터가 풀린다
    rerender(
      <ShellContext.Provider value={shell("records", { navSeq: 2 })}>
        <UrlRecordsLive />
      </ShellContext.Provider>,
    );
    expect(chip(run, "전체")).toHaveAttribute("aria-pressed", "true");
    expect(within(run).getByRole("button", { name: /target\.example/ })).toBeInTheDocument();
  });

  it("끝난 점검은 보고서 저장 뒤 팀 lib 판정으로 거르고, 상세에 판정 집계를 보인다", async () => {
    vi.mocked(api.fetchRemediation).mockResolvedValue({ task_id: 42, entries: [candidate(1)] });
    useUrl.setState({ handle, job: completed, verified: true });
    render(
      <ShellContext.Provider value={shell("records")}>
        <UrlRecordsLive />
      </ShellContext.Provider>,
    );
    const done = screen.getByRole("region", { name: "끝난 점검" });
    await waitFor(() => expect(chip(done, "검토 필요")).toHaveTextContent("1"));
    expect(api.fetchRemediation).toHaveBeenCalledWith("42", "internal");
    expect(chip(done, "발견 있음")).toHaveTextContent("0");
    expect(chip(done, "발견 없음")).toHaveTextContent("0");
    expect(chip(done, "판단 불가")).toHaveTextContent("0");
    expect(screen.getByText("진행 중인 점검이 없습니다.")).toBeInTheDocument();

    fireEvent.click(chip(done, "발견 있음"));
    expect(within(done).getByText("이 결과의 끝난 점검이 없습니다.")).toBeInTheDocument();
    fireEvent.click(chip(done, "검토 필요"));
    expect(within(done).getByRole("button", { name: /target\.example/ })).toBeInTheDocument();

    const detail = screen.getByRole("complementary", { name: "선택한 점검" });
    expect(within(detail).getByText("확정 0 · 검토 필요 1 · 미실행 0 · 결과 불명 0")).toBeInTheDocument();
    expect(within(detail).getByRole("button", { name: "이 주소로 다시 점검" })).toBeInTheDocument();
  });

  it("보고서를 못 읽은 끝난 점검(결과 저장 미확인)은 판정을 부르지 않고 '전체' 에만 보인다", () => {
    useUrl.setState({ handle, job: { ...completed, state: "cancelled", result_ready: false }, verified: true });
    render(
      <ShellContext.Provider value={shell("records")}>
        <UrlRecordsLive />
      </ShellContext.Provider>,
    );
    const done = screen.getByRole("region", { name: "끝난 점검" });
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
      <ShellContext.Provider value={shell("records", { goTo })}>
        <UrlRecordsLive />
      </ShellContext.Provider>,
    );
    const run = screen.getByRole("region", { name: "진행 중" });
    await waitFor(() => expect(chip(run, "승인 대기")).toHaveTextContent("1"));
    expect(chip(run, "점검 중")).toHaveTextContent("0");
    const detail = screen.getByRole("complementary", { name: "선택한 점검" });
    expect(within(detail).getByText("요청 1건")).toBeInTheDocument();
    fireEvent.click(within(detail).getByRole("button", { name: "승인 요청 보기" }));
    expect(goTo).toHaveBeenCalledWith("dashboard");
  });

  it("이어 보는 점검이 없으면 두 칸 모두 빈 안내와 새 점검 링크", () => {
    render(
      <ShellContext.Provider value={shell("records")}>
        <UrlRecordsLive />
      </ShellContext.Provider>,
    );
    expect(screen.getByText("진행 중인 점검이 없습니다.")).toBeInTheDocument();
    expect(screen.getByText("끝난 점검이 없습니다.")).toBeInTheDocument();
    expect(screen.getByText("이 브라우저에서 이어 보는 점검이 없습니다.")).toBeInTheDocument();
  });
});

describe("레일 → 점검 기록", () => {
  it("레일의 점검 줄을 누르면 점검 기록으로 가고, 점검 기록을 보는 동안 현재 표시를 단다", () => {
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
    expect(goTo).toHaveBeenCalledWith("records");
    rerender(
      <ShellContext.Provider value={shell("records", { goTo })}>
        <LiveRailTasks />
      </ShellContext.Provider>,
    );
    expect(screen.getByRole("button", { name: /target\.example/ })).toHaveAttribute("aria-current", "true");
  });
});
