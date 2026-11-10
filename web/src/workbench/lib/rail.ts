// 레일 빨간 점: 지금 오른쪽에 보이는 점검 기록이 레일(예시)의 어느 줄인지. 원본 railKey · syncRailSel.
// 화면이 정하고, 대시보드에서는 고른 항목(dSel)이 어느 점검의 기록인지로 정한다.

import type { View } from "../shell";
import type { DSel } from "../store";

/** 레일 예시 줄: 새 점검 · shop(진행 중) · api.store-dev · blog · docs · app.sample */
export type RailKey = "new" | "shop" | "api" | "blog" | "docs" | "app";

/** 대시보드 항목 → 그 항목이 속한 점검 줄. 원본 DSEL_RK. */
const DSEL_RK: Readonly<Record<DSel, RailKey>> = {
  appr: "api",
  "act-api": "api",
  path: "shop",
  "act-shop": "shop",
  "done-blog": "blog",
  und: "app",
  "done-app": "app",
  cov: "docs",
  "done-docs": "docs",
};

/**
 * 빨간 점을 찍을 줄. 원본 `railKey()`.
 * newVisible: 새 점검 줄이 지금 보이는가 (숨은 줄에는 찍지 않는다).
 */
export function railKey(route: View, dSel: DSel, reportKind: "blog" | "live", newVisible: boolean): RailKey | null {
  if (route === "crawl" || route === "scope") return newVisible ? "new" : null;
  if (route === "live") return "shop";
  if (route === "report") return reportKind === "blog" ? "blog" : "shop";
  if (route === "dashboard") return DSEL_RK[dSel];
  return null;
}
