// 탐색 화면 '보낸 요청'(e2-flow)은 진행 중일 때만 주기적으로 다시 읽고, 끝난 점검은 한 번만 읽는다(#938 윤재상 P3).

import "@testing-library/jest-dom/vitest";
import { cleanup, render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../views/live/sentRequests", async (original) => ({
  ...(await original<typeof import("../views/live/sentRequests")>()),
  useE2FlowFor: vi.fn(() => ({
    data: null,
    error: null,
    loading: false,
    unsupported: false,
    reload: () => undefined,
    fetchedAt: null,
  })),
}));

import { catalog, handle, running } from "@/lib/diagnosis/diagnosis-url-fixtures";

import { resetUrl, useUrl } from "../live/urlDiagnosis";
import { type Shell, ShellContext } from "../shell";
import { useE2FlowFor } from "../views/live/sentRequests";
import { WorkflowCrawlLive } from "../views/live/workflowViews";

const noop = () => undefined;
const shell: Shell = {
  route: "crawl",
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

beforeEach(() => {
  vi.mocked(useE2FlowFor).mockClear();
  resetUrl({ kind: "v2", catalog });
});
afterEach(cleanup);

describe("WorkflowCrawlLive — 보낸 요청 e2-flow 조회 주기", () => {
  it.each([
    ["running", false],
    ["completed", true],
    ["cancelled", true],
    ["failed", true],
  ] as const)("점검 상태 %s → ended=%s 로 읽는다", (state, ended) => {
    useUrl.setState({ handle, job: { ...running, state }, verified: true });
    render(
      <ShellContext.Provider value={shell}>
        <WorkflowCrawlLive />
      </ShellContext.Provider>,
    );
    expect(vi.mocked(useE2FlowFor)).toHaveBeenLastCalledWith(running.task_id, true, ended);
  });
});
