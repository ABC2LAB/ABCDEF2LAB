import { apiFetch } from "./api";

/**
 * 엔진 DTO의 UI가 쓰는 부분만 추린 타입 (정본: server/dto.go).
 * 필드를 늘릴 땐 dto.go 와 맞춰서 추가한다.
 */

/** server/dto.go FindingSeverityDTO — 심각도별 발견 수 (UI Severity 와 동일 키) */
export type FindingSeverity = {
  critical: number;
  high: number;
  medium: number;
  low: number;
};

/** server/dto.go TaskDTO (부분) */
export type EngineTask = {
  id: string;
  name: string;
  description: string;
  goal: string;
  /** created | running | paused | done | failed */
  status: string;
  created_at: string;
  completed_at: string;
  goals_total: number;
  goals_met: number;
  in_flight: number;
  findings: FindingSeverity;
};

/** GET /api/tasks 응답: 목록 + 현재 활성 task id */
export type TaskListResponse = {
  active: string;
  tasks: EngineTask[];
};

/** GET /api/tasks — 점검(task) 목록 (+ active id) */
export async function listTasks(): Promise<TaskListResponse> {
  const res = await apiFetch<TaskListResponse>("/api/tasks");
  return { active: res.active ?? "", tasks: res.tasks ?? [] };
}

/** GET /api/tasks/{id} — 점검 하나 상세 (보고서/대시보드 헤더용) */
export async function getTask(taskId: string): Promise<EngineTask> {
  return apiFetch<EngineTask>(`/api/tasks/${encodeURIComponent(taskId)}`);
}

/** server/dto.go FindingAssetDTO — 발견이 걸린 자산(표시용 라벨 포함) */
export type EngineFindingAsset = {
  id: string;
  type: string;
  /** 예: "GET /api/orders/1" */
  label: string;
};

/**
 * server/dto.go FindingDTO (UI가 쓰는 부분).
 * ※ E③ 판정(CONFIRMED/SUSPECTED/UNDECIDABLE)은 아직 이 DTO에 없다(verdict-record 계약 대기).
 *   status 는 워크플로우 상태라 판정과 별개 — 판정 뱃지로 쓰지 않는다.
 */
export type EngineFinding = {
  id: string;
  finding_id?: string;
  vulnclass: string;
  name?: string;
  /** critical | high | medium | low */
  severity: string;
  /** pending | in_progress | confirmed | resolved | fixed | false_positive | ignored | duplicate | risk_accepted */
  status: string;
  summary: string;
  assets?: EngineFindingAsset[];
  task_id?: string;
  ts: string;
};

/** GET /api/exploration/findings?task={id} — 해당 task의 발견 목록(bare array) */
export async function listTaskFindings(taskId: string): Promise<EngineFinding[]> {
  if (!taskId) return [];
  const res = await apiFetch<EngineFinding[]>(`/api/exploration/findings?task=${encodeURIComponent(taskId)}`);
  return res ?? [];
}

/**
 * guard/guard.go AuditEntry — E²(GUARD)가 게이트한 도구 호출 1건.
 * ※ 변조탐지 해시체인·권한분리(우리 기여)는 아직 이 레코드에 없다 — UI에서 "준비 중"으로 둔다.
 */
export type EngineAuditEntry = {
  /** unix epoch(초) */
  ts: number;
  tool: string;
  /** allow | block */
  action: string;
  reason?: string;
  command?: string;
};

/** GET /api/audit?task={id} 응답: 게이트 로그 + 실패 귀속 집계 */
export type AuditResponse = {
  entries: EngineAuditEntry[];
};

/** GET /api/audit?task={id} — 해당 task의 E² 게이트 감사 로그 */
export async function getAudit(taskId: string): Promise<AuditResponse> {
  if (!taskId) return { entries: [] };
  // task 없으면 서버가 bare [] 를 주므로(핸들러), entries 추출을 방어적으로.
  const res = await apiFetch<AuditResponse | EngineAuditEntry[]>(`/api/audit?task=${encodeURIComponent(taskId)}`);
  if (Array.isArray(res)) return { entries: res };
  return { entries: res.entries ?? [] };
}

/**
 * GET /api/intercept/history — 전송 통제(E²)가 개입한 요청 기록.
 * 허용까지 다 남기는 원장이 아니라, 룰/판정으로 **차단**했거나 승인 대기로 잡힌 이벤트다.
 * (tool_input 원문은 민감값을 품을 수 있어 의도적으로 안 받는다.)
 * ※ 변조탐지 해시체인·권한분리는 우리 기여 — 아직 이 레코드에 없다("준비 중").
 */
