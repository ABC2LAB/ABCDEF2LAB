import { type ReactNode, useMemo, useState } from "react";

import { Screen } from "../components/Screen";
import { TopBar } from "../components/TopBar";
import { Pill, RefreshIcon, Sev } from "../components/ui";
import { useApiResource } from "../lib/api";
import { cx } from "../lib/cx";
import {
  type EngineCoverage,
  type EngineTask,
  type FindingSeverity,
  getCoverage,
  getInterceptRules,
  getLLM,
  getTokenUsage,
  listPendingApprovals,
  listTasks,
} from "../lib/engine";
import { isLocalUrl } from "../lib/net";
import { useSelection } from "../lib/selection";
import { useApprovalTarget } from "../lib/approval";
import { useNav } from "../nav";
import { SEVERITY_LABEL, type Severity } from "../types";

const SEV_ORDER: Severity[] = ["critical", "high", "medium", "low"];
const STATUS_LABEL: Record<string, string> = {
  created: "대기",
  running: "진행 중",
  queued: "실행 대기",
  paused: "일시중지",
  done: "완료",
  failed: "실패",
  timeout: "시간초과",
};
/** 진행 중 목록의 상태 표시 색: 실행=파랑, 그 외(일시중지 · 대기)=주황 */
const STATUS_TAG: Record<string, string> = { running: "run" };
const ACTIVE = new Set(["running", "queued", "created", "paused"]);
const TERMINAL = new Set(["done", "failed", "timeout"]);
const NO_FINDINGS: FindingSeverity = { critical: 0, high: 0, medium: 0, low: 0 };
const DAY = 24 * 60 * 60 * 1000;
/** 발견 현황 · 토큰 타일의 기간 */
const WINDOW = 30 * DAY;
/** 실패 · 시간초과 점검을 '처리할 일'에 올리는 기간 */
const RECENT_FAIL = 7 * DAY;
/** 이 비율보다 덜 본 점검은 '미검사 영역'으로 처리할 일에 올린다 */
const LOW_COVERAGE = 0.8;
const LIST_MAX = 5;

