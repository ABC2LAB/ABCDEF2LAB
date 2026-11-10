import { useCallback, useEffect, useMemo, useState } from "react";
import "./abc2lab.css";

import { restoreSession, subscribeAuthRequired } from "./lib/api";
import { ApprovalContext, type ApprovalTarget } from "./lib/approval";
import { validTaskID } from "./lib/first-diagnosis";
import { MOCK, MOCK_DEFAULT_TASK } from "./lib/mock";
import { toTarget } from "./lib/net";
import { type Selection, SelectionContext } from "./lib/selection";
import { isScreenId, NavContext, type NavValue, type ScreenId } from "./nav";
import AccessMapScreen from "./screens/AccessMapScreen";
import ApprovalScreen from "./screens/ApprovalScreen";
import AuditScreen from "./screens/AuditScreen";
import CoverageScreen from "./screens/CoverageScreen";
import DashboardScreen from "./screens/DashboardScreen";
import E2FlowScreen from "./screens/E2FlowScreen";
import EntryScreen from "./screens/EntryScreen";
import FindingScreen from "./screens/FindingScreen";
import FindingUndScreen from "./screens/FindingUndScreen";
import HistoryScreen from "./screens/HistoryScreen";
import JobScreen from "./screens/JobScreen";
import LiveScreen from "./screens/LiveScreen";
import LoginScreen from "./screens/LoginScreen";
import ReportScreen from "./screens/ReportScreen";
import ScopeScreen from "./screens/ScopeScreen";
import SettingsScreen from "./screens/SettingsScreen";
import TargetScreen from "./screens/TargetScreen";
import TokensScreen from "./screens/TokensScreen";
import { DEFAULT_TARGET_URL, type Target } from "./types";

export type ABC2LabUIProps = {
  /** 처음 보여줄 화면 (URL 해시가 있으면 해시가 우선) */
  initialScreen?: ScreenId;
  /** 일반 ARTEX와 CLI 첫 진단의 서버 계약을 섞지 않는다. */
  mode?: "first-diagnosis" | "artex";
  /**
   * 화면을 바꿀 때 URL 해시(#report 등)를 history 에 쌓을지 (기본 true).
   * true 면 브라우저 뒤로/앞으로가 화면 전환으로 동작한다. react-router를 쓰면 false 추천.
   */
  syncHash?: boolean;
  /** 테마 고정. 비우면 OS 설정(prefers-color-scheme)을 따라가요 */
  theme?: "light" | "dark";
};

/** 로그인 뒤에만 화면을 마운트한다. 화면들은 마운트 즉시 API를 읽기 때문이다. */
export default function ABC2LabUI(props: ABC2LabUIProps) {
  const [auth, setAuth] = useState<"checking" | "in" | "out">(MOCK ? "in" : "checking");

  useEffect(() => {
    if (MOCK) return;
    let live = true;
    restoreSession()
      .then((restored) => {
        if (live) setAuth(restored ? "in" : "out");
      })
      .catch(() => {
        if (live) setAuth("out");
      });
    const unsubscribe = subscribeAuthRequired(() => setAuth("out"));
    return () => {
      live = false;
      unsubscribe();
    };
  }, []);

  if (auth === "checking") return null;
  if (auth === "out") return <LoginScreen onSignedIn={() => setAuth("in")} />;
  return <Workspace {...props} />;
}

