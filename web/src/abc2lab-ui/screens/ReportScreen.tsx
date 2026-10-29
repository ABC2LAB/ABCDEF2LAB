import { useMemo, useState } from "react";
import { Screen } from "../components/Screen";
import { Tabs } from "../components/Tabs";
import { TopBar } from "../components/TopBar";
import { Pill, Sev, Strong, Sub } from "../components/ui";
import { useApiResource } from "../lib/api";
import { type EngineFinding, getTask, listTaskFindings } from "../lib/engine";
import { useSelection } from "../lib/selection";
import { useGo } from "../nav";
import { type Severity } from "../types";

type PanelKey = "sum" | "list" | "map" | "ev";

const SEV_ORDER: Severity[] = ["critical", "high", "medium", "low"];
const SEV_SET = new Set<string>(SEV_ORDER);
/** 엔진 severity 문자열 → UI Severity (모르는 값은 low로 보수적으로) */
function toSeverity(raw: string): Severity {
  return SEV_SET.has(raw) ? (raw as Severity) : "low";
}

const STATUS_LABEL: Record<string, string> = {
  created: "대기",
  running: "진행 중",
  paused: "일시중지",
  done: "완료",
  failed: "실패",
};

/** 발견 하나의 표시용 엔드포인트: 첫 자산 라벨("GET /url") 우선, 없으면 유형 */
function findingEndpoint(f: EngineFinding): string {
  return f.assets?.[0]?.label || f.vulnclass || "";
}
function findingName(f: EngineFinding): string {
  return f.name || f.vulnclass || "이름 없는 발견";
}

/** created_at(RFC3339) → "10/25 14:03" */
function whenLabel(iso: string): string {
  if (!iso) return "";
  const dt = new Date(iso);
  if (Number.isNaN(dt.getTime())) return "";
  const mm = dt.getMonth() + 1;
  const dd = dt.getDate();
  const hh = String(dt.getHours()).padStart(2, "0");
  const mi = String(dt.getMinutes()).padStart(2, "0");
  return `${mm}/${dd} ${hh}:${mi}`;
}

const btnSm = { padding: "9px 14px", fontSize: 13 } as const;
const bodyText = { fontSize: 13.5, color: "var(--muted)", lineHeight: 1.7, margin: 0 } as const;

