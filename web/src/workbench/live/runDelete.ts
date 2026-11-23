// 끝난 점검 '삭제'(2026-11-23 사용자·팀 결정): POST /api/diagnoses/v2/drafts/{id}/hide. 서버는 그 회차를 Workbench 목록·
// 조회에서 숨기기만 한다 — DB·회차 폴더·저널은 그대로이고 실행 횟수·예산에 계속 센다.

import { ApiError } from "@/lib/diagnosis/api";
import { commandURLDiagnosis, type URLDiagnosisJob } from "@/lib/diagnosis/diagnosis-url";

export async function deleteURLRun(job: URLDiagnosisJob, owner: string): Promise<void> {
  await commandURLDiagnosis(job, "hide", owner);
}

/** 이미 숨긴 회차(앞 요청의 응답만 잃은 경우 포함)는 서버가 not_found 로 답한다 → 지워진 것으로 본다. */
export function deleteRunGone(error: unknown): boolean {
  return error instanceof ApiError && error.status === 404 && error.message === "not_found";
}

// 결과를 모르는 실패(연결 끊김·503·응답 형식 확인 실패 등)를 '못 지웠다'로 말하지 않는다(규칙 5, 이경준 #867 리뷰).
// 다시 누르면 서버가 지금 상태로 답한다(이미 숨겼으면 not_found → 목록에서 사라짐).
const UNKNOWN = "삭제됐는지 확인하지 못했습니다. 다시 누르면 확인합니다.";

export function deleteRunFailureText(error: unknown): string {
  const status = error instanceof ApiError ? error.status : 0;
  const code = error instanceof ApiError ? error.message : "";
  if (code === "route_not_found") return "서버가 아직 점검 삭제를 지원하지 않습니다.";
  if (code === "run_active") return "진행 중이거나 마감 중인 점검은 지울 수 없습니다.";
  if (code === "revision_conflict") return "점검 상태가 바뀌었습니다. 다시 연 뒤 지워 주세요.";
  // authentication_changed 는 응답을 받은 뒤 세션이 바뀐 경우라 서버가 처리했을 수 있다 → 결과 불명
  if (status === 401 && code !== "authentication_changed") return "다시 로그인해 주세요.";
  if (status === 403) return "이 점검을 지울 권한이 없습니다.";
  return UNKNOWN;
}
