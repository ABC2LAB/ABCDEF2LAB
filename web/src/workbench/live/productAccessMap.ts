// 제품 접근통제 지도 조회 (GET /api/tasks/{id}/product-access-map, 계약 docs/contracts/product-access-map.md).
// 수평(자원 종류 × 소유 A · 교차 B)만이다. 평가 지도(lib/diagnosis/kg.ts checkAccessMapResponse)와 파서 · 상태 표를 공유하지
// 않는다 — owner_control · na · na_basis 는 이 파서에서만 받는다(§3 파서 분리). 파서는 서버(server/product_access_map.go)가
// 만들 수 있는 모양만 받는다(#689 방어 파서 원칙): 하나라도 어긋나면 응답 전체를 깨진 응답으로 보고 일부만 그리지 않는다.

import { apiFetch } from "@/lib/diagnosis/api";

import {
  type CheckState,
  type ConfirmationStatus,
  type EvidenceSeal,
  isOracleReason,
  MAX_PRODUCT_VERDICTS,
  parseEvidenceSeal,
} from "./productVerdicts";

export const PRODUCT_ACCESS_MAP_FORMAT = "jindo.workbench-product-access-map/v1";
/** 선언 자원 종류 상한(e3contract.MaxDeclarationTypes). 숨긴 행 수도 판정 행 상한(종류 수)을 넘지 않는다. */
const MAX_TYPES = MAX_PRODUCT_VERDICTS;
const MAX_REASONS = 32;
const MAX_CANDIDATES = 64;

export type ProductMapState = "owner_control" | "na" | "confirmed" | "suspected" | "unknown";
export type ProductMapRole = "owner" | "cross";
/** 칸 투영 오류(§2.3). 오라클 사유와 따로다. */
export type ProductMapError =
  | "selection_ambiguous"
  | "record_invalid"
  | "record_hidden"
  | "record_missing_after_attempt"
  | "observation_unavailable"
  | "na_evidence_mismatch";

/** 판정이 실린 B 칸의 판정 값(확정 · 의심 · 판단 불가만). */
export interface ProductMapVerdict {
  verdictId: string;
  oracleValue: "confirmed" | "suspected" | "undecidable";
  checks: { policy: CheckState; identity: CheckState; data: CheckState };
  oracleReasonCodes: string[];
  productReasonCodes: string[];
  candidateRefs: string[];
  observedAt: string;
  evidenceSeal: EvidenceSeal | null;
}

export interface ProductMapCell {
  principal: string;
  role: ProductMapRole;
  resourceType: string;
  state: ProductMapState;
  /** owner = null(판정 대상 아님), 공유 종류 = false, 시험 대상 종류 = true. */
  applicable: boolean | null;
  naBasis: "declared_shared" | null;
  verdict: ProductMapVerdict | null;
  projectionError: ProductMapError | null;
}

export interface ProductMapResource {
  resourceType: string;
  sharing: "owner_only" | "shared";
  confirmable: boolean;
}

export interface ProductAccessMap {
  taskId: string;
  policySource: { ref: string; digest: string };
  /** 결과 파일이 없으면 null — 확정 단계가 아직 끝나지 않았다(§3 O2). '꺼짐'으로 추정하지 않는다. */
  confirmation: { status: ConfirmationStatus; reason: string | null } | null;
  owner: string;
  cross: string;
  resources: ProductMapResource[];
  hiddenRecords: number;
  cells: ProductMapCell[];
}

// 서버 정규식 그대로(server/product_verdict_list.go · diagnosisprep · db/e3_product_verdicts.go)
const DECIMAL = /^[1-9][0-9]{0,18}$/;
const PRODUCT_REF = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;
const PRODUCT_CODE = /^[a-z][a-z0-9_]{0,63}$/;
const HEX64 = /^[0-9a-f]{64}$/;
const ACCOUNT_LABEL = /^[a-z0-9][a-z0-9_-]{0,31}$/;
const CANONICAL_TIME = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{6}Z$/;
const POLICY_REF_PREFIX = "declaration-";

