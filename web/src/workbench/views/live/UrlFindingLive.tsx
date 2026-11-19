"use client";

// 단독 발견 상세 화면 (#finding). 보고서 오른쪽 패널 머리 '상세 보기'로 고른 발견을 보고서 안 상세 페이지와 같은 부품
// (FindingPage — AI 분석 · 재현 절차 · 상세 보고서 · 보안 조치 · AI 프롬프트 · 상태 카드)으로 그린다.
// 데이터는 저장 결과(내부 remediation, GET /api/report/diagnostic/remediation)뿐이다 — 예시 레코드로 채우지 않는다.
// E③ 원문 판정 · 다계정 비교는 읽기 API 가 없어 이 화면에 두지 않는다(백엔드 후속, lib/diagnosis/finding 의 표시 규칙만 남김).

import { type ReactNode, useEffect, useState } from "react";

import { cx } from "../../lib/cx";
import { fetchRemediation, type Remediation } from "../../live/api";
import { clearFindingPick, usePickedFinding } from "../../live/findingPick";
import { useUrl, useUrlGates, useUrlSelectionKey } from "../../live/urlDiagnosis";
import { useShell } from "../../shell";
import s from "../../workbench.module.css";
import { ReportReadError } from "./runRows";
import { FindingPage } from "./UrlLiveLive";
import { UrlHead, useUrlHost } from "./urlParts";

/**
 * 저장 결과를 읽은 것. key = 그 요청을 시작한 진입 — 선택 + task + 진입 세대. 다른 진입(직전에 들어왔을 때)의 성공 · 실패는
 * 지금 결과로 그리지 않는다(#695 리뷰: 시각 비교는 같은 밀리초에서 이전 상세를 보였다).
 */
type Read = { key: string; data: Remediation | null; error: unknown };

/** 단독 발견 상세 — 현재 Job 의 task 에서 고른 발견만. */
export function UrlFindingLive() {
  const { route, goTo } = useShell();
  const visible = route === "finding";
  const g = useUrlGates();
  const job = useUrl((st) => st.job);
  const host = useUrlHost();
  const selection = useUrlSelectionKey();
  // 보고서와 같은 관문: 결과 저장이 확인된 task 만 읽는다
  const taskId = g.reportReadable && job?.task_id ? job.task_id : null;
  const entryId = usePickedFinding(taskId);
  // 진입 세대: 화면에 다시 들어올 때마다(숨김 → 보임) 올린다. 렌더 중에 맞춰서 같은 렌더에서 직전 진입의 결과를 버린다
  // (React 의 '이전 렌더 값에서 파생' 방식 — 값이 다를 때만 한 번 갱신)
  const [entered, setEntered] = useState({ visible, gen: 0 });
  if (entered.visible !== visible) setEntered({ visible, gen: visible ? entered.gen + 1 : entered.gen });
  const want = visible && taskId && entryId ? `${selection}|${taskId}|${entered.gen}` : "";
  const [read, setRead] = useState<Read | null>(null);
  // 다시 읽기(오류의 '다시 시도' · 상세 안 조작 뒤 갱신). 같은 진입이라 받아 둔 결과는 새 응답 전까지 둔다
  const [attempt, setAttempt] = useState(0);
  const reload = () => setAttempt((n) => n + 1);

  // biome-ignore lint/correctness/useExhaustiveDependencies: attempt 는 같은 진입에서 다시 읽는 트리거
  useEffect(() => {
    if (!want || !taskId) return;
    let current = true;
    fetchRemediation(taskId, "internal").then(
      (data) => {
        if (current) setRead({ key: want, data, error: null });
      },
      (error: unknown) => {
        if (current) setRead((prev) => ({ key: want, data: prev?.key === want ? prev.data : null, error }));
      },
    );
    return () => {
      current = false;
    };
  }, [want, taskId, attempt]);

  // 이번 진입에서 시작한 요청의 결과만
  const now = read && want && read.key === want ? read : null;
  const data = now?.data ?? null;
  const entry = entryId && data && !now?.error ? data.entries.find((e) => e.entry_id === entryId) : undefined;
  // 목록으로 갈 때 공용 선택을 지운다 — 남아 있으면 보고서가 같은 상세를 다시 연다(#695 리뷰)
  const backToList = () => {
    clearFindingPick();
    goTo("report");
  };
  const toReport = (
    <button className={cx(s.btn, s.sm)} type="button" onClick={backToList}>
      보고서 취약점 목록으로
    </button>
  );

  let body: ReactNode;
  if (!job?.task_id) body = <p className={s.empty}>실제 진단 작업을 선택하세요.</p>;
  else if (!taskId) body = <p className={s.empty}>점검이 끝나고 결과가 저장되면 취약점 상세를 볼 수 있습니다.</p>;
  else if (!entryId)
    body = (
      <div className={s.note} role="note">
        <span className={s["lv-grow"]}>
          보고서 취약점 목록에서 취약점을 고르고 오른쪽 패널의 ‘상세 보기’를 누르세요.
        </span>
        {toReport}
      </div>
    );
  else if (now?.error) body = <ReportReadError error={now.error} onRetry={reload} />;
  else if (!data) body = <p className={s.empty}>불러오는 중…</p>;
  else if (!entry)
    body = (
      <div className={s.note} role="note">
        <span className={s["lv-grow"]}>고른 취약점이 지금 저장 결과에 없습니다. 보고서에서 다시 고르세요.</span>
        {toReport}
      </div>
    );
  else
    body = (
      <FindingPage key={entry.entry_id} e={entry} host={host} taskId={taskId} onReload={reload} onBack={backToList} />
    );

  return (
    <>
      <UrlHead title="취약점 상세" crumb="취약점 상세" />
      <div className={cx(s.vb, s.solo)}>
        <div className={s.vm}>{body}</div>
      </div>
    </>
  );
}
