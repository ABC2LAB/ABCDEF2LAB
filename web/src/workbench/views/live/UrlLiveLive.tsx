"use client";

// 점검 진행 · 보고서 (시안 짜임).
// 점검 진행: 지금 하는 일 · 확인 필요(요청 승인) · 전송 통제 · 점검 계획 · 지난 활동 + 오른쪽 진행 상황.
// 보고서: 판정 집계 · 우선 조치 · 발견 목록 / 증거 · 감사 + 오른쪽 발견 상세.
// 진행률 · 남은 시간 · 심각도 · 공격 경로 · 지식 그래프 · 계정 비교 · 재현 명령은 서버가 주지 않아 두지 않는다.
// 판정은 팀 lib(summarizeDiagnosticEntries · diagnosticOutcome)의 분류만 쓰고 화면이 만들지 않는다.

import { type ReactNode, useEffect, useRef, useState } from "react";

import { urlPhaseLabels } from "@/lib/diagnosis/diagnosis-url";
import {
  type DiagnosticOutcome,
  diagnosticOutcome,
  diagnosticOutcomeLabels,
  summarizeDiagnosticEntries,
} from "@/lib/diagnosis/diagnostic-results";
import type { RemediationEntry } from "@/lib/diagnosis/remediation";
import { type RequestApprovalRunState, RUN_COUNT_ROWS } from "@/lib/diagnosis/request-approvals";

import { SEVL, type SevKey } from "../../data/findings";
import { cx } from "../../lib/cx";
import {
  type DiagnosisRunView,
  downloadDiagnostic,
  fetchCoverage,
  fetchDiagnosisRun,
  fetchRemediation,
} from "../../live/api";
import { approvalCounts, clockOf, errorText, fmtCount, isUnsupported, planState, sevKey } from "../../live/map";
import { maskSecrets } from "../../live/mask";
import { ErrorNote, LoadingRow, NotYet } from "../../live/parts";
import { useUrl, useUrlGates } from "../../live/urlDiagnosis";
import { factText, URL_ENDED_STATES, urlStateText, usdText } from "../../live/urlMap";
import { type Poll, usePoll } from "../../live/usePoll";
import { flowStageText } from "../../live/workflow";
import { useShell } from "../../shell";
import { Icon } from "../../ui/icons";
import { InspHead, InspOpenButton, Isec, Kv, PaneToggle } from "../../ui/parts";
import s from "../../workbench.module.css";
import { ApprovalFacts, ApprovalRequestCode, useRequestApprovals } from "./approvals";
import { DiagnosticEntrySections } from "./DiagnosticEntrySections";
import { reasonText, UrlFacts, UrlHead, UrlNotices, UrlPill, UrlRefreshButton, useUrlHost } from "./urlParts";
import { PauseButton, useStopConfirm, WorkflowActivity, WorkflowNotice, WorkflowReport } from "./workflowViews";

/** 보고서 위치의 상위 화면 (작업대 / 끝난 점검 / 주소 / 보고서) */
const FINISHED = { href: "#finished", label: "끝난 점검" };

/** 목록 칸에 맞춘 짧은 판정 글. */
const SHORT: Readonly<Record<DiagnosticOutcome, string>> = {
  confirmed: "확정",
  review: "검토 필요",
  not_executed: "미실행",
  unknown: "결과 불명",
  negative: "정상",
};

/** 판정 딱지 모양 (확정 = 기본, 검토 필요 = 의심 모양, 정상 = 초록, 그 밖 = 판단 불가 모양). */
export function Outcome({ o, short = false }: { o: DiagnosticOutcome; short?: boolean }) {
  return (
    <span
      className={cx(
        s.vd,
        o === "review" && s.sus,
        o === "negative" && s.neg,
        (o === "not_executed" || o === "unknown") && s.und,
      )}
    >
      {short ? SHORT[o] : diagnosticOutcomeLabels[o]}
    </span>
  );
}

/** 모델 사용량 (서버 기록). 상한은 승인 범위 예산. */
function Usage() {
  const job = useUrl((st) => st.job);
  const u = job?.model_usage;
  const b = job?.scope?.budget;
  if (!u) return <p className={s.empty}>모델 사용량 기록이 아직 없습니다.</p>;
  const of = (n: number, cap?: number) => (cap === undefined ? fmtCount(n) : `${fmtCount(n)} / ${fmtCount(cap)}`);
  // 실패·중지로 사용량을 못 받은 호출은 예약한 양으로 셌으므로, 그런 호출이 있으면 토큰·비용은 실제 사용의
  // 최대치다. 예산 상한(n / 상한)과 헷갈리지 않게 "최대치로 계산"이라고 쓴다. 실행이 중지됐는지는 상태가 따로 보인다.
  const bound = (u.unknown_usage_calls ?? 0) > 0 ? " (최대치로 계산)" : "";
  const rows: [string, string][] = [
    ["모델 요청", of(u.requests, b?.model_requests)],
    ["입력 토큰", of(u.input_tokens, b?.input_tokens) + bound],
    ["출력 토큰", of(u.output_tokens, b?.output_tokens) + bound],
    ["비용", u.usage_known ? usdText(u.cost_usd_micros) + bound : "불명"],
  ];
  if (bound) rows.push(["예약량으로 계산한 호출", `${fmtCount(u.unknown_usage_calls ?? 0)}건`]);
  return <Kv rows={rows} />;
}

