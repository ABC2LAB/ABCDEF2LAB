"use client";

import type { ReactNode } from "react";

import { cx } from "../../lib/cx";
import { maskSecrets } from "../../live/mask";
import { ErrorNote, LoadingRow } from "../../live/parts";
import { useActivityTail } from "../../live/useActivityTail";
import s from "../../workbench.module.css";

/** 새 task 결속마다 커서를 새로 만든다. 서버가 공개한 고정 요약과 sequence만 표시한다. */
export function TaskActivity({ taskId, enabled }: { taskId: string | null; enabled: boolean }) {
  const activity = useActivityTail(taskId, { enabled, intervalMs: 4000 });
  let body: ReactNode = <LoadingRow />;
  if (activity.error) body = <ErrorNote error={activity.error} onRetry={activity.reload} />;
  else if (activity.data)
    body = (
      <>
        {activity.data.items.length ? (
          <ul className={s.feed}>
            {[...activity.data.items].reverse().map((row) => (
              <li key={row.seq}>
                <span className={s.mono}>#{row.seq}</span> {maskSecrets(row.summary) || "진단 활동 기록"}
              </li>
            ))}
          </ul>
        ) : (
          <p className={s.empty}>반환된 활동 기록이 없습니다.</p>
        )}
        {!activity.data.caughtUp ? <p className={s.help}>이전 활동을 이어 조회하는 중입니다.</p> : null}
      </>
    );
  return (
    <section className={s.sec} aria-label="서버 진단 활동">
      <div className={s.sh}>
        <h2>서버 진단 활동</h2>
        <button type="button" className={cx(s.btn, s.sm)} onClick={activity.reload}>
          활동 다시 조회
        </button>
      </div>
      {body}
    </section>
  );
}
