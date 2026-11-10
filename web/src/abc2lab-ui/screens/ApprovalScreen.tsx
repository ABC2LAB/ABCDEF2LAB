import { type ReactNode, useState } from "react";

import { RequestApprovalPanel } from "../components/RequestApprovalPanel";
import { Screen } from "../components/Screen";
import { TopBar } from "../components/TopBar";
import { Pill, RefreshIcon, Strong } from "../components/ui";
import { ApiError, useApiResource } from "../lib/api";
import { useApprovalTarget } from "../lib/approval";
import { decidePendingApproval, getPendingApproval, listPendingApprovals, listTasks } from "../lib/engine";
import { redactInput } from "../lib/redact";
import { useSelection } from "../lib/selection";
import { useNav } from "../nav";

const STATUS_LABEL: Record<string, string> = {
  pending: "승인 대기",
  allowed: "허용됨",
  denied: "거부됨",
  timeout: "시간 초과로 처리됨",
};
/** 상태 배지 색: 대기=주황, 허용=초록, 거부 · 시간 초과=빨강 */
const STATUS_TONE: Record<string, string> = { pending: "warn", allowed: "ok", denied: "bad", timeout: "bad" };
const bodyText = { fontSize: 14, color: "var(--muted)", lineHeight: 1.7, margin: 0 } as const;
const optionDesc = { fontWeight: 500, color: "var(--muted)", fontSize: 13, marginTop: 2 } as const;

type Result = { id: number; kind: "allowed" | "denied" | "already" | "error"; message?: string };

function ago(iso: string): string {
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return "";
  const min = Math.max(0, Math.round((Date.now() - t) / 60000));
  if (min < 1) return "방금";
  if (min < 60) return `${min}분 전`;
  const hr = Math.round(min / 60);
  return hr < 24 ? `${hr}시간 전` : `${Math.round(hr / 24)}일 전`;
}

/** ===== 승인 게이트: 엔진의 승인 대기 요청(intercept pending)을 사람이 허용 · 거부 ===== */
export default function ApprovalScreen() {
  const { mode } = useSelection();
  return mode === "first-diagnosis" ? <FirstDiagnosisApprovalScreen /> : <ArtexApprovalScreen />;
}

function FirstDiagnosisApprovalScreen() {
  const { screen, go } = useNav();
  const { taskId, connection } = useSelection();
  const [nonce, setNonce] = useState(0);
  return (
    <Screen id="approval">
      <div className="frame">
        <TopBar current="dashboard" />
        <div style={{ padding: "22px 24px", maxWidth: 720 }}>
          <button type="button" className="backbtn" onClick={() => go("live")}>
            ← 실행 상태
          </button>
          <h1>첫 진단 HTTP 요청 승인</h1>
          <p className="hint">CLI로 준비된 task의 E② 송신 승인만 처리해요. ARTEX 도구 승인과는 별개예요.</p>
          {connection === "report" ? <p>저장 보고서 서버에서는 승인·중지·실행 변경을 할 수 없어요.</p> : null}
          {connection !== "report" && taskId ? (
            <>
              <p>task #{taskId}</p>
              <button type="button" className="btn xs" onClick={() => setNonce((n) => n + 1)}>
                새로고침
              </button>
              <RequestApprovalPanel taskId={taskId} active={screen === "approval"} refreshKey={nonce} />
            </>
          ) : null}
          {connection !== "report" && !taskId && (
            <p>
              연결된 task가 없어요.{" "}
              <button type="button" className="ap-link" onClick={() => go("entry")}>
                실제 task 연결
              </button>
            </p>
          )}
        </div>
      </div>
    </Screen>
  );
}

