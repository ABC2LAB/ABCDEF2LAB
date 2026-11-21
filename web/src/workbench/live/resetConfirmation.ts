// 대상 리셋 확인(E③ 결정 §6.1). 확정 단계가 대상에 시험 객체를 만든 뒤에는 운영자가 대상을 리셋했다고 확인해야 다음
// 확정 점검이 시작된다(서버 CheckRunStart: reset_confirmation_missing).
// 계약: 서버 `GET`·`POST /api/product/targets/{id}/reset-confirmation`(server/product_targets_http.go) — 응답은
// ProductResponse 의 `reset`(diagnosisprep.ProductResetState). 동의는 화면이 본 쓰기 기록 줄(`last_writes_ref`)에 묶이고, 그
// 사이 기록이 바뀌면 409 `reset_record_changed`다. 객체 ID · 표식은 응답에 없고 개수만 있다.

import { ApiError, apiFetch } from "@/lib/diagnosis/api";

export type ResetState = {
  /** 운영자 리셋 확인 전까지 다음 확정 점검이 시작되지 않는다. */
  resetRequired: boolean;
  /** 마지막 확정 단계 쓰기를 남긴 회차. 없으면 쓰기 기록이 없다(아래 칸도 모두 비어 있음). */
  lastWritesJob: string;
  lastWritesAt: string;
  lastWritesObjects: number;
  /** 동의를 묶을 쓰기 기록 줄 참조(SHA-256 16진). 그대로 POST 에 보낸다. */
  lastWritesRef: string;
  confirmedBy: string;
  confirmedAt: string;
  /** 서버가 고정한 리셋 진술. 화면은 바꾸지 않고 그대로 보인다. */
  statement: string;
};

const REF = /^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}$/;
const WRITES_REF = /^[0-9a-f]{64}$/;
// 서버 상한(e3contract.MaxDeclarationTypes)과 같다. 넘으면 계약 밖이다.
const MAX_OBJECTS = 16;
const MAX_STATEMENT = 1024;
const RESET_KEYS = new Set([
  "reset_required",
  "last_writes_job",
  "last_writes_at",
  "last_writes_objects",
  "last_writes_ref",
  "confirmed_by",
  "confirmed_at",
  "statement",
]);
// Go time.Time 의 영(0) 값. omitempty 가 time.Time 에는 걸리지 않아 빈 시각도 이 값으로 온다.
const ZERO_TIME = "0001-01-01T00:00:00Z";

const isObj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const timeOf = (v: unknown): string | null => {
  if (v === undefined || v === ZERO_TIME) return "";
  return typeof v === "string" && !Number.isNaN(Date.parse(v)) ? v : null;
};

/**
 * 서버 응답 → 리셋 상태. 서버 검사(diagnosisprep.validProductResetState)와 같은 조건이 아니면 응답 전체를 깨진 것으로
 * 본다(fail-closed): 모르는 칸, 쓰기 기록 없음인데 다른 칸이 있음, 확인 여부와 시각이 어긋남 등.
 */
export function parseResetResponse(res: unknown): ResetState {
  const r = isObj(res) ? res.reset : undefined;
  if (!isObj(r) || Object.keys(r).some((k) => !RESET_KEYS.has(k))) throw new ApiError(422, "invalid_reset_state");
  // 빈 칸은 omitempty 로 빠져 온다. 있으면 문자열이어야 한다
  const str = (k: string): string | null => {
    const v = r[k];
    if (v === undefined) return "";
    return typeof v === "string" ? v : null;
  };
  const job = str("last_writes_job");
  const ref = str("last_writes_ref");
  const by = str("confirmed_by");
  const statement = r.statement;
  const objects = r.last_writes_objects ?? 0;
  const at = timeOf(r.last_writes_at);
  const confirmedAt = timeOf(r.confirmed_at);
  if (
    typeof r.reset_required !== "boolean" ||
    job === null ||
    ref === null ||
    by === null ||
    at === null ||
    confirmedAt === null ||
    typeof statement !== "string" ||
    statement.length > MAX_STATEMENT ||
    typeof objects !== "number" ||
    !Number.isInteger(objects) ||
    objects < 0 ||
    objects > MAX_OBJECTS
  )
    throw new ApiError(422, "invalid_reset_state");
  const state: ResetState = {
    resetRequired: r.reset_required,
    lastWritesJob: job,
    lastWritesAt: at,
    lastWritesObjects: objects,
    lastWritesRef: ref,
    confirmedBy: by,
    confirmedAt,
    statement,
  };
  const confirmed = by !== "";
  const consistent =
    job === ""
      ? !state.resetRequired && at === "" && objects === 0 && ref === "" && !confirmed && confirmedAt === ""
      : REF.test(job) &&
        WRITES_REF.test(ref) &&
        at !== "" &&
        objects > 0 &&
        state.resetRequired !== confirmed &&
        confirmed !== (confirmedAt === "") &&
        (!confirmed || REF.test(by));
  if (!consistent) throw new ApiError(422, "invalid_reset_state");
  return state;
}

const resetPath = (targetId: string) => `/api/product/targets/${encodeURIComponent(targetId)}/reset-confirmation`;

export async function getResetState(targetId: string, signal?: AbortSignal): Promise<ResetState> {
  const res = await apiFetch<unknown>(resetPath(targetId), { cache: "no-store", redirect: "error", signal });
  return parseResetResponse(res);
}

/** 운영자가 진술에 동의했음을 남긴다. 화면이 본 쓰기 기록 줄에만 묶인다. */
export async function confirmReset(targetId: string, writesRef: string): Promise<ResetState> {
  if (!WRITES_REF.test(writesRef)) throw new ApiError(400, "invalid_reset_request");
  const res = await apiFetch<unknown>(resetPath(targetId), {
    method: "POST",
    cache: "no-store",
    redirect: "error",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ statement_confirmed: true, writes_ref: writesRef }),
  });
  return parseResetResponse(res);
}

/** 리셋 확인 실패 문구. 모르는 오류는 일반 실패로 둔다(확인된 것으로 추정하지 않음). */
export function resetFailureText(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.status === 409 && e.message === "reset_record_changed")
      return "그사이 쓰기 기록이 바뀌었습니다. 새 기록을 보고 다시 확인해 주세요.";
    if (e.status === 409 && e.message === "reset_run_active") return "진행 중인 점검이 끝난 뒤에 확인할 수 있습니다.";
    if (e.status === 403) return "이 대상의 리셋을 확인할 권한이 없습니다.";
    if (e.status === 404) return "대상을 찾지 못했습니다.";
  }
  return "리셋 확인을 남기지 못했습니다. 다시 시도해 주세요.";
}
