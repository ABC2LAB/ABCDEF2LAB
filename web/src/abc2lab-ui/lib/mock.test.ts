import { afterEach, describe, expect, it, vi } from "vitest";

import type { EngineFinding, EngineTask, TaskListResponse } from "./engine";
import { MOCK_DEFAULT_TASK } from "./mock";
import { mockFetch } from "./mock-data";

afterEach(() => {
  vi.unstubAllEnvs();
  vi.restoreAllMocks();
  vi.resetModules();
});

const SEVERITIES = ["critical", "high", "medium", "low"] as const;

async function tasks(): Promise<EngineTask[]> {
  return (await mockFetch<TaskListResponse>("/api/tasks")).tasks;
}

describe("mock data", () => {
  it("keeps each card's severity badges equal to the findings its report lists", async () => {
    const list = await tasks();
    expect(list.length).toBeGreaterThan(0);
    await Promise.all(
      list.map(async (task) => {
        const findings = await mockFetch<EngineFinding[]>(`/api/exploration/findings?task=${task.id}`);
        for (const severity of SEVERITIES) {
          expect(findings.filter((f) => f.severity === severity).length, `${task.id} ${severity}`).toBe(
            task.findings[severity],
          );
        }
        expect(findings.every((f) => f.task_id === task.id)).toBe(true);
      }),
    );
  });

  it("gives every listed task the data its report and dashboard read", async () => {
    const list = await tasks();
    await Promise.all(
      list.map(async (task) => {
        const id = encodeURIComponent(task.id);
        const [detail, coverage, tokens, activity, control] = await Promise.all([
          mockFetch<EngineTask>(`/api/tasks/${id}`),
          mockFetch<{ enabled: boolean; denominator: number; tested: number }>(`/api/tasks/${id}/coverage`),
          mockFetch<{ total: { input_tokens: number } }>(`/api/exploration/tokens?task=${id}`),
          mockFetch<{ items: { seq: number }[] }>(`/api/exploration/activity?task=${id}&limit=300`),
          mockFetch<{ items: { task_id: string }[] }>(`/api/intercept/task/${id}`),
        ]);
        expect(detail).toEqual(task);
        expect(coverage.enabled).toBe(true);
        expect(coverage.tested).toBeLessThanOrEqual(coverage.denominator);
        expect(tokens.total.input_tokens).toBeGreaterThan(0);
        expect(activity.items.length).toBeGreaterThan(0);
        expect(control.items.every((row) => row.task_id === task.id)).toBe(true);
      }),
    );
  });

  it("opens on a task that exists and is still running, so the dashboard is reachable from its card", async () => {
    const list = await tasks();
    const selected = list.find((t) => t.id === MOCK_DEFAULT_TASK);
    expect(selected?.status).toBe("running");
    // 기록 카드는 발견이 있으면 대시보드 대신 보고서를 연다(HistoryScreen canReport).
    expect(SEVERITIES.every((s) => selected?.findings[s] === 0)).toBe(true);
  });

  it("rejects unknown paths, unknown ids and the wrong method instead of inventing data", async () => {
    await expect(mockFetch("/api/unknown")).rejects.toThrow("목업 모드");
    await expect(mockFetch("/api/tasks/9999")).rejects.toThrow("목업 모드");
    await expect(mockFetch("/api/tasks/9999/coverage")).rejects.toThrow("목업 모드");
    await expect(mockFetch("/api/tasks", { method: "POST" })).rejects.toThrow("목업 모드");
  });

  it("answers task control with no content and links an archive receipt to its task", async () => {
    await expect(mockFetch("/api/tasks/1005/control", { method: "POST" })).resolves.toBeUndefined();
    const receipt = await mockFetch<{ id: string; task_id: string }>("/api/tasks/1004/archive", { method: "POST" });
    expect(receipt.task_id).toBe("1004");
    const archived = await mockFetch<{ id: string; task_id: string; state: string }>(
      `/api/task-archives/${receipt.id}`,
    );
    expect(archived).toMatchObject({ id: receipt.id, task_id: "1004", state: "archived" });
  });
});

describe("apiFetch in mock mode", () => {
  it("never calls the engine when NEXT_PUBLIC_MOCK=1", async () => {
    vi.stubEnv("NEXT_PUBLIC_MOCK", "1");
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    const { apiFetch } = await import("./api");
    const res = await apiFetch<TaskListResponse>("/api/tasks");
    expect(res.tasks.length).toBeGreaterThan(0);
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it("still calls the engine without the flag", async () => {
    vi.stubEnv("NEXT_PUBLIC_MOCK", "");
    const fetchSpy = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(new Response(JSON.stringify({ active: "", tasks: [] }), { status: 200 }));
    const { apiFetch } = await import("./api");
    await apiFetch("/api/tasks");
    expect(fetchSpy).toHaveBeenCalledWith("/api/tasks", expect.anything());
  });
});
