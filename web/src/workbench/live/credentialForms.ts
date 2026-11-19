// 선언 칸 값을 서버(#705 e3contract.credentialForms)가 자격 검사에 대는 모양들로 푼다.
// 서버는 원문 말고도 이스케이프를 푼 모양에서 자격 형식이 드러나면 저장을 거부한다 — 화면이 원문만 보면
// `"Cookie: …"`·`/api/token%3D…` 같은 값을 통과시켰다가 서버에서 거절된다. 그래서 같은 규칙으로 푼다.
//  - JSON 본문 틀: 첫 JSON 값만 엄격하게 읽는다(Go json.Decoder.Decode — 뒤에 붙은 것은 보지 않음). 객체마다
//    `키=<값 JSON>`(Go json.Marshal: 키 정렬, < > & U+2028 U+2029 이스케이프, 수는 원문), 문자열 값이면 `키="값"`·`키: 값`·값,
//    수면 `키=수`, 그 밖은 안으로 들어간다. 배열의 문자열 원소는 값 그대로.
//  - form 본문 틀: Go url.ParseQuery — `;`·잘못된 %가 하나라도 있으면 푼 모양을 쓰지 않는다. 키·`키=값`·값.
//  - 경로 칸(.path·.path_template): Go url.PathUnescape 가 성공하고 달라지면 그 모양도.
// 값 원문은 검사에만 쓰고 어디에도 옮기지 않는다.

export type CredentialFormKind = "plain" | "path" | "json" | "form";

/** Go 가 JSON 수를 원문(json.Number)으로 들고 있으므로 수는 원문 문자열로 둔다. */
type GoJson = null | boolean | string | { num: string } | GoJson[] | { obj: Map<string, GoJson> };

class JsonError extends Error {}

/** Go json.Decoder 처럼 앞 공백 뒤 첫 JSON 값 하나를 엄격하게 읽는다. 뒤에 남은 글자는 보지 않는다. 실패하면 null. */
export function goDecodeFirstJson(text: string): GoJson | undefined {
  let i = 0;
  const ws = () => {
    while (i < text.length && " \t\r\n".includes(text[i])) i++;
  };
  const fail = (): never => {
    throw new JsonError();
  };
  const str = (): string => {
    i++; // "
    let out = "";
    for (;;) {
      if (i >= text.length) fail();
      const c = text.charCodeAt(i);
      if (c === 0x22) {
        i++;
        return out;
      }
      if (c < 0x20) fail();
      if (c !== 0x5c) {
        out += text[i++];
        continue;
      }
      const e = text[i + 1];
      i += 2;
      const simple: Record<string, string> = {
        '"': '"',
        "\\": "\\",
        "/": "/",
        b: "\b",
        f: "\f",
        n: "\n",
        r: "\r",
        t: "\t",
      };
      if (e !== undefined && Object.hasOwn(simple, e)) {
        out += simple[e];
        continue;
      }
      if (e !== "u") fail();
      const hex = (at: number) => {
        const h = text.slice(at, at + 4);
        if (!/^[0-9a-fA-F]{4}$/.test(h)) fail();
        return Number.parseInt(h, 16);
      };
      const u = hex(i);
      i += 4;
      // Go: 짝이 맞는 대리쌍만 합치고, 홀로 선 대리 코드는 U+FFFD 로 바꾼다
      if (u >= 0xd800 && u < 0xdc00 && text.slice(i, i + 2) === "\\u") {
        const lo = hex(i + 2);
        if (lo >= 0xdc00 && lo < 0xe000) {
          out += String.fromCharCode(u, lo);
          i += 6;
          continue;
        }
      }
      out += u >= 0xd800 && u < 0xe000 ? "�" : String.fromCharCode(u);
    }
  };
  const value = (): GoJson => {
    ws();
    const c = text[i];
    if (c === "{") {
      i++;
      const obj = new Map<string, GoJson>();
      ws();
      if (text[i] === "}") {
        i++;
        return { obj };
      }
      for (;;) {
        ws();
        if (text[i] !== '"') fail();
        const k = str();
        ws();
        if (text[i] !== ":") fail();
        i++;
        obj.set(k, value()); // 겹친 키는 뒤 값(Go map 과 같음)
        ws();
        if (text[i] === ",") {
          i++;
          continue;
        }
        if (text[i] === "}") {
          i++;
          return { obj };
        }
        fail();
      }
    }
    if (c === "[") {
      i++;
      const arr: GoJson[] = [];
      ws();
      if (text[i] === "]") {
        i++;
        return arr;
      }
      for (;;) {
        arr.push(value());
        ws();
        if (text[i] === ",") {
          i++;
          continue;
        }
        if (text[i] === "]") {
          i++;
          return arr;
        }
        fail();
      }
    }
    if (c === '"') return str();
    for (const [word, v] of [
      ["true", true],
      ["false", false],
      ["null", null],
    ] as const) {
      if (text.startsWith(word, i)) {
        i += word.length;
        return v;
      }
    }
    const m = /^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?/.exec(text.slice(i));
    if (!m) fail();
    i += (m as RegExpExecArray)[0].length;
    return { num: (m as RegExpExecArray)[0] };
  };
  try {
    return value();
  } catch (err) {
    if (err instanceof JsonError) return undefined;
    throw err;
  }
}

