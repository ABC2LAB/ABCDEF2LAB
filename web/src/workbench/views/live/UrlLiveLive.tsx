"use client";

// URL 진단(v2) 시작 뒤 두 화면: 점검 진행(서버 단계 · 사실 · 모델 사용량 · 중지 + 실제 task 의 전송 통제 집계)과
// 보고서(결과 저장이 확인된 실제 task 의 우선 조치 · 내려받기). URL 진단 서버는 결속된 task 의
// 요청별 승인 · 실행 기록 · 보고서만 열어 주므로, 의도 · 활동 · 발견 목록 · 커버리지는 '이 서버 모드 미제공'으로 둔다.

import type { ReactNode } from "react";

import { diagnosticOutcomeLabels, summarizeDiagnosticEntries } from "@/abc2lab-ui/lib/diagnostic-results";
import { type RequestApprovalRunState, RUN_COUNT_NOTE, RUN_COUNT_ROWS } from "@/abc2lab-ui/lib/request-approvals";

import { cx } from "../../lib/cx";
import {
  type DiagnosisRunView,
  fetchDiagnosisRun,
  fetchRemediation,
  fetchRequestApprovals,
  type Remediation,
} from "../../live/api";
import { approvalCounts, fmtCount } from "../../live/map";
import { ErrorNote, LoadingRow, ModeNote } from "../../live/parts";
import { runUrlAction, useUrl, useUrlGates } from "../../live/urlDiagnosis";
import { factText, isUrlActive, URL_ENDED_STATES, urlStateText } from "../../live/urlMap";
import { type Poll, usePoll } from "../../live/usePoll";
import { useShell } from "../../shell";
import { Icon } from "../../ui/icons";
import { InspHead, InspOpenButton, Isec, Kv } from "../../ui/parts";
import s from "../../workbench.module.css";
import { Download, Prio } from "./ReportLive";
import {
  reasonText,
  UrlDisconnect,
  UrlFacts,
  UrlHead,
  UrlNoJob,
  UrlNotices,
  UrlPhaseBar,
  UrlPill,
  UrlRefreshButton,
  UrlUsage,
  useUrlHost,
} from "./urlParts";

/** 진행 중 실제 task 의 요청별 승인 상태 재조회 간격(ms). 끝났으면 진입 때 한 번. */
const APPROVALS_EVERY = 5000;

/** 전송 통제 (실제 task 의 요청별 승인 · 감독자 집계). 엔진 시작이 확인된 뒤에만 부른다. */
function SendControl({ taskId, active, visible }: { taskId: string; active: boolean; visible: boolean }) {
  const job = useUrl((st) => st.job);
  const approvals = usePoll(() => fetchRequestApprovals(taskId), [taskId], {
    intervalMs: active ? APPROVALS_EVERY : 0,
    enabled: visible,
  });
  const everyRequest = job?.scope?.approval_mode === "confirm_every_request";
  let body: ReactNode;
  if (approvals.unsupported) body = <ModeNote what="전송 통제" />;
  else if (!approvals.data && approvals.error) body = <ErrorNote error={approvals.error} onRetry={approvals.reload} />;
  else if (!approvals.data) body = <LoadingRow />;
  else {
    const ap = approvalCounts(approvals.data.run, approvals.data.approvals);
    const known = (n: number) => (ap.known ? fmtCount(n) : "—");
    body = (
      <>
        <div className={s.meters}>
          <div className={s.cell}>
            <div className={s.k}>허용 판정</div>
            <div className={s.v} id="luAllowed">
              {known(ap.allowed)}
            </div>
            <div className={s.sub}>{`실제 전송 아님 · 대상 전송 시도 ${known(ap.forwarded)}`}</div>
          </div>
          <div className={s.cell}>
            <div className={s.k}>막은 요청</div>
            <div className={s.v}>{known(ap.blocked)}</div>
            <div className={s.sub}>차단 + 미지원</div>
          </div>
          <div className={s.cell}>
            <div className={s.k}>승인 필요로 보류</div>
            <div className={s.v}>{fmtCount(ap.held)}</div>
            <div className={s.sub}>승인 대기 중</div>
          </div>
          <div className={s.cell}>
            <div className={s.k}>전송 불명</div>
            <div className={s.v}>{known(ap.unknown)}</div>
            <div className={s.sub}>결과를 모름</div>
          </div>
        </div>
        <p className={s.help}>
          {ap.known
            ? "엔진 실행 감독자가 센 요청 수입니다. 허용 판정에는 보내기 전에 취소된 요청도 들어가며, 실제로 보낸 수는 대상 전송 시도입니다."
            : "실행 정보가 아직 없어 허용 · 막음 · 불명은 비워 둡니다."}
        </p>
      </>
    );
  }
  const c = approvals.data?.run.execution?.counts;
  return (
    <section className={s.sec} aria-labelledby="luSendH">
      <div className={s.sh}>
        <h2 id="luSendH">전송 통제</h2>
        {c ? (
          <span className={s.aside}>
            {`요청 접수 ${fmtCount(c.attempts)} · 대상 전송 시도 ${fmtCount(c.forward_attempted)}`}
          </span>
        ) : null}
      </div>
      {body}
      {everyRequest ? (
        <div className={s.note} role="note">
          <Icon name="info" />
          <span>
            이 진단은 매 요청 확인 방식입니다. 요청별 허용 · 거절은 아직 작업대에 없어 팀 화면의 점검 진행(실제 HTTP
            요청 승인)에서 결정합니다.
          </span>
        </div>
      ) : null}
    </section>
  );
}