const CHECK_STATES = new Set<string>(["yes", "no", "unknown"]);
const CONFIRMATION_STATUSES = new Set<string>([
  "disabled",
  "start_refused",
  "completed",
  "interrupted",
  "confirmation_integrity_failed",
  "unreadable",
]);
/** 판정 없는 시험 대상 칸의 오류 — 공유 칸 · 소유 칸에는 오지 않는다. */
const CROSS_EMPTY_ERRORS = new Set<string>([
  "selection_ambiguous",
  "record_invalid",
  "record_hidden",
  "record_missing_after_attempt",
  "observation_unavailable",
]);

const TOP_KEYS = [
  "format",
  "task_id",
  "source_kind",
  "axis",
  "policy_source",
  "confirmation",
  "principals",
  "resources",
  "hidden_records",
  "cells",
];
const CELL_KEYS = [
  "principal",
  "resource_type",
  "state",
  "applicable",
  "na_basis",
  "verdict_id",
  "record_status",
  "oracle_value",
  "checks",
  "oracle_reason_codes",
  "product_reason_codes",
  "candidate_refs",
  "observed_at",
  "evidence_seal",
  "projection_error",
];
const VERDICT_KEYS = ["verdict_id", "record_status", "oracle_value", "checks", "observed_at", "evidence_seal"] as const;

class ProductAccessMapShapeError extends Error {
  constructor(where: string) {
    super(`invalid_product_access_map:${where}`);
    this.name = "ProductAccessMapShapeError";
  }
}
export function isProductAccessMapShapeError(e: unknown): boolean {
  return e instanceof ProductAccessMapShapeError;
}

type Obj = Record<string, unknown>;
function obj(v: unknown, keys: readonly string[], where: string): Obj {
  if (!v || typeof v !== "object" || Array.isArray(v)) throw new ProductAccessMapShapeError(where);
  const got = Object.keys(v).sort();
  const want = [...keys].sort();
  if (got.length !== want.length || got.some((k, i) => k !== want[i])) throw new ProductAccessMapShapeError(where);
  return v as Obj;
}
function str(v: unknown, re: RegExp, where: string): string {
  if (typeof v !== "string" || !re.test(v)) throw new ProductAccessMapShapeError(where);
  return v;
}
function list(v: unknown, max: number, ok: (s: string) => boolean, where: string): string[] {
  if (!Array.isArray(v) || v.length > max) throw new ProductAccessMapShapeError(where);
  const seen = new Set<string>();
  for (const s of v) {
    if (typeof s !== "string" || !ok(s) || seen.has(s)) throw new ProductAccessMapShapeError(where);
    seen.add(s);
  }
  return v as string[];
}
function emptyList(v: unknown): boolean {
  return Array.isArray(v) && v.length === 0;
}

/** 판정 칸이 모두 비어 있는지(판정 없는 칸 · 소유 칸 · 공유 칸). */
function noVerdict(o: Obj): boolean {
  return (
    VERDICT_KEYS.every((k) => o[k] === null) &&
    emptyList(o.oracle_reason_codes) &&
    emptyList(o.product_reason_codes) &&
    emptyList(o.candidate_refs)
  );
}

