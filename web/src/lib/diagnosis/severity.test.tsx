import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { resetUrl, useUrl } from "@/workbench/live/urlDiagnosis";
import { type Shell, ShellContext } from "@/workbench/shell";
import { UrlDashboardLive } from "@/workbench/views/live/UrlDashboardLive";
import { Outcome, Sev } from "@/workbench/views/live/UrlLiveLive";

import { signIn, signOut } from "./api";
import { catalog, handle, running } from "./diagnosis-url-fixtures";
import { diagnosticOutcome } from "./diagnostic-results";
import { parseRemediationData, type RemediationEntry, readRemediation } from "./remediation";
import { entrySeverity, entrySeverityLabel } from "./severity";

const entry: RemediationEntry = {
  entry_id: "candidate-1",
  title: "저장된 문서 노출 후보",
  outcome: "review",
  status: "completed",
  verification: "manual_review_required",
  evidence_digest: "e".repeat(64),
  source_digest: "d".repeat(64),
  report_version: "1",
  run: "run-1",
  intent_id: 7,
  observed_at: "2026-11-17T00:00:00Z",
  evidence_version: 1,
  summary: "실제 관찰 후보",
  evidence: "",
  report: "",
  remediation: "",
  available: true,
  unavailable_reason: "",
  severity: "high",
  severity_source: "worker_estimate",
  severity_rationale: "응답에 비공개 문서 내용이 포함되어 접근통제 영향을 검토해야 합니다.",
};
const finding: RemediationEntry = {
  ...entry,
  entry_id: "finding-2",
  title: "저장된 확정 결과",
  outcome: "confirmed",
  verification: "stored_verification",
  severity_source: undefined,
  severity_rationale: undefined,
};
beforeEach(() => {
  cleanup();
  signOut();
  resetUrl();
  sessionStorage.clear();
  localStorage.clear();
  // 이 시험은 점검별(점검 하나) 대시보드를 본다 — 기본 보기는 전체(통합 대시보드, 11/26)
  localStorage.setItem("abc2lab-dash-mode", "run");
});
afterEach(() => {
  cleanup();
  signOut();
  vi.unstubAllGlobals();
});