/** 전송 통제를 읽지 않을 때의 안내 (마감 · 결과 불명 · 시작 전). */
function sendPlaceholder(job: { drain_complete: boolean; state: string }, uncertain: boolean): string {
  if (job.drain_complete)
    return "실행이 마감됐습니다. 마감된 회차의 요청 · 전송 집계는 보고서의 실행 종료 기록에서 봅니다.";
  if (job.state === "unknown" || uncertain)
    return "결과 불명인 진단은 실행 감독자 상태를 이어 조회하지 않습니다. 상태 다시 조회로 서버 사실만 확인하세요.";
  return "엔진 시작이 확인되면 실제 task 의 전송 통제 집계를 봅니다.";
}

/** live 점검 진행 (URL 진단). */
export function UrlLiveLive() {
  const { route, goTo } = useShell();
  const visible = route === "live";
  const handle = useUrl((st) => st.handle);
  const job = useUrl((st) => st.job);
  const verified = useUrl((st) => st.verified);
  const busy = useUrl((st) => st.busy);
  const uncertain = useUrl((st) => st.uncertain);
  const g = useUrlGates();
  const host = useUrlHost();

  const pill = job ? <UrlPill job={job} verified={verified} /> : null;
  if (!handle || !job) {
    return (
      <>
        <UrlHead title="점검 진행" crumb="점검 진행" host={host} acts={handle ? <UrlRefreshButton /> : undefined} />
        <div className={s.vm}>
          {handle ? (
            <>
              {busy ? <LoadingRow text="진단 상태를 확인하는 중…" /> : null}
              <UrlNotices />
            </>
          ) : (
            <UrlNoJob what="점검 진행" />
          )}
        </div>
      </>
    );
  }

  const active = isUrlActive(job);
  const stoppable = job.state === "preparing" || job.state === "running";
  // 끝남은 마감이 확인된 상태만. 결과 불명(unknown)은 마감 여부와 독립이라 따로 안내한다.
  const ended = URL_ENDED_STATES.includes(job.state);
  const unknownNote =
    job.state === "unknown" ? (
      <div className={cx(s.note, s.warn)} role="note" id="luUnknown">
        <Icon name="alert" />
        <span>
          {`결과 불명 — 준비 또는 실행 결과를 확인하지 못했습니다. 끝난 것으로 보지 않고 자동으로 다시 시작하지 않습니다. 엔진 시작 ${factText(job.started)} · 마감 확인 ${factText(job.drain_complete)} · 결과 저장 ${factText(job.result_ready)}`}
        </span>
      </div>
    ) : null;

  let done: ReactNode = null;
  if (g.reportReadable) {
    done = (
      <div className={s.ldone}>
        <div className={s.dm}>
          <b>
            <Icon name="check" />
            <span>결과 저장을 확인했습니다</span>
          </b>
          <span>{`${urlStateText(job.state)} · 실제 task #${job.task_id}`}</span>
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
        <span>{`진단이 끝났습니다 (${urlStateText(job.state, verified)}) · 결과 저장 ${job.result_ready ? "확인됨" : "미확인"}`}</span>
      </div>
    );
  } else if (job.state === "ready" || job.state === "awaiting_approval") {
    done = (
      <div className={s.note} role="note">
        <Icon name="info" />
        <span className={s["lv-grow"]}>아직 시작하지 않은 진단입니다.</span>
        <button className={cx(s.btn, s.sm)} type="button" onClick={() => goTo("scope")}>
          범위 확인으로
        </button>
      </div>
    );
  }

  return (
    <>
      <UrlHead
        title="점검 진행"
        crumb="점검 진행"
        host={host}
        pill={pill}
        acts={
          <>
            <UrlRefreshButton />
            {stoppable ? (
              <button
                className={cx(s.btn, s.danger)}
                type="button"
                disabled={!g.canStop}
                onClick={() => void runUrlAction("stop")}
              >
                <Icon name="stop" />
                <span>진단 중지 요청</span>
              </button>
            ) : null}
            <InspOpenButton>진단 정보</InspOpenButton>
          </>
        }
      />
      <div className={s.vb}>
        <div className={s.vm}>
          <UrlPhaseBar job={job} />
          {job.reason_code ? <p className={s.help}>{`서버 사유: ${reasonText(job.reason_code)}`}</p> : null}
          <UrlNotices />
          <p className={s.help}>
            진행률 · 남은 시간은 추정하지 않습니다. 실행 마감 · HTTP 응답 · 후보 0건은 취약점 없음이나 확정을 뜻하지
            않습니다.
          </p>
          {unknownNote}
          {done}
          {g.runReadable && job.task_id ? (
            <SendControl taskId={job.task_id} active={active} visible={visible} />
          ) : (
            <section className={s.sec} aria-labelledby="luSendH">
              <div className={s.sh}>
                <h2 id="luSendH">전송 통제</h2>
              </div>
              <p className={s.empty}>{sendPlaceholder(job, uncertain)}</p>
            </section>
          )}
          <section className={s.sec} aria-labelledby="luUseH">
            <div className={s.sh}>
              <h2 id="luUseH">모델 사용량</h2>
            </div>
            <UrlUsage job={job} />
          </section>
          <section className={s.sec} aria-labelledby="luMoreH">
            <div className={s.sh}>
              <h2 id="luMoreH">점검 계획 · 지난 활동 · 발견</h2>
            </div>
            <ModeNote what="의도 목록 · 활동 · 발견" />
            <p className={s.help}>URL 진단 서버는 결속된 task 의 요청 승인 · 실행 기록 · 보고서만 엽니다.</p>
          </section>
        </div>
        <aside className={s.vi} aria-label="진단 정보">
          <InspHead eyebrow={host ?? ""} title="진단 정보" />
          <Isec>
            <UrlFacts job={job} />
          </Isec>
          <UrlDisconnect />
        </aside>
      </div>
    </>
  );
}

