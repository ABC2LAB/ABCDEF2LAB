import type { ReactNode } from "react";

import { MOCK } from "../lib/mock";
import {
  canControlRun,
  type DiagnosisRunRecord,
  type RequestApprovalRunState,
  RUN_COUNT_NOTE,
  RUN_COUNT_ROWS,
} from "../lib/request-approvals";
import { describeRun, type RunView, useRunStatus } from "../lib/run-status";
import { Pill } from "./ui";

const APPROVAL_MODE: Record<string, string> = {
  confirm_every_request: "매 요청 확인",
  preauthorized_scope: "사전 승인 범위",
};
const EXECUTION_STATE: Record<string, string> = { active: "전송 가능", blocked: "전송 차단" };
const grid = { display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(150px, 1fr))", gap: "0 18px" };

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="srow">
      <div className="k">{label}</div>
      <div className="v">{children}</div>
    </div>
  );
}

function RunFields({ run }: { run: RequestApprovalRunState }) {
  const summary = describeRun(run);
  const execution = run.execution;
  return (
    <>
      <p style={{ marginBottom: 12 }}>
        <Pill tone={summary.tone}>{summary.label}</Pill>
      </p>
      {summary.note && <p className="hint">{summary.note}</p>}
      <div style={grid}>
        <Row label="회차">{run.run_ref || "—"}</Row>
        <Row label="마감 확인">{run.drain_complete ? "확인됨" : "아직 안 됨"}</Row>
        <Row label="중지 요청">{run.stop_requested ? "있음" : "없음"}</Row>
        {run.closure_failed && <Row label="자원 마감">실패</Row>}
      </div>
      {execution ? (
        <>
          <div style={grid}>
            <Row label="전송 경계">{EXECUTION_STATE[execution.state] ?? execution.state}</Row>
            <Row label="승인 방식">{APPROVAL_MODE[execution.approval_mode] ?? execution.approval_mode}</Row>
            <Row label="감사 기록">{execution.audit_complete ? "완결" : "미완결"}</Row>
            {execution.reason && <Row label="사유">{execution.reason}</Row>}
          </div>
          <div style={grid}>
            {RUN_COUNT_ROWS.map(({ key, label }) => (
              <Row key={key} label={label}>
                {execution.counts[key]}
              </Row>
            ))}
          </div>
          <p className="hint">{RUN_COUNT_NOTE}</p>
        </>
      ) : (
        <p className="hint">아직 전송 경계를 획득하지 않았어요.</p>
      )}
    </>
  );
}

function SavedModel({ record }: { record: DiagnosisRunRecord }) {
  const model = record.model;
  const preparation = record.model_preparation?.length ?? 0;
  if (!model && preparation === 0) return null;
  return (
    <div style={grid}>
      {model && (
        <>
          <Row label="제공자 / 모델">
            {model.provider_slug || "—"} / {model.model || "—"}
          </Row>
          <Row label="모델 요청">{model.requests}</Row>
          <Row label="입력·출력 토큰">
            {model.input_tokens} / {model.output_tokens}
          </Row>
          <Row label="보고된 비용">${(model.cost_usd_micros / 1_000_000).toFixed(4)}</Row>
          {model.errors > 0 && <Row label="모델 오류">{model.errors}건, 실제 비용 불확실</Row>}
          {model.failure_reason && <Row label="모델 실패 사유">{model.failure_reason}</Row>}
        </>
      )}
      {preparation > 0 && <Row label="계획 준비 기록">{preparation}건</Row>}
    </div>
  );
}

function LastKnown({ run }: { run: RequestApprovalRunState | null }) {
  if (!run) return null;
  return (
    <>
      <p className="hint" style={{ marginTop: 10 }}>
        마지막으로 확인한 상태예요. 지금 상태가 아닐 수 있어요.
      </p>
      <RunFields run={run} />
    </>
  );
}

function Body({ view, onOpenApprovals }: { view: RunView; onOpenApprovals: () => void }) {
  switch (view.kind) {
    case "loading":
      return <p className="hint">실행 상태를 확인하는 중이에요.</p>;
    case "unbound":
      return (
        <p className="hint">
          이 점검에 연결된 실행 또는 저장된 종료 결과가 없어요. task ID와 CLI 준비 상태를 확인해 주세요.
        </p>
      );
    case "forbidden":
      return <p role="alert">이 task의 실행 상태를 볼 권한이 없어요. 이전 실행 데이터는 표시하지 않아요.</p>;
    case "unavailable":
      return (
        <>
          <p role="alert">실행 상태를 읽지 못했어요. ({view.message})</p>
          <LastKnown run={view.lastRun} />
        </>
      );
    case "disconnected":
      return (
        <>
          <p role="alert">
            진단 서버와 연결이 끊겼어요. 실행이 끝나도 서버가 닫혀요. 끝났다고 보지 말고, 보고서 서버를 띄운 뒤 다시
            확인해 주세요.
          </p>
          <LastKnown run={view.lastRun} />
        </>
      );
    case "preparedOnly":
      return (
        <>
          <p>계획 준비만 기록됐고 실행은 시작되지 않았어요.</p>
          <SavedModel record={view.record} />
        </>
      );
    case "saved":
      return (
        <>
          <p className="hint">
            저장된 종료 기록이에요{view.record.saved_at ? ` (${new Date(view.record.saved_at).toLocaleString()})` : ""}.
            승인과 중지는 할 수 없어요.
          </p>
          {view.record.run && <RunFields run={view.record.run} />}
          <SavedModel record={view.record} />
        </>
      );
    case "live":
      return (
        <>
          <RunFields run={view.run} />
          {view.pending > 0 && canControlRun(view.run) && (
            <button className="btn primary" type="button" onClick={onOpenApprovals}>
              승인 {view.pending}건 보기
            </button>
          )}
        </>
      );
  }
}

type PanelProps = { taskId: string; active: boolean; onOpenApprovals: () => void; source?: "run" | "report" };

/** 선택한 점검의 실제 실행 상태. 서버가 준 값만 보여 주고 없는 실행 정보를 만들지 않는다. 목업에는 실행이 없다. */
export function RunStatusPanel(props: PanelProps) {
  if (MOCK) return null;
  return <LiveRunStatus {...props} />;
}

function LiveRunStatus({ taskId, active, onOpenApprovals, source }: PanelProps) {
  const view = useRunStatus(taskId, active, source);
  return (
    <section className="card" aria-label="실행 상태">
      <h3>실행 상태</h3>
      <Body view={view} onOpenApprovals={onOpenApprovals} />
    </section>
  );
}
