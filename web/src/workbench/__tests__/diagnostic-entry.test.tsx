import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { apiFetchBlob } from "@/lib/diagnosis/api";
import {
  generateRemediation,
  type RemediationBundle,
  type RemediationEntry,
  readCopyPrompt,
} from "@/lib/diagnosis/remediation";

import { DiagnosticEntrySections } from "../views/live/DiagnosticEntrySections";

vi.mock("@/lib/diagnosis/api", () => ({ apiFetchBlob: vi.fn() }));
vi.mock("@/lib/diagnosis/remediation", () => ({ generateRemediation: vi.fn(), readCopyPrompt: vi.fn() }));
const bundle: RemediationBundle = {
  finding_id: "finding-1",
  input_digest: "saved-input",
  source_digest: "source",
  evidence_version: "1",
  report_version: "report",
  remediation_version: "remediation-v6",
  ai_prompt: "Analyze the customer repository, apply a minimal authorization fix, and run security regression tests.",
  technologies: [],
  security_condition: ["서버 인가 검사"],
  immediate_mitigation: [],
  root_fix: [],
  configuration: [],
  code_example: [],
  assumptions: [],
  verification: ["정상 조회 보존"],
  residual_risk: [],
};
function fixture(): RemediationEntry {
  return {
    entry_id: "finding-1",
    title: "저장된 E③ 항목",
    outcome: "confirmed",
    status: "pending",
    verification: "e3_structured",
    evidence_digest: "a".repeat(64),
    report_version: "report",
    run: "run-a",
    intent_id: 1,
    observed_at: "2026-11-11T00:00:00Z",
    evidence_version: 1,
    source_digest: "source",
    summary: "시험 자원 교차 접근",
    evidence: "stored receipt",
    report: "",
    remediation: "확인된 인가 조건을 검사하세요.",
    available: true,
    unavailable_reason: "",
    record: { id: 7, entry_id: "finding-1", stale: false, bundle },
    steps: [
      {
        task_id: 42,
        entry_id: "finding-1",
        id: `step-${"b".repeat(24)}`,
        number: 1,
        title: "저장된 실행 관찰 1",
        source_step_id: "owner-read",
        run_ref: "run-a",
        call_ref: "",
        evidence_ref: "owner-proof",
        evidence_version: 1,
        evidence_digest: "a".repeat(64),
        binding_digest: "b".repeat(64),
        observed_at: "2026-11-11T00:00:00Z",
        method: "GET",
        path: "",
        prerequisites: "등록된 시험 자원",
        observation: "등록된 계정의 실제 조회",
        truncated: false,
        capture: { status: "not_captured" },
      },
    ],
  };
}
const copy = vi.fn();
beforeEach(() => {
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: copy } });
});
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
  vi.unstubAllGlobals();
  Reflect.deleteProperty(URL, "createObjectURL");
});
it("shows four sections in order and only reads on explicit copy", async () => {
  const e = fixture();
  render(<DiagnosticEntrySections task="42" entry={e} onReload={vi.fn()} />);
  expect(screen.getAllByRole("heading", { level: 3 }).map((x) => x.textContent?.[0])).toEqual(["①", "②", "③", "④"]);
  expect(screen.getByText(/스크린샷 미확보 — 실제 요청·응답 및 실행 기록으로 증거 제공/)).toBeVisible();
  expect(readCopyPrompt).not.toHaveBeenCalled();
  expect(generateRemediation).not.toHaveBeenCalled();
  expect(apiFetchBlob).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "전체 프롬프트 보기" }));
  expect(screen.getByText(bundle.ai_prompt)).toBeVisible();
  vi.mocked(readCopyPrompt).mockResolvedValue({
    entry_id: e.entry_id,
    record_id: 7,
    input_digest: bundle.input_digest,
    ai_prompt: bundle.ai_prompt,
  });
  fireEvent.click(screen.getByRole("button", { name: "AI 수정 프롬프트 복사" }));
  await waitFor(() => expect(copy).toHaveBeenCalledWith(bundle.ai_prompt));
});
it("rejects a superseded server response and never copies stale stored data", async () => {
  const e = fixture();
  vi.mocked(readCopyPrompt).mockResolvedValue({
    entry_id: e.entry_id,
    record_id: 8,
    input_digest: bundle.input_digest,
    ai_prompt: bundle.ai_prompt,
  });
  const v = render(<DiagnosticEntrySections task="42" entry={e} onReload={vi.fn()} />);
  fireEvent.click(screen.getByRole("button", { name: "AI 수정 프롬프트 복사" }));
  await screen.findByText(/프롬프트가 변경됐거나 복사 권한을 확인하지 못했습니다/);
  expect(copy).not.toHaveBeenCalled();
  v.unmount();
  render(
    <DiagnosticEntrySections
      task="42"
      entry={{ ...e, record: { id: 7, entry_id: e.entry_id, stale: true } }}
      onReload={vi.fn()}
    />,
  );
  expect(screen.queryByRole("button", { name: "AI 수정 프롬프트 복사" })).not.toBeInTheDocument();
  expect(screen.getByRole("alert")).toHaveTextContent("오래된 프롬프트 사용을 차단");
});
it("stores remediation only on explicit generation and reloads current evidence", async () => {
  const reload = vi.fn(),
    e = fixture();
  render(<DiagnosticEntrySections task="42" entry={{ ...e, record: undefined }} onReload={reload} />);
  expect(generateRemediation).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "보안 조치·AI 프롬프트 생성·저장" }));
  await waitFor(() => expect(reload).toHaveBeenCalledOnce());
  expect(generateRemediation).toHaveBeenCalledWith(
    "42",
    expect.objectContaining({ entry_id: e.entry_id }),
    [],
    expect.any(AbortSignal),
  );
});
it("fetches images only on explicit view and rejects a mismatched image hash", async () => {
  const e = fixture();
  if (!e.steps?.[0]) throw Error("missing fixture step");
  e.steps[0].capture = {
    status: "captured",
    digest: "c".repeat(64),
    ref: `shot-${"b".repeat(24)}`,
    captured_at: "2026-11-11T00:00:00Z",
    width: 4,
    height: 4,
  };
  const digest = vi.fn().mockResolvedValue(new Uint8Array(32).buffer);
  vi.stubGlobal("crypto", { subtle: { digest } });
  const create = vi.fn(() => "blob:owned-capture");
  Object.defineProperty(URL, "createObjectURL", { configurable: true, value: create });
  const png = new Blob([new Uint8Array([137, 80, 78, 71])], { type: "image/png" });
  Object.defineProperty(png, "arrayBuffer", { value: async () => new Uint8Array([137, 80, 78, 71]).buffer });
  vi.mocked(apiFetchBlob).mockResolvedValue(png);
  render(<DiagnosticEntrySections task="42" entry={e} onReload={vi.fn()} />);
  expect(apiFetchBlob).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "검증된 증적 이미지 보기" }));
  await screen.findByRole("alert");
  expect(digest).toHaveBeenCalledOnce();
  expect(create).not.toHaveBeenCalled();
  expect(screen.queryByRole("img")).not.toBeInTheDocument();
});

