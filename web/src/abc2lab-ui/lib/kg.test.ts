import { describe, expect, it } from "vitest";

import { type AccessMap, buildCellResolver, countDecisions, getAccessMap, type KgCell } from "./kg";

// 접근통제 지도 리졸버·집계의 fail-closed 규칙 검증(kg-policy §4.2(2)·(7)).
// 목업/계약 shape 에 대한 로직 테스트이며, 실행 결과 건수를 증명하는 자료는 아니다.

function gridMap(principals: string[], resources: string[], cells: KgCell[]): AccessMap {
  return {
    policyVersion: "test",
    mock: true,
    principals: principals.map((ref) => ({ ref, role: "u", axis: "H" })),
    resources: resources.map((id) => ({ id, label: id, ownerRef: principals[0] ?? "" })),
    cells,
  };
}

describe("buildCellResolver", () => {
  it("일반 셀은 decision·note 를 그대로 돌려준다", () => {
    const resolve = buildCellResolver([
      { principalRef: "A", resourceId: "r1", decision: "allow-ok", note: "본인 자원" },
    ]);
    expect(resolve("A", "r1")).toEqual({ decision: "allow-ok", note: "본인 자원" });
  });

  it("projection-error 가 실리면 decision 을 unknown 으로 강제하고 사유를 보존한다", () => {
    const resolve = buildCellResolver([
      { principalRef: "A", resourceId: "r1", decision: "vuln", note: "n", projectionError: "출처 미검증" },
    ]);
    // 색(decision)을 추정하지 않는다 — vuln 이었어도 unknown.
    expect(resolve("A", "r1")).toEqual({
      decision: "unknown",
      note: "n",
      projectionError: "출처 미검증",
    });
  });

  it("중복 레코드는 last-writer-wins 가 아니라 unknown(선택 모호)", () => {
    const resolve = buildCellResolver([
      { principalRef: "A", resourceId: "r1", decision: "allow-ok" },
      { principalRef: "A", resourceId: "r1", decision: "vuln" },
    ]);
    const got = resolve("A", "r1");
    expect(got.decision).toBe("unknown");
    expect(got.projectionError).toBe("중복 레코드 — 선택 모호");
  });

  it("projection 미반환 칸은 untested 로 추정하지 않고 unknown(미확인)", () => {
    const resolve = buildCellResolver([]);
    const got = resolve("A", "r1");
    expect(got.decision).toBe("unknown");
    expect(got.projectionError).toBe("projection 미반환 — 미확인");
  });

  it("한 칸의 중복이 다른 칸의 판정을 오염시키지 않는다", () => {
    const resolve = buildCellResolver([
      { principalRef: "A", resourceId: "r1", decision: "vuln" },
      { principalRef: "A", resourceId: "r1", decision: "allow-ok" }, // A|r1 중복 → unknown
      { principalRef: "B", resourceId: "r1", decision: "allow-ok" }, // 독립
    ]);
    expect(resolve("A", "r1").decision).toBe("unknown");
    expect(resolve("B", "r1").decision).toBe("allow-ok");
  });
});

describe("countDecisions", () => {
  it("렌더 격자 전체를 집계하고 미반환 칸은 unknown 으로 포함한다", () => {
    const map = gridMap(
      ["A", "B"],
      ["r1", "r2"],
      [
        { principalRef: "A", resourceId: "r1", decision: "allow-ok" },
        { principalRef: "B", resourceId: "r1", decision: "vuln" },
        // A|r2, B|r2 누락 → unknown 2칸
      ],
    );
    const c = countDecisions(map);
    expect(c["allow-ok"]).toBe(1);
    expect(c.vuln).toBe(1);
    expect(c.unknown).toBe(2);
    expect(c.untested).toBe(0); // 미반환을 미검사로 추정하지 않는다
  });

  it("중복 셀도 격자 기준 unknown 1칸으로만 센다", () => {
    const map = gridMap(
      ["A"],
      ["r1"],
      [
        { principalRef: "A", resourceId: "r1", decision: "allow-ok" },
        { principalRef: "A", resourceId: "r1", decision: "vuln" },
      ],
    );
    const c = countDecisions(map);
    expect(c.unknown).toBe(1);
    expect(c["allow-ok"]).toBe(0);
    expect(c.vuln).toBe(0);
  });

  it("합계는 항상 principals × resources 격자 크기와 같다", () => {
    const map = gridMap(["A", "B", "C"], ["r1", "r2"], [{ principalRef: "A", resourceId: "r1", decision: "allow-ok" }]);
    const total = Object.values(countDecisions(map)).reduce((a, b) => a + b, 0);
    expect(total).toBe(3 * 2);
  });

  it("목업 지도도 합계가 격자 크기와 맞는다(중복·누락 없음)", async () => {
    const map = await getAccessMap(null);
    const total = Object.values(countDecisions(map)).reduce((a, b) => a + b, 0);
    expect(total).toBe(map.principals.length * map.resources.length);
  });
});
