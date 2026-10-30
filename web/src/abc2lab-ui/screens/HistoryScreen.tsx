import { type ReactNode, useMemo, useState } from "react";

import { Screen } from "../components/Screen";
import { TopBar } from "../components/TopBar";
import { Pill, Sev, Strong } from "../components/ui";
import { useApiResource } from "../lib/api";
import { type EngineTask, listTasks } from "../lib/engine";
import { useSelection } from "../lib/selection";
import { useGo } from "../nav";
import type { Severity } from "../types";

const btnSm = { padding: "9px 15px", fontSize: 13 } as const;
// 긴 이름/설명이 카드를 밀어내지 않게 한 줄로 자르고 … 처리 (부모가 minWidth:0 이라야 동작)
const oneLine = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" } as const;

const STATUS_LABEL: Record<string, string> = {
  created: "대기",
  queued: "실행 대기",
  running: "진행 중",
  paused: "일시중지",
  done: "완료",
  failed: "실패",
  timeout: "시간초과",
};

const SORTS = [
  { key: "new", label: "최신순" },
  { key: "old", label: "오래된순" },
] as const;
type SortKey = (typeof SORTS)[number]["key"];

const SEV_ORDER: Severity[] = ["critical", "high", "medium", "low"];
const SEV_COUNT_LABEL: Record<Severity, string> = {
  critical: "심각",
  high: "높음",
  medium: "중간",
  low: "낮음",
};

function isRunning(status: string): boolean {
  return status === "running" || status === "paused" || status === "created" || status === "queued";
}
function isTerminal(status: string): boolean {
  return status === "done" || status === "failed" || status === "timeout";
}

/** created_at(RFC3339) → 그룹 키(YYYY-MM-DD)와 표시용 날짜/시각 */
function dayKey(iso: string): string {
  return iso ? iso.slice(0, 10) : "";
}
function dayLabel(key: string): string {
  if (!key) return "날짜 미상";
  const today = new Date().toISOString().slice(0, 10);
  if (key === today) return "오늘";
  const [, m, d] = key.split("-");
  return `${Number(m)}/${Number(d)}`;
}
function timeLabel(iso: string): string {
  if (!iso) return "";
  const dt = new Date(iso);
  if (Number.isNaN(dt.getTime())) return "";
  return `${String(dt.getHours()).padStart(2, "0")}:${String(dt.getMinutes()).padStart(2, "0")}`;
}

/** 한 점검(task) 카드 */
function TaskCard({ task, onOpen }: { task: EngineTask; onOpen: (s: "live" | "report") => void }) {
  const f = task.findings;
  const badges = SEV_ORDER.filter((lvl) => f[lvl] > 0);
  const terminal = isTerminal(task.status);
  const hasFindings = badges.length > 0;
  // 종료된 점검이거나 이미 발견이 쌓인 점검이면 상태와 무관하게 보고서를 볼 수 있다
  // 일시중지/진행 중이어도 지금까지의 결과를 열람한다.
  const canReport = terminal || hasFindings;
  const name = task.name.trim();
  const title = name || "이름 없는 점검";
  // 이름이 비어 있을 때만 설명(대상 프롬프트 등)을 한 줄로 잘라 부제로 — 긴 문단이 제목을 먹지 않게.
  const subtitle = name ? undefined : task.description.trim();

  const parts = [timeLabel(task.created_at), STATUS_LABEL[task.status] ?? task.status];
  if (task.goals_total > 0) parts.push(`목표 ${task.goals_met}/${task.goals_total}`);

  let findingsBadge: ReactNode;
  if (badges.length > 0) {
    findingsBadge = badges.map((lvl) => (
      <Sev key={lvl} level={lvl}>
        {SEV_COUNT_LABEL[lvl]} {f[lvl]}
      </Sev>
    ));
  } else if (task.status === "failed" || task.status === "timeout") {
    findingsBadge = <Pill tone="warn">{STATUS_LABEL[task.status]} · 미완료</Pill>;
  } else if (task.status === "done") {
    findingsBadge = <Pill tone="muted">발견 없음</Pill>;
  } else {
    findingsBadge = null;
  }

  return (
    <div className="hcard">
      <div style={{ minWidth: 0 }}>
        <div className="nm" style={oneLine} title={title}>
          {title}
        </div>
        {subtitle && (
          <div className="meta" style={oneLine} title={subtitle}>
            {subtitle}
          </div>
        )}
        <div className="meta">{parts.filter(Boolean).join(" · ")}</div>
      </div>
      <div className="acts">
        {findingsBadge}
        <button
          className={canReport ? "btn" : "btn primary"}
          type="button"
          onClick={() => onOpen(canReport ? "report" : "live")}
          style={btnSm}
        >
          {canReport ? "보고서" : "이어 보기"}
        </button>
      </div>
    </div>
  );
}

