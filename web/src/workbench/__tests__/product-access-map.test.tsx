// 제품 접근통제 지도(docs/contracts/product-access-map.md): 파서는 서버가 만든 응답(server-golden: server
// assembleProductAccessMap 으로 뽑은 실제 JSON)을 모두 받고, 서버가 만들 수 없는 값은 응답 전체로 거절한다(#689 방어 파서
// 원칙). 평가 지도 파서와 상태 표를 공유하지 않는다. 화면은 소유 칸을 A 조회 성공으로 읽히지 않게 두고(O1), 확정 단계 결과가
// 없으면 격자 대신 안내만 보인다(O2).

import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/diagnosis/api", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/api")>()),
  apiFetch: vi.fn(),
}));

import { ApiError, apiFetch } from "@/lib/diagnosis/api";
import { running } from "@/lib/diagnosis/diagnosis-url-fixtures";
import { buildCellResolver, checkAccessMapResponse, mapApiAccessMap } from "@/lib/diagnosis/kg";

import { isProductAccessMapShapeError, parseProductAccessMap } from "../live/productAccessMap";
import { resetUrl, useUrl } from "../live/urlDiagnosis";
import { ProductAccessMapGrid } from "../views/live/productAccessMapView";
import { AccessMapPanel } from "../views/live/UrlAccessMapLive";
import golden from "./product-access-map.server-golden.json";

type Json = Record<string, unknown>;
type Golden = Json & { task_id: string; cells: Json[]; resources: Json[]; principals: Json[] };
const cases = golden as unknown as Record<string, Golden>;
const clone = <T,>(v: T): T => JSON.parse(JSON.stringify(v)) as T;
const parse = (raw: Golden) => parseProductAccessMap(raw, raw.task_id);

beforeEach(() => vi.resetAllMocks());
afterEach(cleanup);

function serve(body: unknown) {
  vi.mocked(apiFetch).mockImplementation(async () => {
    if (body instanceof Error) throw body;
    return body as never;
  });
}

/** 셀 하나를 바꾼 복사본. index 는 cells 순서(종류 오름차순 × owner → cross). */
function withCell(name: string, index: number, change: (c: Json) => void): Golden {
  const raw = clone(cases[name]);
  change(raw.cells[index]);
  return raw;
}

function expectRejected(raw: unknown, taskId: string) {
  let err: unknown = null;
  try {
    parseProductAccessMap(raw, taskId);
  } catch (e) {
    err = e;
  }
  expect(isProductAccessMapShapeError(err)).toBe(true);
}

