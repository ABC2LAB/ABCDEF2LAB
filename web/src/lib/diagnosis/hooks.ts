import { useEffect, useMemo, useRef } from "react";

/** 언마운트되면 자동으로 정리되는 setTimeout 묶음 */
export function useTimeouts() {
  const ids = useRef<number[]>([]);

  useEffect(
    () => () => {
      ids.current.forEach((id) => {
        window.clearTimeout(id);
      });
      ids.current = [];
    },
    [],
  );

  return useMemo(
    () => ({
      set(fn: () => void, ms: number) {
        const id = window.setTimeout(() => {
          ids.current = ids.current.filter((x) => x !== id);
          fn();
        }, ms);
        ids.current.push(id);
        return id;
      },
      clearAll() {
        ids.current.forEach((id) => {
          window.clearTimeout(id);
        });
        ids.current = [];
      },
    }),
    [],
  );
}

/** 팝업이 열려 있는 동안 배경 스크롤 잠금 (원본: body.style.overflow='hidden') */
export function useBodyScrollLock(active: boolean) {
  useEffect(() => {
    if (!active) return;
    const prev = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      document.body.style.overflow = prev;
    };
  }, [active]);
}

/** ESC 키로 닫기 */
export function useEscape(active: boolean, onEscape: () => void) {
  const latest = useRef(onEscape);
  useEffect(() => {
    latest.current = onEscape;
  });
  useEffect(() => {
    if (!active) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") latest.current();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [active]);
}
