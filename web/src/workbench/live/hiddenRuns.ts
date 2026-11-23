"use client";

// 끝난 점검 '목록에서 숨기기'(2026-11-23 사용자·팀 결정, #867 이동찬 리뷰). 화면 표시 상태일 뿐이다 — 서버·DB·준비
// 저널에는 아무것도 쓰지 않고, 숨긴 점검도 보고서·판정·승인·실행 이력을 그대로 열 수 있다. '숨긴 점검'에서 다시 열거나
// 목록에 되돌린다. 이 브라우저에만 owner 문맥별로 기억한다(다른 브라우저·PC에서는 다시 보인다). 저장소를 못 쓰면
// 이 탭에서만 기억한다.

import { useEffect } from "react";

import { create } from "zustand";

const KEY = "abc2lab-hidden-runs:";
// 오래된 것부터 버린다. 서버 Job ID 참조 모양(diagnosis-url 의 ref)만 받는다
const MAX_HIDDEN = 512;
const RUN_ID = /^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$/;
const EMPTY: readonly string[] = [];

interface HiddenRunStore {
  owner: string | null;
  ids: readonly string[];
}
export const useHiddenRuns = create<HiddenRunStore>()(() => ({ owner: null, ids: EMPTY }));

function read(owner: string): readonly string[] {
  try {
    const raw = window.localStorage.getItem(KEY + owner);
    const value: unknown = raw ? JSON.parse(raw) : [];
    if (!Array.isArray(value)) return EMPTY;
    return [...new Set(value.filter((id): id is string => typeof id === "string" && RUN_ID.test(id)))].slice(
      -MAX_HIDDEN,
    );
  } catch {
    return EMPTY;
  }
}
function write(owner: string, ids: readonly string[]) {
  try {
    window.localStorage.setItem(KEY + owner, JSON.stringify(ids));
  } catch {
    // 저장소를 못 쓰면 이 탭에서만 기억한다
  }
}
function current(owner: string): readonly string[] {
  const st = useHiddenRuns.getState();
  return st.owner === owner ? st.ids : read(owner);
}

export function hideRun(owner: string, id: string): void {
  if (!RUN_ID.test(id)) return;
  const ids = [...current(owner).filter((x) => x !== id), id].slice(-MAX_HIDDEN);
  write(owner, ids);
  useHiddenRuns.setState({ owner, ids });
}
export function showRun(owner: string, id: string): void {
  const ids = current(owner).filter((x) => x !== id);
  write(owner, ids);
  useHiddenRuns.setState({ owner, ids });
}

/** owner 문맥의 숨긴 Job ID. 처음 쓰일 때 저장소에서 읽는다. owner 를 모르면 빈 목록. */
export function useHiddenRunIDs(owner: string | null): readonly string[] {
  const st = useHiddenRuns();
  useEffect(() => {
    if (owner && st.owner !== owner) useHiddenRuns.setState({ owner, ids: read(owner) });
  }, [owner, st.owner]);
  return owner && st.owner === owner ? st.ids : EMPTY;
}