it("실제 HTTP 파서가 내부 API의 예상 높음을 표시해도 후보는 검토 필요다", async () => {
  const requests: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const path = String(input);
      requests.push(`${init.method ?? "GET"} ${path}`);
      const data =
        path === "/api/auth/login" ? { token: "synthetic-severity-session" } : { task_id: 42, entries: [entry] };
      return new Response(JSON.stringify(data), { status: 200, headers: { "Content-Type": "application/json" } });
    }),
  );
  await signIn("synthetic-operator", "synthetic-password");
  const data = await readRemediation("42", "internal");
  expect(diagnosticOutcome(data.entries[0])).toBe("review");
  render(
    <>
      <Sev e={data.entries[0]} />
      <Outcome o={diagnosticOutcome(data.entries[0])} />
    </>,
  );
  // 딱지엔 '예상'을 붙이지 않고(사용자 11/24) 확정이 아님은 판정 딱지 '검토 필요'가 말한다
  expect(screen.getByText("높음")).toBeInTheDocument();
  expect(screen.queryByText(/예상/)).toBeNull();
  expect(screen.getByText("검토 필요")).toBeInTheDocument();
  expect(requests).toEqual([
    "POST /api/auth/login",
    "GET /api/report/diagnostic/remediation?task=42&audience=internal",
  ]);
});
it.each(["critical", "high", "medium", "low", "info"])("예상 %s는 확정 판정을 만들지 않는다", (severity) => {
  const value = { ...entry, severity };
  expect(entrySeverity(value)).toBe(severity);
  expect(diagnosticOutcome(value)).toBe("review");
  expect(entrySeverityLabel(value)).not.toMatch(/예상/);
});
it.each([
  { severity: undefined },
  { severity: "urgent" },
  { severity_source: "verified" },
  { severity_rationale: "" },
  { severity_rationale: "추측" },
  { outcome: "not_executed", status: "policy_not_executed" },
  { outcome: "unknown", status: "result_unknown" },
  { source_load_failed: true },
  { truncated: true },
  { source_digest: "damaged" },
  { severity_source: undefined, severity_rationale: undefined },
])("근거가 부족하거나 미실행·불명인 평가는 생략한다: %j", (patch) => {
  const value = { ...entry, ...patch };
  expect(entrySeverity(value)).toBeNull();
  expect(entrySeverityLabel(value)).toBe("위험도 미평가");
});
it("확정 finding의 높음과 정보성은 예상 딱지 없이 표시한다", () => {
  render(
    <>
      <Sev e={finding} />
      <Sev e={{ ...finding, entry_id: "finding-3", severity: "info" }} />
    </>,
  );
  expect(screen.getByText("높음")).toBeInTheDocument();
  expect(screen.getByText("정보성")).toBeInTheDocument();
  expect(screen.queryByText(/예상/)).toBeNull();
});
it("공유 API가 새 평가 정보를 보내면 공개 계약 위반으로 거부한다", () => {
  expect(() =>
    parseRemediationData({ task_id: 42, entries: [{ ...entry, source_digest: "" }] }, "42", "shared"),
  ).toThrow();
});
it("대시보드는 확정 분포와 후보 예상 분포를 구분하고 예상 등급으로 필터링한다", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input);
      const data = path.includes("/remediation")
        ? {
            task_id: 42,
            entries: [
              entry,
              finding,
              { ...entry, entry_id: "candidate-3", title: "정보성 관찰 후보", severity: "info" },
            ],
          }
        : {};
      return new Response(JSON.stringify(data), {
        status: path.includes("/remediation") ? 200 : 404,
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
  const job = {
    ...running,
    state: "completed" as const,
    phase: "report" as const,
    drain_complete: true,
    result_ready: true,
  };
  useUrl.setState({ cap: { kind: "v2", catalog }, handle, job, verified: true });
  const noop = () => undefined;
  const shell: Shell = {
    route: "dashboard",
    navSeq: 1,
    narrow: false,
    phone: false,
    reduce: false,
    inspOpen: true,
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
  render(
    <ShellContext.Provider value={shell}>
      <UrlDashboardLive />
    </ShellContext.Provider>,
  );
  const summary = await screen.findByRole("region", { name: "요약" });
  await waitFor(() => expect(summary).toHaveTextContent("확정 높음 1"));
  // 확정 분포와 검토 필요(후보) 분포는 따로 적는다 — '예상' 대신 '검토 필요'(사용자 11/24)
  expect(summary).toHaveTextContent("검토 필요 높음 1");
  expect(summary).not.toHaveTextContent("예상");
  expect(summary).not.toHaveTextContent("확정 높음 2");
  const rows = screen.getByRole("region", { name: "발견 · 검토 대상" });
  expect(within(rows).getAllByText("높음").length).toBeGreaterThan(0);
  expect(rows).not.toHaveTextContent("예상");
  const confirmedRow = within(rows).getByText(finding.title).closest("button");
  expect(confirmedRow).toHaveTextContent("높음");
  expect(confirmedRow).not.toHaveTextContent("예상");
  // 줄 딱지도 '정보성'으로 시작해 거르기 칩 묶음 안에서 고른다
  fireEvent.click(
    within(within(rows).getByRole("group", { name: "위험도로 거르기" })).getByRole("button", { name: /^정보성/ }),
  );
  expect(within(rows).queryByText(entry.title)).toBeNull();
  // 거른 뒤 남은 줄은 정보성 후보 하나(칩 글 '정보성'과 줄 딱지가 같이 있다)
  expect(rows.querySelectorAll("[data-dsel^='fd-']")).toHaveLength(1);
  expect(rows.querySelector("[data-dsel^='fd-']")).toHaveTextContent(/^정보성/);
});
