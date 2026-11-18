"use client";

// 단독 발견 상세 화면 (#finding). 보고서 오른쪽 패널 머리 '상세 보기'로 고른 발견을 보고서 안 상세 페이지와 같은 부품
// (FindingPage — 판정과 영향 · 재현 절차 · 상세 보고서 · 보안 조치 · AI 프롬프트 · 상태 카드)으로 그린다.
// 데이터는 저장 결과(내부 remediation, GET /api/report/diagnostic/remediation)뿐이다 — 예시 레코드로 채우지 않는다.
// E③ 원문 판정 · 다계정 비교는 읽기 API 가 없어 이 화면에 두지 않는다(백엔드 후속, lib/diagnosis/finding 의 표시 규칙만 남김).

import type { ReactNode } from "react";

import { cx } from "../../lib/cx";
import { fetchRemediation } from "../../live/api";
import { usePickedFinding } from "../../live/findingPick";
import { useUrl, useUrlGates } from "../../live/urlDiagnosis";
import { usePoll } from "../../live/usePoll";
import { useShell } from "../../shell";
import s from "../../workbench.module.css";
import { ReportReadError } from "./runRows";
import { FindingPage } from "./UrlLiveLive";
import { UrlHead, useUrlHost } from "./urlParts";

/** 단독 발견 상세 — 현재 Job 의 task 에서 고른 발견만. */
export function UrlFindingLive() {
  const { route, goTo } = useShell();
  const visible = route === "finding";
  const g = useUrlGates();
  const job = useUrl((st) => st.job);
  const host = useUrlHost();
  // 보고서와 같은 관문: 결과 저장이 확인된 task 만 읽는다
  const taskId = g.reportReadable && job?.task_id ? job.task_id : null;
  const entryId = usePickedFinding(taskId);
  const rem = usePoll(() => fetchRemediation(taskId as string, "internal"), [taskId], {
    enabled: visible && !!taskId && !!entryId,
  });
  const entry = entryId && rem.data && !rem.error ? rem.data.entries.find((e) => e.entry_id === entryId) : undefined;
  const toReport = (
    <button className={cx(s.btn, s.sm)} type="button" onClick={() => goTo("report")}>
      보고서 발견 목록으로
    </button>
  );

  let body: ReactNode;
  if (!job?.task_id) body = <p className={s.empty}>실제 진단 작업을 선택하세요.</p>;
  else if (!taskId) body = <p className={s.empty}>점검이 끝나고 결과가 저장되면 발견 상세를 볼 수 있습니다.</p>;
  else if (!entryId)
    body = (
      <div className={s.note} role="note">
        <span className={s["lv-grow"]}>보고서 발견 목록에서 발견을 고르고 오른쪽 패널의 ‘상세 보기’를 누르세요.</span>
        {toReport}
      </div>
    );
  else if (rem.error) body = <ReportReadError error={rem.error} onRetry={rem.reload} />;
  else if (!rem.data) body = <p className={s.empty}>불러오는 중…</p>;
  else if (!entry)
    body = (
      <div className={s.note} role="note">
        <span className={s["lv-grow"]}>고른 발견이 지금 저장 결과에 없습니다. 보고서에서 다시 고르세요.</span>
        {toReport}
      </div>
    );
  else
    body = (
      <FindingPage
        key={entry.entry_id}
        e={entry}
        host={host}
        taskId={taskId}
        onReload={rem.reload}
        onBack={() => goTo("report")}
      />
    );

  return (
    <>
      <UrlHead title="발견 상세" crumb="발견 상세" />
      <div className={cx(s.vb, s.solo)}>
        <div className={s.vm}>{body}</div>
      </div>
    </>
  );
}
