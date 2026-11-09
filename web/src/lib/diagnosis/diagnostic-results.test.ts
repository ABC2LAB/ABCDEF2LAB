import { describe, expect, it } from "vitest";

import { diagnosticOutcome, summarizeDiagnosticEntries } from "./diagnostic-results";
import { parseRemediationData, type RemediationEntry } from "./remediation";

const valid = {
  entry_id: "candidate-1",
  title: "의심 후보",
  outcome: "review",
  status: "completed",
  verification: "manual_review_required",
  source_digest: "a".repeat(64),
  evidence_digest: "b".repeat(64),
  evidence_version: 1,
  report_version: "report-v1",
  observed_at: "2026-11-09T00:00:00Z",
  run: "run-1",
  intent_id: 1,
  summary: "관찰",
  evidence: "비민감 근거",
  report: "",
  remediation: "",
  available: true,
  unavailable_reason: "",
} satisfies RemediationEntry;

describe("diagnostic result DTO", () => {
  it("uses canonical report entries without summing a second findings ledger", () => {
    const data = parseRemediationData(
      {
        task_id: 42,
        entries: [
          valid,
          { ...valid, entry_id: "finding-2", outcome: "confirmed", verification: "stored_verification" },
          { ...valid, entry_id: "candidate-3", outcome: "not_executed", status: "policy_not_executed" },
          { ...valid, entry_id: "candidate-4", outcome: "unknown", status: "result_unknown" },
          {
            ...valid,
            entry_id: "verdict-5",
            outcome: "negative",
            status: "allowed",
            verification: "stored_verification",
          },
        ],
      },
      "42",
      "internal",
    );
    expect(summarizeDiagnosticEntries(data.entries)).toEqual({
      confirmed: 1,
      review: 1,
      not_executed: 1,
      unknown: 1,
      negative: 1,
    });
  });

  it.each([
    { ...valid, outcome: "confirmed" },
    { ...valid, outcome: "negative" },
    { ...valid, entry_id: "finding-1", outcome: "confirmed" },
    { ...valid, outcome: "future_value" },
    { ...valid, evidence_digest: "" },
    { ...valid, evidence_version: -1 },
    { ...valid, verification: "unconfirmed" },
    { ...valid, observed_at: "" },
  ])("never upgrades unsupported or incomplete evidence", (entry) => {
    expect(diagnosticOutcome(entry)).toBe("unknown");
  });

  it("keeps an ambiguous judgment safely unknown without inventing evidence", () => {
    const data = parseRemediationData(
      { task_id: 42, entries: [{ entry_id: "candidate-1", outcome: { invalid: true } }] },
      "42",
      "internal",
    );
    expect(data.entries[0].outcome).toBe("unknown");
    expect(diagnosticOutcome(data.entries[0])).toBe("unknown");
  });

  it.each([
    { task_id: 99, entries: [valid] },
    { task_id: "42", entries: [valid] },
    { task_id: 42, entries: [valid, valid] },
    { task_id: 42, entries: [{ ...valid, entry_id: "untrusted-1" }] },
    { task_id: 42, entries: null },
    { task_id: 42, entries: [{ ...valid, context: { component: {} } }] },
    { task_id: 42, entries: [{ ...valid, record: { id: 1, entry_id: "candidate-2", stale: false } }] },
  ])("rejects wrong scope, duplicate identity, and malformed nested data", (value) => {
    expect(() => parseRemediationData(value, "42", "internal")).toThrow();
  });

  it("preserves a legacy finding with evidence version zero as review, not confirmed", () => {
    expect(diagnosticOutcome({ ...valid, entry_id: "finding-1", evidence_version: 0 })).toBe("review");
  });

  it("distinguishes a valid empty ledger from a failed read", () => {
    expect(parseRemediationData({ task_id: 42, entries: [] }, "42", "internal").entries).toEqual([]);
    expect(() => parseRemediationData(undefined, "42", "internal")).toThrow();
  });
});
