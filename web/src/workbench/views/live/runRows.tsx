"use client";

import { ApiError } from "@/lib/diagnosis/api";
import type { URLRunSummary } from "@/lib/diagnosis/diagnosis-runs";
import { activeURLRun, endedURLRun, runApprovalLabels } from "@/lib/diagnosis/diagnosis-runs";
import { urlPhaseLabels, urlReasonLabels } from "@/lib/diagnosis/diagnosis-url";

import { cx } from "../../lib/cx";
import { fmtDur } from "../../live/map";
import { maskSecrets } from "../../live/mask";
import { useRunInput } from "../../live/runInput";
import { runDurationSec, runStartText } from "../../live/runTiming";
import { selectUrlRun, useUrl } from "../../live/urlDiagnosis";
import { urlStateText } from "../../live/urlMap";
import { loadUrlRuns, useUrlRuns } from "../../live/urlRunList";
import { useShell } from "../../shell";
import { Icon } from "../../ui/icons";
import s from "../../workbench.module.css";

export function RunRow({ row, onPick }: { row: URLRunSummary; onPick?: () => void }) {
  const selected = useUrl((st) => st.selectedID === row.id);
  // 이미 열어 둔 점검을 다시 누르면 상태를 다시 읽지 않는다 — 다시 읽으면 화면을 새로 그려 오른쪽이 깜빡였다(사용자 11/23).
  // 최신 상태는 상세의 새로 조회 · 목록 새로 조회로 본다
  const opened = useUrl((st) => st.selectedID === row.id && st.job?.id === row.id && !st.busy);
  const writeBusy = useUrl((st) => st.writeBusy);
  const { openInsp } = useShell();
  const active = activeURLRun(row);
  // 끝난 점검(완료 · 중지 · 실패)의 승인 만료(expired)는 붙이지 않는다(사용자 11/24: 대상 승인 시간이 지나면 서버가 끝난
  // 점검까지 승인 만료 · 확인 필요로 내려 '점검 완료 · 승인 만료'로 보였다). 결과 · 보고서는 그대로 유효하고, 만료는 새 점검
  // 전에 대상을 다시 승인하라는 뜻이라 새 점검 화면이 알린다. 철회 · 변경 · 불명은 끝난 점검에도 보인다 — 정상 성공으로
  // 보이지 않게(product-run-list 계약 화면 3, #917 리뷰 P2)
  const ended = endedURLRun(row);
  const endedExpired = ended && row.approval_status === "expired";
  const approvalIssue = !endedExpired && row.action_required && row.approval_status !== "not_revalidated";
  const showStatus = active || (!endedExpired && row.action_required);
  const owner = useUrlRuns((st) => st.owner);
  const info = useRunInput(row.id, owner);
  const startText = runStartText(row);
  const durSec = runDurationSec(row);
  const meta = [
    info?.host,
    info?.mode,
    startText ? `${startText.slice(5, 16)} 시작` : null,
    durSec !== null ? `소요 ${fmtDur(durSec)}` : null,
  ].filter((m): m is string => !!m);
  return (
    <button
      type="button"
      className={cx(s.row, selected && s.sel)}
      data-url-run-id={row.id}
      aria-current={selected ? "true" : undefined}
      disabled={writeBusy}
      onClick={() => {
        if (opened) {
          onPick?.();
          openInsp();
          return;
        }
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
        {/* 이름 줄: '1. prep-…'처럼 작업 번호(서버 task) + 이름 (사용자 11/18). 끝난 점검이 많아져도 줄마다 구분되고 오른쪽
            패널 '작업 #n'과 같은 번호다 */}
        <span className={s["r-title"]}>
          {row.task_id ? (
            <span className={s["r-no"]}>
              <span className={s["sr-only"]}>작업 </span>
              {`${row.task_id}.`}
            </span>
          ) : null}
          <span className={s["r-name"]}>{maskSecrets(row.display_name?.trim() || row.id)}</span>
        </span>
        {/* 같은 이름 · 같은 대상 점검을 구분하는 줄(사용자 11/24): 주소 · 진단 모드 · 시작 시각 · 소요. 주소 · 모드는 그 점검
            상태를 한 번 읽어 채우고, 시각은 서버가 확인한 값만(없으면 뺀다) */}
        {meta.length ? (
          <span className={s["r-s"]}>
            {meta.map((m) => (
              <span key={m}>{m}</span>
            ))}
          </span>
        ) : null}
        {/* 사람이 할 일이 남은 줄은 종료 여부와 관계없이 승인 상태와 확인 필요를 보인다. */}
        {/* 이름을 붙인 점검도 서버 ID(prep-…)는 보이지 않는다 — 이름 칸을 둔 이유(사용자 11/23) */}
        {showStatus || row.reason_code ? (
          <span className={s["r-s"]}>
            {active ? <span>{urlPhaseLabels[row.phase]}</span> : null}
            {active || approvalIssue ? <span>{runApprovalLabels[row.approval_status]}</span> : null}
            {row.action_required && !endedExpired ? <span>확인 필요</span> : null}
            {row.reason_code ? (
              <span>
                {Object.hasOwn(urlReasonLabels, row.reason_code) ? urlReasonLabels[row.reason_code] : "서버 사유 있음"}
              </span>
            ) : null}
          </span>
        ) : null}
      </span>
      <span className={s["r-side"]}>
        {/* 점검 완료는 초록(사용자 11/24, 게이지 '점검 완료'와 같은 색). 결과 불명 · 승인 문제는 노랑 */}
        <span
          className={cx(s.pill, row.state === "unknown" || approvalIssue ? s.wait : row.state === "completed" && s.ok)}
        >
          {row.state === "unknown"
            ? "상태 확인 필요 · 결과 불명"
            : `${urlStateText(row.state)}${approvalIssue ? ` · ${runApprovalLabels[row.approval_status]}` : ""}`}
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

/**
 * 목록 조회 버튼 줄. refresh=false 면 [목록 새로 조회]를 두지 않는다 — 대시보드는 머리 [새로고침]이 목록까지 다시 읽어 같은
 * 버튼이 둘이 됐다(사용자 11/24). 다음 쪽이 있으면 [더 불러오기]는 그대로
 */
export function RunListControls({ refresh = true }: { refresh?: boolean }) {
  const { page, owner, loading } = useUrlRuns();
  const cap = useUrl((st) => st.cap);
  if (cap.kind !== "v2") return null;
  const more = !!page?.more && owner === cap.catalog.ownerContext;
  if (!refresh && !more) return null;
  return (
    <div className={s.acts}>
      {refresh ? (
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
      ) : null}
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