export type EngineControlEvent = {
  id: number;
  /** unix epoch(ms) — decided_at 우선, 없으면 created_at */
  ts: number;
  taskId: string;
  /** 요청한 에이전트 (예: work#1 · #256) */
  agent: string;
  tool: string;
  /** denied | approved | pending | … (이 데이터셋은 대개 denied) */
  status: string;
  /** 누가 정했나: rule | judge | user */
  source: string;
  reason: string;
  ruleName: string;
};

type InterceptHistoryRaw = {
  id: number;
  task_id?: number | string;
  agent_name?: string;
  tool_name?: string;
  status?: string;
  decision_source?: string;
  reason?: string;
  rule_name?: string;
  decided_at?: string;
  created_at?: string;
};

// TODO(이경준): 서버가 history task 필터를 지원하면 ?task= 로 좁히기(지금은 전역 받아 클라에서 task_id 필터).
export async function getControlLog(taskId: string): Promise<EngineControlEvent[]> {
  if (!taskId) return [];
  const res = await apiFetch<{ items?: InterceptHistoryRaw[]; total?: number }>("/api/intercept/history?limit=500");
  const items = res.items ?? [];
  return items
    .filter((r) => String(r.task_id ?? "") === taskId)
    .map((r) => ({
      id: r.id,
      ts: Date.parse(r.decided_at || r.created_at || "") || 0,
      taskId: String(r.task_id ?? ""),
      agent: r.agent_name ?? "",
      tool: r.tool_name ?? "",
      status: r.status ?? "",
      source: r.decision_source ?? "",
      reason: r.reason ?? "",
      ruleName: r.rule_name ?? "",
    }));
}

/**
 * GET /api/exploration/activity?task={id} — 에이전트 활동 타임라인.
 * 사람이 읽는 summary 만 받는다(원문 metadata·payload 는 민감값 가능 → 제외).
 * seq 오름차순 = 시간순(초기 목표 분해 → 계획 → 도구 호출 → 결과 …).
 */
export type EngineActivity = {
  seq: number;
  worker: string;
  /** unix epoch(ms) */
  ts: number;
  /** round | text | result | tool | observation | … */
  kind: string;
  isError: boolean;
  summary: string;
};

type ActivityRaw = {
  seq: number;
  worker?: string;
  ts?: string;
  kind?: string;
  is_error?: boolean;
  summary?: string;
};

// TODO(이경준): cursor 페이지네이션/stream(SSE) 연동. 지금은 앞에서 limit 건만.
export async function getActivity(taskId: string, limit = 300): Promise<EngineActivity[]> {
  if (!taskId) return [];
  const res = await apiFetch<{ items?: ActivityRaw[]; cursor?: string }>(
    `/api/exploration/activity?task=${encodeURIComponent(taskId)}&limit=${limit}`,
  );
  return (res.items ?? []).map((r) => ({
    seq: r.seq,
    worker: r.worker ?? "",
    ts: Date.parse(r.ts ?? "") || 0,
    kind: r.kind ?? "",
    isError: !!r.is_error,
    summary: r.summary ?? "",
  }));
}

/**
 * GET /api/llm 응답 — 현재 LLM 설정(전역, task 무관).
 * ※ key_set 은 "키가 설정됐는지" 불리언일 뿐 키 값이 아니다(값은 서버가 안 준다).
 */
export type EngineLLM = {
  provider: string;
  model: string;
  base_url: string;
  configured: boolean;
  key_set: boolean;
  context_window_k: number;
};

/** GET /api/llm — 현재 모델 설정 */
export async function getLLM(): Promise<EngineLLM> {
  return apiFetch<EngineLLM>("/api/llm");
}

/** GET /api/intercept/rules 의 규칙 1건 — E² 전송 통제 규칙(전역) */
export type EngineInterceptRule = {
  id: number;
  name: string;
  enabled: boolean;
  priority: number;
  /** tool_input | url | … */
  match_target: string;
  /** regex | … */
  match_type: string;
  pattern: string;
  /** deny | allow */
  action: string;
  message?: string;
};

/** GET /api/intercept/rules — 활성 통제 규칙 목록(전역, bare {rules}) */
export async function getInterceptRules(): Promise<EngineInterceptRule[]> {
  const res = await apiFetch<{ rules?: EngineInterceptRule[] }>("/api/intercept/rules");
  return res.rules ?? [];
}
