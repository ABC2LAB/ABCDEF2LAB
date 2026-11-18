import "@testing-library/jest-dom/vitest";
import { act, cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { apiFetch } from "@/lib/diagnosis/api";
import { coverageJob, coverageSnapshot } from "@/lib/diagnosis/coverage-fixtures";

import { resetUrl, useUrl } from "../live/urlDiagnosis";
import type { WorkflowSnapshot } from "../live/workflow";
import { UrlExploreLive } from "../views/live/UrlExploreLive";

vi.mock("@/lib/diagnosis/api", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/api")>()),
  apiFetch: vi.fn(),
}));

const point = (over: Record<string, unknown>) => ({
  kind: "api",
  from: "/",
  in_scope: true,
  change: "attested_read",
  visited: true,
  selected: false,
  duplicates: 0,
  ...over,
});
/** 커버리지 시험과 같은 Job · 회차에 결속된 스냅샷(끝점은 탐색 결과가 보는 칸까지 채움). */
function snapshot(over: Record<string, unknown> = {}) {
  return {
    ...coverageSnapshot().workflow,
    counts: { found: 4, visited: 2, queued: 0, sent: 2, attempted: 2, unknown: 0, blocked: 0, omitted: 1 },
    endpoints: [
      point({
        id: "one",
        method: "GET",
        path: "/rest/products",
        source: "browser_xhr",
        status: 200,
        discovery_confidence: "observed",
        exploration_seq: 4,
        query_keys: ["q"],
      }),
      point({
        id: "two",
        method: "POST",
        path: "/api/orders",
        source: "api_recon_static",
        visited: false,
        change: "possible",
        discovery_confidence: "inferred",
        from: "/main.js",
      }),
      point({
        id: "three",
        method: "GET",
        path: "/incomplete",
        source: "html_link",
        in_scope: false,
        observation_error: "body_incomplete",
      }),
    ],
    ...over,
  };
}
function publish(flow: unknown = snapshot(), job: unknown = coverageJob) {
  useUrl.setState({
    job: job as never,
    flow: flow as WorkflowSnapshot,
    flowVerified: true,
    flowError: "",
    flowUncertain: false,
  });
}
beforeEach(() => {
  vi.resetAllMocks();
  resetUrl();
  publish();
});
afterEach(cleanup);

