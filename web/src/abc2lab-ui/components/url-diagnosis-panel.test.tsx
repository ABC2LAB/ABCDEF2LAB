import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "../lib/api";
import * as url from "../lib/diagnosis-url";
import {
  catalog,
  draft,
  externalModel,
  externalReady,
  handle,
  owner,
  ready,
  running,
  scope,
} from "../lib/diagnosis-url-fixtures";
import { verifyDiagnosisTask } from "../lib/first-diagnosis";
import { SelectionContext } from "../lib/selection";
import { NavContext } from "../nav";
import { URLDiagnosisPanel } from "./URLDiagnosisPanel";

vi.mock("../lib/diagnosis-url", async (original) => ({
  ...(await original<typeof import("../lib/diagnosis-url")>()),
  createURLDraft: vi.fn(),
  commandURLDiagnosis: vi.fn(),
  readURLReceipt: vi.fn(),
  readURLStatus: vi.fn(),
}));
vi.mock("../lib/first-diagnosis", async (original) => ({
  ...(await original<typeof import("../lib/first-diagnosis")>()),
  verifyDiagnosisTask: vi.fn(),
}));
const go = vi.fn(),
  connectTask = vi.fn();
function page(value = catalog) {
  return (
    <NavContext.Provider value={{ screen: "entry", go }}>
      <SelectionContext.Provider
        value={{ taskId: null, tasksVersion: 0, selectTask: vi.fn(), refreshTasks: vi.fn(), connectTask }}
      >
        <URLDiagnosisPanel catalog={value} />
      </SelectionContext.Provider>
    </NavContext.Provider>
  );
}
function mount() {
  return render(page());
}
beforeEach(() => {
  vi.resetAllMocks();
  sessionStorage.clear();
  vi.mocked(url.createURLDraft).mockResolvedValue(draft);
  vi.mocked(url.commandURLDiagnosis).mockResolvedValue(ready);
  vi.mocked(url.readURLStatus).mockResolvedValue(ready);
  vi.mocked(url.readURLReceipt).mockResolvedValue(draft);
  vi.mocked(verifyDiagnosisTask).mockResolvedValue();
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

async function prepare() {
  mount();
  fireEvent.change(screen.getByLabelText("진단 대상 URL"), { target: { value: draft.input.target_url } });
  const validate = screen.getByRole("button", { name: "승인 범위 확인" });
  fireEvent.click(validate);
  await screen.findByText("실행 전 검증 완료");
  await waitFor(() => expect(screen.getByRole("checkbox")).toBeEnabled());
}
function saved(value = ready) {
  url.storeURLHandle(handle);
  vi.mocked(url.readURLStatus).mockResolvedValue(value);
}

describe("URL 입력에서 명시 시작까지", () => {
  it("URL/모델/이름만 입력하고 scope 확인까지 실제 모델 준비를 시작하지 않는다", async () => {
    await prepare();
    expect(url.createURLDraft).toHaveBeenCalledWith(draft.input, expect.any(String), owner);
    expect(url.commandURLDiagnosis).toHaveBeenCalledTimes(1);
    expect(url.commandURLDiagnosis).toHaveBeenCalledWith(draft, "validate", owner);
    expect(screen.getByLabelText("서버가 확인한 승인 범위")).toHaveTextContent("https://target.example");
    expect(screen.getByRole("button", { name: "진단 시작" })).toBeDisabled();
    expect(screen.queryByLabelText("API 키")).toBeNull();
    expect(screen.queryByRole("progressbar")).toBeNull();
  });
  it("시작 확인과 버튼이 있어야 준비가 시작되며 반복 클릭을 단회로 제한한다", async () => {
    await prepare();
    let finish!: (job: url.URLDiagnosisJob) => void;
    vi.mocked(url.commandURLDiagnosis).mockReturnValueOnce(
      new Promise((resolve) => {
        finish = resolve;
      }),
    );
    fireEvent.click(screen.getByRole("checkbox"));
    const start = screen.getByRole("button", { name: "진단 시작" });
    fireEvent.click(start);
    fireEvent.click(start);
    expect(url.commandURLDiagnosis).toHaveBeenCalledTimes(2);
    const startHandle = url.readURLHandle(owner);
    expect(startHandle?.startKey).toBeTruthy();
    expect(startHandle?.startRevision).toBe(2);
    expect(screen.queryByText("진단 준비 중")).toBeNull();
    await act(async () =>
      finish({ ...ready, revision: 3, state: "preparing", phase: "planning", preparation_started: true }),
    );
    expect(screen.getByText("진단 준비 중")).toBeVisible();
    expect(screen.getByText("엔진 시작: 미확인")).toBeVisible();
    expect(url.commandURLDiagnosis).toHaveBeenLastCalledWith(ready, "start", owner, startHandle?.startKey, {
      externalTransferConfirmed: true,
    });
  });
  it("팀 화면도 provider·외부 전송·USD 안내를 확인한 동의만 Start에 전달한다", async () => {
    saved(externalReady);
    vi.mocked(url.commandURLDiagnosis).mockResolvedValue({
      ...externalReady,
      revision: 3,
      state: "preparing",
      phase: "planning",
      preparation_started: true,
    });
    render(page({ ...catalog, profiles: [{ ...catalog.profiles[0], model: externalModel }] }));
    const consent = await screen.findByRole("checkbox", { name: /대상 응답·도구 출력의 외부 전송/ });
    const approved = screen.getByLabelText("서버가 확인한 승인 범위");
    expect(approved).toHaveTextContent(externalModel.model);
    expect(approved).toHaveTextContent(externalModel.provider);
    expect(approved).toHaveTextContent("USD 6.000000");
    expect(approved).toHaveTextContent("입력 262144 / 출력 2048");
    expect(consent).not.toBeChecked();
    const start = screen.getByRole("button", { name: "진단 시작" });
    expect(start).toBeDisabled();
    fireEvent.click(start);
    expect(url.commandURLDiagnosis).not.toHaveBeenCalled();
    fireEvent.click(consent);
    fireEvent.click(start);
    await waitFor(() =>
      expect(url.commandURLDiagnosis).toHaveBeenCalledWith(externalReady, "start", owner, expect.any(String), {
        externalTransferConfirmed: true,
      }),
    );
    expect(sessionStorage.getItem("abc2lab.urlDiagnosisHandle.v2")).not.toContain("externalTransferConfirmed");
  });
  it("미승인 대상은 이유를 표시하고 시작 버튼을 만들지 않는다", async () => {
    vi.mocked(url.commandURLDiagnosis).mockResolvedValue({ ...draft, revision: 2, reason_code: "target_not_approved" });
    mount();
    fireEvent.change(screen.getByLabelText("진단 대상 URL"), { target: { value: draft.input.target_url } });
    fireEvent.click(screen.getByRole("button", { name: "승인 범위 확인" }));
    expect(await screen.findByText(/이 대상은 설치된 승인 범위에 없어요/)).toBeVisible();
    expect(screen.queryByRole("button", { name: "진단 시작" })).toBeNull();
  });
  it("입력에 credentials/query가 있으면 서버에 보내지 않는다", () => {
    mount();
    fireEvent.change(screen.getByLabelText("진단 대상 URL"), {
      target: { value: "https://user:secret@target.example/" },
    });
    expect(screen.getByRole("button", { name: "승인 범위 확인" })).toBeDisabled();
    expect(url.createURLDraft).not.toHaveBeenCalled();
  });
});

describe("영속 receipt와 로그인·새로고침 복구", () => {
  it("create 응답 유실 후 닫기/재열기는 같은 key GET receipt만 사용한다", async () => {
    vi.mocked(url.createURLDraft).mockRejectedValue(new TypeError("lost"));
    const view = mount();
    fireEvent.change(screen.getByLabelText("진단 대상 URL"), { target: { value: draft.input.target_url } });
    fireEvent.click(screen.getByRole("button", { name: "승인 범위 확인" }));
    await screen.findByRole("alert");
    const stored = url.readURLHandle(owner);
    expect(stored?.input).toEqual(draft.input);
    expect(stored?.id).toBeUndefined();
    view.unmount();
    mount();
    await waitFor(() => expect(url.readURLReceipt).toHaveBeenCalledWith(stored?.creationKey, owner));
    expect(url.createURLDraft).toHaveBeenCalledTimes(1);
    expect(url.commandURLDiagnosis).not.toHaveBeenCalled();
  });
  it("start 응답 유실 뒤 GET status가 ready여도 다시 시작하지 않는다", async () => {
    await prepare();
    vi.mocked(url.commandURLDiagnosis).mockRejectedValueOnce(new ApiError(503, "uncertain"));
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "진단 시작" }));
    await screen.findByRole("alert");
    const stored = url.readURLHandle(owner);
    cleanup();
    mount();
    await screen.findByText("실행 전 검증 완료");
    expect(screen.getByRole("button", { name: "진단 시작" })).toBeDisabled();
    expect(url.readURLHandle(owner)?.startKey).toBe(stored?.startKey);
    expect(url.commandURLDiagnosis).toHaveBeenCalledTimes(2);
    expect(url.readURLStatus).toHaveBeenCalledWith("job-1", owner);
  });
  it("401 중단 뒤 같은 owner는 조회만 복구하며 다른 owner는 조회하지 않는다", async () => {
    saved();
    const view = mount();
    await screen.findByText("실행 전 검증 완료");
    vi.mocked(url.commandURLDiagnosis).mockRejectedValueOnce(new ApiError(401, "expired"));
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "진단 시작" }));
    await screen.findByRole("alert");
    expect(url.readURLHandle(owner)?.startKey).toBeTruthy();
    view.unmount();
    const second = mount();
    await screen.findByText("실행 전 검증 완료");
    expect(url.commandURLDiagnosis).toHaveBeenCalledTimes(1);
    const reads = vi.mocked(url.readURLStatus).mock.calls.length;
    second.rerender(page({ ...catalog, ownerContext: "b".repeat(64) }));
    expect(url.readURLStatus).toHaveBeenCalledTimes(reads);
    expect(screen.queryByText("실행 전 검증 완료")).toBeNull();
    expect(url.readURLHandle("b".repeat(64))).toBeNull();
  });
  it("손상된 handle은 조회·시작하지 않는다", async () => {
    sessionStorage.setItem("abc2lab.urlDiagnosisHandle.v2", JSON.stringify({ ...handle, token: "secret" }));
    mount();
    await waitFor(() => expect(screen.getByLabelText("진단 대상 URL")).toBeEnabled());
    expect(url.readURLStatus).not.toHaveBeenCalled();
    expect(url.commandURLDiagnosis).not.toHaveBeenCalled();
  });
  it("403 재조회 뒤 이전 승인 상태와 핸들을 제거한다", async () => {
    saved();
    mount();
    await screen.findByText("실행 전 검증 완료");
    vi.mocked(url.readURLStatus).mockRejectedValue(new ApiError(403, "forbidden"));
    fireEvent.click(screen.getByRole("button", { name: "진단 상태 다시 조회" }));
    await screen.findByRole("alert");
    expect(screen.queryByRole("button", { name: "진단 시작" })).toBeNull();
    expect(url.readURLHandle(owner)).toBeNull();
  });
});

