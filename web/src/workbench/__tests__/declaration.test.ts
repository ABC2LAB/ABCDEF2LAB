import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/diagnosis/api", async (importOriginal) => {
  const real = await importOriginal<typeof import("@/lib/diagnosis/api")>();
  return { ...real, apiFetch: vi.fn() };
});

import { ApiError, apiFetch } from "@/lib/diagnosis/api";

import {
  confirmable,
  credentialFields,
  type DeclarationDraft,
  type DeclarationTarget,
  draftFieldOf,
  draftFromStored,
  emptyCreate,
  emptyRead,
  getDeclaration,
  getDeclarationTargets,
  hasCredentialText,
  looksLikeSecretName,
  normalizeDraft,
  parseDeclarationResponse,
  rejectedFieldOf,
  saveDeclaration,
  saveFailureOf,
  scopeWarnings,
  secretFieldsInTemplate,
  type TypeDraft,
  toRequestBody,
  validateDraft,
} from "../live/declaration";

const api = vi.mocked(apiFetch);
beforeEach(() => api.mockReset());

// e3contract run_test.go declaration() 과 같은 합성 선언(실제 앱 경로 아님)
const ownerOnly = (type: string, path: string): TypeDraft => ({
  type,
  sharing: "owner_only",
  ownerOnlyFields: ["body"],
  create: {
    path,
    method: "POST",
    bodyKind: "json",
    bodyTemplate: '{"title":"t"}',
    markerField: "body",
    idPath: "id",
    idField: "",
  },
  read: { pathTemplate: `${path}/{id}`, dataPath: ".", statusPath: "", normalValue: "" },
});
const shared = (type: string): TypeDraft => ({
  type,
  sharing: "shared",
  ownerOnlyFields: [],
  create: emptyCreate(),
  read: emptyRead(),
});
const base = (): DeclarationDraft => ({
  identity: { path: "/api/me", subjectIdPath: "id" },
  types: [ownerOnly("note", "/api/notes"), shared("post"), ownerOnly("secret", "/api/secrets")],
});
const edit = (f: (d: DeclarationDraft) => void): DeclarationDraft => {
  const d = structuredClone(base());
  f(d);
  return d;
};
const fields = (d: DeclarationDraft) => validateDraft(d).map((i) => i.field);

/** 서버가 낼 저장 레코드(Go Declaration JSON — 공유 종류도 빈 create·read 를 칸째 냄, 빈 목록은 null). */
const storedJson = (over: Record<string, unknown> = {}) => ({
  version: "e3-product-declaration/v1",
  target_ref: "target-1",
  target_approval_revision: "3",
  declared_by: "operator-1",
  declared_at: "2026-11-18T00:00:00.000000Z",
  identity: { path: "/api/me", subject_id_path: "id" },
  types: [
    {
      type: "note",
      sharing: "owner_only",
      owner_only_fields: ["body"],
      create: {
        path: "/api/notes",
        method: "POST",
        body_kind: "json",
        body_template: '{"title":"t"}',
        marker_field: "body",
        id_path: "id",
      },
      read: { path_template: "/api/notes/{id}", data_path: "." },
    },
    {
      type: "post",
      sharing: "shared",
      owner_only_fields: null,
      create: { path: "", method: "", body_kind: "", body_template: "", marker_field: "", id_path: "" },
      read: { path_template: "", data_path: "" },
    },
  ],
  ...over,
});

