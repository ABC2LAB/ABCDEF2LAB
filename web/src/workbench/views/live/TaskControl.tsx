"use client";

// 탐색 흐름 없는 점검(일반 URL 진단)의 중지 (C33 → #830).
// 제품 첫 진단 서버에서 control pause 는 엔진을 멈춰 두지 않고 회차를 바로 끝내는 운영자 중지다(확정 단계 interrupted,
// 응답 status: stopping · reason_code: pause_stops_run). 재개는 지원하지 않는다(409 reason_code: resume_unsupported) —
// 일시정지 뒤에는 실행 연결이 닫혀 이어 갈 수 없었다. 그래서 화면은 '일시정지 · 다시 시작' 대신 확인을 거친 '중지' 하나만 둔다.
// 진짜 재개 지원은 측정 뒤 개선(개발 계획)이라, 그때 다시 시작 버튼을 되살린다.
// 흐름이 있는 점검은 워크플로 API 의 PauseButton(workflowViews) 을 쓴다 — 둘을 같이 두지 않는다.

import { type ReactElement, useCallback, useEffect, useRef, useState } from "react";

import { ApiError } from "@/lib/diagnosis/api";

import { cx } from "../../lib/cx";
import { controlTask } from "../../live/api";
import { maskSecrets } from "../../live/mask";
import { runUrlAction } from "../../live/urlDiagnosis";
import { Icon } from "../../ui/icons";
import s from "../../workbench.module.css";

/** 서버 고정 사유 코드(server first_diagnosis_startup.go firstDiagnosisControl). */
export const PAUSE_STOPS_RUN = "pause_stops_run";
export const RESUME_UNSUPPORTED = "resume_unsupported";

/** 중지 제어 상태. */
export interface TaskControl {
  /** 서버가 중지를 받아 마감 중이다(stopping) → 버튼을 다시 두지 않는다 */
  stopped: boolean;
  busy: boolean;
  message: string;
  /** 중지를 보낸다(서버 control 의 pause). 확인 상자를 거친 뒤에만 부른다. */
  stop: () => Promise<void>;
}

/** 실패 안내. resume_unsupported 는 오류가 아니라 '아직 지원하지 않음', 그 밖 409 는 서버 사유를 가려서 그대로. */
export function controlFailText(err: unknown): string {
  const status = err instanceof ApiError ? err.status : 0;
  if (err instanceof ApiError && err.status === 409 && err.reasonCode === RESUME_UNSUPPORTED)
    return "재개는 아직 지원하지 않습니다.";
  if (status === 409 && err instanceof Error && err.message) return `중지 거절 · ${maskSecrets(err.message)}`;
  if (status === 0) return "중지 결과를 확인하지 못했습니다. [상태 다시 조회]로 점검이 끝났는지 확인하세요.";
  const next: Record<number, string> = {
    401: "다시 로그인해 주세요.",
    403: "이 점검을 제어할 권한이 없거나 승인이 바뀌었습니다.",
    404: "점검을 찾지 못했습니다.",
    503: "진단 서비스가 응답하지 않습니다.",
  };
  return `중지 실패 (HTTP ${status}) · ${next[status] ?? "응답을 확인하지 못했습니다."}`;
}

/** taskId 가 바뀌면 처음부터 다시 센다. */
export function useTaskControl(taskId: string | null): TaskControl {
  const [st, setSt] = useState<{ task: string | null; stopped: boolean; busy: boolean; message: string }>({
    task: taskId,
    stopped: false,
    busy: false,
    message: "",
  });
  useEffect(() => {
    setSt((cur) => (cur.task === taskId ? cur : { task: taskId, stopped: false, busy: false, message: "" }));
  }, [taskId]);
  const stop = useCallback(async () => {
    if (!taskId) return;
    setSt((cur) => ({ ...cur, busy: true, message: "" }));
    try {
      const r = await controlTask(taskId, "pause");
      // #830 이전 서버는 pause 를 일시정지로만 처리한다(재개도 실패) — 중지됐다고 말하지 않고 종료를 안내한다
      const stopped = r.status === "stopping" && r.reasonCode === PAUSE_STOPS_RUN;
      setSt((cur) =>
        cur.task === taskId
          ? {
              ...cur,
              stopped,
              busy: false,
              message: stopped
                ? "중지를 요청했습니다. 점검을 마감하는 중이고, 마감되면 모은 결과로 보고서를 만듭니다."
                : "서버가 이 요청을 중지가 아닌 일시정지로 처리했습니다. 점검을 끝내려면 [종료]를 누르세요.",
            }
          : cur,
      );
      // 마감 상태(stopping)를 바로 보이게 진단 상태를 한 번 다시 읽는다(조회만, 새 전송 없음)
      if (stopped) void runUrlAction("refresh");
    } catch (err) {
      setSt((cur) => (cur.task === taskId ? { ...cur, busy: false, message: controlFailText(err) } : cur));
    }
  }, [taskId]);
  const same = st.task === taskId;
  return {
    stopped: same && st.stopped,
    busy: same && st.busy,
    message: same ? st.message : "",
    stop,
  };
}

/**
 * 중지 버튼 + 확인 상자. 누르면 바로 보내지 않고 '이 점검을 끝냅니다'를 한 번 확인받는다(되돌릴 수 없음).
 * 버튼은 머리(acts)에, 확인 상자는 본문 맨 위에 둔다(종료 확인 상자와 같은 자리).
 */
export function useTaskStopConfirm(
  c: TaskControl,
  disabled: boolean,
): { button: ReactElement | null; box: ReactElement | null } {
  const [open, setOpen] = useState(false);
  const opener = useRef<HTMLButtonElement>(null);
  const yes = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (open) yes.current?.focus();
  }, [open]);
  const hidden = c.stopped;
  const button = hidden ? null : (
    <button
      ref={opener}
      className={s.btn}
      type="button"
      disabled={disabled || c.busy}
      aria-expanded={open}
      onClick={() => setOpen(true)}
    >
      <Icon name="stop" />
      <span>{c.busy ? "보내는 중…" : "중지"}</span>
    </button>
  );
  const box =
    open && !hidden && !disabled ? (
      <div className={s.confirm} role="alertdialog" aria-labelledby="taskStopH">
        <h2 id="taskStopH">이 점검을 끝냅니다. 중지할까요?</h2>
        <ul>
          <li>중지하면 바로 마감하고, 다시 이어 갈 수 없습니다(재개는 아직 지원하지 않습니다).</li>
          <li>새 요청을 보내지 않고, 모은 증거와 발견은 남겨 중지 시점까지의 결과로 보고서를 만듭니다.</li>
          <li>확정 단계를 켠 점검이면 확정 단계는 중단(interrupted)으로 기록됩니다.</li>
        </ul>
        <div className={s.acts}>
          <button
            ref={yes}
            className={cx(s.btn, s.danger)}
            type="button"
            onClick={() => {
              setOpen(false);
              void c.stop();
            }}
          >
            중지
          </button>
          <button
            className={s.btn}
            type="button"
            onClick={() => {
              setOpen(false);
              opener.current?.focus();
            }}
          >
            계속 점검
          </button>
        </div>
      </div>
    ) : null;
  return { button, box };
}
