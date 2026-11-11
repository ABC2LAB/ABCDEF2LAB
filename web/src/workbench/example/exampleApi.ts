// 예시 데이터 모드: 실제 엔진 없이 화면을 만져 보기 위한 브라우저 안의 가짜 /api 응답.
// NEXT_PUBLIC_ABC2LAB_EXAMPLE=1 로 띄울 때만 켜진다(npm run dev:example). 켜지면 화면 위 띠에 항상
// "예시 데이터 · 실제 서버 아님" 을 표시한다. 실제 진단 · 실제 요청은 하나도 보내지 않는다.
// 응답 모양은 lib/diagnosis 의 엄격한 검사(owner · revision · 단계 순서 · 핸들)를 그대로 통과해야 한다.
// 상태는 sessionStorage 에 두어 새로고침해도 이어지고, 시간이 지나면 다음 단계로 넘어간다.

/** 빌드할 때 정해지는 예시 모드 스위치 (기본 꺼짐). */
export const EXAMPLE_MODE = process.env.NEXT_PUBLIC_ABC2LAB_EXAMPLE === "1";

const OWNER = "a".repeat(64);
const DIGEST = "c".repeat(64);
const STATE_KEY = "abc2lab.example.state.v1";
const HANDLE_KEY = "abc2lab.urlDiagnosisHandle.v2";
const PENDING_KEY = "abc2lab.example.pendingHandle.v1";
const TOKEN_KEY = "abc2lab.devToken";
const VERSION = "diagnosis-api.v2";
const FLOW_FORMAT = "abc2lab.workbench-flow/v1";
const JOB = "job-1";
const TASK = "42";
const RUN = `run-${JOB}`;

type Json = Record<string, unknown>;
type WorkflowMode = "user_approval_v1" | "ai_auto_v1";
interface Input {
  target_url: string;
  profile_id: string;
  display_name?: string;
  workflow_mode?: WorkflowMode;
  excluded_paths?: string;
}
type Stage = "crawling" | "awaiting_selection" | "diagnosing" | "completed" | "stopped";
type Approval = "none" | "pending" | "allowed" | "denied";
interface St {
  input: Input | null;
  job: Json | null;
  createKey: string;
  startedAt: number;
  flowRev: number;
  stage: Stage;
  /** 탐색 중 지금까지 보여 준 경로 수 */
  shown: number;
  selected: string[];
  selectedAt: number;
  paused: boolean;
  commands: { key: string; operation: string; revision: number }[];
  approval: Approval;
  decidedAt: number;
  doneAt: number;
}

const LIMITS = {
  format: FLOW_FORMAT,
  max_pages: 24,
  max_endpoints: 32,
  max_response_bytes: 4096,
  allow_reversible_writes: true,
};
const OPENROUTER_MODEL = {
  mode: "openrouter",
  model: "qwen/qwen3.5-35b-a3b",
  provider: "openrouter/alibaba",
  external_transfer: true,
  input_tokens_per_call: 262144,
  output_tokens_per_call: 2048,
};
const CATALOG = {
  version: VERSION,
  owner_context: OWNER,
  profiles: [
    {
      id: "local-qwen",
      label: "로컬 분석",
      model_profile_ref: "local-qwen",
      model_label: "Qwen3.5 35B (로컬)",
      workflow: LIMITS,
      workflow_request_limit: 40,
    },
    {
      id: "openrouter-qwen",
      label: "외부 분석",
      model_profile_ref: "openrouter-qwen",
      model_label: "Qwen3.5 35B (OpenRouter)",
      model: OPENROUTER_MODEL,
      workflow: LIMITS,
      workflow_request_limit: 40,
    },
  ],
};

const iso = (ms: number) => new Date(ms).toISOString();
const closedOutcome = (s: St) => (s.job?.state === "completed" ? "completed" : "cancelled");
/** 계획 한 줄의 상태: 끝남 → 진행 중 → 아직 */
function stepState(done: boolean, running: boolean) {
  if (done) return "done";
  return running ? "running" : "open";
}
function deepClosed(done: boolean, first: boolean) {
  if (done) return 2;
  return first ? 1 : 0;
}
function urlOf(input: RequestInfo | URL) {
  if (typeof input === "string") return input;
  return input instanceof URL ? input.href : input.url;
}
const originOf = (url: string) => new URL(url).origin;
const hostOf = (url: string) => new URL(url).host;