describe("validateDraft (e3contract Declaration.Validate 화면 사본)", () => {
  it("기본 선언은 통과하고, 공유 종류는 시험 대상이 아니다", () => {
    expect(validateDraft(base())).toEqual([]);
    expect(base().types.map(confirmable)).toEqual([true, false, true]);
  });

  it.each<[string, (d: DeclarationDraft) => void, string]>([
    ["no_types", (d) => (d.types = []), "types"],
    ["dup", (d) => (d.types[2].type = "note"), "types.2.type"],
    ["identity_path", (d) => (d.identity.path = "/api/me?x=1"), "identity.path"],
    ["identity_subject", (d) => (d.identity.subjectIdPath = ""), "identity.subjectIdPath"],
    ["type_name", (d) => (d.types[0].type = "Note"), "types.0.type"],
    ["marker_not_owner", (d) => (d.types[0].create.markerField = "title"), "types.0.create.markerField"],
    ["no_owner_fields", (d) => (d.types[0].ownerOnlyFields = []), "types.0.ownerOnlyFields"],
    ["create_get", (d) => (d.types[0].create.method = "GET" as never), "types.0.create.method"],
    ["create_delete", (d) => (d.types[0].create.method = "DELETE" as never), "types.0.create.method"],
    ["create_dotdot", (d) => (d.types[0].create.path = "/api/../notes"), "types.0.create.path"],
    ["create_template", (d) => (d.types[0].create.path = "/api/{id}"), "types.0.create.path"],
    ["body_not_object", (d) => (d.types[0].create.bodyTemplate = "[1]"), "types.0.create.bodyTemplate"],
    ["body_null", (d) => (d.types[0].create.bodyTemplate = "null"), "types.0.create.bodyTemplate"],
    ["body_has_marker", (d) => (d.types[0].create.bodyTemplate = '{"body":"x"}'), "types.0.create.bodyTemplate"],
    ["read_no_id", (d) => (d.types[0].read.pathTemplate = "/api/notes"), "types.0.read.pathTemplate"],
    ["read_two_ids", (d) => (d.types[0].read.pathTemplate = "/api/{id}/{id}"), "types.0.read.pathTemplate"],
    ["read_other_brace", (d) => (d.types[0].read.pathTemplate = "/api/{x}/{id}"), "types.0.read.pathTemplate"],
    ["read_data", (d) => (d.types[0].read.dataPath = ""), "types.0.read.dataPath"],
    ["status_half", (d) => (d.types[0].read.statusPath = "status"), "types.0.read.statusPath"],
    ["sharing", (d) => (d.types[0].sharing = "public" as never), "types.0.sharing"],
    ["id_path", (d) => (d.types[0].create.idPath = ""), "types.0.create.idPath"],
    // 객체 ID 출처는 둘 중 하나만(id_path 또는 id_field, e3contract Validate와 같음)
    ["id_both", (d) => (d.types[0].create.idField = "title_id"), "types.0.create.idField"],
    [
      "id_field_is_marker",
      (d) => Object.assign(d.types[0].create, { idPath: "", idField: "body" }),
      "types.0.create.idField",
    ],
    [
      "id_field_under_marker",
      (d) => Object.assign(d.types[0].create, { idPath: "", idField: "body.id" }),
      "types.0.create.idField",
    ],
    [
      "id_field_above_marker",
      (d) => {
        Object.assign(d.types[0].create, { idPath: "", idField: "meta", markerField: "meta.body" });
        d.types[0].ownerOnlyFields = ["meta.body"];
      },
      "types.0.create.idField",
    ],
    [
      "id_field_in_body",
      (d) => Object.assign(d.types[0].create, { idPath: "", idField: "title" }),
      "types.0.create.bodyTemplate",
    ],
    [
      "id_field_form_dotted",
      (d) => Object.assign(d.types[0].create, { idPath: "", idField: "a.b", bodyKind: "form", bodyTemplate: "a=1" }),
      "types.0.create.idField",
    ],
    [
      "id_field_form_in_body",
      (d) => Object.assign(d.types[0].create, { idPath: "", idField: "a", bodyKind: "form", bodyTemplate: "a=1" }),
      "types.0.create.bodyTemplate",
    ],
    [
      "form_dotted",
      (d) => {
        d.types[0].create = { ...d.types[0].create, bodyKind: "form", bodyTemplate: "a=1", markerField: "a.b" };
        d.types[0].ownerOnlyFields = ["a.b"];
      },
      "types.0.create.bodyTemplate",
    ],
    [
      "form_has_marker",
      (d) => (d.types[0].create = { ...d.types[0].create, bodyKind: "form", bodyTemplate: "body=1" }),
      "types.0.create.bodyTemplate",
    ],
    [
      "form_semicolon",
      (d) => (d.types[0].create = { ...d.types[0].create, bodyKind: "form", bodyTemplate: "a=1;b=2" }),
      "types.0.create.bodyTemplate",
    ],
    [
      "form_bad_percent",
      (d) => (d.types[0].create = { ...d.types[0].create, bodyKind: "form", bodyTemplate: "a=%zz" }),
      "types.0.create.bodyTemplate",
    ],
    ["body_kind", (d) => (d.types[0].create.bodyKind = "xml" as never), "types.0.create.bodyKind"],
  ])("%s 이면 그 칸을 거부한다", (_, change, field) => {
    expect(fields(edit(change))).toContain(field);
  });

  it("경로 모양: 512바이트 상한(UTF-8), 비ASCII 는 받고 공백·제어 문자·역슬래시는 받지 않는다", () => {
    const at = (path: string) => fields(edit((d) => (d.types[0].create.path = path)));
    expect(at(`/${"a".repeat(511)}`)).not.toContain("types.0.create.path");
    expect(at(`/${"a".repeat(512)}`)).toContain("types.0.create.path");
    expect(at(`/${"가".repeat(170)}`)).not.toContain("types.0.create.path"); // 1 + 510 바이트
    expect(at(`/${"가".repeat(171)}`)).toContain("types.0.create.path"); // 1 + 513 바이트
    expect(at("/api/메모")).not.toContain("types.0.create.path");
    for (const bad of ["api/notes", "/api/no tes", "/api/notes\u0007", "/api\\notes", "/api/notes#x", "/api/./notes"])
      expect(at(bad)).toContain("types.0.create.path");
  });

  it("form 틀: '+'·정상 % 는 받고, 표식 칸은 풀린 이름으로 비교한다", () => {
    const form = (bodyTemplate: string) =>
      fields(edit((d) => (d.types[0].create = { ...d.types[0].create, bodyKind: "form", bodyTemplate })));
    expect(form("title=a+b&memo=%EA%B0%80")).not.toContain("types.0.create.bodyTemplate");
    expect(form("b%6Fdy=1")).toContain("types.0.create.bodyTemplate"); // b%6Fdy = body
  });

  it("공유 종류는 요청 칸을 보지 않는다(정규화가 비운다)", () => {
    const d = edit((x) => (x.types[1].create.path = "/api/posts"));
    expect(validateDraft(d)).toEqual([]);
    expect(normalizeDraft(d).types.find((t) => t.type === "post")?.create).toEqual(emptyCreate());
  });
});

