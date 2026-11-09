import {
  type CSSProperties,
  type ReactNode,
  type PointerEvent as RPointerEvent,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";

import { DiagnosisRunSummary } from "../components/DiagnosisRunSummary";
import { DiagnosticDownload } from "../components/DiagnosticDownload";
import { RemediationPanel } from "../components/RemediationPanel";
import { Screen } from "../components/Screen";
import { TopBar } from "../components/TopBar";
import { Strong, Sub } from "../components/ui";
import { useApiResource } from "../lib/api";
import { type EngineCoverage, type EngineFinding, getCoverage, getTask, listTaskFindings } from "../lib/engine";
import { type AccessDecision, buildCellResolver, DECISION_CELL, DECISION_LABEL, getAccessMap } from "../lib/kg";
import { useSelection } from "../lib/selection";
import { canonicalVulnClass } from "../lib/vulnclass";
import { useGo, useNav } from "../nav";
import type { Severity } from "../types";
import AssetGraphPanel from "./AssetGraphPanel";

type Tab = "sum" | "list" | "cov" | "map" | "asset" | "ev" | "remediation";
type DimKey = "sev" | "type";
type Kind = "bar" | "share" | "table";

const SEV_ORDER: Severity[] = ["critical", "high", "medium", "low"];
const SEV_SET = new Set<string>(SEV_ORDER);
const SEV_LABEL: Record<Severity, string> = { critical: "심각", high: "높음", medium: "중간", low: "낮음" };
const SEV_COLOR: Record<Severity, string> = {
  critical: "var(--crit)",
  high: "var(--hi)",
  medium: "var(--mid)",
  low: "var(--lo)",
};
const SEV_SUB: Record<Severity, string> = { critical: "즉시 확인", high: "우선 확인", medium: "참고", low: "참고" };
/** 엔진 severity 문자열 → UI Severity. 알 수 없는 값은 미분류로 보존한다. */
function toSeverity(raw: string): Severity | undefined {
  return SEV_SET.has(raw) ? (raw as Severity) : undefined;
}
const NOMINAL_COLORS = ["--v-c1", "--v-c2", "--v-c3", "--v-c4"];

const TYPE_LABEL: Record<string, string> = {
  endpoint: "엔드포인트",
  service: "서비스",
  host: "호스트",
  page: "페이지",
  domain: "도메인",
  root_domain: "루트 도메인",
  subdomain: "서브도메인",
  app: "앱",
  ip: "IP",
};

function findingEndpoint(f: EngineFinding): string {
  return f.assets?.[0]?.label || f.vulnclass || "";
}
function findingName(f: EngineFinding): string {
  if (f.name?.length) return f.name;
  if (f.vulnclass.length > 0) return f.vulnclass;
  return "이름 없는 발견";
}
function whenLabel(iso: string): string {
  if (!iso) return "";
  const dt = new Date(iso);
  if (Number.isNaN(dt.getTime())) return "";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${dt.getMonth() + 1}/${dt.getDate()} ${p(dt.getHours())}:${p(dt.getMinutes())}`;
}

const STATUS_LABEL: Record<string, string> = {
  created: "대기",
  queued: "실행 대기",
  running: "진행 중",
  paused: "일시중지",
  done: "완료",
  failed: "실패",
  timeout: "시간초과",
};

const bodyText: CSSProperties = { fontSize: 14, color: "var(--muted)", lineHeight: 1.7, margin: 0 };
const subH: CSSProperties = { fontWeight: 500, color: "var(--muted)", fontSize: 13 };

/** 커버리지 한 줄 바 */
function CoverageBar({
  label,
  tested,
  total,
  strong,
}: {
  label: string;
  tested: number;
  total: number;
  strong?: boolean;
}) {
  const pct = total > 0 ? Math.round((tested / total) * 100) : 0;
  return (
    <div style={{ margin: strong ? "8px 0 16px" : "0 0 10px" }}>
      <div
        style={{ display: "flex", justifyContent: "space-between", fontSize: strong ? 13.5 : 12.5, marginBottom: 4 }}
      >
        <span style={{ fontWeight: strong ? 700 : 600, color: strong ? "var(--fg)" : "var(--muted)" }}>{label}</span>
        <span style={{ color: "var(--muted)", fontVariantNumeric: "tabular-nums" }}>
          {tested}/{total} · {pct}%
        </span>
      </div>
      <div
        style={{
          height: strong ? 10 : 7,
          background: "var(--surface-2)",
          border: "1px solid var(--line)",
          borderRadius: 4,
          overflow: "hidden",
        }}
      >
        <div style={{ width: `${pct}%`, height: "100%", background: "var(--teal)", borderRadius: 4 }} />
      </div>
    </div>
  );
}

/** ===== 보고서 ===== */
export default function ReportScreen() {
  const go = useGo();
  const nav = useNav();
  const { taskId, tasksVersion } = useSelection();
  const [tab, setTab] = useState<Tab>("sum");
  const [openRow, setOpenRow] = useState<string | null>(null);

  useEffect(() => {
    if (taskId === null) {
      setTab("sum");
      setOpenRow(null);
      return;
    }
    setTab("sum");
    setOpenRow(null);
  }, [taskId]);

  const taskRes = useApiResource(() => (taskId ? getTask(taskId) : Promise.resolve(null)), [taskId, tasksVersion]);
  const findRes = useApiResource<EngineFinding[]>(() => listTaskFindings(taskId ?? ""), [taskId, tasksVersion]);
  const covRes = useApiResource<EngineCoverage | null>(() => getCoverage(taskId ?? ""), [taskId, tasksVersion]);

  const task = taskRes.data;
  const findings = useMemo(() => findRes.data ?? [], [findRes.data]);
  const cov = covRes.data;

  const sevCounts = useMemo(() => {
    const c: Record<Severity, number> = { critical: 0, high: 0, medium: 0, low: 0 };
    for (const f of findings) {
      const severity = toSeverity(f.severity);
      if (severity) c[severity] += 1;
    }
    return c;
  }, [findings]);
  const unknownCount = useMemo(() => findings.filter((f) => !toSeverity(f.severity)).length, [findings]);
  const findingsReady = !findRes.loading && !findRes.error && findRes.data !== null && findRes.data !== undefined;

  const loading = taskRes.loading || findRes.loading;
  const error = taskRes.error ?? findRes.error;
  const title = task?.name || task?.description || (taskId ? `점검 #${taskId}` : "");
  let reportLead = "…";
  if (error) reportLead = "보고서를 불러오지 못했어요.";
  else if (task) {
    reportLead = [
      whenLabel(task.created_at),
      STATUS_LABEL[task.status] ?? task.status,
      task.goals_total > 0 ? `목표 ${task.goals_met}/${task.goals_total}` : "",
    ]
      .filter(Boolean)
      .join(" · ");
  }
  let findingSummary = "조회 실패 · 미확인";
  if (findRes.loading) findingSummary = "불러오는 중…";
  else if (findingsReady && findings.length === 0) findingSummary = "아직 없음";
  else if (findingsReady) {
    findingSummary = [
      ...SEV_ORDER.filter((s) => sevCounts[s] > 0).map((s) => `${SEV_LABEL[s]} ${sevCounts[s]}`),
      ...(unknownCount ? [`미분류 ${unknownCount}`] : []),
    ].join(" · ");
  }

  function openFinding(id: string) {
    setTab("list");
    setOpenRow(id);
    requestAnimationFrame(() => document.getElementById(`frow-${id}`)?.scrollIntoView({ block: "center" }));
  }

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
          <button className="backbtn" type="button" onClick={() => go("history")}>
            <span className="arr" aria-hidden="true">
              ←
            </span>
            점검 기록으로 돌아가기
          </button>
          <div className="crumb">
            <button
              type="button"
              className="crumb-link"
              onClick={(event) => {
                event.preventDefault();
                go("history");
              }}
            >
              점검 기록
            </button>{" "}
            › <span>{title || "…"}</span>
          </div>

          {/* === 헤더 · 내보내기 === */}
          <div className="rhead">
            <div style={{ minWidth: 0 }}>
              <h1 className="big" style={{ marginBottom: 4 }}>
                {loading && !task ? "불러오는 중…" : title}
              </h1>
              <p className="lead" style={{ marginBottom: 0 }}>
                {reportLead}
              </p>
            </div>
            <DiagnosticDownload
              key={taskId}
              taskId={taskId}
              enabled={nav.screen === "report" && !taskRes.loading && !taskRes.error && task?.id === taskId}
            />
          </div>

          {/* 실행 종료 상태 — 자족형 seam(데이터는 lib/request-approvals). 위치는 한 줄만 옮기면 바뀐다. */}
          <DiagnosisRunSummary key={taskId} taskId={taskId} active={nav.screen === "report"} />

          {/* === 요약 타일 (위험도 4단) === */}
          <div className="dgrid" style={{ marginBottom: 14 }}>
            <div className="tile">
              <div className="k">발견</div>
              <div className="v">{findingsReady ? findings.length : "—"}</div>
              <div className="sub">{findingSummary}</div>
            </div>
            {SEV_ORDER.map((s) => (
              <div className="tile" key={s}>
                <div className="k">{SEV_LABEL[s]}</div>
                <div className="v" style={{ color: findingsReady && sevCounts[s] > 0 ? SEV_COLOR[s] : undefined }}>
                  {findingsReady ? sevCounts[s] : "—"}
                </div>
                <div className="sub">{SEV_SUB[s]}</div>
              </div>
            ))}
            <div className="tile">
              <div className="k">판정</div>
              <div className="v" style={{ fontSize: 16, color: "var(--muted)", fontWeight: 600 }}>
                준비 중
              </div>
              <div className="sub">E③ 연동 후</div>
            </div>
          </div>

          {/* === 배지 읽는 법 === */}
          <div className="legend-key">
            <div>
              <span className="lk">위험도</span>
              <span className="sev critical">심각</span>
              <span className="sev high">높음</span>
              <span className="sev medium">중간</span>
              <span className="sev low">낮음</span>
              <span className="ld">뚫렸을 때 피해가 얼마나 큰지</span>
            </div>
            <div>
              <span className="lk">판정</span>
              <span className="vbadge2 vd-conf">확정</span>
              <span className="vbadge2 vd-sus">의심</span>
              <span className="vbadge2 vd-und">판단 불가</span>
              <span className="ld">증거로 얼마나 확인됐는지 · E③ 연동 후</span>
            </div>
          </div>

          {/* === 탭 === */}
          <div className="tabs">
            {(
              [
                ["sum", "요약"],
                ["list", "발견 목록"],
                ["remediation", "상세 · 수정 프롬프트"],
                ["cov", "커버리지"],
                ["map", "접근통제 지도"],
                ["asset", "자산 그래프"],
                ["ev", "증거 · 감사"],
              ] as [Tab, string][]
            ).map(([k, l]) => (
              <button key={k} className={tab === k ? "on tab" : "tab"} type="button" onClick={() => setTab(k)}>
                {l}
                {k === "list" && <span className="c">{findingsReady ? findings.length : "—"}</span>}
              </button>
            ))}
          </div>

          {tab === "sum" && (
            <SummaryPanel
              findings={findings}
              findingsReady={findingsReady}
              findingsLoading={findRes.loading}
              findingsError={findRes.error}
              sevCounts={sevCounts}
              cov={cov}
              coverageLoading={covRes.loading}
              coverageError={covRes.error}
              taskKey={taskId}
              onOpenFinding={openFinding}
            />
          )}
          {tab === "list" && (
            <ListPanel
              findings={findings}
              loading={findRes.loading}
              error={findRes.error}
              openRow={openRow}
              setOpenRow={setOpenRow}
            />
          )}
          {tab === "cov" && <CoveragePanel cov={cov} loading={covRes.loading} error={covRes.error} go={go} />}
          {tab === "map" && <MapPanel taskId={taskId} go={go} />}
          {tab === "asset" && <AssetGraphPanel taskId={taskId} findings={findings} onOpenFinding={openFinding} />}
          {tab === "ev" && <EvidencePanel go={go} />}
          {tab === "remediation" && <RemediationPanel key={taskId} taskId={taskId} active={nav.screen === "report"} />}
        </div>
      </div>
    </Screen>
  );
}

