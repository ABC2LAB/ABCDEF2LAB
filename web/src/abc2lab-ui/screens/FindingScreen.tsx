import { type ReactNode, useMemo } from "react";

import { Screen } from "../components/Screen";
import { TopBar } from "../components/TopBar";
import { Pill, Sev, Strong, Sub } from "../components/ui";
import { useApiResource } from "../lib/api";
import {
  type EngineFinding,
  type EngineFindingRetest,
  type EngineFindingTraffic,
  getFinding,
  getFindingTraffic,
  getTask,
  listFindingRetests,
} from "../lib/engine";
import { redactInput } from "../lib/redact";
import { useSelection } from "../lib/selection";
import { useNav } from "../nav";
import type { Severity } from "../types";

const bodyText = { fontSize: 14, color: "var(--muted)", lineHeight: 1.7, margin: 0 } as const;
const cellSub = { color: "var(--muted)", fontSize: 13 } as const;
const reqMethod = { fontFamily: "var(--mono)", fontSize: 13, fontWeight: 600 } as const;
const reqUrl = { fontFamily: "var(--mono)", fontSize: 12, color: "var(--muted)", overflowWrap: "anywhere" } as const;
/** .ev 는 white-space: pre — 에이전트가 쓴 긴 줄은 화면 폭에서 접는다 */
const evText = { whiteSpace: "pre-wrap", overflowWrap: "anywhere" } as const;

const SEVERITIES = new Set<string>(["critical", "high", "medium", "low"]);

/** 발견의 처리 상태(엔진 findings.status). E③ 판정(확정 · 의심 · 판단 불가)과는 다른 값이다 */
const STATUS_LABEL: Record<string, string> = {
  pending: "검토 전",
  in_progress: "검토 중",
  confirmed: "검토에서 취약점으로 확인",
  resolved: "처리됨",
  fixed: "수정됨",
  false_positive: "오탐",
  ignored: "무시",
  duplicate: "중복",
  risk_accepted: "위험 수용",
};
const ROLE_LABEL: Record<string, string> = {
  baseline: "기준 요청",
  proof: "증명",
  verification: "검증",
  supporting: "참고",
};
const RETEST_STATUS: Record<string, string> = {
  pending: "대기",
  running: "진행 중",
  completed: "완료",
  failed: "실패",
  stopped: "중지",
};
const RETEST_VERDICT: Record<string, string> = {
  reproduced: "재현됨",
  fixed: "고쳐짐",
  inconclusive: "결론 못 냄",
};

function findingName(f: EngineFinding): string {
  if (f.name) return f.name;
  if (f.vulnclass) return f.vulnclass;
  return "이름 없는 발견";
}
function when(iso: string | null | undefined): string {
  if (!iso) return "";
  const dt = new Date(iso);
  if (Number.isNaN(dt.getTime())) return "";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${dt.getMonth() + 1}/${dt.getDate()} ${p(dt.getHours())}:${p(dt.getMinutes())}`;
}
function size(n: number): string {
  if (n >= 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)}MB`;
  if (n >= 1024) return `${(n / 1024).toFixed(1)}KB`;
  return `${n}B`;
}

