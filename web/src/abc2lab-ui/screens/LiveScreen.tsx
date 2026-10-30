import { Fragment, type ReactNode, useEffect, useMemo, useRef, useState } from "react";

import { Modal } from "../components/Modal";
import { Screen } from "../components/Screen";
import { TopBar } from "../components/TopBar";
import { Sev, Strong, Sub } from "../components/ui";
import { useApiResource } from "../lib/api";
import {
  archiveTask,
  controlTask,
  type EngineActivity,
  type EngineCoverage,
  type EngineFinding,
  type EngineTaskArchive,
  type EngineTokens,
  getActivity,
  getCoverage,
  getTask,
  getTaskArchive,
  getTokens,
  listTaskFindings,
  listTasks,
} from "../lib/engine";
import { useSelection } from "../lib/selection";
import { useNav } from "../nav";
import type { Severity, Target } from "../types";

/* ── 목업(엔진 개발 후 실데이터로) ─────────────────────────────────
 * 예상 남은 시간 / 예상 토큰은 추정치라 엔진 작업 후 추가 예정 — 지금은 목업 유지.
 * 그 외 슬롯(진행바·토큰 실사용·발견·커버리지·활동)은 실데이터로 배선.         */
const MOCK = { etaRange: "12~15", elapsed: "경과 18분", tokenEst: "예상 1.8~2.1M" } as const;

const PIPELINE = ["자산 탐색", "범위 확인", "점검 계획", "취약점 점검", "검증", "마무리", "보고서"];

const STATUS_LABEL: Record<string, string> = {
  created: "대기",
  queued: "실행 대기",
  running: "진행 중",
  paused: "일시중지",
  done: "완료",
  failed: "실패",
  timeout: "시간초과",
};

const SEV_ORDER: Severity[] = ["critical", "high", "medium", "low"];
const SEV_SET = new Set<string>(SEV_ORDER);
const SEV_LABEL: Record<Severity, string> = { critical: "심각", high: "높음", medium: "중간", low: "낮음" };
function toSeverity(raw: string): Severity | undefined {
  return SEV_SET.has(raw) ? (raw as Severity) : undefined;
}

function fmtTokens(n: number): string {
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}K`;
  return String(n);
}
function tsLabel(ts: number): string {
  if (!ts) return "—";
  const dt = new Date(ts);
  if (Number.isNaN(dt.getTime())) return "—";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(dt.getHours())}:${p(dt.getMinutes())}:${p(dt.getSeconds())}`;
}

/** 탐색 집계 비율을 표시한다. 각 칸은 특정 자산이나 취약점 판정을 나타내지 않는다. */
function coverageCells(denominator: number, tested: number, cap = 48): string[] {
  const n = Math.min(denominator, cap) || 0;
  const scale = denominator > cap ? cap / denominator : 1;
  const t = Math.min(Math.round(tested * scale), n);
  return Array.from({ length: n }, (_, i) => (i < t ? "o" : "n"));
}

type ActDot = "teal" | "blue" | "warn" | "red";
function actDot(e: EngineActivity): ActDot {
  if (e.isError || e.kind === "finding") return "red";
  if (e.kind === "tool" || e.kind === "tool_use") return "blue";
  if (e.kind === "result" || e.kind === "text") return "teal";
  return "warn";
}

