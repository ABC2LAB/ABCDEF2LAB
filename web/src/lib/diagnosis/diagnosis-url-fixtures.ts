import type { URLDiagnosisCatalog, URLDiagnosisHandle, URLDiagnosisJob, URLDiagnosisScope } from "./diagnosis-url";

export const owner = "a".repeat(64);
export const catalog: URLDiagnosisCatalog = {
  ownerContext: owner,
  profiles: [{ id: "profile-1", label: "승인된 읽기", model_profile_ref: "synthetic", model_label: "합성 모델" }],
};
export const scope: URLDiagnosisScope = {
  target_ref: "target-1",
  scope_ref: "scope-1",
  origin: "https://target.example",
  path_prefixes: ["/"],
  methods: ["GET"],
  query_policy: "any",
  body_policy: "bounded",
  max_body_bytes: 0,
  approval_mode: "preauthorized_scope",
  attestation_ref: "attestation-1",
  expires_at: "2030-01-01T00:00:00Z",
  budget: {
    requests: 3,
    model_requests: 2,
    input_tokens: 1000,
    output_tokens: 1000,
    cost_usd_micros: 0,
    duration_seconds: 60,
  },
};
export const draft: URLDiagnosisJob = {
  id: "job-1",
  revision: 1,
  source_digest: "c".repeat(64),
  input: { target_url: "https://target.example/", profile_id: "profile-1" },
  state: "awaiting_approval",
  phase: "draft",
  preparation_started: false,
  authority_state: "not_issued",
  started: false,
  drain_complete: false,
  result_ready: false,
};
export const ready: URLDiagnosisJob = { ...draft, revision: 2, state: "ready", phase: "scope_check", scope };
export const running: URLDiagnosisJob = {
  ...ready,
  revision: 5,
  state: "running",
  phase: "execution",
  task_id: "42",
  preparation_started: true,
  authority_state: "consumed",
  started: true,
};
export const handle: URLDiagnosisHandle = {
  ownerContext: owner,
  creationKey: "create-1",
  input: draft.input,
  id: draft.id,
  revision: 2,
  sourceDigest: draft.source_digest,
};
export const envelope = (value: unknown) => ({ version: "diagnosis-api.v2", owner_context: owner, draft: value });

export const externalModel = {
  mode: "openrouter",
  model: "qwen/qwen3.6-35b-a3b",
  provider: "fixture/endpoint",
  external_transfer: true,
  input_tokens_per_call: 262144,
  output_tokens_per_call: 2048,
} as const;
export const externalScope: URLDiagnosisScope = {
  ...scope,
  model: externalModel,
  budget: { ...scope.budget, input_tokens: 524288, output_tokens: 4096, cost_usd_micros: 6000000 },
};
export const externalReady: URLDiagnosisJob = { ...ready, scope: externalScope };
