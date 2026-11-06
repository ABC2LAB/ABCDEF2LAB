/**
 * 목업 예시 데이터. 목업 빌드(NEXT_PUBLIC_MOCK=1)에서만 api.ts 가 동적으로 불러온다 — 일반 빌드 번들에는 실리지 않게.
 * 실제 점검 결과가 아니다(루트 화면 위 배너로 항상 표시). 응답 형태는 lib/engine.ts 의 DTO 와 같다.
 * 기록 카드의 심각도 배지와 보고서 집계가 어긋나지 않게 task.findings 는 발견 목록에서 계산한다.
 */

import { MOCK_DEFAULT_TASK } from "./mock";

/** 불러오는 중 상태가 한 번은 보이게 두는 지연(ms) */
const MOCK_DELAY_MS = 150;

const now = Date.now();
const H = 3600_000;
const iso = (offsetMs: number) => new Date(now - offsetMs).toISOString();

type Finding = {
  id: string;
  finding_id: string;
  vulnclass: string;
  name: string;
  severity: string;
  status: string;
  summary: string;
  assets: { id: string; type: string; label: string }[];
  task_id: string;
  ts: string;
};

function finding(
  task: string,
  n: number,
  severity: string,
  vulnclass: string,
  name: string,
  summary: string,
  asset: string,
  status = "pending",
  ageMs = H,
): Finding {
  return {
    id: `${task}-${n}`,
    finding_id: `F-${task}-${n}`,
    vulnclass,
    name,
    severity,
    status,
    summary,
    assets: [{ id: `a-${task}-${n}`, type: "endpoint", label: asset }],
    task_id: task,
    ts: iso(ageMs),
  };
}

const FINDINGS: Record<string, Finding[]> = {
  "1005": [],
  "1004": [
    finding(
      "1004",
      1,
      "high",
      "IDOR",
      "다른 사용자 주문 상세 조회",
      "일반 사용자 B의 토큰으로 사용자 A의 주문 상세가 200으로 응답했어요(정상 대조: 본인 주문만 200).",
      "GET /api/orders/{id}",
      "confirmed",
      1.5 * H,
    ),
    finding(
      "1004",
      2,
      "medium",
      "Information Disclosure",
      "주문 목록 응답에 내부 필드 노출",
      "주문 목록 응답에 결제 처리 내부 상태 필드가 포함돼요.",
      "GET /api/orders",
      "pending",
      1.2 * H,
    ),
  ],
  "1003": [
    finding(
      "1003",
      1,
      "critical",
      "Broken Access Control",
      "일반 사용자의 관리자 사용자 목록 접근",
      "역할이 user인 계정으로 관리자 전용 사용자 목록 API가 200으로 응답했어요(수직 권한 상승).",
      "GET /api/admin/users",
      "confirmed",
      26 * H,
    ),
    finding(
      "1003",
      2,
      "high",
      "IDOR",
      "다른 사용자 프로필 수정",
      "사용자 B가 사용자 A의 프로필 id로 수정 요청을 보내 반영됐어요(시험 환경 리셋 후 재현).",
      "PUT /api/users/{id}/profile",
      "confirmed",
      26.5 * H,
    ),
    finding(
      "1003",
      3,
      "high",
      "IDOR",
      "다른 사용자 배송지 조회",
      "배송지 id를 바꾸면 다른 사용자의 주소가 조회돼요.",
      "GET /api/addresses/{id}",
      "confirmed",
      27 * H,
    ),
    finding(
      "1003",
      4,
      "medium",
      "Authentication",
      "로그인 시도 횟수 제한 없음",
      "잘못된 비밀번호 30회 연속 시도에도 잠금·지연이 없어요.",
      "POST /api/auth/login",
      "pending",
      27.5 * H,
    ),
    finding(
      "1003",
      5,
      "low",
      "Information Disclosure",
      "오류 응답에 스택 정보 포함",
      "잘못된 입력에 대한 오류 응답에 서버 내부 경로가 보여요.",
      "GET /api/products?sort=",
      "pending",
      28 * H,
    ),
  ],
  "1002": [
    finding(
      "1002",
      1,
      "high",
      "IDOR",
      "다른 사용자 장바구니 조회",
      "장바구니 id를 바꾸면 다른 사용자의 장바구니 항목이 보여요.",
      "GET /api/carts/{id}",
      "confirmed",
      50 * H,
    ),
    finding(
      "1002",
      2,
      "medium",
      "Broken Access Control",
      "상품 리뷰 삭제 권한 검사 누락",
      "작성자가 아닌 사용자의 리뷰 삭제 요청이 허용돼요.",
      "DELETE /api/reviews/{id}",
      "pending",
      50.5 * H,
    ),
    finding(
      "1002",
      3,
      "medium",
      "Information Disclosure",
      "사용자 목록 API에 이메일 노출",
      "공개 사용자 목록에 다른 사용자의 이메일이 포함돼요.",
      "GET /api/users",
      "pending",
      51 * H,
    ),
    finding(
      "1002",
      4,
      "low",
      "Information Disclosure",
      "서버 버전 헤더 노출",
      "응답 헤더에 웹 서버 버전이 노출돼요.",
      "GET /",
      "pending",
      51.5 * H,
    ),
    finding(
      "1002",
      5,
      "low",
      "Authentication",
      "세션 만료 시간 과도",
      "로그인 토큰 만료가 30일로 설정돼 있어요.",
      "POST /api/auth/login",
      "pending",
      52 * H,
    ),
  ],
  "1001": [],
};

