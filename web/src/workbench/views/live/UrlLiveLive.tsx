"use client";

// 점검 진행 · 보고서 (시안 짜임).
// 점검 진행: 상태 카드(지금 · 계획 · 심화 점검 · 발견 후보 · 확인 필요 · 작업 합계 토큰) · 확인 필요(요청 승인) ·
//   발견 후보 목록(실시간, 검증 전 후보) · 활동 기록
//   + 오른쪽 진행 요약(전송 통제 · 탐색 현황 · 커버리지 · 점검 정보).
//   서버가 점검 중에 주지 않는 값(진행률 · 남은 시간 · 판정)은 자리도 두지 않는다. 시작 · 경과는 서버 started_at 으로
//   진행 요약(jobSummary)의 시작 칸에만 보인다(이 카드에는 자리를 새로 두지 않음).
// 보고서: 판정 집계 · 취약점 목록 / 보고서 + 오른쪽 취약점 상세(화면 글은 '취약점', 사용자 11/19 — 판정 딱지로 확정 여부 구분). (우선 조치 칸은 11/17 사용자 요청으로 뺌)
// 심각도 · 공격 경로 · 지식 그래프 · 계정 비교 · 재현 명령은 서버가 주지 않아 두지 않는다.
// 판정은 팀 lib(summarizeDiagnosticEntries · diagnosticOutcome)의 분류만 쓰고 화면이 만들지 않는다.

