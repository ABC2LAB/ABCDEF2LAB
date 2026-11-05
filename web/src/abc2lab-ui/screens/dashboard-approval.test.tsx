import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { type ReactNode, useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "../lib/api";
import { ApprovalContext } from "../lib/approval";
import * as engine from "../lib/engine";
import { SelectionContext } from "../lib/selection";
import { NavContext, type ScreenId } from "../nav";
import ApprovalScreen from "./ApprovalScreen";
import DashboardScreen from "./DashboardScreen";

vi.mock("../lib/engine", () => ({
  listTasks: vi.fn(),
  listPendingApprovals: vi.fn(),
  getPendingApproval: vi.fn(),
  decidePendingApproval: vi.fn(),
  getTokenUsage: vi.fn(),
  getLLM: vi.fn(),
  getInterceptRules: vi.fn(),
  getCoverage: vi.fn(),
}));

const iso = (agoMs: number) => new Date(Date.now() - agoMs).toISOString();
const task = (id: string, status: string) => ({
  id,
  name: `점검 ${id}`,
  description: "",
  goal: "",
  status,
  created_at: iso(3_600_000),
  completed_at: status === "done" ? iso(60_000) : "",
  goals_total: 0,
  goals_met: 0,
  in_flight: 0,
  findings: { critical: 0, high: 0, medium: 0, low: 0 },
});
const pending = (id: number, taskId: string) => ({
  id,
  task_id: taskId,
  agent_name: "work#1",
  tool_name: "http_request",
  status: "pending",
  created_at: iso(60_000),
});
const tasksOf = (...t: ReturnType<typeof task>[]) => ({ active: "", tasks: t }) as unknown as engine.TaskListResponse;

/** ABC2LabUI 와 같은 방식으로 화면 전환 · 선택 · 승인 대상을 실제 상태로 들고 있는 틀 */
function Harness({ children }: { children: ReactNode }) {
  const [screenId, setScreen] = useState<ScreenId>("dashboard");
  const [taskId, setTaskId] = useState<string | null>(null);
  const [version, setVersion] = useState(0);
  const [pendingId, setPendingId] = useState<number | null>(null);
  const go = (s: ScreenId) => setScreen(s);
  return (
    <NavContext.Provider value={{ screen: screenId, go }}>
      <SelectionContext.Provider
        value={{ taskId, selectTask: setTaskId, tasksVersion: version, refreshTasks: () => setVersion((v) => v + 1) }}
      >
        <ApprovalContext.Provider
          value={{
            pendingId,
            selectPending: setPendingId,
            openApproval: (id) => {
              setPendingId(id);
              go("approval");
            },
          }}
        >
          <button type="button" onClick={() => go("history")}>
            다른 화면으로
          </button>
          <button type="button" onClick={() => go("dashboard")}>
            대시보드로
          </button>
          {children}
        </ApprovalContext.Provider>
      </SelectionContext.Provider>
    </NavContext.Provider>
  );
}

function show() {
  return render(
    <Harness>
      <DashboardScreen />
      <ApprovalScreen />
    </Harness>,
  );
}

const tileValue = (label: string) =>
  within(screen.getByText(label, { selector: ".tile .k" }).closest(".tile") as HTMLElement).getByText(/^\d+$|^—$/, {
    selector: ".v",
  });

beforeEach(() => {
  vi.resetAllMocks();
  Element.prototype.scrollIntoView = vi.fn();
  vi.mocked(engine.getTokenUsage).mockResolvedValue({ byProfile: [], daily: [] });
  vi.mocked(engine.getLLM).mockResolvedValue({
    provider: "openai",
    model: "m",
    base_url: "http://127.0.0.1:1234/v1",
    configured: true,
    key_set: false,
    context_window_k: 0,
  });
  vi.mocked(engine.getInterceptRules).mockResolvedValue([]);
  vi.mocked(engine.getCoverage).mockResolvedValue(null);
});
afterEach(cleanup);

describe("dashboard snapshot refresh", () => {
  beforeEach(() => {
    vi.mocked(engine.listTasks).mockResolvedValue(tasksOf(task("a", "running")));
    vi.mocked(engine.listPendingApprovals).mockResolvedValue([]);
  });
  const finishAndAsk = () => {
    vi.mocked(engine.listTasks).mockResolvedValue(tasksOf(task("a", "done")));
    vi.mocked(engine.listPendingApprovals).mockResolvedValue([pending(7, "a")]);
  };

  it("reads tasks and approvals again when the dashboard is entered again", async () => {
    show();
    await waitFor(() => expect(tileValue("진행 중인 점검")).toHaveTextContent("1"));
    expect(tileValue("승인 대기")).toHaveTextContent("0");

    finishAndAsk();
    fireEvent.click(screen.getByRole("button", { name: "다른 화면으로" }));
    fireEvent.click(screen.getByRole("button", { name: "대시보드로" }));

    await waitFor(() => expect(tileValue("진행 중인 점검")).toHaveTextContent("0"));
    await waitFor(() => expect(tileValue("승인 대기")).toHaveTextContent("1"));
    expect(engine.listTasks).toHaveBeenCalledTimes(2);
    expect(engine.listPendingApprovals).toHaveBeenCalledTimes(2);
  });

  it("reads them again on 새로고침 while staying on the dashboard", async () => {
    show();
    await waitFor(() => expect(tileValue("진행 중인 점검")).toHaveTextContent("1"));

    finishAndAsk();
    fireEvent.click(screen.getByRole("button", { name: "새로고침" }));

    await waitFor(() => expect(tileValue("진행 중인 점검")).toHaveTextContent("0"));
    await waitFor(() => expect(tileValue("승인 대기")).toHaveTextContent("1"));
  });

  it("does not query while the dashboard is hidden", async () => {
    show();
    await waitFor(() => expect(engine.listTasks).toHaveBeenCalledTimes(1));
    fireEvent.click(screen.getByRole("button", { name: "다른 화면으로" }));
    await new Promise((r) => setTimeout(r, 20));
    expect(engine.listTasks).toHaveBeenCalledTimes(1);
  });
});

describe("approving a real pending request from the dashboard", () => {
  beforeEach(() => {
    vi.mocked(engine.listTasks).mockResolvedValue(tasksOf(task("a", "running"), task("b", "running")));
    vi.mocked(engine.listPendingApprovals).mockResolvedValue([pending(41, "a"), pending(42, "b")]);
    vi.mocked(engine.getPendingApproval).mockImplementation(async (id) => ({
      ...pending(id, id === 41 ? "a" : "b"),
      tool_input: { url: "https://shop.test/api/users/2", headers: { Authorization: "Bearer real-secret" } },
      reason: "가로채기 규칙 [상태 변경 승인] 에 따라 사용자 승인이 필요합니다",
    }));
  });

  const openRowFor = async (taskLabel: string) => {
    const buttons = await screen.findAllByRole("button", { name: "승인하기" });
    const button = buttons.find((b) => within(b.closest(".recent-row") as HTMLElement).queryByText(taskLabel));
    if (!button) throw new Error(`승인하기 없음: ${taskLabel}`);
    fireEvent.click(button);
  };

  it("binds the chosen request id and sends that decision", async () => {
    vi.mocked(engine.decidePendingApproval).mockResolvedValue(undefined);
    show();
    await waitFor(() => expect(screen.getAllByRole("button", { name: "승인하기" })).toHaveLength(2));
    await openRowFor("점검 b");

    await waitFor(() => expect(engine.getPendingApproval).toHaveBeenCalledWith(42));
    expect(engine.getPendingApproval).not.toHaveBeenCalledWith(41);
    // 요청 내용은 보이되 비밀 값은 가려진다
    expect(await screen.findByText(/shop\.test\/api\/users\/2/)).toBeVisible();
    expect(screen.queryByText(/real-secret/)).toBeNull();

    vi.mocked(engine.getPendingApproval).mockResolvedValue({
      ...pending(42, "b"),
      status: "allowed",
      tool_input: {},
    });
    fireEvent.click(screen.getByRole("button", { name: /이번 한 번만 허용/ }));

    await waitFor(() => expect(engine.decidePendingApproval).toHaveBeenCalledWith(42, "allowed"));
    expect(engine.decidePendingApproval).toHaveBeenCalledTimes(1);
    expect(await screen.findByText("요청 #42을 허용했어요. 에이전트가 이 요청 하나를 보내요.")).toBeVisible();
    // 다시 읽은 요청 상태로 결과를 확인하고, 더 결정할 수 없다
    expect(await screen.findByText("허용됨", { selector: "strong" })).toBeVisible();
    expect(screen.queryByRole("button", { name: /이번 한 번만 허용/ })).toBeNull();
  });

  it("reports a request someone else already decided", async () => {
    vi.mocked(engine.decidePendingApproval).mockRejectedValue(
      new ApiError(409, "이미 처리되었거나 존재하지 않는 승인입니다"),
    );
    show();
    await waitFor(() => expect(screen.getAllByRole("button", { name: "승인하기" })).toHaveLength(2));
    await openRowFor("점검 a");
    await waitFor(() => expect(engine.getPendingApproval).toHaveBeenCalledWith(41));
    fireEvent.click(await screen.findByRole("button", { name: /거부/ }));

    await waitFor(() => expect(engine.decidePendingApproval).toHaveBeenCalledWith(41, "denied"));
    expect(await screen.findByText("요청 #41은 이미 처리된 요청이에요.")).toBeVisible();
  });
});
