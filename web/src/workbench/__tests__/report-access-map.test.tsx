import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError, apiFetch } from "@/lib/diagnosis/api";
import { running } from "@/lib/diagnosis/diagnosis-url-fixtures";

import { resetUrl, useUrl } from "../live/urlDiagnosis";
import { AccessMapPanel } from "../views/live/UrlAccessMapLive";

// 보고서 '접근통제 지도' 탭(#683): 보고서는 <AccessMapPanel active={보고서 화면 && 지도 탭} /> 을 그린다.
// 패널의 경쟁·선택·revision 처리는 access-map-view.test.tsx(지도 화면)와 같은 코드라 거기서 본다. 여기는 탭 조건과 오류 뜻.
// 제품 판정 목록(#801)은 이 시험 범위가 아니다 — 제품 관리 중계가 아닌 서버처럼(404) 구획을 두지 않게 한다.
// vi.fn 이 아니어서 resetAllMocks 에도 그대로다
vi.mock("../live/productVerdicts", async (original) => {
  const real = await original<typeof import("../live/productVerdicts")>();
  const { ApiError: NotInstalled } = await import("@/lib/diagnosis/api");
  return {
    ...real,
    fetchProductVerdicts: async () => {
      throw new NotInstalled(404, "not found");
    },
  };
});
// 제품 접근통제 지도(product-access-map)도 이 시험 범위가 아니다 — 제품 관리 중계가 아닌 서버처럼(404) 구획을 두지 않는다
vi.mock("../live/productAccessMap", async (original) => {
  const real = await original<typeof import("../live/productAccessMap")>();
  const { ApiError: NotInstalled } = await import("@/lib/diagnosis/api");
  const notInstalled = async (): Promise<never> => {
    throw new NotInstalled(404, "not found");
  };
  return { ...real, fetchProductAccessMap: notInstalled, getProductAccessMap: notInstalled };
});
vi.mock("@/lib/diagnosis/api", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/api")>()),
  apiFetch: vi.fn(),
}));

const f = vi.mocked(apiFetch);
/** product-report-graph-read.md 지도 DTO 모양의 합성 응답(비민감) — 칸 하나. */
const apiMap = () => ({
  task_id: 42,
  run_ref: "execution-42",
  reset_generation: "reset-1",
  axis: "horizontal",
  policy_version: `kg-fixture/v1:map-snapshot:${"a".repeat(64)}`,
  cells: [
    {
      principal: "reader",
      owner_ref: "owner",
      resource_ref: "document:one",
      resource_label: "document:one",
      state: "suspected",
      oracle_value: null,
      applicable: true,
      record_id: null,
      case_ref: null,
      observed_at: null,
      reason_codes: [],
      canary_fields: [],
      projection_error: null,
      policy_check: "unknown",
      identity_check: "unknown",
      data_check: "unknown",
      same_resource: "unknown",
      owner_normal_data: "unknown",
      cross_normal_data: "unknown",
      nontrivial_data_match: "unknown",
      reflection_excluded: "unknown",
      template_excluded: "unknown",
    },
  ],
});
function deferred() {
  let resolve: (v: unknown) => void = () => undefined;
  const promise = new Promise<unknown>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

beforeEach(() => {
  vi.resetAllMocks();
  resetUrl();
  useUrl.setState({ job: running, verified: true });
});
afterEach(cleanup);

describe("보고서 '접근통제 지도' 탭 (AccessMapPanel)", () => {
  it("다른 탭이 보이는 동안은 읽지 않고, 지도 탭으로 오면 이 점검의 저장 지도를 읽는다", async () => {
    f.mockResolvedValue(apiMap());
    const v = render(<AccessMapPanel active={false} />);
    expect(f).not.toHaveBeenCalled();
    v.rerender(<AccessMapPanel active />);
    expect(await screen.findByRole("table", { name: /격자/ })).toBeInTheDocument();
    expect(f).toHaveBeenCalledWith("/api/tasks/42/access-map", expect.objectContaining({ cache: "no-store" }));
    expect(f).toHaveBeenCalledTimes(1);
    // 운영자 전용 고지는 보고서 탭에서도 같다(내보내기 · 공유 · 모델 입력 금지)
    expect(screen.getByText(/운영자 전용/)).toBeInTheDocument();
    expect(screen.queryByText(/연결 전|예시/)).toBeNull();
  });

  it("409 는 KG 미등록 · 실행 전 등의 뜻으로 보이고 빈 격자 · 예시로 대신하지 않는다", async () => {
    f.mockRejectedValue(new ApiError(409, "kg_unregistered"));
    render(<AccessMapPanel active />);
    // 1차 리허설 F7: 제품 회차는 늘 409라 오류 경고 대신 안내(note)로 보인다. 뜻(KG 미등록 · 실행 전 · 세대 무효)은 그대로
    expect(await screen.findByText(/KG 미등록 · 실행 전 · 세대 무효/)).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.queryByRole("table")).toBeNull();
    expect(screen.queryByText(/예시/)).toBeNull();
  });

  it("503 은 그대로(재조회 안내) 보이고, 다시 읽기는 같은 task 를 다시 부른다", async () => {
    f.mockRejectedValueOnce(new ApiError(503, "x")).mockResolvedValueOnce(apiMap());
    render(<AccessMapPanel active />);
    expect(await screen.findByRole("alert")).toHaveTextContent("HTTP 503");
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "다시 읽기" })));
    expect(await screen.findByRole("table", { name: /격자/ })).toBeInTheDocument();
    expect(f).toHaveBeenCalledTimes(2);
    expect(f.mock.calls.every(([path]) => path === "/api/tasks/42/access-map")).toBe(true);
  });

  it("탭을 떠났다 돌아오면 지난 격자를 버리고 새로 읽는다 — 새 조회 실패를 이전 격자로 가리지 않는다", async () => {
    f.mockResolvedValueOnce(apiMap());
    const v = render(<AccessMapPanel active />);
    expect(await screen.findByRole("table", { name: /격자/ })).toBeInTheDocument();
    const later = deferred();
    f.mockReturnValueOnce(later.promise as never);
    v.rerender(<AccessMapPanel active={false} />);
    v.rerender(<AccessMapPanel active />);
    expect(screen.queryByRole("table")).toBeNull();
    expect(screen.getByText("불러오는 중…")).toBeInTheDocument();
    await act(async () => later.resolve(Promise.reject(new ApiError(403, "x"))));
    expect(await screen.findByRole("alert")).toHaveTextContent(/권한을 확인하지 못했습니다/);
    expect(screen.queryByRole("table")).toBeNull();
  });
});
