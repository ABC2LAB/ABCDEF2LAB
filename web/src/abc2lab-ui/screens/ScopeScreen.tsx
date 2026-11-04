import { type ReactNode, type RefObject, useEffect, useMemo, useRef, useState } from "react";

import { Brand } from "../components/Brand";
import { Screen } from "../components/Screen";
import { MockBanner } from "../components/ui";
import { cx } from "../lib/cx";
import { useGo } from "../nav";
import type { Target } from "../types";

/* ===== 범위 확인 (목업 v51과 같은 구성) =====
 * - 기능 묶음(AI 분류) 안을 메서드별(GET → POST → PUT → PATCH → DELETE)로 나눠 보여준다
 * - 체크 = 점검 포함/제외. 위험한 기본값(삭제·재설정·로그아웃)과 전역 제외는 관찰 사실로 정한다
 * - 체크를 끄면 '비슷한 경로도 뺄까요?' → 제외 규칙으로 저장(지금 목록 + 앞으로 찾을 경로에 적용)
 * - 실행은 하단 버튼으로 분리
 * 엔진 연동 전 시안 데이터. 연동 시 GET /api/assets?type=endpoint&task_id=… 와 /api/traffic 으로 바꾼다.
 */

type Method = "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
type GroupId = "acct" | "order" | "pay" | "admin" | "shop" | "page";

type Endpoint = {
  g: GroupId;
  m: Method;
  p: string;
  /** AI가 추정한 한 줄 설명 */
  d: string;
  /** 관찰: 토큰 없이 보내면 401 */
  auth?: boolean;
  /** 점검 방식의 계정으로 자동 로그인하는 경로 */
  login?: boolean;
  /** 기본 제외 사유 (관찰 사실: 메서드·경로 이름) */
  off?: string;
  /** 전역 제외 경로 패턴 — 설정에서만 바꿀 수 있음 */
  lock?: string;
  st: string;
  from: string;
  n: number;
  /** AI 추정의 근거 */
  basis: string;
};

type Rule = { m: Method; prefix: string };

const GROUPS: { id: GroupId; t: string; d: string }[] = [
  { id: "acct", t: "계정·인증", d: "로그인, 회원가입, 회원 정보" },
  { id: "order", t: "장바구니·주문", d: "담기, 주문 생성·변경" },
  { id: "pay", t: "결제·쿠폰", d: "결제 요청, 쿠폰 적용" },
  { id: "admin", t: "관리자", d: "관리자 전용으로 보이는 기능" },
  { id: "shop", t: "상품·리뷰", d: "상품 조회, 검색, 리뷰" },
  { id: "page", t: "공개 페이지", d: "로그인 없이 보이는 일반 페이지" },
];

const ADMIN_SCRIPT = "관리자 메뉴 스크립트";

