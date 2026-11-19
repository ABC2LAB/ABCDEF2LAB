// 점검 시작·경과·소요 표시 (docs/contracts/diagnosis-run-timing.md '화면 인계').
// 서버가 확인해 저장한 started_at·finished_at 만 쓴다 — 브라우저 시작 시각·새 스톱워치·보고서 saved_at 대용·ETA 를 만들지 않는다.
// 파서(diagnosis-run-timing.ts)가 형식·사실 결속·순서(finished_at ≥ started_at)를 이미 닫았다. 여기는 표시용 계산만.

import type { URLDiagnosisJob } from "@/lib/diagnosis/diagnosis-url";

import { dateTimeOf, toMs } from "./map";

type Timing = Pick<URLDiagnosisJob, "state" | "started" | "started_at" | "finished_at">;

/** 경과를 셀 수 있는 상태. unknown 은 finished_at 이 없어도 실행 중이라는 근거가 아니다(계약 §화면 인계 4). */
const TICKING: readonly URLDiagnosisJob["state"][] = ["running", "stopping"];

/** 서버가 확인한 시작 시각(로컬 'YYYY-MM-DD HH:MM:SS'). 없으면 null. */
export function runStartText(job: Timing | null | undefined): string | null {
  return job?.started_at ? dateTimeOf(job.started_at) || null : null;
}

/** 소요(초) = finished_at − started_at. 둘 중 하나라도 없으면 null — 추정하지 않는다(§화면 인계 2·3). */
export function runDurationSec(job: Timing | null | undefined): number | null {
  const start = toMs(job?.started_at);
  const finish = toMs(job?.finished_at);
  if (start === null || finish === null) return null;
  return Math.max(0, Math.floor((finish - start) / 1000));
}

/**
 * 진행 중 경과(초) = 지금 − started_at. 서버 시작 시각이 기준이라 새로고침·재연결에도 0 으로 돌아가지 않는다(§화면 인계 1).
 * 실행 중(running·stopping)이고 아직 마감 시각이 없을 때만. nowMs 가 0(마운트 전)이면 null.
 * 브라우저 시계가 서버보다 늦어 음수가 되면 0 으로 둔다.
 */
export function runElapsedSec(job: Timing | null | undefined, nowMs: number): number | null {
  if (!job || !TICKING.includes(job.state) || job.finished_at || nowMs <= 0) return null;
  const start = toMs(job.started_at);
  if (start === null) return null;
  return Math.max(0, Math.floor((nowMs - start) / 1000));
}

/** 시작 시각이 없을 때 '연결 전' 딱지에 붙이는 이유. 시작 전과 시각을 남기지 않은 이전 회차를 나눈다. */
export function runStartMissingWhy(job: Timing | null | undefined): string {
  if (!job) return "점검 상태를 아직 읽지 못했습니다";
  if (!job.started) return "아직 엔진 실행이 시작되지 않았습니다";
  return "서버가 시작 시각을 남기지 않은 이전 회차입니다";
}

/**
 * 소요가 없을 때의 이유. 마감 시각이 없을 때 실행 중(running·stopping)이면 '아직 없음', 그 밖(결과 불명·끝난 상태)이면
 * '남기지 않음' — 결과 불명의 finished_at 부재를 곧 생길 값처럼 읽히게 하지 않는다(§화면 인계 4).
 */
export function runDurationMissingWhy(job: Timing | null | undefined): string {
  if (!job) return "점검 상태를 아직 읽지 못했습니다";
  if (job.started_at && !job.finished_at)
    return TICKING.includes(job.state)
      ? "마감 시각이 아직 없습니다. 시작·마감 시각이 둘 다 있을 때만 셉니다"
      : "서버가 마감 시각을 남기지 않았습니다. 시작·마감 시각이 둘 다 있을 때만 셉니다";
  return "시작·마감 시각이 둘 다 있을 때만 셉니다";
}
