import { beforeEach, expect, it, vi } from "vitest";

import { ApiError, apiFetch } from "@/lib/diagnosis/api";
import { parseURLJob, urlDiagnosisInput } from "@/lib/diagnosis/diagnosis-url";
import { catalog, handle, running, scope } from "@/lib/diagnosis/diagnosis-url-fixtures";

import { refreshWorkflow, resetUrl, runUrlAction, runWorkflowAction, urlGates, useUrl } from "../live/urlDiagnosis";
import { FLOW_VERSION, parseWorkflow, type WorkflowSnapshot } from "../live/workflow";

vi.mock("@/lib/diagnosis/api", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/api")>()),
  apiFetch: vi.fn(),
}));
const job = {
  ...running,
  input: { ...running.input, workflow_mode: "user_approval_v1" as const },
  scope: { ...scope, approval_mode: "workbench_selective_v1" as const, methods: ["GET", "POST"] },
};
const snapshot: WorkflowSnapshot = {
  format: FLOW_VERSION,
  job_id: job.id,
  task_id: "42",
  run_ref: `run-${job.id}`,
  source_digest: job.source_digest,
  revision: 12,
  mode: "user_approval_v1",
  stage: "awaiting_selection",
  paused: false,
  initial_completed: true,
  reversible_writes: true,
  deep_runs: 0,
  deep_closed: 0,
  limits: {
    format: FLOW_VERSION,
    max_pages: 4,
    max_endpoints: 32,
    max_response_bytes: 4096,
    allow_reversible_writes: true,
  },
  counts: { found: 1, visited: 1, queued: 0, sent: 1, attempted: 1, unknown: 0, blocked: 0 },
  endpoints: [
    {
      id: "endpoint-owned",
      method: "GET",
      path: "/read",
      from: "/",
      kind: "page",
      source: "html_link",
      in_scope: true,
      change: "attested_read",
      visited: true,
      status: 200,
      selected: false,
      duplicates: 0,
    },
  ],
  events: [],
  commands: [],
  updated_at: "2026-11-10T06:00:00Z",
};
beforeEach(() => {
  vi.resetAllMocks();
  resetUrl();
  useUrl.setState({ job, handle: { ...handle, input: job.input }, verified: true, flow: snapshot, flowVerified: true });
});
it("binds selective scope to the manual input and retains exclusions", () => {
  const input = urlDiagnosisInput(
    "https://target.example/",
    "profile-1",
    "owned",
    "user_approval_v1",
    "/admin\n/api/*/delete",
  );
  expect(input.workflow_mode).toBe("user_approval_v1");
  expect(input.excluded_paths).toBe("/admin\n/api/*/delete");
  const envelope = (draft: unknown) => ({ version: "diagnosis-api.v2", owner_context: handle.ownerContext, draft });
  expect(parseURLJob(envelope(job), handle.ownerContext).scope?.methods).toEqual(["GET", "POST"]);
  expect(() =>
    parseURLJob(envelope({ ...job, input: { ...job.input, workflow_mode: "ai_auto_v1" } }), handle.ownerContext),
  ).toThrow();
});
it.each([
  { job_id: "foreign" },
  { task_id: "99" },
  { run_ref: "run-foreign" },
  { source_digest: "b".repeat(64) },
  { deep_runs: 0, deep_closed: 1 },
  { endpoints: [...snapshot.endpoints, ...snapshot.endpoints] },
])("rejects malformed or foreign workflow %j", (change) => {
  expect(() => parseWorkflow({ workflow: { ...snapshot, ...change } }, job)).toThrow();
});
// 끝점 목록 상한은 서버 공개 목록 개수 상한(256)과 같다. events·commands의 32 상한은 그대로다(VAmPI 실제 회차 24b, #603,
// 끝점 바이트 절단).
const withEndpoints = (n: number): WorkflowSnapshot => ({
  ...snapshot,
  limits: { ...snapshot.limits, max_endpoints: 256 },
  endpoints: Array.from({ length: n }, (_, i) => ({
    ...snapshot.endpoints[0],
    id: `endpoint-${i}`,
    path: `/read/${i}`,
  })),
});
it.each([32, 33, 64, 65, 256])("accepts a workflow snapshot with %i endpoints", (n) => {
  expect(parseWorkflow({ workflow: withEndpoints(n) }, job).endpoints).toHaveLength(n);
});
it("rejects a workflow snapshot over the endpoint cap", () => {
  expect(() => parseWorkflow({ workflow: withEndpoints(257) }, job)).toThrow();
});
it("sends exact discovery IDs once and binds the command receipt", async () => {
  vi.mocked(apiFetch).mockImplementation(async (_path, init) => {
    const key = (init?.headers as Record<string, string>)["Idempotency-Key"];
    return {
      workflow: {
        ...snapshot,
        revision: 13,
        stage: "diagnosing",
        commands: [{ key, operation: "selection", revision: 13 }],
      },
    };
  });
  expect(await runWorkflowAction("selection", ["endpoint-owned"])).toBe(true);
  expect(apiFetch).toHaveBeenCalledTimes(1);
  const [_path, init, retry] = vi.mocked(apiFetch).mock.calls[0];
  expect(JSON.parse(String(init?.body))).toEqual({
    version: FLOW_VERSION,
    expected_revision: 12,
    selected_ids: ["endpoint-owned"],
  });
  expect(retry).toBe(false);
  expect(useUrl.getState().flow?.stage).toBe("diagnosing");
});
it("lost command response blocks duplicate sends until GET proves the outcome", async () => {
  vi.mocked(apiFetch).mockRejectedValueOnce(new TypeError("owned response loss"));
  expect(await runWorkflowAction("selection", ["endpoint-owned"])).toBe(false);
  expect(useUrl.getState().flowUncertain).toBe(true);
  expect(await runWorkflowAction("selection", ["endpoint-owned"])).toBe(false);
  expect(apiFetch).toHaveBeenCalledTimes(1);
  vi.mocked(apiFetch).mockResolvedValueOnce({ workflow: snapshot });
  await refreshWorkflow();
  expect(useUrl.getState().flowUncertain).toBe(true);
  vi.mocked(apiFetch).mockResolvedValueOnce({ workflow: { ...snapshot, revision: 13, stage: "diagnosing" } });
  await refreshWorkflow();
  expect(useUrl.getState().flowUncertain).toBe(false);
});
it("ignores a GET that began before a later confirmed mutation", async () => {
  let resolve: (v: unknown) => void = () => undefined;
  vi.mocked(apiFetch).mockReturnValueOnce(
    new Promise((r) => {
      resolve = r;
    }),
  );
  const read = refreshWorkflow();
  vi.mocked(apiFetch).mockImplementationOnce(async (_path, init) => ({
    workflow: {
      ...snapshot,
      revision: 13,
      stage: "diagnosing",
      commands: [
        { key: (init?.headers as Record<string, string>)["Idempotency-Key"], operation: "selection", revision: 13 },
      ],
    },
  }));
  expect(await runWorkflowAction("selection", ["endpoint-owned"])).toBe(true);
  resolve({ workflow: snapshot });
  await read;
  expect(useUrl.getState().flow?.revision).toBe(13);
});
it("known revision conflicts still require a fresh read", async () => {
  vi.mocked(apiFetch).mockRejectedValue(new ApiError(409, "workflow_revision_conflict"));
  expect(await runWorkflowAction("pause")).toBe(false);
  expect(useUrl.getState().flowUncertain).toBe(false);
  expect(useUrl.getState().flowVerified).toBe(false);
});

