import { beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError, apiFetch } from "./api";
import { connectionError, listPreparedTasks, validTaskID, verifyDiagnosisTask } from "./first-diagnosis";
import { readRemediation } from "./remediation";
import { readDiagnosisRun, readRequestApprovalState } from "./request-approvals";

vi.mock("./api", async (original) => ({ ...(await original<typeof import("./api")>()), apiFetch: vi.fn() }));
vi.mock("./request-approvals", () => ({ readDiagnosisRun: vi.fn(), readRequestApprovalState: vi.fn() }));
vi.mock("./remediation", () => ({ readRemediation: vi.fn() }));
beforeEach(() => vi.resetAllMocks());

describe("첫 진단 서버 계약 연결", () => {
  it.each([
    "",
    "0",
    "-1",
    "01",
    "https://target",
    "1?token=secret",
    "9223372036854775808",
  ])("합성/비정본 task %s를 거부한다", (id) => {
    expect(validTaskID(id)).toBe(false);
  });
  it("int64 범위를 정확히 검사한다", () => {
    expect(validTaskID("9223372036854775807")).toBe(true);
    expect(validTaskID("42")).toBe(true);
  });
  it("서버 목록의 검증된 ID만 표시한다", async () => {
    vi.mocked(apiFetch).mockResolvedValue({
      tasks: [{ id: "42", name: "서버 작업" }, { id: "../42" }, { id: 10 }, null],
    });
    expect(await listPreparedTasks()).toEqual([{ id: "42", name: "서버 작업", status: "" }]);
  });
  it("목록 응답을 타입 단언으로 신뢰하지 않는다", async () => {
    vi.mocked(apiFetch).mockResolvedValue({ tasks: "invalid" });
    await expect(listPreparedTasks()).rejects.toMatchObject({ status: 422 });
  });
  it("입력 ID만으로 작업 선택을 확정하지 않는다", async () => {
    vi.mocked(readRequestApprovalState).mockRejectedValue(new ApiError(403, "bound task only"));
    await expect(verifyDiagnosisTask("42", "run")).rejects.toMatchObject({ status: 403 });
    expect(readDiagnosisRun).not.toHaveBeenCalled();
    expect(apiFetch).not.toHaveBeenCalled();
  });
  it("report 서버에서 일반 목록/승인을 조회하지 않는다", async () => {
    vi.mocked(readDiagnosisRun).mockResolvedValue({ version: 1, task_id: 42, run: null, execution: "NOT_RUN" });
    await verifyDiagnosisTask("42", "report");
    expect(readRequestApprovalState).not.toHaveBeenCalled();
    expect(apiFetch).not.toHaveBeenCalled();
  });
  it("저장 종료 기록이 없어도 실제 task 보고서로 확인한다", async () => {
    vi.mocked(readDiagnosisRun).mockRejectedValue(new ApiError(404, "no outcome"));
    vi.mocked(readRemediation).mockResolvedValue({ task_id: 42, entries: [] });
    await verifyDiagnosisTask("42", "report");
    expect(readRemediation).toHaveBeenCalledWith("42", "internal");
  });
  it.each([401, 403, 503])("%i 실패 뒤 보고서로 우회하지 않는다", async (status) => {
    vi.mocked(readDiagnosisRun).mockRejectedValue(new ApiError(status, "failed"));
    await expect(verifyDiagnosisTask("42", "report")).rejects.toMatchObject({ status });
    expect(readRemediation).not.toHaveBeenCalled();
  });
  it("다른 task 저장본을 거부한다", async () => {
    vi.mocked(readDiagnosisRun).mockResolvedValue({ version: 1, task_id: 43, run: null, execution: "NOT_RUN" });
    await expect(verifyDiagnosisTask("42", "report")).rejects.toMatchObject({ status: 422 });
  });
  it("권한/없는 실행/네트워크/서비스 장애를 구분한다", () => {
    expect(connectionError(new ApiError(403, ""))).toContain("권한");
    expect(connectionError(new ApiError(404, ""))).toContain("실행이 없");
    expect(connectionError(new ApiError(503, ""))).toContain("아직");
    expect(connectionError(new TypeError("offline"))).toContain("연결이 끊");
  });
});
