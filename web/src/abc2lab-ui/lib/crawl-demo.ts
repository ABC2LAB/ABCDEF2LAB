/**
 * 자산 탐색(1차 탐색 · 크롤링) 시안 데이터.
 * 엔진에는 이 단계가 따로 없다 — 에이전트가 정찰부터 공격까지 한 흐름으로 진행하고, 범위 확인에서 멈추지도 않는다.
 * 그래서 화면은 시안이고, 범위 확인 시안(ScopeScreen)의 37개 경로를 그대로 찾아 가도록 맞춰 두었다(crawl-screen.test 가 대조).
 * 연동 시 GET /api/exploration/activity · /api/assets?type=endpoint&task_id=… · /api/intercept/task/{id} 로 바꾼다.
 */

export type CrawlMethod = "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
export type CrawlGroup = "page" | "shop" | "acct" | "order" | "pay" | "admin";
/** req 보냄 · login 테스트 계정 로그인(보냄) · rec 찾기만 하고 보내지 않음 · skip 규칙으로 막음 · sys 알림 */
export type CrawlKind = "sys" | "req" | "login" | "rec" | "skip";

export type CrawlPath = { m: CrawlMethod; p: string; g: CrawlGroup };

export type CrawlEvent = {
  kind: CrawlKind;
  m?: CrawlMethod;
  p?: string;
  /** 응답 코드 — 보낸 요청(req · login)만 */
  status?: number;
  /** 비어 있는 sys 줄은 화면이 대상 주소로 채운다 */
  note: string;
  /** 이번에 새로 찾은 경로 */
  found: CrawlPath[];
  /** 이 줄 뒤의 방문 대기 링크 수 */
  queue: number;
  /** 이 줄에 걸린 시간의 비중(예시 초) — 전체 재생 시간(CRAWL_TOTAL_MS)을 이 비율로 나눠 쓴다 */
  sec: number;
};

/** 묶음 이름은 AI가 추정한 기능 분류 — 범위 확인 시안의 묶음과 같다 */
export const CRAWL_GROUP_LABEL: Record<CrawlGroup, string> = {
  page: "공개 페이지",
  shop: "상품·리뷰",
  acct: "계정·인증",
  order: "장바구니·주문",
  pay: "결제·쿠폰",
  admin: "관리자",
};

/**
 * 처음부터 끝까지 재생하는 시간(ms). 화면의 시각 · 경과도 이 시간으로 계산해서 실제로 흐른 시간과 같다.
 * 시안이라 실제 탐색보다 훨씬 짧게 둔다.
 */
export const CRAWL_TOTAL_MS = 10_000;

/** 첫 줄의 시각 10:42:05 (하루 기준 초) */
export const CRAWL_START_SEC = 10 * 3600 + 42 * 60 + 5;

/** 'GET /products shop' → { m, p, g } */
function paths(list: string[]): CrawlPath[] {
  return list.map((s) => {
    const [m, p, g] = s.split(" ");
    return { m: m as CrawlMethod, p, g: g as CrawlGroup };
  });
}

/** [종류, 메서드, 경로, 응답 코드, 설명, 이번에 찾은 경로, 방문 대기, 걸린 초] */
type Line = [CrawlKind, CrawlMethod | "", string, number, string, string[], number, number];

const ADMIN_SCRIPT = [
  "GET /admin admin",
  "GET /admin/stats admin",
  "GET /admin/users admin",
  "POST /admin/users admin",
  "GET /admin/orders admin",
  "PUT /admin/products/{id} admin",
  "GET /admin/logs admin",
  "DELETE /admin/delete-user/{id} admin",
];