/* ---------- 상세 보기 팝업 (활동 로그) ---------- */
function LiveDetailModal({
  open,
  onClose,
  title,
  status,
  covPct,
  acts,
  truncated,
  loading,
  error,
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  status: string;
  covPct: number | null;
  acts: EngineActivity[];
  truncated: boolean;
  loading: boolean;
  error: string | null;
}) {
  let activityCountLabel: string;
  if (loading) {
    activityCountLabel = "조회 중";
  } else if (error) {
    activityCountLabel = "조회 실패";
  } else {
    activityCountLabel = `${acts.length}건`;
  }

  let activityRows: ReactNode;
  if (loading) {
    activityRows = <div>활동 기록을 불러오는 중이에요.</div>;
  } else if (error) {
    activityRows = <div role="alert">활동 기록을 불러오지 못했어요.</div>;
  } else if (acts.length === 0) {
    activityRows = <div style={{ color: "var(--muted)", fontSize: 13, padding: "12px 0" }}>기록된 활동이 없어요.</div>;
  } else {
    activityRows = acts.map((e) => (
      <div className="rrow" key={e.seq}>
        <span className="rt2">{tsLabel(e.ts)}</span>
        <span className={`rdot ${actDot(e)}`} />
        <span className="rx">
          {e.worker ? `${e.worker} · ` : ""}
          {e.summary || e.kind}
        </span>
      </div>
    ));
  }

  return (
    <Modal open={open} onClose={onClose}>
      <div className="rpop" role="dialog" aria-label="활동 로그">
        <div className="rpop-head">
          <div style={{ minWidth: 0, flex: 1 }}>
            <div className="rpt">{title}</div>
            <div className="rpbar">
              <i style={{ width: `${covPct ?? 0}%` }} />
            </div>
            <div className="rpd">
              탐색 범위 {covPct === null ? "미확인" : `${covPct}%`} · 활동 {activityCountLabel}
              {truncated ? " (앞부분)" : ""}
            </div>
          </div>
          <button className="rpop-x" type="button" onClick={onClose} aria-label="닫기">
            ✕
          </button>
        </div>
        <div className="rpop-now">
          {status === "running" ? <span className="spin" /> : null}
          <div style={{ minWidth: 0 }}>
            <div className="rpnl">상태</div>
            <div className="rpnv">{STATUS_LABEL[status] ?? status}</div>
          </div>
        </div>
        <div className="rpop-body">
          <div className="rp-left">
            <div className="rp-h">활동 로그</div>
            <div className="rlegend">
              <span>
                <span className="rdot teal" />
                메시지·결과
              </span>
              <span>
                <span className="rdot blue" />
                도구
              </span>
              <span>
                <span className="rdot warn" />
                라운드·계획
              </span>
              <span>
                <span className="rdot red" />
                발견·오류
              </span>
            </div>
            <div className="rlog">{activityRows}</div>
          </div>
          <div className="rp-right">
            <div className="rp-h">점검 계획</div>
            <div className="rpnote">
              의도(계획)는 플래너만 만들고 워커들이 실행해요. 계획 단계 표시는 <Strong>엔진 연동 후</Strong> 실데이터로
              채워져요(지금은 활동 로그로 과정을 확인).
            </div>
          </div>
        </div>
      </div>
    </Modal>
  );
}

/* ---------- 진행 대시보드 ---------- */
const small = { fontSize: 13, fontWeight: 600, color: "var(--muted)" } as const;
const btnSm = { padding: "8px 13px", fontSize: 13 } as const;

