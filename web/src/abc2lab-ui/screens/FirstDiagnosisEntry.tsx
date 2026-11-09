import { type FormEvent, useEffect, useRef, useState } from "react";

import { NewDiagnosisPanel } from "../components/NewDiagnosisPanel";
import { Screen } from "../components/Screen";
import { TopBar } from "../components/TopBar";
import {
  connectionError,
  type DiagnosisServer,
  listPreparedTasks,
  type PreparedTask,
  validTaskID,
  verifyDiagnosisTask,
} from "../lib/first-diagnosis";
import { useSelection } from "../lib/selection";
import { useNav } from "../nav";

type TaskList =
  | { kind: "loading" }
  | { kind: "ready"; tasks: PreparedTask[] }
  | { kind: "unavailable"; message: string };

/** CLI에서 만든 task의 서버 조회만 수행한다. */
export default function FirstDiagnosisEntry() {
  const { screen, go } = useNav();
  const { taskId, connection, connectTask, selectTask } = useSelection();
  const [createOpen, setCreateOpen] = useState(false);
  const [input, setInput] = useState(taskId ?? "");
  const [server, setServer] = useState<DiagnosisServer>(connection ?? "run");
  const [list, setList] = useState<TaskList>({ kind: "loading" });
  const [revision, setRevision] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const generation = useRef(0);
  const initialTask = useRef(taskId);
  const [initialized, setInitialized] = useState(false);
  const active = screen === "entry";

  useEffect(() => {
    if (!initialTask.current) {
      const params = new URLSearchParams(window.location.search);
      const proposed = params.get("task");
      if (proposed && validTaskID(proposed)) setInput(proposed);
      if (params.get("server") === "report") setServer("report");
    }
    setInitialized(true);
  }, []);

  // biome-ignore lint/correctness/useExhaustiveDependencies: revision은 명시적인 목록 새로고침 요청이다.
  useEffect(() => {
    let cancelled = false;
    generation.current += 1;
    setBusy(false);
    setError("");
    if (initialized && active && server === "run") {
      setList({ kind: "loading" });
      listPreparedTasks().then(
        (tasks) => {
          if (!cancelled) setList({ kind: "ready", tasks });
        },
        (reason) => {
          if (!cancelled) setList({ kind: "unavailable", message: connectionError(reason) });
        },
      );
    }
    return () => {
      cancelled = true;
      generation.current += 1;
    };
  }, [initialized, active, server, revision]);

  function change(value: string) {
    generation.current += 1;
    setBusy(false);
    setError("");
    setInput(value);
  }

  async function connect(event: FormEvent) {
    event.preventDefault();
    if (busy) return;
    const requested = input.trim();
    const current = ++generation.current;
    setBusy(true);
    setError("");
    // 실패한 새 선택 뒤 이전 task 자료를 복원하지 않는다.
    selectTask(null);
    try {
      await verifyDiagnosisTask(requested, server);
      if (generation.current !== current) return;
      connectTask?.(requested, server);
      go(server === "report" ? "report" : "live");
    } catch (reason) {
      if (generation.current === current) setError(connectionError(reason));
    } finally {
      if (generation.current === current) setBusy(false);
    }
  }

  const connectLabel = server === "report" ? "저장 결과 연결" : "실행 연결";
  return (
    <Screen id="entry">
      <div className="frame">
        <TopBar current="entry" />
        <div style={{ padding: "40px 24px", maxWidth: 800, margin: "0 auto" }}>
          <h1 className="big">CLI 첫 진단에 연결</h1>
          <button className="btn" type="button" onClick={() => setCreateOpen((value) => !value)}>
            {createOpen ? "초안 화면 닫기" : "새 진단 초안"}
          </button>
          {createOpen && active && <NewDiagnosisPanel />}
          <p className="hint">
            운영자가 prepare-task → prepare-intents → check → run으로 준비한 실제 task를 선택해 주세요. 여기서는 기존
            진단에 연결해요.
          </p>
          <form className="card" onSubmit={connect}>
            <label className="fl" htmlFor="diagnosis-server">
              연결할 서버
            </label>
            <select
              id="diagnosis-server"
              className="inp"
              value={server}
              onChange={(event) => setServer(event.target.value === "report" ? "report" : "run")}
            >
              <option value="run">실행 서버</option>
              <option value="report">저장 보고서 서버</option>
            </select>
            {server === "run" ? (
              <div style={{ margin: "16px 0" }}>
                <label className="fl" htmlFor="prepared-task">
                  서버의 task 목록
                </label>
                <select
                  id="prepared-task"
                  className="inp"
                  value={list.kind === "ready" && list.tasks.some((item) => item.id === input) ? input : ""}
                  onChange={(event) => change(event.target.value)}
                >
                  <option value="">실제 task를 선택하거나 아래 ID 입력</option>
                  {list.kind === "ready" &&
                    list.tasks.map((task) => (
                      <option value={task.id} key={task.id}>
                        #{task.id} · {task.name || "이름 없는 작업"} · {task.status || "상태 미확인"}
                      </option>
                    ))}
                </select>
                {list.kind === "loading" && <p className="hint">작업 목록을 확인하는 중이에요.</p>}
                {list.kind === "ready" && list.tasks.length === 0 && (
                  <p className="hint">서버가 제공하는 task가 없어요. 실행 중인 진단으로 표시하지 않아요.</p>
                )}
                {list.kind === "unavailable" && (
                  <p className="hint">{list.message} 목록 없이 CLI task ID로 연결할 수도 있어요.</p>
                )}
                <button className="btn xs" type="button" onClick={() => setRevision((value) => value + 1)}>
                  목록 새로고침
                </button>
              </div>
            ) : (
              <p className="hint">
                report 서버는 단일 task의 저장 자료만 제공해요. 일반 작업 목록은 요청하지 않아요. 종료 기록이 없어도
                보고서 자료를 조회할 수 있지만, 실행 완료로 판단하지 않아요.
              </p>
            )}
            <label className="fl" htmlFor="diagnosis-task">
              첫 진단 task ID
            </label>
            <div className="row" style={{ gap: 12 }}>
              <input
                id="diagnosis-task"
                className="inp"
                value={input}
                onChange={(event) => change(event.target.value)}
                inputMode="numeric"
                autoComplete="off"
                placeholder="CLI가 출력한 task ID"
              />
              <button className="btn primary" type="submit" disabled={busy || !input.trim()}>
                {busy ? "연결 확인 중" : connectLabel}
              </button>
            </div>
            {error && (
              <p role="alert" style={{ marginTop: 12 }}>
                {error}
              </p>
            )}
          </form>
          <section className="card" aria-label="지원하지 않는 설정">
            <h3>준비와 비밀정보는 기존 보호된 절차로</h3>
            <p className="hint">
              DNS TXT 소유 확인, API 키 등록, 세션·Cookie·Authorization 입력은 아직 지원하지 않아요. 이 화면에
              비밀정보를 입력하지 마세요.
            </p>
            <p className="hint">
              접근통제 지도·상세 커버리지·E② 감사 흐름 시각화는 후속 범위예요. 예시 값을 실제 결과로 표시하지 않아요.
            </p>
          </section>
        </div>
      </div>
    </Screen>
  );
}
