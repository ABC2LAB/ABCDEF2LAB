import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/diagnosis/api", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/api")>()),
  apiFetch: vi.fn(),
}));

import { ApiError, apiFetch } from "@/lib/diagnosis/api";

import { confirmationPlanOf } from "../live/confirmationPlan";
import type { StoredDeclaration } from "../live/declaration";
import { parseResetResponse } from "../live/resetConfirmation";
import { ResetConfirmationBox } from "../views/live/ResetConfirmationBox";

const ZERO = "0001-01-01T00:00:00Z";
const REF = "b".repeat(64);
const STATEMENT = "확정 단계가 남긴 시험 객체를 지우거나 대상을 초기 상태로 되돌렸습니다.";
// 서버 ProductResetState 모양(diagnosisprep). 빈 시각은 Go time.Time 영 값으로 온다
const none = { reset: { reset_required: false, last_writes_at: ZERO, confirmed_at: ZERO, statement: STATEMENT } };
const required = {
  reset: {
    reset_required: true,
    last_writes_job: "job-7",
    last_writes_at: "2026-11-20T01:02:03Z",
    last_writes_objects: 2,
    last_writes_ref: REF,
    confirmed_at: ZERO,
    statement: STATEMENT,
  },
};
const confirmed = {
  reset: {
    ...required.reset,
    reset_required: false,
    confirmed_by: "operator-1",
    confirmed_at: "2026-11-20T02:00:00Z",
  },
};

beforeEach(() => vi.resetAllMocks());
afterEach(cleanup);

describe("parseResetResponse — 서버 검사와 같은 조건만 받는다", () => {
  it("쓰기 기록 없음 · 확인 필요 · 확인됨을 읽는다", () => {
    expect(parseResetResponse(none)).toMatchObject({ resetRequired: false, lastWritesJob: "", lastWritesAt: "" });
    expect(parseResetResponse(required)).toMatchObject({
      resetRequired: true,
      lastWritesObjects: 2,
      lastWritesRef: REF,
    });
    expect(parseResetResponse(confirmed)).toMatchObject({ resetRequired: false, confirmedBy: "operator-1" });
  });

  it.each([
    ["reset 칸 없음", {}],
    ["모르는 칸", { reset: { ...required.reset, object_ids: ["x"] } }],
    ["기록 없음인데 확인 필요", { reset: { ...none.reset, reset_required: true } }],
    ["기록 참조 모양 틀림", { reset: { ...required.reset, last_writes_ref: "job-7" } }],
    ["객체 0개", { reset: { ...required.reset, last_writes_objects: 0 } }],
    ["객체 상한 초과", { reset: { ...required.reset, last_writes_objects: 17 } }],
    ["확인 필요인데 확인자 있음", { reset: { ...required.reset, confirmed_by: "operator-1" } }],
    ["확인됨인데 시각 없음", { reset: { ...confirmed.reset, confirmed_at: ZERO } }],
    ["진술이 문자열 아님", { reset: { ...required.reset, statement: 1 } }],
  ])("%s → 깨진 응답", (_, res) => {
    expect(() => parseResetResponse(res)).toThrow(ApiError);
  });
});

