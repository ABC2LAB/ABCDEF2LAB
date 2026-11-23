"use client";

// 끝난 점검 삭제 버튼 + 확인 상자. 되돌릴 수 없어 한 번 더 묻는다(점검 종료 확인 상자와 같은 모양).
// 서버가 지운 뒤에만 선택을 풀고 목록을 다시 읽는다. 실패하면 상자를 열어 둔 채 이유를 보인다.
// 서버는 점검 결과만 지우고 감사 기록·실행 횟수는 남기므로 확인 문구도 그대로 말한다.

import { useEffect, useRef, useState } from "react";

import type { URLDiagnosisJob } from "@/lib/diagnosis/diagnosis-url";

import { cx } from "../../lib/cx";
import { deleteRunFailureText, deleteURLRun } from "../../live/runDelete";
import { disconnectUrl } from "../../live/urlDiagnosis";
import { loadUrlRuns, useUrlRuns } from "../../live/urlRunList";
import s from "../../workbench.module.css";

export function DeleteRunButton({ job, owner }: { job: URLDiagnosisJob; owner: string }) {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const opener = useRef<HTMLButtonElement>(null);
  const yes = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (open) yes.current?.focus();
  }, [open]);

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
      setError(deleteRunFailureText(e));
      setBusy(false);
      return;
    }
    setBusy(false);
    setOpen(false);
    disconnectUrl();
    void loadUrlRuns(useUrlRuns.getState().owner ?? owner);
  };

  return (
    <>
      <button ref={opener} className={s.btn} type="button" aria-expanded={open} onClick={() => setOpen(true)}>
        삭제
      </button>
      {open ? (
        <div className={s.confirm} role="alertdialog" aria-labelledby="deleteRunH">
          <h2 id="deleteRunH">이 점검을 삭제할까요?</h2>
          <p>점검 결과와 보고서가 지워지고 되돌릴 수 없습니다. 감사 기록과 실행 횟수는 남습니다.</p>
          {error ? (
            <p className={cx(s.note, s.warn)} role="alert">
              {error}
            </p>
          ) : null}
          <div className={s.acts}>
            <button
              ref={yes}
              className={cx(s.btn, s.danger)}
              type="button"
              disabled={busy}
              onClick={() => void remove()}
            >
              삭제
            </button>
            <button className={s.btn} type="button" disabled={busy} onClick={close}>
              취소
            </button>
          </div>
        </div>
      ) : null}
    </>
  );
}
