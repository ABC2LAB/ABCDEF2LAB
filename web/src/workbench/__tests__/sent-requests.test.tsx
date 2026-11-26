// '보낸 요청' 칸(사용자 2026-11-25): 큰 숫자는 대상에 나간 요청 총계, 마우스를 올리면 '모델 50 · 로그인 6' 내역.

import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { parseE2Flow } from "@/lib/diagnosis/e2flow";
import { e2Flow } from "@/lib/diagnosis/e2flow-fixtures";

import type { WorkflowSnapshot } from "../live/workflow";
import { SentRequestsValue } from "../views/live/sentRequests";
import { CrawlMeters } from "../views/live/workflowViews";

afterEach(cleanup);

describe("SentRequestsValue", () => {
  it("총계를 크게, 내역은 title(마우스 올림)과 읽기 프로그램 글로", () => {
    const flow = parseE2Flow({
      ...e2Flow(),
      target_requests: {
        model: 50,
        login: 6,
        confirmation: 0,
        unclassified: 0,
        exploration: 0,
        exploration_state: "none",
        total: 56,
      },
    });
    const { container } = render(<SentRequestsValue flow={flow} />);
    const el = container.querySelector("[data-sent]") as HTMLElement;
    expect(el).toHaveAttribute("title", "모델 50 · 로그인 6");
    expect(el).toHaveTextContent("56건 (모델 50 · 로그인 6)");
    expect(screen.getByText("56건", { exact: false })).toBeInTheDocument();
  });

  it("못 읽었으면 '—' — 진단 흐름의 모델 요청 수로 대신하지 않는다", () => {
    const { container } = render(<SentRequestsValue flow={null} />);
    expect(container).toHaveTextContent(/^—$/);
    expect(container.querySelector("[data-sent]")).toHaveAttribute("data-sent", "none");
  });

  // '자료 없음'과 '읽기 실패'를 가른다(팀 공유 11/26): 조회 실패를 '—' · '0건'처럼 보이지 않는다(절대규칙 5)
  it("조회에 실패했으면 '읽기 실패' — 보낸 요청이 없다는 뜻이 아니다", () => {
    const { container } = render(<SentRequestsValue flow={null} failed />);
    const el = container.querySelector("[data-sent]") as HTMLElement;
    expect(el).toHaveTextContent(/^읽기 실패$/);
    expect(el).toHaveAttribute("data-sent", "failed");
    expect(el.getAttribute("title")).toContain("없다는 뜻이 아닙니다");
  });

  it("받았는데 내역 칸이 없는 서버면 '—'에 '내역을 주지 않음' — 읽는 중과 다르다", () => {
    // 합성 fixture 는 target_requests 칸이 없는 응답이다(#929 이전 서버)
    const flow = parseE2Flow(e2Flow());
    const { container } = render(<SentRequestsValue flow={flow} />);
    const el = container.querySelector("[data-sent]") as HTMLElement;
    expect(el).toHaveTextContent(/^—$/);
    expect(el).toHaveAttribute("data-sent", "absent");
    expect(el).toHaveAttribute("title", "이 서버는 보낸 요청 내역을 주지 않습니다");
  });

  it("앞서 받은 값이 있으면 조회가 한 번 실패해도 그 값을 보인다", () => {
    const flow = parseE2Flow({
      ...e2Flow(),
      target_requests: {
        model: 3,
        login: 0,
        confirmation: 0,
        unclassified: 0,
        exploration: 0,
        exploration_state: "none",
        total: 3,
      },
    });
    const { container } = render(<SentRequestsValue flow={flow} failed />);
    expect(container.querySelector("[data-sent]")).toHaveAttribute("data-sent", "3건");
  });
});

// 자산 탐색 숫자 칸 3개는 자산 탐색 화면과 보고서가 같은 부품으로 그린다(사용자 11/25 — 보고서에도)
describe("CrawlMeters", () => {
  it("발견한 API · 보낸 요청(탐색 + 모델 · 로그인 총계) · 막은 요청", () => {
    const flow = {
      stage: "completed",
      deep_runs: 1,
      counts: { found: 2, visited: 2, queued: 0, sent: 0, attempted: 0, unknown: 0, blocked: 13 },
      endpoints: [
        { method: "GET", path: "/a", in_scope: true, change: "attested_read" },
        { method: "POST", path: "/b", in_scope: true, change: "possible" },
      ],
    } as unknown as WorkflowSnapshot;
    const e2 = parseE2Flow({
      ...e2Flow(),
      target_requests: {
        model: 110,
        login: 10,
        confirmation: 0,
        unclassified: 0,
        exploration: 38,
        exploration_state: "closed",
        total: 158,
      },
    });
    const { container } = render(<CrawlMeters flow={flow} e2={e2} />);
    const cells = [...container.querySelectorAll("[class*=cell]")].map((c) => c.textContent);
    expect(cells).toEqual([
      "발견한 API2조회 1 · 데이터 변경 1 · 제외 0",
      "보낸 요청158건 (모델 110 · 로그인 10 · 탐색 38)심화 점검 포함",
      "막은 요청13범위 밖 · 안전 규칙",
    ]);
  });
});