import {
  type KeyboardEvent,
  type ReactElement,
  type ReactNode,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";

import { ApiError } from "@/lib/diagnosis/api";
import { activeURLRun } from "@/lib/diagnosis/diagnosis-runs";
import { type URLDiagnosisJob, type URLDiagnosisUsage, urlPhaseLabels } from "@/lib/diagnosis/diagnosis-url";
import {
  type DiagnosticOutcome,
  diagnosticOutcome,
  diagnosticOutcomeLabels,
  summarizeDiagnosticEntries,
} from "@/lib/diagnosis/diagnostic-results";
import type { RemediationEntry } from "@/lib/diagnosis/remediation";
import { entrySeverity, entrySeverityLabel, type SeverityLevel } from "@/lib/diagnosis/severity";

import { SEVL, type SevKey } from "../../data/findings";
import { cx } from "../../lib/cx";
import { downloadDiagnostic, fetchCoverage, fetchRemediation, type Remediation } from "../../live/api";
import { renderedCopyText } from "../../live/copyMask";
import {
  candidateCountText,
  candidateErrorText,
  candidateSeverityText,
  fetchLiveCandidates,
  isCandidateRunNotActive,
  LIVE_CANDIDATES_POLL_MS,
  type LiveCandidate,
  type LiveCandidatePage,
} from "../../live/diagnosticCandidates";
import type { E2RequestRow } from "../../live/e2Requests";
import { clearFindingPick, pickFinding, usePickedFinding } from "../../live/findingPick";
import {
  approvalCounts,
  clockOf,
  dateTimeOf,
  fmtCount,
  fmtDur,
  fmtTokensShort,
  planState,
  sevKey,
} from "../../live/map";
import { ErrorNote, LoadingRow, NotYet } from "../../live/parts";
import { runDurationMissingWhy, runDurationSec, runStartMissingWhy, runStartText } from "../../live/runTiming";
import type { SavedApproval } from "../../live/savedApprovals";
import { selectUrlRun, useUrl, useUrlGates } from "../../live/urlDiagnosis";
import { factText, URL_ENDED_STATES, type UrlState, urlOutcomeText, urlStateText } from "../../live/urlMap";
import { useUrlRuns } from "../../live/urlRunList";
import { type Poll, usePoll } from "../../live/usePoll";
import { flowStageText, MAX_FLOW_PLAN, type WorkflowSnapshot } from "../../live/workflow";
import { useShell } from "../../shell";
import { Icon } from "../../ui/icons";
import { Markdown } from "../../ui/Markdown";
import { InspHead, InspOpenButton, Isec, PaneToggle, useScrollTopOnChange, vulnClassParts } from "../../ui/parts";
import s from "../../workbench.module.css";
import { ApprovalHistory, ApprovalQueue, useRequestApprovals } from "./approvals";
import { AssetGraphTab } from "./coverageGraphView";
import { DiagnosticEntrySections, stepHeading } from "./DiagnosticEntrySections";
import { AccountAuthWarning, LoginMethodMissingWarning } from "./liveAccountWarnings";
import { PriceCapsNotice } from "./PriceCapsNotice";
import { ProductVerdictReport, productConfirmationSummary, useProductVerdicts } from "./productVerdictViews";
import { ReportReadError } from "./runRows";
import {
  ReportApprovalDetail,
  ReportApprovals,
  SavedApprovalHistory,
  SavedApprovalSummary,
  showApprovalTab,
  useSavedApprovals,
} from "./savedApprovals";
import { useE2FlowFor } from "./sentRequests";
import { TaskActivity } from "./TaskActivity";
import { AccessMapPanel } from "./UrlAccessMapLive";
import {
  BackButton,
  reasonText,
  UrlFacts,
  UrlHead,
  UrlNotices,
  UrlPill,
  UrlRefreshButton,
  useUrlHost,
} from "./urlParts";
import { VerdictDetailCard } from "./VerdictDetailCard";
import { CrawlMeters, crawlPct, PauseButton, useStopConfirm, WorkflowNotice, WorkflowReport } from "./workflowViews";

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

/**
 * coverage 403(이 task 에선 제공 안 함)을 task 마다 기억한다. 바뀐 task 는 다시 한 번 부른다.
 * 403은 그 요청을 보낸 task 에 붙인다(`track(id, 요청)`). usePoll 은 task 가 바뀐 렌더에서 이전 task 의 오류를 잠깐 그대로
 * 돌려줘서, 렌더 때 오류를 보고 지금 task 에 붙이면 이전 task 의 403 때문에 새 task 를 한 번도 부르지 않게 된다(#851 리뷰).
 */
export function useCoverageForbidden(taskId: string | null): {
  forbidden: boolean;
  track: <T>(id: string, request: Promise<T>) => Promise<T>;
} {
  const [forbiddenTask, setForbiddenTask] = useState<string | null>(null);
  const forbidden = !!taskId && forbiddenTask === taskId;
  const track = useCallback(
    <T,>(id: string, request: Promise<T>): Promise<T> =>
      request.catch((error: unknown) => {
        if (error instanceof ApiError && error.status === 403) setForbiddenTask(id);
        throw error;
      }),
    [],
  );
  return { forbidden, track };
}

/**
 * '승인 필요로 보류' 칸 설명: 실제로 보류된 요청의 메서드로 쓴다(사용자 2026-11-22, 1차 리허설 F13 — 사용자 승인 모드에서
 * '승인 후 전송' 경로는 GET도 보류되는데 늘 "쓰기 메서드"라고 적었다).
 */
export function heldReason(pending: readonly { method: string }[]): string {
  if (!pending.length) return "보내기 전 사람 확인";
  const writes = pending.filter((r) => !["GET", "HEAD"].includes(r.method)).length;
  if (writes === pending.length) return "쓰기 메서드 · 보내기 전 확인";
  if (writes === 0) return "승인 후 전송 경로(읽기) · 보내기 전 확인";
  return `쓰기 ${writes} · 읽기 ${pending.length - writes} · 보내기 전 확인`;
}

/** 판정 딱지 모양 (확정 = 기본, 검토 필요 = 의심 모양, 정상 = 초록, 그 밖 = 판단 불가 모양). */

/**
 * 이어 보는 점검이 없을 때(새 브라우저·브라우저 재시작) 서버의 진행 중 점검을 점검마다 '열기' 버튼으로 보인다(사용자
 * 2026-11-22, 1차 리허설 F14). 연 점검(조회 결속)에서도 승인·거부·범위 선택을 할 수 있다(F16). 자동으로 고르지 않는다.
 */
export function UnfollowedActiveRuns() {
  // 선택자는 저장소 값 그대로(page)를 고른다 — `?? []`처럼 새 배열을 돌려주면 getSnapshot이 매번 달라져 무한 렌더링이 난다
  const page = useUrlRuns((st) => st.page);
  const { goTo } = useShell();
  const active = (page?.runs ?? []).filter(activeURLRun);
  if (!active.length) return null;
  return (
    <div className={cx(s.note, s.warn)} role="note" aria-label="이어 보지 않는 진행 중 점검">
      <Icon name="alert" />
      <div>
        <p>{`이 브라우저가 이어 보지 않는 진행 중 점검이 ${active.length}건 있습니다. 승인 대기가 있을 수 있으니 열어 확인하세요.`}</p>
        <ul className={s.policy}>
          {active.map((row) => (
            <li key={row.id}>
              <span>{`${row.display_name || row.id}${row.action_required ? " · 확인 필요" : ""}`}</span>{" "}
              <button
                className={cx(s.btn, s.sm)}
                type="button"
                onClick={() =>
                  void selectUrlRun(row).then((ok) => {
                    if (ok) goTo("live");
                  })
                }
              >
                이 점검 열기
              </button>
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}

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

/**
 * 후보를 읽지 않는 까닭. 서버 조회 경계(ProductCandidateJobActive)가 열리는 때 — 시작 확인 · running/stopping ·
 * 마감 · 결과 저장 전 — 에만 null(읽음). 끝난 점검은 후보 목록 대신 저장된 발견 목록을 보이므로(안내 문장은 뺐다, 사용자 11/23)
 * 여기서는 시작 전과 같이 본다.
 */
export function candidateIdle(job: URLDiagnosisJob | null | undefined, verified: boolean): CandidateIdle {
  if (!job) return "prestart";
  if (job.state === "unknown") return "unknown";
  if (!verified) return "unverified";
  const live =
    job.started && !job.drain_complete && !job.result_ready && (job.state === "running" || job.state === "stopping");
  return live ? null : "prestart";
}

/** 후보 등급 딱지. Worker 가 붙인 등급 그대로(확정이 아님은 '검토 필요'가 말한다, 11/24), 없으면 '위험도 미평가'(낮음 · 정보성으로 바꾸지 않는다). */
function CandidateSev({ sev }: { sev: SeverityLevel | null }) {
  const k = sev ? sevKey(sev) : null;
  // 등급이 없으면 '위험도 미평가' 회색 딱지(사용자 11/23)
  return <span className={cx(s.sev, k ? s[k] : s["sev-none"])}>{candidateSeverityText(sev)}</span>;
}

/**
 * 화면을 연 뒤 새로 들어온 후보 id (처음 읽은 목록은 '새로'가 아니다). 점검이 바뀌면 다시 센다.
 * 새 줄은 들어올 때 한 번 반짝이고 '새로' 딱지가 붙는다.
 */
function useFreshIds(task: string | undefined, list: readonly LiveCandidate[] | null | undefined): ReadonlySet<string> {
  const first = useRef<{ task: string; ids: Set<string> } | null>(null);
  if (task && list && first.current?.task !== task)
    first.current = { task, ids: new Set(list.map((c) => c.candidateId)) };
  const cur = first.current;
  const base = cur && cur.task === task ? cur.ids : null;
  return new Set(base && list ? list.filter((c) => !base.has(c.candidateId)).map((c) => c.candidateId) : []);
}

/**
 * 발견 후보 조회 실패 알림. 공용 ErrorNote 는 403 · 404 · 503 을 '제공되지 않음'으로 접고 다시 시도 버튼을 빼는데,
 * 점검 중 후보 목록은 일시 장애일 수 있어 HTTP 상태와 [다시 읽기]를 늘 둔다. stale 이면 아래가 마지막으로 읽은 목록이라고.
 */
function FindReadError({ error, onRetry, stale = false }: { error: unknown; onRetry: () => void; stale?: boolean }) {
  const status = error instanceof ApiError && error.status ? ` (HTTP ${error.status})` : "";
  return (
    <div className={cx(s.note, s.warn)} role="alert">
      <Icon name="alert" />
      <span className={s["lv-grow"]}>
        {stale
          ? `발견한 취약점을 다시 읽지 못해 갱신이 멈췄습니다${status}. ${candidateErrorText(error)} 아래는 마지막으로 읽은 목록이라 최신이 아닐 수 있습니다.`
          : `발견한 취약점을 읽지 못했습니다${status}. ${candidateErrorText(error)}`}
      </span>
      <button className={cx(s.btn, s.sm)} type="button" onClick={onRetry}>
        다시 읽기
      </button>
    </div>
  );
}

/** 점검 중 고른 후보의 상세(보고서 자료)를 다시 읽는 간격 — 증거가 더 붙을 수 있다 */
export const LIVE_DETAIL_POLL_MS = 10000;

/**
 * 점검 진행 오른쪽 패널의 후보 상세(사용자 11/23). 보고서 오른쪽 패널과 같은 FindingDetail(AI 분석 · 실제 요청 · 응답 ·
 * PoC)을 쓴다. 아직 저장되지 않았거나 못 읽으면 그렇게 적고, [진행 요약으로]로 돌아간다. 후보는 검토 대상이다(절대 규칙 5)
 */
function LiveCandidateDetail({
  entry: entryId,
  poll,
  host,
  taskId,
  onBack,
}: {
  /** 보고서 항목 ID (진행 중 후보는 'candidate-<id>') */
  entry: string;
  poll: Poll<Remediation>;
  host: string | null;
  taskId: string;
  onBack: () => void;
}) {
  const entry = poll.data?.entries.find((e) => e.entry_id === entryId);
  // 한 번 읽은 뒤 다시 읽기가 실패해도(usePoll 은 마지막 자료를 남기고 403 · 503 등이면 멈춘다) 오래된 PoC · 증거를 최신처럼
  // 두지 않는다 — 갱신 실패와 [다시 읽기]를 위에 두고 아래는 마지막으로 읽은 상세라고 적는다(#874 이동찬 P2, 후보 목록과 같은 방식)
  const status = poll.error instanceof ApiError && poll.error.status ? ` (HTTP ${poll.error.status})` : "";
  const stale =
    entry && poll.error ? (
      <div className={cx(s.note, s.warn)} role="alert">
        <Icon name="alert" />
        <span className={s["lv-grow"]}>
          {`상세를 다시 읽지 못해 갱신이 멈췄습니다${status}. 아래는 마지막으로 읽은 상세라 최신이 아닐 수 있습니다.`}
        </span>
        <button className={cx(s.btn, s.sm)} type="button" onClick={poll.reload}>
          다시 읽기
        </button>
      </div>
    ) : null;
  let body: ReactNode = <FindingDetail e={entry} host={host} taskId={taskId} onReload={poll.reload} />;
  if (!entry) {
    let state: ReactNode = <LoadingRow text="취약점 상세를 읽는 중…" />;
    if (poll.error) state = <ErrorNote error={poll.error} onRetry={poll.reload} />;
    else if (poll.data)
      state = <p className={s.empty}>이 취약점의 상세가 아직 저장되지 않았습니다. 잠시 뒤 다시 읽습니다.</p>;
    body = (
      <>
        <InspHead
          title={entryId.startsWith("candidate-") ? `취약점 #${entryId.slice("candidate-".length)}` : entryId}
        />
        {state}
      </>
    );
  }
  return (
    <>
      <div className={s["vi-back"]}>
        <BackButton to="진행 요약" onClick={onBack} />
      </div>
      {stale}
      {body}
    </>
  );
}

/** 후보 목록을 읽지 않는(또는 더 읽지 않는) 까닭. null 이면 실행 중이라 읽는다. */
export type CandidateIdle = "prestart" | "unknown" | "unverified" | null;

/**
 * 점검 진행 가운데의 발견 후보 목록 (사용자 11/17 '취약점 나온 것들 다 나열, 실시간'). 제품 Worker 는 원본 ARTEX
 * report_finding 이 없고(agent/tool_profile.go) 진단 후보를 저장하므로, 제품 후보 조회
 * (GET /api/tasks/{id}/diagnostic-candidates, #694)를 최근 순으로 보인다. 서버가 주는 것은 후보 ID · 검토 상태 ·
 * 관측 시각 · 추정 등급뿐이라 제목 · 경로 · 증거 자리는 두지 않는다. 후보는 검증 전 검토 대상이다(절대 규칙 5).
 * 409 candidate_run_not_active 는 오류가 아니라 '실행 조회 경계가 닫힘'이라 경고 대신 안내로 바꾸고 다시 읽지 않는다.
 */
function LiveCandidates({
  poll,
  fresh,
  idle,
  picked,
  onPick,
  titles,
}: {
  poll: Poll<LiveCandidatePage>;
  /** 후보 ID → 취약점 이름(보고서 자료의 'candidate-<id>' 항목 제목). 아직 못 읽은 후보는 없다 */
  titles: ReadonlyMap<string, string>;
  fresh: ReadonlySet<string>;
  idle: CandidateIdle;
  /** 오른쪽에 상세를 띄운 후보 ID */
  picked: string | null;
  /** 후보를 누르면 오른쪽에 그 후보의 PoC · 증거(사용자 11/23) */
  onPick: (id: string) => void;
}) {
  const page = poll.data;
  const list = page?.items ?? [];
  const closed = isCandidateRunNotActive(poll.error);
  // 한 번 읽은 뒤의 실패도 숨기지 않는다: usePoll 은 직전 목록을 남기고 403 · 404 · 503 이면 다시 부르지 않으므로,
  // 목록 위에 갱신 실패와 [다시 읽기]를 보이고 아래 목록은 마지막으로 읽은 것이라고 적는다. 409 는 실패가 아니다
  const failed = !!poll.error && !closed;
  // 결과 불명 · 상태 미확인으로 읽기를 멈췄을 때 남은 목록도 마지막으로 읽은 것이다
  const stale = !!page && (failed || closed || idle !== null);
  const bySev = SEV_LEVELS.map((v) => [v, list.filter((c) => c.suggestedSeverity === v).length] as const).filter(
    ([v, n]) => v !== "info" || n > 0,
  );
  const noSev = list.filter((c) => !c.suggestedSeverity).length;
  let lead: ReactNode = null;
  if (idle === "prestart") lead = <p className={s.empty}>점검 시작이 확인되면 취약점을 읽습니다.</p>;
  else if (idle === "unknown")
    lead = <p className={s.empty}>결과 불명인 점검은 취약점을 이어 읽지 않습니다. 상태를 다시 조회하세요.</p>;
  else if (idle === "unverified")
    lead = <p className={s.empty}>점검 상태를 확인하지 못해 취약점을 읽지 않습니다. 상태를 다시 조회하세요.</p>;
  else if (closed)
    lead = (
      <div className={s.note} role="status" id="lFindsClosed">
        <Icon name="info" />
        <span className={s["lv-grow"]}>
          {`점검 실행이 닫혀(시작 확인 전 · 마감 · 결과 불명) 취약점을 더 읽지 않습니다.${page ? " 아래는 마지막으로 읽은 목록입니다." : ""} 끝난 점검의 결과와 판정은 보고서에서 봅니다.`}
        </span>
      </div>
    );
  else if (stale) lead = <FindReadError error={poll.error} onRetry={poll.reload} stale />;
  let body: ReactNode = null;
  if (idle && !page) body = null;
  else if (!page && failed) body = <FindReadError error={poll.error} onRetry={poll.reload} />;
  else if (!page && !closed) body = <LoadingRow />;
  else if (page && !list.length)
    body = <p className={s.empty}>아직 나온 취약점이 없습니다. 없다고 안전하다는 뜻은 아닙니다.</p>;
  else if (page)
    body = (
      <ul className={s.lfinds} id="lFinds">
        {list.map((c) => (
          <li key={c.candidateId} className={cx(s.lfind, fresh.has(c.candidateId) && s.fresh)}>
            <button
              className={s["lfind-row"]}
              type="button"
              aria-current={picked === c.candidateId ? "true" : undefined}
              onClick={() => onPick(c.candidateId)}
            >
              <CandidateSev sev={c.suggestedSeverity} />
              <span className={s["lfind-m"]}>
                {/* 줄 제목은 무슨 취약점인지(사용자 11/24: '후보' 말은 쓰지 않고 취약점 이름). 이름을 아직 못 읽었으면 '취약점 #번호' */}
                <span className={s["lfind-t"]}>
                  {titles.get(c.candidateId) ?? `취약점 #${c.candidateId}`}
                  {fresh.has(c.candidateId) ? <span className={cx(s.tag, s.new)}>새로</span> : null}
                </span>
                <span className={s["lfind-s"]}>
                  <time dateTime={c.observedAt} title={dateTimeOf(c.observedAt)}>
                    {`관측 ${clockOf(c.observedAt)}`}
                  </time>
                  {" · 검토 필요"}
                </span>
              </span>
            </button>
          </li>
        ))}
      </ul>
    );
  let countText = "";
  if (page) countText = `${candidateCountText(page)} · ${stale ? "마지막으로 읽은 목록" : "최근 순"}`;
  return (
    <section className={s.sec} aria-labelledby="lFindsH">
      <div className={s.sh}>
        <h2 id="lFindsH">발견한 취약점</h2>
        <span className={s.n} id="lFindsN">
          {countText}
        </span>
      </div>
      {list.length ? (
        <div className={s.vbar}>
          {/* 확정 분포처럼 읽히지 않게 앞에 '검토 필요'(계약 diagnostic-candidate-severity: 후보 분포는 별도 문구, #917 리뷰 P3) */}
          <span>검토 필요</span>
          {bySev.map(([v, n]) => (
            <span key={v}>
              {`${candidateSeverityText(v)} `}
              <b>{n}</b>
            </span>
          ))}
          {noSev ? (
            <span>
              {`${candidateSeverityText(null)} `}
              <b>{noSev}</b>
            </span>
          ) : null}
        </div>
      ) : null}
      <p className={s.help}>
        {`점검 중에 저장된 검증 전 취약점입니다(모두 검토 필요). 등급은 Worker 가 붙인 값이라 확정이 아니고, 확정 여부는 점검이 끝난 뒤 보고서의 판정에서 봅니다.${
          page?.truncated
            ? ` 취약점이 많아 최근 ${page.returned}건만 받았고, 위 집계도 받은 ${page.returned}건 기준입니다.`
            : ""
        }`}
      </p>
      {lead}
      {body}
    </section>
  );
}

/** 후보 추정 등급 순서(집계 띠). 정보성은 있을 때만 보인다. */
const SEV_LEVELS: readonly SeverityLevel[] = ["critical", "high", "medium", "low", "info"];

/**
 * 점검 진행 상태 카드의 모델 사용량 한 줄 (입 · 출 칩, 사용자 11/17 '입출만'). 출처는 이 진단의 전송 통제 예산 기록
 * (job.model_usage): 제품(첫 진단) 서버가 원본 토큰 집계(/api/exploration/tokens)를 열지 않아 그쪽은 읽지 않는다.
 * 줄 어디에 올려도 두 수의 정확한 값을 한꺼번에 보인다. 실패 · 중지로 사용량을 못 받은 호출은 예약량으로 셌으므로
 * 그런 호출이 있으면 마우스를 올린 정확한 값에 최대치라고 같이 적는다(tokenExact). 칩 옆 '(최대)'(사용자 11/18)와
 * (?) 설명(사용자 11/19 '무슨 말인지 모르겠다')은 두지 않는다.
 */
function TaskTokens({ u }: { u: URLDiagnosisUsage }) {
  return (
    <p className={s.toks} title={tokenExact(u)}>
      {/* 칸 사이 공백: 화면은 flex 간격으로 띄우고, 글로 읽을 때(화면 읽기 · 복사) 붙지 않게 */}
      <span>작업 합계 ·</span> <TokenChips u={u} bound={false} />
    </p>
  );
}

/**
 * 마우스를 올리면 보일 정확한 값: 입력 · 출력 두 줄만(사용자 11/17 — 요청 수 · 비용은 넣지 않음). 사용량을 못 받아
 * 예약량으로 센 호출이 있으면 두 수가 실제의 최대치라 줄마다 '(최대치)'를 붙인다.
 */
function tokenExact(u: URLDiagnosisUsage): string {
  const bound = (u.unknown_usage_calls ?? 0) > 0 ? " (최대치)" : "";
  return [`입력 ${fmtCount(u.input_tokens)} 토큰${bound}`, `출력 ${fmtCount(u.output_tokens)} 토큰${bound}`].join("\n");
}

/**
 * 입 · 출 칩 (점검 진행 상태 카드 · 보고서 머리 줄이 같이 쓴다). 예약량으로 센 호출이 있으면 보고서 머리 줄은 칩 옆에
 * '(최대)'. 점검 진행은 칩 옆 '(최대)'를 빼고(사용자 11/18) 마우스를 올린 정확한 값(tokenExact)에만 '최대치'를 남긴다.
 */
function TokenChips({ u, bound = true }: { u: URLDiagnosisUsage; bound?: boolean }) {
  const chip = (k: string, n: number) => (
    <span>
      {`${k} `}
      <b>{fmtTokensShort(n)}</b>
    </span>
  );
  return (
    <>
      {chip("입", u.input_tokens)} {chip("출", u.output_tokens)}
      {bound && u.unknown_usage_calls ? <span> (최대)</span> : null}
    </>
  );
}

/** 단계 게이지 한 칸: 끝남 · 지금 · 기다림(사람 · 저장) · 아직 · 멈춤. */
type GaugeTone = "done" | "run" | "hold" | "wait" | "stop";
type GaugeSeg = { label: string; tone: GaugeTone; pct: number; sub: string };

/**
 * 점검 진행 맨 위 단계 게이지 (사용자 11/17 '상단에 게이지바'). 서버가 전체 진행률을 주지 않아 하나짜리 % 막대는 만들지
 * 않고, 단계(자산 탐색 → 범위 확인 → 심화 점검 → 정리 · 저장)마다 실제 값으로만 채운다: 끝난 단계는 꽉, 지금 단계는
 * 자산 탐색 = 방문 / 페이지 상한(서버가 탐색 완료라 하면 꽉), 심화 점검 = 계획 완료 / 계획(계획이 없으면 마감 / 시작),
 * 사람 · 저장을 기다리는 단계는 빗금. 중지 · 실패면 그 자리에서 멈춘 모양. 상태를 모르면(unknown) null.
 */
export function gaugeSegments(flow: WorkflowSnapshot, resultReady: boolean): GaugeSeg[] | null {
  const plan = flow.plan ?? [];
  const planDone = plan.filter((p) => planState(p.state).row === "done").length;
  const order: Partial<Record<WorkflowSnapshot["stage"], number>> = {
    crawling: 0,
    awaiting_selection: 1,
    diagnosing: 2,
    completed: 3,
  };
  let cur = order[flow.stage];
  const stopped = flow.stage === "stopped" || flow.stage === "failed";
  // 멈춘 자리: 심화 점검을 시작했으면 심화 점검, 첫 탐색을 마쳤으면 범위 확인, 아니면 자산 탐색
  if (stopped) {
    cur = 0;
    if (flow.deep_runs > 0) cur = 2;
    else if (flow.initial_completed) cur = 1;
  }
  if (cur === undefined) return null;
  const auto = flow.mode === "ai_auto_v1";
  // 계획 목록이 상한(MAX_FLOW_PLAN)만큼 꽉 차 오면 앞부분만 받은 것일 수 있어 전체 완료율로 쓰지 않고 심화 점검 마감 수로
  const planWhole = plan.length > 0 && plan.length < MAX_FLOW_PLAN;
  let deepPct = 0;
  if (planWhole) deepPct = (planDone / plan.length) * 100;
  else if (flow.deep_runs) deepPct = (flow.deep_closed / flow.deep_runs) * 100;
  const deepSub = planWhole ? `계획 ${planDone}/${plan.length}` : `${flow.deep_closed}/${flow.deep_runs} 마감`;
  const rows: [string, number, GaugeTone, string][] = [
    ["자산 탐색", crawlPct(flow), "run", `방문 ${flow.counts.visited}/${flow.limits.max_pages}`],
    [auto ? "범위 선택 (AI)" : "범위 확인", 0, "hold", "선택 대기"],
    ["심화 점검", Math.round(deepPct), "run", deepSub],
    ["정리 · 저장", 0, "hold", "결과 저장 확인 중"],
  ];
  const doneSub = ["탐색 완료", auto ? "AI 선택" : "범위 고름", "마감", "결과 저장"];
  return rows.map(([label, pct, tone, sub], i): GaugeSeg => {
    if (i < cur || (i === 3 && cur === 3 && resultReady)) return { label, tone: "done", pct: 100, sub: doneSub[i] };
    if (i > cur) return { label, tone: "wait", pct: 0, sub: "" };
    if (stopped) return { label, tone: "stop", pct, sub: flow.stage === "failed" ? "실패" : "중지됨" };
    return { label, tone, pct, sub };
  });
}

/** 지금 단계 글: 진행 중이면 '… 중', 기다림 · 멈춤은 그 값과 같이. */
function gaugeHeadline(x: GaugeSeg): string {
  if (x.tone === "run") return `${x.label} 중${x.sub ? ` · ${x.sub}` : ""}`;
  return x.sub ? `${x.label} · ${x.sub}` : x.label;
}

/**
 * 단계 게이지 (사용자 11/18 '지금 단계 크게 + 작은 점'): 위 줄은 단계마다 작은 점(끝남 초록 · 지금 강조 · 기다림 점선 ·
 * 멈춤 회색 · 아직 빈 점)과 '단계 n/4', 가운데는 지금 단계 글을 크게, 아래는 지금 단계 막대 하나(값은 gaugeSegments 그대로).
 */
function StageGauge({ segs }: { segs: GaugeSeg[] }) {
  const at = segs.findIndex((x) => x.tone !== "done");
  const allDone = at < 0;
  const now = allDone ? segs[segs.length - 1] : segs[at];
  const summary = allDone ? "점검 완료" : `단계 ${at + 1}/${segs.length} ${now.label}${now.sub ? ` · ${now.sub}` : ""}`;
  const tone: GaugeTone = allDone ? "done" : now.tone;
  let pct = Math.min(100, Math.max(0, now.pct));
  if (allDone) pct = 100;
  else if (tone === "hold") pct = 0; // 사람 · 저장을 기다리는 단계는 채우지 않고 빗금
  return (
    // biome-ignore lint/a11y/useSemanticElements: 점 · 글 · 막대를 한 묶음으로 읽히게 (fieldset 은 입력 묶음이라 맞지 않음)
    <div className={s.lgauge} id="lGauge" role="group" aria-label={`점검 단계 진행: ${summary}`}>
      <div className={s.lgtop} aria-hidden="true">
        <ol className={s.lgdots}>
          {segs.map((x) => (
            <li key={x.label} className={cx(s.lgdot, s[x.tone])} title={`${x.label}${x.sub ? ` · ${x.sub}` : ""}`} />
          ))}
        </ol>
        <span className={s.lgstep}>{`단계 ${allDone ? segs.length : at + 1}/${segs.length}`}</span>
      </div>
      <div className={cx(s.lghead, tone === "stop" && s.stop, allDone && s.done)} aria-hidden="true">
        {/* 다 끝나면 '점검 완료' 한 마디(사용자 11/23: '모든 단계를 마쳤습니다 · 결과 저장'이 길었다) */}
        {allDone ? "점검 완료" : gaugeHeadline(now)}
      </div>
      <div className={cx(s.prog, s.lgbar, s[tone])} aria-hidden="true">
        <i style={{ width: `${pct}%` }} />
      </div>
    </div>
  );
}

/**
 * 끝난 점검의 발견 (사용자 11/17 '보고서 형식으로'): 실제 제품 서버는 점검이 끝나면 점검 중 발견 목록
 * (/api/exploration/findings)을 닫고 저장 보고서만 열어 둔다(cmd/first-controlled-diagnosis url_preparation_runtime.go).
 * 그래서 저장된 판정 결과를 보고서 '발견 목록'과 같은 줄(FindingRows)로 보인다. 줄을 누르면 오른쪽 패널에 그 항목의
 * 상세(PoC · 증거)를 띄운다(사용자 11/23 — 11/17 의 '읽기 줄'에서 바뀜). 보고서로는 위 상태 카드의 [보고서 보기]로 간다.
 */
function SavedFindings({
  poll,
  picked,
  onPick,
}: {
  poll: Poll<Remediation>;
  /** 오른쪽에 상세를 띄운 항목(entry_id) */
  picked: string | null;
  /** 누르면 오른쪽에 그 항목의 PoC · 증거(사용자 11/23) */
  onPick: (entryId: string) => void;
}) {
  const sorted = poll.data ? sortEntries(poll.data.entries) : [];
  let body: ReactNode;
  if (poll.error) body = <ReportReadError error={poll.error} onRetry={poll.reload} />;
  else if (!poll.data) body = <LoadingRow />;
  else if (!sorted.length)
    body = <p className={s.empty}>저장된 결과가 없습니다. 결과가 없다고 안전하다는 뜻은 아닙니다.</p>;
  else body = <FindingRows entries={sorted} id="lSaved" chosen={picked} onPick={(e) => onPick(e.entry_id)} />;
  return (
    <section className={s.sec} aria-labelledby="lSavedH">
      <div className={s.sh}>
        <h2 id="lSavedH">발견 목록</h2>
        <span className={s.n}>{poll.data && !poll.error ? `${sorted.length}건` : ""}</span>
      </div>
      {/* [보고서 보기]는 위 상태 카드에 있다 — 여기 또 두지 않는다 */}
      {body}
    </section>
  );
}

/** 흐름 일시정지 상태를 알약에 어떻게 보일지. */
export type FlowPause = "paused" | "running" | "unknown" | "none";

/**
 * 흐름 스냅숏 → 일시정지 상태(11/24 화면 VAmPI 발견 10). 확인된(verified · 결과 불명 아님) 스냅숏일 때만 paused/running 이고,
 * 탐색 흐름이 있는데 다시 읽지 못했으면 'unknown' — 마지막으로 읽은 스냅숏으로 '진행 중'을 단정하지 않는다(절대 규칙 5).
 * 흐름이 없는 점검은 'none'.
 */
export function flowPauseOf(
  workflow: boolean,
  flow: Pick<WorkflowSnapshot, "paused"> | null,
  flowVerified: boolean,
  flowUncertain: boolean,
): FlowPause {
  if (!workflow || !flow) return "none";
  if (!flowVerified || flowUncertain) return "unknown";
  return flow.paused ? "paused" : "running";
}

/**
 * 점검 진행 화면의 상태 알약 (시안 lState: 진행 중 · 일시정지됨). 일시정지됨은 확인된 흐름 스냅숏이 일시정지일 때, 점검이 아직
 * 움직이는 상태(준비 · 실행 중)면 보인다 — 일시정지 버튼이 있는 탐색(preparing)에서도 뜬다(전에는 running 만이라 탐색 중
 * 일시정지가 안 보였다). 실행 중인데 흐름 상태를 다시 읽지 못했으면 '일시정지 여부 확인 중'. 그 밖은 공통 알약(서버 상태 글).
 */
function LivePill({ job, verified, pause }: { job: { state: UrlState }; verified: boolean; pause: FlowPause }) {
  const moving = job.state === "running" || job.state === "preparing";
  if (moving && pause === "paused")
    return (
      <span className={cx(s.pill, s.wait)} data-flow-pause="paused">
        일시정지됨
      </span>
    );
  if (!verified || job.state !== "running") return <UrlPill job={job} verified={verified} />;
  if (pause === "unknown")
    return (
      <span className={cx(s.pill, s.wait)} data-flow-pause="unknown">
        일시정지 여부 확인 중
      </span>
    );
  return <span className={cx(s.pill, s.run)}>진행 중</span>;
}

/** live 점검 진행 (URL 진단, 시안 data-view="live"). */
export function UrlLiveLive() {
  const { route, goTo, openInsp } = useShell();
  const visible = route === "live";
  const handle = useUrl((st) => st.handle);
  const job = useUrl((st) => st.job);
  const verified = useUrl((st) => st.verified);
  const busy = useUrl((st) => st.busy);
  const flow = useUrl((st) => st.flow);
  const flowVerified = useUrl((st) => st.flowVerified);
  const flowUncertain = useUrl((st) => st.flowUncertain);
  const g = useUrlGates();
  const host = useUrlHost();
  const stop = useStopConfirm("종료");
  const a = useRequestApprovals(job?.task_id ?? "", visible && g.runReadable);
  // 끝난 점검은 저장 승인 이력(계약 saved-request-approvals, 읽기 전용)으로 뭘 승인 · 거부했는지 본다(사용자 11/26)
  const savedAp = useSavedApprovals(
    job && URL_ENDED_STATES.includes(job.state) && job.task_id ? job.task_id : null,
    visible,
  );
  // 자산 커버리지 (시안 v22 진행 상세): 실제 task 가 생긴 뒤 서버 값. 꺼졌거나 못 읽으면 칸을 두지 않는다
  // 제품 점검은 공개 진단 API만 열어 coverage 가 403이다(1차 리허설 F6: 15초마다 403이 쌓였다). 한 번 403을 받으면 그
  // task 에서는 다시 부르지 않고 칸을 두지 않는다(서버 응답으로 판단 — 모드를 화면이 추정하지 않는다)
  const covOff = useCoverageForbidden(job?.task_id ?? null);
  const cov = usePoll(
    () => covOff.track(job?.task_id as string, fetchCoverage(job?.task_id as string)),
    [job?.task_id],
    {
      enabled: visible && !!job?.task_id && !covOff.forbidden,
      intervalMs: 15000,
    },
  );
  // 모델 사용량: 원본 ARTEX 작업 합계(입력 · 캐시 읽기 · 출력). 전송 통제 예산 기록(model_usage)에는 캐시가 없다
  // 점검 중 발견 후보 (제품 후보 조회 #694). 서버가 여는 조건(ProductCandidateJobActive: 시작 확인 · running/stopping ·
  // 마감 · 결과 저장 전)과 같을 때만 읽어 시작 전 · 끝난 뒤에 409 를 일부러 부르지 않는다. 화면이 안 보이면(다른 탭 ·
  // 문서 숨김) 멈추고, 응답 뒤 5초 쉬고 다시 읽는다(겹치지 않음). task 가 바뀌면 남은 요청을 끊는다(usePoll)
  const findsIdle = candidateIdle(job, verified);
  const finds = usePoll((signal) => fetchLiveCandidates(job?.task_id as string, signal), [job?.task_id], {
    enabled: visible && !!job?.task_id && findsIdle === null,
    intervalMs: LIVE_CANDIDATES_POLL_MS,
  });
  const fresh = useFreshIds(job?.task_id, finds.data?.items);
  // 점검 중 후보를 누르면 오른쪽 패널에 그 후보의 PoC · 증거(사용자 11/23: 끝나야 볼 수 있었다). 보고서와 같은 자료
  // (GET /api/report/diagnostic/remediation, 후보 = 'candidate-<id>')를 실행 중에도 읽는다 — 실행 중에는 실행 핸들러가 같은
  // 보고서 API 를 연다. 고른 후보는 task 에 묶어 다른 점검으로 바뀌면 풀린다. 고른 동안만, 쉬었다가 다시 읽는다
  // 끝난 점검의 '발견 목록'도 같은 방식으로 누르면 오른쪽에 띄운다(이미 읽은 저장 결과를 쓴다). 고른 것은 보고서 항목 ID
  // (진행 중 후보는 'candidate-<id>')로 둔다
  const [pick, setPick] = useState<{ task: string; entry: string } | null>(null);
  const picked = pick && pick.task === job?.task_id ? pick.entry : null;
  // 오른쪽 패널에 처리된 승인 요청 목록(사용자 11/25: 가운데엔 대기 요청만). 고른 취약점과 번갈아 하나만 띄운다. task 에 묶어
  // 다른 점검으로 바뀌면 풀린다
  const [apTask, setApTask] = useState<string | null>(null);
  const apShown = !picked && !!job?.task_id && apTask === job.task_id;
  // 다른 후보를 고르면 오른쪽 패널을 맨 위로(사용자 11/24)
  const liveInspRef = useScrollTopOnChange<HTMLElement>(picked ?? (apShown ? "approvals" : null));
  let inspLabel = "진행 요약";
  if (picked) inspLabel = "취약점 상세";
  else if (apShown) inspLabel = "처리된 승인 요청";
  const runEnded = !job || URL_ENDED_STATES.includes(job.state);
  // 같은 자료로 후보 줄에 취약점 이름도 붙인다(사용자 11/24). 그래서 고른 동안만이 아니라 후보가 있으면 읽고, 새 후보가
  // 오면 바로 다시 읽는다(그 사이엔 쉬었다가)
  const newestCandidate = finds.data?.items[0]?.candidateId ?? null;
  // 고른 후보가 바뀌어도 다시 읽지 않는다(이미 읽은 자료에 다 있다) — 다시 읽는 동안 줄 이름이 번호로 깜빡이지 않게
  const pickedRem = usePoll(
    () => fetchRemediation(job?.task_id as string, "internal"),
    [job?.task_id, newestCandidate],
    {
      enabled: visible && !runEnded && !!job?.task_id && (!!picked || !!newestCandidate),
      intervalMs: LIVE_DETAIL_POLL_MS,
    },
  );
  // 이름은 점검마다 모아 둔다: 다시 읽는 동안(자료가 잠깐 비어도) 한 번 받은 이름은 그대로 보인다
  const titleBook = useRef<{ task: string | null; names: Map<string, string> }>({ task: null, names: new Map() });
  const candidateTitles = useMemo(() => {
    const task = job?.task_id ?? null;
    if (titleBook.current.task !== task) titleBook.current = { task, names: new Map() };
    for (const e of pickedRem.data?.entries ?? []) {
      if (!e.entry_id.startsWith("candidate-")) continue;
      const title = e.title.trim();
      if (title) titleBook.current.names.set(e.entry_id.slice("candidate-".length), title);
    }
    return new Map(titleBook.current.names);
  }, [pickedRem.data, job?.task_id]);
  const pickEntry = (entry: string) => {
    if (!job?.task_id) return;
    setApTask(null);
    setPick({ task: job.task_id, entry });
    openInsp();
  };
  const showDoneApprovals = () => {
    if (!job?.task_id) return;
    setPick(null);
    setApTask(job.task_id);
    openInsp();
  };
  const planTitle = (id: number) => flow?.plan?.find((p) => p.id === id)?.title;
  // 끝난 점검의 발견: 결과 저장이 확인된 task 의 저장 판정 결과 (보고서와 같은 자료)
  const savedTask = g.reportReadable && job?.task_id ? job.task_id : null;
  const saved = usePoll(() => fetchRemediation(savedTask as string, "internal"), [savedTask], {
    enabled: visible && !!savedTask,
  });
  const savedVerdicts = useProductVerdicts(savedTask, visible);
  const savedConfirmation = productConfirmationSummary(savedVerdicts);
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
              <UnfollowedActiveRuns />
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
  // 서버가 보고한 흐름 상태만 표시한다. 일시정지 · 다시 시작은 흐름 API(PauseButton), 점검 제어 API 는 부르지 않는다.
  const pause = flowPauseOf(!!job.input.workflow_mode, flow, flowVerified, flowUncertain);
  const pausedNow = pause === "paused";
  const run = a.poll.data?.run;
  const ap = approvalCounts(run, a.poll.data?.approvals);
  const known = (n: number) => (ap.known ? fmtCount(n) : "—");
  const runningPlan = flow?.plan?.find((p) => planState(p.state).row === "run");
  const nowText =
    runningPlan?.title ?? flow?.current_path ?? (flow ? flowStageText[flow.stage] : urlPhaseLabels[job.phase]);
  // 상태 카드 첫 줄 (시안 lNowK · lTask): 실행 마감(completed)이면 완료 · '점검을 마쳤습니다', 그 밖에 끝났으면 종료됨, 멈췄으면 멈춤.
  // 결과 저장이 확인되면 따로 두던 완료 카드('점검을 마쳤습니다 [보고서 보기]')를 이 카드에 합친다(같은 말이 두 번 나와서)
  let nowK: ReactNode;
  let taskText: ReactNode = nowText;
  let doneSub: string | null = null;
  // completed 는 마감 · 저장 확인이지 전부 검사했다는 뜻이 아니다(run-outcome-states.md §4). 일부 완료 · 검사 안 함은
  // '마쳤습니다' · 완료 톤으로 보이지 않게 하고, 실행 결과 한 줄(urlOutcomeText)을 카드에 같이 둔다
  const outcomeText = urlOutcomeText(job);
  const shortfall = job.state === "completed" && (job.outcome === "partial" || job.outcome === "no_intents");
  let completedText = "점검을 마쳤습니다";
  if (shortfall)
    completedText = job.outcome === "no_intents" ? "검사하지 않고 마감했습니다" : "일부만 점검하고 마감했습니다";
  if (job.state === "completed") {
    nowK = shortfall ? (
      <span className={cx(s["now-k"], s.idle)}>
        <i />
        마감
      </span>
    ) : (
      <span className={cx(s["now-k"], s.ok)}>
        <Icon name="check" />
        완료
      </span>
    );
    taskText = completedText;
  } else if (ended) {
    nowK = (
      <span className={cx(s["now-k"], s.idle)}>
        <i />
        종료됨
      </span>
    );
    taskText = "새 요청을 보내지 않습니다";
  } else {
    nowK = (
      <span className={cx(s["now-k"], pausedNow && s.idle)}>
        <i />
        {pausedNow ? "멈춤" : "지금"}
      </span>
    );
  }
  if (g.reportReadable) {
    taskText = job.state === "completed" ? completedText : "점검을 종료했습니다";
    doneSub = `${urlStateText(job.state)} · 결과 저장 확인`;
  }
  if (outcomeText) doneSub = doneSub ? `${doneSub} · ${outcomeText}` : outcomeText;

  // 카드 밑 상태 안내: 끝났는데 결과 저장을 확인하지 못함 · 결과 불명 · 시작 전
  let stateNote: ReactNode = null;
  if (ended && !g.reportReadable) {
    stateNote = (
      <div className={cx(s.note, s.warn)}>
        <Icon name="alert" />
        <span>{`점검이 끝났습니다 (${urlStateText(job.state, verified)}) · 결과 저장 ${factText(job.result_ready)}`}</span>
      </div>
    );
  } else if (job.state === "unknown") {
    stateNote = (
      <div className={cx(s.note, s.warn)} role="note" id="luUnknown">
        <Icon name="alert" />
        <span>{`결과 불명 · 끝난 것으로 보지 않고 자동으로 다시 시작하지 않습니다. 엔진 시작 ${factText(job.started)} · 마감 ${factText(job.drain_complete)}`}</span>
      </div>
    );
  } else if (job.state === "ready" || job.state === "awaiting_approval") {
    stateNote = (
      <div className={s.note} role="note">
        <Icon name="info" />
        <span className={s["lv-grow"]}>아직 시작하지 않은 점검입니다.</span>
        <button className={cx(s.btn, s.sm)} type="button" onClick={() => goTo("new")}>
          시작하러 가기
        </button>
      </div>
    );
  }

  let sendWaitText = "엔진이 시작되면 나옵니다.";
  if (job.drain_complete) sendWaitText = "점검이 끝나 전송 상태를 더 조회하지 않습니다.";
  else if (job.state === "unknown" || !verified)
    sendWaitText = "결과 불명인 점검은 전송 상태를 이어 조회하지 않습니다. 상태를 다시 조회하세요.";
  const selected = flow?.endpoints.filter((e) => e.selected).length ?? 0;
  const plan = flow?.plan ?? [];
  const planDone = plan.filter((p) => planState(p.state).row === "done").length;
  // 상단 카드 숫자 칸: 서버 값이 있는 것만 (계획은 계획이 있을 때만, 발견 후보는 읽었을 때만)
  const found = finds.data ?? null;
  const highUp = found
    ? found.items.filter((c) => c.suggestedSeverity === "critical" || c.suggestedSeverity === "high").length
    : 0;
  const gauge = flow && job.state !== "unknown" ? gaugeSegments(flow, !!job.result_ready) : null;
  const stats: { k: string; v: string; sub: string; warn?: boolean }[] = [];
  if (flow && job.state !== "unknown") {
    // 계획이 상한만큼 꽉 차 오면 전체 수를 모른다 → 'N+' 와 '일부만 받음'
    if (plan.length >= MAX_FLOW_PLAN) stats.push({ k: "계획", v: `${plan.length}+`, sub: "목록 일부만 받음" });
    else if (plan.length) stats.push({ k: "계획", v: `${planDone}/${plan.length}`, sub: "완료" });
    stats.push({ k: "심화 점검", v: `${flow.deep_closed}/${flow.deep_runs}`, sub: "마감" });
  }
  if (ended && saved.data && !saved.error) {
    const savedFindings = saved.data.entries.filter((e) => diagnosticOutcome(e) !== "negative");
    const confirmed = savedFindings.filter((e) => diagnosticOutcome(e) === "confirmed").length;
    const summary = `판정 확정 ${confirmed + savedConfirmation.confirmed}`;
    stats.push({
      k: "발견",
      v: String(savedFindings.length),
      sub: savedConfirmation.notice ? `${summary} · ${savedConfirmation.notice}` : summary,
    });
  } else if (!ended && found)
    // 일부만 왔으면 받은 행 수에 '+' — 전체 수는 모른다(candidate_total null). 받은 수를 전체로 쓰지 않는다
    stats.push({
      k: "발견한 취약점",
      v: found.truncated ? `${found.returned}+` : String(found.returned),
      sub: found.truncated ? `최근 ${found.returned}건만 받음` : `심각 · 높음 ${highUp}`,
    });
  if (g.runReadable)
    stats.push({ k: "확인 필요", v: String(a.pending.length), sub: "승인 대기", warn: a.pending.length > 0 });
  // 모델 사용량은 상태 카드 안 한 줄 (입 · 출). 서버 기록이 아직 없으면 자리를 두지 않는다
  const tokensBody: ReactElement | null = job.model_usage ? <TaskTokens u={job.model_usage} /> : null;

  return (
    <>
      <UrlHead
        title="점검 진행"
        crumb="점검 진행"
        host={host}
        phase="live"
        pill={<LivePill job={job} verified={verified} pause={pause} />}
        acts={
          <>
            <PauseButton />
            {stop.button}
            {/* 좁은 화면에서만 보이는 오른쪽 서랍 열기 (넓은 화면은 PaneToggle) */}
            <InspOpenButton>진행 요약</InspOpenButton>
          </>
        }
      />
      <div className={s.vb}>
        <div className={s.vm}>
          {stop.box}
          <UrlNotices />
          <WorkflowNotice />
          {/* 시험 계정 인증 경고(#936 account_auth, 2초 조회로 갱신) · 로그인 방식 없음(#914). 알림만, 회차는 멈추지 않는다 */}
          <AccountAuthWarning auth={job.account_auth} at={job.account_auth_updated_at} />
          <LoginMethodMissingWarning
            targetUrl={handle.input.target_url}
            active={visible && !ended}
            authTried={!!job.account_auth}
          />
          {job.reason_code ? <p className={s.help}>{`서버 사유: ${reasonText(job.reason_code)}`}</p> : null}
          {job.diagnostic_log_incomplete ? (
            <p className={s.help}>{reasonText("diagnostic_logging_incomplete")}</p>
          ) : null}
          {/* 끝난 점검의 단가 상한 안내(v4 제품 점검): '결과 불명'이 단가 상한 때문이면 어느 제공자가 왜 빠졌는지 */}
          {ended && job.task_id ? <PriceCapsNotice taskId={String(job.task_id)} ready={g.reportReadable} /> : null}
          {/* 상단 카드: 지금 하는 일을 크게(끝나면 결과와 [보고서 보기]), 숫자 칸(계획 · 심화 점검 · 발견 후보 · 확인 필요),
              작업 합계 토큰. 계획 단계 목록은 두지 않는다(사용자 11/17 '계획 접기 빼기' — 완료 수만 숫자 칸에).
              결과 불명이면 첫 줄 대신 아래 안내가 말하고, 진행률 · 남은 시간은 서버가 주지 않아 두지 않는다.
              시작 · 경과는 진행 요약의 시작 칸에 있다 */}
          {/* 맨 위 단계 게이지 (탐색 흐름이 있는 점검만, 상태를 모르면 두지 않는다) */}
          {gauge ? <StageGauge segs={gauge} /> : null}
          {job.state !== "unknown" || tokensBody || stats.length ? (
            <section className={cx(s.lcard, g.reportReadable && !shortfall && s.ok)} aria-label="지금 상태">
              {job.state !== "unknown" ? (
                <div className={s["lcard-h"]}>
                  <div className={s["lcard-now"]} aria-live="polite">
                    {nowK}
                    <span id="lTask">{taskText}</span>
                  </div>
                  {g.reportReadable ? (
                    <button className={cx(s.btn, s.pri)} type="button" onClick={() => goTo("report")}>
                      보고서 보기
                      <Icon name="arrow" />
                    </button>
                  ) : null}
                </div>
              ) : null}
              {doneSub ? <div className={s["lcard-sub"]}>{doneSub}</div> : null}
              {stats.length ? (
                <div className={cx(s.meters, stats.length === 3 && s.m3)} id="lMeta">
                  {stats.map((x) => (
                    <div className={cx(s.cell, x.warn && s.warn)} key={x.k}>
                      <div className={s.k}>{x.k}</div>
                      <div className={s.v}>{x.v}</div>
                      <div className={s.sub}>{x.sub}</div>
                    </div>
                  ))}
                </div>
              ) : null}
              {tokensBody ? <div className={s["lcard-m"]}>{tokensBody}</div> : null}
            </section>
          ) : null}
          {stateNote}
          {/* 승인 요청: 대기 요청만 한 줄씩(사용자 11/24 · 11/25). 줄을 누르면 미리보기 · 승인 정보 · 승인/거부가 펼쳐진다.
              처리된 요청은 머리 버튼으로 오른쪽 패널에 */}
          {ended ? (
            <SavedApprovalSummary poll={savedAp} shown={apShown} onShow={showDoneApprovals} />
          ) : (
            <ApprovalQueue a={a} planTitle={planTitle} doneShown={apShown} onShowDone={showDoneApprovals} />
          )}
          {/* 발견: 점검 중엔 발견 후보(실시간), 끝나면 저장된 결과를 보고서 형식으로 */}
          {!ended && job.task_id ? (
            <LiveCandidates
              poll={finds}
              fresh={fresh}
              idle={findsIdle}
              picked={picked?.startsWith("candidate-") ? picked.slice("candidate-".length) : null}
              onPick={(id) => pickEntry(`candidate-${id}`)}
              titles={candidateTitles}
            />
          ) : null}
          {ended && savedTask ? <SavedFindings poll={saved} picked={picked} onPick={pickEntry} /> : null}
          {ended && !savedTask ? <p className={s.empty}>결과 저장이 확인되면 발견 목록이 여기에 나옵니다.</p> : null}
          {a.message ? <p role="status">{a.message}</p> : null}
          {a.unresolved ? <p role="alert">결정 결과 불명 · 조회로 확인하기 전에는 다시 결정하지 않습니다.</p> : null}
          {/* 활동 기록(탐색 흐름 활동 · 워커 콘솔)은 두지 않는다(사용자 11/17): 서버가 도구 입출력 · 모델 글이 비밀을 품을 수
              있어 원문 대신 고정 문장만 준다(diagnosisflow postgres.go publicActivity) — 보여 줄 내용이 없다.
              흐름이 없는 점검만 점검 중에 서버 진단 활동(#642 TaskActivity, product-run-list.md 인계 5, #661 이경준 리뷰)을
              둔다 — 끝나면 제품 서버가 활동 조회를 닫는다(403) */}
          {!job.input.workflow_mode && !ended && job.task_id && verified ? (
            <TaskActivity key={job.task_id} taskId={job.task_id} enabled={visible} />
          ) : null}
        </div>
        {/* 오른쪽 진행 요약: 서버가 점검 중에 주는 값만(전송 통제 · 탐색 현황 · 커버리지 · 점검 정보).
            발견은 가운데에 둔다(점검 중엔 발견 후보, 끝나면 저장된 결과) */}
        <aside className={s.vi} id="lInsp" ref={liveInspRef} aria-label={inspLabel}>
          {picked && job.task_id ? (
            <LiveCandidateDetail
              entry={picked}
              poll={ended ? saved : pickedRem}
              host={host}
              taskId={job.task_id}
              onBack={() => setPick(null)}
            />
          ) : null}
          {apShown ? (
            <>
              <div className={s["vi-back"]}>
                <BackButton to="진행 요약" onClick={() => setApTask(null)} />
              </div>
              <InspHead
                eyebrow={<span className={s.host}>{host}</span>}
                title={
                  ended
                    ? `승인 기록 ${savedAp.data?.approvals.length ?? 0}`
                    : `처리된 요청 ${a.rows.filter((r) => r.state !== "pending").length}`
                }
              />
              {ended && job.task_id ? (
                <SavedApprovalHistory rows={savedAp.data?.approvals ?? []} task={job.task_id} />
              ) : (
                <ApprovalHistory a={a} planTitle={planTitle} />
              )}
            </>
          ) : null}
          {!(picked && job.task_id) && !apShown ? (
            <>
              <InspHead
                eyebrow={
                  <>
                    {`${urlStateText(job.state, verified)} · `}
                    <span className={s.host}>{host}</span>
                  </>
                }
                title="진행 요약"
              />
              <Isec t="전송 통제">
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
                      <div className={s.sub}>{heldReason(a.pending)}</div>
                    </div>
                    <div className={s.cell}>
                      <div className={s.k}>전송 불명</div>
                      <div className={s.v}>{known(ap.unknown)}</div>
                      <div className={s.sub}>응답 없음</div>
                    </div>
                  </div>
                ) : null}
                {g.runReadable ? null : <p className={s.empty}>{sendWaitText}</p>}
              </Isec>
              {flow ? (
                <Isec t="탐색 현황">
                  <div className={s.vbar}>
                    <span>
                      방문 <b>{flow.counts.visited}</b>
                    </span>
                    <span>
                      발견한 API <b>{flow.counts.found}</b>
                    </span>
                    <span>
                      심화 선택 <b>{selected}</b>
                    </span>
                  </div>
                </Isec>
              ) : null}
              {cov.data?.enabled && cov.data.denominator > 0 ? (
                <Isec t="커버리지">
                  <div className={s.prog} aria-hidden="true">
                    <i style={{ width: `${Math.round((cov.data.pct ?? 0) * 100)}%` }} />
                  </div>
                  <div className={s.vbar}>
                    <span>
                      검사함 <b>{cov.data.tested}</b>
                      {` / ${cov.data.denominator} 자산`}
                    </span>
                    <span>
                      <b>{`${Math.round((cov.data.pct ?? 0) * 100)}%`}</b>
                    </span>
                  </div>
                </Isec>
              ) : null}
              <Isec t="점검 정보">
                <UrlFacts job={job} />
              </Isec>
            </>
          ) : null}
        </aside>
        <PaneToggle />
      </div>
    </>
  );
}

// ---------- 보고서 ----------

/** 목록 순서: 확정 → 검토 필요 → 미실행 → 결과 불명 → 정상 대조. */
export const ORDER: readonly DiagnosticOutcome[] = ["confirmed", "review", "not_executed", "unknown", "negative"];
/** 심각도 순서. 정보성도 집계하며 미평가와 구분한다. */
const SEV_KEYS: readonly SevKey[] = ["crit", "hi", "mid", "lo", "info"];
/** 위험도 딱지. 서버가 심각도를 주지 않았으면 "등급 없음". */
export function Sev({ e }: { e: RemediationEntry }) {
  const k = sevKey(entrySeverity(e) ?? "");
  return (
    <span
      className={cx(s.sev, k ? s[k] : s["sev-none"])}
      title={e.severity_source === "worker_estimate" ? (e.severity_rationale ?? "") : undefined}
    >
      {entrySeverityLabel(e)}
    </span>
  );
}

/** 서버가 값을 모른다고 적은 맥락 칸 ("unknown" · "미확인…")은 없는 것으로 본다. */
const knownText = (v?: string): v is string => !!v && v !== "unknown" && !v.startsWith("미확인");

/**
 * 발견의 엔드포인트 (시안 ep(m,p)): 서버가 저장한 맥락의 경로 템플릿이 있을 때만. 메서드는 그 경로의 관찰이
 * 한 가지 메서드로 맞을 때만 붙인다(여러 개 · 모름이면 경로만). 없으면 null.
 */
const HTTP_METHOD = /^[A-Z]{3,7}$/;

/** "GET /a" · "/a" → 메서드 · 경로 (경로가 / 로 시작하지 않으면 null). 메서드가 글에 없으면 따로 받은 값. */
function splitEndpoint(raw: string, method?: string): { m: string | null; p: string } | null {
  const text = raw.trim();
  const sp = text.indexOf(" ");
  const head = sp > 0 ? text.slice(0, sp) : "";
  const hasMethod = HTTP_METHOD.test(head);
  const p = hasMethod ? text.slice(sp + 1).trim() : text;
  if (!p.startsWith("/")) return null;
  if (hasMethod) return { m: head, p };
  const given = method?.toUpperCase();
  return { m: given && HTTP_METHOD.test(given) ? given : null, p };
}

/**
 * 관련 경로 (메서드 · 경로 · 그 밖 경로 수). 대표 경로(context.endpoint)가 있으면 그것, 메서드는 같은 경로 관찰이 한 가지로
 * 모일 때만. 대표 경로가 없으면 — 서버가 엔진 후보에는 채우지 않는다(db/diagnostic_remediation.go) — E②에 기록된
 * 요청 시도(차단된 시도 포함)의 경로로 보인다. 관찰 경로가 여럿이면 첫 경로와 나머지 수. 취약한 경로로 단정하지 않는다.
 */
export function entryEp(e: RemediationEntry): { m: string | null; p: string; more: number } | null {
  const top = e.context?.endpoint;
  const obs = e.context?.observations ?? [];
  if (knownText(top)) {
    const ep = splitEndpoint(top) ?? { m: null, p: top };
    let m = ep.m;
    if (!m) {
      const ms = new Set(
        obs.filter((o) => o.endpoint === top && knownText(o.method)).map((o) => String(o.method).toUpperCase()),
      );
      m = ms.size === 1 ? [...ms][0] : null;
    }
    return { m: m && HTTP_METHOD.test(m) ? m : null, p: ep.p, more: 0 };
  }
  const seen = new Map<string, { m: string | null; p: string }>();
  for (const o of obs) {
    if (!knownText(o.endpoint)) continue;
    const ep = splitEndpoint(o.endpoint, o.method);
    if (ep && !seen.has(`${ep.m ?? ""} ${ep.p}`)) seen.set(`${ep.m ?? ""} ${ep.p}`, ep);
  }
  const [first, ...rest] = [...seen.values()];
  return first ? { m: first.m, p: first.p, more: rest.length } : null;
}

/** 대표 경로와 E② 요청 시도의 관찰 경로를 구분한다. 둘 다 취약점 확정 근거가 아니다. */
function entryPathLabel(e: RemediationEntry): string {
  if (knownText(e.context?.endpoint) || !entryEp(e)) return "관련 경로";
  return "관찰된 요청 경로";
}

/** 엔드포인트 글 (시안 ep · pth: 메서드 딱지 + 경로, 경로의 {이름} 자리는 기울임). */
export function EntryEp({ e }: { e: RemediationEntry }) {
  const ep = entryEp(e);
  if (!ep) return null;
  return (
    <>
      {ep.m ? (
        <>
          <span className={cx(s.m, s[ep.m])}>{ep.m}</span>{" "}
        </>
      ) : null}
      <span className={s.p}>
        {ep.p.split(/(\{\w+\})/).map((x, i) =>
          // biome-ignore lint/suspicious/noArrayIndexKey: 고정 문자열을 쪼갠 조각이라 순서가 곧 정체성
          /^\{\w+\}$/.test(x) ? <i key={i}>{x}</i> : x,
        )}
      </span>
      {ep.more ? ` 외 ${ep.more}개` : null}
    </>
  );
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
  return typeof value === "string" ? value : JSON.stringify(value, null, 2);
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
      <div className={s.acts}>
        {btn("shared", "html", "공유용 사본", true)}
        {btn("internal", "html", "내부 상세 사본")}
        {btn("shared", "docx", "Word 공유용")}
        {btn("internal", "docx", "Word 내부 상세")}
      </div>
      {st.err ? <ReportReadError error={st.err} /> : null}
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
  onOpen,
}: {
  e: RemediationEntry | undefined;
  host: string | null;
  taskId: string;
  onReload: () => void;
  /** 있으면 머리 딱지 줄 오른쪽 끝(패널 열고 닫기 버튼 밑)에 '상세 보기' — 이 발견의 상세 페이지를 연다 (보고서) */
  onOpen?: (e: RemediationEntry) => void;
}) {
  if (!e) {
    return (
      <>
        <InspHead title="취약점 상세" />
        <p className={s.empty}>저장된 취약점이 없습니다.</p>
      </>
    );
  }
  const o = diagnosticOutcome(e);
  return (
    <>
      {/* 시안 findingHtml 머리: 유형 · 주소, 제목, 위험도 + 판정 딱지, 엔드포인트 */}
      <InspHead
        eyebrow={
          <>
            {`${e.vuln_class ? e.vuln_class : diagnosticOutcomeLabels[o]} · `}
            <span className={s.host}>{host}</span>
          </>
        }
        title={e.title || e.entry_id}
        chips={
          <>
            <Sev e={e} />
            <Outcome o={o} />
            {onOpen ? (
              <button className={cx(s.btn, s.sm, s["vi-open"])} type="button" onClick={() => onOpen(e)}>
                상세 보기
                <Icon name="arrow" />
              </button>
            ) : null}
          </>
        }
      />
      {/* ARTEX 발견 상세처럼(사용자 11/17) 칸마다 흰 카드로: 맨 위 상태 카드(ID · 위험도 · 판정 · 유형 · 관련 경로 · 발견 시각,
          줄마다 구분선 · 값은 오른쪽), 그 아래 AI 분석 · 재현 절차 · 보안 조치 · AI 프롬프트 · 기술 정보 카드.
          ARTEX 와 달리 위험도 · 처리 상태를 여기서 바꾸지 않는다 — 판정은 저장된 E③ 근거만 따른다(절대규칙 3 · 5) */}
      <div className={s.fdet}>
        <StatusCard e={e} />
        {entrySeverity(e) && e.severity_source === "worker_estimate" ? (
          <Isec t="심각도 근거 · 모델 제안">
            <p>{e.severity_rationale ?? ""}</p>
            <p className={s.help}>이 등급은 검토 우선순위 제안이며 취약점 확정이나 CVSS 점수가 아닙니다.</p>
          </Isec>
        ) : null}
        {!e.available ? (
          <div className={cx(s.note, s.warn)} role="note">
            <Icon name="alert" />
            <span>{e.unavailable_reason}</span>
          </div>
        ) : null}
        <DiagnosticEntrySections
          key={`${taskId}:${e.entry_id}:${e.source_digest}:${e.record?.id ?? 0}:${e.record?.bundle?.input_digest ?? ""}`}
          task={taskId}
          entry={e}
          onReload={onReload}
        />
      </div>
      {/* 계정별 응답 비교 · 다음 단계('연결 전' 자리)와 분류(머리 유형과 겹침)는 뺐다 — 사용자 11/17 '오른쪽 패널 지저분' */}
    </>
  );
}

/**
 * 발견 상태 카드 (ARTEX 발견 상세의 상태 카드): ID · 위험도 · 판정 · 유형 · 관련 경로 · 발견 시각. 오른쪽 패널과 상세 페이지가
 * 같이 쓴다. ARTEX 와 달리 위험도 · 처리 상태를 여기서 바꾸지 않는다 — 판정은 저장된 E③ 근거만 따른다(절대규칙 3 · 5).
 */
function StatusCard({ e, host }: { e: RemediationEntry; host?: string | null }) {
  const o = diagnosticOutcome(e);
  return (
    <Isec t="상태">
      <dl className={s.frows}>
        <div>
          <dt>ID</dt>
          <dd>
            <span className={s.chip}>{e.entry_id}</span>
          </dd>
        </div>
        <div>
          <dt>위험도</dt>
          <dd>
            <Sev e={e} />
          </dd>
        </div>
        <div>
          <dt>판정</dt>
          <dd>
            <Outcome o={o} />
          </dd>
        </div>
        <div>
          <dt>유형</dt>
          <dd>
            {e.vuln_class ? (
              <span className={s["chip-list"]}>
                {vulnClassParts(e.vuln_class).map((part) => (
                  <span key={part} className={s.chip}>
                    {part}
                  </span>
                ))}
              </span>
            ) : (
              "—"
            )}
          </dd>
        </div>
        <div>
          <dt>{entryPathLabel(e)}</dt>
          <dd>{entryEp(e) ? <EntryEp e={e} /> : "—"}</dd>
        </div>
        {host ? (
          <div>
            <dt>점검 대상</dt>
            <dd>{host}</dd>
          </div>
        ) : null}
        <div>
          <dt>발견 시각</dt>
          <dd>{dateTimeOf(e.observed_at) || "—"}</dd>
        </div>
      </dl>
    </Isec>
  );
}

/**
 * 발견 하나의 상세 페이지 (ARTEX 발견 '상세 보기' → 상세 페이지, 사용자 11/17). 오른쪽 패널 머리의 [상세 보기]로 열고
 * 보고서 가운데를 이 페이지로 바꾼다:
 * 왼쪽은 AI 분석 · 재현 절차(증거) · 보안 조치 · AI 프롬프트 · 기술 정보 카드, 오른쪽은 따라오는 상태 카드.
 */
export function FindingPage({
  e,
  host,
  taskId,
  onReload,
  onBack,
}: {
  e: RemediationEntry;
  host: string | null;
  taskId: string;
  onReload: () => void;
  onBack: () => void;
}) {
  const o = diagnosticOutcome(e);
  const head = useRef<HTMLHeadingElement>(null);
  // 목록 중간에서 열어도 페이지 맨 위(제목)부터 보이고, 키보드 · 화면 읽기 초점도 제목으로 옮긴다.
  // 부르는 쪽이 발견마다 key 로 새로 그리므로 처음 그릴 때 한 번이면 된다
  useEffect(() => {
    head.current?.scrollIntoView?.({ block: "start" });
    head.current?.focus({ preventScroll: true });
  }, []);
  return (
    <section className={s.fpage} aria-label="취약점 상세 페이지">
      {/* ARTEX 발견 상세 머리: ← · 제목 · 위험도 · 판정 딱지를 한 줄로 (유형 · 대상 · ID 는 오른쪽 상태 카드) */}
      <div className={s["fpage-h"]}>
        <BackButton to="취약점 목록" onClick={onBack} />
        <h2 ref={head} tabIndex={-1}>
          {e.title || e.entry_id}
        </h2>
        <Sev e={e} />
        <Outcome o={o} />
      </div>
      <div className={s["fpage-b"]}>
        <div className={s.fdet}>
          {!e.available ? (
            <div className={cx(s.note, s.warn)} role="note">
              <Icon name="alert" />
              <span>{e.unavailable_reason}</span>
            </div>
          ) : null}
          <DiagnosticEntrySections
            key={`${taskId}:${e.entry_id}:${e.source_digest}:${e.record?.id ?? 0}:${e.record?.bundle?.input_digest ?? ""}`}
            task={taskId}
            entry={e}
            onReload={onReload}
            layout="page"
          />
        </div>
        <aside className={s.fdet} aria-label="취약점 상태">
          <StatusCard e={e} host={host} />
          {/* E③ 확정 단계 판정: 이 후보를 참조하는 제품 판정이 있을 때만(목록과 같은 표시 규칙) */}
          <VerdictDetailCard taskId={taskId} entryId={e.entry_id} />
          {entrySeverity(e) && e.severity_source === "worker_estimate" ? (
            <Isec t="심각도 근거 · 모델 제안">
              <p>{e.severity_rationale ?? ""}</p>
              <p className={s.help}>이 등급은 검토 우선순위 제안이며 취약점 확정이나 CVSS 점수가 아닙니다.</p>
            </Isec>
          ) : null}
        </aside>
      </div>
    </section>
  );
}

/**
 * 보고서 발견 목록 위험도 거르기 값: 전체 · 심각 · 높음 · 중간 · 낮음. '등급 없음'(서버가 심각도를 안 줌)은 거르기로 두지 않는다
 * (사용자 11/17) — 그런 줄은 전체(거르기 안 함)에서 본다. 화면에 '전체' 칸은 두지 않고 고른 등급을 다시 누르면 전체(11/18).
 */
export type SevFilter = "all" | SevKey;
/** 보고서 발견 목록 정렬: 판정 순(기본) · 위험도 순 · 발견 시각 최신 순 · 오래된 순. */
export type EntryOrder = "verdict" | "sev" | "new" | "old";
// 위험도는 #666 entrySeverity 기준(확정 등급 · 근거 있는 후보 예상 등급만, 나머지는 미평가)
const sevOf = (e: RemediationEntry): SevKey | "none" => sevKey(entrySeverity(e) ?? "") ?? "none";
/**
 * 보고서 발견 목록 거르기 · 정렬 (사용자 11/17 '발견 시각 · 위험도별로'). list 는 판정 순으로 정렬된 것을 받고,
 * 위험도 순 · 시각 순은 안정 정렬이라 같은 값끼리는 판정 순이 남는다. 발견 시각을 못 읽는 줄은 시각 순에서 맨 뒤.
 */
export function arrangeEntries(
  list: readonly RemediationEntry[],
  sev: SevFilter,
  order: EntryOrder,
): RemediationEntry[] {
  const out = list.filter((e) => sev === "all" || sevOf(e) === sev);
  const at = (e: RemediationEntry) => {
    const v = Date.parse(e.observed_at);
    return Number.isNaN(v) ? null : v;
  };
  const byTime = (dir: 1 | -1) => (x: RemediationEntry, y: RemediationEntry) => {
    const a = at(x);
    const b = at(y);
    if (a === null || b === null) return Number(a === null) - Number(b === null);
    return dir * (a - b);
  };
  const rank = (e: RemediationEntry) => {
    const k = sevOf(e);
    return k === "none" ? SEV_KEYS.length : SEV_KEYS.indexOf(k);
  };
  if (order === "new") return out.sort(byTime(-1));
  if (order === "old") return out.sort(byTime(1));
  if (order === "sev") return out.sort((x, y) => rank(x) - rank(y));
  return out;
}
const SEV_FILTERS: ReadonlyArray<readonly [SevFilter, string]> = [
  ["all", "전체"],
  ["crit", "심각"],
  ["hi", "높음"],
  ["mid", "중간"],
  ["lo", "낮음"],
  ["info", "정보성"],
];

/** 목록 줄 안에 넣는 글: 줄바꿈을 띄어쓰기로(목록이 깨지지 않게). 서버 글은 가리지 않는다(DLP 폐기). */
const mdInline = (v: string) => v.replace(/\s*\n+\s*/g, " ");
/** 코드 상자: 본문 안의 ` 줄보다 긴 울타리로 감싼다(본문이 상자를 닫지 못하게). */
function mdFence(body: string): string {
  const longest = Math.max(2, ...[...body.matchAll(/`+/g)].map((m) => m[0].length));
  const f = "`".repeat(longest + 1);
  return `${f}text\n${body}\n${f}`;
}

/** 보고서 탭 문서 머리에 넣는 점검 정보 (서버가 준 값만). */
export type PocInfo = {
  targetUrl: string | null;
  mode: string | null;
  model: string | null;
  excluded: string | null;
  /** 탐색 흐름 수(있을 때만): 방문한 페이지 · 발견한 API */
  crawl: { visited: number; found: number } | null;
  productConfirmed?: number;
  productConfirmationNotice?: string | null;
};

/**
 * 보고서 탭의 점검 보고서 (사용자 11/17 — ARTEX '침투 테스트 보고서 (Markdown)' 짜임: 제목(대상) → 설명 → 정보 줄 → 요약 →
 * 취약점(1. [등급] 제목 → 설명 → PoC / 증거, 취약점 사이 구분선)). 제품 서버는 ARTEX 원래 작업 보고서(GET /api/report)를 열지 않으므로
 * (first_diagnosis_startup.go 허용 목록), 이미 읽은 저장 결과(/api/report/diagnostic)로 화면에서 만든다.
 * ARTEX 와 달리 '확인된 취약점'이라 묶지 않고 확정 · 검토 필요를 나눠 센다(절대규칙 5). 정상 대조는 빼고 판정 순.
 * 재현 단계가 없으면 만들지 않고 없다고 적는다. 생성 시각은 서버가 주지 않아 두지 않는다.
 */
export function buildPocMarkdown(entries: readonly RemediationEntry[], info: PocInfo): string {
  const list = entries.filter((e) => diagnosticOutcome(e) !== "negative");
  const count = (o: DiagnosticOutcome) => list.filter((e) => diagnosticOutcome(e) === o).length;
  const out: string[] = [`# 취약점 점검 보고서: ${mdInline(info.targetUrl ?? "대상 미확인")}`, ""];
  out.push(
    "승인한 대상 안에서 전송 통제(E②)를 거쳐 보낸 요청과 저장된 결과로 만든 문서입니다. '검토 필요'는 확정이 아니며, 판정은 저장된 E③ 근거만 따릅니다.",
    "",
  );
  if (info.mode) out.push(`- **점검 방식**: ${mdInline(info.mode)}`);
  if (info.model) out.push(`- **모델**: ${mdInline(info.model)}`);
  if (info.excluded) out.push(`- **제외 경로**: ${mdInline(info.excluded)}`);
  out.push("", "**요약**", "");
  // 확정 전(검토 필요 · 미실행 · 결과 불명)까지 합한 수를 '취약점 n건'으로 쓰면 확정 수처럼 읽혀 합계를 두지 않고 판정별로만
  // 센다. 확정이 맨 앞(절대규칙 5, #773 이동찬 · 이경준 P2)
  out.push(
    `- 취약점: 확정 ${count("confirmed") + (info.productConfirmed ?? 0)}건 · 검토 필요 ${count("review")}건 · 미실행 ${count("not_executed")}건 · 결과 불명 ${count("unknown")}건`,
  );
  if (info.productConfirmed) out.push(`- 제품 확정 판정: ${info.productConfirmed}건`);
  if (info.productConfirmationNotice) out.push(`- ${mdInline(info.productConfirmationNotice)}`);
  if (info.crawl) out.push(`- 탐색: 방문 ${info.crawl.visited} · 발견한 API ${info.crawl.found}`);
  out.push("", "**취약점**", "");
  if (!list.length) out.push("저장된 취약점이 없습니다. 결과가 없다고 안전하다는 뜻은 아닙니다.");
  list.forEach((e, i) => {
    const ep = entryEp(e);
    const at = dateTimeOf(e.observed_at);
    const tag = `[${entrySeverityLabel(e)} · ${diagnosticOutcomeLabels[diagnosticOutcome(e)]}]`;
    // 취약점마다 구분선 + 제목(h2, 진한 띠) — 어디까지가 몇 번 취약점인지 보이게(사용자 11/18)
    if (i > 0) out.push("---", "");
    // 제목 글의 '#'는 이스케이프한다 — 끝의 '###'가 Markdown 제목 닫기로 읽혀 사라지지 않게(#709 리뷰)
    out.push(`## ${i + 1}. ${tag} ${mdInline(e.title || e.entry_id).replace(/#/g, "\\#")}`, "");
    // 오른쪽 상세와 같은 출처별 경로 이름 · 발견 시각, 내부 ID(candidate-n)는 두지 않는다
    const facts = [
      e.vuln_class ? `유형 ${mdInline(e.vuln_class)}` : "",
      ep ? `${entryPathLabel(e)} ${ep.m ? `${ep.m} ` : ""}${mdInline(ep.p)}${ep.more ? ` 외 ${ep.more}개` : ""}` : "",
      at ? `발견 시각 ${at}` : "",
    ].filter(Boolean);
    if (facts.length) out.push(facts.join(" · "), "");
    if (e.summary) out.push(e.summary, "");
    out.push("**요청 시도 · 응답 관찰:**", "");
    if (e.source_load_failed)
      out.push("근거 불러오기 실패. 현재 결속을 확인할 수 없어 재현 단계를 제공하지 않습니다.", "");
    else if (e.steps?.length) {
      for (const st of e.steps) {
        out.push(`- **${mdInline(stepHeading(st))}** — ${mdInline(`${st.method} ${st.path || "비민감 경로 미기록"}`)}`);
        if (st.prerequisites) out.push(`  - 사전 조건: ${mdInline(st.prerequisites)}`);
        if (st.observation) out.push(`  - 관찰: ${mdInline(st.observation)}`);
        if (st.truncated) out.push("  - 이 단계의 관측이 잘렸습니다.");
      }
      if (e.steps_truncated) out.push("", "실행 관측이 잘렸습니다. 전체 재현 절차로 해석하지 마세요.");
      out.push("");
    } else out.push("구조화된 실제 재현 단계 미확보. 실행하지 않은 절차를 만들지 않습니다.", "");
    out.push(e.evidence ? mdFence(prettyEvidence(e.evidence)) : "저장된 증거 본문 없음", "");
    if (e.truncated) out.push("실제 도구 출력이 잘렸습니다. 완전한 재현 증거가 아닙니다.", "");
  });
  return out.join("\n");
}

/** 보고서 탭: 점검 보고서 (ARTEX 모양 — 문서 아이콘 · 제목, [복사], 테두리 상자 안 스크롤). 화면은 가리지 않고, 복사만 그려진 글을 가린다. */
function PocReport({ entries, info }: { entries: readonly RemediationEntry[]; info: PocInfo }) {
  const box = useRef<HTMLDivElement>(null);
  const [msg, setMsg] = useState("");
  // 원문 Markdown 이 아니라 그려진 글을 가려서 복사한다: 클립보드는 화면 밖(팀 채팅 · 이슈)으로 나가므로 가리고(#975 이동찬 P2),
  // 붙여 넣은 곳의 Markdown 이 서식으로 쪼갠 값을 다시 이어 붙이지 않게 그려진 글을 쓴다
  async function copy() {
    const el = box.current;
    try {
      // 상세 보고와 같은 복사 처리: 블록 · 표 줄 단위로 모아 가린다(#975 이동찬 리뷰)
      await navigator.clipboard.writeText(el ? renderedCopyText(el) : "");
      setMsg("보고서 글을 복사했습니다.");
    } catch (error) {
      console.warn("보고서 글 복사 실패", error);
      setMsg("복사 권한을 확인하지 못했습니다.");
    }
  }
  return (
    <section className={s.pocw} aria-label="취약점 점검 보고서">
      <h3 className={s["poc-t"]}>
        <Icon name="doc" />
        취약점 점검 보고서 (Markdown)
      </h3>
      <div className={s["poc-a"]}>
        <button className={cx(s.btn, s.sm)} type="button" onClick={() => void copy()}>
          <Icon name="copy" />
          복사
        </button>
        {msg ? <span role="status">{msg}</span> : null}
      </div>
      <div ref={box} className={s.pocbox}>
        <Markdown text={buildPocMarkdown(entries, info)} />
      </div>
    </section>
  );
}

/** 판정 순서대로 (보고서 목록 · 점검 진행의 끝난 점검 발견이 같이 쓴다). */
export const sortEntries = (entries: readonly RemediationEntry[]) =>
  [...entries].sort((x, y) => ORDER.indexOf(diagnosticOutcome(x)) - ORDER.indexOf(diagnosticOutcome(y)));

/**
 * 저장된 결과 줄 목록 (보고서 '발견 목록' 모양): 위험도 딱지 · 제목 / 엔드포인트 · 유형(없으면 서버 요약) · 판정.
 * 보고서와 점검 진행(끝난 점검)이 같은 모양을 쓰게 한 부품이다.
 */
export function FindingRows({
  entries,
  id,
  chosen = null,
  onPick,
}: {
  entries: readonly RemediationEntry[];
  id?: string;
  chosen?: string | null;
  /** 없으면 누르지 않는 읽기 줄(점검 진행의 끝난 점검 발견) */
  onPick?: (e: RemediationEntry) => void;
}) {
  return (
    <div className={s.list} id={id}>
      {entries.map((e) => {
        const o = diagnosticOutcome(e);
        const on = chosen === e.entry_id;
        // 시안 줄 아래 글: 엔드포인트 · 유형. 둘 다 없으면 서버 요약
        const fe = entryEp(e) ?? e.vuln_class;
        let feBody: ReactNode = null;
        if (fe)
          feBody = (
            <span className={s.fe}>
              <EntryEp e={e} />
              {e.vuln_class ? <span>{e.vuln_class}</span> : null}
            </span>
          );
        else if (e.summary) feBody = <span className={s.fe}>{e.summary}</span>;
        const cells = (
          <>
            <Sev e={e} />
            <span className={s.fm}>
              <span className={s.ft}>{e.title || e.entry_id}</span>
              {feBody}
            </span>
            <span className={s.fs}>
              <Outcome o={o} short />
            </span>
          </>
        );
        if (!onPick)
          return (
            <div key={e.entry_id} className={cx(s.frow, s.still)}>
              {cells}
            </div>
          );
        return (
          <button
            key={e.entry_id}
            className={cx(s.frow, on && s.sel)}
            type="button"
            data-rsel={e.entry_id}
            aria-current={on ? "true" : undefined}
            onClick={() => onPick(e)}
          >
            {cells}
          </button>
        );
      })}
    </div>
  );
}

/** 보고서 탭 (시안 rTabs). 자산 지식 그래프는 KG 조회 v1, 접근통제 지도는 이 점검의 저장 지도를 읽는다. */
const R_TABS = [
  ["list", "취약점 목록"],
  ["graph", "자산 지식 그래프"],
  ["map", "접근통제 지도"],
  ["doc", "보고서"],
  // 끝난 점검의 승인 · 거부 기록(사용자 11/26) — 기록이 있을 때만(showApprovalTab)
  ["approvals", "승인 기록"],
] as const;
type RTab = (typeof R_TABS)[number][0];

/** live 보고서 (URL 진단): 결과 저장이 확인된 실제 task 만. */
export function UrlReportLive() {
  const { route, goTo, openInsp, closeInsp, narrow } = useShell();
  const visible = route === "report";
  const handle = useUrl((st) => st.handle);
  const job = useUrl((st) => st.job);
  const verified = useUrl((st) => st.verified);
  const cap = useUrl((st) => st.cap);
  const flow = useUrl((st) => st.flow);
  const g = useUrlGates();
  const host = useUrlHost();
  const taskId = g.reportReadable && job?.task_id ? job.task_id : null;
  const rem = usePoll(() => fetchRemediation(taskId as string, "internal"), [taskId], {
    enabled: visible && !!taskId,
  });
  // 확정 단계(IDOR) 판정: 요약 '판정 확정' 칸이 일반 후보 판정만 세서 제품 판정 CONFIRMED 가 '확정 0'으로만 보였다(#875
  // 실측 7절 1(나)). 내보내기와 같은 확정 합계를 표시하고, 제품 판정 출처는 아래 줄에 남긴다.
  const verdicts = useProductVerdicts(taskId, visible);
  // 승인 기록 탭: 끝난 점검의 저장 승인 이력(계약 saved-request-approvals). 기록이 없으면 탭을 두지 않는다
  const savedAp = useSavedApprovals(taskId, visible);
  const rTabs = R_TABS.filter(([k]) => k !== "approvals" || showApprovalTab(savedAp));
  const { confirmed: productConfirmed, notice: productConfirmationNotice } = productConfirmationSummary(verdicts);
  // 저장된 실행 기록(diagnosis-run)은 보고서에서 더 쓰지 않는다(사용자 11/19 — 실행 종료 기록 · 요청 수를 뺌)
  // 자산 탐색 숫자 칸 3개(발견한 API · 보낸 요청 · 막은 요청)를 보고서에도(사용자 11/25). 보고서는 끝난 점검이라 전송 기록을
  // 한 번만 읽는다. 탐색 흐름이 없는 점검(flow 없음)은 칸을 두지 않는다
  const crawlE2 = useE2FlowFor(flow ? taskId : null, visible, true);
  // 자산 커버리지 (요약 칸): 서버 task 의 방문 · 검사한 자산 비율(판정 커버리지 아님 · 리뷰 #479).
  // 꺼졌거나 못 읽으면 값을 만들지 않고 "—"
  const covOff = useCoverageForbidden(taskId);
  const cov = usePoll(() => covOff.track(taskId as string, fetchCoverage(taskId as string)), [taskId], {
    enabled: visible && !!taskId && !covOff.forbidden,
  });
  const covPct = cov.data?.enabled && cov.data.pct !== null ? Math.round(cov.data.pct * 100) : null;
  const [tab, setTab] = useState<RTab>("list");
  const tabRefs = useRef<Partial<Record<RTab, HTMLButtonElement | null>>>({});
  const [sel, setSel] = useState<string | null>(null);
  // 다른 취약점을 고르면 오른쪽 패널을 맨 위로(사용자 11/24)
  // [승인 기록] 탭에서 고른 줄(오른쪽 패널, 사용자 11/26). 다른 점검의 고름은 쓰지 않는다
  const [apPick, setApPick] = useState<{
    task: string;
    row: SavedApproval;
    sent: E2RequestRow | undefined;
  } | null>(null);
  const apSel = apPick && apPick.task === taskId ? apPick : null;
  const apMode = tab === "approvals";
  const reportInspRef = useScrollTopOnChange<HTMLElement>(apMode ? `ap:${apSel?.row.id ?? ""}` : sel);
  // 오른쪽 패널 머리 '상세 보기'로 연 발견 (그 발견이 목록에서 사라지면 — 다른 점검 등 — 목록으로 돌아간다).
  // 단독 발견 상세(#finding)도 같은 발견을 보도록 공용 상태(findingPick)에 둔다
  const detail = usePickedFinding(taskId);
  const setDetail = (entry: string | null) => (entry && taskId ? pickFinding(taskId, entry) : clearFindingPick());
  // 발견 목록 거르기 · 정렬 (사용자 11/17)
  const [sevF, setSevF] = useState<SevFilter>("all");
  const [order, setOrder] = useState<EntryOrder>("verdict");
  const title = host ? `${host} 보고서` : "보고서";

  if (!handle || !job || !taskId) {
    return (
      <>
        <UrlHead
          title={title}
          crumb=""
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

  const entries = rem.error ? [] : (rem.data?.entries ?? []);
  const sorted = sortEntries(entries);
  const counts = rem.data && !rem.error ? summarizeDiagnosticEntries(entries) : null;
  const findings = sorted.filter((e) => diagnosticOutcome(e) !== "negative");
  // 발견 칸 보조 글 (사용자 11/18 '간단하게'): 0건 등급은 빼고 확정 · 예상(검토 필요의 예상 심각도) · 위험도 미평가로 묶는다.
  // '확정 심각 0 · 확정 높음 0 · …'처럼 등급마다 다 적으면 어지러웠다. 확정과 예상은 섞지 않는다(절대규칙 5)
  const sevLine = (o: DiagnosticOutcome) =>
    SEV_KEYS.map(
      (k) =>
        [k, findings.filter((e) => diagnosticOutcome(e) === o && sevKey(entrySeverity(e) ?? "") === k).length] as const,
    )
      .filter(([, n]) => n > 0)
      .map(([k, n]) => `${SEVL[k]} ${n}`)
      .join(", ");
  const confirmedSev = sevLine("confirmed");
  const estimatedSev = sevLine("review");
  const noSev = findings.filter((e) => !sevKey(entrySeverity(e) ?? "")).length;
  // 고른 위험도에 해당하는 줄이 (다시 읽은 뒤) 없어졌으면 전체로 본다 — 빈 목록이 '발견 없음'으로 읽히지 않게
  const sevCount = (k: SevFilter) => (k === "all" ? sorted.length : sorted.filter((e) => sevOf(e) === k).length);
  const sevNow: SevFilter = sevCount(sevF) ? sevF : "all";
  // 거르기 칸: 줄이 있는 등급만('전체' 칸은 두지 않는다)
  const sevChips = SEV_FILTERS.filter(([k]) => k !== "all" && sevCount(k) > 0);
  const shown = arrangeEntries(sorted, sevNow, order);
  // 고른 발견도 보이는 줄(거르기 · 정렬 결과) 안에서만 찾는다: 재조회로 위험도가 바뀌어 거르기에서 빠지면 오른쪽 상세 ·
  // '상세 보기'가 목록에 없는 발견에 남지 않고 보이는 줄로 옮긴다(#677 이동찬 리뷰)
  const chosen =
    (sel ? shown.find((e) => e.entry_id === sel) : undefined) ??
    shown.find((e) => diagnosticOutcome(e) !== "negative") ??
    shown[0] ??
    findings[0] ??
    sorted[0];
  const detailEntry = detail ? (sorted.find((e) => e.entry_id === detail) ?? null) : null;
  const profile = cap.kind === "v2" ? cap.catalog.profiles.find((p) => p.id === job.input.profile_id) : undefined;
  let covSub = "읽는 중";
  if (covPct !== null) covSub = `미검사 ${100 - covPct}%`;
  else if (cov.data && !cov.data.enabled) covSub = "이 점검에서 꺼짐";
  else if (cov.data) covSub = "검사 대상 자산 없음";
  else if (covOff.forbidden) covSub = "이 점검에선 제공 안 함";
  else if (cov.error) covSub = "읽지 못함";

  // 탭: 화살표로 옮기고 고른 탭만 Tab 순서에 둔다 (시안 selTab)
  const pickTab = (k: RTab, focus = false) => {
    setTab(k);
    if (focus) tabRefs.current[k]?.focus();
  };
  const onTabKey = (i: number) => (ev: KeyboardEvent<HTMLButtonElement>) => {
    const d = { ArrowRight: 1, ArrowLeft: -1 }[ev.key];
    if (!d) return;
    ev.preventDefault();
    pickTab(rTabs[(i + d + rTabs.length) % rTabs.length][0], true);
  };

  // 보고서 탭(사용자 11/17): 발견마다 PoC 를 Markdown 문서 한 장으로
  let docBody: ReactNode;
  if (rem.error) docBody = <ReportReadError error={rem.error} onRetry={rem.reload} />;
  else if (!rem.data) docBody = <LoadingRow />;
  else {
    const mode = job.input.workflow_mode;
    let modeText = "설치 범위";
    if (mode === "ai_auto_v1") modeText = "AI 자동";
    else if (mode === "user_approval_v1") modeText = "사용자 승인";
    docBody = (
      <PocReport
        entries={sorted}
        info={{
          targetUrl: job.input.target_url,
          mode: modeText,
          model: profile ? `${profile.model_label} · ${profile.model?.external_transfer ? "외부" : "로컬"}` : null,
          excluded: job.input.excluded_paths?.split("\n").filter(Boolean).join(", ") || null,
          crawl: flow ? { visited: flow.counts.visited, found: flow.counts.found } : null,
          productConfirmed,
          productConfirmationNotice,
        }}
      />
    );
  }

  let listBody: ReactNode;
  if (rem.error) listBody = <ReportReadError error={rem.error} onRetry={rem.reload} />;
  else if (!rem.data) listBody = <LoadingRow />;
  else if (!sorted.length)
    listBody = <p className={s.empty}>저장된 결과가 없습니다. 결과가 없다고 안전하다는 뜻은 아닙니다.</p>;
  else
    listBody = (
      <>
        {/* 거르기 · 정렬: 위험도(줄이 있는 등급만, 수와 함께) · 발견 시각 순. '전체 n개' 칸은 오른쪽 'n / n건'과 수가 겹쳐
            두지 않는다(사용자 11/18) — 등급 칸을 누르면 그 등급만, 다시 누르면 전체 */}
        <div className={cx(s.fctl, s.rfil)}>
          {sevChips.length ? (
            // biome-ignore lint/a11y/useSemanticElements: 켜고 끄는 거르기 칸 묶음 (fieldset 은 폼 입력 묶음이라 맞지 않음)
            <div className={s.seg} role="group" aria-label="위험도로 거르기">
              {sevChips.map(([k, label]) => (
                <button
                  key={k}
                  type="button"
                  aria-pressed={sevNow === k}
                  onClick={() => {
                    const to: SevFilter = sevNow === k ? "all" : k;
                    setSevF(to);
                    // 오른쪽 상세에 떠 있던 발견이 거르기로 빠지면 보이는 첫 줄로 옮긴다
                    const next = arrangeEntries(sorted, to, order);
                    if (sel && !next.some((e) => e.entry_id === sel)) setSel(next[0]?.entry_id ?? null);
                  }}
                >
                  {`${label} ${sevCount(k)}개`}
                </button>
              ))}
            </div>
          ) : null}
          <label className={s.rord}>
            <span>정렬</span>
            <select className={s.inp} value={order} onChange={(ev) => setOrder(ev.target.value as EntryOrder)}>
              <option value="verdict">판정 순</option>
              <option value="sev">위험도 순</option>
              <option value="new">발견 시각 최신 순</option>
              <option value="old">발견 시각 오래된 순</option>
            </select>
          </label>
          <span className={s.n}>{`${shown.length} / ${sorted.length}건`}</span>
        </div>
        <FindingRows
          entries={shown}
          id="rList"
          chosen={chosen?.entry_id ?? null}
          onPick={(e) => {
            setSel(e.entry_id);
            openInsp();
          }}
        />
      </>
    );

  // 머리 줄: 시작 · 소요 · 모델 · 모델 사용량(입 · 출). 시작 · 소요는 서버가 확인한 started_at · finished_at 만 쓰고,
  // 소요는 둘 다 있을 때만 센다(diagnosis-run-timing.md 화면 인계). 없으면 '연결 전' — saved_at 등으로 추정하지 않는다.
  // 자산 커버리지는 아래 요약 칸에 있어 머리에서 빼고 그 자리에 모델 사용량을 둔다(사용자 11/17).
  // 모델 사용량은 이 진단의 전송 통제 예산 기록(job.model_usage), 기록이 없으면 "—"
  const usage = job?.model_usage;
  const startText = runStartText(job);
  const durationSec = runDurationSec(job);
  // 두 줄(사용자 11/25): 위 시작 · 소요, 아래 모델 · 토큰
  const meta = (
    <>
      <span className={s["vh-meta-ln"]}>
        <span>시작 {startText ? <b>{startText}</b> : <NotYet why={runStartMissingWhy(job)} />}</span>
        <span>
          소요 {durationSec !== null ? <b>{fmtDur(durationSec)}</b> : <NotYet why={runDurationMissingWhy(job)} />}
        </span>
      </span>
      <span className={s["vh-meta-ln"]}>
        {profile ? (
          <span>
            모델 <b>{`${profile.model_label} · ${profile.model?.external_transfer ? "외부" : "로컬"}`}</b>
          </span>
        ) : null}
        {/* 머리 줄은 토큰(입 · 출)에서 끝낸다(사용자 11/19 — 길어서 '모델 사용량' → '토큰', 요청 수는 두지 않음) */}
        <span title={usage ? tokenExact(usage) : undefined}>토큰 {usage ? <TokenChips u={usage} /> : <b>—</b>}</span>
      </span>
    </>
  );

  return (
    <>
      <UrlHead
        title={title}
        crumb=""
        parent={FINISHED}
        host={host}
        pill={<UrlPill job={job} verified={verified} />}
        acts={detailEntry ? undefined : <InspOpenButton>취약점 상세</InspOpenButton>}
        meta={meta}
        // 다른 세 화면처럼 머리에 단계 줄(자산 탐색 · 범위 확인 · 점검 진행 · 보고서, 눌러서 이동)을 둔다(사용자 11/17 —
        // 보고서에서 앞 단계로 돌아갈 길이 주소 입력뿐이었다)
        phase="report"
        // 상세 페이지에서 단계 줄 '보고서'를 누르면 ← 처럼 발견 목록으로 (사용자 11/19 — 주소가 같아 그냥은 안 바뀐다)
        onCurrentPhase={detailEntry ? () => setDetail(null) : undefined}
      />
      {/* 상세 페이지를 열면 가운데를 그 페이지로 바꾸고 오른쪽 패널은 두지 않는다(페이지 안에 상태 카드가 있다) */}
      {detailEntry ? (
        <div className={cx(s.vb, s.solo)}>
          <div className={s.vm}>
            <FindingPage
              key={detailEntry.entry_id}
              e={detailEntry}
              host={host}
              taskId={taskId}
              onReload={rem.reload}
              onBack={() => setDetail(null)}
            />
          </div>
        </div>
      ) : (
        <div className={s.vb}>
          <div className={s.vm}>
            {counts ? (
              // 요약 칸 (시안 짜임: 발견 · 판정 확정 · 검토 필요/판단 불가 · 자산 커버리지)
              <section className={s.strip} aria-label="판정 집계">
                <div className={s.cell}>
                  <div className={s.k}>취약점</div>
                  <div className={s.v}>{findings.length}</div>
                  <div className={s.sub}>
                    {[
                      confirmedSev && `확정 ${confirmedSev}`,
                      estimatedSev && `검토 필요 ${estimatedSev}`,
                      noSev ? `위험도 미평가 ${noSev}` : "",
                    ]
                      .filter(Boolean)
                      .join(" · ")}
                  </div>
                </div>
                <div className={s.cell}>
                  <div className={s.k}>판정 확정</div>
                  <div className={s.v}>{counts.confirmed + productConfirmed}</div>
                  <div className={s.sub}>
                    {productConfirmationNotice ??
                      (productConfirmed ? `증거 충분 · 확정 단계 확정 ${productConfirmed}` : "증거 충분")}
                  </div>
                </div>
                <div className={s.cell}>
                  <div className={s.k}>{`${diagnosticOutcomeLabels.review} / 판단 불가`}</div>
                  <div className={s.v}>
                    {counts.review}
                    <small>{`/ ${counts.not_executed + counts.unknown}`}</small>
                  </div>
                  <div className={s.sub}>추가 확인 권장</div>
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
                      "—"
                    )}
                  </div>
                  <div className={s.sub}>{covSub}</div>
                </div>
              </section>
            ) : null}
            {flow ? <CrawlMeters flow={flow} e2={crawlE2.data} /> : null}
            <section className={s.sec}>
              <div className={s["seg-tabs"]} role="tablist" aria-label="보고서 내용" id="rTabs">
                {rTabs.map(([k, label], i) => (
                  <button
                    key={k}
                    ref={(el) => {
                      tabRefs.current[k] = el;
                    }}
                    type="button"
                    role="tab"
                    id={`rtab-${k}`}
                    aria-controls={`rp-${k}`}
                    aria-selected={tab === k}
                    tabIndex={tab === k ? 0 : -1}
                    onClick={() => pickTab(k)}
                    onKeyDown={onTabKey(i)}
                  >
                    {label}
                    {k === "list" ? <span className={s.c}>{sorted.length}</span> : null}
                    {k === "approvals" && savedAp.data ? (
                      <span className={s.c}>{savedAp.data.approvals.length}</span>
                    ) : null}
                  </button>
                ))}
              </div>
              <div role="tabpanel" id="rp-list" aria-label="취약점 목록" hidden={tab !== "list"}>
                {listBody}
                {/* 제품 확정 단계 판정은 위 목록(평가 · 일반 발견)과 섞지 않고 따로(#801 §3). 제품 관리 중계가 아니면(404) 두지 않는다 */}
                <ProductVerdictReport taskId={taskId} active={visible && tab === "list"} poll={verdicts} />
              </div>
              <div role="tabpanel" id="rp-graph" aria-label="자산 지식 그래프" hidden={tab !== "graph"}>
                {/* 제품 소유 · 접근 그래프는 접근통제 지도와 같은 자료(product-access-map)라 여기 두지 않고 접근통제 지도 탭의
                    [격자 | 그래프] 보기로 옮겼다(11/24 — 자산 지식 그래프와 혼동) */}
                {/* 탐색이 관찰한 자산 · 엔드포인트 관계(coverage-graph)에 포함 근거 · 출처(KG 조회 v1)를 붙인다 — 노드 상세에 그 자산의
                    근거 · 출처, 그래프 아래에 요약 한 줄(11/26: 따로 늘어놓던 글 목록을 합침) */}
                <AssetGraphTab taskId={taskId} active={visible && tab === "graph"} />
              </div>
              <div
                role="tabpanel"
                id="rp-map"
                aria-label="접근통제 지도"
                hidden={tab !== "map"}
                style={{ display: tab === "map" ? "flex" : undefined, flexDirection: "column", gap: 12 }}
              >
                {/* 이 점검의 저장 지도(GET /api/tasks/{id}/access-map, #683). 탭이 보일 때만 읽고 409(평가 KG 지도 범위 없음 — KG
                    미등록 · 실행 전 · 세대 무효)·503 은 그 뜻대로 보인다 — 빈 격자 · 예시로 대신하지 않는다. 제품 회차는 이 경로가
                    늘 409("현재 세대의 지도 조회 범위를 확인할 수 없습니다", #875 §3.4)라 패널이 제품 지도를 [격자 | 그래프]로 따로 그린다.
                    넓게 보려면 지도 화면으로 간다 */}
                <AccessMapPanel active={visible && tab === "map"} />
                <div>
                  <button className={s.btn} type="button" onClick={() => goTo("accessmap")}>
                    접근통제 지도 화면 열기
                    <Icon name="arrow" />
                  </button>
                </div>
              </div>
              {/* '증거 · 요청 기록' 탭은 없앴다(사용자 11/19 — 취약점 목록과 겹치고 볼 게 적음). 내려받기 · 심화 점검으로
                  고른 경로는 보고서 문서 아래로, 요청 · 탐색 수는 머리 줄로 */}
              <div
                role="tabpanel"
                id="rp-doc"
                aria-label="보고서"
                hidden={tab !== "doc"}
                style={{ display: tab === "doc" ? "flex" : undefined, flexDirection: "column", gap: 26 }}
              >
                {docBody}
                <Download taskId={taskId} />
                <WorkflowReport />
              </div>
              <div role="tabpanel" id="rp-approvals" aria-label="승인 기록" hidden={tab !== "approvals"}>
                {tab === "approvals" ? (
                  <ReportApprovals
                    poll={savedAp}
                    task={taskId}
                    current={apSel?.row.id ?? null}
                    onPick={(row, sent) => {
                      setApPick({ task: taskId, row, sent });
                      openInsp();
                    }}
                  />
                ) : null}
              </div>
            </section>
          </div>
          <aside className={s.vi} id="rInsp" ref={reportInspRef} aria-label={apMode ? "승인 기록 상세" : "취약점 상세"}>
            {apMode ? (
              <ReportApprovalDetail pick={apSel} task={taskId} />
            ) : (
              <FindingDetail
                e={chosen}
                host={host}
                taskId={taskId}
                onReload={rem.reload}
                onOpen={(e) => {
                  setDetail(e.entry_id);
                  // 좁은 화면은 상세가 서랍이라 닫아야 가운데 페이지가 보인다(넓은 화면은 페이지가 패널 자리를 쓴다)
                  if (narrow) closeInsp();
                }}
              />
            )}
          </aside>
          <PaneToggle />
        </div>
      )}
    </>
  );
}
