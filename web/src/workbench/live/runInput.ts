"use client";

// 서버 점검 목록 줄에는 대상 주소 · 진단 모드가 없다(product-run-list 계약 — 목록은 Job 의 최소 칸만). 같은 이름 · 같은 대상의
// 점검을 구분하려면(사용자 11/24: 끝난 점검 두 개가 둘 다 'ABC2LAB'이라 뭐가 뭔지 모름) 그 점검 상태를 한 번 읽어 입력의 주소 ·
// 진단 모드를 붙인다. 입력은 점검이 만들어진 뒤 바뀌지 않아 주체 · 점검별로 한 번만 읽는다. 못 읽으면 null — 줄은 숨기지 않고
// 목록 값만으로 보인다.

import { useEffect, useState } from "react";

import { readURLStatus, type URLDiagnosisJob } from "@/lib/diagnosis/diagnosis-url";

import { urlHost } from "./urlMap";

export type RunInput = { host: string; mode: string };

/** 진단 모드 글. 탐색 흐름이 없는 점검은 설치 범위로 바로 진단한다 */
const MODE_TEXT: Readonly<Record<NonNullable<URLDiagnosisJob["input"]["workflow_mode"]> | "none", string>> = {
  ai_auto_v1: "AI 자동",
  user_approval_v1: "사용자 승인",
  none: "설치 범위",
};

export function runModeText(mode: URLDiagnosisJob["input"]["workflow_mode"]): string {
  return MODE_TEXT[mode ?? "none"];
}

const cache = new Map<string, RunInput>();

/** 점검 하나의 주소 · 진단 모드(그 점검 상태를 한 번 읽음). 읽기 전 · 못 읽음은 null */
export function useRunInput(id: string, owner: string | null): RunInput | null {
  const key = `${owner ?? ""}:${id}`;
  const [info, setInfo] = useState<RunInput | null>(() => cache.get(key) ?? null);
  useEffect(() => {
    const known = cache.get(key);
    if (known !== undefined || !owner) {
      setInfo(known ?? null);
      return;
    }
    let live = true;
    readURLStatus(id, owner)
      .then((j) => {
        const got = { host: urlHost(j.input.target_url), mode: runModeText(j.input.workflow_mode) };
        cache.set(key, got);
        if (live) setInfo(got);
      })
      .catch(() => {
        // 주소 · 모드 없이 목록 값으로 보인다(상태는 목록 값 그대로). 다음에 다시 그릴 때 다시 읽는다
      });
    return () => {
      live = false;
    };
  }, [id, owner, key]);
  return info;
}
