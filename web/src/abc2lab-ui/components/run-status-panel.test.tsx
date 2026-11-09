import "@testing-library/jest-dom/vitest";

import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { DiagnosisRunSummary } from "./DiagnosisRunSummary";
import { RunStatusPanel } from "./RunStatusPanel";

type Handler = (path: string) => Response | Promise<Response>;

function json(status: number, body: unknown) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}
const run = (overrides: object = {}) => ({
  run_ref: "run-1",
  phase: "running",
  outcome: "",
  stop_requested: false,
  context_cancelled: false,
  drain_complete: false,
  closure_failed: false,
  ...overrides,
});
const execution = {
  approval_mode: "confirm_every_request",
  state: "active",
  reason: "",
  audit_complete: false,
  counts: {
    attempts: 11,
    allowed: 12,
    denied: 13,
    unsupported: 14,
    forward_attempted: 15,
    responses: 16,
    unknown: 17,
    calls: 18,
  },
};
const liveBody = (r: object, approvals: object[] | null = []) => json(200, { approvals, run: r });
const usage = (overrides: object = {}) => ({
  requests: 7,
  errors: 0,
  input_tokens: 100,
  output_tokens: 20,
  cost_usd_micros: 1_500_000,
  provider_slug: "deepinfra/fp8",
  model: "qwen/qwen3.6-35b-a3b",
  blocked: false,
  failure_reason: "",
  ...overrides,
});
const COUNT_NOTE = "요청 접수는 판정 전에 받은 수라 차단분도 포함해요. 실제로 대상에 보낸 건 대상 전송 시도예요.";

let live: Handler;
let saved: Handler;
let requests: string[];

beforeEach(() => {
  localStorage.setItem("abc2lab.devToken", "valid");
  requests = [];
  live = () => liveBody(run());
  saved = () => json(404, { error: "none" });
  vi.stubGlobal("fetch", async (path: string) => {
    requests.push(path);
    return path.endsWith("/diagnosis-run") ? saved(path) : live(path);
  });
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  localStorage.clear();
});

const field = (label: string) => screen.getByText(label).parentElement?.querySelector(".v")?.textContent;
const panel = (taskId = "42", active = true, open = vi.fn()) => (
  <RunStatusPanel taskId={taskId} active={active} onOpenApprovals={open} />
);

