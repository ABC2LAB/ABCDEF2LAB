// 화면 표시용 민감값 가림. 서버 원문은 그대로이고, 여기서는 그리기 직전의 글만 바꾼다.
// 대상: 인증 헤더 값 · JSON/쿼리/폼/`key: value` 의 민감 키 값 · `Bearer xxx` · JWT 모양 문자열.
// 지나치게 가리는 쪽을 택한다(놓치는 것보다 낫다). 여러 번 걸어도 결과가 같다(멱등).

/** 가린 자리에 넣는 글. 서버 dlp.Redacted 와 같다. */
export const REDACTED = "[REDACTED]";

/** 민감 키 이름 조각 (대소문자 무시, 이름 안 어디에 있어도). */
const KEY_PART = "passw(?:or)?d|pwd|token|secret|session|api[_-]?key|authorization|cookie|credential";
const KEY_RE = new RegExp(KEY_PART, "i");

/** 값을 통째로 가리는 헤더. Cookie·Set-Cookie 는 이름을 남기고 값만 가린다. */
const HEADER_RE =
  /\b(Set-Cookie|Cookie|Proxy-Authorization|Authorization|X-Api-Key|X-Auth-Token|X-Access-Token)(\s*:[ \t]*)([^\r\n"'\\]*)/gi;
const HEADER_NAMES = new Set([
  "set-cookie",
  "cookie",
  "proxy-authorization",
  "authorization",
  "x-api-key",
  "x-auth-token",
  "x-access-token",
]);

// JSON 키: "password": "…" · "token": 123 · 이스케이프된 JSON(\"password\":\"…\")
const JSON_STR_RE = new RegExp(`"([^"\\\\]*?(?:${KEY_PART})[^"\\\\]*)"(\\s*:\\s*)"(?:[^"\\\\]|\\\\.)*"`, "gi");
const JSON_LIT_RE = new RegExp(
  `"([^"\\\\]*?(?:${KEY_PART})[^"\\\\]*)"(\\s*:\\s*)(-?\\d[\\d.eE+-]*|true|false)(?=\\s*[,}\\]\\r\\n]|\\s*$)`,
  "gi",
);
const JSON_ESC_RE = new RegExp(
  `\\\\"([^"\\\\]*?(?:${KEY_PART})[^"\\\\]*)\\\\"(\\s*:\\s*)\\\\"((?:[^"\\\\]|\\\\(?!")[^"])*)\\\\"`,
  "gi",
);
// 쿼리·폼·쿠키·설정: ?password=… &token=… · api_key = … (값은 & ; 공백 따옴표 # \ 앞까지)
const QUERY_RE = new RegExp(
  `(^|[?&;"'\\s,{(])([\\w.\\-\\[\\]]*?(?:${KEY_PART})[\\w.\\-\\[\\]]*)([ \\t]*=[ \\t]*)([^&\\s#"'\\\\;]+)`,
  "gim",
);
// 따옴표 없는 key: value (YAML·로그). 헤더 이름은 HEADER_RE 가 맡는다.
const PLAIN_RE = new RegExp(
  `(^|[\\s{,(])([\\w.-]*?(?:${KEY_PART})[\\w.-]*)(\\s*:[ \\t]+)(?!\\[REDACTED\\])([^\\s,;"'}\\]]+)`,
  "gim",
);
const BEARER_RE = /\bBearer\s+(?!\[REDACTED\])[A-Za-z0-9._~+/=-]+/g;
const JWT_RE = /\beyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]*/g;

/** 이 이름이 민감 키인가 (password·token·session·cookie·api_key … 를 품음). */
export function isSensitiveKey(name: string): boolean {
  return KEY_RE.test(name);
}

/** 헤더 값 가리기: Cookie 는 `a=[R]; b=[R]`, Set-Cookie 는 첫 쌍만 가리고 속성 유지, Authorization 은 방식(Bearer 등) 유지. */
function maskHeaderValue(name: string, value: string): string {
  const v = value.trim();
  if (!v) return value;
  const n = name.toLowerCase();
  if (n === "cookie") {
    return v
      .split(";")
      .map((part) => {
        const p = part.trim();
        const eq = p.indexOf("=");
        return eq > 0 ? `${p.slice(0, eq)}=${REDACTED}` : p && REDACTED;
      })
      .filter(Boolean)
      .join("; ");
  }
  if (n === "set-cookie") {
    const [first, ...attrs] = v.split(";");
    const eq = first.indexOf("=");
    const head = eq > 0 ? `${first.slice(0, eq).trim()}=${REDACTED}` : REDACTED;
    return [head, ...attrs.map((a) => a.trim())].join("; ");
  }
  if (n === "authorization" || n === "proxy-authorization") {
    const m = v.match(/^([A-Za-z][\w-]*)\s+\S/);
    return m ? `${m[1]} ${REDACTED}` : REDACTED;
  }
  return REDACTED;
}

/**
 * 글 안의 민감값을 `[REDACTED]` 로 바꾼다 (화면 표시용).
 * - 헤더: Cookie · Set-Cookie · Authorization · Proxy-Authorization · X-Api-Key (· X-Auth-Token · X-Access-Token)
 * - JSON(이스케이프된 JSON 포함)·쿼리·폼·`key: value` 에서 이름에 password|passwd|pwd|token|secret|session|api_key|apikey|
 *   access_token|refresh_token|authorization|cookie|credential 이 들어간 키의 값
 * - `Bearer xxx`, JWT 모양(eyJ….….…)
 * 서버 원문은 가려지지 않는다 → 화면에 `<MaskedNote />` 를 함께 둔다.
 */
export function maskSecrets(text: string): string {
  if (!text) return text;
  let out = text.replace(HEADER_RE, (_m, name: string, sep: string, value: string) => {
    return `${name}${sep}${maskHeaderValue(name, value)}`;
  });
  out = out.replace(JSON_STR_RE, (_m, key: string, sep: string) => `"${key}"${sep}"${REDACTED}"`);
  out = out.replace(JSON_LIT_RE, (_m, key: string, sep: string) => `"${key}"${sep}"${REDACTED}"`);
  out = out.replace(JSON_ESC_RE, (_m, key: string, sep: string) => `\\"${key}\\"${sep}\\"${REDACTED}\\"`);
  out = out.replace(QUERY_RE, (_m, pre: string, key: string, eq: string) => `${pre}${key}${eq}${REDACTED}`);
  out = out.replace(PLAIN_RE, (m, pre: string, key: string, sep: string) =>
    HEADER_NAMES.has(key.toLowerCase()) ? m : `${pre}${key}${sep}${REDACTED}`,
  );
  out = out.replace(BEARER_RE, `Bearer ${REDACTED}`);
  out = out.replace(JWT_RE, REDACTED);
  return out;
}

/** 이름-값 한 쌍(파라미터·미리보기 칸) 가리기: 이름이 민감 키면 값 전체, 아니면 maskSecrets(값). */
export function maskParam(name: string, value: string): string {
  if (!value) return value;
  return isSensitiveKey(name) ? REDACTED : maskSecrets(value);
}

/** 서버가 준 JSON 값을 보기 좋게 문자열로 만든 뒤 가린다 (문자열이면 그대로 가림). */
export function maskJson(value: unknown): string {
  if (value === undefined || value === null) return "";
  if (typeof value === "string") return maskSecrets(value);
  let text: string;
  try {
    text = JSON.stringify(value, null, 2);
  } catch {
    text = String(value);
  }
  return maskSecrets(text);
}
