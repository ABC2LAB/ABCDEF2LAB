"use client";

// 자산 탐색 (live). 고른 점검의 찾은 경로(assets endpoint) · 요청 로그(traffic, 호스트 기준) · 일시정지/재개(control).
// 엔진에는 탐색 단계 진행률과 종료(취소)가 없어 '엔진 미지원'으로 둔다. 숨은 화면(route 아님)은 부르지 않는다.
// demo Crawl 과 같은 머리 · 가운데 · 오른쪽 패널 구조(묶음별 카드 · 주소 구조 · 같은 패널 안 API 상세).

import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";

import { ApiError } from "@/abc2lab-ui/lib/api";

import { cx } from "../../lib/cx";
import type { PKind } from "../../lib/kind";
import { sortedKids, type TrieNode, trie, under } from "../../lib/trie";
import { controlTask, fetchEndpoints, fetchTraffic, type TrafficRow } from "../../live/api";
import { ctlAction, type EpRow, epRow, fmtBytes, groupRows, KIND_LABEL, kindCounts } from "../../live/crawlScope";
import { reloadTasks, useSelectedTask } from "../../live/feeds";
import { clockOf, elapsed, fmtCount, fmtDur, hostOf, isActive, isUnsupported, pathOf, taskLabel } from "../../live/map";
import { maskSecrets } from "../../live/mask";
import { ErrorNote, EstimateTag, LoadingRow, ModeNote, TaskPicker, Unsupported, useNow } from "../../live/parts";
import { usePoll } from "../../live/usePoll";
import { useShell } from "../../shell";
import { Icon } from "../../ui/icons";
import { CloseInspButton, InspOpenButton, Pth } from "../../ui/parts";
import s from "../../workbench.module.css";
import { ApiDetailLive } from "./ApiDetailLive";
import { LiveHead, MissingTaskNote, TrafficOffNote } from "./crawlScopeParts";

/** 요청 로그 재조회 간격(ms) · 한 번에 받는 수. */
const TRAFFIC_MS = 4000;
const TRAFFIC_SIZE = 50;

// ---------- 요청 로그 ----------

/** 기록 한 줄: 시각 · 메서드 · 경로 · 상태 코드, 둘째 줄에 형식 · 크기(· 다른 호스트면 호스트). */
function TrafficLine({ r, host }: { r: TrafficRow; host: string }) {
  const meta = [
    r.host && r.host.toLowerCase() !== host.toLowerCase() ? r.host : "",
    r.content_type,
    fmtBytes(r.resp_len),
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <div className={s.lr}>
      <span className={s.t}>{clockOf(r.ts)}</span>
      <span className={cx(s.m, s[r.method])}>{r.method || "?"}</span>
      <span className={s.p}>{maskSecrets(pathOf(r.url))}</span>
      <span>
        {r.status ? (
          <span className={cx(s.sc, s[`s${String(r.status).charAt(0)}`])}>{r.status}</span>
        ) : (
          <span className={cx(s.tag, s.out)}>응답 없음</span>
        )}
      </span>
      {meta ? <span className={s.ln}>{meta}</span> : null}
    </div>
  );
}

// ---------- 찾은 경로 ----------

type View = "card" | "tree";
type Kf = "all" | "ap" | "ex";

const VIEW_OPTS: ReadonlyArray<readonly [View, string]> = [
  ["card", "묶음별"],
  ["tree", "주소 구조"],
];
const KF_OPTS: ReadonlyArray<readonly [Kf, string]> = [
  ["all", "전체"],
  ["ap", KIND_LABEL.ap],
  ["ex", KIND_LABEL.ex],
];
/** 알약 끝의 작은 글자 */
const PILL_FLAG: Record<PKind, string> = { get: "", login: "로그인", ap: "변경", ex: "제외 후보" };
/** 주소 구조 칩 클래스 (ex 는 demo 의 off 모양) */
const SMM_CLS: Record<PKind, string | undefined> = { get: undefined, login: s.login, ap: s.ap, ex: s.off };

const mShort = (m: string) => (m === "DELETE" ? "DEL" : m || "?");
const ariaOf = (r: EpRow, multi: boolean) =>
  `${r.m || "?"} ${r.p}${multi && r.ep.domain ? ` (${r.ep.domain})` : ""}, ${KIND_LABEL[r.k]}(추정), 상세 보기`;

