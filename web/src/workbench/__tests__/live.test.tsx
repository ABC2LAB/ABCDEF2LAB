// 점검 진행 화면: 일시정지 · 종료 확인 · 지난 활동 더 보기 · 끝까지 재생한 결과 · 발견 패널.
// 재생 엔진 없이 스토어를 직접 한 단계씩 움직인다. 기대 문구·수치는 원본 시안과 같다.

import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { createInitialState, useWb } from "../store";
import { Live } from "../views/Live";

beforeEach(() => {
  useWb.setState(createInitialState());
});
afterEach(() => {
  cleanup();
});

/** 끝날 때까지 한 단계씩 재생 */
function playToEnd() {
  act(() => {
    for (let n = 0; n < 50 && !useWb.getState().live.done; n++) useWb.getState().liveStep();
  });
}

describe("<Live />", () => {
  it("이어 하는 점검(약 58%)을 원본 문구 그대로 보인다", () => {
    const { container } = render(<Live />);
    expect(screen.getByRole("heading", { level: 1, name: "점검 진행" })).toBeInTheDocument();
    expect(container.querySelector("#lState")).toHaveTextContent("진행 중");
    expect(screen.getByRole("progressbar", { name: "점검 진행률" })).toHaveAttribute("aria-valuenow", "58");
    expect(container.querySelector("#lPct")).toHaveTextContent("58%");
    expect(container.querySelector("#lNowK")).toHaveTextContent("지금 하는 일");
    expect(container.querySelector("#lTask")).toHaveTextContent(
      "결제 금액 변경 요청은 승인 없이 보내지 않음 · POST /api/checkout",
    );
    expect(container.querySelector("#lMeta")).toHaveTextContent(
      "시작 10:42경과 17분남은 시간 약 13분토큰 1.2M / 예상 2.0M엔드포인트 47 (처음 37 · 새로 10)",
    );
    expect(container.querySelector("#mSent")).toHaveTextContent("1,261");
    expect(container.querySelector("#lPlanN")).toHaveTextContent("5/10 완료");
    expect(screen.getByText("업무 흐름 우회: 결제·주문 (승인 필요) · 진행 중")).toBeInTheDocument();
    expect(container.querySelector("#lFeedN")).toHaveTextContent("11건");
    // 사람이 정할 새 경로
    expect(screen.getByRole("button", { name: "점검에 포함" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "발견 3" })).toBeInTheDocument();
  });

  it("일시정지를 누르면 멈춤, 다시 누르면 재생", () => {
    const { container } = render(<Live />);
    const pause = screen.getByRole("button", { name: "일시정지" });
    expect(pause).toHaveAttribute("aria-pressed", "false");
    fireEvent.click(pause);
    expect(useWb.getState().live.paused).toBe(true);
    expect(pause).toHaveAttribute("aria-pressed", "true");
    expect(pause).toHaveTextContent("다시 시작");
    expect(container.querySelector("#lState")).toHaveTextContent("일시정지됨");
    expect(container.querySelector("#lNowK")).toHaveTextContent("멈춤");
    expect(screen.getByText("업무 흐름 우회: 결제·주문 (승인 필요) · 멈춤")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "다시 시작" }));
    expect(useWb.getState().live.paused).toBe(false);
    expect(useWb.getState().live.playing).toBe(true);
    expect(pause).toHaveTextContent("일시정지");
    expect(container.querySelector("#lState")).toHaveTextContent("진행 중");
  });

  it("종료: 확인 대화상자 → 계속 점검이면 닫고, 종료면 끝내고 h1 에 포커스", () => {
    const { container } = render(<Live />);
    expect(screen.queryByRole("alertdialog")).toBeNull();
    const end = screen.getByRole("button", { name: "종료" });
    fireEvent.click(end);
    const dlg = screen.getByRole("alertdialog", { name: "점검을 종료할까요? 종료하면 다시 시작할 수 없습니다." });
    expect(document.activeElement).toBe(within(dlg).getByRole("button", { name: "종료" }));

    fireEvent.click(within(dlg).getByRole("button", { name: "계속 점검" }));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(document.activeElement).toBe(end);
    expect(useWb.getState().live.ended).toBe(false);

    fireEvent.click(end);
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "종료" }));
    expect(useWb.getState().live.ended).toBe(true);
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(document.activeElement).toBe(screen.getByRole("heading", { level: 1, name: "점검 진행" }));
    expect(container.querySelector("#lState")).toHaveTextContent("종료됨");
    expect(screen.queryByRole("button", { name: "일시정지" })).toBeNull();
    expect(screen.queryByRole("button", { name: "종료" })).toBeNull();
    expect(container.querySelector("#lNowK")).toHaveTextContent("종료됨");
    expect(container.querySelector("#lDoneT")).toHaveTextContent("점검을 종료했습니다");
    expect(container.querySelector("#lDoneS")).toHaveTextContent(
      "발견 3건 · 판정 중 3 · 종료 시점까지의 결과로 보고서를 만듭니다",
    );
    expect(screen.getByText("커버리지 정리 및 최종 보고 · 미검사")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "보고서 보기" })).toHaveAttribute("href", "#report");
    // 결정하지 않은 새 경로는 미검사로 남김
    expect(screen.getByText("결정 없음")).toBeInTheDocument();
  });

  it("지난 활동은 최근 5건, 더 보기로 전부, 다시 누르면 5건", () => {
    const { container } = render(<Live />);
    const feed = container.querySelector<HTMLElement>("#lFeed");
    if (!feed) throw new Error("no feed");
    expect(within(feed).getAllByRole("listitem")).toHaveLength(5);
    const more = screen.getByRole("button", { name: "이전 활동 6건 더 보기" });
    expect(more).toHaveAttribute("aria-expanded", "false");
    expect(more).toHaveAttribute("aria-controls", "lFeed");
    fireEvent.click(more);
    expect(within(feed).getAllByRole("listitem")).toHaveLength(11);
    expect(more).toHaveTextContent("최근 5건만 보기");
    expect(more).toHaveAttribute("aria-expanded", "true");
    fireEvent.click(more);
    expect(within(feed).getAllByRole("listitem")).toHaveLength(5);
  });

  it("끝까지 재생하면 발견 6건 · 확정 3 · 의심 2 · 판단 불가 1", () => {
    const { container } = render(<Live />);
    playToEnd();
    expect(container.querySelector("#lState")).toHaveTextContent("점검 완료");
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "100");
    expect(container.querySelector("#lNowK")).toHaveTextContent("완료");
    expect(container.querySelector("#lTask")).toHaveTextContent("계획한 작업을 모두 마쳤습니다");
    expect(container.querySelector("#lPlanN")).toHaveTextContent("10/10 완료");
    expect(container.querySelector("#lDoneT")).toHaveTextContent("점검을 마쳤습니다");
    expect(container.querySelector("#lDoneS")).toHaveTextContent(
      "발견 6건 · 확정 3 · 의심 2 · 판단 불가 1 · 보고서가 준비됐습니다",
    );
    const insp = container.querySelector<HTMLElement>("#lInsp");
    if (!insp) throw new Error("no insp");
    expect(within(insp).getByRole("heading", { level: 2, name: "발견 6건" })).toBeInTheDocument();
    expect(insp.querySelectorAll("[data-lsel]")).toHaveLength(6);
    expect(insp).toHaveTextContent("확정 3의심 2판단 불가 1");
    expect(insp).toHaveTextContent("검사함 43 / 47 엔드포인트");
    expect(screen.getByRole("button", { name: "발견 6" })).toBeInTheDocument();
  });

  it("발견을 고르면 상세와 '발견 목록' 버튼, 돌아가면 그 줄에 포커스 · 재시작하면 고른 것을 지운다", () => {
    const { container } = render(<Live />);
    const insp = container.querySelector<HTMLElement>("#lInsp");
    if (!insp) throw new Error("no insp");
    fireEvent.click(within(insp).getByRole("button", { name: /다른 사용자의 주문 내역이 조회됨/ }));
    const back = within(insp).getByRole("button", { name: "발견 목록" });
    expect(document.activeElement).toBe(back);
    expect(
      within(insp).getByRole("heading", { level: 2, name: "다른 사용자의 주문 내역이 조회됨" }),
    ).toBeInTheDocument();
    expect(insp).toHaveTextContent("판정 중");
    expect(insp).toHaveTextContent("검증 에이전트가 확인 중입니다.");
    expect(insp.querySelector("#repro-live")).toHaveTextContent(
      "curl -s https://shop.example.com/api/orders/1012 \\ -H 'Cookie: session=<계정 B 세션>'",
    );
    fireEvent.click(back);
    expect(document.activeElement).toBe(insp.querySelector('[data-lsel="f1"]'));

    fireEvent.click(within(insp).getByRole("button", { name: /다른 사용자의 주문 내역이 조회됨/ }));
    act(() => useWb.getState().liveReset("fresh", 9 * 3600));
    expect(within(insp).getByRole("heading", { level: 2, name: "발견 0건" })).toBeInTheDocument();
    expect(insp).toHaveTextContent("아직 발견이 없습니다.");
  });

  it("새 경로 결정: 점검에 포함을 누르면 포함함", () => {
    render(<Live />);
    fireEvent.click(screen.getByRole("button", { name: "점검에 포함" }));
    expect(useWb.getState().S.path).toBe("in");
    expect(screen.getByText("포함함")).toBeInTheDocument();
    expect(
      screen.getByText("계정 A, 계정 B, 비로그인으로 조회 요청만 보내 확인합니다. 결정은 감사 로그에 남았습니다."),
    ).toBeInTheDocument();
  });
});
