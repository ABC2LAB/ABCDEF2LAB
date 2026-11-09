"use client";

// 실제 엔진 대시보드: 요약 칸 · 지금 처리할 일(도구 호출 승인) · 진행 중 / 최근 끝난 점검 · 선택한 항목 상세.
// 마크업·클래스는 시안(views/Dashboard.tsx)과 같다. 엔진에 없는 칸(판단 불가 집계 · 새로 찾은 경로)은 Unsupported 로 둔다.
// 점검 목록·승인 대기는 공용 조회(useLiveTasks · useLivePending), 발견·토큰은 화면에 들어올 때 한 번 + 새로고침.
// 줄을 누르면 오른쪽 상세만 바꾼다(점검 줄은 selectTask 도). 다른 화면으로는 상세의 버튼으로 간다.

import { type ReactNode, useEffect, useRef, useState } from "react";

import { ApiError } from "@/abc2lab-ui/lib/api";

import { cx } from "../../lib/cx";
import {
  decidePendingApproval,
  fetchFindings,
  fetchPendingApproval,
  fetchTokenUsage,
  type LiveFinding,
  type LiveTask,
  type LiveTaskList,
  type PendingApproval,
  type TokenUsage,
} from "../../live/api";
import {
  activeTasks,
  approvalPill,
  doneTasks,
  findingsTotal,
  SEV_ROWS,
  summarizeFindings,
  tokenSummary,
  WINDOW_DAYS,
} from "../../live/dashboard";
import { type FeedSnap, reloadPending, reloadTasks, useLivePending, useLiveTask, useLiveTasks } from "../../live/feeds";
import {
  clockOf,
  elapsed,
  errorText,
  fmtDur,
  fmtTokens,
  isActive,
  relTime,
  taskLabel,
  taskPhase,
  urlOf,
} from "../../live/map";
import { maskSecrets } from "../../live/mask";
import {
  ErrorNote,
  EstimateTag,
  LoadingRow,
  MaskedNote,
  ModeNote,
  PhasePill,
  Unsupported,
  useNow,
} from "../../live/parts";
import { useSource } from "../../live/source";
import { type Poll, usePoll } from "../../live/usePoll";
import { useShell } from "../../shell";
import { useWb } from "../../store";
import { Icon } from "../../ui/icons";
import { InspHead, InspOpenButton, Isec, Kv, MenuButton, Sev } from "../../ui/parts";
import s from "../../workbench.module.css";

/** 최근 끝난 점검 목록에 보일 최대 줄 수. */
export const DASH_DONE_MAX = 8;

/** 오른쪽 상세에 고른 것: 도구 호출 승인 한 건 · 점검 하나 · 없음. */
type Sel = { kind: "appr"; id: number } | { kind: "task"; id: string } | null;

const MODE_TEXT = "이 서버 모드에서는 제공되지 않음";

/** 토큰 수 → "4.2M" 꼴 글. */
function tok(n: number): string {
  const f = fmtTokens(n);
  return `${f.n}${f.unit}`;
}

/** 시각 → "3시간 전 · 14:03:20" (못 읽으면 "—"). */
function when(ts: string | number, now: number): string {
  const c = clockOf(ts);
  if (!c) return "—";
  const r = now ? relTime(ts, now) : "";
  return r ? `${r} · ${c}` : c;
}

/** 상태 알약. */
function Pill({ cls, label }: { cls: "" | "ok" | "wait" | "run"; label: string }) {
  return <span className={cx(s.pill, cls && s[cls])}>{label}</span>;
}

/** 줄 아래 글(불러오는 중 · 미지원 · 오류). 데이터가 있으면 null. */
function snapText<T>(p: Pick<Poll<T>, "data" | "error" | "unsupported">): string | null {
  if (p.unsupported) return MODE_TEXT;
  if (!p.data && p.error) return errorText(p.error);
  if (!p.data) return "불러오는 중…";
  return null;
}

// ---------- 요약 칸 ----------

