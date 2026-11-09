import { useEffect, useState } from "react";

import { ApiError } from "./api";
import {
  classifyDiagnosisRun,
  type DiagnosisRunRecord,
  type RequestApprovalRunState,
  readDiagnosisRun,
  readRequestApprovalState,
} from "./request-approvals";

/** docs/first-diagnosis-launch.md 의 UI 계약: 화면 표시 중 GET 폴링, 1초 간격. */
const RUN_POLL_MS = 1000;
const RUN_RETRY_MS = 3000;

export type RunView =
  | { kind: "loading" }
  | { kind: "live"; run: RequestApprovalRunState; pending: number }
  | { kind: "saved"; record: DiagnosisRunRecord }
  | { kind: "preparedOnly"; record: DiagnosisRunRecord }
  | { kind: "unbound" }
  | { kind: "unavailable"; message: string; lastRun: RequestApprovalRunState | null }
  | { kind: "disconnected"; lastRun: RequestApprovalRunState | null };

export type RunSummary = { label: string; tone?: "muted" | "warn"; note?: string };

/** 서버의 phase·outcome 을 그대로 옮긴다. 실행 마감을 목표 달성이나 취약점 없음으로 바꾸지 않는다. */
export function describeRun(run: RequestApprovalRunState): RunSummary {
  switch (run.phase) {
    case "prepared":
      return { label: "실행 준비됨", tone: "muted" };
    case "running":
      return { label: "실행 중" };
    case "stopping":
      return { label: "중지·마감 중", tone: "warn", note: "신규 전송을 막고 마감을 기다리고 있어요." };
    case "closed":
      if (!run.drain_complete) return { label: "마감 미확인", tone: "warn" };
      if (run.outcome === "completed") {
        return {
          label: "실행 마감",
          tone: "muted",
          note: "이번 실행과 감사가 마감됐어요. 목표 달성이나 취약점 없음을 뜻하지 않아요.",
        };
      }
      if (run.outcome === "cancelled") {
        return { label: "중지 후 마감", tone: "muted", note: "이미 보낸 요청이나 대상 변경은 되돌리지 않아요." };
      }
      if (run.outcome === "failed") return { label: "실패로 마감", tone: "warn" };
      return { label: "마감 결과 미확인", tone: "warn" };
    case "failed":
      return {
        label: "실행 실패",
        tone: "warn",
        note: run.closure_failed ? "자원 마감까지 실패했어요. 종료됐다고 보지 마세요." : undefined,
      };
    default:
      return { label: `알 수 없는 상태 (${String(run.phase)})`, tone: "warn" };
  }
}

/** 실행 조회가 실패했을 때 보여 줄 상태. 인증 실패는 로그인 화면이 대신하므로 null 이다. */
async function viewAfterFailure(
  taskId: string,
  error: unknown,
  lastRun: RequestApprovalRunState | null,
): Promise<RunView | null> {
  if (error instanceof ApiError && error.status === 401) return null;
  // 실행이 끝나 서버가 닫힌 뒤에도 저장 기록은 report 서버에서 읽을 수 있다.
  try {
    const record = await readDiagnosisRun(taskId);
    return classifyDiagnosisRun(record).kind === "run" ? { kind: "saved", record } : { kind: "preparedOnly", record };
  } catch {
    /* 저장 기록이 없으면 원래 실패로 판단한다. */
  }
  if (error instanceof ApiError) {
    if (error.status === 403 || error.status === 404) return { kind: "unbound" };
    if (error.status < 500 || error.status === 503) return { kind: "unavailable", message: error.message, lastRun };
  }
  return { kind: "disconnected", lastRun };
}

/** active 인 동안 한 task 의 실행 상태를 폴링한다. 응답이 겹치거나 이전 task 응답이 끼어들지 않게 한다. */
export function useRunStatus(taskId: string, active: boolean): RunView {
  const [state, setState] = useState<{ taskId: string; view: RunView }>({ taskId, view: { kind: "loading" } });

  useEffect(() => {
    if (!active) return;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let lastRun: RequestApprovalRunState | null = null;

    async function tick() {
      let delay = RUN_POLL_MS;
      try {
        const live = await readRequestApprovalState(taskId);
        if (stopped) return;
        lastRun = live.run;
        const pending = (live.approvals ?? []).filter((item) => item.state === "pending").length;
        setState({ taskId, view: { kind: "live", run: live.run, pending } });
      } catch (error) {
        if (stopped) return;
        delay = RUN_RETRY_MS;
        const view = await viewAfterFailure(taskId, error, lastRun);
        if (stopped) return;
        if (view) setState({ taskId, view });
      }
      timer = setTimeout(() => void tick(), delay);
    }

    void tick();
    return () => {
      stopped = true;
      if (timer !== undefined) clearTimeout(timer);
    };
  }, [taskId, active]);

  return state.taskId === taskId ? state.view : { kind: "loading" };
}
