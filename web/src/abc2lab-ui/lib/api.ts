import { useCallback, useEffect, useRef, useState } from "react";

import { MOCK } from "./mock";

/**
 * ABC2LAB UI ↔ 엔진 API 클라이언트.
 *
 * 호출 경로: 브라우저 → `/api/*` (Next rewrite, dev) 또는 임베드 SPA → Go 서버(:8787).
 * 인증: 엔진의 유용한 엔드포인트는 전부 Bearer 토큰 필수.
 *
 * dev auto-login (임시): 새 UI엔 아직 로그인 화면이 없어, 개발 중에는 dev 자격증명으로
 * 자동 로그인해 토큰을 얻는다. 프로덕션 auth는 미정 — 이 분기는 production 번들에서 제거된다
 * (`process.env.NODE_ENV` 조건은 Next 빌드시 상수 폴딩되어 dead-code 제거).
 * 자격증명 값은 소스에 박지 않고 `web/.env.local`(gitignore)에서만 읽는다.
 */

let token: string | null = null;
let loginInFlight: Promise<string | null> | null = null;

const TOKEN_KEY = "abc2lab.devToken";

function readStoredToken(): string | null {
  try {
    return localStorage.getItem(TOKEN_KEY);
  } catch {
    return null;
  }
}

function storeToken(value: string | null): void {
  try {
    if (value) localStorage.setItem(TOKEN_KEY, value);
    else localStorage.removeItem(TOKEN_KEY);
  } catch {
    /* localStorage 불가(프라이빗 모드 등) — 토큰은 메모리에만 */
  }
}

async function devLogin(): Promise<string | null> {
  if (process.env.NODE_ENV === "production") return null;
  const username = process.env.NEXT_PUBLIC_ARTEX_DEV_USER;
  const password = process.env.NEXT_PUBLIC_ARTEX_DEV_PASS;
  if (!username || !password) return null;
  try {
    const res = await fetch("/api/auth/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username, password }),
    });
    if (!res.ok) return null;
    const data = (await res.json()) as { token?: string };
    return data.token ?? null;
  } catch {
    return null;
  }
}

async function ensureToken(): Promise<string | null> {
  if (token) return token;
  const stored = readStoredToken();
  if (stored) {
    token = stored;
    return token;
  }
  if (!loginInFlight) {
    loginInFlight = devLogin().then((t) => {
      token = t;
      storeToken(t);
      loginInFlight = null;
      return t;
    });
  }
  return loginInFlight;
}

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

/** 엔진 API 호출. path 는 `/api/...` 전체 경로. 401 이면 토큰을 버리고 한 번만 재로그인·재시도. */
export async function apiFetch<T>(path: string, init: RequestInit = {}, retryAuth = true): Promise<T> {
  // 목업 빌드는 엔진에 요청하지 않는다(로그인도 하지 않음). 데이터는 이때만 불러온다.
  if (MOCK) return (await import("./mock-data")).mockFetch<T>(path, init);
  const t = await ensureToken();
  const headers = new Headers(init.headers);
  if (t) headers.set("Authorization", `Bearer ${t}`);
  if (init.body != null && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");

  const res = await fetch(path, { ...init, headers });

  if (res.status === 401 && retryAuth) {
    token = null;
    storeToken(null);
    loginInFlight = null;
    return apiFetch<T>(path, init, false);
  }
  if (!res.ok) {
    let message = `HTTP ${res.status}`;
    try {
      const body = (await res.json()) as { error?: string };
      if (body?.error) message = body.error;
    } catch {
      /* 본문 없음/비 JSON */
    }
    throw new ApiError(res.status, message);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

/**
 * 읽기 전용 리소스 훅: 마운트 시(및 deps 변경 시) fetch + 수동 reload. 로딩/에러/데이터 제공.
 * deps 를 주면 그 값이 바뀔 때 재조회한다(항상 마운트돼 있는 화면이 선택 task 등 입력에 반응해야 할 때).
 * deps 를 생략하면 마운트 때 1회만 조회한다.
 */
export function useApiResource<T>(
  fetcher: () => Promise<T>,
  deps: readonly unknown[] = [],
): {
  data: T | null;
  loading: boolean;
  error: string | null;
  fetchedAt: number | null;
  reload: () => void;
} {
  const [data, setData] = useState<T | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [fetchedAt, setFetchedAt] = useState<number | null>(null);
  const requestVersion = useRef(0);
  // fetcher 는 화면마다 인라인으로 만들어 넘기므로 ref 로 고정한다. 재조회 트리거는 deps 가 전담.
  const fetcherRef = useRef(fetcher);
  fetcherRef.current = fetcher;

  // run 은 deps 에만 의존한다(fetcher 는 ref). deps 변경 → run 재생성 → effect 재실행.
  const run = useCallback(() => {
    const version = ++requestVersion.current;
    setData(null);
    setLoading(true);
    setError(null);
    setFetchedAt(null);
    void (async () => {
      try {
        const value = await fetcherRef.current();
        if (version === requestVersion.current) {
          setData(value);
          setFetchedAt(Date.now());
        }
      } catch (e: unknown) {
        if (version === requestVersion.current) setError(e instanceof Error ? e.message : String(e));
      } finally {
        if (version === requestVersion.current) setLoading(false);
      }
    })();
    // biome-ignore lint/correctness/useExhaustiveDependencies: 재조회 트리거는 호출부 deps 로 명시
  }, deps);

  useEffect(() => {
    run();
    // Include manual reloads: their return values are not effect cleanup callbacks.
    return () => {
      requestVersion.current += 1;
    };
  }, [run]);

  return { data, loading, error, fetchedAt, reload: run };
}