/** ===== 발견 상세 — 엔진이 기록한 발견 1건 (보고서의 [자세히 보기]로 들어온다) ===== */
export default function FindingScreen() {
  const { screen, go } = useNav();
  const { finding, taskId } = useSelection();
  // 화면은 숨겨져 있어도 마운트돼 있다. 보일 때만 조회하고, 다시 들어올 때마다 새로 읽는다.
  const active = screen === "finding";
  const ctxTask = finding?.task_id ? finding.task_id : taskId;
  // 상세 · 요청 기록 · 재검증은 발견 표 id(finding_id)로만 조회된다
  const fid = finding?.finding_id ?? "";
  const when$ = <T,>(ok: boolean, fetch: () => Promise<T>) => (active && ok ? fetch() : Promise.resolve(null));

  const detailRes = useApiResource(() => when$(!!fid, () => getFinding(fid, ctxTask)), [fid, ctxTask, active]);
  const taskRes = useApiResource(() => when$(!!ctxTask, () => getTask(ctxTask ?? "")), [ctxTask, active]);
  const detail = detailRes.data;
  const trafficCount = detail?.traffic_count ?? 0;
  const trafficRes = useApiResource(
    () => when$(!!fid && trafficCount > 0, () => getFindingTraffic(fid, ctxTask)),
    [fid, ctxTask, active, trafficCount],
  );
  const retestRes = useApiResource(() => when$(!!fid, () => listFindingRetests(fid)), [fid, active]);

  // 목록에서 넘어온 값을 먼저 보여 주고, 상세가 오면 그 값으로 덮는다
  const f: EngineFinding | null = detail ?? finding ?? null;
  const evidence = useMemo(() => redactInput(f?.evidence ?? ""), [f?.evidence]);
  const report = useMemo(() => redactInput(detail?.report ?? ""), [detail?.report]);

  const task = taskRes.data;
  const taskLabel = task ? task.name || task.description || `점검 #${task.id}` : "보고서";

  if (!f) {
    return (
      <Screen id="finding">
        <div className="frame">
          <TopBar current="history" />
          <div style={{ padding: "22px 24px", maxWidth: 760 }}>
            <div className="crumb">
              <CrumbLink to="history" go={go}>
                점검 기록
              </CrumbLink>{" "}
              › <span>발견</span>
            </div>
            <h1 className="big">발견 상세</h1>
            <p className="lead">보고서의 발견 목록에서 항목을 펼친 뒤 [자세히 보기]를 누르면 여기서 열려요.</p>
            <button className="btn" type="button" onClick={() => go("history")}>
              점검 기록으로
            </button>
          </div>
        </div>
      </Screen>
    );
  }

  const sev = SEVERITIES.has(f.severity) ? (f.severity as Severity) : undefined;
  const assets = f.assets ?? [];
  return (
    <Screen id="finding">
      <div className="frame">
        <TopBar current="history" />
        <div style={{ padding: "22px 24px", maxWidth: 760 }}>
          <div className="crumb">
            <CrumbLink to="history" go={go}>
              점검 기록
            </CrumbLink>{" "}
            ›{" "}
            <CrumbLink to="report" go={go}>
              {taskLabel}
            </CrumbLink>{" "}
            › <span>발견</span>
          </div>
          <div className="row" style={{ alignItems: "center", gap: 8, marginBottom: 6, flexWrap: "wrap" }}>
            <Sev level={sev} />
            {f.vulnclass && <Pill tone="muted">{f.vulnclass}</Pill>}
            <Pill tone="muted">처리 상태 · {STATUS_LABEL[f.status] ?? f.status}</Pill>
            {f.inherited && <Pill tone="warn">연결된 점검에서 물려받음</Pill>}
          </div>
          <h1 className="big" style={{ marginBottom: 4 }}>
            {findingName(f)}
          </h1>
          {assets.length > 0 && (
            <div className="ep" style={{ fontFamily: "var(--mono)", fontSize: 13, color: "var(--muted)" }}>
              {assets[0].label}
              {assets.length > 1 && ` 외 ${assets.length - 1}개`}
            </div>
          )}
          <p className="hint" style={{ marginTop: 10 }}>
            에이전트가 기록한 내용이에요. 확정 · 의심 · 판단 불가 <Strong>판정</Strong>은 E③ 오라클 연동 후 표시돼요.
          </p>

          {detailRes.error && (
            <div className="hcard dash-err" style={{ marginTop: 12 }}>
              <div style={{ minWidth: 0 }}>
                <div className="nm">발견 상세를 불러오지 못했어요</div>
                <div className="meta">{detailRes.error}</div>
              </div>
              <div className="acts">
                <button className="btn xs" type="button" onClick={detailRes.reload}>
                  다시 시도
                </button>
              </div>
            </div>
          )}

          <div className="card" style={{ marginTop: 16 }}>
            <h3>무엇이 문제인가</h3>
            <p style={bodyText}>{f.summary || "요약이 없어요."}</p>
          </div>

          <div className="card">
            <h3>
              증거 <Sub>· 에이전트 기록 · 쿠키 · 토큰 값은 가려서 보여 줘요</Sub>
            </h3>
            {evidence ? (
              <div className="ev" style={evText}>
                {evidence}
              </div>
            ) : (
              <p style={bodyText}>에이전트가 남긴 증거가 없어요.</p>
            )}
          </div>

          {!fid ? (
            <div className="card" style={{ marginBottom: 0 }}>
              <h3>요청 기록 · 보고서 · 재검증</h3>
              <p style={bodyText}>
                발견 표에 등록되지 않은 발견이라 요청 기록 · 상세 보고서 · 재검증 이력을 불러올 수 없어요.
              </p>
            </div>
          ) : (
            <>
              <TrafficCard
                loading={detailRes.loading || (trafficCount > 0 && trafficRes.loading)}
                error={trafficRes.error}
                count={trafficCount}
                rows={trafficRes.data ?? []}
              />
              <div className="card">
                <h3>
                  상세 보고서 <Sub>· 에이전트 작성</Sub>
                </h3>
                {detailRes.loading && <p style={bodyText}>불러오는 중…</p>}
                {!detailRes.loading && detail && !report && <p style={bodyText}>아직 작성된 보고서가 없어요.</p>}
                {!detailRes.loading && report && (
                  <>
                    {detail?.report_stale && (
                      <p className="hint" style={{ marginTop: 0 }}>
                        증거가 바뀐 뒤 아직 다시 쓰이지 않은 보고서예요.
                      </p>
                    )}
                    <div className="ev" style={evText}>
                      {report}
                    </div>
                  </>
                )}
              </div>
              <RetestCard loading={retestRes.loading} error={retestRes.error} rows={retestRes.data ?? []} />
            </>
          )}
        </div>
      </div>
    </Screen>
  );
}

