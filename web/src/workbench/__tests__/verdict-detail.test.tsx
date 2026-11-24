// 판정 상세(후보 selector, finding-verdict-detail): 파서는 서버가 만든 응답(server-golden: server readFindingVerdictDetail 로
// 뽑은 실제 JSON)을 모두 받고, 서버가 만들 수 없는 값은 응답 전체로 거절한다(#689). 화면은 목록 줄과 같은 규칙으로
// 봉인 표시 · 사유 문구 · 값 숨김을 보인다. 연결된 판정이 없으면 그리지 않는다('판정 없음' · '취약점 없음' 아님).

import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/diagnosis/api", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/api")>()),
  apiFetch: vi.fn(),
}));

import { ApiError, apiFetch } from "@/lib/diagnosis/api";

import { candidateIdOf, isProductVerdictsShapeError, parseVerdictDetail } from "../live/productVerdicts";
import { VerdictDetailCard } from "../views/live/VerdictDetailCard";
import golden from "./verdict-detail.server-golden.json";

type Json = Record<string, unknown>;
type Case = Json & { task_id: string; selection: { id: string }; record: Json | null; view: Json; ids: Json };
const cases = golden as unknown as Record<string, Case>;
const clone = <T,>(v: T): T => JSON.parse(JSON.stringify(v)) as T;
const parse = (raw: Case) => parseVerdictDetail(raw, raw.task_id, raw.selection.id);

beforeEach(() => vi.resetAllMocks());
afterEach(cleanup);

describe("parseVerdictDetail", () => {
  it("서버가 만든 응답 8가지를 모두 받고 목록 행과 같은 모양으로 바꾼다", () => {
    expect(Object.keys(cases).sort()).toEqual([
      "confirmed_sealed",
      "inclusion_failed",
      "integrity_failed",
      "no_link",
      "policy_missing",
      "seal_not_recorded",
      "undecidable_no_evidence",
      "undecidable_reasons",
    ]);
    const row = (name: string) => {
      const d = parse(cases[name]);
      if (d.availability !== "recorded") throw new Error(name);
      return d;
    };
    expect(parse(cases.no_link)).toEqual({ availability: "no_link" });
    expect(row("confirmed_sealed").row).toMatchObject({
      recordStatus: "verified",
      oracleValue: "confirmed",
      resourceType: "document",
      evidenceCalls: 1,
      evidenceSeal: { status: "confirmed", reason: null },
    });
    expect(row("seal_not_recorded").row.evidenceSeal).toEqual({ status: "unconfirmed", reason: "seal_not_recorded" });
    expect(row("policy_missing").row.recordStatus).toBe("policy_source_unavailable");
    expect(row("inclusion_failed").row).toEqual({
      verdictId: cases.inclusion_failed.ids.verdict_id,
      recordStatus: "record_integrity_failed",
      evidenceSeal: { status: "inclusion_failed", reason: null },
    });
    expect(row("integrity_failed").row).toMatchObject({ recordStatus: "record_integrity_failed", evidenceSeal: null });
    const und = row("undecidable_reasons");
    expect(und.missingRequirements).toEqual(["identity_check", "data_check"]);
    expect(und.row).toMatchObject({
      oracleValue: "undecidable",
      productReasonCodes: ["product_identity_unverified"],
      evidenceSeal: { status: "unconfirmed", reason: "evidence_checkpoint_missing" },
    });
    expect(row("undecidable_no_evidence").row.evidenceSeal).toBeNull();
  });

  const broken: Record<string, [string, (o: Case) => void]> = {
    "다른 작업의 응답": ["confirmed_sealed", (o) => (o.task_id = "999")],
    "다른 후보의 응답": ["confirmed_sealed", (o) => ((o.selection as Json).id = "999")],
    "모르는 칸": ["confirmed_sealed", (o) => (o.extra = 1)],
    "finding 이 붙은 제품 판정": ["confirmed_sealed", (o) => (o.finding = { id: "1", triage_status: null })],
    "후보가 수동 검토 필요가 아님": ["confirmed_sealed", (o) => ((o.candidate as Json).verification = "confirmed")],
    "판정이 이 후보를 참조하지 않음": ["confirmed_sealed", (o) => ((o.record as Json).candidate_refs = ["1"])],
    "표시 판정이 원값과 다름": ["undecidable_reasons", (o) => (o.view.display_verdict = "confirmed")],
    "빠진 확정 조건이 검사와 다름": ["undecidable_reasons", (o) => (o.view.missing_requirements = ["data_check"])],
    "값이 보이는데 봉인 포함 실패": [
      "confirmed_sealed",
      (o) => (o.evidence_seal = { status: "inclusion_failed", reason: null }),
    ],
    "근거 호출이 없는데 봉인 표시": [
      "undecidable_no_evidence",
      (o) => (o.evidence_seal = { status: "confirmed", reason: null }),
    ],
    "근거 호출이 있는데 봉인 표시 없음": ["confirmed_sealed", (o) => (o.evidence_seal = null)],
    "모르는 오라클 사유": ["confirmed_sealed", (o) => ((o.record as Json).reason_codes = ["made_up"])],
    "확정인데 세부 사유": [
      "confirmed_sealed",
      (o) => ((o.record as Json).product_reason_codes = ["product_request_timeout"]),
    ],
    "원본 미확인인데 충분성 주장": ["policy_missing", (o) => (o.view.confirmation_evidence_complete = true)],
    "숨긴 판정에 정책 출처": [
      "integrity_failed",
      (o) => (o.policy_source = clone(cases.confirmed_sealed.policy_source)),
    ],
    "연결 없음인데 판정 ID": ["no_link", (o) => (o.ids.verdict_id = "1")],
  };
  it.each(Object.entries(broken))("%s → 깨진 응답", (_, [name, mutate]) => {
    const raw = clone(cases[name]);
    mutate(raw);
    let error: unknown;
    try {
      // 요청한 작업 · 후보는 바꾸기 전 값이다
      parseVerdictDetail(raw, cases[name].task_id, cases[name].selection.id);
    } catch (e) {
      error = e;
    }
    expect(isProductVerdictsShapeError(error)).toBe(true);
  });

  it("보고서 줄 ID 는 후보 줄만 판정 상세로 읽는다", () => {
    expect(candidateIdOf("candidate-12")).toBe("12");
    expect(candidateIdOf("finding-12")).toBeNull();
    expect(candidateIdOf("verdict-12")).toBeNull();
    expect(candidateIdOf("candidate-012")).toBeNull();
  });
});