interface EpOpt {
  kind?: string;
  source?: string;
  in_scope?: boolean;
  change?: string;
  visited?: boolean;
  status?: number | null;
  req?: string;
  res?: string;
}
const ep = (id: string, method: string, path: string, o: EpOpt = {}) => ({
  id,
  method,
  path,
  from: "/",
  kind: o.kind ?? (path.startsWith("/api") || path.startsWith("/rest") ? "api" : "page"),
  source: o.source ?? "html_link",
  in_scope: o.in_scope ?? true,
  change: o.change ?? (method === "GET" ? "attested_read" : "possible"),
  visited: o.visited ?? method === "GET",
  ...(o.status === null || (o.status === undefined && method !== "GET") ? {} : { status: o.status ?? 200 }),
  selected: false,
  duplicates: 0,
  ...(o.req ? { request: o.req } : {}),
  ...(o.res ? { response: o.res } : {}),
});
/** 예시 사이트에서 찾는 경로 14개 (Juice Shop 비슷한 쇼핑몰) */
function endpoints(host: string) {
  return [
    ep("ep-1", "GET", "/", {
      source: "seed",
      req: `GET / HTTP/1.1\nHost: ${host}`,
      res: "HTTP/1.1 200 OK\nContent-Type: text/html",
    }),
    ep("ep-2", "GET", "/about"),
    ep("ep-3", "GET", "/products"),
    ep("ep-4", "GET", "/api/products/1", {
      req: `GET /api/products/1 HTTP/1.1\nHost: ${host}`,
      res: 'HTTP/1.1 200 OK\nContent-Type: application/json\n\n{"id":1,"name":"사과 주스","price":1.99}',
    }),
    ep("ep-5", "GET", "/rest/user/whoami", { status: 401 }),
    ep("ep-6", "POST", "/rest/user/login", { source: "form" }),
    ep("ep-7", "GET", "/api/orders/7", {
      status: 401,
      req: `GET /api/orders/7 HTTP/1.1\nHost: ${host}`,
      res: "HTTP/1.1 401 Unauthorized",
    }),
    ep("ep-8", "POST", "/api/BasketItems", { source: "script" }),
    ep("ep-9", "GET", "/rest/basket/3", { status: 401 }),
    ep("ep-10", "GET", "/administration"),
    ep("ep-11", "GET", "/rest/admin/application-configuration"),
    ep("ep-12", "PUT", "/api/Users/2", { source: "script" }),
    ep("ep-13", "GET", "/main.js", { kind: "script", source: "html_script" }),
    ep("ep-14", "DELETE", "/api/Products/1", { source: "script", change: "blocked", in_scope: false }),
  ];
}

function fresh(): St {
  return {
    input: null,
    job: null,
    createKey: "",
    startedAt: 0,
    flowRev: 0,
    stage: "crawling",
    shown: 0,
    selected: [],
    selectedAt: 0,
    paused: false,
    commands: [],
    approval: "none",
    decidedAt: 0,
    doneAt: 0,
  };
}
// 모듈을 읽기만 해서는 저장소를 건드리지 않는다(예시 모드가 꺼져 있으면 아무 일도 없음)
let state: St | null = null;
function st(): St {
  if (state) return state;
  try {
    const raw = sessionStorage.getItem(STATE_KEY);
    if (raw) {
      state = JSON.parse(raw) as St;
      return state;
    }
  } catch {
    // 새로 시작
  }
  state = fresh();
  return state;
}
function save() {
  try {
    sessionStorage.setItem(STATE_KEY, JSON.stringify(st()));
  } catch {
    // 이번 페이지에서만 유지
  }
}