/** 발견된 취약점 · 최근 30일 (전체 발견 목록을 ts 로 거름). */
function FindingsCell({ poll, now }: { poll: Poll<LiveFinding[]>; now: number }) {
  const wait = snapText(poll) ?? (now ? null : "불러오는 중…");
  const sm = !wait && poll.data ? summarizeFindings(poll.data, now) : null;
  let sub = wait ?? "";
  if (sm) {
    const parts = SEV_ROWS.map(([k, , label]) => `${label} ${sm.bySev[k]}`);
    if (sm.other) parts.push(`기타 ${sm.other}`);
    if (sm.capped) parts.push("최근 500건 안에서");
    sub = parts.join(" · ");
  }
  return (
    <div className={cx(s.cell, sm && sm.bySev.critical > 0 && s.crit)}>
      <div className={s.k}>발견된 취약점 · 최근 30일</div>
      <div className={s.v}>{sm ? sm.total : "—"}</div>
      <div className={s.stack} aria-hidden="true">
        {sm
          ? SEV_ROWS.map(([k, key]) =>
              sm.bySev[k] ? <i key={k} style={{ flex: sm.bySev[k], background: `var(--${key})` }} /> : null,
            )
          : null}
      </div>
      <div className={s.sub}>{sub}</div>
    </div>
  );
}

/** 결정 대기: 도구 호출 승인(intercept) 대기 수만 센다. */
function DecideCell({ pending }: { pending: FeedSnap<PendingApproval[]> }) {
  const wait = snapText(pending);
  const n = !wait && pending.data ? pending.data.length : null;
  return (
    <div className={cx(s.cell, n !== null && n > 0 && s.warn)}>
      <div className={s.k}>결정 대기</div>
      <div className={s.v}>{n ?? "—"}</div>
      <div className={s.sub}>{wait ?? "도구 호출 승인만 셈 (요청별 승인 제외)"}</div>
    </div>
  );
}

/** 결과 확인: 판단 불가 · 미검사 집계는 엔진에 없다. */
function ResultCell() {
  return (
    <div className={s.cell}>
      <div className={s.k}>결과 확인</div>
      <div className={s.v}>—</div>
      <div className={s.sub}>
        <Unsupported chip /> 판단 불가 · 미검사 집계
      </div>
    </div>
  );
}

/** 토큰 사용량 · 최근 30일 (입력+출력 일별 합) · 점검당 평균(추정). */
function TokenCell({ poll, tasks, now }: { poll: Poll<TokenUsage>; tasks: LiveTask[] | null; now: number }) {
  const wait = snapText(poll) ?? (now ? null : "불러오는 중…");
  if (wait || !poll.data) {
    return (
      <div className={s.cell}>
        <div className={s.k}>토큰 사용량 · 최근 30일</div>
        <div className={s.v}>—</div>
        <div className={s.sub}>{wait}</div>
      </div>
    );
  }
  const sum = tokenSummary(poll.data, tasks ?? [], now);
  const f = fmtTokens(sum.total);
  let sub: ReactNode = "입력+출력";
  if (!sum.total) sub = "최근 30일 사용 없음";
  else if (tasks && sum.avg !== null) {
    sub = (
      <>
        입력+출력 · 점검당 평균 {tok(sum.avg)}{" "}
        <EstimateTag title={`토큰 합 ÷ 최근 30일에 만든 점검 ${sum.tasks}개 (토큰은 점검 구분 없는 전역 합)`} />
      </>
    );
  }
  return (
    <div className={s.cell}>
      <div className={s.k}>토큰 사용량 · 최근 30일</div>
      <div className={s.v}>
        {f.n}
        {f.unit ? <small>{f.unit}</small> : null}
      </div>
      <div className={s.sub}>{sub}</div>
    </div>
  );
}

// ---------- 목록 ----------

/** 목록 한 줄 `button.row`. */
function Row({
  sel,
  current,
  onClick,
  children,
  data,
}: {
  sel: boolean;
  current?: boolean;
  onClick: () => void;
  children: ReactNode;
  data: Record<`data-${string}`, string>;
}) {
  return (
    <button
      className={cx(s.row, sel && s.sel)}
      type="button"
      aria-current={current && sel ? "true" : undefined}
      onClick={onClick}
      {...data}
    >
      {children}
    </button>
  );
}

