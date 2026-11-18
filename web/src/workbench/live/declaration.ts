// 정책 선언(E③ 제품 확정 경로) seam — 운영자가 대상 하나에 자원 종류별 접근 정책을 선언한다.
// 레코드 형식 정본: e3contract/declaration.go(`e3-product-declaration/v1`), 계약 docs/contracts/e3-product-confirmation-interface.md §3
//   (#698). 결정 배경: docs/e3-confirm-path-decision.md §4.1·§4.2(#596).
// ※ 화면 검사(validateDraft)는 Go Declaration.Validate 와 같은 규칙을 미리 보여 주는 것뿐이다. 저장 허용은 서버가 정한다.
// ※ 감사 칸(declared_by·declared_at)과 대상 결속 칸(target_ref·target_approval_revision)은 서버가 채운다 — 화면은 보내지 않는다.
// ※ 절대규칙 2: 본문 틀(body_template)에 자격(비밀번호·토큰·쿠키·세션·API 키) 이름이나 값 모양이 있으면 저장하지 않는다.
//   선언은 모델 입력·내보내기·공유에 넣지 않는다.
// ※ 저장 API(최민준, 정책 선언 저장·동결 PR)는 아직 없다. 경로·응답 모양은 #698 코멘트로 질문한 제안안을 가정한다.
//   TODO(이경준): 저장·동결 PR 이 확정되면 DECLARATION_PATH·parseDeclarationResponse·오류 code 를 맞춘다.
//   표현/데이터 분리 — 규칙·파싱은 전부 여기, 화면은 그리기만.

import { ApiError, apiFetch } from "@/lib/diagnosis/api";

/** 정책 선언 레코드 형식 판(e3contract.DeclarationVersion). */
export const DECLARATION_VERSION = "e3-product-declaration/v1";
/** 조회 경로 틀의 객체 ID 자리(e3contract.IDPlaceholder). 한 경로에 정확히 한 번. */
export const ID_PLACEHOLDER = "{id}";

export type Sharing = "shared" | "owner_only";
export type BodyKind = "json" | "form";
export type CreateMethod = "POST" | "PUT";

export const SHARING_LABEL: Record<Sharing, string> = {
  owner_only: "소유자 전용",
  shared: "공유",
};
export const BODY_KIND_LABEL: Record<BodyKind, string> = { json: "JSON", form: "form (urlencoded)" };
export const CREATE_METHODS: readonly CreateMethod[] = ["POST", "PUT"];

/** 시험 객체 생성 요청(e3contract.CreateSpec). 표식은 프로그램이 markerField 에 넣는다. */
export type CreateSpec = {
  path: string;
  method: CreateMethod;
  bodyKind: BodyKind;
  bodyTemplate: string;
  markerField: string;
  idPath: string;
};
/** 객체 조회 경로 틀과 응답 모양(e3contract.ReadSpec). statusPath·normalValue 는 둘 다 쓰거나 둘 다 비운다. */
export type ReadSpec = { pathTemplate: string; dataPath: string; statusPath: string; normalValue: string };
/** 신원 확인 GET(e3contract.IdentitySpec). 대상마다 하나. */
export type IdentitySpec = { path: string; subjectIdPath: string };
/** 자원 종류 하나(e3contract.TypeDeclaration). shared 면 create·read·ownerOnlyFields 를 쓰지 않는다. */
export type TypeDraft = {
  type: string;
  sharing: Sharing;
  ownerOnlyFields: string[];
  create: CreateSpec;
  read: ReadSpec;
};
/** 화면에서 고치는 선언. 서버가 채우는 칸(대상·판·감사)은 없다. */
export type DeclarationDraft = { identity: IdentitySpec; types: TypeDraft[] };

/** 서버에 저장된 선언(조회 결과). 감사·대상 결속 칸은 서버 값 그대로 보인다. */
export type StoredDeclaration = DeclarationDraft & {
  targetRef: string;
  targetApprovalRevision: string;
  declaredBy: string;
  declaredAt: string;
};

