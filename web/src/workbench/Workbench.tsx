"use client";

// 작업대 루트: 노란 배너 · 레일 · 여섯 화면 section · 스크림 · 재생 엔진 · 해시 라우터.
// 원본의 route() · goTo · openInsp/closeInsp · openRail/closeRail · dashPick · Esc · 강조색 복원을 맡는다.
// window · location · matchMedia · localStorage 는 렌더 중에 건드리지 않고 effect·이벤트에서만 쓴다(SSR 사전 렌더).
// 데이터 원천(live/source): demo 면 시안 화면, live 면 같은 자리에 <LiveGate> 로 감싼 실제 엔진 화면을 그린다.
// live 의 대시보드 · 새 점검 · 범위 확인 · 점검 진행 · 보고서는 서버 방식(URL 진단 v2 / 기존)에 따라 views/live/byMode 가 고른다.

import { type ReactNode, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";

import { useCrawlEngine, useLiveEngine } from "./engines";
import { cx } from "./lib/cx";
import { LiveGate, useLiveAuthBoot } from "./live/auth";
import { LiveFeeds } from "./live/feeds";
import { canGoLive, useSource, useSourceBoot } from "./live/source";
import { UrlFeeds } from "./live/urlDiagnosis";
import { isView, type Shell, ShellContext, type View } from "./shell";
import { type DSel, useWb } from "./store";
import { Icon } from "./ui/icons";
import { Rail } from "./ui/Rail";
import { Crawl } from "./views/Crawl";
import { Dashboard } from "./views/Dashboard";
import { Live } from "./views/Live";
import { DashboardByMode, LiveByMode, NewScanByMode, ReportByMode, ScopeByMode } from "./views/live/byMode";
import { CrawlLive } from "./views/live/CrawlLive";
import { NewScan } from "./views/NewScan";
import { Report } from "./views/Report";
import { Scope } from "./views/Scope";
import s from "./workbench.module.css";

/** CSS 모듈 클래스로 querySelector 선택자 만들기 (테스트 환경처럼 클래스가 없으면 null). */
const dot = (cls: string | undefined) => (cls ? `.${cls}` : null);

/** 재생 엔진 자리. 예약이 바뀔 때 Workbench 전체가 다시 그려지지 않도록 따로 둔다. */
function Engines({ route, reduce }: { route: View; reduce: boolean }) {
  useCrawlEngine(route, reduce);
  useLiveEngine(route, reduce);
  return null;
}

interface Nav {
  seq: number;
  byUser: boolean;
  railNewVisible: boolean;
}

/** 맨 위 배너 문구 (demo 문구는 원본 그대로 — 바꾸지 말 것). */
const DEMO_TEXT =
  "작동하지 않는 UI 시안입니다 — 모든 숫자·기록은 예시이며, 실제 점검·소유 확인·세션 저장을 수행하지 않습니다.";
const LIVE_TEXT = "실제 엔진 데이터입니다. '엔진 미지원'으로 표시된 칸은 백엔드에 아직 없는 기능입니다.";

/** 맨 위 배너: demo 는 경고색 + "실제 엔진으로 보기", live 는 정보색 + "예시 재생으로 보기". MOCK 빌드는 버튼 없음. */
function SourceBar() {
  const source = useSource((st) => st.source);
  const setSource = useSource((st) => st.setSource);
  const live = source === "live";
  return (
    <div className={cx(s.demobar, live && s["lv-bar"])} role="note">
      <Icon name={live ? "info" : "alert"} />
      <span>{live ? LIVE_TEXT : DEMO_TEXT}</span>
      {canGoLive() ? (
        <button className={s["lv-bar-btn"]} type="button" onClick={() => setSource(live ? "demo" : "live")}>
          {live ? "예시 재생으로 보기" : "실제 엔진으로 보기"}
        </button>
      ) : null}
    </div>
  );
}

export function Workbench() {
  useSourceBoot();
  useLiveAuthBoot();
  const accent = useWb((st) => st.accent);
  const source = useSource((st) => st.source);
  const [route, setRoute] = useState<View>("dashboard");
  const [nav, setNav] = useState<Nav>({ seq: 0, byUser: false, railNewVisible: false });
  // 두 서랍을 한 상태로: Esc 는 상세 먼저, 없으면 레일 (원본 keydown)
  const [drawers, setDrawers] = useState({ insp: false, rail: false });
  const [inspFocus, setInspFocus] = useState(0);
  const [railFocus, setRailFocus] = useState(0);
  const [narrow, setNarrow] = useState(false);
  const [phone, setPhone] = useState(false);
  const [reduce, setReduce] = useState(false);

  const narrowMq = useRef<MediaQueryList | null>(null);
  const routeRef = useRef<View>("dashboard");
  const openAfter = useRef(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const views = useRef<Partial<Record<View, HTMLElement | null>>>({});

  // 상세 패널(넓지 않은 화면에서는 서랍) · 레일(휴대폰에서는 서랍)
  const openInsp = useCallback(() => {
    if (!narrowMq.current?.matches) return;
    setDrawers((d) => ({ ...d, insp: true }));
    setInspFocus((n) => n + 1);
  }, []);
  const closeInsp = useCallback(() => setDrawers((d) => (d.insp ? { ...d, insp: false } : d)), []);
  const openRail = useCallback(() => {
    setDrawers((d) => ({ ...d, rail: true }));
    setRailFocus((n) => n + 1);
  }, []);
  const closeRail = useCallback(() => setDrawers((d) => (d.rail ? { ...d, rail: false } : d)), []);

  /** 원본 route(byUser): 해시 → 화면. 서랍을 닫고 진입 처리(crawlEnter · liveEnter)를 한 뒤, 스크롤·포커스는 레이아웃 effect 에서. */
  const doRoute = useCallback((byUser: boolean) => {
    const raw = window.location.hash.slice(1);
    const h: View = isView(raw) ? raw : "dashboard";
    setDrawers({ insp: false, rail: false });
    const st = useWb.getState();
    // aria-current 는 이 순간 새 점검 줄이 보였는지로 정한다 (원본은 진입 처리 전에 계산)
    const railNewVisible = st.railNew !== null;
    routeRef.current = h;
    setRoute(h);
    // 재생 진입 처리는 예시 재생일 때만 (live 화면은 엔진 데이터를 그린다)
    if (useSource.getState().source === "demo") {
      if (h === "crawl") st.crawlEnter();
      if (h === "live") st.liveEnter();
    }
    setNav((n) => ({ seq: n.seq + 1, byUser, railNewVisible }));
  }, []);

  const goTo = useCallback(
    (name: View) => {
      if (window.location.hash === `#${name}`) doRoute(true);
      else window.location.hash = name;
    },
    [doRoute],
  );

  const dashPick = useCallback(
    (id: DSel) => {
      useWb.getState().setDSel(id);
      if (routeRef.current !== "dashboard") {
        openAfter.current = true;
        window.location.hash = "dashboard";
        return;
      }
      closeRail();
      const row = views.current.dashboard?.querySelector<HTMLElement>(`[data-dsel="${id}"]`);
      if (row && typeof row.scrollIntoView === "function") row.scrollIntoView({ block: "nearest" });
      openInsp();
    },
    [closeRail, openInsp],
  );

  // live → demo 로 돌아오면 지금 화면의 재생 진입 처리를 한 번 한다 (live 동안은 건너뛰었으므로)
  const prevSource = useRef(source);
  useEffect(() => {
    if (prevSource.current === source) return;
    prevSource.current = source;
    if (source !== "demo") return;
    const st = useWb.getState();
    if (routeRef.current === "crawl") st.crawlEnter();
    if (routeRef.current === "live") st.liveEnter();
  }, [source]);

  // 화면 폭·동작 줄이기 설정 (원본 NARROW · PHONE · REDUCE)
  useEffect(() => {
    if (typeof window.matchMedia !== "function") return;
    const nq = window.matchMedia("(max-width:1199px)");
    const pq = window.matchMedia("(max-width:759px)");
    narrowMq.current = nq;
    setReduce(window.matchMedia("(prefers-reduced-motion: reduce)").matches);
    const sync = () => {
      setNarrow(nq.matches);
      setPhone(pq.matches);
    };
    sync();
    nq.addEventListener("change", sync);
    pq.addEventListener("change", sync);
    return () => {
      nq.removeEventListener("change", sync);
      pq.removeEventListener("change", sync);
    };
  }, []);

  // 해시 라우팅: 처음 한 번(byUser=false) + hashchange 마다(byUser=true)
  useEffect(() => {
    const onHash = () => doRoute(true);
    window.addEventListener("hashchange", onHash);
    doRoute(false);
    return () => window.removeEventListener("hashchange", onHash);
  }, [doRoute]);

  // 라우팅 뒤: 화면 맨 위로, 사용자가 옮겼으면 h1 포커스, 대시보드 항목 고르고 온 길이면 상세 서랍 열기
  useLayoutEffect(() => {
    if (!nav.seq) return;
    const v = views.current[routeRef.current];
    const vmSel = dot(s.vm);
    const vm = vmSel ? v?.querySelector<HTMLElement>(vmSel) : null;
    if (vm) vm.scrollTop = 0;
    try {
      window.scrollTo(0, 0);
    } catch {
      // 스크롤을 못 하는 환경(테스트)
    }
    if (nav.byUser) {
      const t = v?.querySelector<HTMLElement>("h1");
      try {
        t?.focus({ preventScroll: true });
      } catch {
        // 포커스 실패는 무시 (원본도 무시)
      }
    }
    if (openAfter.current) {
      openAfter.current = false;
      openInsp();
    }
  }, [nav, openInsp]);

  // 상세 서랍을 열면 30ms 뒤 그 화면 상세 패널의 첫 닫기 버튼에 포커스 (원본 openInsp)
  useEffect(() => {
    if (!inspFocus) return;
    const v = views.current[routeRef.current];
    const viSel = dot(s.vi);
    const b = v?.querySelector<HTMLElement>(`${viSel ?? ""} [data-insp-close]`.trim());
    if (!b) return;
    const t = window.setTimeout(() => b.focus(), 30);
    return () => window.clearTimeout(t);
  }, [inspFocus]);

  // 레일 서랍을 열면 30ms 뒤 닫기 버튼에 포커스 (원본 openRail)
  useEffect(() => {
    if (!railFocus) return;
    const b = rootRef.current?.querySelector<HTMLElement>("[data-rail-close]");
    if (!b) return;
    const t = window.setTimeout(() => b.focus(), 30);
    return () => window.clearTimeout(t);
  }, [railFocus]);

  // Esc: 상세 서랍이 열려 있으면 그것을, 아니면 레일 서랍을 닫는다 (원본 keydown)
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      setDrawers((d) => {
        if (d.insp) return { ...d, insp: false };
        return d.rail ? { ...d, rail: false } : d;
      });
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);

  // aria-disabled="true" 인 링크는 따라가지 않는다 (원본 전역 click 처리, 예: 탐색 중의 '범위 확인')
  useEffect(() => {
    const root = rootRef.current;
    if (!root) return;
    const onClick = (e: MouseEvent) => {
      const t = e.target;
      if (t instanceof Element && t.closest('a[aria-disabled="true"]')) e.preventDefault();
    };
    root.addEventListener("click", onClick);
    return () => root.removeEventListener("click", onClick);
  }, []);

  // 강조색 시안 복원 (이 브라우저에만 기억)
  useEffect(() => {
    try {
      const saved = localStorage.getItem("abc2lab-accent");
      if (saved === "cobalt" || saved === "mono") useWb.getState().setAccent(saved);
    } catch {
      // 저장소를 못 쓰면 기본색
    }
  }, []);

  // 스크림 클릭: 두 서랍 모두 닫기
  useEffect(() => {
    const el = rootRef.current?.querySelector<HTMLElement>("[data-scrim]");
    if (!el) return;
    const onClick = () => setDrawers({ insp: false, rail: false });
    el.addEventListener("click", onClick);
    return () => el.removeEventListener("click", onClick);
  }, []);

  const shell = useMemo<Shell>(
    () => ({
      route,
      navSeq: nav.seq,
      narrow,
      phone,
      reduce,
      inspOpen: drawers.insp,
      railOpen: drawers.rail,
      railNewAtRoute: nav.railNewVisible,
      goTo,
      openInsp,
      closeInsp,
      openRail,
      closeRail,
      dashPick,
    }),
    [route, nav, narrow, phone, reduce, drawers, goTo, openInsp, closeInsp, openRail, closeRail, dashPick],
  );

  const section = (name: View, body: ReactNode) => (
    <section
      ref={(el) => {
        views.current[name] = el;
      }}
      className={cx(s.view, drawers.insp && route === name && s.io)}
      data-view={name}
      hidden={route !== name}
    >
      {body}
    </section>
  );
  // 같은 자리에 demo 화면 또는 관문으로 감싼 live 화면
  const pick = (title: string, demo: ReactNode, live: ReactNode) =>
    source === "live" ? <LiveGate title={title}>{live}</LiveGate> : demo;

  return (
    <ShellContext.Provider value={shell}>
      <div ref={rootRef} className={s.root} data-accent={accent === "petrol" ? undefined : accent}>
        <Engines route={route} reduce={reduce} />
        {source === "live" ? <LiveFeeds /> : null}
        {source === "live" ? <UrlFeeds /> : null}
        <SourceBar />
        <div className={s.app}>
          <Rail />
          <main className={s.work}>
            {section("dashboard", pick("대시보드", <Dashboard />, <DashboardByMode />))}
            {section("new", pick("새 점검", <NewScan />, <NewScanByMode />))}
            {section("crawl", pick("자산 탐색", <Crawl />, <CrawlLive />))}
            {section("scope", pick("범위 확인", <Scope />, <ScopeByMode />))}
            {section("live", pick("점검 진행", <Live />, <LiveByMode />))}
            {section("report", pick("보고서", <Report />, <ReportByMode />))}
          </main>
        </div>
        <div className={s.scrim} id="scrim" data-scrim="" hidden={!(drawers.insp || drawers.rail)} />
      </div>
    </ShellContext.Provider>
  );
}