/** 점검 id → 목록 이름 (없으면 "점검 #id", id 도 없으면 "점검 미지정"). */
function labelFor(id: string | null, byId: ReadonlyMap<string, LiveTask>): string {
  if (!id) return "점검 미지정";
  const t = byId.get(id);
  return t ? taskLabel(t) : `점검 #${id}`;
}

/** 지금 처리할 일: 도구 호출 승인 대기. 새 경로 · 판단 불가는 엔진에 없어 한 줄 안내만. */
function TodoSection({
  pending,
  byId,
  sel,
  onPick,
  now,
}: {
  pending: FeedSnap<PendingApproval[]>;
  byId: ReadonlyMap<string, LiveTask>;
  sel: Sel;
  onPick: (p: PendingApproval) => void;
  now: number;
}) {
  const list = pending.data ?? [];
  let body: ReactNode;
  if (pending.unsupported) body = <ModeNote what="도구 호출 승인" />;
  else if (!pending.data && pending.error) body = <ErrorNote error={pending.error} onRetry={reloadPending} />;
  else if (!pending.data) body = <LoadingRow />;
  else if (!list.length) body = <p className={s.empty}>지금 결정할 승인 요청이 없습니다.</p>;
  else {
    body = (
      <div className={s.list} id="lvTodo">
        {list.map((p) => (
          <Row
            key={p.id}
            sel={sel?.kind === "appr" && sel.id === p.id}
            current
            onClick={() => onPick(p)}
            data={{ "data-appr": String(p.id) }}
          >
            <span className={s["r-main"]}>
              <span className={s["r-t"]}>
                <span className={s["r-host"]}>{labelFor(p.task_id, byId)}</span>
                {`${p.tool_name || "도구"} 호출`}
              </span>
              <span className={s["r-s"]}>
                {p.agent_name ? `${p.agent_name} · 실행 전 승인 필요` : "실행 전 승인 필요"}
              </span>
            </span>
            <span className={s["r-side"]}>
              <Pill {...approvalPill(p.status)} />
              <span className={s["r-time"]}>{now ? relTime(p.created_at, now) : ""}</span>
            </span>
          </Row>
        ))}
      </div>
    );
  }
  return (
    <section className={s.sec} aria-labelledby="lvTodoH">
      <div className={s.sh}>
        <h2 id="lvTodoH">지금 처리할 일</h2>
        {pending.data && !pending.unsupported ? <span className={s.n}>{`${list.length}건`}</span> : null}
      </div>
      {body}
      <p className={s.help}>
        <Unsupported chip /> 새로 찾은 경로 · 판단 불가 항목은 엔진에 없어 여기에 보이지 않습니다.
      </p>
    </section>
  );
}

/** 점검 목록 공통 상태(미지원 · 오류 · 불러오는 중). 데이터가 있으면 null. */
function tasksState(feed: FeedSnap<LiveTaskList>): ReactNode {
  if (feed.unsupported) return <ModeNote what="점검 목록" />;
  if (!feed.data && feed.error) return <ErrorNote error={feed.error} onRetry={reloadTasks} />;
  if (!feed.data) return <LoadingRow />;
  return null;
}

