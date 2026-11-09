import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { ApiError } from "../lib/api";
import * as remediation from "../lib/remediation";
import { SelectionContext } from "../lib/selection";
import { NavContext } from "../nav";
import FirstDiagnosisReportScreen from "./FirstDiagnosisReportScreen";

vi.mock("../lib/remediation", async (original) => ({
  ...(await original<typeof remediation>()),
  readRemediation: vi.fn(),
}));
vi.mock("../components/TopBar", () => ({ TopBar: () => null }));
vi.mock("../components/DiagnosisRunSummary", () => ({
  DiagnosisRunSummary: ({ taskId }: { taskId: string }) => <div>저장 실행 {taskId}</div>,
}));
vi.mock("../components/DiagnosticDownload", () => ({
  DiagnosticDownload: ({ enabled }: { enabled: boolean }) => (
    <button type="button" disabled={!enabled}>
      종합 HTML 다운로드
    </button>
  ),
}));
vi.mock("../components/RemediationPanel", () => ({
  RemediationPanel: ({ taskId }: { taskId: string }) => <div>저장 조치 {taskId}</div>,
}));

const candidate: remediation.RemediationEntry = {
  entry_id: "candidate-1",
  title: "실제 저장된 후보",
  outcome: "review",
  status: "completed",
  verification: "manual_review_required",
  evidence_digest: "b".repeat(64),
  source_digest: "a".repeat(64),
  evidence_version: 1,
  report_version: "v1",
  run: "run-1",
  intent_id: 1,
  observed_at: "2026-11-09T00:00:00Z",
  summary: "후보 근거",
  evidence: "",
  report: "",
  remediation: "",
  available: false,
  unavailable_reason: "",
};
function view(taskId: string | null = "42", active = true) {
  return (
    <NavContext.Provider value={{ screen: active ? "report" : "entry", go: vi.fn() }}>
      <SelectionContext.Provider
        value={{ taskId, connection: "report", tasksVersion: 0, refreshTasks: vi.fn(), selectTask: vi.fn() }}
      >
        <FirstDiagnosisReportScreen />
      </SelectionContext.Provider>
    </NavContext.Provider>
  );
}
beforeEach(() => {
  vi.mocked(remediation.readRemediation).mockImplementation(async (task) => ({
    task_id: Number(task),
    entries: [candidate],
  }));
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

it("shows a candidate with zero confirmed findings using only the report endpoint", async () => {
  render(view());
  await screen.findByText("실제 저장된 후보");
  const summary = screen.getByRole("region", { name: "진단 판정 집계" });
  expect(summary).toHaveTextContent("저장 결과 1건");
  expect(summary).toHaveTextContent("확정0건");
  expect(summary).toHaveTextContent("검토 필요1건");
  expect(remediation.readRemediation).toHaveBeenCalledWith("42", "internal", expect.any(AbortSignal));
  expect(screen.getByText("종합 HTML 다운로드")).toBeEnabled();
  fireEvent.click(screen.getByText("저장 조치·AI 수정 프롬프트"));
  expect(screen.getByText("저장 조치 42")).toBeInTheDocument();
});

it("does not query without a selected task or while hidden", () => {
  const ui = render(view(null));
  expect(screen.getByText(/선택된 작업이 없어요/)).toBeInTheDocument();
  ui.rerender(view("42", false));
  expect(remediation.readRemediation).not.toHaveBeenCalled();
});

it("distinguishes a genuine empty report", async () => {
  vi.mocked(remediation.readRemediation).mockResolvedValue({ task_id: 42, entries: [] });
  render(view());
  await screen.findByText(/저장된 후보·판정이 0건/);
  expect(screen.getByText("종합 HTML 다운로드")).toBeEnabled();
});

it.each([
  [403, "조회할 권한이 없어요"],
  [404, "저장 보고서를 찾지 못했어요"],
  [503, "보고서 저장소를 사용할 수 없어요"],
])("does not turn HTTP %s into zero results", async (status, message) => {
  vi.mocked(remediation.readRemediation).mockRejectedValue(new ApiError(Number(status), "test"));
  render(view());
  expect(await screen.findByRole("alert")).toHaveTextContent(String(message));
  expect(screen.queryByText(/저장 결과 0건/)).not.toBeInTheDocument();
  expect(screen.getByText("종합 HTML 다운로드")).toBeDisabled();
});

it("discards old task responses and aborts them on selection change", async () => {
  let finish: ((data: remediation.RemediationData) => void) | undefined;
  vi.mocked(remediation.readRemediation).mockImplementation((task) =>
    task === "42"
      ? new Promise((resolve) => {
          finish = resolve;
        })
      : Promise.resolve({ task_id: 43, entries: [{ ...candidate, title: "새 작업 후보" }] }),
  );
  const ui = render(view("42"));
  await waitFor(() => expect(finish).toBeDefined());
  ui.rerender(view("43"));
  await screen.findByText("새 작업 후보");
  finish?.({ task_id: 42, entries: [candidate] });
  await waitFor(() => expect(screen.queryByText("실제 저장된 후보")).not.toBeInTheDocument());
  expect(vi.mocked(remediation.readRemediation).mock.calls[0][2]?.aborted).toBe(true);
});

it("removes old results after a failed refresh instead of restoring cached data", async () => {
  render(view());
  await screen.findByText("실제 저장된 후보");
  vi.mocked(remediation.readRemediation).mockRejectedValue(new TypeError("network"));
  fireEvent.click(screen.getByText("보고서 새로고침"));
  expect(await screen.findByRole("alert")).toHaveTextContent("연결이 끊겼어요");
  expect(screen.queryByText("실제 저장된 후보")).not.toBeInTheDocument();
  expect(screen.getByText("종합 HTML 다운로드")).toBeDisabled();
});
