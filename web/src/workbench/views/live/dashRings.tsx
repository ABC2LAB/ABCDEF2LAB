"use client";

// 대시보드 요약 숫자 칸 아래 원형 게이지 줄(사용자 11/24: '말만 대시보드지 보고서 같다' → 숫자는 그대로, 그 밑에 원형 4개).
// 모두 서버 값만 쓴다 — 없는 값은 만들지 않고 '—'와 이유를 둔다(규칙 5). 색만으로 구분하지 않게 원마다 이름 · 수 범례를
// 붙이고, 조각마다 마우스를 올리면 이름 · 수 · 비율이 뜬다(SVG title). 색은 dataviz 검증기로 밝은 · 다크 화면을 따로 확인했다.
//   ① 위험도: 확정된 것만(diagnostic-candidate-severity 계약). 후보의 예상 등급은 넣지 않는다
//   ② 판정: 확정 · 검토 필요 · 판단 불가(결과 불명 + 미실행)
//   ③ 탐색: 탐색이 찾은 API 중 방문한 비율(탐색 흐름 counts — 끝난 점검도 읽힌다)
//   ④ 전송 통제: E② 가 받은 진단 요청 중 허용 · 막음 · 결과 불명(e2-flow counts). 승인 대기가 있으면 노란 줄로 알린다

import type { ReactNode } from "react";

import { cx } from "../../lib/cx";
import s from "../../workbench.module.css";

export type RingSeg = { key: string; label: string; value: number; color: string };

/** 링 지름 · 두께(px). 가운데 큰 수가 들어가도록 */
const SIZE = 104;
const STROKE = 12;
/** 조각 사이 틈(px): 붙은 조각이 한 덩어리로 보이지 않게 */
const GAP = 2;

/** 고리 하나: 바탕 + 조각(값이 0 인 것은 그리지 않음). total 이 0 이면 바탕만. 조각마다 이름 · 수 · 비율 툴팁 */
function Arcs({ segs, total, r, stroke }: { segs: readonly RingSeg[]; total: number; r: number; stroke: number }) {
  const c = 2 * Math.PI * r;
  const mid = SIZE / 2;
  const shown = segs.filter((g) => g.value > 0);
  const gap = shown.length > 1 ? GAP : 0;
  let offset = 0;
  const arcs =
    total > 0
      ? shown.map((g) => {
          const len = Math.max(0, (c * g.value) / total - gap);
          const el = (
            <circle
              key={g.key}
              cx={mid}
              cy={mid}
              r={r}
              fill="none"
              stroke={g.color}
              strokeWidth={stroke}
              strokeDasharray={`${len} ${c - len}`}
              strokeDashoffset={-offset}
              transform={`rotate(-90 ${mid} ${mid})`}
            >
              <title>{`${g.label} ${g.value} (${Math.round((g.value / total) * 100)}%)`}</title>
            </circle>
          );
          offset += (c * g.value) / total;
          return el;
        })
      : null;
  return (
    <>
      <circle cx={mid} cy={mid} r={r} fill="none" stroke="var(--ring-track)" strokeWidth={stroke} />
      {arcs}
    </>
  );
}

/** 원 하나: 고리 + 가운데 글 */
export function Ring({
  segs,
  total,
  center,
  caption,
  label,
}: {
  segs: readonly RingSeg[];
  /** 고리 전체가 뜻하는 수(조각 합보다 클 수 있다 — 예: 탐색은 방문 / 발견) */
  total: number;
  center: ReactNode;
  caption: string;
  /** 읽기 프로그램용 한 줄 요약 */
  label: string;
}) {
  return (
    <div className={s["dring-ring"]}>
      <svg width={SIZE} height={SIZE} viewBox={`0 0 ${SIZE} ${SIZE}`} role="img" aria-label={label}>
        <Arcs segs={segs} total={total} r={(SIZE - STROKE) / 2} stroke={STROKE} />
      </svg>
      <div className={s["dring-c"]} aria-hidden="true">
        <b>{center}</b>
        <span>{caption}</span>
      </div>
    </div>
  );
}

/** 원 카드: 제목 · 원 · 범례(색 칸 + 이름 + 수). 범례 글은 글자색, 색은 칸에만 */
export function RingCard({
  title,
  ring,
  legend,
  note,
  warn = false,
}: {
  title: string;
  ring: ReactNode;
  legend: ReadonlyArray<{ key: string; label: string; value: ReactNode; color?: string }>;
  /** 아래 작은 글(이유 · 보조 수) */
  note?: ReactNode;
  /** 사람이 할 일이 있으면 카드 테두리를 노랗게 */
  warn?: boolean;
}) {
  return (
    <section className={cx(s.dring, warn && s.warn)} aria-label={title}>
      <h3>{title}</h3>
      <div className={s["dring-b"]}>
        {ring}
        <ul className={s["dring-l"]}>
          {legend.map((l) => (
            <li key={l.key}>
              <i className={s["dring-sw"]} style={l.color ? { background: l.color } : undefined} aria-hidden="true" />
              <span>{l.label}</span>
              <b>{l.value}</b>
            </li>
          ))}
        </ul>
      </div>
      {note !== undefined ? <p className={s["dring-n"]}>{note}</p> : null}
    </section>
  );
}
