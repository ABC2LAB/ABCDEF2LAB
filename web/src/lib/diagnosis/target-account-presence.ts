import { ApiError, apiFetch } from "./api";

const object = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const ref = (v: unknown): v is string => typeof v === "string" && /^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$/.test(v);

/**
 * 현재 URL의 서버 승인 대상과 실제 계정 API만 조회해, 이 대상에 점검에 쓸 시험 계정이 있는지 본다. 조회 실패는 계정 없음이
 * 아니다. 계정 분류 폐지(사용자 결정 2026-11-26, #967) 뒤 서버는 자격이 저장된 등록 계정을 회차에 결속하므로, 같은 기준
 * (credential_configured)으로 센다. 서버가 아직 싣는 classification 칸은 보지 않는다.
 */
export async function readTargetAccountPresence(
  target: string,
  signal?: AbortSignal,
): Promise<{ hasTestAccounts: boolean; loginMethodMissing: boolean; targetId?: string }> {
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
    return { hasTestAccounts: false, loginMethodMissing: false };
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
    accounts.some((a) => !object(a) || !(typeof a.label === "string" && /^[a-z0-9][a-z0-9_-]{0,31}$/.test(a.label)))
  )
    throw new ApiError(422, "invalid_account_contract");
  // 자격은 저장됐는데 이 대상의 로그인 방식이 없으면 서버가 login_method_missing: true 를 준다(#914, workbench-live-data).
  // 대상 설정 상태일 뿐이라 MFA · 지원 안 함으로 읽지 않고, 없다고 로그인 성공으로도 읽지 않는다
  const configured = accounts.filter((a) => object(a) && a.credential_configured === true);
  return {
    hasTestAccounts: configured.length > 0,
    loginMethodMissing: configured.some((a) => object(a) && a.login_method_missing === true),
    // 진행 화면의 '로그인 방식 등록' 안내가 시험 계정 화면에서 이 대상을 미리 고르게 한다
    targetId: t.id,
  };
}
