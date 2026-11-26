// 클립보드 복사용 민감값 가림(#975 이동찬 P2, 사용자 결정 2026-11-26). 화면 표시는 가리지 않지만(DLP 폐기, CLAUDE.md 절대규칙 2 —
// 제품 화면은 내부 담당자만 본다), '복사' · 'PoC 복사' · 코드 상자 복사처럼 화면 밖(팀 채팅 · 이슈 · 보고서)으로 붙여 넣는 글은
// 지금처럼 가린다. 레포 · PR · 이슈 · 팀 채팅에는 비밀번호 · 토큰 값을 붙이지 않는다(같은 규칙 2).
// 대상: 인증 헤더 값 · JSON/쿼리/폼/`key: value` 의 민감 키 값 · `Bearer xxx` · JWT 모양 문자열.
// 지나치게 가리는 쪽을 택한다(놓치는 것보다 낫다). 여러 번 걸어도 결과가 같다(멱등).

/** 가린 자리에 넣는 글. 서버 dlp.Redacted 와 같다. */
export const REDACTED = "[REDACTED]";

/** 민감 키 이름 조각 (대소문자 무시, 이름 안 어디에 있어도). */
const KEY_PART = "passw(?:or)?d|pwd|token|secret|session|api[_-]?key|authorization|cookie|credential";

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
 * 글 안의 민감값을 `[REDACTED]` 로 바꾼다 (한 줄 단위 · 글자 그대로. 클립보드로 낼 때는 maskForCopy).
 * - 헤더: Cookie · Set-Cookie · Authorization · Proxy-Authorization · X-Api-Key (· X-Auth-Token · X-Access-Token)
 * - JSON(이스케이프된 JSON 포함)·쿼리·폼·`key: value` 에서 이름에 password|passwd|pwd|token|secret|session|api_key|apikey|
 *   access_token|refresh_token|authorization|cookie|credential 이 들어간 키의 값
 * - `Bearer xxx`, JWT 모양(eyJ….….…)
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

/**
 * 이름 붙은 HTML 엔티티 중 ASCII 문장부호 · 공백으로 풀리는 것. 키 이름의 구분자(= : _ " 등)를 숨길 수 있어서 푼다.
 * 영문자로 풀리는 이름 엔티티는 없고, 글자는 숫자 엔티티(&#112;)로만 숨길 수 있다.
 */
