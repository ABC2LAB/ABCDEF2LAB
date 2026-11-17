"use client";

import { ApiError } from "@/lib/diagnosis/api";
import type { URLRunSummary } from "@/lib/diagnosis/diagnosis-runs";
import { runApprovalLabels } from "@/lib/diagnosis/diagnosis-runs";
import { urlPhaseLabels, urlReasonLabels, urlStateLabels } from "@/lib/diagnosis/diagnosis-url";

import { cx } from "../../lib/cx";
import { maskSecrets } from "../../live/mask";
import { selectUrlRun, useUrl } from "../../live/urlDiagnosis";
import { loadUrlRuns, useUrlRuns } from "../../live/urlRunList";
import { useShell } from "../../shell";
import { Icon } from "../../ui/icons";
import s from "../../workbench.module.css";

export function RunRow({ row, onPick }: { row: URLRunSummary; onPick?: () => void }) {
  const selected = useUrl((st) => st.selectedID === row.id);
  const writeBusy = useUrl((st) => st.writeBusy);
  const { openInsp } = useShell();
  return (
    <button
      type="button"
      className={cx(s.row, selected && s.sel)}
      data-url-run-id={row.id}
      aria-current={selected ? "true" : undefined}
      disabled={writeBusy}
      onClick={() => {
        void selectUrlRun(row).then((ok) => {
          if (ok) {
            onPick?.();
            openInsp();
          }
        });
      }}
    >
      <span className={s.mk} aria-hidden="true" />
      <span className={s["r-main"]}>
        <span className={s["r-host"]}>{maskSecrets(row.display_name?.trim() || row.id)}</span>
        <span className={s["r-s"]}>
          <span>{row.id}</span>
          <span>{urlPhaseLabels[row.phase]}</span>
          <span>{runApprovalLabels[row.approval_status]}</span>
          {row.action_required ? <span>확인 필요</span> : null}
          {row.reason_code ? (
            <span>
              {Object.hasOwn(urlReasonLabels, row.reason_code) ? urlReasonLabels[row.reason_code] : "서버 사유 있음"}
            </span>
          ) : null}
        </span>
      </span>
      <span className={s["r-side"]}>
        <span className={cx(s.pill, row.state === "unknown" && s.wait)}>
          {row.state === "unknown" ? "상태 확인 필요 · 결과 불명" : urlStateLabels[row.state]}
        </span>
      </span>
    </button>
  );
}

/** 성공한 서버 목록과 오류를 구별한다. 누락된 시각·URL·판정은 만들지 않는다. */
export function RunListStatus({ empty }: { empty?: string }) {
  const { page, loading, error, notice } = useUrlRuns();
  if (error)
    return (
      <p className={s.err} role="alert">
        {error}
      </p>
    );
  if (loading && !page)
    return (
      <p className={s.empty} role="status">
        점검 목록을 읽는 중…
      </p>
    );
  return (
    <>
      {notice ? (
        <p className={s.help} role="status">
          {notice}
        </p>
      ) : null}
      {empty && page ? <p className={s.empty}>{empty}</p> : null}
    </>
  );
}

export function RunListControls() {
  const { page, owner, loading } = useUrlRuns();
  const cap = useUrl((st) => st.cap);
  if (cap.kind !== "v2") return null;
  return (
    <div className={s.acts}>
      <button
        type="button"
        className={cx(s.btn, s.sm)}
        disabled={loading}
        onClick={() => {
          void loadUrlRuns(cap.catalog.ownerContext);
        }}
      >
        <Icon name="refresh" />
        목록 새로 조회
      </button>
      {page?.more && owner === cap.catalog.ownerContext ? (
        <button
          type="button"
          className={cx(s.btn, s.sm)}
          disabled={loading}
          onClick={() => {
            void loadUrlRuns(owner, true);
          }}
        >
          {loading ? "다음 페이지 조회 중…" : "더 불러오기"}
        </button>
      ) : null}
      {page ? (
        <span
          className={s.help}
        >{`조회 ${page.runs.length}건${page.more ? " · 다음 페이지 있음" : ""} · 활성 회차 우선·식별자순`}</span>
      ) : null}
    </div>
  );
}

export function SelectedRunStatus() {
  const selected = useUrl((st) => st.selectedID);
  const busy = useUrl((st) => st.busy);
  const error = useUrl((st) => st.error);
  return (
    <>
      {selected && busy ? (
        <p className={s.empty} role="status">
          선택한 점검의 현재 상태를 읽는 중…
        </p>
      ) : null}
      {error ? (
        <p className={s.err} role="alert">
          {error}
        </p>
      ) : null}
    </>
  );
}

type ReadErrorProps = { error: unknown; onRetry?: () => void };

export function ReportReadError(props: ReadErrorProps) {
  return <RunReadError resource="보고서" {...props} />;
}

export function RunReadError({ resource, error, onRetry }: ReadErrorProps & { resource: "보고서" | "활동" }) {
  const status = error instanceof ApiError ? error.status : 0;
  const details: Record<number, string> = {
    401: "다시 로그인해 주세요.",
    403: "조회 권한을 확인해 주세요.",
    404: `이번 조회에서 ${resource} 자료를 찾지 못했습니다.`,
    503: `${resource} 서비스가 응답하지 않습니다.`,
  };
  const detail = details[status] ?? `${resource} 응답을 확인하지 못했습니다.`;
  return (
    <div role="alert" className={s.err}>
      <p>{`${resource} 조회 실패${status ? ` (HTTP ${status})` : ""} · ${detail}`}</p>
      {onRetry ? (
        <button type="button" className={cx(s.btn, s.sm)} onClick={onRetry}>
          {resource} 다시 조회
        </button>
      ) : null}
    </div>
  );
}
