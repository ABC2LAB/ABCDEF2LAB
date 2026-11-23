"use client";

// 끝난 점검 '삭제' 버튼 + 확인 상자(2026-11-23 사용자·팀 결정, #867). 화면 글은 '삭제'지만 동작은 화면 목록에서만 빼는 것이고
// 서버·DB 기록은 건드리지 않는다(live/hiddenRuns). '삭제한 점검'에서 다시 열면 '목록에 다시 보이기'로 되돌린다.
// 버튼과 상자를 따로 돌려줘 상자는 버튼 줄 아래에 둔다(점검 종료 확인 useStopConfirm과 같은 배치). 처음 버튼은 상자를 켜고
// 끈다. 상자는 질문 한 줄만 둔다(사용자 11/23).

import { type ReactNode, useEffect, useRef, useState } from "react";

import type { URLDiagnosisJob } from "@/lib/diagnosis/diagnosis-url";

import { cx } from "../../lib/cx";
import { hideRun, showRun, useHiddenRunIDs } from "../../live/hiddenRuns";
import { disconnectUrl } from "../../live/urlDiagnosis";
import s from "../../workbench.module.css";

/** target 이 null 이면(끝나지 않은 점검 등) 버튼·상자 둘 다 없다. */
export function useHideRunConfirm(target: { job: URLDiagnosisJob; owner: string } | null): {
  button: ReactNode;
  box: ReactNode;
} {
  const [open, setOpen] = useState(false);
  const opener = useRef<HTMLButtonElement>(null);
  const yes = useRef<HTMLButtonElement>(null);
  const hidden = useHiddenRunIDs(target ? target.owner : null);
  useEffect(() => {
    if (open) yes.current?.focus();
  }, [open]);
  if (!target) return { button: null, box: null };
  const { job, owner } = target;

  if (hidden.includes(job.id))
    return {
      button: (
        <button className={s.btn} type="button" onClick={() => showRun(owner, job.id)}>
          목록에 다시 보이기
        </button>
      ),
      box: null,
    };

  const close = () => {
    setOpen(false);
    opener.current?.focus();
  };
  const button = (
    <button
      ref={opener}
      className={s.btn}
      type="button"
      aria-expanded={open}
      onClick={() => (open ? close() : setOpen(true))}
    >
      삭제
    </button>
  );
  const box = open ? (
    <div className={s.confirm} role="alertdialog" aria-labelledby="hideRunH">
      <h2 id="hideRunH">이 점검을 삭제할까요?</h2>
      <div className={s.acts}>
        <button
          ref={yes}
          className={cx(s.btn, s.danger)}
          type="button"
          onClick={() => {
            setOpen(false);
            hideRun(owner, job.id);
            // 삭제한 점검은 기본 목록에서 빠지므로 이어 보기도 푼다(기록은 '삭제한 점검'에서 다시 연다)
            disconnectUrl();
          }}
        >
          삭제
        </button>
        <button className={s.btn} type="button" onClick={close}>
          취소
        </button>
      </div>
    </div>
  ) : null;
  return { button, box };
}
