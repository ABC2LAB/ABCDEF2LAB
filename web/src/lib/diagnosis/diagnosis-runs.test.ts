import { beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError, apiFetchLimitedJSON, signIn, signOut } from "./api";
import { appendURLRunPage, matchURLRun, parseURLRunPage, readURLRunPage, type URLRunSummary } from "./diagnosis-runs";
import {
  matchURLHandle,
  readURLHandle,
  readURLSelection,
  storeURLHandle,
  type URLRecoveredHandle,
} from "./diagnosis-url";
import { handle, owner, running } from "./diagnosis-url-fixtures";

const run: URLRunSummary = {
  id: running.id,
  revision: running.revision,
  state: running.state,
  phase: running.phase,
  task_id: running.task_id,
  started: true,
  drain_complete: false,
  result_ready: false,
  report_available: false,
  approval_status: "not_revalidated",
  action_required: true,
};
const page = (rows: unknown[] = [run], over: Record<string, unknown> = {}) => ({
  version: "diagnosis-api.v2",
  owner_context: owner,
  page: { runs: rows, snapshot: "d".repeat(64), limit: 25, more: false, ...over },
});
const encode = (s: string) => btoa(s).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");

beforeEach(() => {
  signOut();
  sessionStorage.clear();
  vi.unstubAllGlobals();
});

describe("보호된 제품 진단 목록", () => {
  it("실제 projection만 받고 시각·URL·판정을 보충하지 않는다", () => {
    expect(parseURLRunPage(page(), owner).runs).toEqual([run]);
    expect(Object.keys(parseURLRunPage(page(), owner).runs[0])).not.toContain("target_url");
    expect(() => parseURLRunPage({ ...page(), owner_context: "b".repeat(64) }, owner)).toThrow();
    expect(() => parseURLRunPage({ ...page(), version: "other" }, owner)).toThrow();
  });
  it.each([
    { id: "../job" },
    { revision: 0 },
    { revision: Number.MAX_SAFE_INTEGER + 1 },
    { task_id: "042" },
    { task_id: "9223372036854775808" },
    { state: "future" },
    { state: "toString" },
    { phase: "future" },
    { approval_status: "approved" },
    { approval_status: "toString" },
    { action_required: "true" },
    { action_required: false },
    { report_available: true },
    { started: false },
    { drain_complete: true },
    { result_ready: true },
    { display_name: "x\nsecret" },
    { display_name: "가".repeat(54) },
    { reason_code: "private reason" },
    { state: "ready" },
    { state: "completed" },
    { state: "failed" },
    { state: "cancelled" },
    { target_url: "https://private.example/" },
    { creationKey: "invented" },
    { startKey: "invented" },
    { created_at: "2026-11-17" },
  ])("잘못된 행은 계약 오류다: %j", (change) => {
    expect(() => parseURLRunPage(page([{ ...run, ...change }]), owner)).toThrow(ApiError);
  });
  it.each([
    { more: true },
    { more: false, next_cursor: "YQ" },
    { more: true, next_cursor: "not=a_cursor" },
    { more: true, next_cursor: "a" },
    { more: true, next_cursor: "a".repeat(513) },
    { limit: 51 },
    { limit: 0 },
    { limit: 1.5 },
    { snapshot: "bad" },
  ])("잘못된 페이지를 빈 목록으로 바꾸지 않는다: %j", (change) => {
    expect(() => parseURLRunPage(page([run], change), owner)).toThrow();
  });
  it("개수·바이트·정렬·중복·커서 진행을 검사한다", () => {
    expect(() => parseURLRunPage(page([run, run]), owner)).toThrow();
    expect(() => parseURLRunPage(page([run, { ...run, id: "zz" }]), owner)).toThrow();
    expect(() =>
      parseURLRunPage(
        page(
          Array.from({ length: 51 }, (_, i) => ({ ...run, id: `j${i}` })),
          { limit: 50 },
        ),
        owner,
      ),
    ).toThrow();
    expect(() => parseURLRunPage(page([run], { extra: "x".repeat(25000) }), owner)).toThrow();
    const cursor = encode("cursor-page-2");
    expect(() => parseURLRunPage(page([run], { more: true, next_cursor: cursor }), owner, { cursor })).toThrow();
    expect(() => parseURLRunPage(page(), owner, { limit: 2 })).toThrow();
  });
  it("승인 철회·만료·변경·불가와 unknown을 보존한다", () => {
    for (const status of ["not_revalidated", "revoked", "expired", "changed", "unavailable"]) {
      const unknown = { ...run, state: "unknown", approval_status: status };
      expect(parseURLRunPage(page([unknown]), owner).runs[0]).toMatchObject(unknown);
    }
    for (const state of ["completed", "failed", "cancelled"])
      expect(
        parseURLRunPage(
          page([{ ...run, state, phase: "report", drain_complete: true, result_ready: true, report_available: true }]),
          owner,
        ).runs[0].state,
      ).toBe(state);
  });
  it("snapshot이 같은 다음 페이지도 중복·역순이면 거부한다", () => {
    const first = parseURLRunPage(page([{ ...run, id: "z" }], { more: true, next_cursor: encode("page2") }), owner);
    const next = parseURLRunPage(page([{ ...run, id: "a" }]), owner);
    expect(appendURLRunPage(first, next).runs.map((r) => r.id)).toEqual(["z", "a"]);
    expect(() => appendURLRunPage(first, { ...next, snapshot: "e".repeat(64) })).toThrow();
    expect(() => appendURLRunPage(first, { ...next, runs: first.runs })).toThrow();
  });
  it("오래된 revision은 허용해도 Job·task·known fact 모순은 거부한다", () => {
    expect(matchURLRun({ ...running, revision: 7 }, run)).toBeTruthy();
    for (const change of [{ id: "other" }, { revision: 4 }, { task_id: "43" }, { started: false }, { phase: "draft" }])
      expect(() => matchURLRun({ ...running, ...change } as typeof running, run)).toThrow();
    expect(() => matchURLRun({ ...running, source_digest: "b".repeat(64) }, { ...run, revision: 6 })).toThrow();
  });
  it("복구 핸들은 원래 생성 핸들과 구별하고 최소 선택만 저장한다", () => {
    const recovered: URLRecoveredHandle = {
      kind: "recovered",
      ownerContext: owner,
      id: running.id,
      revision: running.revision,
      sourceDigest: running.source_digest,
      input: { ...running.input, external_test_accounts: true },
      bindingLocked: true,
    };
    storeURLHandle(handle);
    expect(readURLHandle(owner)?.creationKey).toBe(handle.creationKey);
    storeURLHandle(recovered);
    expect(readURLHandle(owner)).toBeNull();
    expect(readURLSelection(owner)).toEqual({ ownerContext: owner, id: running.id });
    const saved = Array.from({ length: sessionStorage.length }, (_, i) =>
      sessionStorage.getItem(sessionStorage.key(i) as string),
    ).join();
    expect(saved).not.toMatch(/creationKey|startKey|target_url|sourceDigest|external_test_accounts|bindingLocked/);
    expect(() => matchURLHandle({ ...running, revision: 6, source_digest: "b".repeat(64) }, recovered)).toThrow();
    expect(readURLSelection("b".repeat(64))).toBeNull();
  });
});

