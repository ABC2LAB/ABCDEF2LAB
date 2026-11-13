"use client";

// 이어 보는 진단 한 줄 · 오른쪽 상세 (시안 activeRowHtml · doneRowHtml · inspFor 의 진행 중 · 끝난 점검 칸).
// 대시보드 · 진행 중인 점검 · 끝난 점검이 같이 쓴다. 값은 서버 스토어(useUrl)와 저장된 판정(보고서 원장)만 쓴다.
// 시안 줄 · 상세의 칸은 모두 둔다. 서버가 주지 않는 값(시작 · 남은 시간 · 소요 · 진행률 % · 막대 · 점검 중 발견)은
// 만들지 않고 "—" · 빈 막대 · '연결 전' 딱지로 둔다.
// 묶음 계산(recRunKind · recDoneKinds)도 여기 둔다: 줄 딱지와 목록 필터가 같은 기준을 쓴다.

import type { ReactNode } from "react";

import type { URLDiagnosisJob } from "@/lib/diagnosis/diagnosis-url";
import {
  type DiagnosticOutcome,
  diagnosticOutcome,
  diagnosticOutcomeLabels,
  summarizeDiagnosticEntries,
} from "@/lib/diagnosis/diagnostic-results";
import type { RemediationEntry } from "@/lib/diagnosis/remediation";
import type { RequestApproval } from "@/lib/diagnosis/request-approvals";

import { SEVL } from "../../data/findings";
import { cx } from "../../lib/cx";
import { fetchCoverage } from "../../live/api";
import { clockOf, planState, sevKey } from "../../live/map";
import { NotYet } from "../../live/parts";
import { disconnectUrl, useUrl } from "../../live/urlDiagnosis";
import {
  scanViewFor,
  URL_ACTIVE_STATES,
  URL_ENDED_STATES,
  URL_PRESTART_STATES,
  type UrlState,
  urlHost,
  urlOutcomeText,
  urlStateText,
} from "../../live/urlMap";
import { usePoll } from "../../live/usePoll";
import { flowStageText, type WorkflowSnapshot } from "../../live/workflow";
import { useShell } from "../../shell";
import { useWb } from "../../store";
import { Icon } from "../../ui/icons";
import { InspHead, Isec, Kv } from "../../ui/parts";
import s from "../../workbench.module.css";
import type { Approvals } from "./approvals";
import { UrlNotices, UrlPill, UrlRefreshButton } from "./urlParts";

const MODE_TEXT = { ai_auto_v1: "AI 자동", user_approval_v1: "사용자 승인", none: "설치 범위" } as const;

export type RunKind = "run" | "wait" | "scope";
export type DoneKind = "hit" | "review" | "clean" | "und";
type FlowStage = WorkflowSnapshot["stage"];

/**
 * 진행 중 점검의 상태 묶음. 승인 대기(시작 전 범위 승인 · 요청 승인 대기) → 범위 확인 대기(탐색 완료 · 경로 선택 대기)
 * → 점검 중(엔진이 움직이는 상태). 상태를 확인하지 못했거나 어느 것도 아니면 null.
 */
export function recRunKind(
  state: UrlState,
  verified: boolean,
  stage: FlowStage | null,
  pending: number,
): RunKind | null {
  if (!verified) return null;
  if (state === "awaiting_approval" || pending > 0) return "wait";
  if (stage === "awaiting_selection") return "scope";
  return URL_ACTIVE_STATES.includes(state) ? "run" : null;
}

/**
 * 끝난 점검의 결과 묶음 (팀 lib 판정 집계 그대로). 실행 마감(completed)된 점검만 나눈다.
 * 확정 → 발견 있음, 검토 필요 → 검토 필요, 미실행 · 결과 불명 → 판단 불가,
 * 정상 대조 결과가 있고 그 넷이 0 이며 실행 결과가 complete(계획한 작업을 모두 마침) → 발견 없음.
 * partial(끝나지 않은 · 중지된 작업이 있음)은 정상 대조 결과만 있어도 발견 없음으로 두지 않는다(절대규칙 5, #440).
 * 보고서를 못 읽었거나 판정 기록이 없으면 빈 목록('전체' 에만).
 */
export function recDoneKinds(
  state: UrlState,
  counts: Record<DiagnosticOutcome, number> | null,
  outcome?: URLDiagnosisJob["outcome"],
): DoneKind[] {
  if (state !== "completed" || !counts) return [];
  const out: DoneKind[] = [];
  if (counts.confirmed > 0) out.push("hit");
  if (counts.review > 0) out.push("review");
  if (counts.not_executed + counts.unknown > 0) out.push("und");
  if (!out.length && counts.negative > 0 && outcome === "complete") out.push("clean");
  return out;
}

