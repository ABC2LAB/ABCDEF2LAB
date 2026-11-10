import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { useApiResource } from "./api";

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
