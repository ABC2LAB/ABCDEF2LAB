"use client";

// 대상 리셋 확인 (E③ 결정 §6.1). 확정 단계가 대상에 시험 객체를 남겼으면 운영자가 대상을 리셋했다고 확인해야 다음 확정
// 점검이 시작된다. 쓰기 기록이 없으면 아무것도 그리지 않는다. 진술 문장은 서버 것을 그대로 보이고, 동의는 지금 보이는 쓰기
// 기록 줄(last_writes_ref)에 묶는다 — 그 사이 기록이 바뀌면 서버가 거절하고 화면은 새 기록을 다시 읽는다.

import { useCallback, useEffect, useRef, useState } from "react";

import { cx } from "../../lib/cx";
import { dateTimeOf } from "../../live/map";
import { confirmReset, getResetState, type ResetState, resetFailureText } from "../../live/resetConfirmation";
import s from "../../workbench.module.css";

export function ResetConfirmationBox({
  targetId,
  disabled,
  onConfirmed,
  onState,
}: {
  targetId: string;
  disabled: boolean;
  onConfirmed?: () => void;
  // 읽은 리셋 상태(모르면 null). 시작 화면 요약이 '리셋 확인 전' 경고를 붙일 때 쓴다
  onState?: (state: ResetState | null) => void;
}) {
  const generation = useRef(0);
  // 부모가 매번 새 함수를 넘겨도 다시 읽지 않게 ref 로 둔다
  const reportState = useRef(onState);
  reportState.current = onState;
  const [state, setState] = useState<ResetState | null>(null);
  const [failed, setFailed] = useState(false);
  const [agreed, setAgreed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");

  const load = useCallback(async () => {
    const version = ++generation.current;
    try {
      const next = await getResetState(targetId);
      if (version !== generation.current) return;
      setState(next);
      setFailed(false);
      reportState.current?.(next);
    } catch {
      if (version !== generation.current) return;
      // 상태를 모르면 '리셋 필요 없음'으로 추정하지 않는다
      setState(null);
      setFailed(true);
      reportState.current?.(null);
    } finally {
      // 확인 POST 실패 뒤 다시 읽는 동안 잠근 입력은 최신 상태 읽기가 끝나야 다시 연다.
      if (version === generation.current) setBusy(false);
    }
  }, [targetId]);

  useEffect(() => {
    setState(null);
    setFailed(false);
    setAgreed(false);
    setMessage("");
    void load();
    return () => {
      generation.current++;
    };
  }, [load]);

  async function confirm() {
    if (!state?.resetRequired || !agreed || busy || disabled) return;
    setBusy(true);
    setMessage("");
    const version = ++generation.current;
    try {
      const next = await confirmReset(targetId, state.lastWritesRef);
      if (version !== generation.current) return;
      setState(next);
      setAgreed(false);
      reportState.current?.(next);
      onConfirmed?.();
    } catch (e) {
      if (version !== generation.current) return;
      setMessage(resetFailureText(e));
      setAgreed(false);
      // 기록이 바뀌었을 수 있으니 지금 기록을 다시 읽는다
      void load();
    } finally {
      if (version === generation.current) setBusy(false);
    }
  }

  if (failed)
    return (
      <section className={s.isec} aria-label="대상 리셋 확인">
        <p className={s.err} role="alert">
          리셋 확인 상태를 읽지 못했습니다. 확정 단계를 돌린 대상이면 다음 확정 점검이 시작되지 않을 수 있습니다.
        </p>
        <button className={cx(s.btn, s.sm)} type="button" onClick={() => void load()}>
          다시 읽기
        </button>
      </section>
    );
  if (!state || state.lastWritesJob === "") return null;

  const when = dateTimeOf(state.lastWritesAt) || state.lastWritesAt;
  if (!state.resetRequired)
    return (
      <section className={s.isec} aria-label="대상 리셋 확인">
        <p className={s.help}>
          리셋 확인됨 · {dateTimeOf(state.confirmedAt) || state.confirmedAt} · 확정 단계 시험 객체{" "}
          {state.lastWritesObjects}개({when} 점검)
        </p>
      </section>
    );

  return (
    <section className={s.isec} aria-label="대상 리셋 확인">
      <h3>대상 리셋 확인 필요</h3>
      <p className={s.help}>
        확정 단계가 이 대상에 시험 객체 {state.lastWritesObjects}개를 남겼습니다({when} 점검). 대상을 리셋하고 확인해야
        다음 확정 점검이 시작됩니다.
      </p>
      <label className={s.ckline}>
        <input
          type="checkbox"
          aria-label="리셋 진술 동의"
          checked={agreed}
          disabled={disabled || busy}
          onChange={(e) => setAgreed(e.target.checked)}
        />
        <span>{state.statement}</span>
      </label>
      <div>
        <button
          className={cx(s.btn, s.sm, s.pri)}
          type="button"
          disabled={!agreed || disabled || busy}
          onClick={() => void confirm()}
        >
          리셋 확인
        </button>
      </div>
      {message ? (
        <p className={s.err} role="alert">
          {message}
        </p>
      ) : null}
    </section>
  );
}