/** live 점검 진행 (URL 진단). */
export function UrlLiveLive() {
  const { route, goTo } = useShell();
  const visible = route === "live";
  const handle = useUrl((st) => st.handle);
  const job = useUrl((st) => st.job);
  const verified = useUrl((st) => st.verified);
  const busy = useUrl((st) => st.busy);
  const flow = useUrl((st) => st.flow);
  const g = useUrlGates();
  const host = useUrlHost();
  const stop = useStopConfirm("종료");
  const a = useRequestApprovals(job?.task_id ?? "", visible && g.runReadable);

  if (!handle || !job) {
    return (
      <>
        <UrlHead
          title="점검 진행"
          crumb="점검 진행"
          host={host}
          phase="live"
          acts={handle ? <UrlRefreshButton /> : undefined}
        />
        <div className={s.vm}>
          {handle ? (
            <>
              {busy ? <LoadingRow text="진단 상태를 확인하는 중…" /> : null}
              <UrlNotices />
            </>
          ) : (
            <>
              <p className={s.empty}>이 브라우저에서 이어 보는 점검이 없습니다.</p>
              <UrlNotices />
              <div>
                <a className={cx(s.btn, s.pri)} href="#new">
                  <Icon name="plus" />새 점검
                </a>
              </div>
            </>
          )}
        </div>
      </>
    );
  }

  const ended = URL_ENDED_STATES.includes(job.state);
  const run = a.poll.data?.run;
  const ap = approvalCounts(run, a.poll.data?.approvals);
  const known = (n: number) => (ap.known ? fmtCount(n) : "—");
  const runningPlan = flow?.plan?.find((p) => planState(p.state).row === "run");
  const nowText =
    runningPlan?.title ?? flow?.current_path ?? (flow ? flowStageText[flow.stage] : urlPhaseLabels[job.phase]);

  let done: ReactNode = null;
  if (g.reportReadable) {
    done = (
      <div className={s.ldone}>
        <div className={s.dm}>
          <b>
            <Icon name="check" />
            <span>점검을 마쳤습니다</span>
          </b>
          <span>{`${urlStateText(job.state)} · 결과 저장 확인`}</span>
        </div>
        <button className={cx(s.btn, s.pri)} type="button" onClick={() => goTo("report")}>
          보고서 보기
          <Icon name="arrow" />
        </button>
      </div>
    );
  } else if (ended) {
    done = (
      <div className={cx(s.note, s.warn)}>
        <Icon name="alert" />
        <span>{`점검이 끝났습니다 (${urlStateText(job.state, verified)}) · 결과 저장 ${factText(job.result_ready)}`}</span>
      </div>
    );
  } else if (job.state === "unknown") {
    done = (
      <div className={cx(s.note, s.warn)} role="note" id="luUnknown">
        <Icon name="alert" />
        <span>{`결과 불명 · 끝난 것으로 보지 않고 자동으로 다시 시작하지 않습니다. 엔진 시작 ${factText(job.started)} · 마감 ${factText(job.drain_complete)}`}</span>
      </div>
    );
  } else if (job.state === "ready" || job.state === "awaiting_approval") {
    done = (
      <div className={s.note} role="note">
        <Icon name="info" />
        <span className={s["lv-grow"]}>아직 시작하지 않은 점검입니다.</span>
        <button className={cx(s.btn, s.sm)} type="button" onClick={() => goTo("new")}>
          시작하러 가기
        </button>
      </div>
    );
  }

  const c = run?.execution?.counts;
  let sendWaitText = "엔진이 시작되면 나옵니다.";
  if (job.drain_complete) sendWaitText = "마감된 점검의 전송 기록은 보고서의 증거 · 감사에 있습니다.";
  else if (job.state === "unknown" || !verified)
    sendWaitText = "결과 불명인 점검은 전송 상태를 이어 조회하지 않습니다. 상태를 다시 조회하세요.";
  const visited = flow?.counts.visited ?? 0;
  const selected = flow?.endpoints.filter((e) => e.selected).length ?? 0;

  return (
    <>
      <UrlHead
        title="점검 진행"
        crumb="점검 진행"
        host={host}
        phase="live"
        pill={<UrlPill job={job} verified={verified} />}
        acts={
          <>
            <PauseButton />
            {stop.button}
            <InspOpenButton>진행 상황</InspOpenButton>
          </>
        }
      />
      <div className={s.vb}>
        <div className={s.vm}>
          {stop.box}
          <UrlNotices />
          <WorkflowNotice />
          {job.reason_code ? <p className={s.help}>{`서버 사유: ${reasonText(job.reason_code)}`}</p> : null}
          {job.diagnostic_log_incomplete ? (
            <p className={s.help}>{reasonText("diagnostic_logging_incomplete")}</p>
          ) : null}
          {!ended && job.state !== "unknown" ? (
            <div className={s.band}>
              {/* 점검 진행률 % · 막대 (시안 v22): 서버에 진행률이 없어 칸만 둔다 */}
              {/* biome-ignore lint/a11y/useSemanticElements: 시안 마크업 그대로 */}
              <div className={s.pct} id="lPct" role="group" aria-label="점검 진행률">
                <NotYet why="서버가 점검 진행률을 주지 않습니다" />
              </div>
              <div className={s.bt}>
                <div className={s["now-l"]} aria-live="polite">
                  <span className={cx(s["now-k"], flow?.paused && s.idle)}>
                    <i />
                    {flow?.paused ? "일시정지" : "지금 하는 일"}
                  </span>
                  <span id="lTask">{nowText}</span>
                </div>
                <div className={s.pmeta} id="lMeta">
                  <span>
                    단계 <b>{urlPhaseLabels[job.phase]}</b>
                  </span>
                  {flow ? (
                    <span>
                      심화 점검 <b>{`${flow.deep_closed}/${flow.deep_runs}`}</b> 마감
                    </span>
                  ) : null}
                  {job.model_usage ? (
                    <span>
                      모델 요청 <b>{fmtCount(job.model_usage.requests)}</b>
                      {job.scope ? ` / ${fmtCount(job.scope.budget.model_requests)}` : ""}
                    </span>
                  ) : null}
                  {flow ? (
                    <span>
                      엔드포인트 <b>{flow.endpoints.length}</b>
                      {` (심화 선택 ${selected})`}
                    </span>
                  ) : null}
                </div>
              </div>
            </div>
          ) : null}
          {done}
          {a.pending.map((r) => (
            <div className={s.decide} key={r.id} id="lDecide">
              <div className={s.dm}>
                <b>
                  <span className={cx(s.pill, s.wait)}>확인 필요</span>
                  <span className={cx(s.m, s[r.method])}>{r.method}</span>
                  <span className={s.p}>{r.path}</span>
                </b>
                <span>{flow?.plan?.find((p) => p.id === r.intent_id)?.title ?? "계획 제목 없음"}</span>
              </div>
              <div className={s["lv-aprev"]}>
                <ApprovalRequestCode row={r} />
                <ApprovalFacts row={r} />
              </div>
              <div className={s.acts}>
                <button
                  className={cx(s.btn, s.pri, s.sm)}
                  type="button"
                  disabled={!a.canDecide(r)}
                  onClick={() => void a.decide(r, "allow")}
                >
                  {a.busy === r.id ? "보내는 중…" : "승인하고 보내기"}
                </button>
                <button
                  className={cx(s.btn, s.sm)}
                  type="button"
                  disabled={!a.canDecide(r)}
                  onClick={() => void a.decide(r, "deny")}
                >
                  거부
                </button>
              </div>
            </div>
          ))}
          {a.message ? <p role="status">{a.message}</p> : null}
          {a.unresolved ? <p role="alert">결정 결과 불명 · 조회로 확인하기 전에는 다시 결정하지 않습니다.</p> : null}
          <section className={s.sec} aria-labelledby="lSendH">
            <div className={s.sh}>
              <h2 id="lSendH">전송 통제</h2>
              {c ? (
                <span
                  className={s.aside}
                >{`요청 접수 ${fmtCount(c.attempts)} · 대상 전송 ${fmtCount(c.forward_attempted)}`}</span>
              ) : null}
            </div>
            {/* 시안 v22 머리 옆 줄: 범위 밖 전송 · 승인 없는 상태 변경 수는 서버 집계에 없어 칸만 둔다 */}
            <p className={s.help}>
              {"범위 밖으로 보낸 요청 · 승인 없이 보낸 상태 변경 "}
              <NotYet why="서버 전송 집계에 이 두 수가 따로 없습니다" />
            </p>
            {g.runReadable && a.poll.error && !a.poll.data ? (
              <ErrorNote error={a.poll.error} onRetry={a.poll.reload} />
            ) : null}
            {g.runReadable && !(a.poll.error && !a.poll.data) ? (
              <div className={s.meters}>
                <div className={s.cell}>
                  <div className={s.k}>허용 범위 안 전송</div>
                  <div className={s.v} id="luAllowed">
                    {known(ap.forwarded)}
                  </div>
                  <div className={s.sub}>{`허용 판정 ${known(ap.allowed)}`}</div>
                </div>
                <div className={s.cell}>
                  <div className={s.k}>막은 요청</div>
                  <div className={s.v}>{known(ap.blocked)}</div>
                  <div className={s.sub}>범위 밖 · 안전 규칙</div>
                </div>
                <div className={s.cell}>
                  <div className={s.k}>승인 필요로 보류</div>
                  <div className={s.v}>{fmtCount(ap.held)}</div>
                  <div className={s.sub}>쓰기 메서드 · 전송 전 확인</div>
                </div>
                <div className={s.cell}>
                  <div className={s.k}>전송 불명</div>
                  <div className={s.v}>{known(ap.unknown)}</div>
                  <div className={s.sub}>응답 없음</div>
                </div>
              </div>
            ) : null}
            {g.runReadable ? null : <p className={s.empty}>{sendWaitText}</p>}
          </section>
          {job.input.workflow_mode ? <WorkflowActivity /> : null}
        </div>
        <aside className={s.vi} id="lInsp" aria-label="진행 상황">
          <InspHead
            eyebrow={
              <>
                {`${urlStateText(job.state, verified)} · `}
                <span className={s.host}>{host}</span>
              </>
            }
            title="진행 상황"
          />
          <Isec t="결정 대기">
            {a.pending.length ? (
              <p>{`전송 전 확인이 필요한 요청 ${a.pending.length}건을 승인하거나 거부해 주세요.`}</p>
            ) : (
              <p className={s.help}>결정할 요청이 없습니다.</p>
            )}
          </Isec>
          {flow ? (
            <Isec t="탐색 현황">
              <div className={s.vbar}>
                <span>
                  방문 <b>{visited}</b> / {flow.counts.found} 경로
                </span>
                <span>
                  심화 선택 <b>{selected}</b>
                </span>
              </div>
            </Isec>
          ) : null}
          <Isec t="모델 사용량">
            <Usage />
          </Isec>
          <Isec t="점검 정보">
            <UrlFacts job={job} />
          </Isec>
        </aside>
        <PaneToggle />
      </div>
    </>
  );
}

