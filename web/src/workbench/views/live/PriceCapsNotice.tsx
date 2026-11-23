"use client";

// 끝난 점검의 단가 상한 안내(제품 설치 v4 점검, 회차 기록 model.price_caps). 오늘(11/23) 같은 '결과 불명'이 단가 상한
// 때문이면 어느 제공자가 왜(단가 > 상한) 빠졌는지 여기서 바로 보인다. 회차 기록은 점검이 끝나야 저장되므로 끝난 점검에서만
// 결과 저장이 확인된 뒤(ready) 읽는다. 기록이 없거나(v1~v3 · 재조회 전 · 저장 없음) 모양이 어긋나면 아무것도 그리지 않는다. 문구 규칙은
// lib/diagnosis/price-caps.ts가 정하고 화면은 그리기만 한다.

import { useEffect, useState } from "react";

import { type PriceCapsNote, parsePriceCaps, priceCapsNotes } from "@/lib/diagnosis/price-caps";
import { readDiagnosisRun } from "@/lib/diagnosis/request-approvals";

import { cx } from "../../lib/cx";
import { Icon } from "../../ui/icons";
import s from "../../workbench.module.css";

// ready는 결과 저장이 확인됐는지다(useUrlGates().reportReadable). 점검이 끝났다는 신호가 결과 저장보다 먼저 올 수
// 있어서, 저장 확인 전에 읽으면 404로 빈 안내를 잡고 다시 읽지 않는다. 그래서 ready가 참이 된 뒤에만 읽고, 바뀌면
// 다시 읽는다(#889 리뷰).
export function PriceCapsNotice({ taskId, ready }: { taskId: string; ready: boolean }) {
  const [notes, setNotes] = useState<{ task: string; notes: PriceCapsNote[] } | null>(null);
  useEffect(() => {
    if (!ready) return;
    let live = true;
    readDiagnosisRun(taskId)
      .then((record) => {
        const caps = parsePriceCaps(record.model);
        if (live) setNotes({ task: taskId, notes: caps ? priceCapsNotes(caps) : [] });
      })
      .catch(() => {
        // 저장 없음(404) · 다른 작업(403) · 조회 실패는 안내할 근거가 없어 그리지 않는다.
        if (live) setNotes({ task: taskId, notes: [] });
      });
    return () => {
      live = false;
    };
  }, [taskId, ready]);
  if (!ready || !notes || notes.task !== taskId || notes.notes.length === 0) return null;
  return (
    <section aria-label="단가 상한">
      {notes.notes.map((n) =>
        n.tone === "warn" ? (
          <div key={n.text} className={cx(s.note, s.warn)} role="note">
            <Icon name="alert" />
            <span>{n.text}</span>
          </div>
        ) : (
          <p key={n.text} className={s.help}>
            {n.text}
          </p>
        ),
      )}
    </section>
  );
}
