import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import * as engine from "../lib/engine";
import { NavContext } from "../nav";
import SettingsScreen from "./SettingsScreen";

vi.mock("../lib/engine", () => ({ getInterceptRules: vi.fn(), getLLM: vi.fn() }));
function rule(id: number, name: string, action: string, priority: number, enabled = true): engine.EngineInterceptRule {
  return {
    id,
    name,
    action,
    priority,
    enabled,
    match_target: "tool_name",
    match_type: "string",
    pattern: "synthetic-tool",
  };
}
beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(engine.getLLM).mockResolvedValue({
    provider: "local",
    model: "test",
    base_url: "http://localhost",
    configured: false,
    key_set: false,
    context_window_k: 0,
  });
  // API order is priority DESC, id ASC. A higher-priority allow can win before deny.
  vi.mocked(engine.getInterceptRules).mockResolvedValue([
    rule(3, "높은 우선 허용", "allow", 100),
    rule(2, "검토 규칙", "ask", 20),
    rule(1, "낮은 우선 차단", "deny", 5),
    rule(4, "꺼진 차단", "deny", 1, false),
    rule(5, "알 수 없는 동작", "future-action", 0, false),
  ]);
});
afterEach(cleanup);
function show() {
  return render(
    <NavContext.Provider value={{ screen: "settings", go: vi.fn() }}>
      <SettingsScreen />
    </NavContext.Provider>,
  );
}
describe("rule display groups", () => {
  it("keeps action groups while exposing the real priority and tie-break semantics", async () => {
    show();
    await screen.findByText("높은 우선 허용");
    const allow = screen.getByText("높은 우선 허용").closest(".frow") as HTMLElement;
    const deny = screen.getByText("낮은 우선 차단").closest(".frow") as HTMLElement;
    expect(within(allow).getByText("우선순위 100 · ID 3")).toBeVisible();
    expect(within(deny).getByText("우선순위 5 · ID 1")).toBeVisible();
    expect(screen.getByText(/그룹은 표시용/)).toHaveTextContent("우선순위 숫자가 큰 규칙부터");
    expect(screen.getByText(/그룹은 표시용/)).toHaveTextContent("같으면 ID가 작은 순서");
    expect(screen.getByText(/그룹은 표시용/)).toHaveTextContent("먼저 일치한");
    const labels = screen.getAllByRole("heading", { level: 4 }).map((h) => h.textContent);
    expect(labels).toEqual(["차단 · 2", "승인 필요 · 1", "허용 · 1", "기타 · 1"]);
  });
  it("preserves disabled and unknown-action rows without labeling the unknown action allowed", async () => {
    show();
    await screen.findByText("알 수 없는 동작");
    const unknown = screen.getByText("알 수 없는 동작").closest(".frow") as HTMLElement;
    const disabled = screen.getByText("꺼진 차단").closest(".frow") as HTMLElement;
    expect(within(unknown).getByText("알 수 없음")).toBeVisible();
    expect(within(unknown).queryByText("허용")).toBeNull();
    expect(within(unknown).getByText("꺼짐")).toBeVisible();
    expect(within(disabled).getByText("꺼짐")).toBeVisible();
    expect(screen.getByText("설정 켜짐 3")).toBeVisible();
  });
  it("does not render empty groups as a successful result after lookup fails", async () => {
    vi.mocked(engine.getInterceptRules).mockRejectedValue(new Error("synthetic rules failure"));
    show();
    expect(await screen.findByText(/통제 규칙을 불러오지 못했어요/)).toBeVisible();
    expect(screen.queryByText("등록된 통제 규칙이 없어요.")).toBeNull();
    expect(screen.queryByRole("heading", { level: 4 })).toBeNull();
  });

  it("labels enabled legacy rules as configuration without claiming the invalid regex is applied", async () => {
    vi.mocked(engine.getInterceptRules).mockResolvedValue([
      { ...rule(6, "기존 정규식 오류 규칙", "deny", 10), match_type: "regex", pattern: "(" },
    ]);
    show();
    const row = (await screen.findByText("기존 정규식 오류 규칙")).closest(".frow") as HTMLElement;
    expect(within(row).getByText("설정 켜짐")).toBeVisible();
    expect(within(row).queryByText("사용")).toBeNull();
    expect(screen.getByText("설정 켜짐 1")).toBeVisible();
    expect(screen.getByText(/정규식 오류가 있는 규칙은 적용되지 않아요/)).toBeVisible();
  });
});