it("URL status polling does not orphan an in-flight selection or lose its response-loss fence", async () => {
  useUrl.setState({ cap: { kind: "v2", catalog } });
  let rejectLoss: (reason: unknown) => void = () => undefined;
  vi.mocked(apiFetch).mockImplementation(async (path, init) => {
    if (String(path).endsWith("/status"))
      return { version: "diagnosis-api.v2", owner_context: handle.ownerContext, draft: job };
    if (init?.method === "POST")
      return new Promise((_resolve, reject) => {
        rejectLoss = reject;
      });
    return { workflow: { ...snapshot, revision: 13, stage: "diagnosing" } };
  });
  const selection = runWorkflowAction("selection", ["endpoint-owned"]);
  expect(await runUrlAction("refresh")).toBe(true);
  rejectLoss(new TypeError("owned response loss"));
  expect(await selection).toBe(false);
  expect(useUrl.getState().flowBusy).toBe(false);
  expect(useUrl.getState().flowUncertain).toBe(true);
  await refreshWorkflow();
  expect(useUrl.getState().flowUncertain).toBe(false);
  expect(useUrl.getState().flow?.stage).toBe("diagnosing");
});

it("explicit recovery reuses the original key and payload after a POST never reached the server", async () => {
  const { retryWorkflowAction } = await import("../live/urlDiagnosis");
  vi.mocked(apiFetch).mockRejectedValueOnce(new TypeError("owned request loss"));
  expect(await runWorkflowAction("selection", ["endpoint-owned"])).toBe(false);
  const original = vi.mocked(apiFetch).mock.calls[0];
  expect(await retryWorkflowAction()).toBe(false);
  vi.mocked(apiFetch).mockResolvedValueOnce({ workflow: snapshot });
  await refreshWorkflow();
  vi.mocked(apiFetch).mockImplementationOnce(async (_path, init) => ({
    workflow: {
      ...snapshot,
      revision: 13,
      stage: "diagnosing",
      commands: [
        { key: (init?.headers as Record<string, string>)["Idempotency-Key"], operation: "selection", revision: 13 },
      ],
    },
  }));
  expect(await retryWorkflowAction()).toBe(true);
  const replay = vi.mocked(apiFetch).mock.calls[2];
  expect(replay[1]?.body).toBe(original[1]?.body);
  expect(replay[1]?.headers).toEqual(original[1]?.headers);
  expect(replay[2]).toBe(false);
});
it.each([
  "completed",
  "failed",
  "unknown",
  "cancelled",
] as const)("terminal URL state %s blocks workflow POST", async (state) => {
  useUrl.setState({ job: { ...job, state } });
  expect(await runWorkflowAction("pause")).toBe(false);
  expect(apiFetch).not.toHaveBeenCalled();
});