const EP: Endpoint[] = [
  {
    g: "acct",
    m: "POST",
    p: "/api/login",
    d: "로그인",
    login: true,
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
    auth: true,
    off: "세션이 끊김",
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
    auth: true,
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
    auth: true,
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
    auth: true,
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
    off: "메일 발송",
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
    auth: true,
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
    auth: true,
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
    auth: true,
    off: "삭제 요청",
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
    auth: true,
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
    auth: true,
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
    auth: true,
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
    auth: true,
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
    auth: true,
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
    auth: true,
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
    auth: true,
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
    auth: true,
    st: "403",
    from: ADMIN_SCRIPT,
    n: 2,
    basis: "경로 이름 admin, 계정 A·B 모두 403",
  },
  {
    g: "admin",
    m: "GET",
    p: "/admin/stats",
    d: "매출 통계",
    auth: true,
    st: "403",
    from: ADMIN_SCRIPT,
    n: 1,
    basis: '스크립트 안 메뉴 이름 "통계"',
  },
  {
    g: "admin",
    m: "GET",
    p: "/admin/users",
    d: "회원 목록",
    auth: true,
    st: "403",
    from: ADMIN_SCRIPT,
    n: 1,
    basis: '스크립트 안 메뉴 이름 "회원 관리"',
  },
  {
    g: "admin",
    m: "POST",
    p: "/admin/users",
    d: "관리자 계정 생성",
    auth: true,
    st: "403",
    from: ADMIN_SCRIPT,
    n: 1,
    basis: "요청 형식에 role 필드",
  },
  {
    g: "admin",
    m: "GET",
    p: "/admin/orders",
    d: "전체 주문 목록",
    auth: true,
    st: "403",
    from: ADMIN_SCRIPT,
    n: 1,
    basis: '스크립트 안 메뉴 이름 "주문 관리"',
  },
  {
    g: "admin",
    m: "PUT",
    p: "/admin/products/{id}",
    d: "상품 정보 수정",
    auth: true,
    st: "403",
    from: ADMIN_SCRIPT,
    n: 1,
    basis: "요청 형식에 price·stock 필드",
  },
  {
    g: "admin",
    m: "GET",
    p: "/admin/logs",
    d: "접속 기록",
    auth: true,
    st: "403",
    from: ADMIN_SCRIPT,
    n: 1,
    basis: '스크립트 안 메뉴 이름 "로그"',
  },
  {
    g: "admin",
    m: "DELETE",
    p: "/admin/delete-user/{id}",
    d: "회원 삭제",
    auth: true,
    lock: "/admin/delete*",
    st: "요청 안 함",
    from: ADMIN_SCRIPT,
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
    auth: true,
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

const METHODS: Method[] = ["GET", "POST", "PUT", "PATCH", "DELETE"];
const CHANGE_METHODS = new Set<Method>(["POST", "PUT", "PATCH", "DELETE"]);
type Filter = "all" | "chg" | "id" | "auth" | "off";
const FILTERS: { f: Filter; t: string }[] = [
  { f: "all", t: "전체" },
  { f: "chg", t: "상태 변경" },
  { f: "id", t: "ID 포함" },
  { f: "auth", t: "로그인 필요" },
  { f: "off", t: "제외됨" },
];

const needsApproval = (e: Endpoint) => CHANGE_METHODS.has(e.m) && !e.login;
const hasId = (e: Endpoint) => e.p.includes("{");
const methodClass = (m: Method) => `m-${m.toLowerCase()}`;
const ruleLabel = (r: Rule) => `${r.m} ${r.prefix}*`;
function patternOf(e: Endpoint): Rule {
  const i = e.p.indexOf("{");
  return { m: e.m, prefix: i >= 0 ? e.p.slice(0, i) : e.p };
}
function ruleHits(r: Rule): number[] {
  return EP.flatMap((e, i) => (e.m === r.m && e.p.startsWith(r.prefix) && !e.lock ? [i] : []));
}
/** 점검 포인트 — AI가 세울 점검 계획 예시 */
function checkPoint(e: Endpoint): string {
  if (e.g === "admin") return "일반 계정(A·B)으로도 열리는지 확인 (수직 권한 상승)";
  if (hasId(e)) return "번호를 다른 사용자 것으로 바꿔도 열리는지 확인 (IDOR)";
  if (e.p === "/search") return "입력값이 화면에 그대로 반사되는지 확인 (XSS)";
  if (e.g === "pay") return "금액·쿠폰 값을 바꿔도 통과되는지 확인 (비즈니스 로직)";
  if (e.login) return "로그인 실패를 반복해도 막히지 않는지 확인 (무차별 대입)";
  return "";
}

/** 묶음 체크박스 상태. 기본 제외·전역 제외는 '켤 수 있는 항목'에서 뺀다 */
function headState(idx: number[], on: boolean[]) {
  const free = idx.filter((i) => !EP[i].lock && !EP[i].off);
  const allOn = free.length > 0 && free.every((i) => on[i]);
  const count = idx.filter((i) => on[i]).length;
  return { allOn, mixed: count > 0 && !allOn, count, free: free.length };
}

/** indeterminate(일부 선택)는 속성이 아니라 DOM 프로퍼티라 ref 로 맞춘다 */
function TriCheck({
  className,
  checked,
  mixed,
  disabled,
  label,
  title,
  onChange,
}: {
  className: string;
  checked: boolean;
  mixed: boolean;
  disabled?: boolean;
  label: string;
  title?: string;
  onChange: (v: boolean) => void;
}) {
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (ref.current) ref.current.indeterminate = mixed;
  }, [mixed]);
  return (
    <input
      ref={ref}
      type="checkbox"
      className={className}
      checked={checked}
      disabled={disabled}
      aria-label={label}
      title={title}
      onChange={(ev) => onChange(ev.target.checked)}
    />
  );
}