// ---------- 보고서 ----------

const RUN_PHASE: Readonly<Record<RequestApprovalRunState["phase"], string>> = {
  prepared: "준비됨 · 실행 전",
  running: "실행 중",
  stopping: "중지 처리 중",
  closed: "종료됨",
  failed: "실패",
};
const RUN_OUTCOME: Readonly<Record<RequestApprovalRunState["outcome"], string>> = {
  "": "결과 미기록",
  completed: "정상 종료",
  cancelled: "취소로 종료",
  failed: "실패로 종료",
};
/** 우선 조치 · 목록 순서: 확정 → 검토 필요 → 미실행 → 결과 불명 → 정상 대조. */
export const ORDER: readonly DiagnosticOutcome[] = ["confirmed", "review", "not_executed", "unknown", "negative"];
/** 위험도 순서 (시안 v20 "위험도와 판정 순"). 서버가 심각도를 안 줬으면 맨 뒤. */
const SEV_KEYS: readonly SevKey[] = ["crit", "hi", "mid", "lo"];
const sevRank = (e: RemediationEntry) => {
  const k = sevKey(e.severity ?? "");
  return k ? SEV_KEYS.indexOf(k) : SEV_KEYS.length;
};
/** 위험도 딱지. 서버가 심각도를 주지 않았으면 "등급 없음". */
function Sev({ e }: { e: RemediationEntry }) {
  const k = sevKey(e.severity ?? "");
  return k ? <span className={cx(s.sev, s[k])}>{SEVL[k]}</span> : <span className={s.sev}>등급 없음</span>;
}