describe("parseProductAccessMap", () => {
  it("서버가 만든 응답 6가지를 모두 받는다", () => {
    expect(Object.keys(cases).sort()).toEqual([
      "completed",
      "completed_skip_mismatch",
      "disabled_reason",
      "hidden",
      "interrupted_suspected",
      "pending",
    ]);
    for (const [name, raw] of Object.entries(cases)) {
      const got = parse(raw);
      expect(got.cells, name).toHaveLength(raw.resources.length * 2);
      expect(got.owner, name).toBe("tester-a");
      expect(got.cross, name).toBe("tester-b");
    }
    const completed = parse(cases.completed);
    expect(completed.cells.map((c) => [c.role, c.resourceType, c.state, c.projectionError])).toEqual([
      ["owner", "document", "owner_control", null],
      ["cross", "document", "confirmed", null],
      ["owner", "note", "owner_control", null],
      ["cross", "note", "unknown", null],
      ["owner", "post", "owner_control", null],
      ["cross", "post", "na", null],
    ]);
    expect(completed.cells[3].verdict?.oracleValue).toBe("undecidable");
    expect(completed.cells[5]).toMatchObject({ applicable: false, naBasis: "declared_shared" });
    expect(parse(cases.pending).confirmation).toBeNull();
    expect(parse(cases.hidden).hiddenRecords).toBe(1);
    expect(parse(cases.disabled_reason).confirmation).toEqual({
      status: "disabled",
      reason: "account_roles_undeclared",
    });
  });

  it("다른 작업 · 다른 형식 · 평가 지도 응답을 받지 않는다", () => {
    expectRejected(cases.completed, "999");
    const fmt = clone(cases.completed);
    fmt.format = "jindo.workbench-product-verdicts/v1";
    expectRejected(fmt, fmt.task_id);
    const axis = clone(cases.completed);
    axis.axis = "vertical";
    expectRejected(axis, axis.task_id);
  });

  it("평가 지도 파서는 제품 지도 상태(owner_control · na)를 받지 않는다(파서 분리)", () => {
    // 평가 지도 모양은 다 맞추고 상태만 제품 값으로 바꾼다 — task_id 검사에서 먼저 던지지 않고 상태 검사까지 가게 한다
    const evalCell = {
      principal: "reader",
      owner_ref: "owner",
      resource_ref: "document:one",
      resource_label: "document:one",
      state: "unknown",
      oracle_value: null,
      applicable: true,
      record_id: null,
      case_ref: null,
      observed_at: null,
      policy_check: "unknown",
      identity_check: "unknown",
      data_check: "unknown",
      same_resource: "unknown",
      owner_normal_data: "unknown",
      cross_normal_data: "unknown",
      nontrivial_data_match: "unknown",
      reflection_excluded: "unknown",
      template_excluded: "unknown",
      reason_codes: [],
      canary_fields: [],
      projection_error: "observation_unavailable",
    };
    const evalBody = (state: string) => ({
      task_id: 41,
      run_ref: "execution-41",
      reset_generation: "reset-1",
      axis: "horizontal",
      policy_version: `kg-fixture/v1:map-snapshot:${"a".repeat(64)}`,
      cells: [{ ...evalCell, state }],
    });
    expect(() => checkAccessMapResponse(evalBody("unknown"), "41")).not.toThrow();
    for (const state of ["owner_control", "na"]) {
      // 평가 경로는 모르는 상태를 칸 단위로 fail-closed(판정 불가 · 미등록 state)로 내린다 — 제품 상태로 칠하지 않는다
      const map = mapApiAccessMap(checkAccessMapResponse(evalBody(state), "41"));
      const cell = buildCellResolver(map.cells)("reader", "document:one");
      expect(cell.decision, state).toBe("unknown");
      expect(cell.projectionError, state).toBe("observation_unavailable");
      const bare = mapApiAccessMap(
        checkAccessMapResponse({ ...evalBody(state), cells: [{ ...evalCell, state, projection_error: null }] }, "41"),
      );
      expect(buildCellResolver(bare.cells)("reader", "document:one"), state).toMatchObject({
        decision: "unknown",
        projectionError: `미등록 state: ${state}`,
      });
    }
    // 제품 응답 통째로는 평가 파서가 받지 않는다(형식 · 칸 모양이 다르다)
    const numeric = { ...clone(cases.completed), task_id: Number(cases.completed.task_id) };
    expect(() => checkAccessMapResponse(numeric, cases.completed.task_id)).toThrow();
  });

  it("confirmable ⇔ owner_only 를 두 방향 다 본다", () => {
    const a = clone(cases.completed);
    a.resources[0].confirmable = false; // 소유자 전용인데 확정 불가
    expectRejected(a, a.task_id);
    const b = clone(cases.completed);
    b.resources[2].confirmable = true; // 공유인데 확정 가능
    expectRejected(b, b.task_id);
  });

  it("축 모양: 계정 둘(owner → cross) · 라벨 다름 · 종류 오름차순 · 칸 전부 순서대로", () => {
    const swapped = clone(cases.completed);
    swapped.principals.reverse();
    expectRejected(swapped, swapped.task_id);
    const same = clone(cases.completed);
    same.principals[1].label = "tester-a";
    expectRejected(same, same.task_id);
    const order = clone(cases.completed);
    order.resources.reverse();
    expectRejected(order, order.task_id);
    const missing = clone(cases.completed);
    missing.cells.pop();
    expectRejected(missing, missing.task_id);
    const moved = clone(cases.completed);
    [moved.cells[0], moved.cells[1]] = [moved.cells[1], moved.cells[0]];
    expectRejected(moved, moved.task_id);
  });

  it("소유 칸은 판정 · 오류 · 적용 근거를 갖지 않는다", () => {
    expectRejected(
      withCell("completed", 0, (c) => {
        c.state = "confirmed";
      }),
      cases.completed.task_id,
    );
    expectRejected(
      withCell("completed", 0, (c) => {
        c.applicable = true;
      }),
      cases.completed.task_id,
    );
    expectRejected(
      withCell("completed", 0, (c) => {
        c.verdict_id = "1";
      }),
      cases.completed.task_id,
    );
  });

  it("공유 칸: na 는 declared_shared 근거와 함께만, 판정을 싣지 않는다", () => {
    expectRejected(
      withCell("completed", 5, (c) => {
        c.na_basis = null;
      }),
      cases.completed.task_id,
    );
    expectRejected(
      withCell("completed", 5, (c) => {
        c.applicable = true;
      }),
      cases.completed.task_id,
    );
    // 결과 파일이 없으면 건너뜀 대조를 하지 않으므로 na_evidence_mismatch 는 나올 수 없다
    expectRejected(
      withCell("pending", 5, (c) => {
        c.state = "unknown";
        c.na_basis = null;
        c.projection_error = "na_evidence_mismatch";
      }),
      cases.pending.task_id,
    );
    expect(parse(cases.completed_skip_mismatch).cells[5]).toMatchObject({
      state: "unknown",
      projectionError: "na_evidence_mismatch",
    });
  });

  it("시험 대상 칸: 오류는 서버 순서대로만(숨김 · 완료 누락 · 관측 없음)", () => {
    // 숨긴 행이 없으면 record_hidden 은 나올 수 없다
    expectRejected(
      withCell("completed_skip_mismatch", 1, (c) => {
        c.projection_error = "record_hidden";
      }),
      cases.completed_skip_mismatch.task_id,
    );
    // 숨긴 행이 있으면 판정 없는 칸은 record_hidden 이다
    expectRejected(
      withCell("hidden", 1, (c) => {
        c.projection_error = "record_missing_after_attempt";
      }),
      cases.hidden.task_id,
    );
    // 완료가 아니면 '완료했는데 누락'은 나올 수 없고, 완료면 '관측 없음'은 나올 수 없다
    expectRejected(
      withCell("interrupted_suspected", 1, (c) => {
        c.projection_error = "record_missing_after_attempt";
      }),
      cases.interrupted_suspected.task_id,
    );
    expectRejected(
      withCell("completed_skip_mismatch", 1, (c) => {
        c.projection_error = "observation_unavailable";
      }),
      cases.completed_skip_mismatch.task_id,
    );
    // 오류 칸은 판정 값을 싣지 않는다
    expectRejected(
      withCell("hidden", 1, (c) => {
        c.oracle_value = "confirmed";
      }),
      cases.hidden.task_id,
    );
  });

  it("판정 칸: 상태와 오라클 값이 맞아야 하고, 확정은 세 검사 yes 뿐", () => {
    expectRejected(
      withCell("completed", 1, (c) => {
        c.state = "suspected";
      }),
      cases.completed.task_id,
    );
    expectRejected(
      withCell("completed", 1, (c) => {
        (c.checks as Json).data = "no";
      }),
      cases.completed.task_id,
    );
    expectRejected(
      withCell("completed", 1, (c) => {
        c.oracle_value = "allowed";
      }),
      cases.completed.task_id,
    );
    expectRejected(
      withCell("completed", 1, (c) => {
        c.record_status = "policy_source_unavailable";
      }),
      cases.completed.task_id,
    );
    expectRejected(
      withCell("completed", 1, (c) => {
        c.evidence_seal = { status: "inclusion_failed", reason: null };
      }),
      cases.completed.task_id,
    );
    expectRejected(
      withCell("completed", 3, (c) => {
        c.state = "confirmed";
      }),
      cases.completed.task_id,
    );
  });
});

