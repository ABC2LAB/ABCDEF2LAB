"use client";

import { create } from "zustand";

import { ApiError } from "@/lib/diagnosis/api";
import { appendURLRunPage, readURLRunPage, type URLRunPage, urlRunListError } from "@/lib/diagnosis/diagnosis-runs";

interface RunListStore {
  owner: string | null;
  page: URLRunPage | null;
  loading: boolean;
  error: string;
  notice: string;
}
const empty = (): RunListStore => ({ owner: null, page: null, loading: false, error: "", notice: "" });
export const useUrlRuns = create<RunListStore>()(() => empty());
let generation = 0;
let controller: AbortController | null = null;
let seenCursors = new Set<string>();

export function resetUrlRuns(): void {
  generation++;
  controller?.abort();
  controller = null;
  seenCursors = new Set();
  useUrlRuns.setState(empty());
}

/** 한 번에 한 페이지. snapshot 충돌은 누적 목록을 버리고 첫 페이지를 딱 한 번만 다시 읽는다. */
export async function loadUrlRuns(owner: string, more = false): Promise<boolean> {
  const previous = useUrlRuns.getState();
  if (more && (previous.loading || previous.owner !== owner || !previous.page?.more)) return false;
  const base = more ? previous.page : null;
  if (!base) seenCursors = new Set();
  const cursor = base?.next_cursor;
  const token = ++generation;
  controller?.abort();
  const request = new AbortController();
  controller = request;
  useUrlRuns.setState({ owner, page: base, loading: true, error: "", notice: "" });
  try {
    let next: URLRunPage;
    try {
      next = await readURLRunPage(owner, cursor ? { cursor, limit: base?.limit } : {}, request.signal);
    } catch (error) {
      if (token !== generation) return false;
      if (!(error instanceof ApiError) || error.status !== 409 || error.message !== "snapshot_changed") throw error;
      seenCursors = new Set();
      useUrlRuns.setState({ page: null, notice: "목록이 바뀌어 이전 페이지를 버리고 처음부터 다시 조회했어요." });
      next = await readURLRunPage(owner, {}, request.signal);
      if (token !== generation) return false;
      if (next.next_cursor) seenCursors.add(next.next_cursor);
      useUrlRuns.setState({ page: next });
      return true;
    }
    if (token !== generation) return false;
    if (next.next_cursor && seenCursors.has(next.next_cursor)) throw new ApiError(422, "cursor_repeated");
    const page = base ? appendURLRunPage(base, next) : next;
    if (next.next_cursor) seenCursors.add(next.next_cursor);
    useUrlRuns.setState({ page });
    return true;
  } catch (error) {
    if (token !== generation) return false;
    useUrlRuns.setState({ page: null, error: urlRunListError(error) });
    return false;
  } finally {
    if (token === generation) {
      controller = null;
      useUrlRuns.setState({ loading: false });
    }
  }
}