const NAMED_ENTITIES: Record<string, string> = {
  amp: "&",
  AMP: "&",
  lt: "<",
  LT: "<",
  gt: ">",
  GT: ">",
  quot: '"',
  QUOT: '"',
  apos: "'",
  excl: "!",
  num: "#",
  dollar: "$",
  percnt: "%",
  lpar: "(",
  rpar: ")",
  ast: "*",
  midast: "*",
  plus: "+",
  comma: ",",
  period: ".",
  sol: "/",
  colon: ":",
  semi: ";",
  equals: "=",
  quest: "?",
  commat: "@",
  lsqb: "[",
  lbrack: "[",
  bsol: "\\",
  rsqb: "]",
  rbrack: "]",
  Hat: "^",
  lowbar: "_",
  UnderBar: "_",
  grave: "`",
  DiacriticalGrave: "`",
  lcub: "{",
  lbrace: "{",
  verbar: "|",
  vert: "|",
  VerticalLine: "|",
  rcub: "}",
  rbrace: "}",
  Tab: "\t",
  NewLine: "\n",
  nbsp: " ",
  NonBreakingSpace: " ",
};
const MAX_CODE_POINT = 0x10ffff;
const ENTITY_RE = /&(?:#(\d{1,8})|#[xX]([0-9a-fA-F]{1,7})|([A-Za-z][A-Za-z0-9]{1,31}));?/g;
/** Markdown 역슬래시 이스케이프(\= \_ \" …): 붙여 넣은 곳이 Markdown 이면 역슬래시가 빠진 글로 보인다. */
const MD_ESCAPE_RE = /\\([!-/:-@[-`{-~])/g;
/** 링크 · 이미지(직접 `[글](주소)` · 참조형 `[글][이름]`): 붙여 넣은 곳에는 글(대체 글)만 보인다. */
const MD_LINK_RE = /!?\[([^\]]*)\](?:\([^)]*\)|\[[^\]]*\])/g;
/** HTML 주석 · 태그: 그려지면 사라져 앞뒤 글이 붙는다(pass<!-- -->word, <b>pass</b>word). */
const HTML_COMMENT_RE = /<!--[\s\S]*?-->/g;
const HTML_TAG_RE = /<\/?[A-Za-z][A-Za-z0-9-]*(?:\s[^<>]*)?\/?>/g;
/** 강조 · 취소선 · 코드 표시(**pass**word, _pass_word, `pass`word): 그려지면 표시가 사라져 글이 붙는다. */
const MD_MARK_RE = /[*_~`]+/g;
/**
 * 붙여 넣은 곳에서 줄을 가르는 글자. 풀린 글(&#10; · &#13; · &NewLine; …)에 생기면 키와 값이 다른 줄로 갈려 한 줄 단위
 * 가림(PLAIN_RE 등)이 못 알아본다(#709 이동찬 리뷰: `password:&#10;값`).
 */
const LINE_BREAK_RE = /[\r\n\v\f\u0085\u2028\u2029]+/g;
/** 엔티티를 몇 겹(&amp;#112; …)까지 풀어 볼지. 이보다 깊으면 안전을 확인할 수 없는 줄로 본다. */
const MAX_DECODE_ROUNDS = 8;
/** 풀어 봐도 끝나지 않는 줄 대신 복사하는 글. */
export const UNVERIFIED_LINE = `${REDACTED} (인코딩이 깊어 안전을 확인하지 못한 줄은 복사하지 않습니다)`;

function fromCode(code: number): string {
  return Number.isFinite(code) && code > 0 && code <= MAX_CODE_POINT ? String.fromCodePoint(code) : "�";
}

/**
 * 붙여 넣은 앱(HTML · Markdown)이 그릴 모양으로 한 겹 푼다: 숫자 · 이름 엔티티, Markdown 역슬래시 이스케이프, 링크 · 이미지는
 * 글만, HTML 주석 · 태그와 강조 표시는 지운다(#677 fixture 의 쪼갠 키). 가릴 게 있는지 보려는 모양이라 실제 그림보다 넓게
 * 지운다(`a*b` 같은 글자도 지움) — 지나치게 가리는 쪽이 놓치는 쪽보다 낫다.
 */
function renderOnce(text: string): string {
  return text
    .replace(ENTITY_RE, (m, dec?: string, hex?: string, name?: string) => {
      if (dec) return fromCode(Number.parseInt(dec, 10));
      if (hex) return fromCode(Number.parseInt(hex, 16));
      return (name && NAMED_ENTITIES[name]) ?? m;
    })
    .replace(MD_ESCAPE_RE, "$1")
    .replace(MD_LINK_RE, "$1")
    .replace(HTML_COMMENT_RE, "")
    .replace(HTML_TAG_RE, "")
    .replace(MD_MARK_RE, "");
}

/** 더 풀리지 않을 때까지 푼 글. MAX_DECODE_ROUNDS 안에 멈추지 않으면 null. */
function settled(text: string): string | null {
  let cur = text;
  for (let i = 0; i < MAX_DECODE_ROUNDS; i++) {
    const next = renderOnce(cur);
    if (next === cur) return cur;
    cur = next;
  }
  return null;
}

/**
 * 클립보드로 내보낼 글 가리기 (#709 이동찬 리뷰). maskSecrets 는 글자 그대로만 보므로 `&#112;assword=값` 처럼 키를 엔티티 ·
 * 역슬래시 · 서식 · 주석 · 이미지 대체 글로 숨기면 값을 놓치고, 붙여 넣은 앱이 그걸 풀면 비밀이 되살아난다(상세 보고 카드가
 * 파싱 뒤에 다시 가리는 것과 같은 모양, #677 fixture). 그래서 줄마다 붙여 넣은 곳이 그릴 모양까지
 * 풀어 다시 가려 보고, 풀린 모양에서 새로 가릴 게 나오면 그 줄은 푼 뒤 가린 글로 바꾼다. 아니면 원래 줄(가린 것)을 그대로
 * 둔다(증거 원문 모양 유지). 끝까지 풀리지 않는 줄은 복사하지 않는다.
 * 풀린 글에 줄바꿈이 생기면 키와 값이 갈린 채로 대조하지 않도록 이어 붙인 한 줄로 가려 본다(maskAcrossBreaks) — 가릴 게
 * 나오면 이어 붙인 채 가린 글을 내보내 붙여 넣은 곳에서 다시 갈라지지 않게 한다.
 */
export function maskForCopy(text: string): string {
  if (!text) return text;
  return text
    .split("\n")
    .map((line) => {
      const masked = maskSecrets(line);
      const shown = settled(masked);
      if (shown === null) return UNVERIFIED_LINE;
      return maskAcrossBreaks(shown) ?? masked;
    })
    .join("\n");
}

/**
 * 줄바꿈을 띄어쓰기로 이어 붙인 한 줄로 가려 본다. 가릴 게 있으면 이어 붙인 채 가린 글, 없으면 null.
 * 풀린 글(엔티티 줄바꿈)에서 키와 값이 다른 줄로 갈리면 한 줄 단위 가림이 못 알아봐서다(#709 이동찬 리뷰).
 */
function maskAcrossBreaks(text: string): string | null {
  const joined = text.replace(LINE_BREAK_RE, " ");
  const masked = maskSecrets(joined);
  return masked === joined ? null : masked;
}

/**
 * 블록(문단 · 제목 · 목록 칸 · 표 칸 · 목록 · 표 · 코드 상자 …). 블록 안에서 다음 블록을 만나기 전까지의 인라인 글(텍스트 · 서식 ·
 * 대체 글)은 한 덩어리로 모아 가린다 — 서식 · 엔티티 줄바꿈으로 갈린 키와 값도 같은 덩어리로 본다(예전 화면 가림 remarkMaskSecrets 의
 * 블록 규칙과 같다, #677 · #709 리뷰). 하위 목록이 있는 목록 칸도 그 앞 글을 모은 뒤 하위 목록을 따로 본다(#975 이동찬 P2).
 */
const BLOCK_TAGS = new Set([
  "P",
  "H1",
  "H2",
  "H3",
  "H4",
  "H5",
  "H6",
  "LI",
  "DT",
  "DD",
  "UL",
  "OL",
  "DL",
  "BLOCKQUOTE",
  "DIV",
  "SECTION",
  "TABLE",
  "THEAD",
  "TBODY",
  "TFOOT",
  "TR",
  "PRE",
  "HR",
]);
const TEXT_NODE = 3;

/** 인라인 글 한 덩어리: 줄바꿈을 이어 붙여 가릴 게 있으면 이어 붙인 채 가린 글, 없으면 줄 단위 가림(maskForCopy). */
function blockCopy(text: string): string {
  return maskForCopy(maskAcrossBreaks(text) ?? text);
}

function collectCopy(el: Element, out: string[]): void {
  let inline = "";
  const flush = () => {
    if (inline.trim()) out.push(blockCopy(inline));
    inline = "";
  };
  for (const node of Array.from(el.childNodes)) {
    if (node.nodeType === TEXT_NODE) {
      inline += node.textContent ?? "";
      continue;
    }
    if (!(node instanceof Element)) continue;
    const tag = node.tagName;
    if (!BLOCK_TAGS.has(tag)) {
      // 인라인 요소(강조 · 코드 · 링크 글 · 이미지 대체 글): 같은 덩어리에 이어 붙인다
      inline += node.textContent ?? "";
      continue;
    }
    flush();
    if (tag === "TR") {
      // 표 한 줄: 칸을 탭으로 갈라 한 줄로(칸 글이 서로 붙어 키를 못 알아보지 않게). 칸마다 이어 붙여 가린 뒤 다시 본다
      const cells = Array.from(node.children).map((c) => maskAcrossBreaks(c.textContent ?? "") ?? c.textContent ?? "");
      out.push(maskForCopy(cells.join("\t")));
    } else if (tag === "PRE") {
      // 코드 상자는 줄 모양을 지킨다(줄 단위 가림)
      out.push(maskForCopy(node.textContent ?? ""));
    } else {
      collectCopy(node, out);
    }
  }
  flush();
}

/**
 * 화면에 그려진 Markdown 상자(보고서 탭 · 상세 보고)를 클립보드용 글로 만들고 가린다(#975 이동찬 P2). 화면은 가리지 않으므로
 * 그려진 글을 그대로 복사하면 블록 안 줄바꿈 · 표 칸 경계로 갈린 값을 줄 단위 가림이 놓친다 — 블록 · 표 줄 단위로 모아 가린다.
 * innerText 에 기대지 않아 브라우저 · 시험 환경(jsdom)에서 같은 글이 나온다.
 */
export function renderedCopyText(root: Element): string {
  const out: string[] = [];
  collectCopy(root, out);
  return out.join("\n");
}
