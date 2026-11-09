import type { RemediationEntry } from "./remediation";

export type DiagnosticOutcome = "confirmed" | "review" | "not_executed" | "unknown" | "negative";
export const diagnosticOutcomeLabels: Record<DiagnosticOutcome, string> = {
  confirmed: "확정",
  review: "검토 필요",
  not_executed: "미실행",
  unknown: "결과 불명",
  negative: "정상 대조 결과",
};

/** 서버가 검증한 결과만 분류한다. 후보를 finding으로 승격하거나 서로 다른 원장을 합산하지 않는다. */
export function diagnosticOutcome(entry: RemediationEntry): DiagnosticOutcome {
  if (
    !/^[a-f0-9]{64}$/.test(entry.source_digest) ||
    !/^[a-f0-9]{64}$/.test(entry.evidence_digest) ||
    entry.evidence_version < 0 ||
    !entry.status ||
    !entry.observed_at ||
    Number.isNaN(Date.parse(entry.observed_at))
  )
    return "unknown";
  const manual = entry.verification === "manual_review_required";
  const verified = entry.verification === "stored_verification";
  if (!manual && !verified) return "unknown";
  switch (entry.outcome) {
    case "confirmed":
      return entry.entry_id.startsWith("finding-") && verified ? "confirmed" : "unknown";
    case "negative":
      return entry.entry_id.startsWith("verdict-") && verified && entry.status === "allowed" ? "negative" : "unknown";
    case "review":
      if (entry.entry_id.startsWith("candidate-"))
        return manual && ["completed", "partial_policy_denial", "partial_unsupported"].includes(entry.status)
          ? "review"
          : "unknown";
      return "review";
    case "not_executed":
      return entry.entry_id.startsWith("candidate-") &&
        manual &&
        ["policy_not_executed", "unsupported", "forward_not_attempted", "isolation_unavailable"].includes(entry.status)
        ? "not_executed"
        : "unknown";
    default:
      return "unknown";
  }
}

export function summarizeDiagnosticEntries(entries: RemediationEntry[]): Record<DiagnosticOutcome, number> {
  const counts = { confirmed: 0, review: 0, not_executed: 0, unknown: 0, negative: 0 };
  for (const entry of entries) counts[diagnosticOutcome(entry)] += 1;
  return counts;
}
