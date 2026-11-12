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
import { WorkflowCrawlLive, WorkflowScopeLive } from "../views/live/workflowViews";

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

describe("자산 탐색", () => {
  it("찾은 경로는 묶음 카드로만 보이고 '주소 구조' 보기 전환은 없다 (시안 v32)", () => {
    useUrl.setState({ handle, job, verified: true, flow, flowVerified: true });
    render(
      <ShellContext.Provider value={shell("crawl")}>
        <WorkflowCrawlLive />
      </ShellContext.Provider>,
    );
    expect(screen.getByRole("radiogroup", { name: "보이는 경로" })).toBeInTheDocument();
    expect(screen.queryByRole("radiogroup", { name: "보기 방식" })).toBeNull();
    expect(screen.queryByText("주소 구조")).toBeNull();
  });
});

describe("탐색 기록 펼치기", () => {
  it("줄을 누르면 바로 아래에 서버 관찰(결과 · 찾은 곳 · 출처 · 요청/응답)이 펼쳐지고, 다시 누르거나 Esc 로 접힌다 (시안 v75 ~ v76)", () => {
    const withLog: WorkflowSnapshot = {
      ...flow,
      endpoints: [
        {
          ...ep("read", "GET", "/api/products/1", "attested_read"),
          status: 200,
          request: "GET /api/products/1 HTTP/1.1",
          response: "HTTP/1.1 200 OK",
        },
        ep("write", "PUT", "/api/users/2", "possible"),
      ],
      events: [
        { at: "2026-11-11T01:00:01Z", kind: "sent", method: "GET", path: "/api/products/1", status: 200 },
        { at: "2026-11-11T01:00:02Z", kind: "held", method: "PUT", path: "/api/users/2" },
        { at: "2026-11-11T01:00:03Z", kind: "탐색 시작" },
      ],
    };
    useUrl.setState({ handle, job, verified: true, flow: withLog, flowVerified: true });
    render(
      <ShellContext.Provider value={shell("crawl")}>
        <WorkflowCrawlLive />
      </ShellContext.Provider>,
    );
    const log = document.getElementById("cLog") as HTMLElement;
    const getRow = within(log).getByRole("button", { name: /GET\s*\/api\/products\/1/ });
    const putRow = within(log).getByRole("button", { name: /PUT\s*\/api\/users\/2/ });
    expect(getRow).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(getRow);
    expect(getRow).toHaveAttribute("aria-expanded", "true");
    const box = screen.getByRole("region", { name: "GET /api/products/1 상세" });
    expect(within(box).getByText("html_link")).toBeInTheDocument();
    expect(within(box).getByText("GET /api/products/1 HTTP/1.1")).toBeInTheDocument();
    expect(within(box).getByText("HTTP/1.1 200 OK")).toBeInTheDocument();
    // 한 번에 하나: 다른 줄을 열면 앞 줄은 접힌다. 보내지 않은 줄은 기록 결과만, 원문은 없다고 말한다
    fireEvent.click(putRow);
    expect(screen.queryByRole("region", { name: "GET /api/products/1 상세" })).toBeNull();
    const put = screen.getByRole("region", { name: "PUT /api/users/2 상세" });
    expect(within(put).getByText("held")).toBeInTheDocument();
    expect(within(put).getByText("저장된 요청 · 응답이 없습니다.")).toBeInTheDocument();
    // 같은 줄을 다시 누르면 접힘
    fireEvent.click(putRow);
    expect(screen.queryByRole("region", { name: "PUT /api/users/2 상세" })).toBeNull();
    // Esc 로 접고 초점은 그 줄로
    fireEvent.click(getRow);
    fireEvent.keyDown(
      within(screen.getByRole("region", { name: "GET /api/products/1 상세" })).getByRole("button", { name: "접기" }),
      {
        key: "Escape",
      },
    );
    expect(screen.queryByRole("region", { name: "GET /api/products/1 상세" })).toBeNull();
    expect(getRow).toHaveFocus();
  });
});

describe("대시보드", () => {
  it("승인 대기 요청은 결정 대기 안내로 알리고, 상세에 보낼 요청과 승인 · 거부를 보인다", async () => {
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
          // 서버(approvalPreview)가 내는 모양 그대로: query.<키> · body.<키> · 표시 제한
          input_preview: {
            "body.role": '"customer"',
            "표시 제한":
              "호스트가 공개로 지정한 경로와 입력만 표시합니다. 비공개 입력·상태 변경 영향은 확인되지 않았습니다.",
          },
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
    // 발견 목록과 따로, 위에 결정 대기 안내 (시안 v88: 지금 처리할 일 → 발견된 취약점)
    const notes = await screen.findByRole("region", { name: "결정 대기" });
    await waitFor(() => expect(within(notes).getByText(/승인 대기 1건/)).toBeInTheDocument());
    expect(screen.queryByRole("region", { name: "지금 처리할 일" })).toBeNull();
    // 고르기 전 상세는 승인 대기 요청 (보내기 전에 사람이 정해야 함)
    const detail = screen.getByRole("complementary", { name: "선택한 항목 상세" });
    expect(within(detail).getByText("PUT /api/users/2")).toBeInTheDocument();
    expect(within(detail).getByText("쓰기 메서드")).toBeInTheDocument();
    // 보낼 요청: 대상(scheme 포함)과 서버 미리보기 키 전부, 표시 제한 안내는 서버 문장 그대로
    expect(within(detail).getByText(/대상: https:\/\/target\.example/)).toBeInTheDocument();
    expect(within(detail).getByText(/body\.role: "customer"/)).toBeInTheDocument();
    expect(within(detail).getByRole("note")).toHaveTextContent(
      "표시 제한: 호스트가 공개로 지정한 경로와 입력만 표시합니다.",
    );
    // 승인 정보: 크기 · 만료(연장 안 함) · attempt · 상태
    expect(within(detail).getByText("19 bytes · 원문 비공개")).toBeInTheDocument();
    expect(within(detail).getByText(/일시정지로 연장하지 않음$/)).toBeInTheDocument();
    expect(within(detail).getByText("attempt-1")).toBeInTheDocument();
    expect(within(detail).getByRole("button", { name: "이 요청 승인" })).toBeInTheDocument();
    expect(within(detail).getByRole("button", { name: "거부" })).toBeInTheDocument();
  });
});