/** 저장 실행 단계 글 (팀 보고서와 같은 뜻). */
const RUN_PHASE: Readonly<Record<RequestApprovalRunState["phase"], string>> = {
  prepared: "준비됨 · 아직 실행 전",
  running: "실행 중",
  stopping: "중지 처리 중",
  closed: "종료됨",
  failed: "실패",
};
/** 저장 실행 결과 글. 정상 종료는 목표 달성 · 취약점 없음이 아니다. */
const RUN_OUTCOME: Readonly<Record<RequestApprovalRunState["outcome"], string>> = {
  "": "결과 미기록",
  completed: "정상 종료",
  cancelled: "취소로 종료",
  failed: "실패로 종료",
};
/** 판정 집계 칸 (정상 대조 결과는 머리 글로). */
const OUTCOME_CELLS = ["confirmed", "review", "not_executed", "unknown"] as const;

/** 진단 판정 집계: 보고서의 고유 결과 ID 를 팀 lib 분류(summarizeDiagnosticEntries)로 한 번씩 센다. */
function OutcomeStrip({ rem }: { rem: Poll<Remediation> }) {
  let body: ReactNode;
  let aside: ReactNode = null;
  if (rem.unsupported) body = <ModeNote what="진단 판정 집계" />;
  else if (!rem.data && rem.error) body = <ErrorNote error={rem.error} onRetry={rem.reload} />;
  else if (!rem.data) body = <LoadingRow />;
  else {
    const c = summarizeDiagnosticEntries(rem.data.entries);
    aside = (
      <span className={s.aside}>
        {`저장 결과 ${rem.data.entries.length}건 · ${diagnosticOutcomeLabels.negative} ${c.negative}건`}
      </span>
    );
    body = (
      <div className={s.strip}>
        {OUTCOME_CELLS.map((k) => (
          <div key={k} className={cx(s.cell, k === "confirmed" && c[k] > 0 && s.crit)}>
            <div className={s.k}>{diagnosticOutcomeLabels[k]}</div>
            <div className={s.v}>{`${c[k]}건`}</div>
          </div>
        ))}
      </div>
    );
  }
  return (
    <section className={s.sec} aria-label="진단 판정 집계">
      <div className={s.sh}>
        <h2>진단 판정 집계</h2>
        {aside}
      </div>
      {body}
      <p className={s.help}>
        보고서의 고유 결과 ID 를 한 번씩 셉니다. 확정은 저장된 검증이 있는 결과만이고 검토 필요는 후보입니다. 후보
        0건이나 실행 마감만으로 안전하다고 판단하지 않습니다.
      </p>
    </section>
  );
}