function parseVerdict(o: Obj, where: string): ProductMapVerdict {
  // 지도는 동결본을 확인한 뒤에만 만들어지므로 칸에 오는 판정은 진위를 확인한(verified) 것뿐이다.
  if (o.record_status !== "verified") throw new ProductAccessMapShapeError(`${where}.record_status`);
  const value = o.oracle_value;
  if (value !== "confirmed" && value !== "suspected" && value !== "undecidable")
    throw new ProductAccessMapShapeError(`${where}.oracle_value`);
  const c = obj(o.checks, ["policy", "identity", "data"], `${where}.checks`);
  for (const k of ["policy", "identity", "data"])
    if (typeof c[k] !== "string" || !CHECK_STATES.has(c[k] as string))
      throw new ProductAccessMapShapeError(`${where}.checks.${k}`);
  let seal: EvidenceSeal | null;
  try {
    seal = parseEvidenceSeal(o.evidence_seal, `${where}.evidence_seal`);
  } catch {
    throw new ProductAccessMapShapeError(`${where}.evidence_seal`);
  }
  // 포함 실패는 진위 실패라 서버가 칸에 놓지 않는다.
  if (seal?.status === "inclusion_failed") throw new ProductAccessMapShapeError(`${where}.evidence_seal`);
  const v: ProductMapVerdict = {
    verdictId: str(o.verdict_id, DECIMAL, `${where}.verdict_id`),
    oracleValue: value,
    checks: { policy: c.policy as CheckState, identity: c.identity as CheckState, data: c.data as CheckState },
    oracleReasonCodes: list(o.oracle_reason_codes, MAX_REASONS, isOracleReason, `${where}.oracle_reason_codes`),
    productReasonCodes: list(
      o.product_reason_codes,
      MAX_REASONS,
      (s) => PRODUCT_CODE.test(s),
      `${where}.product_reason_codes`,
    ),
    candidateRefs: list(o.candidate_refs, MAX_CANDIDATES, (s) => PRODUCT_REF.test(s), `${where}.candidate_refs`),
    observedAt: str(o.observed_at, CANONICAL_TIME, `${where}.observed_at`),
    evidenceSeal: seal,
  };
  // 서버 저장 불변식(verdict-record §2(f)): 확정은 세 검사 yes · 제품 사유 없음일 때만. 확정 칸은 세 검사가 yes 여야 한다(§2.3).
  if (
    value === "confirmed" &&
    (v.checks.policy !== "yes" ||
      v.checks.identity !== "yes" ||
      v.checks.data !== "yes" ||
      v.productReasonCodes.length > 0)
  )
    throw new ProductAccessMapShapeError(`${where}.confirmed`);
  return v;
}

interface CellContext {
  resource: ProductMapResource;
  role: ProductMapRole;
  label: string;
  hidden: number;
  confirmation: ProductAccessMap["confirmation"];
}

function parseCell(v: unknown, i: number, ctx: CellContext): ProductMapCell {
  const where = `cells[${i}]`;
  const o = obj(v, CELL_KEYS, where);
  if (o.principal !== ctx.label || o.resource_type !== ctx.resource.resourceType)
    throw new ProductAccessMapShapeError(`${where}.order`);
  const err = o.projection_error;
  if (err !== null && (typeof err !== "string" || !PRODUCT_CODE.test(err)))
    throw new ProductAccessMapShapeError(`${where}.projection_error`);
  const base = { principal: ctx.label, role: ctx.role, resourceType: ctx.resource.resourceType };

  if (ctx.role === "owner") {
    // A 열: 선언이 소유 계정으로 정했다는 뜻뿐이다. 판정 · 오류 · 적용 근거가 없다(§2.3 O1).
    if (o.state !== "owner_control" || o.applicable !== null || o.na_basis !== null || err !== null || !noVerdict(o))
      throw new ProductAccessMapShapeError(`${where}.owner`);
    return { ...base, state: "owner_control", applicable: null, naBasis: null, verdict: null, projectionError: null };
  }

  if (!ctx.resource.confirmable) {
    // 공유 종류 B 칸: 적용불가(na · declared_shared) 또는 대조 실패(unknown · record_invalid | na_evidence_mismatch).
    if (o.applicable !== false || !noVerdict(o)) throw new ProductAccessMapShapeError(`${where}.shared`);
    if (o.state === "na" && o.na_basis === "declared_shared" && err === null)
      return {
        ...base,
        state: "na",
        applicable: false,
        naBasis: "declared_shared",
        verdict: null,
        projectionError: null,
      };
    // 결과 파일이 없으면 건너뜀이 비어 대조하지 않는다 — 그때 na_evidence_mismatch 는 나올 수 없다.
    const mismatchOk = err === "na_evidence_mismatch" && ctx.confirmation !== null;
    if (o.state === "unknown" && o.na_basis === null && (err === "record_invalid" || mismatchOk))
      return { ...base, state: "unknown", applicable: false, naBasis: null, verdict: null, projectionError: err };
    throw new ProductAccessMapShapeError(`${where}.shared`);
  }

  // 시험 대상 종류 B 칸.
  if (o.applicable !== true || o.na_basis !== null) throw new ProductAccessMapShapeError(`${where}.cross`);
  if (err !== null) {
    if (o.state !== "unknown" || !CROSS_EMPTY_ERRORS.has(err as string) || !noVerdict(o))
      throw new ProductAccessMapShapeError(`${where}.cross_error`);
    // 서버 순서(§2.3): 숨긴 행이 있으면 판정 없는 칸은 record_hidden, 없으면 결과 상태로 가른다.
    const completed = ctx.confirmation?.status === "completed";
    if (
      (err === "record_hidden" && ctx.hidden === 0) ||
      ((err === "record_missing_after_attempt" || err === "observation_unavailable") && ctx.hidden > 0) ||
      (err === "record_missing_after_attempt" && !completed) ||
      (err === "observation_unavailable" && completed)
    )
      throw new ProductAccessMapShapeError(`${where}.cross_error`);
    return {
      ...base,
      state: "unknown",
      applicable: true,
      naBasis: null,
      verdict: null,
      projectionError: err as ProductMapError,
    };
  }
  const verdict = parseVerdict(o, where);
  const want = verdict.oracleValue === "undecidable" ? "unknown" : verdict.oracleValue;
  if (o.state !== want) throw new ProductAccessMapShapeError(`${where}.state`);
  return { ...base, state: want, applicable: true, naBasis: null, verdict, projectionError: null };
}