/** 끝난 점검의 저장된 판정 (보고서 원장을 읽었을 때만). found 는 정상 대조를 뺀 항목. */
export interface JobResult {
  counts: Record<DiagnosticOutcome, number>;
  found: readonly RemediationEntry[];
  kinds: DoneKind[];
}

/** 저장된 판정 → 줄 · 상세가 쓰는 집계. */
export function jobResult(job: Pick<URLDiagnosisJob, "state" | "outcome">, entries: RemediationEntry[]): JobResult {
  const counts = summarizeDiagnosticEntries(entries);
  return {
    counts,
    found: entries.filter((e) => diagnosticOutcome(e) !== "negative"),
    kinds: recDoneKinds(job.state, counts, job.outcome),
  };
}

/**
 * 이 점검의 자산 커버리지 % (서버 task 의 방문 · 검사한 자산 비율, 판정 커버리지 아님 · 리뷰 #479).
 * 결과가 저장된 점검만 읽고, 꺼졌거나 못 읽으면 null(만들지 않음).
 */
export function useCoveragePct(taskId: string | null, enabled: boolean): number | null {
  const cov = usePoll(() => fetchCoverage(taskId as string), [taskId], { enabled: enabled && !!taskId });
  if (!taskId || !cov.data?.enabled || cov.data.pct === null) return null;
  return Math.round(cov.data.pct * 100);
}

/** 상세에 넘길 보낼 요청 승인 대기 줄: 사용자 승인 모드가 아니면 없음([]), 아직 못 읽었으면 모름(null). */
export function pendingRows(on: boolean, a: Approvals): readonly RequestApproval[] | null {
  if (!on) return [];
  return a.poll.data ? a.pending : null;
}

/** 판정 한 줄 (시안 vText "확정 · 의심 · 판단 불가" 를 팀 lib 이름으로). */
const verdictText = (c: Record<DiagnosticOutcome, number>) =>
  (["confirmed", "review", "not_executed", "unknown"] as const)
    .map((k) => `${diagnosticOutcomeLabels[k]} ${c[k]}`)
    .join(" · ");

/** 확정 발견의 심각 · 높음 수 (시안 끝난 점검 줄 · 상세의 위험도 딱지). 확정이 아닌 후보는 세지 않는다. */
function topSev(found: readonly RemediationEntry[]): Array<readonly ["crit" | "hi", number]> {
  const conf = found.filter((e) => diagnosticOutcome(e) === "confirmed");
  return (["crit", "hi"] as const)
    .map((k) => [k, conf.filter((e) => sevKey(e.severity ?? "") === k).length] as const)
    .filter(([, n]) => n > 0);
}

/** 시안의 '새 점검' 줄인가 (시작 전 · 자산 탐색 · 범위 확인): 진행 막대 대신 단계만 보인다. */
function isFresh(state: UrlState, stage: FlowStage | null): boolean {
  return URL_PRESTART_STATES.includes(state) || stage === "crawling" || stage === "awaiting_selection";
}

/**
 * 진행 중 점검의 상태 딱지 (시안: 점검 중 · 승인 대기 · 자산 탐색 중 · 범위 확인 대기). 서버 상태 · 탐색 단계 ·
 * 요청 승인 대기로만 고르고, 그 밖(준비 중 · 중지 중 · 상태 미확인 등)은 서버 상태 글 그대로.
 */
export function RunPill({
  job,
  verified,
  stage,
  pending,
}: {
  job: Pick<URLDiagnosisJob, "state">;
  verified: boolean;
  stage: FlowStage | null;
  pending: number;
}) {
  const k = recRunKind(job.state, verified, stage, pending);
  if (k === "wait") return <span className={cx(s.pill, s.wait)}>승인 대기</span>;
  if (k === "scope") return <span className={cx(s.pill, s.wait)}>범위 확인 대기</span>;
  if (k === "run" && stage === "crawling") return <span className={cx(s.pill, s.run)}>자산 탐색 중</span>;
  if (k === "run" && job.state === "running") return <span className={cx(s.pill, s.run)}>점검 중</span>;
  return <UrlPill job={job} verified={verified} />;
}

/**
 * 끝난 점검 줄의 오른쪽 (시안 doneRowHtml side): 확정 발견의 위험도 딱지 + '발견 n',
 * 확정 · 검토 필요 없이 미실행 · 결과 불명만 있으면 '판단 불가 n', 발견 없음은 근거가 있을 때만(절대규칙 5).
 * 판정을 모르면 서버 상태 딱지.
 */
