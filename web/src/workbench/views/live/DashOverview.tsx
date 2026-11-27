"use client";

// 통합 대시보드(전체 보기, 2026-11-26): 점검 하나가 아니라 최근 점검 전체를 모아 본다.
// 위에서 아래로 '운영 상태 → 지금 할 일 → 판정(추이 · 지금 구성) → 대상별 · 전송 통제 → 확정 취약점 · 진행 중 → 끝난 점검'.
// - 서버에 전체 합계 API 가 없어 목록 한 쪽(최근 n건)의 점검마다 읽어 더한다. 그래서 머리에 '최근 n건 기준'을 늘 적는다.
// - 같은 대상을 여러 번 점검하면 같은 취약점이 겹쳐 세지므로, 지금 판정 · 대상별 · 확정 목록은 '대상마다 마지막 점검'만 쓴다.
//   점검마다의 결과는 추이 차트(점검 하나 = 막대 하나, 같은 대상끼리 묶어 시간 순)로 따로 보인다.
// - 확정(증거)만 취약점이다. 검토 필요(AI 후보 · 확정 단계 의심)는 '할 일', 판단 불가는 이유가 있는 상태다. 확정 0 을
//   '취약점 없음'으로 쓰지 않고, 결과가 없거나 못 읽은 점검 · 끝까지 못 간 점검(진단 실패 · 중지)의 0건은 0 으로 그리지
//   않는다(절대 규칙 5).
// - 위험도 색은 확정의 위험도에만 쓴다. 판정은 강조색(확정) · 같은 색 빗금(검토 필요) · 회색 두 단계로 가르고 범례를 늘 둔다.
// - 원형 차트는 쓰지 않는다(양은 막대 길이로). 막대 · 줄은 누르면 그 점검으로 간다.

import { type ReactNode, useEffect, useRef, useState } from "react";

import { ApiError } from "@/lib/diagnosis/api";
import { activeURLRun, endedURLRun, type URLRunSummary } from "@/lib/diagnosis/diagnosis-runs";
import { readURLStatus, type URLDiagnosisJob, urlReasonLabels } from "@/lib/diagnosis/diagnosis-url";
import { diagnosticOutcome } from "@/lib/diagnosis/diagnostic-results";
import { type E2Flow, getE2Flow } from "@/lib/diagnosis/e2flow";
import type { RemediationData, RemediationEntry } from "@/lib/diagnosis/remediation";
import type { RequestApproval } from "@/lib/diagnosis/request-approvals";
import { entrySeverity } from "@/lib/diagnosis/severity";

import { SEVL, type SevKey } from "../../data/findings";
import { cx } from "../../lib/cx";
import { fetchRemediation, fetchRequestApprovals, type RequestApprovalState } from "../../live/api";
import { useHiddenRunIDs } from "../../live/hiddenRuns";
import { dateTimeOf, fmtDur, sevKey } from "../../live/map";
import { fetchProductVerdicts, type ProductVerdictList, type ProductVerdictShown } from "../../live/productVerdicts";
import { useRunInput } from "../../live/runInput";
import { runDurationSec, runElapsedSec } from "../../live/runTiming";
import { HEALTH_PATH } from "../../live/stuckRelease";
import { selectUrlRun, useUrl } from "../../live/urlDiagnosis";
import { urlHost, urlStateText } from "../../live/urlMap";
import { useUrlRuns } from "../../live/urlRunList";
import { usePoll } from "../../live/usePoll";
import { readWorkflow, type WorkflowSnapshot } from "../../live/workflow";
import { useShell, type View } from "../../shell";
import { HelpTip } from "../../ui/parts";
import s from "../../workbench.module.css";
import { RunListStatus } from "./runRows";
import { entryEp } from "./UrlLiveLive";
import { UrlHead } from "./urlParts";

/** 끝난 점검 표 · 진행 중 목록에 보이는 줄 수. 나머지는 '전체 보기' */
const OVERVIEW_ROWS = 5;
/** 추이 차트에 그리는 끝난 점검 수(오래된 것부터 왼쪽) */
const TREND_RUNS = 14;
/** 확정 취약점 · 대상별 · 점검별 전송 줄 수 */
const LIST_ROWS = 6;
/** 진행 중 점검(승인 대기 · 단계 · 전송 통제)을 다시 읽는 간격(ms) */
const ACTIVE_POLL_MS = 10_000;
/** 끝난 점검 읽기가 일부 실패했을 때 실패한 점검만 다시 읽는 간격(ms)과 횟수 상한(#978 이동찬 리뷰: 일시 오류가 남지 않게) */
const ENDED_RETRY_MS = 15_000;
const ENDED_RETRIES = 4;
/** 점검마다 읽는 요청을 한 번에 보내는 점검 수(#978 이경준 리뷰 P3: 최근 25건을 한꺼번에 보내지 않는다) */
const READ_CONCURRENCY = 5;
/** 대상 승인이 이 안에 끝나면 '만료 임박'(ms) */
const EXPIRY_SOON_MS = 60 * 60 * 1000;
/** 큰 막대 안에 수를 적는 최소 비율(더 좁으면 범례가 수를 맡는다) */
const INLINE_LABEL_SHARE = 0.12;
/** 추이 차트 세로 눈금 최대 칸 수 */
const Y_TICKS = 4;
/** 추이 차트 대상 묶음마다 더하는 폭(막대 한 칸 = 1) */
const GROUP_PAD = 0.5;
/** 목록 카드의 줄이 이 수 이하면 옆 카드 높이로 늘이지 않는다(한두 줄짜리 카드가 속이 빈 큰 상자가 되지 않게) */
const SHORT_LIST = 2;

type SevF = SevKey | "none";
const SEV_RANK: readonly SevF[] = ["crit", "hi", "mid", "lo", "info", "none"];
const sevOf = (e: RemediationEntry): SevF => sevKey(entrySeverity(e) ?? "") ?? "none";
const sevText = (k: SevF) => (k === "none" ? "위험도 미평가" : SEVL[k]);

/** 전송 통제 수(허용은 응답까지 받은 것만 — 결과 불명 · 송신 실패와 겹쳐 세지 않게, #917 리뷰) */
type E2Sum = { allowed: number; denied: number; unsupported: number; unsure: number };
/**
 * 전송 기록 읽기: 수 · none(저널 없음 — E②를 잡기 전이거나 잡지 않고 끝남, 진단 요청 없음) · invalid(검증 실패) · absent(기록이
 * 없음 — 그 경로가 없거나 아직 없음) · failed(조회 실패)
 */
export type E2Read = E2Sum | "none" | "invalid" | "absent" | "failed";
const isE2Sum = (x: E2Read): x is E2Sum => typeof x === "object";

/**
 * 출처 하나를 읽은 결과. '이 서버에 그 경로가 없음 · 아직 없음'(absent)과 '조회 실패'(failed)를 가른다 — 실패를 0건 · 없음으로
 * 보이지 않으려는 것이다(절대 규칙 5, #978 이동찬 · 이경준 리뷰).
 */
export type SourceRead<T> = { state: "ok"; value: T } | { state: "absent" } | { state: "failed" };
/**
 * '그 경로가 없음'으로 읽는 HTTP 상태: 그 경로가 없는 서버(404)뿐이다. 이 서버는 읽기 실패(DB · 봉인 확인 · 승인 기록 읽기)를
 * 503 으로 답하므로 503 · 409 등은 조회 실패다(#978 머지 뒤 이경준 리뷰 P2: isUnsupported 로 읽으면 실패가 0건으로 보임).
 */
export const ABSENT_STATUSES: readonly number[] = [404];
/** absent 에 든 HTTP 상태는 absent, 그 밖의 오류는 failed. 중단은 그대로 올린다 */
export async function sourceOf<T>(p: Promise<T>, absent: readonly number[] = ABSENT_STATUSES): Promise<SourceRead<T>> {
  try {
    return { state: "ok", value: await p };
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError") throw error;
    return error instanceof ApiError && absent.includes(error.status) ? { state: "absent" } : { state: "failed" };
  }
}
/** 한 번에 limit 개씩만 돌린다(결과 순서는 입력 순서) */
export async function mapLimit<T, R>(items: readonly T[], limit: number, fn: (item: T) => Promise<R>): Promise<R[]> {
  const out = new Array<R>(items.length);
  let next = 0;
  const worker = async () => {
    while (next < items.length) {
      const at = next;
      next += 1;
      out[at] = await fn(items[at] as T);
    }
  };
  await Promise.all(Array.from({ length: Math.max(1, Math.min(limit, items.length)) }, worker));
  return out;
}

/** 확정 하나(확정 목록 줄). where = 관련 경로(같은 제목이 여러 줄일 때 가르는 칸) · 제품 판정이면 자원 참조 */
type Confirmed = {
  key: string;
  title: string;
  sev: SevF;
  at: string;
  where: { m: string | null; p: string; more: number } | null;
};

/** 끝난 점검 하나의 판정 집계 */
export type EndedStat = {
  confirmed: number;
  /** AI 가 낸 검토 필요 후보 */
  aiReview: number;
  /** 확정 단계가 낸 '의심' */
  suspected: number;
  undecided: number;
  /** 확정 단계 '정책상 허용'(소유자 정책상 다른 계정 접근이 허용된 것) */
  allowed: number;
  /** 확정의 위험도(확정 단계 확정은 위험도가 없어 none) */
  sev: Record<SevF, number>;
  items: Confirmed[];
  e2: E2Read;
  /** 제품 판정 읽기: failed 면 위 수에 제품 판정이 빠져 있다(부분 결과) */
  pv: "ok" | "absent" | "failed";
};
/**
 * 끝난 점검 하나: 주소(상태를 읽어 앎, hostFailed = 상태 조회 실패) + 결과(none = 결과 없이 끝남, error = 못 읽음)
 */
