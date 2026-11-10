// live 대시보드 계산 (순수 함수). 시계는 인자로 받는다.
// - 30일 발견: 전체 발견 목록(fetchFindings, 최대 500)을 ts 로 거른다.
// - 토큰: 일별 합(입력+출력, 팀 대시보드와 같은 셈) · 점검당 평균은 같은 기간에 만든 점검 수로 나눈 추정값.

import type { SevKey } from "../data/findings";
import type { LiveFinding, LiveTask, TokenUsage } from "./api";
import { hostOf, isActive, SEVERITIES, type Severity, toMs, urlOf } from "./map";

/** 하루(ms). */
export const DAY_MS = 86_400_000;
/** 요약 기간(일). */
export const WINDOW_DAYS = 30;
/** 전체 발견 목록의 서버 상한 (server: 최신순 최대 500). 이만큼 오면 더 오래된 것은 빠졌을 수 있다. */
export const FINDINGS_CAP = 500;

/** 30일 발견 요약. */
export interface FindingSummary {
  /** 기간 안 전체 (모르는 심각도 포함) */
  total: number;
  bySev: Record<Severity, number>;
  /** critical·high·medium·low 가 아닌 심각도 */
  other: number;
  /** 목록이 서버 상한에 닿았다 (기간 안 일부가 빠졌을 수 있음) */
  capped: boolean;
}

/** 발견 목록 → 최근 days 일 심각도별 수. ts 를 못 읽는 발견은 세지 않는다. */
export function summarizeFindings(
  list: readonly Pick<LiveFinding, "ts" | "severity">[],
  nowMs: number,
  days = WINDOW_DAYS,
): FindingSummary {
  const since = nowMs - days * DAY_MS;
  const bySev: Record<Severity, number> = { critical: 0, high: 0, medium: 0, low: 0 };
  let total = 0;
  let other = 0;
  for (const f of list) {
    const t = toMs(f.ts);
    if (t === null || t < since) continue;
    total++;
    const k = f.severity.toLowerCase() as Severity;
    if ((SEVERITIES as readonly string[]).includes(k)) bySev[k]++;
    else other++;
  }
  return { total, bySev, other, capped: list.length >= FINDINGS_CAP };
}

/** 심각도 → 작업대 색 키 · 이름 (요약 막대·딱지 순서). */
export const SEV_ROWS: ReadonlyArray<readonly [Severity, SevKey, string]> = [
  ["critical", "crit", "심각"],
  ["high", "hi", "높음"],
  ["medium", "mid", "중간"],
  ["low", "lo", "낮음"],
];

/** 토큰 요약. avg 는 점검이 없으면 null. */
export interface TokenSummary {
  /** 최근 days 일 입력+출력 합 */
  total: number;
  /** 같은 기간에 만든 점검 수 */
  tasks: number;
  /** total / tasks (추정: 토큰은 전역 UTC 일별 합이라 점검과 정확히 맞물리지 않는다) */
  avg: number | null;
}

/** 일별 토큰(입력+출력) 합과 점검당 평균(추정). */
export function tokenSummary(
  usage: Pick<TokenUsage, "daily">,
  tasks: readonly Pick<LiveTask, "created_unix">[],
  nowMs: number,
  days = WINDOW_DAYS,
): TokenSummary {
  const total = usage.daily.reduce((n, d) => n + d.input + d.output, 0);
  const since = nowMs - days * DAY_MS;
  const n = tasks.filter((t) => t.created_unix > 0 && t.created_unix * 1000 >= since).length;
  return { total, tasks: n, avg: n > 0 ? total / n : null };
}

/** 진행 중 점검 (최근 시작한 것부터). */
export function activeTasks(tasks: readonly LiveTask[]): LiveTask[] {
  return tasks.filter(isActive).sort((a, b) => b.created_unix - a.created_unix);
}

/** 끝난 점검 (최근 끝난 것부터). */
export function doneTasks(tasks: readonly LiveTask[]): LiveTask[] {
  return tasks
    .filter((t) => !isActive(t))
    .sort((a, b) => (b.completed_unix || b.created_unix) - (a.completed_unix || a.created_unix));
}

/** 발견 수 합계 (목록 줄 기준). */
export function findingsTotal(t: Pick<LiveTask, "findings">): number {
  const f = t.findings;
  return f.critical + f.high + f.medium + f.low;
}

/** 최근 점검 주소 칩 한 개. url 은 대상 origin (글에서 뽑은 추정값). */
export interface RecentTarget {
  url: string;
  host: string;
  createdUnix: number;
}

/** 점검 목록 → 최근 점검한 주소 (hostname 기준 중복 제거, 최근 것부터 max 개). */
export function recentTargets(tasks: readonly LiveTask[], max = 5): RecentTarget[] {
  const out: RecentTarget[] = [];
  const seen = new Set<string>();
  for (const t of [...tasks].sort((a, b) => b.created_unix - a.created_unix)) {
    const host = hostOf(t);
    const raw = urlOf(t);
    if (!host || !raw || seen.has(host)) continue;
    let url: string;
    try {
      url = new URL(raw).origin;
    } catch {
      continue;
    }
    seen.add(host);
    out.push({ url, host, createdUnix: t.created_unix });
    if (out.length >= max) break;
  }
  return out;
}

/** 도구 호출 승인 상태 → 알약 (server: pending · allowed · denied · timeout). */
export function approvalPill(status: string): { cls: "" | "ok" | "wait"; label: string } {
  if (status === "pending") return { cls: "wait", label: "승인 대기" };
  if (status === "allowed") return { cls: "ok", label: "허용함" };
  if (status === "denied") return { cls: "", label: "거부함" };
  if (status === "timeout") return { cls: "", label: "시간 초과" };
  return { cls: "", label: status || "알 수 없음" };
}