function Chevron() {
  return (
    <span className="op-chev" aria-hidden="true">
      <svg
        width="16"
        height="16"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth={2}
        aria-hidden="true"
      >
        <path d="m6 9 6 6 6-6" />
      </svg>
    </span>
  );
}

type ScopeState = {
  on: boolean[];
  byRule: (string | null)[];
  rules: Rule[];
  suggestAt: number | null;
  toggle: (i: number, v: boolean) => void;
  bulk: (idx: number[], turnOn: boolean) => void;
  addRule: (i: number) => void;
  removeRule: (k: number) => void;
  dismissSuggest: () => void;
};

function useScopeState(): ScopeState {
  const [on, setOn] = useState<boolean[]>(() => EP.map((e) => !(e.off || e.lock)));
  const [byRule, setByRule] = useState<(string | null)[]>(() => EP.map(() => null));
  const [rules, setRules] = useState<Rule[]>([]);
  const [suggestAt, setSuggestAt] = useState<number | null>(null);

  const toggle = (i: number, v: boolean) => {
    if (EP[i].lock) return;
    setOn((prev) => prev.map((x, j) => (j === i ? v : x)));
    if (v) setByRule((prev) => prev.map((x, j) => (j === i ? null : x)));
    setSuggestAt(!v && !EP[i].off ? i : null);
  };
  const bulk = (idx: number[], turnOn: boolean) => {
    // 묶음 체크로는 기본 제외(삭제·재설정·로그아웃) 항목을 다시 켜지 않는다
    const target = new Set(idx.filter((i) => !EP[i].lock && !(turnOn && EP[i].off && !on[i])));
    setOn((prev) => prev.map((x, j) => (target.has(j) ? turnOn : x)));
    if (turnOn) setByRule((prev) => prev.map((x, j) => (target.has(j) ? null : x)));
    setSuggestAt(null);
  };
  const addRule = (i: number) => {
    const r = patternOf(EP[i]);
    const label = ruleLabel(r);
    const hits = new Set(ruleHits(r).filter((j) => on[j] || j === i));
    setRules((prev) => (prev.some((x) => ruleLabel(x) === label) ? prev : [...prev, r]));
    setOn((prev) => prev.map((x, j) => (hits.has(j) ? false : x)));
    setByRule((prev) => prev.map((x, j) => (hits.has(j) ? label : x)));
    setSuggestAt(null);
  };
  const removeRule = (k: number) => {
    const label = ruleLabel(rules[k]);
    const back = new Set(byRule.flatMap((x, j) => (x === label ? [j] : [])));
    setRules((prev) => prev.filter((_, j) => j !== k));
    setOn((prev) => prev.map((x, j) => (back.has(j) ? true : x)));
    setByRule((prev) => prev.map((x, j) => (back.has(j) ? null : x)));
  };
  return { on, byRule, rules, suggestAt, toggle, bulk, addRule, removeRule, dismissSuggest: () => setSuggestAt(null) };
}

function rowTags(e: Endpoint, on: boolean, rule: string | null): ReactNode[] {
  const tags: ReactNode[] = [];
  if (e.lock) {
    tags.push(
      <span key="s" className="optag lock">
        전역 제외 · 설정
      </span>,
    );
  } else if (!on) {
    let why = "제외함";
    if (rule) why = `규칙 제외 · ${rule}`;
    else if (e.off) why = `기본 제외 · ${e.off}`;
    tags.push(
      <span key="s" className="optag off">
        {why}
      </span>,
    );
  } else if (e.login) {
    tags.push(
      <span key="s" className="optag">
        로그인용 · 자동
      </span>,
    );
  } else if (needsApproval(e)) {
    tags.push(
      <span key="s" className="optag warn">
        승인 후 전송
      </span>,
    );
  }
  if (hasId(e)) {
    tags.push(
      <span key="id" className="optag">
        ID 포함
      </span>,
    );
  }
  return tags;
}

