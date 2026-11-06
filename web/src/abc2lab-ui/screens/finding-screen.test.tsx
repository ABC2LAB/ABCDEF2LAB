import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { NavContext } from "../nav";
import FindingScreen from "./FindingScreen";

// 발견 상세가 판정 레코드를 마스킹·fail-closed 로 그리는지(verdict-record §3·§4).
// 목업(getFinding)은 결정적이라 추가 모킹 없이 렌더한다.

function show() {
  return render(
    <NavContext.Provider value={{ screen: "finding", go: vi.fn() }}>
      <FindingScreen />
    </NavContext.Provider>,
  );
}

afterEach(cleanup);

describe("FindingScreen", () => {
  it("제목과 확정 판정 배지를 그린다", async () => {
    show();
    await screen.findByRole("heading", { name: /교차 계정으로 조회됨/ });
    expect(screen.getAllByText("확정").length).toBeGreaterThan(0);
  });

  it("canary 값은 안 보이고 필드명 + [REDACTED]만 보인다(절대규칙 2)", async () => {
    const { container } = show();
    await screen.findByRole("heading", { name: /교차 계정으로 조회됨/ });
    expect(screen.getByText("[REDACTED]")).toBeInTheDocument();
    expect(screen.getByText("body")).toBeInTheDocument(); // canary 필드명은 보인다
    // canary **값**(sentinel)·raw URL·쿠키 원문은 화면에 없어야 한다. 용어 "canary"는 설명문이라 허용.
    expect(container.textContent).not.toMatch(/canary[-_][0-9a-f]/i);
    expect(container.textContent).not.toMatch(/https?:\/\//);
    expect(container.textContent).not.toMatch(/Cookie:/i);
  });

  it("다계정 비교에 취약 행과 대조군(판단 불가) 행이 함께 있다", async () => {
    const { container } = show();
    await screen.findByRole("heading", { name: /교차 계정으로 조회됨/ });
    const table = container.querySelector("table.cmp");
    expect(table).not.toBeNull();
    const t = table as HTMLElement;
    expect(within(t).getByText("취약")).toBeInTheDocument();
    expect(within(t).getByText("판단 불가")).toBeInTheDocument();
  });

  it("통제변수(정책 버전 등) 재현 메타를 보여준다", async () => {
    show();
    await screen.findByRole("heading", { name: /교차 계정으로 조회됨/ });
    expect(screen.getByText("정책 버전")).toBeInTheDocument();
    expect(screen.getByText("reset 세대")).toBeInTheDocument();
  });
});
