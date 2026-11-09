"use client";

// 자산 탐색. 원본 crawlRender(진행 막대 · 숫자 · 상태 · 일시정지 · 범위 확인) + appendRow(탐색 기록) +
// renderTree(찾은 경로: 묶음별 카드 · 주소 구조) + cApiOpen/cApiClose(같은 패널에서 API 상세).
// 재생은 루트의 엔진이 스토어 crawl 을 한 단계씩 밀고, 이 화면은 그 상태를 그리기만 한다.

import { type ReactNode, useEffect, useLayoutEffect, useRef, useState } from "react";

import { AT, EV, type Ev, GL, GORD, type Group, MORD, START_SEC } from "../data/crawl";
import { epByKey } from "../data/scope";
import type { CrawlFilter, CrawlState, CrawlView, Found } from "../lib/crawlState";
import { crawlCounts, crawlElapsedSec, crawlLast, crawlPct, crawlQueue } from "../lib/crawlState";
import { cx } from "../lib/cx";
import { type Kind, kind, type PKind, pkind } from "../lib/kind";
import { hms } from "../lib/time";
import { sortedKids, type TrieNode, trie, under } from "../lib/trie";
import { useShell } from "../shell";
import { useCurHost, useWb } from "../store";
import { Icon } from "../ui/icons";
import { CloseInspButton, Ep, InspOpenButton, MenuButton, Phases, Pth } from "../ui/parts";
import s from "../workbench.module.css";
import { ApiDetail } from "./ApiDetail";

// ---------- 탐색 기록 ----------

/** 기록 한 줄. 원본 appendRow(e, i) 가 만들던 줄. */
function LogRow({ e, i, fresh, target }: { e: Ev; i: number; fresh: boolean; target: string }) {
  const t = hms(START_SEC + Math.round(AT[i] / 1000));
  if (e.k === "sys") {
    return (
      <div className={cx(s.lr, s.sys, fresh && s.fresh)}>
        <span className={s.t}>{t}</span>
        <span>{e.note || `${target} 연결 확인 · 탐색 시작`}</span>
      </div>
    );
  }
  let res: ReactNode;
  if (e.k === "skip") res = <span className={cx(s.tag, s.out)}>요청 안 함</span>;
  else if (e.k === "rec") {
    res =
      kind(e.m, e.p) === "ap" ? (
        <span className={cx(s.tag, s.ap)}>승인 후 전송</span>
      ) : (
        <span className={cx(s.tag, s.ex)}>제외 예정</span>
      );
  } else res = <span className={cx(s.sc, s[`s${String(e.st).charAt(0)}`])}>{e.st}</span>;
  return (
    <div className={cx(s.lr, e.k === "skip" && s.skip, fresh && s.fresh)}>
      <span className={s.t}>{t}</span>
      <span className={cx(s.m, s[e.m])}>{e.m}</span>
      <span className={s.p}>
        <Pth p={e.p} />
      </span>
      <span>{res}</span>
      <span className={s.ln}>{e.note}</span>
    </div>
  );
}

// ---------- 찾은 경로 ----------

const KORD: readonly PKind[] = ["get", "login", "ap", "ex"];

function kcount(fs: readonly Found[]): Record<PKind, number> {
  const c: Record<PKind, number> = { get: 0, login: 0, ap: 0, ex: 0 };
  for (const f of fs) c[pkind(f)]++;
  return c;
}

/** 분류 비율 막대의 칸들. 원본 kbarHtml(fs). */
function KBar({ fs }: { fs: readonly Found[] }) {
  const c = kcount(fs);
  return (
    <>
      {KORD.filter((k) => c[k]).map((k) => (
        <i key={k} className={s[`k-${k}`]} style={{ flex: c[k] }} />
      ))}
    </>
  );
}

