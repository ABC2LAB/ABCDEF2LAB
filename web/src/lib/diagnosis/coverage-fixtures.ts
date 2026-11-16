// 시험 전용 workflow snapshot. 제품 fallback으로 사용하지 않는다.
import { running } from "./diagnosis-url-fixtures";
export const coverageJob = { ...running, input: { ...running.input, workflow_mode: "user_approval_v1" as const } };
export const coverageSnapshot = () => ({
  workflow: {
    format: "abc2lab.workbench-flow/v1",
    job_id: coverageJob.id,
    task_id: coverageJob.task_id,
    source_digest: coverageJob.source_digest,
    run_ref: `run-${coverageJob.id}`,
    mode: coverageJob.input.workflow_mode,
    revision: 7,
    stage: "awaiting_selection",
    counts: { found: 3, visited: 2, queued: 0, sent: 2, attempted: 2, unknown: 1, blocked: 0 },
    endpoints: [
      {
        id: "one",
        method: "GET",
        path: "/observed",
        source: "browser_xhr",
        visited: true,
        selected: true,
        status: 200,
      },
      { id: "two", method: "POST", path: "/unvisited", source: "api_recon_static", visited: false, selected: false },
      {
        id: "three",
        method: "GET",
        path: "/incomplete",
        source: "html_link",
        visited: true,
        selected: false,
        observation_error: "body_incomplete",
      },
    ],
  },
});
