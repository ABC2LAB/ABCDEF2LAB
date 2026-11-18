import { describe, expect, it } from "vitest";

import {
  type BoundaryRecord,
  type Discovery,
  discoveriesFromWorkflow,
  explorationStateOf,
  mapApiExploration,
  reasonLabel,
  sourceLabel,
  summarizeBoundary,
  summarizeDiscoveries,
  summarizeWorkflowDiscoveries,
} from "../live/exploration";
import type { WorkflowEndpoint } from "../live/workflow";

const rec = (p: Partial<BoundaryRecord>): BoundaryRecord => ({
  seq: 0,
  at: "2026-11-12T00:00:00Z",
  decision: "denied",
  reason: "",
  ingress: "browser",
  destClass: "approved",
  method: "GET",
  status: null,
  bytes: null,
  hasAuthHeader: false,
  ...p,
});

const disc = (p: Partial<Discovery>): Discovery => ({
  id: "d",
  source: "browser_navigation",
  provenance: "inferred",
  method: "GET",
  path: "/",
  kind: "page",
  change: "possible",
  inScope: true,
  visited: false,
  status: null,
  queryKeys: [],
  bodyKeys: [],
  ...p,
});

describe("summarizeBoundary", () => {
  it("전달·차단을 decision 으로 가른다", () => {
    const s = summarizeBoundary([
      rec({ seq: 1, decision: "forwarded", reason: "" }),
      rec({ seq: 2, decision: "forwarded", reason: "" }),
      rec({ seq: 3, decision: "denied", reason: "risky_path" }),
    ]);
    expect(s.forwarded).toBe(2);
    expect(s.denied).toBe(1);
  });

  it("사유별로 많은 것부터, 계약 밖 사유는 unknown 한 칸으로 모은다(안 버림)", () => {
    const s = summarizeBoundary([
      rec({ decision: "denied", reason: "risky_path" }),
      rec({ decision: "denied", reason: "risky_path" }),
      rec({ decision: "denied", reason: "out_of_scope_path" }),
      rec({ decision: "denied", reason: "made_up_reason" }),
    ]);
    expect(s.byReason[0]).toEqual({ reason: "risky_path", label: "위험 경로", count: 2 });
    const unknown = s.byReason.find((r) => r.reason === "unknown");
    expect(unknown).toEqual({ reason: "unknown", label: "알 수 없는 사유", count: 1 });
  });

  it("외부 호스트 수·인증 헤더 수·미등록 입구/목적지를 센다", () => {
    const s = summarizeBoundary([
      rec({
        decision: "denied",
        reason: "external_navigation",
        destClass: "external",
        externalHost: "a.example",
        ingress: "direct",
      }),
      rec({
        decision: "denied",
        reason: "external_navigation",
        destClass: "external",
        externalHost: "a.example",
        ingress: "browser",
      }),
      rec({ decision: "forwarded", reason: "", hasAuthHeader: true, ingress: "weird", destClass: "mystery" }),
    ]);
    expect(s.externalHosts).toEqual([{ host: "a.example", count: 2 }]);
    expect(s.withAuthHeader).toBe(1);
    expect(s.byIngress.find((x) => x.ingress === "unknown")?.count).toBe(1);
    expect(s.byDestClass.find((x) => x.destClass === "unknown")?.count).toBe(1);
  });
});

describe("summarizeDiscoveries", () => {
  it("실제전송/추정·범위·출처별로 센다(미등록 출처는 unknown)", () => {
    const s = summarizeDiscoveries([
      disc({ provenance: "observed", boundarySeq: 1, inScope: true, source: "browser_xhr" }),
      disc({ provenance: "inferred", inScope: false, source: "api_recon_static" }),
      disc({ provenance: "inferred", inScope: true, source: "home_grown" }),
    ]);
    expect(s).toMatchObject({ total: 3, observed: 1, inferred: 2, inScope: 2, outOfScope: 1 });
    expect(s.bySource.find((x) => x.source === "unknown")?.count).toBe(1);
  });
});

