import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { FindingRecord, Signal } from "../lib/finding";
import { NavContext } from "../nav";
import FindingUndScreen, { FindingUndDetail } from "./FindingUndScreen";

const U: Signal = "unknown";
function heldRec(over: Partial<FindingRecord> = {}): FindingRecord {
  return {
    id: "r",
    title: "상태 변경 요청이 막히는지 미확인(테스트)",
    severity: "medium",
    axis: "horizontal",
    vulnClass: "비즈니스 로직 · 상태 변경",
    resourceRef: "checkout:x",
    oracleValue: "undecidable",
    vulnVerdict: "undecidable",
    accessPolicyResult: "판정_불가",
    checks: { policy: U, identity: U, data: U },
    reasonCodes: ["e2_blocked_pre_send"],
    signals: {
      sameResource: U,
      ownerNormalData: U,
      crossNormalData: U,
      nontrivialDataMatch: U,
      reflectionExcluded: U,
      templateExcluded: U,
    },
    canaryFields: [],
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
    intent: "결제 금액을 낮춰 주문이 생성되는지",
    stateChanging: true,
    transmission: "held_pre_send",
    mock: true,
    ...over,
  };
}

afterEach(cleanup);

describe("FindingUndDetail — E² 송신 전 차단 판단 불가", () => {
  it("판단 불가 배지 + 미실행 서사 + 취약 주장 없음 + raw URL/메서드 없음", () => {
    const { container } = render(<FindingUndDetail record={heldRec()} />);
    const text = container.textContent ?? "";
    expect(screen.getByText("판단 불가")).toBeInTheDocument();
    expect(text).toContain("보내기 전에 막아뒀어요"); // E²-held 서사
    expect(text).toContain("결론을 내리지 못했어요");
    expect(container.querySelector(".vbadge2.ok")).toBeNull(); // '확정' 배지 없음
    expect(text).not.toMatch(/https?:\/\//);
    expect(text).not.toMatch(/POST |GET \//); // raw 메서드/경로 아님
  });

  it("세 체크·6 신호가 모두 불명으로 표시된다(미실행)", () => {
    const { container } = render(<FindingUndDetail record={heldRec()} />);
    // 부족한 증거에 '실제 전송·응답 없음' + 불명 신호가 나열된다.
    expect(container.textContent ?? "").toContain("실제 전송·응답이 없음");
  });

  // 이동찬 리뷰(#250): 혼합 신호면 '모두 불명'을 단정하지 않는다.
  it("신호가 하나라도 unknown 이 아니면 '모두 불명'을 말하지 않는다", () => {
    const { container } = render(
      <FindingUndDetail record={heldRec({ signals: { ...heldRec().signals, sameResource: "yes" } })} />,
    );
    const text = container.textContent ?? "";
    expect(text).not.toContain("모두 불명");
    expect(text).toContain("충족된 신호는");
  });

  // 이동찬 리뷰(#250): transmission="none" 은 E² 차단이 아니라 중립 미실행.
  it("transmission=none 이면 'E²가 막았다'로 단정하지 않고 중립 미실행으로 표시한다", () => {
    const { container } = render(<FindingUndDetail record={heldRec({ transmission: "none" })} />);
    const text = container.textContent ?? "";
    expect(text).toContain("실제 전송이 없어"); // 중립 미실행 서사
    expect(text).not.toContain("보내기 전에 막아뒀어요"); // E² 차단 단정 없음
    expect(text).not.toContain("승인 대기"); // 승인 대기 단정 없음
  });
});

describe("FindingUndScreen", () => {
  it("승인 CTA(격리환경 확인)와 보고서 복귀 버튼을 보여준다", async () => {
    render(
      <NavContext.Provider value={{ screen: "finding-und", go: vi.fn() }}>
        <FindingUndScreen />
      </NavContext.Provider>,
    );
    await screen.findByRole("heading", { name: /막히는지 미확인/ });
    expect(screen.getByRole("button", { name: /격리환경에서 확인/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /보고서로 돌아가기/ })).toBeInTheDocument();
  });
});
