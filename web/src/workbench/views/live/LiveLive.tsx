"use client";

// live 점검 진행. demo Live 와 같은 머리 · 진행 띠 · 전송 통제 · 계획/지난 활동 · 발견 패널 모양에 고른 점검의 엔진 값을 넣는다.
// 섹션마다 따로 부르고(403·503 이면 그 섹션만 접음), 화면이 안 보이면 부르지 않는다. 끝난 점검은 진입 때 한 번만 부른다.
// 엔진에 없는 것(종료 · 남은 시간 · 판정 · 새 경로 포함 결정)은 가짜 값 대신 '엔진 미지원'으로 둔다.

import { type ReactElement, type ReactNode, useRef, useState } from "react";

import { flushSync } from "react-dom";

import { ApiError } from "@/abc2lab-ui/lib/api";

import { cx } from "../../lib/cx";
import {
  controlTask,
  fetchCoverage,
  fetchIntents,
  fetchRequestApprovals,
  fetchTaskFindings,
  type LiveCoverage,
  type LiveFinding,
  type LiveTask,
} from "../../live/api";
import { reloadTasks, useLiveTasks, useSelectedTask } from "../../live/feeds";
import { endpointCov, findingTitle, planRows, progressOf, sevCounts } from "../../live/liveReport";
import {
  approvalCounts,
  clockOf,
  elapsed,
  feedRow,
  fmtCount,
  fmtDur,
  fmtTokens,
  hostOf,
  isActive,
  isUnsupported,
  taskLabel,
  taskPhase,
} from "../../live/map";
import { maskSecrets } from "../../live/mask";
import {
  ErrorNote,
  EstimateTag,
  LoadingRow,
  MaskedNote,
  ModeNote,
  PhasePill,
  TaskPicker,
  Unsupported,
  useNow,
} from "../../live/parts";
import { useActivityTail } from "../../live/useActivityTail";
import { type Poll, usePoll } from "../../live/usePoll";
import { useShell } from "../../shell";
import { Icon } from "../../ui/icons";
import { InspHead, InspOpenButton, Isec, MenuButton } from "../../ui/parts";
import s from "../../workbench.module.css";
import { FindingDetailLive, SevBadge, StatusTag } from "./FindingDetailLive";

/** 지난 활동은 최근 이만큼만 보이고 나머지는 '더 보기' (demo 와 같음) */
const FEED_N = 5;
/** 점검 계획은 처음 이만큼만 */
const PLAN_N = 12;
const SEVS = ["crit", "hi", "mid", "lo"] as const;

/** 진행률 기준 이름 */
const BASIS_LABEL = { goals: "목표 달성", coverage: "커버리지", none: "없음" } as const;
/** 진행 중 점검의 재조회 간격(ms). 끝난 점검은 0(진입 때 한 번). */
const EVERY = { findings: 4000, coverage: 5000, intents: 10_000, approvals: 5000, activity: 4000 } as const;

/** 머리 (선택 전 · 불러오는 중 · 점검 화면이 같이 쓴다). */
function Header({ label, pill, acts }: { label?: string; pill?: ReactNode; acts?: ReactNode }) {
  return (
    <header className={s.vh}>
      <div className={s["vh-top"]}>
        <MenuButton />
        <nav className={s.crumb} aria-label="위치">
          <a href="#dashboard">작업대</a>
          <span className={s.sep}>/</span>
          {label ? (
            <>
              <span className={cx(s.host, s["t-host"])}>{label}</span>
              <span className={s.sep}>/</span>
            </>
          ) : null}
          <span>점검 진행</span>
        </nav>
      </div>
      <div className={s["vh-row"]}>
        <div className={s["vh-title"]}>
          <h1 tabIndex={-1}>점검 진행</h1>
          {pill}
        </div>
        {acts !== undefined ? <div className={s["vh-acts"]}>{acts}</div> : null}
      </div>
    </header>
  );
}

/** 섹션 본문: 미지원 → ModeNote, 첫 실패 → ErrorNote, 첫 로딩 → LoadingRow, 아니면 null(그릴 데이터 있음). */
function pollState<T>(p: Poll<T>, what: string): ReactElement | null {
  if (p.unsupported) return <ModeNote what={what} />;
  if (!p.data && p.error) return <ErrorNote error={p.error} onRetry={p.reload} />;
  if (!p.data) return <LoadingRow />;
  return null;
}

