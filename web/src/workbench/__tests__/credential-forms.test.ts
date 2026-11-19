import { describe, expect, it } from "vitest";

import { type CredentialFormKind, credentialForms, goDecodeFirstJson, goMarshal } from "../live/credentialForms";
import { credentialFields, type DeclarationDraft, hasCredentialText, validateDraft } from "../live/declaration";

// 서버 #705 e3contract.CredentialField 는 칸 원문과 함께 이스케이프를 푼 모양(credentialForms)에도 dlp.CredentialText 를 댄다.
// 아래 기대값은 같은 입력을 Go(e3contract.CredentialField, 칸: 경로=identity.path·read.path_template, json·form=create.body_template,
// plain=read.normal_value·identity.subject_id_path)에 넣어 얻은 값이다. 값은 모두 합성 문자열이다.
const GO_CASES: [CredentialFormKind, string, boolean][] = [
  ["json", '{"note":"Cookie\\u003a sid=abc"}', true],
  ["json", '{"title":"t"}', false],
  ["json", '{"token":"x"}', true],
  ["json", '{"a":"token=<x"}', true],
  ["json", '{"a":"token=&x"}', true],
  ["json", '{"a":"tok\\u0065n=<x"}', true],
  ["json", '{"a":"x"} trailing', false],
  ["json", '{"a":"x"} {"b":"\\u0074oken=1"}', false],
  ["json", '{"a":"\\u0074oken=1"}', true],
  ["json", '{"a":["Bearer\\u0020abc"]}', true],
  ["json", '{"a":{"b":"pass\\u0077ord=1"}}', true],
  ["json", '{"api\\u005fkey":1}', true],
  ["json", '{"cookie_consent":true}', false],
  ["json", '{"x":"a&b"}', false],
  ["json", '{"secr\\u0065t": null}', true],
  ["json", '{"secr\\u0065t": {}}', true],
  ["json", '{"secr\\u0065t": []}', true],
  ["json", '{"secr\\u0065t": false}', true],
  ["json", '{"a":"\\ud83d\\ude00tok\\u0065n=1"}', true],
  ["json", '{"a":"x","b":"tok\\u0065n=1",}', false],
  ["json", '{"\\u0074oken":"\\u003cv"}', true],
  ["json", '["ey\\u004aa.b.c"]', true],
  ["json", '  {"a":"Basic\\u0020eA=="}  ', true],
  ["json", '{"a":"\\u000acookie: x"}', true],
  ["json", '{"a":1e5,"b":"ok"}', false],
  ["json", '{"a":"sec\\\\u0072et=1"}', false],
  ["json", '{"a":"\\ud800tok\\u0065n=1"}', true],
  ["json", '{"a":01}', false],
  ["json", '"tok\\u0065n=1"', false],
  ["json", '{"a":[{"b":["\\u0042earer x"]}]}', true],
  ["json", '{"a":"x" "b":"tok\\u0065n=1"}', false],
  ["json", '{"a":"p\\u0061sswd: 1"}', true],
  ["json", '{"t\\u006fken":"<"}', true],
  ["json", '{"x":"<a>","p\\u0077d":1.50}', true],
  ["json", '{"a":"\\u0043ookie\\u003a\\tsid"}', true],
  ["json", '{"a":"x\\u0000"}', false],
  ["json", '{"a":"\\u2028tok\\u0065n=1"}', true],
  ["json", '{"a":"line\\nbreak"}', false],
  ["json", '{"a":"bad\\q"}', false],
  ["json", '{"a":"\\u0074oken=1"} junk', true],
  ["json", '{"k":{"t\\u006fken":"<"}}', true],
  ["json", "", false],
  ["form", "title=a&h=Cookie%3A%20sid%3Dx", true],
  ["form", "title=a&h=Cookie%3A%20sid%3Dx;b=1", false],
  ["form", "title=a&tok%65n=1", true],
  ["form", "title=a&x=%zz&pass%77ord=1", false],
  ["form", "a=Bearer+abc", true],
  ["form", "a=b", false],
  ["form", "=x&y", false],
  ["form", "a=%E2%82%AC", false],
  ["form", "a=%FF%FEtok%65n=1", true],
  ["form", "a=1&b=2&c", false],
  ["form", "tok%65n", false],
  ["form", "a=%", false],
  ["form", "a=%2", false],
  ["form", "a&&b=se%63ret%3A1", true],
  ["form", "a=x%26tok%65n%3D1", true],
  ["form", "tok%65n=%3Cx", false],
  ["path", "/api/token%3D1", true],
  ["path", "/api/token%3d1", true],
  ["path", "/api/x%zz/token%3D1", false],
  ["path", "/api/a+b", false],
  ["path", "/api/%42earer%20abc", true],
  ["path", "/api/notes", false],
  ["path", "/api/pwd%3Ax", true],
  ["path", "/api/x%2Fy", false],
  ["path", "/api/%", false],
  ["path", "/api/pass%77ord=1", true],
  ["path", "/api/{id}/tok%65n%3D1", true],
  ["path", "/api/{id}", false],
  ["plain", "token%3D1", false],
  ["plain", "Cookie: a", true],
  ["plain", '"Cookie\\u003a x"', false],
  ["plain", "token%3D1", false],
  ["plain", "user.id", false],
];

