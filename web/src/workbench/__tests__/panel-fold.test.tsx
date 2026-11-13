// 넓은 화면의 레일 · 상세 패널 접기 (시안 rail-min · insp-min): 버튼으로 64px 줄로 접고 펴고, 이 브라우저에 기억하고,
// 좁은 화면의 서랍 동작은 그대로인지. 화면 내용은 관심 밖이라 여섯 화면을 머리 버튼 · 상세 패널만 있는 작은 화면으로 바꾼다.

import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

vi.mock("@/lib/diagnosis/api", async (importOriginal) => {
  const real = await importOriginal<typeof import("@/lib/diagnosis/api")>();
  return {
    ...real,
    apiFetch: vi.fn(),
    readAuthStatus: vi.fn(),
    restoreSession: vi.fn(),
    subscribeAuthRequired: vi.fn(() => () => undefined),
  };
});

vi.mock("../views/live/byMode", async () => {
  const { InspHead, InspOpenButton, MenuButton, PaneToggle } = await import("../ui/parts");
  const styles = (await import("../workbench.module.css")).default;
  const View = () => (
    <>
      <header>
        <MenuButton />
        <h1 tabIndex={-1}>화면</h1>
        <InspOpenButton>상세</InspOpenButton>
      </header>
      <div className={styles.vb}>
        <div className={styles.vm} />
        <aside className={styles.vi} aria-label="상세 패널">
          <InspHead title="상세" />
        </aside>
        <PaneToggle />
      </div>
    </>
  );
  return {
    DashboardByMode: View,
    RunningByMode: View,
    FinishedByMode: View,
    NewScanByMode: View,
    ScopeByMode: View,
    LiveByMode: View,
    ReportByMode: View,
  };
});
vi.mock("../views/live/UrlAssetsLive", () => ({ UrlAssetsLive: () => null }));

import * as teamApi from "@/lib/diagnosis/api";
import { handle, running } from "@/lib/diagnosis/diagnosis-url-fixtures";

import { setLiveAuthState } from "../live/auth";
import { resetFeeds } from "../live/feeds";
import { resetUrl, useUrl } from "../live/urlDiagnosis";
import { ShellContext, useShell } from "../shell";
import { createInitialState, useWb } from "../store";
import { PaneToggle } from "../ui/parts";
import { Workbench } from "../Workbench";
import s from "../workbench.module.css";

const m = vi.mocked(teamApi);

/** 화면 폭 흉내: narrow = max-width 1199px, phone = max-width 759px. */
function screenWidth({ narrow, phone }: { narrow: boolean; phone: boolean }) {
  const answer = (query: string) => {
    if (query.includes("1199")) return narrow;
    if (query.includes("759")) return phone;
    return false;
  };
  window.matchMedia = ((query: string) => ({
    matches: answer(query),
    media: query,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
  })) as unknown as typeof window.matchMedia;
}

const app = (c: HTMLElement) => c.querySelector(`.${s.app}`) as HTMLElement;
const dashboard = (c: HTMLElement) => c.querySelector('[data-view="dashboard"]') as HTMLElement;

async function mount() {
  const r = render(<Workbench />);
  await screen.findAllByRole("heading", { name: "화면" });
  return r.container;
}

beforeEach(() => {
  for (const f of [m.apiFetch, m.readAuthStatus, m.restoreSession]) f.mockReset();
  m.readAuthStatus.mockResolvedValue({ initialized: true } as never);
  m.restoreSession.mockResolvedValue(true);
  m.apiFetch.mockRejectedValue(new Error("offline"));
  localStorage.clear();
  window.location.hash = "dashboard";
  setLiveAuthState("checking");
  resetFeeds();
  resetUrl();
  useWb.setState(createInitialState());
});
afterEach(cleanup);

