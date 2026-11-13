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
import {
  crawlElapsed,
  durText,
  eventText,
  group,
  Pth,
  WorkflowCrawlLive,
  WorkflowScopeLive,
} from "../views/live/workflowViews";

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
    // 묶음은 경로 규칙으로 나눈 것이라 AI 추정 묶음이라고 부르지 않는다
    expect(screen.getAllByText("경로 규칙 묶음").length).toBeGreaterThan(0);
    expect(screen.queryByText("AI 추정 묶음")).toBeNull();
    // 상세는 표의 첫 경로를 기본으로 보이고, 묶음 줄은 경로 규칙이라고 적는다
    const insp = screen.getByRole("complementary", { name: "선택한 경로 상세" });
    expect(within(insp).getByText("묶음").nextElementSibling).toHaveTextContent(/ · 경로 규칙$/);
  });

  it("시안의 로그인 필요 · 시작 전 점검 · 새 경로 처리 규칙은 서버 값이 없어 '연결 전'으로 보이고, 로그인 필요 거르기는 누를 수 있다", () => {
    useUrl.setState({ handle, job, verified: true, flow, flowVerified: true });
    render(
      <ShellContext.Provider value={shell("scope")}>
        <WorkflowScopeLive />
      </ShellContext.Provider>,
    );
    const pre = screen.getByLabelText("시작 전 점검");
    // 연결 결과를 지어내지 않는다: 값은 비우고 성공 표시(체크)도 두지 않는다
    expect(pre).toHaveTextContent("대상 연결 —계정 로그인 —모델 연결 —");
    expect(pre).toHaveTextContent("연결 전");
    expect(pre.querySelector("svg")).toBeNull();
    expect(screen.getByText("로그인 필요", { selector: "#sCount span" })).toHaveTextContent("로그인 필요—");
    // '로그인 필요' 거르기는 누를 수 있지만 서버 값이 없어 맞는 경로 대신 연결 전 안내가 나온다
    const authChip = screen.getByRole("button", { name: /^로그인 필요/ });
    expect(authChip).toBeEnabled();
    fireEvent.click(authChip);
    expect(authChip).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByText(/^연결 전 · 서버가 경로별 로그인 필요 여부를 아직 주지 않습니다/)).toBeInTheDocument();
    const rules = screen.getByText("점검 중 새로 찾은 경로 처리 규칙").closest("details") as HTMLElement;
    expect(within(rules).getAllByRole("listitem")).toHaveLength(6);
    expect(within(rules).getByText("연결 전")).toHaveAttribute(
      "title",
      expect.stringMatching(/^서버가 점검 중 새 경로를/),
    );
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

  it("찾은 경로: 아직 안 나온 묶음 · 거르기별 빈 글 · 로그인 범례 자리 · 경로 상세의 요청 횟수 · 기능 자리 (시안 v24 · v32)", () => {
    useUrl.setState({
      handle,
      job,
      verified: true,
      flow: {
        ...flow,
        stage: "crawling",
        initial_completed: false,
        endpoints: [ep("read", "GET", "/api/products/1", "attested_read")],
      },
      flowVerified: true,
    });
    render(
      <ShellContext.Provider value={shell("crawl")}>
        <WorkflowCrawlLive />
      </ShellContext.Provider>,
    );
    const aside = screen.getByRole("complementary", { name: "찾은 경로" });
    // 탐색 중 상품·리뷰 하나만 나왔으면 나머지 묶음이 '아직 찾지 못한 묶음' (시안 카드 순서)
    expect(within(aside).getByText(/^아직 찾지 못한 묶음 · /)).toHaveTextContent(
      "공개 페이지·API, 계정·인증, 장바구니·주문, 결제·쿠폰, 관리자, 정적 자원",
    );
    // 범례의 로그인 수는 서버가 나누지 않아 값을 비운다
    expect(within(aside).getByText(/^로그인/)).toHaveTextContent("로그인 —");
    fireEvent.click(within(aside).getByRole("radio", { name: "승인 필요" }));
    expect(within(aside).getByText("승인이 필요한 경로가 아직 없습니다.")).toBeInTheDocument();
    fireEvent.click(within(aside).getByRole("radio", { name: "제외" }));
    expect(within(aside).getByText("제외한 경로가 아직 없습니다.")).toBeInTheDocument();
    fireEvent.click(within(aside).getByRole("radio", { name: "전체" }));
    fireEvent.click(within(aside).getByRole("button", { name: /\/api\/products\/1/ }));
    const detail = screen.getByRole("complementary", { name: "찾은 경로" });
    expect(within(detail).getByText("요청 횟수").nextElementSibling).toHaveTextContent(/^—$/);
    expect(within(detail).getByText("기능").nextElementSibling).toHaveTextContent("— 연결 전");
  });

  it("탐색 막대는 페이지 상한 대비 방문 수, 숫자 칸 보조 글은 서버 값, 경과는 기록이 없으면 — (시안 v24)", () => {
    useUrl.setState({
      handle,
      job,
      verified: true,
      flow: { ...flow, stage: "crawling", initial_completed: false, counts: { ...flow.counts, visited: 1 } },
      flowVerified: true,
    });
    const { rerender } = render(
      <ShellContext.Provider value={shell("crawl")}>
        <WorkflowCrawlLive />
      </ShellContext.Provider>,
    );
    // 방문 1 / 페이지 상한 4 → 25%
    const bar = screen.getByRole("progressbar", { name: /탐색 진행/ });
    expect(bar).toHaveAttribute("aria-valuenow", "25");
    expect(screen.getByText(/^찾은 경로 · 조회 1 · 데이터 변경 1$/)).toBeInTheDocument();
    // 탐색 단계에서는 GET 만 보낸다 (서버 Controller.Before)
    expect(screen.getByText(/^보낸 요청 · 조회만$/)).toBeInTheDocument();
    expect(screen.getByText(/^경과/)).toHaveTextContent(/^경과 —$/);
    // 첫 탐색 단계가 끝나도 막대는 실제 방문 비율 그대로, 단계 끝은 따로 적는다 (리뷰 #479)
    useUrl.setState({ flow });
    rerender(
      <ShellContext.Provider value={shell("crawl")}>
        <WorkflowCrawlLive />
      </ShellContext.Provider>,
    );
    expect(screen.getByRole("progressbar", { name: /탐색 진행/ })).toHaveAttribute("aria-valuenow", "25");
    expect(screen.getByText("첫 탐색 단계 끝남")).toBeInTheDocument();
  });

  it.each([
    [
      "부분 완료 · 상한 소진",
      { exploration_status: "PARTIAL", exploration_reason: "cap_exhausted" },
      "부분 완료 · cap_exhausted",
    ],
    [
      "중지",
      { stage: "stopped", exploration_status: "STOPPED", exploration_reason: "operator_stop" },
      "중지됨 · operator_stop",
    ],
  ])("%s 로 끝나도 막대를 100%% 로 바꾸지 않고 탐색 상태를 따로 보인다 (리뷰 #479 반례)", (_n, over, text) => {
    // 방문 2 / 페이지 상한 8 = 25% — 서버는 PARTIAL 에서도 initial_completed 를 true 로 둔다
    useUrl.setState({
      handle,
      job,
      verified: true,
      flow: {
        ...flow,
        stage: "awaiting_selection",
        initial_completed: true,
        limits: { ...flow.limits, max_pages: 8 },
        counts: { ...flow.counts, visited: 2 },
        ...over,
      } as WorkflowSnapshot,
      flowVerified: true,
    });
    render(
      <ShellContext.Provider value={shell("crawl")}>
        <WorkflowCrawlLive />
      </ShellContext.Provider>,
    );
    expect(screen.getByRole("progressbar", { name: /탐색 진행/ })).toHaveAttribute("aria-valuenow", "25");
    expect(screen.getByText(/^탐색 상태/)).toHaveTextContent(text);
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
    const box = screen.getByRole("region", { name: "GET /api/products/1 요청 · 응답" });
    expect(within(box).getByText("html_link")).toBeInTheDocument();
    expect(within(box).getByText("GET /api/products/1 HTTP/1.1")).toBeInTheDocument();
    expect(within(box).getByText("HTTP/1.1 200 OK")).toBeInTheDocument();
    // 한 번에 하나: 다른 줄을 열면 앞 줄은 접힌다. 응답 코드가 없는 줄은 기록 결과만, 송신 여부는 단정하지 않는다
    fireEvent.click(putRow);
    expect(screen.queryByRole("region", { name: "GET /api/products/1 요청 · 응답" })).toBeNull();
    const put = screen.getByRole("region", { name: "PUT /api/users/2 요청 · 응답" });
    expect(within(put).getByText("held")).toBeInTheDocument();
    expect(within(put).getByText(/보냈는지 · 응답을 받았는지 확인할 수 없습니다/)).toBeInTheDocument();
    // 같은 줄을 다시 누르면 접힘
    fireEvent.click(putRow);
    expect(screen.queryByRole("region", { name: "PUT /api/users/2 요청 · 응답" })).toBeNull();
    // Esc 로 접고 초점은 그 줄로
    fireEvent.click(getRow);
    fireEvent.keyDown(
      within(screen.getByRole("region", { name: "GET /api/products/1 요청 · 응답" })).getByRole("button", {
        name: "접기",
      }),
      {
        key: "Escape",
      },
    );
    expect(screen.queryByRole("region", { name: "GET /api/products/1 요청 · 응답" })).toBeNull();
    expect(getRow).toHaveFocus();
  });
});

