"use client";

// 레일 맨 아래 계정 버튼 + 위로 열리는 계정 메뉴 (시안 v21 · v22).
// 메뉴: 계정 이름 · 분석 모델 · 진단 모드 · 다크 모드 · 강조색 시안 · 로그아웃(로그인 안 됐으면 로그인 · 다시 확인).
// 메뉴는 화면 모드 · 강조색 변수가 있는 작업대 뿌리([data-wb-root]) 안에 그린다 — 휴대폰 레일 서랍(transform) 밖에 두려고.
// 서버에 계정 정보 API 가 없어 이름은 마지막으로 로그인한 아이디만 보인다(비밀번호 · 토큰은 저장하지 않음).

import {
  type KeyboardEvent,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";

import { createPortal } from "react-dom";

import { cx } from "../lib/cx";
import { readAccountName, useLiveAuth } from "../live/auth";
import { LiveAccountModel } from "../live/rail";
import { useShell } from "../shell";
import { type Accent, useWb } from "../store";
import s from "../workbench.module.css";
import { Icon } from "./icons";

const SWATCHES: ReadonlyArray<readonly [Accent, string, string]> = [
  ["petrol", "#0F6D8A", "페트롤"],
  ["cobalt", "#2457D6", "코발트"],
  ["mono", "#E3EBEE", "모노"],
];

/** 강조색을 고르고 이 브라우저에만 기억 (원본 setAccent). */
function pickAccent(k: Accent) {
  useWb.getState().setAccent(k);
  try {
    localStorage.setItem("abc2lab-accent", k);
  } catch {
    // 저장소를 못 쓰면 이번만 적용
  }
}

/** 다크 · 라이트를 고르고 이 브라우저에만 기억. 고르기 전에는 기기 설정을 따른다. */
function pickTheme(t: "light" | "dark") {
  useWb.getState().setTheme(t);
  try {
    localStorage.setItem("abc2lab-theme", t);
  } catch {
    // 이번만 적용
  }
}

const DARK_QUERY = "(prefers-color-scheme: dark)";
function subscribeScheme(cb: () => void) {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function")
    return () => {
      // 구독할 것 없음 (매체 질의를 못 쓰는 환경)
    };
  const q = window.matchMedia(DARK_QUERY);
  q.addEventListener?.("change", cb);
  return () => q.removeEventListener?.("change", cb);
}
const systemDark = () => typeof window.matchMedia === "function" && window.matchMedia(DARK_QUERY).matches;

/** 지금 화면이 다크인가 (고른 값, 없으면 기기 설정). */
function useIsDark(): boolean {
  const theme = useWb((st) => st.theme);
  const sys = useSyncExternalStore(subscribeScheme, systemDark, () => false);
  return theme === "system" ? sys : theme === "dark";
}

/** 머리글자: 영문 · 숫자 이름은 앞 두 글자(대문자), 한글 등은 첫 글자. */
function initials(name: string): string {
  const latin = name.replace(/[^A-Za-z0-9]/g, "");
  return /^[A-Za-z0-9]/.test(name.trim()) && latin ? latin.slice(0, 2).toUpperCase() : name.trim().slice(0, 1);
}

/** 로그인 안 된 상태의 버튼 글. */
const OUT_LABEL: Readonly<Record<string, string>> = {
  login: "로그인 안 됨",
  offline: "연결 안 됨",
  uninitialized: "초기 설정 필요",
};

/** 화살표 키로 다음 · 이전 항목 (끝에서 돌아감, 포커스가 메뉴 밖이면 아래는 첫 항목 · 위는 끝 항목). */
function stepIndex(i: number, n: number, dir: 1 | -1): number {
  if (i < 0) return dir === 1 ? 0 : n - 1;
  return (i + dir + n) % n;
}

/** 메뉴 안에서 누를 수 있는 항목 (화살표 키 이동 순서). */
function menuItems(menu: HTMLElement | null): HTMLElement[] {
  return Array.from(menu?.querySelectorAll<HTMLElement>("button:not([disabled])") ?? []);
}

/** 로그인 패널의 아이디 칸으로 이동 (지금 보이는 화면의 관문). */
function focusLogin() {
  const el = document.querySelector<HTMLInputElement>(
    'section[data-view]:not([hidden]) input[autocomplete="username"]',
  );
  el?.focus();
}

export function AccountMenu() {
  const { route } = useShell();
  const { state, logout, retry } = useLiveAuth();
  const accent = useWb((st) => st.accent);
  const dark = useIsDark();
  const [open, setOpen] = useState(false);
  const [name, setName] = useState<string | null>(null);
  const btnRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const [host, setHost] = useState<HTMLElement | null>(null);

  // 이름은 렌더 중이 아니라 effect 에서 읽는다(SSR 사전 렌더). 로그인 상태가 바뀌면 다시 읽는다.
  useEffect(() => {
    if (state === "ready") setName(readAccountName());
  }, [state]);

  const ready = state === "ready";
  const shown = name ?? "계정";
  const label = ready ? shown : (OUT_LABEL[state] ?? "확인 중");

  const close = useCallback((focusBtn = false) => {
    setOpen(false);
    if (focusBtn) btnRef.current?.focus();
  }, []);

  const place = useCallback(() => {
    const b = btnRef.current;
    const m = menuRef.current;
    if (!b || !m) return;
    const r = b.getBoundingClientRect();
    const w = Math.min(Math.max(248, r.width), window.innerWidth - 16);
    m.style.width = `${w}px`;
    m.style.left = `${Math.max(8, Math.min(r.left, window.innerWidth - w - 8))}px`;
    m.style.bottom = `${window.innerHeight - r.top + 6}px`;
  }, []);

  // 열 때: 자리 잡기 → 첫 항목에 포커스
  useLayoutEffect(() => {
    if (!open) return;
    place();
    menuItems(menuRef.current)[0]?.focus();
  }, [open, place]);

  // 바깥 클릭 · Esc · 창 크기 · 화면 이동
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      const t = e.target as Node;
      if (!menuRef.current?.contains(t) && !btnRef.current?.contains(t)) close();
    };
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        close(true);
      }
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey, true);
    window.addEventListener("resize", place);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey, true);
      window.removeEventListener("resize", place);
    };
  }, [open, close, place]);
  // 다른 화면으로 가면 닫는다 (렌더 중 상태 맞추기)
  const [seenRoute, setSeenRoute] = useState(route);
  if (seenRoute !== route) {
    setSeenRoute(route);
    if (open) setOpen(false);
  }

  const onMenuKey = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
    e.preventDefault();
    const list = menuItems(menuRef.current);
    const i = list.indexOf(document.activeElement as HTMLElement);
    list[stepIndex(i, list.length, e.key === "ArrowDown" ? 1 : -1)]?.focus();
  };

  const toggle = () => {
    if (!open) setHost(btnRef.current?.closest<HTMLElement>("[data-wb-root]") ?? document.body);
    setOpen(!open);
  };

  const menu = (
    <div className={s["acct-menu"]} id="acctMenu" ref={menuRef} role="dialog" aria-label="계정" onKeyDown={onMenuKey}>
      {ready ? (
        <>
          <div className={s["am-head"]}>
            <span className={s.avatar} aria-hidden="true">
              {initials(shown)}
            </span>
            <b>{shown}</b>
          </div>
          <div className={s["am-sep"]} />
          <LiveAccountModel />
        </>
      ) : null}
      {state === "login" ? (
        <button
          className={s["am-item"]}
          type="button"
          onClick={() => {
            close();
            focusLogin();
          }}
        >
          <Icon name="login" />
          <span>
            로그인<small>아이디 · 비밀번호</small>
          </span>
        </button>
      ) : null}
      {state === "offline" ? (
        <button
          className={s["am-item"]}
          type="button"
          onClick={() => {
            retry();
            close(true);
          }}
        >
          <Icon name="refresh" />
          <span>
            다시 연결 확인<small>엔진에 연결되지 않았습니다</small>
          </span>
        </button>
      ) : null}
      <div className={s["am-sep"]} />
      <button
        className={cx(s["am-item"], s["am-switch"])}
        type="button"
        role="switch"
        aria-checked={dark}
        onClick={() => pickTheme(dark ? "light" : "dark")}
      >
        <Icon name="moon" />
        <span>다크 모드</span>
        <i className={s["am-tg"]} aria-hidden="true" />
      </button>
      {/* biome-ignore lint/a11y/useSemanticElements: 원본 마크업 그대로 (fieldset 으로 바꾸면 모양이 달라짐) */}
      <div className={s["am-accent"]} role="group" aria-label="강조색 시안">
        <span>강조색 시안</span>
        <div className={s["sw-row"]}>
          {SWATCHES.map(([k, color, text]) => (
            <button key={k} className={s.sw} type="button" aria-pressed={accent === k} onClick={() => pickAccent(k)}>
              <i style={{ background: color }} />
              {text}
            </button>
          ))}
        </div>
      </div>
      {ready ? (
        <>
          <div className={s["am-sep"]} />
          <button
            className={s["am-item"]}
            type="button"
            onClick={() => {
              logout();
              close(true);
            }}
          >
            <Icon name="logout" />
            <span>로그아웃</span>
          </button>
        </>
      ) : null}
    </div>
  );

  return (
    <>
      <button
        ref={btnRef}
        className={cx(s["acct-btn"], !ready && s.out)}
        type="button"
        id="acctBtn"
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-controls={open ? "acctMenu" : undefined}
        onClick={toggle}
      >
        <span className={s.avatar} aria-hidden="true">
          {ready ? initials(shown) : "?"}
        </span>
        <span className={s["am-who"]}>{label}</span>
        <Icon name="updown" />
      </button>
      {open && host ? createPortal(menu, host) : null}
    </>
  );
}
