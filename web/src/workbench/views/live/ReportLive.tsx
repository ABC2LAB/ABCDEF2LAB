"use client";

// live 보고서. demo Report 와 같은 머리 · 메타 줄 · 요약 띠 · 우선 조치 · 발견 목록 · 상세 패널 모양에 고른 점검의 엔진 값을 넣는다.
// 섹션마다 따로 부르고(403·404·409·503 이면 그 섹션만 접음), 화면이 안 보이면 부르지 않는다. 진입 때 한 번 + 새로고침.
// 엔진에 없는 것(확인된 공격 경로 · 자산 지식 그래프 · 판정 집계)은 가짜 값 대신 '엔진 미지원'으로 둔다.

import { type MouseEvent, type ReactElement, type ReactNode, useEffect, useRef, useState } from "react";

import { cx } from "../../lib/cx";
import {
  downloadDiagnostic,
  fetchCoverage,
  fetchLlmProfiles,
  fetchRemediation,
  fetchTaskFindings,
  type LiveCoverage,
  type LiveFinding,
  type LiveTask,
  type Remediation,
} from "../../live/api";
import { useLiveTasks, useSelectedTask } from "../../live/feeds";
import {
  bySeverity,
  covPct,
  dateTimeOf,
  durationText,
  findingTitle,
  modelOf,
  outcomeLabel,
  prioRows,
  sevCounts,
} from "../../live/liveReport";
import { hostOf, isActive, isUnsupported, taskLabel } from "../../live/map";
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
import { type Poll, usePoll } from "../../live/usePoll";
import { useShell } from "../../shell";
import { Icon } from "../../ui/icons";
import { InspOpenButton, MenuButton } from "../../ui/parts";
import s from "../../workbench.module.css";
import { FindingDetailLive, SevBadge, StatusTag } from "./FindingDetailLive";

const SEVS = ["crit", "hi", "mid", "lo"] as const;
/** 우선 조치는 처음 이만큼만 */
const PRIO_N = 8;
/** 진행 중 점검의 발견 재조회 간격(ms). 끝난 점검은 진입 때 한 번. */
const FINDINGS_EVERY = 5000;

type Audience = "shared" | "internal";
const AUD_LABEL: Readonly<Record<Audience, string>> = { shared: "공유용 사본", internal: "내부 상세 사본" };

/** % 글 (없으면 "—"). */
const pctText = (pct: number | null) => (pct === null ? "—" : `${pct}%`);

/** 머리 (선택 전에도 같은 모양). */
function Header({ label, pill, acts, meta }: { label?: string; pill?: ReactNode; acts?: ReactNode; meta?: ReactNode }) {
  return (
    <header className={s.vh}>
      <div className={s["vh-top"]}>
        <MenuButton />
        <nav className={s.crumb} aria-label="위치">
          <a href="#dashboard">작업대</a>
          <span className={s.sep}>/</span>
          <span>점검 기록</span>
          {label ? (
            <>
              <span className={s.sep}>/</span>
              <span className={s.host}>{label}</span>
            </>
          ) : null}
        </nav>
      </div>
      <div className={s["vh-row"]}>
        <div className={s["vh-title"]}>
          <h1 tabIndex={-1}>{label ? `${label} 보고서` : "보고서"}</h1>
          {pill}
        </div>
        {acts !== undefined ? <div className={s["vh-acts"]}>{acts}</div> : null}
      </div>
      {meta !== undefined ? (
        <div className={s["vh-meta"]} id="rvMeta">
          {meta}
        </div>
      ) : null}
    </header>
  );
}

/** 섹션 본문 상태: 미지원 · 첫 실패 · 첫 로딩이면 그 안내, 그릴 데이터가 있으면 null. */
function pollState<T>(p: Poll<T>, what: string): ReactElement | null {
  if (p.unsupported) return <ModeNote what={what} />;
  if (!p.data && p.error) return <ErrorNote error={p.error} onRetry={p.reload} />;
  if (!p.data) return <LoadingRow />;
  return null;
}

/** 커버리지 칸 아래 글. */
function covSub(p: Poll<LiveCoverage>): string {
  if (p.unsupported) return "이 서버 모드에서는 제공되지 않음";
  if (!p.data) return p.error ? "불러오지 못함" : "불러오는 중…";
  if (!p.data.enabled) return "이 점검은 커버리지를 껐습니다";
  if (p.data.pct === null) return "범위가 아직 잡히지 않음";
  return `검사 ${p.data.tested}/${p.data.denominator} · 미검사는 안전을 뜻하지 않음`;
}

