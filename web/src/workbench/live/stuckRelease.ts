// 멈춘 작업 해제(C04, 재기동형). 결과 불명 작업 하나가 설치 전체의 새 점검 시작을 막을 때 운영자가 쓴다.
// 계약: 서버 `GET`·`POST /api/product/preparation/release`(server/product_release_http.go). 응답 `release`는
// diagnosisprep.ReleaseView, 해제 성공 응답에는 `draft`(막던 작업)도 온다. 거절은 409 `release_<사유>`, 관리자가 아니면 403.
// 해제가 받아들여지면 준비 서비스가 스스로 정상 종료하고 컨테이너 재시작 정책으로 다시 뜬다. 그동안 화면 연결이 끊기므로
// 화면은 '재시작 중'을 보이고 health가 돌아오면 다시 읽는다. 응답을 못 받아도 감사 기록은 서버 저널에 있어 다시 읽으면 안다.

import { ApiError, apiFetch } from "@/lib/diagnosis/api";

export type ReleaseRecord = {
  jobId: string;
  principal: string;
  requestId: string;
  reason: string;
  requestedAt: string;
  lastActivityAt: string;
  receiptUpdatedAt: string;
  quietSeconds: number;
  jobState: string;
  previousInit: string;
  explorationClosed: boolean;
  newInit: string;
  completedAt: string;
  boundaryCheckedAt: string;
};

export type ReleaseView = {
  jobId: string;
  jobRevision: number;
  jobState: string;
  eligible: boolean;
  refusal: string;
  quietSeconds: number;
  now: string;
  lastActivityAt: string;
  receiptUpdatedAt: string;
  eligibleAt: string;
  record: ReleaseRecord | null;
};

export const RELEASE_PATH = "/api/product/preparation/release";
export const HEALTH_PATH = "/api/product/health";
// 서버 상한(diagnosisprep.MaxReleaseReasonBytes)과 같다. UTF-8 바이트로 센다.
export const MAX_REASON_BYTES = 500;
const REFUSALS = new Set([
  "not_stuck",
  "job_running",
  "not_quiet",
  "other_active",
  "exploration_open",
  "unsupported",
  "pending_restart",
]);
const REF = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;

const isObj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const timeOf = (v: unknown): string | null => {
  if (v === undefined) return "";
  return typeof v === "string" && !Number.isNaN(Date.parse(v)) ? v : null;
};
const strOf = (v: unknown): string | null => {
  if (v === undefined) return "";
  return typeof v === "string" ? v : null;
};
const bad = (): never => {
  throw new ApiError(422, "invalid_release_state");
};

export function reasonBytes(reason: string): number {
  return new TextEncoder().encode(reason).length;
}

/** 사유는 비어 있지 않고 서버 상한 안이며 제어 문자가 없어야 한다(서버 validReleaseReason과 같은 조건). */
export function validReason(reason: string): boolean {
  const trimmed = reason.trim();
  // 줄바꿈·탭 같은 제어 문자(C0·DEL·C1)는 감사 기록 한 줄을 깨므로 받지 않는다(서버 unicode.IsControl과 같은 범위)
  const control = [...trimmed].some((ch) => {
    const code = ch.codePointAt(0) ?? 0;
    return code < 0x20 || (code >= 0x7f && code <= 0x9f);
  });
  // 방향 바꿈(U+202E)·폭 없는 문자 같은 서식 문자와 줄·문단 구분자는 화면의 감사 사유 겉보기를 바꾸므로 받지 않는다(서버와 같음)
  const format = /[\p{Cf}\p{Zl}\p{Zp}]/u.test(trimmed);
  return trimmed !== "" && reasonBytes(trimmed) <= MAX_REASON_BYTES && !control && !format;
}

function parseRecord(v: unknown): ReleaseRecord | null {
  if (v === undefined || v === null) return null;
  if (!isObj(v)) return bad();
  const str = (k: string) => strOf(v[k]) ?? bad();
  const time = (k: string) => timeOf(v[k]) ?? bad();
  const quiet = v.quiet_seconds;
  const rec: ReleaseRecord = {
    jobId: str("job_id"),
    principal: str("principal"),
    requestId: str("request_id"),
    reason: str("reason"),
    requestedAt: time("requested_at"),
    lastActivityAt: time("last_activity_at"),
    receiptUpdatedAt: time("receipt_updated_at"),
    quietSeconds: typeof quiet === "number" && Number.isSafeInteger(quiet) ? quiet : bad(),
    jobState: str("job_state"),
    previousInit: str("previous_init"),
    explorationClosed: v.exploration_closed === true,
    newInit: str("new_init"),
    completedAt: time("completed_at"),
    boundaryCheckedAt: time("boundary_checked_at"),
  };
  if (
    !REF.test(rec.jobId) ||
    rec.requestedAt === "" ||
    rec.receiptUpdatedAt === "" ||
    (rec.completedAt === "") !== (rec.newInit === "")
  )
    bad();
  return rec;
}

