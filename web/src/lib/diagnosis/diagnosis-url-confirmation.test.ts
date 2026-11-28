import { describe, expect, it, vi } from "vitest";

import { apiFetch } from "./api";
import { parseURLConfirmation, parseURLServerTime, readURLReceipt, readURLStatus } from "./diagnosis-url";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

vi.mock("./api", async (original) => ({ ...(await original<typeof import("./api")>()), apiFetch: vi.fn() }));

// 실제 Go handler가 내는 회차 상태 응답의 확정 단계 결과(e3-product-confirmation-interface.md '회차 상태 응답').
type Example = {
  version: string;
  cases: { name: string; status: Record<string, unknown> & { owner_context: string } }[];
};
const example = JSON.parse(
  readFileSync(resolve(process.cwd(), "../docs/contracts/run-confirmation.status.example.json"), "utf8"),
) as Example;

describe("회차 상태의 확정 단계 결과", () => {
  it("시간 상한과 확정 단계 상태를 한 회차 응답에서 함께 읽는다", async () => {
    const timing = JSON.parse(
      readFileSync(resolve(process.cwd(), "../docs/contracts/diagnosis-run-timing.responses.example.json"), "utf8"),
    ) as { cases: { name: string; status: Record<string, unknown> & { owner_context: string } }[] };
    const limited = timing.cases.find(({ name }) => name === "running_with_time_limit")?.status;
    const confirmation = example.cases.find(({ name }) => name === "running_not_recorded")?.status.confirmation;
    if (!limited || !confirmation) throw new Error("shared example missing");
    const status = { ...limited, confirmation };
    const draft = limited.draft as { id: string; time_limit: unknown };
    vi.mocked(apiFetch).mockResolvedValueOnce(status);
    const job = await readURLStatus(draft.id, status.owner_context);
    expect(job.time_limit).toEqual(draft.time_limit);
    expect(parseURLServerTime(status, status.owner_context, "status")).toBe(limited.server_time);
    expect(parseURLConfirmation(status, status.owner_context)).toEqual(confirmation);
  });

  it("공유 예시 여덟 경우를 모두 확인한다", () => {
    expect(example.version).toBe("run-confirmation.examples.v1");
    expect(example.cases.map(({ name }) => name)).toEqual([
      "running_not_recorded",
      "completed",
      "disabled",
      "start_refused",
      "interrupted",
      "confirmation_integrity_failed",
      "unreadable",
      "not_product_install",
    ]);
  });

  it.each(example.cases)("공유 원문을 닫힌 파서로 읽는다: $name", ({ status }) => {
    expect(parseURLConfirmation(status, status.owner_context)).toEqual(status.confirmation);
  });

  it("상태 응답의 confirmation만 받고 초안 조회 응답에서는 거절한다", async () => {
    const status = example.cases.find(({ name }) => name === "running_not_recorded")?.status;
    if (!status) throw new Error("shared example missing");
    const { id, state } = status.draft as { id: string; state: string };
    vi.mocked(apiFetch).mockResolvedValueOnce(status).mockResolvedValueOnce(status);
    const job = await readURLStatus(id, status.owner_context);
    expect(job.state).toBe(state);
    expect(job).not.toHaveProperty("confirmation");
    await expect(readURLReceipt("create-1", status.owner_context)).rejects.toMatchObject({
      status: 422,
      message: "invalid_contract",
    });
  });

  const completed = example.cases.find(({ name }) => name === "completed");
  it.each([
    ["모르는 record", { record: "pending", status: null, reason: null }],
    ["기록 없음인데 상태", { record: "absent", status: "disabled", reason: null }],
    ["못 읽음인데 사유", { record: "unreadable", status: null, reason: "x" }],
    ["조회 상태를 결과 상태로", { record: "recorded", status: "unreadable", reason: null }],
    ["모르는 결과 상태", { record: "recorded", status: "maybe", reason: null }],
    ["사유 모양", { record: "recorded", status: "disabled", reason: "Not Wired" }],
    ["칸 빠짐", { record: "recorded", status: "completed" }],
    ["모르는 칸", { record: "recorded", status: "completed", reason: null, extra: 1 }],
  ])("계약 밖이면 회차 상태째 받지 않는다: %s", (_name, confirmation) => {
    if (!completed) throw new Error("completed example missing");
    const raw = { ...completed.status, confirmation };
    expect(() => parseURLConfirmation(raw, completed.status.owner_context)).toThrow();
  });
});
