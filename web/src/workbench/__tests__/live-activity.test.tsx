// live/useActivityTail: 오래된 순 + cursor 응답을 끝까지 따라잡고, 그 뒤로는 새 것만 붙인다.

import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/diagnosis/api", async (importOriginal) => {
  const real = await importOriginal<typeof import("@/lib/diagnosis/api")>();
  return { ...real, apiFetch: vi.fn() };
});

import * as teamApi from "@/lib/diagnosis/api";

import { useActivityTail } from "../live/useActivityTail";

const apiFetch = vi.mocked(teamApi.apiFetch);
/** task 별 활동 seq 끝 번호 (1..last) */
const last: Record<string, number> = {};

beforeEach(() => {
  apiFetch.mockReset();
  apiFetch.mockImplementation(async (path: string) => {
    const q = new URLSearchParams(path.split("?")[1]);
    const task = q.get("task") ?? "";
    const since = Number(q.get("since") ?? 0);
    const limit = Number(q.get("limit"));
    const end = Math.min(last[task] ?? 0, since + limit);
    const items = [];
    for (let s = since + 1; s <= end; s++) items.push({ seq: s, kind: "result", summary: `${task}-${s}`, ts: "" });
    return { items, cursor: end > since ? end : since } as never;
  });
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("useActivityTail", () => {
  it("처음에는 끝까지 따라잡고 최근 keep 건만 남긴다", async () => {
    last.t1 = 1203;
    const { result } = renderHook(() => useActivityTail("t1", { keep: 200, intervalMs: 0 }));
    await waitFor(() => expect(result.current.data?.caughtUp).toBe(true));
    const d = result.current.data;
    expect(d?.cursor).toBe(1203);
    expect(d?.items).toHaveLength(200);
    expect(d?.items[0].seq).toBe(1004);
    expect(d?.items.at(-1)?.seq).toBe(1203);
    // since 0 → 500 → 1000 세 쪽
    expect(apiFetch.mock.calls.map((c) => new URLSearchParams(c[0].split("?")[1]).get("since") ?? "0")).toEqual([
      "0",
      "500",
      "1000",
    ]);

    // 새 활동 두 건 → 다시 부르면 그것만 붙는다
    last.t1 = 1205;
    act(() => result.current.reload());
    await waitFor(() => expect(result.current.data?.cursor).toBe(1205));
    expect(result.current.data?.items.at(-1)?.summary).toBe("t1-1205");
    expect(result.current.data?.items).toHaveLength(200);
  });

  it("점검이 바뀌면 처음부터, null 이면 부르지 않는다", async () => {
    last.a = 3;
    last.b = 2;
    const { result, rerender } = renderHook(({ id }: { id: string | null }) => useActivityTail(id, { intervalMs: 0 }), {
      initialProps: { id: "a" as string | null },
    });
    await waitFor(() => expect(result.current.data?.items.map((x) => x.seq)).toEqual([1, 2, 3]));
    rerender({ id: "b" });
    await waitFor(() => expect(result.current.data?.items.map((x) => x.summary)).toEqual(["b-1", "b-2"]));
    const calls = apiFetch.mock.calls.length;
    rerender({ id: null });
    expect(result.current.data).toBeNull();
    expect(apiFetch.mock.calls.length).toBe(calls);
  });
});
