import { beforeEach, describe, expect, it, vi } from "vitest";

const TOKEN_KEY = "abc2lab.devToken";

function json(status: number, body: unknown) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

type Seen = { path: string; method: string; auth: string | null };

/** 네트워크 경계만 대체한다. "issued" 토큰만 유효한 엔진처럼 응답한다. */
function fakeEngine(responses: { other?: () => Response } = {}) {
  const seen: Seen[] = [];
  vi.stubGlobal("fetch", async (path: string, init: RequestInit = {}) => {
    const auth = new Headers(init.headers).get("Authorization");
    seen.push({ path, method: init.method ?? "GET", auth });
    if (path === "/api/auth/login") {
      const body = JSON.parse(String(init.body)) as { password: string };
      if (body.password === "no-token") return json(200, {});
      return body.password === "right" ? json(200, { token: "issued" }) : json(401, { error: "bad credential" });
    }
    if (responses.other) return responses.other();
    return auth === "Bearer issued" ? json(200, { ok: true }) : json(401, { error: "unauthorized" });
  });
  return seen;
}

async function freshApi() {
  // 토큰을 모듈 변수에 들고 있으므로 시험마다 새로 읽는다.
  vi.resetModules();
  return import("./api");
}

beforeEach(() => {
  localStorage.clear();
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
});

const AUTO_USER = "dev-user";

type Held = { path: string; method: string; auth: string | null; respond: (response: Response) => void };

/** 응답 시점을 시험이 정하는 엔진. 사람이 하는 로그인만 바로 답하고 매번 다른 토큰을 준다. */
function slowEngine() {
  const held: Held[] = [];
  let issued = 0;
  vi.stubGlobal("fetch", (path: string, init: RequestInit = {}) => {
    if (path === "/api/auth/login" && (JSON.parse(String(init.body)) as { username: string }).username !== AUTO_USER) {
      issued += 1;
      return Promise.resolve(json(200, { token: `session-${issued}` }));
    }
    return new Promise<Response>((respond) => {
      held.push({ path, method: init.method ?? "GET", auth: new Headers(init.headers).get("Authorization"), respond });
    });
  });
  return held;
}

async function heldCount(held: Held[], count: number) {
  await vi.waitFor(() => expect(held).toHaveLength(count));
}

describe("지나간 세션의 늦은 401", () => {
  it("읽기 요청의 늦은 401 은 새 세션을 지우지 않고 다시 로그인하라고 알리지 않는다", async () => {
    const held = slowEngine();
    const api = await freshApi();
    const required = vi.fn();
    api.subscribeAuthRequired(required);
    await api.signIn("ARTEX", "pw");
    const old = api.apiFetch("/api/tasks");
    await heldCount(held, 1);
    expect(held[0].auth).toBe("Bearer session-1");

    api.signOut();
    await api.signIn("ARTEX", "pw");
    held[0].respond(json(401, { error: "expired" }));

    await expect(old).rejects.toMatchObject({ status: 401 });
    expect(localStorage.getItem(TOKEN_KEY)).toBe("session-2");
    expect(required).toHaveBeenCalledTimes(1);

    const next = api.apiFetch("/api/next");
    await heldCount(held, 2);
    expect(held[1]).toMatchObject({ path: "/api/next", auth: "Bearer session-2" });
    held[1].respond(json(200, { ok: true }));
    await expect(next).resolves.toEqual({ ok: true });
  });

  it("재전송 금지 요청의 늦은 401 도 새 세션을 지우지 않고 다시 보내지 않는다", async () => {
    const held = slowEngine();
    const api = await freshApi();
    const required = vi.fn();
    api.subscribeAuthRequired(required);
    await api.signIn("ARTEX", "pw");
    const old = api.apiFetch("/api/decide", { method: "POST", body: "{}" }, false);
    await heldCount(held, 1);

    api.signOut();
    await api.signIn("ARTEX", "pw");
    held[0].respond(json(401, { error: "expired" }));

    await expect(old).rejects.toMatchObject({ status: 401, message: "expired" });
    expect(localStorage.getItem(TOKEN_KEY)).toBe("session-2");
    expect(required).toHaveBeenCalledTimes(1);
    expect(held.filter((item) => item.method === "POST")).toHaveLength(1);
  });

  it("401 본문을 읽는 사이 다시 로그인해도 새 세션을 지우지 않는다", async () => {
    const held = slowEngine();
    const api = await freshApi();
    const required = vi.fn();
    api.subscribeAuthRequired(required);
    await api.signIn("ARTEX", "pw");
    const old = api.apiFetch("/api/decide", { method: "POST", body: "{}" }, false);
    await heldCount(held, 1);

    let finishBody: (body: unknown) => void = () => undefined;
    const body = new Promise<unknown>((resolve) => {
      finishBody = resolve;
    });
    const bodyRead = vi.fn(() => body);
    held[0].respond({ status: 401, ok: false, json: bodyRead } as unknown as Response);
    await vi.waitFor(() => expect(bodyRead).toHaveBeenCalledTimes(1));

    api.signOut();
    await api.signIn("ARTEX", "pw");
    finishBody({ error: "expired" });

    await expect(old).rejects.toMatchObject({ status: 401 });
    expect(localStorage.getItem(TOKEN_KEY)).toBe("session-2");
    expect(required).toHaveBeenCalledTimes(1);
  });

  it("같은 세션의 요청 둘이 함께 만료되면 로그인 요구는 한 번만 한다", async () => {
    const held = slowEngine();
    const api = await freshApi();
    const required = vi.fn();
    api.subscribeAuthRequired(required);
    await api.signIn("ARTEX", "pw");
    const first = api.apiFetch("/api/first");
    const second = api.apiFetch("/api/second");
    await heldCount(held, 2);

    held[0].respond(json(401, { error: "expired" }));
    await heldCount(held, 3);
    expect(held[2]).toMatchObject({ path: "/api/first", auth: null });
    held[1].respond(json(401, { error: "expired" }));
    await expect(second).rejects.toMatchObject({ status: 401 });
    held[2].respond(json(401, { error: "expired" }));
    await expect(first).rejects.toMatchObject({ status: 401 });

    expect(required).toHaveBeenCalledTimes(1);
    expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
  });
});

