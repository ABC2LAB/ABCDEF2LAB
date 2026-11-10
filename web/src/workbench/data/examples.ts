// API 상세의 요청·응답 예시 재료. 원본 IDS · QS · REASON · EX 를 그대로 옮김 (범위 확인 · 자산 탐색이 함께 쓴다).
// 응답 본문 안의 HTML(<script …></script> 포함)은 보여 줄 글자일 뿐이다: 화면은 텍스트로만 찍는다(React 가 이스케이프).

/** JSON 본문 (JSON.stringify(…, null, 2) 로 그대로 찍는다 — 키 순서가 곧 표시 순서). */
export type Json = string | number | boolean | null | readonly Json[] | { readonly [k: string]: Json };

/** 응답 예시 재료 (원본 EX[key].res). */
export interface ExRes {
  /** 응답 코드. 없으면 관찰한 응답(st)의 첫 숫자, 그것도 없으면 200 */
  readonly code?: number;
  /** Content-Type 뒤에 붙는 헤더 줄 */
  readonly hdr?: readonly string[];
  readonly json?: Json;
  readonly html?: string;
  /** text 본문의 Content-Type */
  readonly ct?: string;
  /** 글 본문. `{host}` 는 점검 대상 host 로 바뀐다 */
  readonly text?: string;
  /** 본문·Content-Type 을 찍지 않음 (302 · 204) */
  readonly none?: true;
  /** 보내지 않는 요청 → 응답 예시 없음 */
  readonly blocked?: true;
}

/** 요청 본문(JSON) + 응답 예시 재료 (원본 EX 항목). */
export interface Ex {
  readonly req?: { readonly [k: string]: Json };
  readonly res?: ExRes;
}

/** 경로의 {id} 자리에 넣을 예시 번호 (없으면 1). */
export const IDS: Readonly<Partial<Record<string, string>>> = {
  "/api/users/{id}": "1024",
  "/api/orders/{id}": "1012",
  "/api/cart/{id}": "7",
  "/products/{id}": "12",
  "/admin/products/{id}": "12",
  "/admin/delete-user/{id}": "1025",
};

/** 예시 요청에 붙일 질의 문자열 ("메서드 경로" → ?…). */
export const QS: Readonly<Partial<Record<string, string>>> = {
  "GET /search": "?q=test",
  "GET /api/reviews": "?product=12",
};

/** 응답 코드 → 이유 문구. */
export const REASON: Readonly<Partial<Record<number, string>>> = {
  200: "OK",
  201: "Created",
  204: "No Content",
  302: "Found",
  400: "Bad Request",
  401: "Unauthorized",
  403: "Forbidden",
  404: "Not Found",
};