/** 우선 조치 (보고서 조치 자료, 내부 상세). URL 진단 보고서(UrlLiveLive)도 쓴다. */
export function Prio({ rem, findings }: { rem: Poll<Remediation>; findings: LiveFinding[] | null }) {
  const [all, setAll] = useState(false);
  let body: ReactNode = pollState(rem, "우선 조치");
  const rows = rem.data ? prioRows(rem.data.entries, findings) : [];
  if (!body && !rows.length) body = <p className={s.empty}>조치 항목이 없습니다.</p>;
  if (!body) {
    const shown = all ? rows : rows.slice(0, PRIO_N);
    body = (
      <>
        <ol className={s.prio} id="rvPrio">
          {shown.map(({ entry: e, finding: f }) => {
            const b = e.record && !e.record.stale ? e.record.bundle : undefined;
            return (
              <li key={e.entry_id}>
                <span className={s.pt}>{maskSecrets(e.title) || e.entry_id}</span>
                <span className={s.pc}>
                  {f ? <SevBadge severity={f.severity} /> : null}
                  <span className={cx(s.tag, s.ex)} title="보고서가 이 항목에 붙인 분류(outcome)">
                    {outcomeLabel(e.outcome)}
                  </span>
                </span>
                <div className={s.pf}>
                  {e.summary ? <p className={s["lv-pre"]}>{maskSecrets(e.summary)}</p> : null}
                  {e.remediation ? (
                    <p className={s["lv-pre"]}>
                      <b>권고</b> {maskSecrets(e.remediation)}
                    </p>
                  ) : null}
                  {b?.immediate_mitigation?.length ? <Steps t="즉시 완화" items={b.immediate_mitigation} /> : null}
                  {b?.root_fix?.length ? <Steps t="근본 수정" items={b.root_fix} /> : null}
                  {e.record?.stale ? (
                    <p className={s.help}>증거가 바뀐 뒤의 오래된 조치 자료라 보이지 않습니다.</p>
                  ) : null}
                  {!e.available && e.unavailable_reason ? (
                    <p className={s.help}>{maskSecrets(e.unavailable_reason)}</p>
                  ) : null}
                </div>
              </li>
            );
          })}
        </ol>
        {rows.length > PRIO_N ? (
          <button
            className={cx(s.btn, s.sm, s["feed-more"])}
            type="button"
            aria-controls="rvPrio"
            aria-expanded={all}
            onClick={() => setAll((v) => !v)}
          >
            {all ? `처음 ${PRIO_N}건만 보기` : `조치 ${rows.length - PRIO_N}건 더 보기`}
          </button>
        ) : null}
        <MaskedNote />
      </>
    );
  }
  return (
    <section className={s.sec} aria-labelledby="rvPrioH">
      <div className={s.sh}>
        <h2 id="rvPrioH">우선 조치</h2>
        <span className={s.aside}>발견과 이어진 항목은 심각도 순</span>
      </div>
      {body}
      {rem.unsupported ? <p className={s.help}>조치 자료는 첫 진단 모드에서 결속된 점검만 제공됩니다.</p> : null}
    </section>
  );
}

/** 조치 단계 목록 (bundle 의 문장 배열). */
function Steps({ t, items }: { t: string; items: readonly string[] }) {
  return (
    <>
      <b>{t}</b>
      <ul className={cx(s.ilist, s.dash)}>
        {items.map((x, i) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: 서버가 준 문장 배열이라 순서가 곧 정체성(같은 문장이 겹칠 수 있음)
          <li key={i}>
            <Icon name="chev" />
            <span>{maskSecrets(x)}</span>
          </li>
        ))}
      </ul>
    </>
  );
}

