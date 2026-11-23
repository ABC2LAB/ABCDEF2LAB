"use client";

// 끝난 점검 삭제 버튼 + 확인 상자. 되돌릴 수 없어 한 번 더 묻는다(점검 종료 확인 상자와 같은 모양).
// 버튼과 상자를 따로 돌려줘 상자는 버튼 줄 아래에 둔다(사용자 11/23, 종료 확인 useStopConfirm과 같은 배치).
// 서버가 숨긴 뒤에만 선택을 풀고 목록을 다시 읽는다. 실패하면 상자를 열어 둔 채 이유를 보인다.
// 확인 상자는 질문 한 줄만 둔다(사용자 11/23). 서버는 목록·조회에서만 숨기고 DB·실행 횟수는 그대로 둔다.

import { type ReactNode, useEffect, useRef, useState } from "react";

import type { URLDiagnosisJob } from "@/lib/diagnosis/diagnosis-url";

import { cx } from "../../lib/cx";
import { deleteRunFailureText, deleteRunGone, deleteURLRun } from "../../live/runDelete";
import { disconnectUrl } from "../../live/urlDiagnosis";
import { loadUrlRuns, useUrlRuns } from "../../live/urlRunList";
import s from "../../workbench.module.css";

/** target 이 null 이면(끝나지 않은 점검 등) 버튼·상자 둘 다 없다. */
export function useDeleteRunConfirm(target: { job: URLDiagnosisJob; owner: string } | null): {
  button: ReactNode;
  box: ReactNode;
} {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const opener = useRef<HTMLButtonElement>(null);
  const yes = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (open) yes.current?.focus();
  }, [open]);
  if (!target) return { button: null, box: null };
  const { job, owner } = target;

  const close = () => {
    setOpen(false);
    setError("");
    opener.current?.focus();
  };
  const remove = async () => {
    setBusy(true);
    setError("");
    try {
      await deleteURLRun(job, owner);
    } catch (e) {
      // 이미 숨긴 회차(앞 요청의 응답만 잃은 경우)는 성공과 같이 닫고 목록을 다시 읽는다
      if (!deleteRunGone(e)) {
        setError(deleteRunFailureText(e));
        setBusy(false);
        return;
      }
    }
    setBusy(false);
    setOpen(false);
    disconnectUrl();
    void loadUrlRuns(useUrlRuns.getState().owner ?? owner);
  };

  const button = (
    <button ref={opener} className={s.btn} type="button" aria-expanded={open} onClick={() => setOpen(true)}>
      삭제
    </button>
  );
  const box = open ? (
    <div className={s.confirm} role="alertdialog" aria-labelledby="deleteRunH">
      <h2 id="deleteRunH">이 점검을 삭제할까요?</h2>
      {error ? (
        <p className={cx(s.note, s.warn)} role="alert">
          {error}
        </p>
      ) : null}
      <div className={s.acts}>
        <button ref={yes} className={cx(s.btn, s.danger)} type="button" disabled={busy} onClick={() => void remove()}>
          삭제
        </button>
        <button className={s.btn} type="button" disabled={busy} onClick={close}>
          취소
        </button>
      </div>
    </div>
  ) : null;
  return { button, box };
}
