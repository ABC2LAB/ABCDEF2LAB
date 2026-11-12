import { beforeEach, describe, expect, it, vi } from "vitest";

import { apiFetch } from "./api";
import {
  commandURLDiagnosis,
  createURLDraft,
  matchURLHandle,
  matchURLJob,
  parseURLCatalog,
  parseURLJob,
  readURLHandle,
  readURLReceipt,
  readURLStatus,
  storeURLHandle,
  urlDiagnosisInput,
} from "./diagnosis-url";
import { catalog, draft, envelope, handle, owner, ready, running, scope } from "./diagnosis-url-fixtures";

vi.mock("./api", async (original) => ({ ...(await original<typeof import("./api")>()), apiFetch: vi.fn() }));
beforeEach(() => {
  vi.resetAllMocks();
  sessionStorage.clear();
});

describe("URL 진단 v2 공개 계약", () => {
  it("catalog는 owner와 허용된 profile만 받는다", () => {
    expect(parseURLCatalog({ version: "diagnosis-api.v2", owner_context: owner, profiles: catalog.profiles })).toEqual(
      catalog,
    );
    expect(() => parseURLCatalog({ version: "diagnosis-api.v2", profiles: [] })).toThrow();
    expect(() =>
      parseURLCatalog({
        version: "diagnosis-api.v2",
        owner_context: owner,
        profiles: [...catalog.profiles, ...catalog.profiles],
      }),
    ).toThrow();
  });
  it.each([
    "http://target.example/",
    "https://user:pass@target.example/",
    "https://target.example/#x",
    "https://target.example/?token=x",
    "file:///etc/passwd",
    "https://target.example/%2e%2e/admin",
    "https://target.example/../admin",
    "https://target.example//admin",
    "https://target.example:0443/",
    "https://target.example./",
    "https://2130706433/",
    "https://127.1/",
    "https://target.example/공백",
  ])("비밀·미지원 URL을 막는다: %s", (target) => {
    expect(() => urlDiagnosisInput(target, "profile-1", "")).toThrow();
  });
  it("UTF8 이름 상한을 지켜 서버에서 거절될 이름을 저장하지 않는다", () => {
    expect(() => urlDiagnosisInput("https://target.example/", "profile-1", "가".repeat(54))).toThrow();
  });
  it("시작 전 URL canonicalization은 실행 권위를 만들지 않는다", () => {
    expect(urlDiagnosisInput(" https://TARGET.example ", "profile-1", " 진단 ")).toEqual({
      target_url: "https://target.example/",
      profile_id: "profile-1",
      display_name: "진단",
    });
  });
  it.each([
    { state: "unknown", phase: "planning", preparation_started: true, started: false, authority_state: "not_issued" },
    {
      state: "unknown",
      phase: "report",
      preparation_started: true,
      started: true,
      authority_state: "consumed",
      task_id: "42",
      drain_complete: false,
      result_ready: true,
    },
    { state: "cancelled", phase: "planning", preparation_started: true, started: false, drain_complete: true },
    { state: "stopping", phase: "planning", preparation_started: true, started: false },
  ])("준비불명·취소와 독립 known fact를 보존한다: %o", (change) => {
    expect(parseURLJob(envelope({ ...draft, ...change }), owner)).toMatchObject(change);
  });
  it.each([
    { ...ready, scope: undefined },
    { ...running, started: false },
    { ...draft, state: "completed" },
    { ...draft, state: "future" },
    { ...draft, phase: "future" },
    { ...draft, task_id: "00" },
    { ...draft, revision: 0 },
    { ...draft, revision: Number.MAX_SAFE_INTEGER + 1 },
    { ...ready, scope: { ...scope, origin: "https://other.example" } },
    { ...ready, scope: { ...scope, query_policy: "unknown" } },
    { ...ready, scope: { ...scope, methods: ["POST"] } },
    { ...draft, model_usage: { usage_known: false } },
    { ...draft, token: "must-not-enter-ui" },
  ])("불완전하거나 모순된 서버 계약을 fail closed 한다: %o", (value) => {
    expect(() => parseURLJob(envelope(value), owner)).toThrow();
  });
  it("다른 owner 응답은 거절한다", () => {
    expect(() => parseURLJob({ ...envelope(draft), owner_context: "b".repeat(64) }, owner)).toThrow();
  });
  it("create payload는 input뿐이고 auth 자동재시도를 하지 않는다", async () => {
    vi.mocked(apiFetch).mockResolvedValue(envelope(draft));
    await createURLDraft(draft.input, "create-1", owner);
    expect(apiFetch).toHaveBeenCalledWith(
      "/api/diagnoses/v2/drafts",
      {
        method: "POST",
        redirect: "error",
        headers: { "Idempotency-Key": "create-1" },
        body: JSON.stringify({ version: "diagnosis-api.v2", input: draft.input }),
      },
      false,
    );
  });
  it("create 응답 유실 복구는 동일 key GET만 한다", async () => {
    vi.mocked(apiFetch).mockResolvedValue(envelope(ready));
    await readURLReceipt("create-1", owner);
    expect(apiFetch).toHaveBeenCalledWith(
      "/api/diagnoses/v2/drafts",
      {
        cache: "no-store",
        redirect: "error",
        headers: { "Idempotency-Key": "create-1" },
      },
      false,
    );
  });
  it("start는 현재 revision과 같은 key를 보내며 preparing을 실행완료로 해석하지 않는다", async () => {
    const preparing = { ...ready, revision: 3, state: "preparing", phase: "planning", preparation_started: true };
    vi.mocked(apiFetch).mockResolvedValue(envelope(preparing));
    expect(await commandURLDiagnosis(ready, "start", owner, "start-1")).toMatchObject(preparing);
    expect(apiFetch).toHaveBeenCalledWith(
      "/api/diagnoses/v2/drafts/job-1/start",
      {
        method: "POST",
        redirect: "error",
        headers: { "Idempotency-Key": "start-1" },
        body: JSON.stringify({ version: "diagnosis-api.v2", expected_revision: 2 }),
      },
      false,
    );
  });
  it("불명 status 복구에서 시작을 다시 보내지 않는다", async () => {
    vi.mocked(apiFetch).mockResolvedValue(envelope({ ...draft, state: "unknown", preparation_started: true }));
    await readURLStatus("job-1", owner);
    expect(apiFetch).toHaveBeenCalledWith(
      "/api/diagnoses/v2/drafts/job-1/status",
      { cache: "no-store", redirect: "error" },
      false,
    );
    expect(apiFetch).toHaveBeenCalledTimes(1);
  });
  it("증가 revision은 허용하지만 다른 id/input/digest와 뒤로 간 revision은 거절한다", () => {
    expect(matchURLJob(running, ready)).toEqual(running);
    for (const change of [
      { id: "other" },
      { revision: 1 },
      { source_digest: "b".repeat(64) },
      { input: { ...ready.input, profile_id: "other" } },
    ]) {
      expect(() => matchURLJob({ ...running, ...change }, ready)).toThrow();
    }
  });
});