const BASE: Record<string, Ex> = {
  "POST /api/login": {
    req: { email: "<계정 A 이메일>", password: "<비밀번호>" },
    res: { hdr: ["Set-Cookie: session=<세션>; Path=/"], json: { ok: true, user_id: 1024 } },
  },
  "POST /api/register": {
    req: { email: "user@example.com", password: "<비밀번호>", name: "<이름>" },
    res: { code: 201, json: { user_id: "<새 회원 번호>" } },
  },
  "GET /api/logout": { res: { code: 302, hdr: ["Location: /", "Set-Cookie: session=; Max-Age=0"], none: true } },
  "GET /api/profile": { res: { json: { user_id: 1024, email: "<이메일>", name: "<이름>", phone: "<전화번호>" } } },
  "GET /api/users/{id}": { res: { json: { user_id: 1024, name: "<이름>", email: "<이메일>", grade: "일반" } } },
  "PUT /api/users/{id}": { req: { name: "<이름>", address: "<주소>" }, res: { json: { ok: true, user_id: 1024 } } },
  "POST /api/password/reset": { req: { email: "user@example.com" }, res: { json: { sent: true } } },
  "GET /api/cart": {
    res: {
      json: {
        items: [
          { product_id: 12, qty: 1 },
          { product_id: 31, qty: 1 },
        ],
        total: "<금액>",
      },
    },
  },
  "POST /api/cart/add": { req: { product_id: 12, qty: 1 }, res: { json: { ok: true, cart_count: 3 } } },
  "DELETE /api/cart/{id}": { res: { code: 204, none: true } },
  "GET /api/orders": {
    res: {
      json: {
        count: 3,
        orders: [
          { order_id: 1012, status: "배송 중" },
          { order_id: 1013, status: "배송 완료" },
          { order_id: 1014, status: "결제 완료" },
        ],
      },
    },
  },
  "POST /api/orders": {
    req: { cart_id: "<장바구니 번호>", address: "<배송지>" },
    res: { code: 201, json: { order_id: "<새 주문 번호>", status: "결제 대기" } },
  },
  "GET /api/orders/{id}": {
    res: { json: { order_id: 1012, status: "배송 중", items: 2, amount: "<금액>", address: "<배송지>" } },
  },
  "PATCH /api/orders/{id}": { req: { status: "cancelled" }, res: { json: { order_id: 1012, status: "cancelled" } } },
  "POST /api/checkout": {
    req: { order_id: "<주문 번호>", amount: "<금액>", method: "card" },
    res: { json: { payment_id: "<결제 번호>", status: "paid" } },
  },
  "GET /api/coupons": {
    res: {
      json: {
        coupons: [
          { code: "<쿠폰 코드>", rate: 10 },
          { code: "<쿠폰 코드>", rate: 5 },
        ],
      },
    },
  },
  "POST /api/coupons/apply": { req: { code: "<쿠폰 코드>" }, res: { json: { discount_amount: "<할인 금액>" } } },
  "POST /admin/users": { req: { email: "<이메일>", role: "admin" }, res: { code: 403, json: { error: "forbidden" } } },
  "PUT /admin/products/{id}": {
    req: { price: "<가격>", stock: "<재고>" },
    res: { code: 403, json: { error: "forbidden" } },
  },
  "DELETE /admin/delete-user/{id}": { res: { blocked: true } },
  "GET /products": {
    res: {
      html: '<ul class="products">\n  <li><a href="/products/12">…</a></li>\n  <li><a href="/products/31">…</a></li>\n  <!-- 상품 카드 20개 -->\n</ul>',
    },
  },
  "GET /products/{id}": {
    res: { html: '<h1>상품 12</h1>\n<p class="price"><가격></p>\n<script src="/static/js/app.3f2a.js"></script>' },
  },
  "GET /search": { res: { html: "<h2>검색어: ⟦test⟧</h2>\n<p>검색 결과 0건</p>" } },
  "GET /categories": {
    res: {
      html: '<ul class="categories">\n  <li><a href="/products?cat=1">…</a></li>\n  <!-- 카테고리 8개 -->\n</ul>',
    },
  },
  "GET /api/reviews": {
    res: { json: { product_id: 12, count: 7, reviews: [{ author: "<작성자>", rating: 5, body: "<본문>" }] } },
  },
  "POST /api/reviews": {
    req: { product_id: 12, rating: 5, body: "<본문>" },
    res: { code: 201, json: { review_id: "<새 리뷰 번호>" } },
  },
  "GET /": { res: { html: "<!doctype html>\n<title>메인</title>\n<!-- 링크 14개 -->" } },
  "GET /about": { res: { html: "<h1>회사 소개</h1>" } },
  "GET /faq": { res: { html: "<h1>자주 묻는 질문</h1>" } },
  "GET /sitemap.xml": {
    res: {
      ct: "application/xml",
      text: "<urlset>\n  <url><loc>https://{host}/products</loc></url>\n  <url><loc>https://{host}/categories</loc></url>\n  <!-- 경로 21개 -->\n</urlset>",
    },
  },
  "GET /robots.txt": {
    res: { ct: "text/plain", text: "User-agent: *\nDisallow: /admin\nSitemap: https://{host}/sitemap.xml" },
  },
  "GET /health": { res: { json: { status: "ok" } } },
};

// 관리자 화면 다섯 곳은 같은 403 응답 (원본 forEach)
for (const k of ["GET /admin", "GET /admin/stats", "GET /admin/users", "GET /admin/orders", "GET /admin/logs"]) {
  BASE[k] = { res: { code: 403, html: "<h1>403 Forbidden</h1>" } };
}

/** "메서드 경로" → 요청·응답 예시 재료. 원본 EX. */
export const EX: Readonly<Record<string, Ex | undefined>> = BASE;