/** 증거 글을 읽기 좋게: JSON 이면 들여쓰고, 안에 든 JSON 문자열(tool_output 등)도 펼친다. 비밀 값은 가린다. */
export function prettyEvidence(raw: string): string {
  const unwrap = (v: unknown, depth = 0): unknown => {
    if (depth > 8) return v;
    if (typeof v === "string") {
      const t = v.trim();
      if ((t.startsWith("{") && t.endsWith("}")) || (t.startsWith("[") && t.endsWith("]"))) {
        try {
          return unwrap(JSON.parse(t), depth + 1);
        } catch {
          return v;
        }
      }
      return v;
    }
    if (Array.isArray(v)) return v.map((x) => unwrap(x, depth + 1));
    if (v && typeof v === "object")
      return Object.fromEntries(Object.entries(v).map(([k, x]) => [k, unwrap(x, depth + 1)]));
    return v;
  };
  const value = unwrap(raw);
  return maskSecrets(typeof value === "string" ? value : JSON.stringify(value, null, 2));
}

/** 감사 완결 여부 글. */
function auditText(e: RequestApprovalRunState["execution"]): string {
  if (!e) return "—";
  return e.audit_complete ? "완결" : "미완결";
}

/** 저장된 실행 종료 기록 (증거 · 감사 탭). */
function RunRecord({ run }: { run: Poll<DiagnosisRunView> }) {
  const d = run.data;
  let body: ReactNode;
  if (!d && run.error) body = <ErrorNote error={run.error} onRetry={run.reload} />;
  else if (!d) body = <LoadingRow />;
  else if (d.kind === "none") body = <p className={s.empty}>저장된 실행 종료 기록이 없습니다.</p>;
  else if (d.kind === "not_run") body = <p className={s.empty}>실행 종료 기록 없음 · 준비만 기록됨</p>;
  else if (d.kind === "unavailable") body = <p className={s.empty}>이 점검의 실행 기록을 확인할 수 없습니다.</p>;
  else {
    const r = d.run;
    const e = r.execution;
    body = (
      <>
        <Kv
          rows={[
            ["단계", RUN_PHASE[r.phase] ?? String(r.phase)],
            ["결과", RUN_OUTCOME[r.outcome] ?? String(r.outcome)],
            [
              "마감",
              [
                r.drain_complete ? "진행 중 요청 정리 완료" : "진행 중 요청 정리 미완료",
                r.stop_requested ? "중지 요청됨" : "",
                r.closure_failed ? "마감 실패" : "",
              ]
                .filter(Boolean)
                .join(" · "),
            ],
            [
              "실행 ID",
              <span key="run" className={s.mono}>
                {r.run_ref || "—"}
              </span>,
            ],
            ["감사", auditText(e)],
            [
              "저장 시각",
              d.savedAt ? (
                <span key="saved" className={s.mono}>
                  {d.savedAt}
                </span>
              ) : (
                "—"
              ),
            ],
          ]}
        />
        {e ? (
          <div className={s.meters}>
            {RUN_COUNT_ROWS.map(({ key, label }) => (
              <div key={key} className={s.cell}>
                <div className={s.k}>{label}</div>
                <div className={s.v}>{fmtCount(e.counts[key])}</div>
              </div>
            ))}
          </div>
        ) : null}
      </>
    );
  }
  return (
    <section className={s.sec} aria-label="실행 종료 기록">
      <div className={s.sh}>
        <h2>실행 종료 기록</h2>
      </div>
      {body}
    </section>
  );
}