/** 분류 비율 막대 칸들. */
function KBar({ c }: { c: Record<PKind, number> }) {
  return (
    <>
      {(["get", "login", "ap", "ex"] as const)
        .filter((k) => c[k])
        .map((k) => (
          <i key={k} className={s[`k-${k}`]} style={{ flex: c[k] }} />
        ))}
    </>
  );
}

/** 묶음 카드의 경로 알약. */
function Pill({ r, multi, onOpen }: { r: EpRow; multi: boolean; onOpen: (id: number) => void }) {
  const flag = PILL_FLAG[r.k];
  return (
    <button
      className={cx(s.pp, s[r.k])}
      type="button"
      data-api={r.id}
      aria-label={ariaOf(r, multi)}
      onClick={() => onOpen(r.id)}
    >
      <span className={cx(s.pm, s[r.m])}>{mShort(r.m)}</span>
      <span className={s.pt}>
        <Pth p={r.p} />
      </span>
      {flag ? <span className={s.pf}>{flag}</span> : null}
    </button>
  );
}

/** 묶음별 카드. 묶음 숫자는 거르기 전 전체 기준. */
function GroupCards({
  all,
  list,
  multi,
  onOpen,
}: {
  all: readonly EpRow[];
  list: readonly EpRow[];
  multi: boolean;
  onOpen: (id: number) => void;
}) {
  return (
    <div className={s.gcs}>
      {groupRows(list).map(({ g, rows }) => {
        const gAll = all.filter((r) => r.g === g);
        const c = kindCounts(gAll);
        return (
          <section key={g} className={s.gc} aria-label={`${g} ${gAll.length}개`}>
            <div className={s["gc-h"]}>
              <b>{g}</b>
              <span className={s.n}>{gAll.length}</span>
              <span className={s["gc-s"]}>
                {c.ap ? <span className={s.w}>{`${KIND_LABEL.ap} ${c.ap}`}</span> : null}
                {c.ex ? <span>{`${KIND_LABEL.ex} ${c.ex}`}</span> : null}
              </span>
            </div>
            <div className={s.kbar} aria-hidden="true">
              <KBar c={c} />
            </div>
            <div className={s.pills}>
              {rows.map((r) => (
                <Pill key={r.id} r={r} multi={multi} onOpen={onOpen} />
              ))}
            </div>
          </section>
        );
      })}
    </div>
  );
}

interface SmCtx {
  open: Record<string, boolean>;
  filtered: boolean;
  rootLabel: string;
  multi: boolean;
  onOpen: (id: number) => void;
  onToggle: (key: string, open: boolean) => void;
}

