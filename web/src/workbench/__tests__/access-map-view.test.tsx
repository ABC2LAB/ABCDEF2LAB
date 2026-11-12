import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { AccessMap } from "@/lib/diagnosis/kg";

// getAccessMap 만 스파이로 감싸고(기본은 실제 호출 = MOCK_MAP), projection 불일치 테스트에서만 교체한다.
// 리졸버·집계(buildCellResolver·countDecisions…)는 실 로직 그대로 — fail-closed 규칙은 kg.test.ts 가 이미 검증.
vi.mock("@/lib/diagnosis/kg", async (importOriginal) => {
  const real = await importOriginal<typeof import("@/lib/diagnosis/kg")>();
  return { ...real, getAccessMap: vi.fn().mockImplementation(real.getAccessMap) };
});

import * as kg from "@/lib/diagnosis/kg";

import { UrlAccessMapLive } from "../views/live/UrlAccessMapLive";

const m = vi.mocked(kg);

afterEach(cleanup);

describe("UrlAccessMapLive (예시)", () => {
  it("운영자 고지·예시 배너와 수평 판정·격자를 그린다", async () => {
    render(<UrlAccessMapLive />);
    // 운영자 전용 고지(§4)
    expect(screen.getByText(/운영자 전용/)).toBeInTheDocument();
    // 예시(mock) 배너
    expect(await screen.findByText(/예시 데이터/)).toBeInTheDocument();
    // 두 섹션(판정 요약 · 격자)
    expect(screen.getByRole("heading", { name: "수평(교차 계정) 판정" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "접근통제 격자" })).toBeInTheDocument();
    // 격자(신원 × 자원)
    const grid = screen.getByRole("table", { name: /격자/ });
    // 확정·의심이 격자 안에 둘 다 나온다(수평 IDOR 목업)
    expect(within(grid).getAllByText("취약 확정").length).toBeGreaterThan(0);
    expect(within(grid).getAllByText("취약 의심").length).toBeGreaterThan(0);
    // deny-ok(정상 차단) Phase 3 보류·미검사 Phase 2 를 화면에 명시
    expect(screen.getByText(/정상 차단/)).toHaveTextContent("산출하지 않습니다");
  });

  it("확정과 의심은 색으로 구분한다(§4.2(3) 축소 화면 오인 방지)", async () => {
    render(<UrlAccessMapLive />);
    await screen.findByText(/예시 데이터/);
    const grid = screen.getByRole("table", { name: /격자/ });
    const vuln = within(grid).getAllByText("취약 확정")[0].closest("[data-decision]") as HTMLElement;
    const suspected = within(grid).getAllByText("취약 의심")[0].closest("[data-decision]") as HTMLElement;
    expect(vuln).toHaveAttribute("data-decision", "vuln");
    expect(suspected).toHaveAttribute("data-decision", "suspected");
    // 배지로도 보강하지만, 색(배경)이 서로 달라야 한다 — 배지만으로 구분하지 않는다
    expect(vuln.style.background).not.toBe("");
    expect(vuln.style.background).not.toBe(suspected.style.background);
    // 확정/의심 배지(§4.2(7))
    expect(within(grid).getAllByText("확정").length).toBeGreaterThan(0);
    expect(within(grid).getAllByText("의심").length).toBeGreaterThan(0);
  });

  it("projection 불일치는 숨기지 않고 fail-closed 경고로 띄운다(§4.2(2)·N4)", async () => {
    const badMap: AccessMap = {
      policyVersion: "test",
      mock: true,
      principals: [{ ref: "A", role: "u", axis: "H" }],
      resources: [{ id: "r1", label: "r1", ownerRef: "A" }],
      cells: [
        { principalRef: "A", resourceId: "r1", decision: "vuln" },
        { principalRef: "Z", resourceId: "r1", decision: "vuln" }, // Z 는 신원 격자 밖
      ],
    };
    m.getAccessMap.mockResolvedValueOnce(badMap);
    render(<UrlAccessMapLive />);
    expect(await screen.findByRole("alert")).toHaveTextContent("불일치");
  });

  it("§4: 내보내기·공유·export 버튼을 두지 않는다", async () => {
    render(<UrlAccessMapLive />);
    await screen.findByText(/예시 데이터/);
    for (const el of [...screen.queryAllByRole("button"), ...screen.queryAllByRole("link")]) {
      expect(el.textContent ?? "").not.toMatch(/내보내기|공유|export/i);
    }
  });
});
