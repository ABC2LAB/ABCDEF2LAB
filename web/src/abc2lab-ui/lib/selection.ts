import { createContext, useContext } from "react";

/**
 * 지금 보고 있는 점검(task) 선택 상태.
 * 모든 화면이 항상 마운트돼 있어(원본 show/hide 구조), task 범위 화면(report/live/finding…)은
 * "어느 task인지"를 여기서 읽는다. 점검 기록에서 카드를 열 때 selectTask 로 지정된다.
 */
export type Selection = {
  /** 선택된 task id (없으면 null — 화면은 "먼저 선택" 안내로 폴백) */
  taskId: string | null;
  selectTask: (id: string | null) => void;
};

export const SelectionContext = createContext<Selection | null>(null);

export function useSelection(): Selection {
  const value = useContext(SelectionContext);
  if (!value) throw new Error("useSelection은 <ABC2LabUI> 안에서만 쓸 수 있어요.");
  return value;
}
