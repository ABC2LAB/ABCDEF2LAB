// 자산 탐색(크롤링) 각본. 원본 js 의 GL · GORD · MORD · OFF · ADMIN · LINES → EV · AT · PCT 를 그대로 옮김.

/** HTTP 메서드 (CSS 클래스 이름으로도 쓴다: `s[m]`). */
export type Method = "GET" | "POST" | "PUT" | "PATCH" | "DELETE";

/** 경로 묶음 키 → 이름. */
export const GL = {
  page: "공개 페이지",
  shop: "상품·리뷰",
  acct: "계정·인증",
  order: "장바구니·주문",
  pay: "결제·쿠폰",
  admin: "관리자",
} as const;
export type Group = keyof typeof GL;

/** 묶음 표시 순서 (자산 탐색 카드). */
export const GORD: readonly Group[] = ["page", "shop", "acct", "order", "pay", "admin"];
/** 메서드 정렬 순서. */
export const MORD: readonly Method[] = ["GET", "POST", "PUT", "PATCH", "DELETE"];

/** 기본 제외 경로 ("메서드 경로" → 이유). */
export const OFF: Readonly<Record<string, string>> = {
  "GET /api/logout": "세션이 끊김",
  "POST /api/password/reset": "메일 발송",
  "DELETE /api/cart/{id}": "삭제 요청",
};

/** 관리자 메뉴 스크립트에서 찾은 숨은 경로. */
export const ADMIN: readonly string[] = [
  "GET /admin admin",
  "GET /admin/stats admin",
  "GET /admin/users admin",
  "POST /admin/users admin",
  "GET /admin/orders admin",
  "PUT /admin/products/{id} admin",
  "GET /admin/logs admin",
  "DELETE /admin/delete-user/{id} admin",
];

/** 로그 줄 종류: sys 안내 · req 요청 · rec 기록만(보내지 않음) · login 로그인 · skip 요청 안 함 */
export type LineKind = "sys" | "req" | "rec" | "login" | "skip";
/** [종류, 메서드, 경로, 응답, 설명, 이번에 찾은 경로("메서드 경로 묶음"), 방문 대기, 걸린 시간 비중] */
export type Line = readonly [LineKind, Method | "", string, number, string, readonly string[], number, number];

// [종류, 메서드, 경로, 응답, 설명, 이번에 찾은 경로, 방문 대기, 걸린 시간 비중]
export const LINES: readonly Line[] = [
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
  ["req", "GET", "/static/js/app.3f2a.js", 200, "관리자 메뉴 스크립트 · 숨은 경로", ADMIN, 15, 6],
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

/** 탐색 중 찾은 경로 하나 (원본 found 항목. fresh 는 스토어가 붙인다). */
export interface FoundSeed {
  readonly m: Method;
  readonly p: string;
  readonly g: Group;
}

/** 재생 단계 하나 (원본 EV 항목). */
export interface Ev {
  readonly k: LineKind;
  readonly m: Method | "";
  readonly p: string;
  readonly st: number;
  readonly note: string;
  readonly found: readonly FoundSeed[];
  readonly q: number;
  readonly sec: number;
}

/** LINES 를 객체로 (원본과 같은 변환). */
export const EV: readonly Ev[] = LINES.map((l) => ({
  k: l[0],
  m: l[1],
  p: l[2],
  st: l[3],
  note: l[4],
  found: l[5].map((s) => {
    const a = s.split(" ");
    return { m: a[0] as Method, p: a[1], g: a[2] as Group };
  }),
  q: l[6],
  sec: l[7],
}));

/** 전체 재생 시간(ms)과 첫 줄의 가상 시각(10:42:05). */
export const TOTAL_MS = 10000;
export const START_SEC = 10 * 3600 + 42 * 60 + 5;

/** 각 단계가 시작되는 시각(ms). 걸린 시간 비중(sec)을 TOTAL_MS 에 나눠 담는다. */
export const AT: readonly number[] = (() => {
  const sum = EV.reduce((a, e) => a + e.sec, 0);
  let acc = 0;
  return EV.map((e) => {
    const t = Math.round((acc / sum) * TOTAL_MS);
    acc += e.sec;
    return t;
  });
})();

// 진행률은 '방문 / (방문 + 대기)'의 누적 최댓값: 새 링크가 늘어도 막대가 뒤로 가지 않는다
/** 단계 i 까지 마친 뒤의 진행률(%). 마지막은 100. */
export const PCT: readonly number[] = (() => {
  let sent = 0;
  let max = 0;
  return EV.map((e, i) => {
    if (e.k === "req" || e.k === "login") sent++;
    const known = sent + e.q;
    max = Math.max(max, known ? Math.round((sent / known) * 100) : 0);
    return i === EV.length - 1 ? 100 : max;
  });
})();