function scopeFor(input: Input) {
  const local = input.profile_id !== "openrouter-qwen";
  return {
    target_ref: "target-1",
    scope_ref: "scope-1",
    origin: originOf(input.target_url),
    path_prefixes: ["/"],
    methods: ["GET", "POST", "PUT"],
    query_policy: "any",
    body_policy: "bounded",
    max_body_bytes: 4096,
    approval_mode: input.workflow_mode === "user_approval_v1" ? "workbench_selective_v1" : "preauthorized_scope",
    attestation_ref: "attestation-1",
    expires_at: iso(Date.now() + 4 * 3600_000).replace(/\.\d+Z$/, "Z"),
    budget: {
      requests: 40,
      model_requests: 30,
      input_tokens: 200000,
      output_tokens: 20000,
      cost_usd_micros: local ? 0 : 2_000_000,
      duration_seconds: 1800,
    },
    ...(local ? {} : { model: OPENROUTER_MODEL }),
  };
}
/** 진단 상태를 바꾸면 revision 을 하나 올린다(같은 revision 에 다른 값을 주지 않는다). */
function bump(patch: Json) {
  const s = st();
  const job = s.job as Json;
  s.job = { ...job, ...patch, revision: Number(job.revision) + 1 };
}
function usage() {
  return {
    requests: 21,
    input_tokens: 182000,
    output_tokens: 9400,
    reserved_input_tokens: 0,
    reserved_output_tokens: 0,
    cost_usd_micros: 0,
    usage_known: true,
  };
}

/** 데이터를 바꾸는 요청(PUT /api/Users/2) 하나에 대한 전송 전 승인 */
function approvalRow() {
  const s = st();
  const input = s.input as Input;
  const state = s.approval === "none" ? "pending" : s.approval;
  return {
    id: "ap-1",
    task_id: Number(TASK),
    run_ref: RUN,
    intent_id: 3,
    call_ref: "call-1",
    attempt_id: "attempt-1",
    policy_revision: "rev-1",
    account_ref: "account-a",
    session_ref: "session-a",
    request_sha256: "d".repeat(64),
    method: "PUT",
    target_origin: originOf(input.target_url),
    path: "/api/Users/2",
    input_preview: {
      "content-type": "application/json",
      body: `{"email":"user2@${hostOf(input.target_url)}","role":"customer"}`,
    },
    body_bytes: 64,
    created_at: iso(s.selectedAt + 3000),
    expires_at: iso(Date.now() + 3600_000),
    state,
    consumed: state !== "pending",
    execution_state: state === "allowed" ? "completed" : "not_sent",
    live: state === "pending",
    can_decide: state === "pending",
    ...(state !== "pending" ? { decided_by: "ABC2LAB", decided_at: iso(s.decidedAt) } : {}),
  };
}
function runState() {
  const s = st();
  const closed = ["completed", "cancelled"].includes(String(s.job?.state));
  const sent = s.stage === "diagnosing" || s.stage === "completed" ? 6 + (s.approval === "allowed" ? 1 : 0) : 0;
  return {
    run_ref: RUN,
    phase: closed ? "closed" : "running",
    outcome: closed ? closedOutcome(s) : "",
    stop_requested: s.job?.state === "cancelled",
    context_cancelled: false,
    drain_complete: closed,
    closure_failed: false,
    execution: {
      approval_mode: "workbench_selective_v1",
      header_mode: "strip",
      version: "e2.v1",
      scope: { RunRef: RUN, TaskRef: TASK, CaseRef: "case-1", ResetRef: "reset-1", PolicyRef: "policy-1" },
      intent_id: 3,
      intent_ids: [1, 2, 3],
      owner_ref: "owner-1",
      policy_revision: "rev-1",
      state: closed ? "blocked" : "active",
      expires_at: iso(Date.now() + 3600_000),
      reason: "",
      audit_complete: closed,
      counts: {
        attempts: sent + 2,
        allowed: sent,
        denied: s.approval === "denied" ? 1 : 0,
        unsupported: 0,
        forward_attempted: sent,
        responses: sent,
        unknown: 0,
        calls: sent + 4,
      },
    },
  };
}