describe("같은 실행의 사실 보존", () => {
  it("task·승인 범위와 확인된 사실을 뒤로 돌리지 않는다", () => {
    for (const change of [
      { task_id: "43" },
      { started: false },
      { preparation_started: false },
      { scope: { ...scope, methods: ["HEAD"] } },
    ]) {
      expect(() => matchURLJob({ ...running, ...change }, running)).toThrow();
    }
    const closed = { ...running, drain_complete: true, result_ready: true };
    expect(() => matchURLJob({ ...closed, drain_complete: false }, closed)).toThrow();
    expect(() => matchURLJob({ ...closed, result_ready: false }, closed)).toThrow();
  });
});

describe("owner별 조회 handle", () => {
  it("상태나 토큰 없이 같은 payload/key와 시작 당시 revision을 보관한다", () => {
    const saved = { ...handle, revision: 7, startKey: "start-1", startRevision: 2 };
    storeURLHandle(saved);
    expect(readURLHandle(owner)).toEqual(saved);
    expect(Object.keys(JSON.parse(sessionStorage.getItem("abc2lab.urlDiagnosisHandle.v2") ?? "{}"))).not.toContain(
      "started",
    );
  });
  it("owner 변경은 이전 핸들을 재사용하지 않는다", () => {
    storeURLHandle(handle);
    expect(readURLHandle("b".repeat(64))).toBeNull();
    expect(sessionStorage.getItem("abc2lab.urlDiagnosisHandle.v2")).toBeNull();
  });
  it.each([
    { ...handle, token: "secret" },
    { ...handle, startKey: "start-1" },
    { ...handle, startKey: "start-1", startRevision: 99 },
    { ...handle, input: { ...handle.input, target_url: "https://user:pass@target.example/" } },
    { creationKey: "x", ownerContext: owner, input: draft.input, revision: 1 },
  ])("손상되거나 비밀 필드가 섞인 handle은 버린다", (value) => {
    sessionStorage.setItem("abc2lab.urlDiagnosisHandle.v2", JSON.stringify(value));
    expect(readURLHandle(owner)).toBeNull();
  });
});