/** 진행 중인 점검 (목표 진행은 goals_total>0 일 때만 막대). */
function ActiveSection({
  feed,
  sel,
  onPick,
  now,
}: {
  feed: FeedSnap<LiveTaskList>;
  sel: Sel;
  onPick: (t: LiveTask) => void;
  now: number;
}) {
  const rows = activeTasks(feed.data?.tasks ?? []);
  let body = tasksState(feed);
  if (!body && !rows.length) body = <p className={s.empty}>진행 중인 점검이 없습니다.</p>;
  if (!body) {
    body = (
      <div className={s.list} id="lvActive">
        {rows.map((t) => {
          const ph = taskPhase(t);
          const n = findingsTotal(t);
          const meta = [
            now && t.created_unix ? `${relTime(t.created_unix, now)} 시작` : "",
            t.goals_total > 0 ? `목표 ${t.goals_met}/${t.goals_total}` : "",
            n ? `발견 ${n}` : "발견 없음",
          ].filter(Boolean);
          return (
            <Row
              key={t.id}
              sel={sel?.kind === "task" && sel.id === t.id}
              onClick={() => onPick(t)}
              data={{ "data-task": t.id }}
            >
              <span className={s["r-main"]}>
                <span className={s["r-t"]}>
                  <span className={s["r-host"]}>{taskLabel(t)}</span>
                </span>
                <span className={s["r-s"]}>{meta.join(" · ")}</span>
                {t.goals_total > 0 ? (
                  <span className={cx(s["r-bar"], ph.tone === "wait" && s.wait)} aria-hidden="true">
                    <i style={{ width: `${Math.round((100 * t.goals_met) / t.goals_total)}%` }} />
                  </span>
                ) : null}
              </span>
              <span className={s["r-side"]}>
                <PhasePill task={t} />
                <span className={s["r-time"]}>{now ? fmtDur(elapsed(t, now)) : ""}</span>
              </span>
            </Row>
          );
        })}
      </div>
    );
  }
  return (
    <section className={s.sec} aria-labelledby="lvActH">
      <div className={s.sh}>
        <h2 id="lvActH">진행 중인 점검</h2>
        {feed.data ? <span className={s.n}>{`${rows.length}건`}</span> : null}
      </div>
      {body}
    </section>
  );
}

/** 최근 끝난 점검 (발견 수는 목록 줄 기준). */
function DoneSection({
  feed,
  sel,
  onPick,
  now,
}: {
  feed: FeedSnap<LiveTaskList>;
  sel: Sel;
  onPick: (t: LiveTask) => void;
  now: number;
}) {
  const all = doneTasks(feed.data?.tasks ?? []);
  const rows = all.slice(0, DASH_DONE_MAX);
  let body = tasksState(feed);
  if (!body && !rows.length) body = <p className={s.empty}>끝난 점검이 없습니다.</p>;
  if (!body) {
    body = (
      <div className={s.list} id="lvDone">
        {rows.map((t) => {
          const ph = taskPhase(t);
          const n = findingsTotal(t);
          const end = t.completed_unix || t.created_unix;
          const meta = [now && end ? relTime(end, now) : "", now ? fmtDur(elapsed(t, now)) : ""].filter(Boolean);
          return (
            <Row
              key={t.id}
              sel={sel?.kind === "task" && sel.id === t.id}
              onClick={() => onPick(t)}
              data={{ "data-task": t.id }}
            >
              <span className={s["r-main"]}>
                <span className={s["r-t"]}>
                  <span className={s["r-host"]}>{taskLabel(t)}</span>
                </span>
                <span className={s["r-s"]}>{meta.join(" · ")}</span>
              </span>
              <span className={s["r-side"]}>
                {ph.tone === "ok" ? null : <PhasePill task={t} />}
                {t.findings.critical ? <Sev k="crit">{`심각 ${t.findings.critical}`}</Sev> : null}
                {t.findings.high ? <Sev k="hi">{`높음 ${t.findings.high}`}</Sev> : null}
                {n ? <span className={s["r-time"]}>{`발견 ${n}`}</span> : <span className={s.pill}>발견 없음</span>}
              </span>
            </Row>
          );
        })}
      </div>
    );
  }
  return (
    <section className={s.sec} aria-labelledby="lvDoneH">
      <div className={s.sh}>
        <h2 id="lvDoneH">최근 끝난 점검</h2>
        {feed.data ? <span className={s.aside}>{`전체 기록 ${all.length}건`}</span> : null}
      </div>
      {body}
    </section>
  );
}

// ---------- 상세 ----------

/** 상세 패널 맨 아래 버튼 줄. */
function Acts({ children }: { children: ReactNode }) {
  return (
    <Isec>
      <div className={s.acts}>{children}</div>
    </Isec>
  );
}

/** 결정 결과 상자 (ok = 체크 · no = x). */
function Result({ ok, children }: { ok: boolean; children: ReactNode }) {
  return (
    <div className={cx(s.result, ok ? s.ok : s.no)} role="status">
      <Icon name={ok ? "check" : "x"} />
      <div>{children}</div>
    </div>
  );
}