describe("실제 상태·중지·결과 연결", () => {
  it("준비 결과 불명은 started=false를 보존하며 모델 호출을 반복하지 않는다", async () => {
    saved({ ...ready, revision: 3, state: "unknown", phase: "planning", preparation_started: true });
    mount();
    expect(await screen.findByText("결과 불명")).toBeVisible();
    expect(screen.getByText("엔진 시작: 미확인")).toBeVisible();
    expect(screen.queryByRole("button", { name: "진단 시작" })).toBeNull();
    expect(url.commandURLDiagnosis).not.toHaveBeenCalled();
  });
  it("result_ready만 확인된 unknown 보고서는 읽고 마감 미확인은 유지한다", async () => {
    saved({ ...running, state: "unknown", phase: "report", drain_complete: false, result_ready: true });
    mount();
    expect(await screen.findByText("결과 불명")).toBeVisible();
    expect(screen.getByText("마감 확인: 미확인")).toBeVisible();
    const report = screen.getByRole("button", { name: "저장 보고서 보기" });
    await waitFor(() => expect(report).toBeEnabled());
    fireEvent.click(report);
    await waitFor(() => expect(go).toHaveBeenCalledWith("report"));
    expect(verifyDiagnosisTask).toHaveBeenCalledWith("42", "report");
    expect(connectTask).toHaveBeenCalledWith("42", "report");
  });
  it("result_ready=false는 completed 텍스트나 task 존재만으로 보고서를 열지 않는다", async () => {
    saved(running);
    mount();
    await screen.findByText("엔진 실행 중");
    expect(screen.getByRole("button", { name: "저장 보고서 보기" })).toBeDisabled();
    expect(verifyDiagnosisTask).not.toHaveBeenCalled();
  });
  it("준비 중 중지는 started=false인 실제 취소·마감 응답을 보존한다", async () => {
    const preparing: url.URLDiagnosisJob = {
      ...ready,
      revision: 3,
      state: "preparing",
      phase: "planning",
      preparation_started: true,
    };
    saved(preparing);
    vi.mocked(url.commandURLDiagnosis).mockResolvedValue({ ...preparing, revision: 4, state: "stopping" });
    mount();
    const stop = await screen.findByRole("button", { name: "진단 중지 요청" });
    await waitFor(() => expect(stop).toBeEnabled());
    fireEvent.click(stop);
    await screen.findByText("중지·마감 중");
    expect(url.commandURLDiagnosis).toHaveBeenCalledWith(preparing, "stop", owner, undefined);
    expect(screen.getByText("엔진 시작: 미확인")).toBeVisible();
  });
  it("실행·승인 화면도 실제 task API 검증 실패 시 탐색하지 않는다", async () => {
    saved(running);
    vi.mocked(verifyDiagnosisTask).mockRejectedValue(new ApiError(404, "unavailable"));
    mount();
    const live = await screen.findByRole("button", { name: "실제 실행·요청 승인 보기" });
    await waitFor(() => expect(live).toBeEnabled());
    fireEvent.click(live);
    await screen.findByRole("alert");
    expect(go).not.toHaveBeenCalled();
  });
});

