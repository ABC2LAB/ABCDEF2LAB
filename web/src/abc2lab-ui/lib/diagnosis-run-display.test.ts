import { beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError, apiFetch } from "./api";
import { classifyDiagnosisRun, type DiagnosisRunRecord, readDiagnosisRunDisplay } from "./request-approvals";

// apiFetch 만 스텁하고 실제 ApiError(instanceof 판정용)는 그대로 둔다.
vi.mock("./api", async (importActual) => ({
  ...(await importActual<typeof import("./api")>()),
  apiFetch: vi.fn(),
}));
beforeEach(() => vi.resetAllMocks());

const fullRun: DiagnosisRunRecord = {
  version: 1,
  task_id: 42,
  saved_at: "2026-11-08T00:00:00Z",
  run: { phase: "closed", outcome: "completed", stop_requested: false, drain_complete: true } as never,
};

describe("classifyDiagnosisRun", () => {
  it("저장된 종료 레코드는 run 으로 그대로 통과시킨다", () => {
    expect(classifyDiagnosisRun(fullRun)).toEqual({ kind: "run", savedAt: fullRun.saved_at, run: fullRun.run });
  });

  it("run=null·NOT_RUN 은 미실행으로, 완료로 합성하지 않는다", () => {
    expect(classifyDiagnosisRun({ version: 1, task_id: 42, run: null })).toEqual({
      kind: "not_run",
      preparedOnly: false,
    });
    expect(
      classifyDiagnosisRun({ version: 1, task_id: 42, run: null, execution: "NOT_RUN", model_preparation: [{}] }),
    ).toEqual({ kind: "not_run", preparedOnly: true });
  });

  it("레코드 없음은 none(완료 아님)", () => {
    expect(classifyDiagnosisRun(null)).toEqual({ kind: "none" });
  });
});

describe("readDiagnosisRunDisplay", () => {
  it("저장된 종료 결과를 올바른 경로로 읽어 run 으로 분류한다", async () => {
    vi.mocked(apiFetch).mockResolvedValue(fullRun);
    expect(await readDiagnosisRunDisplay("42")).toEqual({ kind: "run", savedAt: fullRun.saved_at, run: fullRun.run });
    expect(apiFetch).toHaveBeenCalledExactlyOnceWith("/api/tasks/42/diagnosis-run", { cache: "no-store" });
  });

  it("404(저장 없음)를 에러로 터뜨리지 않고 none 으로 흡수한다 — 완료로 바꾸지 않는다", async () => {
    vi.mocked(apiFetch).mockRejectedValue(new ApiError(404, "저장된 실행 종료 결과가 없습니다"));
    expect(await readDiagnosisRunDisplay("42")).toEqual({ kind: "none" });
  });

  it("403(다른 작업)은 unavailable 로 흡수한다 — 완료로 보지 않는다", async () => {
    vi.mocked(apiFetch).mockRejectedValue(new ApiError(403, "선택한 작업의 실행 결과만 사용할 수 있습니다"));
    expect(await readDiagnosisRunDisplay("42")).toEqual({
      kind: "unavailable",
      reason: "선택한 작업의 실행 결과만 사용할 수 있습니다",
    });
  });

  it("그 밖의 실패(503 등)는 에러로 올린다", async () => {
    vi.mocked(apiFetch).mockRejectedValue(new ApiError(503, "저장된 실행 종료 결과를 확인할 수 없습니다"));
    await expect(readDiagnosisRunDisplay("42")).rejects.toMatchObject({ status: 503 });
  });
});