function counts(list: Finding[]) {
  const c = { critical: 0, high: 0, medium: 0, low: 0 };
  for (const f of list) {
    if (f.severity in c) c[f.severity as keyof typeof c] += 1;
  }
  return c;
}

const TASKS = [
  {
    id: "1005",
    name: "관리자 기능 수직 권한 점검",
    description: "데모 쇼핑몰 관리자 기능에 일반 사용자가 접근되는지 확인",
    goal: "역할별 접근 통제 확인",
    status: "running",
    created_at: iso(0.6 * H),
    completed_at: "",
    goals_total: 4,
    goals_met: 1,
    in_flight: 2,
  },
  {
    id: "1004",
    name: "주문 API 접근통제 점검",
    description: "주문·결제 API의 사용자 간 접근 통제 확인",
    goal: "다른 사용자의 주문에 접근되는지 확인",
    status: "done",
    created_at: iso(3 * H),
    completed_at: iso(1 * H),
    goals_total: 3,
    goals_met: 3,
    in_flight: 0,
  },
  {
    id: "1003",
    name: "회원·프로필 IDOR 점검",
    description: "회원 정보·프로필·배송지 API 점검",
    goal: "다른 사용자의 자원에 접근되는지 확인",
    status: "done",
    created_at: iso(30 * H),
    completed_at: iso(25.5 * H),
    goals_total: 5,
    goals_met: 5,
    in_flight: 0,
  },
  {
    id: "1002",
    name: "쇼핑몰 전체 접근통제 1차",
    description: "데모 쇼핑몰 전체 범위 1차 점검",
    goal: "접근 통제 전반 확인",
    status: "done",
    created_at: iso(54 * H),
    completed_at: iso(49 * H),
    goals_total: 6,
    goals_met: 5,
    in_flight: 0,
  },
  {
    id: "1001",
    name: "스테이징 기본 점검",
    description: "스테이징 서버 연결 확인용 점검",
    goal: "기본 경로 탐색",
    status: "timeout",
    created_at: iso(78 * H),
    completed_at: iso(77 * H),
    goals_total: 2,
    goals_met: 0,
    in_flight: 0,
  },
].map((t) => ({ ...t, findings: counts(FINDINGS[t.id] ?? []) }));

const COVERAGE: Record<
  string,
  { denominator: number; tested: number; by_type: { type: string; total: number; tested: number }[] }
