import { describe, expect, it } from "vitest";

import {
  type AccessDecision,
  type AccessMap,
  buildCellResolver,
  countDecisions,
  DECISION_CELL,
  getAccessMap,
  type KgCell,
} from "./kg";

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

  it("suspected 는 확정으로 승격하지 않고 그대로 suspected", () => {
    const resolve = buildCellResolver([{ principalRef: "A", resourceId: "r1", decision: "suspected" }]);
    expect(resolve("A", "r1").decision).toBe("suspected");
  });

  it("unknown 은 allow-ok 로 완화하지 않고 그대로 unknown", () => {
    const resolve = buildCellResolver([{ principalRef: "A", resourceId: "r1", decision: "unknown" }]);
    expect(resolve("A", "r1").decision).toBe("unknown");
  });

  it("deny-ok(보류)은 '정상 차단'으로 산출하지 않고 unknown (§4.2(3))", () => {
    const resolve = buildCellResolver([{ principalRef: "A", resourceId: "r1", decision: "deny-ok" }]);
    const got = resolve("A", "r1");
    expect(got.decision).toBe("unknown");
    expect(got.projectionError).toContain("보류");
  });

  it.each([
    "confirmed",
    "SUSPECTED",
    "safe",
    "",
    "constructor",
    "toString",
  ])("미등록 판정값 %o 은 색 추정 없이 unknown(미등록 판정값) (§4.1(c))", (raw) => {
    const resolve = buildCellResolver([{ principalRef: "A", resourceId: "r1", decision: raw as AccessDecision }]);
    const got = resolve("A", "r1");
    expect(got.decision).toBe("unknown");
    expect(got.projectionError).toBe("미등록 판정값");
  });

  it("projectionError 가 빈 문자열이어도 오류로 보고 unknown (fail-closed)", () => {
    const resolve = buildCellResolver([
      { principalRef: "A", resourceId: "r1", decision: "allow-ok", projectionError: "" },
    ]);
    const got = resolve("A", "r1");
    expect(got.decision).toBe("unknown");
    expect(got.projectionError).toBeTruthy(); // 빈 사유는 대체 문구로 채운다
  });

  it("구분자(|)가 섞인 ref·id 도 다른 칸과 키 충돌하지 않는다", () => {
    const resolve = buildCellResolver([
      { principalRef: "A", resourceId: "x|r", decision: "vuln" },
      { principalRef: "A|x", resourceId: "r", decision: "allow-ok" },
    ]);
    // 순진한 `${p}|${r}` 키라면 둘 다 "A|x|r" 로 충돌한다.
    expect(resolve("A", "x|r").decision).toBe("vuln");
    expect(resolve("A|x", "r").decision).toBe("allow-ok");
  });
});

describe("DECISION_CELL", () => {
  it("확정과 의심은 서로 다른 셀 클래스다(색으로 1차 구분, §4.2(7))", () => {
    expect(DECISION_CELL.vuln).toBeTruthy();
    expect(DECISION_CELL.suspected).toBeTruthy();
    expect(DECISION_CELL.vuln).not.toBe(DECISION_CELL.suspected);
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

  it("미등록·프로토타입 판정값이 섞여도 합계가 숫자로 유지된다(NaN·문자열 아님)", () => {
    const map = gridMap(
      ["A"],
      ["r1", "r2"],
      [
        { principalRef: "A", resourceId: "r1", decision: "confirmed" as AccessDecision },
        { principalRef: "A", resourceId: "r2", decision: "constructor" as AccessDecision },
      ],
    );
    const c = countDecisions(map);
    const total = Object.values(c).reduce((a, b) => a + b, 0);
    expect(Number.isFinite(total)).toBe(true);
    expect(total).toBe(1 * 2);
    expect(c.unknown).toBe(2); // 둘 다 unknown 으로 내려감
  });

  it("axis 를 주면 해당 축(H) 신원만 세고, 수직 미리보기는 제외한다", () => {
    const map: AccessMap = {
      policyVersion: "test",
      mock: true,
      principals: [
        { ref: "A", role: "u", axis: "H" },
        { ref: "adm", role: "admin", axis: "V" },
      ],
      resources: [{ id: "r1", label: "r1", ownerRef: "A" }],
      cells: [
        { principalRef: "A", resourceId: "r1", decision: "allow-ok" },
        { principalRef: "adm", resourceId: "r1", decision: "vuln" }, // 수직 — 후속 미리보기
      ],
    };
    const h = countDecisions(map, "H");
    expect(h["allow-ok"]).toBe(1);
    expect(h.vuln).toBe(0); // 판정 축 집계에서 수직 미리보기 제외
    expect(countDecisions(map).vuln).toBe(1); // 축 미지정은 격자 전체
  });
});
