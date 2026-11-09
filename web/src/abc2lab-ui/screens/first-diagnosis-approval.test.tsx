import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { SelectionContext } from "../lib/selection";
import { NavContext } from "../nav";
import ApprovalScreen from "./ApprovalScreen";

const panel = vi.fn();
vi.mock("../components/RequestApprovalPanel", () => ({
  RequestApprovalPanel: (props: unknown) => {
    panel(props);
    return <p>승인 패널</p>;
  },
}));
vi.mock("../lib/engine", () => ({
  listTasks: () => {
    throw new Error("일반 ARTEX 목록 호출 금지");
  },
  listPendingApprovals: () => {
    throw new Error("도구 승인 목록 호출 금지");
  },
}));
afterEach(() => {
  cleanup();
  panel.mockClear();
});
function show(taskId: string | null, connection: "run" | "report") {
  return render(
    <NavContext.Provider value={{ screen: "approval", go: vi.fn() }}>
      <SelectionContext.Provider
        value={{
          taskId,
          connection,
          mode: "first-diagnosis",
          selectTask: vi.fn(),
          tasksVersion: 0,
          refreshTasks: vi.fn(),
        }}
      >
        <ApprovalScreen />
      </SelectionContext.Provider>
    </NavContext.Provider>,
  );
}
describe("first diagnosis approval routing", () => {
  it("mounts only E2 approval for a selected running task", () => {
    show("42", "run");
    expect(screen.getByText("승인 패널")).toBeVisible();
    expect(panel).toHaveBeenCalledWith({ taskId: "42", active: true, refreshKey: 0 });
    expect(screen.queryByText("도구 호출 승인 대기")).toBeNull();
  });
  it("never mounts control on the stored report server", () => {
    show("42", "report");
    expect(screen.getByText(/저장 보고서 서버에서는 승인/)).toBeVisible();
    expect(panel).not.toHaveBeenCalled();
  });
  it("does not fabricate task or control when none is connected", () => {
    show(null, "run");
    expect(screen.getByText(/연결된 task가 없어요/)).toBeVisible();
    expect(panel).not.toHaveBeenCalled();
  });
});
