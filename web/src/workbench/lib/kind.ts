// 경로 분류: 관찰한 사실(메서드·경로)만으로 정한다. 원본 kind · pkind · isEx.

import { OFF } from "../data/crawl";

/** lock 전역 제외 규칙 · off 기본 제외 · get 자동 포함 · login 테스트 계정 · ap 보낼 때 승인 */
export type Kind = "lock" | "off" | "get" | "login" | "ap";
/** 화면 표시용 분류: off·lock 을 ex(제외)로 합친 것. CSS 클래스 이름으로도 쓴다. */
export type PKind = "get" | "login" | "ap" | "ex";

/** 원본 `kind(m,p)`. */
export function kind(m: string, p: string): Kind {
  if (p.indexOf("/admin/delete") === 0) return "lock";
  if (OFF[`${m} ${p}`]) return "off";
  if (m === "GET") return "get";
  if (m === "POST" && p === "/api/login") return "login";
  return "ap";
}

/** 원본 `pkind(f)`. */
export function pkind(f: { m: string; p: string }): PKind {
  const k = kind(f.m, f.p);
  return k === "off" || k === "lock" ? "ex" : k;
}

/** 원본 `isEx(f)`: 제외(기본 제외 또는 전역 제외)인가. */
export function isEx(f: { m: string; p: string }): boolean {
  const k = kind(f.m, f.p);
  return k === "off" || k === "lock";
}