> = {
  "1005": {
    denominator: 24,
    tested: 9,
    by_type: [
      { type: "endpoint", total: 20, tested: 8 },
      { type: "page", total: 4, tested: 1 },
    ],
  },
  "1004": {
    denominator: 18,
    tested: 17,
    by_type: [
      { type: "endpoint", total: 16, tested: 15 },
      { type: "page", total: 2, tested: 2 },
    ],
  },
  "1003": {
    denominator: 32,
    tested: 29,
    by_type: [
      { type: "endpoint", total: 26, tested: 24 },
      { type: "page", total: 6, tested: 5 },
    ],
  },
  "1002": {
    denominator: 48,
    tested: 41,
    by_type: [
      { type: "endpoint", total: 40, tested: 35 },
      { type: "page", total: 8, tested: 6 },
    ],
  },
  "1001": {
    denominator: 12,
    tested: 3,
    by_type: [
      { type: "endpoint", total: 10, tested: 3 },
      { type: "page", total: 2, tested: 0 },
    ],
  },
};

/** 자산 그래프용 데모 쇼핑몰 경로. 발견이 걸린 경로를 먼저 두고 COVERAGE 의 엔드포인트 수만큼 자른다. */
const MOCK_ORIGIN = "https://shop.example.com";
const SHOP_PATHS = [
  "/",
  "/products",
  "/products/{id}",
  "/search",
  "/login",
  "/register",
  "/cart",
  "/api/products",
  "/api/products/{id}",
  "/api/orders",
  "/api/orders/{id}",
  "/api/carts/{id}",
  "/api/users",
  "/api/users/{id}",
  "/api/users/{id}/profile",
  "/api/addresses",
  "/api/addresses/{id}",
  "/api/reviews",
  "/api/reviews/{id}",
  "/api/coupons",
  "/api/wishlist",
  "/api/auth/login",
  "/api/auth/logout",
  "/api/payments/verify",
  "/api/notifications",
  "/api/admin/users",
  "/api/admin/stats",
  "/api/admin/orders",
  "/admin",
  "/admin/users",
  "/admin/orders",
  "/admin/settings",
  "/admin/logs",
  "/api/orders/{id}/receipt",
  "/api/orders/{id}/tracking",
  "/api/products/{id}/qna",
  "/api/users/{id}/orders",
  "/api/admin/coupons",
  "/admin/reports",
  "/api/health",
];

/** GET /api/tasks/{id}/coverage-graph 예시 — 엔드포인트 수 · 검사 수는 COVERAGE 의 endpoint 값과 같다. */
function coverageGraph(id: string) {
  const c = COVERAGE[id];
  if (!c) return undefined;
  const ep = c.by_type.find((t) => t.type === "endpoint") ?? { total: 0, tested: 0 };
  const fromFindings = (FINDINGS[id] ?? []).flatMap((f) => f.assets.map((a) => a.label.replace(/^[A-Z]+\s+/, "")));
  const paths = [...new Set([...fromFindings, ...SHOP_PATHS])].slice(0, ep.total);
  const service = {
    key: "a:1",
    kind: "service",
    label: MOCK_ORIGIN,
    tested: true,
    in_scope: true,
    asset_id: 1,
    url: MOCK_ORIGIN,
  };
  const nodes = [
    {
      key: "r:shop.example.com",
      kind: "root_domain",
      label: "shop.example.com",
      tested: false,
      in_scope: false,
      domain: "shop.example.com",
    },
    service,
    ...paths.map((p, i) => ({
      key: `a:${100 + i}`,
      kind: "endpoint",
      label: `${MOCK_ORIGIN}${p}`,
      url: `${MOCK_ORIGIN}${p}`,
      tested: i < ep.tested,
      in_scope: true,
      asset_id: 100 + i,
    })),
  ];
  const edges = [
    { src: service.key, dst: "r:shop.example.com" },
    ...paths.map((_, i) => ({ src: `a:${100 + i}`, dst: service.key })),
  ];
  return { nodes, edges };
}

const TOKENS: Record<string, [number, number]> = {
  "1005": [182_400, 21_300],
  "1004": [412_900, 48_700],
  "1003": [905_300, 101_200],
  "1002": [1_240_800, 139_500],
  "1001": [88_100, 9_400],
};

