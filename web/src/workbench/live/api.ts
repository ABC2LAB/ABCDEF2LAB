// 실제 엔진 API 호출 (live 모드). 팀 lib(api·engine·request-approvals·remediation)을 그대로 쓰고,
// 없는 것만 apiFetch 로 부른다. 응답 모양은 origin/dev 의 Go 핸들러(server/*.go)에서 확인했다.
// - 네트워크 실패(fetch TypeError)는 ApiError(status 0) 로 바꿔 올린다 → isOffline · errorText 로 판별.
// - 원문 비밀이 들어올 수 있는 칸(트래픽 원문 · 도구 입력 · 파라미터 값 · 요청 미리보기)은 여기서 maskSecrets 를 거친다.
// - 서비스 자산의 auth[] · extra · LLM proxy · API 키 힌트처럼 화면에 필요 없는 민감 칸은 받아도 버린다.
// - 토큰은 URL·쿼리에 넣지 않는다(apiFetch 가 Authorization 헤더로만 보낸다).

import { ApiError, apiFetch, apiFetchBlob } from "@/lib/diagnosis/api";
import * as engine from "@/lib/diagnosis/engine";
import { type RemediationData, readRemediation } from "@/lib/diagnosis/remediation";
import * as ra from "@/lib/diagnosis/request-approvals";

import { maskJson, maskParam, maskSecrets } from "./mask";

// ---------- 공통 ----------

/** 네트워크 실패(TypeError 등)를 ApiError(0) 로 바꾼다. 중단(AbortError)·ApiError 는 그대로. */
function toApiError(err: unknown): unknown {
  if (err instanceof ApiError) return err;
  if (err instanceof DOMException && err.name === "AbortError") return err;
  if (err instanceof TypeError) return new ApiError(0, "엔진에 연결하지 못했습니다.");
  return err;
}

/** 호출 감싸기: 실패를 toApiError 로 정리해서 다시 던진다. */
async function call<T>(run: () => Promise<T>): Promise<T> {
  try {
    return await run();
  } catch (err) {
    throw toApiError(err);
  }
}

const enc = encodeURIComponent;
const num = (v: unknown, d = 0): number => (typeof v === "number" && Number.isFinite(v) ? v : d);
const str = (v: unknown): string => (typeof v === "string" ? v : "");
const arr = <T>(v: T[] | null | undefined): T[] => (Array.isArray(v) ? v : []);

/** BaseURL 의 user:pass@ 를 지운다 (화면에 자격 정보가 섞여 나오지 않게). */
function stripUserinfo(url: string): string {
  return url.replace(/^([a-z][a-z0-9+.-]*:\/\/)[^/@\s]*@/i, "$1");
}

// ---------- 점검(task) ----------

/** server/dto.go TaskStatus. 그 밖의 값이 올 수 있어 string 도 받는다. */
export type TaskStatus = "created" | "queued" | "running" | "paused" | "done" | "failed" | "timeout";

/** 심각도별 발견 수 (server FindingSeverityDTO). */
export type FindingSeverity = engine.FindingSeverity;