/** 응답 본문 검사. taskId 는 요청한 작업 — 다른 작업의 응답을 받지 않는다. */
export function parseProductAccessMap(raw: unknown, taskId: string): ProductAccessMap {
  const top = obj(raw, TOP_KEYS, "top");
  if (
    top.format !== PRODUCT_ACCESS_MAP_FORMAT ||
    top.task_id !== taskId ||
    top.source_kind !== "product" ||
    top.axis !== "horizontal"
  )
    throw new ProductAccessMapShapeError("envelope");
  const p = obj(top.policy_source, ["ref", "digest_sha256"], "policy_source");
  const ref = typeof p.ref === "string" ? p.ref : "";
  if (!ref.startsWith(POLICY_REF_PREFIX) || !PRODUCT_REF.test(ref.slice(POLICY_REF_PREFIX.length)))
    throw new ProductAccessMapShapeError("policy_source.ref");
  const policySource = { ref, digest: str(p.digest_sha256, HEX64, "policy_source.digest_sha256") };

  let confirmation: ProductAccessMap["confirmation"] = null;
  if (top.confirmation !== null) {
    const c = obj(top.confirmation, ["status", "reason"], "confirmation");
    if (typeof c.status !== "string" || !CONFIRMATION_STATUSES.has(c.status))
      throw new ProductAccessMapShapeError("confirmation.status");
    const reason = c.reason === null ? null : str(c.reason, PRODUCT_CODE, "confirmation.reason");
    if (c.status === "unreadable" && reason !== null) throw new ProductAccessMapShapeError("confirmation.reason");
    confirmation = { status: c.status as ConfirmationStatus, reason };
  }

  if (!Array.isArray(top.principals) || top.principals.length !== 2) throw new ProductAccessMapShapeError("principals");
  const [po, pc] = top.principals.map((x, i) => obj(x, ["label", "role"], `principals[${i}]`));
  if (po.role !== "owner" || pc.role !== "cross") throw new ProductAccessMapShapeError("principals.role");
  const owner = str(po.label, ACCOUNT_LABEL, "principals[0].label");
  const cross = str(pc.label, ACCOUNT_LABEL, "principals[1].label");
  if (owner === cross) throw new ProductAccessMapShapeError("principals.label");

  if (!Array.isArray(top.resources) || top.resources.length === 0 || top.resources.length > MAX_TYPES)
    throw new ProductAccessMapShapeError("resources");
  const resources: ProductMapResource[] = top.resources.map((x, i) => {
    const r = obj(x, ["resource_type", "sharing", "confirmable"], `resources[${i}]`);
    const type = str(r.resource_type, PRODUCT_CODE, `resources[${i}].resource_type`);
    // confirmable ⇔ owner_only(양방향, §1). 한 방향만 보면 소유자 전용 종류가 공유 선언으로 보일 수 있다.
    if (
      !((r.sharing === "owner_only" && r.confirmable === true) || (r.sharing === "shared" && r.confirmable === false))
    )
      throw new ProductAccessMapShapeError(`resources[${i}].sharing`);
    return { resourceType: type, sharing: r.sharing, confirmable: r.confirmable };
  });
  for (let i = 1; i < resources.length; i++)
    if (resources[i - 1].resourceType >= resources[i].resourceType)
      throw new ProductAccessMapShapeError("resources.order");

  const hidden = top.hidden_records;
  if (typeof hidden !== "number" || !Number.isSafeInteger(hidden) || hidden < 0 || hidden > MAX_TYPES)
    throw new ProductAccessMapShapeError("hidden_records");

  // 격자 전부, 종류 오름차순 · 그 안에서 owner → cross. 빠진 칸 · 순서 바뀐 칸을 받지 않는다.
  if (!Array.isArray(top.cells) || top.cells.length !== resources.length * 2)
    throw new ProductAccessMapShapeError("cells");
  const rawCells = top.cells;
  const cells = resources.flatMap((resource, r) => [
    parseCell(rawCells[r * 2], r * 2, { resource, role: "owner", label: owner, hidden, confirmation }),
    parseCell(rawCells[r * 2 + 1], r * 2 + 1, { resource, role: "cross", label: cross, hidden, confirmation }),
  ]);
  const ids = cells.flatMap((c) => (c.verdict ? [c.verdict.verdictId] : []));
  if (new Set(ids).size !== ids.length) throw new ProductAccessMapShapeError("cells.verdict_id");
  return { taskId, policySource, confirmation, owner, cross, resources, hiddenRecords: hidden, cells };
}

