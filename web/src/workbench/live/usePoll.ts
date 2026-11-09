// 조회 + 주기적 재조회 훅. 섹션마다 따로 불러 실패를 그 섹션 안에 가둔다.
// - deps 가 바뀌면(다른 점검 등) 이전 데이터를 비우고 새로 부른다.
// - 응답이 온 뒤 intervalMs 만큼 쉬고 다시 부른다(겹치지 않음). 문서가 숨겨지면 멈추고, 다시 보이면 바로 부른다.
// - 늦게 온(지난) 응답은 버린다. 에러가 나도 직전 데이터는 남긴다.
// - "이 서버 모드에서 안 됨"(isUnsupported) 이면 재조회를 멈춘다(reload · deps 변경 때 다시).
// - enabled=false 면 부르지 않고 데이터만 둔다(숨겨진 화면). StrictMode 이중 마운트에도 안전.

import { useCallback, useEffect, useRef, useState } from "react";

import { isUnsupported } from "./map";

/** usePoll 설정. */
export interface PollOptions {
  /** 재조회 간격(ms). 0 또는 생략이면 진입 때 한 번 + reload 만. */
  intervalMs?: number;
  /** false 면 조회하지 않는다(데이터는 유지). 화면이 route 가 아닐 때 끈다. 기본 true. */
  enabled?: boolean;
}

/** usePoll 결과. */
export interface Poll<T> {
  /** 마지막으로 받은 값 (아직 없으면 null). 에러가 나도 지우지 않는다. */
  data: T | null;
  /** 마지막 조회의 에러 (성공하면 null). */
  error: unknown;
  /** 지금 요청 중. 첫 로딩 표시는 `loading && !data`. */
  loading: boolean;
  /** 마지막 에러가 isUnsupported(403·404·409·501·503) 인가 → 그 섹션을 "제공되지 않음"으로 접는다. */
  unsupported: boolean;
  /** 지금 다시 부른다 (enabled=false 여도 한 번). 간격 타이머도 다시 잡는다. */
  reload: () => void;
  /** 마지막 성공 시각 (Date.now() ms), 없으면 null. */
  fetchedAt: number | null;
}

interface PollState<T> {
  data: T | null;
  error: unknown;
  loading: boolean;
  unsupported: boolean;
  fetchedAt: number | null;
}

const EMPTY: PollState<never> = { data: null, error: null, loading: false, unsupported: false, fetchedAt: null };

function sameDeps(a: readonly unknown[], b: readonly unknown[]): boolean {
  return a.length === b.length && a.every((x, i) => Object.is(x, b[i]));
}

function isAbort(err: unknown): boolean {
  return err instanceof DOMException && err.name === "AbortError";
}

/**
 * 조회 훅. fetcher 는 매 렌더 새로 만들어도 된다(최신 것을 ref 로 쓴다). 재조회 트리거는 deps · enabled · reload.
 * fetcher 는 중단 신호를 받는다(쓸 수 있으면 넘기고, 아니면 무시해도 된다 — 지난 응답은 어차피 버린다).
 *
 * @example
 * const { route } = useShell();
 * const findings = usePoll(() => fetchTaskFindings(taskId!), [taskId], { intervalMs: 4000, enabled: route === "live" && !!taskId });
 */
export function usePoll<T>(
  fetcher: (signal: AbortSignal) => Promise<T>,
  deps: readonly unknown[],
  opts: PollOptions = {},
): Poll<T> {
  const { intervalMs = 0, enabled = true } = opts;
  const [st, setSt] = useState<PollState<T>>(EMPTY);
  const fetcherRef = useRef(fetcher);
  fetcherRef.current = fetcher;
  const version = useRef(0);
  const lastDeps = useRef<readonly unknown[] | null>(null);
  const runRef = useRef<(() => void) | null>(null);

  // biome-ignore lint/correctness/useExhaustiveDependencies: 재조회 트리거는 호출부 deps 로 명시
  useEffect(() => {
    if (lastDeps.current && !sameDeps(lastDeps.current, deps)) setSt(EMPTY);
    lastDeps.current = deps;

    let timer: number | undefined;
    let ctrl: AbortController | null = null;
    let halted = false;
    let alive = true;

    const schedule = () => {
      window.clearTimeout(timer);
      if (!alive || !enabled || halted || !intervalMs || document.visibilityState === "hidden") return;
      timer = window.setTimeout(run, intervalMs);
    };

    const run = () => {
      if (!alive) return;
      window.clearTimeout(timer);
      ctrl?.abort();
      const c = new AbortController();
      ctrl = c;
      const v = ++version.current;
      halted = false;
      setSt((s) => (s.loading ? s : { ...s, loading: true }));
      let p: Promise<T>;
      try {
        // 약속이 아닌 값을 돌려줘도 견딘다
        p = Promise.resolve(fetcherRef.current(c.signal));
      } catch (err) {
        p = Promise.reject(err);
      }
      p.then(
        (data) => {
          if (v !== version.current) return;
          setSt({ data, error: null, loading: false, unsupported: false, fetchedAt: Date.now() });
          schedule();
        },
        (err: unknown) => {
          if (v !== version.current || isAbort(err)) return;
          const un = isUnsupported(err);
          halted = un;
          setSt((s) => ({ ...s, error: err, loading: false, unsupported: un }));
          schedule();
        },
      );
    };
    runRef.current = run;

    const onVis = () => {
      if (document.visibilityState === "hidden") window.clearTimeout(timer);
      else if (enabled && !halted && intervalMs) run();
    };
    if (enabled) {
      run();
      document.addEventListener("visibilitychange", onVis);
    }
    return () => {
      alive = false;
      window.clearTimeout(timer);
      ctrl?.abort();
      version.current++;
      if (runRef.current === run) runRef.current = null;
      document.removeEventListener("visibilitychange", onVis);
      setSt((s) => (s.loading ? { ...s, loading: false } : s));
    };
  }, [enabled, intervalMs, ...deps]);

  const reload = useCallback(() => runRef.current?.(), []);

  return { ...st, reload };
}
