// 제품은 실제 서버만 사용한다. 과거 demo 저장값은 복원하지 않는다.
import { create } from "zustand";
export type Source = "live";
export const SOURCE_KEY = "abc2lab-wb-source";
export const canGoLive = () => true;
export const useSource = create<{
  source: Source;
  taskId: string | null;
  setSource: (source: Source) => void;
  selectTask: (id: string | null) => void;
}>((set) => ({
  source: "live",
  taskId: null,
  setSource: () => set({ source: "live" }),
  selectTask: (taskId) => set({ taskId }),
}));
export const useIsLive = () => useSource((st) => st.source === "live");
export function useSourceBoot() {
  /* 과거 demo 저장값을 읽지 않는다. */
}
