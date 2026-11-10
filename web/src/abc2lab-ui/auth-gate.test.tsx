import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const TOKEN_KEY = "abc2lab.devToken";

function json(status: number, body: unknown) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

/** "issued" 토큰만 받는 엔진. 화면 전체를 실제로 마운트해 게이트를 확인한다. */
function fakeEngine() {
  const seen: string[] = [];
  vi.stubGlobal("fetch", async (path: string, init: RequestInit = {}) => {
    seen.push(path);
    if (path === "/api/auth/status") return json(200, { initialized: true });
    if (path === "/api/auth/login") return json(200, { token: "issued" });
    if (new Headers(init.headers).get("Authorization") !== "Bearer issued") return json(401, { error: "expired" });
    if (path === "/api/tasks") return json(200, { tasks: [], active: "" });
    return json(404, { error: "none" });
  });
  return seen;
}

async function mountApp(initialScreen: "dashboard" | "live" = "dashboard") {
  // 토큰을 모듈 변수에 들고 있으므로 시험마다 새로 읽는다.
  vi.resetModules();
  const { default: ABC2LabUI } = await import("./ABC2LabUI");
  return render(<ABC2LabUI syncHash={false} initialScreen={initialScreen} />);
}
const workspace = (view: ReturnType<typeof render>) => view.container.querySelector('[data-screen="dashboard"]');

beforeEach(() => localStorage.clear());
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
});

describe("목업 빌드", () => {
  it("로그인 없이 화면을 열고 로그아웃과 실행 상태를 띄우지 않는다", async () => {
    vi.stubEnv("NEXT_PUBLIC_MOCK", "1");
    const seen = fakeEngine();
    const view = await mountApp("live");
    await waitFor(() => expect(workspace(view)).not.toBeNull());
    expect(screen.queryByLabelText("비밀번호")).toBeNull();
    expect(screen.queryByText("로그아웃")).toBeNull();
    expect(view.container.querySelector('section[aria-label="실행 상태"]')).toBeNull();
    expect(seen).toEqual([]);
  });
});

describe("로그인 게이트", () => {
  it("세션이 없으면 로그인만 보여 주고 화면 API를 부르지 않는다", async () => {
    const seen = fakeEngine();
    const view = await mountApp();
    expect(await screen.findByLabelText("비밀번호")).toBeVisible();
    expect(workspace(view)).toBeNull();
    expect(seen.filter((path) => !path.startsWith("/api/auth/"))).toEqual([]);
  });

  it("유효한 세션이 있으면 바로 화면을 연다", async () => {
    fakeEngine();
    localStorage.setItem(TOKEN_KEY, "issued");
    const view = await mountApp();
    await waitFor(() => expect(workspace(view)).not.toBeNull());
    expect(screen.queryByLabelText("비밀번호")).toBeNull();
  });

  it("로그인에 성공하면 화면을 연다", async () => {
    fakeEngine();
    const view = await mountApp();
    fireEvent.change(await screen.findByLabelText("비밀번호"), { target: { value: "secret" } });
    fireEvent.click(screen.getByRole("button", { name: "로그인" }));
    await waitFor(() => expect(workspace(view)).not.toBeNull());
  });

  it("사용 중 토큰이 만료되면 로그인으로 돌아간다", async () => {
    fakeEngine();
    localStorage.setItem(TOKEN_KEY, "expired");
    const view = await mountApp();
    expect(await screen.findByLabelText("비밀번호")).toBeVisible();
    expect(workspace(view)).toBeNull();
    expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
  });

  it("로그아웃하면 세션을 지우고 로그인으로 돌아간다", async () => {
    fakeEngine();
    localStorage.setItem(TOKEN_KEY, "issued");
    const view = await mountApp();
    await waitFor(() => expect(workspace(view)).not.toBeNull());
    fireEvent.click(screen.getAllByRole("button", { name: "로그아웃" })[0]);
    expect(await screen.findByLabelText("비밀번호")).toBeVisible();
    expect(workspace(view)).toBeNull();
    expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
  });
});