/** 저장된 실행 종료 기록 (팀 readDiagnosisRunDisplay): 단계 · 결과 · 마감 · 실행 ID · E② 요청·전송 집계 · 감사. */
function RunRecord({ run }: { run: Poll<DiagnosisRunView> }) {
  let body: ReactNode;
  const d = run.data;
  if (run.unsupported) body = <ModeNote what="실행 종료 기록" />;
  else if (!d && run.error) body = <ErrorNote error={run.error} onRetry={run.reload} />;
  else if (!d) body = <LoadingRow />;
  else if (d.kind === "none") body = <p className={s.empty}>저장된 실행 종료 결과가 없습니다. 완료로 보지 않습니다.</p>;
  else if (d.kind === "not_run") {
    body = (
      <p className={s.empty}>
        {d.preparedOnly
          ? "모델 준비 기록만 있고 저장된 실행 종료 결과는 없습니다."
          : "저장된 실행 종료 결과가 없습니다."}
        {" 저장 기록의 NOT_RUN 표시는 지금 실행 여부를 보장하지 않습니다."}
      </p>
    );
  } else if (d.kind === "unavailable") {
    body = <p className={s.empty}>이 task 의 실행 결과가 아니거나 확인할 수 없습니다. 완료로 보지 않습니다.</p>;
  } else {
    const r = d.run;
    const flags = [
      r.drain_complete ? "진행 중 요청 정리 완료" : "진행 중 요청 정리 미완료",
      r.stop_requested ? "중지 요청됨" : "",
      r.context_cancelled ? "컨텍스트 취소됨" : "",
      r.closure_failed ? "마감 실패(완전 종료로 보지 않음)" : "",
    ].filter(Boolean);
    const e = r.execution;
    let auditText = "—";
    if (e) auditText = e.audit_complete ? "완결" : "미완결";
    body = (
      <>
        <Kv
          rows={[
            ["단계", RUN_PHASE[r.phase] ?? `알 수 없는 단계 (${String(r.phase)})`],
            ["결과", RUN_OUTCOME[r.outcome] ?? `알 수 없는 결과 (${String(r.outcome)})`],
            ["마감", flags.join(" · ")],
            [
              "실행 ID",
              <span key="run" className={s.mono}>
                {r.run_ref || "—"}
              </span>,
            ],
            [
              "정책 revision",
              <span key="policy" className={s.mono}>
                {e?.policy_revision || "—"}
              </span>,
            ],
            ["감사", auditText],
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
          <>
            <div className={s.meters}>
              {RUN_COUNT_ROWS.map(({ key, label }) => (
                <div key={key} className={s.cell}>
                  <div className={s.k}>{label}</div>
                  <div className={s.v}>{fmtCount(e.counts[key])}</div>
                </div>
              ))}
            </div>
            <p className={s.help}>{RUN_COUNT_NOTE}</p>
          </>
        ) : (
          <p className={s.help}>실행 세부 집계가 저장되지 않았습니다. 전송 건수를 0으로 단정하지 않습니다.</p>
        )}
      </>
    );
  }
  return (
    <section className={s.sec} aria-label="실행 종료 기록">
      <div className={s.sh}>
        <h2>실행 종료 기록</h2>
        <span className={s.aside}>전송 차단 · 실행 마감 · 취약 판정은 서로 대신하지 않음</span>
      </div>
      {body}
    </section>
  );
}