/** ===== 보고서 ===== */
export default function ReportScreen() {
  const go = useGo();
  const { taskId } = useSelection();
  const [panel, setPanel] = useState<PanelKey>("sum");

  const taskRes = useApiResource(() => (taskId ? getTask(taskId) : Promise.resolve(null)), [taskId]);
  const findRes = useApiResource(() => listTaskFindings(taskId ?? ""), [taskId]);

  const task = taskRes.data;
  const findings = useMemo(() => findRes.data ?? [], [findRes.data]);

  // 심각도별 집계(실데이터). 판정(확정/의심/판단불가)은 E③ 연동 전이라 집계하지 않는다.
  const sevCounts = useMemo(() => {
    const c: Record<Severity, number> = { critical: 0, high: 0, medium: 0, low: 0 };
    for (const f of findings) c[toSeverity(f.severity)] += 1;
    return c;
  }, [findings]);

  const loading = taskRes.loading || findRes.loading;
  const error = taskRes.error || findRes.error;
  const title = task?.name || task?.description || (taskId ? `점검 #${taskId}` : "");

  // 선택된 점검이 없으면 안내
  if (!taskId) {
    return (
      <Screen id="report">
        <div className="frame">
          <TopBar current="history" />
          <div style={{ textAlign: "center", padding: "64px 24px", color: "var(--muted)" }}>
            <p style={{ marginBottom: 16 }}>볼 보고서를 먼저 골라 주세요.</p>
            <button className="btn primary" type="button" onClick={() => go("history")}>
              점검 기록 열기
            </button>
          </div>
        </div>
      </Screen>
    );
  }

  return (
    <Screen id="report">
      <div className="frame">
        <TopBar current="history" />
        <div style={{ padding: "22px 24px" }}>
          <div className="crumb">
            {/* biome-ignore lint/a11y/useValidAnchor: 원본 시안의 탭 내 이동(앵커 스타일 유지) */}
            <a onClick={() => go("history")}>점검 기록</a> › <span>{title || "…"}</span>
          </div>
          <div className="rhead">
            <div style={{ minWidth: 0 }}>
              <h1 className="big" style={{ marginBottom: 4 }}>
                {loading && !task ? "불러오는 중…" : title}
              </h1>
              <p className="lead" style={{ marginBottom: 0 }}>
                {error
                  ? "보고서를 불러오지 못했어요."
                  : task
                    ? [
                        whenLabel(task.created_at),
                        STATUS_LABEL[task.status] ?? task.status,
                        task.goals_total > 0 ? `목표 ${task.goals_met}/${task.goals_total}` : "",
                      ]
                        .filter(Boolean)
                        .join(" · ")
                    : "…"}
              </p>
            </div>
            <div style={{ textAlign: "right" }}>
              <div style={{ fontSize: 12, color: "var(--muted)", marginBottom: 6 }}>내보내기</div>
              <div className="row" style={{ flexWrap: "wrap", justifyContent: "flex-end" }}>
                {/* TODO(이경준): GET /api/report 연결 — 지금은 비활성 */}
                <button className="btn" type="button" style={btnSm} disabled>
                  HTML
                </button>
                <button className="btn" type="button" style={btnSm} disabled>
                  PDF
                </button>
                <button className="btn" type="button" style={btnSm} disabled>
                  JSON
                </button>
              </div>
            </div>
          </div>

          <div className="dgrid" style={{ marginBottom: 20 }}>
            <div className="tile">
              <div className="k">발견</div>
              <div className="v">{findings.length}</div>
              <div className="sub">
                {findings.length === 0
                  ? "아직 없음"
                  : SEV_ORDER.filter((s) => sevCounts[s] > 0)
                      .map(
                        (s) =>
                          `${({ critical: "심각", high: "높음", medium: "중간", low: "낮음" } as const)[s]} ${sevCounts[s]}`,
                      )
                      .join(" · ")}
              </div>
            </div>
            <div className="tile">
              <div className="k">심각 · 높음</div>
              <div
                className="v"
                style={{ color: sevCounts.critical + sevCounts.high > 0 ? "var(--danger)" : undefined }}
              >
                {sevCounts.critical + sevCounts.high}
              </div>
              <div className="sub">우선 확인</div>
            </div>
            <div className="tile">
              <div className="k">중간 · 낮음</div>
              <div className="v">{sevCounts.medium + sevCounts.low}</div>
              <div className="sub">참고</div>
            </div>
            <div className="tile">
              <div className="k">판정</div>
              <div className="v" style={{ fontSize: 15, color: "var(--muted)", fontWeight: 600 }}>
                준비 중
              </div>
              <div className="sub">E③ 연동 후</div>
            </div>
          </div>

          <Tabs<PanelKey>
            value={panel}
            onChange={setPanel}
            items={[
              { key: "sum", label: "요약" },
              { key: "list", label: "발견 목록", count: findings.length },
              { key: "map", label: "접근통제 지도" },
              { key: "ev", label: "증거 · 감사" },
            ]}
          />

          {/* 요약 */}
          <div className="rpanel" hidden={panel !== "sum"}>
            <div className="card">
              <h3>한눈에</h3>
              <p style={bodyText}>
                {error ? (
                  "보고서를 불러오지 못했어요. 잠시 후 다시 시도해 주세요."
                ) : findings.length === 0 ? (
                  <>
                    아직 이 점검에서 <Strong>발견이 없어요</Strong>. 점검이 진행되면 심각도별로 여기에 쌓여요.
                    (확정·의심·판단 불가 <Strong>판정</Strong> 구분은 E③ 오라클 연동 후 표시돼요.)
                  </>
                ) : (
                  <>
                    이 점검에서 <Strong>{findings.length}건</Strong>이 발견됐어요. 심각·높음{" "}
                    <Strong>{sevCounts.critical + sevCounts.high}건</Strong>을 먼저 확인하세요. 각 발견의 확정·의심·판단
                    불가 <Strong>판정</Strong>은 E③ 오라클 연동 후 표시돼요.
                  </>
                )}
              </p>
            </div>
          </div>

          {/* 발견 목록 */}
          <div className="rpanel" hidden={panel !== "list"}>
            {findRes.loading ? (
              <div style={{ textAlign: "center", padding: "32px 0", color: "var(--muted)" }}>불러오는 중…</div>
            ) : findings.length === 0 ? (
              <div style={{ textAlign: "center", padding: "40px 0", color: "var(--muted)" }}>
                이 점검에서 발견된 항목이 없어요.
              </div>
            ) : (
              findings.map((f) => (
                // biome-ignore lint/a11y/useKeyWithClickEvents: 원본 시안 .frow 클릭 이동 유지
                <div key={f.id} className="frow" onClick={() => go("finding")}>
                  <Sev level={toSeverity(f.severity)} />
                  <div style={{ minWidth: 0 }}>
                    <div className="nm">{findingName(f)}</div>
                    <div className="ep">{findingEndpoint(f)}</div>
                  </div>
                  <div className="rt">
                    <Pill tone="muted">상세</Pill>
                  </div>
                </div>
              ))
            )}
          </div>

          {/* 접근통제 지도 — 전용 페이지로 */}
          <div className="rpanel" hidden={panel !== "map"}>
            <div className="card">
              <h3>
                접근통제 지도 <Sub>· 누가 어디에 닿는가</Sub>
              </h3>
              <p style={bodyText}>
                행은 <Strong>신원(계정·역할)</Strong>, 열은 <Strong>자원(소유자)</Strong>. 닿으면 안 되는 칸이 열리면
                취약(수평 IDOR·수직 권한상승)이고, <Strong>미검사·판정 불가·취약</Strong>를 색으로 구분해요.
              </p>
              <button className="btn primary" type="button" onClick={() => go("access-map")} style={{ marginTop: 14 }}>
                접근통제 지도 열기 →
              </button>
            </div>
          </div>

          {/* 증거 · 감사 */}
          <div className="rpanel" hidden={panel !== "ev"}>
            <div className="card" style={{ marginBottom: 0 }}>
              <h3>감사 기록</h3>
              <p className="hint" style={{ marginTop: 0, marginBottom: 12 }}>
                이 점검 중 나간 요청과 사람의 승인 결정이 변조 탐지되는 로그로 남아요. 점검 운영자는 로그를 지울 수
                없어요(권한 분리).
              </p>
              <button className="btn" type="button" onClick={() => go("audit")}>
                감사 로그 열기 →
              </button>
            </div>
          </div>
        </div>
      </div>
    </Screen>
  );
}
