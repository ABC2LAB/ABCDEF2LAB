"use client";

// 멈춘 작업 해제(C04, 재기동형). 결과 불명·종료 중인 작업이 새 점검 시작을 막고, 엔진 활동과 기록 갱신이 기준 시간
// 넘게 없을 때만 관리자가 사유를 적고 해제한다. 해제하면 준비 서비스가 다시 시작하며 화면 연결이 잠깐 끊긴다. 화면은
// '재시작 중'을 보이고 다시 붙으면 서버 감사 기록으로 결과를 확인한다. 결과 불명 기록은 그대로 남는다.
// 막는 작업이 없거나(not_stuck), 관리자가 아니거나(403), 근거를 읽지 못하면 아무것도 그리지 않는다(새 시작은 서버가 막는다).

import { useCallback, useEffect, useRef, useState } from "react";

import { cx } from "../../lib/cx";
import { dateTimeOf } from "../../live/map";
import {
  getReleaseView,
  MAX_REASON_BYTES,
  mayBeRestarting,
  preparationReady,
  type ReleaseView,
  reasonBytes,
  refusalText,
  releaseFailureText,
  requestRelease,
  stateLabel,
  validReason,
} from "../../live/stuckRelease";
import s from "../../workbench.module.css";

// 막는 작업이 있을 때 근거(남은 시간)를 다시 읽는 주기
const REFRESH_MS = 30_000;
// 재시작 중 health를 보는 주기와 포기 시한(준비 서비스 마감 상한 60초 + 재시작 정책 대기 + api·workbench 재시작)
const RECONNECT_POLL_MS = 2_000;
const RECONNECT_LIMIT_MS = 10 * 60_000;

type Phase = "idle" | "sending" | "restarting" | "waiting_boundary" | "released" | "gave_up";

const timeText = (v: string) => (v ? dateTimeOf(v) || v : "");