const mIdx = (m: string) => MORD.indexOf(m as (typeof MORD)[number]);
const keyOf = (f: { m: string; p: string }) => `${f.m} ${f.p}`;
/** 같은 묶음 안: 메서드 순서, 그다음 경로 글자 순 (원본 sort) */
function byMethodPath(x: Found, y: Found): number {
  const d = mIdx(x.m) - mIdx(y.m);
  if (d) return d;
  if (x.p === y.p) return 0;
  return x.p < y.p ? -1 : 1;
}

/** 스크린리더용 꼬리말 (원본 aria-label 의 ', 승인 후 전송' · ', 제외'). 주소 구조 칩은 off·lock 을 제외로. */
const ARIA_FLAG: Record<Kind, string> = { get: "", login: "", ap: ", 승인 후 전송", off: ", 제외", lock: ", 제외" };
/** 알약 끝의 작은 글자 */
const PILL_FLAG: Record<PKind, string> = { get: "", login: "계정", ap: "승인", ex: "제외" };

/** 묶음 카드의 경로 알약. 원본 pill(f). */
function Pill({ f, reduce, onOpen }: { f: Found; reduce: boolean; onOpen: (key: string) => void }) {
  const k = pkind(f);
  const flag = PILL_FLAG[k];
  return (
    <button
      className={cx(s.pp, s[k], f.fresh && !reduce && s.fresh)}
      type="button"
      data-api={keyOf(f)}
      aria-label={`${keyOf(f)}${ARIA_FLAG[kind(f.m, f.p)]}, 요청·응답 예시 보기`}
      onClick={() => onOpen(keyOf(f))}
    >
      <span className={cx(s.pm, s[f.m])}>{f.m === "DELETE" ? "DEL" : f.m}</span>
      <span className={s.pt}>
        <Pth p={f.p} />
      </span>
      {flag ? <span className={s.pf}>{flag}</span> : null}
    </button>
  );
}

/** 묶음별 카드. 원본 renderTree 의 card 보기. */
function GroupCards({
  fs,
  list,
  kf,
  done,
  reduce,
  onOpen,
}: {
  fs: readonly Found[];
  list: readonly Found[];
  kf: CrawlFilter;
  done: boolean;
  reduce: boolean;
  onOpen: (key: string) => void;
}) {
  const empty: string[] = [];
  const cards = GORD.map((g: Group) => {
    const gs = list.filter((f) => f.g === g);
    if (!gs.length) {
      if (kf === "all") empty.push(GL[g]);
      return null;
    }
    // 묶음 숫자·승인·제외는 거르기 전 전체 기준
    const all = fs.filter((f) => f.g === g);
    const ap = all.filter((f) => pkind(f) === "ap").length;
    const ex = all.filter((f) => pkind(f) === "ex").length;
    return (
      <section key={g} className={s.gc} aria-label={`${GL[g]} ${all.length}개`}>
        <div className={s["gc-h"]}>
          <b>{GL[g]}</b>
          <span className={s.n}>{all.length}</span>
          <span className={s["gc-s"]}>
            {ap ? <span className={s.w}>승인 {ap}</span> : null}
            {ex ? <span>제외 {ex}</span> : null}
          </span>
        </div>
        <div className={s.kbar} aria-hidden="true">
          <KBar fs={all} />
        </div>
        <div className={s.pills}>
          {[...gs].sort(byMethodPath).map((f) => (
            <Pill key={keyOf(f)} f={f} reduce={reduce} onOpen={onOpen} />
          ))}
        </div>
      </section>
    );
  });
  return (
    <div className={s.gcs}>
      {cards}
      {empty.length && !done ? <p className={s["gc-empty"]}>아직 찾지 못한 묶음 · {empty.join(", ")}</p> : null}
    </div>
  );
}

interface SmCtx {
  smOpen: CrawlState["smOpen"];
  kf: CrawlFilter;
  host: string;
  reduce: boolean;
  onOpen: (key: string) => void;
  onToggle: (key: string, open: boolean) => void;
}