export type EndedRun = { host: string | null; hostFailed: boolean; stat: EndedStat | "none" | "error" };
/** 일시 실패일 수 있어 다시 읽을 점검인지: 상태 · 결과 · 제품 판정 · 전송 기록 중 하나라도 조회에 실패함 */
export function needsRetry(run: EndedRun): boolean {
  if (run.hostFailed || run.stat === "error") return true;
  return isStat(run.stat) && (run.stat.pv === "failed" || run.stat.e2 === "failed");
}
const isStat = (x: EndedRun["stat"] | undefined): x is EndedStat => !!x && typeof x === "object";
/** 끝까지 못 간 점검: 진단 실패(failed) · 운영자 중지(cancelled). 정상으로 끝났으면 null */
export type Cut = "failed" | "stopped";
export function cutOf(state: URLRunSummary["state"]): Cut | null {
  if (state === "failed") return "failed";
  return state === "cancelled" ? "stopped" : null;
}
const CUT_TEXT: Readonly<Record<Cut, string>> = { failed: "진단 실패", stopped: "중지" };
/**
 * '대상별' 줄에 판정이 없을 때 글. 모든 점검이 끝까지 못 갔으면(진단 실패 · 중지) 마지막 점검의 까닭, 아니면 결과 없음.
 * 괄호 덧말('0으로 세지 않음')은 붙이지 않는다 — 0 대신 이 글이 보이는 것으로 충분하다(사용자 11/27)
 */
export function targetNoneText(cuts: readonly (Cut | null)[], lastCut: Cut | null): string {
  if (cuts.length && cuts.every((c) => c !== null))
    return `${lastCut === "stopped" ? "중지돼" : "진단 실패로"} 판정 없음`;
  return "결과 있는 점검 없음";
}
const CUT_MARK: Readonly<Record<Cut, string>> = { failed: "✕", stopped: "■" };
/**
 * 판정에 쓸 결과. 끝까지 못 간 점검(진단 실패 · 중지)인데 결과가 0건이면 '취약점 0'이 아니라 판정이 없는 것이라 결과로
 * 치지 않는다(절대 규칙 5, 사용자 11/26: 진단 실패 두 건이 0 으로만 보여 아무것도 없는 것 같음 — 3초 만에 중지한 점검의
 * 0건도 같다). 끝까지 못 갔어도 남은 결과가 있으면 그 결과(일부)는 쓴다.
 */
export function usableStat(row: Pick<URLRunSummary, "state">, run: EndedRun | undefined): EndedStat | null {
  if (!isStat(run?.stat)) return null;
  const st = run.stat;
  const total = st.confirmed + st.aiReview + st.suspected + st.undecided + st.allowed;
  return cutOf(row.state) && total === 0 ? null : st;
}

export function e2ReadOf(read: SourceRead<E2Flow>): E2Read {
  if (read.state !== "ok") return read.state;
  const flow = read.value;
  if (flow.journal === "none") return "none";
  if (flow.journal !== "verified") return "invalid";
  const c = flow.counts;
  const blocked = c.denied + c.unsupported;
  const unsure = c.unknown + c.transient;
  return { allowed: Math.max(0, c.attempts - blocked - unsure), denied: c.denied, unsupported: c.unsupported, unsure };
}

async function readEnded(task: string, signal: AbortSignal): Promise<EndedStat> {
  const [rem, pv, flow] = await Promise.all([
    fetchRemediation(task, "internal"),
    sourceOf(fetchProductVerdicts(task, signal)),
    sourceOf(getE2Flow(task, undefined, signal)),
  ]);
  return endedStatOf(rem, pv, flow);
}

/**
 * 끝난 점검 하나의 집계. 제품 판정 경로가 없는 서버(absent)면 제품 판정 없이 센다. 조회에 실패(failed)했으면 제품 판정이 빠진
 * 부분 결과라는 것을 pv 에 남긴다 — 가짜 0건을 확정 수치로 보이지 않는다(#978 이동찬 P2).
 */
export function endedStatOf(
  rem: Pick<RemediationData, "entries">,
  pv: SourceRead<Pick<ProductVerdictList, "items">>,
  flow: SourceRead<E2Flow>,
): EndedStat {
  const found = rem.entries.filter((e) => diagnosticOutcome(e) !== "negative");
  const by = (o: string) => found.filter((e) => diagnosticOutcome(e) === o).length;
  const pvRows =
    pv.state === "ok" ? pv.value.items.filter((r): r is ProductVerdictShown => r.recordStatus === "verified") : [];
  const pvBy = (v: string) => pvRows.filter((r) => r.oracleValue === v).length;
  const sev = Object.fromEntries(SEV_RANK.map((k) => [k, 0])) as Record<SevF, number>;
  const confirmedEntries = found.filter((e) => diagnosticOutcome(e) === "confirmed");
  for (const e of confirmedEntries) sev[sevOf(e)] += 1;
  sev.none += pvBy("confirmed");
  const items: Confirmed[] = [
    ...confirmedEntries.map((e) => ({
      key: e.entry_id,
      title: e.title,
      sev: sevOf(e),
      at: e.observed_at,
      where: entryEp(e),
    })),
    ...pvRows
      .filter((r) => r.oracleValue === "confirmed")
      .map((r) => ({
        key: r.verdictId,
        title: `${r.resourceType} · 교차 계정 조회`,
        sev: "none" as const,
        at: r.observedAt,
        where: { m: null, p: r.resourceRef, more: 0 },
      })),
  ];
  return {
    confirmed: confirmedEntries.length + pvBy("confirmed"),
    aiReview: by("review"),
    suspected: pvBy("suspected"),
    undecided: by("unknown") + by("not_executed") + pvBy("undecidable"),
    allowed: pvBy("allowed"),
    sev,
    items,
    e2: e2ReadOf(flow),
    pv: pv.state,
  };
}

async function readEndedRun(row: URLRunSummary, owner: string | null, signal: AbortSignal): Promise<EndedRun> {
  const status: SourceRead<URLDiagnosisJob> = owner
    ? await sourceOf(readURLStatus(row.id, owner, signal))
    : { state: "absent" };
  const job = status.state === "ok" ? status.value : null;
  const host = job ? urlHost(job.input.target_url) : null;
  const hostFailed = status.state === "failed";
  const task = row.task_id ?? job?.task_id ?? null;
  if (!row.report_available || !task) return { host, hostFailed, stat: "none" };
  try {
    return { host, hostFailed, stat: await readEnded(task, signal) };
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError") throw error;
    return { host, hostFailed, stat: "error" };
  }
}

/** 끝난 점검 표 '전송 기록' 칸: 감사 기록 검증 결과(기록이 없으면 —, 조회에 실패했으면 못 읽음) */
export function journalText(e2: E2Read): string {
  if (e2 === "absent") return "—";
  if (e2 === "none") return "진단 요청 없음";
  if (e2 === "failed") return "못 읽음";
  return e2 === "invalid" ? "검증 실패" : "검증됨";
}

/** 진행 중 점검 줄의 전송 통제 글(기록이 없으면 null) */
export function activeE2Text(e2: E2Read): string | null {
  if (isE2Sum(e2)) return `허용 ${e2.allowed} · 막음 ${e2Blocked(e2)}`;
  if (e2 === "none") return "진단 요청 전";
  if (e2 === "invalid") return "전송 기록 검증 실패";
  return e2 === "failed" ? "전송 기록 못 읽음" : null;
}

/** 진행 중 점검 하나: 상태 · 탐색 흐름(단계) · 승인 대기 · 전송 통제 */
export type ActiveInfo = {
  job: URLDiagnosisJob | null;
  /** 상태 조회 실패(어느 모드 · 단계인지 모른다 — 승인 대기 · 만료 임박을 확인하지 못함) */
  jobFailed: boolean;
  flow: WorkflowSnapshot | null;
  pending: RequestApproval[] | null;
  /** 사용자 승인 모드인데 승인 대기 조회에 실패함 */
  pendingFailed: boolean;
  /** 탐색 흐름 조회 실패(범위 확인 대기인지 모른다) */
  flowFailed: boolean;
  e2: E2Read;
};

/** 진행 중 점검 하나의 읽은 결과를 모은다(읽기는 readActive) */
export function activeInfoOf(reads: {
  status: SourceRead<URLDiagnosisJob>;
  flow: SourceRead<WorkflowSnapshot | null>;
  approvals: SourceRead<Pick<RequestApprovalState, "approvals">>;
  e2: SourceRead<E2Flow>;
}): ActiveInfo {
  const { status, flow, approvals, e2 } = reads;
  return {
    job: status.state === "ok" ? status.value : null,
    jobFailed: status.state === "failed",
    flow: flow.state === "ok" ? flow.value : null,
    pending:
      approvals.state === "ok" && approvals.value.approvals
        ? approvals.value.approvals.filter((a) => a.state === "pending")
        : null,
    pendingFailed: approvals.state === "failed",
    flowFailed: flow.state === "failed",
    e2: e2ReadOf(e2),
  };
}

/**
 * 진행 중 사용자 승인 점검의 승인 대기를 읽는다. 403 은 실행 결속 없음만이 아니라 작업 조회 일시 실패도 같은 403 이라 가를 수
 * 없어 조회 실패로 둔다(#989 이동찬 P2). 결속은 작업을 띄울 때(admitRun) 잡혀 진행 중 점검에선 거의 늘 있다.
 */
export function readActiveApprovals(
  task: string,
  read: (task: string) => Promise<RequestApprovalState> = fetchRequestApprovals,
): Promise<SourceRead<RequestApprovalState>> {
  return sourceOf(read(task));
}

