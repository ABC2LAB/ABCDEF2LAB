"use client";

// E② 의 실제 요청별 승인(전송 전 확인). 대시보드의 '승인 요청' 상세와 점검 진행의 '확인 필요' 상자가 같이 쓴다.
// 도구 intercept 나 클라이언트 자동 승인으로 대신하지 않는다. 결정 POST 는 자동으로 다시 보내지 않고,
// 결과가 불명이면 상태 조회로 확인되기 전까지 다시 결정하지 않는다.

import { useCallback, useEffect, useRef, useState } from "react";

import { decideRequestApproval, getRequestApproval, type RequestApproval } from "@/lib/diagnosis/request-approvals";

import { cx } from "../../lib/cx";
import { fetchRequestApprovals, type RequestApprovalState } from "../../live/api";
import { useUrl } from "../../live/urlDiagnosis";
import { type Poll, usePoll } from "../../live/usePoll";
import s from "../../workbench.module.css";

export interface Approvals {
  poll: Poll<RequestApprovalState>;
  /** 승인 상태가 지금 진단 · 회차와 맞는가 (아니면 결정하지 않는다) */
  runMatches: boolean;
  rows: RequestApproval[];
  pending: RequestApproval[];
  /** 결정 중인 승인 id */
  busy: string | null;
  /** 결정 결과 불명 (조회로 확인되기 전까지 다시 결정하지 않는다) */
  unresolved: boolean;
  message: string;
  canDecide: (row: RequestApproval) => boolean;
  decide: (row: RequestApproval, decision: "allow" | "deny") => Promise<void>;
}

/** 실제 task 의 요청별 승인 상태와 결정. active 가 아니면 조회하지 않는다. */
export function useRequestApprovals(taskId: string, active: boolean): Approvals {
  const job = useUrl((st) => st.job);
  const flow = useUrl((st) => st.flow);
  const verified = useUrl((st) => st.verified);
  const uncertain = useUrl((st) => st.uncertain);
  // 미리보기의 비밀 값은 live/api 가 가린다
  const poll = usePoll(() => fetchRequestApprovals(taskId), [taskId], {
    enabled: active,
    intervalMs: active ? 2000 : 0,
  });
  const [busy, setBusy] = useState<string | null>(null);
  const [unconfirmed, setUnconfirmed] = useState<string | null>(null);
  const [message, setMessage] = useState("");
  const lock = useRef(false);
  const generation = useRef(0);
  const identity = useRef(taskId);
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
  useEffect(() => {
    if (!active) {
      generation.current++;
      lock.current = false;
      setBusy(null);
    }
  }, [active]);

  const data = poll.data;
  const runMatches =
    !!data &&
    !poll.error &&
    job?.task_id === taskId &&
    ["preparing", "running"].includes(job.state) &&
    (!flow || (flow.task_id === taskId && data.run.run_ref === flow.run_ref));
  const rows = runMatches ? (data?.approvals ?? []) : [];
  const pending = rows.filter((a) => a.state === "pending");
  const observed = unconfirmed ? data?.approvals?.find((a) => a.id === unconfirmed) : null;
  const unresolved = !!unconfirmed && (!observed || observed.state === "pending");

  const gate = useCallback(
    (row: RequestApproval) => {
      const st = useUrl.getState();
      return (
        st.verified &&
        !st.uncertain &&
        st.job?.task_id === taskId &&
        ["preparing", "running"].includes(st.job.state) &&
        (!st.flow ||
          (st.flow.task_id === taskId &&
            st.flow.run_ref === row.run_ref &&
            !st.flow.paused &&
            !st.flowUncertain &&
            st.flowVerified))
      );
    },
    [taskId],
  );
  const canDecide = (row: RequestApproval) =>
    active && !busy && !unresolved && verified && !uncertain && row.can_decide && row.live && runMatches && gate(row);

  const decide = async (row: RequestApproval, decision: "allow" | "deny") => {
    if (!canDecide(row) || lock.current || flow?.paused) return;
    lock.current = true;
    const token = generation.current;
    setBusy(row.id);
    setMessage("");
    try {
      const fresh = await getRequestApproval(taskId, row.id);
      if (token !== generation.current || identity.current !== taskId || !gate(row)) return;
      if (
        fresh.run_ref !== row.run_ref ||
        fresh.attempt_id !== row.attempt_id ||
        fresh.request_sha256 !== row.request_sha256 ||
        !fresh.live ||
        !fresh.can_decide
      )
        throw new Error("approval_changed");
      setUnconfirmed(row.id);
      await decideRequestApproval(taskId, fresh, decision);
      if (token !== generation.current || identity.current !== taskId) return;
      setUnconfirmed(null);
      setMessage(decision === "allow" ? "이 요청 한 번만 허용했습니다." : "이 요청을 거부했습니다.");
      poll.reload();
    } catch {
      if (token === generation.current && identity.current === taskId) {
        setUnconfirmed(row.id);
        setMessage("결정 결과를 확인하지 못했습니다. 다시 보내지 말고 목록을 다시 조회하세요.");
      }
    } finally {
      if (token === generation.current && identity.current === taskId) {
        lock.current = false;
        setBusy(null);
      }
    }
  };

  return { poll, runMatches, rows, pending, busy, unresolved, message, canDecide, decide };
}

/** 보낼 요청: 메서드 · 경로 · 대상 host 와 서버가 준 미리보기(본문은 빈 줄 뒤). 원문 본문은 서버가 공개하지 않는다. */
export function ApprovalRequestCode({ row }: { row: RequestApproval }) {
  let host = row.target_origin;
  try {
    host = new URL(row.target_origin).host;
  } catch {
    // 서버 값을 그대로 둔다
  }
  const entries = Object.entries(row.input_preview);
  const head = entries.filter(([k]) => k !== "body");
  const body = entries.find(([k]) => k === "body")?.[1];
  const text = [
    `${row.method} ${row.path}`,
    `Host: ${host}`,
    ...head.map(([k, v]) => `${k}: ${v}`),
    ...(body !== undefined ? ["", body] : []),
  ].join("\n");
  return <pre className={s.code}>{text}</pre>;
}

/** 승인하고 보내기 · 거부 버튼 줄 (+ 결정 안내). */
export function ApprovalActions({ a, row, size }: { a: Approvals; row: RequestApproval; size?: "sm" }) {
  const off = !a.canDecide(row);
  return (
    <>
      <div className={s.acts}>
        <button
          className={cx(s.btn, s.pri, size && s.sm)}
          type="button"
          disabled={off}
          onClick={() => void a.decide(row, "allow")}
        >
          {a.busy === row.id ? "보내는 중…" : "승인하고 보내기"}
        </button>
        <button
          className={cx(s.btn, size && s.sm)}
          type="button"
          disabled={off}
          onClick={() => void a.decide(row, "deny")}
        >
          거부
        </button>
      </div>
      {a.message ? <p role="status">{a.message}</p> : null}
      {a.unresolved ? <p role="alert">결정 결과 불명 · 조회로 확인하기 전에는 다시 결정하지 않습니다.</p> : null}
    </>
  );
}

/** 결정 결과 글 (지난 요청 목록). */
export const APPROVAL_STATE_TEXT: Readonly<Record<RequestApproval["state"], string>> = {
  pending: "승인 대기",
  allowed: "허용",
  denied: "거부",
  expired: "만료",
  cancelled: "취소",
};