function activity(taskId: string) {
  const t = TASKS.find((x) => x.id === taskId);
  if (!t) return [];
  const start = Date.parse(t.created_at);
  const steps: [string, string, string, boolean][] = [
    ["planner", "round", "목표를 4개 점검 항목으로 나눴어요: 사용자 목록·주문 관리·설정·통계", false],
    ["planner", "text", "계정 2개(user·admin) 기준으로 역할별 접근 표를 만들어요", false],
    ["work#1", "tool", "관리자 메뉴 경로 탐색", false],
    ["work#1", "result", "관리자 전용 경로 6개 확인", false],
    ["work#2", "tool", "user 계정으로 GET /api/admin/users 시도", false],
    ["work#2", "observation", "403 응답 — 정상 차단(대조 기록 저장)", false],
    ["work#3", "tool", "user 계정으로 GET /api/admin/stats 시도", false],
    ["work#3", "observation", "요청 시간 초과 — 재시도 대기", true],
    ["planner", "round", "통계 경로는 대상 장애 징후로 보류, 설정 경로 먼저", false],
    ["work#1", "tool", "user 계정으로 GET /api/admin/settings 시도", false],
  ];
  return steps.map(([worker, kind, summary, isError], i) => ({
    seq: i + 1,
    worker,
    ts: new Date(start + (i + 1) * 90_000).toISOString(),
    kind,
    is_error: isError,
    summary,
  }));
}

function controlLog(taskId: string) {
  const t = TASKS.find((x) => x.id === taskId);
  if (!t) return { items: [], total: 0 };
  const start = Date.parse(t.created_at);
  const rows: [string, string, string, string, string, string][] = [
    ["work#1 · #12", "http_request", "allowed", "rule", "대상 origin 안의 조회 요청", "대상 origin 허용"],
    ["work#2 · #31", "http_request", "allowed", "rule", "대상 origin 안의 조회 요청", "대상 origin 허용"],
    ["work#2 · #44", "shell", "denied", "rule", "셸 명령은 지원하지 않는 경로라 차단", "셸 기본 차단"],
    ["work#3 · #57", "http_request", "denied", "rule", "범위 밖 origin(외부 도메인)", "범위 밖 차단"],
    ["work#1 · #63", "http_request", "pending", "judge", "상태 변경 요청(PUT) — 승인 필요", "상태 변경 승인"],
    ["work#1 · #63", "http_request", "approved", "user", "시험 환경 리셋 확인 후 승인", "상태 변경 승인"],
  ];
  const items = rows.map(([agent, tool, status, source, reason, rule], i) => ({
    id: Number(taskId) * 100 + i + 1,
    task_id: taskId,
    agent_name: agent,
    tool_name: tool,
    status,
    decision_source: source,
    reason,
    rule_name: rule,
    created_at: new Date(start + (i + 1) * 120_000).toISOString(),
    decided_at: new Date(start + (i + 1) * 120_000 + 4_000).toISOString(),
  }));
  return { items, total: items.length };
}

function tokenUsage() {
  const daily = [];
  for (let d = 13; d >= 0; d -= 1) {
    const date = new Date(now - d * 24 * H).toISOString().slice(0, 10);
    const base = 120_000 + ((d * 37_000) % 260_000);
    daily.push({
      date,
      input_tokens: base,
      output_tokens: Math.round(base * 0.11),
      cache_read_tokens: Math.round(base * 0.4),
    });
  }
  return {
    by_profile: [
      {
        profile_name: "로컬 35B(예시)",
        calls: 1240,
        tasks: 4,
        input_tokens: 2_829_500,
        output_tokens: 320_100,
        cache_read_tokens: 1_104_000,
        cache_write_tokens: 210_400,
      },
      {
        profile_name: "로컬 소형(예시)",
        calls: 210,
        tasks: 1,
        input_tokens: 88_100,
        output_tokens: 9_400,
        cache_read_tokens: 20_300,
        cache_write_tokens: 5_100,
      },
    ],
    daily,
  };
}

const LLM = {
  provider: "openai",
  model: "qwen/qwen3.6-35b-a3b (예시)",
  base_url: "http://llm.internal/v1",
  configured: true,
  key_set: false,
  context_window_k: 128,
};