/** 서버 응답 → 해제 판단 근거. 모양이 계약과 다르면 해제할 수 있다고 추정하지 않는다(fail-closed). */
export function parseReleaseView(res: unknown): ReleaseView {
  const r = isObj(res) ? res.release : undefined;
  if (!isObj(r)) return bad();
  const str = (k: string) => strOf(r[k]) ?? bad();
  const time = (k: string) => timeOf(r[k]) ?? bad();
  const quiet = r.quiet_seconds;
  const revision = r.job_revision ?? 0;
  const view: ReleaseView = {
    jobId: str("job_id"),
    jobRevision: typeof revision === "number" && Number.isSafeInteger(revision) && revision >= 0 ? revision : bad(),
    jobState: str("job_state"),
    eligible: r.eligible === true,
    refusal: str("refusal"),
    quietSeconds: typeof quiet === "number" && Number.isSafeInteger(quiet) && quiet > 0 ? quiet : bad(),
    now: time("now"),
    lastActivityAt: time("last_activity_at"),
    receiptUpdatedAt: time("receipt_updated_at"),
    eligibleAt: time("eligible_at"),
    record: parseRecord(r.record),
  };
  if (view.eligible === (view.refusal !== "") || (!view.eligible && !REFUSALS.has(view.refusal))) bad();
  if (view.jobId !== "" && (!REF.test(view.jobId) || view.jobRevision === 0)) bad();
  if (view.jobId === "" && view.refusal !== "not_stuck") bad();
  return view;
}

export async function getReleaseView(signal?: AbortSignal): Promise<ReleaseView> {
  return parseReleaseView(await apiFetch<unknown>(RELEASE_PATH, { cache: "no-store", redirect: "error", signal }));
}

/** 해제를 요청한다. 성공하면 준비 서비스가 곧 다시 시작한다. */
export async function requestRelease(jobId: string, jobRevision: number, reason: string): Promise<ReleaseView> {
  if (!REF.test(jobId) || !Number.isSafeInteger(jobRevision) || jobRevision < 1 || !validReason(reason))
    throw new ApiError(400, "invalid_request");
  const res = await apiFetch<unknown>(RELEASE_PATH, {
    method: "POST",
    cache: "no-store",
    redirect: "error",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ job_id: jobId, expected_revision: jobRevision, reason: reason.trim() }),
  });
  return parseReleaseView(res);
}

/** 준비 서비스·API가 다시 붙었는지 본다. 인증 없이 읽는 health다. 연결 실패는 아직 재시작 중으로 본다. */
export async function preparationReady(signal?: AbortSignal): Promise<boolean> {
  try {
    const res = await fetch(HEALTH_PATH, { cache: "no-store", redirect: "error", signal });
    if (!res.ok) return false;
    const body: unknown = await res.json();
    return isObj(body) && body.ready === true;
  } catch {
    return false;
  }
}

/**
 * 해제 요청이 서버에 닿았는지 모를 때(응답 전에 연결이 끊김) 재시작 중으로 볼 오류인지. 준비 서비스가 감사 기록 뒤 바로
 * 종료하므로 응답을 잃을 수 있다. 결과는 다시 읽어 감사 기록으로 판단한다.
 */
export function mayBeRestarting(e: unknown): boolean {
  if (!(e instanceof ApiError)) return true;
  return e.status === 0 || e.status === 502 || e.status === 503 || e.status === 504;
}

export function stateLabel(state: string): string {
  switch (state) {
    case "unknown":
      return "결과 불명";
    case "stopping":
      return "종료 중";
    case "running":
      return "실행 중";
    case "preparing":
      return "준비 중";
    default:
      return state || "—";
  }
}

// 해제를 못 하는 사유 문구. 해제할 수 있다고 추정하는 문구는 쓰지 않는다.
const REFUSAL_TEXT: Record<string, (minutes: number, eligibleAtText: string) => string> = {
  not_quiet: (minutes, at) =>
    `최근 ${minutes}분 안에 엔진 활동이나 기록 갱신이 있었어요.${at ? ` ${at}부터 해제할 수 있어요.` : ""}`,
  job_running: () => "그 점검이 아직 실행 중이에요. 먼저 '종료'를 눌러 주세요.",
  other_active: () => "다른 점검이 진행 중이라 해제할 수 없어요.",
  exploration_open: () => "탐색 경계 세션을 닫거나 닫힘을 확인하지 못했어요. 탐색 경계 상태를 확인해 주세요.",
  unsupported: () =>
    "이 설치는 재시작으로 종료를 확인할 수 없어 화면 해제를 쓸 수 없어요. 전체 재기동 절차를 따라 주세요.",
  pending_restart: () => "해제를 기록했어요. 준비 서비스 재시작과 탐색 경계 확인을 기다리고 있어요.",
};

export function refusalText(view: Pick<ReleaseView, "refusal" | "quietSeconds">, eligibleAtText = ""): string {
  const text = REFUSAL_TEXT[view.refusal];
  return text ? text(Math.round(view.quietSeconds / 60), eligibleAtText) : "";
}

export function releaseFailureText(e: unknown, quietSeconds: number): string {
  if (e instanceof ApiError) {
    const code = e.message.startsWith("release_") ? e.message.slice("release_".length) : "";
    if (e.status === 409 && REFUSALS.has(code)) return refusalText({ refusal: code, quietSeconds });
    if (e.status === 409 && e.message === "revision_conflict")
      return "그사이 작업 상태가 바뀌었어요. 다시 읽고 확인해 주세요.";
    if (e.status === 403) return "멈춘 작업 해제는 관리자만 할 수 있어요.";
    if (e.status === 404) return "막던 작업을 찾지 못했어요. 다시 읽어 주세요.";
    if (e.status === 400) return "사유를 확인해 주세요(빈 칸·줄바꿈 없이 500바이트 이하).";
  }
  return "해제를 요청하지 못했어요. 다시 읽고 확인해 주세요.";
}