/** 상세 머리의 작은 제목 '… · <span class="host">host</span>'. */
function Eyebrow({ label, host }: { label: string; host: string }) {
  return (
    <>
      {label} · <span className={s.host}>{host}</span>
    </>
  );
}

/** 아무것도 안 골랐을 때. */
function EmptyInsp() {
  return (
    <>
      <InspHead eyebrow="선택한 항목" title="고른 항목 없음" />
      <Isec>
        <p className={s.empty}>왼쪽에서 승인 요청이나 점검을 고르면 여기에 자세히 보입니다.</p>
      </Isec>
    </>
  );
}

/** 도구 호출 승인 한 건: 가린 도구 입력 + 허용 / 거부. */
function ApprovalInsp({
  id,
  row,
  byId,
  visible,
  refreshSeq,
}: {
  id: number;
  row: PendingApproval | null;
  byId: ReadonlyMap<string, LiveTask>;
  visible: boolean;
  refreshSeq: number;
}) {
  const now = useNow();
  const detail = usePoll(() => fetchPendingApproval(id), [id], { enabled: visible });
  const [sending, setSending] = useState<"allowed" | "denied" | null>(null);
  const [decided, setDecided] = useState<"allowed" | "denied" | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const lock = useRef(false);
  const reloadDetail = detail.reload;
  const seenSeq = useRef(refreshSeq);

  // 머리의 새로고침 (고른 뒤에 누른 것만)
  useEffect(() => {
    if (refreshSeq === seenSeq.current) return;
    seenSeq.current = refreshSeq;
    reloadDetail();
  }, [refreshSeq, reloadDetail]);

  const decide = async (d: "allowed" | "denied") => {
    if (lock.current) return;
    lock.current = true;
    setSending(d);
    setErr(null);
    try {
      await decidePendingApproval(id, d);
      setDecided(d);
    } catch (e) {
      setErr(e);
    } finally {
      lock.current = false;
      setSending(null);
      reloadPending();
      reloadDetail();
    }
  };

  const d = detail.data;
  const base = d ?? row;
  const status = decided ?? d?.status ?? row?.status ?? "";
  const label = labelFor(base?.task_id ?? null, byId);

  let body: ReactNode;
  if (!d) {
    if (detail.error instanceof ApiError && detail.error.status === 404)
      body = <p className={s.empty}>승인 요청을 찾을 수 없습니다(이미 처리돼 지워졌을 수 있음).</p>;
    else if (detail.unsupported) body = <ModeNote what="승인 상세" />;
    else if (detail.error) body = <ErrorNote error={detail.error} onRetry={reloadDetail} />;
    else body = <LoadingRow />;
  } else {
    const rows: Array<readonly [ReactNode, ReactNode]> = [
      ["도구", d.tool_name || "—"],
      ["에이전트", d.agent_name || "—"],
      ["점검", d.task_id ? `${label} (#${d.task_id})` : "점검 미지정"],
      ["요청 시각", when(d.created_at, now)],
      ["상태", approvalPill(status).label],
    ];
    if (d.reason) rows.push(["걸린 이유", maskSecrets(d.reason)]);
    if (d.decided_at) rows.push(["결정 시각", when(d.decided_at, now)]);
    body = (
      <>
        <Isec t="요청 정보">
          <Kv rows={rows} />
        </Isec>
        <Isec t="실행하려는 도구 입력">
          {/* fetchPendingApproval 이 이미 가렸지만 한 번 더(멱등) */}
          <pre className={s.code}>{maskSecrets(d.toolInputMasked) || "(입력 없음)"}</pre>
          <MaskedNote />
        </Isec>
      </>
    );
  }

  let foot: ReactNode = null;
  if (decided) {
    foot = (
      <Isec>
        {decided === "allowed" ? (
          <Result ok>허용했습니다. 이 도구 호출을 이번 한 번 실행합니다.</Result>
        ) : (
          <Result ok={false}>거부했습니다. 이 도구 호출은 실행하지 않습니다.</Result>
        )}
      </Isec>
    );
  } else if (d && d.status !== "pending") {
    foot = (
      <Isec>
        <Result ok={d.status === "allowed"}>{`이미 결정된 요청입니다 (${approvalPill(d.status).label}).`}</Result>
      </Isec>
    );
  } else if (d) {
    foot = (
      <>
        <Isec t="결정하면">
          <p>허용은 이 도구 호출을 이번 한 번만 실행합니다. 거부하면 실행하지 않습니다.</p>
        </Isec>
        <Acts>
          <button
            className={cx(s.btn, s.pri)}
            type="button"
            disabled={sending !== null}
            onClick={() => void decide("allowed")}
          >
            {sending === "allowed" ? "보내는 중…" : "허용"}
          </button>
          <button className={s.btn} type="button" disabled={sending !== null} onClick={() => void decide("denied")}>
            {sending === "denied" ? "보내는 중…" : "거부"}
          </button>
        </Acts>
      </>
    );
  }

  let errBox: ReactNode = null;
  if (err) {
    errBox = (
      <Isec>
        {err instanceof ApiError && err.status === 409 ? (
          <div className={s.note} role="alert">
            <Icon name="info" />
            <span>이미 결정된 요청입니다.</span>
          </div>
        ) : (
          <ErrorNote error={err} />
        )}
      </Isec>
    );
  }

  return (
    <>
      <InspHead
        eyebrow={<Eyebrow label="승인 요청" host={label} />}
        title={`${base?.tool_name || "도구"} 호출 승인`}
        chips={
          <>
            <Pill {...approvalPill(status)} />
            <span className={cx(s.tag, s.ap)}>실행 전 승인 필요</span>
          </>
        }
      />
      {body}
      {foot}
      {errBox}
    </>
  );
}

