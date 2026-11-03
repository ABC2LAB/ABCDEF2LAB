import type { CSSProperties, ReactNode } from "react";

import { SEVERITY_LABEL, type Severity, VERDICT_LABEL, type Verdict } from "../types";

/** <strong style="color:var(--fg)"> — 회색 문장 속 강조 */
export function Strong({ children }: { children: ReactNode }) {
  return <strong style={{ color: "var(--fg)" }}>{children}</strong>;
}

/** 제목 옆 작은 회색 설명 (font-weight:500; color:muted; 12px) */
export function Sub({ children }: { children: ReactNode }) {
  return <span style={{ fontWeight: 500, color: "var(--muted)", fontSize: 13 }}>{children}</span>;
}

const PILL_TONE: Record<"muted" | "warn", CSSProperties> = {
  muted: { background: "var(--surface-2)", color: "var(--muted)" },
  warn: { background: "var(--warn-weak)", color: "var(--warn)" },
};

/** .pill — tone 없으면 기본(teal) */
export function Pill({ tone, children }: { tone?: "muted" | "warn"; children: ReactNode }) {
  return (
    <span className="pill" style={tone ? PILL_TONE[tone] : undefined}>
      {children}
    </span>
  );
}

/** 심각도 배지 (.sev.critical / .high / .medium / .low) */
export function Sev({ level, children }: { level: Severity | undefined; children?: ReactNode }) {
  return (
    <span className={`sev ${level ?? "unknown"}`} style={level ? undefined : PILL_TONE.muted}>
      {children ?? (level ? SEVERITY_LABEL[level] : "미분류")}
    </span>
  );
}

/** 판정 배지 (.vbadge2.ok / .sus / .und) */
export function VerdictBadge({ verdict }: { verdict: Verdict }) {
  return <span className={`vbadge2 ${verdict}`}>{VERDICT_LABEL[verdict]}</span>;
}

/** 목업(시안) 화면임을 알리는 배너 — 실데이터·실동작이 아님을 분명히 한다 */
export function MockBanner({ children }: { children?: ReactNode }) {
  return (
    <div
      style={{
        fontSize: 13,
        color: "var(--warn)",
        background: "var(--warn-weak)",
        border: "1px solid var(--warn-border)",
        borderRadius: 6,
        padding: "9px 12px",
        marginBottom: 14,
      }}
    >
      <strong style={{ color: "var(--warn)" }}>시안(목업)</strong> —{" "}
      {children ?? "디자인 예시 화면이에요. 이 화면의 숫자·내용은 실데이터·실동작이 아니에요."}
    </div>
  );
}
