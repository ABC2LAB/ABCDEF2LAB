import { useCallback, useEffect, useMemo, useState } from "react";
import "./abc2lab.css";
import { MOCK, MOCK_DEFAULT_TASK } from "./lib/mock";
import { toTarget } from "./lib/net";
import { type Selection, SelectionContext } from "./lib/selection";
import { isScreenId, NavContext, type NavValue, type ScreenId } from "./nav";
import AccessMapScreen from "./screens/AccessMapScreen";
import ApprovalScreen from "./screens/ApprovalScreen";
import AuditScreen from "./screens/AuditScreen";
import EntryScreen from "./screens/EntryScreen";
import FindingScreen from "./screens/FindingScreen";
import FindingUndScreen from "./screens/FindingUndScreen";
import HistoryScreen from "./screens/HistoryScreen";
import JobScreen from "./screens/JobScreen";
import LiveScreen from "./screens/LiveScreen";
import ReportScreen from "./screens/ReportScreen";
import ScopeScreen from "./screens/ScopeScreen";
import SettingsScreen from "./screens/SettingsScreen";
import TargetScreen from "./screens/TargetScreen";
import TokensScreen from "./screens/TokensScreen";
import { DEFAULT_TARGET_URL, type Target } from "./types";

export type ABC2LabUIProps = {
  /** 처음 보여줄 화면 (URL 해시가 있으면 해시가 우선) */
  initialScreen?: ScreenId;
  /**
   * 화면을 바꿀 때 URL 해시(#report 등)를 history 에 쌓을지 (기본 true).
   * true 면 브라우저 뒤로/앞으로가 화면 전환으로 동작한다. react-router를 쓰면 false 추천.
   */
  syncHash?: boolean;
  /** 테마 고정. 비우면 OS 설정(prefers-color-scheme)을 따라가요 */
  theme?: "light" | "dark";
};

/** ABC2LAB 점검 흐름 UI (원본 ABC2LAB_UI.html 의 React + TypeScript 버전) */
export default function ABC2LabUI({ initialScreen = "entry", syncHash = true, theme }: ABC2LabUIProps) {
  // 초기값은 SSR/CSR 가 같아야(hydration) 하므로 결정적으로 initialScreen 으로 둔다.
  // URL 해시(#settings 등) 반영은 마운트 후 아래 effect 에서 — 서버는 해시를 못 읽기 때문.
  const [screen, setScreen] = useState<ScreenId>(initialScreen);

  // 진입 화면에서 입력한 대상 주소 → 요약 · 범위 확인 · 대시보드 · 팝업에 반영
  const [target, setTarget] = useState<Target>(() => toTarget(DEFAULT_TARGET_URL));

  // 지금 보고 있는 점검(task) — 기록에서 카드를 열 때 지정. report/live/finding 이 읽는다.
  const [taskId, setTaskId] = useState<string | null>(MOCK ? MOCK_DEFAULT_TASK : null);
  const [tasksVersion, setTasksVersion] = useState(0);
  const refreshTasks = useCallback(() => setTasksVersion((version) => version + 1), []);
  const selection = useMemo<Selection>(
    () => ({ taskId, selectTask: setTaskId, tasksVersion, refreshTasks }),
    [taskId, tasksVersion, refreshTasks],
  );

  const go = useCallback(
    (next: ScreenId) => {
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
    [screen, syncHash],
  );

  // 마운트 시 현재 해시를 화면에 반영 + 브라우저 뒤로/앞으로(popstate) 복원.
  // 초기 state 가 결정적이라 SSR/CSR 가 일치하고, 해시 적용은 하이드레이션 뒤 여기서 한다(여기선 pushState 금지).
  useEffect(() => {
    if (!syncHash) return;
    const applyHash = () => {
      const fromHash = window.location.hash.replace("#", "");
      setScreen(isScreenId(fromHash) ? fromHash : initialScreen);
    };
    applyHash();
    window.addEventListener("popstate", applyHash);
    return () => window.removeEventListener("popstate", applyHash);
  }, [syncHash, initialScreen]);

  const nav = useMemo<NavValue>(() => ({ screen, go }), [screen, go]);

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
        {MOCK && (
          <div className="mock-banner" role="note">
            목업 예시 데이터예요 — 실제 점검 결과가 아니에요
          </div>
        )}
        <div className="stage">
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
          <TokensScreen />
        </div>
      </SelectionContext.Provider>
    </NavContext.Provider>
  );
}