describe("mapApiExploration (fail-closed)", () => {
  it("forwarded 만 전달로 보고 그 밖은 denied, 미등록 사유도 원문으로 담아 센다", () => {
    const r = mapApiExploration({
      session_ref: "s1",
      comm_mode: "full_local",
      records: [
        { seq: 1, decision: "forwarded", ingress: "browser", dest_class: "approved", method: "GET", status: 200 },
        { seq: 2, decision: "weird", reason: "future_reason", ingress: "direct", dest_class: "external" },
      ],
      discoveries_dropped: 3,
    });
    expect(r.commMode).toBe("full_local");
    expect(r.records[0].decision).toBe("forwarded");
    expect(r.records[1].decision).toBe("denied");
    expect(r.records[1].reason).toBe("future_reason");
    expect(r.discoveriesDropped).toBe(3);
    // 미등록 사유가 집계에서 unknown 으로 잡힌다(안 버려짐).
    expect(summarizeBoundary(r.records).byReason.find((x) => x.reason === "unknown")?.count).toBe(1);
  });

  it("observed 는 boundary_seq 가 있어야 실제전송, 없으면 추정으로 내린다(절대규칙 5)", () => {
    const r = mapApiExploration({
      discoveries: [
        {
          id: "a",
          source: "browser_xhr",
          provenance: "observed",
          boundary_seq: 7,
          method: "GET",
          path: "/x",
          in_scope: true,
        },
        { id: "b", source: "browser_xhr", provenance: "observed", method: "GET", path: "/y", in_scope: true },
      ],
    });
    expect(r.discoveries[0]).toMatchObject({ provenance: "observed", boundarySeq: 7 });
    expect(r.discoveries[1]).toMatchObject({ provenance: "inferred", boundarySeq: undefined });
  });

  it("키 이름만 받고 64바이트 초과·32개 초과·비문자열은 버린다(값 섞임 방지)", () => {
    const r = mapApiExploration({
      discoveries: [
        {
          id: "a",
          source: "api_recon_static",
          provenance: "inferred",
          method: "GET",
          path: "/x",
          query_keys: ["ok", "x".repeat(65), 42, ""],
          body_keys: Array.from({ length: 40 }, (_, i) => `k${i}`),
        },
      ],
    });
    expect(r.discoveries[0].queryKeys).toEqual(["ok"]);
    expect(r.discoveries[0].bodyKeys).toHaveLength(32);
  });
});

describe("label helpers", () => {
  it("아는 값은 한국어, 모르는 값은 원문", () => {
    expect(reasonLabel("risky_path")).toBe("위험 경로");
    expect(reasonLabel("made_up")).toBe("made_up");
    expect(reasonLabel("")).toBe("—");
    expect(sourceLabel("browser_form")).toBe("브라우저 폼");
    expect(sourceLabel("mystery")).toBe("mystery");
  });
});

const endpoint = (over: Partial<WorkflowEndpoint> & Record<string, unknown> = {}): WorkflowEndpoint =>
  ({
    id: "e1",
    method: "GET",
    path: "/api/items",
    kind: "api",
    source: "html_link",
    from: "/",
    in_scope: true,
    change: "attested_read",
    visited: true,
    status: 200,
    selected: false,
    duplicates: 0,
    ...over,
  }) as WorkflowEndpoint;

