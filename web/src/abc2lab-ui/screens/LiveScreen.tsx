import { useMemo } from "react";
import { Screen } from "../components/Screen";
import { TopBar } from "../components/TopBar";
import { Sev, Strong } from "../components/ui";
import { useApiResource } from "../lib/api";
import { type EngineActivity, type EngineTask, getActivity, getTask } from "../lib/engine";
import { useSelection } from "../lib/selection";
import { useGo } from "../nav";
import type { Severity, Target } from "../types";

const SEV_ORDER: Severity[] = ["critical", "high", "medium", "low"];
const SEV_LABEL: Record<Severity, string> = { critical: "심각", high: "높음", medium: "중간", low: "낮음" };

const STATUS_LABEL: Record<string, string> = {
  created: "대기",
  running: "진행 중",
  paused: "일시중지",
  done: "완료",
  failed: "실패",
  timeout: "시간초과",
};

// 활동 종류(kind) → 라벨·점 색
const KIND: Record<string, { label: string; dot: string }> = {
  round: { label: "라운드", dot: "var(--muted)" },
  text: { label: "메시지", dot: "var(--teal)" },
  result: { label: "결과", dot: "var(--teal-ink)" },
  tool: { label: "도구", dot: "var(--brand)" },
  tool_use: { label: "도구", dot: "var(--brand)" },
  observation: { label: "관찰", dot: "var(--teal)" },
  finding: { label: "발견", dot: "var(--danger)" },
  plan: { label: "계획", dot: "var(--muted)" },
  intent: { label: "의도", dot: "var(--muted)" },
};
function kindInfo(k: string): { label: string; dot: string } {
  return KIND[k] ?? { label: k || "활동", dot: "var(--line-2)" };
}

function tsLabel(ts: number): string {
  if (!ts) return "—";
  const dt = new Date(ts);
  if (Number.isNaN(dt.getTime())) return "—";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(dt.getHours())}:${p(dt.getMinutes())}:${p(dt.getSeconds())}`;
}

const clamp2 = {
  display: "-webkit-box",
  WebkitLineClamp: 2,
  WebkitBoxOrient: "vertical" as const,
  overflow: "hidden",
} as const;

/** ===== 진행/활동 로그 (선택 점검의 실 활동 타임라인) ===== */
export default function LiveScreen({ target }: { target: Target }) {
  const go = useGo();
  const { taskId } = useSelection();

  const { data: task } = useApiResource<EngineTask | null>(
    () => (taskId ? getTask(taskId) : Promise.resolve(null)),
    [taskId],
  );
  const { data: acts, loading, error } = useApiResource<EngineActivity[]>(() => getActivity(taskId ?? ""), [taskId]);

  const rows = useMemo(() => [...(acts ?? [])].sort((a, b) => a.seq - b.seq), [acts]);
  // getActivity 는 앞(시간순 처음)에서 limit 건만 가져온다 → 꽉 차면 뒤가 더 있다는 뜻.
  const truncated = rows.length >= 300;

  const f = task?.findings ?? { critical: 0, high: 0, medium: 0, low: 0 };
  const sevBadges = SEV_ORDER.filter((lvl) => (f[lvl] ?? 0) > 0);
  const title = task?.name?.trim() || target.url || "진행 상황";
  const statusText = task ? (STATUS_LABEL[task.status] ?? task.status) : "";

  if (!taskId) {
    return (
      <Screen id="live">
        <div className="frame">
          <TopBar current="history" />
          <div style={{ textAlign: "center", padding: "64px 24px", color: "var(--muted)" }}>
            <p style={{ marginBottom: 16 }}>활동 로그를 볼 점검을 먼저 골라 주세요.</p>
            <button className="btn primary" type="button" onClick={() => go("history")}>
              점검 기록 열기
            </button>
          </div>
        </div>
      </Screen>
    );
  }

  return (
    <Screen id="live">
      <div className="frame">
        <TopBar current="history" />
        <div style={{ padding: "22px 24px" }}>
          <div className="crumb">
            {/* biome-ignore lint/a11y/useValidAnchor: 원본 시안 crumb 이동(앵커 스타일 유지) */}
            <a onClick={() => go("history")}>점검 기록</a> › <span>활동 로그</span>
          </div>
          <div className="rhead" style={{ marginBottom: 10 }}>
            <div style={{ minWidth: 0 }}>
              <h1 className="big" style={{ marginBottom: 4, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={title}>
                {title}
              </h1>
              <p className="lead" style={{ marginBottom: 0 }}>
                {statusText ? `${statusText} · ` : ""}
                {loading
                  ? "활동 불러오는 중…"
                  : error
                    ? "활동을 불러오지 못했어요."
                    : `활동 ${rows.length}건${truncated ? " · 앞부분만(이후 더 있음)" : ""}`}
              </p>
            </div>
            <div className="row" style={{ flexWrap: "wrap", alignSelf: "center" }}>
              {sevBadges.map((lvl) => (
                <Sev key={lvl} level={lvl}>
                  {SEV_LABEL[lvl]} {f[lvl]}
                </Sev>
              ))}
              {task && (task.status === "done" || task.status === "failed") && (
                <button className="btn" type="button" onClick={() => go("report")}>
                  보고서
                </button>
              )}
            </div>
          </div>

          {error ? (
            <div style={{ textAlign: "center", padding: "32px 0", color: "var(--muted)" }}>
              활동 로그를 불러오지 못했어요. ({error})
            </div>
          ) : !loading && rows.length === 0 ? (
            <div style={{ textAlign: "center", padding: "40px 0", color: "var(--muted)" }}>
              이 점검에는 기록된 활동이 없어요.
            </div>
          ) : (
            <div className="log">
              {rows.map((e) => {
                const ki = kindInfo(e.kind);
                const dot = e.isError ? "var(--danger)" : ki.dot;
                return (
                  <div
                    key={e.seq}
                    className="logrow"
                    style={e.isError ? { background: "var(--danger-weak)" } : undefined}
                  >
                    <span className="ts">{tsLabel(e.ts)}</span>
                    <span style={{ display: "inline-flex", alignItems: "center", gap: 6, flex: "none", width: 92 }}>
                      <span className="dot" style={{ background: dot }} />
                      <span style={{ fontSize: 11.5, color: "var(--muted)" }}>{ki.label}</span>
                    </span>
                    <span className="msg" style={{ minWidth: 0 }}>
                      <span style={clamp2} title={e.summary}>
                        {e.worker ? <span style={{ color: "var(--faint)" }}>{e.worker} · </span> : null}
                        {e.summary || <span style={{ color: "var(--faint)" }}>(요약 없음)</span>}
                      </span>
                    </span>
                  </div>
                );
              })}
            </div>
          )}

          <div className="hint">
            의도(계획)는 플래너만 만들고 워커들이 실행해요. 위는 그 과정을 시간순으로 기록한 활동이에요. 각 줄의 원문
            입력·응답(민감값 포함 가능)은 <Strong>여기 노출하지 않아요.</Strong>
          </div>
        </div>
      </div>
    </Screen>
  );
}
