import { beforeEach, describe, expect, it, vi } from "vitest";

import { apiFetch } from "./api";
import {
  commandDiagnosis,
  createDiagnosisDraft,
  type DiagnosisConfiguration,
  type DiagnosisDraft,
  readCreatedDiagnosis,
  readDiagnosisDraft,
  readDiagnosisHandle,
  readDiagnosisProfiles,
  storeDiagnosisHandle,
  updateDiagnosisDraft,
} from "./diagnosis-management";

vi.mock("./api", async (original) => ({ ...(await original<typeof import("./api")>()), apiFetch: vi.fn() }));
const config: DiagnosisConfiguration = {
  target_ref: "target-1",
  scope_ref: "scope-1",
  model_profile_ref: "model-1",
  approval_mode: "confirm_every_request",
  budget: { requests: 10, tokens: 1000, cost_usd_micros: 0, duration_seconds: 60 },
};
const basic: DiagnosisDraft = {
  id: "draft-1",
  revision: 1,
  source_digest: "a".repeat(64),
  configuration: config,
  state: "draft",
};
const envelope = (draft: unknown) => ({ version: "diagnosis-api.v1", draft });
const prepared = {
  ...basic,
  state: "prepared",
  task_id: "42",
  snapshot: {
    task_id: "42",
    preparation_ref: "prep-1",
    state: "prepared",
    started: false,
    drain_complete: false,
    result_ready: false,
  },
};
beforeEach(() => {
  vi.resetAllMocks();
  sessionStorage.clear();
});

