import { type ReactNode, useEffect, useRef, useState } from "react";

import { ApiError, useApiResource } from "../lib/api";
import {
  canControlRun,
  decideRequestApproval,
  getRequestApproval,
  type RequestApproval,
  readRequestApprovalState,
  stopRequestApprovalExecution,
} from "../lib/request-approvals";

const states: Record<RequestApproval["state"], string> = {
  pending: "승인 대기 · 아직 보내지 않음",
  allowed: "허용 결정됨 · 전송 결과는 별도",
  denied: "거절됨",
  expired: "만료됨",
  cancelled: "취소됨",
};
const execution: Record<RequestApproval["execution_state"], string> = {
  not_sent: "미전송",
  awaiting_dispatch: "현재 정책·권한·예산 재검사 대기",
  dispatch_pending: "전송 시도 중 · 결과 대기",
  completed: "응답 수신 완료",
  unknown: "실행 결과 불명 · 기존 승인으로 자동 재전송하지 않음",
};

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

/** task가 바뀌면 부모 key로 다시 마운트하여 이전 요청의 결정 상태를 폐기한다. */
export function RequestApprovalPanel(props: { taskId: string; active: boolean; refreshKey: number }) {
  return <TaskApprovalPanel key={props.taskId} {...props} />;
}

function TaskApprovalPanel({ taskId, active, refreshKey }: { taskId: string; active: boolean; refreshKey: number }) {
  const [selected, setSelected] = useState<string | null>(null);
  const [nonce, setNonce] = useState(0);
  const [now, setNow] = useState(Date.now());
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<string | null>(null);
  const [uncertain, setUncertain] = useState<ReadonlySet<string>>(() => new Set());
  const lock = useRef(false);
  const stopLock = useRef(false);
  const [stopAttempted, setStopAttempted] = useState(false);
  const [stopping, setStopping] = useState(false);
  const [stopMessage, setStopMessage] = useState<string | null>(null);
  const current = useRef({ active, selected });
  current.current = { active, selected };
  const list = useApiResource(
    () => (active ? readRequestApprovalState(taskId) : Promise.resolve(null)),
    [taskId, active, refreshKey, nonce],
  );
  const detail = useApiResource(
    () => (active && selected !== null ? getRequestApproval(taskId, selected) : Promise.resolve(null)),
    [taskId, active, selected, refreshKey, nonce],
  );

  useEffect(() => {
    if (!active) return;
    let ticks = 0;
    const timer = setInterval(() => {
      setNow(Date.now());
      // 결정은 보내지 않고 현재 상태만 새로 읽는다.
      if (++ticks % 5 === 0) setNonce((n) => n + 1);
    }, 1000);
    return () => clearInterval(timer);
  }, [active]);

  const run = list.data?.run;
  const runControllable = !!run && canControlRun(run) && !list.loading && !list.error;
  const rows = list.data?.approvals ?? [];
  const d = detail.data;
  const exact = !!d && d.id === selected && String(d.task_id) === taskId;
  const unexpired = !!d && Number.isFinite(Date.parse(d.expires_at)) && Date.parse(d.expires_at) > now;
  const canDecide =
    !!d &&
    active &&
    exact &&
    runControllable &&
    d.run_ref === run?.run_ref &&
    run?.execution?.state !== "blocked" &&
    !detail.loading &&
    !detail.error &&
    d.can_decide &&
    d.live &&
    d.state === "pending" &&
    d.execution_state === "not_sent" &&
    !d.consumed &&
    unexpired &&
    !uncertain.has(d.id) &&
    !stopAttempted &&
    !busy;

  const canStop = active && !list.loading && !list.error && !stopAttempted && runControllable;

  async function stopExecution() {
    if (!canStop || stopLock.current) return;
    stopLock.current = true;
    setStopAttempted(true);
    setStopping(true);
    setStopMessage("현재 전송 경계의 중지를 요청하고 있어요.");
    try {
      await stopRequestApprovalExecution(taskId);
      setStopMessage("현재 전송 경계를 중지했어요. 전체 점검의 종료나 이미 전송한 요청의 취소를 뜻하지 않아요.");
    } catch (error) {
      const known = error instanceof ApiError && [400, 401, 403, 404, 409].includes(error.status);
      setStopMessage(
        known
          ? "전송 중지 요청이 거부됐어요. 중지 완료로 보지 말고 권한과 최신 상태를 확인해 주세요."
          : "전송 중지 결과를 확인하지 못했어요. 중지 완료로 표시하거나 자동 재요청하지 않고 최신 상태를 조회해요.",
      );
    } finally {
      setStopping(false);
      setNonce((n) => n + 1);
    }
  }

  async function decide(decision: "allow" | "deny") {
    if (!canDecide || !d || lock.current || stopLock.current) return;
    lock.current = true;
    const id = d.id;
    setBusy(true);
    setMessage(null);
    try {
      await decideRequestApproval(taskId, d, decision);
      if (current.current.selected === id && current.current.active) {
        setMessage(
          decision === "allow"
            ? "허용 결정을 저장했어요. 실제 전송 결과는 아래에서 따로 확인해요."
            : "거절 결정을 저장했어요. 이 요청은 보내지 않아요.",
        );
      }
    } catch (error) {
      // 화면을 닫거나 숨겨도 결정 결과 불명은 요청에 남아야 한다.
      setUncertain((previous) => new Set([...previous, id]));
      if (current.current.selected === id && current.current.active) {
        const known = error instanceof ApiError && [400, 401, 403, 404, 409].includes(error.status);
        setMessage(
          known
            ? "결정을 적용하지 못했어요. 권한과 최신 요청 상태를 확인해 주세요."
            : "결정 저장 결과를 확인하지 못했어요. 자동으로 다시 보내지 않고 최신 상태를 조회해요.",
        );
      }
    } finally {
      lock.current = false;
      setBusy(false);
      setNonce((n) => n + 1);
    }
  }

  return (
    <section aria-label="실제 HTTP 요청 승인" style={{ margin: "20px 0 28px" }}>
      <h2>실제 HTTP 요청 승인</h2>
      <p className="hint">
        도구 호출 승인과 별개예요. 사용자 확인 모드에서는 모든 지원 요청을 전송 전에 한 건씩 확인하며, 허용 후에도 현재
        정책·권한·예산·감사를 다시 검사해요.
      </p>
      <div className="card">
        <button
          type="button"
          className="btn danger"
          disabled={!canStop || stopping}
          onClick={() => void stopExecution()}
        >
          이 점검의 현재 전송 중지
        </button>
        <p className="hint">
          현재 연결된 실행의 신규 대상 전송과 승인 대기를 중지해요. 전체 점검 종료·이전 전송 취소·대상 상태 복구는
          아니에요.
        </p>
        {stopMessage && <p role="status">{stopMessage}</p>}
      </div>
      {list.error && <p role="alert">요청 승인 목록을 불러오지 못했어요. ({list.error})</p>}
      {list.loading && !selected && <p>요청 승인 목록을 불러오는 중…</p>}
      {!list.loading && !list.error && list.data && rows.length === 0 && (
        <p>이 점검에 기록된 실제 요청 승인이 없어요. 사전 승인 범위 모드에서는 정상일 수 있어요.</p>
      )}
      <div className="recent-list">
        {rows.map((item) => (
          <div className="recent-row" key={item.id}>
            <div className="recent-main">
              <div className="nm">
                {item.method} {item.path}
              </div>
              <div className="meta">{states[item.state] ?? "알 수 없는 승인 상태"}</div>
            </div>
            <button
              type="button"
              className="btn xs"
              disabled={busy}
              onClick={() => {
                setSelected(item.id);
                setMessage(null);
              }}
            >
              요청 상세 {item.id}
            </button>
          </div>
        ))}
      </div>
      {selected && (
        <div className="card" style={{ marginTop: 12 }}>
          <button
            type="button"
            className="btn xs"
            onClick={() => {
              setSelected(null);
              setMessage(null);
            }}
          >
            요청 상세 닫기
          </button>
          {message && <p role="status">{message}</p>}
          {detail.loading && <p>최신 요청 내용을 불러오는 중…</p>}
          {!detail.loading && detail.error && <p role="alert">요청 상세를 불러오지 못했어요. ({detail.error})</p>}
          {!detail.loading && !detail.error && (!d || !exact) && (
            <p role="alert">선택한 점검·요청과 응답이 일치하지 않아 결정할 수 없어요.</p>
          )}
          {!detail.loading && !detail.error && d && exact && (
            <>
              <h3>
                {d.method} {d.path}
              </h3>
              <Row label="승인 상태">{states[d.state] ?? "알 수 없는 승인 상태"}</Row>
              <Row label="실행 상태">{execution[d.execution_state] ?? "알 수 없는 실행 상태"}</Row>
              <Row label="대상">
                {d.target_origin}
                {d.path}
              </Row>
              <Row label="계정 / 보호 세션">
                {d.account_ref} / {d.session_ref} (참조만 표시)
              </Row>
              <Row label="공개 입력">
                <pre style={{ whiteSpace: "pre-wrap" }}>{JSON.stringify(d.input_preview ?? {}, null, 2)}</pre>본문{" "}
                {d.body_bytes}바이트. 비공개 입력 원문은 표시하지 않아요.
              </Row>
              <Row label="검사 목적 / 영향">
                영향 미확인. 이 HTTP 요청의 목적·역할·예상 변경은 별도 근거로 확인해야 해요. 메서드나 모델 설명만으로
                안전하다고 판단하지 않아요.
              </Row>
              <Row label="승인 범위">고정한 요청 단 한 번. 일괄 승인이 아니며 변경된 요청은 새 승인이 필요해요.</Row>
              <Row label="만료">
                {d.expires_at}
                {!unexpired && " · 만료 시각 경과"}
              </Row>
              <Row label="정리 / 복구">이 요청에 검증된 정리·복구 계획이 제공되지 않았어요.</Row>
              <Row label="회차 / 정책">
                {d.run_ref} / {d.policy_revision}
              </Row>
              <Row label="호출 / 시도">
                {d.call_ref} / {d.attempt_id}
              </Row>
              <Row label="요청 식별자">{d.request_sha256}</Row>
              <Row label="소비 / 활성 회차">
                {d.consumed ? "승인 소비됨" : "미소비"} / {d.live ? "활성" : "비활성 · 새 실행 권한 없음"}
              </Row>
              {d.decided_by && (
                <Row label="결정 주체 / 시각">
                  {d.decided_by} / {d.decided_at}
                </Row>
              )}
              {d.reason && <Row label="사유">{d.reason}</Row>}
              <div className="row" style={{ gap: 8, marginTop: 12 }}>
                <button
                  type="button"
                  className="btn primary"
                  disabled={!canDecide}
                  onClick={() => void decide("allow")}
                >
                  이 HTTP 요청만 허용
                </button>
                <button type="button" className="btn danger" disabled={!canDecide} onClick={() => void decide("deny")}>
                  이 HTTP 요청 거절
                </button>
              </div>
              {!canDecide && (
                <p className="hint">
                  활성 회차의 미소비·미만료 승인 대기 요청만 결정할 수 있어요. 결정 실패 후에는 재전송하지 않고 상태를
                  확인해 주세요.
                </p>
              )}
            </>
          )}
        </div>
      )}
    </section>
  );
}