export const emptyCreate = (): CreateSpec => ({
  path: "",
  method: "POST",
  bodyKind: "json",
  bodyTemplate: "{}",
  markerField: "",
  idPath: "",
});
export const emptyRead = (): ReadSpec => ({ pathTemplate: "", dataPath: ".", statusPath: "", normalValue: "" });
export const emptyType = (): TypeDraft => ({
  type: "",
  sharing: "owner_only",
  ownerOnlyFields: [],
  create: emptyCreate(),
  read: emptyRead(),
});
export const emptyDraft = (): DeclarationDraft => ({ identity: { path: "", subjectIdPath: "" }, types: [emptyType()] });

// ── 규칙(e3contract/declaration.go·policy.go 와 같은 정규식) ─────────────────────────────────────────────
const TYPE_PATTERN = /^[a-z][a-z0-9_]{0,63}$/;
const FIELD_PATH_PATTERN = /^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$/;
const REF_PATTERN = /^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}$/;
const ACTOR_PATTERN = /^[A-Za-z0-9][A-Za-z0-9_.:@-]{0,127}$/;
/** 경로 바이트 상한(validPathShape 의 len(p) — UTF-8 바이트). */
const MAX_PATH_BYTES = 512;

const utf8Length = (v: string): number => new TextEncoder().encode(v).length;

