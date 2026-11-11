// 예시 데이터 모드: 기본은 꺼져 있고, 켜면 예시 응답이 팀의 엄격한 v2 검사를 그대로 통과해야 화면이 채워진다.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const realFetch = window.fetch;

beforeEach(() => {
  vi.resetModules();
  sessionStorage.clear();
  localStorage.clear();
  // 예시 모드는 네트워크로 나가면 안 된다
  window.fetch = vi.fn(() => Promise.reject(new Error("network_not_allowed"))) as typeof fetch;
});
afterEach(() => {
  window.fetch = realFetch;
  vi.useRealTimers();
});

async function boot() {
  const example = await import("../example/exampleApi");
  example.installExampleApi();
  const api = await import("@/lib/diagnosis/diagnosis-url");
  const catalog = await api.readURLCatalog();
  return { example, api, owner: catalog.ownerContext };
}

describe("예시 데이터 모드", () => {
  it("환경 변수 없이는 꺼져 있다", async () => {
    const { EXAMPLE_MODE } = await import("../example/exampleApi");
    expect(EXAMPLE_MODE).toBe(false);
  });

  it("처음 열면 승인 대기 상황의 핸들을 넣고, 팀 검사가 그 핸들과 진단을 받아들인다", async () => {
    const { api, owner } = await boot();
    const handle = api.readURLHandle(owner);
    expect(handle?.id).toBe("job-1");
    const job = await api.readURLStatus("job-1", owner);
    expect(() => api.matchURLHandle(job, handle as NonNullable<typeof handle>)).not.toThrow();
    expect(job.state).toBe("running");
    expect(job.task_id).toBe("42");
  });

  it("승인 대기 상황에서 요청별 승인 1건과 진단 흐름을 돌려준다", async () => {
    const { api, owner } = await boot();
    const job = await api.readURLStatus("job-1", owner);
    const { readWorkflow } = await import("../live/workflow");
    const flow = await readWorkflow(job);
    expect(flow?.stage).toBe("diagnosing");
    const { fetchRequestApprovals } = await import("../live/api");
    const approvals = await fetchRequestApprovals("42");
    expect(approvals.approvals.map((a) => [a.method, a.path, a.state])).toEqual([["PUT", "/api/Users/2", "pending"]]);
  });

  it("상황을 바꾸면 다음 로드에서 그 상황의 핸들로 이어 본다", async () => {
    const { example } = await boot();
    const handle = example.buildScene("report");
    expect(JSON.parse(String(handle))).toMatchObject({ id: "job-1", revision: 3, startRevision: 3 });
    sessionStorage.setItem("abc2lab.urlDiagnosisHandle.v2", String(handle));
    const api = await import("@/lib/diagnosis/diagnosis-url");
    const owner = (await api.readURLCatalog()).ownerContext;
    const job = await api.readURLStatus("job-1", owner);
    expect(api.readURLHandle(owner)).not.toBeNull();
    expect([job.state, job.result_ready]).toEqual(["completed", true]);
  });

  it("로그인 값이 이미 있으면 덮어쓰지 않는다", async () => {
    localStorage.setItem("abc2lab.devToken", "real-token");
    await boot();
    expect(localStorage.getItem("abc2lab.devToken")).toBe("real-token");
  });
});
