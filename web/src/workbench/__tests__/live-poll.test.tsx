// live/usePoll: 간격 재조회 · 숨김 멈춤 · 지난 응답 버림 · 에러 때 직전 값 유지 · 미지원 멈춤 · enabled · StrictMode.

import { StrictMode } from "react";

import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "@/abc2lab-ui/lib/api";

import { usePoll } from "../live/usePoll";

let visibility: DocumentVisibilityState = "visible";

beforeEach(() => {
  vi.useFakeTimers();
  visibility = "visible";
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => visibility });
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

/** 바깥에서 하나씩 풀 수 있는 약속 목록. */
function deferreds<T>() {
  const list: Array<{ resolve: (v: T) => void; reject: (e: unknown) => void }> = [];
  const fn = vi.fn(
    () =>
      new Promise<T>((resolve, reject) => {
        list.push({ resolve, reject });
      }),
  );
  return { fn, list };
}

const flush = () =>
  act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
const tick = (ms: number) =>
  act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });

describe("usePoll", () => {
  it("마운트 때 부르고, 응답 뒤 간격마다 다시 부른다", async () => {
    let n = 0;
    const fetcher = vi.fn(async () => ++n);
    const { result } = renderHook(() => usePoll(fetcher, [], { intervalMs: 1000 }));
    expect(result.current.loading).toBe(true);
    await flush();
    expect(result.current.data).toBe(1);
    expect(result.current.loading).toBe(false);
    expect(result.current.fetchedAt).not.toBeNull();
    await tick(999);
    expect(fetcher).toHaveBeenCalledTimes(1);
    await tick(1);
    expect(fetcher).toHaveBeenCalledTimes(2);
    expect(result.current.data).toBe(2);
  });

  it("간격이 없으면 한 번만, reload 로 다시", async () => {
    const fetcher = vi.fn(async () => "x");
    const { result } = renderHook(() => usePoll(fetcher, []));
    await flush();
    await tick(10_000);
    expect(fetcher).toHaveBeenCalledTimes(1);
    act(() => result.current.reload());
    await flush();
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it("에러가 나도 직전 데이터를 남기고, 다시 성공하면 에러를 지운다", async () => {
    const fetcher = vi
      .fn<() => Promise<string>>()
      .mockResolvedValueOnce("ok-1")
      .mockRejectedValueOnce(new ApiError(500, "boom"))
      .mockResolvedValueOnce("ok-2");
    const { result } = renderHook(() => usePoll(fetcher, [], { intervalMs: 100 }));
    await flush();
    expect(result.current.data).toBe("ok-1");
    await tick(100);
    expect(result.current.data).toBe("ok-1");
    expect(result.current.error).toBeInstanceOf(ApiError);
    expect(result.current.unsupported).toBe(false);
    await tick(100);
    expect(result.current.data).toBe("ok-2");
    expect(result.current.error).toBeNull();
  });

  it("미지원(503) 이면 재조회를 멈추고 reload 로만 다시", async () => {
    const fetcher = vi.fn(async () => {
      throw new ApiError(503, "off");
    });
    const { result } = renderHook(() => usePoll(fetcher, [], { intervalMs: 100 }));
    await flush();
    expect(result.current.unsupported).toBe(true);
    await tick(1000);
    expect(fetcher).toHaveBeenCalledTimes(1);
    act(() => result.current.reload());
    await flush();
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it("deps 가 바뀌면 이전 값을 비우고, 늦게 온 지난 응답은 버린다", async () => {
    const d = deferreds<string>();
    const { result, rerender } = renderHook(({ id }) => usePoll(() => d.fn(), [id]), { initialProps: { id: "a" } });
    expect(d.list).toHaveLength(1);
    rerender({ id: "b" });
    expect(d.list).toHaveLength(2);
    // b 가 먼저, a 가 나중에 온다
    await act(async () => d.list[1].resolve("B"));
    expect(result.current.data).toBe("B");
    await act(async () => d.list[0].resolve("A"));
    expect(result.current.data).toBe("B");

    rerender({ id: "c" });
    expect(result.current.data).toBeNull();
    await act(async () => d.list[2].resolve("C"));
    expect(result.current.data).toBe("C");
  });

  it("문서가 숨겨지면 멈추고, 다시 보이면 바로 부른다", async () => {
    const fetcher = vi.fn(async () => 1);
    renderHook(() => usePoll(fetcher, [], { intervalMs: 500 }));
    await flush();
    expect(fetcher).toHaveBeenCalledTimes(1);
    visibility = "hidden";
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    await tick(5000);
    expect(fetcher).toHaveBeenCalledTimes(1);
    visibility = "visible";
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    await flush();
    expect(fetcher).toHaveBeenCalledTimes(2);
    await tick(500);
    expect(fetcher).toHaveBeenCalledTimes(3);
  });

  it("enabled=false 면 부르지 않고 값은 남긴다, 다시 켜면 바로 부른다", async () => {
    let n = 0;
    const fetcher = vi.fn(async () => ++n);
    const { result, rerender } = renderHook(({ on }) => usePoll(fetcher, [], { intervalMs: 200, enabled: on }), {
      initialProps: { on: true },
    });
    await flush();
    expect(result.current.data).toBe(1);
    rerender({ on: false });
    await tick(2000);
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(result.current.data).toBe(1);
    rerender({ on: true });
    await flush();
    expect(fetcher).toHaveBeenCalledTimes(2);
    expect(result.current.data).toBe(2);
  });

  it("처음부터 꺼져 있으면 부르지 않는다", async () => {
    const fetcher = vi.fn(async () => 1);
    const { result } = renderHook(() => usePoll(fetcher, [], { intervalMs: 200, enabled: false }));
    await tick(1000);
    expect(fetcher).not.toHaveBeenCalled();
    expect(result.current.loading).toBe(false);
    expect(result.current.data).toBeNull();
  });

  it("StrictMode 이중 마운트에도 타이머가 하나만 돈다", async () => {
    let n = 0;
    const fetcher = vi.fn(async () => ++n);
    const { result } = renderHook(() => usePoll(fetcher, [], { intervalMs: 1000 }), { wrapper: StrictMode });
    await flush();
    const first = fetcher.mock.calls.length;
    expect(result.current.data).toBe(first);
    await tick(1000);
    expect(fetcher.mock.calls.length).toBe(first + 1);
    await tick(1000);
    expect(fetcher.mock.calls.length).toBe(first + 2);
  });

  it("언마운트하면 멈춘다", async () => {
    const fetcher = vi.fn(async () => 1);
    const { unmount } = renderHook(() => usePoll(fetcher, [], { intervalMs: 100 }));
    await flush();
    unmount();
    await tick(1000);
    expect(fetcher).toHaveBeenCalledTimes(1);
  });
});
