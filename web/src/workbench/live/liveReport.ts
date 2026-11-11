// 보고서 분류(remediation outcome) 이름. dev(#373)의 DiagnosticEntrySections 가 쓰는 자리라 이 이름 그대로 둔다.

import { diagnosticOutcomeLabels } from "@/lib/diagnosis/diagnostic-results";

/** 항목마다 서버가 붙이고 팀 lib(diagnosticOutcome)이 다시 검증한 분류 이름 (팀 보고서와 같은 이름). */
export const OUTCOME_LABEL: Readonly<Record<string, string>> = diagnosticOutcomeLabels;

/** outcome → 이름 (모르는 값은 원문). */
export function outcomeLabel(outcome: string): string {
  return OUTCOME_LABEL[outcome] ?? (outcome || "분류 없음");
}
