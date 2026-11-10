// MOCK 빌드(NEXT_PUBLIC_MOCK=1): live 를 고를 수 없고 전환 버튼도 없다.

import "@testing-library/jest-dom/vitest";
import { act, cleanup, render, renderHook, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/abc2lab-ui/lib/mock", () => ({ MOCK: true, MOCK_DEFAULT_TASK: "1005" }));

import { canGoLive, SOURCE_KEY, useSource, useSourceBoot } from "../live/source";
import { createInitialState, useWb } from "../store";
import { Workbench } from "../Workbench";

beforeEach(() => {
  localStorage.clear();
  useSource.setState({ source: "demo", taskId: null });
  useWb.setState(createInitialState());
  window.location.hash = "";
  vi.spyOn(window, "scrollTo").mockImplementation(() => undefined);
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("MOCK 빌드", () => {
  it("저장값이 live 여도 demo 로 둔다", () => {
    expect(canGoLive()).toBe(false);
    localStorage.setItem(SOURCE_KEY, "live");
    useSource.setState({ source: "live" });
    renderHook(() => useSourceBoot());
    expect(useSource.getState().source).toBe("demo");
  });

  it("setSource('live') 는 demo 로 남는다", () => {
    act(() => useSource.getState().setSource("live"));
    expect(useSource.getState().source).toBe("demo");
  });

  it("배너 전환 버튼과 레일 스위치가 없다", () => {
    localStorage.setItem(SOURCE_KEY, "live");
    render(<Workbench />);
    expect(useSource.getState().source).toBe("demo");
    expect(screen.queryByRole("button", { name: "실제 엔진으로 보기" })).toBeNull();
    expect(screen.queryByRole("group", { name: "데이터 원천" })).toBeNull();
    expect(screen.getByRole("heading", { level: 1, name: "대시보드" })).toBeVisible();
  });
});