interface EntryOpt {
  status?: string;
  verification?: string;
  summary?: string;
  evidence?: string;
  remediation?: string;
}
const entry = (intent: number, kind: string, outcome: string, title: string, o: EntryOpt = {}) => ({
  entry_id: `${kind}-${intent}`,
  title,
  outcome,
  status: o.status ?? "allowed",
  verification: o.verification ?? "stored_verification",
  evidence_digest: "f".repeat(64),
  report_version: "report.v1",
  run: RUN,
  intent_id: intent,
  observed_at: iso(st().selectedAt + 2000 * intent),
  evidence_version: 1,
  source_digest: "b".repeat(64),
  summary: o.summary ?? "",
  evidence: o.evidence ?? "",
  report: "",
  remediation: o.remediation ?? "",
  available: true,
  unavailable_reason: "",
});
function remediation() {
  const s = st();
  const origin = originOf((s.input as Input).target_url);
  return {
    task_id: Number(TASK),
    entries: [
      entry(1, "finding", "confirmed", "비로그인 상태에서 관리자 설정 API가 열림", {
        summary: "로그인하지 않은 요청에 /rest/admin/application-configuration 이 200 과 설정 본문을 돌려줌",
        evidence: JSON.stringify({
          tool_output: JSON.stringify({
            status: 200,
            url: `${origin}/rest/admin/application-configuration`,
            headers: { "content-type": "application/json" },
            body: '{"config":{"server":{"port":3000}}}',
          }),
        }),
        remediation: "관리자 API 앞에 인증 · 관리자 권한 검사를 두고 비로그인 요청은 401 로 막는다.",
      }),
      entry(2, "candidate", "review", "다른 사용자의 주문 조회 가능성", {
        status: "completed",
        verification: "manual_review_required",
        summary: "GET /api/orders/7 이 401 을 돌려줘 비교 계정 없이 판단할 수 없음",
        evidence: JSON.stringify({ tool_output: JSON.stringify({ status: 401, url: `${origin}/api/orders/7` }) }),
      }),
      ...(s.selected.includes("ep-12")
        ? [
            entry(3, "candidate", s.approval === "allowed" ? "review" : "not_executed", "사용자 정보 수정 권한 확인", {
              status: s.approval === "allowed" ? "completed" : "policy_not_executed",
              verification: "manual_review_required",
              summary:
                s.approval === "allowed"
                  ? "PUT /api/Users/2 가 200 을 돌려줘 다른 계정 정보 수정 가능성 검토 필요"
                  : "PUT 요청이 승인 거부로 전송되지 않음",
            }),
          ]
        : []),
      entry(4, "verdict", "negative", "상품 정보 조회는 정상 응답", {
        summary: "GET /api/products/1 은 공개 상품 정보만 돌려줌",
      }),
    ],
  };
}

