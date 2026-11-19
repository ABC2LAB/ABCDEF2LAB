// 제품 판정 목록 조회 (GET /api/tasks/{id}/product-verdicts, 계약 docs/contracts/product-verdict-list-proposal.md, #801·#806).
// 파서는 서버(server/product_verdict_list.go·finding_verdict_detail.go·diagnosisprep/product_declaration.go)가 만들 수 있는
// 모양만 받는다(#689 방어 파서 원칙). 하나라도 어긋나면 응답 전체를 깨진 응답으로 본다 — 일부만 그리지 않는다.
// 평가 판정(source_kind=eval)은 이 응답에 절대 없다. 빈 목록은 '판정 없음'이지 '취약점 없음'이 아니다(절대 규칙 5).

import { apiFetch } from "@/lib/diagnosis/api";

export const PRODUCT_VERDICTS_FORMAT = "jindo.workbench-product-verdicts/v1";
/** 서버 고정 제목(OD11 제품 제목). */
export const PRODUCT_VERDICTS_TITLE = "제품 확정 단계 판정";
/** 한 작업의 판정 수 상한 = 선언 자원 종류 상한(e3contract.MaxDeclarationTypes). */
export const MAX_PRODUCT_VERDICTS = 16;
/** 판정 상세 OD08 배열 상한(maxVerdictDetailReasons · maxVerdictDetailCandidateRefs). */
const MAX_REASONS = 32;
const MAX_CANDIDATES = 64;

export type OracleValue = "confirmed" | "suspected" | "undecidable" | "allowed";
export type CheckState = "yes" | "no" | "unknown";
export type RecordStatus = "verified" | "policy_source_unavailable" | "record_integrity_failed";
export type ConfirmationStatus =
  | "disabled"
  | "start_refused"
  | "completed"
  | "interrupted"
  | "confirmation_integrity_failed"
  | "unreadable";

/** 판정 근거의 봉인 확인(E1, docs/contracts/e3-product-verdict-store.md §7). 근거 호출이 없는 판정은 null. */
export type EvidenceSeal =
  | { status: "confirmed"; reason: null }
  | { status: "unconfirmed"; reason: "seal_not_recorded" | "evidence_checkpoint_missing" }
  | { status: "inclusion_failed"; reason: null };

/** 진위를 확인했거나(verified) 당시 원본만 못 읽은(policy_source_unavailable) 행 — 값이 있다. */
export interface ProductVerdictShown {
  verdictId: string;
  recordStatus: "verified" | "policy_source_unavailable";
  resourceType: string;
  resourceRef: string;
  oracleValue: OracleValue;
  checks: { policy: CheckState; identity: CheckState; data: CheckState };
  oracleReasonCodes: string[];
  productReasonCodes: string[];
  candidateRefs: string[];
  evidenceCalls: number;
  observedAt: string;
  evidenceSeal: EvidenceSeal | null;
}
/** 진위 확인 실패 행 — 서버가 값 · 검사 · 자원을 숨긴다. verdict_id 만 있을 수 있다. */
export interface ProductVerdictHidden {
  verdictId: string | null;
  recordStatus: "record_integrity_failed";
  /** 봉인 포함 실패로 숨긴 행이면 그 표시가 남는다. */
  evidenceSeal: EvidenceSeal | null;
}
export type ProductVerdictRow = ProductVerdictShown | ProductVerdictHidden;

export interface ProductVerdictList {
  taskId: string;
  /** 동결본을 못 읽었거나 없으면 null. */
  policySource: {
    ref: string;
    digest: string;
    /** 진위 확인을 통과한 행들이 모두 같은 판일 때만. */
    targetApprovalRevision: string | null;
    /** 동결본이 두 라벨을 정했을 때만(둘 다 있거나 둘 다 없음). */
    ownerAccountLabel: string | null;
    crossAccountLabel: string | null;
  } | null;
  /** 결과 파일이 없으면 null — '꺼짐'으로 추정하지 않는다. */
  confirmation: { status: ConfirmationStatus; reason: string | null } | null;
  items: ProductVerdictRow[];
}

// 서버 정규식 그대로
const DECIMAL = /^[1-9][0-9]{0,18}$/;
const PRODUCT_REF = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;
const PRODUCT_CODE = /^[a-z][a-z0-9_]{0,63}$/;
const HEX64 = /^[0-9a-f]{64}$/;
const ACCOUNT_LABEL = /^[a-z0-9][a-z0-9_-]{0,31}$/;
const CANONICAL_TIME = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{6}Z$/;
const POLICY_REF_PREFIX = "declaration-";

