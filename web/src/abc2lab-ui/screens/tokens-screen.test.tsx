import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import * as engine from "../lib/engine";
import { NavContext } from "../nav";
import TokensScreen from "./TokensScreen";

vi.mock("../lib/engine", () => ({ getTokenUsage: vi.fn(), TOKEN_DAILY_DAYS: 365 }));
beforeEach(() => vi.resetAllMocks());
afterEach(cleanup);
function show() {
  return render(
    <NavContext.Provider value={{ screen: "tokens", go: vi.fn() }}>
      <TokensScreen />
    </NavContext.Provider>,
  );
}
describe("operational token dashboard", () => {
  it("distinguishes loading, failure, and a successful empty refresh", async () => {
    let fail!: (reason: Error) => void;
    vi.mocked(engine.getTokenUsage).mockReturnValueOnce(
      new Promise((_, reject) => {
        fail = reject;
      }),
    );
    vi.mocked(engine.getTokenUsage).mockResolvedValue({ byProfile: [], daily: [] });
    show();
    expect(screen.getByText("불러오는 중…")).toBeVisible();
    expect(screen.queryByText("아직 집계된 토큰 사용이 없어요.")).toBeNull();
    await act(async () => fail(new Error("synthetic usage failure")));
    expect(await screen.findByRole("alert")).toHaveTextContent("불러오지 못했어요");
    fireEvent.click(screen.getByRole("button", { name: "새로고침" }));
    expect(await screen.findByText("아직 집계된 토큰 사용이 없어요.")).toBeVisible();
  });
  it("shows recent UTC daily data even when no cumulative profiles exist", async () => {
    vi.mocked(engine.getTokenUsage).mockResolvedValue({
      byProfile: [],
      daily: [{ day: "2026-10-30", input: 120, output: 30, cacheRead: 50 }],
    });
    show();
    expect(await screen.findByText("10/30")).toBeVisible();
    expect(screen.getByText("150")).toBeVisible();
    expect(screen.queryByText("아직 집계된 토큰 사용이 없어요.")).toBeNull();
    expect(screen.getByText(/최근 365일\(UTC\)/)).toBeVisible();
  });
});
