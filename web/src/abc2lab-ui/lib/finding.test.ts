import { describe, expect, it } from "vitest";

import {
  confirmedEvidenceComplete,
  type FindingRecord,
  getFinding,
  missingEvidence,
  ORACLE_BADGE,
  type Signal,
} from "./finding";

// 판정 레코드 화면 읽기(verdict-record §2~§4)의 fail-closed·마스킹 로직 검증.
// 목업/계약 shape 로직 테스트이며 실제 판정 건수를 증명하는 자료는 아니다.

function rec(over: Partial<FindingRecord> = {}): FindingRecord {
  const yes: Signal = "yes";
  return {
    id: "r",
    title: "t",
    severity: "high",
    axis: "horizontal",
    vulnClass: "수평 IDOR",
    resourceRef: "basket:x",
    oracleValue: "confirmed",
    vulnVerdict: "confirmed",
    accessPolicyResult: "위반_후보",
    checks: { policy: yes, identity: yes, data: yes },
    reasonCodes: ["r1"],
    signals: {
      sameResource: yes,
      ownerNormalData: yes,
      crossNormalData: yes,
      nontrivialDataMatch: yes,
      reflectionExcluded: yes,
      templateExcluded: yes,
    },
    canaryFields: ["body"],
    comparison: [],
    subjects: { ownerRef: "A", evaluatedRef: "B", ownerStatus: "verified", evaluatedStatus: "verified" },
    control: {
      policyVersion: "kg-fixture/v1:x:y",
      resetGeneration: "g1",
      observedAt: 1,
      modelTag: "m",
      hintMatchesCanonical: true,
      expectedAccess: "deny",
    },
    trafficEvidenceRefs: [],
    mock: true,
    ...over,
  };
}

describe("ORACLE_BADGE", () => {
  it("confirmed/suspected/undecidable 는 각기 다른 배지, allowed 는 취약 배지 없음", () => {
    expect(ORACLE_BADGE.confirmed.verdict).toBe("ok");
    expect(ORACLE_BADGE.suspected.verdict).toBe("sus");
    expect(ORACLE_BADGE.undecidable.verdict).toBe("und");
    expect(ORACLE_BADGE.allowed.verdict).toBeNull();
  });
});

describe("confirmedEvidenceComplete", () => {
  it("세 체크 + 비자명·반사·템플릿 신호가 전부 yes 면 true", () => {
    expect(confirmedEvidenceComplete(rec())).toBe(true);
  });

  it("체크 하나라도 yes 가 아니면 false(모름을 충족으로 접지 않음)", () => {
    expect(confirmedEvidenceComplete(rec({ checks: { policy: "yes", identity: "unknown", data: "yes" } }))).toBe(false);
  });

  it("핵심 신호(비자명 표식·반사·템플릿) 하나라도 비면 false", () => {
    expect(confirmedEvidenceComplete(rec({ signals: { ...rec().signals, nontrivialDataMatch: "no" } }))).toBe(false);
    expect(confirmedEvidenceComplete(rec({ signals: { ...rec().signals, reflectionExcluded: "unknown" } }))).toBe(
      false,
    );
  });
});

describe("missingEvidence", () => {
  it("전부 충족이면 빈 배열", () => {
    expect(missingEvidence(rec())).toEqual([]);
  });

  it("비충족 체크·신호를 라벨로 나열한다", () => {
    const m = missingEvidence(rec({ checks: { policy: "no", identity: "yes", data: "unknown" } }));
    expect(m.some((x) => x.includes("정책 체크"))).toBe(true);
    expect(m.some((x) => x.includes("데이터 체크"))).toBe(true);
    expect(m.some((x) => x.includes("신원 체크"))).toBe(false);
  });
});

describe("getFinding mock (마스킹)", () => {
  it("mock=true, canaryFields 는 필드명만(값·sentinel 문자열 없음)", async () => {
    const r = await getFinding(null);
    expect(r.mock).toBe(true);
    expect(r.canaryFields.length).toBeGreaterThan(0);
    // canary **값**(sentinel) 으로 보이는 문자열이 레코드 어디에도 없어야 한다(§3 마스킹).
    // 용어("canaryFields")는 OK, 값 패턴(canary-<hex>, ABC2LAB-CANARY-… 류)만 금지.
    const blob = JSON.stringify(r);
    expect(blob).not.toMatch(/canary[-_][0-9a-f]/i);
    expect(blob).not.toMatch(/https?:\/\//);
    expect(blob).not.toMatch(/Cookie:/i);
    // canaryFields 는 짧은 필드명 토큰만(값 아님).
    for (const f of r.canaryFields) expect(f).toMatch(/^[A-Za-z_][A-Za-z0-9_]*$/);
  });

  it("목업은 confirmed 이고 증거 3조건이 모두 모여 있다", async () => {
    const r = await getFinding(null);
    expect(r.oracleValue).toBe("confirmed");
    expect(confirmedEvidenceComplete(r)).toBe(true);
  });
});
