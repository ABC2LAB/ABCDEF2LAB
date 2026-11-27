import { afterEach, describe, expect, it, vi } from "vitest";

import { ApiError, apiFetch } from "./api";
import { URL_BUSY_REASONS, urlBusyReason } from "./diagnosis-url";

// 새 시작 거절(409 busy)의 사유 코드(docs/url-diagnosis-preparation.md '새 시작 거절 사유').
describe("새 시작 거절 사유", () => {
  afterEach(() => vi.unstubAllGlobals());

  async function refused(body: unknown, status = 409): Promise<unknown> {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(JSON.stringify(body), { status })),
    );
    return apiFetch("/api/diagnoses/v2/drafts/job-1/start", { method: "POST", body: "{}" }).catch((e: unknown) => e);
  }

  it.each(URL_BUSY_REASONS)("서버 사유를 그대로 읽는다: %s", async (reason) => {
    const cause = await refused({ version: "diagnosis-api.v2", error: "busy", busy_reason: reason });
    expect(cause).toBeInstanceOf(ApiError);
    expect(urlBusyReason(cause)).toBe(reason);
  });

  it("구 서버(사유 없음)·목록 밖 사유·busy 아닌 오류는 사유를 만들지 않는다", async () => {
    expect(urlBusyReason(await refused({ version: "diagnosis-api.v2", error: "busy" }))).toBeUndefined();
    expect(
      urlBusyReason(await refused({ version: "diagnosis-api.v2", error: "busy", busy_reason: "job_running" })),
    ).toBeUndefined();
    expect(
      urlBusyReason(
        await refused({ version: "diagnosis-api.v2", error: "budget_exhausted", busy_reason: "run_active" }),
      ),
    ).toBeUndefined();
    expect(
      urlBusyReason(await refused({ version: "diagnosis-api.v2", error: "busy", busy_reason: "run_active" }, 503)),
    ).toBeUndefined();
  });
});