describe("읽기 전용 HTTP 경계와 세션 경쟁", () => {
  it("GET cursor를 그대로 전달하고 멱등 키·POST를 만들지 않는다", async () => {
    const fetch = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) =>
        new Response(JSON.stringify(page()), { headers: { "Content-Type": "application/json" } }),
    );
    vi.stubGlobal("fetch", fetch);
    await readURLRunPage(owner);
    expect(fetch.mock.calls[0][0]).toBe("/api/diagnoses/v2/runs");
    const init = fetch.mock.calls[0][1] as RequestInit;
    expect(init.method).toBeUndefined();
    expect(new Headers(init.headers).has("Idempotency-Key")).toBe(false);
    const c = encode("cursor-page-2");
    await readURLRunPage(owner, { cursor: c });
    expect(fetch.mock.calls[1][0]).toBe(`/api/diagnoses/v2/runs?cursor=${c}`);
  });
  it("JSON 파싱 전 수신 바이트 상한을 집행한다", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(" ".repeat(26000) + JSON.stringify(page()))),
    );
    await expect(readURLRunPage(owner)).rejects.toMatchObject({ status: 422 });
  });
  it.each([401, 403, 404, 409, 503])("HTTP %s는 성공·빈 목록이 아니다", async (status) => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response(JSON.stringify({ error: status === 409 ? "snapshot_changed" : "unavailable" }), { status }),
      ),
    );
    await expect(readURLRunPage(owner)).rejects.toMatchObject({ status });
  });
  it("로그아웃 이후 늦은 로그인 응답으로 세션을 다시 만들지 않는다", async () => {
    let finish: (value: Response) => void = () => undefined;
    vi.stubGlobal(
      "fetch",
      vi.fn(
        () =>
          new Promise<Response>((resolve) => {
            finish = resolve;
          }),
      ),
    );
    const signing = signIn("operator", "synthetic-password");
    signOut();
    finish(new Response(JSON.stringify({ token: "synthetic-old-token" })));
    await expect(signing).rejects.toMatchObject({ status: 409 });
    expect(localStorage.getItem("abc2lab.devToken")).toBeNull();
  });
  it("주체 교체 중 받은 늦은 JSON 본문을 전달하지 않는다", async () => {
    let enqueue: ReadableStreamDefaultController<Uint8Array> | undefined;
    const stream = new ReadableStream<Uint8Array>({
      start(c) {
        enqueue = c;
      },
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(async (path) =>
        String(path).includes("login")
          ? new Response(JSON.stringify({ token: "synthetic-new-token" }))
          : new Response(stream),
      ),
    );
    const pending = apiFetchLimitedJSON("/api/owned", 1024);
    await Promise.resolve();
    await Promise.resolve();
    await signIn("other", "synthetic-password");
    enqueue?.enqueue(new TextEncoder().encode("{}"));
    enqueue?.close();
    await expect(pending).rejects.toMatchObject({ status: 401 });
  });
});
