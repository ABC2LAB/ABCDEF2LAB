// 자산 지식 그래프: 역할 → 엔드포인트 → 자원, 관찰한 소유·권한 관계. 원본 KGN · KGE · KGP · KX/KW/KH · STL · STC,
// 그리고 좌표 계산 kbox · kcurve · kname · kstate (보고서의 그래프와 노드 상세가 함께 쓴다).

import type { Method } from "./crawl";
import type { FindingId } from "./findings";

/** 노드 종류 = 열: role 계정·역할 · ep 엔드포인트 · res 자원 (CSS 클래스 `kg-n ${t}`) */
export type KgType = "role" | "ep" | "res";

export type KgNode =
  | {
      readonly id: string;
      readonly t: "role";
      /** 노드 가운데 y */
      readonly y: number;
      readonly label: string;
      /** ' · ' 앞부분만 그래프에 보인다 */
      readonly sub: string;
      readonly desc: string;
    }
  | { readonly id: string; readonly t: "ep"; readonly y: number; readonly m: Method; readonly p: string }
  | { readonly id: string; readonly t: "res"; readonly y: number; readonly label: string; readonly sub: string };

export const KGN: readonly KgNode[] = [
  {
    id: "rA",
    t: "role",
    y: 110,
    label: "계정 A",
    sub: "일반 사용자 · 테스트 계정",
    desc: "주문 1012와 프로필 1024의 주인입니다. 대조군으로 자기 자원을 조회했습니다.",
  },
  {
    id: "rB",
    t: "role",
    y: 215,
    label: "계정 B",
    sub: "일반 사용자 · 비교용",
    desc: "계정 A의 자원에 접근되는지 비교하려고 만든 두 번째 테스트 계정입니다.",
  },
  {
    id: "rAnon",
    t: "role",
    y: 340,
    label: "비로그인",
    sub: "게스트 · 세션 없음",
    desc: "세션 쿠키 없이 보낸 요청입니다. 인증이 동작하는지 보는 대조군입니다.",
  },
  {
    id: "rAdm",
    t: "role",
    y: 440,
    label: "관리자",
    sub: "관리자 권한",
    desc: "관리자 기능이 원래 열려야 하는 역할입니다. 비교 기준으로 씁니다.",
  },
  { id: "eLogin", t: "ep", y: 60, m: "POST", p: "/api/login" },
  { id: "eOrder", t: "ep", y: 125, m: "GET", p: "/api/orders/{id}" },
  { id: "eUserGet", t: "ep", y: 190, m: "GET", p: "/api/users/{id}" },
  { id: "eUserPut", t: "ep", y: 255, m: "PUT", p: "/api/users/{id}" },
  { id: "eCheckout", t: "ep", y: 320, m: "POST", p: "/api/checkout" },
  { id: "eAdmin", t: "ep", y: 385, m: "GET", p: "/admin" },
  { id: "eStats", t: "ep", y: 450, m: "GET", p: "/admin/stats" },
  { id: "dCookie", t: "res", y: 60, label: "세션 쿠키", sub: "로그인 때 발급" },
  { id: "dO1012", t: "res", y: 115, label: "주문 1012", sub: "계정 A 소유 · 배송지 포함" },
  { id: "dO2033", t: "res", y: 168, label: "주문 2033", sub: "계정 B 소유" },
  { id: "dP1024", t: "res", y: 222, label: "프로필 1024", sub: "계정 A 소유 · 연락처 포함" },
  { id: "dPay", t: "res", y: 320, label: "결제 금액", sub: "주문 결제 요청 본문" },
  { id: "dStats", t: "res", y: 450, label: "매출 통계", sub: "관리자 전용 데이터" },
];

/** 관계 상태: ok 허용 · block 차단 · vuln 취약 · sus 의심 · untested 미검사 · own 소유 (CSS 클래스 `e-${st}` · `ah ${st}`) */
export type KgState = "ok" | "block" | "vuln" | "sus" | "untested" | "own";

/** [출발, 도착, 상태, 관계, 발견] */
export type KgEdge = readonly [from: string, to: string, st: KgState, rel: string, f?: FindingId];

