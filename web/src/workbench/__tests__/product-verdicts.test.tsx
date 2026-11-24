// 제품 판정 목록(#801·#806): 파서는 서버가 만든 응답(server-golden: #806 server 조립 함수로 뽑은 실제 JSON)을 모두 받고,
// 서버가 만들 수 없는 값은 응답 전체로 거절한다(#689 방어 파서 원칙). 화면은 평가 판정과 섞지 않고, 빈 목록을 '취약점 없음'으로
// 읽히지 않게, 진위 실패 행은 값을 숨긴 채로 보인다.

import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/diagnosis/api", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/api")>()),
  apiFetch: vi.fn(),
}));

import { ApiError, apiFetch } from "@/lib/diagnosis/api";

import { clockOf } from "../live/map";
import { isProductVerdictsShapeError, parseProductVerdicts } from "../live/productVerdicts";
import { ProductVerdictReport } from "../views/live/productVerdictViews";
import golden from "./product-verdicts.server-golden.json";

type Json = Record<string, unknown>;
const cases = golden as unknown as Record<string, Json & { task_id: string; items: Json[] }>;
const clone = <T,>(v: T): T => JSON.parse(JSON.stringify(v)) as T;

beforeEach(() => vi.resetAllMocks());
afterEach(cleanup);

describe("parseProductVerdicts", () => {
  it("서버가 만든 응답 11가지를 모두 받는다", () => {
    expect(Object.keys(cases).sort()).toEqual([
      "disabled_no_labels",
      "empty",
      "mixed_integrity",
      "revisions_differ",
      "seal_checkpoint_missing",
      "seal_confirmed",
      "seal_inclusion_failed",
      "seal_not_recorded",
      "source_not_found",
      "unreadable",
      "verified",
    ]);
    for (const [name, raw] of Object.entries(cases)) {
      const got = parseProductVerdicts(raw, raw.task_id);
      expect(got.taskId, name).toBe(raw.task_id);
      expect(got.items, name).toHaveLength(raw.items.length);
    }
    const mixed = parseProductVerdicts(cases.mixed_integrity, cases.mixed_integrity.task_id);
    expect(mixed.items.map((r) => r.recordStatus)).toEqual(["verified", "record_integrity_failed"]);
    expect(mixed.policySource).toMatchObject({ ownerAccountLabel: "tester-a", crossAccountLabel: "tester-b" });
    expect(mixed.confirmation).toEqual({ status: "completed", reason: null });
    expect(parseProductVerdicts(cases.empty, cases.empty.task_id).confirmation).toBeNull();
    expect(parseProductVerdicts(cases.source_not_found, cases.source_not_found.task_id).policySource).toBeNull();
    expect(
      parseProductVerdicts(cases.revisions_differ, cases.revisions_differ.task_id).policySource?.targetApprovalRevision,
    ).toBeNull();
  });

  // 봉인 확인(E1, e3-product-verdict-store §7): 서버가 실제로 낸 세 단계를 그대로 받고, 근거 호출이 없는 판정은 null 이다.
  // 포함 실패는 진위 실패라 값이 숨겨진 행으로 온다.
  it("서버가 만든 봉인 확인 표시를 받는다", () => {
    const seals = (name: string) =>
      parseProductVerdicts(cases[name], cases[name].task_id).items.map((r) => [r.recordStatus, r.evidenceSeal]);
    expect(seals("seal_confirmed")).toEqual([
      ["verified", { status: "confirmed", reason: null }],
      ["verified", null],
    ]);
    expect(seals("seal_not_recorded")).toEqual([
      ["verified", { status: "unconfirmed", reason: "seal_not_recorded" }],
      ["verified", null],
    ]);
    expect(seals("seal_checkpoint_missing")).toEqual([
      ["verified", { status: "unconfirmed", reason: "evidence_checkpoint_missing" }],
      ["verified", null],
    ]);
    expect(seals("seal_inclusion_failed")).toEqual([
      ["record_integrity_failed", { status: "inclusion_failed", reason: null }],
      ["verified", null],
    ]);
  });

  it.each([
    ["근거 없는 판정에 봉인 표시", 1, { status: "confirmed", reason: null }],
    ["근거 없는 판정에 미확인", 1, { status: "unconfirmed", reason: "seal_not_recorded" }],
    // 서버는 근거가 있는 판정에 늘 확인됨 · 미확인 중 하나를 준다(null 을 만들 수 없다)
    ["근거 있는 판정에 null", 0, null],
  ])("거절: %s", (_name, index, seal) => {
    const o = clone(cases.seal_confirmed);
    expect(() => parseProductVerdicts(clone(o), o.task_id)).not.toThrow();
    (o.items[index] as Json).evidence_seal = seal;
    let error: unknown = null;
    try {
      parseProductVerdicts(o, o.task_id);
    } catch (e) {
      error = e;
    }
    expect(isProductVerdictsShapeError(error)).toBe(true);
  });

  // 하나씩 서버가 만들 수 없는 값으로 바꾼다. 어느 것이든 응답 전체가 거절돼야 한다(일부만 그리지 않음)
  const base = () => clone(cases.mixed_integrity);
  const shown = (o: Json) => (o.items as Json[])[0];
  const hidden = (o: Json) => (o.items as Json[])[1];
  const bad: Record<string, (o: Json) => void> = {
    "모르는 최상위 칸": (o) => {
      o.extra = 1;
    },
    "최상위 칸 빠짐": (o) => {
      delete o.confirmation;
    },
    "형식 판": (o) => {
      o.format = "jindo.workbench-product-verdicts/v2";
    },
    "평가 판정 출처": (o) => {
      o.source_kind = "eval";
    },
    "다른 작업": (o) => {
      o.task_id = "999";
    },
    제목: (o) => {
      o.title = "판정";
    },
    "정책 참조 접두": (o) => {
      (o.policy_source as Json).ref = "kg-job-synthetic";
    },
    "정책 digest": (o) => {
      (o.policy_source as Json).digest_sha256 = "ABC";
    },
    "라벨 하나만": (o) => {
      (o.policy_source as Json).cross_account_label = null;
    },
    "같은 라벨": (o) => {
      (o.policy_source as Json).cross_account_label = "tester-a";
    },
    "모르는 확정 단계 상태": (o) => {
      (o.confirmation as Json).status = "skipped";
    },
    "unreadable 에 사유": (o) => {
      o.confirmation = { status: "unreadable", reason: "io_error" };
    },
    "사유 형식": (o) => {
      o.confirmation = { status: "disabled", reason: "Declaration Missing" };
    },
    "행 상한 초과": (o) => {
      o.items = Array.from({ length: 17 }, (_, i) => ({ ...hidden(o), verdict_id: String(i + 1) }));
    },
    "판정 id 중복": (o) => {
      hidden(o).verdict_id = shown(o).verdict_id;
    },
    "모르는 행 칸": (o) => {
      shown(o).extra = 1;
    },
    "모르는 진위 상태": (o) => {
      shown(o).record_status = "stale";
    },
    "숨긴 행에 값": (o) => {
      hidden(o).oracle_value = "confirmed";
    },
    "숨긴 행에 후보": (o) => {
      hidden(o).candidate_refs = ["candidate-1"];
    },
    "모르는 판정 값": (o) => {
      shown(o).oracle_value = "vulnerable";
    },
    "검사 값": (o) => {
      (shown(o).checks as Json).data = "maybe";
    },
    "자원 참조가 종류와 다름": (o) => {
      shown(o).resource_ref = `other:${"b".repeat(64)}`;
    },
    "모르는 오라클 사유": (o) => {
      shown(o).oracle_reason_codes = ["looks_vulnerable"];
    },
    "관찰 시각 정밀도": (o) => {
      shown(o).observed_at = "2026-11-18T09:00:00Z";
    },
    "근거 호출 수 음수": (o) => {
      shown(o).evidence_calls = -1;
    },
    "봉인 칸 문자열": (o) => {
      shown(o).evidence_seal = "sealed";
    },
    "봉인 모르는 상태": (o) => {
      shown(o).evidence_seal = { status: "sealed", reason: null };
    },
    "봉인 사유 칸 빠짐": (o) => {
      shown(o).evidence_seal = { status: "confirmed" };
    },
    "봉인 모르는 칸": (o) => {
      shown(o).evidence_seal = { status: "confirmed", reason: null, head: 28 };
    },
    "확인됨에 사유": (o) => {
      shown(o).evidence_seal = { status: "confirmed", reason: "seal_not_recorded" };
    },
    "미확인에 사유 없음": (o) => {
      shown(o).evidence_seal = { status: "unconfirmed", reason: null };
    },
    "미확인 모르는 사유": (o) => {
      shown(o).evidence_seal = { status: "unconfirmed", reason: "io_error" };
    },
    "보이는 행에 포함 실패": (o) => {
      shown(o).evidence_seal = { status: "inclusion_failed", reason: null };
    },
    "숨긴 행에 확인됨": (o) => {
      hidden(o).evidence_seal = { status: "confirmed", reason: null };
    },
    "숨긴 행에 미확인": (o) => {
      hidden(o).evidence_seal = { status: "unconfirmed", reason: "seal_not_recorded" };
    },
    "확정인데 검사 아니오": (o) => {
      (shown(o).checks as Json).identity = "no";
    },
    "확정인데 근거 0": (o) => {
      shown(o).evidence_calls = 0;
    },
    "확정인데 제품 사유": (o) => {
      shown(o).product_reason_codes = ["product_write_record_unavailable"];
    },
    "정책 출처 없이 확인함": (o) => {
      o.policy_source = null;
    },
  };
  it.each(Object.entries(bad))("거절: %s", (_name, mutate) => {
    const o = base();
    // 변조 전 기준은 통과한다(공허한 거절이 아님)
    expect(() => parseProductVerdicts(clone(o), o.task_id as string)).not.toThrow();
    mutate(o);
    let error: unknown = null;
    try {
      parseProductVerdicts(o, "2");
    } catch (e) {
      error = e;
    }
    expect(isProductVerdictsShapeError(error)).toBe(true);
  });
});