function DoneSide({ job, verified, result }: { job: URLDiagnosisJob; verified: boolean; result: JobResult | null }) {
  if (!result) return <UrlPill job={job} verified={verified} />;
  const c = result.counts;
  const und = c.not_executed + c.unknown;
  if (c.confirmed + c.review > 0)
    return (
      <>
        {topSev(result.found).map(([k, n]) => (
          <span key={k} className={cx(s.sev, s[k])}>{`${SEVL[k]} ${n}`}</span>
        ))}
        <span className={s["r-time"]}>{`발견 ${result.found.length}`}</span>
      </>
    );
  if (und > 0) return <span className={s.pill}>{`판단 불가 ${und}`}</span>;
  if (result.kinds.includes("clean")) return <span className={s.pill}>발견 없음</span>;
  return <UrlPill job={job} verified={verified} />;
}

/** 이어 보는 진단 한 줄 (시안 .row). 누르면 오른쪽 상세(좁은 화면은 서랍). 이어 보는 진단이 없으면 그리지 않는다. */
export function JobRow({
  sel,
  onPick,
  pending,
  result,
  coverage,
}: {
  sel: boolean;
  onPick: () => void;
  /** 보낼 요청 승인 대기 수 */
  pending: number;
  /** 끝난 점검의 저장된 판정 (못 읽었으면 null) */
  result?: JobResult | null;
  /** 끝난 점검의 자산 커버리지 % (모르면 null) */
  coverage?: number | null;
}) {
  const handle = useUrl((st) => st.handle);
  const job = useUrl((st) => st.job);
  const verified = useUrl((st) => st.verified);
  const flow = useUrl((st) => st.flow);
  if (!handle) return null;
  const ended = !!job && URL_ENDED_STATES.includes(job.state);
  const name = handle.input.display_name?.trim() || null;
  let meta: Array<string | null> = [];
  if (job && ended)
    meta = [
      name,
      urlStateText(job.state, verified),
      result ? verdictText(result.counts) : null,
      typeof coverage === "number" ? `자산 커버리지 ${coverage}%` : null,
    ];
  else if (job)
    meta = [
      name,
      flow ? flowStageText[flow.stage] : urlStateText(job.state, verified),
      pending ? `보낼 요청 승인 대기 ${pending}건` : null,
    ];
  let side: ReactNode = <span className={cx(s.pill, s.wait)}>상태 미확인</span>;
  if (job && ended) side = <DoneSide job={job} verified={verified} result={result ?? null} />;
  else if (job) side = <RunPill job={job} verified={verified} stage={flow?.stage ?? null} pending={pending} />;
  // 진행 막대 · % (시안 activeRowHtml): 엔진이 진단하는 점검만. 서버에 진행률이 없어 빈 막대 · '—%'(0% 로 만들지 않음)
  const bar = !!job && !ended && !isFresh(job.state, flow?.stage ?? null);
  return (
    <button
      className={cx(s.row, sel && s.sel)}
      type="button"
      data-url-job={job?.id ?? ""}
      aria-current={sel ? "true" : undefined}
      onClick={onPick}
    >
      <span className={s["r-main"]}>
        <span className={s["r-t"]}>
          <span className={s["r-host"]}>{urlHost(handle.input.target_url)}</span>
        </span>
        <span className={s["r-s"]}>{meta.filter(Boolean).join(" · ") || "상태 확인 중"}</span>
        {bar ? (
          <span className={cx(s["r-bar"], pending > 0 && s.wait)} aria-hidden="true">
            <i style={{ width: 0 }} />
          </span>
        ) : null}
      </span>
      <span className={s["r-side"]}>
        {side}
        {bar ? (
          <span className={s["r-time"]} title="서버가 점검 진행률을 주지 않습니다">
            —%
          </span>
        ) : null}
      </span>
    </button>
  );
}

/** 갈 화면 → 버튼 글 (시안: 진행 화면 열기 · 이어서 보기 · 보고서 열기). */
const OPEN_TEXT = {
  live: "진행 화면 열기",
  crawl: "이어서 보기",
  scope: "이어서 보기",
  new: "이어서 보기",
  report: "보고서 열기",
};

/**
 * 결정 대기 한 줄 (시안 상세 '결정 대기'): 시작 전 범위 승인 · 보낼 요청 승인(가장 빠른 만료) · 점검할 경로 선택.
 * 승인 대기를 아직 모르면(approvals=null) null.
 */