it("넓은 화면: 레일 접기 버튼으로 아이콘 줄로 접고, 같은 자리의 펼치기 버튼으로 다시 펼치며, 이 브라우저에 기억한다", async () => {
  screenWidth({ narrow: false, phone: false });
  const c = await mount();
  const fold = screen.getByRole("button", { name: "메뉴 접기" });
  expect(fold).toHaveAttribute("aria-expanded", "true");
  expect(fold).toHaveAttribute("aria-controls", "rail");
  fireEvent.click(fold);
  expect(app(c)).toHaveClass(s["rail-min"]);
  expect(localStorage.getItem("abc2lab-rail-min")).toBe("1");
  // 서랍이 아니라 접기라 가림막을 띄우지 않는다
  expect(c.querySelector("[data-scrim]")).not.toBeVisible();
  // 접힌 줄에도 레일은 남고(새 점검 · 대시보드 · 계정), 마우스를 올리면 이름이 뜬다 (시안 RAIL_TIPS)
  expect(screen.getByRole("link", { name: /새 점검/ })).toHaveAttribute("title", "새 점검");
  expect(c.querySelector("#acctBtn")).toHaveAttribute("title", "계정");
  const unfold = screen.getByRole("button", { name: "메뉴 펼치기" });
  expect(unfold).toHaveAttribute("aria-expanded", "false");
  expect(unfold).toHaveAttribute("title", "메뉴 펼치기");
  fireEvent.click(unfold);
  expect(app(c)).not.toHaveClass(s["rail-min"]);
  // 펼친 것도 기억한다 (시안처럼 '0')
  expect(localStorage.getItem("abc2lab-rail-min")).toBe("0");
  expect(screen.getByRole("button", { name: "메뉴 접기" })).toBeInTheDocument();
  expect(screen.getByRole("link", { name: /새 점검/ })).not.toHaveAttribute("title");
  expect(c.querySelector("#acctBtn")).not.toHaveAttribute("title");
  expect(c.querySelector(`.${s.rail}`)).not.toHaveClass(s.open);
});

it("넓은 화면: 상세 패널 칸의 접기 버튼(data-insp-fold)으로 64px 줄로 접고 같은 버튼으로 펴며, 항목을 고르면(상세 열기) 다시 펼친다", async () => {
  screenWidth({ narrow: false, phone: false });
  const c = await mount();
  const view = dashboard(c);
  const fold = within(view).getByRole("button", { name: "오른쪽 패널 접기" });
  expect(fold).toHaveAttribute("data-insp-fold");
  fireEvent.click(fold);
  expect(app(c)).toHaveClass(s["insp-min"]);
  expect(localStorage.getItem("abc2lab-insp-min")).toBe("1");
  fireEvent.click(within(view).getByRole("button", { name: "오른쪽 패널 펼치기" }));
  expect(app(c)).not.toHaveClass(s["insp-min"]);
  expect(localStorage.getItem("abc2lab-insp-min")).toBe("0");
  // 접힌 채 항목을 고르면 시안 openInsp 처럼 펼친다
  fireEvent.click(within(view).getByRole("button", { name: "오른쪽 패널 접기" }));
  fireEvent.click(view.querySelector("[data-insp-open]") as HTMLElement);
  expect(app(c)).not.toHaveClass(s["insp-min"]);
  expect(localStorage.getItem("abc2lab-insp-min")).toBe("0");
  // 넓은 화면은 서랍을 열지 않는다
  expect(view).not.toHaveClass(s.io);
});

it("상세 패널 접기 버튼(PaneToggle): 펼쳐져 있으면 접고, 접혀 있으면 같은 버튼이 펼친다", () => {
  const open = vi.fn();
  const close = vi.fn();
  function Harness() {
    const base = useShell();
    return (
      <ShellContext.Provider value={{ ...base, openInsp: open, closeInsp: close }}>
        <PaneToggle />
      </ShellContext.Provider>
    );
  }
  render(<Harness />);
  const toggle = screen.getByRole("button", { name: "오른쪽 패널 접기" });
  expect(toggle).toHaveAttribute("aria-expanded", "true");
  fireEvent.click(toggle);
  expect(close).toHaveBeenCalledTimes(1);
  act(() => useWb.getState().setPanelMin("insp", true));
  const again = screen.getByRole("button", { name: "오른쪽 패널 펼치기" });
  expect(again).toHaveAttribute("aria-expanded", "false");
  fireEvent.click(again);
  expect(open).toHaveBeenCalledTimes(1);
});

