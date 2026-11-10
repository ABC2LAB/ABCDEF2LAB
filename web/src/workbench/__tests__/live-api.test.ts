// live/api: 경로 · 요청 본문 · 응답 정리(Go 핸들러 모양) · 가림 · 다운로드 방식. 팀 api 의 apiFetch 를 모의한다.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/abc2lab-ui/lib/api", async (importOriginal) => {
  const real = await importOriginal<typeof import("@/abc2lab-ui/lib/api")>();
  return { ...real, apiFetch: vi.fn(), apiFetchBlob: vi.fn() };
});

import * as teamApi from "@/abc2lab-ui/lib/api";
import { ApiError } from "@/abc2lab-ui/lib/api";

import {
  controlTask,
  createTask,
  decidePendingApproval,
  downloadDiagnostic,
  excludePathRules,
  fetchActivity,
  fetchCoverage,
  fetchEndpoints,
  fetchFinding,
  fetchLlm,
  fetchLlmProfiles,
  fetchPendingApproval,
  fetchPendingApprovals,
  fetchRemediation,
  fetchRequestApprovals,
  fetchTask,
  fetchTaskFindings,
  fetchTasks,
  fetchTokenUsage,
  fetchTraffic,
  fetchTrafficExchange,
} from "../live/api";

const apiFetch = vi.mocked(teamApi.apiFetch);
const apiFetchBlob = vi.mocked(teamApi.apiFetchBlob);

/** 경로별 응답 표. 함수면 (path, init) 으로 부른다. */
function route(table: Record<string, unknown>) {
  apiFetch.mockImplementation(async (path: string, init?: RequestInit) => {
    const key = Object.keys(table).find((k) => path === k || path.startsWith(`${k}?`) || path.startsWith(k));
    if (!key) throw new ApiError(404, `no route ${path}`);
    const v = table[key];
    return (typeof v === "function" ? v(path, init) : v) as never;
  });
}

beforeEach(() => {
  apiFetch.mockReset();
  apiFetchBlob.mockReset();
});
afterEach(() => {
  vi.restoreAllMocks();
});

describe("점검", () => {
  it("fetchTasks: null 칸을 정리하고 숫자 칸을 채운다", async () => {
    route({
      "/api/tasks": {
        active: "2",
        tasks: [
          {
            id: "2",
            name: "",
            description: "https://shop.example.com 점검",
            goal: "IDOR",
            status: "running",
            created_unix: 100,
            tokens: { input_tokens: 10, output_tokens: 5, cache_read_tokens: 0, cache_write_tokens: 0 },
            goals_total: 5,
            goals_met: 2,
            findings: { critical: 1, high: 0, medium: 2, low: 0 },
            llm_profile_ids: null,
            source_task_ids: null,
            active_llm_profile_id: 3,
          },
        ],
      },
    });
    const r = await fetchTasks();
    expect(apiFetch).toHaveBeenCalledWith("/api/tasks");
    expect(r.active).toBe("2");
    expect(r.tasks[0]).toMatchObject({
      id: "2",
      status: "running",
      goals_total: 5,
      findings: { critical: 1, high: 0, medium: 2, low: 0 },
      llm_profile_ids: [],
      source_task_ids: [],
      active_llm_profile_id: 3,
      completed_unix: 0,
      paused: false,
    });
  });

  it("fetchTasks: 빈 응답도 견딘다, fetchTask 는 id 를 인코딩", async () => {
    route({ "/api/tasks/a%2Fb": { id: "a/b", status: "done" }, "/api/tasks": { tasks: null } });
    expect(await fetchTasks()).toEqual({ active: "", tasks: [] });
    expect((await fetchTask("a/b")).status).toBe("done");
  });

  it("createTask: 프로필 id 를 숫자로, 제외 경로를 block·fuzzy_url 규칙으로", async () => {
    let body: Record<string, unknown> = {};
    route({
      "/api/tasks": (_p: string, init?: RequestInit) => {
        body = JSON.parse(String(init?.body));
        return { id: "9", status: "created", description: body.description };
      },
    });
    const t = await createTask({
      name: "  쇼핑몰  ",
      description: "대상 https://shop.example.com",
      goal: "접근 통제 점검",
      llm_profile_ids: ["4", 2],
      coverage_enabled: true,
      intercept_rules: excludePathRules(["/admin/delete*", " /admin/delete ", "", "/logout"]),
    });
    expect(t.id).toBe("9");
    expect(apiFetch.mock.calls[0][1]?.method).toBe("POST");
    expect(body).toEqual({
      name: "쇼핑몰",
      description: "대상 https://shop.example.com",
      goal: "접근 통제 점검",
      llm_profile_ids: [4, 2],
      coverage_enabled: true,
      intercept_rules: [
        { enabled: true, action: "block", kind: "fuzzy_url", pattern: "/admin/delete", note: "작업대 제외 경로" },
        { enabled: true, action: "block", kind: "fuzzy_url", pattern: "/logout", note: "작업대 제외 경로" },
      ],
    });
  });

  it("createTask: 잘못된 프로필 id 는 보내지 않고 400", async () => {
    await expect(createTask({ description: "x", goal: "y", llm_profile_ids: ["abc"] })).rejects.toMatchObject({
      status: 400,
    });
    expect(apiFetch).not.toHaveBeenCalled();
  });

  it("controlTask: 결과를 돌려준다", async () => {
    route({ "/api/tasks/5/control": { id: "5", paused: true, queued: false, status: "paused" } });
    expect(await controlTask("5", "pause")).toEqual({ id: "5", paused: true, queued: false, status: "paused" });
    expect(JSON.parse(String(apiFetch.mock.calls[0][1]?.body))).toEqual({ action: "pause" });
  });

  it("네트워크 실패(TypeError)는 ApiError(0)", async () => {
    apiFetch.mockRejectedValue(new TypeError("Failed to fetch"));
    await expect(fetchTasks()).rejects.toMatchObject({ name: "ApiError", status: 0 });
  });
});

