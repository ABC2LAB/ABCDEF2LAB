"use client";

// 제품 확정 단계 판정 표시 (계약 docs/contracts/product-verdict-list-proposal.md §3).
// - 보고서: 평가 판정 행(verdict-<id>)과 섞지 않고 '제품 확정 단계 판정' 구획을 따로 둔다.
// - 접근통제 지도의 제품 격자는 productAccessMapView(GET .../product-access-map)가 따로 그린다(계약 product-access-map §3).
// 확정도 finding 이 아니다(결정 §9) — 후보 상태(수동 검토 필요)는 바꾸지 않는다. 빈 목록은 '판정 없음'이지 '취약점 없음'이 아니다.

import type { ReactNode } from "react";

import { ApiError } from "@/lib/diagnosis/api";

import { cx } from "../../lib/cx";
import { clockOf, dateTimeOf } from "../../live/map";
import {
  CHECK_TEXT,
  CONFIRMATION_TEXT,
  EVIDENCE_SEAL_TEXT,
  type EvidenceSeal,
  evidenceSealText,
  fetchProductVerdicts,
  isProductVerdictsShapeError,
  ORACLE_VALUE_TEXT,
  type OracleValue,
  type ProductVerdictList,
  type ProductVerdictRow,
  type ProductVerdictShown,
  RECORD_STATUS_TEXT,
} from "../../live/productVerdicts";
import { type Poll, usePoll } from "../../live/usePoll";
import { verdictReasonText } from "../../live/verdictReasons";
import { Icon } from "../../ui/icons";
import s from "../../workbench.module.css";

/** 판정 딱지 모양: 확정 = 기본, 의심 = sus, 판단 불가 = und, 정책상 허용 = neg(보고서 정상 대조와 같은 모양, 글은 '정책상 허용'). */
const VALUE_CLASS: Record<OracleValue, string | undefined> = {
  confirmed: undefined,
  suspected: s.sus,
  undecidable: s.und,
  allowed: s.neg,
};

/** 탭이 보일 때만, 작업마다 한 번 읽는다(저장 자료라 다시 읽기 버튼으로만 갱신). */
export function useProductVerdicts(taskId: string | null, active: boolean): Poll<ProductVerdictList> {
  return usePoll((signal) => fetchProductVerdicts(taskId as string, signal), [taskId], {
    enabled: active && !!taskId,
  });
}

/** 404 = 제품 관리 중계가 아니라 이 경로가 없다(소스 서버 등) → 구획을 두지 않는다('판정 없음'으로 그리지 않음). */
function notInstalled(error: unknown): boolean {
  return error instanceof ApiError && error.status === 404;
}

/** 동결본까지 대조한 확정만 세고, 제품 판정을 합계에 반영하지 못한 상태를 요약에 표시한다. */
export function productConfirmationSummary({
  data,
  error,
  loading,
}: Pick<Poll<ProductVerdictList>, "data" | "error" | "loading">): { confirmed: number; notice: string | null } {
  if (error && !loading) {
    return { confirmed: 0, notice: notInstalled(error) ? null : "확정 단계 판정 읽지 못함 · 합계에 빠짐" };
  }
  if (!data || error) {
    return { confirmed: 0, notice: "확정 단계 판정 읽는 중" };
  }
  const confirmed = data.items.filter((r) => r.recordStatus === "verified" && r.oracleValue === "confirmed").length;
  return { confirmed, notice: null };
}

function failText(error: unknown): string {
  if (isProductVerdictsShapeError(error)) return "제품 판정 응답 형식을 확인하지 못해 표시하지 않습니다.";
  const status = error instanceof ApiError ? error.status : 0;
  const text: Record<number, string> = {
    0: "제품 판정을 읽지 못했습니다(연결 실패).",
    401: "제품 판정을 읽지 못했습니다 · 다시 로그인해 주세요.",
    403: "이 점검의 제품 판정을 볼 권한이 없거나 승인이 바뀌었습니다.",
    409: "이 점검에 결속된 회차가 없어 제품 판정을 읽을 수 없습니다.",
    422: "제품 판정 목록이 상한을 넘어 표시하지 않습니다(잘라 보이지 않음).",
    503: "제품 판정 서비스가 응답하지 않습니다.",
  };
  return text[status] ?? `제품 판정을 읽지 못했습니다 (HTTP ${status}).`;
}