type Audience = "shared" | "internal";
type Format = "html" | "docx";
const AUD_TEXT: Readonly<Record<Audience, string>> = { shared: "공유용", internal: "내부 상세" };
/** 내려받는 동안 버튼 글 (Word 는 어느 파일인지 남긴다). */
const busyText = (label: string, format: Format) => (format === "docx" ? `${label} 내려받는 중…` : "내려받는 중…");

/** 보고서 파일 내려받기: 같은 저장 자료로 만든 종합 HTML · Word, 각각 공유용 · 내부 상세. */
export function Download({ taskId }: { taskId: string }) {
  const [st, setSt] = useState<{
    busy: `${Audience}:${Format}` | null;
    err: unknown;
    done: { aud: Audience; format: Format } | null;
  }>({ busy: null, err: null, done: null });
  const ctrl = useRef<AbortController | null>(null);
  useEffect(() => () => ctrl.current?.abort(), []);
  const run = async (aud: Audience, format: Format) => {
    ctrl.current?.abort();
    const c = new AbortController();
    ctrl.current = c;
    setSt({ busy: `${aud}:${format}`, err: null, done: null });
    try {
      await downloadDiagnostic(taskId, aud, c.signal, format);
      if (!c.signal.aborted) setSt({ busy: null, err: null, done: { aud, format } });
    } catch (err) {
      if (!c.signal.aborted) setSt({ busy: null, err, done: null });
    }
  };
  const btn = (aud: Audience, format: Format, label: string, pri = false) => (
    <button
      className={cx(s.btn, s.sm, pri && s.pri)}
      type="button"
      disabled={!!st.busy}
      onClick={() => void run(aud, format)}
    >
      {st.busy === `${aud}:${format}` ? busyText(label, format) : label}
    </button>
  );
  return (
    <section className={s.sec} aria-labelledby="rvDlH">
      <div className={s.sh}>
        <h2 id="rvDlH">내려받기</h2>
      </div>
      {st.err && isUnsupported(st.err) ? (
        <p className={s.empty}>이 점검은 보고서 파일을 제공하지 않습니다.</p>
      ) : (
        <div className={s.acts}>
          {btn("shared", "html", "공유용 사본", true)}
          {btn("internal", "html", "내부 상세 사본")}
          {btn("shared", "docx", "Word 공유용")}
          {btn("internal", "docx", "Word 내부 상세")}
        </div>
      )}
      {st.err && !isUnsupported(st.err) ? <p role="alert">{errorText(st.err)}</p> : null}
      {st.done ? (
        <p className={s.help} role="status">
          {`${st.done.format === "docx" ? "Word" : "HTML"} ${AUD_TEXT[st.done.aud]} 파일을 저장했습니다.`}
        </p>
      ) : null}
      <p className={s.help}>
        종합 HTML · Word 보고서를 같은 저장 자료로 만듭니다. 공유용은 서버가 자유문장까지 가립니다. 내부 상세는 외부로
        보내기 전에 민감정보를 검토하세요.
      </p>
    </section>
  );
}

