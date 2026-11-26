import "@testing-library/jest-dom/vitest";

import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/diagnosis/api", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/api")>()),
  apiFetch: vi.fn(),
}));
vi.mock("../live/remediationFollowup", async (original) => {
  const real = await original<typeof import("../live/remediationFollowup")>();
  return { ...real, readFollowup: vi.fn(real.readFollowup), saveFollowupReview: vi.fn(real.saveFollowupReview) };
});

import { ApiError, apiFetch } from "@/lib/diagnosis/api";
import type { URLRunSummary } from "@/lib/diagnosis/diagnosis-runs";

import * as fu from "../live/remediationFollowup";
import { evidenceFor, parseFollowup } from "../live/remediationFollowup";
import { resetUrlRuns, useUrlRuns } from "../live/urlRunList";
import { type Shell, ShellContext } from "../shell";
import { useReportFollowup } from "../views/live/remediationFollowupView";

const noop = () => undefined;
/** 받은 응답이 화면 상태로 들어갈 때까지 한 번 기다린다 */
const settle = () => act(() => new Promise((r) => setTimeout(r, 0)));
const shell: Shell = {
  route: "report",
  navSeq: 1,
  narrow: false,
  phone: false,
  reduce: false,
  inspOpen: false,
  railOpen: false,
  railNewAtRoute: false,
  goTo: noop,
  openInsp: noop,
  closeInsp: noop,
  openRail: noop,
  closeRail: noop,
  foldRail: noop,
  dashPick: noop,
};

const D = (c: string) => c.repeat(64);
// 서버 응답 모양(server/remediation_followup.go) — 합성 값만
const obs = (over: Record<string, unknown> = {}) => ({
  method: "GET",
  path: "/api/orders/{id}",
  vuln_class: "idor",
  step_id: "step-1",
  binding_digest: D("b"),
  number: 3,
  http_status: 200,
  reviewable: true,
  ...over,
});
const item = (over: Record<string, unknown> = {}) => ({
  entry_id: "entry-1",
  record_id: "11",
  title: "다른 계정 주문 조회",
  original_observation: "계정 B 로 계정 A 의 주문을 읽음",
  criteria: ["계정 B 로 계정 A 주문을 읽으면 403"],
  original_digest: D("a"),
  requested_changes: ["주문 조회에 소유자 검사 추가"],
  original_requests: [{ method: "GET", path: "/api/orders/{id}", vuln_class: "idor" }],
  result: "inconclusive",
  reason: "no_linked_review",
  ...over,
});
const run = (task: string | undefined, over: Record<string, unknown> = {}) => ({
  ...(task ? { task_id: task } : {}),
  state: task ? "completed" : "not_linked",
  started: !!task,
  result_ready: !!task,
  results_state: task ? "available" : "not_linked",
  confirmed_excluded_paths: null,
  unresolved_entries: 0,
  ...over,
});
const page = (current?: string, over: Record<string, unknown> = {}) => ({
  source_task: "42",
  target_id: "target-1",
  target_url: "https://app.test",
  previous_excluded_paths: null,
  current_scope: {
    state: "review_required",
    paths: ["/api"],
    methods: ["GET"],
    request_budget: 100,
    expires_at: "2026-12-01T00:00:00Z",
  },
  items: [item(), item({ entry_id: "entry-2", record_id: "12", title: "관리자 화면 열람", original_requests: [] })],
  current: run(
    current,
    current ? { findings: [{ entry_id: "f-1", title: "주문 IDOR", outcome: "suspected", observations: [obs()] }] } : {},
  ),
  general: run(current),
  additional: [],
  ...over,
});

afterEach(() => {
  cleanup();
  resetUrlRuns();
});
beforeEach(() => {
  vi.mocked(apiFetch).mockReset();
  vi.mocked(fu.readFollowup).mockReset();
  vi.mocked(fu.saveFollowupReview).mockReset();
});