/** 사유 코드들을 한국어 문구로. 코드 원문은 마우스를 올리면 보인다(표에 없는 코드는 '서버 사유 있음'). 판정 상세도 쓴다. */
export function ReasonCodes({ codes }: { codes: readonly string[] }) {
  return (
    <>
      {codes.map((c, i) => (
        <span key={c} title={c} data-reason={c}>
          {`${i ? " · " : ""}${verdictReasonText(c)}`}
        </span>
      ))}
    </>
  );
}

/** 확정 단계 결과 한 줄. 결과 파일이 없으면 '미확인' — 꺼졌다고 추정하지 않는다. */
function Confirmation({ list }: { list: ProductVerdictList }) {
  const c = list.confirmation;
  if (!c) return <>확정 단계 결과 미확인</>;
  return (
    <>
      {CONFIRMATION_TEXT[c.status]}
      {c.reason ? (
        <>
          {" · "}
          <ReasonCodes codes={[c.reason]} />
        </>
      ) : null}
    </>
  );
}

/** 판정이 없을 때 한 줄. 빈 목록은 '취약점 없음'이 아니다(규칙 5). */
function NoVerdicts({ list }: { list: ProductVerdictList }) {
  return (
    <p className={s.empty}>
      제품 판정 없음 · <Confirmation list={list} /> · 취약점이 없다는 뜻이 아닙니다
    </p>
  );
}

/** 출처 딱지: '제품' 딱지 뒤 정책 선언 참조 (계약 §3 `제품 · 정책 선언 declaration-<job>`). 대상 승인 판은 목록에서 뺐다(사용자 11/23). */
function sourceText(list: ProductVerdictList): string {
  const p = list.policySource;
  if (!p) return "정책 선언 동결본 미확인";
  return `정책 선언 ${p.ref}`;
}

/** 읽기 상태(불러오는 중 · 실패 · 미설치)를 공통으로. 그릴 목록이 있으면 null. */
function readState(poll: Poll<ProductVerdictList>): ReactNode | "hide" | null {
  if (poll.error && notInstalled(poll.error)) return "hide";
  if (poll.error)
    return (
      <div className={cx(s.note, s.warn)} role="alert">
        <Icon name="alert" />
        <span className={s["lv-grow"]}>{failText(poll.error)}</span>
        <button className={cx(s.btn, s.sm)} type="button" onClick={poll.reload}>
          제품 판정 다시 읽기
        </button>
      </div>
    );
  if (!poll.data)
    return (
      <p className={s.empty} role="status">
        제품 판정을 불러오는 중…
      </p>
    );
  return null;
}

function Value({ v }: { v: OracleValue }) {
  return <span className={cx(s.vd, VALUE_CLASS[v])}>{ORACLE_VALUE_TEXT[v]}</span>;
}

/**
 * 값이 보이는 판정 한 건의 내용. 목록 줄과 판정 상세(후보 상세 페이지)가 같은 규칙으로 그린다.
 * compact(보고서 목록 줄, 사용자 11/23 '글 다 필요해?'): 판정 번호 · 정상 검사 줄 · 봉인 확인됨을 빼고 관찰 시각만 붙인다.
 * 검사에 '아니오 · 모름'이 있거나 봉인이 확인되지 않았으면 그대로 보인다. 판정 상세는 다 보인다.
 */
export function VerdictBody({ r, compact = false }: { r: ProductVerdictShown; compact?: boolean }) {
  const allYes = r.checks.policy === "yes" && r.checks.identity === "yes" && r.checks.data === "yes";
  const showChecks = !compact || !allYes;
  const showSeal = !!r.evidenceSeal && (!compact || r.evidenceSeal.status !== "confirmed");
  return (
    <>
      <div>
        <Value v={r.oracleValue} /> <b className={s.mono}>{r.resourceType}</b>{" "}
        <span className={s.help}>
          {compact ? `관찰 ${clockOf(r.observedAt) || r.observedAt}` : `판정 #${r.verdictId}`}
        </span>
      </div>
      {showChecks ? (
        <div className={s.help}>
          {`검사: 정책 ${CHECK_TEXT[r.checks.policy]} · 신원 ${CHECK_TEXT[r.checks.identity]} · 데이터 ${CHECK_TEXT[r.checks.data]} · 근거 호출 ${r.evidenceCalls}건 · 관찰 ${dateTimeOf(r.observedAt) || r.observedAt}`}
        </div>
      ) : null}
      {r.oracleReasonCodes.length ? (
        <div className={s.help}>
          판정 사유 <ReasonCodes codes={r.oracleReasonCodes} />
        </div>
      ) : null}
      {r.productReasonCodes.length ? (
        <div className={s.help}>
          세부 사유 <ReasonCodes codes={r.productReasonCodes} />
        </div>
      ) : null}
      <div className={s.help}>
        {/* 확정도 발견(finding)이 아니라 계약 §3 그대로 '검토 대상 후보 N건'이다. 뒤에 붙인 설명 문장은 뺐다(사용자 11/23) */}
        {r.oracleValue === "confirmed"
          ? `확정 판정 · 검토 대상 취약점 ${r.candidateRefs.length}건`
          : `연결된 취약점 ${r.candidateRefs.length}건`}
      </div>
      {r.recordStatus === "policy_source_unavailable" ? (
        <div className={cx(s.note, s.warn)} role="note">
          <Icon name="alert" />
          <span>{`${RECORD_STATUS_TEXT.policy_source_unavailable} · 당시 정책 동결본을 읽지 못해 대조하지 못했습니다(값은 저장된 그대로)`}</span>
        </div>
      ) : null}
      {showSeal && r.evidenceSeal ? (
        <div
          className={r.evidenceSeal.status === "confirmed" ? s.help : cx(s.note, s.warn)}
          data-seal={r.evidenceSeal.status}
        >
          {evidenceSealText(r.evidenceSeal)}
        </div>
      ) : null}
    </>
  );
}