describe("VerdictDetailCard", () => {
  function serve(body: unknown) {
    vi.mocked(apiFetch).mockImplementation(async () => {
      if (body instanceof Error) throw body;
      return body as never;
    });
  }
  const show = (raw: Case) => {
    serve(raw);
    return render(<VerdictDetailCard taskId={raw.task_id} entryId={`candidate-${raw.selection.id}`} />);
  };

  it("후보를 참조하는 판정을 목록과 같은 규칙으로 보인다(봉인 확인됨)", async () => {
    const raw = cases.confirmed_sealed;
    show(raw);
    const card = await screen.findByRole("region", { name: "확정 단계 판정" });
    expect(vi.mocked(apiFetch).mock.calls[0][0]).toBe(
      `/api/tasks/${raw.task_id}/finding-verdict-detail?candidate_id=${raw.selection.id}`,
    );
    expect(card).toHaveTextContent("확정");
    expect(card).toHaveTextContent("document");
    expect(card).toHaveTextContent("검사: 정책 예 · 신원 예 · 데이터 예 · 근거 호출 1건");
    expect(card).toHaveTextContent("판정 사유 정책상 교차 계정 접근 금지");
    expect(within(card).getByTitle("policy_deny_resolved")).toBeInTheDocument();
    expect(card).toHaveTextContent("봉인 확인됨");
    expect(card).toHaveTextContent("확정 판정 · 검토 대상 취약점");
  });

  it("봉인 미확인이면 값은 두고 사유 라벨을 붙인다", async () => {
    show(cases.seal_not_recorded);
    const card = await screen.findByRole("region", { name: "확정 단계 판정" });
    expect(card).toHaveTextContent("document");
    expect(card).toHaveTextContent("봉인 미확인 · 봉인 확인 기록 없음 · 판정 값은 저장된 그대로");
  });

  it("봉인 포함 실패면 값을 숨긴다", async () => {
    show(cases.inclusion_failed);
    const card = await screen.findByRole("region", { name: "확정 단계 판정" });
    expect(card).toHaveTextContent("값 숨김");
    expect(card).toHaveTextContent("봉인 포함 실패 · 값 숨김");
    expect(card).not.toHaveTextContent("document");
    // 판정 값 딱지(확정)도 없다
    expect(within(card).queryByText("확정")).toBeNull();
  });

  it("판단 불가는 사유 · 세부 사유 · 모자란 검사를 한국어로 보인다", async () => {
    show(cases.undecidable_reasons);
    const card = await screen.findByRole("region", { name: "확정 단계 판정" });
    expect(card).toHaveTextContent("판단 불가");
    expect(card).toHaveTextContent("판정 사유 계정 신원을 확인하지 못함");
    expect(card).toHaveTextContent("세부 사유 계정 신원 확인 실패");
    expect(card).toHaveTextContent("확정에 모자란 검사: 신원 검사 · 데이터 검사");
    expect(card).not.toHaveTextContent("product_identity_unverified");
    expect(card).toHaveTextContent("봉인 미확인 · 근거 체크포인트 없음");
  });

  it("당시 정책 원본을 못 읽었으면 값은 두고 그 표시를 붙인다", async () => {
    show(cases.policy_missing);
    const card = await screen.findByRole("region", { name: "확정 단계 판정" });
    expect(card).toHaveTextContent("당시 원본 미확인");
    expect(card).toHaveTextContent("document");
  });

  it("연결된 판정이 없거나(no_link) 경로가 없으면(404) 그리지 않는다", async () => {
    serve(cases.no_link);
    const a = render(
      <VerdictDetailCard taskId={cases.no_link.task_id} entryId={`candidate-${cases.no_link.selection.id}`} />,
    );
    await waitFor(() => expect(apiFetch).toHaveBeenCalled());
    expect(screen.queryByRole("region", { name: "확정 단계 판정" })).toBeNull();
    a.unmount();
    serve(new ApiError(404, "verdict_detail_not_found"));
    render(<VerdictDetailCard taskId="2" entryId="candidate-5" />);
    await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(2));
    expect(screen.queryByRole("region", { name: "확정 단계 판정" })).toBeNull();
  });

  it("후보 줄이 아니면 부르지 않는다", () => {
    render(<VerdictDetailCard taskId="2" entryId="finding-5" />);
    expect(apiFetch).not.toHaveBeenCalled();
  });

  it("읽기 실패 · 422 는 '판정 없음'으로 접지 않고 알린다", async () => {
    serve(new ApiError(422, "verdict_detail_integrity_failed"));
    render(<VerdictDetailCard taskId="2" entryId="candidate-5" />);
    expect(await screen.findByRole("alert")).toHaveTextContent("판정 기록을 확인하지 못해 표시하지 않습니다.");
    expect(screen.getByRole("button", { name: "다시 읽기" })).toBeInTheDocument();
  });
});