describe("discoveriesFromWorkflow (공유 스냅샷 → 발견)", () => {
  it("경로 · 상태 · 출처를 옮기고, 근거 칸이 없으면 근거를 만들지 않는다(크롤러 발견)", () => {
    expect(discoveriesFromWorkflow({ endpoints: [endpoint()] })).toEqual([
      {
        id: "e1",
        method: "GET",
        path: "/api/items",
        source: "html_link",
        preRegistered: false,
        from: "/",
        inScope: true,
        change: "attested_read",
        visited: true,
        status: 200,
        duplicates: 0,
        observationError: null,
        evidence: null,
        queryKeys: [],
        bodyKeys: [],
        contentType: null,
      },
    ]);
  });
  it("observed 는 탐색 seq 가 있으면 실제 관찰, 없으면 '관찰 · 세부 생략'(바이트 압축) — 추정으로 내리지 않는다", () => {
    const [seen, noSeq, guess, odd] = discoveriesFromWorkflow({
      endpoints: [
        endpoint({ id: "a", discovery_confidence: "observed", exploration_seq: 7 }),
        endpoint({ id: "b", discovery_confidence: "observed" }),
        endpoint({ id: "c", discovery_confidence: "inferred" }),
        endpoint({ id: "d", discovery_confidence: "certain", exploration_seq: 3 }),
      ],
    });
    expect([seen.evidence, noSeq.evidence, guess.evidence, odd.evidence]).toEqual([
      "observed",
      "observed_detail_omitted",
      "inferred",
      null,
    ]);
  });
  it("query · 조각 · 역슬래시가 섞인 경로와 발견 위치는 보이지 않는다(값 섞임 방지)", () => {
    const [d] = discoveriesFromWorkflow({
      endpoints: [endpoint({ path: "/api/items?token=synthetic-value", from: "/login#frag" })],
    });
    expect(d.path).toBeNull();
    expect(d.from).toBeNull();
    expect(JSON.stringify(d)).not.toContain("synthetic-value");
  });
  it("키 이름 · content-type 은 이름 · 형식만 받고 범위 밖은 버린다", () => {
    const [d] = discoveriesFromWorkflow({
      endpoints: [
        endpoint({
          query_keys: ["page", "x".repeat(65), 3],
          body_keys: "id",
          content_type: "application/json; charset=utf-8",
        }),
      ],
    });
    expect(d.queryKeys).toEqual(["page"]);
    expect(d.bodyKeys).toEqual([]);
    expect(d.contentType).toBeNull();
  });
  it("응답 코드 범위 밖은 없는 것으로 본다", () => {
    expect(discoveriesFromWorkflow({ endpoints: [endpoint({ status: 0 })] })[0].status).toBeNull();
  });
  it("summarizeWorkflowDiscoveries: 사전 등록(start)은 빼고 범위 · 방문 · 근거 · 출처별(많은 것부터)로 센다", () => {
    const list = discoveriesFromWorkflow({
      endpoints: [
        endpoint({ id: "s", source: "start", path: "/", from: "/" }),
        endpoint({ id: "a", source: "browser_xhr", discovery_confidence: "observed", exploration_seq: 1 }),
        endpoint({ id: "b", in_scope: false, visited: false, status: undefined }),
        endpoint({ id: "c", discovery_confidence: "inferred" }),
        endpoint({ id: "d", source: "browser_xhr", discovery_confidence: "observed" }),
      ],
    });
    expect(list[0].preRegistered).toBe(true);
    expect(summarizeWorkflowDiscoveries(list)).toEqual({
      listed: 5,
      preRegistered: 1,
      found: 4,
      inScope: 3,
      outOfScope: 1,
      visited: 3,
      observed: 1,
      observedDetailOmitted: 1,
      inferred: 1,
      bySource: [
        { source: "browser_xhr", count: 2 },
        { source: "html_link", count: 2 },
      ],
    });
  });
});

describe("explorationStateOf (스냅샷 탐색 실행 상태)", () => {
  it("아는 상태 · 이유만 옮기고, 칸이 없거나 모르는 값이면 상태를 만들지 않는다", () => {
    expect(
      explorationStateOf({ exploration_status: "NOT_RUN", exploration_reason: "browser_unavailable" } as never),
    ).toEqual({
      status: "NOT_RUN",
      reason: "브라우저를 쓸 수 없음",
    });
    expect(explorationStateOf({ exploration_status: "COMPLETED" } as never)).toEqual({
      status: "COMPLETED",
      reason: null,
    });
    expect(explorationStateOf({} as never)).toEqual({ status: null, reason: null });
    expect(
      explorationStateOf({ exploration_status: "DONE", exploration_reason: "browser_unavailable" } as never),
    ).toEqual({
      status: null,
      reason: null,
    });
  });
});