export const KGE: readonly KgEdge[] = [
  ["rA", "eLogin", "ok", "로그인"],
  ["rB", "eLogin", "ok", "로그인"],
  ["eLogin", "dCookie", "vuln", "발급 · HttpOnly 없음", "f4"],
  ["rA", "eOrder", "ok", "자기 주문 조회 · 200"],
  ["rB", "eOrder", "vuln", "A의 주문 조회 · 200", "f1"],
  ["rAnon", "eOrder", "block", "302 · 로그인으로 이동"],
  ["eOrder", "dO1012", "ok", "반환"],
  ["eOrder", "dO2033", "ok", "반환"],
  ["rB", "eUserGet", "sus", "A의 프로필 일부 수신", "f3"],
  ["eUserGet", "dP1024", "ok", "반환"],
  ["rB", "eUserPut", "untested", "보내지 않음 · 승인 필요", "f3"],
  ["eUserPut", "dP1024", "untested", "변경"],
  ["rA", "eCheckout", "untested", "승인 대기 · 보내지 않음", "f5"],
  ["eCheckout", "dPay", "untested", "변경"],
  ["rA", "eAdmin", "block", "403"],
  ["rB", "eAdmin", "block", "403"],
  ["rAdm", "eAdmin", "ok", "허용"],
  ["rA", "eStats", "block", "403"],
  ["rAnon", "eStats", "vuln", "세션 없이 200", "f2"],
  ["rAdm", "eStats", "ok", "허용"],
  ["eStats", "dStats", "ok", "반환"],
  ["rA", "dO1012", "own", "소유"],
  ["rB", "dO2033", "own", "소유"],
  ["rA", "dP1024", "own", "소유"],
];

/** 확인된 공격 경로 (e: 강조할 관계 [출발, 도착]) */
export interface KgPath {
  readonly id: string;
  readonly f: FindingId;
  readonly label: string;
  readonly sub: string;
  readonly e: ReadonlyArray<readonly [string, string]>;
}

export const KGP: readonly KgPath[] = [
  {
    id: "p1",
    f: "f1",
    label: "계정 B → 주문 1012",
    sub: "수평 IDOR · 계정 A 소유 주문이 열림",
    e: [
      ["rB", "eOrder"],
      ["eOrder", "dO1012"],
      ["rA", "dO1012"],
    ],
  },
  {
    id: "p2",
    f: "f2",
    label: "비로그인 → 매출 통계",
    sub: "수직 권한 상승 · 세션 없이 200",
    e: [
      ["rAnon", "eStats"],
      ["eStats", "dStats"],
    ],
  },
  {
    id: "p3",
    f: "f3",
    label: "계정 B → 프로필 1024",
    sub: "의심 · 수정 요청은 아직 보내지 않음",
    e: [
      ["rB", "eUserGet"],
      ["eUserGet", "dP1024"],
      ["rB", "eUserPut"],
      ["eUserPut", "dP1024"],
      ["rA", "dP1024"],
    ],
  },
];

/** 열 가운데 x · 노드 너비 · 노드 높이 */
export const KX: Readonly<Record<KgType, number>> = { role: 95, ep: 410, res: 738 };
export const KW: Readonly<Record<KgType, number>> = { role: 150, ep: 226, res: 172 };
export const KH: Readonly<Record<KgType, number>> = { role: 46, ep: 40, res: 46 };

export const KG_BY_ID: Readonly<Record<string, KgNode>> = Object.fromEntries(KGN.map((n) => [n.id, n]));

/** 관계 상태 이름 */
export const STL: Readonly<Record<KgState, string>> = {
  ok: "허용",
  block: "차단",
  vuln: "취약",
  sus: "의심",
  untested: "미검사",
  own: "소유",
};
/** 관계 상태 → 접근 딱지(`ac`) 클래스 */
export const STC: Readonly<Record<KgState, string>> = {
  ok: "al",
  block: "bl",
  vuln: "vu",
  sus: "su",
  untested: "nt",
  own: "ow",
};

/** 노드 상자. 원본 `kbox(n)`. */
export function kbox(n: KgNode): { x: number; y: number; w: number; h: number } {
  const w = KW[n.t];
  const h = KH[n.t];
  return { x: KX[n.t] - w / 2, y: n.y - h / 2, w, h };
}

