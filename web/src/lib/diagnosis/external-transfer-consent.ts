// 설치 단위 외부 전송 동의 (사용자 결정 2026-11-26: 외부 전송 동의는 설치 때 한 번).
// GET/POST /api/product/external-transfer-consent — 서버 diagnosisprep.ProductExternalConsent 를 닫힌 파서로 읽는다.
// 외부 모델 점검은 이 동의가 있어야 시작한다(회차마다 묻지 않음). 새 설치는 설치 입력으로, 기존 설치는 관리자가 화면에서 한 번.

import { ApiError, apiFetch } from "./api";

export const EXTERNAL_TRANSFER_CONSENT_PATH = "/api/product/external-transfer-consent";

export type ExternalTransferConsent =
  /** 이 설치에 외부 모델이 없다 — 동의가 필요 없다 */
  | { required: false }
  | { required: true; statement: string; consented: false }
  | {
      required: true;
      statement: string;
      consented: true;
      /** install = 설치 입력, admin = 관리자 화면 */
      source: "install" | "admin";
      consentedBy: string;
      consentedAt: string;
    };

/** 서버 validText(2048바이트, 제어 문자 없음)와 같다. 서버가 만들 수 없는 문장은 받지 않는다. */
const STATEMENT_MAX_BYTES = 2048;
// biome-ignore lint/suspicious/noControlCharactersInRegex: 서버(unicode.IsControl)가 거부하는 C0 · DEL · C1 을 같이 거부한다
const controlChars = /[\u0000-\u001f\u007f-\u009f]/;
const REF = /^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$/;
const object = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);

function invalid(): never {
  throw new ApiError(422, "invalid_external_consent_contract");
}
function exact(v: Record<string, unknown>, keys: readonly string[]): void {
  if (Object.keys(v).some((k) => !keys.includes(k))) invalid();
}
function statementOk(v: unknown): v is string {
  return (
    typeof v === "string" &&
    v.length > 0 &&
    new TextEncoder().encode(v).length <= STATEMENT_MAX_BYTES &&
    !controlChars.test(v)
  );
}

/** 응답 본문 { external_consent: {...} } 을 읽는다. 서버 검증(validProductExternalConsent)과 같은 규칙으로 닫는다. */
export function parseExternalTransferConsent(body: unknown): ExternalTransferConsent {
  if (!object(body)) return invalid();
  exact(body, ["external_consent"]);
  const c = body.external_consent;
  if (!object(c)) return invalid();
  exact(c, ["required", "statement", "consented", "source", "consented_by", "consented_at"]);
  if (typeof c.required !== "boolean" || typeof c.consented !== "boolean") return invalid();
  if (!c.required) {
    if (c.consented || Object.keys(c).some((k) => k !== "required" && k !== "consented")) return invalid();
    return { required: false };
  }
  if (!statementOk(c.statement)) return invalid();
  if (!c.consented) {
    if (c.source !== undefined || c.consented_by !== undefined || c.consented_at !== undefined) return invalid();
    return { required: true, statement: c.statement, consented: false };
  }
  if (c.source !== "install" && c.source !== "admin") return invalid();
  if (typeof c.consented_by !== "string" || !REF.test(c.consented_by)) return invalid();
  // Go 의 time.Time 0값(0001-01-01)은 서버가 거부한다
  if (typeof c.consented_at !== "string" || Number.isNaN(Date.parse(c.consented_at)) || Date.parse(c.consented_at) <= 0)
    return invalid();
  return {
    required: true,
    statement: c.statement,
    consented: true,
    source: c.source,
    consentedBy: c.consented_by,
    consentedAt: c.consented_at,
  };
}

/** 이 설치의 외부 전송 동의 상태. 조회 실패는 동의 없음이 아니다(호출 화면이 '확인하지 못함'으로 보인다). */
export async function readExternalTransferConsent(signal?: AbortSignal): Promise<ExternalTransferConsent> {
  return parseExternalTransferConsent(
    await apiFetch<unknown>(EXTERNAL_TRANSFER_CONSENT_PATH, { cache: "no-store", redirect: "error", signal }, false),
  );
}

/** 관리자 동의를 한 번 남긴다. 이미 있으면 서버는 먼저 남긴 동의를 그대로 돌려준다. */
export async function recordExternalTransferConsent(): Promise<ExternalTransferConsent> {
  const result = parseExternalTransferConsent(
    await apiFetch<unknown>(
      EXTERNAL_TRANSFER_CONSENT_PATH,
      {
        method: "POST",
        redirect: "error",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ statement_confirmed: true }),
      },
      false,
    ),
  );
  if (!result.required || !result.consented) return invalid();
  return result;
}
