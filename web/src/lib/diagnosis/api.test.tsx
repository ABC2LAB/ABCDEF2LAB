import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ApiError, apiFetch, useApiResource } from "./api";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: Error) => void;
  const promise = new Promise<T>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}

describe("API resource snapshots", () => {
  it("does not let an older manual refresh replace a newer result", async () => {
    const slow = deferred<string>();
    const fast = deferred<string>();
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce("initial")
      .mockReturnValueOnce(slow.promise)
      .mockReturnValueOnce(fast.promise);
    const { result } = renderHook(() => useApiResource(fetcher));
    await waitFor(() => expect(result.current.data).toBe("initial"));
    act(() => result.current.reload());
    act(() => result.current.reload());
    await act(async () => fast.resolve("newest"));
    await act(async () => slow.resolve("stale"));
    expect(result.current.data).toBe("newest");
  });

  it("ignores a manual refresh from the previously selected task", async () => {
    const oldTask = deferred<string>();
    let aCalls = 0;
    const fetcher = (task: string) => (task === "A" && aCalls++ > 0 ? oldTask.promise : Promise.resolve(task));
    const { result, rerender } = renderHook(({ task }) => useApiResource(() => fetcher(task), [task]), {
      initialProps: { task: "A" },
    });
    await waitFor(() => expect(result.current.data).toBe("A"));
    act(() => result.current.reload());
    rerender({ task: "B" });
    await waitFor(() => expect(result.current.data).toBe("B"));
    await act(async () => oldTask.resolve("stale A"));
    expect(result.current.data).toBe("B");
  });

  it("clears old data during refresh and keeps a failed lookup distinct from empty data", async () => {
    const pending = deferred<string>();
    const fetcher = vi.fn().mockResolvedValueOnce("previous").mockReturnValueOnce(pending.promise);
    const { result } = renderHook(() => useApiResource(fetcher));
    await waitFor(() => expect(result.current.data).toBe("previous"));
    act(() => result.current.reload());
    expect(result.current.data).toBeNull();
    await act(async () => pending.reject(new Error("lookup failed")));
    expect(result.current.data).toBeNull();
    expect(result.current.error).toBe("lookup failed");
    expect(result.current.loading).toBe(false);
  });

  it("timestamps a successful response rather than the refresh click", async () => {
    const pending = deferred<string>();
    const clock = vi.spyOn(Date, "now").mockReturnValue(1000);
    const { result } = renderHook(() => useApiResource(() => pending.promise));
    expect(result.current.fetchedAt).toBeNull();
    clock.mockReturnValue(2000);
    await act(async () => pending.resolve("received"));
    expect(result.current.fetchedAt).toBe(2000);
  });
});

describe("오류 본문", () => {
  const fail = (status: number, body: unknown) =>
    vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }),
      );
  const caught = async (): Promise<ApiError> => {
    try {
      await apiFetch("/api/x");
    } catch (err) {
      if (err instanceof ApiError) return err;
    }
    throw new Error("ApiError 가 나지 않음");
  };

  it("문자열 error 는 그대로 메시지, code 는 없다", async () => {
    fail(403, { error: "forbidden" });
    const e = await caught();
    expect(e.message).toBe("forbidden");
    expect(e.code).toBeUndefined();
  });

  it("봉투 error {code, retryable} 는 code·retryable 로 받는다(KG 조회 v1 §5.4)", async () => {
    fail(413, { version: "abc2lab.kg-query/v1", error: { code: "kg_graph_too_large", retryable: false } });
    const e = await caught();
    expect([e.status, e.code, e.retryable, e.message]).toEqual([
      413,
      "kg_graph_too_large",
      false,
      "kg_graph_too_large",
    ]);
  });

  it("모양이 틀린 봉투는 버리고 메시지에 객체를 넣지 않는다", async () => {
    fail(503, { error: { code: "Not A Code!", retryable: true } });
    const e = await caught();
    expect([e.code, e.retryable, e.message]).toEqual([undefined, undefined, "HTTP 503"]);
    fail(503, { error: { code: "kg_store_unavailable" } });
    expect((await caught()).code).toBeUndefined();
  });
});
