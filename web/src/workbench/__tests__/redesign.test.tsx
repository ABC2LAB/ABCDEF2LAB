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
import { prettyEvidence, UrlLiveLive } from "../views/live/UrlLiveLive";
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
  const mountScope = (over: Partial<Shell> = {}) => {
    useUrl.setState({ handle, job, verified: true, flow, flowVerified: true });
    render(
      <ShellContext.Provider value={shell("scope", over)}>
        <WorkflowScopeLive />
      </ShellContext.Provider>,
    );
    return {
      found: screen.getByRole("region", { name: /^발견한 API/ }),
      insp: screen.getByRole("complementary", { name: "경로 상세" }),
    };
  };

  it("가운데는 발견한 API 카드만 있고, 옛 요약 · 시작 전 점검 · 거르기 · 표 · 새 경로 처리 규칙은 없다", () => {
    const { found } = mountScope();
    expect(within(found).getByRole("radiogroup", { name: "보이는 경로" })).toBeInTheDocument();
    expect(document.getElementById("sCount")).toBeNull();
    expect(document.getElementById("sTable")).toBeNull();
    expect(screen.queryByLabelText("시작 전 점검")).toBeNull();
    expect(screen.queryByText("점검 중 새로 찾은 경로 처리 규칙")).toBeNull();
    // 제목(발견한 API · 수)과 그 위 '탐색하는 대로 채워짐' 글은 화면에 없다
    expect(within(found).queryByRole("heading")).toBeNull();
    expect(within(found).queryByText(/발견한 API/)).toBeNull();
    expect(screen.queryByText("탐색하는 대로 채워짐")).toBeNull();
    expect(screen.queryByText(/찾은 경로/)).toBeNull();
    // 검색은 거르기 줄의 오른쪽 끝에 하나
    const filterRow = within(found).getByRole("radiogroup", { name: "보이는 경로" }).parentElement as HTMLElement;
    expect(filterRow).toContainElement(within(found).getByRole("searchbox", { name: "경로 검색" }));
    // 막대 · 범례(자동 포함 · 로그인 …) · 아래 '묶음 이름 · 경로 규칙' 글은 없다
    expect(within(found).queryByText(/^자동 포함/)).toBeNull();
    expect(within(found).queryByText(/^로그인/)).toBeNull();
    expect(within(found).queryByText("묶음 이름 · 경로 규칙")).toBeNull();
    expect(screen.queryByText("AI 추정 묶음")).toBeNull();
  });

  it("오른쪽 패널은 경로를 고르기 전엔 접어 두고, 펼치기를 누르면 고르라는 안내로 펼친다", () => {
    const openInsp = vi.fn();
    const { insp } = mountScope({ openInsp });
    expect(within(insp).getByText("경로를 고르세요")).toBeInTheDocument();
    expect(insp.parentElement?.className).toMatch(/vb-fold/);
    fireEvent.click(screen.getByRole("button", { name: "오른쪽 패널 펼치기" }));
    expect(openInsp).toHaveBeenCalledTimes(1);
    expect(insp.parentElement?.className).not.toMatch(/vb-fold/);
    expect(screen.getByRole("button", { name: "오른쪽 패널 접기" })).toBeInTheDocument();
  });

  it("알약을 누르면 오른쪽 상세에서 점검 포함을 고른다: 읽기 경로는 기본 포함, 데이터 변경은 직접, 제외 경로는 못 고름", () => {
    const openInsp = vi.fn();
    const { found, insp } = mountScope({ openInsp });
    const getPill = within(found).getByRole("button", { name: /^GET \/api\/products\/1, 점검에 포함/ });
    fireEvent.click(getPill);
    expect(openInsp).toHaveBeenCalledTimes(1);
    expect(insp.parentElement?.className).not.toMatch(/vb-fold/);
    expect(getPill).toHaveAttribute("aria-current", "true");
    expect(within(insp).getByRole("checkbox", { name: "GET /api/products/1 점검에 포함" })).toBeChecked();
    expect(within(insp).getByText("자동 포함")).toBeInTheDocument();
    // 묶음 줄은 경로 규칙이라고 적는다
    expect(within(insp).getByText("묶음").nextElementSibling).toHaveTextContent(/ · 경로 규칙$/);
    // 데이터를 바꾸는 경로는 기본으로 빠져 있고 직접 넣는다. 넣으면 알약에 표시되고 상세 딱지도 바뀐다
    fireEvent.click(within(found).getByRole("button", { name: /^PUT \/api\/users\/2/ }));
    expect(getPill).not.toHaveAttribute("aria-current");
    const write = within(insp).getByRole("checkbox", { name: "PUT /api/users/2 점검에 포함" });
    expect(write).not.toBeChecked();
    expect(within(insp).getByText("제외")).toBeInTheDocument();
    fireEvent.click(write);
    expect(write).toBeChecked();
    expect(within(insp).getByText("포함 · 보낼 때 승인")).toBeInTheDocument();
    expect(within(found).getByRole("button", { name: /^PUT \/api\/users\/2.*점검에 포함/ })).toBeInTheDocument();
    for (const b of screen.getAllByRole("button", { name: /선택한 범위로 점검 시작/ })) expect(b).toBeEnabled();
    // 범위 밖 경로는 고를 수 없다
    fireEvent.click(within(found).getByRole("button", { name: /^DELETE \/api\/products\/1/ }));
    expect(within(insp).getByRole("checkbox", { name: "DELETE /api/products/1 점검에 포함" })).toBeDisabled();
  });
});