describe("자격 차단(절대규칙 2)", () => {
  it.each([
    ['{"title":"t","password":"x"}', "password"],
    ['{"title":"t","user":{"accessToken":"x"}}', "user.accessToken"],
    ['{"title":"t","api_key":"x"}', "api_key"],
    ['{"title":"t","h":"Bearer abc"}', "h"],
    ['{"title":"t","note":"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.x"}', "note"],
    ['{"title":"t","pwd":"x"}', "pwd"],
  ])("JSON %s → 저장을 막고 칸 이름만 알린다", (tpl, name) => {
    const d = edit((x) => (x.types[0].create.bodyTemplate = tpl));
    const msg = validateDraft(d).find((i) => i.field === "types.0.create.bodyTemplate")?.message ?? "";
    expect(msg).toContain(name);
    expect(msg).not.toMatch(/Bearer abc|eyJhbGci/); // 값 원문은 문구에 없다
  });

  it("form 틀의 자격 이름·값도 막는다", () => {
    expect(secretFieldsInTemplate("form", "title=a&session_id=1")).toEqual(["session_id"]);
    expect(secretFieldsInTemplate("form", "title=a&h=Basic%20eA")).toEqual(["h"]);
  });

  // 5차 리뷰: 서버 dlp.CredentialText 가 가리는 형식(#705 CredentialField 가 저장을 거부) — 결과는 Go 로 같은 입력을 돌려 맞춘 값
  it.each<[string, boolean]>([
    ['{"note":"Cookie: sid=abc"}', true],
    ["Cookie: a=b", true],
    ["set-cookie:x", true],
    ["Authorization: Basic eA==", true],
    ["Proxy-Authorization:\tx", true],
    ["COO\u212AIE: z", true], // Go (?i) 는 켈빈 기호를 k 로 접는다
    ["authorization:''", true],
    ['password="a b"', true],
    ["api-key=1", true],
    ["client_secret: x", true],
    ["session_id=1", true],
    ["Bearer abc", true],
    ["\u017Fbearer x", true], // ſ 는 Go \b 에서 단어 문자가 아니다
    ["eyJa.b.c", true],
    ["-----BEGIN RSA PRIVATE KEY-----\nx\n-----END RSA PRIVATE KEY-----", true],
    ["x", false],
    ["author=1", false],
    ['{"cookie_consent":true}', false],
    ["/api/notes/{id}", false],
    ['{"title":"t"}', false],
    ["token\u00a0=1", false], // Go \s 는 NBSP 를 공백으로 보지 않는다
    ["", false],
  ])("hasCredentialText(%j) = %s (서버와 같음)", (v, want) => {
    expect(hasCredentialText(v)).toBe(want);
  });

  it("본문 틀 말고 다른 칸의 자격 형식도 서버처럼 막고, 칸 이름만 알린다", () => {
    const cases: [string, (d: DeclarationDraft) => void][] = [
      [
        "types.0.create.bodyTemplate",
        (d) => (d.types[0].create.bodyTemplate = '{"title":"t","note":"Cookie: sid=synthetic"}'),
      ],
      [
        "types.0.read.normalValue",
        (d) => {
          d.types[0].read.statusPath = "status";
          d.types[0].read.normalValue = "Cookie: sid=synthetic";
        },
      ],
      [
        "types.0.create.bodyTemplate",
        (d) => {
          d.types[0].create.bodyKind = "form";
          d.types[0].create.bodyTemplate = "title=a&h=Cookie%3A%20sid%3Dx&cookie=sid";
        },
      ],
    ];
    for (const [field, f] of cases) {
      const issue = validateDraft(edit(f)).find((i) => /자격 값 모양/.test(i.message));
      expect(issue?.field).toBe(field);
      expect(issue?.message).not.toContain("synthetic");
    }
    // 공유 종류는 요청 칸을 보내지 않으므로 보지 않는다(서버도 정규화 뒤 빈 칸)
    expect(
      validateDraft(edit((d) => (d.types[1].create.bodyTemplate = "Cookie: sid=x"))).some((i) =>
        /자격 값 모양/.test(i.message),
      ),
    ).toBe(false);
  });

  // 6차 리뷰: 검사 문구는 입력 원문을 되풀이하지 않는다(자격 값을 잘못 넣은 칸이 경고에 다시 보이지 않게)
  it("검사 문구에 입력 원문을 넣지 않는다(겹친 이름 · 형식 밖 필드 · 자격 모양 키 이름)", () => {
    const leak = "Bearer synthetic-bearer-value";
    const jwt = "eyJsynthetic.eyJpayload.sig";
    const cases: ((d: DeclarationDraft) => void)[] = [
      (d) => {
        d.types[0].type = leak;
        d.types[2].type = leak;
      },
      (d) => (d.types[0].ownerOnlyFields = ["body", leak]),
      (d) => (d.types[0].ownerOnlyFields = ["body", jwt, jwt]),
      (d) => (d.types[0].create.bodyTemplate = `{"title":"t","Authorization: ${leak}":"x"}`),
      (d) => {
        d.types[0].create.bodyKind = "form";
        d.types[0].create.bodyTemplate = `title=t&${encodeURIComponent(`token: ${leak}`)}=x`;
      },
    ];
    for (const f of cases) {
      const issues = validateDraft(edit(f));
      expect(issues.length).toBeGreaterThan(0);
      for (const i of issues) {
        expect(i.message).not.toContain("synthetic");
        expect(i.message).not.toContain("eyJ");
      }
    }
    // 겹침은 몇 번째인지로 알린다
    expect(
      validateDraft(
        edit((d) => {
          d.types[0].type = leak;
          d.types[2].type = leak;
        }),
      ).find((i) => i.field === "types.2.type")?.message,
    ).toBe("자원 종류 이름이 1번 종류와 겹칩니다.");
    expect(validateDraft(edit((d) => (d.types[0].ownerOnlyFields = ["body", "body"]))).map((i) => i.message)).toContain(
      "소유자 전용 필드 2번째가 앞의 필드와 겹칩니다.",
    );
  });

  it("author · passenger · passage · title 같은 정상 이름은 막지 않는다", () => {
    for (const k of ["author", "passenger", "passage", "title", "subtitle", "description"])
      expect(looksLikeSecretName(k)).toBe(false);
    for (const k of ["pass", "auth", "Auth-Token", "sessionId", "SID", "otp"])
      expect(looksLikeSecretName(k)).toBe(true);
  });
});

