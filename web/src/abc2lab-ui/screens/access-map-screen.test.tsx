import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { SelectionContext } from "../lib/selection";
import { NavContext } from "../nav";
import AccessMapScreen from "./AccessMapScreen";

// 전체 지도 화면이 공유 리졸버·집계를 화면에 올바로 반영하는지(§4.2(3)·(7)).
// taskId=null 이면 getAccessMap 이 예시(MOCK_MAP)를 돌려줘 추가 모킹 없이 결정적으로 렌더한다.
// 실 라우트 응답 매핑은 lib/kg 의 mapApiAccessMap 단위시험에서 따로 검증한다.

function show() {
  return render(
    <NavContext.Provider value={{ screen: "access-map", go: vi.fn() }}>
      <SelectionContext.Provider
        value={{ taskId: null, tasksVersion: 0, selectTask: vi.fn(), refreshTasks: vi.fn() }}
      >
        <AccessMapScreen />
      </SelectionContext.Provider>
    </NavContext.Provider>,
  );
}

afterEach(cleanup);

describe("AccessMapScreen", () => {
  it("확정/의심을 서로 다른 셀 클래스로 그리고, 의심 칸 본문은 '취약'이 아니라 '의심'이다", async () => {
    show();
    const table = await screen.findByRole("table");
    const vulnBody = within(table).getAllByText("취약");
    const susBody = within(table).getAllByText("의심");
    expect(vulnBody.length).toBeGreaterThan(0);
    expect(susBody.length).toBeGreaterThan(0);
    expect(vulnBody[0].closest("td")).toHaveClass("cell-vuln");
    expect(susBody[0].closest("td")).toHaveClass("cell-suspected");
    // 의심 칸의 td 는 확정 칸의 td 와 다른 클래스여야 한다(배지만 같게 바꿔도 깨지도록).
    expect(vulnBody[0].closest("td")?.className).not.toBe(susBody[0].closest("td")?.className);
  });

  it("헤드라인 '취약 확정' 칩은 수직 미리보기를 빼고 판정 축(H)만 센다", async () => {
    const { container } = show();
    await screen.findByRole("table");
    // 목업: 수평 vuln 1칸(B|r1) + 수직 vuln 1칸(비로그인|r4). 판정 축 집계면 1이어야 한다.
    const chips = container.querySelector(".search");
    expect(chips).toHaveTextContent("취약 확정 1");
    expect(chips).not.toHaveTextContent("취약 확정 2");
  });

  it("교차계정 차단 칸(deny-ok)은 '정상 차단'으로 그리지 않는다(Phase 3 보류 → 판정 불가)", async () => {
    show();
    const table = await screen.findByRole("table");
    // deny-ok 는 리졸버가 unknown 으로 내리므로 표에 '정상 차단' 본문이 없어야 한다.
    expect(within(table).queryByText("정상 차단")).toBeNull();
  });
});
