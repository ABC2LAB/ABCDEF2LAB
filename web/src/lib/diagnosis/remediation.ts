import { z } from "zod";

import { apiFetch } from "./api";
import { diagnosticOutcome } from "./diagnostic-results";

// docs/contracts/practical-diagnostic-report.md; matches reportcapture and SQL bounds.
export const DIAGNOSTIC_CAPTURE_LIMITS = { pngBytes: 1048576, dimension: 4096, pixels: 8000000 } as const;

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
  method?: string;
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
export type ReproductionStep = {
  task_id: number;
  entry_id: string;
  id: string;
  number: number;
  title: string;
  source_step_id: string;
  run_ref: string;
  call_ref: string;
  evidence_ref: string;
  evidence_version: number;
  evidence_digest: string;
  binding_digest: string;
  observed_at: string;
  method: string;
  path: string;
  prerequisites: string;
  observation: string;
  depends_on?: string[];
  truncated: boolean;
  capture: { status: string; ref?: string; digest?: string; captured_at?: string; width?: number; height?: number };
};
export type RemediationEntry = {
  steps?: ReproductionStep[];
  steps_truncated?: boolean;
  source_load_failed?: boolean;
  truncated?: boolean;
  vuln_class?: string;
  severity?: string;
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
export async function readRemediation(task: string, audience: string, signal?: AbortSignal): Promise<RemediationData> {
  const data = await apiFetch<unknown>(`${base}?${new URLSearchParams({ task, audience })}`, {
    signal,
    cache: "no-store",
  });
  return parseRemediationData(data, task, audience);
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

// 서버 DTO를 구조 검증한 뒤에만 화면으로 전달한다. 미래 판정이나 빠진 근거는 아래에서 미확인으로 보존한다.
const text = z.string();
const strings = z
  .array(text)
  .nullable()
  .transform((value) => value ?? []);
const natural = z.number().int().nonnegative().max(Number.MAX_SAFE_INTEGER);
const contextFields = {
  component: text.optional(),
  component_role: text.optional(),
  endpoint: text.optional(),
  prerequisites: text.optional(),
  observation: text.optional(),
  expected_behavior: text.optional(),
  prohibited_behavior: text.optional(),
  source: text.optional(),
};
const contextSchema = z.object({
  ...contextFields,
  observations: z
    .array(
      z.object({
        ...contextFields,
        method: text.optional(),
        evidence_ref: text.optional(),
        observed_at: text.optional(),
        run_id: text.optional(),
        call_id: text.optional(),
        step_id: text.optional(),
        depends_on: strings.optional(),
        truncated: z.boolean().optional(),
      }),
    )
    .optional(),
  observations_truncated: z.boolean().optional(),
});
const technologySchema = z.object({
  kind: text,
  name: text,
  version: text,
  confidence: text,
  source: text.optional(),
  component: text.optional(),
  component_role: text.optional(),
  evidence_ref: text.optional(),
  observed_at: text.optional(),
});
const bundleSchema = z.object({
  finding_id: text,
  input_digest: text.min(1),
  source_digest: text.min(1),
  evidence_version: text,
  report_version: text,
  remediation_version: text,
  ai_prompt: text,
  technologies: z
    .array(technologySchema)
    .nullable()
    .transform((value) => value ?? []),
  missing_context_fields: strings.optional(),
  context: contextSchema.optional(),
  security_condition: strings,
  immediate_mitigation: strings,
  root_fix: strings,
  configuration: strings,
  code_example: strings,
  assumptions: strings,
  verification: strings,
  residual_risk: strings,
});
const recordSchema = z.object({
  id: natural.positive(),
  entry_id: text,
  stale: z.boolean(),
  bundle: bundleSchema.optional(),
});
const stepSchema = z.object({
  task_id: natural.positive(),
  entry_id: text.regex(/^(candidate|finding|verdict)-[1-9][0-9]*$/),
  id: text.regex(/^step-[a-f0-9]{24}$/),
  number: natural.positive().max(32),
  title: text,
  source_step_id: text,
  run_ref: text.min(1),
  call_ref: text,
  evidence_ref: text.min(1),
  evidence_version: natural.positive(),
  evidence_digest: text.regex(/^[a-f0-9]{64}$/),
  binding_digest: text.regex(/^[a-f0-9]{64}$/),
  observed_at: text,
  method: text,
  path: text,
  prerequisites: text,
  observation: text,
  depends_on: strings.optional(),
  truncated: z.boolean(),
  capture: z.object({
    status: z.enum([
      "captured",
      "not_captured",
      "not_supported",
      "failed",
      "storage_unavailable",
      "shared_omitted",
      "capacity_omitted",
      "binding_invalid",
    ]),
    ref: text.optional(),
    digest: text.optional(),
    captured_at: text.optional(),
    width: natural.optional(),
    height: natural.optional(),
  }),
});
const entrySchema = z.object({
  steps: z.array(stepSchema).max(32).optional(),
  steps_truncated: z.boolean().optional(),
  source_load_failed: z.boolean().optional(),
  truncated: z.boolean().optional(),
  vuln_class: text.optional(),
  severity: text.optional(),
  entry_id: text.regex(/^(candidate|finding|verdict)-[1-9][0-9]*$/),
  title: text.catch("제목 미확인"),
  outcome: text.catch("unknown"),
  status: text.catch(""),
  verification: text.catch(""),
  evidence_digest: text.catch(""),
  report_version: text.catch(""),
  run: text.catch(""),
  intent_id: natural.catch(0),
  observed_at: text.catch(""),
  evidence_version: natural.catch(-1),
  source_digest: text.catch(""),
  summary: text.catch(""),
  evidence: text.catch(""),
  report: text.catch(""),
  remediation: text.catch(""),
  available: z.boolean().catch(false),
  unavailable_reason: text.catch("근거 미확인"),
  context: contextSchema.optional(),
  technologies: z.array(technologySchema).optional(),
  record: recordSchema.optional(),
});
const dataSchema = z.object({ task_id: natural.positive(), entries: z.array(entrySchema) });

export function parseRemediationData(value: unknown, task: string, audience: string): RemediationData {
  const data = dataSchema.parse(value);
  if (String(data.task_id) !== task || !["internal", "shared"].includes(audience))
    throw new Error("보고서 작업 범위 불일치");
  const ids = new Set<string>();
  for (const entry of data.entries) {
    if (ids.has(entry.entry_id)) throw new Error("보고서 결과 식별자 중복");
    ids.add(entry.entry_id);
    if (
      audience === "shared" &&
      (entry.source_digest ||
        entry.context ||
        entry.technologies?.length ||
        entry.record?.bundle ||
        entry.steps?.length)
    )
      throw new Error("공유 보고서 공개 범위 불일치");
    if (audience === "internal") {
      const outcome = diagnosticOutcome(entry);
      if (outcome !== entry.outcome) {
        entry.available = false;
        entry.unavailable_reason = "판정 또는 증거 정보를 확인하지 못했어요. 다시 조회해 주세요.";
      }
      entry.outcome = outcome;
    }
    if (
      entry.steps?.some(
        (step, i) =>
          step.task_id !== data.task_id ||
          step.entry_id !== entry.entry_id ||
          step.number !== i + 1 ||
          step.run_ref !== entry.run ||
          step.evidence_version !== entry.evidence_version ||
          step.evidence_digest !== entry.evidence_digest ||
          step.id !== `step-${step.binding_digest.slice(0, 24)}`,
      )
    )
      throw new Error("재현 단계의 작업·회차·증거 버전 불일치");
    if (
      entry.steps?.some(
        (step) =>
          step.capture.status === "captured" &&
          (!/^[a-f0-9]{64}$/.test(step.capture.digest ?? "") ||
            step.capture.ref !== `shot-${step.binding_digest.slice(0, 24)}` ||
            !step.capture.captured_at ||
            !step.capture.width ||
            !step.capture.height ||
            step.capture.width > DIAGNOSTIC_CAPTURE_LIMITS.dimension ||
            step.capture.height > DIAGNOSTIC_CAPTURE_LIMITS.dimension ||
            step.capture.width * step.capture.height > DIAGNOSTIC_CAPTURE_LIMITS.pixels),
      )
    )
      throw new Error("스크린샷 결속·규격 불일치");
    const record = entry.record;
    const bundle = record?.bundle;
    if (record && (record.entry_id !== entry.entry_id || (record.stale && bundle)))
      throw new Error("저장 조치 결속 불일치");
    if (
      bundle &&
      (bundle.finding_id !== entry.entry_id ||
        bundle.source_digest !== entry.source_digest ||
        bundle.evidence_version !== String(entry.evidence_version))
    )
      throw new Error("저장 조치 증거 버전 불일치");
  }
  return data;
}