const ORACLE_VALUES = new Set<string>(["confirmed", "suspected", "undecidable", "allowed"]);
const CHECK_STATES = new Set<string>(["yes", "no", "unknown"]);
const CONFIRMATION_STATUSES = new Set<string>([
  "disabled",
  "start_refused",
  "completed",
  "interrupted",
  "confirmation_integrity_failed",
  "unreadable",
]);
/** 오라클 사유(oracle/model.go JudgeReason, server accessMapReason 과 같은 목록). */
const ORACLE_REASONS = new Set<string>([
  "policy_deny_resolved",
  "policy_allows",
  "sharing_or_delegation_allows",
  "expected_access_policy_missing",
  "expected_access_policy_invalid",
  "expected_access_unknown",
  "sharing_or_delegation_unknown",
  "subjects_distinct_verified",
  "subject_not_verified",
  "subjects_not_distinct",
  "data_nontrivial_match",
  "data_no_match",
  "data_reflection_present",
  "data_template_present",
  "data_unverifiable",
]);

const TOP_KEYS = ["format", "task_id", "source_kind", "title", "policy_source", "confirmation", "items"];
const POLICY_KEYS = ["ref", "digest_sha256", "target_approval_revision", "owner_account_label", "cross_account_label"];
const CONFIRMATION_KEYS = ["status", "reason"];
const ITEM_KEYS = [
  "verdict_id",
  "record_status",
  "resource_type",
  "resource_ref",
  "oracle_value",
  "checks",
  "oracle_reason_codes",
  "product_reason_codes",
  "candidate_refs",
  "evidence_calls",
  "observed_at",
  "evidence_seal",
];
const CHECK_KEYS = ["policy", "identity", "data"];
const SEAL_KEYS = ["status", "reason"];
const SEAL_UNCONFIRMED_REASONS = new Set<string>(["seal_not_recorded", "evidence_checkpoint_missing"]);

function parseSeal(v: unknown, where: string): EvidenceSeal | null {
  if (v === null) return null;
  const o = obj(v, SEAL_KEYS, where);
  if (o.status === "unconfirmed" && typeof o.reason === "string" && SEAL_UNCONFIRMED_REASONS.has(o.reason))
    return { status: "unconfirmed", reason: o.reason as "seal_not_recorded" | "evidence_checkpoint_missing" };
  if ((o.status === "confirmed" || o.status === "inclusion_failed") && o.reason === null)
    return { status: o.status, reason: null };
  throw new ProductVerdictsShapeError(where);
}

class ProductVerdictsShapeError extends Error {
  constructor(where: string) {
    super(`invalid_product_verdicts:${where}`);
    this.name = "ProductVerdictsShapeError";
  }
}
export function isProductVerdictsShapeError(e: unknown): boolean {
  return e instanceof ProductVerdictsShapeError;
}

type Obj = Record<string, unknown>;
function obj(v: unknown, keys: readonly string[], where: string): Obj {
  if (!v || typeof v !== "object" || Array.isArray(v)) throw new ProductVerdictsShapeError(where);
  const got = Object.keys(v).sort();
  const want = [...keys].sort();
  if (got.length !== want.length || got.some((k, i) => k !== want[i])) throw new ProductVerdictsShapeError(where);
  return v as Obj;
}
function str(v: unknown, re: RegExp, where: string): string {
  if (typeof v !== "string" || !re.test(v)) throw new ProductVerdictsShapeError(where);
  return v;
}
function optStr(v: unknown, re: RegExp, where: string): string | null {
  return v === null ? null : str(v, re, where);
}
function list(v: unknown, max: number, ok: (s: string) => boolean, where: string): string[] {
  if (!Array.isArray(v) || v.length > max) throw new ProductVerdictsShapeError(where);
  const seen = new Set<string>();
  for (const s of v) {
    if (typeof s !== "string" || !ok(s) || seen.has(s)) throw new ProductVerdictsShapeError(where);
    seen.add(s);
  }
  return v as string[];
}