const RULES = {
  rules: [
    {
      id: 1,
      name: "대상 origin 허용",
      enabled: true,
      priority: 100,
      match_target: "url",
      match_type: "regex",
      pattern: "^https?://demo-shop\\.internal(/|$)",
      action: "allow",
    },
    {
      id: 2,
      name: "범위 밖 차단",
      enabled: true,
      priority: 90,
      match_target: "url",
      match_type: "regex",
      pattern: ".*",
      action: "deny",
      message: "설정한 대상 origin 밖",
    },
    {
      id: 3,
      name: "셸 기본 차단",
      enabled: true,
      priority: 80,
      match_target: "tool_input",
      match_type: "regex",
      pattern: ".*",
      action: "deny",
      message: "지원하지 않는 경로",
    },
    {
      id: 4,
      name: "상태 변경 승인",
      enabled: true,
      priority: 70,
      match_target: "tool_input",
      match_type: "regex",
      pattern: '"method":"(PUT|PATCH|POST|DELETE)"',
      action: "ask",
    },
  ],
};

/** 승인 대기 예시 — 대시보드의 "승인하기"와 승인 화면의 허용 · 거부가 이 상태를 바꾼다(새로고침하면 처음으로) */
const PENDING: {
  id: number;
  task_id: string;
  agent_name: string;
  tool_name: string;
  tool_input: unknown;
  status: string;
  reason: string;
  created_at: string;
  decided_at: string | null;
}[] = [
  {
    id: 41,
    task_id: MOCK_DEFAULT_TASK,
    agent_name: "work#3",
    tool_name: "http_request",
    tool_input: {
      method: "PUT",
      url: "https://shop.example.com/api/users/2/role",
      headers: { Cookie: "session=example-session-value", "Content-Type": "application/json" },
      body: { role: "admin" },
    },
    status: "pending",
    reason: "가로채기 규칙 [상태 변경 승인] 에 따라 사용자 승인이 필요합니다. 잠시 기다려 주세요",
    created_at: iso(0.2 * H),
    decided_at: null,
  },
  {
    id: 42,
    task_id: MOCK_DEFAULT_TASK,
    agent_name: "work#5",
    tool_name: "Bash",
    tool_input: {
      command: "curl -s -X DELETE -H 'Authorization: Bearer example-token' https://shop.example.com/api/reviews/7",
    },
    status: "pending",
    reason: "가로채기 규칙 [상태 변경 승인] 에 따라 사용자 승인이 필요합니다. 잠시 기다려 주세요",
    created_at: iso(0.1 * H),
    decided_at: null,
  },
];

/**
 * 발견 상세(GET /api/exploration/findings/{finding_id}) 예시 — 증거 · 보고서 · 요청 기록 · 재검증.
 * 따로 적지 않은 발견은 요약을 증거로 쓰고, 요청 기록 · 재검증은 비워 둔다.
 */
