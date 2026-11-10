import { ApiError, apiFetch } from "./api";
import { readRemediation } from "./remediation";
import { readDiagnosisRun, readRequestApprovalState } from "./request-approvals";

export type DiagnosisServer = "run" | "report";
export type PreparedTask = { id: string; name: string; status: string };

/** Go의 양수 int64 정본 표현만 허용한다. URL이나 모델 출력으로 task를 만들지 않는다. */
export function validTaskID(value: string): boolean {
  return /^[1-9][0-9]{0,18}$/.test(value) && BigInt(value) <= BigInt("9223372036854775807");
}

export async function listPreparedTasks(): Promise<PreparedTask[]> {
  const raw = await apiFetch<unknown>("/api/tasks", { cache: "no-store" });
  if (!raw || typeof raw !== "object" || !("tasks" in raw) || !Array.isArray(raw.tasks)) {
    throw new ApiError(422, "작업 목록 응답을 확인할 수 없어요.");
  }
  return raw.tasks.flatMap((item: unknown) => {
    if (!item || typeof item !== "object" || !("id" in item) || typeof item.id !== "string" || !validTaskID(item.id))
      return [];
    return [
      {
        id: item.id,
        name: "name" in item && typeof item.name === "string" ? item.name : "",
        status: "status" in item && typeof item.status === "string" ? item.status : "",
      },
    ];
  });
}

/** 서버가 같은 task의 실제 상태를 확인한 뒤에만 화면 선택을 확정한다. */
export async function verifyDiagnosisTask(task: string, server: DiagnosisServer): Promise<void> {
  if (!validTaskID(task)) throw new ApiError(400, "CLI가 출력한 실제 양수 task ID를 입력해 주세요.");
  if (server === "run") {
    const state = await readRequestApprovalState(task);
    if (!state.run?.run_ref) throw new ApiError(422, "실제 task·run 연결을 확인할 수 없어요.");
    return;
  }
  try {
    const record = await readDiagnosisRun(task);
    if (String(record.task_id) !== task) throw new ApiError(422, "선택한 작업과 저장 결과가 일치하지 않아요.");
  } catch (error) {
    // report는 존재하는 task라도 저장 종료 기록이 없을 수 있다. 401/403은 다른 읽기로 우회하지 않는다.
    if (!(error instanceof ApiError) || error.status !== 404) throw error;
    const report = await readRemediation(task, "internal");
    if (String(report.task_id) !== task) throw new ApiError(422, "선택한 작업과 보고서가 일치하지 않아요.");
  }
}

export function connectionError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 400) return error.message;
    if (error.status === 401) return "로그인이 만료됐어요. 다시 로그인해 주세요.";
    if (error.status === 403)
      return "이 서버에서 해당 task를 조회할 권한이 없어요. task ID와 실행/저장 보고서 서버 선택을 확인해 주세요.";
    if (error.status === 404) return "해당 task의 실행이 없어요. CLI task ID와 서버 종류를 확인해 주세요.";
    if (error.status === 503) return "서버가 아직 상태를 제공할 수 없어요. 연결을 다시 확인해 주세요.";
    if (error.status > 0) return `서버 응답을 확인하지 못했어요. (${error.status})`;
  }
  return "서버와 연결이 끊겼어요. 실행이 끝나 서버가 닫혔다면 report 서버를 시작하고 저장 결과로 연결해 주세요.";
}