describe("명시적 로그인", () => {
  it("개발 환경의 공개 자격증명이 있어도 자동 로그인 요청을 보내지 않는다", async () => {
    const seen = fakeEngine();
    vi.stubEnv("NEXT_PUBLIC_ARTEX_DEV_USER", "dev-user");
    vi.stubEnv("NEXT_PUBLIC_ARTEX_DEV_PASS", "right");
    const api = await freshApi();
    expect(await api.restoreSession()).toBe(false);
    expect(seen).toEqual([]);
  });
});

describe("로그인 세션", () => {
  it("로그인하면 받은 토큰으로 이후 요청을 보낸다", async () => {
    const seen = fakeEngine();
    const api = await freshApi();
    await api.signIn("ARTEX", "right");
    await expect(api.apiFetch("/api/tasks")).resolves.toEqual({ ok: true });
    expect(seen.at(-1)).toEqual({ path: "/api/tasks", method: "GET", auth: "Bearer issued" });
    await expect(api.restoreSession()).resolves.toBe(true);
  });

  it("로그인이 거절되면 세션을 남기지 않고 서버 오류를 알린다", async () => {
    fakeEngine();
    const api = await freshApi();
    await expect(api.signIn("ARTEX", "wrong")).rejects.toMatchObject({ status: 401, message: "bad credential" });
    expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
    await expect(api.restoreSession()).resolves.toBe(false);
  });

  it("토큰 없는 성공 응답은 로그인 성공으로 보지 않는다", async () => {
    fakeEngine();
    const api = await freshApi();
    await expect(api.signIn("ARTEX", "no-token")).rejects.toMatchObject({ status: 502 });
    await expect(api.restoreSession()).resolves.toBe(false);
  });

  it("서버에 닿지 못하면 status 0 으로 알린다", async () => {
    vi.stubGlobal("fetch", async () => {
      throw new TypeError("network down");
    });
    const api = await freshApi();
    await expect(api.signIn("ARTEX", "right")).rejects.toMatchObject({ status: 0 });
  });

  it("만료된 토큰은 세션을 지우고 로그인이 필요하다고 알린다", async () => {
    fakeEngine();
    localStorage.setItem(TOKEN_KEY, "expired");
    const api = await freshApi();
    const required = vi.fn();
    api.subscribeAuthRequired(required);
    await expect(api.apiFetch("/api/tasks")).rejects.toMatchObject({ status: 401 });
    expect(required).toHaveBeenCalledTimes(1);
    expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
  });

  it("재시도 금지 요청은 401 이어도 다시 보내지 않고 로그인을 요구한다", async () => {
    const seen = fakeEngine();
    localStorage.setItem(TOKEN_KEY, "expired");
    const api = await freshApi();
    const required = vi.fn();
    api.subscribeAuthRequired(required);
    await expect(api.apiFetch("/api/decide", { method: "POST", body: "{}" }, false)).rejects.toMatchObject({
      status: 401,
    });
    expect(seen.filter((item) => item.method === "POST")).toHaveLength(1);
    expect(required).toHaveBeenCalledTimes(1);
  });

  it("권한 오류(403)는 로그인 만료가 아니므로 세션을 유지한다", async () => {
    fakeEngine({ other: () => json(403, { error: "forbidden" }) });
    localStorage.setItem(TOKEN_KEY, "issued");
    const api = await freshApi();
    const required = vi.fn();
    api.subscribeAuthRequired(required);
    await expect(api.apiFetch("/api/tasks")).rejects.toMatchObject({ status: 403 });
    expect(required).not.toHaveBeenCalled();
    expect(localStorage.getItem(TOKEN_KEY)).toBe("issued");
  });

  it("로그아웃은 세션을 지우고 구독을 해제한 쪽에는 알리지 않는다", async () => {
    fakeEngine();
    const api = await freshApi();
    await api.signIn("ARTEX", "right");
    const kept = vi.fn();
    const removed = vi.fn();
    api.subscribeAuthRequired(kept);
    api.subscribeAuthRequired(removed)();
    api.signOut();
    expect(kept).toHaveBeenCalledTimes(1);
    expect(removed).not.toHaveBeenCalled();
    expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
    await expect(api.restoreSession()).resolves.toBe(false);
  });
});