/** 발견 상세 (오른쪽). 대시보드도 같이 쓴다. */
export function FindingDetail({
  e,
  host,
  taskId,
  onReload,
}: {
  e: RemediationEntry | undefined;
  host: string | null;
  taskId: string;
  onReload: () => void;
}) {
  if (!e) {
    return (
      <>
        <InspHead title="발견 상세" />
        <p className={s.empty}>저장된 발견이 없습니다.</p>
      </>
    );
  }
  const o = diagnosticOutcome(e);
  return (
    <>
      <InspHead
        eyebrow={
          <>
            {`${diagnosticOutcomeLabels[o]} · `}
            <span className={s.host}>{host}</span>
          </>
        }
        title={maskSecrets(e.title) || e.entry_id}
        chips={<Outcome o={o} />}
      />
      {!e.available ? (
        <div className={cx(s.note, s.warn)} role="note">
          <Icon name="alert" />
          <span>{maskSecrets(e.unavailable_reason)}</span>
        </div>
      ) : null}
      <DiagnosticEntrySections
        key={`${taskId}:${e.entry_id}:${e.source_digest}:${e.record?.id ?? 0}:${e.record?.bundle?.input_digest ?? ""}`}
        task={taskId}
        entry={e}
        onReload={onReload}
      />
    </>
  );
}