async function readActive(row: URLRunSummary, owner: string, signal: AbortSignal): Promise<ActiveInfo> {
  const status = await sourceOf(readURLStatus(row.id, owner, signal));
  const job = status.state === "ok" ? status.value : null;
  const task = row.task_id ?? job?.task_id ?? null;
  const none: SourceRead<never> = { state: "absent" };
  const [flow, approvals, e2] = await Promise.all([
    job?.input.workflow_mode && job.started ? sourceOf(readWorkflow(job)) : none,
    task && job?.input.workflow_mode === "user_approval_v1" ? readActiveApprovals(task) : none,
    task && job?.started ? sourceOf(getE2Flow(task, undefined, signal)) : none,
  ]);
  return activeInfoOf({ status, flow, approvals, e2 });
}

/**
 * 끝난 점검들을 읽는다. 한 번에 READ_CONCURRENCY 건씩만 보내고, 조회에 실패한 출처가 있는 점검은 ENDED_RETRY_MS 뒤에 그
 * 점검만 다시 읽는다(최대 ENDED_RETRIES 번). 다 읽은 점검은 목록 revision 이 바뀔 때까지 다시 읽지 않는다.
 */
function useEndedRuns(rows: readonly URLRunSummary[], owner: string | null, enabled: boolean) {
  const key = `${rows.map((r) => `${r.id}:${r.revision}`).join(",")}|${owner ?? ""}`;
  const cache = useRef<{ key: string; runs: Map<string, EndedRun>; tries: number }>({ key, runs: new Map(), tries: 0 });
  if (cache.current.key !== key) cache.current = { key, runs: new Map(), tries: 0 };
  const [retry, setRetry] = useState(false);
  const poll = usePoll(
    async (signal) => {
      const held = cache.current;
      const out = new Map<string, EndedRun>();
      await mapLimit(rows, READ_CONCURRENCY, async (r) => {
        const kept = held.runs.get(r.id);
        out.set(r.id, kept && !needsRetry(kept) ? kept : await readEndedRun(r, owner, signal));
      });
      if (cache.current === held) held.runs = out;
      return out;
    },
    [key],
    { enabled: enabled && rows.length > 0, intervalMs: retry ? ENDED_RETRY_MS : 0 },
  );
  const failed = poll.data ? [...poll.data.values()].some(needsRetry) : false;
  // biome-ignore lint/correctness/useExhaustiveDependencies: 읽기가 끝날 때마다(fetchedAt) 다시 읽을지 정한다
  useEffect(() => {
    if (poll.fetchedAt === null) return;
    const held = cache.current;
    held.tries = failed ? held.tries + 1 : 0;
    setRetry(failed && held.tries <= ENDED_RETRIES);
  }, [poll.fetchedAt]);
  return poll;
}

function useActiveInfos(rows: readonly URLRunSummary[], owner: string | null, enabled: boolean) {
  return usePoll(
    async (signal) => {
      const out = new Map<string, ActiveInfo>();
      await mapLimit(rows, READ_CONCURRENCY, async (r) => {
        out.set(r.id, await readActive(r, owner as string, signal));
      });
      return out;
    },
    [rows.map((r) => `${r.id}:${r.revision}`).join(",")],
    { enabled: enabled && !!owner && rows.length > 0, intervalMs: ACTIVE_POLL_MS },
  );
}

/** 준비 서비스 health(인증 없음). 404 면 이 경로가 없는 서버라 칸을 두지 않는다 */
type Health = "ready" | "not_ready" | "absent" | "error";
async function readHealth(signal: AbortSignal): Promise<Health> {
  try {
    const res = await fetch(HEALTH_PATH, { cache: "no-store", redirect: "error", signal });
    if (res.status === 404) return "absent";
    if (!res.ok) return "not_ready";
    const body: unknown = await res.json();
    return typeof body === "object" && body !== null && (body as { ready?: unknown }).ready === true
      ? "ready"
      : "not_ready";
  } catch {
    return "error";
  }
}

const minutesAgo = (iso: string, now: number) => Math.max(0, Math.round((now - (Date.parse(iso) || now)) / 60000));
const minutesLeft = (iso: string, now: number) => Math.round(((Date.parse(iso) || now) - now) / 60000);
/** 'MM/DD'(서버 시각이 없으면 null) */
const monthDay = (iso: string | undefined | null) =>
  iso ? dateTimeOf(iso).slice(5, 10).replace("-", "/") || null : null;
/** 끝난 순서 열쇠: 마감 시각 → 시작 시각 → ID(시각이 없으면 가장 오래된 쪽) */
const endKey = (r: URLRunSummary) => `${r.finished_at ?? r.started_at ?? "0"}|${r.id}`;
const newestFirst = (a: URLRunSummary, b: URLRunSummary) => (endKey(a) < endKey(b) ? 1 : -1);

/** 이 점검으로 가기: 진행 중이면 점검 진행(범위 고르기 대기면 범위 확인), 끝났으면 보고서(결과 없으면 점검 진행) */
function useOpenRun() {
  const { goTo } = useShell();
  return (row: URLRunSummary, to: View) =>
    void selectUrlRun(row).then((ok) => {
      if (ok) goTo(to);
    });
}

/** 점검 이름: 주소(그 점검 입력을 한 번 읽음), 못 읽으면 표시 이름 · ID */
function RunName({ row }: { row: URLRunSummary }) {
  const owner = useUrlRuns((st) => st.owner);
  const info = useRunInput(row.id, owner);
  return (
    <span className={s["ov-name"]}>
      {row.task_id ? <span className={s["r-no"]}>{`${row.task_id}.`}</span> : null}
      <b>{info ? info.host : row.display_name?.trim() || row.id}</b>
      {info?.mode ? <span className={s["ov-mut"]}>{info.mode}</span> : null}
    </span>
  );
}

/** 지금 할 일 칸: 이름 · 수 · 보조 글, 수가 있으면 노랗게, 누르면 그 화면 */
function TodoTile({
  k,
  n,
  sub,
  onOpen,
  tip,
}: {
  k: string;
  n: number | null;
  sub: ReactNode;
  onOpen?: () => void;
  tip?: ReactNode;
}) {
  const body = (
    <>
      <span className={s["ov-tk"]}>
        {k}
        {tip}
      </span>
      <span className={s["ov-tv"]}>{n ?? "—"}</span>
      <span className={s["ov-ts"]}>{sub}</span>
    </>
  );
  const hot = !!n;
  return onOpen && hot ? (
    <button className={cx(s["ov-tile"], s.hot)} type="button" onClick={onOpen}>
      {body}
    </button>
  ) : (
    <div className={cx(s["ov-tile"], hot && s.hot)}>{body}</div>
  );
}

/** 운영 상태 칩: 글 + 모양(●/▲/–). 색만으로 뜻을 전하지 않는다 */
function Chip({ tone, title, children }: { tone: "ok" | "warn" | "unk" | "bad"; title?: string; children: ReactNode }) {
  const mark = { ok: "●", warn: "▲", unk: "–", bad: "✕" }[tone];
  return (
    <span className={cx(s["ov-chip"], s[`ov-${tone}`])} title={title}>
      <span aria-hidden="true">{mark}</span>
      {children}
    </span>
  );
}

/** 카드: 제목 · 기준 글 · 오른쪽(범례 · 전체 보기) + 본문 */
function Card({
  id,
  title,
  sub,
  side,
  wide,
  fit,
  children,
}: {
  id: string;
  title: string;
  sub?: string;
  side?: ReactNode;
  wide: "w5" | "w7" | "w12";
  /** 옆 카드 높이에 맞춰 늘이지 않고 내용만큼만(한두 줄짜리 목록 카드가 속이 빈 큰 상자가 되지 않게, SHORT_LIST) */
  fit?: boolean;
  children: ReactNode;
}) {
  return (
    <section className={cx(s["ov-card"], s[`ov-${wide}`], fit && s["ov-fit"])} aria-labelledby={id}>
      <div className={s["ov-ch"]}>
        <div>
          <h2 id={id}>{title}</h2>
          {sub ? <p>{sub}</p> : null}
        </div>
        {side}
      </div>
      {children}
    </section>
  );
}

// ── 판정 네 갈래: 확정(강조색) · 검토 필요(같은 색 빗금) · 판단 불가(회색) · 정책상 허용(옅은 회색) ──
type VKey = "c" | "r" | "u" | "a";
const VERDICTS: readonly [VKey, string][] = [
  ["c", "확정"],
  ["r", "검토 필요"],
  ["u", "판단 불가"],
  ["a", "정책상 허용"],
];
const vOf = (x: EndedStat): Record<VKey, number> => ({
  c: x.confirmed,
  r: x.aiReview + x.suspected,
  u: x.undecided,
  a: x.allowed,
});
const vTotal = (v: Record<VKey, number>) => v.c + v.r + v.u + v.a;
const vText = (v: Record<VKey, number>) => VERDICTS.map(([k, l]) => `${l} ${v[k]}`).join(" · ");

function VerdictLegend({ v, fail, stop }: { v?: Record<VKey, number>; fail?: boolean; stop?: boolean }) {
  return (
    <ul className={s["ov-legend"]}>
      {VERDICTS.map(([k, l]) => (
        <li key={k}>
          <span className={cx(s["ov-sw"], s[`ov-v${k}`])} aria-hidden="true" />
          {l}
          {v ? <b>{v[k]}</b> : null}
        </li>
      ))}
      {fail ? (
        <li>
          <span className={s["ov-fk"]} aria-hidden="true">
            ✕
          </span>
          진단 실패
        </li>
      ) : null}
      {stop ? (
        <li>
          <span className={s["ov-sk"]} aria-hidden="true">
            ■
          </span>
          중지
        </li>
      ) : null}
    </ul>
  );
}