/** 상단 경로(.crumb a) 링크 — 주소 해시도 맞춰 두고 화면 전환은 go 로 한다 */
function CrumbLink({
  to,
  go,
  children,
}: {
  to: "history" | "report";
  go: (s: "history" | "report") => void;
  children: ReactNode;
}) {
  return (
    <a
      href={`#${to}`}
      onClick={(event) => {
        event.preventDefault();
        go(to);
      }}
    >
      {children}
    </a>
  );
}

function TrafficCard({
  loading,
  error,
  count,
  rows,
}: {
  loading: boolean;
  error: string | null;
  count: number;
  rows: EngineFindingTraffic[];
}) {
  return (
    <div className="card">
      <h3>
        요청 · 응답 기록 <Sub>· 머리글 · 본문 없이 요약만</Sub>
      </h3>
      {loading && <p style={bodyText}>불러오는 중…</p>}
      {!loading && error && <p style={bodyText}>요청 기록을 불러오지 못했어요. ({error})</p>}
      {!loading && !error && (count === 0 || rows.length === 0) && (
        <p style={bodyText}>이 발견에 묶인 요청 기록이 없어요. 엔진 설정에서 트래픽 기록이 꺼져 있으면 남지 않아요.</p>
      )}
      {!loading && !error && rows.length > 0 && (
        <div className="scrollx">
          <table className="cmp">
            <thead>
              <tr>
                <th>역할</th>
                <th>요청</th>
                <th>응답</th>
                <th>메모</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.id}>
                  <td>
                    <strong>{ROLE_LABEL[r.role] ?? (r.role || "—")}</strong>
                  </td>
                  <td>
                    <span style={reqMethod}>{r.method || "—"}</span>
                    <br />
                    <span style={reqUrl}>{redactInput(r.url)}</span>
                  </td>
                  <td>
                    <code>{r.status || "—"}</code>
                    <br />
                    <span style={cellSub}>
                      {[r.contentType, r.respLen > 0 && size(r.respLen)].filter(Boolean).join(" · ")}
                    </span>
                  </td>
                  <td>
                    <span style={cellSub}>{r.note || "—"}</span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function RetestCard({ loading, error, rows }: { loading: boolean; error: string | null; rows: EngineFindingRetest[] }) {
  return (
    <div className="card" style={{ marginBottom: 0 }}>
      <h3>
        재검증 이력 <Sub>· 같은 공격을 다시 보내 지금도 재현되는지 확인한 결과</Sub>
      </h3>
      {loading && <p style={bodyText}>불러오는 중…</p>}
      {!loading && error && <p style={bodyText}>재검증 이력을 불러오지 못했어요. ({error})</p>}
      {!loading && !error && rows.length === 0 && <p style={bodyText}>아직 재검증한 적이 없어요.</p>}
      {!loading && !error && rows.length > 0 && (
        <div className="recent-list">
          {rows.map((r) => (
            <div className="recent-row" key={r.id}>
              <div className="recent-main">
                <div className="nm">{RETEST_VERDICT[r.verdict] ?? (r.verdict || "결과 없음")}</div>
                <div className="meta">
                  {[when(r.createdAt), RETEST_STATUS[r.status] ?? r.status, r.summary || r.error]
                    .filter(Boolean)
                    .join(" · ")}
                </div>
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
