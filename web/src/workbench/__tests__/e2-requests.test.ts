import { beforeEach, expect, it, vi } from "vitest";

import { ApiError, apiFetch } from "@/lib/diagnosis/api";

import {
  parseE2RequestDetail,
  parseE2Requests,
  pathDetailText,
  readE2RequestDetail,
  readE2Requests,
  validAttemptId,
  validCallRef,
} from "../live/e2Requests";

vi.mock("@/lib/diagnosis/api", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/api")>()),
  apiFetch: vi.fn(),
}));

const A1 = "a".repeat(32);
const A2 = "b".repeat(32);
const row = (index: number, over: Record<string, unknown> = {}) => ({
  at: "2026-11-25T01:00:00Z",
  kind: "request",
  method: "GET",
  path: "/api/users/{id}",
  status: 200,
  call_ref: `call-${index}`,
  attempt_id: A1,
  index,
  path_detail_state: "pattern_only",
  ...over,
});
const page = (over: Record<string, unknown> = {}) => ({
  format: "abc2lab.e2-flow-requests/v1",
  rows: [row(3), row(7)],
  total: 5,
  next_cursor: 7,
  ...over,
});
const detail = (over: Record<string, unknown> = {}) => ({
  format: "abc2lab.e2-flow-request/v1",
  call_ref: "call-3",
  attempt_id: A1,
  kind: "request",
  index: 3,
  at: "2026-11-25T01:00:00Z",
  method: "GET",
  path_pattern: "/api/users/{id}",
  path_detail_state: "pattern_only",
  rule_ref: "rule-get",
  decision: "allowed",
  reason: "complete",
  status_code: 200,
  forward_attempted: true,
  request_written: true,
  response_received: true,
  response_complete: true,
  response_truncated: false,
  request_headers: { state: "not_stored" },
  request_body: { state: "not_stored" },
  response_headers: { state: "not_stored" },
  response_body: { state: "not_stored" },
  ...over,
});

beforeEach(() => {
  vi.resetAllMocks();
});

it("요청 식별자 형식은 서버 브리지와 같다(call_ref 영숫자 · attempt_id 16진수 32자)", () => {
  expect(validCallRef("call-1.a_b")).toBe(true);
  expect(validCallRef("-call")).toBe(false);
  expect(validCallRef("a/b")).toBe(false);
  expect(validAttemptId(A1)).toBe(true);
  expect(validAttemptId("A".repeat(32))).toBe(false);
  expect(validAttemptId("a".repeat(31))).toBe(false);
});

it("전체 기록: 줄 · 전체 수 · 다음 쪽을 읽고, 마지막 쪽이면 next_cursor 는 null", () => {
  const p = parseE2Requests(page());
  expect(p.rows.map((r) => r.index)).toEqual([3, 7]);
  expect(p.rows[0]).toMatchObject({ kind: "request", path: "/api/users/{id}", status: 200, call_ref: "call-3" });
  expect(p.total).toBe(5);
  expect(p.next_cursor).toBe(7);
  expect(parseE2Requests(page({ next_cursor: undefined })).next_cursor).toBeNull();
});

it.each([
  ["다른 형식", { format: "abc2lab.e2-flow/v1" }],
  ["틀린 시도 식별자", { rows: [row(3, { attempt_id: "xyz" })] }],
  ["저널 번호가 오름차순이 아님", { rows: [row(7), row(3)] }],
  ["모르는 경로 상태", { rows: [row(3, { path_detail_state: "raw" })] }],
  ["200 줄 넘음", { rows: Array.from({ length: 201 }, (_, i) => row(i + 1)) }],
  ["전체 수 없음", { total: undefined }],
])("전체 기록: %s 이면 받지 않는다", (_name, change) => {
  expect(() => parseE2Requests(page(change))).toThrow();
});

it("상세: 결정 · 사유 · 보냄 · 응답 · 헤더 본문 '저장 안 함'을 읽는다", () => {
  const d = parseE2RequestDetail(detail(), "call-3", A1);
  expect(d).toMatchObject({ decision: "allowed", reason: "complete", status_code: 200, request_written: true });
  const ns = { state: "not_stored", code: "not_stored" };
  expect(d.fields).toEqual({ request_headers: ns, request_body: ns, response_headers: ns, response_body: ns });
});