/** 세로 눈금: 깔끔한 수(1 · 2 · 5 × 10ⁿ)로 Y_TICKS 칸 이하 */
function niceScale(max: number): { top: number; ticks: number[] } {
  if (max <= 0) return { top: 1, ticks: [0, 1] };
  const raw = max / Y_TICKS;
  const p = 10 ** Math.floor(Math.log10(raw));
  const step = Math.max(1, [1, 2, 5, 10].map((m) => m * p).find((x) => x >= raw) ?? 10 * p);
  const top = Math.ceil(max / step) * step;
  return { top, ticks: Array.from({ length: top / step + 1 }, (_, i) => i * step) };
}

export type TrendCol = { row: URLRunSummary; run: EndedRun | undefined };
/** 추이 차트의 대상 묶음: 같은 대상의 점검을 시간 순으로(왼쪽이 오래된 것) */
export type TrendGroup = { key: string; host: string; cols: TrendCol[] };

/** 점검의 대상 열쇠 · 이름: 주소를 읽었으면 주소, 못 읽었으면 그 점검 하나가 한 묶음 */
const targetKey = (row: URLRunSummary, run: EndedRun | undefined) => run?.host ?? `run:${row.id}`;
const targetName = (row: URLRunSummary, run: EndedRun | undefined) => run?.host ?? (row.display_name?.trim() || row.id);

/**
 * 시간 순 점검 줄을 대상별로 묶는다. 대상이 섞인 한 줄 시간 축은 막대 높낮이가 '추이'가 아니라 대상 차이로 읽혀서
 * (사용자 11/26 점검) 같은 대상끼리 붙인다. 묶음 순서는 마지막 점검이 오래된 대상부터 — 가장 최근에 점검한 대상이 오른쪽 끝.
 */
export function groupTrend(cols: readonly TrendCol[]): TrendGroup[] {
  const by = new Map<string, TrendGroup & { last: number }>();
  cols.forEach((col, i) => {
    const key = targetKey(col.row, col.run);
    const g = by.get(key) ?? { key, host: targetName(col.row, col.run), cols: [], last: i };
    g.cols.push(col);
    g.last = i;
    by.set(key, g);
  });
  return [...by.values()].sort((x, y) => x.last - y.last).map(({ key, host, cols: c }) => ({ key, host, cols: c }));
}

/**
 * 묶음 이름(주소)을 줄바꿈 자리로 나눈다: 점 뒤 · 콜론 앞. 좁은 묶음에서 낱말 중간('blog.exampl / e')이 아니라 여기서 꺾인다.
 * at = 그 조각의 시작 글자 자리(같은 조각이 되풀이돼도 겹치지 않는 열쇠)
 */
export function hostParts(host: string): { at: number; part: string }[] {
  const out: { at: number; part: string }[] = [];
  let at = 0;
  for (const part of host.split(/(?<=\.)|(?=:)/)) {
    out.push({ at, part });
    at += part.length;
  }
  return out;
}

/** 'MM/DD HH:MM'(말풍선 — 같은 날 두 번 점검한 막대를 가른다) */
const monthDayTime = (iso: string | undefined | null) =>
  iso ? dateTimeOf(iso).slice(5, 16).replace("-", "/") || null : null;

/**
 * 대상별 판정 추이: 끝난 점검 하나 = 세로 막대 하나(쌓은 막대), 같은 대상끼리 묶어 시간 순. 결과 없음 · 못 읽음은 0 이
 * 아니라 점선 표시. 끝까지 못 간 점검은 표식(진단 실패 빨간 ✕ · 중지 회색 ■) — 결과가 0건이면 점선 칸, 남은 결과가 있으면
 * 막대 위에(사용자 11/26)
 */
export function TrendChart({ groups, onOpen }: { groups: TrendGroup[]; onOpen: (row: URLRunSummary) => void }) {
  const all = groups.flatMap((g) => g.cols);
  const totals = all.map(({ row, run }) => {
    const st = usableStat(row, run);
    return st ? vTotal(vOf(st)) : 0;
  });
  const peak = Math.max(0, ...totals);
  const { top, ticks } = niceScale(peak);
  const pct = (n: number) => `${(n / top) * 100}%`;
  /** 묶음마다 첫 막대의 전체 순번(말풍선을 오른쪽 절반에서는 왼쪽으로 띄운다) */
  const starts = groups.map((_, gi) => groups.slice(0, gi).reduce((n, g) => n + g.cols.length, 0));
  return (
    // 그릴 막대가 하나도 없으면(전부 판정 없음) 빈 그림 칸을 크게 두지 않는다
    <div className={cx(s["ov-chart"], peak === 0 && s.flat)}>
      <div className={s["ov-yaxis"]} aria-hidden="true">
        {ticks.map((t) => (
          <span key={t} style={{ bottom: pct(t) }}>
            {t}
          </span>
        ))}
      </div>
      <div className={s["ov-plot"]}>
        {ticks.map((t) => (
          <span key={t} className={s["ov-gl"]} style={{ bottom: pct(t) }} aria-hidden="true" />
        ))}
        <ol
          className={s["ov-grps"]}
          // 묶음 폭 = 점검 수 + 반 칸(점검 하나뿐인 대상도 이름이 들어갈 자리를 남긴다)
          style={{ gridTemplateColumns: groups.map((g) => `${g.cols.length + GROUP_PAD}fr`).join(" ") }}
        >
          {groups.map((g, gi) => (
            <li key={g.key} className={s["ov-grp"]} aria-label={`${g.host} · 점검 ${g.cols.length}회`}>
              <ol className={s["ov-cols"]} style={{ gridTemplateColumns: `repeat(${g.cols.length}, minmax(0, 1fr))` }}>
                {g.cols.map(({ row, run }, ci) => {
                  const i = starts[gi] + ci;
                  const at = row.finished_at ?? row.started_at;
                  const date = monthDay(at);
                  const st = usableStat(row, run);
                  const v = st ? vOf(st) : null;
                  const cut = cutOf(row.state);
                  let state = "읽는 중";
                  if (run && cut) state = `${CUT_TEXT[cut]} · 판정 없음(0으로 세지 않음)`;
                  else if (run?.stat === "none") state = "결과 없음(0으로 세지 않음)";
                  else if (run?.stat === "error") state = "결과 못 읽음(0으로 세지 않음)";
                  const partial = v && cut ? `${CUT_TEXT[cut]}(일부 결과) · ` : "";
                  const label = `${date ?? "날짜 없음"} ${g.host} — ${v ? `${partial}${vText(v)}` : state}`;
                  return (
                    <li key={row.id}>
                      <button type="button" className={s["ov-col"]} aria-label={label} onClick={() => onOpen(row)}>
                        {v && vTotal(v) > 0
                          ? VERDICTS.filter(([k]) => v[k] > 0).map(([k]) => (
                              <span key={k} className={cx(s["ov-seg"], s[`ov-v${k}`])} style={{ height: pct(v[k]) }} />
                            ))
                          : null}
                        {v && vTotal(v) === 0 ? <span className={s["ov-zero"]}>0</span> : null}
                        {v && cut ? <span className={cx(s["ov-cutmark"], s[cut])}>{CUT_MARK[cut]}</span> : null}
                        {!v && run && cut ? <span className={cx(s["ov-cutstub"], s[cut])}>{CUT_MARK[cut]}</span> : null}
                        {v || (run && cut) ? null : <span className={s["ov-nil"]} />}
                        <span className={cx(s["ov-tip"], i >= all.length / 2 && s.left)} aria-hidden="true">
                          <b>{g.host}</b>
                          <span>
                            {[monthDayTime(at), run?.stat === undefined ? null : urlStateText(row.state)]
                              .filter(Boolean)
                              .join(" · ")}
                          </span>
                          {v && cut ? (
                            <span className={cut === "failed" ? s["ov-tipbad"] : undefined}>
                              {`${CUT_TEXT[cut]} · 남은 결과만`}
                            </span>
                          ) : null}
                          {v ? (
                            VERDICTS.map(([k, l]) => (
                              <span key={k} className={s["ov-tipr"]}>
                                <span className={cx(s["ov-key"], s[`ov-v${k}`])} />
                                <b>{v[k]}</b> {l}
                              </span>
                            ))
                          ) : (
                            <span className={cut === "failed" ? s["ov-tipbad"] : undefined}>{state}</span>
                          )}
                        </span>
                      </button>
                      <span className={s["ov-x"]} aria-hidden="true">
                        {date ?? "—"}
                      </span>
                    </li>
                  );
                })}
              </ol>
              <span className={s["ov-gx"]} title={g.host} aria-hidden="true">
                {hostParts(g.host).map(({ at, part }) => (
                  <span key={at}>{part}</span>
                ))}
              </span>
            </li>
          ))}
        </ol>
      </div>
    </div>
  );
}

/** 큰 나눔 막대(28px): 한 전체를 갈래별 덩어리로. 넓은 덩어리만 안에 수를 적고 범례가 이름 · 수를 맡는다 */
function SplitBar({
  parts,
  label,
}: {
  parts: { cls: string; n: number; name: string; ink?: "dark" }[];
  label: string;
}) {
  const total = parts.reduce((n, p) => n + p.n, 0);
  return (
    <div className={s["ov-split"]} role="img" aria-label={label}>
      {total ? (
        parts
          .filter((p) => p.n > 0)
          .map((p) => (
            <span
              key={p.cls}
              className={cx(s["ov-blk"], s[p.cls], p.ink === "dark" && s.dk)}
              style={{ flexGrow: p.n }}
              title={`${p.name} ${p.n}`}
            >
              {p.n / total >= INLINE_LABEL_SHARE ? p.n : null}
            </span>
          ))
      ) : (
        <span className={s["ov-blank"]}>0</span>
      )}
    </div>
  );
}

/**
 * 얇은 나눔 막대(8px, 글 없음): 표 · 목록 줄 안에서 옆 수와 함께. max 를 주면 그 목록의 가장 큰 합을 꽉 찬 길이로 삼아
 * 줄끼리 크기를 견줄 수 있다(같은 눈금) — 안 주면 줄마다 꽉 찬 길이(구성만).
 */
