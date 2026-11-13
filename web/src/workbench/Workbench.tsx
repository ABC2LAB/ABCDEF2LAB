"use client";

// 작업대 루트: 노란 배너 · 레일 · 일곱 화면 section · 스크림 · 재생 엔진 · 해시 라우터.
// 원본의 route() · goTo · openInsp/closeInsp · openRail/closeRail · dashPick · Esc · 강조색 복원을 맡는다.
// window · location · matchMedia · localStorage 는 렌더 중에 건드리지 않고 effect·이벤트에서만 쓴다(SSR 사전 렌더).
// 데이터 원천(live/source): demo 면 시안 화면, live 면 같은 자리에 <LiveGate> 로 감싼 실제 엔진 화면을 그린다.
// live 의 대시보드 · 새 점검 · 범위 확인 · 점검 진행 · 보고서는 서버 방식(URL 진단 v2 / 기존)에 따라 views/live/byMode 가 고른다.

import { type ReactNode, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";

import { cx } from "./lib/cx";
import { LiveGate, useLiveAuthBoot } from "./live/auth";
import { UrlFeeds, useUrl } from "./live/urlDiagnosis";
import { URL_ENDED_STATES } from "./live/urlMap";
import { isView, type Shell, ShellContext, type View } from "./shell";
import { type DSel, type Panel, useWb } from "./store";
import { Icon } from "./ui/icons";
import { Rail } from "./ui/Rail";
import { AccountClassifyLive } from "./views/live/AccountClassifyLive";
import {
  DashboardByMode,
  FinishedByMode,
  LiveByMode,
  NewScanByMode,
  ReportByMode,
  RunningByMode,
  ScopeByMode,
} from "./views/live/byMode";
import { UrlAccessMapLive } from "./views/live/UrlAccessMapLive";
import { UrlAssetsLive } from "./views/live/UrlAssetsLive";
import { UrlControlPolicyLive } from "./views/live/UrlControlPolicyLive";
import { UrlCoverageLive } from "./views/live/UrlCoverageLive";
import { UrlE2FlowLive } from "./views/live/UrlE2FlowLive";
import { UrlExploreLive } from "./views/live/UrlExploreLive";
import { UrlFindingLive } from "./views/live/UrlFindingLive";
import s from "./workbench.module.css";

/** CSS 모듈 클래스로 querySelector 선택자 만들기 (테스트 환경처럼 클래스가 없으면 null). */
const dot = (cls: string | undefined) => (cls ? `.${cls}` : null);

interface Nav {
  seq: number;
  byUser: boolean;
  railNewVisible: boolean;
}

/** 넓은 화면에서 접은 패널을 이 브라우저에 기억하는 키 (시안과 같은 키 · 값 '1' 접음 / '0' 펼침). */
const PANEL_KEY: Readonly<Record<Panel, string>> = { rail: "abc2lab-rail-min", insp: "abc2lab-insp-min" };
/** 시안 v91 까지 쓰던 키 ('1' 만 저장). 한 번 읽어 새 키로 옮기고 지운다. */
const OLD_PANEL_KEY: Readonly<Record<Panel, string>> = { rail: "abc2lab-rail-hidden", insp: "abc2lab-insp-hidden" };
/** 패널을 접거나 펼친 뒤 다음 버튼으로 포커스를 옮기기까지 (화면이 다시 그려질 시간). */
const FOCUS_DELAY_MS = 30;

/**
 * 화면이 그 폭보다 넓은가 (matchMedia 결과가 거짓). matchMedia 가 없는 환경(테스트)은 넓다고 보지 않는다
 * → 기존 서랍 동작 그대로.
 */
const wider = (mq: MediaQueryList | null) => !!mq && !mq.matches;

/** 넓은 화면 패널 접기 · 펼치기를 스토어와 이 브라우저 저장소에 같이 쓴다 (시안 setRailMin · setInspMin 의 save). */
function setPanel(panel: Panel, min: boolean) {
  useWb.getState().setPanelMin(panel, min);
  try {
    localStorage.setItem(PANEL_KEY[panel], min ? "1" : "0");
  } catch {
    // 저장소를 못 쓰면 이번만 적용
  }
}

/** 저장해 둔 접기 상태를 읽는다. 새 키가 없으면 옛 키를 한 번 옮겨 온다. */
function readPanel(panel: Panel): boolean {
  const saved = localStorage.getItem(PANEL_KEY[panel]);
  if (saved !== null) return saved === "1";
  const old = localStorage.getItem(OLD_PANEL_KEY[panel]) === "1";
  localStorage.removeItem(OLD_PANEL_KEY[panel]);
  if (old) localStorage.setItem(PANEL_KEY[panel], "1");
  return old;
}

export function Workbench() {
  useLiveAuthBoot();
  const accent = useWb((st) => st.accent);
  const theme = useWb((st) => st.theme);
  const railMin = useWb((st) => st.railMin);
  const inspMin = useWb((st) => st.inspMin);
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
  const phoneMq = useRef<MediaQueryList | null>(null);
  const routeRef = useRef<View>("dashboard");
  const openAfter = useRef(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const views = useRef<Partial<Record<View, HTMLElement | null>>>({});

  /** 지금 화면 안에서 고른 버튼에 잠시 뒤 포커스 (접고 펴도 같은 자리의 버튼에 포커스를 둔다). */
  const focusLater = useCallback((selector: string, inView: boolean) => {
    window.setTimeout(() => {
      const scope = inView ? views.current[routeRef.current] : rootRef.current;
      try {
        scope?.querySelector<HTMLElement>(selector)?.focus();
      } catch {
        // 포커스 실패는 무시
      }
    }, FOCUS_DELAY_MS);
  }, []);
  // 상세 패널: 넓지 않은 화면에서는 서랍, 넓은 화면에서는 접기 · 펼치기(항목을 고르면 다시 펼친다)
  const openInsp = useCallback(() => {
    if (wider(narrowMq.current)) {
      if (!useWb.getState().inspMin) return;
      setPanel("insp", false);
      setInspFocus((n) => n + 1);
      return;
    }
    if (!narrowMq.current?.matches) return;
    setDrawers((d) => ({ ...d, insp: true }));
    setInspFocus((n) => n + 1);
  }, []);
  const closeInsp = useCallback(() => {
    if (wider(narrowMq.current)) {
      setPanel("insp", true);
      focusLater("[data-insp-fold]", true);
      return;
    }
    setDrawers((d) => (d.insp ? { ...d, insp: false } : d));
  }, [focusLater]);
  // 레일: 휴대폰에서는 서랍, 그보다 넓으면 아이콘 줄로 접기 · 펼치기
  const openRail = useCallback(() => {
    if (wider(phoneMq.current)) {
      setPanel("rail", false);
      focusLater("[data-rail-fold]", false);
      return;
    }
    setDrawers((d) => ({ ...d, rail: true }));
    setRailFocus((n) => n + 1);
  }, [focusLater]);
  const closeRail = useCallback(() => setDrawers((d) => (d.rail ? { ...d, rail: false } : d)), []);
  const foldRail = useCallback(() => {
    setPanel("rail", true);
    focusLater("[data-rail-fold]", false);
  }, [focusLater]);

  /** 원본 route(byUser): 해시 → 화면. 서랍을 닫고 진입 처리(crawlEnter · liveEnter)를 한 뒤, 스크롤·포커스는 레이아웃 effect 에서. */
  const doRoute = useCallback((byUser: boolean) => {
    let raw = window.location.hash.slice(1);
    // 옛 주소 #records (점검 기록, 시안 v54 에서 둘로 나눔) → 이어 보는 점검 상태에 맞는 목록 화면으로, 주소도 바꿔 둔다.
    // 상태를 아직 모르면(처음 열 때 · 이어 보는 점검 없음) 어느 쪽인지 짐작하지 않고 대시보드
    if (raw === "records") {
      const job = useUrl.getState().job;
      if (job) {
        raw = URL_ENDED_STATES.includes(job.state) ? "finished" : "running";
        window.history.replaceState(null, "", `#${raw}`);
      }
    }
    const h: View = isView(raw) ? raw : "dashboard";
    setDrawers({ insp: false, rail: false });
    const _st = useWb.getState();
    // aria-current 는 이 순간 새 점검 줄이 보였는지로 정한다 (원본은 진입 처리 전에 계산)
    const railNewVisible = false;
    routeRef.current = h;
    setRoute(h);
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

  // 화면 폭·동작 줄이기 설정 (원본 NARROW · PHONE · REDUCE)
  useEffect(() => {
    if (typeof window.matchMedia !== "function") return;
    const nq = window.matchMedia("(max-width:1199px)");
    const pq = window.matchMedia("(max-width:759px)");
    narrowMq.current = nq;
    phoneMq.current = pq;
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

  // 강조색 시안 · 화면 모드 · 접은 패널 복원 (이 브라우저에만 기억)
  useEffect(() => {
    try {
      const saved = localStorage.getItem("abc2lab-accent");
      if (saved === "cobalt" || saved === "mono") useWb.getState().setAccent(saved);
      const savedTheme = localStorage.getItem("abc2lab-theme");
      if (savedTheme === "light" || savedTheme === "dark") useWb.getState().setTheme(savedTheme);
      for (const panel of ["rail", "insp"] as const) {
        if (readPanel(panel)) useWb.getState().setPanelMin(panel, true);
      }
    } catch {
      // 저장소를 못 쓰면 기본색 · 펼친 패널
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
      foldRail,
      dashPick,
    }),
    [route, nav, narrow, phone, reduce, drawers, goTo, openInsp, closeInsp, openRail, closeRail, foldRail, dashPick],
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
  const pick = (title: string, live: ReactNode) => <LiveGate title={title}>{live}</LiveGate>;

  return (
    <ShellContext.Provider value={shell}>
      <div
        ref={rootRef}
        className={s.root}
        data-wb-root=""
        data-accent={accent === "petrol" ? undefined : accent}
        data-theme={theme === "system" ? undefined : theme}
      >
        <UrlFeeds />
        <div className={cx(s.demobar, s["lv-bar"])} role="note">
          <Icon name="info" />
          <span>실제 서버 전용 · 승인 범위와 사용량은 서버가 확인한 값만 표시합니다.</span>
        </div>
        <div className={cx(s.app, railMin && s["rail-min"], inspMin && s["insp-min"])}>
          <Rail />
          <main className={s.work}>
            {section("dashboard", pick("대시보드", <DashboardByMode />))}
            {section("running", pick("진행 중인 점검", <RunningByMode />))}
            {section("finished", pick("끝난 점검", <FinishedByMode />))}
            {section("new", pick("새 점검", <NewScanByMode />))}
            {section("crawl", pick("자산 탐색", <UrlAssetsLive />))}
            {section("explore", pick("탐색 결과", <UrlExploreLive />))}
            {section("accessmap", pick("접근통제 지도", <UrlAccessMapLive />))}
            {section("coverage", pick("커버리지", <UrlCoverageLive />))}
            {section("finding", pick("발견 상세", <UrlFindingLive />))}
            {section("e2flow", pick("전송 통제 흐름", <UrlE2FlowLive />))}
            {section("controlpolicy", pick("전송 통제 정책", <UrlControlPolicyLive />))}
            {section("classify", pick("계정 분류", <AccountClassifyLive />))}
            {section("scope", pick("범위 확인", <ScopeByMode />))}
            {section("live", pick("점검 진행", <LiveByMode />))}
            {section("report", pick("보고서", <ReportByMode />))}
          </main>
        </div>
        <div className={s.scrim} id="scrim" data-scrim="" hidden={!(drawers.insp || drawers.rail)} />
      </div>
    </ShellContext.Provider>
  );
}
