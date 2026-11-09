"use client";

// 점검 진행 화면. 원본 liveRender (진행률 · 지금 하는 일 · 계획 · 지난 활동 · 마침 안내 · 상태/일시정지/종료 버튼),
// renderLiveDecide (/admin/export 결정), 종료 확인 대화상자, renderLiveInsp (발견 패널: 목록 ↔ 상세) 를 옮긴 것.
// 재생은 루트의 엔진이 스토어(live)를 한 단계씩 바꾸고, 이 화면은 그 상태에서 그려지기만 한다.

import { type ReactNode, useRef, useState } from "react";

import { flushSync } from "react-dom";

import { type FindingId, findingById, type SevKey } from "../data/findings";
import { LCUM, LT, LTOTAL, PLAN } from "../data/live";
import { cx } from "../lib/cx";
import { lcur, lelapsed, lpct, lremain, vcount, vcounts } from "../lib/liveState";
import { hms } from "../lib/time";
import { useShell } from "../shell";
import { useCurHost, useWb } from "../store";
import { Icon } from "../ui/icons";
import { InspHead, InspOpenButton, Isec, MenuButton, On, Phases, ReportLink, Sev, VerdictLive } from "../ui/parts";
import s from "../workbench.module.css";
import { FindingDetail } from "./FindingDetail";

/** 지난 활동은 최근 이만큼만 보이고 나머지는 '더 보기' */
const FEED_N = 5;
const SEVS: readonly SevKey[] = ["crit", "hi", "mid", "lo"];

/** 계획 단계 j 의 마지막 작업 위치 (없으면 -1). 원본 liveRender 의 last. */
const PLAN_LAST = PLAN.map((_, j) => LT.reduce((last, t, k) => (t.p === j ? k : last), -1));

/** 점검 중 새로 찾은 관리자 경로 결정. 원본 `renderLiveDecide()`. */
function LiveDecide() {
  const path = useWb((st) => st.S.path);
  const fin = useWb((st) => st.live.done || st.live.ended);
  const decide = useWb((st) => st.live.decide);
  const setPath = useWb((st) => st.setPath);
  const ep = (
    <>
      <span className={cx(s.m, s.GET)}>GET</span>
      <span className={s.p}>/admin/export</span>
    </>
  );
  let body: ReactNode;
  if (!path && fin) {
    body = (
      <div className={s.dm}>
        <b>
          <span className={s.pill}>결정 없음</span>
          {ep}
        </b>
        <span>점검이 끝날 때까지 포함 여부를 정하지 않아 보내지 않았습니다. 이 경로는 미검사로 남깁니다.</span>
      </div>
    );
  } else if (!path) {
    body = (
      <>
        <div className={s.dm}>
          <b>
            <span className={cx(s.pill, s.wait)}>확인 필요</span>
            {ep}
          </b>
          <span>
            점검 중 새로 찾은 관리자 경로입니다. 관리자 메뉴 스크립트에서 찾았고, 처음 보는 권한 영역이라 자동으로
            처리하지 않았습니다. 그동안 다른 계획은 계속 진행합니다.
          </span>
        </div>
        <div className={s.acts}>
          <button className={cx(s.btn, s.pri, s.sm)} type="button" data-pathd="in" onClick={() => setPath("in")}>
            점검에 포함
          </button>
          <button className={cx(s.btn, s.sm)} type="button" data-pathd="ex" onClick={() => setPath("ex")}>
            제외
          </button>
        </div>
      </>
    );
  } else {
    body = (
      <div className={s.dm}>
        <b>
          {path === "in" ? <span className={cx(s.pill, s.ok)}>포함함</span> : <span className={s.pill}>제외함</span>}
          {ep}
        </b>
        <span>
          {path === "in" ? "계정 A, 계정 B, 비로그인으로 조회 요청만 보내 확인합니다." : "보내지 않고 기록만 남깁니다."}{" "}
          결정은 감사 로그에 남았습니다.
        </span>
      </div>
    );
  }
  return (
    <div className={cx(s.decide, (path || fin) && s.done)} id="lDecide" hidden={!decide}>
      {body}
    </div>
  );
}