/** #705 조회 응답(productDeclarationView) — 최상위 감사·판 칸은 안쪽 레코드와 같은 값. */
type Json = Record<string, unknown>;
const activeRes = (decl: Json = storedJson(), over: Json = {}) => ({
  state: "active",
  target_revision: Number(decl.target_approval_revision),
  declared_by: decl.declared_by,
  declared_at: decl.declared_at,
  declaration: decl,
  ...over,
});
const invalidRes = (reason: string, decl: Json = storedJson()) => ({
  ...activeRes(decl),
  state: "invalid",
  invalid_reason: reason,
});
const noneRes = { state: "none", declaration: null };

describe("normalizeDraft · toRequestBody", () => {
  it("종류·소유자 전용 필드를 오름차순으로 맞추고, 서버가 채우는 칸은 보내지 않는다", () => {
    const d = edit((x) => {
      x.types = [x.types[2], x.types[0], x.types[1]];
      x.types[0].ownerOnlyFields = ["memo", "body"];
    });
    expect(normalizeDraft(d).types.map((t) => t.type)).toEqual(["note", "post", "secret"]);
    const body = toRequestBody(d, 7);
    // #705: {"target_revision": N, "declaration": {"identity", "types"}} — 서버가 채우는 칸은 보내면 거부된다
    expect(Object.keys(body).sort()).toEqual(["declaration", "target_revision"]);
    expect(body.target_revision).toBe(7);
    const decl = body.declaration as Json;
    expect(Object.keys(decl).sort()).toEqual(["identity", "types"]);
    const types = decl.types as Record<string, unknown>[];
    expect(types[1]).toEqual({ type: "post", sharing: "shared" });
    expect(types[2].owner_only_fields).toEqual(["body", "memo"]);
    expect(types[0].read).toEqual({ path_template: "/api/notes/{id}", data_path: "." });
  });

  it("상태 칸을 쓰면 둘 다 보낸다", () => {
    const d = edit((x) => (x.types[0].read = { ...x.types[0].read, statusPath: "status", normalValue: "success" }));
    expect(((toRequestBody(d, 1).declaration as Json).types as Record<string, Json>[])[0].read).toMatchObject({
      status_path: "status",
      normal_value: "success",
    });
  });
});

