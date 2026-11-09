// live/source: 기본 demo · 저장 · 마운트 뒤 복원 · 배너와 레일 스위치로 전환 (일반 빌드).

import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, renderHook, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/abc2lab-ui/lib/api", async (importOriginal) => {
  const real = await importOriginal<typeof import("@/abc2lab-ui/lib/api")>();
  return {
    ...real,
    apiFetch: vi.fn(async () => ({ tasks: [], active: "" })),
    readAuthStatus: vi.fn(async () => ({ initialized: true })),
    restoreSession: vi.fn(async () => false),
    subscribeAuthRequired: vi.fn(() => () => undefined),
  };
});

import { setLiveAuthState } from "../live/auth";
import { resetFeeds } from "../live/feeds";
import { SOURCE_KEY, useSource, useSourceBoot } from "../live/source";
import { createInitialState, useWb } from "../store";
import { Workbench } from "../Workbench";

const DEMO_TEXT =
  "작동하지 않는 UI 시안입니다 — 모든 숫자·기록은 예시이며, 실제 점검·소유 확인·세션 저장을 수행하지 않습니다.";

beforeEach(() => {
  localStorage.clear();
  useSource.setState({ source: "demo", taskId: null });
  setLiveAuthState("checking");
  resetFeeds();
  useWb.setState(createInitialState());
  window.location.hash = "";
  vi.spyOn(window, "scrollTo").mockImplementation(() => undefined);
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("source 스토어", () => {
  it("기본은 demo, setSource 는 저장한다", () => {
    expect(useSource.getState().source).toBe("demo");
    act(() => useSource.getState().setSource("live"));
    expect(useSource.getState().source).toBe("live");
    expect(localStorage.getItem(SOURCE_KEY)).toBe("live");
    act(() => useSource.getState().setSource("demo"));
    expect(localStorage.getItem(SOURCE_KEY)).toBe("demo");
  });

  it("selectTask 는 id 만 바꾼다", () => {
    act(() => useSource.getState().selectTask("12"));
    expect(useSource.getState().taskId).toBe("12");
    act(() => useSource.getState().selectTask(null));
    expect(useSource.getState().taskId).toBeNull();
  });

  it("useSourceBoot 는 마운트 뒤 저장값을 읽는다 (이상한 값은 무시)", () => {
    localStorage.setItem(SOURCE_KEY, "live");
    renderHook(() => useSourceBoot());
    expect(useSource.getState().source).toBe("live");

    useSource.setState({ source: "demo" });
    localStorage.setItem(SOURCE_KEY, "bogus");
    renderHook(() => useSourceBoot());
    expect(useSource.getState().source).toBe("demo");
  });

  it("저장소가 막혀도 동작한다", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    act(() => useSource.getState().setSource("live"));
    expect(useSource.getState().source).toBe("live");
    renderHook(() => useSourceBoot());
    expect(useSource.getState().source).toBe("live");
  });
});

describe("배너 · 레일 스위치", () => {
  it("demo 배너 문구는 그대로이고 '실제 엔진으로 보기' 로 live 가 된다", () => {
    const { container } = render(<Workbench />);
    const bar = container.querySelector<HTMLElement>('[role="note"]');
    expect(bar).not.toBeNull();
    expect(within(bar as HTMLElement).getByText(DEMO_TEXT)).toBeInTheDocument();
    fireEvent.click(within(bar as HTMLElement).getByRole("button", { name: "실제 엔진으로 보기" }));
    expect(useSource.getState().source).toBe("live");
    expect(
      within(bar as HTMLElement).getByText(
        "실제 엔진 데이터입니다. '엔진 미지원'으로 표시된 칸은 백엔드에 아직 없는 기능입니다.",
      ),
    ).toBeInTheDocument();
    fireEvent.click(within(bar as HTMLElement).getByRole("button", { name: "예시 재생으로 보기" }));
    expect(useSource.getState().source).toBe("demo");
    expect(within(bar as HTMLElement).getByText(DEMO_TEXT)).toBeInTheDocument();
  });

  it("레일 스위치는 aria-pressed 로 지금 원천을 보인다", () => {
    render(<Workbench />);
    const group = screen.getByRole("group", { name: "데이터 원천" });
    const demo = within(group).getByRole("button", { name: "예시 재생" });
    const live = within(group).getByRole("button", { name: "실제 엔진" });
    expect(demo).toHaveAttribute("aria-pressed", "true");
    expect(live).toHaveAttribute("aria-pressed", "false");
    fireEvent.click(live);
    expect(useSource.getState().source).toBe("live");
    expect(
      within(screen.getByRole("group", { name: "데이터 원천" })).getByRole("button", { name: "실제 엔진" }),
    ).toHaveAttribute("aria-pressed", "true");
  });

  it("저장해 둔 live 로 열면 시안 재생을 시작하지 않는다", () => {
    localStorage.setItem(SOURCE_KEY, "live");
    window.location.hash = "#live";
    render(<Workbench />);
    expect(useSource.getState().source).toBe("live");
    expect(useWb.getState().live.playing).toBe(false);
    // 예시 재생으로 돌아오면 그 화면의 재생 진입을 한다
    act(() => useSource.getState().setSource("demo"));
    expect(useWb.getState().live.playing).toBe(true);
  });
});
