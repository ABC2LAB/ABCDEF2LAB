import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { ApiError, apiFetchBlob } from "@/lib/diagnosis/api";
import {
  generateRemediation,
  type RemediationBundle,
  type RemediationEntry,
  readCopyPrompt,
} from "@/lib/diagnosis/remediation";

import { DiagnosticEntrySections, pocCopyText, stepHeading } from "../views/live/DiagnosticEntrySections";
import { MARKUP_HIDDEN_SECRETS } from "./mask-fixtures";

vi.mock("@/lib/diagnosis/api", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/api")>()),
  apiFetchBlob: vi.fn(),
}));
vi.mock("@/lib/diagnosis/remediation", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/remediation")>()),
  generateRemediation: vi.fn(),
  readCopyPrompt: vi.fn(),
}));
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
it("shows two sections in order and never offers remediation generation or prompt copy (사용자 11/19)", () => {
  const e = fixture();
  render(<DiagnosticEntrySections task="42" entry={e} onReload={vi.fn()} />);
  expect(screen.getAllByRole("heading", { level: 3 }).map((x) => x.textContent?.[0])).toEqual(["①", "②"]);
  // 검증 방식 · 실행 · 단계별 근거 ID 같은 기록 값('기술 정보')은 화면에 두지 않는다(사용자 11/19 — 내부 사본에 남음)
  expect(screen.queryByText("기술 정보")).toBeNull();
  expect(screen.queryByText(/e3_structured|owner-proof/)).toBeNull();
  expect(screen.getByText(/스크린샷 미확보 — 저장된 요청 시도와 관찰 기록을 확인하세요/)).toBeVisible();
  // 보안 조치 · AI 수정 작업용 프롬프트 칸은 뺐다 — 저장 조치(bundle)가 있어도 보이거나 만들거나 복사하지 않는다
  expect(screen.queryByRole("heading", { name: /보안 조치|AI 수정 작업용 프롬프트/ })).toBeNull();
  expect(screen.queryByText(/자세한 조치|조치 · 프롬프트/)).toBeNull();
  expect(screen.queryByRole("button", { name: /AI 수정 프롬프트 복사|생성·저장|전체 프롬프트/ })).toBeNull();
  expect(readCopyPrompt).not.toHaveBeenCalled();
  expect(generateRemediation).not.toHaveBeenCalled();
  expect(apiFetchBlob).not.toHaveBeenCalled();
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

it("상세 보고는 Markdown 으로 그리되 원문 HTML · 링크 이동 · 이미지 불러오기는 하지 않고, 화면은 그대로 · 복사만 가린다", async () => {
  const writeText = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
  const entry = {
    ...fixture(),
    report:
      "## 재현 요약\n\n- 계정 B로 [주문](https://outside.example/x) 조회\n\n![캡처](https://outside.example/a.png)\n\n<script>alert(1)</script><b>굵게</b>\n\nAuthorization: Bearer synthetic-private-token",
  };
  render(<DiagnosticEntrySections task="42" entry={entry} onReload={vi.fn()} />);
  const card = screen.getByRole("region", { name: "상세 보고" });
  expect(within(card).getByRole("heading", { name: "재현 요약" })).toBeInTheDocument();
  expect(within(card).getByRole("listitem")).toHaveTextContent("계정 B로 주문 조회");
  // 보고 글 속 주소로 나가지 않는다: 링크는 글로만, 이미지는 대체 글로만
  expect(card.querySelector("a, img, script")).toBeNull();
  expect(within(card).getByText("[이미지: 캡처]")).toBeInTheDocument();
  expect(card).not.toHaveTextContent("outside.example");
  expect(card.querySelector("b")).toBeNull();
  // 제품 화면은 내부 담당자만 보므로 대상 세션 값을 가리지 않는다(CLAUDE.md 절대규칙 2, 2026-11-26)
  expect(card).toHaveTextContent("Authorization: Bearer synthetic-private-token");
  fireEvent.click(within(card).getByRole("button", { name: "복사" }));
  await within(card).findByText("상세 보고를 복사했습니다.");
  expect(writeText).toHaveBeenCalledTimes(1);
  // 클립보드는 화면 밖(팀 채팅 · 이슈)으로 나가므로 가린다(#975 이동찬 P2, 절대규칙 2)
  expect(writeText.mock.calls[0][0]).not.toContain("synthetic-private-token");
  expect(writeText.mock.calls[0][0]).toContain("Authorization: Bearer [REDACTED]");
});

it.each(
  MARKUP_HIDDEN_SECRETS,
)("상세 보고 복사는 파서가 푼 글도 가린다 — %s (#677 · #709 리뷰, #975)", async (_, report, secret) => {
  const writeText = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
  render(<DiagnosticEntrySections task="42" entry={{ ...fixture(), report }} onReload={vi.fn()} />);
  const card = screen.getByRole("region", { name: "상세 보고" });
  // 복사는 화면에 그려진 글을 가린 것이라 원문(엔티티)도 값도 클립보드로 나가지 않는다
  fireEvent.click(within(card).getByRole("button", { name: "복사" }));
  await within(card).findByText("상세 보고를 복사했습니다.");
  expect(writeText.mock.calls[0][0]).not.toContain(secret);
  expect(writeText.mock.calls[0][0]).not.toContain("&#");
});

it.each(
  MARKUP_HIDDEN_SECRETS,
)("'PoC 복사'는 응답 증거의 같은 모양도 클립보드에 남기지 않는다 — %s (#709 리뷰, #975)", async (_, evidence, secret) => {
  const writeText = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
  render(
    <DiagnosticEntrySections
      task="42"
      entry={{ ...fixture(), evidence: `HTTP/1.1 200 OK\n${evidence}` }}
      onReload={vi.fn()}
      layout="page"
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "PoC 복사" }));
  await screen.findByText("PoC 를 복사했습니다.");
  const text = writeText.mock.calls[0][0] as string;
  expect(text).not.toContain(secret);
  // 붙여 넣은 앱이 풀 엔티티도 남지 않고, 숨길 것 없는 줄은 그대로다
  expect(text).not.toContain("&#");
  expect(text).toContain("HTTP/1.1 200 OK");
});