describe("parseDeclarationResponse (닫힌 파서)", () => {
  it("서버 none · active · invalid 를 화면 none · current · invalidated 로 나눈다", () => {
    expect(parseDeclarationResponse(noneRes, "target-1")).toEqual({ state: "none" });
    const cur = parseDeclarationResponse(activeRes(), "target-1");
    expect(cur.state).toBe("current");
    if (cur.state === "current") {
      expect(cur.declaration.declaredBy).toBe("operator-1");
      expect(cur.declaration.targetApprovalRevision).toBe("3");
      expect(cur.declaration.types.map((t) => t.type)).toEqual(["note", "post"]);
    }
    for (const reason of ["target_reapproved", "target_expired"]) {
      const inv = parseDeclarationResponse(invalidRes(reason), "target-1");
      expect(inv.state).toBe("invalidated");
      if (inv.state === "invalidated") expect(inv.reason).toBe(reason);
    }
  });

  it.each<[string, unknown]>([
    ["모르는 상태", { state: "draft" }],
    ["예전 가정 상태 이름(current)", { state: "current", declaration: storedJson() }],
    ["none 인데 declaration 칸 없음", { state: "none" }],
    ["none 인데 선언이 있음", { state: "none", declaration: storedJson() }],
    ["모르는 최상위 키", { ...noneRes, extra: 1 }],
    ["active 에 최상위 감사 칸 없음", { state: "active", declaration: storedJson() }],
    ["최상위 판이 안쪽과 다름", activeRes(storedJson(), { target_revision: 4 })],
    ["최상위 선언자가 안쪽과 다름", activeRes(storedJson(), { declared_by: "operator-2" })],
    ["최상위 시각이 안쪽과 다름", activeRes(storedJson(), { declared_at: "2026-11-18T00:00:01Z" })],
    ["판이 다름", activeRes(storedJson({ version: "e3-product-declaration/v0" }))],
    ["모르는 선언 키", activeRes({ ...storedJson(), note: "x" })],
    ["감사 칸 없음", activeRes({ ...storedJson(), declared_by: undefined })],
    ["시각 깨짐", activeRes(storedJson({ declared_at: "어제" }))],
    ["정렬 어긋남", activeRes(storedJson({ types: [...storedJson().types].reverse() }))],
    [
      "공유 종류에 요청 칸",
      activeRes(
        storedJson({
          types: [
            storedJson().types[0],
            { type: "post", sharing: "shared", create: { ...storedJson().types[0].create } },
          ],
        }),
      ),
    ],
    [
      "규칙 위반(조회 틀에 {id} 없음)",
      activeRes(
        storedJson({
          types: [{ ...storedJson().types[0], read: { path_template: "/api/notes", data_path: "." } }],
        }),
      ),
    ],
    ["무효 사유 없음", { ...activeRes(), state: "invalid" }],
    ["모르는 무효 사유", invalidRes("target_deleted")],
    ["배열", []],
    // 5차 리뷰: 다른 대상의 정상 모양 선언(target_ref 가 요청 대상과 다름)
    ["유효 선언의 대상이 다름", activeRes(storedJson({ target_ref: "target-2" }))],
    ["무효 선언의 대상이 다름", invalidRes("target_reapproved", storedJson({ target_ref: "target-2" }))],
  ])("%s → invalid(빈 선언으로 바꾸지 않는다)", (_, res) => {
    expect(parseDeclarationResponse(res, "target-1")).toEqual({ state: "invalid" });
  });
});

