// 레일 묶음 접기 · 계정 메뉴 · 다크 모드 (시안 v20 · v21 · v22 를 옮긴 것).

import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/diagnosis/api", async (importOriginal) => {
  const real = await importOriginal<typeof import("@/lib/diagnosis/api")>();
  return { ...real, signIn: vi.fn(), signOut: vi.fn() };
});

import * as teamApi from "@/lib/diagnosis/api";

import { setLiveAuthState, useLiveAuth } from "../live/auth";
import { LiveRailTasks } from "../live/rail";
import { createInitialState, useWb } from "../store";
import { AccountMenu } from "../ui/AccountMenu";
import s from "../workbench.module.css";

const m = vi.mocked(teamApi);

/** 메뉴가 그려질 작업대 뿌리 + 계정 버튼 */
function Foot() {
  return (
    <div data-wb-root="">
      <AccountMenu />
    </div>
  );
}

let login: ReturnType<typeof useLiveAuth>["login"];
function GrabLogin() {
  login = useLiveAuth().login;
  return null;
}

beforeEach(() => {
  localStorage.clear();
  useWb.setState(createInitialState());
  m.signIn.mockReset();
  m.signOut.mockReset();
});
afterEach(() => {
  cleanup();
  setLiveAuthState("checking");
});

describe("레일 묶음 (진행 중 · 끝난 점검)", () => {
  it("머리를 누르면 접히고 건수는 남으며, 이 브라우저에 기억한다", () => {
    render(<LiveRailTasks />);
    expect(screen.queryByText("최근 끝난 점검")).toBeNull();
    const run = screen.getByRole("button", { name: /^진행 중/ });
    const done = screen.getByRole("button", { name: /^끝난 점검/ });
    expect(run).toHaveAttribute("aria-expanded", "true");
    expect(within(run).getByRole("img", { name: "0건" })).toBeInTheDocument();
    expect(screen.getByText("진행 중인 점검 없음")).toBeVisible();

    fireEvent.click(run);
    expect(run).toHaveAttribute("aria-expanded", "false");
    expect(screen.getByText("진행 중인 점검 없음")).not.toBeVisible();
    expect(screen.getByText("끝난 점검 없음")).toBeVisible();
    expect(localStorage.getItem("abc2lab-rsec-run")).toBe("0");
    expect(done).toHaveAttribute("aria-expanded", "true");

    cleanup();
    render(<LiveRailTasks />);
    expect(screen.getByRole("button", { name: /^진행 중/ })).toHaveAttribute("aria-expanded", "false");
  });
});

