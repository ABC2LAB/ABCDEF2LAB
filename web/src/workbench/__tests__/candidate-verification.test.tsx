import "@testing-library/jest-dom/vitest";

import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { readFileSync } from "node:fs";
import { resolve } from "node:path";

vi.mock("@/lib/diagnosis/api", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/api")>()),
  apiFetch: vi.fn(),
}));

import { ApiError, apiFetch } from "@/lib/diagnosis/api";

import { parseVerificationPage, readVerifications } from "../live/candidateVerification";
import { SelectedVerificationCard } from "../views/live/candidateVerificationView";

// 계약 합성 fixture(docs/contracts/fixtures/selected-candidate-llm-verification.json) — 상태별 부분 응답이라 바깥 칸을 붙여 읽는다
const fixture = JSON.parse(
  readFileSync(resolve(process.cwd(), "../docs/contracts/fixtures/selected-candidate-llm-verification.json"), "utf8"),
) as { history_examples: ({ case: string } & Record<string, unknown>)[] };
const example = (name: string) => {
  const { case: _c, ...rest } = fixture.history_examples.find((x) => x.case === name) as Record<string, unknown>;
  return rest;
};
const D = (c: string) => c.repeat(64);
const verification = (name: string, over: Record<string, unknown> = {}) => ({
  id: "101",
  candidate_id: "7",
  request_id: "ui-request-0001",
  method: "llm/v1",
  original_evidence_digest: D("9"),
  original_evidence_version: 1,
  original_run_ref: "run-1",
  requested_at: "2026-11-26T01:00:00Z",
  ...example(name),
  ...over,
});
const page = (rows: unknown[] = [], over: Record<string, unknown> = {}) => ({
  task_id: "42",
  candidate_id: "7",
  evidence_digest: D("9"),
  automatic: { status: "insufficient", reason: "selected_verification_required" },
  stored_evidence: { state: "available", observation_count: 1 },
  verifications: rows,
  truncated: false,
  ...over,
});

const calls = () =>
  vi.mocked(apiFetch).mock.calls.map((c) => [c[0], (c[1] as RequestInit | undefined)?.method ?? "GET"]);
const bodyOf = (i: number) => JSON.parse(String((vi.mocked(apiFetch).mock.calls[i][1] as RequestInit).body));
/** GET 은 차례대로 다른 쪽을, POST 는 빈 성공을 */
function serve(...pages: unknown[]) {
  let n = 0;
  vi.mocked(apiFetch).mockImplementation(async (_path, init) => {
    if ((init as RequestInit | undefined)?.method === "POST") return {};
    const p = pages[Math.min(n, pages.length - 1)];
    n += 1;
    return p;
  });
}
const settle = () => act(() => new Promise((r) => setTimeout(r, 0)));

afterEach(cleanup);
// 중괄호로 감싼다: mockReset() 은 mock 자신(함수)을 돌려주고, beforeEach 가 돌려준 함수는 시험 뒤 정리 함수로 불린다
beforeEach(() => {
  vi.mocked(apiFetch).mockReset();
});

describe("선택 검증 파서(계약 fixture)", () => {
  it("fixture 15가지 상태를 모두 읽고, 판정은 완료된 결과에서만 받는다", () => {
    for (const ex of fixture.history_examples) {
      const p = parseVerificationPage(page([verification(ex.case)]), "42", "7");
      const v = p.verifications[0];
      expect(v.latest.process_state).toBe((ex.latest as { process_state: string }).process_state);
      if (v.latest.process_state !== "completed") expect(v.latest.verdict).toBeNull();
    }
    const done = parseVerificationPage(page([verification("model_supported")]), "42", "7").verifications[0];
    expect(done.latest.verdict).toBe("model_supported");
    expect(done.final?.verdict).toBe("model_supported");
    expect(done.final?.sources).toHaveLength(2);
    const plan = parseVerificationPage(page([verification("awaiting_approval_authenticated")]), "42", "7")
      .verifications[0];
    expect(plan.plan).toMatchObject({ account: true, auth_preparation: { method: "POST", exact_path: "/login" } });
    expect(plan.plan?.actions[1]).toMatchObject({ kind: "normal_control", query: [["view", "baseline"]] });
  });

  it("진행 중 상태에 판정이 붙어 오면 버린다(확정처럼 보이지 않게), 다른 후보 · 작업 응답은 받지 않는다", () => {
    const odd = verification("running", {
      latest: { process_state: "running", verdict: "model_supported", reason: "x" },
    });
    expect(parseVerificationPage(page([odd]), "42", "7").verifications[0].latest.verdict).toBeNull();
    expect(() => parseVerificationPage(page(), "42", "8")).toThrow();
    expect(() => parseVerificationPage(page(), "43", "7")).toThrow();
    expect(() => parseVerificationPage(page([verification("requested", { candidate_id: "9" })]), "42", "7")).toThrow();
  });

  it("이력은 제품 경로에서 읽고 before 로 앞 쪽을 넘긴다", async () => {
    vi.mocked(apiFetch).mockResolvedValue(page());
    await readVerifications("42", "7");
    await readVerifications("42", "7", "100");
    expect(calls().map((c) => c[0])).toEqual([
      "/api/product/tasks/42/candidates/7/verifications",
      "/api/product/tasks/42/candidates/7/verifications?before=100",
    ]);
    await expect(readVerifications("42", "x")).rejects.toThrow();
  });
});

