import { beforeEach, describe, expect, it, vi } from "vitest";

import { apiFetch } from "./api";
import { archiveTask, getTokenUsage } from "./engine";

vi.mock("./api", () => ({ apiFetch: vi.fn() }));
beforeEach(() => vi.resetAllMocks());

describe("ledger token contract", () => {
  it("uses the usage ledger for both profiles and UTC daily totals", async () => {
    vi.mocked(apiFetch).mockResolvedValue({
      by_profile: [
        {
          profile_name: "profile-a",
          calls: 2,
          tasks: 1,
          input_tokens: 10,
          output_tokens: 3,
          cache_read_tokens: 2,
          cache_write_tokens: 1,
        },
      ],
      daily: [
        { profile_name: "profile-a", date: "2026-10-30", input_tokens: 4, output_tokens: 1, cache_read_tokens: 2 },
        { profile_name: "profile-b", date: "2026-10-30", input_tokens: 6, output_tokens: 2, cache_read_tokens: 1 },
      ],
    });
    const usage = await getTokenUsage();
    expect(apiFetch).toHaveBeenCalledTimes(1);
    expect(apiFetch).toHaveBeenCalledWith("/api/tokens/usage?days=365");
    expect(usage.byProfile[0]).toMatchObject({ profileName: "profile-a", calls: 2, input: 10, output: 3 });
    expect(usage.daily).toEqual([{ day: "2026-10-30", input: 10, output: 3, cacheRead: 3 }]);
  });
});

describe("archive acceptance", () => {
  it("preserves the archive id/state instead of treating 202 as completion", async () => {
    vi.mocked(apiFetch).mockResolvedValue({ id: 42, task_id: 17, state: "archive_queued" });
    expect(await archiveTask("17")).toEqual({ id: "42", taskId: "17", state: "archive_queued" });
  });
  it("rejects an acceptance receipt bound to another task", async () => {
    vi.mocked(apiFetch).mockResolvedValue({ id: 42, task_id: 18, state: "archive_queued" });
    await expect(archiveTask("17")).rejects.toThrow(/작업/);
  });
});
