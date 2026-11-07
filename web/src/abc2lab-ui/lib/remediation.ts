import { apiFetch } from "./api";

export type Technology = {
  kind: string;
  name: string;
  version: string;
  confidence: string;
  source?: string;
  component?: string;
  component_role?: string;
};
export type RemediationContext = {
  component?: string;
  component_role?: string;
  endpoint?: string;
  prerequisites?: string;
  observation?: string;
  expected_behavior?: string;
  prohibited_behavior?: string;
  source?: string;
};
export type RemediationBundle = {
  context?: RemediationContext;
  finding_id: string;
  input_digest: string;
  source_digest: string;
  evidence_version: string;
  report_version: string;
  remediation_version: string;
  ai_prompt: string;
  technologies: Technology[];
  security_condition: string[];
  immediate_mitigation: string[];
  root_fix: string[];
  configuration: string[];
  code_example: string[];
  assumptions: string[];
  verification: string[];
  residual_risk: string[];
};
export type RemediationRecord = { id: number; entry_id: string; stale: boolean; bundle?: RemediationBundle };
export type RemediationEntry = {
  context?: RemediationContext;
  entry_id: string;
  title: string;
  outcome: string;
  evidence_version: number;
  source_digest: string;
  summary: string;
  evidence: string;
  report: string;
  remediation: string;
  available: boolean;
  unavailable_reason: string;
  record?: RemediationRecord;
};
export type RemediationData = { task_id: number; entries: RemediationEntry[] };
const base = "/api/report/diagnostic/remediation";
export function readRemediation(task: string, audience: string, signal?: AbortSignal) {
  return apiFetch<RemediationData>(`${base}?${new URLSearchParams({ task, audience })}`, { signal, cache: "no-store" });
}
export function generateRemediation(
  task: string,
  entry: RemediationEntry,
  technologies: Technology[],
  signal: AbortSignal,
  context?: RemediationContext,
) {
  return apiFetch<RemediationRecord>(`${base}?${new URLSearchParams({ task, audience: "internal" })}`, {
    method: "POST",
    signal,
    body: JSON.stringify({
      entry_id: entry.entry_id,
      expected_source_digest: entry.source_digest,
      technologies,
      ...(context ? { context } : {}),
    }),
  });
}
export function readCopyPrompt(task: string, entry: RemediationEntry, signal: AbortSignal) {
  const record = entry.record;
  if (!record?.bundle || record.stale) throw new Error("stale");
  return apiFetch<{ entry_id: string; record_id: number; input_digest: string; ai_prompt: string }>(
    `${base}/prompt?${new URLSearchParams({ task, audience: "internal", entry: entry.entry_id, record: String(record.id), digest: record.bundle.input_digest })}`,
    { signal, cache: "no-store" },
  );
}
export const technologyLabels: Record<string, string> = {
  language: "언어",
  framework: "프레임워크",
  database: "데이터베이스",
  orm: "ORM",
  server: "웹 서버",
  authentication: "인증",
  deployment: "배포 조건",
};
