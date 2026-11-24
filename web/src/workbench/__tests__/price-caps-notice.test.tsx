// 끝난 점검의 단가 상한 안내: 점검 종료 신호가 결과 저장보다 먼저 와도(ended=true, result_ready=false) 안내가 비지 않고,
// 결과 저장이 확인되면(result_ready=true) 그때 읽어 보인다(#889 리뷰).

import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/diagnosis/request-approvals", async (importOriginal) => {
  const real = await importOriginal<typeof import("@/lib/diagnosis/request-approvals")>();
  return { ...real, readDiagnosisRun: vi.fn() };
});

import * as approvals from "@/lib/diagnosis/request-approvals";

import { PriceCapsNotice } from "../views/live/PriceCapsNotice";

const readDiagnosisRun = vi.mocked(approvals.readDiagnosisRun);

const record = {
  version: 1,
  task_id: 7,
  saved_at: "2026-11-23T10:38:08Z",
  run: null,
  execution: "NOT_RUN",
  model: {
    requests: 1,
    errors: 1,
    input_tokens: 0,
    output_tokens: 0,
    cost_usd_micros: 0,
    provider_slug: "primeintellect/fp8",
    model: "qwen/qwen3.6-35b-a3b",
    blocked: true,
    price_caps: {
      source: "install_fallback",
      failure_reason: "requote_unavailable",
      max_input_price_usd_micros_per_million: 200000,
      max_output_price_usd_micros_per_million: 1500000,
      install_max_input_price_usd_micros_per_million: 200000,
      install_max_output_price_usd_micros_per_million: 1500000,
      queried_at: "2026-11-23T10:11:14Z",
      providers: [
        {
          provider_slug: "primeintellect/fp8",
          provider_name: "PrimeIntellect",
          status: "unknown",
          input_usd_micros_per_million: null,
          output_usd_micros_per_million: null,
        },
      ],
    },
  },
} as unknown as Awaited<ReturnType<typeof approvals.readDiagnosisRun>>;

beforeEach(() => {
  readDiagnosisRun.mockReset();
  readDiagnosisRun.mockResolvedValue(record);
});
afterEach(() => {
  cleanup();
});

describe("PriceCapsNotice", () => {
  it("결과 저장 확인 전에는 읽지 않고, 확인되면 읽어 경고를 보인다", async () => {
    const view = render(<PriceCapsNotice taskId="7" ready={false} />);
    await new Promise((r) => setTimeout(r, 0));
    expect(readDiagnosisRun).not.toHaveBeenCalled();
    expect(screen.queryByRole("region", { name: "단가 상한" })).toBeNull();

    view.rerender(<PriceCapsNotice taskId="7" ready />);
    await waitFor(() => expect(screen.getByRole("region", { name: "단가 상한" })).toBeTruthy());
    expect(readDiagnosisRun).toHaveBeenCalledTimes(1);
    expect(screen.getByText(/설치 때 승인한 상한 입력 \$0\.2 · 출력 \$1\.5/)).toBeTruthy();
  });

  it("저장 확인 전 404를 빈 안내로 굳히지 않는다", async () => {
    readDiagnosisRun.mockRejectedValueOnce(new Error("404"));
    const view = render(<PriceCapsNotice taskId="7" ready />);
    await waitFor(() => expect(readDiagnosisRun).toHaveBeenCalledTimes(1));
    expect(screen.queryByRole("region", { name: "단가 상한" })).toBeNull();
    // 저장 확인이 한 번 내려갔다가 다시 올라오면(다시 연 점검 등) 다시 읽는다.
    view.rerender(<PriceCapsNotice taskId="7" ready={false} />);
    view.rerender(<PriceCapsNotice taskId="7" ready />);
    await waitFor(() => expect(screen.getByRole("region", { name: "단가 상한" })).toBeTruthy());
    expect(readDiagnosisRun).toHaveBeenCalledTimes(2);
  });
});
