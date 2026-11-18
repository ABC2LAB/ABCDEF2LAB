import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/diagnosis/api", async (importOriginal) => {
  const real = await importOriginal<typeof import("@/lib/diagnosis/api")>();
  return { ...real, apiFetch: vi.fn() };
});

import { ApiError, apiFetch } from "@/lib/diagnosis/api";

import {
  confirmable,
  type DeclarationDraft,
  type DeclarationTarget,
  emptyCreate,
  emptyRead,
  getDeclaration,
  getDeclarationTargets,
  looksLikeSecretName,
  normalizeDraft,
  parseDeclarationResponse,
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
  create: { path, method: "POST", bodyKind: "json", bodyTemplate: '{"title":"t"}', markerField: "body", idPath: "id" },
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
  target_approval_revision: "approval-3",
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

  it("author · passenger · passage · title 같은 정상 이름은 막지 않는다", () => {
    for (const k of ["author", "passenger", "passage", "title", "subtitle", "description"])
      expect(looksLikeSecretName(k)).toBe(false);
    for (const k of ["pass", "auth", "Auth-Token", "sessionId", "SID", "otp"])
      expect(looksLikeSecretName(k)).toBe(true);
  });
});

describe("normalizeDraft · toRequestBody", () => {
  it("종류·소유자 전용 필드를 오름차순으로 맞추고, 서버가 채우는 칸은 보내지 않는다", () => {
    const d = edit((x) => {
      x.types = [x.types[2], x.types[0], x.types[1]];
      x.types[0].ownerOnlyFields = ["memo", "body"];
    });
    expect(normalizeDraft(d).types.map((t) => t.type)).toEqual(["note", "post", "secret"]);
    const body = toRequestBody(d, 7);
    expect(body).toMatchObject({ expected_target_revision: 7, version: "e3-product-declaration/v1" });
    for (const k of ["declared_by", "declared_at", "target_ref", "target_approval_revision"])
      expect(body).not.toHaveProperty(k);
    const types = body.types as Record<string, unknown>[];
    expect(types[1]).toEqual({ type: "post", sharing: "shared" });
    expect(types[2].owner_only_fields).toEqual(["body", "memo"]);
    expect(types[0].read).toEqual({ path_template: "/api/notes/{id}", data_path: "." });
  });

  it("상태 칸을 쓰면 둘 다 보낸다", () => {
    const d = edit((x) => (x.types[0].read = { ...x.types[0].read, statusPath: "status", normalValue: "success" }));
    expect((toRequestBody(d, 1).types as Record<string, Record<string, unknown>>[])[0].read).toMatchObject({
      status_path: "status",
      normal_value: "success",
    });
  });
});

describe("parseDeclarationResponse (닫힌 파서)", () => {
  it("none · current · invalidated 를 나눈다", () => {
    expect(parseDeclarationResponse({ state: "none" })).toEqual({ state: "none" });
    const cur = parseDeclarationResponse({ state: "current", declaration: storedJson() });
    expect(cur.state).toBe("current");
    if (cur.state === "current") {
      expect(cur.declaration.declaredBy).toBe("operator-1");
      expect(cur.declaration.types.map((t) => t.type)).toEqual(["note", "post"]);
    }
    const inv = parseDeclarationResponse({ state: "invalidated", reason: "target_reapproved", declaration: null });
    expect(inv).toEqual({ state: "invalidated", reason: "target_reapproved", declaration: null });
  });

  it.each<[string, unknown]>([
    ["모르는 상태", { state: "draft" }],
    ["모르는 최상위 키", { state: "none", extra: 1 }],
    ["판이 다름", { state: "current", declaration: storedJson({ version: "e3-product-declaration/v0" }) }],
    ["모르는 선언 키", { state: "current", declaration: { ...storedJson(), note: "x" } }],
    ["감사 칸 없음", { state: "current", declaration: { ...storedJson(), declared_by: undefined } }],
    ["시각 깨짐", { state: "current", declaration: storedJson({ declared_at: "어제" }) }],
    ["정렬 어긋남", { state: "current", declaration: storedJson({ types: [...storedJson().types].reverse() }) }],
    [
      "공유 종류에 요청 칸",
      {
        state: "current",
        declaration: storedJson({
          types: [
            storedJson().types[0],
            { type: "post", sharing: "shared", create: { ...storedJson().types[0].create } },
          ],
        }),
      },
    ],
    [
      "규칙 위반(조회 틀에 {id} 없음)",
      {
        state: "current",
        declaration: storedJson({
          types: [{ ...storedJson().types[0], read: { path_template: "/api/notes", data_path: "." } }],
        }),
      },
    ],
    ["무효 사유 없음", { state: "invalidated", declaration: null }],
    ["배열", []],
  ])("%s → invalid(빈 선언으로 바꾸지 않는다)", (_, res) => {
    expect(parseDeclarationResponse(res)).toEqual({ state: "invalid" });
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

  it("조회 라우트가 없으면(404·405·501) unavailable — 선언 없음과 다르다. 그 밖의 실패는 던진다", async () => {
    for (const status of [404, 405, 501]) {
      api.mockRejectedValueOnce(new ApiError(status, "x"));
      await expect(getDeclaration("t-1")).resolves.toEqual({ state: "unavailable" });
    }
    api.mockRejectedValueOnce(new ApiError(503, "x"));
    await expect(getDeclaration("t-1")).rejects.toBeInstanceOf(ApiError);
    api.mockResolvedValueOnce({ state: "none" });
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
    expect(JSON.parse(String(init?.body))).toMatchObject({ expected_target_revision: 3 });
    api.mockClear();
    await expect(
      saveDeclaration(
        target,
        edit((d) => (d.types = [])),
      ),
    ).rejects.toThrow();
    expect(api).not.toHaveBeenCalled();
  });

  it("저장 오류 종류", () => {
    expect(saveFailureOf(new ApiError(404, "x"))).toBe("unavailable");
    expect(saveFailureOf(new ApiError(403, "x"))).toBe("forbidden");
    expect(saveFailureOf(new ApiError(409, "x"))).toBe("conflict");
    expect(saveFailureOf(new ApiError(422, "x"))).toBe("rejected");
    expect(saveFailureOf(new ApiError(500, "x"))).toBe("failed");
    expect(saveFailureOf(new Error("x"))).toBe("failed");
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
