// 시안 짜임으로 바꾼 실제 연결 화면: 증거 펼치기 · 레일 현재 표시 · 범위 기본 선택 · 대시보드 승인 카드.

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../live/api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../live/api")>()),
  fetchRequestApprovals: vi.fn(),
}));

import { catalog, handle, running, scope } from "@/lib/diagnosis/diagnosis-url-fixtures";

import * as api from "../live/api";
import { LiveRailTasks } from "../live/rail";
import { resetUrl, useUrl } from "../live/urlDiagnosis";
import { FLOW_VERSION, type WorkflowSnapshot } from "../live/workflow";
import { type Shell, ShellContext, type View } from "../shell";
import { UrlDashboardLive } from "../views/live/UrlDashboardLive";
import { prettyEvidence } from "../views/live/UrlLiveLive";
import { WorkflowScopeLive } from "../views/live/workflowViews";

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

const job = {
  ...running,
  input: { ...running.input, workflow_mode: "user_approval_v1" as const },
  scope: { ...scope, approval_mode: "workbench_selective_v1" as const, methods: ["GET", "POST"] },
};
const ep = (id: string, method: string, path: string, change: "possible" | "attested_read" | "blocked") => ({
  id,
  method,
  path,
  from: "/",
  kind: "api",
  source: "html_link",
  in_scope: change !== "blocked",
  change,
  visited: change === "attested_read",
  selected: false,
  duplicates: 0,
});
const flow: WorkflowSnapshot = {
  format: FLOW_VERSION,
  job_id: job.id,
  task_id: "42",
  run_ref: `run-${job.id}`,
  source_digest: job.source_digest,
  revision: 12,
  mode: "user_approval_v1",
  stage: "awaiting_selection",
  paused: false,
  initial_completed: true,
  reversible_writes: true,
  deep_runs: 0,
  deep_closed: 0,
  limits: {
    format: FLOW_VERSION,
    max_pages: 4,
    max_endpoints: 32,
    max_response_bytes: 4096,
    allow_reversible_writes: true,
  },
  counts: { found: 3, visited: 1, queued: 0, sent: 1, attempted: 1, unknown: 0, blocked: 0 },
  endpoints: [
    ep("read", "GET", "/api/products/1", "attested_read"),
    ep("write", "PUT", "/api/users/2", "possible"),
    ep("off", "DELETE", "/api/products/1", "blocked"),
  ],
  events: [],
  commands: [],
  updated_at: "2026-11-11T01:00:00Z",
};

beforeEach(() => {
  vi.resetAllMocks();
  resetUrl({ kind: "v2", catalog });
});
afterEach(cleanup);

describe("증거 펼치기", () => {
  it("안에 든 JSON 문자열까지 들여 펼치고 비밀 값은 가린다", () => {
    const raw = JSON.stringify({
      tool_output: JSON.stringify({ status: 200, body: JSON.stringify({ token: "abc123", ok: true }) }),
    });
    const out = prettyEvidence(raw);
    expect(out).toContain('"status": 200');
    expect(out).toContain('"ok": true');
    expect(out).not.toContain("abc123");
    expect(out).not.toContain('\\"');
  });
  it("JSON 이 아니면 글 그대로(가림만)", () => {
    expect(prettyEvidence("GET / HTTP/1.1")).toBe("GET / HTTP/1.1");
  });
});

describe("레일 현재 표시", () => {
  it("이 점검의 화면을 보고 있을 때만 그 줄에 aria-current 를 단다", () => {
    useUrl.setState({ handle, job, verified: true, flow, flowVerified: true });
    const { rerender } = render(
      <ShellContext.Provider value={shell("dashboard")}>
        <LiveRailTasks />
      </ShellContext.Provider>,
    );
    expect(screen.getByRole("button", { name: /target\.example/ })).not.toHaveAttribute("aria-current");
    rerender(
      <ShellContext.Provider value={shell("scope")}>
        <LiveRailTasks />
      </ShellContext.Provider>,
    );
    const row = screen.getByRole("button", { name: /target\.example/ });
    expect(row).toHaveAttribute("aria-current", "true");
    // 상태 색 점 대신 현재 표시(mk)만 있다
    expect(row.querySelectorAll("span[aria-hidden]")).toHaveLength(1);
    expect(within(row).getByText("탐색 완료 · 범위 확인 대기")).toBeInTheDocument();
  });
});

describe("범위 확인", () => {
  it("사전 확인한 읽기 경로만 기본 포함하고, 데이터를 바꾸는 경로는 직접 고르며, 제외 경로는 고를 수 없다", () => {
    useUrl.setState({ handle, job, verified: true, flow, flowVerified: true });
    render(
      <ShellContext.Provider value={shell("scope")}>
        <WorkflowScopeLive />
      </ShellContext.Provider>,
    );
    fireEvent.click(screen.getByRole("button", { name: "모두 펼치기" }));
    expect(screen.getByRole("checkbox", { name: "GET /api/products/1 점검에 포함" })).toBeChecked();
    const write = screen.getByRole("checkbox", { name: "PUT /api/users/2 점검에 포함" });
    expect(write).not.toBeChecked();
    expect(screen.getByRole("checkbox", { name: "DELETE /api/products/1 점검에 포함" })).toBeDisabled();
    fireEvent.click(write);
    expect(write).toBeChecked();
    expect(screen.getByText("승인 필요").parentElement).toHaveTextContent("1");
    for (const b of screen.getAllByRole("button", { name: /선택한 범위로 점검 시작/ })) expect(b).toBeEnabled();
    // 묶음을 AI 추정이라고 부르지 않는다
    expect(screen.queryByText(/AI 추정/)).toBeNull();
  });
});

describe("대시보드", () => {
  it("승인 대기 요청을 지금 처리할 일 맨 위에 두고, 상세에 보낼 요청과 승인 · 거부를 보인다", async () => {
    useUrl.setState({ handle, job, verified: true, flow: { ...flow, stage: "diagnosing" }, flowVerified: true });
    vi.mocked(api.fetchRequestApprovals).mockResolvedValue({
      run: {
        run_ref: `run-${job.id}`,
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
          run_ref: `run-${job.id}`,
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
          input_preview: { body: '{"role":"customer"}' },
          body_bytes: 19,
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
    render(
      <ShellContext.Provider value={shell("dashboard")}>
        <UrlDashboardLive />
      </ShellContext.Provider>,
    );
    const todo = await screen.findByRole("region", { name: "지금 처리할 일" });
    await waitFor(() => expect(within(todo).getByText(/PUT \/api\/users\/2/)).toBeInTheDocument());
    const detail = screen.getByRole("complementary", { name: "선택한 항목 상세" });
    expect(within(detail).getByText("데이터를 바꾸는 요청")).toBeInTheDocument();
    expect(within(detail).getByText(/Host: target\.example/)).toBeInTheDocument();
    expect(within(detail).getByRole("button", { name: "승인하고 보내기" })).toBeInTheDocument();
    expect(within(detail).getByRole("button", { name: "거부" })).toBeInTheDocument();
  });
});