describe("API", () => {
  const target: DeclarationTarget = {
    id: "t-1",
    origin: "https://app.example",
    state: "approved",
    revision: 3,
    pathPrefixes: ["/api"],
    methods: ["GET", "POST"],
  };

  it("404 target_not_found 는 대상 없음, 코드 없는 404·405·501 은 라우트 없음 — 둘 다 선언 없음과 다르다. 그 밖은 던진다", async () => {
    api.mockRejectedValueOnce(new ApiError(404, "target_not_found"));
    await expect(getDeclaration("t-1")).resolves.toEqual({ state: "not_found" });
    for (const status of [404, 405, 501]) {
      api.mockRejectedValueOnce(new ApiError(status, `HTTP ${status}`));
      await expect(getDeclaration("t-1")).resolves.toEqual({ state: "unavailable" });
    }
    api.mockRejectedValueOnce(new ApiError(503, "product_control_unavailable"));
    await expect(getDeclaration("t-1")).rejects.toBeInstanceOf(ApiError);
    api.mockResolvedValueOnce(noneRes);
    await expect(getDeclaration("t/1")).resolves.toEqual({ state: "none" });
    expect(api).toHaveBeenLastCalledWith(
      "/api/product/targets/t%2F1/declaration",
      expect.objectContaining({ cache: "no-store" }),
    );
  });

  it("대상 목록은 승인된 · 판이 있는 대상만", async () => {
    api.mockResolvedValueOnce({
      targets: [
        {
          id: "a",
          origin: "https://a.example",
          state: "approved",
          revision: 2,
          path_prefixes: ["/api"],
          methods: ["GET"],
        },
        { id: "b", origin: "https://b.example", state: "pending", revision: 1 },
        { id: "c", origin: "https://c.example", state: "approved", revision: 0 },
        { id: "d", origin: "https://d.example", state: "approved", revision: "2" },
      ],
    });
    const out = await getDeclarationTargets();
    expect(out.map((t) => t.id)).toEqual(["a"]);
    expect(out[0]).toMatchObject({ revision: 2, pathPrefixes: ["/api"], methods: ["GET"] });
  });

  it("저장은 PUT 하나, 검사를 못 넘으면 보내지 않는다", async () => {
    api.mockResolvedValueOnce(undefined);
    await saveDeclaration(target, base());
    expect(api).toHaveBeenCalledTimes(1);
    const [path, init] = api.mock.calls[0];
    expect(path).toBe("/api/product/targets/t-1/declaration");
    expect(init?.method).toBe("PUT");
    expect(JSON.parse(String(init?.body))).toMatchObject({ target_revision: 3, declaration: { identity: {} } });
    api.mockClear();
    await expect(
      saveDeclaration(
        target,
        edit((d) => (d.types = [])),
      ),
    ).rejects.toThrow();
    expect(api).not.toHaveBeenCalled();
    // 쿠키 헤더를 붙여넣은 선언은 요청을 만들기 전에 멈춘다(값이 서버로 가지 않는다)
    await expect(
      saveDeclaration(
        target,
        edit((d) => (d.types[0].create.bodyTemplate = '{"title":"t","note":"Cookie: sid=synthetic"}')),
      ),
    ).rejects.toThrow();
    expect(api).not.toHaveBeenCalled();
  });

  it("저장 오류 종류", () => {
    expect(saveFailureOf(new ApiError(404, "target_not_found"))).toBe("not_found");
    expect(saveFailureOf(new ApiError(404, "HTTP 404"))).toBe("unavailable");
    expect(saveFailureOf(new ApiError(403, "operator_required"))).toBe("forbidden");
    expect(saveFailureOf(new ApiError(409, "target_not_approved"))).toBe("not_approved");
    expect(saveFailureOf(new ApiError(409, "declaration_target_revision_mismatch"))).toBe("conflict");
    // 모르는 409 를 '판이 바뀜'으로 추정하지 않는다
    expect(saveFailureOf(new ApiError(409, "something_else"))).toBe("failed");
    expect(saveFailureOf(new ApiError(400, "invalid_declaration", "types[0].create.path"))).toBe("rejected");
    expect(saveFailureOf(new ApiError(422, "x"))).toBe("failed");
    expect(saveFailureOf(new ApiError(500, "x"))).toBe("failed");
    expect(saveFailureOf(new Error("x"))).toBe("failed");
  });

  it("서버가 짚은 칸 → 화면 칸 이름(값 없이 위치만)", () => {
    expect(rejectedFieldOf(new ApiError(400, "invalid_declaration", "types[0].create.body_template"))).toBe(
      "types[0].create.body_template",
    );
    expect(rejectedFieldOf(new ApiError(409, "target_not_approved"))).toBeNull();
    expect(draftFieldOf("types[0].create.body_template")).toBe("types.0.create.bodyTemplate");
    expect(draftFieldOf("identity.subject_id_path")).toBe("identity.subjectIdPath");
    expect(draftFieldOf("types[2].owner_only_fields[1]")).toBe("types.2.ownerOnlyFields");
    expect(draftFieldOf("target_revision")).toBe("targetRevision");
    expect(draftFieldOf("x=secret")).toBeNull();
  });

  it("승인 범위 경고는 접두 밖 경로 · 승인 밖 메서드만(참고)", () => {
    const d = edit((x) => {
      x.identity.path = "/me";
      x.types[0].create.method = "PUT";
      x.types[2].read.pathTemplate = "/apix/{id}";
    });
    expect(scopeWarnings(d, target).map((w) => w.field)).toEqual([
      "identity.path",
      "types.0.create.method",
      "types.2.read.pathTemplate",
    ]);
    expect(scopeWarnings(base(), target)).toEqual([]);
  });
});