export function ThinBar({ parts, max }: { parts: { cls: string; n: number }[]; max?: number }) {
  const total = parts.reduce((n, p) => n + p.n, 0);
  const share = max && max > 0 ? Math.min(1, total / max) : 1;
  return (
    <span className={s["ov-thin"]} aria-hidden="true">
      {total ? (
        <span className={s["ov-fill"]} style={{ width: `${share * 100}%` }}>
          {parts
            .filter((p) => p.n > 0)
            .map((p) => (
              <span key={p.cls} className={s[p.cls]} style={{ flexGrow: p.n }} />
            ))}
        </span>
      ) : null}
    </span>
  );
}

const SEV_BAR: Record<SevF, string> = {
  crit: "ov-scrit",
  hi: "ov-shi",
  mid: "ov-smid",
  lo: "ov-slo",
  info: "ov-sinfo",
  none: "ov-snone",
};

function SevTag({ k }: { k: SevF }) {
  return <span className={cx(s.sev, k === "none" ? s["sev-none"] : s[k])}>{sevText(k)}</span>;
}

/**
 * 전송 통제 갈래. '막음'은 점검 하나 대시보드 · 진행 요약과 같은 말이고(정책으로 막음 + 미지원 경로 막음), 줄의
 * '허용 n · 막음 n'의 막음은 그 둘의 합이다 — 그래서 둘을 같은 색(채움 · 빗금)으로 그린다.
 */
export const E2_PARTS: readonly [keyof E2Sum, string, string][] = [
  ["allowed", "허용(응답 받음)", "ov-ea"],
  ["denied", "정책으로 막음", "ov-ed"],
  ["unsupported", "미지원 경로 막음", "ov-eu"],
  ["unsure", "결과 불명 · 실패", "ov-ex"],
];
const e2Blocked = (e2: E2Sum) => e2.denied + e2.unsupported;
const e2Sum = (e2: E2Sum) => E2_PARTS.reduce((n, [k]) => n + e2[k], 0);

const STAGES: readonly [string, string][] = [
  ["crawl", "자산 탐색"],
  ["scope", "범위 확인"],
  ["live", "점검 진행"],
  ["report", "보고서"],
];
function stageAt(info: ActiveInfo | undefined): number {
  const st = info?.flow?.stage;
  if (st === "crawling") return 0;
  if (st === "awaiting_selection") return 1;
  return 2;
}

/** 대상 하나: 그 대상의 끝난 점검(새것부터) · 결과가 있는 마지막 점검 */
export type TargetGroup = {
  key: string;
  host: string;
  rows: URLRunSummary[];
  latest: URLRunSummary | null;
  stat: EndedStat | null;
};

/**
 * 대상별 묶음(대상마다 결과가 있는 마지막 점검). 주소를 모르는 점검(상태를 못 읽음)은 어느 대상인지 알 수 없어 묶음에 넣지
 * 않고 unknownTargets 로 따로 돌려준다 — 점검 하나를 별도 '대상'으로 세면 같은 대상의 예전 결과가 한 번 더 합쳐진다
 * (#978 이경준 P2-4). 아직 읽지 않은 점검은 어디에도 넣지 않는다. rows 는 최신 순이다.
 */
export function targetGroupsOf(
  rows: readonly URLRunSummary[],
  runOf: (r: URLRunSummary) => EndedRun | undefined,
): { groups: TargetGroup[]; unknownTargets: URLRunSummary[] } {
  const map = new Map<string, TargetGroup>();
  const unknownTargets: URLRunSummary[] = [];
  for (const r of rows) {
    const run = runOf(r);
    if (!run) continue;
    if (run.host === null) {
      unknownTargets.push(r);
      continue;
    }
    const g = map.get(run.host) ?? { key: run.host, host: run.host, rows: [], latest: null, stat: null };
    g.rows.push(r);
    const st = usableStat(r, run);
    if (!g.stat && st) {
      g.latest = r;
      g.stat = st;
    }
    map.set(run.host, g);
  }
  return { groups: [...map.values()], unknownTargets };
}

/** 합계에 든 대상 중 제품 판정을 못 읽은 곳이 있으면 그 합계가 부분 결과라는 안내(없으면 null) */
export function partialNoteOf(groups: readonly TargetGroup[]): string | null {
  const n = groups.filter((g) => g.stat?.pv === "failed").length;
  return n ? `제품 판정을 못 읽은 대상 ${n}곳 — 합계는 부분 결과` : null;
}

/**
 * 끝난 점검의 전송 기록 집계. 분모(total)는 끝난 점검 수다. verified = 검증된 기록, none = 저널 없음(진단 요청 없음 — 검증으로
 * 세지 않음, #978 머지 뒤 이경준 리뷰 P2), invalid = 검증 실패, unread = 조회 실패
 * (결과를 통째로 못 읽은 점검 포함), absent = 기록이 없음(결과 없이 끝남 포함). loaded = 하나라도 읽었는지(#978 이경준 P2-3)
 */
export type E2Tally = {
  total: number;
  verified: { row: URLRunSummary; e2: E2Sum }[];
  none: number;
  invalid: number;
  unread: number;
  absent: number;
  loaded: boolean;
};
export function e2TallyOf(rows: readonly URLRunSummary[], runOf: (r: URLRunSummary) => EndedRun | undefined): E2Tally {
  const tally: E2Tally = { total: rows.length, verified: [], none: 0, invalid: 0, unread: 0, absent: 0, loaded: false };
  for (const row of rows) {
    const run = runOf(row);
    if (!run) continue;
    tally.loaded = true;
    let e2: E2Read = run.stat === "error" ? "failed" : "absent";
    if (isStat(run.stat)) e2 = run.stat.e2;
    if (isE2Sum(e2)) tally.verified.push({ row, e2 });
    else if (e2 === "none") tally.none += 1;
    else if (e2 === "invalid") tally.invalid += 1;
    else if (e2 === "failed") tally.unread += 1;
    else tally.absent += 1;
  }
  return tally;
}
/**
 * 운영 상태의 전송 기록 칩. 끝난 점검을 모두 읽고 모두 검증됐을 때만 초록이다. 저널 없음(진단 요청 없음)은 검증으로 세지 않는다 —
 * 취득 기록이 생기기 전 판의 회차는 지워진 저널과 가르지 못한다(계약 workbench-live-data §4 한계)
 */
export function e2ChipOf(tally: E2Tally): { tone: "ok" | "warn" | "unk"; text: string } {
  if (!tally.loaded || !tally.total) return { tone: "unk", text: "전송 기록 읽기 전" };
  const base = tally.invalid
    ? `전송 기록 검증 실패 ${tally.invalid} / ${tally.total}`
    : `전송 기록 검증 ${tally.verified.length} / ${tally.total}`;
  const text = [
    base,
    tally.none ? `진단 요청 없음 ${tally.none}` : null,
    tally.unread ? `못 읽음 ${tally.unread}` : null,
    tally.absent ? `기록 없음 ${tally.absent}` : null,
  ]
    .filter(Boolean)
    .join(" · ");
  if (tally.invalid) return { tone: "warn", text };
  return { tone: tally.verified.length === tally.total ? "ok" : "unk", text };
}

/** 전송 통제 카드 머리: 끝난 점검 수 중 검증된 수, 진단 요청 없음 · 검증 실패 · 못 읽음 수 */
export function e2CardSubOf(tally: E2Tally): string {
  return [
    `진단 요청 · 끝난 점검 ${tally.total}건 중 ${tally.verified.length}건`,
    tally.none ? `진단 요청 없음 ${tally.none}건` : null,
    tally.invalid ? `검증 실패 ${tally.invalid}건 뺌` : null,
    tally.unread ? `못 읽음 ${tally.unread}건 뺌` : null,
  ]
    .filter(Boolean)
    .join(" · ");
}

/**
 * 진행 중 점검의 승인 대기. unread = 승인 대기를 확인하지 못한 점검 수(상태 조회 실패 · 사용자 승인 모드인데 승인 대기 조회
 * 실패). 못 읽은 점검이 있으면 대기 0 을 '할 일 없음'으로 단정하지 않는다(#978 이경준 P2-2).
 */
export type PendingTally = { pending: { row: URLRunSummary; a: RequestApproval }[]; unread: number };
export function pendingTallyOf(infos: readonly { row: URLRunSummary; info: ActiveInfo | undefined }[]): PendingTally {
  return {
    pending: infos.flatMap(({ row, info }) => (info?.pending ?? []).map((a) => ({ row, a }))),
    unread: infos.filter(({ info }) => !!info && (info.jobFailed || info.pendingFailed)).length,
  };
}
/** '승인 대기' 칸의 수와 덧글. 못 읽은 점검이 있고 읽은 대기가 0 이면 수를 적지 않는다(0 으로 단정하지 않음) */
export function pendingTileOf(tally: PendingTally, loaded: boolean): { n: number | null; note: string | null } {
  if (!loaded) return { n: null, note: null };
  if (!tally.unread) return { n: tally.pending.length, note: null };
  return tally.pending.length
    ? { n: tally.pending.length, note: `못 읽은 점검 ${tally.unread}건` }
    : { n: null, note: `못 읽은 점검 ${tally.unread}건 · 승인 대기가 있을 수 있음` };
}

/** '범위 확인 대기'를 확인하지 못한 진행 중 점검 수(상태나 탐색 흐름을 못 읽음, #978 머지 뒤 이경준 리뷰 P3) */
export function scopeUnreadOf(infos: readonly { info: ActiveInfo | undefined }[]): number {
  return infos.filter(({ info }) => info?.jobFailed || info?.flowFailed).length;
}
/** '범위 확인 대기' 칸 덧글. 못 읽은 수는 상태 · 탐색 흐름 둘 다 센다(scopeUnreadOf, #989 이동찬 P3) */
export function scopeSubOf(unread: number): string {
  const base = "탐색이 끝나 점검할 경로를 골라야 함";
  return unread ? `${base} · 상태 · 탐색 흐름을 못 읽은 점검 ${unread}건` : base;
}