describe("취약점 상세 선택 검증 칸", () => {
  const card = () => screen.getByRole("region", { name: "선택 검증" });

  it("후보가 아니거나 404 · 선택 검증 대상 아님(기록 없음)이면 두지 않는다", async () => {
    const a = render(<SelectedVerificationCard taskId="42" entryId="finding-1" />);
    expect(a.container).toBeEmptyDOMElement();
    expect(apiFetch).not.toHaveBeenCalled();
    a.unmount();
    vi.mocked(apiFetch).mockRejectedValue(new ApiError(404, "candidate_not_found"));
    const b = render(<SelectedVerificationCard taskId="42" entryId="candidate-7" />);
    await settle();
    expect(b.container).toBeEmptyDOMElement();
    b.unmount();
    // 정상 종료가 아니거나 선택 검증 판이 아닌 회차(조회도 409)
    vi.mocked(apiFetch).mockRejectedValue(new ApiError(409, "diagnosis_not_completed"));
    const b2 = render(<SelectedVerificationCard taskId="42" entryId="candidate-7" />);
    await settle();
    expect(b2.container).toBeEmptyDOMElement();
    b2.unmount();
    vi.mocked(apiFetch).mockReset();
    vi.mocked(apiFetch).mockResolvedValue(page([], { automatic: { status: "confirmed", reason: "rule_matched" } }));
    const c = render(<SelectedVerificationCard taskId="42" entryId="candidate-7" />);
    await settle();
    expect(c.container).toBeEmptyDOMElement();
  });

  it("그 밖의 읽기 실패는 숨기지 않고 까닭 · [다시 읽기]를 둔다", async () => {
    vi.mocked(apiFetch).mockImplementation(async () => {
      throw new ApiError(503, "verification_storage_unavailable");
    });
    render(<SelectedVerificationCard taskId="42" entryId="candidate-7" />);
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("선택 검증 기록을 읽지 못했어요 (HTTP 503)");
    expect(within(alert).getByRole("button", { name: "다시 읽기" })).toBeInTheDocument();
  });

  it("검증 요청: 원본 근거 판본 · llm/v1 · 요청 키를 보내고 다시 읽는다", async () => {
    serve(page(), page([verification("requested")]));
    render(<SelectedVerificationCard taskId="42" entryId="candidate-7" />);
    fireEvent.click(await screen.findByRole("button", { name: "이 후보 검증 요청" }));
    await waitFor(() => expect(within(card()).getByText("요청됨")).toBeInTheDocument());
    const post = calls().findIndex((c) => c[1] === "POST");
    expect(calls()[post][0]).toBe("/api/product/tasks/42/candidates/7/verifications");
    expect(bodyOf(post)).toMatchObject({ evidence_digest: D("9"), method: "llm/v1" });
    expect(bodyOf(post).request_id).toMatch(/^[A-Za-z0-9][A-Za-z0-9._-]{7,127}$/);
    // 끝나지 않은 요청이 있으면 새 요청 버튼을 두지 않는다
    expect(within(card()).queryByRole("button", { name: /검증 요청/ })).toBeNull();
    expect(within(card()).getByRole("button", { name: "검증 취소" })).toBeInTheDocument();
  });

  it("분석 준비: 모델 주장 · 업무 질문 답 저장 · 실행 계획 만들기(분석 판본에 묶음)", async () => {
    serve(page([verification("analysis_ready")]));
    render(<SelectedVerificationCard taskId="42" entryId="candidate-7" />);
    expect(await screen.findByText("합성 응답에서 내부 정보가 보임")).toBeInTheDocument();
    expect(within(card()).getByRole("list", { name: "검증 단계" })).toBeInTheDocument();
    const save = within(card()).getByRole("button", { name: "답 저장" });
    expect(save).toBeDisabled();
    fireEvent.change(within(card()).getByRole("textbox", { name: "질문 1 답" }), {
      target: { value: " 내부 문서예요 " },
    });
    fireEvent.click(save);
    await waitFor(() => expect(calls().some((c) => c[1] === "POST")).toBe(true));
    let post = calls().findIndex((c) => c[1] === "POST");
    expect(calls()[post][0]).toBe("/api/product/tasks/42/candidates/7/verifications/101/answers");
    expect(bodyOf(post)).toEqual({ analysis_digest: D("a"), question_index: 1, answer: "내부 문서예요" });
    await settle();
    fireEvent.click(within(card()).getByRole("button", { name: "실행 계획 만들기" }));
    await waitFor(() => expect(calls().filter((c) => c[1] === "POST")).toHaveLength(2));
    post = calls().length - 1 - [...calls()].reverse().findIndex((c) => c[1] === "POST");
    expect(calls()[post][0]).toBe("/api/product/tasks/42/candidates/7/verifications/101/plan");
    expect(bodyOf(post)).toEqual({ analysis_digest: D("a") });
  });

  it("저장 관찰로 판정할 수 있어 계획이 409 면 실패가 아니라 안내로", async () => {
    serve(page([verification("analysis_ready")]));
    render(<SelectedVerificationCard taskId="42" entryId="candidate-7" />);
    await screen.findByText("합성 응답에서 내부 정보가 보임");
    vi.mocked(apiFetch).mockImplementation(async (_p, init) => {
      if ((init as RequestInit | undefined)?.method === "POST")
        throw new ApiError(409, "stored_evidence_assessment_pending");
      return page([verification("stored_assessing")]);
    });
    fireEvent.click(within(card()).getByRole("button", { name: "실행 계획 만들기" }));
    expect(await within(card()).findByRole("status")).toHaveTextContent(/새 요청 계획 없이 판정을 진행해요/);
    expect(await within(card()).findByText("모델 판정 중")).toBeInTheDocument();
  });

  it("계획 승인 대기: 보낼 요청 · 정상 대조 · 로그인 준비 · 상한을 보이고, 승인은 계획 판본으로 결정한다", async () => {
    serve(page([verification("awaiting_approval_authenticated")]));
    render(<SelectedVerificationCard taskId="42" entryId="candidate-7" />);
    const reqs = await screen.findByRole("list", { name: "보낼 요청" });
    expect(within(reqs).getByText("GET /documents?view=public")).toBeInTheDocument();
    // kind=normal_control 딱지(목적 글도 같은 말이라 딱지를 따로 찾는다)
    expect(
      within(reqs)
        .getAllByText("정상 대조")
        .some((el) => /tag/.test(el.className)),
    ).toBe(true);
    expect(within(card()).getByText("POST /login")).toBeInTheDocument();
    expect(within(card()).getByText("요청 3개 · 90초")).toBeInTheDocument();
    expect(within(card()).getByText("등록된 시험 계정으로")).toBeInTheDocument();
    fireEvent.click(within(card()).getByRole("button", { name: "승인하고 보내기" }));
    await waitFor(() => expect(calls().some((c) => c[1] === "POST")).toBe(true));
    const post = calls().findIndex((c) => c[1] === "POST");
    expect(calls()[post][0]).toBe("/api/product/tasks/42/candidates/7/verifications/101/decision");
    expect(bodyOf(post)).toEqual({ plan_digest: "ab".repeat(32), decision: "approved" });
  });

  it("취소는 계획이 있으면 그 판본을 같이, 없으면 빈 본문으로", async () => {
    serve(page([verification("awaiting_approval")]));
    render(<SelectedVerificationCard taskId="42" entryId="candidate-7" />);
    fireEvent.click(await screen.findByRole("button", { name: "검증 취소" }));
    await waitFor(() => expect(calls().some((c) => c[1] === "POST")).toBe(true));
    let post = calls().findIndex((c) => c[1] === "POST");
    expect(calls()[post][0]).toMatch(/\/verifications\/101\/cancel$/);
    expect(bodyOf(post)).toEqual({ plan_digest: D("b") });
    cleanup();
    vi.mocked(apiFetch).mockReset();
    serve(page([verification("requested")]));
    render(<SelectedVerificationCard taskId="42" entryId="candidate-7" />);
    fireEvent.click(await screen.findByRole("button", { name: "검증 취소" }));
    await waitFor(() => expect(calls().some((c) => c[1] === "POST")).toBe(true));
    post = calls().findIndex((c) => c[1] === "POST");
    expect(bodyOf(post)).toEqual({});
  });

  it("완료: 모델 판정 딱지는 프로그램 확정과 다른 글로, 판정 근거와 함께 · 실패 코드는 사람 글로", async () => {
    serve(page([verification("model_supported"), verification("cancelled", { id: "100" })]));
    render(<SelectedVerificationCard taskId="42" entryId="candidate-7" />);
    expect(await screen.findByText("관찰 확인됨 · 모델 판정")).toBeInTheDocument();
    expect(within(card()).getByText(/프로그램 확정\(E③\)과 따로 세요/)).toBeInTheDocument();
    expect(within(card()).queryByText("확정")).toBeNull();
    expect(within(card()).getByText("내부 정보 제거")).toBeInTheDocument();
    expect(within(card()).getByText("이전 검증 1건")).toBeInTheDocument();
    // 끝난 뒤엔 다시 요청할 수 있고, 서버가 거절하면 까닭을 글로
    vi.mocked(apiFetch).mockImplementation(async (_p, init) => {
      if ((init as RequestInit | undefined)?.method === "POST") throw new ApiError(409, "candidate_evidence_changed");
      return page([verification("model_supported"), verification("cancelled", { id: "100" })]);
    });
    fireEvent.click(within(card()).getByRole("button", { name: "다시 검증 요청" }));
    expect(await within(card()).findByRole("alert")).toHaveTextContent("후보 근거가 바뀌었어요");
  });
});