function parseItem(v: unknown, i: number): ProductVerdictRow {
  const where = `items[${i}]`;
  const o = obj(v, ITEM_KEYS, where);
  const status = o.record_status;
  const seal = parseSeal(o.evidence_seal, `${where}.evidence_seal`);
  if (status === "record_integrity_failed") {
    // 숨긴 행에는 봉인 포함 실패 표시만 남을 수 있다.
    if (seal !== null && seal.status !== "inclusion_failed")
      throw new ProductVerdictsShapeError(`${where}.evidence_seal`);
    const empty = (k: string) => Array.isArray(o[k]) && (o[k] as unknown[]).length === 0;
    if (
      o.resource_type !== null ||
      o.resource_ref !== null ||
      o.oracle_value !== null ||
      o.checks !== null ||
      o.evidence_calls !== null ||
      o.observed_at !== null ||
      !empty("oracle_reason_codes") ||
      !empty("product_reason_codes") ||
      !empty("candidate_refs")
    )
      throw new ProductVerdictsShapeError(`${where}.hidden`);
    return {
      verdictId: optStr(o.verdict_id, DECIMAL, `${where}.verdict_id`),
      recordStatus: status,
      evidenceSeal: seal,
    };
  }
  if (status !== "verified" && status !== "policy_source_unavailable")
    throw new ProductVerdictsShapeError(`${where}.record_status`);
  const resourceType = str(o.resource_type, PRODUCT_CODE, `${where}.resource_type`);
  const resourceRef = str(o.resource_ref, PRODUCT_REF, `${where}.resource_ref`);
  if (!resourceRef.startsWith(`${resourceType}:`)) throw new ProductVerdictsShapeError(`${where}.resource_ref`);
  const value = o.oracle_value;
  if (typeof value !== "string" || !ORACLE_VALUES.has(value))
    throw new ProductVerdictsShapeError(`${where}.oracle_value`);
  const c = obj(o.checks, CHECK_KEYS, `${where}.checks`);
  for (const k of CHECK_KEYS)
    if (typeof c[k] !== "string" || !CHECK_STATES.has(c[k] as string))
      throw new ProductVerdictsShapeError(`${where}.checks.${k}`);
  const evidence = o.evidence_calls;
  if (typeof evidence !== "number" || !Number.isSafeInteger(evidence) || evidence < 0)
    throw new ProductVerdictsShapeError(`${where}.evidence_calls`);
  const row: ProductVerdictShown = {
    verdictId: str(o.verdict_id, DECIMAL, `${where}.verdict_id`),
    recordStatus: status,
    resourceType,
    resourceRef,
    oracleValue: value as OracleValue,
    checks: { policy: c.policy as CheckState, identity: c.identity as CheckState, data: c.data as CheckState },
    oracleReasonCodes: list(
      o.oracle_reason_codes,
      MAX_REASONS,
      (s) => ORACLE_REASONS.has(s),
      `${where}.oracle_reason_codes`,
    ),
    productReasonCodes: list(
      o.product_reason_codes,
      MAX_REASONS,
      (s) => PRODUCT_CODE.test(s),
      `${where}.product_reason_codes`,
    ),
    candidateRefs: list(o.candidate_refs, MAX_CANDIDATES, (s) => PRODUCT_REF.test(s), `${where}.candidate_refs`),
    evidenceCalls: evidence,
    observedAt: str(o.observed_at, CANONICAL_TIME, `${where}.observed_at`),
    evidenceSeal: seal,
  };
  // 값이 보이는 행은 포함 실패일 수 없다(서버가 숨긴다). 서버는 근거 호출이 없는 판정에만 null 을 주고, 근거가 있으면
  // 늘 확인됨 · 미확인 중 하나를 준다(productEvidenceSealFor).
  if (seal?.status === "inclusion_failed" || (row.evidenceCalls === 0) !== (seal === null))
    throw new ProductVerdictsShapeError(`${where}.evidence_seal`);
  // 서버 저장 불변식(verdict-record §2(f)): 확정은 세 검사 yes · 근거 호출 1개 이상 · 제품 사유 없음일 때만
  if (
    row.oracleValue === "confirmed" &&
    (row.checks.policy !== "yes" ||
      row.checks.identity !== "yes" ||
      row.checks.data !== "yes" ||
      row.evidenceCalls < 1 ||
      row.productReasonCodes.length > 0)
  )
    throw new ProductVerdictsShapeError(`${where}.confirmed`);
  return row;
}