/** ABC2LAB 점검 흐름 UI (원본 ABC2LAB_UI.html 의 React + TypeScript 버전) */
function Workspace({ initialScreen = "dashboard", syncHash = true, theme, mode = "artex" }: ABC2LabUIProps) {
  const firstDiagnosis = mode === "first-diagnosis" && !MOCK;
  const allowedScreen = useCallback(
    (next: ScreenId): ScreenId =>
      !firstDiagnosis || ["entry", "live", "approval", "report"].includes(next) ? next : "entry",
    [firstDiagnosis],
  );
  // 초기값은 SSR/CSR 가 같아야(hydration) 하므로 결정적으로 initialScreen 으로 둔다.
  // URL 해시(#settings 등) 반영은 마운트 후 아래 effect 에서 — 서버는 해시를 못 읽기 때문.
  const [screen, setScreen] = useState<ScreenId>(allowedScreen(initialScreen));

  // 진입 화면에서 입력한 대상 주소 → 요약 · 범위 확인 · 대시보드 · 팝업에 반영
  const [target, setTarget] = useState<Target>(() => toTarget(DEFAULT_TARGET_URL));

  // 지금 보고 있는 점검(task) — 기록에서 카드를 열 때 지정. report/live/finding 이 읽는다.
  const [taskId, setTaskId] = useState<string | null>(MOCK ? MOCK_DEFAULT_TASK : null);
  const [tasksVersion, setTasksVersion] = useState(0);
  const [connection, setConnection] = useState<"run" | "report">("run");
  const connectTask = useCallback(
    (id: string, source: "run" | "report") => {
      setTaskId(id);
      setConnection(source);
      if (syncHash) {
        const url = new URL(window.location.href);
        url.searchParams.set("task", id);
        url.searchParams.set("server", source);
        window.history.replaceState(window.history.state, "", url);
      }
    },
    [syncHash],
  );
  // URL에는 task 번호만 남긴다. Entry가 서버 응답으로 검증하기 전 선택 상태로 복원하지 않는다.
  const selectTask = useCallback(
    (id: string | null) => {
      setTaskId(id);
      if (firstDiagnosis && !id && syncHash) {
        const url = new URL(window.location.href);
        url.searchParams.delete("task");
        url.searchParams.delete("server");
        window.history.replaceState(window.history.state, "", url);
      }
    },
    [firstDiagnosis, syncHash],
  );
  const refreshTasks = useCallback(() => setTasksVersion((version) => version + 1), []);
  const selection = useMemo<Selection>(
    () => ({
      taskId,
      selectTask,
      tasksVersion,
      refreshTasks,
      mode: firstDiagnosis ? "first-diagnosis" : "artex",
      connection,
      connectTask,
    }),
    [taskId, selectTask, tasksVersion, refreshTasks, firstDiagnosis, connection, connectTask],
  );

  const go = useCallback(
    (requested: ScreenId) => {
      const next = allowedScreen(requested);
      if (next === screen) return; // 같은 화면이면 히스토리 엔트리 안 쌓음
      setScreen(next);
      if (syncHash) {
        try {
          // pushState 로 엔트리를 쌓아야 브라우저 뒤로/앞으로가 동작한다
          window.history.pushState({ screen: next }, "", `#${next}`);
        } catch {
          /* 무시 */
        }
      }
      window.scrollTo(0, 0);
    },
    [screen, syncHash, allowedScreen],
  );

  // 마운트 시 현재 해시를 화면에 반영 + 브라우저 뒤로/앞으로(popstate) 복원.
  // 초기 state 가 결정적이라 SSR/CSR 가 일치하고, 해시 적용은 하이드레이션 뒤 여기서 한다(여기선 pushState 금지).
  useEffect(() => {
    if (!syncHash) return;
    const applyHash = () => {
      const fromHash = window.location.hash.replace("#", "");
      const requested = allowedScreen(isScreenId(fromHash) ? fromHash : initialScreen);
      const urlTask = new URLSearchParams(window.location.search).get("task");
      // 새로고침·새 링크는 Entry 검증을 거친다. task 변경 뒤 이전 결과를 히스토리로 복구하지 않는다.
      setScreen(firstDiagnosis && (!taskId || urlTask !== taskId || !validTaskID(taskId)) ? "entry" : requested);
    };
    applyHash();
    window.addEventListener("popstate", applyHash);
    return () => window.removeEventListener("popstate", applyHash);
  }, [syncHash, initialScreen, allowedScreen, firstDiagnosis, taskId]);

  const nav = useMemo<NavValue>(() => ({ screen, go }), [screen, go]);

  // 승인 화면이 처리할 승인 요청 — 대시보드의 "승인하기"가 고른 요청 id 를 넘긴다
  const [pendingId, setPendingId] = useState<number | null>(null);
  const approval = useMemo<ApprovalTarget>(
    () => ({
      pendingId,
      selectPending: setPendingId,
      openApproval: (id) => {
        setPendingId(id);
        go("approval");
      },
    }),
    [pendingId, go],
  );

  // 원본 CSS가 :root[data-theme] 로 테마를 고르기 때문에 <html> 에 속성을 달아요
  useEffect(() => {
    const root = document.documentElement;
    if (!theme) return;
    root.setAttribute("data-theme", theme);
    return () => root.removeAttribute("data-theme");
  }, [theme]);

  return (
    <NavContext.Provider value={nav}>
      <SelectionContext.Provider value={selection}>
        <ApprovalContext.Provider value={approval}>
          {MOCK && (
            <div className="mock-banner" role="note">
              목업 예시 데이터예요 — 실제 점검 결과가 아니에요
            </div>
          )}
          <div className="stage">
            {firstDiagnosis ? (
              <>
                <EntryScreen onStart={(input) => setTarget(toTarget(input))} />
                <LiveScreen key={`live:${taskId ?? ""}`} target={target} />
                <ApprovalScreen key={`approval:${taskId ?? ""}`} />
                <ReportScreen key={`report:${taskId ?? ""}`} />
              </>
            ) : (
              <>
                <DashboardScreen />
                <EntryScreen onStart={(input) => setTarget(toTarget(input))} />
                <TargetScreen target={target} />
                <JobScreen target={target} />
                <ScopeScreen target={target} />
                <LiveScreen target={target} />
                <HistoryScreen />
                <ReportScreen />
                <FindingScreen />
                <FindingUndScreen />
                <ApprovalScreen />
                <SettingsScreen />
                <AuditScreen />
                <AccessMapScreen />
                <E2FlowScreen />
                <CoverageScreen />
                <TokensScreen />
              </>
            )}
          </div>
        </ApprovalContext.Provider>
      </SelectionContext.Provider>
    </NavContext.Provider>
  );
}
