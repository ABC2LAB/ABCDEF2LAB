import { beforeEach, expect, it, vi } from "vitest";

import { ApiError, apiFetch } from "@/lib/diagnosis/api";
import { parseURLJob, urlDiagnosisInput } from "@/lib/diagnosis/diagnosis-url";
import { handle, running, scope } from "@/lib/diagnosis/diagnosis-url-fixtures";

import { refreshWorkflow, resetUrl, runWorkflowAction, useUrl } from "../live/urlDiagnosis";
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
