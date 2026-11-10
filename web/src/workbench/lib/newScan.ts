// 새 점검 폼의 순수 로직. 원본 ALLOW · parseUrl · isInternal · hostState · nSync(계산 부분) · nPrefill · nUrl blur.
// 화면(views/NewScan.tsx)은 폼 값(NewForm)과 소유 확인 결과(verified)로 nView() 를 매 렌더 계산한다
// — 원본이 input·change 마다 nSync() 로 다시 그리던 것과 같은 결과.

/** 소유 확인 없이 점검할 수 있는 외부 주소 (예시). 원본 ALLOW. */
export const ALLOW: readonly string[] = [
  "shop.example.com",
  "api.store-dev.com",
  "blog.example.com",
  "docs.example.com",
  "app.sample.io",
];

const SCHEME = /^[a-z][a-z0-9+.-]*:\/\//i;

/** 주소 해석: 비었거나 이상하면 null, 스킴이 없으면 https:// 를 붙여 본다. 원본 `parseUrl(v)`. */
export function parseUrl(raw: string): URL | null {
  let v = (raw || "").trim();
  if (!v) return null;
  if (!SCHEME.test(v)) v = `https://${v}`;
  try {
    const u = new URL(v);
    if (!u.hostname) return null;
    return u;
  } catch {
    return null;
  }
}

/** 내부 주소인가 (localhost · ::1 · 점 없는 이름 · 127/10/192.168/172.16–31). 원본 `isInternal(h)`. */
export function isInternal(h: string): boolean {
  if (h === "localhost" || h === "::1" || h.indexOf(".") < 0) return true;
  const m = h.match(/^(\d+)\.(\d+)\.\d+\.\d+$/);
  if (!m) return false;
  const a = +m[1];
  const b = +m[2];
  return a === 127 || a === 10 || (a === 192 && b === 168) || (a === 172 && b >= 16 && b <= 31);
}

/** 소유 확인(예시)을 통과한 hostname. 원본 VERIFIED. */
export type Verified = Readonly<Record<string, boolean>>;

/**
 * 주소 상태. 원본 `hostState()`.
 * bad 형식 오류·빈 값 · int 내부 주소 · allow 허용 목록 또는 소유 확인 통과 · ext 소유 확인 필요.
 */
export type HostState =
  | { readonly ok: false; readonly kind: "bad"; readonly host?: undefined; readonly url?: undefined }
  | { readonly ok: true; readonly kind: "int" | "allow"; readonly host: string; readonly url: URL }
  | { readonly ok: false; readonly kind: "ext"; readonly host: string; readonly url: URL };

export function hostState(value: string, verified: Verified): HostState {
  const u = parseUrl(value);
  if (!u) return { ok: false, kind: "bad" };
  const h = u.host;
  if (isInternal(u.hostname)) return { ok: true, kind: "int", host: h, url: u };
  if (ALLOW.indexOf(u.hostname) >= 0 || verified[u.hostname]) return { ok: true, kind: "allow", host: h, url: u };
  return { ok: false, kind: "ext", host: h, url: u };
}

/** DNS TXT 소유 확인 토큰 (예시). 원본 tokenFor[h] 를 만들던 식. 렌더 중이 아니라 effect 에서 부를 것. */
export function newToken(rand: () => number = Math.random): string {
  return `abc2lab-verify=${rand().toString(16).slice(2, 10)}${rand().toString(16).slice(2, 8)}`;
}

export type Auth = "in" | "pub";
export type How = "form" | "sess";
export type Llm = "local" | "api";
export type Send = "ap" | "full";
export type Depth = "표준" | "심화" | "제한 없음";

export const DEPTHS: readonly Depth[] = ["표준", "심화", "제한 없음"];
/** 점검 목표 체크 상자 (화면 순서). */
export const GOALS: readonly string[] = ["주문·결제 권한 경계", "관리자 기능 접근", "비로그인 노출"];
export const MODELS: readonly string[] = ["qwen/qwen3.6-35b-a3b", "qwen2.5:32b", "llama3.1:70b", "qwen2.5:7b"];
export const PROVIDERS: readonly string[] = ["OpenAI", "Anthropic", "OpenAI 호환"];
export const WORKERS: readonly string[] = ["1", "2", "3", "4", "6", "8"];

/** 새 점검 폼 값 (원본 #nForm 의 입력들 + nameTouched). */
export interface NewForm {
  readonly url: string;
  /** 작업 이름을 사람이 고쳤는가. 고치기 전에는 주소에 맞춰 'host 점검' 으로 채운다 */
  readonly nameTouched: boolean;
  readonly name: string;
  readonly auth: Auth;
  readonly how: How;
  readonly a1: string;
  readonly a2: string;
  readonly b1: string;
  readonly b2: string;
  readonly sa: string;
  readonly sb: string;
  readonly llm: Llm;
  readonly model: string;
  readonly srv: string;
  readonly prov: string;
  /** GOALS 와 같은 순서의 체크 여부 */
  readonly goals: readonly boolean[];
  readonly send: Send;
  /** 전체 허용일 때 '리셋할 수 있는 시험환경' 확인 */
  readonly reset: boolean;
  readonly depth: Depth;
  readonly workers: string;
  readonly excl: string;
}