/** 점검 전체 토큰 합계 (server TokenTotalDTO). */
export interface TaskTokens {
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

/**
 * server/dto.go TaskDTO (작업대가 쓰는 칸).
 * - 대상 호스트 칸이 없다 → `hostOf(task)` 로 description·goal·name 의 첫 URL 에서 뽑는다.
 * - tokens · goals_* · in_flight · findings · last_activity_unix 는 **목록(fetchTasks) 응답에서만** 채워진다.
 */
export interface LiveTask {
  id: string;
  name: string;
  description: string;
  goal: string;
  /** TaskStatus 중 하나 (모르는 값이 와도 그대로 둔다 → taskPhase 가 원문 표시) */
  status: string;
  /** RFC3339 */
  created_at: string;
  /** unix 초 */
  created_unix: number;
  /** RFC3339, 안 끝났으면 "" */
  completed_at: string;
  /** unix 초, 안 끝났으면 0 */
  completed_unix: number;
  /** 마지막 활동 unix 초 (없으면 0, 목록에서만) */
  last_activity_unix: number;
  paused: boolean;
  queued: boolean;
  tokens: TaskTokens;
  goals_total: number;
  goals_met: number;
  /** 지금 도는 worker(의도) 수 */
  in_flight: number;
  findings: FindingSeverity;
  /** 점검에 지정한 LLM 프로필 id (숫자) */
  llm_profile_ids: number[];
  active_llm_profile_id: number | null;
  llm_failover_state: string;
  coverage_enabled: boolean;
  /** 읽기 전용으로 이어받은 원본 점검 id */
  source_task_ids: string[];
}

/** GET /api/tasks 응답. active = 엔진이 지금 활성으로 둔 task id ("" 가능). */
export interface LiveTaskList {
  active: string;
  tasks: LiveTask[];
}

type RawTask = Partial<LiveTask> & {
  tokens?: Partial<TaskTokens> | null;
  findings?: Partial<FindingSeverity> | null;
  llm_profile_ids?: number[] | null;
  source_task_ids?: string[] | null;
};

function normTask(r: RawTask): LiveTask {
  return {
    id: String(r.id ?? ""),
    name: str(r.name),
    description: str(r.description),
    goal: str(r.goal),
    status: str(r.status) || "created",
    created_at: str(r.created_at),
    created_unix: num(r.created_unix),
    completed_at: str(r.completed_at),
    completed_unix: num(r.completed_unix),
    last_activity_unix: num(r.last_activity_unix),
    paused: !!r.paused,
    queued: !!r.queued,
    tokens: {
      input_tokens: num(r.tokens?.input_tokens),
      output_tokens: num(r.tokens?.output_tokens),
      cache_read_tokens: num(r.tokens?.cache_read_tokens),
      cache_write_tokens: num(r.tokens?.cache_write_tokens),
    },
    goals_total: num(r.goals_total),
    goals_met: num(r.goals_met),
    in_flight: num(r.in_flight),
    findings: {
      critical: num(r.findings?.critical),
      high: num(r.findings?.high),
      medium: num(r.findings?.medium),
      low: num(r.findings?.low),
    },
    llm_profile_ids: arr(r.llm_profile_ids),
    active_llm_profile_id: typeof r.active_llm_profile_id === "number" ? r.active_llm_profile_id : null,
    llm_failover_state: str(r.llm_failover_state),
    coverage_enabled: !!r.coverage_enabled,
    source_task_ids: arr(r.source_task_ids).map(String),
  };
}

/** GET /api/tasks — 점검 목록(엔진 메모리 순서) + 활성 id. 숫자 칸(발견·토큰·목표)은 이 응답이 정본. 첫 진단 모드에서도 됨. */
export function fetchTasks(): Promise<LiveTaskList> {
  return call(async () => {
    const r = await apiFetch<{ active?: string; tasks?: RawTask[] | null }>("/api/tasks");
    return { active: str(r?.active), tasks: arr(r?.tasks).map(normTask) };
  });
}

/**
 * GET /api/tasks/{id} — 점검 하나. **findings · tokens · goals_* · in_flight · last_activity_unix 는 항상 0** 이다
 * (서버가 목록에서만 채움) → 숫자는 fetchTasks 의 같은 id 줄에서 읽을 것. 없는 id 는 ApiError 404.
 */
export function fetchTask(id: string): Promise<LiveTask> {
  return call(async () => normTask(await apiFetch<RawTask>(`/api/tasks/${enc(id)}`)));
}

/** 작업 수준 자산 통제 규칙 (server taskInterceptRuleReq). fuzzy_url 은 URL 에 pattern 이 들어 있으면(대소문자 무시) 걸린다. */
export interface InterceptRuleInput {
  enabled: boolean;
  /** block = 이 자산은 점검 금지, allow = 허용 목록 */
  action: "block" | "allow";
  kind: "exact_domain" | "exact_ip" | "exact_url" | "fuzzy_domain" | "fuzzy_ip" | "fuzzy_url" | "cidr";
  pattern: string;
  note: string;
}

/**
 * 새 점검 입력 (POST /api/tasks 의 createTaskReq). 대상은 description·goal 안의 URL 로 잡힌다.
 * llm_profile_ids 는 프로필 DTO 의 문자열 id 를 그대로 넣으면 숫자로 바꿔 보낸다(서버는 int64 배열).
 */
export interface CreateTaskInput {
  /** 점검 이름 (선택, 빈 값 = 이름 없음) */
  name?: string;
  /** 설명. 비우면 서버가 "이름 없는 작업" 으로 채운다. 대상 URL 을 넣을 것. */
  description: string;
  /** 목표 문장 */
  goal: string;
  /** 순서 있는 LLM 프로필 체인 (첫 항목이 먼저). 비우면 엔진의 활성 프로필. */
  llm_profile_ids?: readonly (string | number)[];
  /** 커버리지 계산 (생략 = 서버 기본 켜짐) */
  coverage_enabled?: boolean;
  /** 점검 제한 시간(초), 0 = 무제한 */
  timeout_seconds?: number;
  /** 작업 수준 자산 통제 규칙 (제외 경로 → `excludePathRules`) */
  intercept_rules?: readonly InterceptRuleInput[];
}

/**
 * 제외 경로 → block · fuzzy_url 규칙. 끝의 `*` 는 지운다(부분 일치라 필요 없음).
 * 주의: 이 규칙은 엔진의 **자산 게이트**(의도 등록·자산 추가 때 검사)다. 개별 HTTP 요청을 프록시에서 막는 장치가 아니다.
 */
export function excludePathRules(paths: readonly string[], note = "작업대 제외 경로"): InterceptRuleInput[] {
  const seen = new Set<string>();
  const out: InterceptRuleInput[] = [];
  for (const raw of paths) {
    const pattern = raw.trim().replace(/\*+$/, "");
    if (!pattern || seen.has(pattern)) continue;
    seen.add(pattern);
    out.push({ enabled: true, action: "block", kind: "fuzzy_url", pattern, note });
  }
  return out;
}

/**
 * POST /api/tasks — 점검 만들기. **만들자마자 시작한다**(범위 확인 단계 없음). 응답은 만들어진 task(201, 숫자 칸은 0).
 * 일반 모드 전용(첫 진단 모드는 403). 잘못된 프로필 id 는 ApiError 400.
 */
export function createTask(input: CreateTaskInput): Promise<LiveTask> {
  return call(async () => {
    const ids = (input.llm_profile_ids ?? []).map((v) => Number(v));
    if (ids.some((n) => !Number.isSafeInteger(n) || n <= 0))
      throw new ApiError(400, "LLM 프로필 id 가 올바르지 않습니다.");
    const body: Record<string, unknown> = { description: input.description, goal: input.goal };
    if (input.name?.trim()) body.name = input.name.trim();
    if (ids.length) body.llm_profile_ids = ids;
    if (input.coverage_enabled !== undefined) body.coverage_enabled = input.coverage_enabled;
    if (input.timeout_seconds) body.timeout_seconds = Math.max(0, Math.floor(input.timeout_seconds));
    if (input.intercept_rules?.length) body.intercept_rules = input.intercept_rules;
    return normTask(await apiFetch<RawTask>("/api/tasks", { method: "POST", body: JSON.stringify(body) }));
  });
}

/** POST /api/tasks/{id}/control 응답 (server taskControlResult). */
export interface TaskControlResult {
  id: string;
  paused: boolean;
  queued: boolean;
  status: string;
}

/**
 * 일시정지/다시 시작. **종료(stop) API 는 없다.** 끝난 점검·이미 멈춘 점검이면 ApiError 409, 없는 id 는 404.
 * 일반 모드 전용(첫 진단 모드 403).
 */
export function controlTask(id: string, action: "pause" | "resume"): Promise<TaskControlResult> {
  return call(async () => {
    const r = await apiFetch<Partial<TaskControlResult>>(`/api/tasks/${enc(id)}/control`, {
      method: "POST",
      body: JSON.stringify({ action }),
    });
    return { id: String(r?.id ?? id), paused: !!r?.paused, queued: !!r?.queued, status: str(r?.status) };
  });
}

// ---------- 발견 ----------

/** 발견이 걸린 자산 (label 예: "GET https://x/api/orders/1"). */
export type FindingAsset = engine.EngineFindingAsset;

/**
 * server/dto.go FindingDTO (목록).
 * - **판정(확정/의심/판단 불가) 칸이 없다.** status 는 처리 상태(워크플로) → `findingStatusLabel` 로 "처리 상태"로만 보인다.
 * - task 목록에서 id 는 발견 **노드** id, finding_id 는 독립 발견 표 id(없을 수 있음). 상세 조회(fetchFinding)는 finding_id 로.
 * - 전체 목록(fetchFindings)에서는 id = finding_id.
 * - summary · evidence 는 서버 원문이다 → 요청·응답 조각이 섞일 수 있으니 보일 때 maskSecrets 를 거칠 것.
 */
export interface LiveFinding {
  id: string;
  finding_id: string;
  vulnclass: string;
  /** 없으면 화면은 vulnclass 로 */
  name: string;
  /** critical | high | medium | low (그 밖의 값도 올 수 있음 → sevKey 가 null) */
  severity: string;
  /** pending | in_progress | confirmed | resolved | fixed | false_positive | ignored | duplicate | risk_accepted */
  status: string;
  summary: string;
  evidence: string;
  traffic_count: number;
  assets: FindingAsset[];
  task_id: string;
  /** 이어받은 원본 점검의 발견이면 그 task id */
  source_task_id: string;
  inherited: boolean;
  /** RFC3339 (만든 시각) */
  ts: string;
}

/** GET /api/exploration/findings/{finding_id} — 상세: + report(Markdown 원문) · evidence_version · report_stale. */
export interface LiveFindingDetail extends LiveFinding {
  /** Markdown 원문 (렌더링하지 말고 텍스트로) */
  report: string;
  evidence_version: number;
  report_stale: boolean;
}

type RawFinding = Partial<LiveFindingDetail> & { assets?: FindingAsset[] | null };

function normFinding(r: RawFinding): LiveFindingDetail {
  return {
    id: String(r.id ?? ""),
    finding_id: String(r.finding_id ?? ""),
    vulnclass: str(r.vulnclass),
    name: str(r.name),
    severity: str(r.severity),
    status: str(r.status) || "pending",
    summary: str(r.summary),
    evidence: str(r.evidence),
    traffic_count: num(r.traffic_count),
    assets: arr(r.assets),
    task_id: str(r.task_id),
    source_task_id: str(r.source_task_id),
    inherited: !!r.inherited,
    ts: str(r.ts),
    report: str(r.report),
    evidence_version: num(r.evidence_version),
    report_stale: !!r.report_stale,
  };
}

/** GET /api/exploration/findings — 모든 점검의 발견(독립 표, 최신순 최대 500). 30일 집계는 ts 로 거른다. 첫 진단 모드에서도 됨. */
export function fetchFindings(): Promise<LiveFinding[]> {
  return call(async () => arr(await apiFetch<RawFinding[] | null>("/api/exploration/findings")).map(normFinding));
}

/** GET /api/exploration/findings?task= — 한 점검의 발견(최신순 최대 200, 이어받은 원본 점검 것 포함). 없는 task 는 []. */
export function fetchTaskFindings(taskId: string): Promise<LiveFinding[]> {
  return call(async () =>
    arr(await apiFetch<RawFinding[] | null>(`/api/exploration/findings?task=${enc(taskId)}`)).map(normFinding),
  );
}

/** GET /api/exploration/findings/{finding_id} — 발견 상세. 인자는 **finding_id** (task 목록의 id 아님). 없으면 404. */
export function fetchFinding(findingId: string): Promise<LiveFindingDetail> {
  return call(async () => normFinding(await apiFetch<RawFinding>(`/api/exploration/findings/${enc(findingId)}`)));
}

// ---------- 커버리지 ----------

/** 자산 유형별 전체/검사 수. */
export type CoverageByType = engine.EngineCoverageType;

/**
 * GET /api/tasks/{id}/coverage (camelCase). enabled=false 면 점검에서 커버리지를 껐다(나머지 0).
 * pct 는 0..1, 분모가 0 이면 **null**(팀 getCoverage 는 0 으로 바꾸므로 여기서 되살린다).
 */
export interface LiveCoverage {
  enabled: boolean;
  /** 0 이면 범위가 아직 안 잡힘 */
  scopeRows: number;
  denominator: number;
  tested: number;
  pct: number | null;
  byType: CoverageByType[];
}

/** 커버리지. 자산 저장소가 꺼졌으면 ApiError 503(→ isUnsupported). 첫 진단 모드에서도 됨. */
export function fetchCoverage(taskId: string): Promise<LiveCoverage> {
  return call(async () => {
    const c = await engine.getCoverage(taskId);
    if (!c) throw new ApiError(404, "task not found");
    return { ...c, pct: c.denominator > 0 ? c.pct : null };
  });
}

/** 커버리지 그래프 노드·간선 (팀 타입 그대로). */
export type CoverageGraph = engine.EngineCoverageGraph;
/** 그래프 노드 하나. kind: company | root_domain | subdomain | ip | service | app | endpoint. */
export type CoverageNode = engine.EngineCoverageNode;

/** GET /api/tasks/{id}/coverage-graph — 범위 안 자산 트리. 자산 저장소가 꺼졌으면 null. */
export function fetchCoverageGraph(taskId: string): Promise<CoverageGraph | null> {
  return call(() => engine.getCoverageGraph(taskId));
}

// ---------- 활동 · 의도 ----------

/**
 * server/dto.go ActivityDTO (화면에 필요한 칸만; metadata 는 버린다).
 * kind: text | tool_use | result | round | llm_switch | usage | user (thinking 은 서버가 빼고 준다).
 */
export interface LiveActivity {
  seq: number;
  /** 의도(intent) 노드 id, 없으면 "" */
  intent_id: string;
  worker: string;
  /** RFC3339 */
  ts: string;
  kind: string;
  tool: string;
  is_error: boolean;
  /** 사람이 읽는 요약(서버 원문) → 보일 때 maskSecrets */
  summary: string;
}

/** 활동 한 쪽: items 는 seq **오름차순**(오래된 것부터), cursor = 받은 것 중 가장 큰 seq(없으면 since). */
export interface ActivityPage {
  items: LiveActivity[];
  cursor: number;
}

/**
 * GET /api/exploration/activity?task=&since=&limit= — seq > since 인 활동을 **오래된 순으로** limit 건.
 * 최신 활동을 보려면 cursor 로 이어 받아야 한다 → 보통은 `useActivityTail` 을 쓸 것. 첫 진단 모드는 403.
 */
export function fetchActivity(taskId: string, limit = 300, since = 0): Promise<ActivityPage> {
  return call(async () => {
    const q = new URLSearchParams({ task: taskId, limit: String(limit) });
    if (since > 0) q.set("since", String(since));
    const r = await apiFetch<{ items?: Array<Partial<LiveActivity>> | null; cursor?: number }>(
      `/api/exploration/activity?${q}`,
    );
    const items = arr(r?.items).map((a) => ({
      seq: num(a.seq),
      intent_id: str(a.intent_id),
      worker: str(a.worker),
      ts: str(a.ts),
      kind: str(a.kind),
      tool: str(a.tool),
      is_error: !!a.is_error,
      summary: str(a.summary),
    }));
    return { items, cursor: Math.max(since, num(r?.cursor), ...items.map((a) => a.seq)) };
  });
}

/**
 * server/dto.go TaskNodeDTO (의도). payload 는 JSON **문자열** `{summary, asset_ids}` → `parseIntentSummary`.
 * state: open | running | done | blocked | exhausted | stopped | paused | deleted.
 */
export interface LiveIntent {
  id: string;
  type: string;
  payload: string;
  priority: number;
  state: string;
  origin: string;
  /** RFC3339 */
  ts: string;
  source_task_id: string;
  inherited: boolean;
}

/** GET /api/exploration/intents?task= — 의도 목록(**최신순**, 최대 limit ≤ 500). 없는 task 는 []. 첫 진단 모드는 403. */
export function fetchIntents(taskId: string, limit = 300): Promise<LiveIntent[]> {
  return call(async () => {
    const q = new URLSearchParams({ task: taskId, limit: String(Math.min(500, Math.max(1, limit))) });
    const r = await apiFetch<Array<Partial<LiveIntent>> | null>(`/api/exploration/intents?${q}`);
    return arr(r).map((n) => ({
      id: String(n.id ?? ""),
      type: str(n.type),
      payload: str(n.payload),
      priority: num(n.priority),
      state: str(n.state),
      origin: str(n.origin),
      ts: str(n.ts),
      source_task_id: str(n.source_task_id),
      inherited: !!n.inherited,
    }));
  });
}

// ---------- 자산(엔드포인트) ----------

/** 엔드포인트 파라미터 (db ParamItem). value 는 maskParam 을 거친 값. */
export interface EndpointParam {
  location: string;
  name: string;
  value: string;
  type: string;
}

/** db.Asset 중 endpoint 가 쓰는 칸만 (auth · extra 등은 버린다). url 은 템플릿({id})일 수 있다. */
export interface LiveEndpoint {
  id: number;
  /** 대문자 메서드, 모르면 "" */
  method: string;
  url: string;
  domain: string;
  root_domain: string;
  port: number | null;
  params: EndpointParam[];
  /** 서버 문자열 그대로 (보통 RFC3339) */
  last_seen: string;
  task_ids: number[];
  status_code: number | null;
  page_title: string;
  /** 자산을 넣은 출처 설명 (있으면) */
  task_source: string;
  task_source_summary: string;
}

/** fetchEndpoints 결과. truncated = 서버 전체(total)가 max 보다 많아 일부만 받음. */
export interface EndpointList {
  total: number;
  assets: LiveEndpoint[];
  truncated: boolean;
}

/** 서버의 한 번 최대 개수 (server maxAssetPageSize). */
const ASSET_PAGE = 200;

function normEndpoint(a: Record<string, unknown>): LiveEndpoint {
  const params = Array.isArray(a.params) ? (a.params as Array<Record<string, unknown>>) : [];
  return {
    id: num(a.id),
    method: str(a.method).toUpperCase(),
    url: str(a.url),
    domain: str(a.domain),
    root_domain: str(a.root_domain),
    port: typeof a.port === "number" ? a.port : null,
    params: params.map((p) => {
      const name = str(p.name);
      return { location: str(p.location), name, value: maskParam(name, str(p.value)), type: str(p.type) };
    }),
    last_seen: str(a.last_seen),
    task_ids: Array.isArray(a.task_ids) ? (a.task_ids as unknown[]).map((v) => num(v)) : [],
    status_code: typeof a.status_code === "number" ? a.status_code : null,
    page_title: str(a.page_title),
    task_source: str(a.task_source),
    task_source_summary: str(a.task_source_summary),
  };
}

/**
 * GET /api/assets?task_id=&type=endpoint — 점검의 엔드포인트. 서버는 한 번에 최대 200 건이라 offset 으로 이어 받아 max(기본 500)까지.
 * endpoint 만 부른다(서비스 자산의 auth[] 에 평문 비밀이 있을 수 있음). 저장소 꺼짐은 ApiError 503. 첫 진단 모드는 403.
 */
export function fetchEndpoints(taskId: string, max = 500): Promise<EndpointList> {
  return call(async () => {
    const assets: LiveEndpoint[] = [];
    let total = 0;
    for (let offset = 0; offset < max; offset += ASSET_PAGE) {
      const limit = Math.min(ASSET_PAGE, max - offset);
      const q = new URLSearchParams({
        task_id: taskId,
        type: "endpoint",
        limit: String(limit),
        offset: String(offset),
      });
      const r = await apiFetch<{ total?: number; assets?: Array<Record<string, unknown>> | null }>(`/api/assets?${q}`);
      const page = arr(r?.assets);
      total = num(r?.total, total);
      assets.push(...page.map(normEndpoint));
      if (page.length < limit || assets.length >= total) break;
    }
    return { total, assets, truncated: total > assets.length };
  });
}

/** 자산에 걸린 의도·사실·발견 한 줄 (server CoverageAssetRefDTO). summary 는 서버 원문 → maskSecrets. */
export interface AssetRef {
  id: number;
  kind: string;
  state: string;
  summary: string;
  source_task_id: string;
  inherited: boolean;
}

/** GET /api/tasks/{id}/asset-refs 응답. */
export interface AssetRefs {
  intents: AssetRef[];
  facts: AssetRef[];
  findings: AssetRef[];
}

/** GET /api/tasks/{id}/asset-refs?asset_id= — 이 자산에 걸린 의도·사실·발견. 첫 진단 모드에서도 됨. */
export function fetchAssetRefs(taskId: string, assetId: number | string): Promise<AssetRefs> {
  return call(async () => {
    const r = await apiFetch<Partial<Record<keyof AssetRefs, Array<Partial<AssetRef>> | null>>>(
      `/api/tasks/${enc(taskId)}/asset-refs?asset_id=${enc(String(assetId))}`,
    );
    const norm = (xs: Array<Partial<AssetRef>> | null | undefined): AssetRef[] =>
      arr(xs).map((x) => ({
        id: num(x.id),
        kind: str(x.kind),
        state: str(x.state),
        summary: str(x.summary),
        source_task_id: str(x.source_task_id),
        inherited: !!x.inherited,
      }));
    return { intents: norm(r?.intents), facts: norm(r?.facts), findings: norm(r?.findings) };
  });
}

// ---------- 트래픽 ----------

/** 트래픽 목록 한 줄 (server TrafficExchangeDTO). url 의 쿼리에 비밀이 있을 수 있어 보일 때 maskSecrets. */
export interface TrafficRow {
  id: string;
  /** RFC3339 */
  ts: string;
  host: string;
  method: string;
  url: string;
  status: number;
  content_type: string;
  resp_len: number;
}

/** GET /api/traffic 응답. enabled=false 면 기록이 꺼져 있다(기본). 작업 구분이 없다(전역) → host 로 거른다. */
export interface TrafficPage {
  enabled: boolean;
  /** 기록 프록시 주소 ("" 가능) */
  proxy: string;
  /** 전체 기록 수(거르기 전) */
  count: number;
  /** 거른 결과 수 */
  total: number;
  page: number;
  size: number;
  exchanges: TrafficRow[];
}

/** fetchTraffic 조건. host · path 는 부분 일치, page 는 0 부터, size ≤ 500 (그 이상은 서버가 100 으로 바꾼다). */
export interface TrafficQuery {
  host?: string;
  method?: string;
  path?: string;
  page?: number;
  size?: number;
}

/** GET /api/traffic — 기록한 요청 목록(전역). 트래픽 저장소가 없으면 enabled:false · 빈 목록. 첫 진단 모드는 403. */
export function fetchTraffic(params: TrafficQuery = {}): Promise<TrafficPage> {
  return call(async () => {
    const q = new URLSearchParams();
    if (params.host) q.set("host", params.host);
    if (params.method) q.set("method", params.method);
    if (params.path) q.set("path", params.path);
    q.set("page", String(Math.max(0, params.page ?? 0)));
    q.set("size", String(Math.min(500, Math.max(1, params.size ?? 100))));
    const r = await apiFetch<Partial<TrafficPage> & { exchanges?: Array<Partial<TrafficRow>> | null }>(
      `/api/traffic?${q}`,
    );
    const exchanges = arr(r?.exchanges).map((e) => ({
      id: str(e.id),
      ts: str(e.ts),
      host: str(e.host),
      method: str(e.method),
      url: str(e.url),
      status: num(e.status),
      content_type: str(e.content_type),
      resp_len: num(e.resp_len),
    }));
    return {
      enabled: !!r?.enabled,
      proxy: str(r?.proxy),
      count: num(r?.count),
      total: num(r?.total, exchanges.length),
      page: num(r?.page),
      size: num(r?.size, exchanges.length),
      exchanges,
    };
  });
}

/** 요청·응답 원문 한 쌍. **이미 maskSecrets 를 거친 글**이다(서버 원문은 가림 없음). */
export interface TrafficExchangeMasked {
  reqMasked: string;
  respMasked: string;
}

/** GET /api/traffic/exchange?id= — 요청·응답 원문을 받아 가린 뒤 돌려준다. 기록 꺼짐·없는 id 는 404. */
export function fetchTrafficExchange(id: string): Promise<TrafficExchangeMasked> {
  return call(async () => {
    const r = await apiFetch<{ req?: string; resp?: string }>(`/api/traffic/exchange?id=${enc(id)}`);
    return { reqMasked: maskSecrets(str(r?.req)), respMasked: maskSecrets(str(r?.resp)) };
  });
}

// ---------- LLM · 토큰 ----------

/** GET /api/llm 중 화면에 쓰는 칸 (proxy · 키 값은 받지 않는다). 로컬 여부 플래그는 없다 → isLocalBaseUrl(추정). */
export interface LiveLlm {
  configured: boolean;
  provider: string;
  model: string;
  /** user:pass@ 는 지운 값 */
  base_url: string;
  /** 키가 설정됐는지 (값 아님) */
  key_set: boolean;
  context_window_k: number;
}

/** GET /api/llm — 엔진 기본 LLM 설정(전역). 점검마다 다른 프로필을 쓸 수 있다(task.llm_profile_ids). 첫 진단 모드는 403. */
export function fetchLlm(): Promise<LiveLlm> {
  return call(async () => {
    const r = await engine.getLLM();
    return {
      configured: !!r?.configured,
      provider: str(r?.provider),
      model: str(r?.model),
      base_url: stripUserinfo(str(r?.base_url)),
      key_set: !!r?.key_set,
      context_window_k: num(r?.context_window_k),
    };
  });
}

/** LLM 프로필 (server LLMProfileDTO 중 쓰는 칸). id 는 문자열 — createTask 에 그대로 넘기면 숫자로 바꾼다. */
export interface LlmProfile {
  id: string;
  name: string;
  /** openai | anthropic | … */
  format: string;
  /** user:pass@ 는 지운 값 */
  base_url: string;
  model: string;
  is_default: boolean;
  priority: number;
}

/** GET /api/llm/profiles — 저장된 프로필 목록. DB 가 없으면 503. 첫 진단 모드는 403. */
export function fetchLlmProfiles(): Promise<LlmProfile[]> {
  return call(async () => {
    const r = await apiFetch<{ profiles?: Array<Record<string, unknown>> | null }>("/api/llm/profiles");
    return arr(r?.profiles).map((p) => ({
      id: String(p.id ?? ""),
      name: str(p.name),
      format: str(p.format),
      base_url: stripUserinfo(str(p.base_url)),
      model: str(p.model),
      is_default: !!p.is_default,
      priority: num(p.priority),
    }));
  });
}

/** 프로필별 누적 (팀 타입). **기간과 무관한 전체 누적**이다. */
export type TokenProfile = engine.TokenProfile;
/** UTC 날짜별 합계 (팀 타입). */
export type TokenDaily = engine.TokenDaily;

/** fetchTokenUsage 결과. total 은 daily(최근 days 일)의 합. byProfile 은 전체 기간 누적(서버가 days 를 안 거름). */
export interface TokenUsage {
  days: number;
  byProfile: TokenProfile[];
  daily: TokenDaily[];
  total: { input: number; output: number; cacheRead: number };
}

/**
 * GET /api/tokens/usage?days= — 최근 days 일(UTC) 날짜별 토큰과 프로필별 누적(전역, 점검 구분 없음).
 * "점검당 평균"은 엔진이 주지 않는다 → 목록(fetchTasks)의 task.tokens 로 계산할 것. 첫 진단 모드는 403.
 */
export function fetchTokenUsage(days = 30): Promise<TokenUsage> {
  return call(async () => {
    type RawP = Partial<{
      profile_name: string;
      calls: number;
      tasks: number;
      input_tokens: number;
      output_tokens: number;
      cache_read_tokens: number;
      cache_write_tokens: number;
    }>;
    type RawD = Partial<{ date: string; input_tokens: number; output_tokens: number; cache_read_tokens: number }>;
    const d = Math.max(1, Math.floor(days));
    const r = await apiFetch<{ by_profile?: RawP[] | null; daily?: RawD[] | null }>(`/api/tokens/usage?days=${d}`);
    const byDay = new Map<string, TokenDaily>();
    for (const row of arr(r?.daily)) {
      const day = str(row.date);
      const cur = byDay.get(day) ?? { day, input: 0, output: 0, cacheRead: 0 };
      cur.input += num(row.input_tokens);
      cur.output += num(row.output_tokens);
      cur.cacheRead += num(row.cache_read_tokens);
      byDay.set(day, cur);
    }
    const daily = [...byDay.values()].sort((a, b) => a.day.localeCompare(b.day));
    const total = daily.reduce(
      (t, x) => ({ input: t.input + x.input, output: t.output + x.output, cacheRead: t.cacheRead + x.cacheRead }),
      { input: 0, output: 0, cacheRead: 0 },
    );
    return {
      days: d,
      byProfile: arr(r?.by_profile).map((p) => ({
        profileName: str(p.profile_name) || "(미지정)",
        calls: num(p.calls),
        tasks: num(p.tasks),
        input: num(p.input_tokens),
        output: num(p.output_tokens),
        cacheRead: num(p.cache_read_tokens),
        cacheWrite: num(p.cache_write_tokens),
      })),
      daily,
      total,
    };
  });
}

// ---------- 도구 호출 승인 (intercept, 일반 모드) ----------

/** 승인 대기 한 건 (도구 입력 원문은 빼고). id 는 숫자. */
export interface PendingApproval {
  id: number;
  /** 어느 점검의 요청인지 (없으면 null) */
  task_id: string | null;
  rule_id: number | null;
  agent_name: string;
  tool_name: string;
  /** pending | allowed | denied | … */
  status: string;
  /** RFC3339 */
  created_at: string;
}

/** 승인 상세: + 가린 도구 입력 · 걸린 규칙/판정 이유. */
export interface PendingApprovalDetail extends PendingApproval {
  /** tool_input(JSON) 을 보기 좋게 풀어 maskSecrets 를 거친 글 */
  toolInputMasked: string;
  /** 규칙 메시지 또는 모델 판정 이유 (서버 원문 → maskSecrets 거침) */
  reason: string;
  decision_source: string;
  decided_at: string | null;
}

function normPending(p: Record<string, unknown>): PendingApproval {
  return {
    id: num(p.id),
    task_id: p.task_id === null || p.task_id === undefined ? null : String(p.task_id),
    rule_id: typeof p.rule_id === "number" ? p.rule_id : null,
    agent_name: str(p.agent_name),
    tool_name: str(p.tool_name),
    status: str(p.status),
    created_at: str(p.created_at),
  };
}

/** GET /api/intercept/pending — 결정 대기 중인 도구 호출(모든 점검, 최신순). 첫 진단 모드는 403 → isUnsupported. */
export function fetchPendingApprovals(): Promise<PendingApproval[]> {
  return call(async () => {
    const list = (await engine.listPendingApprovals()) as unknown as Array<Record<string, unknown>>;
    return arr(list).map(normPending);
  });
}

/** GET /api/intercept/pending/{id} — 한 건 상세(이미 결정된 것도 상태와 함께). 도구 입력은 가려서 준다. */
export function fetchPendingApproval(id: number): Promise<PendingApprovalDetail> {
  return call(async () => {
    const r = (await engine.getPendingApproval(id)) as unknown as Record<string, unknown>;
    return {
      ...normPending(r),
      toolInputMasked: maskJson(r.tool_input),
      reason: maskSecrets(str(r.reason)),
      decision_source: str(r.decision_source),
      decided_at: typeof r.decided_at === "string" ? r.decided_at : null,
    };
  });
}

/** POST /api/intercept/pending/{id}/decide — allowed(이번 한 번 허용) | denied(거부). 이미 결정됐으면 ApiError 409. */
export function decidePendingApproval(id: number, decision: "allowed" | "denied"): Promise<void> {
  return call(() => engine.decidePendingApproval(id, decision));
}

// ---------- 요청별 승인 (request-approvals, 첫 진단 모드) ----------

/** 요청별 승인 한 건 (팀 타입). input_preview 값은 maskParam 을 거친 값. */
export type RequestApproval = ra.RequestApproval;
/** 실행 감독자 상태 (팀 타입). 전송 통제 집계는 run.execution.counts → approvalCounts. */
export type RequestApprovalRun = ra.RequestApprovalRunState;

/** GET /api/tasks/{id}/request-approvals 결과 (approvals 는 null 대신 []). */
export interface RequestApprovalState {
  approvals: RequestApproval[];
  run: RequestApprovalRun;
}

/**
 * 요청별 승인 상태 (첫 진단 모드 전용). 일반 모드는 503, 이 점검에 실행 결속이 없으면 403 → isUnsupported.
 * 결정 POST 는 decideRequestApproval.
 */
export function fetchRequestApprovals(taskId: string): Promise<RequestApprovalState> {
  return call(async () => {
    const r = await ra.readRequestApprovalState(taskId);
    const approvals = arr(r?.approvals).map((a) => ({
      ...a,
      input_preview: Object.fromEntries(
        Object.entries(a.input_preview ?? {}).map(([k, v]) => [k, maskParam(k, String(v))]),
      ),
    }));
    return { approvals, run: r.run };
  });
}

/**
 * POST …/request-approvals/{id}/decision — allow | deny. request_sha256 가 맞아야 한다.
 * 인증이 끊겨도 자동 재시도하지 않는다(팀 함수 그대로). 이미 결정·만료면 409, 권한 없음 403.
 */
export function decideRequestApproval(
  taskId: string,
  approval: Pick<RequestApproval, "id" | "request_sha256">,
  decision: "allow" | "deny",
): Promise<RequestApproval> {
  return call(() => ra.decideRequestApproval(taskId, approval, decision));
}

/** 저장된 실행 종료 기록의 표시용 판정 (팀 타입: none · not_run · unavailable · run). */
export type DiagnosisRunView = ra.DiagnosisRunDisplay;

/**
 * GET /api/tasks/{id}/diagnosis-run — 저장된 실행 종료 기록(팀 readDiagnosisRunDisplay).
 * 저장 없음(404)은 none, 다른 작업(403)은 unavailable 로 흡수하고 완료로 바꾸지 않는다. 그 밖의 실패는 오류.
 */
export function fetchDiagnosisRun(taskId: string): Promise<DiagnosisRunView> {
  return call(() => ra.readDiagnosisRunDisplay(taskId));
}

// ---------- 보고서 ----------

/** 조치 자료 (팀 타입). */
export type Remediation = RemediationData;

/**
 * GET /api/report/diagnostic/remediation?task=&audience= — 우선 조치 자료.
 * audience="shared" 면 제목·요약·조치가 서버에서 `[REDACTED]` 로 가려지고 bundle 이 없다 → 화면은 기본 "internal".
 * 첫 진단 모드(결속된 task)에서만 된다. 일반 모드는 보통 403/404/422.
 */
export function fetchRemediation(taskId: string, audience: "internal" | "shared" = "internal"): Promise<Remediation> {
  return call(async () => {
    const r = await readRemediation(taskId, audience);
    return { task_id: num(r?.task_id), entries: arr(r?.entries) };
  });
}

/**
 * GET /api/report/diagnostic?task=&audience=&download=1 — 종합 HTML 보고서를 **파일로 저장**한다.
 * apiFetchBlob 으로 받아 object URL + `<a download>` 로 내려받고, 그 페이지로 이동하거나 열지 않는다.
 * 첫 진단 모드(결속된 task) 전용. 실패는 ApiError(403/404/422/503 …).
 */
export function downloadDiagnostic(
  taskId: string,
  audience: "shared" | "internal" = "shared",
  signal?: AbortSignal,
): Promise<void> {
  return call(async () => {
    if (!/^[1-9][0-9]*$/.test(taskId)) throw new ApiError(400, "점검 id 가 올바르지 않습니다.");
    const q = new URLSearchParams({ task: taskId, audience, download: "1" });
    const blob = await apiFetchBlob(`/api/report/diagnostic?${q}`, { signal });
    if (signal?.aborted) return;
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `diagnostic-task-${taskId}-${audience}.html`;
    a.rel = "noopener";
    a.hidden = true;
    document.body.appendChild(a);
    try {
      a.click();
    } finally {
      a.remove();
      // 브라우저가 받을 시간을 두고 해제 (미리보기 탭은 열지 않는다)
      window.setTimeout(() => URL.revokeObjectURL(url), 1000);
    }
  });
}
