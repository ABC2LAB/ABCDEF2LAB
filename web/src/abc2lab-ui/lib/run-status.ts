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
  | { kind: "forbidden" }
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

/** 조회 거부는 이전 응답을 복원하지 않는다. 저장 결과를 먼저 읽어 report의 미지원 승인 경로와 분리한다. */
function failureView(error: unknown, lastRun: RequestApprovalRunState | null): RunView {
  if (error instanceof ApiError) {
    if (error.status === 401) return { kind: "loading" };
    if (error.status === 403) return { kind: "forbidden" };
    if (error.status === 404) return { kind: "unbound" };
    return { kind: "unavailable", message: error.message, lastRun };
  }
  if (error instanceof TypeError) return { kind: "disconnected", lastRun };
  return { kind: "unavailable", message: error instanceof Error ? error.message : "응답 미확인", lastRun };
}

async function readView(
  taskId: string,
  lastRun: RequestApprovalRunState | null,
  source?: "run" | "report",
): Promise<RunView> {
  if (source === "report") {
    try {
      const record = await readDiagnosisRun(taskId);
      return classifyDiagnosisRun(record).kind === "run" ? { kind: "saved", record } : { kind: "preparedOnly", record };
    } catch (error) {
      return failureView(error, null);
    }
  }
  if (source === "run") {
    try {
      const live = await readRequestApprovalState(taskId);
      return {
        kind: "live",
        run: live.run,
        pending: (live.approvals ?? []).filter((item) => item.state === "pending").length,
      };
    } catch (error) {
      return failureView(error, lastRun);
    }
  }
  let prepared: DiagnosisRunRecord | null = null;
  try {
    const record = await readDiagnosisRun(taskId);
    if (classifyDiagnosisRun(record).kind === "run") return { kind: "saved", record };
    prepared = record;
  } catch (error) {
    if (error instanceof ApiError && (error.status === 401 || error.status === 403)) {
      return failureView(error, null);
    }
    // 저장 기록이 없거나 아직 저장소를 읽지 못해도 실행 서버 상태를 독립 확인한다.
  }
  try {
    const live = await readRequestApprovalState(taskId);
    return {
      kind: "live",
      run: live.run,
      pending: (live.approvals ?? []).filter((item) => item.state === "pending").length,
    };
  } catch (error) {
    // 방금 권한 검사를 통과해 받은 NOT_RUN만 보존한다. 실행/승인 권한으로 쓰지 않는다.
    if (prepared && error instanceof ApiError && (error.status === 403 || error.status === 404)) {
      return { kind: "preparedOnly", record: prepared };
    }
    return failureView(error, lastRun);
  }
}

/** active 인 동안 한 task 의 실행 상태를 폴링한다. 응답이 겹치거나 이전 task 응답이 끼어들지 않게 한다. */
export function useRunStatus(taskId: string, active: boolean, source?: "run" | "report"): RunView {
  const [state, setState] = useState<{ taskId: string; source?: "run" | "report"; view: RunView }>({
    taskId,
    source,
    view: { kind: "loading" },
  });

  useEffect(() => {
    if (!active) return;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let lastRun: RequestApprovalRunState | null = null;

    async function tick() {
      let delay = RUN_POLL_MS;
      const view = await readView(taskId, lastRun, source);
      if (stopped) return;
      if (view.kind === "live") lastRun = view.run;
      else delay = RUN_RETRY_MS;
      if (view.kind === "forbidden" || view.kind === "loading") lastRun = null;
      setState({ taskId, source, view });
      timer = setTimeout(() => void tick(), delay);
    }

    void tick();
    return () => {
      stopped = true;
      if (timer !== undefined) clearTimeout(timer);
    };
  }, [taskId, active, source]);

  return state.taskId === taskId && state.source === source ? state.view : { kind: "loading" };
}