describe("발견 · 커버리지 · 활동", () => {
  it("task 발견 목록과 finding_id 로 상세", async () => {
    route({
      "/api/exploration/findings/77": {
        id: "77",
        finding_id: "77",
        severity: "high",
        report: "# r",
        report_stale: true,
      },
      "/api/exploration/findings": [
        { id: "301", finding_id: "77", severity: "critical", status: "confirmed", assets: null },
      ],
    });
    const list = await fetchTaskFindings("3");
    expect(apiFetch.mock.calls[0][0]).toBe("/api/exploration/findings?task=3");
    expect(list[0]).toMatchObject({ id: "301", finding_id: "77", status: "confirmed", assets: [] });
    const d = await fetchFinding(list[0].finding_id);
    expect(apiFetch.mock.calls[1][0]).toBe("/api/exploration/findings/77");
    expect(d).toMatchObject({ report: "# r", report_stale: true });
  });

  it("커버리지: 분모 0 이면 pct null", async () => {
    route({
      "/api/tasks/3/coverage": { enabled: true, scope_rows: 0, denominator: 0, tested: 0, pct: null, by_type: [] },
    });
    expect((await fetchCoverage("3")).pct).toBeNull();
    route({
      "/api/tasks/3/coverage": { enabled: true, scope_rows: 2, denominator: 10, tested: 4, pct: 0.4, by_type: [] },
    });
    expect(await fetchCoverage("3")).toMatchObject({ pct: 0.4, denominator: 10, tested: 4, scopeRows: 2 });
  });

  it("활동: since · limit, metadata 는 버리고 cursor 를 돌려준다", async () => {
    route({
      "/api/exploration/activity": {
        items: [{ seq: 11, kind: "result", summary: "ok", metadata: { secret: "x" }, is_error: false, ts: "t" }],
        cursor: 11,
      },
    });
    const page = await fetchActivity("3", 500, 10);
    expect(apiFetch.mock.calls[0][0]).toBe("/api/exploration/activity?task=3&limit=500&since=10");
    expect(page.cursor).toBe(11);
    expect(page.items[0]).not.toHaveProperty("metadata");
  });
});

