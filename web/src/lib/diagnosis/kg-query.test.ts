import { afterEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "./api";
import { getKgQuery, KG_LIMITS, kgFailureOf, kgQueryPath, kgRejectReason, parseKgQuery } from "./kg-query";
import { KG_E01, KG_E02, KG_E03, KG_E04, KG_E05, KG_E07, kgCase, kgTreeCase } from "./kg-query-fixtures";

type Json = Record<string, unknown>;
// biome-ignore lint/suspicious/noExplicitAny: 시험에서 응답 JSON 을 마음대로 고쳐 계약 오류로 거부되는지 본다
type W = any;
const clone = <T>(v: T): T => structuredClone(v);
/** 응답 하나를 고쳐 거부되는지 본다 */
function mutate(base: Json, edit: (r: W) => void): Json {
  const r: W = clone(base);
  edit(r);
  return r;
}
function must(raw: unknown) {
  const g = parseKgQuery(raw);
  if (!g) throw new Error(`거부됨: ${kgRejectReason(raw)}`);
  return g;
}
const accepts = (r: unknown) => expect(kgRejectReason(r)).toBeNull();
const rejects = (r: unknown) => expect(parseKgQuery(r)).toBeNull();

describe("parseKgQuery — §7 계약 사례", () => {
  it("E01 전체 응답을 받는다: 현재 작업 하나, 직접 연결 자산 하나, 소유·진단·정책 출처 미조회", () => {
    const g = parseKgQuery(clone(KG_E01));
    expect(g).not.toBeNull();
    expect(g?.taskId).toBe("41");
    expect(g?.tasks).toEqual([{ id: "t:41", taskId: "41", role: "current" }]);
    expect(g?.assets).toEqual([{ id: "a:101", assetId: "101", assetType: "endpoint" }]);
    expect(g?.edges[0]).toMatchObject({ kind: "task_asset", basis: "attached", refs: ["link:41:101"] });
    expect(g?.sections).toEqual({
      ownership: "not_requested",
      diagnosis: "not_requested",
      policySource: "not_requested",
    });
    expect(g?.policySource).toBeNull();
  });

  it.each([
    ["E02 수동 등록", KG_E02],
    ["E03 fact 관측 참조", KG_E03],
    ["E04 직접 원천 상속", KG_E04],
    ["E05 범위 자산", KG_E05],
    ["E07 소유 근거 없음", KG_E07],
  ])("%s(E01 최상위 교체)를 받는다", (_name, patch) => {
    accepts(kgCase(patch));
  });

  it("E03: 모델이 observed 로 적어도 관측은 모델 주장으로만 남고 자산은 unknown 이다", () => {
    const g = must(kgCase(KG_E03));
    const anchor = g.provenance.find((p) => p.kind === "exploration_anchor");
    expect(anchor).toMatchObject({
      nodeId: "901",
      observation: { authority: "model_claim", claimedConfidence: "observed" },
    });
  });

  it("E04: 원천 작업 출처는 inherited, 관계 출처는 inherited=false", () => {
    const g = must(kgCase(KG_E04));
    expect(g.tasks.map((t) => t.role)).toEqual(["current", "source"]);
    expect(g.provenance.find((p) => p.id === "link:40:101")).toMatchObject({ inherited: true });
  });

  it("포함 트리 사례를 받는다", () => {
    accepts(kgTreeCase());
  });

  it("정상 빈 결과(현재 작업 노드만)를 받는다", () => {
    accepts(kgCase({ nodes: [(KG_E01.nodes as unknown[])[0]], edges: [], provenance: [] }));
  });
});

describe("parseKgQuery — 닫힌 파서(§5.2 알려진 필드·종류·상태 밖 = 계약 오류)", () => {
  it.each<[string, (r: W) => void]>([
    ["모르는 최상위 칸", (r) => (r.extra = 1)],
    ["빠진 최상위 칸", (r) => delete r.issues],
    ["다른 version", (r) => (r.version = "abc2lab.kg-query/v2")],
    ["숫자 task_id", (r) => (r.task_id = 41)],
    ["선행 0 task_id", (r) => (r.task_id = "041")],
    ["int64 초과 ID", (r) => (r.nodes[1].asset_id = "9223372036854775808")],
    ["scope.kind 다름", (r) => (r.scope.kind = "run")],
    ["source_depth 2", (r) => (r.scope.source_depth = 2)],
    ["execution_scope 값", (r) => (r.scope.execution_scope = { run: "1" })],
    ["ownership 조회됨으로 위장", (r) => (r.sections.ownership = "complete")],
    ["diagnosis 조회됨으로 위장", (r) => (r.sections.diagnosis = "complete")],
    ["assets 부분", (r) => (r.sections.assets = "partial")],
    ["policy_source 값(v1 은 항상 null)", (r) => (r.policy_source = { kind: "kg_snapshot", ref: "x" })],
    ["sections.policy_source 조회됨", (r) => (r.sections.policy_source = "complete")],
    ["page has_more", (r) => (r.page.has_more = true)],
    ["page cursor", (r) => (r.page.next_cursor = "abc")],
    ["자산 노드에 소유자 칸", (r) => (r.nodes[1].owner_ref = "acct-a")],
    ["URL 라벨", (r) => (r.nodes[1].label = "http://example.test/api?q=1")],
    ["유형과 다른 라벨", (r) => (r.nodes[1].label = "서비스 #101")],
    ["모르는 자산 유형", (r) => (r.nodes[1].asset_type = "company")],
    ["observed 관측 상태", (r) => (r.nodes[1].observation_state = "observed")],
    ["작업 노드 ID 공식 위반", (r) => (r.nodes[0].id = "task:41")],
    ["첫 노드가 원천", (r) => (r.nodes[0].role = "source")],
    ["관계 ID 공식 위반", (r) => (r.edges[0].id = "edge-1")],
    ["attached 가 derived", (r) => (r.edges[0].state = "derived")],
    ["모르는 basis", (r) => (r.edges[0].basis = "guess")],
    ["끝점이 응답 밖", (r) => (r.edges[0].dst = "a:999")],
    ["풀리지 않는 출처 참조", (r) => (r.edges[0].provenance_refs = ["link:41:999"])],
    ["빈 출처 참조", (r) => (r.edges[0].provenance_refs = [])],
    ["출처 ID 공식 위반", (r) => (r.provenance[0].id = "link:41:102")],
    ["미등록 link source 원문 반사", (r) => (r.provenance[0].source = "imported-by-bob")],
    ["inherited 불일치", (r) => (r.provenance[0].inherited = true)],
    ["출처에 summary 원문", (r) => (r.provenance[0].summary = "Bearer abc")],
    ["provenance null 대신 생략", (r) => delete r.provenance[0].observation],
    ["모르는 issue code", (r) => (r.issues = [{ code: "guess", state: "insufficient", refs: [] }])],
    [
      "issue state 가 code 와 다름",
      (r) => (r.issues = [{ code: "membership_mismatch", state: "insufficient", refs: [] }]),
    ],
    [
      "issue ref 가 응답 밖",
      (r) => (r.issues = [{ code: "legacy_link_missing", state: "insufficient", refs: ["a:999"] }]),
    ],
  ])("%s → 거부", (_name, edit) => {
    rejects(mutate(KG_E01, edit));
  });

  it("자산이 ID 오름차순이 아니면 거부(DB 미지정 순서에 기대지 않음)", () => {
    const r = kgTreeCase() as W;
    [r.nodes[1], r.nodes[2]] = [r.nodes[2], r.nodes[1]];
    rejects(r);
  });

  it("관계·출처가 ID 사전순이 아니면 거부", () => {
    rejects(mutate(kgCase(KG_E03), (r) => r.edges.reverse()));
    rejects(mutate(kgCase(KG_E03), (r) => r.provenance.reverse()));
  });

  it("포함 근거 없는 자산은 거부(선택 집합 밖 자산을 그리지 않음)", () => {
    rejects(
      mutate(KG_E01, (r) => {
        r.edges = [];
        r.provenance = [];
      }),
    );
  });

  it("포함 규칙과 유형이 맞지 않으면 거부(순환·추정 포함 관계 방지)", () => {
    expect(
      kgRejectReason(
        mutate(kgTreeCase(), (r) => {
          const p = r.provenance.find((x: Json) => x.id === "fields:4:3:endpoint_origin");
          const e = r.edges.find((x: Json) => x.id === "contained_by:a:4:a:3");
          p.rule = "service_ip";
          p.id = "fields:4:3:service_ip";
          e.provenance_refs = ["fields:4:3:service_ip"];
        }),
      ),
    ).toMatch(/rule types/);
  });

  it("한 자식에 부모가 둘이면 거부(애매하면 서버가 관계를 생략하고 issue 를 남긴다)", () => {
    const r = kgTreeCase() as W;
    // 서비스 6 을 더하고 엔드포인트 4 를 서비스 3·6 둘 다에 붙인다
    r.nodes.push({
      id: "a:6",
      kind: "asset",
      asset_id: "6",
      asset_type: "service",
      label: "서비스 #6",
      context_state: "included",
      observation_state: "unknown",
    });
    r.edges.push(
      {
        id: "task_asset:t:41:a:6:scope",
        kind: "task_asset",
        src: "t:41",
        dst: "a:6",
        basis: "scope",
        state: "derived",
        provenance_refs: ["scope:20:6"],
      },
      {
        id: "contained_by:a:4:a:6",
        kind: "contained_by",
        src: "a:4",
        dst: "a:6",
        basis: "asset_fields",
        state: "derived",
        provenance_refs: ["fields:4:6:endpoint_origin"],
      },
    );
    r.provenance.push(
      {
        id: "scope:20:6",
        kind: "task_scope",
        scope_id: "20",
        task_id: "41",
        asset_id: "6",
        source: "manual",
        inherited: false,
        observation: null,
      },
      {
        id: "fields:4:6:endpoint_origin",
        kind: "asset_fields",
        task_id: null,
        asset_id: "4",
        parent_asset_id: "6",
        rule: "endpoint_origin",
        source: null,
        inherited: false,
        observation: null,
      },
    );
    const byId = (x: Json, y: Json) => (String(x.id) < String(y.id) ? -1 : 1);
    r.edges.sort(byId);
    r.provenance.sort(byId);
    expect(kgRejectReason(r)).toBe("edges: two parents");
  });

  it("원천 작업의 끝나지 않은 intent 관측은 거부(§3.1 종료 네 상태만 상속)", () => {
    const e04 = kgCase(KG_E04) as W;
    e04.provenance[0].observation = {
      node_id: "77",
      kind: "intent",
      source_state: "running",
      claimed_confidence: "unknown",
      authority: "unverified_reference",
    };
    rejects(e04);
    e04.provenance[0].observation.source_state = "done";
    accepts(e04);
  });

  it("fact 가 아닌 관측에 모델 주장 신뢰도를 실으면 거부", () => {
    rejects(
      mutate(kgCase(KG_E03), (r) => {
        r.provenance[0].observation.kind = "finding";
      }),
    );
  });

  it("앵커 ID 의 노드와 관측 node_id 가 다르면 거부", () => {
    rejects(mutate(kgCase(KG_E03), (r) => (r.provenance[0].observation.node_id = "902")));
  });

  it("원천 작업에 source_task 관계가 없으면 거부", () => {
    rejects(
      mutate(kgCase(KG_E04), (r) => {
        r.edges = r.edges.filter((e: Json) => e.kind !== "source_task");
      }),
    );
  });

  it("상한 초과 노드 수는 200 이어도 거부(서버는 413 이어야 한다)", () => {
    const nodes = [(KG_E01.nodes as unknown[])[0]];
    for (let i = 1; i <= KG_LIMITS.nodes; i++)
      nodes.push({
        id: `a:${i}`,
        kind: "asset",
        asset_id: String(i),
        asset_type: "ip",
        label: `IP #${i}`,
        context_state: "included",
        observation_state: "unknown",
      });
    expect(kgRejectReason(kgCase({ nodes }))).toBe("nodes: limit");
  });
});

describe("kgFailureOf — HTTP 상태 먼저, kg 봉투는 있을 때만(§11-3)", () => {
  const kg = (status: number, code: string, retryable: boolean) => {
    const e = new ApiError(status, code);
    e.code = code;
    e.retryable = retryable;
    return e;
  };

  it("413 kg_graph_too_large 는 다시 읽기 불가", () => {
    expect(kgFailureOf(kg(413, "kg_graph_too_large", false))).toEqual({ failure: "too_large", retryable: false });
  });
  it("503 은 봉투가 있으면 그 retryable, 없으면(중계 writeErr) 다시 읽기 가능", () => {
    expect(kgFailureOf(kg(503, "kg_store_unavailable", true))).toEqual({
      failure: "store_unavailable",
      retryable: true,
    });
    expect(kgFailureOf(new ApiError(503, "report graph unavailable"))).toEqual({
      failure: "store_unavailable",
      retryable: true,
    });
  });
  it("중계가 낸 403(writeErr)은 상태로 권한 실패", () => {
    expect(kgFailureOf(new ApiError(403, "forbidden"))).toEqual({ failure: "forbidden", retryable: false });
  });
  it("봉투 없는 400 은 v1 미지원 서버일 수 있어 요청 형식 오류와 나눈다", () => {
    expect(kgFailureOf(new ApiError(400, "query not allowed")).failure).toBe("unsupported");
    expect(kgFailureOf(kg(400, "kg_request_invalid", false)).failure).toBe("invalid_request");
  });
  it("상태와 맞지 않는 code 는 쓰지 않는다", () => {
    expect(kgFailureOf(kg(503, "kg_graph_too_large", false))).toEqual({
      failure: "store_unavailable",
      retryable: true,
    });
  });
  it("수신 상한·JSON 손상은 계약 오류, 연결 실패는 offline", () => {
    expect(kgFailureOf(new ApiError(422, "response_limit")).failure).toBe("contract");
    expect(kgFailureOf(new ApiError(422, "invalid_contract")).failure).toBe("contract");
    expect(kgFailureOf(new ApiError(0, "x")).failure).toBe("offline");
    expect(kgFailureOf(new ApiError(502, "x")).failure).toBe("offline");
  });
});

describe("getKgQuery — 실제 fetch 경로", () => {
  afterEach(() => vi.unstubAllGlobals());
  const respond = (status: number, body: unknown) =>
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }),
        ),
    );

  it("정확히 view=kg-v1 하나만 붙여 부른다", async () => {
    respond(200, KG_E01);
    const r = await getKgQuery("41");
    expect(r.state).toBe("ready");
    const [path] = (fetch as unknown as ReturnType<typeof vi.fn>).mock.calls[0];
    expect(path).toBe("/api/tasks/41/coverage-graph?view=kg-v1");
    expect(kgQueryPath("41")).toBe(path);
  });

  it("413 kg 봉투가 그대로 too_large·다시 읽기 불가로 온다(§9 Q19)", async () => {
    respond(413, { version: "abc2lab.kg-query/v1", error: { code: "kg_graph_too_large", retryable: false } });
    expect(await getKgQuery("41")).toEqual({ state: "error", failure: "too_large", retryable: false });
  });

  it("다른 task 의 그래프는 받지 않는다", async () => {
    respond(200, KG_E01);
    expect(await getKgQuery("42")).toEqual({ state: "error", failure: "contract", retryable: false });
  });

  it("계약 밖 200 응답은 그래프를 내지 않는다", async () => {
    respond(200, { ...KG_E01, nodes: "x" });
    expect(await getKgQuery("41")).toEqual({ state: "error", failure: "contract", retryable: false });
  });

  it("형식이 틀린 task ID 는 부르지 않는다", async () => {
    respond(200, KG_E01);
    expect((await getKgQuery("0")).state).toBe("error");
    expect(fetch).not.toHaveBeenCalled();
  });
});