/**
 * 진행 화면 오른쪽 발견 패널. 원본 `renderLiveInsp()`: 고른 발견이 없으면 요약·목록, 있으면 '발견 목록' 버튼 + 상세.
 * 고른 발견은 재시작(live.run)마다 지운다 (원본 liveReset 의 lSel=null).
 */
function LiveInsp() {
  const l = useWb((st) => st.live);
  const path = useWb((st) => st.S.path);
  const host = useCurHost();
  const { openInsp } = useShell();
  const [picked, setPicked] = useState<{ id: FindingId; run: number } | null>(null);
  const boxRef = useRef<HTMLElement>(null);
  const backRef = useRef<HTMLButtonElement>(null);
  const lSel = picked && picked.run === l.run ? picked.id : null;

  const pick = (id: FindingId) => {
    flushSync(() => setPicked({ id, run: l.run }));
    if (boxRef.current) boxRef.current.scrollTop = 0;
    backRef.current?.focus();
    openInsp();
  };
  const back = () => {
    const was = lSel;
    flushSync(() => setPicked(null));
    boxRef.current?.querySelector<HTMLElement>(`[data-lsel="${was}"]`)?.focus();
  };

  const one = lSel ? findingById(lSel) : undefined;
  if (one) {
    return (
      <aside className={s.vi} id="lInsp" aria-label="발견" ref={boxRef}>
        <button className={s.back} type="button" data-lback="" ref={backRef} onClick={back}>
          <Icon name="chev" />
          발견 목록
        </button>
        <FindingDetail f={one} host={host} ctx="live" vdNow={l.fv[one.id]} />
      </aside>
    );
  }

  const fs = l.forder.flatMap((id) => findingById(id) ?? []);
  const c: Record<SevKey, number> = { crit: 0, hi: 0, mid: 0, lo: 0 };
  for (const f of fs) c[f.sev]++;
  let phase = "점검 중";
  if (l.done) phase = "점검 완료";
  else if (l.ended) phase = "종료됨";
  const chk = l.done ? 43 : Math.round((43 * LCUM[l.i]) / LTOTAL);
  return (
    <aside className={s.vi} id="lInsp" aria-label="발견" ref={boxRef}>
      <InspHead
        eyebrow={
          <>
            {phase} · <span className={s.host}>{host}</span>
          </>
        }
        title={`발견 ${fs.length}건`}
      />
      {fs.length ? (
        <>
          <div className={s.stack} style={{ height: 8, marginTop: 12 }} aria-hidden="true">
            {SEVS.filter((k) => c[k]).map((k) => (
              <i key={k} style={{ flex: c[k], background: `var(--${k})` }} />
            ))}
          </div>
          <div className={s.vbar} style={{ marginTop: 8 }}>
            <span>
              심각 <b>{c.crit}</b>
            </span>
            <span>
              높음 <b>{c.hi}</b>
            </span>
            <span>
              중간 <b>{c.mid}</b>
            </span>
            <span>
              낮음 <b>{c.lo}</b>
            </span>
          </div>
          <Isec t="판정">
            <div className={s.vbar}>
              {vcounts(l).map(([k, n]) => (
                <span key={k}>
                  {k} <b>{n}</b>
                </span>
              ))}
            </div>
            <p className={s.help}>증거가 충분할 때만 확정하고, 부족하면 판단 불가로 나눕니다.</p>
          </Isec>
          <Isec t="발견 · 최근 순">
            <div className={s.flist}>
              {[...fs].reverse().map((f) => (
                <button key={f.id} className={s.fli} type="button" data-lsel={f.id} onClick={() => pick(f.id)}>
                  <Sev k={f.sev} />
                  <span>{f.title}</span>
                  <VerdictLive v={l.fv[f.id] ?? "판정 중"} />
                </button>
              ))}
            </div>
          </Isec>
        </>
      ) : (
        <Isec>
          <p className={s.empty}>아직 발견이 없습니다. 찾으면 여기에 바로 나타납니다.</p>
        </Isec>
      )}
      <Isec t="커버리지">
        <div className={s.prog} aria-hidden="true">
          <i style={{ width: `${Math.round((chk / l.ep) * 100)}%` }} />
        </div>
        <div className={s.vbar}>
          <span>
            검사함 <b>{chk}</b> / {l.ep} 엔드포인트
          </span>
          <span>
            새로 찾은 경로 <b>{l.ep - 37}</b>
          </span>
        </div>
        <p className={s.help}>미검사 항목은 안전을 뜻하지 않습니다.</p>
      </Isec>
      {l.decide && !path ? (
        <Isec t="결정 대기">
          <p>
            새로 찾은 관리자 경로 <code>/admin/export</code>를 포함할지 정해 주세요. 가운데 화면 위쪽에서 결정할 수
            있습니다.
          </p>
        </Isec>
      ) : null}
    </aside>
  );
}