/** 커버리지 한 줄 글 (pmeta). */
function covLine(p: Poll<LiveCoverage>): ReactNode {
  if (p.unsupported) return <b>이 서버 모드 미제공</b>;
  if (!p.data) return <b>{p.error ? "—" : "…"}</b>;
  if (!p.data.enabled) return <b>꺼짐</b>;
  const ep = endpointCov(p.data);
  const pct = p.data.pct === null ? null : Math.round(p.data.pct * 100);
  if (!ep) return <b>{pct === null ? "—" : `${pct}%`}</b>;
  return (
    <>
      <b>
        {ep.tested}/{ep.total}
      </b>{" "}
      엔드포인트{pct === null ? "" : ` · ${pct}%`}
    </>
  );
}

/** 오른쪽 발견 패널: 목록(최근 순) ↔ 상세. */
function LiveInsp({
  task,
  host,
  findings,
  visible,
}: {
  task: LiveTask;
  host: string;
  findings: Poll<LiveFinding[]>;
  visible: boolean;
}) {
  const { openInsp } = useShell();
  const [picked, setPicked] = useState<{ taskId: string; row: LiveFinding } | null>(null);
  const boxRef = useRef<HTMLElement>(null);
  const backRef = useRef<HTMLButtonElement>(null);
  const list = findings.data ?? [];
  // 고른 발견: 목록에서 새 값으로, 목록에서 사라졌으면 고를 때 값으로
  const cur = picked && picked.taskId === task.id ? (list.find((f) => f.id === picked.row.id) ?? picked.row) : null;

  const pick = (row: LiveFinding) => {
    flushSync(() => setPicked({ taskId: task.id, row }));
    if (boxRef.current) boxRef.current.scrollTop = 0;
    backRef.current?.focus();
    openInsp();
  };
  const back = () => {
    const was = cur?.id;
    flushSync(() => setPicked(null));
    const rows = boxRef.current?.querySelectorAll<HTMLElement>("[data-lvsel]") ?? [];
    for (const el of rows) if (el.dataset.lvsel === was) el.focus();
  };

  if (cur) {
    return (
      <aside className={s.vi} id="lvInsp" aria-label="발견" ref={boxRef}>
        <button className={s.back} type="button" ref={backRef} onClick={back}>
          <Icon name="chev" />
          발견 목록
        </button>
        <FindingDetailLive row={cur} host={host} enabled={visible} />
      </aside>
    );
  }

  const c = sevCounts(list);
  const state = pollState(findings, "발견");
  return (
    <aside className={s.vi} id="lvInsp" aria-label="발견" ref={boxRef}>
      <InspHead
        eyebrow={
          <>
            {taskPhase(task).label} · <span className={s.host}>{host}</span>
          </>
        }
        title={findings.data ? `발견 ${list.length}건` : "발견"}
      />
      {state ? <Isec>{state}</Isec> : null}
      {!state && list.length ? (
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
            {c.other ? (
              <span>
                그 밖 <b>{c.other}</b>
              </span>
            ) : null}
          </div>
          <Isec t="발견 · 최근 순">
            <div className={s.flist}>
              {list.map((f) => (
                <button key={f.id} className={s.fli} type="button" data-lvsel={f.id} onClick={() => pick(f)}>
                  <SevBadge severity={f.severity} />
                  <span>{findingTitle(f)}</span>
                  <StatusTag status={f.status} />
                </button>
              ))}
            </div>
            <p className={s.help}>오른쪽 딱지는 처리 상태입니다(판정 아님).</p>
          </Isec>
        </>
      ) : null}
      {!state && !list.length ? (
        <Isec>
          <p className={s.empty}>아직 발견이 없습니다. 찾으면 여기에 나타납니다.</p>
        </Isec>
      ) : null}
      <Isec t="판정">
        <Unsupported what="판정(확정·의심·판단 불가)" />
      </Isec>
      <Isec t="결정 대기">
        <Unsupported what="새로 찾은 경로 포함 결정" />
      </Isec>
    </aside>
  );
}

