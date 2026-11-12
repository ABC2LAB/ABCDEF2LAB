import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { FindingRecord } from "@/lib/diagnosis/finding";

// getFinding/getFindingHeld 만 스파이로 감싸고(기본은 실제 호출 = 목업), 보수 하향 테스트에서만 교체한다.
// 판정·보수 하향·부족 증거 로직은 실 seam 그대로 — finding.test.ts 가 이미 검증.
vi.mock("@/lib/diagnosis/finding", async (importOriginal) => {
  const real = await importOriginal<typeof import("@/lib/diagnosis/finding")>();
  return {
    ...real,
    getFinding: vi.fn().mockImplementation(real.getFinding),
    getFindingHeld: vi.fn().mockImplementation(real.getFindingHeld),
  };
});

import * as finding from "@/lib/diagnosis/finding";

import { UrlFindingLive } from "../views/live/UrlFindingLive";

const m = vi.mocked(finding);

afterEach(cleanup);

describe("UrlFindingLive (예시)", () => {
  it("확정 사례: 예시·마스킹 고지 + 확정 배지 + 다계정 비교(취약) + 표식 필드명만", async () => {
    const { container } = render(<UrlFindingLive />);
    expect(await screen.findByText(/예시 데이터/)).toBeInTheDocument();
    // 마스킹 고지(응답 본문·값·자격 원문 비표시)
    expect(screen.getByText(/표시하지 않습니다/)).toBeInTheDocument();
    // 발견 제목
    expect(screen.getByText("다른 사용자의 자원이 교차 계정으로 조회됨")).toBeInTheDocument();
    // 화면 판정 배지 = 확정(증거 완비)
    const badge = container.querySelector("[data-verdict]") as HTMLElement;
    expect(badge).toHaveAttribute("data-verdict", "confirmed");
    expect(badge).toHaveTextContent("확정");
    // 다계정 비교: B 행이 취약(확정이라 보수 하향 없음)
    expect(container.querySelector('[data-row="vuln"]')).not.toBeNull();
    // 표식은 필드명만(값 없음) — 'body' 필드명이 보이고 raw 값/URL 은 없다
    expect(screen.getByText(/표식 필드: body/)).toBeInTheDocument();
    expect(container.textContent ?? "").not.toMatch(/https?:\/\//);
    expect(container.textContent ?? "").not.toMatch(/Cookie:/i);
  });

  it("송신 전 차단 사례로 전환하면 판단 불가 + E² 차단 서사 + 비교 없음", async () => {
    render(<UrlFindingLive />);
    await screen.findByText(/예시 데이터/);
    fireEvent.click(screen.getByRole("button", { name: "송신 전 차단 사례" }));
    // 전환 후 판정 배지 = 판단 불가
    const badge = await screen.findByText("판단 불가");
    expect(badge).toHaveAttribute("data-verdict", "undecidable");
    // E²가 송신 전 차단 서사(미실행→증거 없음)
    expect(screen.getByText(/송신 전에 차단/)).toBeInTheDocument();
    // 전송이 없어 비교 표가 없다
    expect(screen.getByText(/비교가 없습니다/)).toBeInTheDocument();
  });

  it("confirmed 인데 증거 불완비면 화면 판정을 보수적으로 판단 불가로 내리고 원값은 감사용으로 보인다(절대규칙 5)", async () => {
    const real = await vi.importActual<typeof import("@/lib/diagnosis/finding")>("@/lib/diagnosis/finding");
    const base = await real.getFinding(null);
    const incomplete: FindingRecord = {
      ...base,
      // 데이터 체크가 불명 → 증거 3조건 불완비 → 화면 판정 하향(원값 confirmed 는 보존)
      checks: { ...base.checks, data: "unknown" },
      signals: { ...base.signals, nontrivialDataMatch: "unknown" },
    };
    m.getFinding.mockResolvedValueOnce(incomplete);
    const { container } = render(<UrlFindingLive />);
    await screen.findByText(/예시 데이터/);
    const badge = container.querySelector("[data-verdict]") as HTMLElement;
    expect(badge).toHaveAttribute("data-verdict", "undecidable"); // 화면 판정 하향
    expect(screen.getByText(/감사용/)).toBeInTheDocument(); // 원값 보존 고지
    // 상단이 판단 불가면 비교 행의 취약도 보수 하향(모순 방지) — vuln 행이 남지 않는다
    expect(container.querySelector('[data-row="vuln"]')).toBeNull();
  });

  it("내보내기·공유·export 버튼을 두지 않는다", async () => {
    render(<UrlFindingLive />);
    await screen.findByText(/예시 데이터/);
    for (const el of [...screen.queryAllByRole("button"), ...screen.queryAllByRole("link")]) {
      expect(el.textContent ?? "").not.toMatch(/내보내기|공유|export/i);
    }
  });
});
