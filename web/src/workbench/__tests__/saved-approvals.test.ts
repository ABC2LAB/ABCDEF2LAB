import { beforeEach, expect, it, vi } from "vitest";

import { apiFetch } from "@/lib/diagnosis/api";

import { parseSavedApprovals, readSavedApprovals } from "../live/savedApprovals";

vi.mock("@/lib/diagnosis/api", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/api")>()),
  apiFetch: vi.fn(),
}));

const ID = "a".repeat(32);
const row = (over: Record<string, unknown> = {}) => ({
  id: ID,
  attempt_ref: ID,
  method: "POST",
  state: "allowed",
  execution_state: "completed",
  created_at: "2026-11-24T01:00:00Z",
  expires_at: "2026-11-24T01:05:00Z",
  decided_at: "2026-11-24T01:00:30Z",
  live: false,
  can_decide: false,
  ...over,
});
const list = (approvals: unknown[]) => ({ run_ref: "run-job-1", approvals });

beforeEach(() => {
  vi.resetAllMocks();
});

it("저장 승인 이력: 읽기 전용 투영(결과 · 보냄 상태 · 시각)만 받는다", () => {
  const got = parseSavedApprovals(
    list([
      row(),
      row({
        id: "b".repeat(32),
        attempt_ref: "b".repeat(32),
        state: "denied",
        execution_state: "not_sent",
        decided_at: undefined,
      }),
    ]),
  );
  expect(got.run_ref).toBe("run-job-1");
  expect(got.approvals.map((a) => [a.state, a.execution_state])).toEqual([
    ["allowed", "completed"],
    ["denied", "not_sent"],
  ]);
  expect(got.approvals[1].decided_at).toBeUndefined();
  expect(parseSavedApprovals(list([])).approvals).toEqual([]);
});

it.each([
  ["결정할 수 있다고 옴(실시간 목록의 줄)", { can_decide: true }],
  ["실행 중이라고 옴", { live: true }],
  ["승인 ID 와 시도 ID 가 다름", { attempt_ref: "c".repeat(32) }],
  ["모르는 결과", { state: "approved" }],
  ["모르는 보냄 상태", { execution_state: "sent" }],
  ["모르는 메서드", { method: "TRACE" }],
  ["시각이 틀림", { created_at: "어제" }],
])("저장 승인 이력: %s 이면 받지 않는다", (_name, change) => {
  expect(() => parseSavedApprovals(list([row(change)]))).toThrow();
});

it("저장 승인 이력: 같은 승인이 두 번 · 1000건 초과 · 회차 참조 없음은 받지 않는다", () => {
  expect(() => parseSavedApprovals(list([row(), row()]))).toThrow();
  expect(() =>
    parseSavedApprovals(list(Array.from({ length: 1001 }, (_, i) => row({ id: `ap-${i}`, attempt_ref: `ap-${i}` })))),
  ).toThrow();
  expect(() => parseSavedApprovals({ approvals: [] })).toThrow();
});

it("끝난 점검 승인 이력은 저장 이력 경로로 묻는다(실시간 승인 API 가 아님)", async () => {
  vi.mocked(apiFetch).mockResolvedValue(list([row()]));
  await readSavedApprovals("42");
  expect(vi.mocked(apiFetch).mock.calls[0][0]).toBe("/api/tasks/42/saved-request-approvals");
  await expect(readSavedApprovals("0")).rejects.toThrow();
});