export function Live() {
  const l = useWb((st) => st.live);
  const host = useCurHost();
  const [confirmOpen, setConfirmOpen] = useState(false);
  const h1Ref = useRef<HTMLHeadingElement>(null);
  const endRef = useRef<HTMLButtonElement>(null);
  const yesRef = useRef<HTMLButtonElement>(null);
  const feedRef = useRef<HTMLUListElement>(null);

  const pct = lpct(l);
  const c = lcur(l);
  const fin = l.done || l.ended;

  // 종료 확인: 열면 '종료'(확정)에, 닫으면 머리의 '종료'에, 확정하면 h1 에 포커스 (원본 lEnd · lEndNo · lEndYes)
  const openConfirm = () => {
    flushSync(() => setConfirmOpen(true));
    yesRef.current?.focus();
  };
  const cancelConfirm = () => {
    flushSync(() => setConfirmOpen(false));
    endRef.current?.focus();
  };
  const endYes = () => {
    flushSync(() => {
      useWb.getState().liveEnd();
      setConfirmOpen(false);
    });
    h1Ref.current?.focus();
  };
  const feedMore = () => {
    flushSync(() => useWb.getState().liveToggleFeedAll());
    if (useWb.getState().live.feedAll && feedRef.current) feedRef.current.scrollTop = 0;
  };

  // 지금 하는 일 (원본 lNowK · lTask)
  let nowK: ReactNode;
  let task: ReactNode;
  let nowCls: string;
  if (c) {
    nowCls = cx(s["now-k"], l.paused && s.idle);
    nowK = (
      <>
        <i />
        {l.paused ? "멈춤" : "지금 하는 일"}
      </>
    );
    task = (
      <>
        {c.t}
        {c.on ? (
          <>
            {" · "}
            <On on={c.on} />
          </>
        ) : null}
      </>
    );
  } else if (l.done) {
    nowCls = cx(s["now-k"], s.ok);
    nowK = (
      <>
        <Icon name="check" />
        완료
      </>
    );
    task = "계획한 작업을 모두 마쳤습니다";
  } else {
    nowCls = cx(s["now-k"], s.idle);
    nowK = (
      <>
        <i />
        종료됨
      </>
    );
    task = "새 요청을 보내지 않습니다. 끝내지 못한 항목은 미검사로 남깁니다";
  }

  // 상태 알약 (원본 lState)
  let pill = { cls: cx(s.pill, s.run), label: "진행 중" };
  if (l.done) pill = { cls: cx(s.pill, s.ok), label: "점검 완료" };
  else if (l.ended) pill = { cls: s.pill, label: "종료됨" };
  else if (l.paused) pill = { cls: s.pill, label: "일시정지됨" };

  // 계획 (원본 lPlan): 마지막 작업까지 끝난 단계는 done, 지금 작업의 단계는 run, 종료 뒤 남은 단계는 미검사
  const curP = c ? c.p : -1;
  let doneN = 0;
  const plan = PLAN.map((label, j) => {
    const last = PLAN_LAST[j];
    const done = l.done || j === 0 || (last >= 0 && last < l.i);
    const run = !done && j === curP;
    if (done) doneN++;
    let tail = "";
    if (run) tail = l.paused ? " · 멈춤" : " · 진행 중";
    else if (!done && l.ended) tail = " · 미검사";
    return (
      <li key={label} className={done ? s.done : cx(run && !l.paused && s.run)}>
        <span className={s.st}>{done ? <Icon name="check" /> : null}</span>
        <span>
          {label}
          {tail}
        </span>
      </li>
    );
  });

  const shown = l.feedAll ? l.feed : l.feed.slice(0, FEED_N);

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
            <span>점검 진행</span>
          </nav>
        </div>
        <div className={s["vh-row"]}>
          <div className={s["vh-title"]}>
            <h1 tabIndex={-1} ref={h1Ref}>
              점검 진행
            </h1>
            <span className={pill.cls} id="lState">
              {pill.label}
            </span>
          </div>
          <div className={s["vh-acts"]}>
            <button
              className={s.btn}
              type="button"
              id="lPause"
              aria-pressed={l.paused}
              hidden={fin}
              onClick={() => useWb.getState().liveTogglePause()}
            >
              {l.paused ? (
                <>
                  <Icon name="play" />
                  <span>다시 시작</span>
                </>
              ) : (
                <>
                  <Icon name="pause" />
                  <span>일시정지</span>
                </>
              )}
            </button>
            <button
              className={cx(s.btn, s.danger)}
              type="button"
              id="lEnd"
              hidden={fin}
              ref={endRef}
              onClick={openConfirm}
            >
              <Icon name="stop" />
              종료
            </button>
            <InspOpenButton id="lInspBtn">{`발견 ${l.forder.length}`}</InspOpenButton>
          </div>
        </div>
        <Phases phase="live" />
      </header>
      <div className={s.vb}>
        <div className={s.vm}>
          <div className={s.confirm} id="lConfirm" hidden={!confirmOpen} role="alertdialog" aria-labelledby="lConfirmH">
            <h2 id="lConfirmH">점검을 종료할까요? 종료하면 다시 시작할 수 없습니다.</h2>
            <ul>
              <li>새 요청을 보내지 않고, 응답을 받지 못한 요청은 전송 불명으로 기록합니다.</li>
              <li>모은 증거와 발견은 남기고, 종료 시점까지의 결과로 보고서를 만듭니다.</li>
              <li>끝내지 못한 항목은 미검사로 표시합니다. 나중에 이어 하려면 일시정지를 쓰세요.</li>
            </ul>
            <div className={s.acts}>
              <button className={cx(s.btn, s.danger)} type="button" id="lEndYes" ref={yesRef} onClick={endYes}>
                종료
              </button>
              <button className={s.btn} type="button" id="lEndNo" onClick={cancelConfirm}>
                계속 점검
              </button>
            </div>
          </div>
          <div className={s.band}>
            <div className={s.pct} id="lPct">
              {pct}
              <small>%</small>
            </div>
            <div className={s.bt}>
              <div
                className={s.prog}
                id="lBar"
                role="progressbar"
                aria-label="점검 진행률"
                aria-valuemin={0}
                aria-valuemax={100}
                aria-valuenow={pct}
              >
                <i style={{ width: `${pct}%` }} />
              </div>
              <div className={s["now-l"]} aria-live="polite">
                <span className={nowCls} id="lNowK">
                  {nowK}
                </span>
                <span id="lTask">{task}</span>
              </div>
              <div className={s.pmeta} id="lMeta">
                <span>
                  시작 <b>{hms(l.start).slice(0, 5)}</b>
                </span>
                <span>
                  경과 <b>{lelapsed(l)}분</b>
                </span>
                {c ? (
                  <span>
                    남은 시간 <b>약 {lremain(l)}분</b>
                  </span>
                ) : null}
                <span>
                  토큰 <b>{((2.0 * LCUM[l.i]) / LTOTAL).toFixed(1)}M</b> / 예상 2.0M
                </span>
                <span>
                  엔드포인트 <b>{l.ep}</b> (처음 37 · 새로 {l.ep - 37})
                </span>
              </div>
            </div>
          </div>
          <div className={s.ldone} id="lDone" hidden={!fin}>
            <div className={s.dm}>
              <b>
                <Icon name="check" />
                {/* 원본은 끝났을 때만 글을 바꾼다 (숨겨진 동안은 처음 글 그대로) */}
                <span id="lDoneT">{l.ended && !l.done ? "점검을 종료했습니다" : "점검을 마쳤습니다"}</span>
              </b>
              <span id="lDoneS">
                {fin
                  ? `발견 ${l.forder.length}건 · ${vcount(l)}${l.done ? " · 보고서가 준비됐습니다" : " · 종료 시점까지의 결과로 보고서를 만듭니다"}`
                  : null}
              </span>
            </div>
            <ReportLink kind="live" className={cx(s.btn, s.pri)}>
              보고서 보기
              <Icon name="arrow" />
            </ReportLink>
          </div>
          <LiveDecide />
          <section className={s.sec} aria-labelledby="lSendH">
            <div className={s.sh}>
              <h2 id="lSendH">전송 통제</h2>
              <span className={s.aside}>범위 밖으로 보낸 요청 0 · 승인 없이 보낸 상태 변경 0</span>
            </div>
            <div className={s.meters}>
              <div className={s.cell}>
                <div className={s.k}>허용 범위 안 전송</div>
                <div className={s.v} id="mSent">
                  {l.sent.toLocaleString("en-US")}
                </div>
                <div className={s.sub}>허용 목록 주소만</div>
              </div>
              <div className={s.cell}>
                <div className={s.k}>막은 요청</div>
                <div className={s.v} id="mBlk">
                  {l.blk}
                </div>
                <div className={s.sub}>범위 밖 · 안전 규칙</div>
              </div>
              <div className={s.cell}>
                <div className={s.k}>승인 필요로 보류</div>
                <div className={s.v} id="mHold">
                  {l.hold}
                </div>
                <div className={s.sub}>데이터를 바꾸는 요청</div>
              </div>
              <div className={s.cell}>
                <div className={s.k}>전송 불명</div>
                <div className={s.v}>0</div>
                <div className={s.sub}>응답 없음</div>
              </div>
            </div>
            <p className={s.help}>
              전송 통제를 거친 HTTP 요청만 셉니다. 셸·브라우저·MCP 같은 다른 도구의 통신은 들어가지 않습니다.
            </p>
          </section>
          <div className={s["two-col"]}>
            <section className={s.sec} aria-labelledby="lPlanH">
              <div className={s.sh}>
                <h2 id="lPlanH">점검 계획</h2>
                <span className={s.n} id="lPlanN">
                  {`${doneN}/${PLAN.length} 완료`}
                </span>
              </div>
              <ol className={s.plan} id="lPlan">
                {plan}
              </ol>
              <p className={s.help}>
                계획은 플래너가 만들고 워커가 실행합니다. 같은 자원·조건의 중복 요청은 한 번만 보냅니다.
              </p>
            </section>
            <section className={s.sec} aria-labelledby="lFeedH">
              <div className={s.sh}>
                <h2 id="lFeedH">
                  지난 활동{" "}
                  <span className={s.n} id="lFeedN">
                    {`${l.feed.length}건`}
                  </span>
                </h2>
                <div className={s.legend}>
                  <span>
                    <span className={cx(s.fd, s.ok, s.dot)} />
                    정상 확인
                  </span>
                  <span>
                    <span className={cx(s.fd, s.col, s.dot)} />
                    경로 수집
                  </span>
                  <span>
                    <span className={cx(s.fd, s.blk, s.dot)} />
                    막은 시도
                  </span>
                  <span>
                    <span className={cx(s.fd, s.hit, s.dot)} />
                    발견
                  </span>
                </div>
              </div>
              <ul className={cx(s.feed, l.feedAll && l.feed.length > FEED_N && s.scroll)} id="lFeed" ref={feedRef}>
                {shown.length ? (
                  shown.map((x) => (
                    <li key={x.k} className={cx(x.k === l.fresh && s.fresh) || undefined}>
                      <span className={s.t}>{x.at}</span>
                      <span className={cx(s.fd, s[x.t.r])} />
                      <span>
                        {x.t.t}
                        {x.t.on ? (
                          <>
                            {" "}
                            <span className={s.p}>{x.t.on}</span>
                          </>
                        ) : null}
                        <span className={s.res}>{x.t.res}</span>
                      </span>
                    </li>
                  ))
                ) : (
                  <li className={s.empty}>아직 끝난 일이 없습니다. 지금 하는 일이 끝나면 여기로 내려옵니다.</li>
                )}
              </ul>
              <button
                className={cx(s.btn, s.sm, s["feed-more"])}
                type="button"
                id="lFeedMore"
                aria-controls="lFeed"
                aria-expanded={l.feedAll}
                hidden={l.feed.length <= FEED_N}
                onClick={feedMore}
              >
                {l.feedAll ? `최근 ${FEED_N}건만 보기` : `이전 활동 ${l.feed.length - FEED_N}건 더 보기`}
              </button>
            </section>
          </div>
        </div>
        <LiveInsp />
      </div>
    </>
  );
}