function stateText(e: Endpoint, on: boolean, rule: string | null): ReactNode {
  if (e.lock) {
    return (
      <>
        전역 제외 경로 <code>{e.lock}</code>에 걸려 요청하지 않습니다. 설정에서만 바꿀 수 있습니다.
      </>
    );
  }
  if (!on) {
    let why: ReactNode = null;
    if (rule) {
      why = (
        <>
          {" "}
          · 제외 규칙 <code>{rule}</code>
        </>
      );
    } else if (e.off) {
      why = ` · 기본 제외 사유: ${e.off}`;
    }
    return (
      <>
        <b>점검에서 제외됨</b>
        {why}
      </>
    );
  }
  if (e.login) {
    return (
      <>
        <b>점검에 포함</b> · 점검 방식에 등록한 계정으로 자동 로그인
      </>
    );
  }
  return (
    <>
      <b>점검에 포함</b>
      {needsApproval(e) && " · 실제 전송은 승인 후"}
    </>
  );
}

function EndpointBody({
  e,
  on,
  rule,
  host,
  onToggle,
}: {
  e: Endpoint;
  on: boolean;
  rule: string | null;
  host: string;
  onToggle: () => void;
}) {
  const go = useGo();
  const point = checkPoint(e);
  return (
    <div className="op-body">
      <div className="op-ctl">
        <span className="op-state">{stateText(e, on, rule)}</span>
        {e.lock ? (
          <button className="btn xs" type="button" onClick={() => go("settings")}>
            통제 정책 열기
          </button>
        ) : (
          <button className="btn xs" type="button" onClick={onToggle}>
            {on ? "제외하기" : "포함하기"}
          </button>
        )}
      </div>
      <div className="op-cols">
        <div className="op-box seen">
          <div className="op-lab">
            <span className="dot" aria-hidden="true" />
            관찰한 정보
          </div>
          <ul>
            <li>
              응답 <b>{e.st}</b>
            </li>
            <li>
              {e.auth ? (
                <>
                  토큰 없이 보내면 401 · <b>로그인 필요</b>
                </>
              ) : (
                "로그인 없이 응답"
              )}
            </li>
            <li>찾은 곳: {e.from}</li>
            <li>{e.n ? `탐색 중 ${e.n}회 관찰` : "전역 제외라 요청하지 않음"}</li>
          </ul>
          <div className="op-lab sm">관찰한 요청 (민감 값 가림)</div>
          <pre className="ev">
            {`${e.m} ${e.p.replace("{id}", "1024")} HTTP/1.1\nHost: ${host}`}
            {e.auth && (
              <>
                {"\nCookie: session="}
                <span className="rd">[REDACTED]</span>
              </>
            )}
            {CHANGE_METHODS.has(e.m) && e.m !== "DELETE" && (
              <>
                {"\n\n"}
                <span className="cm">본문 값은 가림 처리 · 필드 이름만 표시</span>
              </>
            )}
          </pre>
        </div>
        <div className="op-box guess">
          <div className="op-lab">
            <span className="ai">AI 추정</span>
          </div>
          <p>{e.d}</p>
          <p className="op-basis">근거: {e.basis}</p>
          {point && (
            <>
              <div className="op-lab sm">점검 포인트</div>
              <p>{point}</p>
            </>
          )}
        </div>
      </div>
    </div>
  );
}

