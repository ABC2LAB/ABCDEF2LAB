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