/** 응답 본문 검사. taskId 는 요청한 작업 — 다른 작업의 응답을 받지 않는다. */
export function parseProductVerdicts(raw: unknown, taskId: string): ProductVerdictList {
  const top = obj(raw, TOP_KEYS, "top");
  if (
    top.format !== PRODUCT_VERDICTS_FORMAT ||
    top.task_id !== taskId ||
    top.source_kind !== "product" ||
    top.title !== PRODUCT_VERDICTS_TITLE
  )
    throw new ProductVerdictsShapeError("envelope");
  let policySource: ProductVerdictList["policySource"] = null;
  if (top.policy_source !== null) {
    const p = obj(top.policy_source, POLICY_KEYS, "policy_source");
    const ref = typeof p.ref === "string" ? p.ref : "";
    if (!ref.startsWith(POLICY_REF_PREFIX) || !PRODUCT_REF.test(ref.slice(POLICY_REF_PREFIX.length)))
      throw new ProductVerdictsShapeError("policy_source.ref");
    const owner = optStr(p.owner_account_label, ACCOUNT_LABEL, "policy_source.owner_account_label");
    const cross = optStr(p.cross_account_label, ACCOUNT_LABEL, "policy_source.cross_account_label");
    if ((owner === null) !== (cross === null) || (owner !== null && owner === cross))
      throw new ProductVerdictsShapeError("policy_source.labels");
    policySource = {
      ref,
      digest: str(p.digest_sha256, HEX64, "policy_source.digest_sha256"),
      targetApprovalRevision: optStr(p.target_approval_revision, PRODUCT_REF, "policy_source.target_approval_revision"),
      ownerAccountLabel: owner,
      crossAccountLabel: cross,
    };
  }
  let confirmation: ProductVerdictList["confirmation"] = null;
  if (top.confirmation !== null) {
    const c = obj(top.confirmation, CONFIRMATION_KEYS, "confirmation");
    if (typeof c.status !== "string" || !CONFIRMATION_STATUSES.has(c.status))
      throw new ProductVerdictsShapeError("confirmation.status");
    const reason = optStr(c.reason, PRODUCT_CODE, "confirmation.reason");
    if (c.status === "unreadable" && reason !== null) throw new ProductVerdictsShapeError("confirmation.reason");
    confirmation = { status: c.status as ConfirmationStatus, reason };
  }
  if (!Array.isArray(top.items) || top.items.length > MAX_PRODUCT_VERDICTS)
    throw new ProductVerdictsShapeError("items");
  const items = top.items.map(parseItem);
  const ids = items.map((r) => r.verdictId).filter((id): id is string => id !== null);
  if (new Set(ids).size !== ids.length) throw new ProductVerdictsShapeError("items.verdict_id");
  // 동결본 정책 출처가 없으면 당시 원본 대조를 할 수 없다 — 그때 '확인함(verified)' 행은 서버가 만들 수 없다
  if (policySource === null && items.some((r) => r.recordStatus === "verified"))
    throw new ProductVerdictsShapeError("items.record_status");
  return { taskId, policySource, confirmation, items };
}

/** GET /api/tasks/{id}/product-verdicts. 제품 관리 중계에만 있다(제품이 아니면 404). */
export async function fetchProductVerdicts(taskId: string, signal?: AbortSignal): Promise<ProductVerdictList> {
  if (!DECIMAL.test(taskId)) throw new ProductVerdictsShapeError("task_id");
  const raw = await apiFetch<unknown>(`/api/tasks/${taskId}/product-verdicts`, { cache: "no-store", signal });
  return parseProductVerdicts(raw, taskId);
}

// ---------- 표시 글 ----------

export const ORACLE_VALUE_TEXT: Record<OracleValue, string> = {
  confirmed: "확정",
  suspected: "의심",
  undecidable: "판단 불가",
  allowed: "정책상 허용",
};
export const CHECK_TEXT: Record<CheckState, string> = { yes: "예", no: "아니오", unknown: "모름" };
export const CONFIRMATION_TEXT: Record<ConfirmationStatus, string> = {
  completed: "확정 단계 마침",
  disabled: "확정 단계 꺼짐",
  start_refused: "확정 단계 시작 거절",
  interrupted: "확정 단계 중단",
  confirmation_integrity_failed: "확정 단계 결과 무결성 실패",
  unreadable: "확정 단계 결과 파일을 읽지 못함",
};
export const RECORD_STATUS_TEXT: Record<RecordStatus, string> = {
  verified: "당시 정책 원본과 대조함",
  policy_source_unavailable: "당시 원본 미확인",
  record_integrity_failed: "판정 기록 무결성 확인 실패 · 값 숨김",
};

export const EVIDENCE_SEAL_TEXT: Record<EvidenceSeal["status"], string> = {
  confirmed: "봉인 확인됨",
  unconfirmed: "봉인 미확인",
  inclusion_failed: "봉인 포함 실패 · 값 숨김",
};

/** 봉인 미확인 사유 — 판정 값은 저장된 그대로다(E1 계약 §7). */
export const EVIDENCE_SEAL_REASON_TEXT: Record<"seal_not_recorded" | "evidence_checkpoint_missing", string> = {
  seal_not_recorded: "봉인 확인 기록 없음",
  evidence_checkpoint_missing: "근거 체크포인트 없음",
};

/** 화면 한 줄 문구. 미확인이면 사유와 '값은 저장된 그대로'를 붙인다. */
export function evidenceSealText(seal: EvidenceSeal): string {
  if (seal.status !== "unconfirmed") return EVIDENCE_SEAL_TEXT[seal.status];
  return `${EVIDENCE_SEAL_TEXT.unconfirmed} · ${EVIDENCE_SEAL_REASON_TEXT[seal.reason]} · 판정 값은 저장된 그대로`;
}
