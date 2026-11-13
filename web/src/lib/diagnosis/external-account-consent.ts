import { ApiError, apiFetch } from "./api";

export const EXTERNAL_ACCOUNT_STATEMENT =
  "이 점검에서는 시험 계정 세션(쿠키·토큰)이 외부 모델 제공자에게 전달될 수 있습니다. 고객사에 사전 공지하고 승인을 받았습니다.";
export const EXTERNAL_ACCOUNT_PASSWORD_NOTICE = "계정 비밀번호는 모델에 전달되지 않습니다.";
const object = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const ref = (v: unknown): v is string => typeof v === "string" && /^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$/.test(v);

/** 현재 URL의 서버 승인 대상과 실제 계정 API만 조회한다. 조회 실패는 계정 없음이 아니다. */
export async function readExternalTestAccountPresence(
  target: string,
  signal?: AbortSignal,
): Promise<{ hasTestAccounts: boolean; statement: string }> {
  const origin = new URL(target).origin;
  const resolved = await apiFetch<unknown>(
    `/api/product/targets/resolve?url=${encodeURIComponent(target)}`,
    { cache: "no-store", redirect: "error", signal },
    false,
  );
  if (!object(resolved)) throw new ApiError(422, "invalid_target_contract");
  if (
    resolved.target === undefined &&
    (Object.keys(resolved).length === 0 || (Array.isArray(resolved.targets) && resolved.targets.length === 0))
  )
    return { hasTestAccounts: false, statement: "" };
  const t = resolved.target;
  if (!object(t) || !ref(t.id) || t.origin !== origin || t.state !== "approved")
    throw new ApiError(422, "invalid_target_contract");
  const result = await apiFetch<unknown>(
    `/api/product/targets/${encodeURIComponent(t.id)}/accounts`,
    { cache: "no-store", redirect: "error", signal },
    false,
  );
  if (!object(result) || (result.accounts !== undefined && !Array.isArray(result.accounts)))
    throw new ApiError(422, "invalid_account_contract");
  const accounts = result.accounts ?? [];
  if (
    !Array.isArray(accounts) ||
    accounts.some(
      (a) =>
        !object(a) ||
        !(typeof a.label === "string" && /^[a-z0-9][a-z0-9_-]{0,31}$/.test(a.label)) ||
        !["test", "real"].includes(String(a.classification)),
    )
  )
    throw new ApiError(422, "invalid_account_contract");
  const statement =
    typeof result.external_consent_statement === "string" &&
    result.external_consent_statement.length > 0 &&
    new TextEncoder().encode(result.external_consent_statement).length <= 1024 &&
    !/[\x00-\x1f\x7f]/.test(result.external_consent_statement)
      ? result.external_consent_statement
      : "";
  return { hasTestAccounts: accounts.some((a) => object(a) && a.classification === "test"), statement };
}