it.each([
  "review",
  "unknown",
  "not_executed",
  "negative",
])("keeps %s results neutral and preserves stored detail", (outcome) => {
  const entry = {
    ...fixture(),
    outcome,
    report: "저장된 owner-read → cross-read 단계; Authorization: Bearer synthetic-private-token",
    truncated: true,
  };
  render(<DiagnosticEntrySections task="42" entry={entry} onReload={vi.fn()} />);
  expect(screen.getByRole("heading", { name: "① AI 분석" })).toBeVisible();
  // ② 는 E②가 기록한 시도와 관찰이며 미송신·응답 없음도 포함할 수 있다.
  expect(screen.getByRole("heading", { name: "② 요청 시도 · 응답 관찰" })).toBeVisible();
  expect(screen.queryByText(/① 확인된 취약점/)).not.toBeInTheDocument();
  // 상세 보고는 ARTEX 발견 상세처럼 카드에 펼쳐 둔다(높이를 묶은 상자) — 저장된 글 그대로 보인다
  expect(
    within(screen.getByRole("region", { name: "상세 보고" })).getByText(/저장된 owner-read → cross-read 단계/),
  ).toBeVisible();
  expect(document.body.textContent).toContain("synthetic-private-token");
  expect(screen.getByText(/실제 도구 출력이 잘렸습니다/)).toBeVisible();
});

it("거부된 E② 시도를 실제 송신·응답으로 표시하지 않는다", () => {
  const entry = fixture();
  const step = entry.steps?.[0];
  if (!step) throw new Error("fixture step");
  entry.outcome = "review";
  entry.steps = [{ ...step, path: "/api/me", observation: "전송 시도=false, HTTP 상태=0, 응답 완료=false" }];
  const panel = render(<DiagnosticEntrySections task="42" entry={entry} onReload={vi.fn()} />);
  expect(screen.getByRole("heading", { name: "② 요청 시도 · 응답 관찰" })).toBeVisible();
  expect(screen.getByText("전송 시도=false, HTTP 상태=0, 응답 완료=false")).toBeVisible();
  expect(screen.queryByText(/실제 요청·응답|실제로 보내고/)).toBeNull();
  panel.unmount();

  render(<DiagnosticEntrySections task="42" entry={entry} onReload={vi.fn()} layout="page" />);
  expect(screen.getByText(/전송되지 않았거나 응답이 없는 시도도 포함/)).toBeVisible();
  expect(screen.queryByText(/실제 요청·응답|실제로 보내고/)).toBeNull();
  expect(pocCopyText(entry)).toContain("요청 시도 · 응답 관찰:");
  expect(pocCopyText(entry)).not.toContain("실제 요청 · 응답:");
});

it("reports source load failure without presenting generated steps", () => {
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
  expect(screen.queryByRole("heading", { name: /요청 1/ })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /생성·저장/ })).toBeNull();
});

it("distinguishes missing current images from integrity failures", async () => {
  const entry = fixture();
  if (!entry.steps?.[0]) throw Error("fixture step");
  entry.steps[0].capture = { status: "captured", digest: "c".repeat(64), width: 4, height: 4 };
  vi.mocked(apiFetchBlob).mockRejectedValue(new ApiError(404, "missing"));
  render(<DiagnosticEntrySections task="42" entry={entry} onReload={vi.fn()} />);
  fireEvent.click(screen.getByRole("button", { name: "검증된 증적 이미지 보기" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("현재 회차에서 이미지를 찾지 못했습니다");
});

it("실행 단계 제목은 '요청 n' — 서버 기본 제목('저장된 실행 관찰 n')은 되풀이하지 않는다 (사용자 11/20)", () => {
  expect(stepHeading({ number: 1, title: "저장된 실행 관찰 1" })).toBe("요청 1");
  expect(stepHeading({ number: 2, title: "" })).toBe("요청 2");
  expect(stepHeading({ number: 3, title: "계정 B로 계정 A 주문 조회" })).toBe("요청 3 · 계정 B로 계정 A 주문 조회");
  // 제목의 값은 가리지 않는다(DLP 폐기)
  expect(stepHeading({ number: 4, title: "token=synthetic-step-secret" })).toBe("요청 4 · token=synthetic-step-secret");
  const e = fixture();
  const step = e.steps?.[0];
  if (!step) throw new Error("fixture");
  render(
    <DiagnosticEntrySections
      task="42"
      entry={{ ...e, steps: [{ ...step, title: "저장된 실행 관찰 1" }] }}
      onReload={vi.fn()}
    />,
  );
  expect(screen.getByRole("heading", { level: 4, name: "요청 1" })).toBeInTheDocument();
  expect(screen.queryByText(/Step 1|저장된 실행 관찰/)).toBeNull();
});