/** 보고서 내려받기 (종합 HTML, 첫 진단 모드). 미지원이면 접는다. URL 진단 보고서(UrlLiveLive)도 쓴다. */
export function Download({ taskId }: { taskId: string }) {
  const [st, setSt] = useState<{ busy: Audience | null; err: unknown; done: Audience | null }>({
    busy: null,
    err: null,
    done: null,
  });
  const ctrl = useRef<AbortController | null>(null);
  useEffect(() => () => ctrl.current?.abort(), []);

  const run = async (aud: Audience) => {
    ctrl.current?.abort();
    const c = new AbortController();
    ctrl.current = c;
    setSt({ busy: aud, err: null, done: null });
    try {
      await downloadDiagnostic(taskId, aud, c.signal);
      if (!c.signal.aborted) setSt({ busy: null, err: null, done: aud });
    } catch (err) {
      if (!c.signal.aborted) setSt({ busy: null, err, done: null });
    }
  };

  const unsupported = !!st.err && isUnsupported(st.err);
  return (
    <section className={s.sec} aria-labelledby="rvDlH">
      <div className={s.sh}>
        <h2 id="rvDlH">내려받기</h2>
      </div>
      {unsupported ? (
        <>
          <ModeNote what="보고서 내려받기" />
          <p className={s.help}>종합 보고서 파일은 첫 진단 모드에서 결속된 점검만 제공됩니다.</p>
        </>
      ) : (
        <>
          <div className={s.acts}>
            <button className={cx(s.btn, s.pri, s.sm)} type="button" disabled={!!st.busy} onClick={() => run("shared")}>
              {st.busy === "shared" ? "내려받는 중…" : AUD_LABEL.shared}
            </button>
            <button className={cx(s.btn, s.sm)} type="button" disabled={!!st.busy} onClick={() => run("internal")}>
              {st.busy === "internal" ? "내려받는 중…" : AUD_LABEL.internal}
            </button>
          </div>
          {st.err ? <ErrorNote error={st.err} /> : null}
          {st.done ? (
            <p className={s.help} role="status">
              {`${AUD_LABEL[st.done]} 파일을 저장했습니다.`}
            </p>
          ) : null}
          <p className={s.help}>
            종합 HTML 보고서를 파일로 저장합니다. 공유용은 서버가 자유문장까지 가립니다. 내부 상세는 외부로 보내기 전에
            민감정보를 검토하세요.
          </p>
        </>
      )}
    </section>
  );
}