function EndpointRow({ i, s, host }: { i: number; s: ScopeState; host: string }) {
  const [open, setOpen] = useState(false);
  const e = EP[i];
  const on = s.on[i];
  const rule = s.byRule[i];
  const suggest = s.suggestAt === i;
  const pattern = patternOf(e);
  const more = suggest ? ruleHits(pattern).filter((j) => j !== i && s.on[j]).length : 0;
  return (
    <div className={cx("op", methodClass(e.m), !on && "off")}>
      <div className="op-sum">
        <input
          type="checkbox"
          className="op-chk"
          checked={on}
          disabled={!!e.lock}
          title={e.lock ? "전역 제외 경로라 설정에서만 바꿀 수 있습니다" : undefined}
          aria-label={`${e.m} ${e.p} 점검 포함`}
          onChange={(ev) => s.toggle(i, ev.target.checked)}
        />
        <button type="button" className="op-x" aria-expanded={open} onClick={() => setOpen((v) => !v)}>
          <code className="op-p">{e.p}</code>
          <span className="op-tags">{rowTags(e, on, rule)}</span>
          <Chevron />
        </button>
      </div>
      {suggest && (
        <div className="op-sug" role="status">
          <span>
            비슷한 경로도 뺄까요? <code>{ruleLabel(pattern)}</code> 규칙으로 저장하면{" "}
            {more > 0 && (
              <>
                지금 목록에서 <b>{more}개</b>가 더 빠지고,{" "}
              </>
            )}
            앞으로 찾는 같은 모양의 경로도 자동으로 빠집니다.
          </span>
          <span className="op-sug-b">
            <button type="button" className="btn xs primary" onClick={() => s.addRule(i)}>
              규칙으로 저장
            </button>
            <button type="button" className="btn xs" onClick={s.dismissSuggest}>
              이 경로만
            </button>
          </span>
        </div>
      )}
      {open && <EndpointBody e={e} on={on} rule={rule} host={host} onToggle={() => s.toggle(i, !on)} />}
    </div>
  );
}

function MethodBox({
  gid,
  gt,
  m,
  idx,
  rows,
  s,
  host,
  forceOpen,
}: {
  gid: GroupId;
  gt: string;
  m: Method;
  idx: number[];
  rows: number[];
  s: ScopeState;
  host: string;
  forceOpen: boolean;
}) {
  const [closed, setClosed] = useState(false);
  const hs = headState(idx, s.on);
  const open = !closed || forceOpen;
  return (
    <div className={cx("opm", methodClass(m))} data-group={gid}>
      <div className="opm-h">
        <TriCheck
          className="opm-chk"
          checked={hs.allOn}
          mixed={hs.mixed}
          disabled={hs.free === 0}
          label={`${gt} ${m} 전체 포함`}
          title={
            hs.free === 0
              ? "기본 제외 항목이라 한 줄씩 직접 켜야 합니다"
              : "기본 제외 항목은 이 체크로 다시 켜지지 않습니다"
          }
          onChange={() => s.bulk(idx, !hs.allOn)}
        />
        <button type="button" className="opm-x" aria-expanded={open} onClick={() => setClosed((v) => !v)}>
          <span className="op-m">{m}</span>
          <span className="opm-c" title={`${idx.length}개 중 ${hs.count}개 포함`}>
            {hs.count}/{idx.length}
          </span>
          <Chevron />
        </button>
      </div>
      {open && (
        <div className="opm-b">
          {rows.map((i) => (
            <EndpointRow key={i} i={i} s={s} host={host} />
          ))}
        </div>
      )}
    </div>
  );
}

