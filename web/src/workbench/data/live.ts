// 점검 진행 각본: 계획대로 처음부터 끝까지 재생. 원본 PLAN · LT · LCUM · LTOTAL · LIVE_MS · LRESUME.

import type { FindingId, Verdict } from "./findings";

/** 계획 10단계. LT 의 p 가 이 배열의 위치. */
export const PLAN: readonly string[] = [
  "우선 공격 표면·자산 탐색",
  "역할별 로그인·세션 확보 (계정 A·B)",
  "기존 정찰 결과·커버리지 확인",
  "수평 권한 경계(IDOR) 점검: 주문·프로필",
  "수직 권한 상승 점검: 관리자 기능",
  "업무 흐름 우회: 결제·주문 (승인 필요)",
  "발견 → 검증 에이전트 체인 운용",
  "소유·권한 지식그래프 갱신",
  "쿠키·세션 속성 점검",
  "커버리지 정리 및 최종 보고",
];

/** 결과 종류: ok 정상 확인 · col 경로 수집 · blk 막음/보류 · hit 발견 (CSS 클래스 `fd ${r}`) */
export type LiveResult = "ok" | "col" | "blk" | "hit";

// 작업 하나 = 지금 하는 일 한 줄. 끝나면 결과(res)와 함께 지난 활동으로 내려간다.  w: 걸린 시간(분)
// r: ok 정상 확인 · col 경로 수집 · blk 막음/보류 · hit 발견.  f: 새 발견(fv 판정, 없으면 판정 중) · v: 판정 바꿈
export interface LiveTask {
  /** PLAN 위치 */
  readonly p: number;
  /** 걸린 시간(분) */
  readonly w: number;
  /** 지금 하는 일 */
  readonly t: string;
  /** 대상 ("GET /path" 면 메서드+경로로, 아니면 code 로 표시 — parts 의 `On`) */
  readonly on: string;
  readonly r: LiveResult;
  /** 끝난 뒤 결과 문구 */
  readonly res: string;
  readonly sent?: number;
  readonly blk?: number;
  readonly ep?: number;
  readonly hold?: number;
  /** 사람이 정해야 할 새 경로(/admin/export)가 생김 */
  readonly decide?: boolean;
  /** 새 발견 */
  readonly f?: FindingId;
  /** 새 발견의 첫 판정 (없으면 '판정 중') */
  readonly fv?: Verdict;
  /** 판정 바꿈 [발견, 새 판정] */
  readonly v?: readonly [FindingId, Verdict];
}

