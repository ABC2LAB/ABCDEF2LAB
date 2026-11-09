// 범위 확인의 보기 필터. 원본 FL. 포함 여부(on)는 화면에서 바뀌므로 인자로 받는다(원본 e.on).

import type { Endpoint } from "./scope";

export type ScopeFilter = "all" | "chg" | "id" | "auth" | "off";

/** [키, 이름, 조건(엔드포인트, 지금 포함 여부)] — 표시 순서 그대로. */
export const FL: ReadonlyArray<readonly [ScopeFilter, string, (e: Endpoint, on: boolean) => boolean]> = [
  ["all", "전체", () => true],
  ["chg", "데이터 변경", (e) => e.chg],
  ["id", "ID 포함", (e) => e.hasId],
  ["auth", "로그인 필요", (e) => e.auth === 1],
  ["off", "제외됨", (_e, on) => !on],
];