describe("계정 메뉴", () => {
  it("누르면 위로 열리고, 다크 모드 · 강조색을 바꾸고, Esc 로 닫힌다", async () => {
    localStorage.setItem("abc2lab.accountName", "ABC2LAB");
    setLiveAuthState("ready");
    render(<Foot />);
    const btn = screen.getByRole("button", { name: /ABC2LAB/ });
    expect(btn).toHaveAttribute("aria-expanded", "false");
    expect(btn).toHaveTextContent("AB");

    fireEvent.click(btn);
    const menu = screen.getByRole("dialog", { name: "계정" });
    expect(btn).toHaveAttribute("aria-expanded", "true");
    expect(within(menu).queryByText(/엔진 계정/)).toBeNull();
    expect(within(menu).getByText("분석 모델")).toBeInTheDocument();

    const sw = within(menu).getByRole("switch", { name: "다크 모드" });
    expect(sw).toHaveAttribute("aria-checked", "false");
    fireEvent.click(sw);
    expect(useWb.getState().theme).toBe("dark");
    expect(localStorage.getItem("abc2lab-theme")).toBe("dark");
    expect(sw).toHaveAttribute("aria-checked", "true");

    fireEvent.click(within(menu).getByRole("button", { name: "코발트" }));
    expect(useWb.getState().accent).toBe("cobalt");
    expect(screen.getByRole("dialog", { name: "계정" })).toBeInTheDocument();

    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" });
    expect(screen.queryByRole("dialog", { name: "계정" })).toBeNull();
    expect(btn).toHaveFocus();
  });

  it("시안처럼 메뉴가 계정 버튼 바로 뒤에 있는 Tab 순서: 끝 항목에서 Tab 이면 닫히고, 첫 항목에서 Shift+Tab 이면 버튼으로", () => {
    localStorage.setItem("abc2lab.accountName", "ABC2LAB");
    setLiveAuthState("ready");
    render(<Foot />);
    const btn = screen.getByRole("button", { name: /ABC2LAB/ });
    fireEvent.click(btn);
    let menu = screen.getByRole("dialog", { name: "계정" });
    // 첫 항목 = 메뉴 안 첫 버튼 (로그인 상태면 다크 모드 스위치)
    const first = within(menu).getByRole("switch", { name: "다크 모드" });
    // 열면 첫 항목에 포커스, Shift+Tab 이면 계정 버튼으로 가고 메뉴는 열린 채
    expect(first).toHaveFocus();
    fireEvent.keyDown(first, { key: "Tab", shiftKey: true });
    expect(btn).toHaveFocus();
    expect(screen.getByRole("dialog", { name: "계정" })).toBeInTheDocument();
    // 끝 항목(로그아웃)에서 Tab: 메뉴를 닫고 버튼에서 다음 칸으로 (기본 동작은 막지 않음)
    menu = screen.getByRole("dialog", { name: "계정" });
    const last = within(menu).getByRole("button", { name: "로그아웃" });
    last.focus();
    const ev = fireEvent.keyDown(last, { key: "Tab" });
    expect(ev).toBe(true);
    expect(screen.queryByRole("dialog", { name: "계정" })).toBeNull();
    expect(btn).toHaveFocus();
  });

  it("계정 버튼은 시안 짜임(.who > b · .chev)이고, 레일을 접으면 '계정' 이름표가 붙는다", () => {
    localStorage.setItem("abc2lab.accountName", "ABC2LAB");
    setLiveAuthState("ready");
    render(<Foot />);
    const btn = screen.getByRole("button", { name: /ABC2LAB/ });
    expect(btn.querySelector(`.${s.who} > b`)).toHaveTextContent("ABC2LAB");
    expect(btn.querySelector(`svg.${s.chev}`)).not.toBeNull();
    expect(btn).not.toHaveAttribute("title");
    act(() => useWb.getState().setPanelMin("rail", true));
    expect(btn).toHaveAttribute("title", "계정");
    // 메뉴 머리: 머리글자 + 이름 칸(div > b)
    fireEvent.click(btn);
    const head = screen.getByRole("dialog", { name: "계정" }).querySelector(`.${s["am-head"]}`);
    expect(head?.querySelector(":scope > div > b")).toHaveTextContent("ABC2LAB");
  });

  it("로그아웃하면 '로그인 안 됨'이 되고, 기억한 이름도 지우며, 메뉴에는 로그인 항목만 남는다", async () => {
    localStorage.setItem("abc2lab.accountName", "ABC2LAB");
    setLiveAuthState("ready");
    render(<Foot />);
    fireEvent.click(screen.getByRole("button", { name: /ABC2LAB/ }));
    fireEvent.click(within(screen.getByRole("dialog", { name: "계정" })).getByRole("button", { name: "로그아웃" }));
    expect(m.signOut).toHaveBeenCalled();
    // 공용 단말에 이전 아이디가 남지 않게
    expect(localStorage.getItem("abc2lab.accountName")).toBeNull();
    const btn = screen.getByRole("button", { name: /로그인 안 됨/ });
    fireEvent.click(btn);
    const menu = screen.getByRole("dialog", { name: "계정" });
    expect(within(menu).getByRole("button", { name: /^로그인/ })).toBeInTheDocument();
    expect(within(menu).queryByRole("button", { name: "로그아웃" })).toBeNull();
  });

  it("로그인하면 아이디만 기억해 이름 · 머리글자로 보인다 (비밀번호는 남기지 않음)", async () => {
    m.signIn.mockResolvedValue(undefined as never);
    setLiveAuthState("login");
    render(
      <>
        <GrabLogin />
        <Foot />
      </>,
    );
    expect(screen.getByRole("button", { name: /로그인 안 됨/ })).toBeInTheDocument();
    await act(async () => {
      await login("tester", "secret-pw");
    });
    const btn = screen.getByRole("button", { name: /tester/ });
    expect(btn).toHaveTextContent("TE");
    expect(localStorage.getItem("abc2lab.accountName")).toBe("tester");
    for (let i = 0; i < localStorage.length; i++) {
      expect(localStorage.getItem(localStorage.key(i) ?? "")).not.toContain("secret-pw");
    }
  });
});
