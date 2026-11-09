// 범위 확인 엔드포인트 목록. 범위 화면 목록과 자산 탐색의 API 상세가 함께 쓴다.
// 포함 여부(on)는 화면에서 바뀌므로 여기에는 기본값(defaultOn)만 두고, 바뀐 값은 스토어 scopeOn 에 둔다.

import { type Kind, kind } from "../lib/kind";
import type { Group, Method } from "./crawl";

interface EpSeed {
  g: Group;
  m: Method;
  p: string;
  /** 기능 (AI 추정) */
  d: string;
  /** 로그인 필요 */
  auth?: 1;
  /** 관찰한 응답 */
  st: string;
  /** 찾은 곳 */
  from: string;
  /** 요청 횟수 */
  n: number;
  /** 근거 */
  basis: string;
}

/** 엔드포인트 하나. 원본 EP 항목 + 원본이 바로 덧붙이던 파생 값. */
export interface Endpoint extends Readonly<EpSeed> {
  /** EP 안 위치 (원본 e.i — 선택·체크 상자 키) */
  readonly i: number;
  /** kind(m,p) */
  readonly k: Kind;
  /** 전역 제외 규칙에 걸림 → 체크 상자 비활성 */
  readonly lock: boolean;
  /** 처음 포함 여부. 실제 값은 스토어의 `epOn(e, scopeOn)` 으로 읽는다(원본 e.on). */
  readonly defaultOn: boolean;
  /** 경로에 {param} 이 있음 */
  readonly hasId: boolean;
  /** 데이터를 바꾸는 요청 (GET 아님, 로그인 제외) */
  readonly chg: boolean;
}