/** 점검 하나 요약 + 그 점검 화면으로 가는 버튼 (진행 중 → 점검 진행, 끝남 → 보고서). */
function TaskInsp({ id }: { id: string }) {
  const { goTo } = useShell();
  const t = useLiveTask(id);
  const now = useNow();
  const selectTask = useSource((st) => st.selectTask);
  const nPrefill = useWb((st) => st.nPrefill);
  if (!t) {
    return (
      <>
        <InspHead eyebrow="점검" title={`점검 #${id}`} />
        <Isec>
          <p className={s.empty}>점검 목록에 없는 점검입니다.</p>
        </Isec>
      </>
    );
  }
  const active = isActive(t);
  const url = urlOf(t);
  const f = t.findings;
  const n = findingsTotal(t);
  const open = (to: "live" | "report") => {
    selectTask(t.id);
    goTo(to);
  };
  const rows: Array<readonly [ReactNode, ReactNode]> = [
    [
      "대상",
      url ? (
        <>
          {maskSecrets(url)} <EstimateTag title="엔진에 대상 칸이 없어 설명·목표 글에서 찾은 주소입니다" />
        </>
      ) : (
        "글에서 찾지 못함"
      ),
    ],
    ["상태", taskPhase(t).label],
    ["시작", t.created_unix ? when(t.created_unix, now) : "—"],
  ];
  if (!active) rows.push(["끝남", t.completed_unix ? when(t.completed_unix, now) : "—"]);
  rows.push(
    [active ? "경과" : "소요", now ? fmtDur(elapsed(t, now)) : "—"],
    ["목표", t.goals_total > 0 ? `${t.goals_met}/${t.goals_total} 달성` : "아직 없음"],
    ["발견", n ? `심각 ${f.critical} · 높음 ${f.high} · 중간 ${f.medium} · 낮음 ${f.low}` : "없음"],
    ["토큰", `입력 ${tok(t.tokens.input_tokens)} · 출력 ${tok(t.tokens.output_tokens)}`],
  );
  if (active) rows.push(["도는 작업", `${t.in_flight}개`]);
  return (
    <>
      <InspHead
        eyebrow={<Eyebrow label={active ? "진행 중" : "끝난 점검"} host={taskLabel(t)} />}
        title={t.name.trim() || `점검 #${t.id}`}
        chips={
          <>
            <PhasePill task={t} />
            {f.critical ? <Sev k="crit" /> : null}
            {f.high ? <Sev k="hi" /> : null}
          </>
        }
      />
      <Isec t="점검 정보">
        <Kv rows={rows} />
      </Isec>
      <Acts>
        {active ? (
          <button className={cx(s.btn, s.pri)} type="button" onClick={() => open("live")}>
            점검 진행 열기
            <Icon name="arrow" />
          </button>
        ) : (
          <button className={cx(s.btn, s.pri)} type="button" onClick={() => open("report")}>
            보고서 열기
            <Icon name="arrow" />
          </button>
        )}
        {!active && url ? (
          <button
            className={s.btn}
            type="button"
            onClick={() => {
              nPrefill(new URL(url).origin);
              goTo("new");
            }}
          >
            이 주소로 다시 점검
          </button>
        ) : null}
      </Acts>
    </>
  );
}