describe("시작 전 설치 승인 재결속", () => {
  const rebound = {
    ...ready,
    revision: 3,
    source_digest: "d".repeat(64),
    scope: { ...scope, budget: { ...scope.budget, requests: 2 } },
  };
  it("동일 id/input의 상위 revision 검증만 새 digest/scope를 허용한다", async () => {
    expect(matchURLJob(rebound, ready, { allowPreStartRebind: true })).toEqual(rebound);
    expect(() => matchURLJob(rebound, ready)).toThrow();
    expect(() => matchURLJob({ ...rebound, revision: 2 }, ready, { allowPreStartRebind: true })).toThrow();
    vi.mocked(apiFetch).mockResolvedValue(envelope(rebound));
    expect(await commandURLDiagnosis(ready, "validate", owner)).toEqual(rebound);
  });
  it("start key나 준비 관찰 latch 뒤에는 status의 새 결속을 거절한다", () => {
    expect(matchURLHandle(rebound, handle)).toEqual(rebound);
    expect(() => matchURLHandle(rebound, { ...handle, startKey: "start-1", startRevision: 2 })).toThrow();
    expect(() => matchURLHandle(rebound, { ...handle, bindingLocked: true })).toThrow();
    expect(() => matchURLJob(rebound, running, { allowPreStartRebind: true })).toThrow();
  });
});
describe("서버 known fact와 단조성", () => {
  it("동일 revision의 optional 사실 추가도 거절하고 scope 추가에는 명시적 prestart rebind가 필요하다", () => {
    expect(() => matchURLJob({ ...draft, reason_code: "target_not_approved" }, draft)).toThrow();
    const unknown: typeof draft = { ...draft, state: "unknown", preparation_started: true };
    expect(() => matchURLJob({ ...unknown, task_id: "42" }, unknown)).toThrow();
    expect(() => matchURLJob({ ...draft, scope }, draft)).toThrow();
    const scoped = { ...draft, revision: 2, scope };
    expect(() => matchURLJob(scoped, draft)).toThrow();
    expect(matchURLJob(scoped, draft, { allowPreStartRebind: true })).toEqual(scoped);
  });
  it.each([
    { ...draft, state: "failed" },
    { ...draft, state: "cancelled" },
    { ...draft, state: "failed", preparation_started: true },
    { ...draft, state: "failed", drain_complete: true },
    { ...draft, state: "failed", task_id: "42", result_ready: true },
    { ...running, state: "failed", drain_complete: true },
    { ...draft, state: "unknown", task_id: "42", result_ready: true },
  ])("마감·결과·실패·취소의 불완전 사실을 거절한다", (value) => {
    expect(() => parseURLJob(envelope(value), owner)).toThrow();
  });
  it.each(["failed", "cancelled"])("작업 생성 전 %s의 prep/drain은 보존하고 task를 만들지 않는다", (state) => {
    const value = { ...draft, state, preparation_started: true, drain_complete: true };
    expect(parseURLJob(envelope(value), owner)).toMatchObject(value);
    expect(parseURLJob(envelope(value), owner).task_id).toBeUndefined();
  });
  it("같은 revision 변경 및 consumed/unknown 권위·단계·사용량 후퇴를 거절한다", () => {
    expect(() => matchURLJob({ ...running, state: "stopping" }, running)).toThrow();
    expect(() => matchURLJob({ ...running, revision: 6, authority_state: "unknown" }, running)).toThrow();
    expect(() => matchURLJob({ ...running, revision: 6, phase: "planning" }, running)).toThrow();
    const unknownAuthority = { ...running, authority_state: "unknown" as const };
    expect(() => matchURLJob({ ...running, revision: 6 }, unknownAuthority)).toThrow();
    const usage = {
      requests: 2,
      input_tokens: 10,
      output_tokens: 10,
      reserved_input_tokens: 20,
      reserved_output_tokens: 20,
      cost_usd_micros: 10,
      usage_known: true,
    };
    const used = { ...running, model_usage: usage };
    expect(() => matchURLJob({ ...used, revision: 6, model_usage: { ...usage, requests: 1 } }, used)).toThrow();
    expect(() => matchURLJob({ ...running, revision: 6 }, used)).toThrow();
  });
});