/** 주소 구조의 마디 하나 (자식까지 재귀). demo SmNode 와 같은 마크업. */
function SmNode({ n, isRoot, ctx }: { n: TrieNode<EpRow>; isRoot: boolean; ctx: SmCtx }) {
  const kids = sortedKids(n);
  const hasKids = kids.length > 0;
  // 거르는 중이면 모두 펼친다
  const open = ctx.open[n.key] !== false || ctx.filtered;
  const chips = [...n.ms].sort((a, b) => a.m.localeCompare(b.m) || a.id - b.id);
  return (
    <li>
      <div className={s["sm-row"]}>
        {hasKids && !isRoot ? (
          <button
            className={s["sm-t"]}
            type="button"
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
            ctx.rootLabel
          ) : (
            <>
              /<Pth p={n.seg} />
            </>
          )}
        </span>
        {hasKids && !open ? <span className={s["sm-c"]}>{under(n)}</span> : null}
        <span className={s["sm-ms"]}>
          {chips.map((r) => (
            <button
              key={r.id}
              className={cx(s.smm, s[r.m], SMM_CLS[r.k])}
              type="button"
              data-api={r.id}
              aria-label={ariaOf(r, ctx.multi)}
              onClick={() => ctx.onOpen(r.id)}
            >
              {mShort(r.m)}
            </button>
          ))}
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

// ---------- 화면 ----------

export function CrawlLive() {
  const { route, openInsp } = useShell();
  const visible = route === "crawl";
  const { taskId, task, missing } = useSelectedTask();
  const now = useNow(10_000);
  // 목록에서 점검을 확인한 뒤에만 부른다 (지워진 점검에 헛걸음하지 않게)
  const on = visible && !!task;
  const host = task ? hostOf(task) : null;

  // 엔드포인트는 진입 때 한 번 + 새로고침, 요청 로그는 보이는 동안 주기적으로
  const eps = usePoll(() => fetchEndpoints(taskId as string), [taskId], { enabled: on });
  const traffic = usePoll(() => fetchTraffic({ host: host as string, size: TRAFFIC_SIZE }), [taskId, host], {
    intervalMs: TRAFFIC_MS,
    enabled: on && !!host,
  });

  // 일시정지/재개
  const [busy, setBusy] = useState(false);
  const [ctlErr, setCtlErr] = useState<unknown>(null);
  const act = task ? ctlAction(task) : null;
  const ctlOff = isUnsupported(ctlErr) && !(ctlErr instanceof ApiError && ctlErr.status === 409);
  // biome-ignore lint/correctness/useExhaustiveDependencies: 점검이 바뀌면 지난 오류를 지운다
  useEffect(() => setCtlErr(null), [taskId]);
  const onCtl = async () => {
    if (!taskId || !act || busy) return;
    setBusy(true);
    setCtlErr(null);
    try {
      await controlTask(taskId, act);
    } catch (err) {
      setCtlErr(err);
    } finally {
      setBusy(false);
      reloadTasks();
    }
  };

  // 찾은 경로 패널 상태
  const [view, setView] = useState<View>("card");
  const [kf, setKf] = useState<Kf>("all");
  const [smOpen, setSmOpen] = useState<Record<string, boolean>>({});
  const [api, setApi] = useState<{ id: number; task: string } | null>(null);
  const rows = useMemo(() => (eps.data?.assets ?? []).map(epRow), [eps.data]);
  const apiRow = api && api.task === taskId ? (rows.find((r) => r.id === api.id) ?? null) : null;

  const viRef = useRef<HTMLElement>(null);
  const treeRef = useRef<HTMLDivElement>(null);
  const backRef = useRef<HTMLButtonElement>(null);
  const closedId = useRef<number | null>(null);
  const openApi = (id: number) => {
    if (!taskId) return;
    setApi({ id, task: taskId });
    openInsp();
  };
  const closeApi = () => {
    closedId.current = apiRow?.id ?? null;
    setApi(null);
  };
  // 열면 패널 맨 위 + '찾은 경로 목록' 버튼에 포커스, 닫으면 눌렀던 경로 버튼으로 (demo 와 같음)
  const apiId = apiRow?.id ?? null;
  useEffect(() => {
    if (apiId !== null) {
      if (viRef.current) viRef.current.scrollTop = 0;
      backRef.current?.focus();
      return;
    }
    const id = closedId.current;
    closedId.current = null;
    if (id !== null) treeRef.current?.querySelector<HTMLElement>(`[data-api="${id}"]`)?.focus();
  }, [apiId]);

  // ---- 동작 버튼 ----
  const paused = act === "resume";
  const acts = taskId ? (
    <>
      <button
        className={s.btn}
        type="button"
        aria-pressed={paused}
        disabled={!act || busy || ctlOff}
        title={act ? undefined : "점검 중이거나 일시정지된 점검만 바꿀 수 있습니다"}
        onClick={onCtl}
      >
        <Icon name={paused ? "play" : "pause"} />
        <span>{paused ? "다시 시작" : "일시정지"}</span>
      </button>
      <button className={s.btn} type="button" disabled title="엔진에 점검 종료 기능이 없습니다">
        점검 취소 <Unsupported chip />
      </button>
      <InspOpenButton>찾은 경로</InspOpenButton>
      <a className={cx(s.btn, s.pri)} href="#scope">
        범위 확인
        <Icon name="arrow" />
      </a>
    </>
  ) : null;

  if (!taskId || missing) {
    return (
      <>
        <LiveHead title="자산 탐색" phase="crawl" taskId={null} task={null} />
        <div className={s.vm}>
          {missing ? <MissingTaskNote /> : null}
          <TaskPicker />
        </div>
      </>
    );
  }

  // ---- 일시정지/재개 결과 ----
  let ctlNote: ReactNode = null;
  if (ctlErr instanceof ApiError && ctlErr.status === 409) {
    ctlNote = (
      <div className={cx(s.note, s.warn)} role="alert">
        <Icon name="alert" />
        <span>지금 상태에서는 바꿀 수 없습니다 (이미 끝났거나 상태가 바뀜). 목록을 새로 불렀습니다.</span>
      </div>
    );
  } else if (ctlOff) ctlNote = <ModeNote what="일시정지·재개" />;
  else if (ctlErr) ctlNote = <ErrorNote error={ctlErr} onRetry={onCtl} />;

  // ---- 숫자 ----
  const ec = kindCounts(rows);
  const tData = traffic.data;
  const showTraffic = !!tData && (tData.enabled || tData.total > 0);

  // ---- 요청 로그 ----
  let log: ReactNode;
  if (!host) log = <p className={s.empty}>점검 설명에서 대상 호스트를 찾지 못해 요청 로그를 거를 수 없습니다.</p>;
  else if (traffic.unsupported) log = <ModeNote what="요청 로그" />;
  else if (!tData && traffic.error) log = <ErrorNote error={traffic.error} onRetry={traffic.reload} />;
  else if (!tData) log = <LoadingRow />;
  else {
    const lines = tData.exchanges;
    log = (
      <>
        {tData.enabled ? null : <TrafficOffNote />}
        {lines.length ? (
          <div className={s.log} aria-live="off">
            <div className={s.lh}>
              <span>시각</span>
              <span>메서드</span>
              <span>경로</span>
              <span>결과</span>
            </div>
            {lines.map((r) => (
              <TrafficLine key={r.id} r={r} host={host} />
            ))}
          </div>
        ) : (
          <p className={s.empty}>이 호스트로 기록된 요청이 없습니다.</p>
        )}
      </>
    );
  }

  // ---- 찾은 경로 ----
  const list = rows.filter((r) => kf === "all" || r.k === kf);
  const multi = new Set(rows.map((r) => r.ep.domain).filter(Boolean)).size > 1;
  let tree: ReactNode;
  if (eps.unsupported) tree = <ModeNote what="찾은 경로" />;
  else if (!eps.data && eps.error) tree = <ErrorNote error={eps.error} onRetry={eps.reload} />;
  else if (!eps.data) tree = <LoadingRow />;
  else if (!rows.length) tree = <p className={s.empty}>아직 찾은 경로가 없습니다.</p>;
  else if (!list.length) tree = <p className={s.empty}>{`${KIND_LABEL[kf as PKind]}(추정) 경로가 없습니다.`}</p>;
  else if (view === "tree") {
    const ctx: SmCtx = {
      open: smOpen,
      filtered: kf !== "all",
      rootLabel: host ?? taskLabel(task ?? { id: taskId, name: "", description: "", goal: "" }),
      multi,
      onOpen: openApi,
      onToggle: (key, o) => setSmOpen((m) => ({ ...m, [key]: o })),
    };
    tree = (
      <div className={s.sm}>
        <ul className={s["sm-root"]}>
          <SmNode n={trie(list)} isRoot ctx={ctx} />
        </ul>
      </div>
    );
  } else tree = <GroupCards all={rows} list={list} multi={multi} onOpen={openApi} />;

  return (
    <>
      <LiveHead title="자산 탐색" phase="crawl" taskId={taskId} task={task}>
        {acts}
      </LiveHead>
      <div className={s.vb}>
        <div className={s.vm}>
          {ctlNote}
          <div className={s["crwl-top"]}>
            <Unsupported what="탐색 진행률(엔진에 탐색 단계 진행률이 없음)" />
            {task && now ? (
              <div className={s.pmeta}>
                <span>
                  경과 <b>{fmtDur(elapsed(task, now))}</b>
                </span>
                {isActive(task) ? (
                  <span>
                    진행 중 작업 <b>{task.in_flight}</b>
                  </span>
                ) : null}
              </div>
            ) : null}
          </div>
          {eps.data || showTraffic ? (
            <div className={s.cnts}>
              {eps.data ? (
                <div className={s.cn}>
                  <div className={s.v}>{fmtCount(eps.data.total)}</div>
                  <div className={s.k}>
                    {`찾은 엔드포인트 · ${KIND_LABEL.get} ${ec.get} · ${KIND_LABEL.ap} ${ec.ap} (추정)`}
                  </div>
                </div>
              ) : null}
              {showTraffic && tData ? (
                <div className={s.cn}>
                  <div className={s.v}>{fmtCount(tData.total)}</div>
                  <div className={s.k}>{`기록된 요청 · 호스트 기준${tData.enabled ? "" : " · 기록 꺼짐"}`}</div>
                </div>
              ) : null}
            </div>
          ) : null}
          <section className={s.sec} aria-labelledby="lcLogH">
            <div className={s.sh}>
              <h2 id="lcLogH">요청 로그</h2>
              <span className={s.aside}>
                {tData?.exchanges.length
                  ? `호스트 기준 · 최신 ${tData.exchanges.length}건 / ${fmtCount(tData.total)}건`
                  : "호스트 기준"}
              </span>
            </div>
            <p className={s.help}>
              트래픽 기록은 점검을 구분하지 않아 대상 호스트{host ? ` ${host}` : ""}(추정)가 들어간 요청을 최신순으로
              보입니다.
            </p>
            {log}
          </section>
        </div>
        <aside className={s.vi} aria-label="찾은 경로" ref={viRef}>
          {/* 목록은 API 상세를 여는 동안 숨기기만 한다 (보기·펼침 상태 유지) */}
          <div hidden={apiRow !== null}>
            <div className={s["vi-head"]}>
              <div>
                <div className={s.eyebrow}>엔진이 찾은 엔드포인트</div>
                <h2>
                  찾은 경로 <span className={s.num}>{eps.data ? fmtCount(eps.data.total) : ""}</span>
                </h2>
              </div>
              <CloseInspButton label="찾은 경로" />
            </div>
            <div style={{ display: "flex", flexDirection: "column", gap: 7, marginTop: 10 }}>
              <div className={s.kbar} aria-hidden="true">
                <KBar c={ec} />
              </div>
              <div className={s.kleg}>
                <span>
                  <i style={{ background: "var(--accent-line)" }} />
                  {`${KIND_LABEL.get} ${ec.get}`}
                </span>
                <span>
                  <i style={{ background: "var(--accent)" }} />
                  {`${KIND_LABEL.login} ${ec.login}`}
                </span>
                <span>
                  <i style={{ background: "var(--warn)" }} />
                  {`${KIND_LABEL.ap} ${ec.ap}`}
                </span>
                <span>
                  <i
                    className={s["k-ex"]}
                    style={{ background: "repeating-linear-gradient(135deg,var(--rule-2) 0 3px,transparent 3px 6px)" }}
                  />
                  {`${KIND_LABEL.ex} ${ec.ex}`}
                </span>
                <EstimateTag title="종류는 메서드·경로로 화면이 추정했습니다" />
              </div>
            </div>
            <div className={s.isec} style={{ gap: 12 }}>
              <div className={s.fctl}>
                <div className={s.seg} role="radiogroup" aria-label="보기 방식">
                  {VIEW_OPTS.map(([v, label]) => (
                    <label key={v}>
                      <input type="radio" name="lcView" value={v} checked={view === v} onChange={() => setView(v)} />
                      <span>{label}</span>
                    </label>
                  ))}
                </div>
                <div className={s.seg} role="radiogroup" aria-label="보이는 경로">
                  {KF_OPTS.map(([v, label]) => (
                    <label key={v}>
                      <input type="radio" name="lcKf" value={v} checked={kf === v} onChange={() => setKf(v)} />
                      <span>{label}</span>
                    </label>
                  ))}
                </div>
              </div>
              {eps.data?.truncated ? (
                <p className={s.help}>
                  {`서버 전체 ${fmtCount(eps.data.total)}개 중 ${fmtCount(eps.data.assets.length)}개만 받았습니다.`}
                </p>
              ) : null}
              <div ref={treeRef}>{tree}</div>
              <p className={s["tree-hint"]}>
                경로를 누르면 관찰한 사실과 기록된 요청·응답을 볼 수 있습니다. 묶음은 경로 첫 조각입니다.
              </p>
              <button className={cx(s.btn, s.sm)} type="button" onClick={eps.reload} disabled={eps.loading}>
                <Icon name="refresh" />
                새로고침
              </button>
            </div>
          </div>
          <div hidden={apiRow === null}>
            {apiRow ? (
              <>
                <button className={s.back} type="button" ref={backRef} onClick={closeApi}>
                  <Icon name="chev" />
                  찾은 경로 목록
                </button>
                <ApiDetailLive taskId={taskId} row={apiRow} host={host} px="lc" visible={visible} />
              </>
            ) : null}
          </div>
        </aside>
      </div>
    </>
  );
}