/* ---------------- 요약 탭 ---------------- */
function SummaryPanel({
  findings,
  findingsReady,
  findingsLoading,
  findingsError,
  sevCounts,
  cov,
  coverageLoading,
  coverageError,
  taskKey,
  onOpenFinding,
}: {
  findings: EngineFinding[];
  findingsReady: boolean;
  findingsLoading: boolean;
  findingsError: string | null;
  sevCounts: Record<Severity, number>;
  cov: EngineCoverage | null | undefined;
  coverageLoading: boolean;
  coverageError: string | null;
  taskKey: string;
  onOpenFinding: (id: string) => void;
}) {
  const untestedPct =
    !coverageLoading && !coverageError && cov?.enabled && cov.denominator > 0
      ? Math.round(((cov.denominator - cov.tested) / cov.denominator) * 100)
      : null;
  return (
    <div className="rpanel">
      <div className="card">
        <h3>한눈에</h3>
        <p style={bodyText}>
          {!findingsReady && findingsLoading && "발견 목록을 불러오는 중이에요."}
          {!findingsReady && !findingsLoading && findingsError && "발견 목록을 불러오지 못했어요."}
          {!findingsReady && !findingsLoading && !findingsError && "발견 목록을 확인할 수 없어요."}
          {findingsReady && findings.length === 0 && (
            <>
              아직 이 점검에서 <Strong>발견이 없어요</Strong>. 점검이 진행되면 위험도별로 여기에 쌓여요.
            </>
          )}
          {findingsReady && findings.length > 0 && (
            <>
              이 점검에서 <Strong>{findings.length}건</Strong>이 발견됐어요. 위험도{" "}
              <Strong>
                심각 {sevCounts.critical} · 높음 {sevCounts.high}
              </Strong>
              을 먼저 확인하세요.
            </>
          )}{" "}
          각 발견의 확정 · 의심 · 판단 불가 <Strong>판정</Strong>과 증거는 E③ 오라클 연동 후 표시돼요.
        </p>
      </div>

      <FindingsViz
        key={taskKey}
        findings={findings}
        ready={findingsReady}
        loading={findingsLoading}
        error={findingsError}
        onOpenFinding={onOpenFinding}
      />

      <div className="wrap" style={{ padding: 0, gap: 16 }}>
        <div className="col" style={{ flex: "1 1 260px" }}>
          <div className="card" style={{ marginBottom: 0 }}>
            <h3>판정 분포</h3>
            <div className="vcard">
              {["확정", "의심", "판단 불가"].map((l) => (
                <div className="vbox" key={l}>
                  <div className="n" style={{ color: "var(--muted)", fontSize: 18 }}>
                    —
                  </div>
                  <div className="l">{l}</div>
                </div>
              ))}
            </div>
            <div className="hint">
              확정 · 의심 · 판단 불가 <Strong>판정은 E③ 오라클 연동 후</Strong> 채워져요(증거 부족은 오탐 대신 판단
              불가로 남겨요).
            </div>
          </div>
        </div>
        <div className="col" style={{ flex: "1 1 260px" }}>
          <div className="card" style={{ marginBottom: 0 }}>
            <h3>미검사 영역 {untestedPct !== null && <span style={subH}>· {untestedPct}%</span>}</h3>
            {coverageLoading && <p style={bodyText}>커버리지를 불러오는 중이에요.</p>}
            {!coverageLoading && coverageError && <p style={bodyText}>커버리지를 불러오지 못했어요.</p>}
            {!coverageLoading && !coverageError && (cov === null || cov === undefined) && (
              <p style={bodyText}>커버리지 집계 전이거나 확인할 수 없어요.</p>
            )}
            {!coverageLoading && !coverageError && cov !== null && cov !== undefined && !cov.enabled && (
              <p style={bodyText}>커버리지 집계가 꺼져 있어요.</p>
            )}
            {!coverageLoading && !coverageError && cov?.enabled && cov.denominator > 0 && (
              <>
                <div className="find">
                  <span className="pill" style={{ background: "var(--surface-2)", color: "var(--muted)" }}>
                    미검사
                  </span>
                  <span className="nm" style={{ fontWeight: 500 }}>
                    범위 {cov.denominator}개 중 {cov.denominator - cov.tested}개 미탐색
                  </span>
                </div>
                <div className="hint">미검사는 "취약 없음"이 아니라 "아직 안 본 것"이에요.</div>
              </>
            )}
            {!coverageLoading && !coverageError && cov?.enabled && cov.denominator === 0 && (
              <p style={bodyText}>커버리지 집계 전이에요.</p>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}

/* ---------------- 발견 그래프 ---------------- */
type VizRow = { id: string; label: string; color: string; items: EngineFinding[]; n: number };

function FindingsViz({
  findings,
  ready,
  loading,
  error,
  onOpenFinding,
}: {
  findings: EngineFinding[];
  ready: boolean;
  loading: boolean;
  error: string | null;
  onOpenFinding: (id: string) => void;
}) {
  const [dim, setDim] = useState<DimKey>("sev");
  const [kind, setKind] = useState<Kind>("bar");
  const [pick, setPick] = useState<string | null>(null);
  const [tip, setTip] = useState<{ row: VizRow; left: number; top: number } | null>(null);
  const cardRef = useRef<HTMLDivElement>(null);

  const dimName = dim === "sev" ? "위험도" : "유형";
  const pct = (n: number) => `${findings.length ? Math.round((n * 100) / findings.length) : 0}%`;

  const rows: VizRow[] = useMemo(() => {
    if (dim === "sev") {
      const nominal: VizRow[] = SEV_ORDER.map((s) => ({
        id: s,
        label: SEV_LABEL[s],
        color: SEV_COLOR[s],
        items: findings.filter((f) => toSeverity(f.severity) === s),
        n: 0,
      })).map((r) => ({ ...r, n: r.items.length }));
      const unknownItems = findings.filter((f) => !toSeverity(f.severity));
      if (unknownItems.length) {
        nominal.push({
          id: "unknown",
          label: "미분류",
          color: "var(--muted)",
          items: unknownItems,
          n: unknownItems.length,
        });
      }
      return nominal;
    }
    // 유형: vulnclass 를 표시용 정규 계열로 묶는다(원문은 발견 상세에 그대로, 저장·판정 불변).
    // LLM 자유서술이라 같은 취약점이 여러 라벨로 쪼개지는 걸 계열로 합친다(lib/vulnclass).
    const groups = new Map<string, { label: string; items: EngineFinding[] }>();
    for (const f of findings) {
      const fam = canonicalVulnClass(f.vulnclass);
      const g = groups.get(fam.key);
      if (g) g.items.push(f);
      else groups.set(fam.key, { label: fam.label, items: [f] });
    }
    return [...groups.entries()]
      .sort((a, b) => b[1].items.length - a[1].items.length)
      .map(([key, g], i) => ({
        id: key,
        label: g.label,
        color: `var(${NOMINAL_COLORS[i % NOMINAL_COLORS.length]})`,
        items: g.items,
        n: g.items.length,
      }));
  }, [dim, findings]);

  const max = Math.max(1, ...rows.map((r) => r.n));
  const picked = pick ? rows.find((r) => r.id === pick) : undefined;

  const catProps = (r: VizRow) => ({
    type: "button" as const,
    "aria-label": `${dimName} ${r.label} ${r.n}건 ${pct(r.n)}${pick === r.id ? " (선택됨)" : ""}`,
    "aria-pressed": pick === r.id,
    onClick: () => {
      setPick(pick === r.id ? null : r.id);
      setTip(null);
    },
    onPointerMove: (e: RPointerEvent) => showTip(r, e.clientX, e.clientY),
    onPointerLeave: () => setTip(null),
  });
  const dimCls = (r: VizRow) => (pick && pick !== r.id ? " dim" : "");

  function showTip(row: VizRow, x: number, y: number) {
    const cr = cardRef.current?.getBoundingClientRect();
    if (!cr) return;
    setTip({ row, left: Math.max(8, Math.min(cr.width - 160, x - cr.left + 12)), top: y - cr.top + 14 });
  }

  const links = (items: EngineFinding[]) => (
    <div>
      {items.map((f) => (
        <button key={f.id} type="button" className="flink" onClick={() => onOpenFinding(f.id)}>
          {findingName(f)}
        </button>
      ))}
    </div>
  );

  if (!ready) {
    let unavailableMessage = "발견 목록을 확인할 수 없어요.";
    if (loading) unavailableMessage = "발견 목록을 불러오는 중이에요.";
    else if (error) unavailableMessage = "발견 목록을 불러오지 못했어요.";
    return (
      <div className="card">
        <h3>발견 한눈에 보기</h3>
        <p style={bodyText}>{unavailableMessage}</p>
      </div>
    );
  }
  if (findings.length === 0) {
    return (
      <div className="card">
        <h3>발견 한눈에 보기</h3>
        <p style={bodyText}>발견이 쌓이면 위험도 · 유형별 그래프가 여기 떠요.</p>
      </div>
    );
  }

  return (
    <div className="card viz" ref={cardRef}>
      <div className="viz-head">
        <h3>
          발견 한눈에 보기 <span style={subH}>· 같은 발견 {findings.length}건을 기준과 그래프를 바꿔 봐요</span>
        </h3>
        <div className="viz-ctrls">
          <fieldset className="seg">
            <legend className="seg-lab">기준</legend>
            {(["sev", "type"] as DimKey[]).map((k) => (
              <button
                key={k}
                type="button"
                aria-pressed={dim === k}
                onClick={() => {
                  setDim(k);
                  setPick(null);
                }}
              >
                {k === "sev" ? "위험도" : "유형"}
              </button>
            ))}
          </fieldset>
          <fieldset className="seg">
            <legend className="seg-lab">보기</legend>
            {(
              [
                ["bar", "막대"],
                ["share", "비율"],
                ["table", "표"],
              ] as [Kind, string][]
            ).map(([k, l]) => (
              <button key={k} type="button" aria-pressed={kind === k} onClick={() => setKind(k)}>
                {l}
              </button>
            ))}
          </fieldset>
        </div>
      </div>

      {dim === "type" && (
        <p className="hint">
          유형은 표시용 계열로 묶었어요. 빈 값과 분류되지 않은 유형은 기타에 포함하며, 원문 유형은 발견 상세에 그대로
          있어요.
        </p>
      )}

      <div>
        {kind === "bar" && (
          <div className="vbars">
            {rows.map((r) => (
              <button key={r.id} className={`vrow${dimCls(r)}`} {...catProps(r)}>
                <span className="vr-lab">{r.label}</span>
                <span className="vr-track">
                  <span
                    className="vr-fill"
                    style={{ width: `${(r.n / max) * 100}%`, background: r.color, minWidth: r.n ? undefined : 0 }}
                  />
                </span>
                <span className="vr-val">
                  <b>{r.n}건</b> · {pct(r.n)}
                </span>
              </button>
            ))}
          </div>
        )}
        {kind === "share" && (
          <>
            <fieldset className="vshare" aria-label={`${dimName}별 비율`}>
              {rows
                .filter((r) => r.n > 0)
                .map((r) => (
                  <button
                    key={r.id}
                    className={`vseg${dimCls(r)}`}
                    style={{ flex: r.n, background: r.color }}
                    {...catProps(r)}
                  />
                ))}
            </fieldset>
            <div className="vlegend">
              {rows.map((r) => (
                <button key={r.id} className={`vleg${dimCls(r)}`} {...catProps(r)}>
                  <span className="vsw" style={{ background: r.color }} />
                  {r.label}
                  <b>{r.n}건</b>
                  <span className="pc">{pct(r.n)}</span>
                </button>
              ))}
            </div>
          </>
        )}
        {kind === "table" && (
          <div className="scrollx">
            <table className="vtable">
              <thead>
                <tr>
                  {[dimName, "건수", "비율", "해당 발견"].map((h) => (
                    <th key={h}>{h}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => (
                  <tr key={r.id}>
                    <td className="cat">
                      <span className="vsw" style={{ background: r.color }} />
                      {r.label}
                    </td>
                    <td className="num">{r.n}건</td>
                    <td className="num">{pct(r.n)}</td>
                    <td>{r.items.length ? links(r.items) : <span>—</span>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      <div className="viz-note">
        기준: {dimName} · 발견 {findings.length}건 ·{" "}
        {kind === "table"
          ? "발견 이름을 누르면 발견 목록에서 바로 열려요"
          : `${kind === "bar" ? "막대를" : "막대나 범례를"} 누르면 해당 발견이 아래에 보여요`}
      </div>

      {picked && kind !== "table" && (
        <div className="vpick">
          <div className="vpick-h">
            <span className="vsw" style={{ background: picked.color }} />
            {dimName} {picked.label} · {picked.n}건
            <button type="button" onClick={() => setPick(null)}>
              선택 해제
            </button>
          </div>
          {picked.items.length ? links(picked.items) : <div className="hint">해당하는 발견이 없어요</div>}
        </div>
      )}

      {tip && (
        <div className="vtip" role="tooltip" style={{ left: tip.left, top: tip.top }}>
          <strong>
            {tip.row.n}건 · {pct(tip.row.n)}
          </strong>
          {dimName} {tip.row.label}
        </div>
      )}
    </div>
  );
}

/* ---------------- 발견 목록 탭 (아코디언) ---------------- */
function ListPanel({
  findings,
  loading,
  error,
  openRow,
  setOpenRow,
}: {
  findings: EngineFinding[];
  loading: boolean;
  error: string | null;
  openRow: string | null;
  setOpenRow: (id: string | null) => void;
}) {
  const toggle = (id: string) => setOpenRow(openRow === id ? null : id);
  if (loading) {
    return <div style={{ textAlign: "center", padding: "32px 0", color: "var(--muted)" }}>불러오는 중…</div>;
  }
  if (error) {
    return (
      <div style={{ textAlign: "center", padding: "32px 0", color: "var(--muted)" }}>
        발견 목록을 불러오지 못했어요.
      </div>
    );
  }
  if (findings.length === 0) {
    return (
      <div style={{ textAlign: "center", padding: "40px 0", color: "var(--muted)" }}>
        이 점검에서 발견된 항목이 없어요.
      </div>
    );
  }
  return (
    <div className="rpanel">
      <p className="hint" style={{ margin: "0 0 12px" }}>
        이름 · 엔드포인트 · 위험도는 실데이터예요. 항목을 누르면 핵심만 펼쳐지고, 전체 증거 · 비교표 · 판정은{" "}
        <Strong>자세히 보기(E③ 연동 후)</Strong>에서 봐요.
      </p>
      {findings.map((f) => {
        const open = openRow === f.id;
        const sev = toSeverity(f.severity);
        const sevLabel = sev ? SEV_LABEL[sev] : "미분류";
        return (
          <div key={f.id}>
            <button
              type="button"
              id={`frow-${f.id}`}
              className="frow"
              aria-expanded={open}
              aria-controls={`fs-${f.id}`}
              onClick={() => toggle(f.id)}
            >
              <span className={`sev${sev ? ` ${sev}` : ""}`}>
                <span className="chip-k">위험도</span>
                {sevLabel}
              </span>
              <div style={{ minWidth: 0 }}>
                <div className="nm">{findingName(f)}</div>
                <div className="ep">{findingEndpoint(f)}</div>
              </div>
              <div className="rt">
                <span className="vbadge2 vd-na">
                  <span className="chip-k">판정</span>준비 중
                </span>
                <span className="acc-chev" aria-hidden="true">
                  ▾
                </span>
              </div>
            </button>
            {open && (
              <div className="fsum" id={`fs-${f.id}`}>
                <dl>
                  <dt>유형</dt>
                  <dd>{f.vulnclass || "—"}</dd>
                  <dt>엔드포인트</dt>
                  <dd>
                    <code>{findingEndpoint(f) || "—"}</code>
                  </dd>
                  <dt>위험도</dt>
                  <dd>{sevLabel}</dd>
                  <dt>증거 · 판정</dt>
                  <dd style={{ color: "var(--muted)" }}>
                    구조화 증거(요청/응답 · canary)와 확정/의심/판단 불가 판정은 E③ 오라클 연동 후 채워져요.
                  </dd>
                </dl>
                <button type="button" className="btn" disabled>
                  자세히 보기 (E③ 연동 후)
                </button>
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}

/* ---------------- 커버리지 탭 ---------------- */
function CoveragePanel({
  cov,
  loading,
  error,
  go,
}: {
  cov: EngineCoverage | null | undefined;
  loading: boolean;
  error: string | null;
  go: (s: "coverage") => void;
}) {
  let content: ReactNode;
  if (loading) content = <p style={bodyText}>불러오는 중…</p>;
  else if (error) content = <p style={bodyText}>커버리지를 불러오지 못했어요.</p>;
  else if (!cov?.enabled) content = <p style={bodyText}>이 점검은 커버리지 집계가 꺼져 있어요.</p>;
  else if (cov.denominator === 0) content = <p style={bodyText}>아직 범위(scope) 안에 집계된 자산이 없어요.</p>;
  else {
    content = (
      <>
        <CoverageBar label="전체" tested={cov.tested} total={cov.denominator} strong />
        {cov.byType.map((t) => (
          <CoverageBar key={t.type} label={TYPE_LABEL[t.type] ?? t.type} tested={t.tested} total={t.total} />
        ))}
        <p className="hint" style={{ marginTop: 14 }}>
          분모 = 범위(scope) 내 자산 수, 분자 = 실제로 요청 · 탐색한 수. 미검사(분모−분자)는{" "}
          <Strong>"취약 없음"이 아니라 "아직 안 본 것"</Strong>이에요.
        </p>
      </>
    );
  }
  return (
    <div className="rpanel">
      <div className="card">
        <h3>
          검사 커버리지 <Sub>· 범위 내 자산 대비 실제 탐색</Sub>
        </h3>
        {content}
        <button className="btn" type="button" style={{ marginTop: 14 }} onClick={() => go("coverage")}>
          전체 커버리지 열기 →
        </button>
      </div>
    </div>
  );
}

/* ---------------- 접근통제 지도 탭 ---------------- */
/** 칸 안에 쓰는 짧은 이름 (범례는 DECISION_LABEL 의 긴 이름) */
const CELL_SHORT: Record<AccessDecision, string> = {
  "allow-ok": "허용",
  "deny-ok": "차단",
  vuln: "확정",
  suspected: "의심",
  unknown: "판정 불가",
  untested: "미검사",
  blank: "기록 없음",
  na: "—",
};
const LEG_DOT: Record<AccessDecision, CSSProperties> = {
  "allow-ok": { background: "var(--teal)" },
  "deny-ok": { background: "var(--line-2)" },
  vuln: { background: "var(--danger)" },
  suspected: { background: "var(--viol)" },
  unknown: { background: "var(--warn)" },
  untested: { background: "transparent", border: "1px dashed var(--line-2)" },
  blank: { background: "var(--surface-2)", border: "1px solid var(--line-2)" },
  na: { background: "var(--faint)" },
};
/** 보고서 지도 범례 — 기본 축 + 실제로 나타난 상태만 추가 */
const MAP_LEGEND_BASE: AccessDecision[] = ["allow-ok", "deny-ok", "vuln", "untested"];
const MAP_LEGEND_EXTRA: AccessDecision[] = ["suspected", "unknown", "blank"];

function MapPanel({ taskId, go }: { taskId: string; go: (s: "access-map") => void }) {
  const { data, loading, error } = useApiResource(() => getAccessMap(taskId), [taskId]);
  // 전체 지도와 동일한 fail-closed 규칙(중복·미반환 처리)을 공유 리졸버로 적용(kg §4.2(7)).
  const resolve = useMemo(() => buildCellResolver(data?.cells ?? []), [data]);
  // 실제로 나타난 추가 상태만 범례에 넣는다
  const present = new Set<AccessDecision>();
  if (data) for (const p of data.principals) for (const r of data.resources) present.add(resolve(p.ref, r.id).decision);
  const legend: AccessDecision[] = [...MAP_LEGEND_BASE, ...MAP_LEGEND_EXTRA.filter((d) => present.has(d))];

  let content: ReactNode;
  if (loading) content = <p style={bodyText}>불러오는 중…</p>;
  else if (error || !data) content = <p style={bodyText}>접근통제 지도를 불러오지 못했어요.</p>;
  else {
    content = (
      <>
        <div className="scrollx">
          <table className="matrix">
            <thead>
              <tr>
                <th />
                {data.resources.map((r) => (
                  <th key={r.id}>
                    {r.label}
                    <span className="sm">소유 {r.ownerRef}</span>
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {data.principals.map((pr) => (
                <tr key={pr.ref}>
                  <td className="rowh">
                    {pr.ref}
                    <span className="sm">{pr.role}</span>
                  </td>
                  {data.resources.map((r) => {
                    const { decision, note, projectionError } = resolve(pr.ref, r.id);
                    const title = projectionError
                      ? `판정 불가(projection): ${projectionError}`
                      : note || DECISION_LABEL[decision];
                    return (
                      <td key={r.id} className={DECISION_CELL[decision]} title={title}>
                        {/* 축소 뷰: 짧은 라벨(확정/의심)로 구분, 색이 1차 — 배지는 전체 지도에서.
                            deny-ok(보류)은 리졸버가 unknown 으로 내리므로 여기 셀로는 안 나온다. */}
                        <span>{CELL_SHORT[decision]}</span>
                      </td>
                    );
                  })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <div className="leg">
          {legend.map((d) => (
            <span key={d}>
              <span className="dot" style={LEG_DOT[d]} />
              {DECISION_LABEL[d]}
            </span>
          ))}
        </div>
      </>
    );
  }
  return (
    <div className="rpanel">
      <div className="card">
        <h3>접근통제 지도</h3>
        <p className="hint" style={{ marginTop: 0, marginBottom: 14 }}>
          행: 계정·역할, 열: 자원. 빨간 칸은 접근되면 안 되는 조합이 열린 경우예요(같은 역할 간:{" "}
          <Strong>수평 IDOR</Strong>, 상위 역할 자원: <Strong>수직 권한 상승</Strong>).
        </p>
        {data?.mock && <p className="map-mock">예시 데이터예요. 소유·권한 KG 연동 후 실제 결과로 채워져요.</p>}
        {content}
        <button className="btn" type="button" style={{ marginTop: 14 }} onClick={() => go("access-map")}>
          전체 접근통제 지도 열기 →
        </button>
      </div>
    </div>
  );
}

/* ---------------- 증거 · 감사 탭 ---------------- */
function EvidencePanel({ go }: { go: (s: "audit") => void }) {
  return (
    <div className="rpanel">
      <div className="card" style={{ marginBottom: 0 }}>
        <h3>감사 기록</h3>
        <p className="hint" style={{ marginTop: 0, marginBottom: 12 }}>
          이 점검 중 명시적으로 허용하거나 차단한 결정과 승인 요청이 기록으로 남아요. 전체 송신 원장은 별도 기록이며,
          변조 탐지 체인 · 권한 분리는 연동 후 제공돼요.
        </p>
        <button className="btn" type="button" onClick={() => go("audit")}>
          통제 기록 열기 →
        </button>
      </div>
    </div>
  );
}
