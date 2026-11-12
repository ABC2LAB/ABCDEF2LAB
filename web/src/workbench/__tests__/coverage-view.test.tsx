import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { CoverageData } from "@/lib/diagnosis/coverage";

// getCoverage 만 스파이로 감싸고(기본은 실제 호출 = MOCK_COVERAGE), 무결성 경고 테스트에서만 교체한다.
// 집계·무결성(attempted·summarize·coverageIssues…)은 실 로직 그대로 — coverage.test.ts 가 이미 검증.
vi.mock("@/lib/diagnosis/coverage", async (importOriginal) => {
  const real = await importOriginal<typeof import("@/lib/diagnosis/coverage")>();
  return { ...real, getCoverage: vi.fn().mockImplementation(real.getCoverage) };
});

import * as coverage from "@/lib/diagnosis/coverage";

import { UrlCoverageLive } from "../views/live/UrlCoverageLive";

const m = vi.mocked(coverage);

afterEach(cleanup);

describe("UrlCoverageLive (예시)", () => {
  it("예시 배너와 요약·영역별 커버리지를 그린다", async () => {
    render(<UrlCoverageLive />);
    expect(await screen.findByText(/예시 데이터/)).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "검사 커버리지 요약" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "영역별 커버리지" })).toBeInTheDocument();
    // 목업 세 영역 라벨
    expect(screen.getByText("수평 읽기 IDOR")).toBeInTheDocument();
    expect(screen.getByText("수직 권한 상승")).toBeInTheDocument();
    expect(screen.getByText("상태 변경 · 비즈니스 로직")).toBeInTheDocument();
    // 절대규칙 5: '미시도'는 조회 안 함이며 '없음·안전'이 아님을 명시
    expect(screen.getByText(/조회 안 함/)).toBeInTheDocument();
  });

  it("확정과 미시도는 색으로 구분하고, 미시도를 정상색으로 칠하지 않는다(절대규칙 5)", async () => {
    const { container } = render(<UrlCoverageLive />);
    await screen.findByText(/예시 데이터/);
    const confirmed = container.querySelector('[data-status="confirmed"]') as HTMLElement;
    const untested = container.querySelector('[data-status="untested"]') as HTMLElement;
    expect(confirmed).not.toBeNull();
    expect(untested).not.toBeNull();
    // 미시도는 채운 색이 아니라 투명(점선) — 확정색과 달라야 한다
    expect(untested.style.background).toBe("transparent");
    expect(untested.style.borderStyle).toBe("dashed");
    expect(confirmed.style.background).not.toBe(untested.style.background);
  });

  it("집계 불일치(계획≠상태합)는 숨기지 않고 fail-closed 경고로 띄운다", async () => {
    const bad: CoverageData = {
      policyVersion: "test",
      mock: true,
      areas: [
        {
          id: "h-read",
          label: "수평 읽기 IDOR",
          axis: "horizontal",
          planned: 9, // 상태합 4 ≠ 9
          counts: { confirmed: 1, suspected: 0, undecidable: 0, blank: 0, in_progress: 0, untested: 3, na: 0 },
        },
      ],
    };
    m.getCoverage.mockResolvedValueOnce(bad);
    render(<UrlCoverageLive />);
    expect(await screen.findByRole("alert")).toHaveTextContent("불일치");
  });

  it("내보내기·공유·export 버튼을 두지 않는다", async () => {
    render(<UrlCoverageLive />);
    await screen.findByText(/예시 데이터/);
    for (const el of [...screen.queryAllByRole("button"), ...screen.queryAllByRole("link")]) {
      expect(el.textContent ?? "").not.toMatch(/내보내기|공유|export/i);
    }
  });
});