describe("범위 확인 · 한 번에 고르기", () => {
  const mount = (over: Partial<Shell> = {}, f: WorkflowSnapshot = flow) => {
    useUrl.setState({ handle, job, verified: true, flow: f, flowVerified: true });
    render(
      <ShellContext.Provider value={shell("scope", over)}>
        <WorkflowScopeLive />
      </ShellContext.Provider>,
    );
    return screen.getByRole("region", { name: /^발견한 API/ });
  };
  const products = group(ep("x", "GET", "/api/products/1", "attested_read"));

  it("묶음 머리 체크로 묶음 전체를 넣고 빼며, 고를 수 없는 경로는 건너뛰고 알린다", () => {
    const found = mount();
    const box = within(found).getByRole("checkbox", { name: `${products} 묶음 전체 점검에 포함` });
    // 읽기 1 은 기본 포함, 같은 묶음의 삭제 1 은 범위 밖 → 고를 수 있는 것은 전부 들어가 있어 체크
    expect(box).toBeChecked();
    fireEvent.click(box);
    expect(box).not.toBeChecked();
    // 결과 안내 글은 두지 않는다(알약 표시로 보인다)
    expect(within(found).queryByRole("status")).toBeNull();
    expect(within(found).queryAllByRole("button", { name: /점검에 포함/ })).toHaveLength(0);
    fireEvent.click(box);
    expect(box).toBeChecked();
  });

  it("묶음의 일부만 넣었으면 묶음 체크는 빈칸이다(반쯤 체크 '−'를 쓰지 않는다)", () => {
    const f: WorkflowSnapshot = {
      ...flow,
      endpoints: [ep("r", "GET", "/api/products/1", "attested_read"), ep("w", "PUT", "/api/products/2", "possible")],
    };
    expect(group(f.endpoints[1])).toBe(products);
    const found = mount({}, f);
    const box = within(found).getByRole("checkbox", { name: `${products} 묶음 전체 점검에 포함` }) as HTMLInputElement;
    expect(box.checked).toBe(false);
    expect(box.indeterminate).toBe(false);
    fireEvent.click(box);
    expect(box.checked).toBe(true);
    expect(box.indeterminate).toBe(false);
  });

  it("제외 경로(여러 개 고르기): 알약은 상세 대신 고르기를 토글하고, 고른 것을 한 번에 넣고 빼며, 끄면 다시 상세", () => {
    const openInsp = vi.fn();
    const found = mount({ openInsp });
    const mode = within(found).getByRole("button", { name: "제외 경로" });
    fireEvent.click(mode);
    expect(mode).toHaveAttribute("aria-pressed", "true");
    const getPill = within(found).getByRole("button", { name: /^GET \/api\/products\/1/ });
    const putPill = within(found).getByRole("button", { name: /^PUT \/api\/users\/2/ });
    // 알약 전체가 아니라 메서드 옆 네모칸에만 체크가 뜬다
    const box = (pill: HTMLElement) => pill.querySelector("[data-pick]");
    expect(box(getPill)).toHaveAttribute("data-pick", "off");
    expect(box(getPill)?.nextElementSibling).toHaveTextContent("GET");
    fireEvent.click(getPill);
    expect(box(getPill)).toHaveAttribute("data-pick", "on");
    fireEvent.click(putPill);
    expect(openInsp).not.toHaveBeenCalled();
    expect(getPill).toHaveAttribute("aria-pressed", "true");
    expect(putPill).toHaveAttribute("aria-pressed", "true");
    const bar = within(found).getByRole("group", { name: "고른 경로 포함 · 제외" });
    // 몇 개 골랐는지는 보이지 않는다
    expect(within(found).queryByText(/개 고름/)).toBeNull();
    fireEvent.click(within(bar).getByRole("button", { name: "포함하기" }));
    expect(within(found).queryByRole("status")).toBeNull();
    // 넣고 나면 고르기는 비워진다. 데이터 변경 경로도 들어간다(보낼 때 승인은 그대로)
    expect(getPill).toHaveAttribute("aria-pressed", "false");
    expect(within(found).getAllByRole("button", { name: /점검에 포함/ })).toHaveLength(2);
    // 범위 밖 경로가 섞이면 그 경로는 건너뛴다(그대로 제외)
    fireEvent.click(putPill);
    fireEvent.click(within(found).getByRole("button", { name: /^DELETE \/api\/products\/1/ }));
    fireEvent.click(within(bar).getByRole("button", { name: "제외하기" }));
    expect(putPill).not.toHaveAccessibleName(/점검에 포함/);
    expect(getPill).toHaveAccessibleName(/점검에 포함/);
    expect(within(found).getByRole("button", { name: /^DELETE \/api\/products\/1/ })).not.toHaveAccessibleName(
      /점검에 포함/,
    );
    // 끄면 알약은 다시 상세를 연다(네모칸도 사라진다)
    fireEvent.click(mode);
    expect(mode).toHaveAttribute("aria-pressed", "false");
    expect(box(getPill)).toBeNull();
    expect(within(found).queryByRole("group", { name: "고른 경로 포함 · 제외" })).toBeNull();
    fireEvent.click(getPill);
    expect(openInsp).toHaveBeenCalledTimes(1);
  });

  it("제외 경로(여러 개 고르기)를 안 켜도 아래 줄이 늘 보이고, '제외하기'는 오른쪽 상세에 떠 있는 경로 하나를 뺀다", () => {
    const found = mount();
    const drop = within(found).getByRole("button", { name: "제외하기" });
    // 거르기(전체 · 승인 필요 · 제외) 옆에는 제외 경로만, 넣기 · 빼기는 아래 줄에 늘 있다
    const filterRow = within(found).getByRole("radiogroup", { name: "보이는 경로" }).parentElement as HTMLElement;
    expect(filterRow).toContainElement(within(found).getByRole("button", { name: "제외 경로" }));
    expect(filterRow).not.toContainElement(drop);
    const bar = within(found).getByRole("group", { name: "상세 경로 포함 · 제외" });
    expect(bar).toContainElement(drop);
    expect(bar).toContainElement(within(found).getByRole("button", { name: "포함하기" }));
    // 예전 말(점검에 넣기 · 점검에서 빼기)은 없다
    expect(within(found).queryByText(/점검에 넣기|점검에서 빼기/)).toBeNull();
    // 상세에 띄운 경로가 없으면 뺄 것이 없다
    expect(drop).toBeDisabled();
    const getPill = within(found).getByRole("button", { name: /^GET \/api\/products\/1, 점검에 포함/ });
    fireEvent.click(getPill);
    expect(drop).toBeEnabled();
    fireEvent.click(drop);
    expect(within(found).queryByRole("status")).toBeNull();
    expect(within(found).queryAllByRole("button", { name: /점검에 포함/ })).toHaveLength(0);
    const insp = screen.getByRole("complementary", { name: "경로 상세" });
    expect(within(insp).getByText("제외")).toBeInTheDocument();
    // 이미 빠진 경로 · 고를 수 없는 경로는 뺄 것이 없다
    expect(drop).toBeDisabled();
    fireEvent.click(within(found).getByRole("button", { name: /^DELETE \/api\/products\/1/ }));
    expect(drop).toBeDisabled();
  });

  it("제외 경로를 안 켜도 아래 줄의 '포함하기'가 오른쪽 상세에 떠 있는 빠진 경로 하나를 넣는다", () => {
    const found = mount();
    const add = within(found).getByRole("button", { name: "포함하기" });
    expect(add).toBeDisabled();
    // 이미 들어 있는 읽기 경로는 넣을 것이 없다
    fireEvent.click(within(found).getByRole("button", { name: /^GET \/api\/products\/1, 점검에 포함/ }));
    expect(add).toBeDisabled();
    // 빠져 있는 데이터 변경 경로는 넣는다(보낼 때 승인은 그대로)
    fireEvent.click(within(found).getByRole("button", { name: /^PUT \/api\/users\/2/ }));
    expect(add).toBeEnabled();
    fireEvent.click(add);
    expect(within(found).queryByRole("status")).toBeNull();
    expect(within(found).getByRole("button", { name: /^PUT \/api\/users\/2.*점검에 포함/ })).toBeInTheDocument();
    const insp = screen.getByRole("complementary", { name: "경로 상세" });
    expect(within(insp).getByText("포함 · 보낼 때 승인")).toBeInTheDocument();
    expect(add).toBeDisabled();
    // 범위 밖 경로는 넣을 수 없다
    fireEvent.click(within(found).getByRole("button", { name: /^DELETE \/api\/products\/1/ }));
    expect(add).toBeDisabled();
  });

  it("제외 경로를 누르면 눌린 상태로 보이고, 줄 머리글은 고른 수 · 상세 경로를 따른다", () => {
    const found = mount();
    const mode = within(found).getByRole("button", { name: "제외 경로" });
    expect(mode).toHaveAttribute("aria-pressed", "false");
    fireEvent.click(within(found).getByRole("button", { name: /^GET \/api\/products\/1/ }));
    // 경로를 눌러도 아래 줄에 그 경로 글은 뜨지 않는다
    const bar = within(found).getByRole("group", { name: "상세 경로 포함 · 제외" });
    expect(bar).not.toHaveTextContent("/api/products/1");
    expect(bar.querySelector("legend")).toBeNull();
    fireEvent.click(mode);
    expect(mode).toHaveAttribute("aria-pressed", "true");
    expect(within(found).getByRole("group", { name: "고른 경로 포함 · 제외" })).toBeInTheDocument();
    expect(within(found).queryByText(/개 고름/)).toBeNull();
  });

  it("내가 점검에서 뺀 알약만 흐리게 · 취소선으로 표시하고, 다시 넣으면 사라진다 (처음부터 빠진 경로와 구분)", () => {
    const found = mount();
    const putPill = within(found).getByRole("button", { name: /^PUT \/api\/users\/2/ });
    const isDropped = (pill: HTMLElement) => /점검에서 제외함/.test(pill.getAttribute("aria-label") ?? "");
    // 처음부터 빠져 있던 데이터 변경 경로는 뺀 표시가 없다
    expect(isDropped(putPill)).toBe(false);
    const getPill = within(found).getByRole("button", { name: /^GET \/api\/products\/1/ });
    fireEvent.click(getPill);
    fireEvent.click(within(found).getByRole("button", { name: "제외하기" }));
    expect(isDropped(getPill)).toBe(true);
    expect(getPill.className).toMatch(/drop/);
    // '제외함' 글자는 화면에 붙이지 않는다(읽기 이름에만)
    expect(getPill).not.toHaveTextContent("제외함");
    fireEvent.click(within(found).getByRole("button", { name: "포함하기" }));
    expect(isDropped(getPill)).toBe(false);
    expect(getPill.className).not.toMatch(/drop/);
    // 여러 개 고르기로 뺀 것도 같다
    fireEvent.click(within(found).getByRole("button", { name: "제외 경로" }));
    fireEvent.click(getPill);
    fireEvent.click(within(found).getByRole("button", { name: "제외하기" }));
    expect(isDropped(getPill)).toBe(true);
    expect(isDropped(putPill)).toBe(false);
  });

  it("제외 경로(여러 개 고르기)를 켜면 '제외하기'는 고른 알약들을 한꺼번에 뺀다", () => {
    const found = mount();
    fireEvent.click(within(found).getByRole("button", { name: "제외 경로" }));
    const header = within(found).getAllByRole("button", { name: "제외하기" })[0];
    expect(header).toBeDisabled();
    fireEvent.click(within(found).getByRole("button", { name: /^GET \/api\/products\/1/ }));
    fireEvent.click(within(found).getByRole("button", { name: /^PUT \/api\/users\/2/ }));
    expect(header).toBeEnabled();
    fireEvent.click(header);
    expect(within(found).queryByRole("status")).toBeNull();
    expect(within(found).queryAllByRole("button", { name: /점검에 포함/ })).toHaveLength(0);
    // 같은 줄의 '포함하기'도 고른 알약들에 한다
    fireEvent.click(within(found).getByRole("button", { name: /^GET \/api\/products\/1/ }));
    fireEvent.click(within(found).getByRole("button", { name: /^PUT \/api\/users\/2/ }));
    fireEvent.click(within(found).getAllByRole("button", { name: "포함하기" })[0]);
    expect(within(found).getAllByRole("button", { name: /점검에 포함/ })).toHaveLength(2);
  });

  it("제외 경로를 켜면 묶음 머리 체크는 그 묶음 알약의 네모칸을 전부 고르고(넣기 · 빼기는 아님), 다시 누르면 푼다", () => {
    const f: WorkflowSnapshot = {
      ...flow,
      endpoints: [1, 2, 3].map((n) => ep(`p${n}`, "GET", `/api/products/${n}`, "attested_read")),
    };
    const found = mount({}, f);
    fireEvent.click(within(found).getByRole("button", { name: "제외 경로" }));
    const box = within(found).getByRole("checkbox", { name: `${products} 묶음 경로 전부 고르기` }) as HTMLInputElement;
    expect(box.checked).toBe(false);
    const pills = within(found).getAllByRole("button", { name: /^GET \/api\/products\// });
    // 하나만 골라도 빈칸(반쯤 체크 없음)
    fireEvent.click(pills[0]);
    expect(box.checked).toBe(false);
    expect(box.indeterminate).toBe(false);
    fireEvent.click(box);
    expect(box.checked).toBe(true);
    for (const pill of pills) expect(pill.querySelector("[data-pick]")).toHaveAttribute("data-pick", "on");
    expect(within(found).getByRole("group", { name: "고른 경로 포함 · 제외" })).toBeInTheDocument();
    // 고르기만 했지 점검 포함은 그대로(읽기 경로 3개 모두 포함)
    expect(within(found).getAllByRole("button", { name: /점검에 포함/ })).toHaveLength(3);
    fireEvent.click(box);
    expect(box.checked).toBe(false);
    for (const pill of pills) expect(pill.querySelector("[data-pick]")).toHaveAttribute("data-pick", "off");
    // 제외 경로를 끄면 묶음 체크는 다시 묶음 전체 넣기 · 빼기
    fireEvent.click(within(found).getByRole("button", { name: "제외 경로" }));
    expect(within(found).getByRole("checkbox", { name: `${products} 묶음 전체 점검에 포함` })).toBeChecked();
  });

  it("묶음 카드 숫자는 평소 묶음 경로 수, 제외 경로를 켜면 그 묶음에서 고른 수/묶음 경로 수로 보인다", () => {
    const f: WorkflowSnapshot = {
      ...flow,
      endpoints: [1, 2, 3].map((n) => ep(`p${n}`, "GET", `/api/products/${n}`, "attested_read")),
    };
    const found = mount({}, f);
    const card = within(found).getByRole("region", { name: `${products} 3개` });
    const count = () => (card.querySelector("b")?.nextElementSibling as HTMLElement).textContent;
    expect(count()).toBe("3");
    // 카드 머리 오른쪽의 '승인 N · 제외 N'은 없다
    expect(card.querySelector("b")?.parentElement).not.toHaveTextContent(/승인 \d|제외 \d/);
    const mode = within(found).getByRole("button", { name: "제외 경로" });
    fireEvent.click(mode);
    expect(count()).toBe("0/3");
    fireEvent.click(within(found).getByRole("button", { name: /^GET \/api\/products\/1/ }));
    fireEvent.click(within(found).getByRole("button", { name: /^GET \/api\/products\/3/ }));
    expect(count()).toBe("2/3");
    // 끄면 묶음 경로 수, 다시 켜면 골라 둔 수 그대로
    fireEvent.click(mode);
    expect(count()).toBe("3");
    fireEvent.click(mode);
    expect(count()).toBe("2/3");
  });

  it("제외 경로를 껐다 다시 켜도 전에 골라 둔 경로만 그대로 체크돼 있다", () => {
    const found = mount();
    const mode = within(found).getByRole("button", { name: "제외 경로" });
    fireEvent.click(mode);
    const getPill = within(found).getByRole("button", { name: /^GET \/api\/products\/1/ });
    const putPill = within(found).getByRole("button", { name: /^PUT \/api\/users\/2/ });
    fireEvent.click(putPill);
    const box = (pill: HTMLElement) => pill.querySelector("[data-pick]");
    expect(box(putPill)).toHaveAttribute("data-pick", "on");
    // 끄면 네모칸만 숨고, 넣기 · 빼기는 상세 경로 하나에만(고른 것에는 안 함)
    fireEvent.click(mode);
    expect(box(putPill)).toBeNull();
    expect(within(found).getByRole("button", { name: "제외하기" })).toBeDisabled();
    // 다시 켜면 전에 고른 경로만 체크
    fireEvent.click(mode);
    expect(box(putPill)).toHaveAttribute("data-pick", "on");
    expect(box(getPill)).toHaveAttribute("data-pick", "off");
  });

  it("검색창은 메서드 · 경로 글자로 알약을 거르고(대소문자 무시), 맞는 게 없으면 알린다", () => {
    const found = mount();
    const search = within(found).getByRole("searchbox", { name: "경로 검색" });
    fireEvent.change(search, { target: { value: "USERS" } });
    expect(within(found).getByRole("button", { name: /^PUT \/api\/users\/2/ })).toBeInTheDocument();
    expect(within(found).queryByRole("button", { name: /^GET \/api\/products\/1/ })).toBeNull();
    fireEvent.change(search, { target: { value: "delete" } });
    expect(within(found).getByRole("button", { name: /^DELETE \/api\/products\/1/ })).toBeInTheDocument();
    expect(within(found).queryByRole("button", { name: /^PUT / })).toBeNull();
    fireEvent.change(search, { target: { value: "/nothing-here" } });
    expect(within(found).getByText("검색과 맞는 경로가 없습니다.")).toBeInTheDocument();
    fireEvent.change(search, { target: { value: "" } });
    expect(within(found).getAllByRole("button", { name: /^(GET|PUT|DELETE) \// })).toHaveLength(3);
  });

  it("제외 경로를 켜도 Shift 안내 글은 없다", () => {
    const found = mount();
    fireEvent.click(within(found).getByRole("button", { name: "제외 경로" }));
    expect(within(found).queryByText(/Shift/)).toBeNull();
  });

  it("Shift 를 누른 채 누르면 같은 묶음 안에서 앞서 누른 알약부터 그 알약까지 고른다", () => {
    const f: WorkflowSnapshot = {
      ...flow,
      endpoints: [1, 2, 3].map((n) => ep(`p${n}`, "GET", `/api/products/${n}`, "attested_read")),
    };
    const found = mount({}, f);
    fireEvent.click(within(found).getByRole("button", { name: "제외 경로" }));
    fireEvent.click(within(found).getByRole("button", { name: /^GET \/api\/products\/1/ }));
    fireEvent.click(within(found).getByRole("button", { name: /^GET \/api\/products\/3/ }), { shiftKey: true });
    expect(within(found).getByRole("button", { name: /^GET \/api\/products\/2/ })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(within(found).getByRole("group", { name: "고른 경로 포함 · 제외" })).toBeInTheDocument();
  });

  it("범위 확인이 끝난 뒤(선택 대기 아님)에는 묶음 체크와 제외 경로 · 포함하기 · 제외하기를 쓸 수 없다", () => {
    const found = mount({}, { ...flow, stage: "diagnosing" });
    expect(within(found).getByRole("button", { name: "제외 경로" })).toBeDisabled();
    expect(within(found).getByRole("button", { name: "제외하기" })).toBeDisabled();
    expect(within(found).getByRole("button", { name: "포함하기" })).toBeDisabled();
    for (const box of within(found).getAllByRole("checkbox", { name: /묶음 전체 점검에 포함/ }))
      expect(box).toBeDisabled();
  });
});

describe("범위 확인 · 발견한 API (자산 탐색 오른쪽 패널에서 옮김)", () => {
  it("발견한 API는 묶음 카드로만 보이고 '주소 구조' 보기 전환은 없다 (시안 v32)", () => {
    useUrl.setState({ handle, job, verified: true, flow, flowVerified: true });
    render(
      <ShellContext.Provider value={shell("scope")}>
        <WorkflowScopeLive />
      </ShellContext.Provider>,
    );
    const found = screen.getByRole("region", { name: /^발견한 API/ });
    expect(within(found).getByRole("radiogroup", { name: "보이는 경로" })).toBeInTheDocument();
    expect(screen.queryByRole("radiogroup", { name: "보기 방식" })).toBeNull();
    expect(screen.queryByText("주소 구조")).toBeNull();
  });

  it("발견한 API: 아직 안 나온 묶음 · 거르기별 빈 글 · 로그인 범례 자리 · 경로 상세의 요청 횟수 · 기능 자리 (시안 v24 · v32)", () => {
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
      <ShellContext.Provider value={shell("scope")}>
        <WorkflowScopeLive />
      </ShellContext.Provider>,
    );
    const found = screen.getByRole("region", { name: /^발견한 API/ });
    // 탐색 중 상품·리뷰 하나만 나왔으면 나머지 묶음이 '아직 찾지 못한 묶음' (시안 카드 순서)
    expect(within(found).getByText(/^아직 찾지 못한 묶음 · /)).toHaveTextContent(
      "공개 페이지·API, 계정·인증, 장바구니·주문, 결제·쿠폰, 관리자, 정적 자원",
    );
    fireEvent.click(within(found).getByRole("radio", { name: "승인 필요" }));
    expect(within(found).getByText("승인이 필요한 경로가 아직 없습니다.")).toBeInTheDocument();
    fireEvent.click(within(found).getByRole("radio", { name: "제외" }));
    expect(within(found).getByText("제외한 경로가 아직 없습니다.")).toBeInTheDocument();
    fireEvent.click(within(found).getByRole("radio", { name: "전체" }));
    fireEvent.click(within(found).getByRole("button", { name: /\/api\/products\/1/ }));
    const detail = screen.getByRole("complementary", { name: "경로 상세" });
    expect(within(detail).getByText("요청 횟수").nextElementSibling).toHaveTextContent(/^—$/);
    expect(within(detail).getByText("기능").nextElementSibling).toHaveTextContent("— 연결 전");
  });
});

describe("자산 탐색", () => {
  const withRow: WorkflowSnapshot = {
    ...flow,
    events: [{ at: "2026-11-11T01:00:01Z", kind: "sent", method: "GET", path: "/api/products/1", status: 200 }],
  };

  it("오른쪽 패널은 기록을 고르기 전엔 접어 두고, 발견한 API 목록은 자산 탐색에 없다", () => {
    const openInsp = vi.fn();
    useUrl.setState({ handle, job, verified: true, flow: withRow, flowVerified: true });
    render(
      <ShellContext.Provider value={shell("crawl", { openInsp })}>
        <WorkflowCrawlLive />
      </ShellContext.Provider>,
    );
    // 머리의 패널 버튼(좁은 화면 서랍)은 '기록 상세'
    expect(screen.getByRole("button", { name: "기록 상세" })).toBeInTheDocument();
    expect(screen.queryByRole("radiogroup", { name: "보이는 경로" })).toBeNull();
    const aside = screen.getByRole("complementary", { name: "기록 상세" });
    expect(within(aside).getByText("기록을 고르세요")).toBeInTheDocument();
    // 이 화면만 접은 모양: 펼치기 버튼이 보이고(공통 접힘은 펼친 상태), 누르면 고르라는 안내가 보이는 패널로 펼친다
    const unfold = screen.getByRole("button", { name: "오른쪽 패널 펼치기" });
    expect(unfold).toHaveAttribute("aria-expanded", "false");
    expect(aside.parentElement?.className).toMatch(/vb-fold/);
    fireEvent.click(unfold);
    expect(openInsp).toHaveBeenCalledTimes(1);
    expect(aside.parentElement?.className).not.toMatch(/vb-fold/);
    expect(screen.getByRole("button", { name: "오른쪽 패널 접기" })).toBeInTheDocument();
  });

  it("기록 줄을 누르면 패널이 펼쳐지고 그 기록이 오른쪽 패널에 나온다 (줄 아래로 펼치지 않는다)", () => {
    const openInsp = vi.fn();
    useUrl.setState({ handle, job, verified: true, flow: withRow, flowVerified: true });
    render(
      <ShellContext.Provider value={shell("crawl", { openInsp })}>
        <WorkflowCrawlLive />
      </ShellContext.Provider>,
    );
    const log = document.getElementById("cLog") as HTMLElement;
    fireEvent.click(within(log).getByRole("button", { name: /GET\s*\/api\/products\/1/ }));
    expect(openInsp).toHaveBeenCalledTimes(1);
    const aside = screen.getByRole("complementary", { name: "기록 상세" });
    expect(aside.parentElement?.className).not.toMatch(/vb-fold/);
    expect(within(aside).getByRole("region", { name: "GET /api/products/1 요청 · 응답" })).toBeInTheDocument();
    expect(within(log).queryByRole("region")).toBeNull();
  });

  it("AI 자동 모드에서 첫 탐색 뒤 심화 진단 중이면 '지금 보는 곳'에 심화 진단 중이라고 같이 적는다", () => {
    const auto = { ...job, input: { ...job.input, workflow_mode: "ai_auto_v1" as const } };
    const view = (f: WorkflowSnapshot) => {
      useUrl.setState({ handle, job: auto, verified: true, flow: f, flowVerified: true });
      return render(
        <ShellContext.Provider value={shell("crawl")}>
          <WorkflowCrawlLive />
        </ShellContext.Provider>,
      );
    };
    const { unmount } = view({ ...flow, mode: "ai_auto_v1", stage: "diagnosing", initial_completed: true });
    expect(document.getElementById("cNow")).toHaveTextContent(
      "탐색을 마쳤습니다 · 지금은 심화 진단 중 (아래 기록에 심화 진단 요청이 이어서 쌓입니다)",
    );
    unmount();
    // 사용자 승인 모드의 범위 확인 대기는 지금처럼 '탐색을 마쳤습니다'
    view({ ...flow, stage: "awaiting_selection", initial_completed: true });
    expect(document.getElementById("cNow")).toHaveTextContent(/^탐색을 마쳤습니다$/);
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
    expect(screen.getByText(/^발견한 API · 조회 1 · 데이터 변경 1$/)).toBeInTheDocument();
    // 탐색 단계에서는 GET 만 보낸다 (서버 Controller.Before)
    expect(screen.getByText(/^보낸 요청 · 조회만$/)).toBeInTheDocument();
    expect(screen.getByText(/^경과/)).toHaveTextContent(/^경과 —$/);
    // 첫 탐색 단계가 끝나도(서버가 탐색 완료라고 하지 않으면) 막대는 실제 방문 비율 그대로 (리뷰 #479).
    // '첫 탐색 단계 끝남' 글은 위 '탐색을 마쳤습니다'와 겹쳐 적지 않는다
    useUrl.setState({ flow });
    rerender(
      <ShellContext.Provider value={shell("crawl")}>
        <WorkflowCrawlLive />
      </ShellContext.Provider>,
    );
    expect(screen.getByRole("progressbar", { name: /탐색 진행/ })).toHaveAttribute("aria-valuenow", "25");
    expect(screen.queryByText("첫 탐색 단계 끝남")).toBeNull();
  });

  it("서버가 탐색 완료(COMPLETED)라고 하면 막대를 끝까지 채우고, 겹치는 '탐색 상태' 글은 적지 않는다", () => {
    // 방문 9 / 페이지 상한 12 = 75% 지만 알려진 링크를 다 돌아 서버가 완료라고 했다
    useUrl.setState({
      handle,
      job,
      verified: true,
      flow: {
        ...flow,
        stage: "awaiting_selection",
        initial_completed: true,
        limits: { ...flow.limits, max_pages: 12 },
        counts: { ...flow.counts, visited: 9, queued: 0 },
        exploration_status: "COMPLETED",
        exploration_reason: "completed",
      } as WorkflowSnapshot,
      flowVerified: true,
    });
    render(
      <ShellContext.Provider value={shell("crawl")}>
        <WorkflowCrawlLive />
      </ShellContext.Provider>,
    );
    const bar = screen.getByRole("progressbar", { name: /탐색 진행/ });
    expect(bar).toHaveAttribute("aria-valuenow", "100");
    expect(bar).toHaveAttribute("aria-valuetext", "100% · 탐색 완료");
    expect(document.getElementById("cNow")).toHaveTextContent(/^탐색을 마쳤습니다$/);
    expect(screen.queryByText(/^탐색 상태/)).toBeNull();
    expect(screen.queryByText("첫 탐색 단계 끝남")).toBeNull();
    // 방문 · 대기 · 상한 숫자는 그대로 남긴다
    expect(document.getElementById("cVis")).toHaveTextContent("9");
  });

  it.each([
    [
      "한도 소진 사유는 status 뒤에 보인다",
      { exploration_status: "PARTIAL", exploration_reason: "cap_exhausted" },
      "탐색 상태 부분 완료 · 요청 상한 소진",
    ],
    [
      "모델 오류 사유는 status 뒤에 보인다",
      { exploration_status: "PARTIAL", exploration_reason: "model_error" },
      "탐색 상태 부분 완료 · 모델 오류",
    ],
    [
      "status 와 모순되는 사유(PARTIAL 의 completed)는 화면에서 숨긴다",
      { exploration_status: "PARTIAL", exploration_reason: "completed" },
      "탐색 상태 부분 완료",
    ],
  ])("%s · 막대는 100%% 로 바꾸지 않고 탐색 상태를 따로 보인다 (리뷰 #479)", (_n, over, text) => {
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
    // 화면은 status 중심: 뜻을 더하는 사유만 뒤에 붙고, 겹치거나 모순되는 토큰은 빠진다(원본 영문 토큰 노출 금지)
    expect(screen.getByText(/^탐색 상태/).textContent).toBe(text);
  });
});

describe("탐색 기록 상세", () => {
  it("줄을 누르면 오른쪽 패널에 서버 관찰(결과 · 찾은 곳 · 출처 · 요청/응답)이 나오고, 다른 줄을 누르면 그 줄로 바뀐다", () => {
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
    const aside = screen.getByRole("complementary", { name: "기록 상세" });
    const getRow = within(log).getByRole("button", { name: /GET\s*\/api\/products\/1/ });
    const putRow = within(log).getByRole("button", { name: /PUT\s*\/api\/users\/2/ });
    expect(getRow).not.toHaveAttribute("aria-current");
    fireEvent.click(getRow);
    expect(getRow).toHaveAttribute("aria-current", "true");
    const box = within(aside).getByRole("region", { name: "GET /api/products/1 요청 · 응답" });
    expect(within(box).getByText("html_link")).toBeInTheDocument();
    expect(within(box).getByText("GET /api/products/1 HTTP/1.1")).toBeInTheDocument();
    expect(within(box).getByText("HTTP/1.1 200 OK")).toBeInTheDocument();
    // 한 번에 하나: 다른 줄을 누르면 패널이 그 줄로 바뀐다. 응답 코드가 없는 줄은 기록 결과만, 송신 여부는 단정하지 않는다
    fireEvent.click(putRow);
    expect(getRow).not.toHaveAttribute("aria-current");
    expect(screen.queryByRole("region", { name: "GET /api/products/1 요청 · 응답" })).toBeNull();
    const put = within(aside).getByRole("region", { name: "PUT /api/users/2 요청 · 응답" });
    expect(within(put).getByText("held")).toBeInTheDocument();
    expect(within(put).getByText(/보냈는지 · 응답을 받았는지 확인할 수 없습니다/)).toBeInTheDocument();
    // 같은 줄을 다시 눌러도 그대로 (줄 아래로 펼치고 접던 동작은 없다)
    fireEvent.click(putRow);
    expect(within(aside).getByRole("region", { name: "PUT /api/users/2 요청 · 응답" })).toBeInTheDocument();
    expect(within(log).queryByRole("region")).toBeNull();
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

  it("범위 확인: 점검에 넣은 경로의 알약만 체크로 표시하고, 빼면 체크가 사라진다", () => {
    useUrl.setState({ handle, job, verified: true, flow, flowVerified: true });
    render(
      <ShellContext.Provider value={shell("scope")}>
        <WorkflowScopeLive />
      </ShellContext.Provider>,
    );
    const found = screen.getByRole("region", { name: /^발견한 API/ });
    const marked = () => within(found).queryAllByRole("button", { name: /점검에 포함/ });
    // 읽기 1 포함, 쓰기 1 은 아직 안 고름, 삭제 1 은 고를 수 없음 → 체크 1
    expect(marked()).toHaveLength(1);
    fireEvent.click(marked()[0]);
    const insp = screen.getByRole("complementary", { name: "경로 상세" });
    fireEvent.click(within(insp).getByRole("checkbox", { name: "GET /api/products/1 점검에 포함" }));
    expect(marked()).toHaveLength(0);
    expect(within(insp).getByText("제외")).toBeInTheDocument();
    // 하나도 안 고르면 시작할 수 없다
    for (const b of screen.getAllByRole("button", { name: /선택한 범위로 점검 시작/ })) expect(b).toBeDisabled();
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

describe("점검 진행", () => {
  it("가운데는 지금 하는 일 · 심화 점검 마감 수 · 계획 · 활동, 전송 통제와 탐색 현황은 오른쪽 진행 요약에", async () => {
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
      approvals: [],
    });
    const f: WorkflowSnapshot = {
      ...flow,
      stage: "diagnosing",
      deep_runs: 3,
      deep_closed: 1,
      endpoints: flow.endpoints.map((e) => (e.id === "read" ? { ...e, selected: true } : e)),
      plan: [
        { id: 1, state: "completed", title: "공개 경로 탐색" },
        { id: 2, state: "running", title: "고른 경로 심화 점검" },
      ],
      activity: [{ id: 1, at: "2026-11-11T01:00:00Z", worker: "planner", kind: "text", summary: "탐색을 마쳤습니다." }],
    };
    useUrl.setState({ handle, job, verified: true, flow: f, flowVerified: true });
    render(
      <ShellContext.Provider value={shell("live")}>
        <UrlLiveLive />
      </ShellContext.Provider>,
    );
    expect(document.getElementById("lMeta")).toHaveTextContent(/^심화 점검 1\/3 마감$/);
    // 점검 계획 · 지난 활동은 좌우 두 칸이 아니라 가운데 열에 위아래로 (각자 전체 너비)
    const planSec = screen.getByRole("region", { name: "점검 계획" });
    const feedSec = screen.getByRole("region", { name: "지난 활동" });
    expect(planSec.parentElement).toBe(feedSec.parentElement);
    expect(planSec.parentElement).toContainElement(document.getElementById("lMeta"));
    // 지난 활동 건수는 제목 밖 오른쪽, 활동 줄에 엔진 내부 이름(planner 등)은 없다
    expect(within(feedSec).getByRole("heading", { level: 2 })).toHaveTextContent(/^지난 활동$/);
    expect(document.getElementById("lFeedN")).toHaveTextContent("1건");
    expect(within(feedSec).getByText("탐색을 마쳤습니다.")).toBeInTheDocument();
    expect(within(feedSec).queryByText("planner")).toBeNull();
    // 전송 통제는 가운데 섹션이 아니라 오른쪽 진행 요약의 한 칸
    expect(screen.queryByRole("heading", { level: 2, name: "전송 통제" })).toBeNull();
    const insp = screen.getByRole("complementary", { name: "진행 요약" });
    expect(within(insp).getByRole("heading", { level: 3, name: "전송 통제" })).toBeInTheDocument();
    // 탐색 현황: 방문 · 발견한 API · 심화 선택 (서버 수 그대로)
    expect(within(insp).getByText("탐색 현황").parentElement).toHaveTextContent("방문 1발견한 API 3심화 선택 1");
  });
});
