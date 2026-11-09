import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import * as remediation from "../lib/remediation";
import * as runs from "../lib/request-approvals";
import { SelectionContext } from "../lib/selection";
import { NavContext } from "../nav";
import FirstDiagnosisReportScreen from "./FirstDiagnosisReportScreen";

vi.mock("../lib/mock", () => ({ MOCK: false }));
vi.mock("../lib/remediation", async (original) => ({
  ...(await original<typeof remediation>()),
  readRemediation: vi.fn(),
}));
vi.mock("../lib/request-approvals", async (original) => ({
  ...(await original<typeof runs>()),
  readRequestApprovalState: vi.fn(),
  readDiagnosisRun: vi.fn(),
  readDiagnosisRunDisplay: vi.fn(),
}));
vi.mock("../components/TopBar", () => ({ TopBar: () => null }));
vi.mock("../components/DiagnosticDownload", () => ({ DiagnosticDownload: () => null }));

beforeEach(() => {
  vi.mocked(remediation.readRemediation).mockResolvedValue({ task_id: 42, entries: [] });
  vi.mocked(runs.readRequestApprovalState).mockResolvedValue({
    approvals: [],
    run: {
      run_ref: "active-run",
      phase: "running",
      outcome: "",
      stop_requested: false,
      context_cancelled: false,
      drain_complete: false,
      closure_failed: false,
    },
  });
  vi.mocked(runs.readDiagnosisRun).mockResolvedValue({
    version: 1,
    task_id: 42,
    run: null,
    execution: "NOT_RUN",
    model_preparation: [{}],
  });
  vi.mocked(runs.readDiagnosisRunDisplay).mockResolvedValue({ kind: "not_run", preparedOnly: true });
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function view(connection: "run" | "report") {
  return (
    <NavContext.Provider value={{ screen: "report", go: vi.fn() }}>
      <SelectionContext.Provider
        value={{
          taskId: "42",
          connection,
          mode: "first-diagnosis",
          tasksVersion: 0,
          selectTask: vi.fn(),
          refreshTasks: vi.fn(),
        }}
      >
        <FirstDiagnosisReportScreen />
      </SelectionContext.Provider>
    </NavContext.Provider>
  );
}
it("uses the active supervisor even when saved preparation would report NOT_RUN", async () => {
  render(view("run"));
  await screen.findByText("실행 중");
  expect(screen.getByText("active-run")).toBeInTheDocument();
  expect(runs.readRequestApprovalState).toHaveBeenCalledWith("42");
  expect(runs.readDiagnosisRun).not.toHaveBeenCalled();
  expect(runs.readDiagnosisRunDisplay).not.toHaveBeenCalled();
  expect(screen.queryByText(/실행은 하지 않았어요|아직 실행하지 않았어요/)).not.toBeInTheDocument();
});
it("describes report-only NOT_RUN as missing termination evidence, not never executed", async () => {
  render(view("report"));
  await screen.findByText(/모델 준비 기록은 있지만 저장된 실행 종료 결과는 없어요/);
  expect(screen.getByText(/현재 실행 여부를 보장하지 않아요/)).toBeInTheDocument();
  expect(runs.readRequestApprovalState).not.toHaveBeenCalled();
  expect(screen.queryByText(/실행은 하지 않았어요|아직 실행하지 않았어요/)).not.toBeInTheDocument();
});