describe("UrlExploreLive (공유 워크플로 스냅샷)", () => {
  it("발견 경로 · 상태 · 출처를 추가 조회 없이 그리고, 예시 · 경계 기록 칸은 없다", () => {
    render(<UrlExploreLive />);
    expect(screen.getByText(/운영자 전용/)).toBeInTheDocument();
    expect(screen.queryByText(/예시/)).toBeNull();
    expect(screen.queryByRole("heading", { name: "경계 기록" })).toBeNull();
    expect(apiFetch).not.toHaveBeenCalled();
    const items = within(screen.getByRole("list", { name: "발견 목록" })).getAllByRole("listitem");
    expect(items.map((li) => li.textContent)).toEqual([
      "GET /rest/products범위 안실제 관찰방문 200 · 브라우저 요청 · 발견 위치 / · 비변경 근거query: q",
      "POST /api/orders범위 안추정미방문 · API 정적 발견 · 발견 위치 /main.js · 변경 가능",
      "GET /incomplete범위 밖방문 · 응답 코드 없음 · 응답 본문 불완전 · HTML 링크 · 발견 위치 / · 비변경 근거",
    ]);
    // 서버 누계 + 목록이 전체가 아닐 수 있음을 알린다
    const sec = screen.getByRole("region", { name: "발견" });
    expect(sec).toHaveTextContent("탐색 발견 3");
    expect(sec).toHaveTextContent("목록에서 생략 1");
    expect(sec).toHaveTextContent("아래 목록은 서버가 화면에 준 3개입니다. 서버 누계 4개 전체가 아닐 수 있습니다.");
    expect(within(screen.getByRole("list", { name: "출처별 발견" })).getAllByRole("listitem")).toHaveLength(3);
  });

  it("방문 전에 요청이 실패한 행은 '미방문'만이 아니라 오류도 보인다 (#691 리뷰)", () => {
    publish(
      snapshot({
        endpoints: [
          point({
            id: "f",
            method: "GET",
            path: "/fail",
            source: "html_link",
            visited: false,
            observation_error: "request_failed",
          }),
          point({ id: "u", method: "GET", path: "/later", source: "html_link", visited: false }),
        ],
      }),
    );
    render(<UrlExploreLive />);
    const items = within(screen.getByRole("list", { name: "발견 목록" })).getAllByRole("listitem");
    expect(items[0]).toHaveTextContent("미방문 · 요청 실패");
    expect(items[1]).toHaveTextContent("미방문 · HTML 링크");
    expect(items[1]).not.toHaveTextContent("실패");
  });

  it("관찰로 기록됐지만 seq 가 빠진 행은 추정이 아니라 '관찰 · 세부 생략' (#691 리뷰)", () => {
    publish(
      snapshot({
        endpoints: [
          point({
            id: "o",
            method: "GET",
            path: "/seen",
            source: "browser_xhr",
            status: 200,
            discovery_confidence: "observed",
          }),
        ],
      }),
    );
    render(<UrlExploreLive />);
    const sec = screen.getByRole("region", { name: "발견" });
    expect(within(screen.getByRole("list", { name: "발견 목록" })).getByRole("listitem")).toHaveTextContent(
      "관찰 · 세부 생략",
    );
    expect(sec).toHaveTextContent("관찰 · 세부 생략 1");
    expect(sec).not.toHaveTextContent("추정");
  });

  it("탐색을 실행하지 않은 회차는 상태를 보이고, 시작 경로를 발견으로 세지 않는다 (#691 리뷰)", () => {
    publish(
      snapshot({
        exploration_status: "NOT_RUN",
        exploration_reason: "browser_unavailable",
        exploration_partial: true,
        counts: { found: 1, visited: 1, queued: 0, sent: 1, attempted: 1, unknown: 0, blocked: 0 },
        endpoints: [point({ id: "s", method: "GET", path: "/", source: "start", status: 200, kind: "page" })],
      }),
    );
    render(<UrlExploreLive />);
    const sec = screen.getByRole("region", { name: "발견" });
    expect(sec).toHaveTextContent("탐색 상태: 탐색 안 함 (브라우저를 쓸 수 없음)");
    expect(sec).toHaveTextContent("이번 점검은 탐색을 실행하지 않았습니다");
    expect(sec).toHaveTextContent("탐색 발견 0");
    expect(sec).toHaveTextContent("사전 등록 경로 1");
    expect(within(screen.getByRole("list", { name: "사전 등록 경로" })).getByRole("listitem")).toHaveTextContent(
      "GET /",
    );
    expect(screen.queryByRole("list", { name: "발견 목록" })).toBeNull();
    expect(sec).toHaveTextContent("탐색을 실행하지 않아 발견한 경로가 없습니다.");
  });

  it("query 가 섞인 경로는 그대로 보이지 않는다", () => {
    publish(
      snapshot({ endpoints: [point({ id: "q", method: "GET", path: "/a?token=synthetic-x", source: "html_link" })] }),
    );
    render(<UrlExploreLive />);
    expect(screen.getByText("GET 형식 밖 경로")).toBeInTheDocument();
    expect(document.body.textContent).not.toContain("synthetic-x");
  });

  it("다른 Job · 회차의 스냅샷이면 그리지 않는다(이전 회차 결과 없음)", () => {
    publish(snapshot({ job_id: "job-old", run_ref: "run-job-old" }));
    render(<UrlExploreLive />);
    expect(screen.getByRole("alert")).toHaveTextContent("이전 회차 결과는 표시하지 않습니다");
    expect(screen.queryByRole("list", { name: "발견 목록" })).toBeNull();
  });

  it("Job 을 바꾸면(스냅샷이 비워지면) 이전 발견을 바로 지운다", () => {
    render(<UrlExploreLive />);
    expect(screen.getByRole("list", { name: "발견 목록" })).toBeInTheDocument();
    act(() =>
      useUrl.setState({ job: { ...coverageJob, id: "job-2", task_id: "43" }, flow: null, flowVerified: false }),
    );
    expect(screen.getByText("불러오는 중…")).toBeInTheDocument();
    expect(screen.queryByText("/rest/products", { exact: false })).toBeNull();
  });

  it.each([
    ["조회 오류", { flowError: "탐색 상태를 확인할 수 없습니다. 다시 조회하세요." }],
    ["명령 결과 불명", { flowUncertain: true }],
    ["검증 안 된 스냅샷", { flowVerified: false }],
  ])("%s 이면 이전 발견을 숨긴다", (_, patch) => {
    render(<UrlExploreLive />);
    act(() => useUrl.setState(patch));
    expect(screen.getByRole("alert")).toHaveTextContent("탐색 결과를 확인하지 못했습니다");
    expect(screen.queryByRole("list", { name: "발견 목록" })).toBeNull();
  });

  it("Job 이 빠지면(권한 철회 · 재로그인) 예시로 채우지 않는다", () => {
    render(<UrlExploreLive />);
    act(() => useUrl.setState({ job: null, flow: null, flowVerified: false }));
    expect(screen.getByText("실제 워크플로 진단을 선택하세요.")).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "발견" })).toBeNull();
    expect(screen.queryByText(/예시/)).toBeNull();
  });

  it("§4: 내보내기·공유·export 버튼을 두지 않는다", () => {
    render(<UrlExploreLive />);
    for (const el of [...screen.queryAllByRole("button"), ...screen.queryAllByRole("link")]) {
      expect(el.textContent ?? "").not.toMatch(/내보내기|공유|export/i);
    }
  });
});