export function StuckReleaseBox({ onReleased }: { onReleased?: () => void }) {
  const generation = useRef(0);
  const [view, setView] = useState<ReleaseView | null>(null);
  const [hidden, setHidden] = useState(true);
  const [reason, setReason] = useState("");
  const [agreed, setAgreed] = useState(false);
  const [phase, setPhase] = useState<Phase>("idle");
  const [target, setTarget] = useState<{ jobId: string; sentAt: number } | null>(null);
  const [message, setMessage] = useState("");

  const load = useCallback(async () => {
    const version = ++generation.current;
    try {
      const next = await getReleaseView();
      if (version !== generation.current) return null;
      setView(next);
      setHidden(false);
      return next;
    } catch {
      if (version !== generation.current) return null;
      // 관리자가 아니거나(403) 근거를 읽지 못하면 그리지 않는다. 안 보여도 새 시작은 서버가 막으므로 해제 가능으로
      // 추정하는 것이 아니다. 재시작 중 실패는 아래 재접속이 다시 읽는다.
      setHidden(true);
      return null;
    }
  }, []);

  useEffect(() => {
    void load();
    return () => {
      generation.current++;
    };
  }, [load]);

  // 막는 작업이 있으면 남은 시간을 주기적으로 다시 읽는다(재시작 중에는 아래 재접속이 맡는다)
  useEffect(() => {
    if (phase !== "idle" || !view?.jobId) return;
    const timer = setInterval(() => void load(), REFRESH_MS);
    return () => clearInterval(timer);
  }, [phase, view?.jobId, load]);

  // 재시작 중: health가 돌아오면 감사 기록을 읽어 결과를 정한다. 경계 확인을 기다리면 계속 본다
  useEffect(() => {
    if ((phase !== "restarting" && phase !== "waiting_boundary") || !target) return;
    let stopped = false;
    const controller = new AbortController();
    const tick = async () => {
      if (stopped) return;
      if (Date.now() - target.sentAt > RECONNECT_LIMIT_MS) {
        setPhase("gave_up");
        return;
      }
      if (await preparationReady(controller.signal)) {
        const next = await load();
        if (stopped) return;
        const rec = next?.record;
        if (rec && rec.jobId === target.jobId && Date.parse(rec.requestedAt) >= target.sentAt - RECONNECT_LIMIT_MS) {
          if (rec.completedAt) {
            setPhase("released");
            setAgreed(false);
            setReason("");
            onReleased?.();
            return;
          }
          setPhase("waiting_boundary");
        } else if (next && phase === "restarting") {
          // 서버가 다시 붙었는데 해제 기록이 없다: 요청이 닿기 전에 끊긴 것이다
          setPhase("idle");
          setMessage("해제가 기록되지 않았어요. 근거를 다시 보고 확인해 주세요.");
          return;
        }
      }
      if (!stopped) setTimeout(() => void tick(), RECONNECT_POLL_MS);
    };
    const first = setTimeout(() => void tick(), RECONNECT_POLL_MS);
    return () => {
      stopped = true;
      controller.abort();
      clearTimeout(first);
    };
  }, [phase, target, load, onReleased]);

  async function release() {
    if (!view?.eligible || !agreed || !validReason(reason) || phase !== "idle") return;
    setPhase("sending");
    setMessage("");
    const sentAt = Date.now();
    try {
      await requestRelease(view.jobId, view.jobRevision, reason);
      setTarget({ jobId: view.jobId, sentAt });
      setPhase("restarting");
    } catch (e) {
      if (mayBeRestarting(e)) {
        // 응답 전에 준비 서비스가 종료했을 수 있다. 다시 붙은 뒤 감사 기록으로 판단한다
        setTarget({ jobId: view.jobId, sentAt });
        setPhase("restarting");
        return;
      }
      setPhase("idle");
      setAgreed(false);
      setMessage(releaseFailureText(e, view.quietSeconds));
      void load();
    }
  }

  if (phase === "restarting" || phase === "waiting_boundary" || phase === "gave_up")
    return (
      <section className={s.isec} aria-label="멈춘 작업 해제">
        <h3>멈춘 작업 해제</h3>
        {phase === "gave_up" ? (
          <p className={s.err} role="alert">
            재시작이 확인되지 않아요. 서버가 다시 떴는지 확인한 뒤 다시 읽어 주세요.
          </p>
        ) : (
          <p className={s.help} role="status" aria-live="polite">
            {phase === "restarting"
              ? "재시작 중이에요. 잠시 뒤 다시 연결해요."
              : "다시 연결했어요. 탐색 경계 세션 없음을 확인하고 있어요."}
          </p>
        )}
        {phase === "gave_up" ? (
          <button
            className={cx(s.btn, s.sm)}
            type="button"
            onClick={() => {
              setPhase(target ? "restarting" : "idle");
              if (target) setTarget({ ...target, sentAt: Date.now() });
            }}
          >
            다시 연결
          </button>
        ) : null}
      </section>
    );

  if (hidden || !view) return null;
  const record = view.record;
  if (!view.jobId) {
    if (phase !== "released" || !record) return null;
    return (
      <section className={s.isec} aria-label="멈춘 작업 해제">
        <p className={s.help} role="status">
          다시 연결했어요. 해제를 마쳤어요(새 init 확인 · 탐색 경계 세션 없음 · {timeText(record.completedAt)}). 결과
          불명 기록은 그대로 남아요.
        </p>
      </section>
    );
  }

  const bytes = reasonBytes(reason.trim());
  const reasonOk = validReason(reason);
  const refusal = view.eligible ? "" : refusalText(view, timeText(view.eligibleAt));
  return (
    <section className={s.isec} aria-label="멈춘 작업 해제">
      <h3>멈춘 작업 해제</h3>
      <p className={s.help}>
        다른 작업이 새 점검 시작을 막고 있어요 · 작업 {view.jobId} · {stateLabel(view.jobState)}
      </p>
      <p className={s.help}>
        마지막 엔진 활동 {timeText(view.lastActivityAt) || "기록 없음"} · 기록 마지막 갱신{" "}
        {timeText(view.receiptUpdatedAt) || "—"} · 기준 {Math.round(view.quietSeconds / 60)}분
      </p>
      {refusal ? <p className={s.help}>{refusal}</p> : null}
      {view.eligible ? (
        <>
          <label className={s.help} htmlFor="stuckReleaseReason">
            해제 사유(감사 기록에 남아요)
          </label>
          <textarea
            id="stuckReleaseReason"
            aria-label="해제 사유"
            value={reason}
            disabled={phase !== "idle"}
            onChange={(e) => setReason(e.target.value)}
            rows={2}
          />
          <p className={s.help}>
            {bytes}/{MAX_REASON_BYTES}바이트
          </p>
          <label className={s.ckline}>
            <input
              type="checkbox"
              aria-label="해제 영향 동의"
              checked={agreed}
              disabled={phase !== "idle"}
              onChange={(e) => setAgreed(e.target.checked)}
            />
            <span>준비 서비스가 다시 시작되고 화면이 잠깐 끊겨요. 결과 불명 기록과 예약은 그대로 남아요.</span>
          </label>
          <div>
            <button
              className={cx(s.btn, s.sm, s.pri)}
              type="button"
              disabled={!agreed || !reasonOk || phase !== "idle"}
              onClick={() => void release()}
            >
              멈춘 작업 해제
            </button>
          </div>
        </>
      ) : null}
      {record?.completedAt ? (
        <p className={s.help}>
          마지막 해제 {timeText(record.completedAt)} · {record.principal} · {record.reason}
        </p>
      ) : null}
      {message ? (
        <p className={s.err} role="alert">
          {message}
        </p>
      ) : null}
    </section>
  );
}