/** 원본 HTML 의 처음 값. */
export function initialForm(): NewForm {
  return {
    url: "https://shop.example.com",
    nameTouched: false,
    name: "shop.example.com 점검",
    auth: "in",
    how: "form",
    a1: "tester_a",
    a2: "",
    b1: "tester_b",
    b2: "",
    sa: "",
    sb: "",
    llm: "local",
    model: MODELS[0],
    srv: "http://ollama:11434",
    prov: PROVIDERS[0],
    goals: [true, false, false],
    send: "ap",
    reset: false,
    depth: "표준",
    workers: "3",
    excl: "",
  };
}

/** nSync 의 이름 채우기: 이름을 안 고쳤고 주소가 맞으면 'hostname 점검'. 바뀐 게 없으면 같은 객체. */
export function syncName(f: NewForm): NewForm {
  if (f.nameTouched) return f;
  const u = parseUrl(f.url);
  if (!u) return f;
  const name = `${u.hostname} 점검`;
  return name === f.name ? f : { ...f, name };
}

/** 원본 `nPrefill(u)`: 주소를 바꾸고 이름을 다시 주소에 맞춘다. */
export function prefillForm(f: NewForm, url: string): NewForm {
  return syncName({ ...f, url, nameTouched: false });
}

/** 원본 nUrl blur: 스킴 없이 적은 주소가 맞으면 origin 으로 바꾼다. 바꿀 게 없으면 null. */
export function normalizeUrl(v: string): string | null {
  const u = parseUrl(v);
  if (u && !SCHEME.test(v.trim())) return u.origin;
  return null;
}

/** 주소 아래 한 줄 (원본 #nHost). err = 오류 글 · pill = 알약 + 도움말. */
export type HostLine =
  | { readonly kind: "err"; readonly text: string }
  | { readonly kind: "pill"; readonly cls: "ok" | "wait"; readonly pill: string };

/** nSync 가 그리던 값 전부. */
export interface NView {
  readonly st: HostState;
  /** 주소 칸 aria-invalid (형식이 틀렸고 비어 있지 않음) */
  readonly urlInvalid: boolean;
  readonly hostLine: HostLine;
  /** 작업 이름이 있음 (없으면 오류 글 · aria-invalid) */
  readonly nameOk: boolean;
  /** 오른쪽 '점검 요약' (sTarget · sAuth · sLlm · sDepth · sGoal · sSend) */
  readonly sum: {
    readonly target: string;
    readonly auth: string;
    readonly llm: string;
    readonly depth: string;
    readonly goal: string;
    readonly send: string;
  };
  /** 안전 정책 작은 글 (pTarget · pSend · pExcl) */
  readonly pol: { readonly target: string; readonly send: string; readonly excl: string };
  /** 시작할 수 없는 까닭 (비면 시작 가능). 화면에는 ' · ' 로 잇는다 */
  readonly why: readonly string[];
}

/** 원본 `nSync()` 의 계산. */
export function nView(f: NewForm, verified: Verified): NView {
  const st = hostState(f.url, verified);
  const filled = !!f.url.trim();
  const ver = !!(st.url && verified[st.url.hostname]);

  let hostLine: HostLine;
  if (st.kind === "bad") {
    hostLine = {
      kind: "err",
      text: filled ? "주소 형식을 확인하세요. 예: https://shop.example.com" : "점검할 사이트 주소를 입력하세요.",
    };
  } else if (st.kind === "int") {
    hostLine = { kind: "pill", cls: "ok", pill: "내부 주소" };
  } else if (st.kind === "allow") {
    hostLine = { kind: "pill", cls: "ok", pill: ver ? "허용 · 예시 통과(시안)" : "허용 목록 · 예시" };
  } else {
    hostLine = { kind: "pill", cls: "wait", pill: "소유 확인 필요" };
  }

  const name = f.name.trim();
  const goals = GOALS.filter((_, i) => f.goals[i]);

  let pTarget = "주소 없음";
  if (st.url) {
    let tail = " · 소유 확인 필요";
    if (st.kind === "int") tail = " · 내부 주소";
    else if (st.kind === "allow") tail = ver ? " · 예시 통과(시안)" : " · 허용 목록(예시)";
    pTarget = st.url.hostname + tail;
  }
  const extra = f.excl
    .split("\n")
    .map((x) => x.trim())
    .filter(Boolean).length;

  const why: string[] = [];
  if (!st.url) why.push("주소를 입력하세요");
  else if (!st.ok) why.push("외부 주소의 소유를 먼저 확인하세요");
  if (!name) why.push("작업 이름을 입력하세요");
  if (f.send === "full" && !f.reset) why.push("전체 허용은 시험환경 확인이 필요합니다");

  return {
    st,
    urlInvalid: st.kind === "bad" && filled,
    hostLine,
    nameOk: !!name,
    sum: {
      target: st.url ? st.url.origin : "—",
      auth:
        f.auth === "in"
          ? `로그인 영역 포함 · 계정 2개 (${f.how === "form" ? "아이디·비밀번호" : "세션 직접 입력"})`
          : "공개 영역만",
      llm: f.llm === "local" ? `로컬 · ${f.model}` : `외부 API · ${f.prov}`,
      depth: `${f.depth} · 워커 ${f.workers}`,
      goal: goals.length ? goals.join(", ") : "기본 목표",
      send: f.send === "ap" ? "승인 후 전송" : "전체 허용 · 시험환경 전용",
    },
    pol: {
      target: pTarget,
      send: f.send === "ap" ? "승인한 요청만 보냄" : "승인 없이 보냄 · 시험환경 확인 필요",
      excl: `전역 3개 (/admin/delete* 등)${extra ? ` + 이 점검 ${extra}개` : ""}`,
    },
    why,
  };
}
