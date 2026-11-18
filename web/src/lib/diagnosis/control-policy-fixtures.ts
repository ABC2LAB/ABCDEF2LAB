// 시험 전용 계약 응답(전송 통제 정책 읽기 §3). 제품 화면의 fallback으로 사용하지 않는다. 비밀 값 없음.

/** 계약 §3 모양의 합성 응답 (서버 url_live_views.go controlPolicyView 와 같은 칸). */
export function serverPolicy(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    format: "abc2lab.control-policy-view/v1",
    policy_ref: "scope-target-1",
    policy_revision: "rev-1",
    policy_fingerprint_sha256: "a".repeat(64),
    approval_mode: "preauthorized_scope",
    header_mode: "preserve",
    valid_from: "2026-11-01T00:00:00Z",
    valid_until: "2026-12-01T00:00:00Z",
    rules: [
      {
        ref: "target-path-a",
        purpose: "",
        origin: "https://shop.example",
        connect_ip: "10.0.0.5",
        path_prefix: "/api",
        path_scope_ref: "readonly-path-a",
        methods: ["GET", "HEAD"],
        tls_pinned: true,
      },
      {
        ref: "target-path-b",
        purpose: "",
        origin: "https://shop.example",
        connect_ip: "10.0.0.5",
        path_prefix: "/api/orders",
        path_scope_ref: "",
        methods: ["GET", "POST"],
        tls_pinned: true,
      },
    ],
    limits: {
      max_requests: 64,
      max_attempts: 200,
      max_concurrent: 1,
      max_body_bytes: 65536,
      max_response_body_bytes: 16777216,
      request_timeout_ms: 5000,
    },
    accounts: ["tester-a"],
    audit_bound: true,
    ...over,
  };
}
