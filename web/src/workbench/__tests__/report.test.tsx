// 보고서 화면: 발견 고르기 · 탭(화살표 이동) · 지식 그래프 노드/경로 고르기 → 상세 패널 · 보고서 대상 바꾸기.
// 기대 문구·수치는 원본 시안과 같다. 클래스 이름은 검사하지 않는다.

import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { createInitialState, useWb } from "../store";
import { Report } from "../views/Report";

beforeEach(() => {
  useWb.setState(createInitialState());
});
afterEach(() => {
  cleanup();
});

function insp(container: HTMLElement): HTMLElement {
  const el = container.querySelector<HTMLElement>("#rInsp");
  if (!el) throw new Error("no #rInsp");
  return el;
}

function openGraph() {
  fireEvent.click(screen.getByRole("tab", { name: "자산 지식 그래프" }));
}

describe("<Report />", () => {
  it("처음에는 blog 예시 보고서와 첫 발견(f1) 상세", () => {
    const { container } = render(<Report />);
    expect(screen.getByRole("heading", { level: 1, name: "blog.example.com 보고서" })).toBeInTheDocument();
    expect(container.querySelector("#rPill")).toHaveTextContent("점검 완료");
    expect(container.querySelector("#rMeta")).toHaveTextContent(
      "시작 10/25 14:03소요 28분모델 qwen/qwen3.6-35b-a3b · 로컬커버리지 91%",
    );
    expect(container.querySelector("#rExample")).not.toBeVisible();
    const box = insp(container);
    expect(
      within(box).getByRole("heading", { level: 2, name: "다른 사용자의 주문 내역이 조회됨" }),
    ).toBeInTheDocument();
    expect(box).toHaveTextContent("수평 IDOR · blog.example.com");
    expect(box).toHaveTextContent("조치 및 재점검");
    expect(box.querySelector("#repro-report")).toHaveTextContent("curl -s https://blog.example.com/api/orders/1012");
    const row = screen.getByRole("button", { name: /다른 사용자의 주문 내역이 조회됨/ });
    expect(row).toHaveAttribute("aria-current", "true");
  });

  it("발견 목록에서 고르면 그 줄이 현재 · 포커스 · 상세가 바뀐다", () => {
    const { container } = render(<Report />);
    const row = screen.getByRole("button", { name: /비로그인 상태에서 관리자 통계가 열림/ });
    fireEvent.click(row);
    expect(row).toHaveAttribute("aria-current", "true");
    expect(document.activeElement).toBe(row);
    expect(screen.getByRole("button", { name: /다른 사용자의 주문 내역이 조회됨/ })).not.toHaveAttribute(
      "aria-current",
    );
    const box = insp(container);
    expect(
      within(box).getByRole("heading", { level: 2, name: "비로그인 상태에서 관리자 통계가 열림" }),
    ).toBeInTheDocument();
    expect(within(box).getByText("신규")).toBeInTheDocument();
    expect(box).toHaveTextContent("세션 쿠키 없이 보낸 요청 1건 · 비교용 대조군 1건");
  });

  it("탭: 누르면 그 패널만 보이고, 좌우 화살표로 옮겨 다닌다(끝에서 되돌아감)", () => {
    render(<Report />);
    const list = screen.getByRole("tab", { name: /^발견 목록/ });
    expect(list).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tabpanel", { name: /^발견 목록/ })).toBeVisible();
    openGraph();
    const graph = screen.getByRole("tab", { name: "자산 지식 그래프" });
    expect(graph).toHaveAttribute("aria-selected", "true");
    expect(graph).toHaveAttribute("tabindex", "0");
    expect(list).toHaveAttribute("tabindex", "-1");
    expect(screen.getByRole("tabpanel", { name: "자산 지식 그래프" })).toBeVisible();
    expect(screen.queryByRole("tabpanel", { name: /^발견 목록/ })).toBeNull();

    fireEvent.keyDown(graph, { key: "ArrowRight" });
    const map = screen.getByRole("tab", { name: "접근통제 지도" });
    expect(map).toHaveAttribute("aria-selected", "true");
    expect(document.activeElement).toBe(map);
    fireEvent.keyDown(map, { key: "ArrowLeft" });
    fireEvent.keyDown(graph, { key: "ArrowLeft" });
    expect(document.activeElement).toBe(list);
    fireEvent.keyDown(list, { key: "ArrowLeft" });
    expect(screen.getByRole("tab", { name: "증거 · 감사" })).toHaveAttribute("aria-selected", "true");
  });

  it("그래프 노드를 고르면 상세 패널이 그 노드 · Enter 로도 · 관계를 누르면 그 노드로 옮겨 포커스", () => {
    const { container } = render(<Report />);
    openGraph();
    const svg = container.querySelector("#kgSvg");
    if (!svg) throw new Error("no svg");
    expect(svg).toHaveAttribute("viewBox", "0 0 860 490");
    fireEvent.click(screen.getByRole("button", { name: "계정 B, 관계 6개" }));
    const box = insp(container);
    expect(within(box).getByRole("heading", { level: 2, name: "계정 B" })).toBeInTheDocument();
    expect(box).toHaveTextContent("계정 · 역할 · blog.example.com");
    expect(box).toHaveTextContent("취약 1의심 1미검사 1차단 1허용 1");
    expect(within(box).getByRole("heading", { level: 3, name: "관계 6" })).toBeInTheDocument();
    expect(box.querySelectorAll("[data-kgf]")).toHaveLength(2);

    const stats = screen.getByRole("button", { name: "GET /admin/stats, 관계 4개" });
    fireEvent.keyDown(stats, { key: "Enter" });
    expect(within(box).getByRole("heading", { level: 2, name: "GET /admin/stats" })).toBeInTheDocument();

    // 다른 탭에 있어도 관계를 누르면 그래프 탭으로 돌아가 그 노드에 포커스
    fireEvent.click(screen.getByRole("tab", { name: /^발견 목록/ }));
    fireEvent.click(within(box).getByRole("button", { name: /매출 통계/ }));
    expect(screen.getByRole("tab", { name: "자산 지식 그래프" })).toHaveAttribute("aria-selected", "true");
    expect(within(box).getByRole("heading", { level: 2, name: "매출 통계" })).toBeInTheDocument();
    expect(document.activeElement).toBe(svg.querySelector('[data-kgn="dStats"]'));

    // 관련 발견을 누르면 발견 상세
    fireEvent.click(screen.getByRole("button", { name: "비로그인, 관계 2개" }));
    fireEvent.click(within(box).getByRole("button", { name: /비로그인 상태에서 관리자 통계가 열림/ }));
    expect(
      within(box).getByRole("heading", { level: 2, name: "비로그인 상태에서 관리자 통계가 열림" }),
    ).toBeInTheDocument();
  });

  it("공격 경로를 누르면 눌림 · 그 발견 상세, 다시 누르면 풀림 · 발견 표시를 누르면 그 발견", () => {
    const { container } = render(<Report />);
    openGraph();
    expect(screen.getByText("역할 4 · 엔드포인트 7 · 자원 6 · 관계 24")).toBeInTheDocument();
    const p2 = screen.getByRole("button", { name: /비로그인 → 매출 통계/ });
    expect(p2).toHaveAttribute("aria-pressed", "false");
    fireEvent.click(p2);
    expect(p2).toHaveAttribute("aria-pressed", "true");
    const box = insp(container);
    expect(
      within(box).getByRole("heading", { level: 2, name: "비로그인 상태에서 관리자 통계가 열림" }),
    ).toBeInTheDocument();
    fireEvent.click(p2);
    expect(p2).toHaveAttribute("aria-pressed", "false");

    fireEvent.click(screen.getByRole("button", { name: "발견: 쿠키에 보안 속성이 빠져 있음" }));
    expect(within(box).getByRole("heading", { level: 2, name: "쿠키에 보안 속성이 빠져 있음" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /서버 버전 정보가 노출됨/ }));
    expect(within(box).getByRole("heading", { level: 2, name: "서버 버전 정보가 노출됨" })).toBeInTheDocument();
  });

  it("보고서 대상을 방금 끝낸 점검으로 바꾸면 제목·알약·메타·예시 안내가 바뀌고 노드 상세는 지운다", () => {
    const { container } = render(<Report />);
    openGraph();
    fireEvent.click(screen.getByRole("button", { name: "계정 A, 관계 7개" }));
    expect(within(insp(container)).getByRole("heading", { level: 2, name: "계정 A" })).toBeInTheDocument();

    act(() => {
      useWb.getState().liveEnd();
      useWb.getState().setReportHost("live");
    });
    expect(screen.getByRole("heading", { level: 1, name: "shop.example.com 보고서" })).toBeInTheDocument();
    expect(container.querySelector("#rCrumbHost")).toHaveTextContent("shop.example.com");
    expect(container.querySelector("#rPill")).toHaveTextContent("종료됨");
    expect(container.querySelector("#rMeta")).toHaveTextContent(
      "시작 오늘 10:42소요 17분모델 qwen/qwen3.6-35b-a3b · 로컬커버리지 예시조기 종료 · 실제로는 종료 시점까지만 집계",
    );
    expect(container.querySelector("#rExample")).toBeVisible();
    expect(container.querySelector("#rExample")).toHaveTextContent(
      "방금 종료한 점검의 실제 결과가 아닙니다. 아래 발견·판정·커버리지·그래프는 완료된 예시 점검(blog.example.com)의 고정 샘플입니다. 실제로 조기 종료하면 종료 시점까지 수집한 결과만, 커버리지도 그만큼만 집계됩니다.",
    );
    const box = insp(container);
    expect(
      within(box).getByRole("heading", { level: 2, name: "다른 사용자의 주문 내역이 조회됨" }),
    ).toBeInTheDocument();
    expect(box).toHaveTextContent("수평 IDOR · shop.example.com");
    expect(container.querySelector("#kgBox")).toHaveTextContent("대상 shop.example.com");

    act(() => useWb.getState().setReportHost("blog"));
    expect(screen.getByRole("heading", { level: 1, name: "blog.example.com 보고서" })).toBeInTheDocument();
    expect(container.querySelector("#rPill")).toHaveTextContent("점검 완료");
    expect(container.querySelector("#rExample")).not.toBeVisible();
  });
});