function planFor(diag: boolean, done: boolean, since: number, selected: Set<string>) {
  if (!diag) return undefined;
  const s = st();
  const blocked = s.approval === "pending" ? "blocked" : "open";
  return [
    {
      id: 1,
      state: done || since > 3000 ? "done" : "running",
      title: "다른 사용자 주문 조회 가능 여부 확인 (/api/orders/{id})",
    },
    {
      id: 2,
      state: stepState(done || since > 6000, since > 3000),
      title: "관리자 설정 API 비로그인 접근 확인",
    },
    ...(selected.has("ep-12")
      ? [{ id: 3, state: done ? "done" : blocked, title: "사용자 정보 수정 요청 권한 확인 (PUT /api/Users/{id})" }]
      : []),
  ];
}
function activityFor(diag: boolean, since: number) {
  if (!diag) return undefined;
  const s = st();
  const decided = s.approval === "allowed" || s.approval === "denied";
  return [
    {
      id: 1,
      at: iso(s.selectedAt + 200),
      worker: "planner",
      kind: "plan",
      summary: `선택한 경로 ${s.selected.length}개로 점검 계획을 세움`,
    },
    ...(since > 1500
      ? [
          {
            id: 2,
            at: iso(s.selectedAt + 1500),
            worker: "worker-1",
            kind: "request",
            summary: "GET /api/orders/7 응답 401 확인",
          },
        ]
      : []),
    ...(since > 4000
      ? [
          {
            id: 3,
            at: iso(s.selectedAt + 4000),
            worker: "worker-1",
            kind: "request",
            summary: "GET /rest/admin/application-configuration 비로그인 200 응답",
          },
        ]
      : []),
    ...(s.approval !== "none"
      ? [
          {
            id: 4,
            at: iso(s.selectedAt + 3000),
            worker: "worker-2",
            kind: "approval",
            summary: "PUT /api/Users/2 전송 전 승인 요청",
          },
        ]
      : []),
    ...(decided
      ? [
          {
            id: 5,
            at: iso(s.decidedAt),
            worker: "worker-2",
            kind: "approval",
            summary: `PUT /api/Users/2 ${s.approval === "allowed" ? "허용 후 전송" : "거부로 전송 안 함"}`,
          },
        ]
      : []),
  ];
}
function workflow() {
  const s = st();
  const input = s.input as Input;
  const all = endpoints(hostOf(input.target_url));
  const selected = new Set(s.selected);
  const diag = s.stage === "diagnosing" || s.stage === "completed";
  const since = diag ? Date.now() - s.selectedAt : 0;
  const done = s.stage === "completed";
  const shown = all
    .slice(0, s.stage === "crawling" ? s.shown : all.length)
    .map((e) => ({ ...e, selected: selected.has(e.id) }));
  const visited = shown.filter((e) => e.visited);
  const plan = planFor(diag, done, since, selected);
  const activity = activityFor(diag, since);
  return {
    workflow: {
      format: FLOW_FORMAT,
      job_id: JOB,
      task_id: TASK,
      run_ref: RUN,
      source_digest: DIGEST,
      revision: s.flowRev,
      mode: input.workflow_mode ?? "user_approval_v1",
      stage: s.stage,
      paused: s.paused,
      initial_completed: s.stage !== "crawling",
      deep_runs: diag ? 2 : 0,
      deep_closed: deepClosed(done, diag && since > 6000),
      reversible_writes: true,
      ...(s.stage === "crawling" && shown.length ? { current_path: shown[shown.length - 1].path } : {}),
      limits: LIMITS,
      counts: {
        found: shown.length,
        visited: visited.length,
        queued: s.stage === "crawling" ? Math.max(0, all.length - shown.length) : 0,
        sent: visited.length + (diag ? 6 : 0),
        attempted: visited.length + (diag ? 7 : 0),
        unknown: 0,
        blocked: 1,
      },
      endpoints: shown,
      events: visited.slice(-12).map((e, i) => ({
        at: iso(s.startedAt + 1200 + 700 * i),
        kind: "visit",
        method: e.method,
        path: e.path,
        status: e.status,
      })),
      commands: s.commands,
      ...(plan ? { plan } : {}),
      ...(activity ? { activity } : {}),
      updated_at: iso(Date.now()),
    },
  };
}