const SEEDS: EpSeed[] = [
  {
    g: "acct",
    m: "POST",
    p: "/api/login",
    d: "로그인",
    st: "200 / 401",
    from: "/login 화면",
    n: 6,
    basis: "요청 본문에 email·password, 응답에 토큰",
  },
  {
    g: "acct",
    m: "POST",
    p: "/api/register",
    d: "회원가입",
    st: "201",
    from: "/register 화면",
    n: 2,
    basis: "요청 본문에 email·password·이름",
  },
  {
    g: "acct",
    m: "GET",
    p: "/api/logout",
    d: "로그아웃",
    auth: 1,
    st: "302",
    from: "상단 메뉴",
    n: 1,
    basis: "경로 이름 logout, 응답 뒤 세션 쿠키 삭제",
  },
  {
    g: "acct",
    m: "GET",
    p: "/api/profile",
    d: "내 정보 조회",
    auth: 1,
    st: "200 / 401",
    from: "/mypage 화면",
    n: 9,
    basis: "응답에 email·이름·전화번호",
  },
  {
    g: "acct",
    m: "GET",
    p: "/api/users/{id}",
    d: "회원 정보 조회",
    auth: 1,
    st: "200 / 401",
    from: "/mypage 화면",
    n: 4,
    basis: "경로에 회원 번호, 응답에 회원 정보",
  },
  {
    g: "acct",
    m: "PUT",
    p: "/api/users/{id}",
    d: "회원 정보 수정",
    auth: 1,
    st: "200",
    from: "/mypage/edit 화면",
    n: 1,
    basis: "요청 본문에 이름·주소",
  },
  {
    g: "acct",
    m: "POST",
    p: "/api/password/reset",
    d: "비밀번호 재설정 메일 발송",
    st: "200",
    from: "/login 화면",
    n: 1,
    basis: "경로 이름 reset, 요청 본문에 email",
  },
  {
    g: "order",
    m: "GET",
    p: "/api/cart",
    d: "장바구니 조회",
    auth: 1,
    st: "200 / 401",
    from: "/cart 화면",
    n: 8,
    basis: "응답에 상품·수량 목록",
  },
  {
    g: "order",
    m: "POST",
    p: "/api/cart/add",
    d: "장바구니 담기",
    auth: 1,
    st: "200",
    from: "상품 상세",
    n: 3,
    basis: "요청 본문에 상품 번호·수량",
  },
  {
    g: "order",
    m: "DELETE",
    p: "/api/cart/{id}",
    d: "장바구니 항목 삭제",
    auth: 1,
    st: "204",
    from: "/cart 화면",
    n: 1,
    basis: "메서드 DELETE, 경로에 항목 번호",
  },
  {
    g: "order",
    m: "GET",
    p: "/api/orders",
    d: "내 주문 목록",
    auth: 1,
    st: "200 / 401",
    from: "/orders 화면",
    n: 5,
    basis: "응답에 주문 번호·금액·상태",
  },
  {
    g: "order",
    m: "POST",
    p: "/api/orders",
    d: "주문 생성",
    auth: 1,
    st: "201",
    from: "/checkout 화면",
    n: 1,
    basis: "요청 본문에 장바구니 번호·배송지",
  },
  {
    g: "order",
    m: "GET",
    p: "/api/orders/{id}",
    d: "주문 상세",
    auth: 1,
    st: "200 / 403",
    from: "/orders 화면",
    n: 6,
    basis: "경로에 주문 번호, 응답에 배송지",
  },
  {
    g: "order",
    m: "PATCH",
    p: "/api/orders/{id}",
    d: "주문 변경 (배송지·취소)",
    auth: 1,
    st: "200",
    from: "주문 상세",
    n: 1,
    basis: "요청 본문에 status·address",
  },
  {
    g: "pay",
    m: "POST",
    p: "/api/checkout",
    d: "결제",
    auth: 1,
    st: "200",
    from: "/checkout 화면",
    n: 1,
    basis: "요청 본문에 금액·결제 수단",
  },
  {
    g: "pay",
    m: "GET",
    p: "/api/coupons",
    d: "보유 쿠폰 조회",
    auth: 1,
    st: "200",
    from: "/checkout 화면",
    n: 2,
    basis: "응답에 쿠폰 코드·할인율",
  },
  {
    g: "pay",
    m: "POST",
    p: "/api/coupons/apply",
    d: "쿠폰 적용",
    auth: 1,
    st: "200 / 400",
    from: "/checkout 화면",
    n: 2,
    basis: "요청 본문에 쿠폰 코드, 응답에 할인 금액",
  },
  {
    g: "admin",
    m: "GET",
    p: "/admin",
    d: "관리자 메인",
    auth: 1,
    st: "403",
    from: "관리자 메뉴 스크립트",
    n: 2,
    basis: "경로 이름 admin, 계정 A·B 모두 403",
  },
  {
    g: "admin",
    m: "GET",
    p: "/admin/stats",
    d: "매출 통계",
    auth: 1,
    st: "403",
    from: "관리자 메뉴 스크립트",
    n: 1,
    basis: '스크립트 안 메뉴 이름 "통계"',
  },
  {
    g: "admin",
    m: "GET",
    p: "/admin/users",
    d: "회원 목록",
    auth: 1,
    st: "403",
    from: "관리자 메뉴 스크립트",
    n: 1,
    basis: '스크립트 안 메뉴 이름 "회원 관리"',
  },
  {
    g: "admin",
    m: "POST",
    p: "/admin/users",
    d: "관리자 계정 생성",
    auth: 1,
    st: "403",
    from: "관리자 메뉴 스크립트",
    n: 1,
    basis: "요청 형식에 role 필드",
  },
  {
    g: "admin",
    m: "GET",
    p: "/admin/orders",
    d: "전체 주문 목록",
    auth: 1,
    st: "403",
    from: "관리자 메뉴 스크립트",
    n: 1,
    basis: '스크립트 안 메뉴 이름 "주문 관리"',
  },
  {
    g: "admin",
    m: "PUT",
    p: "/admin/products/{id}",
    d: "상품 정보 수정",
    auth: 1,
    st: "403",
    from: "관리자 메뉴 스크립트",
    n: 1,
    basis: "요청 형식에 price·stock 필드",
  },
  {
    g: "admin",
    m: "GET",
    p: "/admin/logs",
    d: "접속 기록",
    auth: 1,
    st: "403",
    from: "관리자 메뉴 스크립트",
    n: 1,
    basis: '스크립트 안 메뉴 이름 "로그"',
  },
  {
    g: "admin",
    m: "DELETE",
    p: "/admin/delete-user/{id}",
    d: "회원 삭제",
    auth: 1,
    st: "요청 안 함",
    from: "관리자 메뉴 스크립트",
    n: 0,
    basis: "경로 이름 delete-user",
  },
  {
    g: "shop",
    m: "GET",
    p: "/products",
    d: "상품 목록",
    st: "200",
    from: "메인 화면",
    n: 14,
    basis: "응답이 상품 카드 목록",
  },
  {
    g: "shop",
    m: "GET",
    p: "/products/{id}",
    d: "상품 상세",
    st: "200 / 404",
    from: "상품 목록",
    n: 21,
    basis: "경로에 상품 번호, 응답에 가격·재고",
  },
  {
    g: "shop",
    m: "GET",
    p: "/search",
    d: "상품 검색",
    st: "200",
    from: "상단 검색창",
    n: 5,
    basis: "q 값이 결과 화면에 그대로 표시됨",
  },
  {
    g: "shop",
    m: "GET",
    p: "/categories",
    d: "카테고리 목록",
    st: "200",
    from: "메인 화면",
    n: 3,
    basis: "응답이 카테고리 이름 목록",
  },
  {
    g: "shop",
    m: "GET",
    p: "/api/reviews",
    d: "리뷰 목록",
    st: "200",
    from: "상품 상세",
    n: 7,
    basis: "응답에 작성자·별점·본문",
  },
  {
    g: "shop",
    m: "POST",
    p: "/api/reviews",
    d: "리뷰 작성",
    auth: 1,
    st: "201 / 401",
    from: "상품 상세",
    n: 1,
    basis: "요청 본문에 별점·본문",
  },
  { g: "page", m: "GET", p: "/", d: "메인 화면", st: "200", from: "첫 접속", n: 4, basis: "첫 화면 HTML" },
  { g: "page", m: "GET", p: "/about", d: "회사 소개", st: "200", from: "하단 메뉴", n: 1, basis: "정적 HTML" },
  { g: "page", m: "GET", p: "/faq", d: "자주 묻는 질문", st: "200", from: "하단 메뉴", n: 1, basis: "정적 HTML" },
  {
    g: "page",
    m: "GET",
    p: "/sitemap.xml",
    d: "사이트맵",
    st: "200",
    from: "robots.txt",
    n: 1,
    basis: "XML 경로 목록",
  },
  {
    g: "page",
    m: "GET",
    p: "/robots.txt",
    d: "검색 로봇 규칙",
    st: "200",
    from: "첫 접속",
    n: 1,
    basis: "Disallow: /admin 줄",
  },
  {
    g: "page",
    m: "GET",
    p: "/health",
    d: "서버 상태 확인",
    st: "200",
    from: "robots.txt",
    n: 1,
    basis: '응답 {"status":"ok"}',
  },
];

/** 엔드포인트 37개 (원본 EP + 파생 값). */
export const EP: readonly Endpoint[] = SEEDS.map((e, i) => {
  const k = kind(e.m, e.p);
  return {
    ...e,
    i,
    k,
    lock: k === "lock",
    defaultOn: !(k === "off" || k === "lock"),
    hasId: e.p.indexOf("{") >= 0,
    chg: e.m !== "GET" && k !== "login",
  };
});

/** 범위 화면의 묶음 순서. */
export const SG: readonly Group[] = ["acct", "order", "pay", "admin", "shop", "page"];

/** "메서드 경로" 로 엔드포인트 찾기 (자산 탐색의 API 상세 열기용, 원본 cApiOpen). */
export function epByKey(key: string): Endpoint | undefined {
  return EP.find((x) => `${x.m} ${x.p}` === key);
}

/** 지금 포함 여부. 원본 `e.on` — 화면에서 바꾼 값(scopeOn)이 있으면 그 값. */
export function epOn(e: Endpoint, scopeOn: Readonly<Record<number, boolean>>): boolean {
  return scopeOn[e.i] ?? e.defaultOn;
}