function decideText(
  job: URLDiagnosisJob,
  verified: boolean,
  stage: FlowStage | null,
  approvals: readonly RequestApproval[] | null,
): string | null {
  const parts: string[] = [];
  if (job.state === "awaiting_approval" && verified) parts.push("시작 전 범위 승인");
  if (approvals?.length) {
    const soonest = Math.min(...approvals.map((r) => Date.parse(r.expires_at)).filter((t) => !Number.isNaN(t)));
    const exp = Number.isFinite(soonest) ? ` · 가장 빠른 만료 ${clockOf(soonest)}` : "";
    parts.push(`보낼 요청 ${approvals.length}건${exp}`);
  }
  if (stage === "awaiting_selection") parts.push("점검할 경로 선택");
  if (parts.length) return parts.join(" · ");
  return approvals ? "없음" : null;
}

/** 상세 머리 (시안 inspFor head): 진행 중 · 끝난 점검 · 결과 확인(판단 불가만 있음) · 제목 · 딱지. */
function summaryHead(
  job: URLDiagnosisJob | null,
  verified: boolean,
  stage: FlowStage | null,
  pending: number,
  result: JobResult | null,
): { eyebrow: string; title: string; chips: ReactNode } {
  if (!job) return { eyebrow: "진행 중", title: "새 점검", chips: null };
  if (!URL_ENDED_STATES.includes(job.state)) {
    // 시작 전 · 자산 탐색 · 범위 확인은 시안의 '새 점검' 칸, 그 뒤는 '점검 진행'(시안의 진행률 % 는 서버 값이 없어 붙이지 않음)
    return {
      eyebrow: "진행 중",
      title: isFresh(job.state, stage) ? "새 점검" : "점검 진행",
      chips: <RunPill job={job} verified={verified} stage={stage} pending={pending} />,
    };
  }
  const pill = <UrlPill job={job} verified={verified} />;
  if (!result) return { eyebrow: "끝난 점검", title: urlStateText(job.state, verified), chips: pill };
  const c = result.counts;
  const und = c.not_executed + c.unknown;
  if (c.confirmed + c.review > 0)
    return {
      eyebrow: "끝난 점검",
      title: `발견 ${result.found.length}건`,
      chips: (
        <>
          {topSev(result.found).map(([k]) => (
            <span key={k} className={cx(s.sev, s[k])}>
              {SEVL[k]}
            </span>
          ))}
          {pill}
        </>
      ),
    };
  if (und > 0) return { eyebrow: "결과 확인", title: `판단 불가 ${und}건`, chips: pill };
  return {
    eyebrow: "끝난 점검",
    title: result.kinds.includes("clean") ? "발견 없음" : urlStateText(job.state, verified),
    chips: pill,
  };
}

/**
 * 이어 보는 진단의 상세 (시안 inspFor). 진행 중: 지금 하는 일 · 점검 정보 · 결정 대기 · 화면 열기,
 * 끝난 점검: 발견 수 · 판정 · 보고서 열기 · 다시 점검. 이어 보는 진단이 없으면 그리지 않는다(화면이 빈 안내를 그린다).
 */