describe("자산 · 트래픽", () => {
  it("엔드포인트: 200 건씩 이어 받고 auth·extra 는 버리며 파라미터 값을 가린다", async () => {
    const mk = (i: number) => ({
      id: i,
      type: "endpoint",
      method: "get",
      url: `https://x.test/api/${i}`,
      params: [
        { location: "query", name: "token", value: "tk-secret" },
        { location: "query", name: "page", value: "2" },
      ],
      auth: [{ password: "plain-pw" }],
      extra: { cookie: "c" },
      last_seen: "2026-11-09",
    });
    route({
      "/api/assets": (p: string) => {
        const q = new URLSearchParams(p.split("?")[1]);
        const off = Number(q.get("offset"));
        const lim = Number(q.get("limit"));
        const total = 450;
        return {
          count: 0,
          total,
          assets: Array.from({ length: Math.max(0, Math.min(lim, total - off)) }, (_, k) => mk(off + k)),
        };
      },
    });
    const r = await fetchEndpoints("3");
    const urls = apiFetch.mock.calls.map((c) => c[0]);
    expect(urls).toHaveLength(3);
    expect(urls[0]).toBe("/api/assets?task_id=3&type=endpoint&limit=200&offset=0");
    expect(urls[2]).toContain("offset=400");
    expect(r.total).toBe(450);
    expect(r.assets).toHaveLength(450);
    expect(r.truncated).toBe(false);
    const a = r.assets[0];
    expect(a.method).toBe("GET");
    expect(a).not.toHaveProperty("auth");
    expect(a).not.toHaveProperty("extra");
    expect(a.params).toEqual([
      { location: "query", name: "token", value: "[REDACTED]", type: "" },
      { location: "query", name: "page", value: "2", type: "" },
    ]);
    expect(JSON.stringify(r)).not.toContain("plain-pw");
  });

  it("엔드포인트: max 에서 멈추고 truncated", async () => {
    route({ "/api/assets": { total: 900, assets: Array.from({ length: 200 }, (_, i) => ({ id: i, url: "/a" })) } });
    const r = await fetchEndpoints("3", 300);
    expect(apiFetch.mock.calls.map((c) => c[0])[1]).toContain("limit=100&offset=200");
    expect(r.truncated).toBe(true);
  });

  it("트래픽: 저장소가 없을 때 모양({enabled:false, exchanges:[]})을 채운다", async () => {
    route({ "/api/traffic": { enabled: false, exchanges: [] } });
    const r = await fetchTraffic({ host: "shop.example.com", size: 9999 });
    expect(apiFetch.mock.calls[0][0]).toBe("/api/traffic?host=shop.example.com&page=0&size=500");
    expect(r).toEqual({ enabled: false, proxy: "", count: 0, total: 0, page: 0, size: 0, exchanges: [] });
  });

  it("트래픽 원문은 가려서 준다", async () => {
    route({
      "/api/traffic/exchange": {
        req: "GET /me HTTP/1.1\nCookie: sid=raw-sid\nAuthorization: Bearer raw-bearer\n\n",
        resp: 'HTTP/1.1 200 OK\n\n{"token":"raw-token"}',
      },
    });
    const r = await fetchTrafficExchange("ex-1");
    expect(apiFetch.mock.calls[0][0]).toBe("/api/traffic/exchange?id=ex-1");
    expect(JSON.stringify(r)).not.toMatch(/raw-sid|raw-bearer|raw-token/);
    expect(r.reqMasked).toContain("Cookie: sid=[REDACTED]");
  });
});