/** live 보고서 (URL 진단): 결과 저장이 확인된 실제 task 만. */
export function UrlReportLive() {
  const { route, goTo, openInsp } = useShell();
  const visible = route === "report";
  const handle = useUrl((st) => st.handle);
  const job = useUrl((st) => st.job);
  const verified = useUrl((st) => st.verified);
  const cap = useUrl((st) => st.cap);
  const g = useUrlGates();
  const host = useUrlHost();
  const taskId = g.reportReadable && job?.task_id ? job.task_id : null;
  const rem = usePoll(() => fetchRemediation(taskId as string, "internal"), [taskId], {
    enabled: visible && !!taskId,
  });
  const run = usePoll(() => fetchDiagnosisRun(taskId as string), [taskId], { enabled: visible && !!taskId });
  // 자산 커버리지 (시안 v20 머리 줄 · 요약 칸): 서버 task 의 방문 · 검사한 자산 비율(판정 커버리지 아님 · 리뷰 #479).
  // 꺼졌거나 못 읽으면 "아직 구현 안 됨"으로 둔다
  const cov = usePoll(() => fetchCoverage(taskId as string), [taskId], { enabled: visible && !!taskId });
  const covPct = cov.data?.enabled && cov.data.pct !== null ? Math.round(cov.data.pct * 100) : null;
  const [tab, setTab] = useState<"list" | "graph" | "map" | "ev">("list");
  const [sel, setSel] = useState<string | null>(null);
  const title = host ? `${host} 보고서` : "보고서";

  if (!handle || !job || !taskId) {
    return (
      <>
        <UrlHead
          title={title}
          crumb="보고서"
          parent={FINISHED}
          host={host}
          pill={job ? <UrlPill job={job} verified={verified} /> : null}
          acts={handle ? <UrlRefreshButton /> : undefined}
        />
        <div className={s.vm}>
          {handle && job ? (
            <div className={s.note} role="note">
              <Icon name="info" />
              <span
                className={s["lv-grow"]}
              >{`점검이 끝나고 결과가 저장되면 보고서가 나옵니다 · 지금 ${urlStateText(job.state, verified)}`}</span>
              <button className={cx(s.btn, s.sm)} type="button" onClick={() => goTo("live")}>
                점검 진행 보기
              </button>
            </div>
          ) : (
            <p className={s.empty}>
              {handle ? "진단 상태를 확인하지 못했습니다." : "이 브라우저에서 이어 보는 점검이 없습니다."}
            </p>
          )}
          <UrlNotices />
        </div>
      </>
    );
  }

  const entries = rem.data?.entries ?? [];
  const sorted = [...entries].sort((x, y) => ORDER.indexOf(diagnosticOutcome(x)) - ORDER.indexOf(diagnosticOutcome(y)));
  const counts = rem.data ? summarizeDiagnosticEntries(entries) : null;
  const findings = sorted.filter((e) => diagnosticOutcome(e) !== "negative");
  // 우선 조치: 시안 v20 처럼 위험도 → 판정 순
  const prio = sorted
    .filter((e) => ["confirmed", "review"].includes(diagnosticOutcome(e)) && (e.remediation || e.record?.bundle))
    .sort(
      (x, y) => sevRank(x) - sevRank(y) || ORDER.indexOf(diagnosticOutcome(x)) - ORDER.indexOf(diagnosticOutcome(y)),
    );
  const bySev = SEV_KEYS.map((k) => [k, findings.filter((e) => sevKey(e.severity ?? "") === k).length] as const);
  const noSev = findings.filter((e) => !sevKey(e.severity ?? "")).length;
  const chosen = sorted.find((e) => e.entry_id === sel) ?? findings[0] ?? sorted[0];
  const profile = cap.kind === "v2" ? cap.catalog.profiles.find((p) => p.id === job.input.profile_id) : undefined;
  const u = job.model_usage;

  let listBody: ReactNode;
  if (!rem.data && rem.error) listBody = <ErrorNote error={rem.error} onRetry={rem.reload} />;
  else if (!rem.data) listBody = <LoadingRow />;
  else if (!sorted.length)
    listBody = <p className={s.empty}>저장된 결과가 없습니다. 결과가 없다고 안전하다는 뜻은 아닙니다.</p>;
  else
    listBody = (
      <div className={s.list} id="rList">
        {sorted.map((e) => {
          const o = diagnosticOutcome(e);
          const on = chosen?.entry_id === e.entry_id;
          return (
            <button
              key={e.entry_id}
              className={cx(s.frow, on && s.sel)}
              type="button"
              data-rsel={e.entry_id}
              aria-current={on ? "true" : undefined}
              onClick={() => {
                setSel(e.entry_id);
                openInsp();
              }}
            >
              <Sev e={e} />
              <span className={s.fm}>
                <span className={s.ft}>{maskSecrets(e.title) || e.entry_id}</span>
                {e.summary ? <span className={s.fe}>{maskSecrets(e.summary)}</span> : null}
              </span>
              <span className={s.fs}>
                <Outcome o={o} short />
                <span className={s["r-time"]}>{clockOf(e.observed_at)}</span>
              </span>
            </button>
          );
        })}
      </div>
    );

  return (
    <>
      <UrlHead
        title={title}
        crumb="보고서"
        parent={FINISHED}
        host={host}
        pill={<UrlPill job={job} verified={verified} />}
        acts={
          <>
            <button
              className={s.btn}
              type="button"
              onClick={() => {
                rem.reload();
                run.reload();
              }}
            >
              <Icon name="refresh" />
              새로고침
            </button>
            <InspOpenButton>발견 상세</InspOpenButton>
          </>
        }
        meta={
          <>
            {job.scope ? (
              <span>
                대상 <b>{job.scope.origin}</b>
              </span>
            ) : null}
            <span>
              시작 <NotYet why="서버가 점검 시작 시각을 주지 않습니다" />
            </span>
            <span>
              소요 <NotYet why="서버가 점검 소요 시간을 주지 않습니다" />
            </span>
            <span>
              자산 커버리지{" "}
              {covPct !== null ? (
                <b>{`${covPct}%`}</b>
              ) : (
                <NotYet why="서버가 이 점검의 자산 커버리지를 주지 않습니다" />
              )}
            </span>
            {profile ? (
              <span>
                모델 <b>{`${profile.model_label} · ${profile.model?.external_transfer ? "외부" : "로컬"}`}</b>
              </span>
            ) : null}
            {u ? (
              <span>
                모델 요청 <b>{fmtCount(u.requests)}</b>
              </span>
            ) : null}
            <span>
              task <b>{`#${taskId}`}</b>
            </span>
          </>
        }
      />
      <div className={s.vb}>
        <div className={s.vm}>
          {counts ? (
            // 요약 칸 (시안 v20 짜임: 발견 · 확정 · 검토 필요/결과 불명 · 자산 커버리지). 미실행은 셋째 칸 아래에 둔다
            <section className={s.strip} aria-label="판정 집계">
              <div className={s.cell}>
                <div className={s.k}>발견 · 검토 대상</div>
                <div className={s.v}>{findings.length}</div>
                <div className={s.sub}>
                  {[...bySev.map(([k, n]) => `${SEVL[k]} ${n}`), ...(noSev ? [`등급 없음 ${noSev}`] : [])].join(" · ")}
                </div>
              </div>
              <div className={cx(s.cell, counts.confirmed > 0 && s.crit)}>
                <div className={s.k}>{diagnosticOutcomeLabels.confirmed}</div>
                <div className={s.v}>{counts.confirmed}</div>
                <div className={s.sub}>서버 판정 확정</div>
              </div>
              <div className={s.cell}>
                <div className={s.k}>{`${diagnosticOutcomeLabels.review} / ${diagnosticOutcomeLabels.unknown}`}</div>
                <div className={s.v}>
                  {counts.review}
                  <small>{`/ ${counts.unknown}`}</small>
                </div>
                <div
                  className={s.sub}
                >{`${diagnosticOutcomeLabels.not_executed} ${counts.not_executed} · 추가 확인 권장`}</div>
              </div>
              <div className={s.cell}>
                <div className={s.k}>자산 커버리지</div>
                <div className={s.v}>
                  {covPct !== null ? (
                    <>
                      {covPct}
                      <small>%</small>
                    </>
                  ) : (
                    <NotYet why="서버가 이 점검의 자산 커버리지를 주지 않습니다" />
                  )}
                </div>
                <div className={s.sub}>{covPct !== null ? `미검사 ${100 - covPct}%` : "방문 · 검사한 자산 비율"}</div>
              </div>
            </section>
          ) : null}
          {prio.length ? (
            <section className={s.sec} aria-labelledby="rPrioH">
              <div className={s.sh}>
                <h2 id="rPrioH">우선 조치</h2>
                <span className={s.aside}>위험도와 판정 순</span>
              </div>
              <ol className={s.prio} id="rvPrio">
                {prio.map((e) => {
                  const b = e.record && !e.record.stale ? e.record.bundle : undefined;
                  const fix = b?.immediate_mitigation[0] ?? b?.root_fix[0] ?? e.remediation;
                  return (
                    <li key={e.entry_id}>
                      <span className={s.pt}>{maskSecrets(e.title) || e.entry_id}</span>
                      <span className={s.pc}>
                        <Sev e={e} />
                        <Outcome o={diagnosticOutcome(e)} />
                      </span>
                      <span className={s.pf}>{maskSecrets(fix)}</span>
                    </li>
                  );
                })}
              </ol>
            </section>
          ) : null}
          <section className={s.sec}>
            <div className={s["seg-tabs"]} role="tablist" aria-label="보고서 내용" id="rTabs">
              <button type="button" role="tab" aria-selected={tab === "list"} onClick={() => setTab("list")}>
                발견 목록<span className={s.c}>{sorted.length}</span>
              </button>
              <button type="button" role="tab" aria-selected={tab === "graph"} onClick={() => setTab("graph")}>
                자산 지식 그래프
              </button>
              <button type="button" role="tab" aria-selected={tab === "map"} onClick={() => setTab("map")}>
                접근통제 지도
              </button>
              <button type="button" role="tab" aria-selected={tab === "ev"} onClick={() => setTab("ev")}>
                증거 · 감사
              </button>
            </div>
            {tab === "list" ? (
              <div role="tabpanel" aria-label="발견 목록">
                {listBody}
              </div>
            ) : null}
            {tab === "graph" ? (
              // 시안 v20 자산 지식 그래프: 보고서에 붙일 그래프 API 가 아직 없다
              <div role="tabpanel" aria-label="자산 지식 그래프">
                <NotYet block why="보고서에 붙일 자산 지식 그래프 서버 연결이 없습니다" />
              </div>
            ) : null}
            {tab === "map" ? (
              // 시안 v20 접근통제 지도: 실제 지도는 KG API 연결 뒤. 지금은 따로 있는 접근통제 지도 화면(예시)으로 간다
              <div
                role="tabpanel"
                aria-label="접근통제 지도"
                style={{ display: "flex", flexDirection: "column", gap: 12 }}
              >
                <NotYet block why="이 점검의 접근통제 지도 서버 연결이 없습니다" />
                <div>
                  <button className={s.btn} type="button" onClick={() => goTo("accessmap")}>
                    접근통제 지도 화면 열기
                    <Icon name="arrow" />
                  </button>
                </div>
              </div>
            ) : null}
            {tab === "ev" ? (
              <div
                role="tabpanel"
                aria-label="증거 · 감사"
                style={{ display: "flex", flexDirection: "column", gap: 26 }}
              >
                <RunRecord run={run} />
                <WorkflowReport />
                <section className={s.sec} aria-labelledby="rUseH">
                  <div className={s.sh}>
                    <h2 id="rUseH">모델 사용량</h2>
                  </div>
                  <Usage />
                </section>
                <Download taskId={taskId} />
              </div>
            ) : null}
          </section>
        </div>
        <aside className={s.vi} id="rInsp" aria-label="발견 상세">
          <FindingDetail e={chosen} host={host} taskId={taskId} onReload={rem.reload} />
        </aside>
        <PaneToggle />
      </div>
    </>
  );
}
