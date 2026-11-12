import { describe, expect, it } from "vitest";

import {
  type BoundaryRecord,
  type Discovery,
  getExploration,
  mapApiExploration,
  reasonLabel,
  sourceLabel,
  summarizeBoundary,
  summarizeDiscoveries,
} from "../live/exploration";

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

describe("getExploration", () => {
  it("jobId 없으면 예시(mock)를 준다", async () => {
    const r = await getExploration(null);
    expect(r.mock).toBe(true);
    expect(r.records.length).toBeGreaterThan(0);
    expect(r.discoveries.length).toBeGreaterThan(0);
  });
});