describe("credentialForms — 서버 자격 검사와 같은 모양", () => {
  it.each(GO_CASES)("%s %j → 서버 거부=%s", (kind, value, want) => {
    expect(value !== "" && credentialForms(value, kind).some(hasCredentialText)).toBe(want);
  });

  it("첫 JSON 값만 엄격하게 읽고, 수는 원문을 지킨다(Go json.Decoder + UseNumber)", () => {
    const v = goDecodeFirstJson(String.raw`{"b":1.50,"a":"<&>\u2028"} {"x":1}`);
    if (v === undefined) throw new Error("첫 값을 읽지 못함");
    expect(goMarshal(v)).toBe(String.raw`{"a":"\u003c\u0026\u003e\u2028","b":1.50}`);
    for (const bad of ['{"a":1,}', '{"a" 1}', "[01]", '{"a":"\\q"}', '{"a":"\n"}', "", "  "])
      expect(goDecodeFirstJson(bad)).toBeUndefined();
  });
});

describe("credentialFields — 이스케이프로 숨긴 자격도 칸 이름으로 막는다", () => {
  const base = (): DeclarationDraft => ({
    identity: { path: "/api/me", subjectIdPath: "id" },
    types: [
      {
        type: "note",
        sharing: "owner_only",
        ownerOnlyFields: ["title"],
        create: {
          path: "/api/notes",
          method: "POST",
          bodyKind: "json",
          bodyTemplate: "{}",
          markerField: "title",
          idPath: "id",
        },
        read: { pathTemplate: "/api/notes/{id}", dataPath: ".", statusPath: "", normalValue: "" },
      },
    ],
  });
  it.each<[string, (d: DeclarationDraft) => void]>([
    [
      "types.0.create.bodyTemplate",
      (d) => (d.types[0].create.bodyTemplate = String.raw`{"note":"Cookie\u003a sid=synthetic"}`),
    ],
    [
      "types.0.create.bodyTemplate",
      (d) => {
        d.types[0].create.bodyKind = "form";
        d.types[0].create.bodyTemplate = "note=tok%65n%3Dsynthetic";
      },
    ],
    ["identity.path", (d) => (d.identity.path = "/api/token%3Dsynthetic")],
    ["types.0.create.path", (d) => (d.types[0].create.path = "/api/%42earer%20synthetic")],
    ["types.0.read.pathTemplate", (d) => (d.types[0].read.pathTemplate = "/api/{id}/pwd%3Asynthetic")],
  ])("%s", (field, f) => {
    const d = base();
    f(d);
    expect(credentialFields(d)).toEqual([field]);
    const issue = validateDraft(d).find((i) => /자격 값 모양/.test(i.message));
    expect(issue?.field).toBe(field);
    expect(issue?.message).not.toContain("synthetic"); // 문구에 원문 없음
  });

  it("퍼센트 인코딩은 경로 칸에서만 푼다(신원 식별자 위치·정상 값은 서버도 원문만 본다)", () => {
    const d = base();
    d.identity.subjectIdPath = "token%3D1";
    d.types[0].read.statusPath = "status";
    d.types[0].read.normalValue = "token%3D1";
    expect(credentialFields(d)).toEqual([]);
    expect(credentialFields(base())).toEqual([]);
  });
});