describe("탐색 기록 원문 결속 (리뷰 #473)", () => {
  const mountLog = (
    events: WorkflowSnapshot["events"],
    endpoint: WorkflowSnapshot["endpoints"][number],
    counts: WorkflowSnapshot["counts"] = flow.counts,
  ) => {
    useUrl.setState({
      handle,
      job,
      verified: true,
      flow: { ...flow, counts, endpoints: [endpoint], events },
      flowVerified: true,
    });
    render(
      <ShellContext.Provider value={shell("crawl")}>
        <WorkflowCrawlLive />
      </ShellContext.Provider>,
    );
    return within(document.getElementById("cLog") as HTMLElement).getAllByRole("button", { name: /GET/ });
  };

  it("같은 경로에 200 → 403 이면, 먼저 보낸 200 줄에 나중 403 원문을 붙이지 않는다", () => {
    const rows = mountLog(
      [
        { at: "2026-11-11T01:00:01Z", kind: "sent", method: "GET", path: "/api/a", status: 200 },
        { at: "2026-11-11T01:00:05Z", kind: "sent", method: "GET", path: "/api/a", status: 403 },
      ],
      {
        ...ep("a", "GET", "/api/a", "attested_read"),
        status: 403,
        request: "GET /api/a (403 요청)",
        response: "HTTP status 403",
      },
    );
    fireEvent.click(rows[0]);
    const first = screen.getByRole("region", { name: "GET /api/a 요청 · 응답" });
    expect(within(first).queryByText("HTTP status 403")).toBeNull();
    expect(within(first).getByText(/서버에 남은 원문은 이 줄의 것이 아닙니다/)).toBeInTheDocument();
    // 나중(403) 줄에는 '경로의 마지막 관찰'로 분리해 보인다
    fireEvent.click(rows[1]);
    const last = screen.getByRole("region", { name: "GET /api/a 요청 · 응답" });
    expect(within(last).getAllByText("이 경로의 마지막 관찰").length).toBeGreaterThan(0);
    expect(within(last).getByText("HTTP status 403")).toBeInTheDocument();
  });

  it("200 송신 뒤 응답 코드 없는 같은 경로 줄에는 이전 200 원문을 붙이지 않는다", () => {
    const rows = mountLog(
      [
        { at: "2026-11-11T01:00:01Z", kind: "sent", method: "GET", path: "/api/a", status: 200 },
        { at: "2026-11-11T01:00:05Z", kind: "request", method: "GET", path: "/api/a" },
      ],
      {
        ...ep("a", "GET", "/api/a", "attested_read"),
        status: 200,
        request: "GET /api/a HTTP/1.1",
        response: "HTTP/1.1 200 OK",
      },
    );
    fireEvent.click(rows[1]);
    const nostatus = screen.getByRole("region", { name: "GET /api/a 요청 · 응답" });
    expect(within(nostatus).queryByText("HTTP/1.1 200 OK")).toBeNull();
    expect(within(nostatus).getByText(/보냈는지 · 응답을 받았는지 확인할 수 없습니다/)).toBeInTheDocument();
    // 보낸 200 줄은 이 경로의 마지막 송신이라 원문을 '경로의 마지막 관찰'로 보인다
    fireEvent.click(rows[0]);
    const sent = screen.getByRole("region", { name: "GET /api/a 요청 · 응답" });
    expect(within(sent).getByText("HTTP/1.1 200 OK")).toBeInTheDocument();
  });

  const openSecretRow = () => {
    const rows = mountLog(
      [{ at: "2026-11-11T01:00:01Z", kind: "request", method: "GET", path: "/api/a", status: 200 }],
      {
        ...ep("a", "GET", "/api/a", "attested_read"),
        status: 200,
        request: "GET /api/a HTTP/1.1\nCookie: session=synthetic-cookie-1",
        response: "HTTP/1.1 200 OK\nSet-Cookie: session=synthetic-cookie-2",
      },
    );
    fireEvent.click(rows[0]);
    return screen.getByRole("region", { name: "GET /api/a 요청 · 응답" });
  };

  it("펼친 칸의 원문은 화면에서도 비밀 값을 가린다 (리뷰 #473 비차단)", () => {
    const box = openSecretRow();
    expect(box).not.toHaveTextContent("synthetic-cookie");
    expect(box).toHaveTextContent("[REDACTED]");
  });

  it("펼친 칸의 Esc 는 작업대 전역 Esc(상세 서랍 닫기)로 새지 않는다 (리뷰 #473 비차단)", () => {
    const box = openSecretRow();
    const global = vi.fn();
    document.addEventListener("keydown", global);
    fireEvent.keyDown(within(box).getByRole("button", { name: "접기" }), { key: "Escape" });
    document.removeEventListener("keydown", global);
    expect(screen.queryByRole("region", { name: "GET /api/a 요청 · 응답" })).toBeNull();
    expect(global).not.toHaveBeenCalled();
  });

  it("보냈지만 응답을 못 받은 줄(status 0 · 결과 불명)을 '보내지 않은 요청'으로 단정하지 않는다", () => {
    // 서버 Controller.After: ForwardAttempted · RequestWritten = true, ResponseReceived = false, Reason = Unknown,
    // StatusCode = 0 → counts.sent · attempted · unknown 이 오르지만 이벤트 status 는 omitempty 로 빠진다
    const rows = mountLog(
      [{ at: "2026-11-11T01:00:01Z", kind: "request", method: "GET", path: "/api/a" }],
      {
        ...ep("a", "GET", "/api/a", "attested_read"),
        visited: false,
        request: "GET /api/a HTTP/1.1",
        response: "HTTP status 0 · response incomplete",
      },
      { ...flow.counts, sent: 1, attempted: 1, unknown: 1 },
    );
    fireEvent.click(rows[0]);
    const box = screen.getByRole("region", { name: "GET /api/a 요청 · 응답" });
    expect(within(box).queryByText(/보내지 않은/)).toBeNull();
    expect(within(box).getByText(/보냈는지 · 응답을 받았는지 확인할 수 없습니다/)).toBeInTheDocument();
    expect(within(box).queryByText("HTTP status 0 · response incomplete")).toBeNull();
  });
});