function ScopeList({ s, host }: { s: ScopeState; host: string }) {
  const [filter, setFilter] = useState<Filter>("all");
  const [query, setQuery] = useState("");
  const [openG, setOpenG] = useState<Partial<Record<GroupId, boolean>>>({});
  const q = query.trim().toLowerCase();
  const filtering = filter !== "all" || q !== "";

  const visible = (i: number) => {
    const e = EP[i];
    if (q && !`${e.m} ${e.p} ${e.d}`.toLowerCase().includes(q)) return false;
    if (filter === "chg") return CHANGE_METHODS.has(e.m);
    if (filter === "id") return hasId(e);
    if (filter === "auth") return !!e.auth;
    if (filter === "off") return !s.on[i];
    return true;
  };
  const counts: Record<Filter, number> = {
    all: EP.length,
    chg: EP.filter((e) => CHANGE_METHODS.has(e.m)).length,
    id: EP.filter(hasId).length,
    auth: EP.filter((e) => e.auth).length,
    off: s.on.filter((v) => !v).length,
  };
  const allOpen = GROUPS.every((g) => openG[g.id]);
  const groups = GROUPS.map((g) => {
    const idx = EP.flatMap((e, i) => (e.g === g.id ? [i] : []));
    return { g, idx, vis: idx.filter(visible) };
  }).filter((x) => x.vis.length > 0);

  return (
    <div className="card ep-card">
      <div className="ep-tools">
        <input
          className="inp sm"
          type="search"
          placeholder="경로나 설명 검색"
          aria-label="엔드포인트 검색"
          autoComplete="off"
          spellCheck={false}
          value={query}
          onChange={(ev) => setQuery(ev.target.value)}
        />
        <fieldset className="ep-chips" aria-label="엔드포인트 걸러 보기">
          {FILTERS.map(({ f, t }) => (
            <button key={f} type="button" className="ep-chip" aria-pressed={filter === f} onClick={() => setFilter(f)}>
              {t}
              <span className="c">{counts[f]}</span>
            </button>
          ))}
        </fieldset>
        <button
          type="button"
          className="scope-link"
          onClick={() => setOpenG(Object.fromEntries(GROUPS.map((g) => [g.id, !allOpen])))}
        >
          {allOpen ? "모두 접기" : "모두 펼치기"}
        </button>
      </div>
      <p className="ep-legend">
        <span className="ai">AI 추정</span> 묶음과 경로 설명은 AI가 추정한 내용입니다. 펼치면 관찰한 정보와 나눠 볼 수
        있고, 포함 여부와 승인 필요 여부는 메서드·경로처럼 <strong>관찰한 사실</strong>로만 정합니다.
      </p>
      {groups.length === 0 && <p className="op-empty">조건에 맞는 엔드포인트가 없습니다.</p>}
      {groups.map(({ g, idx, vis }) => {
        const hs = headState(idx, s.on);
        const open = !!openG[g.id] || filtering;
        return (
          <section key={g.id} className="opg" aria-label={g.t}>
            <div className="opg-h">
              <TriCheck
                className="opg-chk"
                checked={hs.allOn}
                mixed={hs.mixed}
                label={`${g.t} 묶음 전체 포함`}
                title="기본 제외 항목은 묶음 체크로 다시 켜지지 않습니다"
                onChange={() => s.bulk(idx, !hs.allOn)}
              />
              <button
                type="button"
                className="opg-x"
                aria-expanded={open}
                onClick={() => setOpenG((prev) => ({ ...prev, [g.id]: !prev[g.id] }))}
              >
                <span className="opg-t">{g.t}</span>
                <span className="opg-d">{g.d}</span>
                <span className="opg-c" title={`${idx.length}개 중 ${hs.count}개 포함`}>
                  {hs.count}/{idx.length}
                </span>
                <Chevron />
              </button>
            </div>
            {open && (
              <div className="opg-b">
                {METHODS.map((m) => {
                  const rows = vis.filter((i) => EP[i].m === m);
                  if (rows.length === 0) return null;
                  return (
                    <MethodBox
                      key={m}
                      gid={g.id}
                      gt={g.t}
                      m={m}
                      idx={idx.filter((i) => EP[i].m === m)}
                      rows={rows}
                      s={s}
                      host={host}
                      forceOpen={filtering}
                    />
                  );
                })}
              </div>
            )}
          </section>
        );
      })}
    </div>
  );
}

const RULE_STEPS: { cond: ReactNode; tag: string; cls: string }[] = [
  { cond: "허용한 주소 밖", tag: "요청 안 함 · 기록만", cls: "out" },
  { cond: "아래 제외 규칙과 같은 모양", tag: "자동 제외", cls: "ex" },
  {
    cond: (
      <>
        <span className="op-m m-get">GET</span> 보기만 하는 요청
      </>
    ),
    tag: "자동 포함",
    cls: "in",
  },
  {
    cond: (
      <>
        <span className="op-m m-post">POST</span>
        <span className="op-m m-put">PUT</span>
        <span className="op-m m-patch">PATCH</span> 데이터를 바꾸는 요청
      </>
    ),
    tag: "포함 · 보낼 때 승인",
    cls: "ap",
  },
  {
    cond: (
      <>
        <span className="op-m m-delete">DELETE</span> 지우기 · 재설정 · 로그아웃
      </>
    ),
    tag: "자동 제외 · 사유 표시",
    cls: "ex",
  },
  { cond: "관리자 경로 · 처음 보는 권한 영역", tag: "확인 요청", cls: "chk" },
];

