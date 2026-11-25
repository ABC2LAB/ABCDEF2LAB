// '보낸 요청' 칸(사용자 2026-11-25): 큰 숫자는 대상에 나간 요청 총계, 마우스를 올리면 '모델 50 · 로그인 6' 내역.

import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { parseE2Flow } from "@/lib/diagnosis/e2flow";
import { e2Flow } from "@/lib/diagnosis/e2flow-fixtures";

import { SentRequestsValue } from "../views/live/sentRequests";

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
