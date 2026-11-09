import { beforeEach, expect, it, vi } from "vitest";
import { apiFetch } from "./api";
import { commandURLDiagnosis, matchURLJob, parseURLCatalog, parseURLJob } from "./diagnosis-url";
import { catalog, envelope, owner, ready, running } from "./diagnosis-url-fixtures";

vi.mock("./api", async (original) => ({ ...(await original<typeof import("./api")>()), apiFetch: vi.fn() }));
beforeEach(() => vi.resetAllMocks());
const model = {
  mode: "openrouter",
  model: "qwen/qwen3.5-35b-a3b",
  provider: "fixture/endpoint",
  external_transfer: true,
  input_tokens_per_call: 262144,
  output_tokens_per_call: 2048,
} as const;
const scope = { ...ready.scope!, model, budget: { ...ready.scope!.budget, cost_usd_micros: 6000000 } };
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
it.each([
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
  await commandURLDiagnosis(current, "start", owner, "external-start");
  const options = vi.mocked(apiFetch).mock.calls[0][1]!;
  expect(JSON.parse(String(options.body))).toEqual({
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
