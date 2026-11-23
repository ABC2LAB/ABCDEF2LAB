// 끝난 점검 삭제(2026-11-23): POST /api/diagnoses/v2/drafts/{id}/delete. 서버가 그 회차의 점검 결과(작업 기록·보고서·
// 판정·회차 폴더)를 지우고, 회차 기록 자체는 남겨 실행 횟수·예산에 계속 센다. E② 감사 기록·대상 리셋 기록은 지우지 않는다.

import { ApiError } from "@/lib/diagnosis/api";
import { commandURLDiagnosis, type URLDiagnosisJob } from "@/lib/diagnosis/diagnosis-url";

export async function deleteURLRun(job: URLDiagnosisJob, owner: string): Promise<void> {
  await commandURLDiagnosis(job, "delete", owner);
}

export function deleteRunFailureText(error: unknown): string {
  const status = error instanceof ApiError ? error.status : 0;
  const code = error instanceof ApiError ? error.message : "";
  if (code === "route_not_found") return "서버가 아직 점검 삭제를 지원하지 않습니다.";
  if (code === "run_active") return "진행 중이거나 마감 중인 점검은 지울 수 없습니다.";
  if (code === "revision_conflict") return "점검 상태가 바뀌었습니다. 다시 연 뒤 지워 주세요.";
  const text: Record<number, string> = {
    0: "삭제하지 못했습니다(연결 실패).",
    401: "다시 로그인해 주세요.",
    403: "이 점검을 지울 권한이 없습니다.",
    404: "이미 지워졌거나 없는 점검입니다.",
    422: "서버 응답을 확인하지 못했습니다. 목록을 다시 읽어 주세요.",
    503: "지금은 지울 수 없습니다. 잠시 뒤 다시 시도해 주세요.",
  };
  return text[status] ?? `삭제하지 못했습니다 (HTTP ${status}).`;
}