/** 고른 점검 화면 (task 가 있을 때만 그려서 조회 훅이 늘 그 점검을 본다). */
function LiveTaskView({ task, visible }: { task: LiveTask; visible: boolean }) {
  const { goTo } = useShell();
  const id = task.id;
  const active = isActive(task);
  const every = (ms: number) => (active ? ms : 0);
  const now = useNow(visible ? 5000 : 60_000);

  const findings = usePoll(() => fetchTaskFindings(id), [id], { intervalMs: every(EVERY.findings), enabled: visible });
  const coverage = usePoll(() => fetchCoverage(id), [id], { intervalMs: every(EVERY.coverage), enabled: visible });
  const intents = usePoll(() => fetchIntents(id), [id], { intervalMs: every(EVERY.intents), enabled: visible });
  const approvals = usePoll(() => fetchRequestApprovals(id), [id], {
    intervalMs: every(EVERY.approvals),
    enabled: visible,
  });
  const tail = useActivityTail(id, { intervalMs: every(EVERY.activity), enabled: visible });

  const [ctl, setCtl] = useState<{ busy: boolean; err: unknown }>({ busy: false, err: null });
  const [feedAll, setFeedAll] = useState(false);
  const [planAll, setPlanAll] = useState(false);
  const feedRef = useRef<HTMLUListElement>(null);

  const phase = taskPhase(task);
  const paused = task.paused || task.status === "paused";
  const label = taskLabel(task);
  const host = hostOf(task);
  const fsum = task.findings.critical + task.findings.high + task.findings.medium + task.findings.low;
  const fN = findings.data ? findings.data.length : fsum;

  const toggle = async () => {
    setCtl({ busy: true, err: null });
    try {
      await controlTask(id, paused ? "resume" : "pause");
      setCtl({ busy: false, err: null });
    } catch (err) {
      setCtl({ busy: false, err });
    } finally {
      reloadTasks();
    }
  };

  // 제어 실패 안내: 409 는 지금 상태에서 안 됨(서버 글), 403·404·503 은 이 서버 모드 미지원
  let ctlNote: ReactNode = null;
  if (ctl.err instanceof ApiError && ctl.err.status === 409) {
    ctlNote = (
      <div className={cx(s.note, s.warn)} role="alert">
        <Icon name="alert" />
        <span>{`지금 상태에서는 바꿀 수 없습니다${ctl.err.message ? `: ${ctl.err.message}` : ""}`}</span>
      </div>
    );
  } else if (ctl.err && isUnsupported(ctl.err)) ctlNote = <ModeNote what="일시정지 · 다시 시작" />;
  else if (ctl.err) ctlNote = <ErrorNote error={ctl.err} />;

  // 진행 띠
  const prog = progressOf(task, coverage.data);
  const plan = intents.data ? planRows(intents.data) : [];
  const running = plan.filter((r) => r.state.row === "run");
  const doneN = plan.filter((r) => r.state.row === "done").length;
  let nowK: ReactNode;
  let nowText: string;
  let nowCls: string;
  if (task.status === "done") {
    nowCls = cx(s["now-k"], s.ok);
    nowK = (
      <>
        <Icon name="check" />
        완료
      </>
    );
    nowText = "엔진이 점검을 마쳤습니다";
  } else if (!active) {
    nowCls = cx(s["now-k"], s.idle);
    nowK = (
      <>
        <i />
        끝남
      </>
    );
    nowText = phase.label;
  } else if (paused) {
    nowCls = cx(s["now-k"], s.idle);
    nowK = (
      <>
        <i />
        멈춤
      </>
    );
    nowText = "일시정지됨 · 다시 시작하면 이어서 진행합니다";
  } else {
    nowCls = s["now-k"];
    nowK = (
      <>
        <i />
        지금 하는 일
      </>
    );
    if (running.length)
      nowText = running.length > 1 ? `${running[0].text} 외 ${running.length - 1}건` : running[0].text;
    else nowText = intents.data ? `${phase.label} · 진행 중인 의도 없음` : phase.label;
  }
  const tok = fmtTokens(task.tokens.input_tokens + task.tokens.output_tokens);

  // 전송 통제
  const ap = approvals.data ? approvalCounts(approvals.data.run, approvals.data.approvals) : null;
  const apCounts = approvals.data?.run.execution?.counts;
  const known = (n: number) => (ap?.known ? fmtCount(n) : "—");

  // 지난 활동 (최신부터)
  const feed = (tail.data?.items ?? []).slice().reverse().map(feedRow);
  const shown = feedAll ? feed : feed.slice(0, FEED_N);
  const feedMore = () => {
    flushSync(() => setFeedAll((v) => !v));
    if (!feedAll && feedRef.current) feedRef.current.scrollTop = 0;
  };

  const planShown = planAll ? plan : plan.slice(0, PLAN_N);
  const planNote = pollState(intents, "점검 계획");
  const feedNote = pollState(tail, "지난 활동");
  const apNote = approvals.unsupported ? null : pollState(approvals, "전송 통제");

  return (
    <>
      <Header
        label={label}
        pill={<PhasePill task={task} />}
        acts={
          <>
            {active ? (
              <button
                className={s.btn}
                type="button"
                id="lvPause"
                aria-pressed={paused}
                disabled={ctl.busy}
                onClick={toggle}
              >
                {paused ? (
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
            ) : null}
            {active ? (
              <span className={s.help} title="일반 모드 엔진에는 점검 종료(stop) API 가 없습니다">
                종료 <Unsupported chip />
              </span>
            ) : null}
            <InspOpenButton id="lvInspBtn">{`발견 ${fN}`}</InspOpenButton>
          </>
        }
      />
      <div className={s.vb}>
        <div className={s.vm}>
          {ctlNote}
          <div className={s.band}>
            <div className={s.pct} id="lvPct">
              {prog.pct ?? "—"}
              {prog.pct === null ? null : <small>%</small>}
            </div>
            <div className={s.bt}>
              {prog.pct === null ? null : (
                <div
                  className={s.prog}
                  role="progressbar"
                  aria-label={prog.basis === "goals" ? "목표 달성률" : "커버리지"}
                  aria-valuemin={0}
                  aria-valuemax={100}
                  aria-valuenow={prog.pct}
                >
                  <i style={{ width: `${prog.pct}%` }} />
                </div>
              )}
              <div className={s["now-l"]} aria-live="polite">
                <span className={nowCls}>{nowK}</span>
                <span id="lvTask">{nowText}</span>
              </div>
              <div className={s.pmeta} id="lvMeta">
                <span>
                  진행률 기준 <b>{BASIS_LABEL[prog.basis ?? "none"]}</b>
                </span>
                <span>
                  시작 <b>{clockOf(task.created_unix || task.created_at).slice(0, 5) || "—"}</b>
                </span>
                <span>
                  경과 <b>{now ? fmtDur(elapsed(task, now)) : "—"}</b>
                </span>
                <span>
                  목표 <b>{task.goals_total > 0 ? `${task.goals_met}/${task.goals_total}` : "—"}</b>
                </span>
                <span
                  title={`입력 ${fmtCount(task.tokens.input_tokens)} · 출력 ${fmtCount(task.tokens.output_tokens)} · 캐시 읽기 ${fmtCount(task.tokens.cache_read_tokens)}`}
                >
                  토큰 <b>{`${tok.n}${tok.unit}`}</b> (입력+출력)
                </span>
                <span>커버리지 {covLine(coverage)}</span>
                {active ? (
                  <span>
                    워커 <b>{task.in_flight}</b>
                  </span>
                ) : null}
                {host ? (
                  <span>
                    대상 <b>{host}</b> <EstimateTag title="점검 설명에서 뽑은 주소입니다(엔진에 대상 칸이 없음)" />
                  </span>
                ) : null}
              </div>
            </div>
          </div>
          {prog.pct === null ? (
            <p className={s.help}>
              목표와 커버리지가 아직 없어 진행률을 셀 수 없습니다. 남은 시간은 엔진이 알려 주지 않습니다.
            </p>
          ) : null}
          {task.status === "done" ? (
            <div className={s.ldone} id="lvDone">
              <div className={s.dm}>
                <b>
                  <Icon name="check" />
                  <span>점검을 마쳤습니다</span>
                </b>
                <span>{`발견 ${fN}건 · 소요 ${fmtDur(elapsed(task, now))}`}</span>
              </div>
              <button className={cx(s.btn, s.pri)} type="button" onClick={() => goTo("report")}>
                보고서 보기
                <Icon name="arrow" />
              </button>
            </div>
          ) : null}
          {!active && task.status !== "done" ? (
            <div className={cx(s.note, s.warn)} id="lvDone">
              <Icon name="alert" />
              <span className={s["lv-grow"]}>{`점검이 끝났습니다 (${phase.label}) · 발견 ${fN}건`}</span>
              <button className={cx(s.btn, s.sm)} type="button" onClick={() => goTo("report")}>
                보고서 보기
              </button>
            </div>
          ) : null}
          <section className={s.sec} aria-labelledby="lvSendH">
            <div className={s.sh}>
              <h2 id="lvSendH">전송 통제</h2>
              {apCounts ? (
                <span className={s.aside}>
                  {`요청 접수 ${fmtCount(apCounts.attempts)} · 대상 전송 시도 ${fmtCount(apCounts.forward_attempted)}`}
                </span>
              ) : null}
            </div>
            {approvals.unsupported ? (
              <>
                <ModeNote what="전송 통제" />
                <p className={s.help}>요청별 전송 통제는 첫 진단 모드에서만 제공됩니다.</p>
              </>
            ) : (
              (apNote ?? (
                <>
                  <div className={s.meters}>
                    <div className={s.cell}>
                      <div className={s.k}>허용 판정</div>
                      <div className={s.v} id="lvAllowed">
                        {known(ap?.allowed ?? 0)}
                      </div>
                      <div className={s.sub}>{`실제 전송 아님 · 대상 전송 시도 ${known(ap?.forwarded ?? 0)}`}</div>
                    </div>
                    <div className={s.cell}>
                      <div className={s.k}>막은 요청</div>
                      <div className={s.v} id="lvBlk">
                        {known(ap?.blocked ?? 0)}
                      </div>
                      <div className={s.sub}>차단 + 미지원</div>
                    </div>
                    <div className={s.cell}>
                      <div className={s.k}>승인 필요로 보류</div>
                      <div className={s.v} id="lvHold">
                        {fmtCount(ap?.held ?? 0)}
                      </div>
                      <div className={s.sub}>승인 대기 중</div>
                    </div>
                    <div className={s.cell}>
                      <div className={s.k}>전송 불명</div>
                      <div className={s.v} id="lvUnk">
                        {known(ap?.unknown ?? 0)}
                      </div>
                      <div className={s.sub}>결과를 모름</div>
                    </div>
                  </div>
                  <p className={s.help}>
                    {ap?.known
                      ? "엔진 실행 감독자가 센 요청 수입니다. 허용 판정에는 보내기 전에 취소된 요청도 들어가며, 실제로 보낸 수는 대상 전송 시도입니다."
                      : "실행 정보가 아직 없어 허용 · 막음 · 불명은 비워 둡니다."}
                  </p>
                </>
              ))
            )}
          </section>
          <div className={s["two-col"]}>
            <section className={s.sec} aria-labelledby="lvPlanH">
              <div className={s.sh}>
                <h2 id="lvPlanH">점검 계획</h2>
                {intents.data ? (
                  <span className={s.n} id="lvPlanN">
                    {`${doneN}/${plan.length} 완료`}
                  </span>
                ) : null}
              </div>
              {planNote ??
                (plan.length ? (
                  <>
                    <ol className={s.plan} id="lvPlan">
                      {planShown.map((r) => {
                        const done = r.state.row === "done";
                        return (
                          <li
                            key={r.key}
                            className={cx(done && s.done, r.state.row === "run" && !paused && s.run) || undefined}
                          >
                            <span className={s.st}>{done ? <Icon name="check" /> : null}</span>
                            <span>
                              {r.text}
                              {` · ${r.state.label}`}
                            </span>
                          </li>
                        );
                      })}
                    </ol>
                    {plan.length > PLAN_N ? (
                      <button
                        className={cx(s.btn, s.sm, s["feed-more"])}
                        type="button"
                        aria-controls="lvPlan"
                        aria-expanded={planAll}
                        onClick={() => setPlanAll((v) => !v)}
                      >
                        {planAll ? `처음 ${PLAN_N}건만 보기` : `의도 ${plan.length - PLAN_N}건 더 보기`}
                      </button>
                    ) : null}
                  </>
                ) : (
                  <p className={s.empty}>아직 의도가 없습니다.</p>
                ))}
              {intents.unsupported ? null : (
                <p className={s.help}>
                  엔진의 의도 목록입니다(고정 단계 아님). 점검 중에 엔진이 의도를 더하고 실행합니다. 진행 중 → 대기 →
                  완료 → 그 밖(원문 상태) 순으로 보입니다.
                </p>
              )}
            </section>
            <section className={s.sec} aria-labelledby="lvFeedH">
              <div className={s.sh}>
                <h2 id="lvFeedH">
                  지난 활동{" "}
                  <span className={s.n} id="lvFeedN">
                    {tail.data ? `${feed.length}건` : ""}
                  </span>
                </h2>
                <div className={s.legend}>
                  <span>
                    <span className={cx(s.fd, s.ok, s.dot)} />
                    결과
                  </span>
                  <span>
                    <span className={cx(s.fd, s.col, s.dot)} />
                    도구 호출
                  </span>
                  <span>
                    <span className={cx(s.fd, s.blk, s.dot)} />
                    오류
                  </span>
                </div>
              </div>
              {feedNote ?? (
                <>
                  <ul className={cx(s.feed, feedAll && feed.length > FEED_N && s.scroll)} id="lvFeed" ref={feedRef}>
                    {shown.length ? (
                      shown.map((r) => (
                        <li key={r.key}>
                          <span className={s.t}>{r.time}</span>
                          <span className={cx(s.fd, r.dot && s[r.dot])} />
                          <span>
                            {maskSecrets(r.text) || "(내용 없음)"}
                            <span className={cx(s.res, r.isError && s.err)}>
                              {r.isError ? `${r.kindLabel} · 오류` : r.kindLabel}
                            </span>
                          </span>
                        </li>
                      ))
                    ) : (
                      <li className={s.empty}>아직 활동이 없습니다.</li>
                    )}
                  </ul>
                  <button
                    className={cx(s.btn, s.sm, s["feed-more"])}
                    type="button"
                    id="lvFeedMore"
                    aria-controls="lvFeed"
                    aria-expanded={feedAll}
                    hidden={feed.length <= FEED_N}
                    onClick={feedMore}
                  >
                    {feedAll ? `최근 ${FEED_N}건만 보기` : `이전 활동 ${feed.length - FEED_N}건 더 보기`}
                  </button>
                  {tail.data && !tail.data.caughtUp ? <p className={s.help}>이전 활동을 따라잡는 중입니다.</p> : null}
                  <MaskedNote />
                </>
              )}
            </section>
          </div>
        </div>
        <LiveInsp task={task} host={host ?? label} findings={findings} visible={visible} />
      </div>
    </>
  );
}

/** live 점검 진행: 고른 점검(useSelectedTask). 없으면 진행 중 점검 고르기. */
export function LiveLive() {
  const { route } = useShell();
  const visible = route === "live";
  const { taskId, task, missing } = useSelectedTask();
  const feed = useLiveTasks();

  if (taskId && task) return <LiveTaskView key={task.id} task={task} visible={visible} />;

  let body: ReactNode;
  if (!taskId || missing) {
    body = (
      <>
        {missing ? (
          <div className={cx(s.note, s.warn)} role="note">
            <Icon name="alert" />
            <span>{`고른 점검(#${taskId})을 목록에서 찾을 수 없습니다. 다른 점검을 고르세요.`}</span>
          </div>
        ) : null}
        <TaskPicker which="active" title="진행 중인 점검을 고르세요" />
        <p className={s.help}>끝난 점검은 보고서에서 봅니다.</p>
      </>
    );
  } else if (feed.unsupported) body = <ModeNote what="점검 목록" />;
  else if (!feed.data && feed.error) body = <ErrorNote error={feed.error} onRetry={feed.reload} />;
  else body = <LoadingRow />;

  return (
    <>
      <Header />
      <div className={s.vm}>{body}</div>
    </>
  );
}