describe("LLM · 토큰 · 승인", () => {
  it("LLM: proxy · 자격 정보를 버린다", async () => {
    route({
      "/api/llm/profiles": {
        profiles: [
          {
            id: "4",
            name: "로컬",
            base_url: "http://u:p@127.0.0.1:1234/v1",
            api_key_hint: "sk-…abcd",
            proxy: "http://px",
          },
        ],
      },
      "/api/llm": {
        configured: true,
        provider: "openai",
        model: "qwen",
        base_url: "http://user:pw@localhost:1234/v1",
        proxy: "http://a:b@proxy",
        key_set: true,
      },
    });
    const llm = await fetchLlm();
    expect(llm).toEqual({
      configured: true,
      provider: "openai",
      model: "qwen",
      base_url: "http://localhost:1234/v1",
      key_set: true,
      context_window_k: 0,
    });
    const ps = await fetchLlmProfiles();
    expect(ps[0]).toEqual({
      id: "4",
      name: "로컬",
      format: "",
      base_url: "http://127.0.0.1:1234/v1",
      model: "",
      is_default: false,
      priority: 0,
    });
  });

  it("토큰: 날짜별로 프로필을 합치고 days 를 넘긴다", async () => {
    route({
      "/api/tokens/usage": {
        by_profile: [
          {
            profile_name: "",
            calls: 2,
            tasks: 1,
            input_tokens: 5,
            output_tokens: 1,
            cache_read_tokens: 0,
            cache_write_tokens: 0,
          },
        ],
        daily: [
          { profile_name: "a", date: "2026-11-02", input_tokens: 10, output_tokens: 2, cache_read_tokens: 1 },
          { profile_name: "b", date: "2026-11-02", input_tokens: 5, output_tokens: 1, cache_read_tokens: 0 },
          { profile_name: "a", date: "2026-11-01", input_tokens: 1, output_tokens: 1, cache_read_tokens: 0 },
        ],
      },
    });
    const r = await fetchTokenUsage(30);
    expect(apiFetch.mock.calls[0][0]).toBe("/api/tokens/usage?days=30");
    expect(r.daily).toEqual([
      { day: "2026-11-01", input: 1, output: 1, cacheRead: 0 },
      { day: "2026-11-02", input: 15, output: 3, cacheRead: 1 },
    ]);
    expect(r.total).toEqual({ input: 16, output: 4, cacheRead: 1 });
    expect(r.byProfile[0].profileName).toBe("(미지정)");
  });

  it("승인 대기: 목록에서 도구 입력을 버리고, 상세는 가려서 준다", async () => {
    route({
      "/api/intercept/pending/5": {
        id: 5,
        task_id: "3",
        tool_name: "http_request",
        status: "pending",
        tool_input: { url: "https://x.test/?token=raw-1", headers: { Cookie: "sid=raw-2" } },
        reason: "Bearer raw-3 사용",
        decided_at: null,
      },
      "/api/intercept/pending": {
        pending: [
          { id: 5, task_id: "3", tool_name: "http_request", status: "pending", tool_input: { password: "raw-0" } },
          { id: 6, task_id: null, status: "allowed" },
        ],
      },
    });
    const list = await fetchPendingApprovals();
    expect(list).toHaveLength(1);
    expect(list[0]).not.toHaveProperty("tool_input");
    expect(JSON.stringify(list)).not.toContain("raw-0");
    const d = await fetchPendingApproval(5);
    expect(JSON.stringify(d)).not.toMatch(/raw-1|raw-2|raw-3/);
    expect(d.toolInputMasked).toContain('"url": "https://x.test/?token=[REDACTED]"');
    await decidePendingApproval(5, "denied");
    expect(apiFetch.mock.calls.at(-1)?.[0]).toBe("/api/intercept/pending/5/decide");
    expect(JSON.parse(String(apiFetch.mock.calls.at(-1)?.[1]?.body))).toEqual({ decision: "denied" });
  });

  it("요청별 승인: approvals null → [], 미리보기 값을 가린다", async () => {
    // 팀 함수(readRequestApprovalState)가 응답 구조를 검증하므로 서버가 실제로 채우는 필드를 다 넣는다.
    const run = (phase: string) => ({
      run_ref: "run-1",
      phase,
      outcome: "",
      stop_requested: false,
      context_cancelled: false,
      drain_complete: false,
      closure_failed: false,
    });
    const approval = {
      id: "a1",
      task_id: 3,
      intent_id: 1,
      run_ref: "run-1",
      call_ref: "call-1",
      attempt_id: "att-1",
      policy_revision: "p1",
      account_ref: "acct-a",
      session_ref: "sess-a",
      request_sha256: "a".repeat(64),
      method: "GET",
      target_origin: "https://x.test",
      path: "/search",
      created_at: "2026-11-09T00:00:00Z",
      expires_at: "2026-11-09T00:10:00Z",
      body_bytes: 0,
      consumed: false,
      live: true,
      can_decide: true,
      state: "pending",
      execution_state: "not_sent",
      input_preview: { session: "raw-s", q: "shoes" },
    };
    route({ "/api/tasks/3/request-approvals": { approvals: [approval], run: run("running") } });
    const r = await fetchRequestApprovals("3");
    expect(r.approvals[0].input_preview).toEqual({ session: "[REDACTED]", q: "shoes" });
    route({ "/api/tasks/3/request-approvals": { approvals: null, run: run("prepared") } });
    expect((await fetchRequestApprovals("3")).approvals).toEqual([]);
  });

  it("조치 자료는 기본 internal", async () => {
    // 서버는 entries 를 항상 배열로 준다(diagnostic_remediation.go). null 은 팀 함수가 거부한다.
    route({ "/api/report/diagnostic/remediation": { task_id: 3, entries: [] } });
    expect(await fetchRemediation("3")).toEqual({ task_id: 3, entries: [] });
    expect(apiFetch.mock.calls[0][0]).toBe("/api/report/diagnostic/remediation?task=3&audience=internal");
  });
});

describe("downloadDiagnostic", () => {
  it("object URL + a[download] 로 저장하고 그 주소로 이동하지 않는다", async () => {
    vi.useFakeTimers();
    apiFetchBlob.mockResolvedValue(new Blob(["<html></html>"]));
    const create = vi.fn(() => "blob:mock-1");
    const revoke = vi.fn();
    Object.assign(URL, { createObjectURL: create, revokeObjectURL: revoke });
    const clicks: HTMLAnchorElement[] = [];
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      clicks.push(this);
    });
    const before = window.location.href;
    await downloadDiagnostic("12", "internal");
    expect(apiFetchBlob.mock.calls[0][0]).toBe("/api/report/diagnostic?task=12&audience=internal&download=1");
    expect(apiFetchBlob.mock.calls[0][0]).not.toMatch(/token|Bearer/i);
    expect(click).toHaveBeenCalledTimes(1);
    expect(clicks[0].download).toBe("diagnostic-task-12-internal.html");
    expect(clicks[0].getAttribute("href")).toBe("blob:mock-1");
    expect(clicks[0].isConnected).toBe(false);
    expect(window.location.href).toBe(before);
    vi.advanceTimersByTime(1000);
    expect(revoke).toHaveBeenCalledWith("blob:mock-1");
    vi.useRealTimers();
  });

  it("숫자가 아닌 task id 는 부르지 않는다", async () => {
    await expect(downloadDiagnostic("../x")).rejects.toMatchObject({ status: 400 });
    expect(apiFetchBlob).not.toHaveBeenCalled();
  });
});