/** ===== 점검 기록 ===== */
export default function HistoryScreen() {
  const go = useGo();
  const { selectTask } = useSelection();
  const { data, loading, error, reload } = useApiResource(listTasks);
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState<SortKey>("new");

  const tasks = data?.tasks ?? [];

  const counts = useMemo(
    () => ({
      all: tasks.length,
      running: tasks.filter((t) => isRunning(t.status)).length,
      done: tasks.filter((t) => t.status === "done").length,
      failed: tasks.filter((t) => t.status === "failed" || t.status === "timeout").length,
    }),
    [tasks],
  );

  const groups = useMemo(() => {
    const q = query.trim().toLowerCase();
    const filtered = tasks.filter((t) => {
      if (!q) return true;
      return (t.name || t.description || "").toLowerCase().includes(q);
    });
    filtered.sort((a, b) =>
      sort === "new" ? b.created_at.localeCompare(a.created_at) : a.created_at.localeCompare(b.created_at),
    );
    const byDay = new Map<string, EngineTask[]>();
    for (const t of filtered) {
      const k = dayKey(t.created_at);
      const arr = byDay.get(k);
      if (arr) arr.push(t);
      else byDay.set(k, [t]);
    }
    return [...byDay.entries()];
  }, [tasks, query, sort]);

  let leadContent: ReactNode;
  if (loading) {
    leadContent = "불러오는 중…";
  } else if (error) {
    leadContent = "기록을 불러오지 못했어요.";
  } else {
    leadContent = (
      <>
        지금까지 <Strong>{counts.all}번</Strong> 점검했어요. 각 점검의 보고서는 카드에서 바로 열어요.
      </>
    );
  }

  let historyContent: ReactNode;
  if (error) {
    historyContent = (
      <div className="hcard" style={{ borderColor: "var(--danger-border)", background: "var(--danger-weak)" }}>
        <div style={{ minWidth: 0 }}>
          <div className="nm">불러오기 실패</div>
          <div className="meta">{error}</div>
        </div>
        <div className="acts">
          <button className="btn" type="button" onClick={reload} style={btnSm}>
            다시 시도
          </button>
        </div>
      </div>
    );
  } else if (!loading && tasks.length === 0) {
    historyContent = (
      <div style={{ textAlign: "center", padding: "48px 0", color: "var(--muted)" }}>
        <p style={{ marginBottom: 16 }}>아직 점검 기록이 없어요.</p>
        <button className="btn primary" type="button" onClick={() => go("entry")}>
          ＋ 첫 점검 시작
        </button>
      </div>
    );
  } else {
    historyContent = (
      <>
        {counts.all > 0 && (
          <div className="search" style={{ gap: 8, marginTop: 4 }}>
            <Pill tone="muted">전체 {counts.all}</Pill>
            {counts.running > 0 && <Pill>진행·대기 {counts.running}</Pill>}
            {counts.done > 0 && <Pill tone="muted">완료 {counts.done}</Pill>}
            {counts.failed > 0 && <Pill tone="warn">미완료 {counts.failed}</Pill>}
          </div>
        )}
        {groups.map(([key, items]) => (
          <div key={key || "unknown"}>
            <div className="daylab">{dayLabel(key)}</div>
            {items.map((t) => (
              <TaskCard
                key={t.id}
                task={t}
                onOpen={(s) => {
                  selectTask(t.id);
                  go(s);
                }}
              />
            ))}
          </div>
        ))}
        {!loading && groups.length === 0 && query && (
          <div style={{ textAlign: "center", padding: "32px 0", color: "var(--muted)" }}>
            “{query}” 에 맞는 기록이 없어요.
          </div>
        )}
      </>
    );
  }

  return (
    <Screen id="history">
      <div className="frame">
        <TopBar current="history" />
        <div style={{ padding: "22px 24px" }}>
          <h1 className="big">점검 기록</h1>
          <p className="lead">{leadContent}</p>

          <div className="search">
            <input
              className="inp"
              placeholder="기록 검색"
              style={{ flex: "1 1 220px" }}
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              aria-label="기록 검색"
            />
            <select
              className="inp"
              style={{ flex: "0 1 160px" }}
              value={sort}
              onChange={(e) => setSort(e.target.value as SortKey)}
              aria-label="정렬"
            >
              {SORTS.map((s) => (
                <option key={s.key} value={s.key}>
                  {s.label}
                </option>
              ))}
            </select>
            <button className="btn primary" type="button" onClick={() => go("entry")}>
              ＋ 새 점검
            </button>
          </div>

          {historyContent}
        </div>
      </div>
    </Screen>
  );
}
