import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { SelectionContext } from "../lib/selection";
import { NavContext } from "../nav";
import E2FlowScreen from "./E2FlowScreen";

// 전송 통제 흐름 화면이 투영을 fail-closed 로 올바로 그리는지(#183 §5·#189 §5.3).
// 목업(getE2Flow)은 결정적이라 추가 모킹 없이 렌더한다.

function show() {
  return render(
    <NavContext.Provider value={{ screen: "e2-flow", go: vi.fn() }}>
      <SelectionContext.Provider
        value={{ taskId: "task-1", tasksVersion: 0, selectTask: vi.fn(), refreshTasks: vi.fn() }}
      >
        <E2FlowScreen />
      </SelectionContext.Provider>
    </NavContext.Provider>,
  );
}

afterEach(cleanup);

describe("E2FlowScreen", () => {
  it("통과·차단·불명 칩을 모두 보여준다", async () => {
    const { container } = show();
    await screen.findByRole("heading", { name: "전송 통제 흐름" });
    const chips = container.querySelector(".search");
    expect(chips).toHaveTextContent("통과");
    expect(chips).toHaveTextContent("차단");
    expect(chips).toHaveTextContent("불명");
  });

  // 윤재상 리뷰(#196): 송신 뒤 감사 실패는 차단 수에 넣지 않는다.
  // 목업: 송신 전 거부 3건(차단) + 송신 뒤 감사 실패 1 + 배수 불명 1(불명 2).
  it("송신 뒤 감사 실패는 차단 수에 안 들어간다(차단 3 · 불명 2)", async () => {
    const { container } = show();
    await screen.findByRole("heading", { name: "전송 통제 흐름" });
    const chips = container.querySelector(".search");
    expect(chips).toHaveTextContent("차단 3");
    expect(chips).toHaveTextContent("불명 2");
  });

  it("통과 ≠ 대상 도달 ≠ 감사 저장을 화면에 명시한다(통과를 성공으로 오인시키지 않음)", async () => {
    show();
    await screen.findByRole("heading", { name: "전송 통제 흐름" });
    expect(screen.getByText(/통과 ≠ 대상 도달 ≠ 감사 저장/)).toBeInTheDocument();
  });

  it("배수·감사 불명 시도가 있는 호출은 '미완료(중간 집계)'로 표시한다(성공 축소 금지)", async () => {
    show();
    await screen.findByRole("heading", { name: "전송 통제 흐름" });
    expect(screen.getAllByText(/미완료\(중간 집계\)/).length).toBeGreaterThan(0);
  });

  it("명령 원문이 아니라 불투명 호출 참조·시도 ID 로만 잇는다(원문 노출 0)", async () => {
    const { container } = show();
    await screen.findByRole("heading", { name: "전송 통제 흐름" });
    // 호출 참조·시도 ID 는 뜨지만, curl 명령 원문(스킴/주소)은 화면에 없다.
    expect(within(container).getByText("call-7a1")).toBeInTheDocument();
    expect(container.textContent).not.toMatch(/https?:\/\//);
  });
});
