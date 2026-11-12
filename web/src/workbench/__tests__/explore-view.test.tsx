import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { UrlExploreLive } from "../views/live/UrlExploreLive";

afterEach(cleanup);

describe("UrlExploreLive (예시)", () => {
  it("운영자 고지·예시 배너와 발견·경계 요약을 그린다", async () => {
    render(<UrlExploreLive />);
    // 운영자 전용 고지(§4)
    expect(screen.getByText(/운영자 전용/)).toBeInTheDocument();
    // 예시(mock) 배너
    expect(await screen.findByText(/예시 데이터입니다/)).toBeInTheDocument();
    // 두 섹션
    expect(screen.getByRole("heading", { name: "발견" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "경계 기록" })).toBeInTheDocument();
    // 발견 목록(메서드+경로가 한 칸): 발견 섹션 안에서만 확인
    expect(within(screen.getByLabelText("발견")).getByText(/\/rest\/products\/\{id\}/)).toBeInTheDocument();
    // 외부 호스트(요약·기록에 중복 등장 가능)
    expect(screen.getAllByText("evil.example.com").length).toBeGreaterThan(0);
    // 차단 사유가 한국어 라벨로 (집계·기록에 중복 등장 가능)
    expect(within(screen.getByLabelText("경계 기록")).getAllByText("외부 이동 차단").length).toBeGreaterThan(0);
  });

  it("§4: 내보내기·공유·export 버튼을 두지 않는다", async () => {
    render(<UrlExploreLive />);
    await screen.findByText(/예시 데이터입니다/);
    for (const el of [...screen.queryAllByRole("button"), ...screen.queryAllByRole("link")]) {
      expect(el.textContent ?? "").not.toMatch(/내보내기|공유|export/i);
    }
  });
});