function serve(body: unknown) {
  vi.mocked(apiFetch).mockImplementation(async () => {
    if (body instanceof Error) throw body;
    return body as never;
  });
}

describe("ProductVerdictReport", () => {
  it("평가 목록과 따로, 출처 · 확정 단계 결과와 함께 행을 보이고 진위 실패 행은 값을 숨긴다", async () => {
    serve(cases.mixed_integrity);
    render(<ProductVerdictReport taskId="2" active />);
    const sec = await screen.findByRole("region", { name: "제품 확정 단계 판정" });
    await waitFor(() => expect(within(sec).getByText("2건")).toBeInTheDocument());
    expect(vi.mocked(apiFetch).mock.calls[0][0]).toBe("/api/tasks/2/product-verdicts");
    // 목록은 짧게(사용자 11/23): 출처 딱지만, 확정 단계가 정상으로 마쳤으면 그 말은 두지 않는다
    expect(sec).toHaveTextContent("제품 정책 선언 declaration-job-synthetic");
    expect(sec).not.toHaveTextContent("대상 승인 판");
    expect(sec).not.toHaveTextContent("섞지 않습니다");
    expect(sec).not.toHaveTextContent("확정 단계 마침");
    const [ok, failed] = within(sec).getAllByRole("listitem");
    expect(ok).toHaveTextContent("확정");
    expect(ok).toHaveTextContent("document");
    // 검사가 다 '예'면 검사 줄과 판정 번호는 두지 않고 관찰 시각만
    expect(ok).not.toHaveTextContent("검사:");
    expect(ok).not.toHaveTextContent("판정 #");
    expect(ok).toHaveTextContent(`관찰 ${clockOf("2026-11-18T09:00:00.000000Z")}`);
    expect(ok).toHaveTextContent("확정 판정 · 검토 대상 취약점 1건");
    expect(ok).not.toHaveTextContent("수동 검토 필요 그대로");
    expect(failed).toHaveTextContent("값 숨김");
    expect(failed).toHaveTextContent("판정 기록 무결성 확인 실패 · 값 숨김");
    expect(failed).not.toHaveTextContent("note");
  });

  it("봉인 확인 표시: 확인됨 · 미확인은 값과 함께, 포함 실패는 값을 숨기고 그 사유를 보인다", async () => {
    serve(cases.seal_confirmed);
    const a = render(<ProductVerdictReport taskId="2" active />);
    let sec = await screen.findByRole("region", { name: "제품 확정 단계 판정" });
    await waitFor(() => expect(within(sec).getAllByRole("listitem")).toHaveLength(2));
    let [first, second] = within(sec).getAllByRole("listitem");
    // 목록에서 봉인 확인됨은 따로 두지 않는다(계약 e3-product-verdict-store: 확인됨은 '그대로'). 판정 상세는 보인다
    expect(first).not.toHaveTextContent("봉인");
    expect(first).toHaveTextContent("document");
    expect(second).not.toHaveTextContent("봉인");
    a.unmount();
    serve(cases.seal_not_recorded);
    const b = render(<ProductVerdictReport taskId="2" active />);
    sec = await screen.findByRole("region", { name: "제품 확정 단계 판정" });
    await waitFor(() => expect(within(sec).getAllByRole("listitem")).toHaveLength(2));
    [first] = within(sec).getAllByRole("listitem");
    expect(first).toHaveTextContent("봉인 미확인 · 봉인 확인 기록 없음 · 판정 값은 저장된 그대로");
    expect(first).toHaveTextContent("document");
    b.unmount();
    serve(cases.seal_checkpoint_missing);
    const c = render(<ProductVerdictReport taskId="2" active />);
    sec = await screen.findByRole("region", { name: "제품 확정 단계 판정" });
    await waitFor(() => expect(within(sec).getAllByRole("listitem")).toHaveLength(2));
    [first] = within(sec).getAllByRole("listitem");
    expect(first).toHaveTextContent("봉인 미확인 · 근거 체크포인트 없음 · 판정 값은 저장된 그대로");
    c.unmount();
    serve(cases.seal_inclusion_failed);
    render(<ProductVerdictReport taskId="2" active />);
    sec = await screen.findByRole("region", { name: "제품 확정 단계 판정" });
    await waitFor(() => expect(within(sec).getAllByRole("listitem")).toHaveLength(2));
    [first] = within(sec).getAllByRole("listitem");
    expect(first).toHaveTextContent("값 숨김");
    expect(first).toHaveTextContent("봉인 포함 실패 · 값 숨김");
    expect(first).not.toHaveTextContent("document");
  });

  it("판정 사유 · 세부 사유는 한국어 문구로, 코드는 마우스를 올렸을 때만, 표에 없는 코드는 '서버 사유 있음'", async () => {
    const raw = clone(cases.verified);
    const item = raw.items[1] as Json;
    item.oracle_value = "undecidable";
    item.oracle_reason_codes = ["policy_deny_resolved", "data_unverifiable"];
    item.product_reason_codes = ["product_request_timeout", "product_future_reason"];
    serve(raw);
    render(<ProductVerdictReport taskId={raw.task_id} active />);
    const sec = await screen.findByRole("region", { name: "제품 확정 단계 판정" });
    await waitFor(() => expect(within(sec).getAllByRole("listitem")).toHaveLength(2));
    const second = within(sec).getAllByRole("listitem")[1];
    expect(second).toHaveTextContent("판정 사유 정책상 교차 계정 접근 금지 · 데이터를 확인할 수 없음");
    expect(second).toHaveTextContent("세부 사유 요청 시간 초과 · 서버 사유 있음");
    for (const code of [
      "policy_deny_resolved",
      "data_unverifiable",
      "product_request_timeout",
      "product_future_reason",
    ]) {
      expect(second).not.toHaveTextContent(code);
      expect(within(second).getByTitle(code)).toBeInTheDocument();
    }
  });

  it("빈 목록은 '취약점 없음'으로 읽히지 않고, 확정 단계 결과가 없으면 꺼졌다고 하지 않는다", async () => {
    serve(cases.empty);
    render(<ProductVerdictReport taskId="4" active />);
    const sec = await screen.findByRole("region", { name: "제품 확정 단계 판정" });
    await waitFor(() =>
      expect(sec).toHaveTextContent("제품 판정 없음 · 확정 단계 결과 미확인 · 취약점이 없다는 뜻이 아닙니다"),
    );
    expect(sec).not.toHaveTextContent("꺼짐");
  });

  it("확정 단계 꺼짐 · 사유와 당시 원본 미확인을 보인다", async () => {
    serve(cases.disabled_no_labels);
    const { unmount } = render(<ProductVerdictReport taskId="2" active />);
    const off = await screen.findByRole("region", { name: "제품 확정 단계 판정" });
    await waitFor(() => expect(off).toHaveTextContent("확정 단계 꺼짐 · 정책 선언 없음"));
    // 사유 코드 원문은 글에 두지 않고 마우스를 올렸을 때만(title)
    expect(off).not.toHaveTextContent("declaration_missing");
    expect(within(off).getByTitle("declaration_missing")).toHaveTextContent("정책 선언 없음");
    unmount();
    serve(cases.source_not_found);
    render(<ProductVerdictReport taskId="2" active />);
    const sec = await screen.findByRole("region", { name: "제품 확정 단계 판정" });
    await waitFor(() => expect(within(sec).getAllByRole("listitem")).toHaveLength(2));
    expect(sec).toHaveTextContent("제품 정책 선언 동결본 미확인");
    expect(within(sec).getAllByText(/당시 원본 미확인 · 당시 정책 동결본을 읽지 못해/)).toHaveLength(2);
  });

  it("제품 관리 중계가 아니면(404) 구획을 두지 않고, 422 · 형식 오류는 '판정 없음'으로 접지 않는다", async () => {
    serve(new ApiError(404, "not found"));
    const { container, unmount } = render(<ProductVerdictReport taskId="2" active />);
    await waitFor(() => expect(apiFetch).toHaveBeenCalled());
    await waitFor(() => expect(container).toBeEmptyDOMElement());
    unmount();
    serve(new ApiError(422, "product_verdicts_integrity_failed"));
    const r422 = render(<ProductVerdictReport taskId="2" active />);
    expect(await screen.findByRole("alert")).toHaveTextContent("상한을 넘어 표시하지 않습니다(잘라 보이지 않음)");
    expect(screen.queryByText(/제품 판정 없음/)).toBeNull();
    r422.unmount();
    const broken = clone(cases.verified);
    broken.source_kind = "eval";
    serve(broken);
    render(<ProductVerdictReport taskId="2" active />);
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "제품 판정 응답 형식을 확인하지 못해 표시하지 않습니다.",
    );
    expect(screen.queryByText(/제품 판정 없음/)).toBeNull();
  });

  it("탭이 안 보이거나 작업이 없으면 부르지 않는다", () => {
    serve(cases.verified);
    render(<ProductVerdictReport taskId="2" active={false} />);
    render(<ProductVerdictReport taskId={null} active />);
    expect(apiFetch).not.toHaveBeenCalled();
  });
});