const isNum = (v: GoJson): v is { num: string } => typeof v === "object" && v !== null && "num" in v;
const isObj = (v: GoJson): v is { obj: Map<string, GoJson> } => typeof v === "object" && v !== null && "obj" in v;

/** 코드 포인트 순(= Go 의 UTF-8 바이트 순) 비교 */
function codePointOrder(a: string, b: string): number {
  const x = Array.from(a);
  const y = Array.from(b);
  for (let k = 0; k < Math.min(x.length, y.length); k++) {
    const d = (x[k].codePointAt(0) as number) - (y[k].codePointAt(0) as number);
    if (d) return d;
  }
  return x.length - y.length;
}

/** Go json.Marshal 의 문자열 — JSON.stringify 에 Go 의 HTML·줄 구분자 이스케이프를 더한다(\b·\f 는 Go 1.22+ 와 같음). */
function goJsonString(s: string): string {
  return JSON.stringify(s).replace(/[<>&\u2028\u2029]/g, (c) => `\\u${c.charCodeAt(0).toString(16).padStart(4, "0")}`);
}

/** Go json.Marshal(any) — 객체 키 정렬, 수는 원문. */
export function goMarshal(v: GoJson): string {
  if (v === null) return "null";
  if (typeof v === "boolean") return String(v);
  if (typeof v === "string") return goJsonString(v);
  if (isNum(v)) return v.num;
  if (Array.isArray(v)) return `[${v.map(goMarshal).join(",")}]`;
  const keys = [...v.obj.keys()].sort(codePointOrder);
  return `{${keys.map((k) => `${goJsonString(k)}:${goMarshal(v.obj.get(k) as GoJson)}`).join(",")}}`;
}

/** Go jsonCredentialForms 와 같은 모양들. */
function jsonForms(v: GoJson, out: string[]): void {
  if (Array.isArray(v)) {
    for (const child of v) {
      if (typeof child === "string") out.push(child);
      else jsonForms(child, out);
    }
    return;
  }
  if (!isObj(v)) return;
  for (const [k, child] of v.obj) {
    out.push(`${k}=${goMarshal(child)}`);
    if (typeof child === "string") {
      // strconv.Quote 대신 JSON 따옴표: 둘 다 `"…"` 한 덩어리라 따옴표 값 형식 판정이 같고, 이스케이프가 만드는 글자
      // (\ x u 0-9 a-f)로는 자격 키 이름이 생기지 않는다. 원문 값은 따로 c 로 본다.
      out.push(`${k}=${JSON.stringify(child)}`, `${k}: ${child}`, child);
    } else if (isNum(child)) out.push(`${k}=${child.num}`);
    else jsonForms(child, out);
  }
}

const HEX = /^[0-9a-fA-F]{2}$/;
const utf8 = new TextDecoder("utf-8");

/** Go url 의 퍼센트 풀기. query=true 면 '+' 를 공백으로(QueryUnescape), 아니면 그대로(PathUnescape). 잘못되면 null. */
function goUnescape(s: string, query: boolean): string | null {
  const bytes: number[] = [];
  const enc = new TextEncoder();
  for (let k = 0; k < s.length; ) {
    const c = s[k];
    if (c === "%") {
      const h = s.slice(k + 1, k + 3);
      if (!HEX.test(h)) return null;
      bytes.push(Number.parseInt(h, 16));
      k += 3;
    } else if (c === "+" && query) {
      bytes.push(0x20);
      k++;
    } else {
      const cp = s.codePointAt(k) as number;
      const ch = String.fromCodePoint(cp);
      bytes.push(...enc.encode(ch));
      k += ch.length;
    }
  }
  return utf8.decode(new Uint8Array(bytes));
}

/** Go url.ParseQuery. 하나라도 오류면 null(서버는 그때 푼 모양을 쓰지 않는다). */
function goParseQuery(q: string): [string, string][] | null {
  const out: [string, string][] = [];
  let bad = false;
  for (const piece of q.split("&")) {
    if (piece.includes(";")) {
      bad = true;
      continue;
    }
    if (piece === "") continue;
    const at = piece.indexOf("=");
    const k = goUnescape(at < 0 ? piece : piece.slice(0, at), true);
    const v = goUnescape(at < 0 ? "" : piece.slice(at + 1), true);
    if (k === null || v === null) {
      bad = true;
      continue;
    }
    out.push([k, v]);
  }
  return bad ? null : out;
}

/** 칸 값 → 서버가 자격 검사에 대는 모양들(첫 원소는 원문). */
export function credentialForms(value: string, kind: CredentialFormKind): string[] {
  const forms = [value];
  if (kind === "json") {
    const parsed = goDecodeFirstJson(value);
    if (parsed !== undefined) jsonForms(parsed, forms);
  } else if (kind === "form") {
    for (const [k, v] of goParseQuery(value) ?? []) forms.push(k, `${k}=${v}`, v);
  } else if (kind === "path") {
    const decoded = goUnescape(value, false);
    if (decoded !== null && decoded !== value) forms.push(decoded);
  }
  return forms;
}
