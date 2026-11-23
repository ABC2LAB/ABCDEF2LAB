"use client";

// 멈춘 작업 해제(C04, 재기동형). 결과 불명·종료 중인 작업이 새 점검 시작을 막고, 엔진 활동과 기록 갱신이 기준 시간
// 넘게 없을 때만 관리자가 사유를 적고 해제한다. 해제하면 준비 서비스가 다시 시작하며 화면 연결이 잠깐 끊긴다. 화면은
// '재시작 중'을 보이고 다시 붙으면 서버 감사 기록으로 결과를 확인한다. 결과 불명 기록은 그대로 남는다.
// 막는 작업이 없거나(not_stuck), 관리자가 아니거나(403), 근거를 읽지 못하면 아무것도 그리지 않는다(새 시작은 서버가 막는다).

import { useCallback, useEffect, useRef, useState } from "react";

import { ApiError } from "@/lib/diagnosis/api";

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

// 근거를 다시 읽는 주기. 막는 작업이 없을 때도 읽는다(화면을 열어 둔 동안 다른 점검이 결과 불명이 될 수 있다)
const REFRESH_MS = 30_000;
// 재시작 중 health를 보는 주기
const RECONNECT_POLL_MS = 2_000;
// 이만큼 지나도 다시 붙지 않으면 서버에서 다시 띄우는 명령을 안내한다(준비 서비스 정상 재시작은 보통 수십 초 안)
const RECOVERY_HINT_MS = 60_000;
// 재시작을 기다리는 시한. 넘으면 기다림을 멈추고 '다시 연결' 버튼을 보인다
const RECONNECT_LIMIT_MS = 10 * 60_000;
export const RECOVERY_COMMAND = "./scripts/product-compose.sh up -d";

type Phase = "idle" | "sending" | "restarting" | "waiting_boundary" | "released" | "gave_up";
// jobId: 해제를 보낸 작업. prevRequestId: 보내기 전 그 작업에 이미 있던 해제 기록(없으면 빈 값). 다시 붙은 뒤 이것과 다른
// 기록이 보이면 이번 요청의 기록이다(서버·브라우저 시계를 비교하지 않는다). since: 이번 기다림의 시작(브라우저 시계, 시한·안내용).
type Target = { jobId: string; prevRequestId: string; since: number };

const timeText = (v: string) => (v ? dateTimeOf(v) || v : "");

export function StuckReleaseBox({ onReleased }: { onReleased?: () => void }) {
  const generation = useRef(0);
  const [view, setView] = useState<ReleaseView | null>(null);
  const [hidden, setHidden] = useState(true);
  const [reason, setReason] = useState("");
  const [agreed, setAgreed] = useState(false);
  const [phase, setPhase] = useState<Phase>("idle");
  const [target, setTarget] = useState<Target | null>(null);
  const [slow, setSlow] = useState(false);
  const [message, setMessage] = useState("");

  const load = useCallback(async () => {
    const version = ++generation.current;
    try {
      const next = await getReleaseView();
      if (version !== generation.current) return null;
      setView(next);
      setHidden(false);
      // 해제를 마친 뒤 다른 작업이 새로 막으면 다시 해제할 수 있게 처음 상태로 돌아간다
      if (next.jobId) setPhase((p) => (p === "released" ? "idle" : p));
      return next;
    } catch {
      if (version !== generation.current) return null;
      // 관리자가 아니거나(403) 근거를 읽지 못하면 그리지 않는다. 안 보여도 새 시작은 서버가 막으므로 해제 가능으로
      // 추정하는 것이 아니다. 주기적으로 다시 읽는다.
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

  // 재시작을 기다리지 않을 때는 늘 주기적으로 다시 읽는다(막는 작업 없음·첫 조회 실패에서도)
  useEffect(() => {
    if (phase !== "idle" && phase !== "released") return;
    const timer = setInterval(() => void load(), REFRESH_MS);
    return () => clearInterval(timer);
  }, [phase, load]);

  // 재시작 중: health가 돌아오면 감사 기록으로 결과를 정한다. 경계 확인을 기다리면 계속 본다
  useEffect(() => {
    if ((phase !== "restarting" && phase !== "waiting_boundary") || !target) return;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const controller = new AbortController();
    const tick = async () => {
      if (stopped) return;
      const waited = Date.now() - target.since;
      if (phase === "restarting") {
        setSlow(waited > RECOVERY_HINT_MS);
        if (waited > RECONNECT_LIMIT_MS) {
          setPhase("gave_up");
          return;
        }
      }
      if (await preparationReady(controller.signal)) {
        const next = await load();
        if (stopped) return;
        const rec = next?.record;
        if (rec && rec.jobId === target.jobId && rec.requestId !== target.prevRequestId) {
          if (rec.completedAt) {
            setPhase("released");
            setSlow(false);
            setAgreed(false);
            setReason("");
            onReleased?.();
            return;
          }
          setSlow(false);
          setPhase("waiting_boundary");
        } else if (next && phase === "restarting") {
          // 서버가 다시 붙었는데 이번 해제 기록이 없다: 요청이 닿기 전에 끊긴 것이다
          setPhase("idle");
          setSlow(false);
          setMessage("해제가 기록되지 않았어요. 근거를 다시 보고 확인해 주세요.");
          return;
        }
      }
      if (!stopped) timer = setTimeout(() => void tick(), RECONNECT_POLL_MS);
    };
    timer = setTimeout(() => void tick(), RECONNECT_POLL_MS);
    return () => {
      stopped = true;
      controller.abort();
      clearTimeout(timer);
    };
  }, [phase, target, load, onReleased]);

  async function release() {
    if (!view?.eligible || !agreed || !validReason(reason) || phase !== "idle") return;
    setPhase("sending");
    setMessage("");
    const prevRequestId = view.record?.jobId === view.jobId ? view.record.requestId : "";
    const waitFor = { jobId: view.jobId, prevRequestId, since: Date.now() };
    try {
      await requestRelease(view.jobId, view.jobRevision, reason);
      setTarget(waitFor);
      setPhase("restarting");
    } catch (e) {
      // 응답 전에 준비 서비스가 종료했거나(연결 끊김), 앞서 보낸 같은 요청이 이미 기록됐으면(release_pending_restart)
      // 다시 붙은 뒤 감사 기록으로 판단한다
      if (
        mayBeRestarting(e) ||
        (e instanceof ApiError && e.status === 409 && e.message === "release_pending_restart")
      ) {
        setTarget(waitFor);
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
            재시작이 확인되지 않아요. 서버에서 <code>{RECOVERY_COMMAND}</code>로 다시 띄운 뒤 다시 연결해 주세요.
          </p>
        ) : (
          <p className={s.help} role="status" aria-live="polite">
            {phase === "restarting"
              ? "재시작 중이에요. 잠시 뒤 다시 연결해요."
              : "재시작은 확인했어요. 탐색 경계 세션 없음을 확인하고 있어요."}
          </p>
        )}
        {phase === "restarting" && slow ? (
          <p className={s.err} role="alert">
            1분이 지나도 다시 붙지 않아요. 서버에서 <code>{RECOVERY_COMMAND}</code>를 실행해 주세요(기록·볼륨은
            그대로예요).
          </p>
        ) : null}
        {phase === "gave_up" ? (
          <button
            className={cx(s.btn, s.sm)}
            type="button"
            onClick={() => {
              // 기다림만 다시 시작한다. 이번 요청을 알아볼 기준(prevRequestId)은 그대로 둔다
              if (target) setTarget({ ...target, since: Date.now() });
              setPhase(target ? "restarting" : "idle");
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
