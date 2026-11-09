import type { ReactNode } from "react";

import { useApiResource } from "../lib/api";
import { type RequestApprovalRunState, readDiagnosisRunDisplay } from "../lib/request-approvals";

/**
 * 진단 실행의 저장된 종료 상태를 보여주는 자족형 카드.
 *
 * 데이터·판정은 전부 lib/request-approvals(readDiagnosisRunDisplay)에 있고 여기선 렌더만 한다.
 * taskId·active 만 받는 드롭인이라 레이아웃이 바뀌어도 배치 한 줄만 옮기면 된다(데이터 seam 불변).
 *
 * fail-closed: "결과 없음"·"미실행"·"다른 작업"을 완료·성공으로 칠하지 않는다. 전송 차단·실행 마감·
 * 취약 판정은 서로 대신하지 않으므로 counts 를 "검증됨"으로 해석하지 않는다.
 */

const PHASE: Record<RequestApprovalRunState["phase"], string> = {
  prepared: "준비됨 · 아직 실행 전",
  running: "실행 중",
  stopping: "중지 처리 중",
  closed: "종료됨",
  failed: "실패",
};

const OUTCOME: Record<RequestApprovalRunState["outcome"], string> = {
  "": "결과 미기록",
  completed: "정상 종료",
  cancelled: "취소로 종료",
  failed: "실패로 종료",
};

// attempts 는 E②가 판정 전에 접수한 수(차단분 포함)라 "전송"이 아니다. 실제 대상 송신은 forward_attempted.
const COUNTS: { key: keyof NonNullable<RequestApprovalRunState["execution"]>["counts"]; label: string }[] = [
  { key: "attempts", label: "요청 접수" },
  { key: "allowed", label: "허용" },
  { key: "denied", label: "차단" },
  { key: "unsupported", label: "미지원" },
  { key: "forward_attempted", label: "대상 전송 시도" },
  { key: "responses", label: "응답 수신" },
  { key: "unknown", label: "불명" },
  { key: "calls", label: "호출" },
];

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="srow">
      <div className="k">{label}</div>
      <div className="v" style={{ overflowWrap: "anywhere" }}>
        {children}
      </div>
    </div>
  );
}

export function DiagnosisRunSummary({ taskId, active }: { taskId: string; active: boolean }) {
  const { data, loading, error } = useApiResource(
    () => (active ? readDiagnosisRunDisplay(taskId) : Promise.resolve(null)),
    [taskId, active],
  );

  return (
    <section aria-label="진단 실행 종료 상태" style={{ margin: "16px 0 20px" }}>
      <h2>진단 실행 종료 상태</h2>
      <p className="hint">
        마지막으로 저장된 실행 회차의 종료 상태예요. 전송 차단·실행 마감·취약 판정은 서로 다른 것이라 아래 수치를
        "검증됨"으로 읽지 않아요.
      </p>
      <div className="card">
        {loading && <p>실행 종료 상태를 불러오는 중…</p>}
        {!loading && error && <p role="alert">실행 종료 상태를 불러오지 못했어요. ({error})</p>}
        {!loading && !error && data?.kind === "none" && (
          <p role="status">저장된 실행 종료 결과가 없어요. 완료로 간주하지 않아요.</p>
        )}
        {!loading && !error && data?.kind === "not_run" && (
          <p role="status">
            {data.preparedOnly ? "모델 준비 단계만 있고 실행은 하지 않았어요." : "아직 실행하지 않았어요."} 완료 회차로
            보지 않아요.
          </p>
        )}
        {!loading && !error && data?.kind === "unavailable" && (
          <p role="status">현재 선택한 작업의 실행 결과가 아니거나 확인할 수 없어요. 완료로 보지 않아요.</p>
        )}
        {!loading && !error && data?.kind === "run" && (
          <>
            <Row label="단계">{PHASE[data.run.phase] ?? "알 수 없는 단계"}</Row>
            <Row label="결과">{OUTCOME[data.run.outcome] ?? "알 수 없는 결과"}</Row>
            <Row label="마감">
              {data.run.drain_complete ? "진행 중 요청 정리 완료" : "진행 중 요청 정리 미완료"}
              {data.run.stop_requested && " · 중지 요청됨"}
              {data.run.context_cancelled && " · 컨텍스트 취소됨"}
              {data.run.closure_failed && " · 마감 실패(완전 종료로 보지 않음)"}
            </Row>
            {data.run.execution ? (
              <>
                <Row label="요청 · 전송 집계">
                  <div className="dgrid" style={{ gridTemplateColumns: "repeat(4, 1fr)", gap: 8, margin: "4px 0" }}>
                    {COUNTS.map(({ key, label }) => (
                      <div className="tile" key={key} style={{ padding: "8px 10px" }}>
                        <div className="k">{label}</div>
                        <div className="v" style={{ fontSize: 18 }}>
                          {data.run.execution?.counts[key] ?? "—"}
                        </div>
                      </div>
                    ))}
                  </div>
                  <span className="hint">
                    요청 접수는 판정 전에 받은 수라 차단분도 포함해요. 실제로 대상에 보낸 건 대상 전송 시도예요.
                  </span>
                  <div>감사 {data.run.execution.audit_complete ? "완결" : "미완결"}</div>
                </Row>
                <Row label="회차 / 정책">
                  {data.run.run_ref || "—"} / {data.run.execution.policy_revision || "—"} (참조만 표시)
                </Row>
              </>
            ) : (
              <Row label="전송 집계">실행 세부 집계가 저장되지 않았어요. 전송 건수를 0으로 단정하지 않아요.</Row>
            )}
            {data.savedAt && <Row label="저장 시각">{data.savedAt}</Row>}
          </>
        )}
      </div>
    </section>
  );
}