/** 시간이 지난 만큼 다음 단계로 넘긴다: 준비 1.2초 → 탐색(경로가 하나씩) → 범위 확인 → 진단 → 승인 → 완료. */
function tick() {
  const s = st();
  const job = s.job;
  if (!job || !s.startedAt || s.paused) return;
  const now = Date.now();
  const elapsed = now - s.startedAt;
  if (job.state === "preparing" && elapsed > 1200) {
    bump({ state: "running", phase: "execution", started: true, authority_state: "consumed" });
    s.flowRev = 1;
    s.shown = 3;
  }
  if (s.job?.state === "stopping") {
    bump({ state: "cancelled", phase: "report", drain_complete: true, result_ready: true, model_usage: usage() });
    s.stage = "stopped";
    s.flowRev++;
  }
  if (s.job?.state !== "running") return;
  const total = endpoints("example.test").length;
  if (s.stage === "crawling") {
    const reveal = Math.min(total, 3 + Math.floor((elapsed - 1200) / 700));
    if (reveal > s.shown) {
      s.shown = reveal;
      s.flowRev++;
    }
    if (s.shown >= total && elapsed > 11000) {
      s.flowRev++;
      if (s.input?.workflow_mode === "ai_auto_v1") {
        s.selected = endpoints("x")
          .filter((e) => e.change === "attested_read" && e.in_scope)
          .map((e) => e.id);
        s.stage = "diagnosing";
        s.selectedAt = now;
      } else s.stage = "awaiting_selection";
    }
  }
  if (s.stage === "diagnosing") {
    const since = now - s.selectedAt;
    const writes = s.selected.some((id) => endpoints("x").find((e) => e.id === id)?.change === "possible");
    if (writes && s.approval === "none" && since > 3000) {
      s.approval = "pending";
      s.flowRev++;
    }
    const finishAt = s.decidedAt ? s.decidedAt + 4000 : s.selectedAt + 9000;
    if (s.approval !== "pending" && (!writes || s.decidedAt) && now > finishAt) {
      s.stage = "completed";
      s.doneAt = now;
      s.flowRev++;
      bump({ state: "completed", phase: "report", drain_complete: true, result_ready: true, model_usage: usage() });
    }
  }
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}
const envelope = (draft: unknown) => ({ version: VERSION, owner_context: OWNER, draft });

function draftAction(action: string) {
  const s = st();
  const job = s.job as Json;
  if (action === "validate" && job.state === "awaiting_approval")
    bump({ state: "ready", phase: "scope_check", scope: scopeFor(s.input as Input) });
  else if (action === "start" && job.state === "ready") {
    bump({
      state: "preparing",
      phase: "task_creation",
      preparation_started: true,
      authority_state: "issued",
      task_id: TASK,
    });
    s.startedAt = Date.now();
  } else if (action === "stop" && ["preparing", "running"].includes(String(job.state)))
    bump({ state: "stopping", phase: "closure" });
  return json(envelope(s.job));
}
function workflowCommand(op: string | undefined, key: string, body: Json) {
  const s = st();
  if (op && !s.commands.some((c) => c.key === key)) {
    s.flowRev++;
    if (op === "selection" && s.stage === "awaiting_selection") {
      s.selected = Array.isArray(body.selected_ids) ? (body.selected_ids as string[]) : [];
      s.stage = "diagnosing";
      s.selectedAt = Date.now();
    }
    if (op === "pause") s.paused = true;
    if (op === "resume") s.paused = false;
    s.commands = [...s.commands, { key, operation: op, revision: s.flowRev }].slice(-30);
  }
  return json(workflow());
}
function createDraft(key: string, body: Json) {
  const s = st();
  if (s.job && s.createKey === key) return json(envelope(s.job));
  state = fresh();
  const n = st();
  n.input = body.input as Input;
  n.createKey = key;
  n.job = {
    id: JOB,
    revision: 1,
    source_digest: DIGEST,
    input: n.input,
    state: "awaiting_approval",
    phase: "draft",
    preparation_started: false,
    authority_state: "not_issued",
    started: false,
    drain_complete: false,
    result_ready: false,
  };
  return json(envelope(n.job));
}
function targetFor(url: string) {
  return {
    target: {
      id: `target-${"1".repeat(32)}`,
      origin: originOf(url),
      state: "approved",
      path_prefixes: ["/"],
      methods: ["GET", "HEAD"],
      requests: 4,
      addresses: ["203.0.113.10"],
      port: 443,
      tls_spki_sha256: "9".repeat(64),
      expires_at: iso(Date.now() + 4 * 3600_000),
    },
  };
}

