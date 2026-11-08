import { afterEach, expect, it, vi } from "vitest";

import { apiFetch } from "./api";
import {
  contextConflicts,
  generateRemediation,
  type RemediationEntry,
  readCopyPrompt,
  readRemediation,
  technologyConflicts,
} from "./remediation";

vi.mock("./api", () => ({ apiFetch: vi.fn() }));
afterEach(() => vi.clearAllMocks());
const entry = {
  entry_id: "finding-1",
  source_digest: "source",
  record: { id: 2, stale: false, bundle: { input_digest: "digest" } },
} as RemediationEntry;
it("explicitly scopes every operation and sends source CAS and tech only in the body", async () => {
  const signal = new AbortController().signal;
  await readRemediation("42", "shared", signal);
  expect(apiFetch).toHaveBeenLastCalledWith("/api/report/diagnostic/remediation?task=42&audience=shared", {
    signal,
    cache: "no-store",
  });
  await generateRemediation("42", entry, [], signal);
  expect(apiFetch).toHaveBeenLastCalledWith("/api/report/diagnostic/remediation?task=42&audience=internal", {
    signal,
    method: "POST",
    body: JSON.stringify({ entry_id: "finding-1", expected_source_digest: "source", technologies: [] }),
  });
  await readCopyPrompt("42", entry, signal);
  expect(apiFetch).toHaveBeenLastCalledWith(
    "/api/report/diagnostic/remediation/prompt?task=42&audience=internal&entry=finding-1&record=2&digest=digest",
    { signal, cache: "no-store" },
  );
});
it("refuses stale records before any request", () => {
  expect(() =>
    readCopyPrompt(
      "42",
      { ...entry, record: { ...(entry.record as NonNullable<RemediationEntry["record"]>), stale: true } },
      new AbortController().signal,
    ),
  ).toThrow("stale");
  expect(apiFetch).not.toHaveBeenCalled();
});

it("sends reviewed context in the CAS body, never the query", async () => {
  const signal = new AbortController().signal;
  const context = { component: "orders-api", endpoint: "GET /orders/{id}", source: "user_provided" };
  await generateRemediation("42", entry, [], signal, context);
  expect(apiFetch).toHaveBeenLastCalledWith("/api/report/diagnostic/remediation?task=42&audience=internal", {
    signal,
    method: "POST",
    body: JSON.stringify({ entry_id: "finding-1", expected_source_digest: "source", technologies: [], context }),
  });
});

it("never resubmits original observation metadata as a user supplement", async () => {
  await generateRemediation("42", entry, [], new AbortController().signal, {
    component: "orders-api",
    source: "observed",
    observations: [{ observation: "original", source: "observed", evidence_ref: "trusted-1" }],
    observations_truncated: true,
  });
  const body = JSON.parse(vi.mocked(apiFetch).mock.calls[0][1]?.body as string);
  expect(body.context).toEqual({ component: "orders-api", source: "user_provided" });
});
it("flags contradictory supplements without conflating different components or unknown values", () => {
  expect(
    contextConflicts({
      source: "user_provided",
      observation: "user summary",
      endpoint: "미확인",
      observations: [{ observation: "original summary", endpoint: "/orders" }],
    }),
  ).toEqual(["실제 관찰의 비민감 요약"]);
  const original = {
    kind: "language",
    name: "Go",
    version: "1",
    confidence: "observed",
    component: "orders-api",
    component_role: "backend",
  };
  expect(technologyConflicts([original, { ...original, name: "Elixir", component: "accounts-api" }])).toEqual([]);
  expect(technologyConflicts([original, { ...original, name: "Elixir" }])).toEqual(["orders-api / backend / 언어"]);
});