describe("시안 짜임 (자산 탐색 · 범위 확인 기록 · 묶음)", () => {
  it("탐색 기록 종류는 글로 바꾸고 모르는 종류는 코드 그대로, 경과는 탐색 시작 ~ 끝 기록으로만 잰다", () => {
    expect(eventText("crawl_started")).toBe("탐색 시작");
    expect(eventText("page_failed")).toBe("관찰 실패");
    expect(eventText("awaiting_selection")).toBe("탐색 완료 · 범위 확인 대기");
    expect(eventText("constructor")).toBe("constructor");
    expect(eventText("new_server_kind")).toBe("new_server_kind");
    const at = (s: number) => `2026-11-11T01:00:${String(s).padStart(2, "0")}Z`;
    const base = { ...flow, stage: "awaiting_selection" as const };
    expect(
      crawlElapsed({
        ...base,
        events: [
          { at: at(1), kind: "crawl_started" },
          { at: at(42), kind: "crawl_finished" },
        ],
      }),
    ).toBe(41);
    // 끝 기록이 없는데 탐색이 끝났으면 모른다 (지어내지 않음)
    expect(crawlElapsed({ ...base, events: [{ at: at(1), kind: "crawl_started" }] })).toBeNull();
    // 탐색 중이면 마지막 갱신 시각까지
    expect(
      crawlElapsed({
        ...base,
        stage: "crawling",
        updated_at: at(11),
        events: [{ at: at(1), kind: "crawl_started" }],
      }),
    ).toBe(10);
    // 시작 기록이 잘려 나갔으면 모른다
    expect(crawlElapsed({ ...base, events: [] })).toBeNull();
    expect(durText(10)).toBe("10초");
    expect(durText(185)).toBe("3분 5초");
    expect(durText(3720)).toBe("1시간 2분");
  });

  it("묶음 규칙: 관리자 · 결제를 먼저 보고, {변수}는 따로 표시한다", () => {
    expect(group(ep("a", "GET", "/admin/users", "attested_read"))).toBe("관리자");
    expect(group(ep("b", "POST", "/api/checkout", "possible"))).toBe("결제·쿠폰");
    expect(group(ep("c", "GET", "/api/orders/{id}", "attested_read"))).toBe("장바구니·주문");
    render(<Pth p="/api/orders/{id}/items" />);
    expect(screen.getByText("{id}").tagName).toBe("I");
  });

  it("자산 탐색: 결과 칸은 응답 코드 · 실패 딱지 · —, 줄 아래에 기록 글, 범례 · 숫자 칸은 시안 글", () => {
    useUrl.setState({
      handle,
      job,
      verified: true,
      flow: {
        ...flow,
        events: [
          { at: "2026-11-11T01:00:00Z", kind: "crawl_started" },
          { at: "2026-11-11T01:00:01Z", kind: "visit_started", method: "GET", path: "/a" },
          { at: "2026-11-11T01:00:02Z", kind: "page_failed", method: "GET", path: "/b" },
          { at: "2026-11-11T01:00:03Z", kind: "exploration_observed", method: "GET", path: "/c", status: 403 },
        ],
      },
      flowVerified: true,
    });
    render(
      <ShellContext.Provider value={shell("crawl")}>
        <WorkflowCrawlLive />
      </ShellContext.Provider>,
    );
    const log = document.getElementById("cLog") as HTMLElement;
    expect(within(log).getByText("탐색 시작")).toBeInTheDocument();
    const [a, b, c] = within(log).getAllByRole("button");
    expect(a).toHaveTextContent(/\/a—방문 시작$/);
    expect(b).toHaveTextContent(/\/b실패관찰 실패$/);
    expect(c).toHaveTextContent(/\/c403관찰함$/);
    expect(screen.getByText(/^승인 후 전송 · 보내기 전에 승인$/)).toBeInTheDocument();
    expect(screen.getByText(/^요청 안 함 · 규칙으로 막음$/)).toBeInTheDocument();
    // 탐색 완료 · 확인 대기 (시안 cState)
    expect(screen.getByText("탐색 완료 · 확인 대기")).toBeInTheDocument();
  });

  it("점검 취소: 시안처럼 보통 버튼, 확인 상자가 열리면 초점은 취소 버튼으로, 계속 점검이면 처음 버튼으로", () => {
    useUrl.setState({ handle, job, verified: true, flow, flowVerified: true });
    render(
      <ShellContext.Provider value={shell("crawl")}>
        <WorkflowCrawlLive />
      </ShellContext.Provider>,
    );
    const open = screen.getByRole("button", { name: "점검 취소" });
    expect(open.querySelector("svg")).toBeNull();
    fireEvent.click(open);
    const box = screen.getByRole("alertdialog");
    expect(box).toHaveTextContent("점검을 취소할까요? 취소하면 다시 시작할 수 없습니다.");
    expect(box).toHaveTextContent("응답을 받지 못한 요청은 전송 불명으로 기록합니다");
    // 범위 확인 대기에는 일시정지가 없어 이어 하기 안내를 붙이지 않는다
    expect(box).not.toHaveTextContent("일시정지");
    expect(within(box).getByRole("button", { name: "점검 취소" })).toHaveFocus();
    fireEvent.click(within(box).getByRole("button", { name: "계속 점검" }));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(open).toHaveFocus();
  });

  it("범위 확인: 빠진 경로는 제외 딱지 · 제외 수 · 제외됨 거르기에 들어가고, 상세 딱지도 고른 상태를 따른다", () => {
    useUrl.setState({ handle, job, verified: true, flow, flowVerified: true });
    render(
      <ShellContext.Provider value={shell("scope")}>
        <WorkflowScopeLive />
      </ShellContext.Provider>,
    );
    // 읽기 1 포함, 쓰기 1 은 아직 안 고름, 삭제 1 은 고를 수 없음 → 제외 2
    expect(screen.getByText("제외", { selector: "#sCount span" })).toHaveTextContent("제외2");
    expect(screen.getByRole("button", { name: /^제외됨/ })).toHaveTextContent("제외됨2");
    fireEvent.click(screen.getByRole("button", { name: "모두 펼치기" }));
    const write = screen.getByRole("checkbox", { name: "PUT /api/users/2 점검에 포함" });
    const row = write.closest("div") as HTMLElement;
    expect(within(row).getByText("제외")).toBeInTheDocument();
    fireEvent.click(write);
    expect(within(row).getByText("포함 · 보낼 때 승인")).toBeInTheDocument();
    // 체크한 경로가 오른쪽 상세에 오고, 상세 딱지도 같다
    const insp = screen.getByRole("complementary", { name: "선택한 경로 상세" });
    expect(within(insp).getByText("포함 · 보낼 때 승인")).toBeInTheDocument();
  });
});

