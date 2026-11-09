import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "../lib/api";
import * as management from "../lib/diagnosis-management";
import { verifyDiagnosisTask } from "../lib/first-diagnosis";
import { SelectionContext } from "../lib/selection";
import { NavContext } from "../nav";
import { NewDiagnosisPanel } from "./NewDiagnosisPanel";

vi.mock("../lib/diagnosis-management", async (original) => ({
  ...(await original<typeof import("../lib/diagnosis-management")>()),
  readDiagnosisProfiles: vi.fn(),
  createDiagnosisDraft: vi.fn(),
  readCreatedDiagnosis: vi.fn(),
  readDiagnosisDraft: vi.fn(),
  updateDiagnosisDraft: vi.fn(),
  commandDiagnosis: vi.fn(),
}));
vi.mock("../lib/first-diagnosis", async (original) => ({
  ...(await original<typeof import("../lib/first-diagnosis")>()),
  verifyDiagnosisTask: vi.fn(),
}));
const config: management.DiagnosisConfiguration = {
  target_ref: "target-1",
  scope_ref: "scope-1",
  model_profile_ref: "model-1",
  approval_mode: "confirm_every_request",
  budget: { requests: 10, tokens: 1000, cost_usd_micros: 0, duration_seconds: 60 },
};
const profiles = [
  { id: "p1", label: "설정 하나", configuration: config },
  { id: "p2", label: "설정 둘", configuration: { ...config, target_ref: "target-2" } },
];
const basic: management.DiagnosisDraft = {
  id: "draft-1",
  revision: 1,
  source_digest: "a".repeat(64),
  configuration: config,
  state: "draft",
};
const prepared: management.DiagnosisDraft = { ...basic, state: "prepared", task_id: "42" };
const running: management.DiagnosisDraft = { ...basic, state: "running", task_id: "42" };
const go = vi.fn(),
  connectTask = vi.fn();
