import { useEffect, useState } from "react";

import { DiagnosisRunSummary } from "../components/DiagnosisRunSummary";
import { DiagnosticDownload } from "../components/DiagnosticDownload";
import { RemediationPanel } from "../components/RemediationPanel";
import { RunStatusPanel } from "../components/RunStatusPanel";
import { Screen } from "../components/Screen";
import { TopBar } from "../components/TopBar";
import { ApiError } from "../lib/api";
import { diagnosticOutcome, diagnosticOutcomeLabels, summarizeDiagnosticEntries } from "../lib/diagnostic-results";
import { type RemediationData, readRemediation } from "../lib/remediation";
import { useSelection } from "../lib/selection";
import { useNav } from "../nav";

function reportError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 0) return "보고서 서버와 연결이 끊겼어요. 서버 연결을 확인해 주세요.";
    if (error.status === 401) return "로그인이 만료됐어요. 다시 로그인해 주세요.";
    if (error.status === 403) return "이 작업의 보고서를 조회할 권한이 없어요.";
    if (error.status === 404) return "이 작업의 저장 보고서를 찾지 못했어요.";
    if (error.status === 503) return "보고서 저장소를 사용할 수 없어요. 완료나 결과 0건으로 보지 않아요.";
    return "서버가 보고서를 반환하지 못했어요. 저장 결과를 다시 확인해 주세요.";
  }
  if (error instanceof TypeError) return "보고서 서버와 연결이 끊겼어요. 서버 연결을 확인해 주세요.";
  return "보고서의 작업·판정·증거 정보를 확인하지 못했어요. 데이터를 복원하거나 결과 0건으로 보지 않아요.";
}

/** 첫 진단 report 서버는 일반 task/findings/coverage API를 제공하지 않는다. */
export default function FirstDiagnosisReportScreen() {
  const { taskId, connection = "run" } = useSelection();
  const { screen, go } = useNav();
  const active = screen === "report";
  return (
    <Screen id="report">
      <div className="frame">
        <TopBar current="history" />
        <div style={{ padding: "24px", maxWidth: 1100, margin: "0 auto" }}>
          <h1>첫 통합 진단 보고서</h1>
          <p className="hint">
            실제 작업의 저장 결과예요. 실행 마감과 취약점 판정은 별개이며, 후보는 확정 취약점이 아니에요.
          </p>
          <button className="btn" type="button" onClick={() => go("entry")}>
            작업 연결·변경
          </button>
          {taskId ? (
            <Results key={`${taskId}-${connection}`} taskId={taskId} active={active} source={connection} />
          ) : (
            <p role="status">먼저 실제 진단 작업에 연결해 주세요. 선택된 작업이 없어요.</p>
          )}
        </div>
      </div>
    </Screen>
  );
}

function Results({ taskId, active, source }: { taskId: string; active: boolean; source: "run" | "report" }) {
  const { go } = useNav();
  const [result, setResult] = useState<RemediationData | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [refresh, setRefresh] = useState(0);
  const [tab, setTab] = useState<"results" | "remediation">("results");

  // biome-ignore lint/correctness/useExhaustiveDependencies: refresh는 사용자의 명시적 재조회 요청이다.
  useEffect(() => {
    setResult(null);
    setError("");
    if (!active) return;
    const controller = new AbortController();
    setLoading(true);
    void readRemediation(taskId, "internal", controller.signal)
      .then((data) => {
        if (!controller.signal.aborted) setResult(data);
      })
      .catch((cause: unknown) => {
        if (!controller.signal.aborted) setError(reportError(cause));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => {
      controller.abort();
    };
  }, [taskId, active, refresh]);

  // 숨긴 화면의 이전 결과도 다시 열기 전에는 노출하거나 다운로드 권한으로 쓰지 않는다.
  const ready = active && !loading && !error && result?.task_id === Number(taskId);
  const entries = ready && result ? result.entries : [];
  const counts = summarizeDiagnosticEntries(entries);

  return (
    <>
      <div className="row" style={{ justifyContent: "space-between", alignItems: "start", marginTop: 16 }}>
        <div>
          <h2>작업 #{taskId}</h2>
          <button className="btn" type="button" disabled={!active || loading} onClick={() => setRefresh((n) => n + 1)}>
            보고서 새로고침
          </button>
        </div>
        <DiagnosticDownload key={taskId} taskId={taskId} enabled={ready} />
      </div>
      {source === "run" ? (
        <RunStatusPanel taskId={taskId} active={active} source="run" onOpenApprovals={() => go("approval")} />
      ) : (
        <DiagnosisRunSummary key={`${taskId}-${refresh}`} taskId={taskId} active={active} />
      )}
      {loading && active && <p role="status">진단 결과를 조회하는 중이에요.</p>}
      {error && active && <p role="alert">{error}</p>}
      {ready && (
        <>
          <section className="card" aria-label="진단 판정 집계">
            <h2>저장 결과 {entries.length}건</h2>
            <p className="hint">
              진단 보고서의 고유 결과 ID를 한 번씩 집계해요. 일반 ARTEX findings 수를 더하지 않아요. 모든 저장 회차가
              포함되며, 후보 0건이나 실행 종료만으로 안전하다고 판단하지 않아요.
            </p>
            <dl style={{ display: "flex", flexWrap: "wrap", gap: 24 }}>
              {Object.entries(diagnosticOutcomeLabels).map(([key, label]) => (
                <div key={key}>
                  <dt>{label}</dt>
                  <dd style={{ margin: 0, fontSize: 24 }}>{counts[key as keyof typeof counts]}건</dd>
                </div>
              ))}
            </dl>
          </section>
          <div className="row" style={{ margin: "16px 0" }}>
            <button className="btn" type="button" aria-pressed={tab === "results"} onClick={() => setTab("results")}>
              후보·판정 목록
            </button>
            <button
              className="btn"
              type="button"
              aria-pressed={tab === "remediation"}
              onClick={() => setTab("remediation")}
            >
              저장 조치·AI 수정 프롬프트
            </button>
          </div>
          {tab === "results" ? (
            <section aria-label="후보·판정 목록">
              {entries.length === 0 && <p>저장된 후보·판정이 0건이에요. 취약점 없음이나 정상 완료를 뜻하지 않아요.</p>}
              {entries.map((entry) => (
                <article key={entry.entry_id} className="card">
                  <h3>{entry.title || "제목 미확인"}</h3>
                  <p>
                    {entry.entry_id} · {diagnosticOutcomeLabels[diagnosticOutcome(entry)]}
                  </p>
                  {entry.verification === "manual_review_required" && (
                    <p className="hint">수동 검토가 필요한 결과예요. 실제 HTTP 관찰만으로 확정하지 않아요.</p>
                  )}
                  {diagnosticOutcome(entry) === "unknown" && (
                    <p className="hint">
                      송신·판정·근거 중 확인되지 않은 부분이 있어요. 정상이나 확정으로 보지 않아요.
                    </p>
                  )}
                  <p style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}>
                    {entry.summary || "저장된 요약 없음"}
                  </p>
                  <p className="hint">
                    회차: {entry.run || "미확인"} · 증거 버전:{" "}
                    {entry.evidence_version >= 0 ? entry.evidence_version : "미확인"}
                  </p>
                </article>
              ))}
            </section>
          ) : (
            <RemediationPanel key={taskId} taskId={taskId} active={active && ready} />
          )}
          <p className="hint">단계별 진행률·ETA·일반 활동/토큰 상세·KG 예시 지도는 이 보고서에 포함하지 않아요.</p>
        </>
      )}
    </>
  );
}