it("상세: 물은 호출 · 시도와 다른 줄은 버린다(다른 요청 결과를 이 줄에 붙이지 않음)", () => {
  expect(() => parseE2RequestDetail(detail(), "call-3", A2)).toThrow();
  expect(() => parseE2RequestDetail(detail(), "call-9", A1)).toThrow();
});

it("상세: 모르는 결정은 결과 불명, 모르는 사유는 코드 그대로, 모르는 칸 모양은 '모름'으로 — 지어내지 않는다", () => {
  const d = parseE2RequestDetail(
    detail({ decision: "maybe", reason: "new_reason", response_body: { state: "stored_later" } }),
    "call-3",
    A1,
  );
  expect(d.decision).toBe("unknown");
  expect(d.reason).toBeNull();
  expect(d.reasonCode).toBe("new_reason");
  expect(d.fields.response_body).toEqual({ state: "unknown", code: "stored_later" });
  expect(() => parseE2RequestDetail(detail({ request_written: "yes" }), "call-3", A1)).toThrow();
});

it("전체 기록 조회: 첫 쪽은 cursor 없이, 다음 쪽은 앞 쪽의 next_cursor 로 묻는다", async () => {
  vi.mocked(apiFetch).mockResolvedValue(page());
  await readE2Requests("42", null);
  await readE2Requests("42", 7);
  const paths = vi.mocked(apiFetch).mock.calls.map((c) => c[0]);
  expect(paths[0]).toBe("/api/tasks/42/e2-flow/requests?limit=100");
  expect(paths[1]).toBe("/api/tasks/42/e2-flow/requests?limit=100&cursor=7");
  await expect(readE2Requests("x", null)).rejects.toThrow();
});

it.each([
  [404, "not_found"],
  [409, "journal_invalid"],
  [403, "forbidden"],
  [503, "error"],
] as const)("상세 조회 HTTP %s 는 '%s'로 돌려준다(그 칸만 못 보임)", async (status, why) => {
  vi.mocked(apiFetch).mockRejectedValue(new ApiError(status, `HTTP ${status}`));
  const got = await readE2RequestDetail("42", "call-3", A1);
  expect(got).toMatchObject({ ok: false, why });
  expect(vi.mocked(apiFetch).mock.calls[0][0]).toBe(`/api/tasks/42/e2-flow/requests/call-3/${A1}`);
});

it("상세 조회: 모양이 틀린 응답은 오류로(값을 보이지 않음)", async () => {
  vi.mocked(apiFetch).mockResolvedValue(detail({ format: "x" }));
  expect(await readE2RequestDetail("42", "call-3", A1)).toMatchObject({ ok: false, why: "error" });
  await expect(readE2RequestDetail("42", "bad/ref", A1)).rejects.toThrow();
});

it("경로 표시: 값을 가린 틀 · 정확한 경로 · 틀 없음(기록 안 됨 / 너무 김)을 가른다", () => {
  expect(pathDetailText("pattern_only", "/users/{id}")).toMatch(/값을 가린 경로 틀/);
  expect(pathDetailText("exact", "/users/1")).toBe("정확한 경로");
  expect(pathDetailText("unavailable", "/{too_long}")).toBe("경로가 너무 길어 틀로 남기지 않았어요");
  expect(pathDetailText("unavailable")).toBe("이 시도의 경로 틀이 기록되지 않았어요");
});

// 계약 §4.1: "화면은 상태 필드가 없는 이전 서버 응답도 받는다"(1bb6f386 이전 서버)
it("경로 상태 칸이 없는 이전 서버 응답도 받고, 모양은 지어내지 않는다(null)", () => {
  const { path_detail_state: _drop, ...oldRow } = row(3);
  const p = parseE2Requests(page({ rows: [oldRow] }));
  expect(p.rows[0].path_detail_state).toBeNull();
  expect(p.rows[0].path).toBe("/api/users/{id}");
  const { path_detail_state: _d2, ...oldDetail } = detail();
  const d = parseE2RequestDetail(oldDetail, "call-3", A1);
  expect(d.path_detail_state).toBeNull();
  expect(d.path_pattern).toBe("/api/users/{id}");
  expect(pathDetailText(null, "/api/users/{id}")).toBeNull();
});
