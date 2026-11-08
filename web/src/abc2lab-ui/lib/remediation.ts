import { apiFetch } from "./api";

export type Technology = {
  kind: string;
  name: string;
  version: string;
  confidence: string;
  source?: string;
  component?: string;
  component_role?: string;
  evidence_ref?: string;
  observed_at?: string;
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
  observations?: ContextObservation[];
  observations_truncated?: boolean;
};
export type ContextObservation = Omit<RemediationContext, "observations" | "observations_truncated"> & {
  evidence_ref?: string;
  observed_at?: string;
  run_id?: string;
  call_id?: string;
  step_id?: string;
  depends_on?: string[];
  truncated?: boolean;
};
export type RemediationBundle = {
  missing_context_fields?: string[];
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
  technologies?: Technology[];
  entry_id: string;
  title: string;
  outcome: string;
  status: string;
  verification: string;
  evidence_digest: string;
  report_version: string;
  run: string;
  intent_id: number;
  observed_at: string;
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
      ...(context
        ? {
            context: {
              ...Object.fromEntries(
                Object.keys(contextLabels)
                  .filter((key) => context[key as keyof typeof contextLabels] !== undefined)
                  .map((key) => [key, context[key as keyof typeof contextLabels]]),
              ),
              ...(context.component_role !== undefined ? { component_role: context.component_role } : {}),
              source: "user_provided",
            },
          }
        : {}),
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

// 출처와 확인 수준은 별개다. 수동 확인을 프로그램 관측으로 표시하지 않는다.
export const sourceLabels: Record<string, string> = {
  observed: "실행 관측",
  inferred: "모델·분석기 추정",
  user_provided: "사용자 제공·수동 검토",
  unknown: "미확인",
};
export const confidenceLabels: Record<string, string> = {
  confirmed: "제공자가 확인했다고 표시",
  observed: "관측했다고 표시",
  inferred: "추정",
  unknown: "미확인",
};
export const contextLabels = {
  component: "영향 컴포넌트",
  endpoint: "엔드포인트 경로 템플릿",
  prerequisites: "재현 사전 조건",
  observation: "실제 관찰의 비민감 요약",
  expected_behavior: "유지할 정상 동작",
  prohibited_behavior: "차단할 취약 동작",
};
function known(value?: string) {
  return !!value && value !== "unknown" && !value.startsWith("미확인");
}
export function contextConflicts(context: RemediationContext) {
  if (context.source !== "user_provided") return [];
  return (Object.keys(contextLabels) as (keyof typeof contextLabels)[])
    .filter((key) => known(context[key]) && context.observations?.some((o) => known(o[key]) && o[key] !== context[key]))
    .map((key) => contextLabels[key]);
}
export function technologyConflicts(technologies: Technology[]) {
  const groups = new Map<string, Set<string>>();
  for (const t of technologies) {
    if (!known(t.component) || !known(t.name)) continue;
    const key = `${t.component} / ${t.component_role ?? "unknown"} / ${technologyLabels[t.kind] ?? t.kind}`;
    const values = groups.get(key) ?? new Set<string>();
    values.add(`${t.name} ${known(t.version) ? t.version : ""}`.trim());
    groups.set(key, values);
  }
  return [...groups].filter(([, values]) => values.size > 1).map(([key]) => key);
}