export const LT: readonly LiveTask[] = [
  { p: 1, w: 1, t: "계정 A로 로그인해 세션 확보", on: "POST /api/login", r: "ok", res: "세션 받음", sent: 2 },
  {
    p: 1,
    w: 1,
    t: "계정 B로 로그인해 세션 확보",
    on: "POST /api/login",
    r: "ok",
    res: "세션 받음 · A·B 비교 준비",
    sent: 2,
  },
  { p: 2, w: 1.5, t: "자산 탐색 결과와 범위 규칙 불러오기", on: "", r: "col", res: "경로 37 · 포함 33 · 제외 4" },
  {
    p: 2,
    w: 2,
    t: "화면 속 새 링크 따라가기",
    on: "GET /api/orders/{id}/receipt",
    r: "col",
    res: "새 경로 9 · 허용 주소 밖 링크 26건은 요청 안 함",
    sent: 1180,
    blk: 26,
    ep: 9,
  },
  {
    p: 3,
    w: 1.5,
    t: "계정 A로 주문 1012 조회 (대조군)",
    on: "GET /api/orders/1012",
    r: "ok",
    res: "200 · 본인 주문",
    sent: 6,
  },
  {
    p: 3,
    w: 2,
    t: "계정 B로 계정 A의 주문 1012 조회",
    on: "GET /api/orders/1012",
    r: "hit",
    res: "200 · A 주문 표식이 B 응답에 보임 · 발견 보고",
    sent: 8,
    f: "f1",
  },
  {
    p: 3,
    w: 2,
    t: "계정 B로 계정 A의 프로필 1024 조회",
    on: "GET /api/users/1024",
    r: "hit",
    res: "200 · 프로필 일부 수신 · 발견 보고",
    sent: 10,
    f: "f3",
  },
  {
    p: 3,
    w: 1,
    t: "프로필 수정 요청은 보내지 않음",
    on: "PUT /api/users/{id}",
    r: "blk",
    res: "데이터를 바꾸는 요청이라 승인 필요",
    hold: 1,
  },
  {
    p: 4,
    w: 1.5,
    t: "관리자 메뉴 스크립트의 새 경로 확인",
    on: "GET /admin/export",
    r: "col",
    res: "처음 보는 관리자 경로 · 사람에게 확인 요청",
    sent: 3,
    ep: 1,
    decide: true,
  },
  { p: 4, w: 1.5, t: "계정 A로 관리자 통계 접근", on: "GET /admin/stats", r: "ok", res: "403 · 정상 차단", sent: 4 },
  {
    p: 4,
    w: 2,
    t: "세션 없이 관리자 통계 접근",
    on: "GET /admin/stats",
    r: "hit",
    res: "200 · 관리자 데이터 응답 · 발견 보고",
    sent: 6,
    f: "f2",
  },
  {
    p: 5,
    w: 1.5,
    t: "결제 금액 변경 요청은 승인 없이 보내지 않음",
    on: "POST /api/checkout",
    r: "blk",
    res: "승인 요청 남김",
    hold: 1,
  },
  {
    p: 5,
    w: 1,
    t: "주문 취소 요청은 보내지 않음",
    on: "PATCH /api/orders/{id}",
    r: "blk",
    res: "데이터를 바꾸는 요청이라 승인 필요",
    hold: 1,
  },
  {
    p: 6,
    w: 1.5,
    t: "검증 에이전트: 주문 1012 표식 대조",
    on: "GET /api/orders/1012",
    r: "ok",
    res: "표식 일치 · 대조군과 다름 · 확정",
    sent: 3,
    v: ["f1", "확정"],
  },
  {
    p: 6,
    w: 1,
    t: "검증 에이전트: 관리자 통계 다시 요청",
    on: "GET /admin/stats",
    r: "ok",
    res: "세션 없이 다시 200 · 확정",
    sent: 2,
    v: ["f2", "확정"],
  },
  {
    p: 6,
    w: 1,
    t: "검증 에이전트: 프로필 응답 다시 확인",
    on: "GET /api/users/1024",
    r: "ok",
    res: "정황만 있음 · 의심으로 둠",
    sent: 2,
    v: ["f3", "의심"],
  },
  { p: 7, w: 1, t: "소유·권한 지식그래프 갱신", on: "", r: "col", res: "역할 4 · 엔드포인트 7 · 자원 6 · 관계 24" },
  {
    p: 8,
    w: 1,
    t: "세션 쿠키 속성 확인",
    on: "Set-Cookie: session",
    r: "hit",
    res: "HttpOnly 없음 · 발견 보고",
    sent: 1,
    f: "f4",
    fv: "확정",
  },
  {
    p: 8,
    w: 1,
    t: "응답 헤더의 서버 정보 확인",
    on: "Server: nginx/1.21.0",
    r: "hit",
    res: "버전 노출 · 발견 보고",
    sent: 1,
    f: "f6",
    fv: "의심",
  },
  {
    p: 8,
    w: 0.5,
    t: "안전 규칙으로 막은 시도: 점검 프로그램을 건드리는 명령",
    on: "",
    r: "blk",
    res: "실행하지 않음",
    blk: 1,
  },
  {
    p: 9,
    w: 1,
    t: "결제 금액 변경 항목 정리",
    on: "POST /api/checkout",
    r: "ok",
    res: "승인 없어 보내지 않음 · 판단 불가",
    f: "f5",
    fv: "판단 불가",
  },
  { p: 9, w: 1, t: "커버리지 정리", on: "", r: "ok", res: "검사 43 / 47 · 미검사 4" },
  { p: 9, w: 1, t: "보고서 만들기", on: "", r: "ok", res: "발견 6건 · 증거 묶음 3개" },
];

/** LCUM[k] = 작업 k 직전까지 걸린 시간(분)의 누적. 길이 LT.length + 1. */
export const LCUM: readonly number[] = LT.reduce<number[]>(
  (acc, t) => {
    acc.push(acc[acc.length - 1] + t.w);
    return acc;
  },
  [0],
);
/** 전체 점검 시간(분) = 29.5 */
export const LTOTAL = LCUM[LCUM.length - 1];
/** 재생 속도: 점검 1분 = 1.1초 */
export const LIVE_MS = 1100;
/** 대시보드에 '진행 중'으로 보이는 점검은 이 작업(약 58%)부터 이어서 재생 */
export const LRESUME = 11;