it.each([
  "review",
  "unknown",
  "not_executed",
  "negative",
])("keeps %s results neutral and preserves masked stored detail", (outcome) => {
  const entry = {
    ...fixture(),
    outcome,
    report: "저장된 owner-read → cross-read 단계; Authorization: Bearer synthetic-private-token",
    truncated: true,
  };
  render(<DiagnosticEntrySections task="42" entry={entry} onReload={vi.fn()} />);
  expect(screen.getByRole("heading", { name: "① 판정과 영향" })).toBeVisible();
  expect(screen.queryByText(/① 확인된 취약점/)).not.toBeInTheDocument();
  expect(screen.getByText(/저장된 owner-read → cross-read 단계/)).toBeVisible();
  expect(document.body.textContent).not.toContain("synthetic-private-token");
  expect(screen.getByText(/실제 도구 출력이 잘렸습니다/)).toBeVisible();
});

it("reports source load failure without presenting generated steps or offering generation", () => {
  const entry = {
    ...fixture(),
    outcome: "unknown",
    source_load_failed: true,
    available: false,
    record: undefined,
    steps: [],
  };
  render(<DiagnosticEntrySections task="42" entry={entry} onReload={vi.fn()} />);
  expect(screen.getByRole("alert")).toHaveTextContent("근거 불러오기 실패");
  expect(screen.queryByRole("heading", { name: /Step 1/ })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "보안 조치·AI 프롬프트 생성·저장" })).toBeDisabled();
});