describe("계정 역할(계약 §4, 선택 칸)", () => {
  const roles = { owner_account_label: "tester-a", cross_account_label: "tester-b", accounts_same_privilege: true };

  it("역할이 든 저장본을 읽고, 화면 값으로 옮겨 다시 저장해도 지우지 않는다", () => {
    const cur = parseDeclarationResponse(activeRes(storedJson(roles)), "target-1");
    expect(cur.state).toBe("current");
    if (cur.state !== "current") return;
    expect(cur.declaration.roles).toEqual({
      ownerAccountLabel: "tester-a",
      crossAccountLabel: "tester-b",
      accountsSamePrivilege: true,
    });
    const body = toRequestBody(draftFromStored(cur.declaration), 3) as { declaration: Record<string, unknown> };
    expect(body.declaration).toMatchObject(roles);
  });

  it("역할 없는 저장본은 그대로 읽고, 저장 본문에 역할 칸을 넣지 않는다", () => {
    const cur = parseDeclarationResponse(activeRes(storedJson()), "target-1");
    expect(cur.state).toBe("current");
    if (cur.state !== "current") return;
    expect(cur.declaration.roles).toBeUndefined();
    const body = toRequestBody(draftFromStored(cur.declaration), 3) as { declaration: Record<string, unknown> };
    expect(Object.keys(body.declaration).sort()).toEqual(["identity", "types"]);
  });

  it("등급 확인이 거짓이면 본문에서 빼고(서버 정본과 같음) 역할은 보낸다", () => {
    const cur = parseDeclarationResponse(
      activeRes(storedJson({ owner_account_label: "tester-a", cross_account_label: "tester-b" })),
      "target-1",
    );
    if (cur.state !== "current") throw new Error("current 아님");
    expect(cur.declaration.roles?.accountsSamePrivilege).toBe(false);
    const body = toRequestBody(draftFromStored(cur.declaration), 3) as { declaration: Record<string, unknown> };
    expect(body.declaration).not.toHaveProperty("accounts_same_privilege");
    expect(body.declaration).toMatchObject({ owner_account_label: "tester-a", cross_account_label: "tester-b" });
  });

  it.each<[string, Record<string, unknown>]>([
    ["한쪽만", { owner_account_label: "tester-a" }],
    ["같은 계정", { owner_account_label: "tester-a", cross_account_label: "tester-a" }],
    ["라벨 모양", { owner_account_label: "tester a", cross_account_label: "tester-b" }],
    ["등급 확인만", { accounts_same_privilege: true }],
    ["등급 확인이 문자열", { ...roles, accounts_same_privilege: "yes" }],
    ["등급 확인이 false로 직접", { ...roles, accounts_same_privilege: false }],
  ])("계약 밖 역할 칸(%s)은 깨진 응답이다", (_name, over) => {
    expect(parseDeclarationResponse(activeRes(storedJson(over)), "target-1").state).toBe("invalid");
  });
});

