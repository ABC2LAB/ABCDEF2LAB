import { beforeEach, expect, it, vi } from "vitest";

import { apiFetch } from "./api";
import { MAX_TARGET_REQUESTS, commandURLDiagnosis, matchURLJob, parseURLCatalog, parseURLJob } from "./diagnosis-url";
import {
  catalog,
  envelope,
  externalModel as model,
  owner,
  ready,
  running,
  externalScope as scope,
} from "./diagnosis-url-fixtures";

vi.mock("./api", async (original) => ({ ...(await original<typeof import("./api")>()), apiFetch: vi.fn() }));
beforeEach(() => vi.resetAllMocks());
it("catalog and scope preserve installed provider and USD cap without credentials", () => {
  const profiles = [{ ...catalog.profiles[0], model }];
  expect(parseURLCatalog({ version: "diagnosis-api.v2", owner_context: owner, profiles }).profiles[0].model).toEqual(
    model,
  );
  expect(parseURLJob(envelope({ ...ready, scope }), owner).scope).toEqual(scope);
  expect(() =>
    parseURLCatalog({
      version: "diagnosis-api.v2",
      owner_context: owner,
      profiles: [{ ...profiles[0], api_key: "synthetic" }],
    }),
  ).toThrow();
  expect(() =>
    parseURLJob(envelope({ ...ready, scope: { ...scope, model: { ...model, api_key: "synthetic" } } }), owner),
  ).toThrow();
});
// 지금 보내는 모델은 3.6이지만 2026-11-14 전에 만든 작업 기록(3.5)도 그 모델 그대로 읽는다.
it("reads a job recorded with the previous model as recorded", () => {
  const recorded = { ...model, model: "qwen/qwen3.5-35b-a3b" };
  expect(parseURLJob(envelope({ ...ready, scope: { ...scope, model: recorded } }), owner).scope?.model).toEqual(recorded);
});
it.each([
  { ...model, model: "qwen/qwen3.7-35b-a3b" },
  { ...model, mode: "other" },
  { ...model, model: "unapproved" },
  { ...model, external_transfer: false },
  { ...model, input_tokens_per_call: 1 },
])("rejects an invalid external model contract", (value) => {
  expect(() => parseURLJob(envelope({ ...ready, scope: { ...scope, model: value } }), owner)).toThrow();
});
it("Start binds explicit external consent while omitting model credentials and routing input", async () => {
  const current = { ...ready, scope };
  vi.mocked(apiFetch).mockResolvedValue(envelope({ ...running, revision: current.revision + 1, scope }));
  await commandURLDiagnosis(current, "start", owner, "external-start", { externalTransferConfirmed: true });
  expect(apiFetch).toHaveBeenCalledTimes(1);
  const options = vi.mocked(apiFetch).mock.calls[0][1];
  expect(JSON.parse(String(options?.body))).toEqual({
    version: "diagnosis-api.v2",
    expected_revision: current.revision,
    external_transfer_confirmed: true,
  });
});
it("keeps reserved cost on unknown and rejects decreasing reservation", () => {
  const usage = {
    requests: 1,
    input_tokens: 10,
    output_tokens: 10,
    reserved_input_tokens: 262144,
    reserved_output_tokens: 2048,
    cost_usd_micros: 1,
    reserved_cost_usd_micros: 264192,
    usage_known: false,
  };
  const current = parseURLJob(envelope({ ...running, scope, model_usage: usage }), owner);
  expect(current.model_usage?.usage_known).toBe(false);
  const next = { ...current, revision: current.revision + 1, model_usage: { ...usage, reserved_cost_usd_micros: 0 } };
  expect(() => matchURLJob(next, current)).toThrow();
});
it("accepts failed calls counted at their reservation and rejects invalid or decreasing counts", () => {
  const usage = {
    requests: 2,
    input_tokens: 10,
    output_tokens: 10,
    reserved_input_tokens: 20,
    reserved_output_tokens: 20,
    cost_usd_micros: 1,
    usage_known: true,
    unknown_usage_calls: 1,
  };
  const current = parseURLJob(envelope({ ...running, scope, model_usage: usage }), owner);
  expect(current.model_usage?.unknown_usage_calls).toBe(1);
  for (const bad of [3, -1, 1.5, "1"]) {
    expect(() =>
      parseURLJob(envelope({ ...running, scope, model_usage: { ...usage, unknown_usage_calls: bad } }), owner),
    ).toThrow();
  }
  const next = { ...current, revision: current.revision + 1, model_usage: { ...usage, unknown_usage_calls: 0 } };
  expect(() => matchURLJob(next, current)).toThrow();
});