function taskName(t: { name?: string } | undefined): string {
  return t?.name?.trim() || "이름 없는 점검";
}
/** 엔진이 findings 를 비워 보내는 점검이 있어서 선택값으로 받는다 */
function findingsOf(t: { findings?: FindingSeverity | null }): FindingSeverity {
  return t.findings ?? NO_FINDINGS;
}
function totalOf(f: FindingSeverity): number {
  return SEV_ORDER.reduce((n, s) => n + f[s], 0);
}
function timeOf(iso: string): number {
  const t = new Date(iso).getTime();
  return Number.isNaN(t) ? 0 : t;
}
function dateLabel(iso: string): string {
  const t = timeOf(iso);
  if (!t) return "";
  const d = new Date(t);
  return `${d.getMonth() + 1}/${d.getDate()}`;
}
function startLabel(iso: string): string {
  const t = timeOf(iso);
  if (!t) return "";
  const d = new Date(t);
  const hm = `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
  return new Date().toDateString() === d.toDateString() ? `오늘 ${hm} 시작` : `${dateLabel(iso)} ${hm} 시작`;
}
function ago(iso: string): string {
  const t = timeOf(iso);
  if (!t) return "";
  const min = Math.max(0, Math.round((Date.now() - t) / 60000));
  if (min < 1) return "방금";
  if (min < 60) return `${min}분 전`;
  const hr = Math.round(min / 60);
  if (hr < 24) return `${hr}시간 전`;
  return `${Math.round(hr / 24)}일 전`;
}
function duration(t: EngineTask): string {
  const ms = timeOf(t.completed_at) - timeOf(t.created_at);
  if (!t.completed_at || ms <= 0) return "";
  const min = Math.round(ms / 60000);
  return min < 60 ? `${min}분` : `${Math.floor(min / 60)}시간 ${min % 60}분`;
}
/** 끝난 시각(없으면 만든 시각) */
function endedAt(t: EngineTask): string {
  return t.completed_at || t.created_at;
}
/** 토큰 수 → [숫자, 단위] (예: 4_200_000 → ["4.2", "M"]) */
function tokenParts(n: number): [string, string] {
  if (n >= 1e9) return [(n / 1e9).toFixed(1), "B"];
  if (n >= 1e6) return [(n / 1e6).toFixed(1), "M"];
  if (n >= 1e3) return [(n / 1e3).toFixed(1), "K"];
  return [String(n), ""];
}
function tokenText(n: number): string {
  return tokenParts(n).join("");
}
function clock(ms: number): string {
  const d = new Date(ms);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}
const join = (parts: (string | false | null | undefined)[]) => parts.filter(Boolean).join(" · ");

function Tile({ k, v, sub, attn }: { k: string; v: ReactNode; sub: ReactNode; attn?: "warn" }) {
  return (
    <div className={cx("tile", attn && `attn-${attn}`)}>
      <div className="k">{k}</div>
      <div className="v">{v}</div>
      <div className="sub">{sub}</div>
    </div>
  );
}

type TodoKind = "approval" | "paused" | "unfinished" | "coverage";
type Todo = { key: string; kind: TodoKind; title: string; meta: string; action: ReactNode };
const TODO_KIND_LABEL: Record<Exclude<TodoKind, "approval">, string> = {
  paused: "일시중지",
  unfinished: "끝까지 못 감",
  coverage: "미검사 영역",
};

/** ===== 대시보드 (메인) ===== */
export default function DashboardScreen() {
  const { screen, go } = useNav();
  const { selectTask, tasksVersion } = useSelection();
  const { openApproval } = useApprovalTarget();
  // 화면은 언마운트되지 않고 숨겨지기만 한다. 그래서 대시보드에 들어올 때마다(active) 다시 조회하고,
  // 머무는 동안에는 [새로고침](refresh)으로 다시 읽는다. 숨겨져 있을 때는 조회하지 않는다.
  const active = screen === "dashboard";
  const [refresh, setRefresh] = useState(0);
  const when = <T,>(fetch: () => Promise<T>) => (active ? fetch() : Promise.resolve(null));
  const tasksRes = useApiResource(() => when(listTasks), [tasksVersion, active, refresh]);
  const pendingRes = useApiResource(() => when(listPendingApprovals), [tasksVersion, active, refresh]);
  const tokenRes = useApiResource(() => when(getTokenUsage), [tasksVersion, active, refresh]);
  const llmRes = useApiResource(() => when(getLLM), [active, refresh]);
  const rulesRes = useApiResource(() => when(getInterceptRules), [active, refresh]);

  const tasks = useMemo<EngineTask[]>(() => tasksRes.data?.tasks ?? [], [tasksRes.data]);
  const pending = useMemo(() => pendingRes.data ?? [], [pendingRes.data]);
  const byId = useMemo(() => new Map(tasks.map((t) => [t.id, t])), [tasks]);

  const stats = useMemo(() => {
    const since = Date.now() - WINDOW;
    const active = tasks.filter((t) => ACTIVE.has(t.status)).sort((a, b) => b.created_at.localeCompare(a.created_at));
    const finished = tasks.filter((t) => TERMINAL.has(t.status)).sort((a, b) => endedAt(b).localeCompare(endedAt(a)));
    const recent = tasks.filter((t) => timeOf(t.created_at) >= since);
    const sev: FindingSeverity = { ...NO_FINDINGS };
    for (const t of recent) for (const s of SEV_ORDER) sev[s] += findingsOf(t)[s];
    const ranked = recent
      .filter((t) => totalOf(findingsOf(t)) > 0)
      .sort((a, b) => {
        const fa = findingsOf(a);
        const fb = findingsOf(b);
        return fb.critical - fa.critical || fb.high - fa.high || totalOf(fb) - totalOf(fa);
      })
      .slice(0, 3);
    return { active, finished, recent, sev, total: totalOf(sev), ranked };
  }, [tasks]);

  // 최근 끝난 점검의 커버리지: 목록의 "커버리지 N%"와 처리할 일의 "미검사 영역"에 쓴다
  const shown = stats.finished.slice(0, LIST_MAX);
  const shownKey = shown.map((t) => t.id).join(",");
  const covRes = useApiResource(
    () => when(() => Promise.all(shown.map((t) => getCoverage(t.id).catch(() => null)))),
    [shownKey, tasksVersion, active, refresh],
  );
  const coverage = new Map<string, EngineCoverage>();
  shown.forEach((t, i) => {
    const c = covRes.data?.[i];
    if (c?.enabled && c.denominator > 0) coverage.set(t.id, c);
  });
  const covPct = (id: string) => {
    const c = coverage.get(id);
    return c ? Math.round((c.tested / c.denominator) * 100) : null;
  };

  const open = (id: string, screen: "live" | "report") => {
    selectTask(id);
    go(screen);
  };

  // ── 지금 처리할 일: 승인 대기 → 일시중지 → 최근 7일 안에 끝까지 못 간 점검 → 덜 본 점검
  const todos: Todo[] = [];
  for (const p of pending) {
    todos.push({
      key: `ap-${p.id}`,
      kind: "approval",
      title: p.task_id ? taskName(byId.get(p.task_id)) : "점검 미지정",
      meta: join([p.tool_name && `${p.tool_name} 호출`, "실행 전 승인 필요", ago(p.created_at)]),
      action: (
        <button
          className="btn xs primary"
          type="button"
          onClick={() => {
            if (p.task_id) selectTask(p.task_id);
            openApproval(p.id);
          }}
        >
          승인하기
        </button>
      ),
    });
  }
  for (const t of stats.active.filter((x) => x.status === "paused")) {
    todos.push({
      key: `pa-${t.id}`,
      kind: "paused",
      title: taskName(t),
      meta: join([startLabel(t.created_at), "멈춘 상태 · 이어서 진행할지 확인"]),
      action: (
        <button className="btn xs" type="button" onClick={() => open(t.id, "live")}>
          확인하기
        </button>
      ),
    });
  }
  const failSince = Date.now() - RECENT_FAIL;
  for (const t of stats.finished.filter(
    (x) => (x.status === "failed" || x.status === "timeout") && timeOf(endedAt(x)) >= failSince,
  )) {
    todos.push({
      key: `fa-${t.id}`,
      kind: "unfinished",
      title: taskName(t),
      meta: join([`${STATUS_LABEL[t.status]}로 끝까지 진행되지 않음`, ago(endedAt(t))]),
      action: (
        <button className="btn xs" type="button" onClick={() => open(t.id, "report")}>
          기록 보기
        </button>
      ),
    });
  }
  for (const t of shown.filter((x) => x.status === "done")) {
    const pct = covPct(t.id);
    if (pct === null || pct >= LOW_COVERAGE * 100) continue;
    todos.push({
      key: `cv-${t.id}`,
      kind: "coverage",
      title: taskName(t),
      meta: join(["미검사 영역 있음", `커버리지 ${pct}%`]),
      action: (
        <button className="btn xs" type="button" onClick={() => open(t.id, "report")}>
          보고서
        </button>
      ),
    });
  }
  const checks = todos.filter((t) => t.kind !== "approval");
  const checkKinds = [...new Set(checks.map((t) => t.kind as Exclude<TodoKind, "approval">))];

  const loadingTasks = tasksRes.loading && !tasksRes.data;
  const dash = (n: number) => (loadingTasks ? "—" : n);

  // ── 타일
  const count = (st: string) => stats.active.filter((t) => t.status === st).length;
  const activeSub =
    join([
      count("running") > 0 && `실행 ${count("running")}`,
      count("paused") > 0 && `일시중지 ${count("paused")}`,
      count("queued") + count("created") > 0 && `대기 ${count("queued") + count("created")}`,
    ]) || "지금 도는 점검 없음";
  let pendingSub: ReactNode = "결정할 요청 없음";
  if (pendingRes.error) pendingSub = "불러오지 못했어요";
  else if (pending.length > 0) pendingSub = "상태 변경 요청이 멈춰 있음";
  const pendingReady = !pendingRes.error && pendingRes.data !== null;

  const tokens = useMemo(() => {
    const daily = tokenRes.data?.daily;
    if (!daily) return null;
    const key = (ms: number) => new Date(ms).toISOString().slice(0, 10);
    const cut1 = key(Date.now() - WINDOW);
    const cut2 = key(Date.now() - 2 * WINDOW);
    let cur = 0;
    let prev = 0;
    for (const d of daily) {
      if (d.day > cut1) cur += d.input + d.output;
      else if (d.day > cut2) prev += d.input + d.output;
    }
    return { cur, change: prev > 0 ? Math.round(((cur - prev) / prev) * 100) : null };
  }, [tokenRes.data]);
  let tokenValue: ReactNode = "—";
  let tokenSub: ReactNode = tokenRes.error ? "불러오지 못했어요" : "불러오는 중";
  if (tokens) {
    const [num, unit] = tokenParts(tokens.cur);
    tokenValue = (
      <>
        {num}
        {unit && <span className="unit">{unit}</span>}
      </>
    );
    tokenSub =
      join([
        tokens.change !== null && `지난 30일 대비 ${tokens.change >= 0 ? "+" : ""}${tokens.change}%`,
        stats.recent.length > 0 &&
          tokens.cur > 0 &&
          `점검당 평균 ${tokenText(Math.round(tokens.cur / stats.recent.length))}`,
      ]) || "최근 30일 사용 없음";
  }

  const llm = llmRes.data;
  const rules = rulesRes.data ?? [];
  const rulesOn = rules.filter((r) => r.enabled);
  const rulesDeny = rulesOn.filter((r) => r.action === "deny").length;

  const resources = [tasksRes, pendingRes, tokenRes, llmRes, rulesRes];
  const reloading = resources.some((r) => r.loading);
  const fetched = resources.map((r) => r.fetchedAt).filter((t): t is number => t !== null);
  let snapshot = "조회 중…";
  if (!reloading && resources.some((r) => r.error)) snapshot = "일부 조회 실패 · 새로고침해 주세요";
  else if (!reloading && fetched.length) snapshot = `조회 ${clock(Math.min(...fetched))}`;

  return (
    <Screen id="dashboard">
      <div className="frame">
        <TopBar current="dashboard" />
        <div className="dash">
          <div className="dash-head">
            <h1 className="big" style={{ marginBottom: 0 }}>
              대시보드
            </h1>
            <div className="dash-acts">
              <span className="dash-snap">{snapshot}</span>
              <button className="btn" type="button" onClick={() => setRefresh((n) => n + 1)} disabled={reloading}>
                <RefreshIcon /> 새로고침
              </button>
              <button className="btn primary" type="button" onClick={() => go("entry")}>
                + 새 점검
              </button>
            </div>
          </div>

          {tasksRes.error && (
            <div className="hcard dash-err">
              <div style={{ minWidth: 0 }}>
                <div className="nm">점검 목록을 불러오지 못했어요</div>
                <div className="meta">{tasksRes.error}</div>
              </div>
              <div className="acts">
                <button className="btn xs" type="button" onClick={tasksRes.reload}>
                  다시 시도
                </button>
              </div>
            </div>
          )}

          <div className="dgrid dash-tiles">
            <Tile k="진행 중인 점검" v={dash(stats.active.length)} sub={loadingTasks ? "불러오는 중" : activeSub} />
            <Tile
              k="승인 대기"
              v={pendingReady ? pending.length : "—"}
              sub={pendingSub}
              attn={pending.length > 0 ? "warn" : undefined}
            />
            <Tile
              k="확인 필요"
              v={dash(checks.length)}
              sub={loadingTasks ? "불러오는 중" : join(checkKinds.map((k) => TODO_KIND_LABEL[k])) || "확인할 점검 없음"}
            />
            <Tile k="토큰 사용량 · 최근 30일" v={tokenValue} sub={tokenSub} />
          </div>

          <div className="dash-cols">
            <div className="dash-main">
              {/* 지금 처리할 일 */}
              <section className="dash-sec" aria-labelledby="dash-todo">
                <div className="recent-head">
                  <span id="dash-todo">지금 처리할 일</span>
                  {todos.length > 0 && <span className="dash-cnt">{todos.length}건</span>}
                </div>
                {todos.length === 0 ? (
                  <div className="dash-empty">{loadingTasks ? "불러오는 중…" : "지금 처리할 일이 없어요."}</div>
                ) : (
                  <div className="recent-list">
                    {todos.map((t) => (
                      <div className="recent-row" key={t.key}>
                        <div className="recent-main">
                          <div className="nm">{t.title}</div>
                          <div className="meta">{t.meta}</div>
                        </div>
                        <div className="recent-acts">
                          {t.kind === "approval" ? (
                            <span className="stag wait">승인 대기</span>
                          ) : (
                            <span className="stag none">확인 필요</span>
                          )}
                          {t.action}
                        </div>
                      </div>
                    ))}
                  </div>
                )}
              </section>

              {/* 진행 중인 점검 */}
              <section className="dash-sec" aria-labelledby="dash-active">
                <div className="recent-head">
                  <span id="dash-active">진행 중인 점검</span>
                  {stats.active.length > LIST_MAX && (
                    <button type="button" className="lnk" onClick={() => go("history")}>
                      {stats.active.length}개 모두 보기
                    </button>
                  )}
                </div>
                {stats.active.length === 0 ? (
                  <div className="dash-empty">{loadingTasks ? "불러오는 중…" : "진행 중인 점검이 없어요."}</div>
                ) : (
                  <div className="recent-list">
                    {stats.active.slice(0, LIST_MAX).map((t) => {
                      const pct = t.goals_total > 0 ? Math.round(Math.min(1, t.goals_met / t.goals_total) * 100) : null;
                      return (
                        <div className="recent-row" key={t.id}>
                          <div className="recent-main">
                            <div className="nm">{taskName(t)}</div>
                            <div className="meta">
                              {join([
                                startLabel(t.created_at),
                                t.goals_total > 0 && `목표 ${t.goals_met}/${t.goals_total}`,
                                t.in_flight > 0 && `작업 ${t.in_flight}개 실행 중`,
                              ])}
                            </div>
                            {pct !== null && (
                              <div
                                className={cx("bar dash-prog", t.status !== "running" && "paused")}
                                role="progressbar"
                                aria-label={`${taskName(t)} 목표 달성`}
                                aria-valuemin={0}
                                aria-valuemax={100}
                                aria-valuenow={pct}
                              >
                                <i style={{ width: `${pct}%` }} />
                              </div>
                            )}
                          </div>
                          <div className="recent-acts">
                            <span className={cx("stag", STATUS_TAG[t.status] ?? "wait")}>
                              {STATUS_LABEL[t.status] ?? t.status}
                            </span>
                            <button className="btn xs" type="button" onClick={() => open(t.id, "live")}>
                              이어 보기
                            </button>
                          </div>
                        </div>
                      );
                    })}
                  </div>
                )}
              </section>

              {/* 최근 끝난 점검 */}
              <section className="dash-sec" aria-labelledby="dash-done">
                <div className="recent-head">
                  <span id="dash-done">최근 끝난 점검</span>
                  {tasks.length > 0 && (
                    <button type="button" className="lnk" onClick={() => go("history")}>
                      전체 기록 {tasks.length}
                    </button>
                  )}
                </div>
                {stats.finished.length === 0 ? (
                  <div className="dash-empty">{loadingTasks ? "불러오는 중…" : "아직 끝난 점검이 없어요."}</div>
                ) : (
                  <div className="recent-list">
                    {shown.map((t) => {
                      const f = findingsOf(t);
                      const top = SEV_ORDER.filter((s) => f[s] > 0).slice(0, 2);
                      const pct = covPct(t.id);
                      let badges: ReactNode;
                      if (t.status !== "done") badges = <span className="stag fail">{STATUS_LABEL[t.status]}</span>;
                      else if (top.length)
                        badges = top.map((s) => <Sev key={s} level={s}>{`${SEVERITY_LABEL[s]} ${f[s]}`}</Sev>);
                      else badges = <span className="stag none">발견 없음</span>;
                      return (
                        <div className="recent-row" key={t.id}>
                          <div className="recent-main">
                            <div className="nm">{taskName(t)}</div>
                            <div className="meta">
                              {join([dateLabel(endedAt(t)), duration(t), pct !== null && `커버리지 ${pct}%`])}
                            </div>
                          </div>
                          <div className="recent-acts">
                            {badges}
                            <button className="btn xs" type="button" onClick={() => open(t.id, "report")}>
                              보고서
                            </button>
                          </div>
                        </div>
                      );
                    })}
                  </div>
                )}
              </section>
            </div>

            <div className="dash-side">
              {/* 발견 현황: 막대는 전체 발견 대비 비율(가장 큰 값 기준이 아님) */}
              <div className="card">
                <h3>
                  발견 현황 <span className="dash-h3sub">최근 30일{!loadingTasks && ` · 총 ${stats.total}건`}</span>
                </h3>
                <div className="dash-sev">
                  {SEV_ORDER.map((s) => (
                    <div className="dash-sevrow" key={s}>
                      <Sev level={s} />
                      <div className="bar" aria-hidden="true">
                        <i
                          className={s}
                          style={{ width: `${stats.total ? Math.round((stats.sev[s] / stats.total) * 100) : 0}%` }}
                        />
                      </div>
                      <span className="n">{dash(stats.sev[s])}</span>
                    </div>
                  ))}
                </div>
                {stats.ranked.length > 0 && (
                  <>
                    <div className="dash-sublab">발견이 많은 점검</div>
                    {stats.ranked.map((t) => {
                      const f = findingsOf(t);
                      const top = SEV_ORDER.find((s) => f[s] > 0);
                      return (
                        <button key={t.id} type="button" className="dash-link" onClick={() => open(t.id, "report")}>
                          <span className="nm">
                            {taskName(t)}
                            {t.status === "running" && <span className="dash-run">진행 중</span>}
                          </span>
                          <span className="cnt">
                            {top && <Sev level={top}>{`${SEVERITY_LABEL[top]} ${f[top]}`}</Sev>}
                            <span className="dash-tot">총 {totalOf(f)}</span>
                          </span>
                        </button>
                      );
                    })}
                  </>
                )}
              </div>

              {/* 운영 상태 */}
              <div className="card">
                <h3>운영 상태</h3>
                <div className="srow">
                  <div className="k">분석 모델</div>
                  <div className="v">
                    {llmRes.loading && !llm && "불러오는 중…"}
                    {llmRes.error && "불러오지 못했어요"}
                    {llm && !llm.configured && "아직 연결 안 됨"}
                    {llm?.configured && (
                      <>
                        {llm.model || "모델명 미지정"}{" "}
                        <Pill tone={isLocalUrl(llm.base_url) ? undefined : "warn"}>
                          {isLocalUrl(llm.base_url) ? "로컬" : "외부"}
                        </Pill>
                      </>
                    )}
                  </div>
                </div>
                <div className="srow">
                  <div className="k">통제 규칙</div>
                  <div className="v">
                    {rulesRes.loading && !rulesRes.data && "불러오는 중…"}
                    {rulesRes.error && "불러오지 못했어요"}
                    {rulesRes.data && join([`켜진 규칙 ${rulesOn.length}개`, rulesDeny > 0 && `차단 ${rulesDeny}`])}
                  </div>
                </div>
                <button className="btn xs" type="button" onClick={() => go("settings")}>
                  통제 정책 열기
                </button>
              </div>
            </div>
          </div>
        </div>
      </div>
    </Screen>
  );
}
