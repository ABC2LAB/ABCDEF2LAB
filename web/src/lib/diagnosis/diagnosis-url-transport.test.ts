import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { draft, owner, ready, running } from "./diagnosis-url-fixtures";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";

const nativeFetch = globalThis.fetch;
let server: Server | undefined;
const seen: { path: string; method: string; key?: string }[] = [];

beforeEach(() => {
  vi.resetModules();
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
  localStorage.clear();
  localStorage.setItem("abc2lab.devToken", "synthetic-test-session");
  seen.length = 0;
  vi.stubEnv("NEXT_PUBLIC_MOCK", "0");
});
afterEach(async () => {
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
  localStorage.clear();
  if (server) {
    const closing = new Promise<void>((resolve, reject) => {
      server?.close((error) => (error ? reject(error) : resolve()));
    });
    server.closeAllConnections();
    await closing;
    server = undefined;
  }
});

async function listen(status: number) {
  server = createServer((request, response) => {
    seen.push({
      path: request.url ?? "",
      method: request.method ?? "",
      key: request.headers["idempotency-key"] as string | undefined,
    });
    if (request.url === "/replayed") {
      response.writeHead(404, { "Content-Type": "application/json" });
      response.end(JSON.stringify({ error: "not_found" }));
    } else if (status === 401) {
      response.writeHead(401, { "Content-Type": "application/json" });
      response.end(JSON.stringify({ error: "unauthorized" }));
    } else {
      response.writeHead(status, { Location: "/replayed" });
      response.end();
    }
  });
  await new Promise<void>((resolve) => {
    server?.listen(0, "127.0.0.1", resolve);
  });
  const origin = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  // 실제 apiFetch와 native Fetch를 통과한다. 상대 API 경로만 소유한 일회용 loopback 서버로 해석한다.
  const transport = vi.fn((path: string, init?: RequestInit) => nativeFetch(new URL(path, origin).href, init));
  vi.stubGlobal("fetch", transport);
  return transport;
}

describe("v2 실제 전송의 자동 재전송 차단", () => {
  it.each([307, 308])("HTTP %i가 POST/receipt/catalog를 다른 URL로 재전송하지 않는다", async (status) => {
    const transport = await listen(status);
    const api = await import("./diagnosis-url");
    const actions = [
      () => api.readURLCatalog(),
      () => api.createURLDraft(draft.input, "create-1", owner),
      () => api.readURLReceipt("create-1", owner),
      () => api.readURLStatus("job-1", owner),
      () => api.commandURLDiagnosis(ready, "validate", owner),
      () => api.commandURLDiagnosis(ready, "start", owner, "start-1"),
      () => api.commandURLDiagnosis(running, "stop", owner),
    ];
    for (const action of actions) await expect(action()).rejects.toBeInstanceOf(TypeError);
    expect(seen).toHaveLength(actions.length);
    expect(seen.some((request) => request.path === "/replayed")).toBe(false);
    expect(seen.filter((request) => request.method === "POST")).toHaveLength(4);
    expect(seen.find((request) => request.path.endsWith("/start"))?.key).toBe("start-1");
    for (const [, init] of transport.mock.calls) expect(init?.redirect).toBe("error");
    // redirect된 404/501을 catalog 미지원으로 잘못 해석할 응답 자체가 없다.
  });
  it("실제 401 시작 응답에도 재로그인·재전송하지 않는다", async () => {
    await listen(401);
    const api = await import("./diagnosis-url");
    await expect(api.commandURLDiagnosis(ready, "start", owner, "start-1")).rejects.toMatchObject({ status: 401 });
    expect(seen).toEqual([{ path: "/api/diagnoses/v2/drafts/job-1/start", method: "POST", key: "start-1" }]);
  });
});
