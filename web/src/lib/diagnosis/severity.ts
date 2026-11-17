import { diagnosticOutcome } from "./diagnostic-results";
import type { RemediationEntry } from "./remediation";

export const SEVERITY_LABELS = { critical: "심각", high: "높음", medium: "중간", low: "낮음", info: "정보성" } as const;
export type SeverityLevel = keyof typeof SEVERITY_LABELS;
export function severityLevel(value: unknown): SeverityLevel | null {
  return typeof value === "string" && Object.hasOwn(SEVERITY_LABELS, value) ? (value as SeverityLevel) : null;
}

/** 심각도는 판정이 아니다. 미평가·불명·비취약 자료를 위험도에 섞지 않는다. */
export function entrySeverity(entry: RemediationEntry): SeverityLevel | null {
  const value = severityLevel(entry.severity),
    outcome = diagnosticOutcome(entry);
  if (!value || entry.source_load_failed) return null;
  if (outcome === "confirmed") return value;
  if (
    outcome === "review" &&
    entry.entry_id.startsWith("candidate-") &&
    entry.status === "completed" &&
    !entry.truncated &&
    entry.severity_source === "worker_estimate" &&
    validSeverityRationale(entry.severity_rationale)
  )
    return value;
  return null;
}
export function entrySeverityLabel(entry: RemediationEntry): string {
  const value = entrySeverity(entry);
  if (!value) return "위험도 미평가";
  return `${diagnosticOutcome(entry) === "review" ? "예상 " : ""}${SEVERITY_LABELS[value]}`;
}

export function validSeverityRationale(value: unknown): value is string {
  return (
    typeof value === "string" &&
    [...value.trim()].length >= 8 &&
    new TextEncoder().encode(value).length <= 1024 &&
    /\p{Script=Hangul}/u.test(value) &&
    !/\p{Cc}/u.test(value)
  );
}
