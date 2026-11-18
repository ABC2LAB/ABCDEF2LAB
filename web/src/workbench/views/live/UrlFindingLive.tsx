"use client";

// 단독 발견 상세 화면 (#finding). 보고서 오른쪽 패널 머리 '상세 보기'로 고른 발견을 보고서 안 상세 페이지와 같은 부품
// (FindingPage — 판정과 영향 · 재현 절차 · 상세 보고서 · 보안 조치 · AI 프롬프트 · 상태 카드)으로 그린다.
// 데이터는 저장 결과(내부 remediation, GET /api/report/diagnostic/remediation)뿐이다 — 예시 레코드로 채우지 않는다.
// E③ 원문 판정 · 다계정 비교는 읽기 API 가 없어 이 화면에 두지 않는다(백엔드 후속, lib/diagnosis/finding 의 표시 규칙만 남김).

import { type ReactNode, useState } from "react";

import { cx } from "../../lib/cx";
import { fetchRemediation } from "../../live/api";
import { clearFindingPick, usePickedFinding } from "../../live/findingPick";
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
  // 다시 들어온 때(숨김 → 보임). 그 뒤에 받은 응답만 지금 결과로 그린다 — 직전 진입의 응답을 새 결과처럼 보이지 않게
  // (#695 리뷰). 렌더 중 상태 갱신은 이전 값과 다를 때만 한 번 일어난다(React 의 '이전 렌더 값에서 파생' 방식)
  const [entered, setEntered] = useState({ visible, at: visible ? Date.now() : 0 });
  if (entered.visible !== visible) setEntered({ visible, at: visible ? Date.now() : entered.at });
  const fresh = rem.fetchedAt !== null && rem.fetchedAt >= entered.at;
  const data = fresh ? rem.data : null;
  const entry = entryId && data && !rem.error ? data.entries.find((e) => e.entry_id === entryId) : undefined;
  // 목록으로 갈 때 공용 선택을 지운다 — 남아 있으면 보고서가 같은 상세를 다시 연다(#695 리뷰)
  const backToList = () => {
    clearFindingPick();
    goTo("report");
  };
  const toReport = (
    <button className={cx(s.btn, s.sm)} type="button" onClick={backToList}>
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
  else if (!data) body = <p className={s.empty}>불러오는 중…</p>;
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
        onBack={backToList}
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