/** 통합 대시보드. tabs = 머리 제목 옆 보기 전환(전체 · 점검별) */
export function DashOverview({ tabs }: { tabs?: ReactNode }) {
  const { route } = useShell();
  const visible = route === "dashboard";
  const open = useOpenRun();
  const page = useUrlRuns((st) => st.page);
  const owner = useUrlRuns((st) => st.owner);
  const listReady = useUrlRuns((st) => !!st.page && !st.error);
  const hidden = useHiddenRunIDs(owner);
  const cap = useUrl((st) => st.cap);
  const rows = page?.runs ?? [];
  const actRows = rows.filter(activeURLRun);
  const doneRows = rows.filter((r) => endedURLRun(r) && !hidden.includes(r.id)).sort(newestFirst);
  const ended = useEndedRuns(doneRows, owner, visible);
  const active = useActiveInfos(actRows, owner, visible);
  const health = usePoll((signal) => readHealth(signal), [], { enabled: visible });
  const now = Date.now();
  const runOf = (r: URLRunSummary) => ended.data?.get(r.id);
  const loading = doneRows.length > 0 && !ended.data;

  // ── 대상별 묶음(대상마다 결과가 있는 마지막 점검). 주소를 모르는 점검은 합계에서 빼고 따로 센다 ──
  const { groups, unknownTargets } = targetGroupsOf(doneRows, runOf);
  const partialNote = partialNoteOf(groups);
  const latestStats = groups.map((g) => g.stat).filter((x): x is EndedStat => !!x);
  const sum = (f: (x: EndedStat) => number) => latestStats.reduce((n, x) => n + f(x), 0);
  const now4: Record<VKey, number> = {
    c: sum((x) => x.confirmed),
    r: sum((x) => x.aiReview + x.suspected),
    u: sum((x) => x.undecided),
    a: sum((x) => x.allowed),
  };
  const aiReview = sum((x) => x.aiReview);
  const suspected = sum((x) => x.suspected);
  const sevSum = (k: SevF) => sum((x) => x.sev[k]);
  const failedReads = doneRows.filter((r) => runOf(r)?.stat === "error").length;
  const noResult = doneRows.filter((r) => runOf(r)?.stat === "none" && !cutOf(r.state)).length;
  /** 최근 끝난 점검 중 진단 실패 */
  const failedRuns = doneRows.filter((r) => r.state === "failed");
  /**
   * 대상마다 마지막 점검이 끝까지 못 간 곳(진단 실패 · 중지). counted = 남은 결과가 있어 그 일부를 센 곳, 아니면 결과가 없어
   * 판정에서 빠진 곳(그 대상은 앞선 점검 결과를 쓰거나 판정이 없다)
   */
  const lastCuts = groups.flatMap((g) => {
    const last = g.rows[0];
    const cut = last ? cutOf(last.state) : null;
    return last && cut ? [{ cut, counted: !!usableStat(last, runOf(last)) }] : [];
  });
  const cutOut = (c: Cut) => lastCuts.filter((x) => x.cut === c && !x.counted).length;
  const cutPartial = lastCuts.filter((x) => x.counted).length;
  const cutOutText = (["failed", "stopped"] as const)
    .filter((c) => cutOut(c) > 0)
    .map((c) => `${CUT_TEXT[c]} ${cutOut(c)}곳`)
    .join(" · ");
  const noVerdict = !loading && latestStats.length === 0;

  // ── 전송 통제(최근 끝난 점검 전부 — 판정이 아니라 보낸 요청이라 겹쳐 세지 않음) ──
  const e2Tally = e2TallyOf(doneRows, runOf);
  const e2Ok = e2Tally.verified;
  const e2Total = (k: keyof E2Sum) => e2Ok.reduce((n, x) => n + x.e2[k], 0);
  const e2All = E2_PARTS.reduce((n, [k]) => n + e2Total(k), 0);

  // ── 확정 목록(대상마다 마지막 점검) ──
  const confirmedList = groups
    .flatMap((g) => (g.stat && g.latest ? g.stat.items.map((it) => ({ it, g, row: g.latest as URLRunSummary })) : []))
    .sort((a, b) => SEV_RANK.indexOf(a.it.sev) - SEV_RANK.indexOf(b.it.sev) || (a.it.at < b.it.at ? 1 : -1));

  // ── 진행 중 ──
  const infos = actRows.map((r) => ({ row: r, info: active.data?.get(r.id) }));
  const pendingTally = pendingTallyOf(infos);
  const pendingAll = pendingTally.pending;
  const pendingTile = pendingTileOf(pendingTally, !!active.data || !actRows.length);
  const oldest = pendingAll.reduce<{ row: URLRunSummary; a: RequestApproval } | null>(
    (m, x) => (!m || Date.parse(x.a.created_at) < Date.parse(m.a.created_at) ? x : m),
    null,
  );
  const soonest = pendingAll.reduce<RequestApproval | null>(
    (m, x) => (!m || Date.parse(x.a.expires_at) < Date.parse(m.expires_at) ? x.a : m),
    null,
  );
  /** '승인 대기' 칸 덧글: 가장 오래된 대기 · 만료, 못 읽은 점검이 있으면 그 수 */
  let pendingLead: string | null = "보낼 요청 승인 · 만료되면 보내지 않음";
  if (oldest && soonest)
    pendingLead = `가장 오래 ${minutesAgo(oldest.a.created_at, now)}분 · ${Math.max(0, minutesLeft(soonest.expires_at, now))}분 뒤 만료`;
  else if (pendingTile.note) pendingLead = null;
  const pendingSub = [pendingLead, pendingTile.note].filter(Boolean).join(" · ");
  const selecting = infos.filter(({ info }) => info?.flow?.stage === "awaiting_selection");
  /** 상태나 탐색 흐름을 못 읽어 단계 · 승인 만료를 확인하지 못한 진행 중 점검 수 */
  const statusUnread = scopeUnreadOf(infos);
  const approvalIssue = rows.filter(
    (r) =>
      r.action_required &&
      ["revoked", "changed", "unavailable"].includes(r.approval_status) &&
      !(endedURLRun(r) && hidden.includes(r.id)),
  );
  const expirySoon = infos.filter(({ info }) => {
    const at = info?.job?.scope?.expires_at;
    return !!at && Date.parse(at) - now < EXPIRY_SOON_MS;
  });
  const models = cap.kind === "v2" ? cap.catalog.profiles : [];
  const externalModels = models.filter((p) => p.model?.external_transfer).length;
  const localModels = models.length - externalModels;
  const modelText = [localModels ? `로컬 ${localModels}` : null, externalModels ? `외부 API ${externalModels}` : null]
    .filter(Boolean)
    .join(" · ");

  // ── 머리: 집계 기준 ──
  const scopeText = listReady
    ? `최근 점검 ${rows.length}건 기준${page?.more ? " · 더 있음(그 뒤는 세지 않음)" : ""}`
    : "점검 목록을 읽는 중";

  const e2ChipView = e2ChipOf(e2Tally);
  const e2Chip: ReactNode = <Chip tone={e2ChipView.tone}>{e2ChipView.text}</Chip>;
  let healthChip: ReactNode = null;
  if (health.data === "ready") healthChip = <Chip tone="ok">준비 서비스 준비됨</Chip>;
  else if (health.data === "not_ready") healthChip = <Chip tone="warn">준비 서비스 준비 안 됨</Chip>;
  else if (health.data === "error") healthChip = <Chip tone="unk">준비 서비스 확인 못 함</Chip>;

  const trendCols: TrendCol[] = doneRows
    .slice(0, TREND_RUNS)
    .reverse()
    .map((row) => ({ row, run: runOf(row) }));
  const trendGroups = groupTrend(trendCols);
  const confirmedSevs = SEV_RANK.filter((k) => sevSum(k) > 0);
  const sevMax = Math.max(1, ...confirmedSevs.map(sevSum));
  const vParts = (v: Record<VKey, number>) =>
    VERDICTS.map(([k, name]) => ({ cls: `ov-v${k}`, n: v[k], name, ink: k === "a" ? ("dark" as const) : undefined }));
  // 목록마다 줄끼리 크기를 견줄 수 있게 그 목록의 가장 큰 합을 꽉 찬 길이로(같은 눈금)
  const statTotal = (st: EndedStat | null | undefined) => (st ? vTotal(vOf(st)) : 0);
  const tgtShown = groups
    .slice()
    .sort(
      (a, b) =>
        (b.stat?.confirmed ?? -1) - (a.stat?.confirmed ?? -1) ||
        (b.stat ? vOf(b.stat).r : -1) - (a.stat ? vOf(a.stat).r : -1),
    )
    .slice(0, LIST_ROWS);
  const tgtMax = Math.max(0, ...tgtShown.map((g) => statTotal(g.stat)));
  const e2Shown = e2Ok.slice(0, LIST_ROWS - 1);
  const e2Max = Math.max(0, ...e2Shown.map((x) => e2Sum(x.e2)));
  const doneShown = doneRows.slice(0, OVERVIEW_ROWS);
  const doneMax = Math.max(0, ...doneShown.map((r) => statTotal(usableStat(r, runOf(r)))));
  const notReadText = [
    noResult ? `결과 없이 끝남 ${noResult}건` : null,
    failedReads ? `결과 못 읽음 ${failedReads}건` : null,
    // 주소를 못 읽은 점검은 어느 대상인지 몰라 합계에 넣지 않는다(#978 이경준 P2-4)
    unknownTargets.length ? `대상 확인 못 함 ${unknownTargets.length}건(합계에서 뺌)` : null,
  ]
    .filter(Boolean)
    .join(" · ");

  return (
    <>
      <UrlHead title="대시보드" crumb="대시보드" pill={tabs} />
      <div className={cx(s.vb, s.solo)}>
        <div className={cx(s.vm, s["ov-page"])}>
          {/* ① 운영 상태: 폐쇄망 설치는 대신 지켜보는 공급사가 없어 통제 상태를 첫 화면에 */}
          <div className={s["ov-top"]}>
            <p className={s["ov-scope"]}>{scopeText}</p>
            <section className={s["ov-status"]} aria-label="운영 상태">
              {healthChip}
              {models.length ? (
                <Chip
                  tone="ok"
                  title="이 설치에 등록된 모델 수예요. 외부 API 모델로 점검하면 대상 데이터와 프롬프트가 그 제공자로 나가요."
                >
                  {`설치된 모델 ${modelText}`}
                </Chip>
              ) : null}
              {failedRuns.length ? <Chip tone="bad">{`진단 실패 ${failedRuns.length}건`}</Chip> : null}
              {e2Chip}
              {approvalIssue.length || expirySoon.length ? (
                <Chip tone="warn">
                  {[
                    approvalIssue.length ? `대상 승인 문제 ${approvalIssue.length}` : null,
                    expirySoon.length ? `승인 만료 임박 ${expirySoon.length}` : null,
                  ]
                    .filter(Boolean)
                    .join(" · ")}
                </Chip>
              ) : (
                <Chip tone="ok">대상 승인 문제 없음</Chip>
              )}
            </section>
          </div>
          <RunListStatus />

          {/* ② 지금 할 일: 승인 · 범위 고르기 · 검토 · 대상 승인 확인 */}
          <section className={s["ov-todo"]} aria-label="지금 할 일">
            <TodoTile
              k="승인 대기"
              n={pendingTile.n}
              sub={pendingSub}
              onOpen={oldest ? () => open(oldest.row, "live") : undefined}
            />
            <TodoTile
              k="범위 확인 대기"
              n={(active.data || !actRows.length) && (selecting.length > 0 || !statusUnread) ? selecting.length : null}
              sub={scopeSubOf(statusUnread)}
              onOpen={selecting[0] ? () => open(selecting[0].row, "scope") : undefined}
            />
            <TodoTile
              k="검토 필요"
              n={loading ? null : now4.r}
              sub={`AI 후보 ${aiReview} · 확정 단계 의심 ${suspected}`}
              tip={
                <HelpTip label="검토 필요 설명">
                  취약점 수가 아니에요. AI 가 낸 후보와 확정 단계가 의심으로 남긴 것으로, 사람이 보고서에서 확인해야
                  해요. 대상마다 마지막 점검 기준이에요.
                </HelpTip>
              }
            />
            <TodoTile
              k="대상 승인 확인"
              n={listReady ? approvalIssue.length : null}
              sub="승인 철회 · 변경 · 확인 불가"
            />
          </section>

          <div className={s["ov-grid"]}>
            {/* ③-1 대상별 판정 추이: 같은 대상끼리 묶어 시간 순(대상이 섞인 한 줄 시간 축은 추이로 읽히지 않는다) */}
            <Card
              id="ovTrendH"
              title="대상별 판정 추이"
              sub={`끝난 점검 ${Math.min(doneRows.length, TREND_RUNS)}건 · 같은 대상끼리 시간 순 · 막대를 누르면 그 보고서`}
              side={
                <VerdictLegend
                  fail={trendCols.some(({ row }) => cutOf(row.state) === "failed")}
                  stop={trendCols.some(({ row }) => cutOf(row.state) === "stopped")}
                />
              }
              wide="w7"
            >
              {loading ? <p className={s.empty}>결과를 읽는 중</p> : null}
              {!loading && trendCols.length ? (
                <TrendChart
                  groups={trendGroups}
                  onOpen={(row) => open(row, row.report_available ? "report" : "live")}
                />
              ) : null}
              {!loading && !trendCols.length ? <RunListStatus empty="끝난 점검이 없어요." /> : null}
            </Card>

            {/* ③-2 지금 판정 구성: 대상마다 마지막 점검 */}
            <Card
              id="ovNowH"
              title="지금 판정"
              sub={`대상 ${groups.length}곳 · 각 대상의 마지막 결과${notReadText ? ` · ${notReadText}` : ""}`}
              wide="w5"
            >
              {!loading && cutOutText ? (
                <p className={cx(s["ov-alert"], !cutOut("failed") && s.quiet)}>
                  <span aria-hidden="true">{CUT_MARK[cutOut("failed") ? "failed" : "stopped"]}</span>
                  {`마지막 점검 ${cutOutText} · 판정에서 뺌`}
                </p>
              ) : null}
              {!loading && cutPartial ? (
                <p className={s["ov-note"]}>{`마지막 점검이 끝까지 못 간 ${cutPartial}곳은 남은 결과만 셌어요`}</p>
              ) : null}
              {!loading && partialNote ? <p className={s["ov-note"]}>{partialNote}</p> : null}
              <div className={s["ov-hero"]}>
                <span className={s["ov-hn"]}>{loading || noVerdict ? "—" : now4.c}</span>
                <span className={s["ov-hl"]}>{noVerdict ? "판정할 결과 없음" : "확정 취약점"}</span>
              </div>
              {noVerdict ? null : (
                <>
                  <SplitBar parts={vParts(now4)} label={vText(now4)} />
                  <VerdictLegend v={now4} />
                  <div className={s["ov-sub"]}>
                    <h3>확정 위험도</h3>
                    {now4.c ? (
                      <ul className={s["ov-meters"]}>
                        {confirmedSevs.map((k) => (
                          <li key={k}>
                            <span>{sevText(k)}</span>
                            <span className={s["ov-track"]}>
                              <span className={s[SEV_BAR[k]]} style={{ width: `${(sevSum(k) / sevMax) * 100}%` }} />
                            </span>
                            <b>{sevSum(k)}</b>
                          </li>
                        ))}
                      </ul>
                    ) : (
                      <p className={s["ov-note"]}>
                        {`확정 0 · 판단 불가 ${now4.u} — 확정 0은 취약점이 없다는 뜻이 아니에요`}
                      </p>
                    )}
                  </div>
                </>
              )}
            </Card>

            {/* ⑥ 대상별: 목록 줄의 주소로 묶음(서버 대상 칸이 생기면 그것으로) */}
            <Card
              id="ovTgtH"
              title="대상별"
              sub={`대상 ${groups.length}곳 · 마지막 결과 기준`}
              side={<VerdictLegend />}
              wide="w7"
              // 옆(전송 통제)은 점검이 하나라도 있으면 늘 길다 — 그때만 줄인다(둘 다 짧으면 높이를 맞춘다)
              fit={tgtShown.length <= SHORT_LIST && e2Ok.length > 0}
            >
              {groups.length ? (
                <ul className={s["ov-tgts"]}>
                  {tgtShown.map((g) => {
                    const v = g.stat ? vOf(g.stat) : null;
                    const top = g.stat ? SEV_RANK.find((k) => (g.stat as EndedStat).sev[k] > 0) : undefined;
                    const last = g.rows[0];
                    const lastCut = last ? cutOf(last.state) : null;
                    const lastBad = !lastCut && last && !isStat(runOf(last)?.stat);
                    // 마지막 점검이 끝까지 못 갔을 때 이 줄의 판정이 어디서 왔는지
                    let basis: string | null = null;
                    if (lastCut && g.stat && g.latest === last) basis = `${CUT_TEXT[lastCut]} · 남은 결과만 셈`;
                    else if (lastCut && g.stat) basis = `판정은 ${monthDay(g.latest?.finished_at) ?? "이전"} 점검 기준`;
                    const none = targetNoneText(
                      g.rows.map((r) => cutOf(r.state)),
                      lastCut,
                    );
                    return (
                      <li key={g.key}>
                        <button
                          type="button"
                          className={s["ov-tgt"]}
                          onClick={() => open(g.latest ?? last, g.latest ? "report" : "live")}
                        >
                          <span className={s["ov-tn"]}>
                            <b>
                              {g.host}
                              {lastCut ? (
                                <span className={cx(s.pill, lastCut === "failed" && s.fail)}>
                                  {lastCut === "failed" ? "진단 실패" : "중지됨"}
                                </span>
                              ) : null}
                            </b>
                            <span className={s["ov-mut"]}>
                              {[
                                `점검 ${g.rows.length}회`,
                                monthDay(last?.finished_at ?? last?.started_at)
                                  ? `마지막 ${monthDay(last?.finished_at ?? last?.started_at)}`
                                  : null,
                                lastBad ? "마지막 점검 결과 없음" : null,
                                basis,
                              ]
                                .filter(Boolean)
                                .join(" · ")}
                            </span>
                          </span>
                          {v ? (
                            <>
                              <ThinBar parts={vParts(v)} max={tgtMax} />
                              <span className={s["ov-tf"]}>
                                <b>{`확정 ${v.c}`}</b>
                                <span>{`검토 ${v.r}`}</span>
                                <span>{`불가 ${v.u}`}</span>
                              </span>
                              <span className={s["ov-ttag"]}>{top ? <SevTag k={top} /> : null}</span>
                            </>
                          ) : (
                            <span className={s["ov-tnone"]}>{none}</span>
                          )}
                        </button>
                      </li>
                    );
                  })}
                </ul>
              ) : (
                <RunListStatus empty="끝난 점검이 없어요." />
              )}
            </Card>

            {/* 전송 통제: 진단 요청의 허용 · 막음(E²) — 우리만 보이는 통제 지표 */}
            <Card id="ovE2H" title="전송 통제" sub={e2CardSubOf(e2Tally)} wide="w5">
              <div className={s["ov-hero"]}>
                <span className={s["ov-hn"]}>{e2Ok.length ? e2All : "—"}</span>
                <span className={s["ov-hl"]}>진단 요청</span>
              </div>
              <SplitBar
                parts={E2_PARTS.map(([k, name, cls]) => ({
                  cls,
                  n: e2Total(k),
                  name,
                  ink: k === "unsure" ? "dark" : undefined,
                }))}
                label={E2_PARTS.map(([k, name]) => `${name} ${e2Total(k)}`).join(" · ")}
              />
              <ul className={s["ov-legend"]}>
                {E2_PARTS.map(([k, name, cls]) => (
                  <li key={k}>
                    <span className={cx(s["ov-sw"], s[cls])} aria-hidden="true" />
                    {name}
                    <b>{e2Total(k)}</b>
                  </li>
                ))}
              </ul>
              {e2Ok.length ? (
                <div className={s["ov-sub"]}>
                  <h3>점검별</h3>
                  <ul className={s["ov-e2runs"]}>
                    {e2Shown.map(({ row, e2 }) => (
                      <li key={row.id}>
                        <span className={s["ov-mut"]}>{monthDay(row.finished_at) ?? "—"}</span>
                        <span className={s["ov-en"]}>{runOf(row)?.host ?? (row.display_name?.trim() || row.id)}</span>
                        <ThinBar parts={E2_PARTS.map(([k, , cls]) => ({ cls, n: e2[k] }))} max={e2Max} />
                        <span className={s["ov-ef"]}>{`허용 ${e2.allowed} · 막음 ${e2Blocked(e2)}`}</span>
                      </li>
                    ))}
                  </ul>
                </div>
              ) : null}
            </Card>

            {/* 확정 취약점: 대상마다 마지막 점검에서 확정된 것, 위험도 순 */}
            <Card
              id="ovConfH"
              title="확정 취약점"
              sub="대상마다 마지막 점검 · 위험도 순"
              side={
                confirmedList.length > LIST_ROWS ? (
                  <span className={s["ov-mut"]}>{`${LIST_ROWS} / ${confirmedList.length}`}</span>
                ) : null
              }
              wide="w7"
              // 옆 카드(진행 중)가 길 때만 줄인다 — 둘 다 짧으면 높이를 맞춰 아래 줄이 어긋나지 않게
              fit={confirmedList.length <= SHORT_LIST && actRows.length > SHORT_LIST}
            >
              {confirmedList.length ? (
                <ul className={s["ov-finds"]}>
                  {confirmedList.slice(0, LIST_ROWS).map(({ it, g, row }) => (
                    <li key={`${g.key}:${it.key}`}>
                      <button type="button" className={s["ov-find"]} onClick={() => open(row, "report")}>
                        <SevTag k={it.sev} />
                        <span className={s["ov-ft"]}>
                          <span>{it.title}</span>
                          {it.where ? (
                            <span className={s["ov-fp"]}>
                              {[it.where.m, it.where.p, it.where.more ? `외 ${it.where.more}곳` : null]
                                .filter(Boolean)
                                .join(" ")}
                            </span>
                          ) : null}
                        </span>
                        <span className={s["ov-mut"]}>{g.host}</span>
                        <span className={s["ov-mut"]}>{monthDay(it.at) ?? "—"}</span>
                      </button>
                    </li>
                  ))}
                </ul>
              ) : (
                <p className={s["ov-note"]}>
                  {loading ? "결과를 읽는 중" : null}
                  {!loading && noVerdict
                    ? `판정할 결과가 없어요${failedRuns.length ? ` · 진단 실패 ${failedRuns.length}건` : ""}`
                    : null}
                  {!loading && !noVerdict
                    ? `확정 0건 · 검토 필요 ${now4.r}건 — 확정 0은 취약점이 없다는 뜻이 아니에요`
                    : null}
                </p>
              )}
            </Card>

            {/* ④ 진행 중: 단계 줄 · 경과 · 승인 대기 · 전송 통제 */}
            <Card
              id="ovActH"
              title="진행 중"
              sub={listReady ? `${actRows.length}건` : "—"}
              side={
                <a className={s.aside} href="#running">
                  전체 보기
                </a>
              }
              wide="w5"
              fit={actRows.length <= SHORT_LIST && confirmedList.length > SHORT_LIST}
            >
              {actRows.length ? (
                <div className={s["ov-acts"]}>
                  {infos.slice(0, OVERVIEW_ROWS).map(({ row, info }) => {
                    const at = stageAt(info);
                    const el = runElapsedSec(row, now);
                    const e2 = info?.e2;
                    return (
                      <button
                        key={row.id}
                        className={s["ov-act"]}
                        type="button"
                        onClick={() => open(row, at === 1 ? "scope" : "live")}
                      >
                        <span className={s["ov-actt"]}>
                          <RunName row={row} />
                          {info?.pending?.length ? (
                            <span className={cx(s.pill, s.wait)}>{`승인 대기 ${info.pending.length}`}</span>
                          ) : null}
                          {row.action_required && !info?.pending?.length ? (
                            <span className={cx(s.pill, s.wait)}>확인 필요</span>
                          ) : null}
                        </span>
                        <span className={s["ov-steps"]}>
                          {STAGES.map(([k, l], i) => (
                            <span
                              key={k}
                              className={cx(i < at && s.done, i === at && s.now)}
                              aria-current={i === at ? "step" : undefined}
                            >
                              {i < at ? "✓ " : ""}
                              {l}
                            </span>
                          ))}
                        </span>
                        <span className={s["ov-mut"]}>
                          {[el !== null ? `경과 ${fmtDur(el)}` : null, e2 ? activeE2Text(e2) : null]
                            .filter(Boolean)
                            .join(" · ") || "—"}
                        </span>
                      </button>
                    );
                  })}
                </div>
              ) : (
                <RunListStatus empty="진행 중인 점검이 없어요." />
              )}
            </Card>

            {/* ⑤ 끝난 점검 표: 확정 · 검토 필요 · 판단 불가를 각각 열로, 위험도 딱지는 확정에만 */}
            <Card
              id="ovDoneH"
              title="끝난 점검"
              sub={listReady ? `${doneRows.length}건 · 끝난 순` : "—"}
              side={
                <a className={s.aside} href="#finished">
                  전체 보기
                </a>
              }
              wide="w12"
            >
              {doneRows.length ? (
                <div className={s["ov-table"]}>
                  <div className={s["ov-tr"]} aria-hidden="true">
                    {[
                      "점검",
                      "끝남 · 소요",
                      "결과",
                      "판정",
                      "확정",
                      "검토 필요",
                      "판단 불가",
                      "확정 최고 위험도",
                      "전송 기록",
                    ].map((h) => (
                      <span key={h}>{h}</span>
                    ))}
                  </div>
                  {doneShown.map((row) => {
                    const raw = runOf(row)?.stat;
                    const cut = cutOf(row.state);
                    const failed = cut === "failed";
                    const st = isStat(raw) && !usableStat(row, runOf(row)) ? "cut-empty" : raw;
                    const dur = runDurationSec(row);
                    const reason = row.reason_code ? (urlReasonLabels[row.reason_code] ?? "서버 사유 있음") : null;
                    const top = st !== "cut-empty" && isStat(st) ? SEV_RANK.find((k) => st.sev[k] > 0) : undefined;
                    let cells: ReactNode;
                    if (st === "cut-empty")
                      cells = (
                        <span className={s["ov-span"]}>
                          {`판정 없음(${CUT_TEXT[cut ?? "failed"]} — 0으로 세지 않음)`}
                        </span>
                      );
                    else if (st === "none") cells = <span className={s["ov-span"]}>결과 없음</span>;
                    else if (st === "error")
                      cells = <span className={s["ov-span"]}>결과 못 읽음(0으로 세지 않음)</span>;
                    else if (!st) cells = <span className={s["ov-span"]}>읽는 중</span>;
                    else
                      cells = (
                        <>
                          <ThinBar parts={vParts(vOf(st))} max={doneMax} />
                          <b>{st.confirmed}</b>
                          <span>{st.aiReview + st.suspected}</span>
                          <span>{st.undecided}</span>
                          <span>{top ? <SevTag k={top} /> : "—"}</span>
                          <span>{journalText(st.e2)}</span>
                        </>
                      );
                    const partialRow = st !== "cut-empty" && isStat(st) && st.pv === "failed";
                    return (
                      <button
                        key={row.id}
                        className={cx(s["ov-tr"], s["ov-trow"])}
                        type="button"
                        onClick={() => open(row, row.report_available ? "report" : "live")}
                      >
                        <span>
                          <RunName row={row} />
                        </span>
                        <span className={s["ov-mut"]}>
                          {[
                            row.finished_at ? dateTimeOf(row.finished_at).slice(5, 16).replace("-", "/") : null,
                            dur !== null ? fmtDur(dur) : null,
                          ]
                            .filter(Boolean)
                            .join(" · ") || "—"}
                        </span>
                        {failed ? (
                          <span className={s["ov-res"]}>
                            <span className={cx(s.pill, s.fail)}>{urlStateText(row.state)}</span>
                            {reason ? <span className={s["ov-mut"]}>{reason}</span> : null}
                          </span>
                        ) : (
                          <span>
                            {[urlStateText(row.state), reason, partialRow ? "제품 판정 못 읽음(부분 결과)" : null]
                              .filter(Boolean)
                              .join(" · ")}
                          </span>
                        )}
                        {cells}
                      </button>
                    );
                  })}
                </div>
              ) : (
                <RunListStatus empty="끝난 점검이 없어요." />
              )}
            </Card>
          </div>
        </div>
      </div>
    </>
  );
}
