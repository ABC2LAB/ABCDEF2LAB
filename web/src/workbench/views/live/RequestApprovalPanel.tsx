"use client";
import { useEffect, useRef, useState } from "react";

import {
  decideRequestApproval,
  getRequestApproval,
  type RequestApproval,
  readRequestApprovalState,
} from "@/lib/diagnosis/request-approvals";

import { useUrl } from "../../live/urlDiagnosis";
import { usePoll } from "../../live/usePoll";
import s from "../../workbench.module.css";

// E②의 실제 요청별 승인이다. 도구 intercept나 클라이언트 자동 승인으로 대신하지 않는다.
export function RequestApprovalPanel({
  taskId,
  active,
  compact = false,
}: {
  taskId: string;
  active: boolean;
  compact?: boolean;
}) {
  const flow = useUrl((st) => st.flow),
    verified = useUrl((st) => st.verified),
    uncertain = useUrl((st) => st.uncertain);
  const state = usePoll(() => readRequestApprovalState(taskId), [taskId], {
    enabled: active,
    intervalMs: active ? 2000 : 0,
  });
  const [busy, setBusy] = useState<string | null>(null),
    [unconfirmed, setUnconfirmed] = useState<string | null>(null),
    [message, setMessage] = useState("");
  const lock = useRef(false),
    generation = useRef(0),
    identity = useRef(taskId);
  useEffect(() => {
    identity.current = taskId;
    generation.current++;
    setBusy(null);
    setUnconfirmed(null);
    setMessage("");
    lock.current = false;
    return () => {
      generation.current++;
    };
  }, [taskId]);
  const data = state.data,
    runMatches = !!data && (!flow || data.run.run_ref === flow.run_ref);
  const observed = unconfirmed ? data?.approvals?.find((a) => a.id === unconfirmed) : null;
  // 상태 조회가 실제 결정·만료·취소를 확인하기 전에는 결정 POST를 반복하지 않는다.
  const unresolved = !!unconfirmed && (!observed || observed.state === "pending");
  const rows = data?.approvals ?? [];
  const pending = rows.filter((a) => a.state === "pending");
  const decide = async (row: RequestApproval, decision: "allow" | "deny") => {
    if (
      lock.current ||
      !runMatches ||
      !verified ||
      uncertain ||
      unresolved ||
      flow?.paused ||
      !row.can_decide ||
      !row.live
    )
      return;
    lock.current = true;
    const token = generation.current;
    setBusy(row.id);
    setMessage("");
    try {
      const fresh = await getRequestApproval(taskId, row.id);
      if (token !== generation.current || identity.current !== taskId) return;
      if (
        fresh.run_ref !== row.run_ref ||
        fresh.attempt_id !== row.attempt_id ||
        fresh.request_sha256 !== row.request_sha256 ||
        !fresh.live ||
        !fresh.can_decide
      )
        throw new Error("approval_changed");
      await decideRequestApproval(taskId, fresh, decision);
      if (token !== generation.current || identity.current !== taskId) return;
      setUnconfirmed(null);
      setMessage(
        decision === "allow"
          ? "이 요청의 단회 허용을 저장했습니다. 실제 전송 결과는 별도로 확인합니다."
          : "이 요청을 거부했습니다. 다른 요청을 자동 허용하지 않습니다.",
      );
      state.reload();
    } catch {
      if (token === generation.current && identity.current === taskId) {
        setUnconfirmed(row.id);
        setMessage("결정 결과를 확인하지 못했습니다. 재전송하지 말고 목록을 다시 조회하세요.");
      }
    } finally {
      if (token === generation.current && identity.current === taskId) {
        lock.current = false;
        setBusy(null);
      }
    }
  };
  if (!active) return null;
  return (
    <section className={s.sec} id="lDecide" aria-label="실제 HTTP 요청 승인">
      <div className={s.sh}>
        <h2>
          전송 전 확인 <span className={s.n}>{runMatches ? pending.length : "—"}</span>
        </h2>
        <button className={s.btn} type="button" disabled={!!busy} onClick={state.reload}>
          승인 목록 다시 조회
        </button>
      </div>
      {!runMatches ? <p role="status">현재 회차에 결속된 승인 상태를 확인하는 중입니다.</p> : null}
      {runMatches && !pending.length ? <p className={s.help}>승인 대기 요청이 없습니다.</p> : null}
      {runMatches
        ? pending.map((a) => (
            <div className={s.decide} key={a.id}>
              <h3>
                {a.method} {a.path}
              </h3>
              <p>
                {a.target_origin} · 실제 attempt {a.attempt_id}
              </p>
              <p className={s.help}>
                AI가 제출한 요청입니다. 영향은 자동 검증하지 않았습니다. 상태를 바꿀 수 있으므로 권한·복구 범위를
                확인하세요.
              </p>
              <dl className={s.kv}>
                <dt>서버 미리보기</dt>
                <dd>
                  {Object.entries(a.input_preview).map(([k, v]) => (
                    <div key={k}>
                      {k}: {v}
                    </div>
                  ))}
                </dd>
                <dt>본문 크기</dt>
                <dd>{a.body_bytes} bytes · 원문 비공개</dd>
                <dt>상태</dt>
                <dd>
                  {a.state} · {a.execution_state}
                </dd>
              </dl>
              <div className={s.acts}>
                <button
                  className={s.btn}
                  type="button"
                  disabled={!!busy || unresolved || !a.can_decide || !a.live || !!flow?.paused}
                  onClick={() => void decide(a, "allow")}
                >
                  이 요청 허용
                </button>
                <button
                  className={s.btn}
                  type="button"
                  disabled={!!busy || unresolved || !a.can_decide || !a.live || !!flow?.paused}
                  onClick={() => void decide(a, "deny")}
                >
                  이 요청 거부
                </button>
              </div>
            </div>
          ))
        : null}
      {!compact && runMatches ? (
        <div className={s.help}>
          {rows
            .filter((a) => a.state !== "pending")
            .slice(-10)
            .map((a) => (
              <p key={a.id}>
                {a.method} {a.path} · 결정 {a.state} · 전송 {a.execution_state}
              </p>
            ))}
        </div>
      ) : null}
      {message ? <p role="status">{message}</p> : null}
      {unresolved ? <p role="alert">승인 결정 결과 불명 · 조회로 확인하기 전에는 다시 결정하지 않습니다.</p> : null}
    </section>
  );
}