function route(method: string, url: URL, init: RequestInit | undefined, headers: Headers): Response {
  const path = url.pathname;
  const body: Json = typeof init?.body === "string" && init.body ? (JSON.parse(init.body) as Json) : {};
  const s = st();
  if (path === "/api/auth/status") return json({ initialized: true });
  if (path === "/api/auth/login") return json({ token: "example-token" });
  if (path === "/api/diagnoses/v2/catalog") return json(CATALOG);
  if (path === "/api/diagnoses/v2/drafts" && method === "POST")
    return createDraft(headers.get("Idempotency-Key") ?? "", body);
  if (path === "/api/diagnoses/v2/drafts" && method === "GET")
    return s.job ? json(envelope(s.job)) : json({ error: "not_found" }, 404);
  const draft = path.match(/^\/api\/diagnoses\/v2\/drafts\/([^/]+)\/(status|validate|start|stop)$/);
  if (draft) return !s.job || draft[1] !== JOB ? json({ error: "not_found" }, 404) : draftAction(draft[2]);
  const flow = path.match(/^\/api\/product\/workflows\/([^/]+)(?:\/(selection|pause|resume))?$/);
  if (flow) {
    if (!s.job || !s.flowRev) return json({ error: "workflow_not_started" }, 409);
    return workflowCommand(flow[2], headers.get("Idempotency-Key") ?? "", body);
  }
  if (path === `/api/tasks/${TASK}/request-approvals`)
    return json({ run: runState(), approvals: s.approval === "none" ? [] : [approvalRow()] });
  if (path === `/api/tasks/${TASK}/request-approvals/ap-1`) return json(approvalRow());
  if (path === `/api/tasks/${TASK}/request-approvals/ap-1/decision` && s.approval === "pending") {
    s.approval = body.decision === "allow" ? "allowed" : "denied";
    s.decidedAt = Date.now();
    s.flowRev++;
    return json(approvalRow());
  }
  if (path === `/api/tasks/${TASK}/diagnosis-run`) {
    if (!s.job?.result_ready) return json({ error: "not_found" }, 404);
    return json({
      version: 1,
      task_id: Number(TASK),
      saved_at: iso(s.doneAt || Date.now()),
      run: runState(),
      model: {
        requests: 21,
        errors: 0,
        input_tokens: 182000,
        output_tokens: 9400,
        cost_usd_micros: 0,
        provider_slug: "local",
        model: "qwen3.5-35b",
        blocked: false,
      },
    });
  }
  if (path === "/api/report/diagnostic/remediation")
    return s.job?.result_ready ? json(remediation()) : json({ error: "not_found" }, 404);
  if (path === "/api/report/diagnostic")
    return new Response("<!doctype html><title>예시 보고서</title><p>예시 데이터로 만든 보고서 파일입니다.</p>", {
      status: 200,
      headers: { "Content-Type": "text/html" },
    });
  if (path === "/api/product/targets/resolve")
    return json(targetFor(url.searchParams.get("url") ?? "https://example.test"));
  if (path === "/api/product/model-credentials")
    return json({ credential: { configured: true, model: "qwen/qwen3.5-35b-a3b" } });
  return json({ error: "example_not_supported" }, 501);
}
function handle(method: string, url: URL, init: RequestInit | undefined, headers: Headers): Response {
  tick();
  try {
    return route(method, url, init, headers);
  } finally {
    save();
  }
}

/** 예시 상황 (띠의 고르기 칸) */
export const EXAMPLE_SCENES = [
  ["start", "처음부터 (새 점검)"],
  ["crawl", "자산 탐색 중"],
  ["scope", "범위 확인 대기"],
  ["live", "심화 진단 · 승인 대기"],
  ["report", "완료 · 보고서"],
] as const;
export type ExampleScene = (typeof EXAMPLE_SCENES)[number][0];
const SCENE_HASH: Record<ExampleScene, string> = {
  start: "new",
  crawl: "crawl",
  scope: "scope",
  live: "live",
  report: "report",
};
const SCENE_INPUT: Input = {
  target_url: "https://shop.example.test/",
  profile_id: "local-qwen",
  display_name: "주문 권한 점검",
  workflow_mode: "user_approval_v1",
};