/** GET /api/tasks/{id}/product-access-map. 제품 관리 중계에만 있다(제품이 아니면 404). */
export async function fetchProductAccessMap(taskId: string, signal?: AbortSignal): Promise<ProductAccessMap> {
  if (!DECIMAL.test(taskId)) throw new ProductAccessMapShapeError("task_id");
  const raw = await apiFetch<unknown>(`/api/tasks/${taskId}/product-access-map`, { cache: "no-store", signal });
  return parseProductAccessMap(raw, taskId);
}

/**
 * 화면이 부르는 얇은 seam(계약 §3). 레이아웃이 바뀌어도 이것만 갈아 끼운다. 제품 판정 목록 seam(fetchProductVerdicts)과
 * 같은 자리(workbench/live)에 둔다 — lib/diagnosis 는 workbench 를 가져오지 않는다.
 */
export const getProductAccessMap = fetchProductAccessMap;

// ---------- 표시 글 ----------

/** 칸 이름. owner_control 은 A 조회 성공으로 읽히지 않게 중립 문구다(O1). */
export const PRODUCT_MAP_STATE_TEXT: Record<ProductMapState, string> = {
  owner_control: "소유 계정(선언) · 판정 대상 아님",
  na: "— (공유 선언)",
  confirmed: "확정",
  suspected: "의심",
  unknown: "판단 불가",
};

export const PRODUCT_MAP_ERROR_TEXT: Record<ProductMapError, string> = {
  selection_ambiguous: "판정 둘 이상 · 선택 안 함",
  record_invalid: "판정 기록이 칸 규칙과 맞지 않음",
  record_hidden: "무결성 실패로 숨긴 판정이 이 종류일 수 있음",
  record_missing_after_attempt: "확정 단계를 마쳤는데 판정 기록 없음",
  observation_unavailable: "관측 근거 없음(확정 단계 미완료)",
  na_evidence_mismatch: "공유 선언과 확정 단계 건너뜀 기록이 맞지 않음",
};