function ArtexApprovalScreen() {
  const { screen, go } = useNav();
  const { taskId, refreshTasks, selectTask } = useSelection();
  const { pendingId, selectPending } = useApprovalTarget();
  // 숨겨진 동안에는 조회하지 않고, 들어올 때마다 · 결정한 뒤(nonce) 다시 읽는다
  const active = screen === "approval";
  const [nonce, setNonce] = useState(0);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<Result | null>(null);

  const listRes = useApiResource(() => (active ? listPendingApprovals() : Promise.resolve(null)), [active, nonce]);
  const tasksRes = useApiResource(() => (active ? listTasks() : Promise.resolve(null)), [active]);
  const detailRes = useApiResource(
    () => (active && pendingId !== null ? getPendingApproval(pendingId) : Promise.resolve(null)),
    [active, pendingId, nonce],
  );
  const taskName = (id: string | null | undefined) => {
    if (!id) return "점검 미지정";
    return tasksRes.data?.tasks.find((t) => t.id === id)?.name?.trim() || `점검 #${id}`;
  };

  async function decide(decision: "allowed" | "denied") {
    if (pendingId === null || busy) return;
    const id = pendingId;
    setBusy(true);
    try {
      await decidePendingApproval(id, decision);
      setResult({ id, kind: decision });
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      setResult({ id, kind: err instanceof ApiError && err.status === 409 ? "already" : "error", message });
    } finally {
      setBusy(false);
      setNonce((n) => n + 1);
      refreshTasks();
    }
  }

  const shownResult = result && result.id === pendingId ? result : null;
  const pending = listRes.data ?? [];

  let body: ReactNode;
  if (pendingId === null) {
    // ── 대기 목록에서 고르기
    let list: ReactNode;
    if (listRes.loading) list = <p style={bodyText}>불러오는 중…</p>;
    else if (listRes.error) list = <p style={bodyText}>승인 대기 목록을 불러오지 못했어요. ({listRes.error})</p>;
    else if (pending.length === 0) list = <div className="dash-empty">지금 승인할 요청이 없어요.</div>;
    else
      list = (
        <div className="recent-list">
          {pending.map((p) => (
            <div className="recent-row" key={p.id}>
              <div className="recent-main">
                <div className="nm">{taskName(p.task_id)}</div>
                <div className="meta">
                  {[`요청 #${p.id}`, p.tool_name, p.agent_name, ago(p.created_at)].filter(Boolean).join(" · ")}
                </div>
              </div>
              <div className="recent-acts">
                <button className="btn xs primary" type="button" onClick={() => selectPending(p.id)}>
                  검토하기
                </button>
              </div>
            </div>
          ))}
        </div>
      );
    body = (
      <>
        <h1 className="big" style={{ marginBottom: 14 }}>
          도구 호출 승인 대기
        </h1>
        {list}
      </>
    );
  } else {
    // ── 고른 요청 하나를 보고 결정하기
    const d = detailRes.data;
    const decided = d && d.status !== "pending";
    let detail: ReactNode;
    if (detailRes.loading) detail = <p style={bodyText}>요청 #{pendingId}을 불러오는 중…</p>;
    else if (detailRes.error || !d)
      detail = (
        <p style={bodyText}>
          요청 #{pendingId}을 불러오지 못했어요. ({detailRes.error})
        </p>
      );
    else {
      const input = redactInput(d.tool_input);
      detail = (
        <>
          <div className="row" style={{ alignItems: "center", gap: 8, marginBottom: 6, flexWrap: "wrap" }}>
            <span className={`badge ${STATUS_TONE[d.status] ?? "warn"}`} style={{ cursor: "default" }}>
              {STATUS_LABEL[d.status] ?? d.status}
            </span>
            <Pill tone="muted">{taskName(d.task_id)}</Pill>
          </div>
          <h1 className="big" style={{ marginBottom: 14 }}>
            {decided ? "결정된 요청이에요" : "이 도구 호출을 허용할까요?"}
          </h1>

          <div className="card">
            <h3>에이전트의 도구 호출</h3>
            <div className="srow">
              <div className="k">요청</div>
              <div className="v">
                #{d.id} · <code>{d.tool_name || "도구 미상"}</code>
                {d.agent_name && ` · ${d.agent_name}`} · {ago(d.created_at)}
              </div>
            </div>
            {d.reason && (
              <div className="srow">
                <div className="k">멈춘 이유</div>
                <div className="v">{d.reason}</div>
              </div>
            )}
            <div className="ev" style={{ marginTop: 10 }}>
              {input || "입력 내용이 없어요."}
            </div>
            <div className="hint">쿠키 · 인증 헤더 · 토큰 · 비밀번호 값은 가려서 보여 줘요.</div>
          </div>

          <div className="card" style={{ marginBottom: 16 }}>
            <h3>어떻게 할까요?</h3>
            {decided ? (
              <p style={bodyText}>
                이 요청은 이미 <Strong>{STATUS_LABEL[d.status] ?? d.status}</Strong> 상태예요.
              </p>
            ) : (
              <div className="stack" style={{ gap: 10 }}>
                <button
                  className="btn"
                  type="button"
                  disabled={busy}
                  onClick={() => decide("allowed")}
                  style={{ textAlign: "left", borderColor: "var(--teal-border)" }}
                >
                  <strong style={{ color: "var(--teal-ink)" }}>이번 한 번만 허용</strong>
                  <div style={optionDesc}>이 도구 호출을 허용해요. 실제 HTTP 요청 승인은 별도예요.</div>
                </button>
                <button
                  className="btn danger"
                  type="button"
                  disabled={busy}
                  onClick={() => decide("denied")}
                  style={{ textAlign: "left" }}
                >
                  <strong>거부</strong>
                  <div style={optionDesc}>이 도구 호출을 실행하지 않아요</div>
                </button>
              </div>
            )}
          </div>
        </>
      );
    }
    body = (
      <>
        <button className="backbtn" type="button" onClick={() => selectPending(null)}>
          <span className="arr" aria-hidden="true">
            ←
          </span>
          승인 대기 목록
        </button>
        {shownResult && (
          <div
            className={`hcard ${shownResult.kind === "error" ? "dash-err" : ""}`}
            role="status"
            style={{ marginBottom: 14 }}
          >
            <div style={{ minWidth: 0 }}>
              <div className="nm">
                {shownResult.kind === "allowed" &&
                  `도구 호출 #${shownResult.id}을 허용했어요. 실제 HTTP 요청의 승인·전송 여부는 별도로 확인해요.`}
                {shownResult.kind === "denied" &&
                  `도구 호출 #${shownResult.id}을 거부했어요. 이 도구 호출은 실행하지 않아요.`}
                {shownResult.kind === "already" && `요청 #${shownResult.id}은 이미 처리된 요청이에요.`}
                {shownResult.kind === "error" && `요청 #${shownResult.id}을 처리하지 못했어요.`}
              </div>
              {shownResult.kind === "error" && <div className="meta">{shownResult.message}</div>}
            </div>
          </div>
        )}
        {detail}
      </>
    );
  }

  const auditTask = pendingId !== null ? detailRes.data?.task_id : null;
  return (
    <Screen id="approval">
      <div className="frame">
        <TopBar current="dashboard" />
        <div style={{ padding: "22px 24px", maxWidth: 720 }}>
          <div className="crumb" style={{ display: "flex", alignItems: "center", gap: 8 }}>
            <span style={{ flex: 1 }}>
              <button type="button" className="ap-link" onClick={() => go("dashboard")}>
                대시보드
              </button>{" "}
              › <span>승인</span>
            </span>
            <button
              className="btn xs"
              type="button"
              onClick={() => setNonce((n) => n + 1)}
              disabled={listRes.loading || detailRes.loading}
            >
              <RefreshIcon /> 새로고침
            </button>
          </div>
          <label htmlFor="request-approval-task">실제 요청을 확인할 점검</label>
          <select
            id="request-approval-task"
            value={taskId ?? ""}
            onChange={(event) => selectTask(event.target.value || null)}
          >
            <option value="">점검을 선택하세요</option>
            {(tasksRes.data?.tasks ?? []).map((task) => (
              <option key={task.id} value={task.id}>
                {task.name || `점검 #${task.id}`}
              </option>
            ))}
          </select>
          {taskId ? (
            <RequestApprovalPanel key={taskId} taskId={taskId} active={active} refreshKey={nonce} />
          ) : (
            <p className="hint">점검을 선택하면 실제 HTTP 요청의 승인·실행 상태를 확인할 수 있어요.</p>
          )}
          {body}
          <div className="hint">
            결정은 누가 · 언제 내렸는지와 함께{" "}
            <button
              type="button"
              className="ap-link"
              onClick={() => {
                if (auditTask) selectTask(auditTask);
                go("audit");
              }}
            >
              통제 기록
            </button>
            에 남아요.
          </div>
        </div>
      </div>
    </Screen>
  );
}
