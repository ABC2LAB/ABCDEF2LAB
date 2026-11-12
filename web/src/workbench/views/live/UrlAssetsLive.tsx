"use client";
import { WorkflowCrawlLive } from "./workflowViews";

/** 자산 탐색 화면 (탐색 워크플로가 없는 점검이면 그 안에서 안내한다). */
export function UrlAssetsLive() {
  return <WorkflowCrawlLive />;
}
