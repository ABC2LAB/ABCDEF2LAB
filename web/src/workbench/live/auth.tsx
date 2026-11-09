"use client";

// live 모드 인증 관문. 팀 api(restoreSession · readAuthStatus · signIn · signOut · subscribeAuthRequired)만 쓴다.
// 상태는 한 스토어에 두고(레일·여섯 화면이 같이 본다) 확인은 Workbench 의 useLiveAuthBoot 가 한 번 돌린다.
// 비밀번호는 로그인 패널의 상태에만 잠깐 두고 제출하자마자 비운다. 기록·저장하지 않는다.

import { type FormEvent, type ReactNode, useEffect, useId, useRef, useState } from "react";

import { create } from "zustand";

import { ApiError, readAuthStatus, restoreSession, signIn, signOut, subscribeAuthRequired } from "@/abc2lab-ui/lib/api";
import { readURLCatalog } from "@/abc2lab-ui/lib/diagnosis-url";

import { cx } from "../lib/cx";
import { Icon } from "../ui/icons";
import { MenuButton } from "../ui/parts";
import s from "../workbench.module.css";
import { fetchTasks } from "./api";
import { errorText, isOffline, isUnsupported } from "./map";
import { useSource } from "./source";

/**
 * 인증 상태. checking 확인 중 · ready 호출 가능 · login 로그인 필요 ·
 * uninitialized 엔진 관리자 비밀번호 미설정 · offline 엔진에 닿지 못함.
 */
export type LiveAuthState = "checking" | "ready" | "login" | "uninitialized" | "offline";

interface AuthStore {
  state: LiveAuthState;
  /** offline 일 때 덧붙일 짧은 설명 (오류 글) */
  detail: string | null;
}

const useAuthStore = create<AuthStore>()(() => ({ state: "checking", detail: null }));

/** 테스트용: 인증 상태를 직접 정한다. */
export function setLiveAuthState(state: LiveAuthState, detail: string | null = null): void {
  useAuthStore.setState({ state, detail });
}

/** 지금 인증 상태 (구독 없이, 이벤트·테스트에서). */
export function getLiveAuthState(): LiveAuthState {
  return useAuthStore.getState().state;
}

let runId = 0;

/**
 * 연결·세션 확인: readAuthStatus(설치 상태·연결) → restoreSession(저장된 토큰) → /api/tasks 로 토큰 확인.
 * /api/tasks 가 없는 서버(URL 진단 v2 관리 서버 등: 403·404·409·501·503)는 그 서버의 인증 경로인
 * v2 catalog 로 토큰을 다시 확인한다(읽기 전용, 실행 요청 아님). 연결 실패는 offline, initialized=false 는
 * uninitialized, 토큰 없음·401 은 login, 그 밖은 ready(섹션이 따로 접는다). 겹쳐 부르면 마지막 것만 반영한다.
 */
export async function checkLiveAuth(): Promise<LiveAuthState> {
  const id = ++runId;
  const done = (state: LiveAuthState, detail: string | null = null) => {
    if (id === runId) useAuthStore.setState({ state, detail });
    return state;
  };
  useAuthStore.setState({ state: "checking", detail: null });
  try {
    const st = await readAuthStatus();
    if (id !== runId) return useAuthStore.getState().state;
    if (st?.initialized === false) return done("uninitialized");
  } catch (err) {
    if (id !== runId) return useAuthStore.getState().state;
    if (isOffline(err)) return done("offline", errorText(err));
    // 403(첫 진단 준비 서버) · 503 등은 계속 진행하고 실제 호출이 판단한다
  }
  let restored = false;
  try {
    restored = await restoreSession();
  } catch {
    restored = false;
  }
  if (id !== runId) return useAuthStore.getState().state;
  if (!restored) return done("login");
  try {
    await fetchTasks();
  } catch (err) {
    if (id !== runId) return useAuthStore.getState().state;
    if (err instanceof ApiError && err.status === 401) return done("login");
    if (isOffline(err)) return done("offline", errorText(err));
    if (isUnsupported(err)) {
      try {
        await readURLCatalog();
      } catch (e) {
        if (id !== runId) return useAuthStore.getState().state;
        if (e instanceof ApiError && e.status === 401) return done("login");
        if (isOffline(e)) return done("offline", errorText(e));
        // 404·501 등은 v2 서버도 아니라는 뜻 — 토큰 판단은 각 섹션의 실제 호출에 맡긴다
      }
    }
  }
  if (id !== runId) return useAuthStore.getState().state;
  return done("ready");
}

/** 로그인 실패 → 안내 글. */
function loginError(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 401) return "아이디 또는 비밀번호가 맞지 않습니다.";
    if (err.status === 429) return "시도가 너무 많습니다. 잠시 뒤 다시 시도하세요.";
    if (err.status === 403 && err.message) return err.message;
  }
  return errorText(err);
}

/** useLiveAuth 결과. */
export interface LiveAuth {
  state: LiveAuthState;
  /** offline 설명 (없으면 null) */
  detail: string | null;
  /** 연결·세션을 다시 확인한다. */
  retry: () => void;
  /** 로그인. 성공하면 null, 실패하면 안내 글. 비밀번호는 저장하지 않는다. */
  login: (username: string, password: string) => Promise<string | null>;
  /** 로그아웃 (팀 signOut: 토큰을 지우고 팀 화면에도 알린다). */
  logout: () => void;
}

async function login(username: string, password: string): Promise<string | null> {
  runId++; // 진행 중인 확인 결과는 버린다
  try {
    await signIn(username, password);
    useAuthStore.setState({ state: "ready", detail: null });
    return null;
  } catch (err) {
    if (isOffline(err)) useAuthStore.setState({ state: "offline", detail: errorText(err) });
    return loginError(err);
  }
}

