"use client";

// live 공용 조회: 점검 목록(5초) · 도구 호출 승인 대기(5초)를 한 곳에서만 부르고 스토어로 나눠 준다.
// 레일 · 점검 고르기 · 여섯 화면이 같은 값을 보므로 /api/tasks 를 화면마다 따로 부르지 않는다.
// <LiveFeeds /> 는 Workbench 가 live 일 때 한 번 그린다. 로그인이 끝나야(ready) 부른다.

import { useEffect } from "react";

import { create } from "zustand";

import { fetchPendingApprovals, fetchTasks, type LiveTask, type LiveTaskList, type PendingApproval } from "./api";
import { useLiveAuth } from "./auth";
import { useSource } from "./source";
import { useUrl } from "./urlDiagnosis";
import { type Poll, usePoll } from "./usePoll";

/** 공용 조회 한 칸의 스냅숏 (usePoll 결과와 같은 모양). */
export type FeedSnap<T> = Poll<T>;

const noop = () => undefined;
const empty = <T,>(): FeedSnap<T> => ({
  data: null,
  error: null,
  loading: false,
  unsupported: false,
  fetchedAt: null,
  reload: noop,
});

interface FeedState {
  tasks: FeedSnap<LiveTaskList>;
  pending: FeedSnap<PendingApproval[]>;
}

const useFeeds = create<FeedState>()(() => ({ tasks: empty(), pending: empty() }));

/** 공용 조회 간격(ms). */
export const FEED_INTERVAL_MS = 5000;

/** 테스트·로그아웃용: 공용 조회 값을 비운다. */
export function resetFeeds(): void {
  useFeeds.setState({ tasks: empty(), pending: empty() });
}

/** 공용 조회 실행자. Workbench 가 live 일 때 한 번만 그린다(아무것도 그리지 않음). 기존(일반 · 첫 진단) 서버에서만 부른다. */
export function LiveFeeds() {
  // URL 진단(v2) 서버 · 서버 방식 확인 중에는 부르지 않는다(그 서버에는 점검 목록 · 도구 승인 대기가 없다)
  const general = useUrl((st) => false);
  const ready = useLiveAuth().state === "ready" && general;
  const tasks = usePoll(() => fetchTasks(), [], { intervalMs: FEED_INTERVAL_MS, enabled: ready });
  const pending = usePoll(() => fetchPendingApprovals(), [], { intervalMs: FEED_INTERVAL_MS, enabled: ready });

  // 결과 객체는 렌더마다 새로 만들어지므로 칸이 바뀔 때만 스토어에 넣는다
  // biome-ignore lint/correctness/useExhaustiveDependencies: 칸 단위로 비교
  useEffect(() => {
    useFeeds.setState({ tasks });
  }, [tasks.data, tasks.error, tasks.loading, tasks.unsupported, tasks.fetchedAt, tasks.reload]);
  // biome-ignore lint/correctness/useExhaustiveDependencies: 칸 단위로 비교
  useEffect(() => {
    useFeeds.setState({ pending });
  }, [pending.data, pending.error, pending.loading, pending.unsupported, pending.fetchedAt, pending.reload]);
  // 화면을 떠나면(데모로 전환) 다음 진입 때 지난 값이 잠깐 보이지 않게 비운다
  useEffect(() => resetFeeds, []);
  return null;
}

/** 점검 목록 스냅숏 (fetchTasks, 5초). data.tasks 의 숫자 칸(발견·토큰·목표)이 정본. */
export function useLiveTasks(): FeedSnap<LiveTaskList> {
  return useFeeds((st) => st.tasks);
}

/** 도구 호출 승인 대기 스냅숏 (fetchPendingApprovals, 5초). 첫 진단 모드면 unsupported. */
export function useLivePending(): FeedSnap<PendingApproval[]> {
  return useFeeds((st) => st.pending);
}

/** 점검 목록을 지금 다시 부른다 (점검 생성·일시정지 뒤 등). */
export function reloadTasks(): void {
  useFeeds.getState().tasks.reload();
}

/** 승인 대기를 지금 다시 부른다 (결정 뒤 등). */
export function reloadPending(): void {
  useFeeds.getState().pending.reload();
}

/** 목록에서 id 로 점검 하나 (없거나 아직 못 받았으면 null). */
export function useLiveTask(id: string | null): LiveTask | null {
  return useFeeds((st) => (id ? (st.tasks.data?.tasks.find((t) => t.id === id) ?? null) : null));
}

/** 고른 점검. */
export interface SelectedTask {
  /** 고른 id (없으면 null → 화면은 <TaskPicker />) */
  taskId: string | null;
  /** 목록 줄 (숫자 칸 포함). 목록을 아직 못 받았거나 사라졌으면 null */
  task: LiveTask | null;
  /** 목록을 받았는데 고른 id 가 없다 (지워진 점검 등) */
  missing: boolean;
}

/** 지금 고른 점검 (useSource().taskId + 공용 목록). */
export function useSelectedTask(): SelectedTask {
  const taskId = useSource((st) => st.taskId);
  const task = useLiveTask(taskId);
  const listed = useFeeds((st) => st.tasks.data !== null);
  return { taskId, task, missing: !!taskId && listed && !task };
}
