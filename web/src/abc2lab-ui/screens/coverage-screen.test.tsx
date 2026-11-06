import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { CoverageData } from "../lib/coverage";
import { SelectionContext } from "../lib/selection";
import { NavContext } from "../nav";
import CoverageScreen, { CoveragePanel } from "./CoverageScreen";

function show() {
  return render(
    <NavContext.Provider value={{ screen: "coverage", go: vi.fn() }}>
      <SelectionContext.Provider value={{ taskId: "t1", tasksVersion: 0, selectTask: vi.fn(), refreshTasks: vi.fn() }}>
        <CoverageScreen />
      </SelectionContext.Provider>
    </NavContext.Provider>,
  );
}

afterEach(cleanup);

describe("CoverageScreen", () => {
  it("영역 라벨·미시도·'조회 안 함 ≠ 없음' 힌트를 보여준다", async () => {
    const { container } = show();
    await screen.findByRole("heading", { name: "커버리지" });
    expect(screen.getByText("수평 읽기 IDOR")).toBeInTheDocument();
    expect(screen.getByText("수직 권한 상승")).toBeInTheDocument();
    expect(container.textContent ?? "").toContain("조회 안 함 ≠ 없음");
    expect(screen.getAllByText(/미시도/).length).toBeGreaterThan(0); // 미시도 별도 표시
  });
});

describe("CoveragePanel — 무결성 fail-closed", () => {
  it("상태합이 계획과 다르면 집계 무결성 경고를 띄운다(조용히 안 맞춤)", () => {
    const bad: CoverageData = {
      policyVersion: "t",
      mock: false,
      areas: [
        {
          id: "a",
          label: "영역",
          axis: "horizontal",
          planned: 9, // 상태합 1 ≠ 9
          counts: { confirmed: 1, suspected: 0, undecidable: 0, blank: 0, in_progress: 0, untested: 0, na: 0 },
        },
      ],
    };
    const { container } = render(<CoveragePanel data={bad} />);
    expect(container.textContent ?? "").toContain("집계 무결성 경고");
  });
});