/** 고른 상황으로 예시 상태를 만들고 저장한다. 이어 볼 핸들(JSON)을 돌려준다(처음부터면 null). */
export function buildScene(scene: ExampleScene): string | null {
  state = fresh();
  const s = st();
  if (scene === "start") {
    save();
    return null;
  }
  const now = Date.now();
  s.input = SCENE_INPUT;
  s.createKey = "create-example";
  s.startedAt = now - 2000;
  s.flowRev = 5;
  s.shown = 6;
  s.job = {
    id: JOB,
    revision: 4,
    source_digest: DIGEST,
    input: SCENE_INPUT,
    state: "running",
    phase: "execution",
    preparation_started: true,
    authority_state: "consumed",
    started: true,
    drain_complete: false,
    result_ready: false,
    task_id: TASK,
    scope: scopeFor(SCENE_INPUT),
  };
  if (scene !== "crawl") {
    s.startedAt = now - 20000;
    s.shown = 14;
    s.stage = "awaiting_selection";
  }
  if (scene === "live" || scene === "report") {
    s.stage = "diagnosing";
    s.selected = ["ep-4", "ep-7", "ep-9", "ep-11", "ep-12"];
    s.selectedAt = now - 5000;
    s.approval = "pending";
    s.commands = [{ key: "sel-example", operation: "selection", revision: 6 }];
    s.flowRev = 8;
  }
  if (scene === "report") {
    s.approval = "denied";
    s.selectedAt = now - 20000;
    s.decidedAt = now - 8000;
    s.doneAt = now - 3000;
    s.stage = "completed";
    s.job = {
      ...s.job,
      revision: 6,
      state: "completed",
      phase: "report",
      drain_complete: true,
      result_ready: true,
      model_usage: usage(),
    };
  }
  save();
  // revision 은 startRevision 보다 작으면 안 된다(lib/diagnosis readURLHandle 규칙)
  return JSON.stringify({
    ownerContext: OWNER,
    creationKey: s.createKey,
    input: SCENE_INPUT,
    id: JOB,
    revision: 3,
    sourceDigest: DIGEST,
    startKey: "start-example",
    startRevision: 3,
  });
}

/**
 * 상황을 바꾸고 다시 읽는다. 핸들은 다음 로드 때(앱이 뜨기 전) 넣는다 —
 * 지금 넣으면 화면 이동 처리기가 이전 상태로 덮어쓴다.
 */
export function setScene(scene: ExampleScene) {
  const pending = buildScene(scene);
  try {
    sessionStorage.removeItem(HANDLE_KEY);
    if (pending) sessionStorage.setItem(PENDING_KEY, pending);
    else sessionStorage.removeItem(PENDING_KEY);
  } catch {
    // 무시
  }
  location.hash = SCENE_HASH[scene];
  location.reload();
}

let installed = false;
/** 브라우저 fetch 를 가로채 /api/* 를 예시 응답으로 대신한다. 처음 열면 '승인 대기' 상황으로 시작한다. */
export function installExampleApi() {
  if (installed) return;
  installed = true;
  try {
    const pending = sessionStorage.getItem(PENDING_KEY);
    if (pending) {
      sessionStorage.setItem(HANDLE_KEY, pending);
      sessionStorage.removeItem(PENDING_KEY);
    } else if (!sessionStorage.getItem(STATE_KEY)) {
      const first = buildScene("live");
      if (first) sessionStorage.setItem(HANDLE_KEY, first);
    }
  } catch {
    // 저장소를 못 쓰면 빈 상태에서 시작
  }
  try {
    // 진짜 로그인 값이 있으면 덮어쓰지 않는다
    if (!localStorage.getItem(TOKEN_KEY)) localStorage.setItem(TOKEN_KEY, "example-token");
  } catch {
    // 로그인 화면이 나오면 아무 값이나 넣으면 된다
  }
  const real = window.fetch.bind(window);
  window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
    const raw = urlOf(input);
    const url = new URL(raw, location.href);
    if (!url.pathname.startsWith("/api/")) return real(input, init);
    const method = (init?.method ?? (input instanceof Request ? input.method : "GET")).toUpperCase();
    const headers = new Headers(init?.headers ?? (input instanceof Request ? input.headers : undefined));
    await new Promise((r) => setTimeout(r, 120));
    return handle(method, url, init, headers);
  };
}