describe("ProductAccessMapGrid", () => {
  it("종류 × A/B 격자: 소유 칸은 중립 문구, 확정 · 판정 불가 · 공유 선언을 서버 상태대로(평가 지도와 같은 글)", async () => {
    serve(cases.completed);
    render(<ProductAccessMapGrid taskId={cases.completed.task_id} active />);
    const table = await screen.findByRole("table", { name: "자원 종류 × 소유 · 교차 계정 제품 접근통제 격자" });
    expect(within(table).getByRole("columnheader", { name: /tester-a\s*소유\(A\)/ })).toBeInTheDocument();
    expect(within(table).getByRole("columnheader", { name: /tester-b\s*교차\(B\)/ })).toBeInTheDocument();
    const doc = within(table).getByRole("row", { name: /document/ });
    expect(doc).toHaveTextContent("소유 계정(선언) · 판정 대상 아님");
    expect(doc).toHaveTextContent("취약 확정");
    // 근거 체크포인트는 있는데 봉인 확인 기록이 없으면 봉인 미확인을 칸에 보인다(지운 예전 표 시험이 보던 것)
    expect(doc.querySelector("[data-seal='unconfirmed']")).toHaveTextContent("봉인 미확인");
    // 소유 칸이 성공 · 정상처럼 보이지 않는다
    expect(doc).not.toHaveTextContent(/성공|정상|통과/);
    expect(within(table).getByRole("row", { name: /note/ })).toHaveTextContent("판정 불가");
    expect(within(table).getByRole("row", { name: /post/ })).toHaveTextContent("— (공유 선언)");
    const counts = screen.getByTestId("product-map-counts");
    expect(counts).toHaveTextContent("취약 확정 1");
    expect(counts.querySelector("[data-tally='undecidable']")).toHaveTextContent("판정 불가(저장 판정) 1");
    expect(counts.querySelector("[data-tally='no_verdict']")).toBeNull();
  });

  it("합계 칩은 저장된 판정 불가와 판정 없는 칸을 합치지 않는다", async () => {
    serve(cases.interrupted_suspected);
    render(<ProductAccessMapGrid taskId={cases.interrupted_suspected.task_id} active />);
    const counts = await screen.findByTestId("product-map-counts");
    expect(counts.querySelector("[data-tally='suspected']")).toHaveTextContent("취약 의심 1");
    expect(counts.querySelector("[data-tally='no_verdict']")).toHaveTextContent("판정 기록 · 근거 없음 1");
    expect(counts.querySelector("[data-tally='undecidable']")).toBeNull();
    // 관측 근거 없음 문구는 원인(확정 단계 미완료)을 추정하지 않는다
    const doc = screen.getByRole("row", { name: /document/ });
    expect(doc).toHaveTextContent("관측 근거 없음");
    expect(doc).not.toHaveTextContent("미완료");
  });

  it("확정 단계 결과가 없으면 격자 대신 안내만", async () => {
    serve(cases.pending);
    render(<ProductAccessMapGrid taskId={cases.pending.task_id} active />);
    expect(await screen.findByTestId("product-map-pending")).toHaveTextContent("확정 단계가 끝나면");
    expect(screen.queryByRole("table")).toBeNull();
    // 안내 화면에서도 다시 읽을 수 있다 — 확정 단계가 끝나면 같은 화면에서 격자로 바뀐다
    serve({ ...clone(cases.completed), task_id: cases.pending.task_id });
    screen.getByRole("button", { name: "제품 지도 다시 읽기" }).click();
    expect(await screen.findByRole("table", { name: /제품 접근통제 격자/ })).toBeInTheDocument();
    expect(apiFetch).toHaveBeenCalledTimes(2);
  });

  it("숨긴 행 건수와 칸의 '숨긴 판정일 수 있음'을 보이고, 판정 없음을 안전으로 칠하지 않는다", async () => {
    serve(cases.hidden);
    render(<ProductAccessMapGrid taskId={cases.hidden.task_id} active />);
    const table = await screen.findByRole("table", { name: /제품 접근통제 격자/ });
    expect(screen.getByRole("note")).toHaveTextContent("판정 기록 무결성 확인 실패 1건");
    expect(within(table).getByRole("row", { name: /document/ })).toHaveTextContent("숨긴 판정이 이 종류일 수 있음");
  });

  it("409 는 사유별 안내, 404 는 구획 없음, 형식 오류는 경고", async () => {
    serve(new ApiError(409, "product_access_map_roles_undeclared"));
    const a = render(<ProductAccessMapGrid taskId="2" active />);
    expect(await screen.findByRole("note")).toHaveTextContent("계정 역할이 없어");
    expect(screen.queryByRole("alert")).toBeNull();
    a.unmount();
    serve(new ApiError(404, "not found"));
    const b = render(<ProductAccessMapGrid taskId="2" active />);
    await waitFor(() => expect(apiFetch).toHaveBeenCalled());
    await waitFor(() => expect(b.container).toBeEmptyDOMElement());
    b.unmount();
    serve({ format: "nope" });
    render(<ProductAccessMapGrid taskId="2" active />);
    expect(await screen.findByRole("alert")).toHaveTextContent("응답 형식을 확인하지 못해");
  });

  it("탭이 안 보이거나 작업이 없으면 부르지 않는다", () => {
    serve(cases.completed);
    render(<ProductAccessMapGrid taskId="2" active={false} />);
    render(<ProductAccessMapGrid taskId={null} active />);
    expect(apiFetch).not.toHaveBeenCalled();
  });
});