/** 고른 점검의 보고서 (task 가 있을 때만 그려서 조회 훅이 늘 그 점검을 본다). */
function ReportTaskView({ task, visible }: { task: LiveTask; visible: boolean }) {
  const { openInsp } = useShell();
  const id = task.id;
  const active = isActive(task);
  const now = useNow(visible ? 30_000 : 120_000);

  const findings = usePoll(() => fetchTaskFindings(id), [id], {
    intervalMs: active ? FINDINGS_EVERY : 0,
    enabled: visible,
  });
  const coverage = usePoll(() => fetchCoverage(id), [id], { enabled: visible });
  const profiles = usePoll(() => fetchLlmProfiles(), [], { enabled: visible });
  const rem = usePoll(() => fetchRemediation(id, "internal"), [id], { enabled: visible });

  const [sel, setSel] = useState<string | null>(null);
  const inspRef = useRef<HTMLElement>(null);

  const label = taskLabel(task);
  const host = hostOf(task);
  const list = findings.data ? bySeverity(findings.data) : [];
  const c = sevCounts(list);
  const pendingN = list.filter((f) => f.status === "pending").length;
  // 고른 발견 (처음·사라짐 → 첫 줄)
  const cur = list.find((f) => f.id === sel) ?? list[0] ?? null;

  const dur = durationText(task, now);
  const model = modelOf(task, profiles.data);
  const pct = covPct(coverage.data);
  const fv = (n: number) => (findings.data ? n : "—");

  const reloadAll = () => {
    findings.reload();
    coverage.reload();
    rem.reload();
  };
  const pickRow = (f: LiveFinding, e: MouseEvent<HTMLButtonElement>) => {
    setSel(f.id);
    if (inspRef.current) inspRef.current.scrollTop = 0;
    e.currentTarget.focus();
    openInsp();
  };

  const listNote = pollState(findings, "발견");
  return (
    <>
      <Header
        label={label}
        pill={<PhasePill task={task} />}
        acts={
          <>
            <button className={cx(s.btn, s.sm)} type="button" onClick={reloadAll}>
              <Icon name="refresh" />
              새로고침
            </button>
            <InspOpenButton>발견 상세</InspOpenButton>
          </>
        }
        meta={
          <>
            <span>
              시작 <b>{dateTimeOf(task.created_at || task.created_unix, now) || "—"}</b>
            </span>
            <span>
              소요 <b>{dur.text}</b>
            </span>
            <span>
              커버리지 <b>{coverage.unsupported ? "미제공" : pctText(pct)}</b>
            </span>
            <span>
              모델{" "}
              <b>
                {model ? model.text : "—"}
                {model?.local ? " · 로컬" : ""}
              </b>
              {model && (model.estimated || model.local) ? (
                <>
                  {" "}
                  <EstimateTag
                    title={
                      model.estimated
                        ? "활성 프로필 정보가 없어 지정 목록 · 기본 프로필에서 짐작했습니다"
                        : "주소로 로컬 모델이라고 짐작했습니다"
                    }
                  />
                </>
              ) : null}
            </span>
            {host ? (
              <span>
                대상 <b>{host}</b> <EstimateTag title="점검 설명에서 뽑은 주소입니다(엔진에 대상 칸이 없음)" />
              </span>
            ) : null}
          </>
        }
      />
      <div className={s.vb}>
        <div className={s.vm}>
          <div className={s.strip} id="rvStrip">
            <div className={s.cell}>
              <div className={s.k}>발견</div>
              <div className={s.v}>{fv(c.total)}</div>
              {c.total ? (
                <div className={s.stack} aria-hidden="true">
                  {SEVS.filter((k) => c[k]).map((k) => (
                    <i key={k} style={{ flex: c[k], background: `var(--${k})` }} />
                  ))}
                </div>
              ) : null}
              <div className={s.sub}>
                {findings.data
                  ? `심각 ${c.crit} · 높음 ${c.hi} · 중간 ${c.mid} · 낮음 ${c.lo}${c.other ? ` · 그 밖 ${c.other}` : ""}`
                  : "—"}
              </div>
            </div>
            <div className={cx(s.cell, c.crit + c.hi > 0 && s.crit)}>
              <div className={s.k}>심각 · 높음</div>
              <div className={s.v}>{fv(c.crit + c.hi)}</div>
              <div className={s.sub}>먼저 볼 발견</div>
            </div>
            <div className={s.cell}>
              <div className={s.k}>미검토</div>
              <div className={s.v}>{fv(pendingN)}</div>
              <div className={s.sub}>처리 상태 기준 · 판정 아님</div>
            </div>
            <div className={s.cell}>
              <div className={s.k}>커버리지</div>
              <div className={s.v}>
                {pct ?? "—"}
                {pct === null ? null : <small>%</small>}
              </div>
              <div className={s.sub}>{covSub(coverage)}</div>
            </div>
          </div>
          <Unsupported what="판정 집계(확정 · 의심 · 판단 불가)" />
          <Prio rem={rem} findings={findings.data} />
          <section className={s.sec} aria-labelledby="rvListH">
            <div className={s.sh}>
              <h2 id="rvListH">발견 목록</h2>
              <span className={s.aside}>심각도 순 · 오른쪽은 처리 상태(판정 아님)</span>
            </div>
            {listNote ??
              (list.length ? (
                <div className={s.list} id="rvList">
                  {list.map((f) => {
                    const on = cur?.id === f.id;
                    return (
                      <button
                        key={f.id}
                        className={cx(s.frow, on && s.sel)}
                        type="button"
                        data-rvsel={f.id}
                        aria-current={on ? "true" : undefined}
                        onClick={(e) => pickRow(f, e)}
                      >
                        <SevBadge severity={f.severity} />
                        <span className={s.fm}>
                          <span className={s.ft}>
                            {findingTitle(f)}
                            {f.inherited ? <span className={cx(s.tag, s.ex)}>이어받음</span> : null}
                          </span>
                          <span className={s.fe}>
                            {f.assets[0] ? <span className={s.p}>{maskSecrets(f.assets[0].label)}</span> : null}
                            <span>{f.vulnclass}</span>
                          </span>
                        </span>
                        <span className={s.fs}>
                          <StatusTag status={f.status} />
                        </span>
                      </button>
                    );
                  })}
                </div>
              ) : (
                <p className={s.empty}>발견이 없습니다.</p>
              ))}
          </section>
          <section className={s.sec} aria-labelledby="rvMoreH">
            <div className={s.sh}>
              <h2 id="rvMoreH">그 밖의 보고서 항목</h2>
            </div>
            <Unsupported what="확인된 공격 경로" />
            <Unsupported what="자산 지식 그래프" />
            <p className={s.help}>엔진의 자산 그래프(coverage-graph)는 모양이 달라 이번에는 붙이지 않았습니다.</p>
          </section>
          <Download taskId={id} />
        </div>
        <aside className={s.vi} id="rvInsp" aria-label="발견 상세" ref={inspRef}>
          {cur ? (
            <FindingDetailLive key={cur.id} row={cur} host={host ?? label} enabled={visible} />
          ) : (
            <p className={s.empty}>{findings.data ? "발견이 없습니다." : "발견을 불러오면 여기에 상세가 보입니다."}</p>
          )}
        </aside>
      </div>
    </>
  );
}

/** live 보고서: 고른 점검(useSelectedTask). 없으면 점검 고르기(전체). */
export function ReportLive() {
  const { route } = useShell();
  const visible = route === "report";
  const { taskId, task, missing } = useSelectedTask();
  const feed = useLiveTasks();

  if (taskId && task) return <ReportTaskView key={task.id} task={task} visible={visible} />;

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
        <TaskPicker which="all" title="보고서를 볼 점검을 고르세요" />
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