function RulesCard({ s, cardRef }: { s: ScopeState; cardRef: RefObject<HTMLDivElement | null> }) {
  return (
    <div className="card rules-card" ref={cardRef}>
      <h3>
        새로 찾은 경로 처리 규칙{" "}
        <span className="rules-h3sub">추가 탐색 중 새 경로가 나오면 위에서부터 순서대로 확인해 자동 처리합니다</span>
      </h3>
      <ol className="rules">
        {RULE_STEPS.map((r) => (
          <li key={r.tag + r.cls}>
            <span className="rc">{r.cond}</span>
            <span className={cx("dtag", r.cls)}>{r.tag}</span>
          </li>
        ))}
      </ol>
      <div className="rules-sub">
        제외 규칙 <span className="hint">목록에서 체크를 끄면 '규칙으로 저장'을 고를 수 있습니다</span>
      </div>
      <div className="rchips">
        <span className="rchip lock">
          <code>/admin/delete*</code> 전역 · 설정에서 관리
        </span>
        {s.rules.map((r, k) => (
          <span key={ruleLabel(r)} className="rchip">
            <code>{ruleLabel(r)}</code>
            <button
              type="button"
              className="rchip-x"
              aria-label={`${ruleLabel(r)} 규칙 삭제`}
              onClick={() => s.removeRule(k)}
            >
              ×
            </button>
          </span>
        ))}
      </div>
    </div>
  );
}

export default function ScopeScreen({ target }: { target: Target }) {
  const go = useGo();
  const s = useScopeState();
  const rulesRef = useRef<HTMLDivElement>(null);
  const sum = useMemo(() => {
    const inc = s.on.filter(Boolean).length;
    const ap = EP.filter((e, i) => s.on[i] && needsApproval(e)).length;
    return { inc, exc: EP.length - inc, ap };
  }, [s.on]);

  return (
    <Screen id="scope">
      <div className="frame">
        <div className="top">
          <div style={{ minWidth: 0 }}>
            <Brand style={{ fontSize: 16 }}>ABC2LAB</Brand>
            <div style={{ fontSize: 13, color: "var(--muted)", marginTop: 3 }}>
              점검 대상 · <span className="t-url">{target.url}</span>
            </div>
          </div>
          <span className="badge warn" style={{ cursor: "default" }}>
            ⏸ 탐색 완료 · 확인 대기
          </span>
        </div>
        <div style={{ padding: "22px 24px 0" }}>
          <MockBanner>
            범위 확인 화면은 시안이에요. 엔드포인트 목록과 관찰 정보는 엔진 연동 후 실데이터로 바뀌어요.
          </MockBanner>
          <h1 className="big">범위 확인</h1>
          <p className="lead">1차 탐색에서 찾은 엔드포인트입니다. 점검할 대상을 고른 뒤 추가 탐색을 시작하세요.</p>
          <div className="dgrid" style={{ marginBottom: 20 }}>
            <div className="tile">
              <div className="k">엔드포인트</div>
              <div className="v">{EP.length}</div>
            </div>
            <div className="tile">
              <div className="k">로그인 영역</div>
              <div className="v" style={{ fontSize: 18 }}>
                확인됨
              </div>
            </div>
            <div className="tile">
              <div className="k">승인 후 전송</div>
              <div className="v">{sum.ap}</div>
            </div>
          </div>
          <ScopeList s={s} host={target.host} />
          <RulesCard s={s} cardRef={rulesRef} />
          <div className="scope-bar" aria-live="polite">
            <div className="sb-sum">
              <div className="sb-n">
                포함 <b>{sum.inc}</b> · 제외 <b>{sum.exc}</b> · 승인 필요 <b>{sum.ap}</b>
              </div>
              <div className="sb-pol">
                추가 탐색 중 새로 찾은 경로는 처리 규칙대로 자동 분류하고, 민감한 경로만 확인을 요청합니다{" "}
                <button
                  type="button"
                  className="scope-link"
                  onClick={() => rulesRef.current?.scrollIntoView({ block: "center", behavior: "smooth" })}
                >
                  규칙 보기
                </button>
              </div>
            </div>
            <div className="row" style={{ gap: 8, flex: "none" }}>
              <button className="btn" type="button" onClick={() => go("entry")}>
                점검 취소
              </button>
              <button className="btn primary" type="button" onClick={() => go("live")}>
                선택한 범위로 추가 탐색
              </button>
            </div>
          </div>
        </div>
      </div>
    </Screen>
  );
}