/** live 보고서 (URL 진단): 결과 저장이 확인된 실제 task 의 판정 집계 · 실행 기록 · 모델 사용량 · 우선 조치 · 내려받기. */
export function UrlReportLive() {
  const { route, goTo } = useShell();
  const visible = route === "report";
  const handle = useUrl((st) => st.handle);
  const job = useUrl((st) => st.job);
  const verified = useUrl((st) => st.verified);
  const g = useUrlGates();
  const host = useUrlHost();
  const taskId = g.reportReadable && job?.task_id ? job.task_id : null;
  const rem = usePoll(() => fetchRemediation(taskId as string, "internal"), [taskId], {
    enabled: visible && !!taskId,
  });
  const run = usePoll(() => fetchDiagnosisRun(taskId as string), [taskId], { enabled: visible && !!taskId });

  const title = host ? `${host} 보고서` : "보고서";
  if (!handle || !job || !taskId) {
    let body: ReactNode;
    if (!handle) body = <UrlNoJob what="보고서" />;
    else {
      body = (
        <>
          <div className={s.note} role="note">
            <Icon name="info" />
            <span className={s["lv-grow"]}>
              {job
                ? `결과 저장이 확인되면 보고서를 봅니다 · 지금 ${urlStateText(job.state, verified)}, 결과 저장 ${job.result_ready ? "확인됨" : "미확인"}`
                : "진단 상태를 확인하지 못했습니다."}
            </span>
            {job ? (
              <button className={cx(s.btn, s.sm)} type="button" onClick={() => goTo("live")}>
                점검 진행 보기
              </button>
            ) : null}
          </div>
          <UrlNotices />
        </>
      );
    }
    return (
      <>
        <UrlHead
          title={title}
          crumb="점검 기록"
          host={host}
          pill={job ? <UrlPill job={job} verified={verified} /> : null}
          acts={handle ? <UrlRefreshButton /> : undefined}
        />
        <div className={s.vm}>{body}</div>
      </>
    );
  }

  return (
    <>
      <UrlHead
        title={title}
        crumb="점검 기록"
        host={host}
        pill={<UrlPill job={job} verified={verified} />}
        acts={
          <button
            className={cx(s.btn, s.sm)}
            type="button"
            onClick={() => {
              rem.reload();
              run.reload();
            }}
          >
            <Icon name="refresh" />
            새로고침
          </button>
        }
        meta={
          <>
            {job.scope ? (
              <span>
                실제 대상 <b>{job.scope.origin}</b>
              </span>
            ) : null}
            <span>
              진단 <b>{job.id}</b>
            </span>
            <span>
              실제 task <b>{`#${taskId}`}</b>
            </span>
            {run.data?.kind === "run" && run.data.run.run_ref ? (
              <span>
                실행 <b>{run.data.run.run_ref}</b>
              </span>
            ) : null}
            <span>
              상태 <b>{urlStateText(job.state, verified)}</b>
            </span>
          </>
        }
      />
      <div className={s.vm}>
        <p className={s.lead}>
          실제 task 의 저장 결과입니다. 실행 마감과 취약점 판정은 별개이며, 후보는 확정 취약점이 아닙니다.
        </p>
        <OutcomeStrip rem={rem} />
        <RunRecord run={run} />
        <section className={s.sec} aria-labelledby="luRepUseH">
          <div className={s.sh}>
            <h2 id="luRepUseH">모델 사용량 · 비용</h2>
          </div>
          <UrlUsage job={job} />
        </section>
        <Prio rem={rem} findings={null} />
        <Download taskId={taskId} />
        <section className={s.sec} aria-labelledby="luRepMoreH">
          <div className={s.sh}>
            <h2 id="luRepMoreH">발견 목록 · 커버리지</h2>
          </div>
          <ModeNote what="발견 목록 · 커버리지" />
          <p className={s.help}>
            URL 진단 서버는 결속된 task 의 보고서 · 조치 자료만 엽니다. 내려받은 보고서에서 보세요.
          </p>
        </section>
      </div>
    </>
  );
}
