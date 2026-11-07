import { afterEach, expect, it, vi } from "vitest";

import { apiFetch } from "./api";
import { generateRemediation, type RemediationEntry, readCopyPrompt, readRemediation } from "./remediation";

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