function mount() {
  return render(
    <NavContext.Provider value={{ screen: "entry", go }}>
      <SelectionContext.Provider
        value={{ taskId: null, tasksVersion: 0, selectTask: vi.fn(), refreshTasks: vi.fn(), connectTask }}
      >
        <NewDiagnosisPanel />
      </SelectionContext.Provider>
    </NavContext.Provider>,
  );
}
beforeEach(() => {
  vi.resetAllMocks();
  sessionStorage.clear();
  vi.mocked(management.readDiagnosisProfiles).mockResolvedValue(profiles);
  vi.mocked(management.createDiagnosisDraft).mockResolvedValue(basic);
  vi.mocked(management.commandDiagnosis).mockResolvedValue(prepared);
  vi.mocked(management.readDiagnosisDraft).mockResolvedValue(prepared);
  vi.mocked(management.readCreatedDiagnosis).mockResolvedValue(basic);
  vi.mocked(verifyDiagnosisTask).mockResolvedValue();
});
afterEach(cleanup);
async function prepare() {
  mount();
  await waitFor(() => expect(screen.getByRole("button", { name: "실행 없이 초안 생성" })).toBeEnabled());
  fireEvent.click(screen.getByRole("button", { name: "실행 없이 초안 생성" }));
  await waitFor(() => expect(management.createDiagnosisDraft).toHaveBeenCalledTimes(1));
  await waitFor(() => expect(screen.getByRole("button", { name: "실행 전 검증" })).toBeEnabled());
  fireEvent.click(screen.getByRole("button", { name: "실행 전 검증" }));
  await screen.findByText("실행 준비 확인");
}
describe("새 진단 안전 제어", () => {
  it.each(["missing", "completed"])("사용자가 명시적으로 %s 연결을 해제하면 새 초안을 만들 수 있다", async (state) => {
    management.storeDiagnosisHandle({ id: "draft-1", creationKey: "create-1" });
    if (state === "missing")
      vi.mocked(management.readDiagnosisDraft).mockRejectedValueOnce(new ApiError(404, "missing"));
    else vi.mocked(management.readDiagnosisDraft).mockResolvedValueOnce({ ...running, state: "completed" });
    mount();
    const reset = await screen.findByRole("button", { name: "다른 초안 만들기 / 조회 연결 해제" });
    await waitFor(() => expect(reset).toBeEnabled());
    expect(screen.getByText(/기존 실행을 중지하거나 초안을 삭제하지/)).toBeVisible();
    expect(screen.getByLabelText("운영자 제공 설정")).toBeDisabled();
    fireEvent.click(reset);
    expect(management.readDiagnosisHandle()).toBeNull();
    expect(screen.getByLabelText("운영자 제공 설정")).toBeEnabled();
    expect(screen.getByRole("button", { name: "실행 없이 초안 생성" })).toBeEnabled();
    expect(management.commandDiagnosis).not.toHaveBeenCalled();
    expect(management.createDiagnosisDraft).not.toHaveBeenCalled();
  });
  it("검증된 unknown 결과는 완료로 바꾸지 않고 읽기 전용 보고서 연결한다", async () => {
    management.storeDiagnosisHandle({ id: "draft-1", creationKey: "create-1" });
    vi.mocked(management.readDiagnosisDraft).mockResolvedValueOnce({
      ...running,
      state: "unknown",
      drain_complete: true,
      result_ready: true,
    });
    mount();
    expect(await screen.findByText("결과 불명")).toBeVisible();
    const link = screen.getByRole("button", { name: "실제 task 상태·보고서 연결" });
    await waitFor(() => expect(link).toBeEnabled());
    fireEvent.click(link);
    await waitFor(() => expect(go).toHaveBeenCalledWith("report"));
    expect(verifyDiagnosisTask).toHaveBeenCalledWith("42", "report");
    expect(management.commandDiagnosis).not.toHaveBeenCalled();
    expect(screen.queryByText("실행 마감")).toBeNull();
  });

  it("목록 장애에도 기존 실행을 서버에서 복구하여 중지할 수 있다", async () => {
    management.storeDiagnosisHandle({ id: "draft-1", creationKey: "create-1" });
    vi.mocked(management.readDiagnosisProfiles).mockRejectedValueOnce(new ApiError(503, "unavailable"));
    vi.mocked(management.readDiagnosisDraft).mockResolvedValueOnce(running);
    mount();
    await waitFor(() => expect(screen.getByRole("button", { name: "신규 전송 중지 요청" })).toBeEnabled());
    expect(management.readDiagnosisDraft).toHaveBeenCalledWith("draft-1");
  });
  it("초안 생성은 시작 호출과 분리한다", async () => {
    mount();
    await waitFor(() => expect(screen.getByRole("button", { name: "실행 없이 초안 생성" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "실행 없이 초안 생성" }));
    await screen.findByRole("button", { name: "초안 설정 저장" });
    expect(management.commandDiagnosis).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: "승인된 진단 시작" })).toBeNull();
  });
  it("미저장 설정 변경은 검증·시작을 막고 저장된 범위를 표시한다", async () => {
    await prepare();
    fireEvent.change(screen.getByLabelText("운영자 제공 설정"), { target: { value: "p2" } });
    expect(screen.getByRole("button", { name: "실행 전 검증" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "승인된 진단 시작" })).toBeNull();
    expect(screen.getByLabelText("서버에 저장된 설정")).toHaveTextContent("target-1");
    expect(screen.getByText(/선택한 설정은 아직 저장되지/)).toBeVisible();
  });
  it("시작은 명시 확인 뒤 단회, 불명은 GET으로만 확인한다", async () => {
    await prepare();
    const start = screen.getByRole("button", { name: "승인된 진단 시작" });
    expect(start).toBeDisabled();
    fireEvent.click(screen.getByRole("checkbox"));
    vi.mocked(management.commandDiagnosis).mockRejectedValueOnce(new TypeError("network"));
    fireEvent.click(start);
    fireEvent.click(start);
    await screen.findByText(/응답을 확인하지 못했어요/);
    expect(management.commandDiagnosis).toHaveBeenCalledTimes(2);
    expect(screen.queryByRole("button", { name: "승인된 진단 시작" })).toBeNull();
    vi.mocked(management.readDiagnosisDraft).mockResolvedValue(running);
    fireEvent.click(screen.getByRole("button", { name: "초안·실행 상태 다시 조회" }));
    await screen.findByText("실행 중");
    expect(management.commandDiagnosis).toHaveBeenCalledTimes(2);
    expect(management.readDiagnosisDraft).toHaveBeenCalledWith("draft-1");
  });
  it("닫기/재열기 시 저장 상태가 아니라 핸들을 GET 검증해 복원한다", async () => {
    management.storeDiagnosisHandle({ id: "draft-1", creationKey: "create-1" });
    let resolve!: (value: management.DiagnosisDraft) => void;
    vi.mocked(management.readDiagnosisDraft).mockReturnValue(
      new Promise((done) => {
        resolve = done;
      }),
    );
    mount();
    await waitFor(() => expect(management.readDiagnosisDraft).toHaveBeenCalledWith("draft-1"));
    expect(screen.queryByText("실행 준비 확인")).toBeNull();
    await act(async () => resolve(prepared));
    expect(await screen.findByText("실행 준비 확인")).toBeVisible();
    expect(management.createDiagnosisDraft).not.toHaveBeenCalled();
  });
  it("생성 응답 불명 핸들을 재열 때 receipt 조회한다", async () => {
    management.storeDiagnosisHandle({ creationKey: "create-1" });
    mount();
    expect(await screen.findByRole("button", { name: "초안 설정 저장" })).toBeVisible();
    expect(management.readCreatedDiagnosis).toHaveBeenCalledWith("create-1");
    expect(management.createDiagnosisDraft).not.toHaveBeenCalled();
  });
  it("권한 실패 재조회 후 이전 prepared와 핸들을 제거한다", async () => {
    await prepare();
    vi.mocked(management.readDiagnosisDraft).mockRejectedValue(new ApiError(403, "forbidden"));
    fireEvent.click(screen.getByRole("button", { name: "초안·실행 상태 다시 조회" }));
    await screen.findByRole("alert");
    expect(screen.queryByText("실행 준비 확인")).toBeNull();
    expect(screen.queryByRole("button", { name: "승인된 진단 시작" })).toBeNull();
    expect(management.readDiagnosisHandle()).toBeNull();
  });
  it("관리 전용 서버에서는 task API를 확인하기 전 탐색하지 않는다", async () => {
    await prepare();
    vi.mocked(verifyDiagnosisTask).mockRejectedValue(new ApiError(404, "unsupported"));
    fireEvent.click(screen.getByRole("button", { name: "실제 task 상태·보고서 연결" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("관리 API와 실행·보고서 API 연결");
    expect(connectTask).not.toHaveBeenCalled();
    expect(go).not.toHaveBeenCalled();
  });
});
