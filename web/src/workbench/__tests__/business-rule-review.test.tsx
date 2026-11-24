import "@testing-library/jest-dom/vitest";

import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "@/lib/diagnosis/api";
import type { RemediationEntry } from "@/lib/diagnosis/remediation";

import * as brr from "../live/businessRuleReview";
import { DiagnosticEntrySections } from "../views/live/DiagnosticEntrySections";

const { parseBusinessRuleReviews } =
  await vi.importActual<typeof import("../live/businessRuleReview")>("../live/businessRuleReview");

vi.mock("../live/businessRuleReview", async (original) => ({
  ...(await original<typeof import("../live/businessRuleReview")>()),
  fetchBusinessRuleReviews: vi.fn(),
}));

const review = {
  id: "candidate-7",
  status: "deferred",
  reason: "business_rule_unverified",
  observation: "재현 단계 2 (GET, HTTP 200)",
  question: "성공 응답을 받은 요청의 계정은 해당 자원에 접근할 수 있나요? 소유권이나 공유 규칙을 확인해 주세요.",
} as const;

function entry(id: string): RemediationEntry {
  return {
    entry_id: id,
    title: "다른 사용자 책 조회",
    outcome: "review",
    status: "completed",
    verification: "manual_review_required",
    evidence_digest: "e".repeat(64),
    report_version: "1",
    run: "run-1",
    intent_id: 7,
    observed_at: "2026-11-24T05:00:00Z",
    evidence_version: 1,
    source_digest: "d".repeat(64),
    summary: "다른 계정 응답 후보",
    evidence: "",
  } as unknown as RemediationEntry;
}

// 블록으로 감싼다: 화살표가 mockReset() 의 반환값(그 스파이)을 돌려주면 vitest 가 정리 함수로 알고 시험 뒤에 부른다
beforeEach(() => {
  vi.mocked(brr.fetchBusinessRuleReviews).mockReset();
});
afterEach(cleanup);

describe("업무 규칙 검토 응답(#914 review-json)", () => {
  it("항목 ID 별로 읽고, 다른 점검 · 모르는 칸 · 다른 상태는 쓰지 않는다", () => {
    const ok = parseBusinessRuleReviews({ task_id: "42", entries: [review] }, "42");
    expect(ok.get("candidate-7")?.question).toContain("소유권");
    expect(parseBusinessRuleReviews({ task_id: "42", entries: [] }, "42").size).toBe(0);
    for (const bad of [
      { task_id: 42, entries: [] },
      { task_id: "43", entries: [] },
      { task_id: "42", entries: [{ ...review, extra: 1 }] },
      { task_id: "42", entries: [{ ...review, status: "confirmed" }] },
      { task_id: "42", entries: [{ ...review, id: "finding-7" }] },
      { task_id: "42", entries: [{ ...review, question: "" }] },
      { task_id: "42", entries: [review, review] },
    ])
      expect(() => parseBusinessRuleReviews(bad, "42")).toThrow(brr.BusinessRuleReviewShapeError);
  });
});

describe("취약점 상세의 업무 규칙 검토 칸", () => {
  const show = (id: string) =>
    render(<DiagnosticEntrySections task="42" entry={entry(id)} onReload={() => undefined} />);

  it("서버가 준 후보에 관찰을 먼저, 질문을 '확인할 질문(선택)'으로 붙이고 '업무 규칙 미확인'을 단다", async () => {
    vi.mocked(brr.fetchBusinessRuleReviews).mockResolvedValue(new Map([["candidate-7", { ...review }]]));
    show("candidate-7");
    const box = await screen.findByRole("region", { name: "업무 규칙 검토" });
    expect(box).toHaveTextContent("업무 규칙 미확인");
    expect(box.textContent?.indexOf("관찰")).toBeLessThan(box.textContent?.indexOf("확인할 질문(선택)") ?? -1);
    expect(box).toHaveTextContent("재현 단계 2 (GET, HTTP 200)");
    expect(box).toHaveTextContent(review.question);
    // 후보의 판정 · 검토 상태는 바꾸지 않는다(확정처럼 읽히는 말 없음)
    expect(box).not.toHaveTextContent(/확정|취약점 없음/);
  });

  it("검토가 없는 후보 · 옛 서버(형식 없음)는 칸을 두지 않는다", async () => {
    vi.mocked(brr.fetchBusinessRuleReviews).mockResolvedValue(new Map());
    show("candidate-8");
    await waitFor(() => expect(brr.fetchBusinessRuleReviews).toHaveBeenCalledWith("42"));
    expect(screen.queryByRole("region", { name: "업무 규칙 검토" })).toBeNull();
    cleanup();
    vi.mocked(brr.fetchBusinessRuleReviews).mockRejectedValue(new ApiError(404, "not_found"));
    show("candidate-7");
    await waitFor(() => expect(brr.fetchBusinessRuleReviews).toHaveBeenCalledTimes(2));
    expect(screen.queryByText("업무 규칙 검토를 읽지 못했습니다.")).toBeNull();
  });

  it("그 밖의 실패는 '없음'으로 단정하지 않고 읽지 못했다고 알린다", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    vi.mocked(brr.fetchBusinessRuleReviews).mockRejectedValue(new ApiError(503, "unavailable"));
    show("candidate-7");
    expect(await screen.findByText("업무 규칙 검토를 읽지 못했습니다.")).toBeInTheDocument();
    warn.mockRestore();
  });

  it("후보가 아닌 항목은 읽지 않는다", () => {
    show("finding-3");
    expect(brr.fetchBusinessRuleReviews).not.toHaveBeenCalled();
  });
});
