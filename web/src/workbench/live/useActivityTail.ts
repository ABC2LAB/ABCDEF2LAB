// 최근 활동 꼬리 훅. 엔진 활동 API 는 seq 오름차순(오래된 것부터) + cursor 라서,
// 처음에는 끝까지 따라잡고(쪽당 500, 최대 maxPages 쪽) 그 뒤로는 cursor 이후만 받아 붙인다. 최근 keep 건만 남긴다.

import { useRef } from "react";

import { fetchActivity, type LiveActivity } from "./api";
import { type Poll, usePoll } from "./usePoll";

/** useActivityTail 설정. */
export interface ActivityTailOptions {
  /** 재조회 간격(ms). 기본 4000. */
  intervalMs?: number;
  /** false 면 멈춤(데이터 유지). 기본 true. */
  enabled?: boolean;
  /** 남길 최근 활동 수. 기본 200. */
  keep?: number;
  /** 한 번 조회에서 이어 받을 최대 쪽 수(쪽당 500). 기본 20. */
  maxPages?: number;
}

/** 활동 꼬리. items 는 오래된 것 → 최신 순(화면은 보통 뒤집어 최신부터). caughtUp=false 면 아직 따라잡는 중(쪽 한도에 걸림). */
export interface ActivityTail {
  items: LiveActivity[];
  cursor: number;
  caughtUp: boolean;
}

const PAGE = 500;

/**
 * 한 점검의 최근 활동을 이어 받는다 (usePoll 위에서: 숨김 멈춤 · 지난 응답 버림 · 에러 때 직전 값 유지 · 미지원이면 멈춤).
 * taskId 가 null 이면 부르지 않는다. 첫 진단 모드는 403 → unsupported.
 */
export function useActivityTail(taskId: string | null, opts: ActivityTailOptions = {}): Poll<ActivityTail> {
  const { intervalMs = 4000, enabled = true, keep = 200, maxPages = 20 } = opts;
  const acc = useRef<{ taskId: string | null; tail: ActivityTail }>({
    taskId: null,
    tail: { items: [], cursor: 0, caughtUp: false },
  });

  return usePoll<ActivityTail>(
    async () => {
      if (!taskId) return { items: [], cursor: 0, caughtUp: true };
      // 점검이 바뀌면 처음부터
      if (acc.current.taskId !== taskId) acc.current = { taskId, tail: { items: [], cursor: 0, caughtUp: false } };
      const box = acc.current;
      let { items, cursor } = box.tail;
      let caughtUp = false;
      for (let i = 0; i < maxPages; i++) {
        const page = await fetchActivity(taskId, PAGE, cursor);
        // 겹친 조회가 있어도 중복되지 않게 cursor 보다 큰 것만 붙인다
        const fresh = page.items.filter((a) => a.seq > cursor);
        if (fresh.length) items = [...items, ...fresh].slice(-keep);
        cursor = Math.max(cursor, page.cursor);
        if (page.items.length < PAGE) {
          caughtUp = true;
          break;
        }
      }
      // 그 사이 점검이 바뀌었으면 이 결과는 버린다 (usePoll 도 지난 응답을 버린다)
      if (acc.current !== box) return box.tail;
      box.tail = { items, cursor, caughtUp };
      return box.tail;
    },
    [taskId, keep, maxPages],
    { intervalMs, enabled: enabled && !!taskId },
  );
}
