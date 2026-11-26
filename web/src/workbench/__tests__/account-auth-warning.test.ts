import { describe, expect, it } from "vitest";

import {
  AUTH_CONSEQUENCE,
  AUTH_UNCONFIRMED_CONSEQUENCE,
  accountAuthConsequences,
  accountAuthWarningRows,
} from "../views/live/liveAccountWarnings";
import { ACCOUNT_AUTH_LABEL } from "../live/urlMap";

const NOW = Date.parse("2026-11-26T12:00:00Z");

// 모델 직접 로그인 회차의 새 값(#969 이경준 P2): 실행 중 not_attempted는 경고가 아니고, unconfirmed는 '요청 못 함' 결과 줄을 쓰지 않는다.
describe("시험 계정 인증 경고 — 모델 로그인 값", () => {
  it("실행 중 not_attempted만 있으면 경고 줄·결과 줄이 없다", () => {
    const auth = { "acct-a": "not_attempted", "acct-b": "authenticated" } as const;
    expect(accountAuthWarningRows(auth, undefined, NOW)).toEqual([]);
    expect(accountAuthConsequences(auth)).toEqual([]);
  });
  it("끝난 점검의 not_attempted는 줄과 '요청 못 함' 결과 줄이 있다", () => {
    const auth = { "acct-a": "not_attempted" } as const;
    expect(accountAuthWarningRows(auth, undefined, NOW, true)).toEqual(["A(acct-a) — 로그인하지 않음"]);
    expect(accountAuthConsequences(auth, true)).toEqual([AUTH_CONSEQUENCE]);
  });
  it("unconfirmed만 있으면 짧은 줄과 확인 안 됨 결과 줄만 있다", () => {
    const auth = { "acct-a": "unconfirmed" } as const;
    expect(accountAuthWarningRows(auth, undefined, NOW)).toEqual(["A(acct-a) — 로그인 성공 확인 안 됨"]);
    expect(accountAuthConsequences(auth)).toEqual([AUTH_UNCONFIRMED_CONSEQUENCE]);
  });
  it("인증 안 됨과 확인 안 됨이 같이 있으면 결과 줄이 둘이다", () => {
    const auth = { "acct-a": "login_failed", "acct-b": "unconfirmed" } as const;
    expect(accountAuthConsequences(auth)).toEqual([AUTH_CONSEQUENCE, AUTH_UNCONFIRMED_CONSEQUENCE]);
  });
  it("점검 정보 칸은 판단불가를 '비인증 진행'으로 단정하지 않는다", () => {
    expect(ACCOUNT_AUTH_LABEL.unconfirmed).toBe("인증 확인 안 됨");
    expect(ACCOUNT_AUTH_LABEL.not_attempted).toBe("로그인 시도 없음");
  });
});
