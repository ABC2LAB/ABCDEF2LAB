// 순수 로직 검사: 분류 · 진행률 · 크롤링/점검 재생 · 결정 대기. 기대값은 원본 시안 스크립트를 그대로 돌린 결과와 같다.

import { beforeEach, describe, expect, it } from "vitest";

import { AT, EV, LINES, PCT } from "../data/crawl";
import { LCUM, LRESUME, LT, LTOTAL } from "../data/live";
import { EP, epOn } from "../data/scope";
import { crawlEnter, crawlInit, crawlReset, crawlSetView, crawlStep, crawlTogglePause } from "../lib/crawlState";
import { pending, pendingSub, railApi } from "../lib/decisions";
import { curHost } from "../lib/host";
import { isEx, kind, pkind } from "../lib/kind";
import {
  type LiveState,
  lcur,
  liveKick,
  liveReset,
  liveStep,
  liveTogglePause,
  lpct,
  lremain,
  railShop,
  vcount,
} from "../lib/liveState";
import { hms } from "../lib/time";
import { sortedKids, trie, under } from "../lib/trie";
import { createInitialState, useWb } from "../store";

beforeEach(() => {
  useWb.setState(createInitialState());
});

describe("kind() 분류", () => {
  it("전역 제외 규칙 /admin/delete* 는 메서드와 상관없이 lock", () => {
    expect(kind("DELETE", "/admin/delete-user/{id}")).toBe("lock");
    expect(kind("GET", "/admin/delete-all")).toBe("lock");
  });
  it("기본 제외 경로는 off", () => {
    expect(kind("GET", "/api/logout")).toBe("off");
    expect(kind("POST", "/api/password/reset")).toBe("off");
    expect(kind("DELETE", "/api/cart/{id}")).toBe("off");
  });
  it("GET 은 get, 로그인은 login, 나머지 변경 요청은 ap", () => {
    expect(kind("GET", "/products")).toBe("get");
    expect(kind("POST", "/api/login")).toBe("login");
    expect(kind("POST", "/api/checkout")).toBe("ap");
    expect(kind("PUT", "/api/users/{id}")).toBe("ap");
    expect(kind("DELETE", "/api/orders/{id}")).toBe("ap");
  });
  it("pkind 는 off·lock 을 ex 로 합치고 isEx 는 그 둘만 참", () => {
    expect(pkind({ m: "GET", p: "/api/logout" })).toBe("ex");
    expect(pkind({ m: "DELETE", p: "/admin/delete-user/{id}" })).toBe("ex");
    expect(pkind({ m: "POST", p: "/api/login" })).toBe("login");
    expect(isEx({ m: "GET", p: "/api/logout" })).toBe(true);
    expect(isEx({ m: "POST", p: "/api/checkout" })).toBe(false);
  });
  it("범위 엔드포인트: 37개 중 기본 포함 33 · 제외 4 (전역 제외 1)", () => {
    expect(EP).toHaveLength(37);
    expect(EP.filter((e) => e.defaultOn)).toHaveLength(33);
    expect(EP.filter((e) => e.lock).map((e) => e.p)).toEqual(["/admin/delete-user/{id}"]);
    const logout = EP.find((e) => e.p === "/api/logout");
    expect(logout && epOn(logout, {})).toBe(false);
    expect(logout && epOn(logout, { [logout.i]: true })).toBe(true);
  });
});

describe("자산 탐색 각본", () => {
  it("EV 는 LINES 와 같은 길이, AT 는 0 에서 시작해 늘어난다", () => {
    expect(EV).toHaveLength(LINES.length);
    expect(AT[0]).toBe(0);
    for (let i = 1; i < AT.length; i++) expect(AT[i]).toBeGreaterThan(AT[i - 1]);
  });
  it("PCT 는 줄지 않고 마지막은 100", () => {
    expect(PCT).toHaveLength(EV.length);
    for (let i = 1; i < PCT.length; i++) expect(PCT[i]).toBeGreaterThanOrEqual(PCT[i - 1]);
    expect(PCT[PCT.length - 1]).toBe(100);
    expect(PCT.slice(0, 6)).toEqual([0, 7, 12, 18, 24, 24]);
  });
});