// 요청 본문 칸 ID(create.id_field, #757): 서버는 id_path를 늘 내보내고(빈 값 포함) id_field는 그 선언에만 싣는다.
describe("객체 ID 출처 id_field", () => {
  const withIDField = () => {
    const j = storedJson() as Record<string, unknown> & { types: Record<string, unknown>[] };
    j.types[0] = { ...j.types[0], create: { ...(j.types[0].create as Json), id_path: "", id_field: "title_id" } };
    return j;
  };

  it("id_field 선언을 읽고 다시 저장해도 id_field를 지우지 않는다", () => {
    const cur = parseDeclarationResponse(activeRes(withIDField()), "target-1");
    expect(cur.state).toBe("current");
    if (cur.state !== "current") return;
    expect(cur.declaration.types[0].create).toMatchObject({ idPath: "", idField: "title_id" });
    const draft = draftFromStored(cur.declaration);
    expect(validateDraft(draft)).toEqual([]);
    const body = toRequestBody(draft, 3) as { declaration: { types: { create?: Json }[] } };
    expect(body.declaration.types[0].create).toMatchObject({ id_path: "", id_field: "title_id" });
  });

  it("id_path 선언·공유 종류는 칸 모양이 그대로이고 저장 본문에 id_field를 넣지 않는다", () => {
    const cur = parseDeclarationResponse(activeRes(storedJson()), "target-1");
    if (cur.state !== "current") throw new Error("current 아님");
    expect(cur.declaration.types[0].create.idField).toBe("");
    const body = toRequestBody(draftFromStored(cur.declaration), 3) as { declaration: { types: { create?: Json }[] } };
    expect(body.declaration.types[0].create).not.toHaveProperty("id_field");
  });

  it.each<[string, Json]>([
    ["둘 다", { id_path: "id", id_field: "title_id" }],
    ["둘 다 없음", { id_path: "" }],
    ["id_field 문자열 아님", { id_path: "", id_field: 1 }],
  ])("ID 출처가 어긋난 응답은 계약 밖이다: %s", (_name, create) => {
    const j = storedJson() as Record<string, unknown> & { types: Record<string, unknown>[] };
    j.types[0] = { ...j.types[0], create: { ...(j.types[0].create as Json), ...create } };
    expect(parseDeclarationResponse(activeRes(j), "target-1").state).toBe("invalid");
  });

  it("자격 모양 검사에 ID 칸도 들어간다(서버 CredentialField와 같음)", () => {
    const d = base();
    Object.assign(d.types[0].create, { idPath: "", idField: "session_id=1" });
    expect(credentialFields(d)).toContain("types.0.create.idField");
  });

  it("서버 칸 이름 create.id_field를 화면 칸으로 옮긴다", () => {
    expect(draftFieldOf("types[0].create.id_field")).toBe("types.0.create.idField");
  });
});