type FindingExtra = {
  evidence: string;
  report?: string;
  traffic?: { role: string; note: string; method: string; url: string; status: number; resp_len: number }[];
  retests?: { verdict: string; status: string; summary: string; ageMs: number }[];
};
const FINDING_EXTRA: Record<string, FindingExtra> = {
  "F-1004-1": {
    evidence:
      "계정 B 세션으로 계정 A의 주문 1012 상세 요청\nGET /api/orders/1012\nCookie: session=b-7f2c91e0\n→ 200, 응답에 계정 A의 주문 표식(buyer_note)이 그대로 나옴\n대조: 계정 B 본인 주문 1013 → 200, 비로그인 → 401",
    report:
      "## 요약\n주문 상세 API가 요청한 사용자가 주문의 주인인지 확인하지 않아, 주문 번호만 바꾸면 다른 사용자의 주문을 볼 수 있어요.\n\n## 재현 방법\n1. 계정 B로 로그인한다.\n2. GET /api/orders/1012 (계정 A의 주문)를 요청한다.\n3. 200과 함께 계정 A의 주문 내용이 돌아온다.\n\n## 고치려면\n주문 조회 시 order.owner_id 와 세션 사용자를 서버에서 비교하세요.",
    traffic: [
      {
        role: "baseline",
        note: "계정 A 본인 주문 조회",
        method: "GET",
        url: "https://shop.example.com/api/orders/1012",
        status: 200,
        resp_len: 1840,
      },
      {
        role: "proof",
        note: "계정 B로 계정 A의 주문 조회",
        method: "GET",
        url: "https://shop.example.com/api/orders/1012",
        status: 200,
        resp_len: 1840,
      },
      {
        role: "verification",
        note: "비로그인 대조",
        method: "GET",
        url: "https://shop.example.com/api/orders/1012",
        status: 401,
        resp_len: 52,
      },
    ],
    retests: [
      { verdict: "reproduced", status: "completed", summary: "같은 요청이 여전히 200으로 응답", ageMs: 0.5 * H },
    ],
  },
  "F-1003-1": {
    evidence:
      "role=user 계정 토큰으로 관리자 API 호출\nGET /api/admin/users\nAuthorization: Bearer user-token-3b8e\n→ 200, 전체 사용자 목록(이메일 · 역할) 반환",
    report:
      "## 요약\n관리자 전용 사용자 목록 API가 역할(role)을 확인하지 않아 일반 사용자도 전체 회원 정보를 볼 수 있어요.\n\n## 고치려면\n관리자 API 앞단에서 세션 사용자의 role 이 admin 인지 서버에서 확인하세요.",
    traffic: [
      {
        role: "proof",
        note: "일반 사용자 토큰으로 호출",
        method: "GET",
        url: "https://api.store-dev.com/api/admin/users",
        status: 200,
        resp_len: 9230,
      },
    ],
    retests: [
      { verdict: "fixed", status: "completed", summary: "패치 후 403으로 바뀜", ageMs: 2 * H },
      { verdict: "reproduced", status: "completed", summary: "여전히 200", ageMs: 20 * H },
    ],
  },
};

function findingDetail(fid: string) {
  const f = Object.values(FINDINGS)
    .flat()
    .find((x) => x.finding_id === fid);
  if (!f) return undefined;
  const extra = FINDING_EXTRA[fid];
  return {
    ...f,
    evidence: extra?.evidence ?? f.summary,
    report: extra?.report ?? "",
    report_stale: false,
    traffic_count: extra?.traffic?.length ?? 0,
    task_description: TASKS.find((t) => t.id === f.task_id)?.description ?? "",
  };
}

function findingTraffic(fid: string) {
  if (!findingDetail(fid)) return undefined;
  return {
    finding_id: fid,
    bindings: (FINDING_EXTRA[fid]?.traffic ?? []).map((t, i) => ({
      id: `${fid}-b${i}`,
      role: t.role,
      note: t.note,
      position: i,
      snapshot: {
        method: t.method,
        url: t.url,
        status: t.status,
        content_type: "application/json",
        req_len: 0,
        resp_len: t.resp_len,
        captured_at: Math.floor((now - 1.6 * H) / 1000) + i * 7,
      },
    })),
  };
}

function findingRetests(fid: string) {
  if (!findingDetail(fid)) return undefined;
  return {
    retests: (FINDING_EXTRA[fid]?.retests ?? []).map((r, i) => ({
      id: i + 1,
      finding_id: fid,
      status: r.status,
      verdict: r.verdict,
      summary: r.summary,
      error: "",
      created_at: iso(r.ageMs + 60_000),
      finished_at: iso(r.ageMs),
    })),
  };
}

function taskIdFrom(url: URL): string {
  return url.searchParams.get("task") ?? "";
}

type Route = {
  method: string;
  path: RegExp;
  /** undefined 를 돌려주면 "없음"으로 실패시킨다 */
  reply: (params: string[], url: URL, init: RequestInit) => unknown;
};

const NO_CONTENT = Symbol("no-content");

