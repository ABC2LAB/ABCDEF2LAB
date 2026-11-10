// 대시보드 상수. 원본 TODO · REQ_CHECKOUT.
// 원본 문자열 안의 <code> · <span class="rd|hl"> 는 HTML 파서에 맡기지 않도록 여기서 JSX(ReactNode)로 바꿨다.

import type { ReactNode } from "react";

import type { DSel } from "../store";
import s from "../workbench.module.css";

/** '지금 처리할 일' 한 줄. 원본 TODO 항목. */
export interface TodoRow {
  readonly id: Extract<DSel, "appr" | "path" | "und" | "cov">;
  readonly host: string;
  /** 제목 (경로는 <code>) */
  readonly t: ReactNode;
  readonly s: string;
  readonly time: string;
}

export const TODO: readonly TodoRow[] = [
  { id: "appr", host: "api.store-dev.com", t: "결제 금액 변경 요청", s: "보내기 전 승인 필요", time: "12분 전" },
  {
    id: "path",
    host: "shop.example.com",
    t: (
      <>
        새로 찾은 경로 <code>/admin/export</code>
      </>
    ),
    s: "관리자 경로라 자동으로 처리하지 않음",
    time: "18:31",
  },
  { id: "und", host: "app.sample.io", t: "판단 불가 2건", s: "일부 응답이 느려 결과를 확정하지 못함", time: "10/23" },
  { id: "cov", host: "docs.example.com", t: "미검사 영역 있음", s: "커버리지 76%", time: "10/24" },
];

/** 승인 요청 상세의 '보낼 요청'. 세션 값은 가리고(rd) 바꾼 금액은 강조(hl). 원본 REQ_CHECKOUT. */
export const REQ_CHECKOUT: ReactNode = (
  <>
    {"POST /api/checkout HTTP/1.1\nHost: api.store-dev.com\nCookie: session="}
    <span className={s.rd}>[REDACTED]</span>
    {'\nContent-Type: application/json\n\n{"order_id": 2041, "amount": '}
    <span className={s.hl}>1</span>
    {', "method": "card"}'}
  </>
);