describe("진단 관리 API 공개 응답 검증", () => {
  it("초안 생성은 버전·멱등키를 보내며 인증 재전송을 금지한다", async () => {
    vi.mocked(apiFetch).mockResolvedValue(envelope(basic));
    expect(await createDiagnosisDraft(config, "create-1")).toEqual(basic);
    expect(apiFetch).toHaveBeenCalledWith(
      "/api/diagnoses/v1/drafts",
      expect.objectContaining({
        method: "POST",
        headers: { "Idempotency-Key": "create-1" },
      }),
      false,
    );
  });
  it("생성 불명은 GET receipt로만 재조회한다", async () => {
    vi.mocked(apiFetch).mockResolvedValue(envelope(basic));
    await readCreatedDiagnosis("create-1");
    expect(apiFetch).toHaveBeenCalledWith("/api/diagnoses/v1/drafts", {
      cache: "no-store",
      headers: { "Idempotency-Key": "create-1" },
    });
  });
  it.each([
    { ...basic, state: "running" },
    { ...basic, revision: 2 },
    { ...basic, task_id: "99" },
    { ...basic, source_digest: "not-a-digest" },
    { ...basic, configuration: { ...config, target_ref: "different" } },
  ])("생성이 실행·다른 설정·잘못된 세대로 응답하면 거부한다", async (value) => {
    vi.mocked(apiFetch).mockResolvedValue(envelope(value));
    await expect(createDiagnosisDraft(config, "create-1")).rejects.toMatchObject({ status: 422 });
  });
  it("수정은 동일 ID의 다음 세대와 요청한 설정만 허용한다", async () => {
    const updated = { ...basic, revision: 2 };
    vi.mocked(apiFetch).mockResolvedValue(envelope(updated));
    expect(await updateDiagnosisDraft(basic, config)).toEqual(updated);
    vi.mocked(apiFetch).mockResolvedValue(envelope({ ...updated, id: "another" }));
    await expect(updateDiagnosisDraft(basic, config)).rejects.toMatchObject({ status: 422 });
  });
  it("검증 응답은 기존 초안 지문·설정·세대에 결속한다", async () => {
    vi.mocked(apiFetch).mockResolvedValue(envelope(prepared));
    expect((await commandDiagnosis(basic, "validate")).task_id).toBe("42");
    vi.mocked(apiFetch).mockResolvedValue(envelope({ ...prepared, source_digest: "b".repeat(64) }));
    await expect(commandDiagnosis(basic, "validate")).rejects.toMatchObject({ status: 422 });
  });
  it.each([
    {
      ...prepared,
      state: "unknown",
      task_id: undefined,
      snapshot: { ...prepared.snapshot, state: "unknown", task_id: undefined },
    },
    { ...prepared, snapshot: { ...prepared.snapshot, task_id: "43" } },
    { ...prepared, snapshot: { ...prepared.snapshot, started: true } },
    { ...prepared, state: "completed", snapshot: { ...prepared.snapshot, state: "completed", started: true } },
    {
      ...prepared,
      state: "running",
      snapshot: { ...prepared.snapshot, state: "running", started: true, result_ready: true },
    },
  ])("task/마감 모순 snapshot을 상태로 승격하지 않는다", async (value) => {
    vi.mocked(apiFetch).mockResolvedValue(envelope(value));
    await expect(readDiagnosisDraft("draft-1")).rejects.toMatchObject({ status: 422 });
  });
  it("unknown은 결과 준비와 독립이며 결과에는 drain이 필요하다", async () => {
    const value = {
      ...prepared,
      state: "unknown",
      snapshot: { ...prepared.snapshot, state: "unknown", started: true, drain_complete: true, result_ready: true },
    };
    vi.mocked(apiFetch).mockResolvedValue(envelope(value));
    expect(await readDiagnosisDraft("draft-1")).toMatchObject({
      state: "unknown",
      drain_complete: true,
      result_ready: true,
    });
    vi.mocked(apiFetch).mockResolvedValue(
      envelope({ ...value, snapshot: { ...value.snapshot, drain_complete: false } }),
    );
    await expect(readDiagnosisDraft("draft-1")).rejects.toMatchObject({ status: 422 });
  });
  it("미시작 unknown 조회는 허용하되 시작 응답은 거부한다", async () => {
    const value = { ...prepared, state: "unknown", snapshot: { ...prepared.snapshot, state: "unknown" } };
    vi.mocked(apiFetch).mockResolvedValue(envelope(value));
    expect((await readDiagnosisDraft("draft-1")).state).toBe("unknown");
    await expect(
      commandDiagnosis({ ...basic, state: "prepared", task_id: "42" }, "start", "start-1"),
    ).rejects.toMatchObject({ status: 422 });
  });
  it("다른 초안 조회 결과를 거부한다", async () => {
    vi.mocked(apiFetch).mockResolvedValue(envelope({ ...basic, id: "draft-2" }));
    await expect(readDiagnosisDraft("draft-1")).rejects.toMatchObject({ status: 422 });
  });
  it("시작이 prepared로 응답하면 실행 성공으로 보지 않는다", async () => {
    vi.mocked(apiFetch).mockResolvedValue(envelope(prepared));
    await expect(
      commandDiagnosis({ ...basic, state: "prepared", task_id: "42" }, "start", "start-1"),
    ).rejects.toMatchObject({ status: 422 });
  });
  it("catalog 참조·중복·예산을 검증한다", async () => {
    const profile = { id: "p1", label: "승인 설정", configuration: config };
    vi.mocked(apiFetch).mockResolvedValue({ version: "diagnosis-api.v1", profiles: [profile] });
    expect(await readDiagnosisProfiles()).toEqual([profile]);
    vi.mocked(apiFetch).mockResolvedValue({ version: "diagnosis-api.v1", profiles: [profile, profile] });
    await expect(readDiagnosisProfiles()).rejects.toMatchObject({ status: 422 });
    vi.mocked(apiFetch).mockResolvedValue({
      version: "diagnosis-api.v1",
      profiles: [{ ...profile, configuration: { ...config, target_ref: "https://target" } }],
    });
    await expect(readDiagnosisProfiles()).rejects.toMatchObject({ status: 422 });
  });
  it("저장소는 비민감 조회 핸들만 읽고 임의 상태를 복원하지 않는다", () => {
    storeDiagnosisHandle({ id: "draft-1", creationKey: "create-1", startKey: "start-1" });
    expect(readDiagnosisHandle()).toEqual({ id: "draft-1", creationKey: "create-1", startKey: "start-1" });
    expect(sessionStorage.getItem("abc2lab.diagnosisHandle.v1")).not.toContain("configuration");
    sessionStorage.setItem(
      "abc2lab.diagnosisHandle.v1",
      JSON.stringify({ id: "../bad", creationKey: "create-1", state: "prepared" }),
    );
    expect(readDiagnosisHandle()).toBeNull();
  });
});
