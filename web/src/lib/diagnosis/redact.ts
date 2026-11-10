/**
 * 승인 화면에 보여 줄 도구 입력(tool_input)에서 쿠키 · 인증 헤더 · 토큰 · 비밀번호 값을 가린다.
 * 사람이 "무엇을 보내려는지"는 판단할 수 있게 구조와 나머지 값은 그대로 두고, 비밀 값만 [REDACTED] 로 바꾼다.
 * 최선 노력 방식이라 모든 비밀을 보장하지는 않는다 — 화면 표시용이고 저장 · 전송하지 않는다.
 */

export const REDACTED = "[REDACTED]";

/** 이름만 보고 값을 통째로 가릴 키 */
const SECRET_KEY = /(cookie|authorization|token|secret|passw(or)?d|api[-_]?key|session|credential)/i;

/** 문자열 안의 헤더 · Bearer · curl 옵션 · key=value 형태 비밀 값 */
const TEXT_RULES: [RegExp, string][] = [
  [/^(\s*(?:cookie|set-cookie|authorization|proxy-authorization|x-api-key)\s*:\s*).+$/gim, `$1${REDACTED}`],
  [/(-H\s*['"]?\s*(?:cookie|authorization|x-api-key)\s*:\s*)[^'"\n]+/gi, `$1${REDACTED}`],
  [/(-b\s+['"]?)[^'"\s]+/g, `$1${REDACTED}`],
  [/(bearer\s+)[A-Za-z0-9._~+/=-]+/gi, `$1${REDACTED}`],
  [/((?:password|passwd|pwd|token|api_key|apikey|secret|session)=)[^&\s'"]+/gi, `$1${REDACTED}`],
];

function redactText(s: string): string {
  return TEXT_RULES.reduce((acc, [re, to]) => acc.replace(re, to), s);
}

function walk(v: unknown): unknown {
  if (typeof v === "string") return redactText(v);
  if (Array.isArray(v)) return v.map(walk);
  if (v && typeof v === "object") {
    return Object.fromEntries(Object.entries(v).map(([k, val]) => [k, SECRET_KEY.test(k) ? REDACTED : walk(val)]));
  }
  return v;
}

/** tool_input(JSON 값 또는 JSON 문자열) → 가린 뒤 보기 좋게 줄 바꾼 문자열 */
export function redactInput(input: unknown): string {
  let value = input;
  if (typeof input === "string") {
    try {
      value = JSON.parse(input);
    } catch {
      return redactText(input);
    }
  }
  if (value === undefined || value === null) return "";
  return JSON.stringify(walk(value), null, 2);
}