describe("자산 탐색 재생", () => {
  it("진입하면 처음부터 시작해 300ms 뒤 첫 단계를 예약하고, 이미 시작했으면 그대로", () => {
    const c = crawlEnter(crawlInit());
    expect(c.started).toBe(true);
    expect(c.sched?.ms).toBe(300);
    expect(crawlEnter(c)).toBe(c);
  });

  it("끝까지 재생하면 done · 찾은 경로 37 · 보낸 요청 40 · 요청 안 함 3", () => {
    let c = crawlEnter(crawlInit());
    let steps = 0;
    while (!c.done) {
      const i = c.i;
      c = crawlStep(c, { rowFresh: true });
      steps++;
      if (!c.done) expect(c.sched?.ms).toBe(AT[i + 1] - AT[i] || 120);
      expect(steps).toBeLessThanOrEqual(EV.length);
    }
    expect(steps).toBe(EV.length);
    expect(c.sched).toBeNull();
    expect(c.found).toHaveLength(37);
    expect(c.sent).toBe(EV.filter((e) => e.k === "req" || e.k === "login").length);
    expect(c.sent).toBe(40);
    expect(c.skip).toBe(3);
    expect(c.rowFresh).toHaveLength(EV.length);
    // 마지막 단계는 새 경로가 없으므로 fresh 는 그 전 마지막 발견(관리자 스크립트 8개)만
    expect(c.found.filter((f) => f.fresh)).toHaveLength(8);
    expect(crawlStep(c)).toBe(c);
  });

  it("방금 찾은 것만 fresh, 보기를 바꾸면 모두 꺼짐", () => {
    let c = crawlEnter(crawlInit());
    c = crawlStep(c); // sys
    c = crawlStep(c); // GET / → 6개
    expect(c.found.filter((f) => f.fresh)).toHaveLength(6);
    c = crawlStep(c); // robots → 3개
    expect(c.found.filter((f) => f.fresh).map((f) => f.p)).toEqual(["/robots.txt", "/sitemap.xml", "/health"]);
    c = crawlStep(c); // sitemap: 새 경로 없음 → 그대로
    expect(c.found.filter((f) => f.fresh)).toHaveLength(3);
    c = crawlSetView(c, "tree");
    expect(c.found.some((f) => f.fresh)).toBe(false);
  });

  it("일시정지하면 예약이 없어지고 재생이 멈추며, 다시 시작하면 250ms 뒤", () => {
    let c = crawlStep(crawlEnter(crawlInit()));
    c = crawlTogglePause(c);
    expect(c.paused).toBe(true);
    expect(c.sched).toBeNull();
    expect(crawlStep(c)).toBe(c);
    c = crawlTogglePause(c);
    expect(c.sched?.ms).toBe(250);
  });

  it("재시작은 view·smOpen 을 남기고 kf 는 all 로", () => {
    const c = crawlReset({ ...crawlInit(), view: "tree", kf: "ex", smOpen: { "/api": false }, i: 9 });
    expect(c).toMatchObject({ started: true, i: 0, view: "tree", kf: "all", smOpen: { "/api": false }, run: 1 });
  });
});

/** 재생을 끝까지 돌린다 (엔진 대신). */
function playToEnd(l0: LiveState): LiveState {
  let l = liveKick(l0);
  let guard = 0;
  while (!l.done) {
    expect(l.sched).not.toBeNull();
    l = liveStep(l, { fresh: false });
    guard++;
    expect(guard).toBeLessThanOrEqual(LT.length);
  }
  return l;
}

describe("점검 진행 재생", () => {
  it("'resume' 은 10:42 시작 · 작업 11(약 58%)부터, 발견 3건은 판정 중", () => {
    const l = liveReset("resume");
    expect(l.i).toBe(LRESUME);
    expect(lpct(l)).toBe(58);
    expect(lremain(l)).toBe(13);
    expect(hms(l.start).slice(0, 5)).toBe("10:42");
    expect(l.forder).toEqual(["f1", "f3", "f2"]);
    expect(vcount(l)).toBe("판정 중 3");
    expect(l.decide).toBe(true);
    expect(l.feed).toHaveLength(LRESUME);
    expect(l.feed[0].k).toBe(LRESUME - 1);
    expect(l.sent).toBe(1261);
    expect(l.playing).toBe(false);
    expect(l.sched).toBeNull();
    expect(lcur(l)).toBe(LT[LRESUME]);
  });

  it("끝까지 재생하면 done · 발견 6건 (확정 3 · 의심 2 · 판단 불가 1)", () => {
    const l = playToEnd(liveReset("resume"));
    expect(l.done).toBe(true);
    expect(l.playing).toBe(false);
    expect(l.i).toBe(LT.length);
    expect(lpct(l)).toBe(100);
    expect(lcur(l)).toBeNull();
    expect(l.forder).toEqual(["f1", "f3", "f2", "f4", "f6", "f5"]);
    expect(l.fv).toEqual({ f1: "확정", f2: "확정", f3: "의심", f4: "확정", f5: "판단 불가", f6: "의심" });
    expect(vcount(l)).toBe("확정 3 · 의심 2 · 판단 불가 1");
    expect(l.feed).toHaveLength(LT.length);
    expect(l.sent).toBe(1270);
    expect(l.sent).toBe(40 + LT.reduce((a, t) => a + (t.sent ?? 0), 0));
    expect(l).toMatchObject({ ep: 47, blk: 30, hold: 3 });
    expect(l.feed[0].at).toBe("11:11:30");
    expect(railShop(l)).toEqual({ dot: "ok", sub: "점검 완료 · 발견 6", pct: 100 });
  });

  it("'fresh' 는 주어진 시각에 처음부터 시작해 같은 결과로 끝난다", () => {
    const start = 9 * 3600 + 5 * 60;
    const l0 = liveReset("fresh", start);
    expect(l0).toMatchObject({ i: 0, start, forder: [], feed: [], sent: 40, ep: 37, decide: false });
    expect(lpct(l0)).toBe(0);
    const l = playToEnd(l0);
    expect(vcount(l)).toBe("확정 3 · 의심 2 · 판단 불가 1");
    expect(l.feed[0].at).toBe(hms(start + Math.round(LTOTAL * 60)));
  });

  it("작업 간격은 걸린 시간 × 1.1초 (최소 400ms), 일시정지하면 예약 해제", () => {
    let l = liveKick(liveReset("resume"));
    expect(l.playing).toBe(true);
    expect(l.sched?.ms).toBe(Math.round(LT[LRESUME].w * 1100));
    l = liveTogglePause(l);
    expect(l).toMatchObject({ paused: true, playing: false, sched: null });
    expect(liveStep(l)).toBe(l);
    expect(liveKick(l)).toBe(l);
    l = liveTogglePause(l);
    expect(l.sched).not.toBeNull();
    expect(LCUM[LT.length]).toBe(LTOTAL);
  });

  it("단계 하나를 끝내면 fresh 는 엔진이 알려준 대로", () => {
    const l = liveStep(liveKick(liveReset("resume")), { fresh: true });
    expect(l.fresh).toBe(LRESUME);
    expect(liveStep(liveKick(liveReset("resume")), { fresh: false }).fresh).toBe(-1);
  });
});

