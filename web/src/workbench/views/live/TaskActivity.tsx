"use client";

import type { ReactNode } from "react";

import { cx } from "../../lib/cx";
import type { LiveActivity } from "../../live/api";
import { clockOf } from "../../live/map";
import { LoadingRow } from "../../live/parts";
import { useActivityTail } from "../../live/useActivityTail";
import s from "../../workbench.module.css";
import { RunReadError } from "./runRows";

/** 제품 공개 활동의 주체(server publicActivity: planner · mainagent · worker, 그 밖은 worker 로 접힌다). */
const workerText: Record<string, string> = { planner: "계획", mainagent: "주 에이전트", worker: "작업자" };

/**
 * 활동 한 줄: 순번 · 시각 · 주체 · 의도 · 오류. 제품 서버는 원문 명령 · 모델 글 · 도구 인자를 빼고 고정 요약만 주므로
 * (server diagnosis_management_operations.go publicActivity) 무엇을 했는지는 여기 없다. 값이 없는 칸은 두지 않는다.
 */
function ActivityRow({ row }: { row: LiveActivity }) {
  const time = clockOf(row.ts);
  const who = workerText[row.worker] ?? "";
  return (
    <li>
      <span className={s.mono}>#{row.seq}</span>
      {time ? <span className={s.mono}>{` ${time}`}</span> : null}
      {who ? ` · ${who}` : ""}
      {row.intent_id ? ` · 의도 #${row.intent_id}` : ""}
      {" · "}
      <span>{row.summary || "진단 활동 기록"}</span>
      {row.is_error ? <b>{" · 오류"}</b> : null}
    </li>
  );
}

/** 새 task 결속마다 커서를 새로 만든다. 서버가 공개한 고정 요약과 sequence만 표시한다. */
export function TaskActivity({ taskId, enabled }: { taskId: string | null; enabled: boolean }) {
  const activity = useActivityTail(taskId, { enabled, intervalMs: 4000 });
  let body: ReactNode = <LoadingRow />;
  if (activity.error) body = <RunReadError resource="활동" error={activity.error} onRetry={activity.reload} />;
  else if (activity.data)
    body = (
      <>
        {activity.data.items.length ? (
          <ul className={s.feed}>
            {[...activity.data.items].reverse().map((row) => (
              <ActivityRow key={row.seq} row={row} />
            ))}
          </ul>
        ) : (
          <p className={s.empty}>반환된 활동 기록이 없습니다.</p>
        )}
        {!activity.data.caughtUp ? <p className={s.help}>이전 활동을 이어 조회하는 중입니다.</p> : null}
        <p className={s.help}>원문 명령 · 모델 글 · 요청 내용은 비밀이 섞일 수 있어 공개 활동에 싣지 않습니다.</p>
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