describe("접근통제 지도 패널 연결", () => {
  it("평가 지도가 409(KG 미등록)여도 같은 task 의 제품 지도를 따로 보인다", async () => {
    resetUrl();
    useUrl.setState({ job: running, verified: true });
    const body = clone(cases.completed);
    body.task_id = running.task_id as string;
    vi.mocked(apiFetch).mockImplementation(async (path) => {
      if (path === `/api/tasks/${running.task_id}/access-map`) throw new ApiError(409, "kg_unregistered");
      if (path === `/api/tasks/${running.task_id}/product-access-map`) return body as never;
      throw new Error(`unexpected ${String(path)}`);
    });
    render(<AccessMapPanel active />);
    expect(await screen.findByRole("table", { name: /제품 접근통제 격자/ })).toBeInTheDocument();
    // 409는 오류 경고가 아니라 안내로 보인다(1차 리허설 F7) — 뜻(KG 미등록)은 그대로
    expect(screen.getByText(/KG 미등록/)).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.queryByRole("table", { name: "신원 × 자원 접근통제 격자" })).toBeNull();
    // 예전 제품 판정 목록 경로는 지도 화면에서 부르지 않는다
    expect(vi.mocked(apiFetch).mock.calls.some(([p]) => String(p).endsWith("/product-verdicts"))).toBe(false);
  });
});
