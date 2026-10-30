import { Fragment, useMemo, useState } from "react";
import { Modal } from "../components/Modal";
import { Screen } from "../components/Screen";
import { TopBar } from "../components/TopBar";
import { Sev, Strong, Sub } from "../components/ui";
import { useApiResource } from "../lib/api";
import {
  type EngineActivity,
  type EngineCoverage,
  type EngineFinding,
  type EngineTask,
  type EngineTokens,
  archiveTask,
  controlTask,
  getActivity,
  getCoverage,
  getTask,
  getTokens,
  listTaskFindings,
  listTasks,
} from "../lib/engine";
import { cx } from "../lib/cx";
import { useSelection } from "../lib/selection";
import { useGo } from "../nav";
import type { Severity, Target } from "../types";

/* ── 목업(엔진 개발 후 실데이터로) ─────────────────────────────────
 * 예상 남은 시간 / 예상 토큰은 추정치라 엔진 작업 후 추가 예정 — 지금은 목업 유지.
 * 그 외 슬롯(진행바·토큰 실사용·발견·커버리지·활동)은 실데이터로 배선.         */
const MOCK = { etaRange: "12~15", elapsed: "경과 18분", tokenEst: "예상 1.8~2.1M" } as const;

const PIPELINE = ["자산 탐색", "범위 확인", "점검 계획", "취약점 점검", "검증", "마무리", "보고서"];
// 실 단계 데이터는 아직 없어 status 로 근사(슬롯은 실데이터 들어오면 교체).
function pipelineNow(status: string): number {
  if (status === "done" || status === "failed") return 6;
  if (status === "running" || status === "paused" || status === "timeout") return 3;
  return 1;
}

const STATUS_LABEL: Record<string, string> = {
  created: "대기",
  running: "진행 중",
  paused: "일시중지",
  done: "완료",
  failed: "실패",
  timeout: "시간초과",
};