export function JobSummary({
  approvals,
  onApprovals,
  result,
  coverage,
}: {
  /** 보낼 요청 승인 대기 줄 (모르면 null) */
  approvals?: readonly RequestApproval[] | null;
  /** '승인 요청 보기' (승인 대기가 있을 때) */
  onApprovals?: () => void;
  /** 끝난 점검의 저장된 판정 (못 읽었으면 null) */
  result?: JobResult | null;
  /** 끝난 점검의 자산 커버리지 % (모르면 null) */
  coverage?: number | null;
}) {
  const { goTo } = useShell();
  const handle = useUrl((st) => st.handle);
  const job = useUrl((st) => st.job);
  const verified = useUrl((st) => st.verified);
  const flow = useUrl((st) => st.flow);
  const cap = useUrl((st) => st.cap);
  const uncertain = useUrl((st) => st.uncertain);
  const error = useUrl((st) => st.error);
  if (!handle) return null;

  const ended = !!job && URL_ENDED_STATES.includes(job.state);
  const host = urlHost(handle.input.target_url);
  const name = handle.input.display_name?.trim() || null;
  const stage = flow?.stage ?? null;
  const to = job ? scanViewFor(job, stage) : null;
  let stageText = "—";
  if (flow) stageText = flowStageText[flow.stage];
  else if (job) stageText = urlStateText(job.state, verified);
  const profile = cap.kind === "v2" ? cap.catalog.profiles.find((p) => p.id === handle.input.profile_id) : undefined;
  const outcomeText = job ? urlOutcomeText(job) : "";
  const pending = approvals?.length ?? 0;
  // 지금 하는 일 (시안 진행 중 상세): 진행 중인 계획 → 지금 보는 경로 → 단계 순으로 서버 값만
  const nowText = flow?.plan?.find((p) => planState(p.state).row === "run")?.title ?? flow?.current_path ?? null;

  const head = summaryHead(job, verified, stage, pending, result ?? null);
  const decide = job && !ended ? decideText(job, verified, stage, approvals ?? null) : null;

  // 시안 점검 정보 줄을 먼저 (진행 중: 시작 · 남은 시간 · 발견 · 결정 대기 / 끝난 점검: 점검일 · 소요 · 커버리지 · 판정).
  // 서버가 주지 않는 값은 '연결 전'. 그 뒤에 서버가 확인한 진단 정보.
  const mockRows: Array<readonly [ReactNode, ReactNode]> = ended
    ? [
        ["점검일", <NotYet key="d" why="서버가 점검 시작 시각을 주지 않습니다" />],
        ["소요", <NotYet key="t" why="서버가 점검 소요 시간을 주지 않습니다" />],
        ["자산 커버리지", typeof coverage === "number" ? `${coverage}%` : "—"],
        ["판정", result ? verdictText(result.counts) : "—"],
      ]
    : [
        ["시작", <NotYet key="s" why="서버가 점검 시작 시각을 주지 않습니다" />],
        ["남은 시간", <NotYet key="r" why="서버가 남은 시간을 주지 않습니다" />],
        ["발견", <NotYet key="f" why="점검 중 발견 수는 서버가 주지 않습니다. 결과가 저장되면 셉니다" />],
        ["결정 대기", decide ?? "—"],
      ];
  const rows: Array<readonly [ReactNode, ReactNode]> = [
    ...mockRows,
    [
      "대상",
      <span key="t" className={s.mono}>
        {handle.input.target_url}
      </span>,
    ],
    ...(name ? [["점검 이름", name] as const] : []),
    ["분석 모델", profile ? profile.model_label : handle.input.profile_id],
    ["진단 모드", MODE_TEXT[handle.input.workflow_mode ?? "none"]],
    ...(ended ? [] : [["단계", stageText] as const]),
    ["실제 task", job?.task_id ? `#${job.task_id}` : "아직 없음"],
    ...(flow ? [["보낸 요청", `${flow.counts.sent}건`] as const] : []),
    ...(outcomeText ? [["실행 결과", outcomeText] as const] : []),
  ];

  const open =
    to && to !== "report" ? (
      <button className={cx(s.btn, !ended && !pending && s.pri)} type="button" onClick={() => goTo(to)}>
        {OPEN_TEXT[to]}
        {!ended && !pending ? <Icon name="arrow" /> : null}
      </button>
    ) : null;
  const again = ended ? (
    <button
      className={cx(s.btn, to !== "report" && s.pri)}
      type="button"
      onClick={() => {
        const url = handle.input.target_url;
        disconnectUrl();
        useWb.getState().nPrefill(url);
        goTo("new");
      }}
    >
      이 주소로 다시 점검
    </button>
  ) : null;

  return (
    <>
      <InspHead
        eyebrow={
          <>
            {`${head.eyebrow} · `}
            <span className={s.host}>{host}</span>
          </>
        }
        title={head.title}
        chips={head.chips}
      />
      {!ended && job ? (
        <Isec t="지금 하는 일">
          <p>{nowText ?? stageText}</p>
        </Isec>
      ) : null}
      <Isec t="점검 정보">
        <Kv rows={rows} />
      </Isec>
      <Isec>
        <div className={s.acts}>
          {pending > 0 && onApprovals ? (
            <button className={cx(s.btn, s.pri)} type="button" onClick={onApprovals}>
              승인 요청 보기
            </button>
          ) : null}
          {to === "report" ? (
            <button className={cx(s.btn, s.pri)} type="button" onClick={() => goTo("report")}>
              보고서 열기
              <Icon name="arrow" />
            </button>
          ) : null}
          {ended ? again : open}
          {ended ? open : null}
        </div>
        {ended ? <p className={s.help}>다시 점검하면 이 점검은 이 브라우저 목록에서 빠집니다.</p> : null}
        <UrlNotices />
        {uncertain || error ? (
          <div className={s.acts}>
            <UrlRefreshButton />
          </div>
        ) : null}
      </Isec>
    </>
  );
}