describe("실제 상태 주기 조회", () => {
  it("활성 진단은 GET만 조회하고 unknown 뒤에는 주기 조회·재시작을 멈춘다", async () => {
    saved(running);
    mount();
    await screen.findByText("엔진 실행 중");
    await waitFor(() => expect(screen.getByRole("button", { name: "진단 상태 다시 조회" })).toBeEnabled());
    vi.useFakeTimers();
    fireEvent.click(screen.getByRole("button", { name: "진단 상태 다시 조회" }));
    await act(async () => {
      await Promise.resolve();
    });
    vi.mocked(url.readURLStatus).mockResolvedValue({ ...running, revision: 6, state: "unknown" });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2100);
    });
    expect(screen.getByText("결과 불명")).toBeVisible();
    const calls = vi.mocked(url.readURLStatus).mock.calls.length;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(6000);
    });
    expect(url.readURLStatus).toHaveBeenCalledTimes(calls);
    expect(url.commandURLDiagnosis).not.toHaveBeenCalled();
  });
});

describe("늦은 응답과 탐색 경합", () => {
  it("닫힌 패널의 늦은 create 응답은 화면·저장 핸들을 바꾸거나 validate하지 않는다", async () => {
    let finish!: (job: url.URLDiagnosisJob) => void;
    vi.mocked(url.createURLDraft).mockReturnValue(
      new Promise((resolve) => {
        finish = resolve;
      }),
    );
    const view = mount();
    fireEvent.change(screen.getByLabelText("진단 대상 URL"), { target: { value: draft.input.target_url } });
    fireEvent.click(screen.getByRole("button", { name: "승인 범위 확인" }));
    view.unmount();
    await act(async () => finish(draft));
    expect(url.readURLHandle(owner)?.id).toBeUndefined();
    expect(url.commandURLDiagnosis).not.toHaveBeenCalled();
    expect(go).not.toHaveBeenCalled();
  });
  it("Back/Forward로 패널 재열기는 조회만 하고 닫힌 패널의 report 응답은 탐색하지 않는다", async () => {
    saved({ ...running, state: "completed", drain_complete: true, result_ready: true });
    let finish!: () => void;
    vi.mocked(verifyDiagnosisTask).mockReturnValue(
      new Promise((resolve) => {
        finish = resolve;
      }),
    );
    const view = mount();
    const report = await screen.findByRole("button", { name: "저장 보고서 보기" });
    await waitFor(() => expect(report).toBeEnabled());
    fireEvent.click(report);
    view.unmount();
    mount();
    await act(async () => finish());
    expect(go).not.toHaveBeenCalled();
    expect(url.readURLStatus).toHaveBeenCalledTimes(2);
    expect(url.commandURLDiagnosis).not.toHaveBeenCalled();
  });
  it("새 owner 화면에 이전 지연 응답을 적용하지 않는다", async () => {
    saved();
    let finish!: (job: url.URLDiagnosisJob) => void;
    vi.mocked(url.readURLStatus).mockReturnValueOnce(
      new Promise((resolve) => {
        finish = resolve;
      }),
    );
    const view = mount();
    view.rerender(page({ ...catalog, ownerContext: "b".repeat(64) }));
    await act(async () => finish(ready));
    expect(screen.queryByText("실행 전 검증 완료")).toBeNull();
    expect(url.readURLHandle("b".repeat(64))).toBeNull();
  });
});