const LINES: Line[] = [
  ["sys", "", "", 0, "", [], 0, 2],
  [
    "req",
    "GET",
    "/",
    200,
    "첫 화면 · 링크 14개",
    [
      "GET / page",
      "GET /products shop",
      "GET /categories shop",
      "GET /search shop",
      "GET /about page",
      "GET /faq page",
    ],
    13,
    4,
  ],
  [
    "req",
    "GET",
    "/robots.txt",
    200,
    "Disallow: /admin · 사이트맵 위치",
    ["GET /robots.txt page", "GET /sitemap.xml page", "GET /health page"],
    15,
    3,
  ],
  ["req", "GET", "/sitemap.xml", 200, "경로 21개 · 이미 아는 경로", [], 14, 3],
  ["req", "GET", "/health", 200, '응답 {"status":"ok"}', [], 13, 2],
  ["req", "GET", "/products", 200, "상품 카드 20개 · 같은 모양은 3개만 열어 봄", ["GET /products/{id} shop"], 16, 5],
  ["req", "GET", "/categories", 200, "카테고리 8개", [], 15, 3],
  ["req", "GET", "/products/12", 200, "리뷰 목록 API 호출 확인", ["GET /api/reviews shop"], 17, 5],
  ["rec", "POST", "/api/cart/add", 0, "상품 상세의 담기 버튼에서 발견", ["POST /api/cart/add order"], 17, 1],
  ["rec", "POST", "/api/reviews", 0, "리뷰 작성 폼 · 로그인 필요", ["POST /api/reviews shop"], 17, 1],
  ["req", "GET", "/api/reviews?product=12", 200, "리뷰 7건", [], 16, 3],
  ["req", "GET", "/products/31", 200, "같은 모양 경로", [], 15, 4],
  ["req", "GET", "/api/reviews?product=31", 200, "리뷰 3건", [], 14, 2],
  ["req", "GET", "/search?q=test", 200, "검색어가 결과 화면에 그대로 표시됨", [], 13, 4],
  ["req", "GET", "/about", 200, "정적 페이지", [], 12, 2],
  ["req", "GET", "/faq", 200, "정적 페이지", [], 11, 2],
  ["req", "GET", "/login", 200, "로그인 화면 · 폼 2개", [], 12, 3],
  [
    "rec",
    "POST",
    "/api/password/reset",
    0,
    "비밀번호 찾기 폼 · 메일이 발송돼 보내지 않음",
    ["POST /api/password/reset acct"],
    12,
    1,
  ],
  ["req", "GET", "/register", 200, "회원가입 화면", [], 11, 3],
  ["rec", "POST", "/api/register", 0, "회원가입 폼에서 발견", ["POST /api/register acct"], 11, 1],
  ["login", "POST", "/api/login", 200, "계정 A 로그인 성공 · 세션 쿠키 받음", ["POST /api/login acct"], 13, 4],
  ["req", "GET", "/mypage", 200, "로그인 후 메뉴 6개", ["GET /api/profile acct", "GET /api/users/{id} acct"], 18, 4],
  ["skip", "GET", "/api/logout", 0, "로그아웃 링크 · 세션이 끊겨 열지 않음", ["GET /api/logout acct"], 17, 1],
  ["req", "GET", "/api/profile", 200, "이메일·이름·전화번호 응답", [], 16, 3],
  ["req", "GET", "/api/users/1024", 200, "경로에 내 회원 번호", [], 15, 3],
  ["req", "GET", "/mypage/edit", 200, "회원 정보 수정 화면", [], 14, 3],
  ["rec", "PUT", "/api/users/{id}", 0, "정보 수정 폼에서 발견", ["PUT /api/users/{id} acct"], 14, 1],
  ["req", "GET", "/cart", 200, "장바구니 화면", ["GET /api/cart order"], 14, 3],
  ["req", "GET", "/api/cart", 200, "상품 2개", [], 13, 2],
  ["rec", "DELETE", "/api/cart/{id}", 0, "삭제 버튼 스크립트에서 발견", ["DELETE /api/cart/{id} order"], 13, 1],
  ["req", "GET", "/orders", 200, "주문 목록 화면", ["GET /api/orders order", "GET /api/orders/{id} order"], 14, 3],
  ["req", "GET", "/api/orders", 200, "주문 3건", [], 13, 2],
  ["req", "GET", "/api/orders/1012", 200, "배송지가 포함된 응답", [], 12, 3],
  ["req", "GET", "/api/orders/1013", 200, "같은 모양 경로", [], 11, 2],
  [
    "rec",
    "PATCH",
    "/api/orders/{id}",
    0,
    "배송지 변경·주문 취소 버튼에서 발견",
    ["PATCH /api/orders/{id} order"],
    11,
    1,
  ],
  ["req", "GET", "/checkout", 200, "주문서 화면 · 폼 3개", ["GET /api/coupons pay"], 11, 4],
  ["rec", "POST", "/api/orders", 0, "주문서 폼에서 발견", ["POST /api/orders order"], 11, 1],
  ["rec", "POST", "/api/checkout", 0, "결제 버튼에서 발견", ["POST /api/checkout pay"], 11, 1],
  ["rec", "POST", "/api/coupons/apply", 0, "쿠폰 입력란에서 발견", ["POST /api/coupons/apply pay"], 11, 1],
  ["req", "GET", "/api/coupons", 200, "쿠폰 2장", [], 10, 2],
  ["req", "GET", "/static/js/app.3f2a.js", 200, "관리자 메뉴 스크립트 · 숨은 경로", ADMIN_SCRIPT, 15, 6],
  ["req", "GET", "/admin", 403, "계정 A 접근 거부", [], 14, 2],
  ["req", "GET", "/admin/stats", 403, "계정 A 접근 거부", [], 13, 2],
  ["req", "GET", "/admin/users", 403, "계정 A 접근 거부", [], 12, 2],
  ["req", "GET", "/admin/orders", 403, "계정 A 접근 거부", [], 11, 2],
  ["req", "GET", "/admin/logs", 403, "계정 A 접근 거부", [], 10, 2],
  ["skip", "DELETE", "/admin/delete-user/{id}", 0, "전역 제외 규칙 /admin/delete*", [], 9, 1],
  ["skip", "GET", "https://cdn.example-analytics.com/collect", 0, "허용 주소 밖 · 기록만", [], 8, 1],
  ["login", "POST", "/api/login", 200, "계정 B 로그인 성공", [], 8, 4],
  ["req", "GET", "/api/orders", 200, "계정 B · 주문 2건", [], 6, 3],
  ["req", "GET", "/admin", 403, "계정 B도 접근 거부", [], 4, 2],
  ["req", "GET", "/products/7", 200, "같은 모양 경로", [], 3, 3],
  ["req", "GET", "/products?page=2", 200, "다음 페이지 · 새 경로 없음", [], 1, 3],
  ["req", "GET", "/products?page=3", 200, "마지막 페이지 · 새 경로 없음", [], 0, 3],
  ["sys", "", "", 0, "새 경로가 더 나오지 않아 탐색을 마칩니다", [], 0, 2],
];

export const CRAWL_EVENTS: CrawlEvent[] = LINES.map(([kind, m, p, status, note, found, queue, sec]) => ({
  kind,
  m: m || undefined,
  p: p || undefined,
  status: status || undefined,
  note,
  found: paths(found),
  queue,
  sec,
}));

/** 전역 제외 규칙에 걸려 범위 확인에서도 요청하지 않는 경로 */
export function isLockedPath(p: string): boolean {
  return p.startsWith("/admin/delete");
}