function ShownRow({ r }: { r: ProductVerdictShown }) {
  return (
    <li data-verdict={r.verdictId}>
      <VerdictBody r={r} compact />
    </li>
  );
}

/** 진위 확인 실패로 값을 숨긴 판정 한 건. 봉인 포함 실패면 그 표시를 보인다. 판정 상세도 쓴다. */
export function HiddenVerdict({ id, seal }: { id: string | null; seal: EvidenceSeal | null }) {
  return (
    <>
      <span className={cx(s.vd, s.und)}>값 숨김</span>{" "}
      <span>{`${id ? `판정 #${id} · ` : ""}${seal ? EVIDENCE_SEAL_TEXT[seal.status] : RECORD_STATUS_TEXT.record_integrity_failed}`}</span>
    </>
  );
}

function HiddenRow({ id, seal }: { id: string | null; seal: EvidenceSeal | null }) {
  return (
    <li>
      <HiddenVerdict id={id} seal={seal} />
    </li>
  );
}

function Row({ r }: { r: ProductVerdictRow }) {
  return r.recordStatus === "record_integrity_failed" ? (
    <HiddenRow id={r.verdictId} seal={r.evidenceSeal} />
  ) : (
    <ShownRow r={r} />
  );
}

/** 보고서 '취약점 목록' 탭 아래 구획. poll 을 주면 그 조회를 쓴다(보고서 요약 칸과 한 번만 읽게). */
export function ProductVerdictReport({
  taskId,
  active,
  poll: given,
}: {
  taskId: string | null;
  active: boolean;
  poll?: Poll<ProductVerdictList>;
}) {
  const own = useProductVerdicts(taskId, active && !given);
  const poll = given ?? own;
  if (!taskId) return null;
  const state = readState(poll);
  if (state === "hide") return null;
  const list = poll.data;
  return (
    <section className={s.sec} aria-label="제품 확정 단계 판정">
      <div className={s.sh}>
        <h2>제품 확정 단계 판정</h2>
        {list && !state ? <span className={s.n}>{`${list.items.length}건`}</span> : null}
      </div>
      {state ?? (list ? <ReportBody list={list} /> : null)}
    </section>
  );
}

function ReportBody({ list }: { list: ProductVerdictList }) {
  return (
    <>
      {/* 덧붙인 안내 문장('위 목록과 섞지 않습니다 …')은 뺐다(사용자 11/23: 글이 이상함). 섞지 않는 건 구획을 따로 둔 것으로 보인다 */}
      {/* 확정 단계가 정상으로 마쳤으면 '확정 단계 마침'은 두지 않는다(사용자 11/23). 미확인 · 중단 · 거절 등만 붙인다 */}
      <p className={s.help}>
        <span className={s.pill}>제품</span> {sourceText(list)}
        {list.confirmation?.status === "completed" ? null : (
          <>
            {" · "}
            <Confirmation list={list} />
          </>
        )}
      </p>
      {list.items.length ? (
        <ul className={s["verdict-list"]}>
          {list.items.map((r, i) => (
            <Row key={r.verdictId ?? `hidden-${i}`} r={r} />
          ))}
        </ul>
      ) : (
        <NoVerdicts list={list} />
      )}
    </>
  );
}