describe("설치 승인 변경의 안전한 재확인", () => {
  it("준비 관찰 latch는 재열기 뒤 ready 응답만으로 시작·재검증을 허용하지 않는다", async () => {
    url.storeURLHandle({ ...handle, bindingLocked: true });
    vi.mocked(url.readURLStatus).mockResolvedValue(ready);
    mount();
    await screen.findByText("실행 전 검증 완료");
    expect(screen.getByRole("button", { name: "진단 시작" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "승인 범위 확인" })).toBeDisabled();
    expect(url.commandURLDiagnosis).not.toHaveBeenCalled();
  });
  const rebound: url.URLDiagnosisJob = {
    ...ready,
    revision: 3,
    source_digest: "d".repeat(64),
    scope: { ...scope, budget: { ...scope.budget, requests: 2 } },
  };
  it("새 범위 검증은 화면·handle을 갱신하고 이전 체크 확인을 폐기한다", async () => {
    saved();
    mount();
    await screen.findByText("실행 전 검증 완료");
    fireEvent.click(screen.getByRole("checkbox"));
    expect(screen.getByRole("button", { name: "진단 시작" })).toBeEnabled();
    vi.mocked(url.commandURLDiagnosis).mockResolvedValue(rebound);
    fireEvent.click(screen.getByRole("button", { name: "승인 범위 확인" }));
    await waitFor(() => expect(url.readURLHandle(owner)?.sourceDigest).toBe(rebound.source_digest));
    expect(screen.getByLabelText("서버가 확인한 승인 범위")).toHaveTextContent("2회");
    expect(screen.getByRole("checkbox")).not.toBeChecked();
    expect(screen.getByRole("button", { name: "진단 시작" })).toBeDisabled();
  });
  it("설치 승인 뒤 validate 응답 유실은 재열기의 GET으로 새 결속을 복구한다", async () => {
    saved();
    const view = mount();
    await screen.findByText("실행 전 검증 완료");
    vi.mocked(url.commandURLDiagnosis).mockRejectedValue(new TypeError("lost validate"));
    fireEvent.click(screen.getByRole("button", { name: "승인 범위 확인" }));
    await screen.findByRole("alert");
    view.unmount();
    vi.mocked(url.readURLStatus).mockResolvedValue(rebound);
    mount();
    await screen.findByText("실행 전 검증 완료");
    expect(url.readURLHandle(owner)?.sourceDigest).toBe(rebound.source_digest);
    expect(screen.getByRole("checkbox")).not.toBeChecked();
    expect(url.commandURLDiagnosis).toHaveBeenCalledTimes(1);
  });
  it("awaiting 초안은 설치 승인 뒤 같은 id의 ready 범위를 표시한다", async () => {
    saved({ ...draft, revision: 2 });
    mount();
    await screen.findByText("승인 대기");
    vi.mocked(url.commandURLDiagnosis).mockResolvedValue(rebound);
    fireEvent.click(screen.getByRole("button", { name: "승인 범위 확인" }));
    await screen.findByText("실행 전 검증 완료");
    expect(url.readURLHandle(owner)?.id).toBe("job-1");
    expect(url.createURLDraft).not.toHaveBeenCalled();
  });
  it("준비를 관찰한 핸들은 start key가 없어도 새 승인 결속으로 돌아가지 않는다", async () => {
    saved(running);
    mount();
    await screen.findByText("엔진 실행 중");
    expect(url.readURLHandle(owner)?.bindingLocked).toBe(true);
    vi.mocked(url.readURLStatus).mockResolvedValue({ ...rebound, revision: 6 });
    fireEvent.click(screen.getByRole("button", { name: "진단 상태 다시 조회" }));
    await screen.findByRole("alert");
    expect(screen.queryByRole("button", { name: "진단 시작" })).toBeNull();
    expect(url.commandURLDiagnosis).not.toHaveBeenCalled();
  });
});
