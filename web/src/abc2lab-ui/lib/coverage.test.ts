import { describe, expect, it } from "vitest";

import {
  attempted,
  type CoverageArea,
  type CoverageData,
  type CoverageStatus,
  coverageIssues,
  getCoverage,
  STATUS_ORDER,
  summarize,
  total,
} from "./coverage";

// 커버리지 집계·무결성의 fail-closed 규칙 검증. 목업/계약 shape 로직 테스트이며 실 검사 건수 증명 아님.

function zero(): Record<CoverageStatus, number> {
  return { confirmed: 0, suspected: 0, undecidable: 0, blank: 0, in_progress: 0, untested: 0, na: 0 };
}
function area(over: Partial<CoverageArea> = {}): CoverageArea {
  return { id: "a", label: "x", axis: "horizontal", planned: 0, counts: zero(), ...over };
}
function data(areas: CoverageArea[]): CoverageData {
  return { areas, policyVersion: "t", mock: true };
}

describe("attempted / total", () => {
  it("attempted = 확정+의심+판정불가+기록없음+진행중 (미시도·해당없음 제외)", () => {
    const a = area({
      counts: { confirmed: 1, suspected: 1, undecidable: 1, blank: 1, in_progress: 1, untested: 3, na: 2 },
    });
    expect(attempted(a)).toBe(5);
    expect(total(a)).toBe(10);
  });
});

describe("coverageIssues (무결성 — 조용히 안 맞춤)", () => {
  it("상태합 = planned 면 경고 0", () => {
    const a = area({ planned: 4, counts: { ...zero(), confirmed: 1, untested: 3 } });
    expect(coverageIssues(data([a])).mismatched).toBe(0);
  });
  it("상태합 ≠ planned 면 그 영역을 센다(fail-closed)", () => {
    const a = area({ planned: 9, counts: { ...zero(), confirmed: 1, untested: 3 } }); // 합 4 ≠ 9
    expect(coverageIssues(data([a])).mismatched).toBe(1);
  });
});

describe("summarize", () => {
  it("영역 걸쳐 상태별 합 + 계획·시도 총계", () => {
    const s = summarize(
      data([
        area({ planned: 4, counts: { ...zero(), confirmed: 1, untested: 3 } }),
        area({ planned: 2, counts: { ...zero(), undecidable: 1, untested: 1 } }),
      ]),
    );
    expect(s.planned).toBe(6);
    expect(s.attempted).toBe(2); // confirmed 1 + undecidable 1
    expect(s.byStatus.untested).toBe(4);
    expect(s.byStatus.confirmed).toBe(1);
  });

  it("합계는 숫자로 유지(NaN 아님)", () => {
    const s = summarize(data([area({ planned: 1, counts: { ...zero(), blank: 1 } })]));
    expect(STATUS_ORDER.reduce((n, k) => n + s.byStatus[k], 0)).toBe(1);
  });
});

describe("getCoverage mock", () => {
  it("mock=true, 후속 축(수직·상태변경)은 미시도가 커서 '검사했다'로 과장되지 않는다", async () => {
    const d = await getCoverage(null);
    expect(d.mock).toBe(true);
    const vertical = d.areas.find((a) => a.axis === "vertical");
    expect(vertical).toBeDefined();
    // 미시도가 시도보다 많다(= 조회 안 함을 '없음'으로 칠하지 않음).
    if (vertical) expect(vertical.counts.untested).toBeGreaterThan(attempted(vertical));
    // 목업은 무결성 OK(계획=상태합).
    expect(coverageIssues(d).mismatched).toBe(0);
  });
});
