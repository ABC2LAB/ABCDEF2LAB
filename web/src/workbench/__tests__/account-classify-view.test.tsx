import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { AccountClassifyLive } from "../views/live/AccountClassifyLive";

afterEach(cleanup);

describe("AccountClassifyLive (목업)", () => {
  it("미정·목업·규칙2 고지와 분류 목록을 그린다", async () => {
    render(<AccountClassifyLive />);
    expect(await screen.findByText(/저장 위치 · 승인 주체가 아직 정해지지 않았습니다/)).toBeInTheDocument();
    expect(screen.getByText(/비밀번호 · 토큰 · 세션 값은 등록 · 표시 · 전송하지 않습니다/)).toBeInTheDocument();
    expect(screen.getByText(/지금은 어떤 계정에도 탐색 · 모델 입력 권한을 부여하지 않습니다/)).toBeInTheDocument();
    const list = within(screen.getByLabelText("계정 목록"));
    expect(list.getByText("acct:test-01")).toBeInTheDocument();
    expect(list.getAllByText("시험 계정").length).toBeGreaterThan(0);
    // 미표시 계정은 실계정 취급으로 표시
    expect(list.getAllByText(/실계정 취급/).length).toBeGreaterThan(0);
  });

  it("§2 운영자 전용: 내보내기·공유·export 버튼을 두지 않는다", async () => {
    render(<AccountClassifyLive />);
    await screen.findByText(/저장 위치 · 승인 주체/);
    for (const el of [...screen.queryAllByRole("button"), ...screen.queryAllByRole("link")]) {
      expect(el.textContent ?? "").not.toMatch(/내보내기|공유|export/i);
    }
  });

  it("목업 조작(로컬): 미표시 → 시험 표시 등록 → 승인", async () => {
    render(<AccountClassifyLive />);
    const list = within(await screen.findByLabelText("계정 목록"));
    const row = list.getByText("acct:legacy-03").closest("li") as HTMLElement;
    fireEvent.click(within(row).getByRole("button", { name: "시험 표시 등록" }));
    expect(within(row).getByText("표시 등록 · 승인 대기")).toBeInTheDocument();
    fireEvent.click(within(row).getByRole("button", { name: "승인" }));
    expect(within(row).getAllByText("시험 계정").length).toBeGreaterThan(0);
  });

  // #465 리뷰: §8은 목표 규칙(현행 S09) — 예시 승인·로컬 승인 어느 쪽도 지금 입력 가능으로 보이지 않는다.
  it("초기 승인 예시와 로컬 승인 뒤 모두 '모델 입력 가능'으로 표시하지 않는다", async () => {
    render(<AccountClassifyLive />);
    const list = within(await screen.findByLabelText("계정 목록"));
    expect(screen.queryByText(/쓸 수 있음/)).toBeNull();
    expect(list.queryByText(/모델 입력 가능/)).toBeNull();
    const approved = list.getByText("acct:test-01").closest("li") as HTMLElement;
    expect(within(approved).getByText(/아직 탐색 · 모델 입력 권한 없음/)).toBeInTheDocument();
    const row = list.getByText("acct:legacy-03").closest("li") as HTMLElement;
    fireEvent.click(within(row).getByRole("button", { name: "시험 표시 등록" }));
    fireEvent.click(within(row).getByRole("button", { name: "승인" }));
    expect(within(row).getByText(/아직 탐색 · 모델 입력 권한 없음/)).toBeInTheDocument();
    expect(list.queryByText(/모델 입력 가능/)).toBeNull();
  });
});