describe("ResetConfirmationBox", () => {
  const path = "/api/product/targets/target-1/reset-confirmation";

  it("쓰기 기록이 없으면 아무것도 그리지 않는다", async () => {
    vi.mocked(apiFetch).mockResolvedValueOnce(none);
    const onState = vi.fn();
    render(<ResetConfirmationBox targetId="target-1" disabled={false} onState={onState} />);
    await vi.waitFor(() => expect(onState).toHaveBeenCalledWith(expect.objectContaining({ resetRequired: false })));
    expect(screen.queryByRole("region", { name: "대상 리셋 확인" })).toBeNull();
    expect(vi.mocked(apiFetch).mock.calls[0][0]).toBe(path);
  });

  it("확인이 필요하면 서버 진술에 동의해야 보내고, 본 기록 참조를 싣는다", async () => {
    vi.mocked(apiFetch).mockResolvedValueOnce(required).mockResolvedValueOnce(confirmed);
    const onConfirmed = vi.fn();
    render(<ResetConfirmationBox targetId="target-1" disabled={false} onConfirmed={onConfirmed} />);
    const box = await screen.findByRole("region", { name: "대상 리셋 확인" });
    expect(box).toHaveTextContent("시험 객체 2개");
    expect(box).toHaveTextContent(STATEMENT);
    const button = screen.getByRole("button", { name: "리셋 확인" });
    expect(button).toBeDisabled();
    fireEvent.click(screen.getByLabelText("리셋 진술 동의"));
    await act(async () => fireEvent.click(button));
    const [sent, init] = vi.mocked(apiFetch).mock.calls[1];
    expect(sent).toBe(path);
    expect(init?.method).toBe("POST");
    expect(JSON.parse(String(init?.body))).toEqual({ statement_confirmed: true, writes_ref: REF });
    expect(await screen.findByText(/리셋 확인됨/)).toBeInTheDocument();
    expect(onConfirmed).toHaveBeenCalledTimes(1);
  });

  it("그사이 기록이 바뀌면 확인한 것으로 보이지 않고 새 기록을 다시 읽는다", async () => {
    const newer = { reset: { ...required.reset, last_writes_job: "job-8", last_writes_ref: "c".repeat(64) } };
    vi.mocked(apiFetch)
      .mockResolvedValueOnce(required)
      .mockRejectedValueOnce(new ApiError(409, "reset_record_changed"))
      .mockResolvedValueOnce(newer)
      .mockResolvedValueOnce(confirmed);
    const onConfirmed = vi.fn();
    render(<ResetConfirmationBox targetId="target-1" disabled={false} onConfirmed={onConfirmed} />);
    await screen.findByRole("region", { name: "대상 리셋 확인" });
    fireEvent.click(screen.getByLabelText("리셋 진술 동의"));
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "리셋 확인" })));
    expect(await screen.findByText(/쓰기 기록이 바뀌었습니다/)).toBeInTheDocument();
    await vi.waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(3));
    expect(onConfirmed).not.toHaveBeenCalled();
    expect(screen.queryByText(/리셋 확인됨/)).toBeNull();
    // 동의는 다시 체크해야 한다(새 기록에 다시 동의)
    const agreement = screen.getByLabelText("리셋 진술 동의");
    expect(agreement).not.toBeChecked();
    await vi.waitFor(() => expect(agreement).toBeEnabled());
    fireEvent.click(agreement);
    const retry = screen.getByRole("button", { name: "리셋 확인" });
    expect(retry).toBeEnabled();
    await act(async () => fireEvent.click(retry));
    expect(JSON.parse(String(vi.mocked(apiFetch).mock.calls[3][1]?.body))).toEqual({
      statement_confirmed: true,
      writes_ref: newer.reset.last_writes_ref,
    });
    expect(onConfirmed).toHaveBeenCalledTimes(1);
    expect(await screen.findByText(/리셋 확인됨/)).toBeInTheDocument();
  });

  it("확인 POST가 503이면 다시 읽은 뒤 잠금이 풀려 재시도할 수 있다", async () => {
    vi.mocked(apiFetch)
      .mockResolvedValueOnce(required)
      .mockRejectedValueOnce(new ApiError(503, "product_control_unavailable"))
      .mockResolvedValueOnce(required);
    render(<ResetConfirmationBox targetId="target-1" disabled={false} />);
    await screen.findByRole("region", { name: "대상 리셋 확인" });
    fireEvent.click(screen.getByLabelText("리셋 진술 동의"));
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "리셋 확인" })));
    expect(await screen.findByRole("alert")).toHaveTextContent("리셋 확인을 남기지 못했습니다. 다시 시도해 주세요.");
    const agreement = screen.getByLabelText("리셋 진술 동의");
    await vi.waitFor(() => expect(agreement).toBeEnabled());
    expect(agreement).not.toBeChecked();
    fireEvent.click(agreement);
    expect(screen.getByRole("button", { name: "리셋 확인" })).toBeEnabled();
  });

  it("상태를 못 읽으면 '필요 없음'으로 숨기지 않고 알린다", async () => {
    vi.mocked(apiFetch).mockRejectedValueOnce(new ApiError(503, "product_control_unavailable"));
    const onState = vi.fn();
    render(<ResetConfirmationBox targetId="target-1" disabled={false} onState={onState} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("리셋 확인 상태를 읽지 못했습니다");
    expect(onState).toHaveBeenCalledWith(null);
  });
});

describe("confirmationPlanOf — 시작 화면 확정 단계 안내", () => {
  const declaration = (over: Partial<StoredDeclaration> = {}): StoredDeclaration =>
    ({
      identity: { path: "/me", subjectIdPath: "data.username" },
      types: [
        {
          type: "book",
          sharing: "owner_only",
          ownerOnlyFields: ["secret"],
          create: { markerField: "secret" },
        },
        { type: "shared_note", sharing: "shared", ownerOnlyFields: [], create: { markerField: "" } },
      ],
      roles: { ownerAccountLabel: "tester-a", crossAccountLabel: "tester-b", accountsSamePrivilege: true },
      targetRef: "target-1",
      targetApprovalRevision: "1",
      declaredBy: "operator-1",
      declaredAt: "2026-11-20T00:00:00Z",
      ...over,
    }) as unknown as StoredDeclaration;

  it("현재 선언 · 두 역할 · 등급 확인 · 시험할 종류가 있으면 안내한다", () => {
    expect(confirmationPlanOf({ state: "current", declaration: declaration() })).toEqual({
      types: 1,
      ownerAccount: "tester-a",
      crossAccount: "tester-b",
    });
  });

  it.each([
    ["선언 없음", { state: "none" } as const],
    ["무효 선언", { state: "invalidated", declaration: declaration(), reason: "target_reapproved" } as const],
    ["역할 없음", { state: "current", declaration: declaration({ roles: undefined }) } as const],
    [
      "등급 확인 없음",
      {
        state: "current",
        declaration: declaration({
          roles: { ownerAccountLabel: "tester-a", crossAccountLabel: "tester-b", accountsSamePrivilege: false },
        }),
      } as const,
    ],
    ["시험할 종류 없음", { state: "current", declaration: declaration({ types: [] }) } as const],
  ])("%s → 안내하지 않음", (_, load) => {
    expect(confirmationPlanOf(load)).toBeNull();
  });
});
