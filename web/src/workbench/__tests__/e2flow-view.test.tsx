import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { UrlE2FlowLive } from "../views/live/UrlE2FlowLive";

// 실 seam(getE2Flow → MOCK_FLOW)을 그대로 쓴다. 3분류·정규화·집계 로직은 e2flow.test.ts 가 이미 검증.
afterEach(cleanup);

describe("UrlE2FlowLive (예시)", () => {
  it("마스킹 고지·예시 배너와 요약·호출/시도를 그린다", async () => {
    const { container } = render(<UrlE2FlowLive />);
    expect(await screen.findByText(/예시 데이터/)).toBeInTheDocument();
    // 마스킹 고지(명령 원문·URL·헤더·본문 비표시)
    expect(screen.getByText(/명령 원문·URL·헤더·본문은 표시하지 않습니다/)).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "전송 통제 요약" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "호출 · 시도" })).toBeInTheDocument();
    // 시도 ID(프록시 발급 32hex)가 그대로 보인다
    expect(screen.getByText("a1f03c9e7b2d4856c0e1f2a3b4c5d6e7")).toBeInTheDocument();
    // 값·원문 마스킹: raw URL·Cookie 는 없다
    expect(container.textContent ?? "").not.toMatch(/https?:\/\//);
    expect(container.textContent ?? "").not.toMatch(/Cookie:/i);
  });

  it("3분류 fail-closed: admitted=통과, 송신 전 거부=차단, 송신 뒤 감사 실패=불명(차단 아님)", async () => {
    render(<UrlE2FlowLive />);
    await screen.findByText(/예시 데이터/);
    // admitted → 통과(allow)
    const admitted = screen.getAllByText("통과(송신 허용)")[0].closest("[data-class]") as HTMLElement;
    expect(admitted).toHaveAttribute("data-class", "allow");
    // 예산 소진(pre_send·쓰기 없음) → 차단(deny)
    const budget = screen.getByText("예산 소진").closest("[data-class]") as HTMLElement;
    expect(budget).toHaveAttribute("data-class", "deny");
    // 감사 건강 실패(sent) → 이미 나갔을 수 있어 '차단'이 아니라 '불명'(fail-closed)
    const audit = screen.getByText("감사 건강 실패").closest("[data-class]") as HTMLElement;
    expect(audit).toHaveAttribute("data-class", "unknown");
    // 색도 차단(deny)과 불명(unknown)이 달라야 한다
    expect(budget.style.background).not.toBe(audit.style.background);
    // 두 배지 모두 실제로 칠해져 있다
    expect(budget.style.background).not.toBe("");
  });

  it("미완결(중간 상태) 호출은 최종 집계가 아님을 명시한다(§5.3)", async () => {
    render(<UrlE2FlowLive />);
    await screen.findByText(/예시 데이터/);
    // MOCK_FLOW 는 finalized=false 호출 2개 → 요약 경고
    expect(screen.getByText(/최종 집계가 아닙니다/)).toHaveTextContent("2개");
  });

  it("내보내기·공유·export 버튼을 두지 않는다", async () => {
    render(<UrlE2FlowLive />);
    await screen.findByText(/예시 데이터/);
    for (const el of [...screen.queryAllByRole("button"), ...screen.queryAllByRole("link")]) {
      expect(el.textContent ?? "").not.toMatch(/내보내기|공유|export/i);
    }
  });
});