/** 노드 이름 (aria-label). 원본 `kname(n)`. */
export function kname(n: KgNode): string {
  return n.t === "ep" ? `${n.m} ${n.p}` : n.label;
}

/** a 오른쪽 끝 → b 왼쪽(화살촉 자리 7 앞) 3차 곡선. at(t) 는 곡선 위 점 (발견 표시 자리). 원본 `kcurve(a,b)`. */
export function kcurve(a: KgNode, b: KgNode): { d: string; at: (t: number) => { x: number; y: number } } {
  const A = kbox(a);
  const B = kbox(b);
  const x1 = A.x + A.w;
  const y1 = a.y;
  const x2 = B.x - 7;
  const y2 = b.y;
  const d = (x2 - x1) / 2;
  return {
    d: `M${x1} ${y1} C${x1 + d} ${y1} ${x2 - d} ${y2} ${x2} ${y2}`,
    at: (t) => {
      const u = 1 - t;
      const cx1 = x1 + d;
      const cx2 = x2 - d;
      return {
        x: u * u * u * x1 + 3 * u * u * t * cx1 + 3 * u * t * t * cx2 + t * t * t * x2,
        y: u * u * u * y1 + 3 * u * u * t * y1 + 3 * u * t * t * y2 + t * t * t * y2,
      };
    },
  };
}

/** 노드 테두리 상태: 소유를 뺀 관계 중 취약 > 의심 > 미검사. 원본 `kstate(id)` (CSS 클래스 이름, 없으면 ""). */
export function kstate(id: string): "n-vuln" | "n-sus" | "n-unt" | "" {
  const st = KGE.filter((e) => (e[0] === id || e[1] === id) && e[2] !== "own").map((e) => e[2]);
  if (st.includes("vuln")) return "n-vuln";
  if (st.includes("sus")) return "n-sus";
  if (st.includes("untested")) return "n-unt";
  return "";
}

/** 이 노드가 닿은 관계 (KGE 위치와 함께). */
export function krels(id: string): Array<{ e: KgEdge; i: number }> {
  return KGE.flatMap((e, i) => (e[0] === id || e[1] === id ? [{ e, i }] : []));
}

/** 상태별 관계 수 (범례). 원본 `kgCount(st)`. */
export function kgCount(st: KgState): number {
  return KGE.filter((e) => e[2] === st).length;
}

/** 그래프에서 고른 것: 노드 · 발견 · 공격 경로. 원본 kgSel. */
export type KgSel = { readonly type: "node" | "f" | "path"; readonly id: string } | null;

/**
 * 강조할 노드·관계. 원본 `kgApply()` 의 계산 부분:
 * 노드면 그 노드와 닿은 관계 전부, 경로면 그 경로의 관계, 발견이면 그 발견이 붙은 관계 + 그 발견의 경로.
 */
export function kgHighlight(sel: KgSel): { nodes: ReadonlySet<string>; edges: ReadonlySet<number> } {
  const nodes = new Set<string>();
  const edges = new Set<number>();
  if (!sel) return { nodes, edges };
  if (sel.type === "node") {
    nodes.add(sel.id);
    KGE.forEach((e, i) => {
      if (e[0] === sel.id || e[1] === sel.id) {
        edges.add(i);
        nodes.add(e[0]);
        nodes.add(e[1]);
      }
    });
    return { nodes, edges };
  }
  let pairs: Array<readonly [string, string]> = [];
  if (sel.type === "path") pairs = [...(KGP.find((p) => p.id === sel.id)?.e ?? [])];
  else {
    for (const e of KGE) if (e[4] === sel.id) pairs.push([e[0], e[1]]);
    for (const p of KGP) if (p.f === sel.id) pairs = pairs.concat(p.e);
  }
  KGE.forEach((e, i) => {
    for (const pr of pairs) {
      if (pr[0] === e[0] && pr[1] === e[1]) {
        edges.add(i);
        nodes.add(e[0]);
        nodes.add(e[1]);
      }
    }
  });
  return { nodes, edges };
}
