import { useMemo, useState } from "react";
import { Screen } from "../components/Screen";
import { Tabs } from "../components/Tabs";
import { TopBar } from "../components/TopBar";
import { Strong } from "../components/ui";
import { useApiResource } from "../lib/api";
import { type EngineAuditEntry, getAudit } from "../lib/engine";
import { useSelection } from "../lib/selection";
import { useGo } from "../nav";

type Who = "sys" | "agent" | "op";
const WHO_LABEL: Record<Who, string> = { sys: "시스템", agent: "에이전트", op: "사람" };

type FilterKey = "all" | "agent" | "human" | "system";
const FILTER_WHO: Record<Exclude<FilterKey, "all">, Who> = { agent: "agent", human: "op", system: "sys" };

/** E² 게이트 로그는 에이전트 도구 호출. 차단은 통제(시스템)가 막은 것 → who 파생 */
function entryWho(e: EngineAuditEntry): Who {
  return e.action === "block" ? "sys" : "agent";
}

/** unix epoch(초 또는 ms) → HH:MM:SS */
function tsLabel(ts: number): string {
  if (!ts) return "—";
  const ms = ts < 1e12 ? ts * 1000 : ts;
  const dt = new Date(ms);
  if (Number.isNaN(dt.getTime())) return "—";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(dt.getHours())}:${p(dt.getMinutes())}:${p(dt.getSeconds())}`;
}

const allowed = { color: "var(--teal-ink)" } as const;
const blocked = { color: "var(--warn)" } as const;
const noticeBox = {
  fontSize: 12.5,
  color: "var(--muted)",
  background: "var(--surface-2)",
  border: "1px dashed var(--line-2)",
  borderRadius: 8,
  padding: "10px 12px",
  marginBottom: 14,
} as const;

/** ===== 감사 로그 ===== */
export default function AuditScreen() {
  const go = useGo();
  const { taskId } = useSelection();
  const [filter, setFilter] = useState<FilterKey>("all");

  const { data, loading, error } = useApiResource(() => getAudit(taskId ?? ""), [taskId]);
  const entries = useMemo(() => data?.entries ?? [], [data]);

  const counts = useMemo(() => {
    let agent = 0;
    let sys = 0;
    for (const e of entries) {
      if (entryWho(e) === "sys") sys += 1;
      else agent += 1;
    }
    return { all: entries.length, agent, system: sys, human: 0 };
  }, [entries]);

  const rows = useMemo(() => {
    if (filter === "all") return entries;
    const who = FILTER_WHO[filter];
    return entries.filter((e) => entryWho(e) === who);
  }, [entries, filter]);

  if (!taskId) {
    return (
      <Screen id="audit">
        <div className="frame">
          <TopBar current="settings" />
          <div style={{ textAlign: "center", padding: "64px 24px", color: "var(--muted)" }}>
            <p style={{ marginBottom: 16 }}>감사 로그를 볼 점검을 먼저 골라 주세요.</p>
            <button className="btn primary" type="button" onClick={() => go("history")}>
              점검 기록 열기
            </button>
          </div>
        </div>
      </Screen>
    );
  }

  return (
    <Screen id="audit">
      <div className="frame">
        <TopBar current="settings" />
        <div style={{ padding: "22px 24px" }}>
          <div className="crumb">
            {/* biome-ignore lint/a11y/useValidAnchor: 원본 시안 crumb 이동(앵커 스타일 유지) */}
            <a onClick={() => go("settings")}>설정</a> › <span>감사 로그</span>
          </div>
          <div className="rhead" style={{ marginBottom: 14 }}>
            <div>
              <h1 className="big" style={{ marginBottom: 4 }}>
                감사 로그
              </h1>
              <p className="lead" style={{ marginBottom: 0 }}>
                {loading ? "불러오는 중…" : error ? "불러오지 못했어요." : `E² 게이트 기록 ${entries.length}건`}
              </p>
            </div>
            {/* 변조탐지 해시체인은 우리 기여(미구현) — "무결"을 단정하지 않는다 */}
            <span className="badge" style={{ alignSelf: "center", color: "var(--muted)" }}>
              변조 탐지 준비 중
            </span>
          </div>

          <div style={noticeBox}>
            아래는 전송 통제(E²)가 게이트한 요청 기록이에요. <Strong>변조 탐지 해시체인</Strong>과{" "}
            <Strong>로그 권한 분리</Strong>는 우리가 얹는 기여로, 연동되면 각 줄에 체인 해시가 붙어요.
          </div>

          <Tabs<FilterKey>
            value={filter}
            onChange={setFilter}
            items={[
              { key: "all", label: "전체", count: counts.all },
              { key: "agent", label: "에이전트 요청", count: counts.agent },
              { key: "human", label: "사람 결정", count: counts.human },
              { key: "system", label: "시스템", count: counts.system },
            ]}
          />

          {error ? (
            <div style={{ textAlign: "center", padding: "32px 0", color: "var(--muted)" }}>
              감사 로그를 불러오지 못했어요. ({error})
            </div>
          ) : !loading && entries.length === 0 ? (
            <div style={{ textAlign: "center", padding: "40px 0", color: "var(--muted)" }}>
              아직 이 점검에서 나간 요청 기록이 없어요.
            </div>
          ) : (
            <div className="log">
              {rows.map((e, i) => {
                const who = entryWho(e);
                const isBlock = e.action === "block";
                return (
                  <div
                    // biome-ignore lint/suspicious/noArrayIndexKey: 감사 엔트리에 안정 id 없음(ts+tool+i)
                    key={`${e.ts}-${e.tool}-${i}`}
                    className="logrow"
                    style={isBlock ? { background: "var(--warn-weak)" } : undefined}
                  >
                    <span className="ts">{tsLabel(e.ts)}</span>
                    <span className={`who ${who}`}>{WHO_LABEL[who]}</span>
                    <span className="msg">
                      <code>{e.command || e.tool}</code> ·{" "}
                      <strong style={isBlock ? blocked : allowed}>{isBlock ? "E² 차단" : "E² 허용"}</strong>
                      {e.reason ? ` · ${e.reason}` : ""}
                    </span>
                    <span className="hash" style={{ color: "var(--muted)" }}>
                      준비 중
                    </span>
                  </div>
                );
              })}
              {!loading && rows.length === 0 && entries.length > 0 && (
                <div style={{ textAlign: "center", padding: "24px 0", color: "var(--muted)" }}>
                  이 분류에 해당하는 기록이 없어요.
                </div>
              )}
            </div>
          )}

          <div className="hint">
            각 줄은 전송 통제(E²)가 허용/차단한 요청이에요. 변조 탐지 체인과 권한 분리가 연동되면 한 줄이라도 바뀔 때{" "}
            <Strong>그 뒤 체인이 전부 깨져서 변조가 드러나요.</Strong>
          </div>
        </div>
      </div>
    </Screen>
  );
}
