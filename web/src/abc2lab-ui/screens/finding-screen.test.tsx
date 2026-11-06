import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { FindingRecord, Signal } from "../lib/finding";
import { NavContext } from "../nav";
import FindingScreen, { FindingDetail } from "./FindingScreen";

const YES: Signal = "yes";
function rec(over: Partial<FindingRecord> = {}): FindingRecord {
  return {
    id: "r",
    title: "교차 계정으로 조회됨(테스트)",
    severity: "high",
    axis: "horizontal",
    vulnClass: "수평 IDOR",
    resourceRef: "basket:x",
    oracleValue: "confirmed",
    vulnVerdict: "confirmed",
    accessPolicyResult: "위반_후보",
    checks: { policy: YES, identity: YES, data: YES },
    reasonCodes: ["r1"],
    signals: {
      sameResource: YES,
      ownerNormalData: YES,
      crossNormalData: YES,
      nontrivialDataMatch: YES,
      reflectionExcluded: YES,
      templateExcluded: YES,
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

// 발견 상세가 판정 레코드를 마스킹·fail-closed 로 그리는지(verdict-record §3·§4).
// 목업(getFinding)은 결정적이라 추가 모킹 없이 렌더한다.

function show() {
  return render(
    <NavContext.Provider value={{ screen: "finding", go: vi.fn() }}>
      <FindingScreen />
    </NavContext.Provider>,
  );
}

afterEach(cleanup);

describe("FindingScreen", () => {
  it("제목과 확정 판정 배지를 그린다", async () => {
    show();
    await screen.findByRole("heading", { name: /교차 계정으로 조회됨/ });
    expect(screen.getAllByText("확정").length).toBeGreaterThan(0);
  });

  it("canary 값은 안 보이고 필드명 + [REDACTED]만 보인다(절대규칙 2)", async () => {
    const { container } = show();
    await screen.findByRole("heading", { name: /교차 계정으로 조회됨/ });
    expect(screen.getByText("[REDACTED]")).toBeInTheDocument();
    expect(screen.getByText("body")).toBeInTheDocument(); // canary 필드명은 보인다
    // canary **값**(sentinel)·raw URL·쿠키 원문은 화면에 없어야 한다. 용어 "canary"는 설명문이라 허용.
    expect(container.textContent).not.toMatch(/canary[-_][0-9a-f]/i);
    expect(container.textContent).not.toMatch(/https?:\/\//);
    expect(container.textContent).not.toMatch(/Cookie:/i);
  });

  it("다계정 비교에 취약 행과 대조군(판단 불가) 행이 함께 있다", async () => {
    const { container } = show();
    await screen.findByRole("heading", { name: /교차 계정으로 조회됨/ });
    const table = container.querySelector("table.cmp");
    expect(table).not.toBeNull();
    const t = table as HTMLElement;
    expect(within(t).getByText("취약")).toBeInTheDocument();
    expect(within(t).getByText("판단 불가")).toBeInTheDocument();
  });

  it("통제변수(정책 버전 등) 재현 메타를 보여준다", async () => {
    show();
    await screen.findByRole("heading", { name: /교차 계정으로 조회됨/ });
    expect(screen.getByText("정책 버전")).toBeInTheDocument();
    expect(screen.getByText("reset 세대")).toBeInTheDocument();
  });
});

// 이동찬 리뷰(#245): 레코드가 불일치하면 화면 판정은 보수적으로, 서술은 충족 신호에서만.
describe("FindingDetail — 불일치 레코드 보수 표시", () => {
  it("oracle=confirmed 인데 data 체크 unknown 이면 화면 배지는 '확정'이 아니라 '판단 불가'로 내리고 원값을 밝힌다", () => {
    const { container } = render(
      <FindingDetail
        record={rec({ oracleValue: "confirmed", checks: { policy: "yes", identity: "yes", data: "unknown" } })}
      />,
    );
    const text = container.textContent ?? "";
    // 증거 3조건 불완비 → 화면 배지는 '확정'(vbadge2.ok) 아님, 보수 하향 안내가 뜨고 원값(confirmed)을 밝힌다.
    expect(container.querySelector(".vbadge2.ok")).toBeNull();
    expect(text).toMatch(/증거 3조건이 불완비라 화면 판정은 보수적으로/);
    expect(text).toMatch(/원값 확정/);
  });

  it("oracle=undecidable + 비자명표식 unknown 이면 '표식이 나타났다'를 단정하지 않는다", () => {
    const { container } = render(
      <FindingDetail
        record={rec({
          oracleValue: "undecidable",
          vulnVerdict: "undecidable",
          signals: { ...rec().signals, nontrivialDataMatch: "unknown" },
        })}
      />,
    );
    const text = container.textContent ?? "";
    expect(text).toContain("단정하지 않아요");
    expect(text).not.toContain("응답 필드에 그대로 나타났");
  });
});