describe("스토어", () => {
  it("결정하면 대기 수가 줄고 레일 문구가 바뀐다", () => {
    const st = useWb.getState();
    expect(pending(st.S)).toBe(2);
    expect(pendingSub(st.S)).toBe("승인 1 · 새 경로 1");
    st.setAppr("ok");
    expect(pending(useWb.getState().S)).toBe(1);
    expect(railApi(useWb.getState().S)).toEqual({ dot: "run", sub: "점검 중 · 결제 흐름", wait: false });
    useWb.getState().setPath("ex");
    expect(pending(useWb.getState().S)).toBe(0);
    expect(pendingSub(useWb.getState().S)).toBe("모두 결정함");
  });

  it("처음 상태는 원본 init: 진행 점검 58%, 레일 새 점검 줄 숨김, 대상 shop.example.com", () => {
    const st = useWb.getState();
    expect(lpct(st.live)).toBe(58);
    expect(st.railNew).toBeNull();
    expect(curHost(st.target)).toBe("shop.example.com");
    expect(st.dSel).toBe("appr");
    expect(railShop(st.live)).toEqual({ dot: "run", sub: "점검 중 · 58%", pct: 58 });
  });

  it("새 점검 → 자산 탐색 진입 → 끝 → 범위 확인 → 점검 시작 → 진행 진입", () => {
    const st = useWb.getState();
    st.startScan("https://app.sample.io");
    expect(useWb.getState()).toMatchObject({ target: "https://app.sample.io", railNew: "crawl" });
    expect(useWb.getState().crawl.started).toBe(false);
    useWb.getState().crawlEnter();
    expect(useWb.getState().crawl.sched?.ms).toBe(300);
    useWb.getState().crawlNext();
    expect(useWb.getState().railNew).toBe("crawl"); // 끝나기 전에는 무시
    while (!useWb.getState().crawl.done) useWb.getState().crawlStep();
    useWb.getState().crawlNext();
    expect(useWb.getState().railNew).toBe("scope");
    useWb.getState().scopeStart();
    expect(useWb.getState()).toMatchObject({ railNew: null, liveFresh: true });
    const run = useWb.getState().live.run;
    useWb.getState().liveEnter(8 * 3600);
    const l = useWb.getState().live;
    expect(useWb.getState().liveFresh).toBe(false);
    expect(l).toMatchObject({ i: 0, start: 8 * 3600, playing: true, run: run + 1 });
    useWb.getState().setReportHost("live");
    expect(useWb.getState().report).toMatchObject({ kind: "live", host: "app.sample.io", ended: false, mins: 0 });
  });

  it("탐색 취소는 타이머를 지우고 다음 진입 때 처음부터", () => {
    useWb.getState().crawlEnter();
    useWb.getState().crawlStep();
    useWb.getState().crawlCancel();
    expect(useWb.getState().crawl).toMatchObject({ started: false, sched: null });
    expect(useWb.getState().railNew).toBeNull();
    useWb.getState().crawlEnter();
    expect(useWb.getState().crawl).toMatchObject({ started: true, i: 0 });
  });
});

describe("경로 트리", () => {
  it("같은 주소의 메서드는 한 마디에, {param} 조각은 뒤로", () => {
    const t = trie([
      { m: "GET", p: "/api/orders/{id}" },
      { m: "PATCH", p: "/api/orders/{id}" },
      { m: "GET", p: "/api/orders" },
      { m: "GET", p: "/api/cart" },
      { m: "GET", p: "/" },
    ]);
    expect(t.ms).toHaveLength(1);
    expect(under(t)).toBe(5);
    const api = t.kids.get("api");
    expect(api && sortedKids(api).map((k) => k.seg)).toEqual(["cart", "orders"]);
    const orders = api?.kids.get("orders");
    expect(orders && sortedKids(orders).map((k) => k.key)).toEqual(["/api/orders/{id}"]);
    expect(orders?.kids.get("{id}")?.ms).toHaveLength(2);
  });
});
