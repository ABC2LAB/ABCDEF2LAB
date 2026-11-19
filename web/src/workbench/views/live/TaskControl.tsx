"use client";

// 탐색 흐름 없는 점검(일반 URL 진단)의 일시정지 · 다시 시작 (C33).
// 제품 관리 중계는 결속된 task 의 POST /api/tasks/{id}/control 에 pause · resume 만 넘긴다(server diagnosis_management_bridge.go).
// 흐름이 있는 점검은 워크플로 API 의 PauseButton(workflowViews) 을 쓴다 — 둘을 같이 두지 않는다.
// 일시정지 상태를 읽는 조회 API 가 없어서, 화면은 이 화면에서 받은 제어 응답만 안다(paused: null = 모름).

import { useCallback, useEffect, useState } from "react";

import { ApiError } from "@/lib/diagnosis/api";

import { cx } from "../../lib/cx";
import { controlTask } from "../../live/api";
import { maskSecrets } from "../../live/mask";
import { Icon } from "../../ui/icons";
import s from "../../workbench.module.css";

type Action = "pause" | "resume";

/** 일시정지 제어 상태. paused 는 이 화면에서 받은 응답으로만 정한다. */
export interface TaskControl {
  /** true = 멈춤 · false = 진행 · null = 모름(아직 제어하지 않았거나 결과를 확인하지 못함) */
  paused: boolean | null;
  /** 서버가 지금 상태와 맞지 않는다고 거절했거나(409) 결과를 모른다 → 두 버튼을 다 보인다 */
  both: boolean;
  busy: boolean;
  message: string;
  run: (action: Action) => Promise<void>;
}

const actionText: Record<Action, string> = { pause: "일시정지", resume: "다시 시작" };

/** 실패 안내. 409 는 서버 사유(이미 멈춤 · 종료됨 등)를 그대로, 그 밖은 HTTP 상태와 다음 할 일. */
function failText(action: Action, err: unknown): string {
  const status = err instanceof ApiError ? err.status : 0;
  if (status === 409 && err instanceof Error && err.message)
    return `${actionText[action]} 거절 · ${maskSecrets(err.message)}`;
  if (status === 0)
    return `${actionText[action]} 결과를 확인하지 못했습니다. 상태가 바뀌었는지 모르니 다시 눌러 확인하세요.`;
  const next: Record<number, string> = {
    401: "다시 로그인해 주세요.",
    403: "이 점검을 제어할 권한이 없거나 승인이 바뀌었습니다.",
    404: "점검을 찾지 못했습니다.",
    503: "진단 서비스가 응답하지 않습니다.",
  };
  return `${actionText[action]} 실패 (HTTP ${status}) · ${next[status] ?? "응답을 확인하지 못했습니다."}`;
}

/** taskId 가 바뀌면 처음부터(모름) 다시 센다. */
export function useTaskControl(taskId: string | null): TaskControl {
  const [st, setSt] = useState<{
    task: string | null;
    paused: boolean | null;
    both: boolean;
    busy: boolean;
    message: string;
  }>({ task: taskId, paused: null, both: false, busy: false, message: "" });
  useEffect(() => {
    setSt((cur) => (cur.task === taskId ? cur : { task: taskId, paused: null, both: false, busy: false, message: "" }));
  }, [taskId]);
  const run = useCallback(
    async (action: Action) => {
      if (!taskId) return;
      setSt((cur) => ({ ...cur, busy: true, message: "" }));
      try {
        const r = await controlTask(taskId, action);
        // 다시 시작 응답은 paused 칸 없이 status(queued · running)만 준다
        const paused = action === "pause" ? r.paused : false;
        setSt((cur) =>
          cur.task === taskId
            ? { ...cur, paused, both: false, busy: false, message: paused ? "일시정지했습니다." : "다시 시작했습니다." }
            : cur,
        );
      } catch (err) {
        const status = err instanceof ApiError ? err.status : 0;
        setSt((cur) =>
          cur.task === taskId
            ? {
                ...cur,
                // 409(상태 불일치) · 결과 모름은 지금 상태를 모른다 → 둘 다 보인다. 그 밖(권한 등)은 상태를 바꾸지 않는다
                paused: status === 409 || status === 0 ? null : cur.paused,
                both: status === 409 || status === 0 ? true : cur.both,
                busy: false,
                message: failText(action, err),
              }
            : cur,
        );
      }
    },
    [taskId],
  );
  const same = st.task === taskId;
  return {
    paused: same ? st.paused : null,
    both: same && st.both,
    busy: same && st.busy,
    message: same ? st.message : "",
    run,
  };
}

/** 일시정지 · 다시 시작 버튼. 상태를 모르고 서버가 거절했으면 둘 다 보인다. */
export function TaskPauseButtons({ c, disabled }: { c: TaskControl; disabled: boolean }) {
  const actions: Action[] = c.both ? ["pause", "resume"] : [c.paused ? "resume" : "pause"];
  return (
    <>
      {actions.map((action) => (
        <button
          key={action}
          className={cx(s.btn)}
          type="button"
          disabled={disabled || c.busy}
          aria-pressed={c.both ? undefined : c.paused === true}
          title="일시정지해도 점검 실행 시간 상한은 계속 흐릅니다"
          onClick={() => void c.run(action)}
        >
          <Icon name={action === "resume" ? "play" : "pause"} />
          <span>{c.busy ? "보내는 중…" : actionText[action]}</span>
        </button>
      ))}
    </>
  );
}