describe("이행점검 파서 · 조회", () => {
  it("빈 Go 슬라이스(null)는 빈 목록으로, 다른 원래 작업 · 모르는 결과는 받지 않는다", () => {
    const p = parseFollowup(
      page(undefined, {
        current_scope: { ...page().current_scope, paths: null, methods: null },
        items: [item({ criteria: null })],
      }),
      "42",
    );
    expect(p.current_scope.paths).toEqual([]);
    expect(p.items[0].criteria).toEqual([]);
    expect(p.current.findings).toEqual([]);
    expect(() => parseFollowup(page(), "43")).toThrow();
    expect(() => parseFollowup(page(undefined, { items: [item({ result: "safe" })] }), "42")).toThrow();
  });

  it("판정 근거: 판정 가능하고 원래 요청과 메서드 · 경로 · 유형이 모두 같은 관찰만(계약)", () => {
    const p = parseFollowup(
      page("50", {
        current: run("50", {
          findings: [
            {
              entry_id: "f-1",
              title: "주문",
              outcome: "suspected",
              observations: [
                obs(),
                obs({ step_id: "s-2", reviewable: false }),
                obs({ step_id: "s-3", method: "POST" }),
                obs({ step_id: "s-4", vuln_class: "bola" }),
              ],
            },
          ],
        }),
      }),
      "42",
    );
    expect(evidenceFor(p.items[0], p.current).map((e) => e.obs.step_id)).toEqual(["step-1"]);
    expect(evidenceFor(p.items[1], p.current)).toEqual([]);
  });

  it("조회는 원래 작업 경로에 후속 회차를 붙이고, 저장은 PUT ?current_task= 로만", async () => {
    const { readFollowup, saveFollowupReview } =
      await vi.importActual<typeof import("../live/remediationFollowup")>("../live/remediationFollowup");
    vi.mocked(apiFetch).mockResolvedValue(page("50"));
    await readFollowup("42");
    await readFollowup("42", "50", "50");
    const edit = {
      entry_id: "entry-1",
      record_id: "11",
      original_digest: D("a"),
      outcome: "inconclusive" as const,
      reason: "insufficient_evidence" as const,
      conditions_comparable: false,
      all_criteria_met: false,
      expected_revision: 0,
    };
    await saveFollowupReview("42", "50", edit);
    const calls = vi.mocked(apiFetch).mock.calls;
    expect(calls[0][0]).toBe("/api/tasks/42/remediation-followup");
    expect(calls[1][0]).toBe("/api/tasks/42/remediation-followup?current_task=50&general_task=50");
    expect(calls[2][0]).toBe("/api/tasks/42/remediation-followup?current_task=50");
    expect(calls[2][1]).toMatchObject({ method: "PUT", body: JSON.stringify(edit) });
    await expect(readFollowup("42", "x")).rejects.toThrow();
  });
});

const summary = (task: string, over: Partial<URLRunSummary> = {}): URLRunSummary => ({
  id: `job-${task}`,
  revision: 3,
  state: "completed",
  phase: "report",
  task_id: task,
  started: true,
  drain_complete: true,
  finished_at: "2026-11-20T03:00:00Z",
  result_ready: true,
  report_available: true,
  approval_status: "not_revalidated",
  action_required: false,
  ...over,
});

function Harness() {
  const f = useReportFollowup("42", true, true, noop);
  if (!f.showTab) return <p>탭 없음</p>;
  return (
    <ShellContext.Provider value={shell}>
      <div role="tabpanel" aria-label="이행점검">
        {f.body}
      </div>
      <aside aria-label="이행점검 상세">{f.panel}</aside>
    </ShellContext.Provider>
  );
}

