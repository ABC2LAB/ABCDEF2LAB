import type { CSSProperties, ReactNode } from 'react';
import { SEVERITY_LABEL, VERDICT_LABEL, type Severity, type Verdict } from '../types';

/** <strong style="color:var(--fg)"> — 회색 문장 속 강조 */
export function Strong({ children }: { children: ReactNode }) {
  return <strong style={{ color: 'var(--fg)' }}>{children}</strong>;
}

/** 제목 옆 작은 회색 설명 (font-weight:500; color:muted; 12px) */
export function Sub({ children }: { children: ReactNode }) {
  return <span style={{ fontWeight: 500, color: 'var(--muted)', fontSize: 12 }}>{children}</span>;
}

const PILL_TONE: Record<'muted' | 'warn', CSSProperties> = {
  muted: { background: 'var(--surface-2)', color: 'var(--muted)' },
  warn: { background: 'var(--warn-weak)', color: 'var(--warn)' },
};

/** .pill — tone 없으면 기본(teal) */
export function Pill({ tone, children }: { tone?: 'muted' | 'warn'; children: ReactNode }) {
  return (
    <span className="pill" style={tone ? PILL_TONE[tone] : undefined}>
      {children}
    </span>
  );
}

/** 심각도 배지 (.sev.critical / .high / .medium / .low) */
export function Sev({ level, children }: { level: Severity; children?: ReactNode }) {
  return <span className={`sev ${level}`}>{children ?? SEVERITY_LABEL[level]}</span>;
}

/** 판정 배지 (.vbadge2.ok / .sus / .und) */
export function VerdictBadge({ verdict }: { verdict: Verdict }) {
  return <span className={`vbadge2 ${verdict}`}>{VERDICT_LABEL[verdict]}</span>;
}
