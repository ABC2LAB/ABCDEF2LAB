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
/** [더 불러오기]로 다음 쪽을 붙였는가. 붙였으면 바탕 다시 읽기가 첫 쪽으로 되돌리지 않게 쉰다 */
let extended = false;

/** 바탕 다시 읽기 간격(ms). 대시보드 [새로고침]을 없애고 목록이 알아서 따라오게 한다(사용자 11/24) */
export const RUN_LIST_POLL_MS = 15_000;

export function resetUrlRuns(): void {
  generation++;
  controller?.abort();
  controller = null;
  seenCursors = new Set();
  extended = false;
  useUrlRuns.setState(empty());
}

/** 한 번에 한 페이지. snapshot 충돌은 누적 목록을 버리고 첫 페이지를 딱 한 번만 다시 읽는다. */
export async function loadUrlRuns(owner: string, more = false, quiet = false): Promise<boolean> {
  const previous = useUrlRuns.getState();
  if (more && (previous.loading || previous.owner !== owner || !previous.page?.more)) return false;
  const base = more ? previous.page : null;
  if (!base) seenCursors = new Set();
  const cursor = base?.next_cursor;
  const token = ++generation;
  controller?.abort();
  const request = new AbortController();
  controller = request;
  // 바탕 다시 읽기(quiet)는 읽는 동안 보던 목록을 그대로 둔다 — 비우면 15초마다 줄이 사라졌다 나타난다
  const keep = quiet && previous.owner === owner ? previous.page : base;
  useUrlRuns.setState(
    quiet ? { owner, page: keep, loading: true } : { owner, page: base, loading: true, error: "", notice: "" },
  );
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
    extended = !!base;
    useUrlRuns.setState(quiet ? { page, error: "", notice: "" } : { page });
    return true;
  } catch (error) {
    if (token !== generation) return false;
    // 바탕 다시 읽기가 실패하면 보던 목록은 두고 마지막으로 읽은 목록이라고 알린다(새것처럼 두지 않음)
    if (quiet && keep)
      useUrlRuns.setState({ page: keep, notice: "목록을 다시 읽지 못했어요. 마지막으로 읽은 목록입니다." });
    else useUrlRuns.setState({ page: null, error: urlRunListError(error) });
    return false;
  } finally {
    if (token === generation) {
      controller = null;
      useUrlRuns.setState({ loading: false });
    }
  }
}

/**
 * 바탕 다시 읽기: 첫 쪽만, 깜빡임 없이. 읽는 중이거나 [더 불러오기]로 쪽을 붙였으면 쉰다(붙인 쪽을 버리지 않게).
 * 다른 브라우저 · 러너가 시작한 점검도 버튼 없이 목록 · 레일에 따라온다.
 */
export function refreshUrlRunsQuietly(owner: string): void {
  const st = useUrlRuns.getState();
  if (st.loading || extended || (st.owner !== null && st.owner !== owner)) return;
  void loadUrlRuns(owner, false, true);
}
