"use client";

// 제품 확정 단계 판정 표시 (계약 docs/contracts/product-verdict-list-proposal.md §3).
// - 보고서: 평가 판정 행(verdict-<id>)과 섞지 않고 '제품 확정 단계 판정' 구획을 따로 둔다.
// - 접근통제 지도: 평가 KG 격자에 넣지 않고(축 출처가 다름: KG 등록 대 정책 선언) 아래에 '자원 종류 × 소유→교차' 표를 따로 둔다.
// 확정도 finding 이 아니다(결정 §9) — 후보 상태(수동 검토 필요)는 바꾸지 않는다. 빈 목록은 '판정 없음'이지 '취약점 없음'이 아니다.

import type { ReactNode } from "react";

import { ApiError } from "@/lib/diagnosis/api";

import { cx } from "../../lib/cx";
import { dateTimeOf } from "../../live/map";
import {
  CHECK_TEXT,
  CONFIRMATION_TEXT,
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

/** 확정 단계 결과 한 줄. 결과 파일이 없으면 '미확인' — 꺼졌다고 추정하지 않는다. */
function confirmationText(list: ProductVerdictList): string {
  const c = list.confirmation;
  if (!c) return "확정 단계 결과 미확인";
  return c.reason ? `${CONFIRMATION_TEXT[c.status]} · 사유 ${c.reason}` : CONFIRMATION_TEXT[c.status];
}

/** 출처 딱지: 제품 · 정책 선언 참조 (대상 승인 판은 행들이 같은 판일 때만). */
function sourceText(list: ProductVerdictList): string {
  const p = list.policySource;
  if (!p) return "출처: 제품 확정 단계 · 정책 선언 동결본 미확인";
  return `출처: 제품 확정 단계 · 정책 선언 ${p.ref}${p.targetApprovalRevision ? ` · 대상 승인 판 ${p.targetApprovalRevision}` : ""}`;
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

function ShownRow({ r }: { r: ProductVerdictShown }) {
  return (
    <li data-verdict={r.verdictId}>
      <div>
        <Value v={r.oracleValue} /> <b className={s.mono}>{r.resourceType}</b>{" "}
        <span className={s.help}>{`판정 #${r.verdictId}`}</span>
      </div>
      <div className={s.help}>
        {`검사: 정책 ${CHECK_TEXT[r.checks.policy]} · 신원 ${CHECK_TEXT[r.checks.identity]} · 데이터 ${CHECK_TEXT[r.checks.data]} · 근거 호출 ${r.evidenceCalls}건 · 관찰 ${dateTimeOf(r.observedAt) || r.observedAt}`}
      </div>
      {r.oracleReasonCodes.length ? (
        <div className={s.help}>
          오라클 사유 <span className={s.mono}>{r.oracleReasonCodes.join(", ")}</span>
        </div>
      ) : null}
      {r.productReasonCodes.length ? (
        <div className={s.help}>
          제품 사유 <span className={s.mono}>{r.productReasonCodes.join(", ")}</span>
        </div>
      ) : null}
      <div className={s.help}>
        {r.oracleValue === "confirmed"
          ? `확정 판정 · 검토 대상 후보 ${r.candidateRefs.length}건 · 발견(finding)을 만들지 않고 후보는 수동 검토 필요 그대로입니다`
          : `연결된 후보 ${r.candidateRefs.length}건`}
      </div>
      {r.recordStatus === "policy_source_unavailable" ? (
        <div className={cx(s.note, s.warn)} role="note">
          <Icon name="alert" />
          <span>{`${RECORD_STATUS_TEXT.policy_source_unavailable} · 당시 정책 동결본을 읽지 못해 대조하지 못했습니다(값은 저장된 그대로)`}</span>
        </div>
      ) : null}
    </li>
  );
}

function HiddenRow({ id }: { id: string | null }) {
  return (
    <li>
      <span className={cx(s.vd, s.und)}>값 숨김</span>{" "}
      <span>{`${id ? `판정 #${id} · ` : ""}${RECORD_STATUS_TEXT.record_integrity_failed}`}</span>
    </li>
  );
}

function Row({ r }: { r: ProductVerdictRow }) {
  return r.recordStatus === "record_integrity_failed" ? <HiddenRow id={r.verdictId} /> : <ShownRow r={r} />;
}

/** 보고서 '취약점 목록' 탭 아래 구획. */
export function ProductVerdictReport({ taskId, active }: { taskId: string | null; active: boolean }) {
  const poll = useProductVerdicts(taskId, active);
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
      <p className={s.help}>
        <span className={s.pill}>제품</span> {sourceText(list)} · {confirmationText(list)}
      </p>
      <p className={s.help}>
        위 목록(평가 · 일반 발견)과 섞지 않습니다. 확정 단계가 시험 계정 둘로 직접 확인한 판정입니다.
      </p>
      {list.items.length ? (
        <ul className={s.feed}>
          {list.items.map((r, i) => (
            <Row key={r.verdictId ?? `hidden-${i}`} r={r} />
          ))}
        </ul>
      ) : (
        <p className={s.empty}>{`제품 판정 없음 · ${confirmationText(list)} · 취약점이 없다는 뜻이 아닙니다`}</p>
      )}
    </>
  );
}

/** 지도 표의 한 칸: 같은 자원 종류에 판정이 둘 이상이면 고르지 않는다(접근통제 지도의 selection_ambiguous 와 같은 규칙). */
function MapCell({ rows }: { rows: ProductVerdictShown[] }) {
  if (rows.length !== 1) return <span className={cx(s.vd, s.und)}>판정 둘 이상 · 선택 안 함</span>;
  const r = rows[0];
  return (
    <>
      <Value v={r.oracleValue} />
      {r.recordStatus === "policy_source_unavailable" ? (
        <small className={s.help}>{` ${RECORD_STATUS_TEXT.policy_source_unavailable}`}</small>
      ) : null}
    </>
  );
}

/** 접근통제 지도(보고서 탭 · 지도 화면) 아래 표. */
export function ProductVerdictMap({ taskId, active }: { taskId: string | null; active: boolean }) {
  const poll = useProductVerdicts(taskId, active);
  if (!taskId) return null;
  const state = readState(poll);
  if (state === "hide") return null;
  const list = poll.data;
  return (
    <section className={s.isec} aria-label="제품 확정 단계 표">
      <h2>제품 확정 단계 (정책 선언 기준)</h2>
      <p className={s.help}>
        평가 KG 격자와 축 출처가 달라 따로 둡니다. 소유 계정 칸은 정상 대조라 판정 대상이 아니어서 두지 않습니다.
      </p>
      {state ?? (list ? <MapBody list={list} /> : null)}
    </section>
  );
}

function MapBody({ list }: { list: ProductVerdictList }) {
  const shown = list.items.filter((r): r is ProductVerdictShown => r.recordStatus !== "record_integrity_failed");
  const hidden = list.items.length - shown.length;
  const types = [...new Set(shown.map((r) => r.resourceType))];
  const owner = list.policySource?.ownerAccountLabel ?? "소유 계정";
  const cross = list.policySource?.crossAccountLabel ?? "교차 계정";
  return (
    <>
      <p className={s.help}>
        <span className={s.pill}>제품</span> {sourceText(list)} · {confirmationText(list)}
      </p>
      {types.length ? (
        <table aria-label="자원 종류 × 소유→교차 제품 판정">
          <thead>
            <tr>
              <th scope="col">자원 종류</th>
              <th scope="col">{`${owner} → ${cross}`}</th>
            </tr>
          </thead>
          <tbody>
            {types.map((t) => (
              <tr key={t}>
                <th scope="row" className={s.mono}>
                  {t}
                </th>
                <td>
                  <MapCell rows={shown.filter((r) => r.resourceType === t)} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : null}
      {hidden ? (
        <p className={cx(s.note, s.warn)} role="note">
          <Icon name="alert" />
          <span>{`${RECORD_STATUS_TEXT.record_integrity_failed} ${hidden}건 · 자원 종류도 숨겨져 표에 두지 않습니다`}</span>
        </p>
      ) : null}
      {list.items.length ? null : (
        <p className={s.empty}>{`제품 판정 없음 · ${confirmationText(list)} · 취약점이 없다는 뜻이 아닙니다`}</p>
      )}
    </>
  );
}
