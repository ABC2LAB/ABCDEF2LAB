// 발견 6건 (보고서 · 점검 진행 공통). 원본 F.
// 원본 문자열 안의 <code> 는 HTML 파서에 맡기지 않도록 여기서 JSX(ReactNode)로 바꿨다(ev · fix).

import type { ReactNode } from "react";

import type { Method } from "./crawl";

/** 위험도 키 (CSS 클래스 `sev ${k}` 와 `--${k}` 색 토큰) */
export type SevKey = "crit" | "hi" | "mid" | "lo";
/** 위험도 이름. 원본 SEVL. */
export const SEVL: Readonly<Record<SevKey, string>> = { crit: "심각", hi: "높음", mid: "중간", lo: "낮음" };

/** 최종 판정 */
export type Verdict = "확정" | "의심" | "판단 불가";
/** 점검 진행 중 판정 (검증 전이면 '판정 중') */
export type LiveVerdict = Verdict | "판정 중";

export type FindingId = "f1" | "f2" | "f3" | "f4" | "f5" | "f6";

export interface Finding {
  readonly id: FindingId;
  readonly sev: SevKey;
  /** 보고서 기준 판정 */
  readonly vd: Verdict;
  /** 보고서에서 '신규' 표시 */
  readonly isNew?: boolean;
  /** 제목 (aria-label 에도 쓰이므로 문자열) */
  readonly title: string;
  /** 메서드·경로가 있으면 Ep 로, 없으면 hdr 를 표시 (원본 fEp) */
  readonly m?: Method;
  readonly p?: string;
  readonly hdr?: string;
  readonly type: string;
  /** 계정별 비교표·증거·재현 방법까지 있는 대표 발견 (f1) */
  readonly full?: boolean;
  readonly what: string;
  /** 증거 (full 이 아닐 때) */
  readonly ev?: ReactNode;
  readonly basisL: string;
  readonly basis: readonly string[];
  readonly fix?: ReactNode;
  readonly next?: string;
  readonly cls: readonly string[];
}

export const F: readonly Finding[] = [
  {
    id: "f1",
    sev: "crit",
    vd: "확정",
    title: "다른 사용자의 주문 내역이 조회됨",
    m: "GET",
    p: "/api/orders/{id}",
    type: "수평 IDOR",
    full: true,
    what: "계정 B로 A의 주문(1012)을 요청하자 서버가 소유자 확인 없이 200 응답과 함께 주문 정보를 돌려줬습니다. 주문 번호만 바꾸면 다른 사용자의 주문을 볼 수 있습니다.",
    basisL: "확정 근거",
    basis: [
      "B의 응답에 A의 표식이 들어 있음 (실제 수신)",
      "B 본인 주문에는 표식 없음 (대조군과 다름)",
      "비로그인 요청은 차단됨 · 인증은 동작하지만 소유자 검사가 빠진 상태",
    ],
    fix: (
      <>
        서버에서 주문을 조회할 때 <code>order.owner_id == 세션 사용자</code>를 검사하세요. 순차 ID 대신 추측하기 어려운
        식별자를 쓰는 것도 권장합니다.
      </>
    ),
    cls: ["OWASP A01 접근 통제", "WSTG-ATHZ-04", "CWE-639"],
  },
  {
    id: "f2",
    sev: "hi",
    vd: "확정",
    isNew: true,
    title: "비로그인 상태에서 관리자 통계가 열림",
    m: "GET",
    p: "/admin/stats",
    type: "수직 권한 상승",
    what: "세션 없이 보낸 요청에 관리자 통계 일부가 200으로 응답했습니다.",
    ev: "세션 쿠키 없이 보낸 요청 1건 · 비교용 대조군 1건",
    basisL: "확정 근거",
    basis: ["세션 없이 관리자 데이터 응답을 받음"],
    fix: (
      <>
        <code>/admin</code> 경로 전체에 서버 쪽 로그인·관리자 역할 검사를 넣으세요.
      </>
    ),
    cls: ["OWASP A01 접근 통제", "WSTG-ATHZ-02", "CWE-306"],
  },
  {
    id: "f3",
    sev: "mid",
    vd: "의심",
    isNew: true,
    title: "다른 사용자의 프로필이 수정 가능해 보임",
    m: "PUT",
    p: "/api/users/{id}",
    type: "수평 IDOR",
    what: "같은 경로의 GET에서 B가 A의 프로필 일부를 받았습니다. PUT도 소유자 검사가 빠졌을 가능성이 높습니다.",
    ev: "GET 응답 정황만 확보 · PUT은 데이터를 바꾸는 요청이라 보내지 않음",
    basisL: "의심 근거",
    basis: ["실제로 수정되는지는 확인하지 않음"],
    next: "격리된 시험환경에서 승인 후 PUT 재현",
    cls: ["OWASP A01 접근 통제", "WSTG-ATHZ-04", "CWE-639"],
  },
  {
    id: "f4",
    sev: "mid",
    vd: "확정",
    title: "쿠키에 보안 속성이 빠져 있음",
    hdr: "Set-Cookie: session",
    type: "보안 설정",
    what: "세션 쿠키에 HttpOnly 속성이 없어 스크립트에서 쿠키를 읽을 수 있습니다.",
    ev: <code>Set-Cookie: session=[REDACTED]; Path=/</code>,
    basisL: "확정 근거",
    basis: ["응답 헤더에서 직접 확인"],
    fix: (
      <>
        세션 쿠키에 <code>HttpOnly</code> <code>Secure</code> <code>SameSite</code>를 지정하세요.
      </>
    ),
    cls: ["OWASP A05 보안 설정 오류", "WSTG-SESS-02", "CWE-1004"],
  },
  {
    id: "f5",
    sev: "mid",
    vd: "판단 불가",
    title: "결제 금액 변경이 막히는지 미확인",
    m: "POST",
    p: "/api/checkout",
    type: "업무 흐름 우회",
    what: "결제 금액을 1원으로 바꿔 보내도 주문이 만들어지는지 확인하려던 항목입니다.",
    ev: "데이터를 바꾸는 요청이라 승인 전에는 보내지 않음",
    basisL: "판단 불가 사유",
    basis: ["보내지 않았으므로 결과가 없음 · 안전하다는 뜻이 아님"],
    next: "격리된 시험환경에서 승인 후 재현하면 확정할 수 있습니다",
    cls: ["OWASP A04 안전하지 않은 설계", "WSTG-BUSL-01", "CWE-472"],
  },
  {
    id: "f6",
    sev: "lo",
    vd: "의심",
    title: "서버 버전 정보가 노출됨",
    hdr: "Server: nginx/1.21.0",
    type: "정보 노출",
    what: "응답 헤더에 서버 종류와 버전이 드러납니다.",
    ev: <code>Server: nginx/1.21.0</code>,
    basisL: "의심 근거",
    basis: ["버전 정보만으로는 취약한지 판단할 수 없음 · 알려진 취약점 해당 여부 미확인"],
    fix: (
      <>
        nginx에서 <code>server_tokens off</code>로 버전을 숨기세요.
      </>
    ),
    cls: ["OWASP A05 보안 설정 오류", "WSTG-INFO-02", "CWE-200"],
  },
];

/** id 로 발견 찾기 (원본 `F.filter(x => x.id === id)[0]`). */
export function findingById(id: string): Finding | undefined {
  return F.find((f) => f.id === id);
}