/** 주소 구조의 마디 하나 (자식까지 재귀). 원본 smNode(n, isRoot). */
function SmNode({ n, isRoot, ctx }: { n: TrieNode<Found>; isRoot: boolean; ctx: SmCtx }) {
  const kids = sortedKids(n);
  const hasKids = kids.length > 0;
  // 거르는 중이면 모두 펼쳐서 보여 준다
  const open = ctx.smOpen[n.key] !== false || ctx.kf !== "all";
  const chips = [...n.ms].sort((x, y) => mIdx(x.m) - mIdx(y.m));
  return (
    <li className={!ctx.reduce && n.ms.some((f) => f.fresh) ? s.fresh : undefined}>
      <div className={s["sm-row"]}>
        {hasKids && !isRoot ? (
          <button
            className={s["sm-t"]}
            type="button"
            data-smt={n.key}
            aria-expanded={open}
            aria-label={`${n.key} ${open ? "접기" : "펼치기"}`}
            onClick={() => ctx.onToggle(n.key, !open)}
          >
            <Icon name="chev" />
          </button>
        ) : (
          <span className={s["sm-sp"]} />
        )}
        <span className={s["sm-seg"]}>
          {isRoot ? (
            ctx.host
          ) : (
            <>
              /<Pth p={n.seg} />
            </>
          )}
        </span>
        {hasKids && !open ? <span className={s["sm-c"]}>{under(n)}</span> : null}
        <span className={s["sm-ms"]}>
          {chips.map((f) => {
            const k = kind(f.m, f.p);
            return (
              <button
                key={f.m}
                className={cx(s.smm, s[f.m], s[k])}
                type="button"
                data-api={keyOf(f)}
                aria-label={`${keyOf(f)}${ARIA_FLAG[k]}, 요청·응답 예시 보기`}
                onClick={() => ctx.onOpen(keyOf(f))}
              >
                {f.m === "DELETE" ? "DEL" : f.m}
              </button>
            );
          })}
        </span>
      </div>
      {hasKids && (open || isRoot) ? (
        <ul>
          {kids.map((k) => (
            <SmNode key={k.key} n={k} isRoot={false} ctx={ctx} />
          ))}
        </ul>
      ) : null}
    </li>
  );
}

const VIEW_OPTS: ReadonlyArray<readonly [CrawlView, string]> = [
  ["card", "묶음별"],
  ["tree", "주소 구조"],
];
const KF_OPTS: ReadonlyArray<readonly [CrawlFilter, string]> = [
  ["all", "전체"],
  ["ap", "승인 필요"],
  ["ex", "제외"],
];

// ---------- 화면 ----------