/** e3contract validPathShape: 절대 경로, 512바이트 이하, query·조각·공백·역슬래시·점 조각·제어 문자 없음. */
function validPathShape(p: string): boolean {
  if (!p.startsWith("/") || utf8Length(p) > MAX_PATH_BYTES || /[?#\s\\]/.test(p)) return false;
  if (
    p
      .slice(1)
      .split("/")
      .some((seg) => seg === "." || seg === "..")
  )
    return false;
  for (const ch of p) {
    const code = ch.codePointAt(0) ?? 0;
    if (code < 0x21 || code === 0x7f) return false;
  }
  return true;
}
/** {id} 없는 절대 경로(plainPath). */
export const isPlainPath = (p: string): boolean => validPathShape(p) && !/[{}]/.test(p);
/** {id} 조각이 정확히 하나인 절대 경로(readTemplate). 다른 중괄호는 받지 않는다. */
export function isReadTemplate(p: string): boolean {
  if (!validPathShape(p)) return false;
  let ids = 0;
  for (const seg of p.slice(1).split("/")) {
    if (seg === ID_PLACEHOLDER) ids++;
    else if (/[{}]/.test(seg)) return false;
  }
  return ids === 1;
}
export const isFieldPath = (v: string): boolean => FIELD_PATH_PATTERN.test(v);
export const isTypeName = (v: string): boolean => TYPE_PATTERN.test(v);

/** 확정 단계가 이 종류를 시험하는가(TypeDeclaration.Confirmable): 소유자 전용이고 표식 칸이 소유자 전용 필드일 때만. */
export function confirmable(t: TypeDraft): boolean {
  return t.sharing === "owner_only" && t.create.markerField !== "" && t.ownerOnlyFields.includes(t.create.markerField);
}

// ── 본문 틀 해석 ─────────────────────────────────────────────────────────────────────────────────────
const isObj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);

/** JSON 본문 틀 → 맨 위 객체. 객체가 아니면(null·배열·값·깨짐) null. */
function parseJsonTemplate(raw: string): Record<string, unknown> | null {
  try {
    const v: unknown = JSON.parse(raw);
    return isObj(v) ? v : null;
  } catch {
    return null;
  }
}

/** percent 조각 하나를 푼다(Go url.QueryUnescape 와 같이 '+'는 공백, 잘못된 %는 오류). 오류면 null. */
function queryUnescape(s: string): string | null {
  if (/%(?![0-9A-Fa-f]{2})/.test(s)) return null;
  const bytes: number[] = [];
  const raw = new TextEncoder().encode(s.replace(/\+/g, " "));
  for (let i = 0; i < raw.length; i++) {
    if (raw[i] === 0x25) {
      bytes.push(Number.parseInt(String.fromCharCode(raw[i + 1], raw[i + 2]), 16));
      i += 2;
    } else bytes.push(raw[i]);
  }
  // Go 는 UTF-8 을 검사하지 않으므로 깨진 바이트도 오류로 보지 않는다(대체 문자로 읽기만)
  return new TextDecoder().decode(new Uint8Array(bytes));
}

/**
 * form 본문 틀 → [이름, 값] 목록. Go url.ParseQuery 와 같이 '&'로 나누고, ';'가 든 조각·잘못된 % 는 오류(null).
 */
function parseFormTemplate(raw: string): [string, string][] | null {
  const out: [string, string][] = [];
  for (const piece of raw.split("&")) {
    if (piece === "") continue;
    if (piece.includes(";")) return null;
    const eq = piece.indexOf("=");
    const k = queryUnescape(eq < 0 ? piece : piece.slice(0, eq));
    const v = queryUnescape(eq < 0 ? "" : piece.slice(eq + 1));
    if (k === null || v === null) return null;
    out.push([k, v]);
  }
  return out;
}

// ── 자격 차단(절대규칙 2) ──────────────────────────────────────────────────────────────────────────────
/** 자격을 뜻하는 이름 조각(소문자·영숫자만 남긴 키에 들어 있으면 자격 칸으로 본다). */
const SECRET_NAME_PARTS = [
  "password",
  "passwd",
  "passphrase",
  "token",
  "secret",
  "authorization",
  "cookie",
  "session",
  "apikey",
  "credential",
  "jwt",
  "bearer",
  "privatekey",
];
/** 단어 하나로만 쓰일 때 자격으로 보는 짧은 이름(부분 일치하면 author·passenger 같은 정상 칸까지 막는다). */
const SECRET_NAME_WORDS = new Set(["pwd", "pass", "auth", "sid", "otp"]);

/** 키 이름이 자격 칸으로 보이는가. camelCase·_·-·. 경계로 단어를 나눠 짧은 이름은 단어 일치, 긴 이름은 부분 일치. */
export function looksLikeSecretName(key: string): boolean {
  const flat = key.toLowerCase().replace(/[^a-z0-9]/g, "");
  if (SECRET_NAME_PARTS.some((p) => flat.includes(p))) return true;
  const words = key
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
    .toLowerCase()
    .split(/[^a-z0-9]+/)
    .filter(Boolean);
  return words.some((w) => SECRET_NAME_WORDS.has(w));
}
/** 값이 자격 모양인가(Bearer·Basic 헤더 값, JWT). 값 원문은 어디에도 옮기지 않고 참·거짓만 본다. */
export function looksLikeSecretValue(v: string): boolean {
  return /^\s*(bearer|basic)\s+\S/i.test(v) || /eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\./.test(v);
}

/** JSON 틀의 모든 키·문자열 값을 훑어 자격으로 보이는 키 이름을 모은다(값은 이름 대신 위치만). */
function secretsInJson(v: unknown, at: string, out: string[]): void {
  if (Array.isArray(v)) {
    v.forEach((x, i) => {
      secretsInJson(x, `${at}[${i}]`, out);
    });
    return;
  }
  if (isObj(v)) {
    for (const [k, x] of Object.entries(v)) {
      const here = at ? `${at}.${k}` : k;
      if (looksLikeSecretName(k)) out.push(here);
      else secretsInJson(x, here, out);
    }
    return;
  }
  if (typeof v === "string" && looksLikeSecretValue(v)) out.push(at || "(값)");
}

/** 본문 틀에서 자격으로 보이는 칸(이름·위치). 값 원문은 돌려주지 않는다. */
export function secretFieldsInTemplate(kind: BodyKind, raw: string): string[] {
  const out: string[] = [];
  if (kind === "json") {
    const obj = parseJsonTemplate(raw);
    if (obj) secretsInJson(obj, "", out);
    return out;
  }
  for (const [k, v] of parseFormTemplate(raw) ?? []) {
    if (looksLikeSecretName(k) || looksLikeSecretValue(v)) out.push(k);
  }
  return out;
}

// ── 검사 ─────────────────────────────────────────────────────────────────────────────────────────────
/** 칸 하나의 문제. field 는 `identity.path`·`types.0.create.path` 같은 위치, message 는 화면 문구. */
export type DraftIssue = { field: string; message: string };

function validateType(t: TypeDraft, at: string, issues: DraftIssue[]): void {
  const bad = (field: string, message: string) => issues.push({ field: `${at}.${field}`, message });
  if (!isTypeName(t.type)) bad("type", "자원 종류 이름은 영문 소문자로 시작하고 소문자·숫자·_ 만 씁니다(64자 이하).");
  if (t.sharing !== "shared" && t.sharing !== "owner_only") bad("sharing", "공유 방식을 고르세요.");
  if (t.sharing === "shared") return; // 공유 종류는 시험하지 않아 요청 칸을 보내지 않는다(normalizeDraft 가 비움)

  const fields = new Set<string>();
  for (const f of t.ownerOnlyFields) {
    if (!isFieldPath(f)) bad("ownerOnlyFields", `소유자 전용 필드 이름이 형식 밖입니다: ${f || "(빈 값)"}`);
    else if (fields.has(f)) bad("ownerOnlyFields", `소유자 전용 필드가 겹칩니다: ${f}`);
    fields.add(f);
  }
  if (t.ownerOnlyFields.length === 0) bad("ownerOnlyFields", "소유자 전용 필드를 하나 이상 적으세요.");
  if (!confirmable(t)) bad("create.markerField", "표식 칸은 소유자 전용 필드 중 하나여야 합니다.");

  const c = t.create;
  if (!isPlainPath(c.path))
    bad("create.path", "생성 경로는 {id} 없는 절대 경로입니다(query · # · 공백 · 점 조각 불가).");
  if (c.method !== "POST" && c.method !== "PUT") bad("create.method", "생성 메서드는 POST 또는 PUT 입니다.");
  if (!isFieldPath(c.idPath)) bad("create.idPath", "생성 응답의 객체 ID 위치(JSON 경로)를 적으세요.");
  if (c.bodyKind === "json") {
    const obj = parseJsonTemplate(c.bodyTemplate);
    if (!obj) bad("create.bodyTemplate", "JSON 본문 틀은 JSON 객체여야 합니다.");
    // 표식 칸이 틀에 이미 있으면 덮어쓰는지 남기는지 모호하다. 맨 위 칸만 본다(중첩 칸은 프로그램이 만든다 — Go 와 같음)
    else if (!c.markerField.includes(".") && Object.hasOwn(obj, c.markerField))
      bad("create.bodyTemplate", "본문 틀에 표식 칸이 이미 있습니다. 표식 칸은 비워 두세요(프로그램이 넣습니다).");
  } else if (c.bodyKind === "form") {
    const pairs = parseFormTemplate(c.bodyTemplate);
    if (!pairs || c.markerField.includes("."))
      bad("create.bodyTemplate", "form 본문 틀이 형식 밖이거나 표식 칸에 점(.)이 있습니다.");
    else if (pairs.some(([k]) => k === c.markerField))
      bad("create.bodyTemplate", "본문 틀에 표식 칸이 이미 있습니다. 표식 칸은 비워 두세요(프로그램이 넣습니다).");
  } else bad("create.bodyKind", "본문 모양은 JSON 또는 form 입니다.");
  const secrets = secretFieldsInTemplate(c.bodyKind, c.bodyTemplate);
  if (secrets.length)
    bad(
      "create.bodyTemplate",
      `본문 틀에 자격으로 보이는 칸이 있어 저장하지 않습니다(${secrets.join(", ")}). 세션은 시험 계정 로그인이 따로 붙입니다.`,
    );

  const r = t.read;
  if (!isReadTemplate(r.pathTemplate))
    bad("read.pathTemplate", "조회 경로 틀은 {id} 조각이 정확히 하나인 절대 경로입니다(예: /api/things/{id}).");
  if (r.dataPath !== "." && !isFieldPath(r.dataPath))
    bad("read.dataPath", "응답 속 객체 데이터 위치를 적으세요(본문 전체면 .).");
  if ((r.statusPath === "") !== (r.normalValue === ""))
    bad("read.statusPath", "상태 칸과 정상 값은 둘 다 적거나 둘 다 비웁니다.");
  else if (r.statusPath !== "" && !isFieldPath(r.statusPath)) bad("read.statusPath", "상태 칸 위치가 형식 밖입니다.");
}

/** 선언 검사(Go Declaration.Validate 의 화면 사본, 서버가 채우는 칸 제외). 문제가 없으면 빈 배열. */
export function validateDraft(d: DeclarationDraft): DraftIssue[] {
  const issues: DraftIssue[] = [];
  if (!isPlainPath(d.identity.path))
    issues.push({
      field: "identity.path",
      message: "신원 확인 경로는 {id} 없는 절대 GET 경로입니다(예: 내 정보 조회).",
    });
  if (!isFieldPath(d.identity.subjectIdPath))
    issues.push({ field: "identity.subjectIdPath", message: "응답 속 사용자 식별자 위치(JSON 경로)를 적으세요." });
  if (d.types.length === 0) issues.push({ field: "types", message: "자원 종류를 하나 이상 선언하세요." });
  const names = new Set<string>();
  d.types.forEach((t, i) => {
    if (t.type && names.has(t.type))
      issues.push({ field: `types.${i}.type`, message: `자원 종류가 겹칩니다: ${t.type}` });
    names.add(t.type);
    validateType(t, `types.${i}`, issues);
  });
  return issues;
}

/** 정본 순서로 맞춘다: 종류·소유자 전용 필드 오름차순(Go 는 순서가 어긋나면 거부). 공유 종류의 요청 칸은 비운다. */
export function normalizeDraft(d: DeclarationDraft): DeclarationDraft {
  const types = d.types
    .map(
      (t): TypeDraft =>
        t.sharing === "shared"
          ? { type: t.type, sharing: "shared", ownerOnlyFields: [], create: emptyCreate(), read: emptyRead() }
          : { ...t, ownerOnlyFields: [...t.ownerOnlyFields].sort(byteOrder) },
    )
    .sort((a, b) => byteOrder(a.type, b.type));
  return { identity: { ...d.identity }, types };
}
// 규칙상 ASCII 이름만 통과하므로 UTF-16 비교가 Go 의 바이트 비교와 같다
function byteOrder(a: string, b: string): number {
  if (a === b) return 0;
  return a < b ? -1 : 1;
}

/** 저장 요청 본문(서버 칸 이름). 감사·대상 결속 칸은 없다. expected_target_revision 은 화면이 본 대상 판(#698 질문 2). */
export function toRequestBody(d: DeclarationDraft, expectedTargetRevision: number): Record<string, unknown> {
  const n = normalizeDraft(d);
  return {
    expected_target_revision: expectedTargetRevision,
    version: DECLARATION_VERSION,
    identity: { path: n.identity.path, subject_id_path: n.identity.subjectIdPath },
    types: n.types.map((t) =>
      t.sharing === "shared"
        ? { type: t.type, sharing: "shared" }
        : {
            type: t.type,
            sharing: t.sharing,
            owner_only_fields: t.ownerOnlyFields,
            create: {
              path: t.create.path,
              method: t.create.method,
              body_kind: t.create.bodyKind,
              body_template: t.create.bodyTemplate,
              marker_field: t.create.markerField,
              id_path: t.create.idPath,
            },
            read: {
              path_template: t.read.pathTemplate,
              data_path: t.read.dataPath,
              ...(t.read.statusPath ? { status_path: t.read.statusPath, normal_value: t.read.normalValue } : {}),
            },
          },
    ),
  };
}

// ── 서버 응답 파싱(닫힌 파서) ───────────────────────────────────────────────────────────────────────────
class ShapeError extends Error {}
const fail = (): never => {
  throw new ShapeError("declaration_shape");
};
/** 모르는 키가 있으면 거부(계약 밖 칸을 조용히 버리지 않는다). */
function exact(o: unknown, required: readonly string[], optional: readonly string[] = []): Record<string, unknown> {
  if (!isObj(o)) return fail();
  const allowed = new Set([...required, ...optional]);
  if (Object.keys(o).some((k) => !allowed.has(k)) || required.some((k) => !(k in o))) fail();
  return o;
}
const str = (v: unknown): string => (typeof v === "string" ? v : fail());
const optStr = (v: unknown): string => (v === undefined ? "" : str(v));

const CREATE_KEYS = ["path", "method", "body_kind", "body_template", "marker_field", "id_path"] as const;
const READ_KEYS = ["path_template", "data_path"] as const;

/** Go 는 공유 종류의 빈 create·read 도 칸째 낸다(omitempty 없음) — 없거나 전부 빈 값만 받는다. */
function emptyRequestObject(v: unknown, keys: readonly string[], optional: readonly string[] = []): boolean {
  if (v === undefined) return true;
  const o = exact(v, keys, optional);
  return Object.values(o).every((x) => x === "");
}

function parseType(v: unknown): TypeDraft {
  const o = exact(v, ["type", "sharing"], ["owner_only_fields", "create", "read"]);
  const sharing = o.sharing === "shared" || o.sharing === "owner_only" ? o.sharing : fail();
  const fieldsRaw = o.owner_only_fields;
  // Go 정본은 빈 목록을 null 로 낸다 — 없음·null 은 빈 목록, 그 밖의 배열 아닌 값은 계약 밖
  let ownerOnlyFields: string[] = [];
  if (fieldsRaw !== undefined && fieldsRaw !== null)
    ownerOnlyFields = Array.isArray(fieldsRaw) ? fieldsRaw.map(str) : fail();
  if (sharing === "shared") {
    if (
      ownerOnlyFields.length ||
      !emptyRequestObject(o.create, CREATE_KEYS) ||
      !emptyRequestObject(o.read, READ_KEYS, ["status_path", "normal_value"])
    )
      fail();
    return { type: str(o.type), sharing, ownerOnlyFields: [], create: emptyCreate(), read: emptyRead() };
  }
  const c = exact(o.create, CREATE_KEYS);
  const r = exact(o.read, READ_KEYS, ["status_path", "normal_value"]);
  const method = c.method === "POST" || c.method === "PUT" ? c.method : fail();
  const bodyKind = c.body_kind === "json" || c.body_kind === "form" ? c.body_kind : fail();
  return {
    type: str(o.type),
    sharing,
    ownerOnlyFields,
    create: {
      path: str(c.path),
      method,
      bodyKind,
      bodyTemplate: str(c.body_template),
      markerField: str(c.marker_field),
      idPath: str(c.id_path),
    },
    read: {
      pathTemplate: str(r.path_template),
      dataPath: str(r.data_path),
      statusPath: optStr(r.status_path),
      normalValue: optStr(r.normal_value),
    },
  };
}

/** 저장된 선언 레코드(e3contract.Declaration JSON) → 화면 값. 모양·규칙이 어긋나면 ShapeError. */
export function parseStoredDeclaration(v: unknown): StoredDeclaration {
  const o = exact(v, [
    "version",
    "target_ref",
    "target_approval_revision",
    "declared_by",
    "declared_at",
    "identity",
    "types",
  ]);
  if (o.version !== DECLARATION_VERSION) fail();
  const id = exact(o.identity, ["path", "subject_id_path"]);
  const types = Array.isArray(o.types) ? o.types.map(parseType) : fail();
  const stored: StoredDeclaration = {
    targetRef: str(o.target_ref),
    targetApprovalRevision: str(o.target_approval_revision),
    declaredBy: str(o.declared_by),
    declaredAt: str(o.declared_at),
    identity: { path: str(id.path), subjectIdPath: str(id.subject_id_path) },
    types,
  };
  if (
    !REF_PATTERN.test(stored.targetRef) ||
    !REF_PATTERN.test(stored.targetApprovalRevision) ||
    !ACTOR_PATTERN.test(stored.declaredBy) ||
    !Number.isFinite(Date.parse(stored.declaredAt))
  )
    fail();
  // 저장본도 같은 규칙을 지켜야 한다(정렬 포함) — 어긋나면 화면이 고쳐 보이지 않고 계약 밖으로 본다
  if (validateDraft(stored).length) fail();
  const n = normalizeDraft(stored);
  if (
    n.types.some(
      (t, i) =>
        t.type !== stored.types[i].type || t.ownerOnlyFields.join("\n") !== stored.types[i].ownerOnlyFields.join("\n"),
    )
  )
    fail();
  return stored;
}

/** 조회 결과. 미조회·실패를 '선언 없음'으로 바꾸지 않는다. */
export type DeclarationLoad =
  | { state: "none" }
  | { state: "current"; declaration: StoredDeclaration }
  | { state: "invalidated"; declaration: StoredDeclaration | null; reason: string }
  | { state: "unavailable" }
  | { state: "invalid" };

/** 무효 사유 문구. 모르는 사유는 '사유 불명'(닫힌 목록). */
export const INVALIDATED_REASON_LABEL: Record<string, string> = {
  target_reapproved: "대상을 다시 승인해서 이전 선언이 무효가 됐습니다",
};
export const invalidatedReasonText = (reason: string): string => INVALIDATED_REASON_LABEL[reason] ?? "사유 불명";

/**
 * 조회 응답 → 결과. 가정한 모양(#698 질문 3·4): {"state":"none"} | {"state":"current","declaration":{…}} |
 * {"state":"invalidated","reason":"target_reapproved","declaration":{…}|null}. 밖이면 invalid.
 */
export function parseDeclarationResponse(res: unknown): DeclarationLoad {
  try {
    if (!isObj(res)) return { state: "invalid" };
    if (res.state === "none") {
      exact(res, ["state"]);
      return { state: "none" };
    }
    if (res.state === "current") {
      const o = exact(res, ["state", "declaration"]);
      return { state: "current", declaration: parseStoredDeclaration(o.declaration) };
    }
    if (res.state === "invalidated") {
      const o = exact(res, ["state", "reason"], ["declaration"]);
      const reason = str(o.reason);
      const declaration =
        o.declaration === undefined || o.declaration === null ? null : parseStoredDeclaration(o.declaration);
      return { state: "invalidated", declaration, reason };
    }
    return { state: "invalid" };
  } catch (e) {
    if (e instanceof ShapeError) return { state: "invalid" };
    throw e;
  }
}

// ── 대상 ─────────────────────────────────────────────────────────────────────────────────────────────
/** 선언할 대상. revision 은 저장 때 기대값으로 보낸다. 승인 경로 접두·메서드는 참고 경고에만 쓴다(허용 판단은 E②). */
export type DeclarationTarget = {
  id: string;
  origin: string;
  state: string;
  revision: number;
  pathPrefixes: string[];
  methods: string[];
};

const names = (v: unknown): string[] => (Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : []);

/** 승인된 대상 목록. 실 라우트 GET /api/product/targets. 형식이 깨진 행·승인 아닌 대상은 버린다(fail-closed). */
export async function getDeclarationTargets(signal?: AbortSignal): Promise<DeclarationTarget[]> {
  const res = await apiFetch<unknown>("/api/product/targets", { cache: "no-store", redirect: "error", signal });
  const rows = isObj(res) && Array.isArray(res.targets) ? res.targets : [];
  const out: DeclarationTarget[] = [];
  for (const t of rows) {
    if (
      isObj(t) &&
      typeof t.id === "string" &&
      t.id !== "" &&
      typeof t.origin === "string" &&
      t.state === "approved" &&
      Number.isSafeInteger(t.revision) &&
      Number(t.revision) > 0
    )
      out.push({
        id: t.id,
        origin: t.origin,
        state: t.state,
        revision: Number(t.revision),
        pathPrefixes: names(t.path_prefixes),
        methods: names(t.methods),
      });
  }
  return out;
}

/** 참고 경고: 경로가 승인 접두 밖이거나 메서드가 승인 밖이면 문구. 저장을 막지 않는다 — 실제 허용은 E²가 요청마다 본다. */
export function scopeWarnings(d: DeclarationDraft, target: DeclarationTarget): DraftIssue[] {
  const inScope = (p: string) =>
    target.pathPrefixes.some((pre) => p === pre || p.startsWith(pre.endsWith("/") ? pre : `${pre}/`));
  const out: DraftIssue[] = [];
  if (d.identity.path && !inScope(d.identity.path))
    out.push({ field: "identity.path", message: "승인된 경로 접두 밖입니다 — 확정 단계에서 전송 통제가 막습니다." });
  const methods = new Set(target.methods.map((m) => m.toUpperCase()));
  d.types.forEach((t, i) => {
    if (t.sharing !== "owner_only") return;
    if (t.create.path && !inScope(t.create.path))
      out.push({ field: `types.${i}.create.path`, message: "승인된 경로 접두 밖입니다 — 전송 통제가 막습니다." });
    if (!methods.has(t.create.method))
      out.push({
        field: `types.${i}.create.method`,
        message: `대상 승인에 ${t.create.method} 가 없습니다 — 쓰기 승인이 없으면 확정 단계가 꺼집니다.`,
      });
    const readPath = t.read.pathTemplate.replace(ID_PLACEHOLDER, "x");
    if (t.read.pathTemplate && !inScope(readPath))
      out.push({ field: `types.${i}.read.pathTemplate`, message: "승인된 경로 접두 밖입니다 — 전송 통제가 막습니다." });
  });
  return out;
}

// ── 저장·조회 API ────────────────────────────────────────────────────────────────────────────────────
/** 가정 경로(#698 질문 1). 계정 분류(/accounts)와 같은 대상 하위 자리. */
export const declarationPath = (targetId: string): string =>
  `/api/product/targets/${encodeURIComponent(targetId)}/declaration`;

/** 라우트가 아직 없을 때의 응답(저장·동결 PR 전). 선언 없음과 다르게 보인다. */
const UNAVAILABLE_STATUS = new Set([404, 405, 501]);

/** 한 대상의 선언 조회. 라우트가 없으면 unavailable(선언 없음으로 바꾸지 않는다). 그 밖의 실패는 던진다. */
export async function getDeclaration(targetId: string, signal?: AbortSignal): Promise<DeclarationLoad> {
  try {
    const res = await apiFetch<unknown>(declarationPath(targetId), { cache: "no-store", redirect: "error", signal });
    return parseDeclarationResponse(res);
  } catch (e) {
    if (e instanceof ApiError && UNAVAILABLE_STATUS.has(e.status)) return { state: "unavailable" };
    throw e;
  }
}

/** 저장 실패 종류 — 화면 문구를 고르는 데만 쓴다. */
export type SaveFailure = "unavailable" | "forbidden" | "conflict" | "rejected" | "failed";

/** 저장 오류 → 종류. 403 승인자 아님, 409 대상 판이 바뀜, 400·422 서버 검사 거부. */
export function saveFailureOf(e: unknown): SaveFailure {
  if (!(e instanceof ApiError)) return "failed";
  if (UNAVAILABLE_STATUS.has(e.status)) return "unavailable";
  if (e.status === 403) return "forbidden";
  if (e.status === 409) return "conflict";
  if (e.status === 400 || e.status === 422) return "rejected";
  return "failed";
}

export const SAVE_FAILURE_TEXT: Record<SaveFailure, string> = {
  unavailable: "서버에 선언 저장 기능이 아직 없습니다(정책 선언 저장 · 동결 PR 대기). 입력은 화면에만 남아 있습니다.",
  forbidden: "이 대상의 승인자만 선언할 수 있습니다.",
  conflict: "그사이 대상 승인이 바뀌었습니다. 대상을 다시 읽고 선언을 확인하세요.",
  rejected: "서버가 선언 형식을 받지 않았습니다. 칸을 확인하고 다시 저장하세요.",
  failed: "선언을 저장하지 못했습니다. 잠시 뒤 다시 시도하세요.",
};

/** 선언 저장(PUT). 화면 검사를 통과한 것만 보낸다. 성공 뒤 화면은 다시 조회해 서버 값을 보인다. */
export async function saveDeclaration(target: DeclarationTarget, d: DeclarationDraft): Promise<void> {
  if (validateDraft(d).length) throw new Error("declaration_draft_invalid");
  await apiFetch<unknown>(declarationPath(target.id), {
    method: "PUT",
    body: JSON.stringify(toRequestBody(d, target.revision)),
    cache: "no-store",
    redirect: "error",
  });
}

/** 저장본 → 고칠 수 있는 화면 값(감사·대상 칸 제외, 깊은 복사). */
export function draftFromStored(d: StoredDeclaration): DeclarationDraft {
  return {
    identity: { ...d.identity },
    types: d.types.map((t) => ({
      ...t,
      ownerOnlyFields: [...t.ownerOnlyFields],
      create: { ...t.create },
      read: { ...t.read },
    })),
  };
}