it("접어 둔 패널은 다시 열어도 접힌 채로 시작한다", async () => {
  screenWidth({ narrow: false, phone: false });
  localStorage.setItem("abc2lab-rail-min", "1");
  localStorage.setItem("abc2lab-insp-min", "1");
  const c = await mount();
  await waitFor(() => expect(app(c)).toHaveClass(s["rail-min"]));
  expect(app(c)).toHaveClass(s["insp-min"]);
  expect(screen.getByRole("button", { name: "메뉴 펼치기" })).toHaveAttribute("aria-expanded", "false");
  expect(within(dashboard(c)).getByRole("button", { name: "오른쪽 패널 펼치기" })).toHaveAttribute(
    "aria-expanded",
    "false",
  );
});

it("펼쳐 둔 것('0')은 펼친 채로 시작하고, 옛 키(abc2lab-*-hidden)는 새 키로 한 번 옮겨 온다", async () => {
  screenWidth({ narrow: false, phone: false });
  localStorage.setItem("abc2lab-rail-min", "0");
  localStorage.setItem("abc2lab-rail-hidden", "1");
  localStorage.setItem("abc2lab-insp-hidden", "1");
  const c = await mount();
  await waitFor(() => expect(app(c)).toHaveClass(s["insp-min"]));
  // 새 키가 있으면 그것을 따른다
  expect(app(c)).not.toHaveClass(s["rail-min"]);
  expect(localStorage.getItem("abc2lab-insp-min")).toBe("1");
  expect(localStorage.getItem("abc2lab-insp-hidden")).toBeNull();
});

it("좁은 화면: 상세 버튼 · 닫기는 예전처럼 서랍을 열고 닫으며 접기 기억을 건드리지 않는다", async () => {
  screenWidth({ narrow: true, phone: false });
  const c = await mount();
  const view = dashboard(c);
  act(() => {
    fireEvent.click(view.querySelector("[data-insp-open]") as HTMLElement);
  });
  expect(view).toHaveClass(s.io);
  fireEvent.click(view.querySelector("[data-insp-close]") as HTMLElement);
  expect(view).not.toHaveClass(s.io);
  expect(app(c)).not.toHaveClass(s["insp-min"]);
  expect(localStorage.getItem("abc2lab-insp-min")).toBeNull();
});

it("휴대폰: 메뉴 버튼은 레일 서랍을 열고 접기 상태를 바꾸지 않는다", async () => {
  screenWidth({ narrow: true, phone: true });
  const c = await mount();
  fireEvent.click(dashboard(c).querySelector("[data-rail-open]") as HTMLElement);
  expect(c.querySelector(`.${s.rail}`)).toHaveClass(s.open);
  expect(app(c)).not.toHaveClass(s["rail-min"]);
  expect(localStorage.getItem("abc2lab-rail-min")).toBeNull();
});

it("옛 주소 #records 는 이어 보는 점검 상태에 맞춰 진행 중인 점검 · 끝난 점검으로, 모르면 대시보드로 간다", async () => {
  const c = await mount();
  const go = (h: string) =>
    act(() => {
      window.location.hash = h;
      window.dispatchEvent(new HashChangeEvent("hashchange"));
    });
  const shown = () => c.querySelector("section[data-view]:not([hidden])")?.getAttribute("data-view");
  // 이어 보는 점검이 없으면 짐작하지 않는다
  go("records");
  expect(shown()).toBe("dashboard");
  go("dashboard");
  useUrl.setState({ handle, job: { ...running, state: "completed" }, verified: true });
  go("records");
  expect(window.location.hash).toBe("#finished");
  expect(shown()).toBe("finished");
  go("dashboard");
  useUrl.setState({ handle, job: running, verified: true });
  go("records");
  expect(window.location.hash).toBe("#running");
  expect(shown()).toBe("running");
});