const ROUTES: Route[] = [
  { method: "GET", path: /^\/api\/tasks$/, reply: () => ({ active: MOCK_DEFAULT_TASK, tasks: TASKS }) },
  { method: "GET", path: /^\/api\/tasks\/([^/]+)$/, reply: ([id]) => TASKS.find((t) => t.id === id) },
  {
    method: "GET",
    path: /^\/api\/tasks\/([^/]+)\/coverage$/,
    reply: ([id]) => {
      const c = COVERAGE[id];
      return c && { enabled: true, scope_rows: c.denominator, pct: c.tested / c.denominator, ...c };
    },
  },
  { method: "GET", path: /^\/api\/tasks\/([^/]+)\/coverage-graph$/, reply: ([id]) => coverageGraph(id) },
  { method: "POST", path: /^\/api\/tasks\/([^/]+)\/control$/, reply: () => NO_CONTENT },
  {
    method: "POST",
    path: /^\/api\/tasks\/([^/]+)\/archive$/,
    reply: ([id]) => ({ id: `ar-${id}`, task_id: id, state: "archive_queued" }),
  },
  {
    method: "GET",
    path: /^\/api\/task-archives\/ar-([^/]+)$/,
    reply: ([id]) => ({ id: `ar-${id}`, task_id: id, state: "archived" }),
  },
  { method: "GET", path: /^\/api\/exploration\/findings$/, reply: (_, url) => FINDINGS[taskIdFrom(url)] ?? [] },
  { method: "GET", path: /^\/api\/exploration\/findings\/([^/]+)$/, reply: ([fid]) => findingDetail(fid) },
  { method: "GET", path: /^\/api\/exploration\/findings\/([^/]+)\/traffic$/, reply: ([fid]) => findingTraffic(fid) },
  { method: "GET", path: /^\/api\/exploration\/findings\/([^/]+)\/retests$/, reply: ([fid]) => findingRetests(fid) },
  { method: "GET", path: /^\/api\/exploration\/activity$/, reply: (_, url) => ({ items: activity(taskIdFrom(url)) }) },
  {
    method: "GET",
    path: /^\/api\/exploration\/tokens$/,
    reply: (_, url) => {
      const [input, output] = TOKENS[taskIdFrom(url)] ?? [0, 0];
      return { total: { input_tokens: input, output_tokens: output } };
    },
  },
  { method: "GET", path: /^\/api\/audit$/, reply: () => ({ entries: [] }) },
  { method: "GET", path: /^\/api\/intercept\/task\/([^/]+)$/, reply: ([id]) => controlLog(id) },
  { method: "GET", path: /^\/api\/intercept\/rules$/, reply: () => RULES },
  {
    method: "GET",
    path: /^\/api\/intercept\/pending$/,
    reply: () => ({ pending: PENDING.filter((p) => p.status === "pending").map(({ tool_input: _i, ...p }) => p) }),
  },
  { method: "GET", path: /^\/api\/intercept\/pending\/(\d+)$/, reply: ([id]) => PENDING.find((p) => p.id === +id) },
  {
    method: "POST",
    path: /^\/api\/intercept\/pending\/(\d+)\/decide$/,
    reply: ([id], _url, init) => {
      const p = PENDING.find((x) => x.id === +id);
      const { decision } = JSON.parse(typeof init.body === "string" ? init.body : "{}") as { decision?: string };
      // 이미 결정된 요청 · 잘못된 값은 실패시킨다(엔진은 409 · 400)
      if (p?.status !== "pending" || (decision !== "allowed" && decision !== "denied")) return undefined;
      p.status = decision;
      p.decided_at = new Date().toISOString();
      return { ok: true };
    },
  },
  { method: "GET", path: /^\/api\/tokens\/usage$/, reply: () => tokenUsage() },
  { method: "GET", path: /^\/api\/llm$/, reply: () => LLM },
];

/** 경로 → 예시 응답. 모르는 경로·없는 id 는 실패시킨다(화면은 기존 오류 상태로 보인다). */
export async function mockFetch<T>(path: string, init: RequestInit = {}): Promise<T> {
  await new Promise((resolve) => setTimeout(resolve, MOCK_DELAY_MS));
  const url = new URL(path, "http://mock.invalid");
  const method = (init.method ?? "GET").toUpperCase();
  for (const route of ROUTES) {
    const match = route.method === method ? url.pathname.match(route.path) : null;
    if (!match) continue;
    const body = route.reply(match.slice(1).map(decodeURIComponent), url, init);
    if (body === NO_CONTENT) return undefined as T;
    if (body !== undefined) return body as T;
    break;
  }
  throw new Error("목업 모드: 이 요청의 예시 데이터는 없어요");
}