it.each([
  undefined,
  {},
  { externalTransferConfirmed: false },
  { externalTransferConfirmed: "true" as unknown as boolean },
])("명시적 외부 전송 동의가 없으면 요청 전에 시작을 거부한다: %o", async (confirmation) => {
  const current = { ...ready, scope };
  vi.mocked(apiFetch).mockResolvedValue(envelope({ ...running, revision: current.revision + 1, scope }));
  await expect(commandURLDiagnosis(current, "start", owner, "external-start", confirmation)).rejects.toMatchObject({
    status: 400,
    message: "external_transfer_confirmation_required",
  });
  expect(apiFetch).not.toHaveBeenCalled();
});
it("로컬 시작은 외부 전송 동의를 생성하지 않는다", async () => {
  vi.mocked(apiFetch).mockResolvedValue(envelope({ ...running, revision: ready.revision + 1 }));
  await commandURLDiagnosis(ready, "start", owner, "local-start", { externalTransferConfirmed: true });
  expect(JSON.parse(String(vi.mocked(apiFetch).mock.calls[0][1]?.body))).toEqual({
    version: "diagnosis-api.v2",
    expected_revision: ready.revision,
  });
});
it.each(["validate", "stop"] as const)("%s 요청은 외부 전송 동의를 담지 않는다", async (action) => {
  const current = { ...(action === "stop" ? running : ready), scope };
  const next = action === "stop" ? { ...current, revision: current.revision + 1, state: "stopping" } : current;
  vi.mocked(apiFetch).mockResolvedValue(envelope(next));
  await commandURLDiagnosis(current, action, owner, undefined, { externalTransferConfirmed: true });
  expect(JSON.parse(String(vi.mocked(apiFetch).mock.calls[0][1]?.body))).toEqual({
    version: "diagnosis-api.v2",
    expected_revision: current.revision,
  });
});

// 회차당 요청 상한은 워크플로 여부와 상관없이 받는다(일반 HTTP 모드 화면도 설치 값으로 등록). 범위 밖이면 거부하고,
// 워크플로 프로파일에는 반드시 있어야 한다.
it.each([
  ["general-400", false, 400, true],
  ["general-absent", false, undefined, true],
  ["general-1001", false, 1001, false],
  ["general-0", false, 0, false],
  ["workflow-absent", true, undefined, false],
] as const)("request limit %s", (_name, withWorkflow, limit, ok) => {
  const workflow = {
    format: "abc2lab.workbench-flow/v1",
    max_pages: 8,
    max_endpoints: 32,
    max_response_bytes: 65536,
    allow_reversible_writes: false,
  };
  const profile: Record<string, unknown> = { ...catalog.profiles[0] };
  if (withWorkflow) profile.workflow = workflow;
  if (limit !== undefined) profile.workflow_request_limit = limit;
  const parse = () => parseURLCatalog({ version: "diagnosis-api.v2", owner_context: owner, profiles: [profile] });
  if (!ok) {
    expect(parse).toThrow();
    return;
  }
  expect(parse().profiles[0].workflow_request_limit).toBe(limit);
  expect(parse().profiles[0].workflow).toBeUndefined();
});
// 워크플로 프로파일 요청 한도는 서버와 같은 코드 상한(1000)까지 받는다.
it.each([
  [1000, true],
  [1001, false],
])("workflow request limit %i accepted=%s", (limit, ok) => {
  const workflow = {
    format: "abc2lab.workbench-flow/v1",
    max_pages: 8,
    max_endpoints: 32,
    max_response_bytes: 65536,
    allow_reversible_writes: false,
  };
  const profiles = [{ ...catalog.profiles[0], workflow, workflow_request_limit: limit }];
  const parse = () => parseURLCatalog({ version: "diagnosis-api.v2", owner_context: owner, profiles });
  if (ok) expect(parse().profiles[0].workflow_request_limit).toBe(limit);
  else expect(parse).toThrow();
  expect(MAX_TARGET_REQUESTS).toBe(1000);
});