it("recovered binding (reopened from the run list) can still send the scope selection", async () => {
  // 사용자 2026-11-22(1차 리허설 F16): 브라우저를 다시 띄워 진행 목록에서 연 점검도 범위 선택을 보낼 수 있어야 한다.
  // 서버가 진단 흐름 결속·주체를 다시 본다. 생성·시작 키는 여전히 물려받지 않는다(urlGates canStart).
  useUrl.setState({
    handle: {
      kind: "recovered",
      ownerContext: catalog.ownerContext,
      input: job.input,
      id: job.id,
      revision: job.revision,
      sourceDigest: job.source_digest,
      bindingLocked: true,
    },
    job,
    verified: true,
    flow: snapshot,
    flowVerified: true,
  });
  vi.mocked(apiFetch).mockImplementation(async (_path, init) => {
    const key = (init?.headers as Record<string, string>)["Idempotency-Key"];
    return {
      workflow: {
        ...snapshot,
        revision: 13,
        stage: "diagnosing",
        commands: [{ key, operation: "selection", revision: 13 }],
      },
    };
  });
  expect(await runWorkflowAction("selection", ["endpoint-owned"])).toBe(true);
  expect(apiFetch).toHaveBeenCalledTimes(1);
  expect(useUrl.getState().flow?.stage).toBe("diagnosing");
  expect(urlGates(useUrl.getState()).canStart).toBe(false);
});
// 요청을 보낸 흐름 기록 줄에는 요청 식별자 두 개가 같이 붙는다(계약 workbench-engine-workflow, 11/25)
const reqEvent = (over: Record<string, unknown> = {}) => ({
  at: "2026-11-25T01:00:00Z",
  kind: "request",
  method: "GET",
  path: "/read",
  status: 200,
  call_ref: "call-1",
  attempt_id: "a".repeat(32),
  ...over,
});
it("흐름 기록 줄의 요청 식별자(call_ref · attempt_id)를 받는다", () => {
  const parsed = parseWorkflow({ workflow: { ...snapshot, events: [reqEvent()] } }, job);
  expect(parsed.events[0]).toMatchObject({ call_ref: "call-1", attempt_id: "a".repeat(32) });
});
it.each([
  ["하나만 옴", { attempt_id: undefined }],
  ["시도 식별자 모양이 틀림", { attempt_id: "XYZ" }],
  ["호출 식별자 모양이 틀림", { call_ref: "a/b" }],
])("흐름 기록 줄의 요청 식별자가 %s 이면 받지 않는다(다른 요청을 열 수 있음)", (_name, change) => {
  expect(() => parseWorkflow({ workflow: { ...snapshot, events: [reqEvent(change)] } }, job)).toThrow();
});