describe("실행 상태 패널", () => {
  it("서버의 전송 경계 값을 항목별로 따로 보여 준다", async () => {
    live = () => liveBody(run({ execution }));
    render(panel());
    expect(await screen.findByText("실행 중")).toBeVisible();
    expect(field("승인 방식")).toBe("매 요청 확인");
    expect(field("전송 경계")).toBe("전송 가능");
    expect(field("감사 기록")).toBe("미완결");
    expect(field("요청 접수")).toBe("11");
    expect(field("허용")).toBe("12");
    expect(field("차단")).toBe("13");
    expect(field("미지원")).toBe("14");
    expect(field("대상 전송 시도")).toBe("15");
    expect(field("응답 수신")).toBe("16");
    expect(field("불명")).toBe("17");
    expect(field("호출")).toBe("18");
    expect(screen.getByText(COUNT_NOTE)).toBeVisible();
  });

  it("보고서의 종료 상태도 같은 집계 이름으로 보여 준다", async () => {
    saved = () =>
      json(200, { version: 1, task_id: 42, run: run({ phase: "closed", outcome: "completed", execution }) });
    render(<DiagnosisRunSummary taskId="42" active />);
    expect(await screen.findByText(COUNT_NOTE)).toBeVisible();
    expect(field("요청 접수")).toBe("11");
    expect(field("차단")).toBe("13");
    expect(field("대상 전송 시도")).toBe("15");
    expect(field("응답 수신")).toBe("16");
    expect(field("불명")).toBe("17");
    expect(field("호출")).toBe("18");
  });

  it("전송 경계를 얻기 전에는 숫자를 만들어 내지 않는다", async () => {
    render(panel());
    expect(await screen.findByText("아직 전송 경계를 획득하지 않았어요.")).toBeVisible();
    expect(screen.queryByText("대상 전송 시도")).toBeNull();
  });

  it("승인 대기 건수는 pending 상태만 센다", async () => {
    const open = vi.fn();
    live = () =>
      liveBody(run(), [{ state: "pending" }, { state: "allowed" }, { state: "pending" }, { state: "denied" }]);
    render(panel("42", true, open));
    fireEvent.click(await screen.findByRole("button", { name: "승인 2건 보기" }));
    expect(open).toHaveBeenCalledTimes(1);
  });

  it("승인 목록이 null 이어도 대기 버튼 없이 표시한다", async () => {
    live = () => liveBody(run(), null);
    render(panel());
    expect(await screen.findByText("실행 중")).toBeVisible();
    expect(screen.queryByRole("button", { name: /승인/ })).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("1초마다 새 상태를 읽고, 언마운트하면 멈춘다", async () => {
    vi.useFakeTimers();
    const view = render(panel());
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(screen.getByText("실행 중")).toBeVisible();
    live = () => liveBody(run({ phase: "closed", outcome: "completed", drain_complete: true }));
    await act(() => vi.advanceTimersByTimeAsync(999));
    expect(screen.getByText("실행 중")).toBeVisible();
    await act(() => vi.advanceTimersByTimeAsync(1));
    expect(screen.getByText("실행 마감")).toBeVisible();
    view.unmount();
    const before = requests.length;
    await vi.advanceTimersByTimeAsync(10_000);
    expect(requests).toHaveLength(before);
  });

  it("다른 화면을 보는 동안에는 요청하지 않는다", async () => {
    vi.useFakeTimers();
    render(panel("42", false));
    await act(() => vi.advanceTimersByTimeAsync(5000));
    expect(requests).toEqual([]);
  });

  it("연결이 끊기면 마지막 상태를 현재 상태처럼 보이지 않게 한다", async () => {
    vi.useFakeTimers();
    render(panel());
    await act(() => vi.advanceTimersByTimeAsync(0));
    live = () => {
      throw new TypeError("connection refused");
    };
    saved = () => {
      throw new TypeError("connection refused");
    };
    await act(() => vi.advanceTimersByTimeAsync(1000));
    expect(screen.getByRole("alert")).toHaveTextContent("진단 서버와 연결이 끊겼어요");
    expect(screen.getByText("마지막으로 확인한 상태예요. 지금 상태가 아닐 수 있어요.")).toBeVisible();
    expect(screen.queryByText("실행 마감")).toBeNull();
  });

  it("끊긴 뒤 저장 기록이 읽히면 저장된 종료 결과로 바꾼다", async () => {
    live = () => json(403, { error: "report only" });
    saved = () =>
      json(200, {
        version: 1,
        task_id: 42,
        saved_at: "2026-11-08T00:00:00Z",
        run: run({ phase: "closed", outcome: "cancelled", drain_complete: true, stop_requested: true }),
        model: usage(),
      });
    render(panel());
    expect(await screen.findByText("중지 후 마감")).toBeVisible();
    expect(screen.getByText(/저장된 종료 기록이에요/)).toBeVisible();
    expect(field("제공자 / 모델")).toBe("deepinfra/fp8 / qwen/qwen3.6-35b-a3b");
    expect(field("모델 요청")).toBe("7");
    expect(field("입력·출력 토큰")).toBe("100 / 20");
    expect(field("보고된 비용")).toBe("$1.5000");
    expect(screen.queryByText("모델 오류")).toBeNull();
  });

  it("모델 오류가 한 건이라도 있으면 보고된 비용이 전부가 아니라고 알린다", async () => {
    live = () => json(403, { error: "report only" });
    saved = () =>
      json(200, {
        version: 1,
        task_id: 42,
        run: run({ phase: "closed", drain_complete: true }),
        model: usage({ errors: 1 }),
      });
    render(panel());
    await screen.findByText("보고된 비용");
    expect(field("모델 오류")).toBe("1건, 실제 비용 불확실");
  });

  it("계획 준비만 기록된 task 를 실행 성공으로 표시하지 않는다", async () => {
    live = () => json(403, { error: "report only" });
    saved = () => json(200, { version: 1, task_id: 42, run: null, execution: "NOT_RUN", model_preparation: [{}, {}] });
    render(panel());
    expect(await screen.findByText("계획 준비만 기록됐고 실행은 시작되지 않았어요.")).toBeVisible();
    expect(field("계획 준비 기록")).toBe("2건");
    expect(screen.queryByText(/마감/)).toBeNull();
  });

  it.each([403, 404])("실행도 저장 기록도 없으면(%i) 연결된 실행이 없다고 말한다", async (code) => {
    live = () => json(code, { error: "no binding" });
    render(panel());
    expect(await screen.findByText(/이 점검에 연결된 실행이 없어요/)).toBeVisible();
  });

  it("그 밖의 요청 오류는 서버 사유를 보여 준다", async () => {
    live = () => json(400, { error: "작업 ID가 올바르지 않습니다" });
    render(panel());
    expect(await screen.findByRole("alert")).toHaveTextContent("작업 ID가 올바르지 않습니다");
  });

  it("마감·중지·사유 값을 서버가 준 그대로 보여 준다", async () => {
    live = () =>
      liveBody(
        run({
          phase: "failed",
          stop_requested: true,
          closure_failed: true,
          execution: { ...execution, state: "blocked", audit_complete: true, reason: "budget_exhausted" },
        }),
      );
    render(panel());
    expect(await screen.findByText("실행 실패")).toBeVisible();
    expect(field("마감 확인")).toBe("아직 안 됨");
    expect(field("중지 요청")).toBe("있음");
    expect(field("자원 마감")).toBe("실패");
    expect(field("전송 경계")).toBe("전송 차단");
    expect(field("감사 기록")).toBe("완결");
    expect(field("사유")).toBe("budget_exhausted");
  });

  it("정상 실행에는 마감 실패 항목을 띄우지 않는다", async () => {
    live = () => liveBody(run({ drain_complete: true }));
    render(panel());
    expect(await screen.findByText("실행 중")).toBeVisible();
    expect(field("마감 확인")).toBe("확인됨");
    expect(field("중지 요청")).toBe("없음");
    expect(screen.queryByText("자원 마감")).toBeNull();
  });

  it("연결이 끊긴 동안에는 3초 간격으로만 다시 확인한다", async () => {
    vi.useFakeTimers();
    live = () => {
      throw new TypeError("connection refused");
    };
    saved = () => {
      throw new TypeError("connection refused");
    };
    render(panel());
    await act(() => vi.advanceTimersByTimeAsync(0));
    const attempts = () => requests.filter((path) => path.endsWith("/request-approvals")).length;
    expect(attempts()).toBe(1);
    await act(() => vi.advanceTimersByTimeAsync(2999));
    expect(attempts()).toBe(1);
    await act(() => vi.advanceTimersByTimeAsync(1));
    expect(attempts()).toBe(2);
  });

  it("연결이 돌아오면 실시간 상태로 복귀한다", async () => {
    vi.useFakeTimers();
    live = () => {
      throw new TypeError("connection refused");
    };
    render(panel());
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(screen.getByRole("alert")).toHaveTextContent("연결이 끊겼어요");
    live = () => liveBody(run());
    await act(() => vi.advanceTimersByTimeAsync(3000));
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.getByText("실행 중")).toBeVisible();
  });

  it("서버가 503 을 주면 끊김으로 뭉개지 않고 사유를 보여 준다", async () => {
    live = () => json(503, { error: "승인 처리기가 연결되지 않았습니다" });
    render(panel());
    expect(await screen.findByRole("alert")).toHaveTextContent("승인 처리기가 연결되지 않았습니다");
    expect(screen.queryByText(/연결이 끊겼어요/)).toBeNull();
  });

  it("인증 실패를 서버 끊김으로 표시하지 않는다", async () => {
    vi.useFakeTimers();
    render(panel());
    await act(() => vi.advanceTimersByTimeAsync(0));
    live = () => json(401, { error: "expired" });
    await act(() => vi.advanceTimersByTimeAsync(1000));
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("task 를 바꾼 직후에는 이전 task 의 상태를 보여 주지 않는다", async () => {
    live = (path) =>
      path.includes("/tasks/a/")
        ? liveBody(run({ run_ref: "run-a" }))
        : new Promise<Response>(() => {
            /* b 의 응답은 일부러 오지 않는다. */
          });
    const view = render(panel("a"));
    expect(await screen.findByText("run-a")).toBeVisible();
    view.rerender(panel("b"));
    expect(screen.queryByText("run-a")).toBeNull();
    expect(screen.getByText("실행 상태를 확인하는 중이에요.")).toBeVisible();
  });

  it("task 를 바꾸면 이전 task 의 늦은 응답을 버린다", async () => {
    let finishA!: (response: Response) => void;
    live = (path) =>
      path.includes("/tasks/a/")
        ? new Promise<Response>((resolve) => {
            finishA = resolve;
          })
        : liveBody(run({ run_ref: "run-b" }));
    const view = render(panel("a"));
    await act(() => Promise.resolve());
    view.rerender(panel("b"));
    expect(await screen.findByText("run-b")).toBeVisible();
    await act(async () => finishA(liveBody(run({ run_ref: "run-a" }))));
    expect(screen.getByText("run-b")).toBeVisible();
    expect(screen.queryByText("run-a")).toBeNull();
  });
});