/** ===== 진행 대시보드 ===== */
export default function LiveScreen({ target }: { target: Target }) {
  const nav = useNav();
  const go = nav.go;
  const { taskId, selectTask, tasksVersion, refreshTasks } = useSelection();
  const currentView = useRef({ taskId, screen: nav.screen });
  currentView.current = { taskId, screen: nav.screen };
  const actionLock = useRef(false);
  const [actionBusy, setActionBusy] = useState(false);
  const [actionMsg, setActionMsg] = useState<{ taskId: string; message: string } | null>(null);
  const [endTarget, setEndTarget] = useState<{ id: string; name: string } | null>(null);
  type ArchiveInfo = EngineTaskArchive & { name: string };
  const [archives, setArchives] = useState<Record<string, ArchiveInfo>>({});
  const archivesRef = useRef(archives);
  archivesRef.current = archives;
  const [archiveCheckBusy, setArchiveCheckBusy] = useState(false);
  const [detailOpen, setDetailOpen] = useState(false);

  const taskRes = useApiResource(() => (taskId ? getTask(taskId) : Promise.resolve(null)), [taskId, tasksVersion]);
  const findRes = useApiResource<EngineFinding[]>(() => listTaskFindings(taskId ?? ""), [taskId, tasksVersion]);
  const covRes = useApiResource<EngineCoverage | null>(() => getCoverage(taskId ?? ""), [taskId, tasksVersion]);
  const tokRes = useApiResource<EngineTokens>(() => getTokens(taskId ?? ""), [taskId, tasksVersion]);
  const actRes = useApiResource<EngineActivity[]>(() => getActivity(taskId ?? ""), [taskId, tasksVersion]);

  const tasksRes = useApiResource(listTasks, [tasksVersion]);
  useEffect(() => {
    setEndTarget((previous) => (previous?.id === taskId && nav.screen === "live" ? previous : null));
    setDetailOpen(false);
  }, [taskId, nav.screen]);

  const task = taskRes.data;
  const findings = useMemo(() => findRes.data ?? [], [findRes.data]);
  const cov = covRes.data;
  const acts = useMemo(() => [...(actRes.data ?? [])].sort((a, b) => a.seq - b.seq), [actRes.data]);
  const resources = [taskRes, findRes, covRes, tokRes, actRes];
  const snapshotLoading = resources.some((r) => r.loading);
  const snapshotError = resources.some((r) => r.error);
  const fetchedAt = resources.every((r) => r.fetchedAt !== null)
    ? Math.min(...resources.map((r) => r.fetchedAt ?? 0))
    : null;
  const findingsReady = !findRes.loading && !findRes.error && findRes.data !== null;

  const sevCounts = useMemo(() => {
    const c: Record<Severity, number> = { critical: 0, high: 0, medium: 0, low: 0 };
    for (const f of findings) {
      const severity = toSeverity(f.severity);
      if (severity) c[severity] += 1;
    }
    return c;
  }, [findings]);

  const unclassified = findings.length - Object.values(sevCounts).reduce((sum, count) => sum + count, 0);

  const status = task?.status ?? "";
  const title = task?.name?.trim() || task?.description?.trim() || (taskId ? `점검 #${taskId}` : target.url);
  const covPct = cov?.enabled && cov.denominator > 0 ? Math.round((cov.tested / cov.denominator) * 100) : null;
  const tokenTotal = (tokRes.data?.input ?? 0) + (tokRes.data?.output ?? 0);
  const cells = cov ? coverageCells(cov.denominator, cov.tested) : [];

  const terminal = ["done", "failed", "timeout"].includes(status);
  const taskReady = !!task && task.id === taskId && !taskRes.loading && !taskRes.error;
  const currentArchive = taskId ? archives[taskId] : undefined;
  const archivePending = currentArchive && ["archive_queued", "archiving"].includes(currentArchive.state);
  const canPause = taskReady && ["running", "queued"].includes(status);
  const canResume = taskReady && status === "paused";
  const canArchive = taskReady && (status === "paused" || terminal) && task.in_flight === 0;
  const jobs = (tasksRes.data?.tasks ?? []).filter((t) =>
    ["created", "queued", "running", "paused"].includes(t.status),
  );
  const taskListReady = !tasksRes.loading && !tasksRes.error && tasksRes.data !== null;
  const classifiedFindings = (tasksRes.data?.tasks ?? []).reduce(
    (sum, t) => sum + Object.values(t.findings).reduce((n, c) => n + c, 0),
    0,
  );

  async function doControl(action: "pause" | "resume") {
    if (!taskId || !taskReady || actionLock.current || archivePending) return;
    if ((action === "pause" && !canPause) || (action === "resume" && !canResume)) return;
    const id = taskId;
    actionLock.current = true;
    setActionBusy(true);
    setActionMsg(null);
    try {
      await controlTask(id, action);
      refreshTasks();
    } catch (error) {
      setActionMsg({ taskId: id, message: error instanceof Error ? error.message : "제어에 실패했어요." });
    } finally {
      actionLock.current = false;
      setActionBusy(false);
    }
  }

  async function doArchive() {
    const target = endTarget;
    if (!target || target.id !== taskId || !canArchive || actionLock.current || archivePending) return;
    actionLock.current = true;
    setActionBusy(true);
    setActionMsg(null);
    try {
      const receipt = await archiveTask(target.id);
      setArchives((previous) => ({ ...previous, [target.id]: { ...receipt, name: target.name } }));
      setEndTarget(null);
      refreshTasks();
    } catch (error) {
      setActionMsg({
        taskId: target.id,
        message: error instanceof Error ? error.message : "보관 요청을 확인하지 못했어요.",
      });
    } finally {
      actionLock.current = false;
      setActionBusy(false);
    }
  }

  async function checkArchive() {
    if (!currentArchive || archiveCheckBusy) return;
    const receipt = currentArchive;
    setArchiveCheckBusy(true);
    try {
      const result = await getTaskArchive(receipt.id);
      if (result.taskId !== receipt.taskId) throw new Error("보관 응답의 작업 연결을 확인하지 못했어요.");
      if (archivesRef.current[receipt.taskId]?.id !== receipt.id) return;
      setArchives((previous) => ({ ...previous, [receipt.taskId]: { ...receipt, ...result } }));
      if (result.state === "ready") {
        refreshTasks();
        if (currentView.current.taskId === receipt.taskId && currentView.current.screen === "live") {
          selectTask(null);
          go("history");
        }
      }
    } catch (error) {
      setActionMsg({
        taskId: receipt.taskId,
        message: error instanceof Error ? error.message : "보관 상태를 확인하지 못했어요.",
      });
    } finally {
      setArchiveCheckBusy(false);
    }
  }

  // 데이터는 task 선택 시 한 번 조회한 스냅샷 — 자동 갱신은 미연결(후속). 수동 새로고침만.
  function refreshAll() {
    taskRes.reload();
    findRes.reload();
    covRes.reload();
    tokRes.reload();
    actRes.reload();
    tasksRes.reload();
  }

  if (!taskId) {
    return (
      <Screen id="live">
        <div className="frame">
          <TopBar current="history" />
          <div style={{ textAlign: "center", padding: "64px 24px", color: "var(--muted)" }}>
            <p style={{ marginBottom: 16 }}>진행 상황을 볼 점검을 먼저 골라 주세요.</p>
            <button className="btn primary" type="button" onClick={() => go("history")}>
              점검 기록 열기
            </button>
          </div>
        </div>
      </Screen>
    );
  }

  let snapshotLabel: string;
  if (snapshotLoading) {
    snapshotLabel = "조회 중…";
  } else if (snapshotError) {
    snapshotLabel = "일부 조회 실패 — 새로고침해 주세요";
  } else {
    snapshotLabel = `조회 ${tsLabel(fetchedAt ?? 0)}`;
  }

  let statusLabel: string;
  if (status === "running") {
    statusLabel = "점검 진행 중";
  } else if (status === "done") {
    statusLabel = "점검 완료 — 아래 결과·보고서를 확인하세요";
  } else {
    statusLabel = (STATUS_LABEL[status] ?? status) || "—";
  }

  let tokenLabel: string;
  if (tokRes.loading) {
    tokenLabel = "…";
  } else if (tokRes.error) {
    tokenLabel = "—";
  } else {
    tokenLabel = fmtTokens(tokenTotal);
  }

  let findingRows: ReactNode;
  if (findRes.loading) {
    findingRows = <div style={{ padding: "12px 0", color: "var(--muted)", fontSize: 13 }}>불러오는 중…</div>;
  } else if (findRes.error) {
    findingRows = (
      <div role="alert" style={{ padding: "12px 0", color: "var(--muted)", fontSize: 13 }}>
        발견 목록을 불러오지 못했어요.
      </div>
    );
  } else if (findings.length === 0) {
    findingRows = <div style={{ padding: "12px 0", color: "var(--muted)", fontSize: 13 }}>아직 발견이 없어요.</div>;
  } else {
    findingRows = findings.slice(0, 4).map((f) => (
      <button
        key={f.id}
        className="find"
        type="button"
        onClick={() => go("report")}
        style={{
          color: "inherit",
          background: "none",
          borderRight: 0,
          borderBottom: 0,
          borderLeft: 0,
          width: "100%",
          textAlign: "left",
          cursor: "pointer",
        }}
      >
        <Sev level={toSeverity(f.severity)} />
        <span className="nm">{f.name || f.vulnclass || "이름 없는 발견"}</span>
      </button>
    ));
  }

  let coverageMessage: string;
  if (covRes.loading) {
    coverageMessage = "커버리지 조회 중이에요.";
  } else if (covRes.error) {
    coverageMessage = "커버리지를 불러오지 못했어요.";
  } else if (cov && !cov.enabled) {
    coverageMessage = "커버리지 기능이 비활성화돼 있어요.";
  } else {
    coverageMessage = "커버리지 집계 전이에요.";
  }

  let activityLabel: string;
  if (actRes.loading) {
    activityLabel = "조회 중";
  } else if (actRes.error) {
    activityLabel = "조회 실패";
  } else {
    activityLabel = `${acts.length}건 기록됨`;
  }

  return (
    <Screen id="live">
      <div className="frame">
        <TopBar current="history" />
        <div style={{ padding: "22px 24px" }}>
          {tasksRes.error && <p role="alert">작업 목록을 불러오지 못했어요.</p>}
          {jobs.length > 0 && (
            <div className="jobtabs" role="tablist" aria-label="실행 중 작업">
              {jobs.map((t) => (
                <button
                  key={t.id}
                  type="button"
                  role="tab"
                  aria-selected={t.id === taskId}
                  disabled={actionBusy}
                  className={t.id === taskId ? "jt on" : "jt"}
                  onClick={() => selectTask(t.id)}
                >
                  <span className={`jt-dot ${t.status === "running" ? "run" : "wait"}`} />
                  <span className="jt-name">{t.name.trim() || "이름 없는 점검"}</span>
                  <span className="jt-sub">{STATUS_LABEL[t.status] ?? t.status}</span>
                </button>
              ))}
              <span className="jt-total">
                위험도 분류 집계 <b>{taskListReady ? classifiedFindings : "—"}</b>건 · 미분류 제외
              </span>
            </div>
          )}
          <div
            style={{
              display: "flex",
              alignItems: "center",
              justifyContent: "space-between",
              gap: 12,
              flexWrap: "wrap",
              marginBottom: 16,
            }}
          >
            <div style={{ fontSize: 13, color: "var(--muted)", minWidth: 0 }}>
              점검 · <Strong>{title}</Strong>
              {status ? ` · ${STATUS_LABEL[status] ?? status}` : ""}
              <div style={{ fontSize: 11.5, color: "var(--faint)", marginTop: 2 }}>
                {snapshotLabel} · 자동 갱신 미연결(새로고침 눌러 갱신)
              </div>
            </div>
            <div className="row" style={{ flexWrap: "wrap" }}>
              <button className="btn" type="button" style={btnSm} onClick={refreshAll} disabled={snapshotLoading}>
                ↻ 새로고침
              </button>
              <button
                className="badge"
                type="button"
                style={{ border: 0, background: "none", cursor: "pointer" }}
                onClick={() => go("approval")}
              >
                승인 화면
              </button>
              {(canPause || canResume) && (
                <button
                  className={canResume ? "btn primary" : "btn"}
                  type="button"
                  style={btnSm}
                  disabled={actionBusy || !!archivePending}
                  aria-pressed={canResume}
                  onClick={() => {
                    void doControl(canResume ? "resume" : "pause");
                  }}
                >
                  {canResume ? "다시 시작" : "일시정지"}
                </button>
              )}
              {taskReady && (
                <button
                  className="btn danger"
                  type="button"
                  style={btnSm}
                  disabled={!canArchive || actionBusy || !!archivePending}
                  title={
                    canArchive
                      ? "작업과 증거의 비동기 보관을 요청합니다"
                      : "일시정지 또는 종료 상태이고 진행 요청이 없을 때 보관할 수 있어요"
                  }
                  onClick={() => {
                    setActionMsg(null);
                    setEndTarget({ id: taskId, name: title });
                  }}
                >
                  보관 요청
                </button>
              )}
              {(status === "done" || status === "failed" || status === "timeout") && (
                <button className="btn" type="button" style={btnSm} onClick={() => go("report")}>
                  보고서
                </button>
              )}
            </div>
          </div>

          {taskRes.error && <p role="alert">점검 정보를 불러오지 못했어요. 새로고침해 주세요.</p>}
          {actionMsg?.taskId === taskId && !endTarget && <p role="alert">{actionMsg.message}</p>}
          {currentArchive && (
            <div className="card" role="status">
              <p>
                보관 #{currentArchive.id} · {currentArchive.name} ·{" "}
                {(
                  {
                    archive_queued: "요청 접수 · 처리 대기",
                    archiving: "보관 처리 중",
                    archive_failed: "보관 처리 실패",
                    ready: "보관 완료",
                  } as Record<string, string>
                )[currentArchive.state] ?? "상태 미확인"}
              </p>
              <button
                className="btn"
                type="button"
                disabled={archiveCheckBusy}
                onClick={() => {
                  void checkArchive();
                }}
              >
                보관 상태 새로고침
              </button>
            </div>
          )}
          <p className="hint">점검 흐름 예시 · 단계별 진행 미연결</p>
          <div className="pipe">
            {PIPELINE.map((label, i) => (
              <Fragment key={label}>
                {i > 0 && <span className="d" />}
                <span className="p">{label}</span>
              </Fragment>
            ))}
          </div>
          <div style={{ fontSize: 13, color: "var(--muted)", marginBottom: 4 }}>
            검사 범위 <Strong>{covPct === null ? "—" : `${covPct}%`}</Strong>
            {covPct !== null && cov ? ` · ${cov.tested}/${cov.denominator} 자산 탐색` : " · 미확인"}
          </div>
          <div className="bar">
            <i style={{ width: `${covPct ?? 0}%` }} />
          </div>

          <div className="nowdoing" style={{ marginTop: 16 }}>
            {status === "running" ? <span className="spin" /> : null}
            <div style={{ minWidth: 0 }}>
              <div style={{ fontSize: 12, color: "var(--muted)" }}>상태</div>
              <div style={{ fontSize: 13.5, fontWeight: 600 }}>{statusLabel}</div>
            </div>
            <button
              className="btn"
              type="button"
              onClick={() => setDetailOpen(true)}
              style={{ marginLeft: "auto", flex: "none", ...btnSm }}
            >
              상세 보기
            </button>
          </div>

          <div className="dgrid" style={{ margin: "0 0 22px" }}>
            {/* 목업 유지: 예상 남은 시간(엔진 개발 후) */}
            <div className="tile">
              <div className="k">예상 남은 시간</div>
              <div className="v">
                {MOCK.etaRange}
                <span style={small}>분</span>
              </div>
              <div className="sub">{MOCK.elapsed} · 예시</div>
            </div>
            {/* 실데이터: 소모 토큰(실사용). 예상치 sub 는 목업 */}
            <div className="tile">
              <div className="k">소모 토큰</div>
              <div className="v">{tokenLabel}</div>
              <div className="sub">{MOCK.tokenEst} · 예시</div>
            </div>
            {/* 실데이터: 탐색한 자산 */}
            <div className="tile">
              <div className="k">탐색한 자산</div>
              <div className="v">{cov?.enabled ? cov.tested : "—"}</div>
              <div className="sub">{cov?.enabled ? `범위 ${cov.denominator}` : "집계 전"}</div>
            </div>
          </div>

          <div className="wrap" style={{ padding: 0, gap: 22 }}>
            <div className="col" style={{ flex: "1 1 300px" }}>
              <h3 style={{ fontSize: 14, marginBottom: 10 }}>
                발견 (심각도별) <Sub>· {findingsReady ? `총 ${findings.length}건` : "조회 중/미확인"}</Sub>
              </h3>
              <div className="row" style={{ flexWrap: "wrap", gap: 8, marginBottom: 8 }}>
                {SEV_ORDER.map((s) => (
                  <Sev key={s} level={s}>
                    {SEV_LABEL[s]} {findingsReady ? sevCounts[s] : "—"}
                  </Sev>
                ))}
                {findingsReady && unclassified > 0 && <Sev level={undefined}>미분류 {unclassified}</Sev>}
              </div>
              <div
                style={{
                  border: "1px solid var(--line)",
                  borderRadius: 12,
                  padding: "4px 14px",
                  marginTop: 8,
                }}
              >
                <div style={{ fontSize: 12, color: "var(--muted)", paddingTop: 11 }}>최근 발견</div>
                {findingRows}
              </div>
            </div>
            <div className="col" style={{ flex: "1 1 260px" }}>
              <h3 style={{ fontSize: 14, marginBottom: 10 }}>판정 결과</h3>
              <div className="vcard">
                {[
                  ["확정", "var(--teal-ink)"],
                  ["의심", "var(--mid)"],
                  ["판단 불가", "var(--muted)"],
                  ["판정 중", "var(--faint)"],
                ].map(([label, color]) => (
                  <div className="vbox" key={label}>
                    <div className="n" style={{ color, fontSize: 18 }}>
                      —
                    </div>
                    <div className="l">{label}</div>
                  </div>
                ))}
              </div>
              <div className="hint">
                확정·의심·판단 불가 <Strong>판정</Strong>은 E③ 오라클 연동 후 채워져요(증거 부족은 오탐 대신 판단 불가로
                남겨요).
              </div>
              <h3 style={{ fontSize: 14, margin: "18px 0 8px" }}>
                검사 커버리지 <Sub>· {cov?.enabled ? `${cov.tested} / ${cov.denominator} 자산` : "집계 전"}</Sub>
              </h3>
              {covPct !== null ? (
                <>
                  <div className="covgrid">
                    {cells.map((c, i) => (
                      // biome-ignore lint/suspicious/noArrayIndexKey: 와플 셀은 위치가 곧 키
                      <span key={i} className={`cc ${c}`} />
                    ))}
                  </div>
                  <div className="cov">
                    <span>
                      <span className="dot" style={{ background: "var(--teal-border)" }} />
                      탐색됨 · 판정 미연결
                    </span>
                    <span>
                      <span className="dot" style={{ background: "var(--line-2)" }} />
                      미탐색
                    </span>
                  </div>
                  <p className="hint">범위 내 자산의 탐색 비율이에요. 자산별 취약 판정은 아직 연결 전이에요.</p>
                </>
              ) : (
                <div style={{ fontSize: 13, color: "var(--muted)", padding: "8px 0" }}>{coverageMessage}</div>
              )}
            </div>
          </div>

          <div className="hr" />
          <div
            style={{
              display: "flex",
              alignItems: "center",
              justifyContent: "space-between",
              gap: 12,
              flexWrap: "wrap",
            }}
          >
            <div style={{ fontSize: 13, color: "var(--muted)" }}>
              활동 {activityLabel} · 상세는 <Strong>상세 보기</Strong>
            </div>
            <button className="btn" type="button" onClick={() => go("history")}>
              점검 기록 보기
            </button>
          </div>
        </div>
      </div>

      <LiveDetailModal
        open={detailOpen}
        onClose={() => setDetailOpen(false)}
        title={title}
        status={status}
        covPct={covPct}
        acts={acts}
        truncated={acts.length >= 300}
        loading={actRes.loading}
        error={actRes.error}
      />
      {endTarget?.id === taskId && nav.screen === "live" && (
        <Modal
          open
          onClose={() => {
            if (!actionBusy) setEndTarget(null);
          }}
        >
          <div className="rpop" role="dialog" aria-label="보관 요청 확인" style={{ maxWidth: 440 }}>
            <div className="rpop-head">
              <div className="rpt">보관을 요청할까요?</div>
              <button
                className="rpop-x"
                type="button"
                disabled={actionBusy}
                onClick={() => setEndTarget(null)}
                aria-label="닫기"
              >
                ✕
              </button>
            </div>
            <div style={{ padding: "16px 22px" }}>
              <p style={{ fontSize: 13.5, color: "var(--muted)", lineHeight: 1.7 }}>
                <Strong>{endTarget.name}</Strong> 작업과 증거의 보관을 요청해요. 접수 후 백그라운드에서 처리하며, 완료
                전에는 보관 성공이 아니에요. 완료된 보관은 별도 복원 기능으로 복원할 수 있어요.
              </p>
              {actionMsg?.taskId === endTarget.id && <p role="alert">{actionMsg.message}</p>}
            </div>
            <div className="vpop-foot">
              <button className="btn" type="button" disabled={actionBusy} onClick={() => setEndTarget(null)}>
                취소
              </button>
              <button
                className="btn danger"
                type="button"
                disabled={actionBusy || !canArchive}
                onClick={() => {
                  void doArchive();
                }}
              >
                {actionBusy ? "요청 중…" : "보관 요청"}
              </button>
            </div>
          </div>
        </Modal>
      )}
    </Screen>
  );
}