describe("실행 결과 칸(docs/contracts/run-outcome-states.md §3.2)", () => {
  const completed = {
    ...running,
    state: "completed" as const,
    phase: "report" as const,
    drain_complete: true,
    result_ready: true,
  };
  const counts = (c: Partial<Record<"done" | "policy_limited" | "incomplete" | "unknown" | "stopped", number>>) => ({
    done: 0,
    policy_limited: 0,
    incomplete: 0,
    unknown: 0,
    stopped: 0,
    ...c,
  });
  it.each([
    { outcome: "complete", intent_outcomes: counts({ done: 2, policy_limited: 1 }) },
    { outcome: "partial", closing_reason: "budget_limited", intent_outcomes: counts({ done: 1, incomplete: 2 }) },
    { outcome: "no_intents", closing_reason: "budget_limited", intent_outcomes: counts({}) },
  ])("completed는 대조식에 맞는 결과를 받는다: %o", (result) => {
    expect(parseURLJob(envelope({ ...completed, ...result }), owner)).toMatchObject(result);
  });
  it("이 칸 이전에 저장된 completed는 결과 없이 온다", () => {
    expect(parseURLJob(envelope(completed), owner).outcome).toBeUndefined();
  });
  it("cancelled는 개수만 받는다", () => {
    const cancelled = { ...completed, state: "cancelled" as const, intent_outcomes: counts({ done: 1, stopped: 1 }) };
    expect(parseURLJob(envelope(cancelled), owner).intent_outcomes).toEqual(counts({ done: 1, stopped: 1 }));
  });
  it.each([
    { ...completed, outcome: "complete", intent_outcomes: counts({ done: 1, incomplete: 1 }) },
    { ...completed, outcome: "complete" },
    { ...completed, intent_outcomes: counts({ done: 1 }) },
    { ...completed, closing_reason: "budget_limited" },
    { ...completed, outcome: "complete", closing_reason: "time_limited", intent_outcomes: counts({ done: 1 }) },
    { ...completed, outcome: "partial", intent_outcomes: counts({ done: 1, stopped: -1, incomplete: 2 }) },
    { ...completed, outcome: "complete", intent_outcomes: { ...counts({ done: 1 }), extra: 0 } },
    { ...completed, outcome: "vulnerable", intent_outcomes: counts({ done: 1 }) },
    { ...completed, state: "cancelled", outcome: "complete", intent_outcomes: counts({ done: 1 }) },
    { ...completed, state: "failed", intent_outcomes: counts({ done: 1 }) },
    { ...running, intent_outcomes: counts({ done: 1 }) },
  ])("계약 밖 결과는 fail closed 한다: %o", (value) => {
    expect(() => parseURLJob(envelope(value), owner)).toThrow();
  });
});