const SEV_ORDER: Severity[] = ["critical", "high", "medium", "low"];
const SEV_SET = new Set<string>(SEV_ORDER);
const SEV_LABEL: Record<Severity, string> = { critical: "심각", high: "높음", medium: "중간", low: "낮음" };
function toSeverity(raw: string): Severity {
  return SEV_SET.has(raw) ? (raw as Severity) : "low";
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

/** 실 커버리지(분모·탐색수·발견수)를 셀 그리드로(와플). per-endpoint 판정은 없어 비율 근사:
 *  발견수만큼 'f', 나머지 탐색분 'o', 나머지 'n'. 숫자 라벨은 실값. */
function coverageCells(denominator: number, tested: number, found: number, cap = 48): string[] {
  const n = Math.min(denominator, cap) || 0;
  const scale = denominator > cap ? cap / denominator : 1;
  const t = Math.min(Math.round(tested * scale), n);
  const f = Math.min(Math.round(found * scale), t);
  return Array.from({ length: n }, (_, i) => (i < f ? "f" : i < t ? "o" : "n"));
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
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  status: string;
  covPct: number;
  acts: EngineActivity[];
  truncated: boolean;
}) {
  return (
    <Modal open={open} onClose={onClose}>
      <div className="rpop" role="dialog" aria-label="활동 로그">
        <div className="rpop-head">
          <div style={{ minWidth: 0, flex: 1 }}>
            <div className="rpt">{title}</div>
            <div className="rpbar">
              <i style={{ width: `${covPct}%` }} />
            </div>
            <div className="rpd">
              검사 범위 {covPct}% · 활동 {acts.length}건{truncated ? " (앞부분)" : ""}
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
            <div className="rlog">
              {acts.length === 0 ? (
                <div style={{ color: "var(--muted)", fontSize: 13, padding: "12px 0" }}>기록된 활동이 없어요.</div>
              ) : (
                acts.map((e) => (
                  <div className="rrow" key={e.seq}>
                    <span className="rt2">{tsLabel(e.ts)}</span>
                    <span className={`rdot ${actDot(e)}`} />
                    <span className="rx">
                      {e.worker ? `${e.worker} · ` : ""}
                      {e.summary || e.kind}
                    </span>
                  </div>
                ))
              )}
            </div>
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
  const go = useGo();
  const { taskId, selectTask } = useSelection();
  const [detailOpen, setDetailOpen] = useState(false);
  const [endOpen, setEndOpen] = useState(false);
  const [actionBusy, setActionBusy] = useState(false);
  const [actionMsg, setActionMsg] = useState<string | null>(null);
  const [fetchedAt, setFetchedAt] = useState(() => Date.now());

  const taskRes = useApiResource(() => (taskId ? getTask(taskId) : Promise.resolve(null)), [taskId]);
  const findRes = useApiResource<EngineFinding[]>(() => listTaskFindings(taskId ?? ""), [taskId]);
  const covRes = useApiResource<EngineCoverage | null>(() => getCoverage(taskId ?? ""), [taskId]);
  const tokRes = useApiResource<EngineTokens>(() => getTokens(taskId ?? ""), [taskId]);
  const actRes = useApiResource<EngineActivity[]>(() => getActivity(taskId ?? ""), [taskId]);
  const tasksRes = useApiResource(listTasks);

  const task = taskRes.data;
  const findings = useMemo(() => findRes.data ?? [], [findRes.data]);
  const cov = covRes.data;
  const acts = useMemo(() => [...(actRes.data ?? [])].sort((a, b) => a.seq - b.seq), [actRes.data]);

  const sevCounts = useMemo(() => {
    const c: Record<Severity, number> = { critical: 0, high: 0, medium: 0, low: 0 };
    for (const f of findings) c[toSeverity(f.severity)] += 1;
    return c;
  }, [findings]);

  const status = task?.status ?? "";
  const title = task?.name?.trim() || target.url || "진행 상황";
  const covPct = cov && cov.denominator > 0 ? Math.round((cov.tested / cov.denominator) * 100) : 0;
  const tokenTotal = (tokRes.data?.input ?? 0) + (tokRes.data?.output ?? 0);
  const cells = cov ? coverageCells(cov.denominator, cov.tested, findings.length) : [];

  // 데이터는 task 선택 시 한 번 조회한 스냅샷 — 자동 갱신 미연결(후속). 수동 새로고침만.
  function refreshAll() {
    taskRes.reload();
    findRes.reload();
    covRes.reload();
    tokRes.reload();
    actRes.reload();
    tasksRes.reload();
    setFetchedAt(Date.now());
  }
  const terminal = status === "done" || status === "failed" || status === "timeout";

  // 진행 중인 작업 탭 = 종료되지 않은(실행/일시정지/대기) task 들
  const jobs = useMemo<EngineTask[]>(
    () => (tasksRes.data?.tasks ?? []).filter((t) => !["done", "failed", "timeout"].includes(t.status)),
    [tasksRes.data],
  );
  const allFindings = useMemo(
    () =>
      (tasksRes.data?.tasks ?? []).reduce(
        (s, t) => s + (t.findings ? t.findings.critical + t.findings.high + t.findings.medium + t.findings.low : 0),
        0,
      ),
    [tasksRes.data],
  );

  // 일시정지/다시 시작·종료는 엔진 상태 변경(running/paused 인 task에만 유효) — 완료된 task엔 409가 날 수 있다.
  async function doControl(action: "pause" | "resume") {
    if (!taskId) return;
    setActionBusy(true);
    setActionMsg(null);
    try {
      await controlTask(taskId, action);
      taskRes.reload();
      tasksRes.reload();
    } catch (e) {
      setActionMsg(e instanceof Error ? e.message : "제어에 실패했어요.");
    } finally {
      setActionBusy(false);
    }
  }
  async function doArchive() {
    if (!taskId) return;
    setActionBusy(true);
    setActionMsg(null);
    try {
      await archiveTask(taskId);
      setEndOpen(false);
      tasksRes.reload();
      go("history");
    } catch (e) {
      setActionMsg(e instanceof Error ? e.message : "종료에 실패했어요.");
    } finally {
      setActionBusy(false);
    }
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

  return (
    <Screen id="live">
      <div className="frame">
        <TopBar current="history" />
        <div style={{ padding: "22px 24px" }}>
          {jobs.length > 0 && (
            <div className="jobtabs" role="tablist" aria-label="진행 중인 작업">
              <span className="jt-lab">진행 중인 작업 {jobs.length}</span>
              {jobs.map((t) => {
                const on = t.id === taskId;
                return (
                  <button
                    key={t.id}
                    type="button"
                    role="tab"
                    aria-selected={on}
                    className={`jt${on ? " on" : ""}`}
                    onClick={() => selectTask(t.id)}
                  >
                    <span className={`jt-dot ${t.status === "running" ? "run" : "wait"}`} />
                    <span className="jt-name">{t.name?.trim() || "이름 없는 점검"}</span>
                    <span className="jt-sub">{STATUS_LABEL[t.status] ?? t.status}</span>
                  </button>
                );
              })}
              <span className="jt-total">
                전체 작업 발견 <b>{allFindings}</b>건
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
                조회 {tsLabel(fetchedAt)} · 자동 갱신 미연결(스냅샷 — 새로고침 눌러 갱신)
              </div>
            </div>
            <div className="row" style={{ flexWrap: "wrap" }}>
              <button className="btn" type="button" style={btnSm} onClick={refreshAll}>
                ↻ 새로고침
              </button>
              {/* biome-ignore lint/a11y/useKeyWithClickEvents: 원본 시안 badge 이동 유지 */}
              <span className="badge" onClick={() => go("approval")}>
                승인 대기
              </span>
              {terminal ? (
                <button className="btn" type="button" style={btnSm} onClick={() => go("report")}>
                  보고서
                </button>
              ) : (
                <>
                  <button
                    className={`btn${status === "paused" ? " primary" : ""}`}
                    type="button"
                    style={btnSm}
                    disabled={actionBusy}
                    aria-pressed={status === "paused"}
                    onClick={() => doControl(status === "paused" ? "resume" : "pause")}
                  >
                    {status === "paused" ? "다시 시작" : "일시정지"}
                  </button>
                  <button
                    className="btn danger"
                    type="button"
                    style={btnSm}
                    disabled={actionBusy}
                    onClick={() => setEndOpen(true)}
                  >
                    종료
                  </button>
                </>
              )}
            </div>
          </div>
          {actionMsg && (
            <div
              style={{
                fontSize: 12.5,
                color: "var(--warn)",
                background: "var(--warn-weak)",
                border: "1px solid var(--warn-border)",
                borderRadius: 8,
                padding: "8px 12px",
                marginBottom: 12,
              }}
            >
              {actionMsg}
            </div>
          )}

          <div className="pipe">
            {PIPELINE.map((label, i) => {
              const now = pipelineNow(status);
              return (
                <Fragment key={label}>
                  {i > 0 && <span className={cx("d", i <= now && "on")} />}
                  <span className={cx("p", i < now && "done", i === now && "now")}>{label}</span>
                </Fragment>
              );
            })}
          </div>
          <div style={{ fontSize: 13, color: "var(--muted)", marginBottom: 4 }}>
            검사 범위 <Strong>{covPct}%</Strong>
            {cov && cov.denominator > 0 ? ` · ${cov.tested}/${cov.denominator} 자산 탐색` : ""}
          </div>
          <div className="bar">
            <i style={{ width: `${covPct}%` }} />
          </div>

          <div className="nowdoing" style={{ marginTop: 16 }}>
            {status === "running" ? <span className="spin" /> : null}
            <div style={{ minWidth: 0 }}>
              <div style={{ fontSize: 12, color: "var(--muted)" }}>상태</div>
              <div style={{ fontSize: 13.5, fontWeight: 600 }}>
                {status === "running"
                  ? "점검 진행 중"
                  : status === "done"
                    ? "점검 완료 — 아래 결과·보고서를 확인하세요"
                    : (STATUS_LABEL[status] ?? status) || "—"}
              </div>
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
              <div className="v">{tokRes.loading ? "…" : fmtTokens(tokenTotal)}</div>
              <div className="sub">{MOCK.tokenEst} · 예시</div>
            </div>
            {/* 실데이터: 탐색한 자산 */}
            <div className="tile">
              <div className="k">탐색한 자산</div>
              <div className="v">{cov ? cov.tested : "—"}</div>
              <div className="sub">{cov ? `범위 ${cov.denominator}` : "집계 전"}</div>
            </div>
          </div>

          <div className="wrap" style={{ padding: 0, gap: 22 }}>
            <div className="col" style={{ flex: "1 1 300px" }}>
              <h3 style={{ fontSize: 14, marginBottom: 10 }}>
                발견 (심각도별) <Sub>· 총 {findings.length}건</Sub>
              </h3>
              <div className="row" style={{ flexWrap: "wrap", gap: 8, marginBottom: 8 }}>
                {SEV_ORDER.map((s) => (
                  <Sev key={s} level={s}>
                    {SEV_LABEL[s]} {sevCounts[s]}
                  </Sev>
                ))}
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
                {findRes.loading ? (
                  <div style={{ padding: "12px 0", color: "var(--muted)", fontSize: 13 }}>불러오는 중…</div>
                ) : findings.length === 0 ? (
                  <div style={{ padding: "12px 0", color: "var(--muted)", fontSize: 13 }}>아직 발견이 없어요.</div>
                ) : (
                  findings.slice(0, 4).map((f) => (
                    // biome-ignore lint/a11y/useKeyWithClickEvents: 원본 시안 .find 클릭 이동 유지
                    <div key={f.id} className="find" onClick={() => go("report")} style={{ cursor: "pointer" }}>
                      <Sev level={toSeverity(f.severity)} />
                      <span className="nm">{f.name || f.vulnclass || "이름 없는 발견"}</span>
                    </div>
                  ))
                )}
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
                검사 커버리지 <Sub>· {cov ? `${cov.tested} / ${cov.denominator} 자산` : "집계 전"}</Sub>
              </h3>
              {cov && cov.denominator > 0 ? (
                <>
                  <div className="covgrid">
                    {cells.map((c, i) => (
                      // biome-ignore lint/suspicious/noArrayIndexKey: 와플 셀은 위치가 곧 키
                      <span key={i} className={`cc ${c}`} />
                    ))}
                  </div>
                  <div className="cov">
                    <span>
                      <span className="dot" style={{ background: "var(--teal)" }} />
                      검사 · 발견
                    </span>
                    <span>
                      <span className="dot" style={{ background: "var(--teal-border)" }} />
                      검사 · 이상 없음
                    </span>
                    <span>
                      <span className="dot" style={{ background: "var(--line-2)" }} />
                      미검사
                    </span>
                  </div>
                </>
              ) : (
                <div style={{ fontSize: 13, color: "var(--muted)", padding: "8px 0" }}>커버리지 집계 전이에요.</div>
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
              활동 {acts.length}건 기록됨 · 상세는 <Strong>상세 보기</Strong>
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
      />

      {endOpen && (
        <Modal open={endOpen} onClose={() => setEndOpen(false)}>
          <div className="rpop" role="dialog" aria-label="점검 종료 확인" style={{ maxWidth: 440 }}>
            <div className="rpop-head">
              <div style={{ minWidth: 0 }}>
                <div className="rpt">점검을 종료할까요?</div>
              </div>
              <button className="rpop-x" type="button" onClick={() => setEndOpen(false)} aria-label="닫기">
                ✕
              </button>
            </div>
            <div style={{ padding: "16px 22px" }}>
              <p style={{ fontSize: 13.5, color: "var(--muted)", lineHeight: 1.7, margin: 0 }}>
                <Strong>{title}</Strong> 점검을 종료(보관)해요. 지금까지의 증거는 보존되고 미검사 항목은 미검사로
                남아요. 되돌릴 수 없어요.
              </p>
            </div>
            <div className="vpop-foot">
              <button className="btn" type="button" onClick={() => setEndOpen(false)} disabled={actionBusy}>
                취소
              </button>
              <button className="btn danger" type="button" onClick={doArchive} disabled={actionBusy}>
                {actionBusy ? "종료 중…" : "종료"}
              </button>
            </div>
          </div>
        </Modal>
      )}
    </Screen>
  );
}