export function DashboardLive() {
  const { route, openInsp } = useShell();
  const visible = route === "dashboard";
  const now = useNow();
  const tasks = useLiveTasks();
  const pending = useLivePending();
  const selectTask = useSource((st) => st.selectTask);
  // 발견·토큰: 화면에 들어올 때 한 번 + 새로고침 (공용 조회가 아니라 이 화면만 쓴다)
  const findings = usePoll(() => fetchFindings(), [], { enabled: visible });
  const tokens = usePoll(() => fetchTokenUsage(WINDOW_DAYS), [], { enabled: visible });
  const [sel, setSel] = useState<Sel>(null);
  const [refreshSeq, setRefreshSeq] = useState(0);

  const list = tasks.data?.tasks ?? [];
  const byId = new Map(list.map((t) => [t.id, t] as const));

  const pickAppr = (p: PendingApproval) => {
    setSel({ kind: "appr", id: p.id });
    openInsp();
  };
  const pickTask = (t: LiveTask) => {
    setSel({ kind: "task", id: t.id });
    selectTask(t.id);
    openInsp();
  };
  const refresh = () => {
    reloadTasks();
    reloadPending();
    findings.reload();
    tokens.reload();
    setRefreshSeq((n) => n + 1);
  };

  // 조회 시각: 받아 둔 칸 중 가장 오래된 것
  const fetched = [tasks, pending, findings, tokens]
    .filter((p) => !p.unsupported)
    .map((p) => p.fetchedAt)
    .filter((t): t is number => t !== null);
  const snap = fetched.length ? `조회 ${clockOf(Math.min(...fetched))}` : "조회 중…";

  let insp: ReactNode;
  if (!sel) insp = <EmptyInsp />;
  else if (sel.kind === "appr") {
    insp = (
      <ApprovalInsp
        key={sel.id}
        id={sel.id}
        row={pending.data?.find((p) => p.id === sel.id) ?? null}
        byId={byId}
        visible={visible}
        refreshSeq={refreshSeq}
      />
    );
  } else insp = <TaskInsp id={sel.id} />;

  return (
    <>
      <header className={s.vh}>
        <div className={s["vh-top"]}>
          <MenuButton />
          <span className={s.crumb}>작업대 · 실제 엔진</span>
        </div>
        <div className={s["vh-row"]}>
          <div className={s["vh-title"]}>
            <h1 tabIndex={-1}>대시보드</h1>
            <span className={s.snap} id="lvSnap">
              {snap}
            </span>
          </div>
          <div className={s["vh-acts"]}>
            <button className={s.btn} type="button" onClick={refresh}>
              <Icon name="refresh" />
              새로고침
            </button>
            <InspOpenButton>상세</InspOpenButton>
          </div>
        </div>
      </header>
      <div className={s.vb}>
        <div className={s.vm}>
          <div className={s.strip}>
            <FindingsCell poll={findings} now={now} />
            <DecideCell pending={pending} />
            <ResultCell />
            <TokenCell poll={tokens} tasks={tasks.data ? list : null} now={now} />
          </div>
          <TodoSection pending={pending} byId={byId} sel={sel} onPick={pickAppr} now={now} />
          <ActiveSection feed={tasks} sel={sel} onPick={pickTask} now={now} />
          <DoneSection feed={tasks} sel={sel} onPick={pickTask} now={now} />
        </div>
        <aside className={s.vi} aria-label="선택한 항목 상세">
          {insp}
        </aside>
      </div>
    </>
  );
}