function logout(): void {
  runId++;
  signOut();
  useAuthStore.setState({ state: "login", detail: null });
}

function retry(): void {
  void checkLiveAuth();
}

/** 인증 상태와 동작 (구독). 레일·화면 어디서나 써도 된다 — 확인은 useLiveAuthBoot 가 한다. */
export function useLiveAuth(): LiveAuth {
  const state = useAuthStore((st) => st.state);
  const detail = useAuthStore((st) => st.detail);
  return { state, detail, retry, login, logout };
}

/**
 * live 로 들어올 때 연결·세션을 확인하고, 인증이 끊겼다는 알림(subscribeAuthRequired)을 받으면 login 으로 돌린다.
 * Workbench 에서 한 번 부른다. demo 일 때는 아무것도 하지 않는다.
 */
export function useLiveAuthBoot(): void {
  const source = useSource((st) => st.source);
  useEffect(() => {
    if (source !== "live") return;
    const off = subscribeAuthRequired(() => {
      runId++;
      useAuthStore.setState({ state: "login", detail: null });
    });
    if (useAuthStore.getState().state !== "ready") void checkLiveAuth();
    return off;
  }, [source]);
}

// ---------- 화면 ----------

/** 로그인 패널. 아이디 기본값은 엔진 관리자 계정 이름(ARTEX). */
function LoginPanel() {
  const { login: doLogin } = useLiveAuth();
  const [username, setUsername] = useState("ARTEX");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const lock = useRef(false);
  const uid = useId();

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (lock.current || !username || !password) return;
    lock.current = true;
    const pw = password;
    // 제출하자마자 비운다 (상태에 남기지 않음)
    setPassword("");
    setBusy(true);
    setError(null);
    const msg = await doLogin(username, pw);
    lock.current = false;
    setBusy(false);
    setError(msg);
  };

  return (
    <form className={s["lv-gate"]} onSubmit={(e) => void submit(e)} noValidate>
      <div className={s.sec}>
        <h2 className={s["lv-gate-h"]}>엔진 로그인</h2>
        <p className={s.help}>
          실제 엔진 데이터를 보려면 엔진 관리자 계정으로 로그인하세요. 팀 화면에서 로그인했다면 그대로 이어집니다.
        </p>
      </div>
      <div className={s.field}>
        <label className={s.lbl} htmlFor={`${uid}-u`}>
          아이디
        </label>
        <input
          id={`${uid}-u`}
          className={s.inp}
          autoComplete="username"
          value={username}
          onChange={(e) => setUsername(e.target.value)}
        />
      </div>
      <div className={s.field}>
        <label className={s.lbl} htmlFor={`${uid}-p`}>
          비밀번호
        </label>
        <input
          id={`${uid}-p`}
          className={s.inp}
          type="password"
          autoComplete="current-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
      </div>
      {error ? (
        <p className={s.err} role="alert">
          {error}
        </p>
      ) : null}
      <button className={cx(s.btn, s.pri, s.full)} type="submit" disabled={busy || !username || !password}>
        {busy ? "확인 중…" : "로그인"}
      </button>
    </form>
  );
}

/** 연결 실패 · 초기 설정 필요 안내 + 다시 시도 + 예시 재생으로 보기. */
function StatusPanel({ title, children }: { title: string; children?: ReactNode }) {
  const { retry: again } = useLiveAuth();
  const setSource = useSource((st) => st.setSource);
  return (
    <div className={s["lv-gate"]}>
      <div className={cx(s.note, s.warn)} role="alert">
        <Icon name="alert" />
        <div>
          <b>{title}</b>
          {children}
        </div>
      </div>
      <div className={s["vh-acts"]}>
        <button className={cx(s.btn, s.pri)} type="button" onClick={again}>
          <Icon name="refresh" />
          다시 시도
        </button>
        <button className={s.btn} type="button" onClick={() => setSource("demo")}>
          예시 재생으로 보기
        </button>
      </div>
    </div>
  );
}

/**
 * live 화면 관문: 인증이 ready 일 때만 children(실제 화면)을 그린다. 아니면 머리(메뉴 버튼 · h1 title)와
 * 확인 중 / 로그인 / 초기 설정 필요 / 연결 실패 패널을 그린다. 레일은 관문 밖이라 그대로 남는다.
 */
export function LiveGate({ title, children }: { title: string; children: ReactNode }) {
  const { state, detail } = useLiveAuth();
  if (state === "ready") return <>{children}</>;
  let body: ReactNode;
  if (state === "checking") {
    body = (
      <p className={s.empty} role="status">
        엔진 연결을 확인하는 중…
      </p>
    );
  } else if (state === "login") {
    body = <LoginPanel />;
  } else if (state === "uninitialized") {
    body = (
      <StatusPanel title="엔진 초기 설정이 필요합니다(팀 화면에서 설정)">
        <p className={s.help}>엔진 관리자 비밀번호가 아직 없습니다. 팀 화면의 설치 절차에서 먼저 설정하세요.</p>
      </StatusPanel>
    );
  } else {
    body = (
      <StatusPanel title="엔진(8787)에 연결할 수 없습니다">
        <p className={s.help}>엔진 서버가 켜져 있는지 확인하세요.{detail ? ` (${detail})` : ""}</p>
      </StatusPanel>
    );
  }
  return (
    <>
      <header className={s.vh}>
        <div className={s["vh-top"]}>
          <MenuButton />
          <span className={s.crumb}>작업대 · 실제 엔진</span>
        </div>
        <div className={s["vh-row"]}>
          <div className={s["vh-title"]}>
            <h1 tabIndex={-1}>{title}</h1>
          </div>
        </div>
      </header>
      <div className={s.vm}>{body}</div>
    </>
  );
}
