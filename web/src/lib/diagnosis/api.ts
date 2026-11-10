import { useCallback, useEffect, useRef, useState } from "react";

/**
 * ABC2LAB UI ↔ 엔진 API 클라이언트.
 *
 * 호출 경로: 브라우저 → `/api/*` (Next rewrite, dev) 또는 임베드 SPA → Go 서버(:8787).
 * 인증: 엔진의 유용한 엔드포인트는 전부 Bearer 토큰 필수.
 *
 * 로그인: LoginScreen 이 signIn 으로 토큰을 받는다. 인증이 끊기면 subscribeAuthRequired 로 알려
 * 화면이 로그인으로 돌아간다.
 *
 */

/** 로그인할 때마다 새 객체를 만든다. 늦게 온 응답이 지금 세션의 것인지 객체 동일성으로 가린다. */
type Session = { token: string };

let session: Session | null = null;

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

async function ensureSession(): Promise<Session | null> {
  if (session) return session;
  const stored = readStoredToken();
  if (stored) {
    session = { token: stored };
    return session;
  }
  return null;
}

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

const authListeners = new Set<() => void>();

/** 재시도로도 인증을 회복하지 못했을 때 호출된다. 화면을 로그인으로 돌리는 데 쓴다. */
export function subscribeAuthRequired(listener: () => void): () => void {
  authListeners.add(listener);
  return () => {
    authListeners.delete(listener);
  };
}

function startSession(value: string): Session {
  session = { token: value };
  storeToken(value);
  return session;
}

function dropSession(): void {
  session = null;
  storeToken(null);
}

function requireSignIn(): void {
  dropSession();
  for (const listener of authListeners) listener();
}

async function responseError(res: Response): Promise<ApiError> {
  let message = `HTTP ${res.status}`;
  try {
    const body = (await res.json()) as { error?: string };
    if (body?.error) message = body.error;
  } catch {
    /* 본문 없음/비 JSON */
  }
  return new ApiError(res.status, message);
}

/** 저장된 토큰이나 개발용 자동 로그인으로 세션을 잇는다. 토큰의 유효성은 첫 API 응답이 판단한다. */
export async function restoreSession(): Promise<boolean> {
  return (await ensureSession()) !== null;
}

/** 연결 실패는 status 0 인 ApiError 로 알린다. 실패한 시도는 세션을 남기지 않는다. */
export async function signIn(username: string, password: string): Promise<void> {
  let res: Response;
  try {
    res = await fetch("/api/auth/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username, password }),
      cache: "no-store",
    });
  } catch {
    throw new ApiError(0, "엔진에 연결하지 못했어요.");
  }
  if (!res.ok) throw await responseError(res);
  const data = (await res.json()) as { token?: string };
  if (!data.token) throw new ApiError(502, "로그인 응답을 확인하지 못했어요.");
  startSession(data.token);
}

export function signOut(): void {
  requireSignIn();
}

/** 로그인 없이 읽을 수 있는 설치 상태. initialized=false 면 비밀번호가 아직 없다. */
export async function readAuthStatus(): Promise<{ initialized: boolean }> {
  const res = await fetch("/api/auth/status", { cache: "no-store" });
  if (!res.ok) throw await responseError(res);
  return (await res.json()) as { initialized: boolean };
}

/** 엔진 API 호출. path 는 `/api/...` 전체 경로. 401 이면 토큰을 버리고 한 번만 재로그인·재시도. */
export async function apiFetch<T>(path: string, init: RequestInit = {}, retryAuth = true): Promise<T> {
  const res = await authenticatedResponse(path, init, retryAuth);
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

async function authenticatedResponse(path: string, init: RequestInit, retryAuth: boolean): Promise<Response> {
  const sent = await ensureSession();
  const headers = new Headers(init.headers);
  if (sent) headers.set("Authorization", `Bearer ${sent.token}`);
  if (init.body != null && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");

  const res = await fetch(path, { ...init, headers });

  if (res.status === 401) {
    // 응답을 기다리는 사이 세션이 바뀌었으면 이 401 은 지나간 세션의 것이다. 이 요청만 실패로 끝내고 지금 세션은 건드리지 않는다.
    if (retryAuth && sent === session) {
      dropSession();
      return authenticatedResponse(path, init, false);
    }
    // 재시도로 회복하지 못했거나 재전송하면 안 되는 요청(승인 결정·중지)이다.
    const error = await responseError(res);
    // 본문을 읽는 사이에도 세션이 바뀔 수 있어 읽은 뒤에 확인한다.
    if (sent === session) requireSignIn();
    throw error;
  }
  if (!res.ok) throw await responseError(res);
  return res;
}

/** Authenticated download; never put a bearer token in an href or query string. */
export async function apiFetchBlob(path: string, init: RequestInit = {}): Promise<Blob> {
  const res = await authenticatedResponse(path, { ...init, cache: "no-store", redirect: "error" }, true);
  if (!res.headers.get("Content-Type")?.toLowerCase().startsWith("text/html")) {
    throw new ApiError(422, "보고서 응답 형식을 확인할 수 없습니다.");
  }
  // Download only. Do not navigate to or render the authenticated HTML in this origin.
  return new Blob([await res.arrayBuffer()], { type: "application/octet-stream" });
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
