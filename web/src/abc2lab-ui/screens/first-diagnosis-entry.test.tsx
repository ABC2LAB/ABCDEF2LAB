import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "../lib/api";
import { listPreparedTasks, verifyDiagnosisTask } from "../lib/first-diagnosis";
import { SelectionContext } from "../lib/selection";
import { NavContext } from "../nav";
import FirstDiagnosisEntry from "./FirstDiagnosisEntry";

vi.mock("../lib/first-diagnosis", async (original) => ({
  ...(await original<typeof import("../lib/first-diagnosis")>()),
  listPreparedTasks: vi.fn(),
  verifyDiagnosisTask: vi.fn(),
}));
const go = vi.fn(),
  connectTask = vi.fn(),
  selectTask = vi.fn();
function page(source: "run" | "report" = "run") {
  return (
    <NavContext.Provider value={{ screen: "entry", go }}>
      <SelectionContext.Provider
        value={{
          mode: "first-diagnosis",
          connection: source,
          taskId: null,
          selectTask,
          connectTask,
          tasksVersion: 0,
          refreshTasks: vi.fn(),
        }}
      >
        <FirstDiagnosisEntry />
      </SelectionContext.Provider>
    </NavContext.Provider>
  );
}
beforeEach(() => {
  vi.resetAllMocks();
  window.history.replaceState(null, "", "/");
  vi.mocked(listPreparedTasks).mockResolvedValue([]);
  vi.mocked(verifyDiagnosisTask).mockResolvedValue();
});
afterEach(cleanup);

describe("실제 task 연결 화면", () => {
  it("서버 작업이 없으면 실행을 합성하지 않는다", async () => {
    render(page());
    expect(await screen.findByText(/서버가 제공하는 task가 없/)).toBeVisible();
    expect(connectTask).not.toHaveBeenCalled();
    expect(screen.queryByLabelText("사이트 주소")).toBeNull();
    expect(screen.queryByLabelText("API 키")).toBeNull();
  });
  it("실제 task를 검증한 뒤 실행 화면으로 연결한다", async () => {
    render(page());
    fireEvent.change(screen.getByLabelText("첫 진단 task ID"), { target: { value: "42" } });
    fireEvent.click(screen.getByRole("button", { name: "실행 연결" }));
    await waitFor(() => expect(connectTask).toHaveBeenCalledWith("42", "run"));
    expect(selectTask).toHaveBeenCalledWith(null);
    expect(go).toHaveBeenCalledWith("live");
  });
  it("URL의 비민감 task는 입력 후보이며 승인된 선택이 아니다", async () => {
    window.history.replaceState(null, "", "/?task=42&server=report#report");
    render(page());
    expect(await screen.findByRole("button", { name: "저장 결과 연결" })).toBeVisible();
    expect(screen.getByLabelText("첫 진단 task ID")).toHaveValue("42");
    expect(connectTask).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "저장 결과 연결" }));
    await waitFor(() => expect(connectTask).toHaveBeenCalledWith("42", "report"));
  });
  it("권한 실패 후 이전 선택을 복원하지 않는다", async () => {
    vi.mocked(verifyDiagnosisTask).mockRejectedValue(new ApiError(403, "denied"));
    render(page());
    fireEvent.change(screen.getByLabelText("첫 진단 task ID"), { target: { value: "42" } });
    fireEvent.click(screen.getByRole("button", { name: "실행 연결" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("권한");
    expect(selectTask).toHaveBeenCalledWith(null);
    expect(connectTask).not.toHaveBeenCalled();
  });
  it("변경된 ID에 이전 지연 응답을 적용하지 않는다", async () => {
    let finish!: () => void;
    vi.mocked(verifyDiagnosisTask).mockReturnValue(
      new Promise<void>((resolve) => {
        finish = resolve;
      }),
    );
    render(page());
    fireEvent.change(screen.getByLabelText("첫 진단 task ID"), { target: { value: "42" } });
    fireEvent.click(screen.getByRole("button", { name: "실행 연결" }));
    fireEvent.change(screen.getByLabelText("첫 진단 task ID"), { target: { value: "43" } });
    finish();
    await waitFor(() => expect(screen.getByRole("button", { name: "실행 연결" })).toBeEnabled());
    expect(connectTask).not.toHaveBeenCalled();
  });
});