describe("보고서 이행점검 탭 (사용자 11/26 — 원래 보고서에서 뒤에 돌린 점검의 판정 결과)", () => {
  function seedRuns() {
    useUrlRuns.setState({
      owner: "o",
      page: {
        snapshot: D("c"),
        limit: 25,
        more: false,
        runs: [
          summary("40"), // 원래보다 앞 회차 — 묻지 않음
          summary("50"),
          summary("55", { state: "running", result_ready: false, report_available: false }), // 진행 중 — 묻지 않음
          summary("60"), // 다른 대상 — 서버가 409
        ],
      },
    });
  }
  function serve() {
    vi.mocked(fu.readFollowup).mockImplementation(async (source, current) => {
      if (!current) return parseFollowup(page(), source);
      if (current === "60") throw new ApiError(409, "HTTP 409");
      return parseFollowup(page(current), source);
    });
  }

  it("같은 대상의 뒤 끝난 점검만 카드로(다른 대상 409 는 뺌), 고른 회차의 조치별 결과를 보인다", async () => {
    seedRuns();
    serve();
    render(<Harness />);
    const runs = await screen.findByRole("region", { name: "뒤에 돌린 점검" });
    await waitFor(() => expect(within(runs).getAllByRole("button")).toHaveLength(1));
    const card = within(runs).getByRole("button");
    expect(card).toHaveTextContent("#50");
    expect(card).toHaveTextContent("수정 확인 0 · 다시 관찰됨 0 · 확인 불가 2 · 추가 발견 0");
    expect(card).toHaveAttribute("aria-current", "true");
    const asked = vi.mocked(fu.readFollowup).mock.calls.map((c) => c[1] ?? null);
    expect(asked.sort()).toEqual(["50", "60", null]);
    const items = screen.getByRole("region", { name: "후속 #50 조치별 결과" });
    expect(within(items).getAllByRole("button")).toHaveLength(2);
    // '아직 판정하지 않음'은 '판정 전'과 같은 말이라 하나만
    expect(within(items).getAllByText("판정 전")).toHaveLength(2);
    expect(screen.getByText("추가 발견 0건")).toBeInTheDocument();
  });

  it("조치를 누르면 오른쪽 패널에서 근거 관찰 · 조건 확인 뒤에만 '수정 확인'을 저장하고, 저장 결과를 다시 그린다", async () => {
    seedRuns();
    serve();
    const savedPage = page("50", {
      items: [
        item({
          result: "fixed",
          reason: undefined,
          review: {
            evidence_entry_id: "f-1",
            step_id: "step-1",
            binding_digest: D("b"),
            reviewer: "운영자",
            revision: 1,
            reviewed_at: "2026-11-26T02:00:00Z",
          },
        }),
      ],
    });
    // 저장 응답엔 추가 발견(general)이 없어 화면이 같은 회차를 다시 읽는다 — 그 뒤로는 저장된 판정이 담긴 쪽
    let stored = false;
    vi.mocked(fu.saveFollowupReview).mockImplementation(async () => {
      stored = true;
      return parseFollowup({ ...savedPage, general: run(undefined) }, "42");
    });
    vi.mocked(fu.readFollowup).mockImplementation(async (source, current) => {
      if (!current) return parseFollowup(page(), source);
      if (current === "60") throw new ApiError(409, "HTTP 409");
      return parseFollowup(stored ? savedPage : page(current), source);
    });
    render(<Harness />);
    const items = await screen.findByRole("region", { name: "후속 #50 조치별 결과" });
    fireEvent.click(within(items).getByRole("button", { name: /다른 계정 주문 조회/ }));
    const side = screen.getByRole("complementary", { name: "이행점검 상세" });
    expect(within(side).getByRole("heading", { name: "다른 계정 주문 조회" })).toBeInTheDocument();
    expect(within(side).getByText("주문 조회에 소유자 검사 추가")).toBeInTheDocument();
    const save = within(side).getByRole("button", { name: "판정 저장" });
    expect(save).toBeDisabled(); // 결과를 미리 고르지 않는다
    fireEvent.click(within(side).getByRole("radio", { name: "수정 확인" }));
    expect(within(side).getByRole("radio", { name: /#3 GET \/api\/orders\/\{id\} · 200/ })).toBeChecked();
    expect(save).toBeDisabled();
    fireEvent.click(within(side).getByRole("checkbox", { name: /조건이 원래 관찰과 같아요/ }));
    expect(save).toBeDisabled();
    fireEvent.click(within(side).getByRole("checkbox", { name: /검증 기준을 모두 만족해요/ }));
    expect(save).toBeEnabled();
    fireEvent.click(save);
    await waitFor(() => expect(fu.saveFollowupReview).toHaveBeenCalledTimes(1));
    expect(vi.mocked(fu.saveFollowupReview).mock.calls[0]).toEqual([
      "42",
      "50",
      {
        entry_id: "entry-1",
        record_id: "11",
        original_digest: D("a"),
        evidence_entry_id: "f-1",
        step_id: "step-1",
        binding_digest: D("b"),
        outcome: "fixed",
        conditions_comparable: true,
        all_criteria_met: true,
        expected_revision: 0,
      },
    ]);
    expect(await within(items).findByText("판본 1 · 운영자")).toBeInTheDocument();
    const card = within(screen.getByRole("region", { name: "뒤에 돌린 점검" })).getByRole("button");
    expect(card).toHaveTextContent("수정 확인 1 · 다시 관찰됨 0 · 확인 불가 0 · 추가 발견 0");
    expect(vi.mocked(fu.readFollowup).mock.calls.at(-1)).toEqual(["42", "50", "50"]);
  });

  it("같은 요청 관찰이 없으면 '확인 불가'만 이유와 함께, 근거가 바뀌어 409 면 저장하지 않았다고 알린다", async () => {
    seedRuns();
    serve();
    vi.mocked(fu.saveFollowupReview).mockRejectedValue(new ApiError(409, "HTTP 409"));
    render(<Harness />);
    const items = await screen.findByRole("region", { name: "후속 #50 조치별 결과" });
    fireEvent.click(within(items).getByRole("button", { name: /관리자 화면 열람/ }));
    const side = screen.getByRole("complementary", { name: "이행점검 상세" });
    expect(within(side).getByRole("radio", { name: "수정 확인" })).toBeDisabled();
    expect(within(side).getByRole("radio", { name: "다시 관찰됨" })).toBeDisabled();
    fireEvent.click(within(side).getByRole("radio", { name: "확인 불가" }));
    fireEvent.change(within(side).getByRole("combobox"), { target: { value: "outside_scope" } });
    fireEvent.click(within(side).getByRole("button", { name: "판정 저장" }));
    expect(await within(side).findByRole("alert")).toHaveTextContent(/바뀌어 저장하지 않았어요/);
    expect(vi.mocked(fu.saveFollowupReview).mock.calls[0][2]).toMatchObject({
      entry_id: "entry-2",
      outcome: "inconclusive",
      reason: "outside_scope",
      conditions_comparable: false,
      all_criteria_met: false,
    });
    expect(vi.mocked(fu.saveFollowupReview).mock.calls[0][2]).not.toHaveProperty("step_id");
  });

  it("대상 승인이 만료되면 판정 저장을 막고 까닭을 적는다", async () => {
    seedRuns();
    vi.mocked(fu.readFollowup).mockImplementation(async (source, current) => {
      if (current === "60") throw new ApiError(409, "HTTP 409");
      return parseFollowup(page(current, { current_scope: { ...page().current_scope, state: "expired" } }), source);
    });
    render(<Harness />);
    const items = await screen.findByRole("region", { name: "후속 #50 조치별 결과" });
    fireEvent.click(within(items).getByRole("button", { name: /관리자 화면 열람/ }));
    const side = screen.getByRole("complementary", { name: "이행점검 상세" });
    expect(within(side).getByText(/대상 승인 만료 — 지금은 판정을 저장할 수 없어요/)).toBeInTheDocument();
    fireEvent.click(within(side).getByRole("radio", { name: "확인 불가" }));
    expect(within(side).getByRole("button", { name: "판정 저장" })).toBeDisabled();
  });

  it("뒤에 돌린 점검이 없으면 다시 점검 안내, 조치가 없거나 404 · 409 면 탭을 두지 않고, 그 밖의 실패는 탭에 까닭을 적는다", async () => {
    vi.mocked(fu.readFollowup).mockResolvedValue(parseFollowup(page(), "42"));
    const first = render(<Harness />);
    expect(await screen.findByText(/이 대상으로 뒤에 돌린 점검이 없어요/)).toBeInTheDocument();
    first.unmount();
    vi.mocked(fu.readFollowup).mockResolvedValue(parseFollowup(page(undefined, { items: [] }), "42"));
    const second = render(<Harness />);
    await settle();
    expect(screen.getByText("탭 없음")).toBeInTheDocument();
    second.unmount();
    for (const status of [404, 409]) {
      vi.mocked(fu.readFollowup).mockRejectedValue(new ApiError(status, `HTTP ${status}`));
      const r = render(<Harness />);
      await settle();
      expect(screen.getByText("탭 없음")).toBeInTheDocument();
      r.unmount();
    }
    vi.mocked(fu.readFollowup).mockRejectedValue(new ApiError(503, "HTTP 503"));
    render(<Harness />);
    expect(await screen.findByText(/이행점검 정보를 읽지 못했어요 \(HTTP 503\)/)).toBeInTheDocument();
  });
});