export function Crawl() {
  const { goTo, openInsp, reduce } = useShell();
  const host = useCurHost();
  const target = useWb((st) => st.target);
  const c = useWb((st) => st.crawl);
  const crawlTogglePause = useWb((st) => st.crawlTogglePause);
  const crawlCancel = useWb((st) => st.crawlCancel);
  const crawlNext = useWb((st) => st.crawlNext);
  const crawlSetView = useWb((st) => st.crawlSetView);
  const crawlSetFilter = useWb((st) => st.crawlSetFilter);
  const crawlSetSmOpen = useWb((st) => st.crawlSetSmOpen);

  // 열린 API 상세 (원본 cApiKey). 탐색을 처음부터 다시 하면(run 이 바뀌면) 닫힌다 (원본 crawlReset 의 cApiClose).
  const [api, setApi] = useState<{ key: string; run: number } | null>(null);
  const apiKey = api && api.run === c.run ? api.key : null;
  const apiEp = apiKey ? epByKey(apiKey) : undefined;

  const viRef = useRef<HTMLElement>(null);
  const treeRef = useRef<HTMLDivElement>(null);
  const backRef = useRef<HTMLButtonElement>(null);
  const closedKey = useRef<string | null>(null);

  const openApi = (key: string) => {
    if (!epByKey(key)) return;
    setApi({ key, run: c.run });
    openInsp();
  };
  const closeApi = () => {
    closedKey.current = apiKey;
    setApi(null);
  };

  // 열면 패널 맨 위로 + '찾은 경로 목록' 버튼에 포커스, 닫으면 눌렀던 경로 버튼으로 포커스 (원본 cApiOpen/cApiClose)
  useEffect(() => {
    if (apiKey) {
      if (viRef.current) viRef.current.scrollTop = 0;
      backRef.current?.focus();
      return;
    }
    const k = closedKey.current;
    closedKey.current = null;
    if (k) treeRef.current?.querySelector<HTMLElement>(`[data-api="${k.replace(/"/g, "")}"]`)?.focus();
  }, [apiKey]);

  // 기록이 늘 때 맨 아래 근처(48px)를 보고 있었으면 따라 내려간다 (원본 appendRow 의 near)
  const logRef = useRef<HTMLDivElement>(null);
  const near = useRef(true);
  const onLogScroll = () => {
    const l = logRef.current;
    if (l) near.current = l.scrollHeight - l.scrollTop - l.clientHeight < 48;
  };
  useLayoutEffect(() => {
    const l = logRef.current;
    if (!l) return;
    if (c.i === 0) {
      near.current = true;
      return;
    }
    if (near.current) l.scrollTop = l.scrollHeight;
  }, [c.i]);

  const pct = crawlPct(c);
  const last = crawlLast(c);
  const q = crawlQueue(c);
  const { get, ap } = crawlCounts(c);
  const fs = c.found;
  const kc = kcount(fs);
  const list = fs.filter((f) => c.kf === "all" || c.kf === pkind(f));

  let stateCls: string | undefined = s.run;
  let stateTxt = "탐색 중";
  if (c.done) {
    stateCls = s.wait;
    stateTxt = "탐색 완료 · 확인 대기";
  } else if (c.paused) {
    stateCls = undefined;
    stateTxt = "일시정지됨";
  }

  let nowEl: ReactNode = "대상 연결 확인 중";
  if (c.done) nowEl = "새 경로가 더 나오지 않아 탐색을 마쳤습니다";
  else if (last?.p && last.m) nowEl = <Ep m={last.m} p={last.p} />;

  let tree: ReactNode;
  if (!fs.length) tree = <p className={s.empty}>아직 찾은 경로가 없습니다.</p>;
  else if (!list.length) {
    tree = (
      <p className={s.empty}>
        {c.kf === "ap" ? "승인이 필요한 경로가 아직 없습니다." : "제외한 경로가 아직 없습니다."}
      </p>
    );
  } else if (c.view === "tree") {
    const ctx: SmCtx = { smOpen: c.smOpen, kf: c.kf, host, reduce, onOpen: openApi, onToggle: crawlSetSmOpen };
    tree = (
      <div className={s.sm}>
        <ul className={s["sm-root"]}>
          <SmNode n={trie(list)} isRoot ctx={ctx} />
        </ul>
      </div>
    );
  } else {
    tree = <GroupCards fs={fs} list={list} kf={c.kf} done={c.done} reduce={reduce} onOpen={openApi} />;
  }

  return (
    <>
      <header className={cx(s.vh, s["has-ph"])}>
        <div className={s["vh-top"]}>
          <MenuButton />
          <nav className={s.crumb} aria-label="위치">
            <a href="#dashboard">작업대</a>
            <span className={s.sep}>/</span>
            <span className={cx(s.host, s["t-host"])}>{host}</span>
            <span className={s.sep}>/</span>
            <span>자산 탐색</span>
          </nav>
        </div>
        <div className={s["vh-row"]}>
          <div className={s["vh-title"]}>
            <h1 tabIndex={-1}>자산 탐색</h1>
            <span className={cx(s.pill, stateCls)} id="cState">
              {stateTxt}
            </span>
          </div>
          <div className={s["vh-acts"]}>
            <button
              className={s.btn}
              type="button"
              id="cPause"
              aria-pressed={c.paused}
              hidden={c.done}
              onClick={crawlTogglePause}
            >
              <Icon name={c.paused ? "play" : "pause"} />
              <span>{c.paused ? "다시 시작" : "일시정지"}</span>
            </button>
            <button
              className={s.btn}
              type="button"
              id="cCancel"
              onClick={() => {
                crawlCancel();
                goTo("dashboard");
              }}
            >
              점검 취소
            </button>
            <InspOpenButton>찾은 경로</InspOpenButton>
            {/* 탐색 중에는 aria-disabled="true" 라 루트가 이동을 막는다. 끝났으면 레일 줄을 '범위 확인 대기'로 (원본 cNext) */}
            {/* biome-ignore lint/a11y/useValidAnchor: 해시 라우팅 링크라 이동은 href 가 하고, onClick 은 레일 단계만 바꾼다 */}
            <a
              className={cx(s.btn, s.pri)}
              href="#scope"
              id="cNext"
              aria-disabled={c.done ? "false" : "true"}
              onClick={crawlNext}
            >
              범위 확인
              <Icon name="arrow" />
            </a>
          </div>
        </div>
        <Phases phase="crawl" />
      </header>
      <div className={s.vb}>
        <div className={s.vm}>
          <div className={s["crwl-top"]}>
            <div className={s.now}>
              <span className={s.k}>지금 보는 곳</span>
              <span id="cNow">{nowEl}</span>
            </div>
            <div
              className={s.prog}
              role="progressbar"
              aria-label="탐색 진행"
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={pct}
              id="cBar"
            >
              <i style={{ width: `${pct}%` }} />
            </div>
            <div className={s.pmeta}>
              <span>
                방문 <b id="cVis">{c.sent}</b> / 알려진 링크 <b id="cKnown">{c.sent + q}</b>
              </span>
              <span>
                방문 대기 <b id="cQueue">{q}</b>
              </span>
              <span>
                경과 <b id="cEl">{crawlElapsedSec(c)}초</b>
              </span>
            </div>
          </div>
          <div className={s.cnts}>
            <div className={s.cn}>
              <div className={s.v} id="cFound">
                {fs.length}
              </div>
              <div className={s.k}>
                찾은 경로 ·{" "}
                <span id="cFoundSub">
                  조회 {get} · 데이터 변경 {fs.length - get}
                </span>
              </div>
            </div>
            <div className={s.cn}>
              <div className={s.v} id="cSent">
                {c.sent}
              </div>
              <div className={s.k}>보낸 요청 · 조회만</div>
            </div>
            <div className={cx(s.cn, ap > 0 && s.warn)} id="cApTile">
              <div className={s.v} id="cAp">
                {ap}
              </div>
              <div className={s.k}>승인 후 전송 · 보내기 전에 승인</div>
            </div>
            <div className={s.cn}>
              <div className={s.v} id="cSkip">
                {c.skip}
              </div>
              <div className={s.k}>요청 안 함 · 규칙으로 막음</div>
            </div>
          </div>
          <div className={s.note}>
            <Icon name="info" />
            <div>
              탐색에서는 보기만 하는 <b>GET 요청만</b> 보냅니다. 데이터를 바꾸는 경로(POST·PUT·PATCH·DELETE)는 찾아서
              기록만 하고, 범위 확인 뒤 승인을 받아 보냅니다. 로그인은 입력한 테스트 계정으로만 합니다.
            </div>
          </div>
          <section className={s.sec} aria-labelledby="cLogH">
            <div className={s.sh}>
              <h2 id="cLogH">탐색 기록</h2>
              <div className={s.legend}>
                <span>
                  <span className={cx(s.sc, s.s2)}>200</span>보낸 요청
                </span>
                <span>
                  <span className={cx(s.tag, s.ap)}>승인 후 전송</span>보내기 전 승인
                </span>
                <span>
                  <span className={cx(s.tag, s.ex)}>제외 예정</span>보내지 않음
                </span>
                <span>
                  <span className={cx(s.tag, s.out)}>요청 안 함</span>규칙으로 막음
                </span>
              </div>
            </div>
            <div className={s.log} id="cLog" aria-live="off" ref={logRef} onScroll={onLogScroll}>
              <div className={s.lh}>
                <span>시각</span>
                <span>메서드</span>
                <span>경로</span>
                <span>결과</span>
              </div>
              {EV.slice(0, c.i).map((e, i) => (
                // biome-ignore lint/suspicious/noArrayIndexKey: 기록은 회차(run) 안에서 덧붙기만 해서 위치가 곧 정체성
                <LogRow key={`${c.run}-${i}`} e={e} i={i} fresh={c.rowFresh[i] === true} target={target} />
              ))}
            </div>
          </section>
        </div>
        <aside className={s.vi} aria-label="찾은 경로" ref={viRef}>
          {/* 목록은 API 상세를 여는 동안 숨기기만 한다 (펼침·보기 상태 유지) */}
          <div id="cTreeWrap" hidden={apiKey !== null}>
            <div className={s["vi-head"]}>
              <div>
                <div className={s.eyebrow}>탐색하는 대로 채워짐</div>
                <h2>
                  찾은 경로{" "}
                  <span className={s.num} id="tCount">
                    {fs.length}
                  </span>
                </h2>
              </div>
              <CloseInspButton label="찾은 경로" />
            </div>
            <div style={{ display: "flex", flexDirection: "column", gap: 7, marginTop: 10 }}>
              <div className={s.kbar} id="cKbar" aria-hidden="true">
                <KBar fs={fs} />
              </div>
              <div className={s.kleg} id="cStat">
                <span>
                  <i style={{ background: "var(--accent-line)" }} />
                  자동 포함 {kc.get}
                </span>
                <span>
                  <i style={{ background: "var(--accent)" }} />
                  로그인 {kc.login}
                </span>
                <span>
                  <i style={{ background: "var(--warn)" }} />
                  승인 후 전송 {kc.ap}
                </span>
                <span>
                  <i
                    className={s["k-ex"]}
                    style={{ background: "repeating-linear-gradient(135deg,var(--rule-2) 0 3px,transparent 3px 6px)" }}
                  />
                  제외 {kc.ex}
                </span>
              </div>
            </div>
            <div className={s.isec} style={{ gap: 12 }}>
              <div className={s.fctl}>
                <div className={s.seg} role="radiogroup" aria-label="보기 방식">
                  {VIEW_OPTS.map(([v, label]) => (
                    <label key={v}>
                      <input
                        type="radio"
                        name="cView"
                        value={v}
                        checked={c.view === v}
                        onChange={() => crawlSetView(v)}
                      />
                      <span>{label}</span>
                    </label>
                  ))}
                </div>
                <div className={s.seg} role="radiogroup" aria-label="보이는 경로">
                  {KF_OPTS.map(([v, label]) => (
                    <label key={v}>
                      <input
                        type="radio"
                        name="cKf"
                        value={v}
                        checked={c.kf === v}
                        onChange={() => crawlSetFilter(v)}
                      />
                      <span>{label}</span>
                    </label>
                  ))}
                </div>
              </div>
              <div id="cTree" ref={treeRef}>
                {tree}
              </div>
              <p className={s["tree-hint"]}>
                경로를 누르면 요청·응답 예시를 볼 수 있습니다. 묶음 이름은 AI 추정입니다.
              </p>
            </div>
          </div>
          <div id="cApi" hidden={apiKey === null}>
            {apiEp ? (
              <>
                <button className={s.back} type="button" data-capi-back="" ref={backRef} onClick={closeApi}>
                  <Icon name="chev" />
                  찾은 경로 목록
                </button>
                <ApiDetail e={apiEp} px="c" />
              </>
            ) : null}
          </div>
        </aside>
      </div>
    </>
  );
}
