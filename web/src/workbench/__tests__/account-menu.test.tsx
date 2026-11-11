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
