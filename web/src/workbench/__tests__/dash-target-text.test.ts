// 통합 대시보드 '대상별' 줄: 판정이 없는 대상의 글엔 괄호 덧말을 붙이지 않는다(사용자 11/27)
import { expect, it } from "vitest";

import { targetNoneText } from "../views/live/DashOverview";

it("대상별: 판정이 없는 대상 글은 괄호 덧말 없이 — 진단 실패 · 중지 · 결과 없음", () => {
  expect(targetNoneText(["failed"], "failed")).toBe("진단 실패로 판정 없음");
  expect(targetNoneText(["stopped", "failed"], "stopped")).toBe("중지돼 판정 없음");
  // 끝까지 간 점검이 하나라도 있으면(결과를 못 읽었거나 없음) 결과 없음
  expect(targetNoneText(["failed", null], "failed")).toBe("결과 있는 점검 없음");
  expect(targetNoneText([null], null)).toBe("결과 있는 점검 없음");
  for (const cuts of [["failed"], ["stopped"], [null]] as const) {
    expect(targetNoneText(cuts, cuts[0])).not.toMatch(/[()]/);
  }
});