describe("대시보드", () => {
  it("승인 대기 요청은 요약 칸 · 점검 상세로 알리고, 상세에 보낼 요청과 승인 · 거부를 보인다", async () => {
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
    // 시안처럼 따로 안내 묶음을 두지 않는다: 요약 칸의 결정 대기 · 점검 줄 딱지 · 상세로 알린다 (시안 strip · inspFor)
    const decide = within(await screen.findByRole("region", { name: "요약" })).getByText("결정 대기")
      .parentElement as HTMLElement;
    await waitFor(() => expect(decide).toHaveTextContent(/^결정 대기1승인 1$/));
    expect(screen.queryByRole("region", { name: "결정 대기" })).toBeNull();
    const act = within(screen.getByRole("region", { name: "진행 중인 점검" })).getByRole("button", {
      name: /target\.example/,
    });
    expect(within(act).getByText("승인 대기")).toBeInTheDocument();
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
    // 점검 줄을 고르면 점검 상세의 결정 대기 · '승인 요청 보기' 로 다시 승인 요청을 연다 (시안 act-api)
    fireEvent.click(act);
    expect(within(detail).getByText(/^보낼 요청 1건 · 가장 빠른 만료/)).toBeInTheDocument();
    fireEvent.click(within(detail).getByRole("button", { name: "승인 요청 보기" }));
    expect(within(detail).getByText("PUT /api/users/2")).toBeInTheDocument();
  });
});
