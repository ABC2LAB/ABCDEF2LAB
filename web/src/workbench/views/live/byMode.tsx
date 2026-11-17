"use client";
import type { ReactNode } from "react";

import { LoadingRow } from "../../live/parts";
import { recheckUrl, useUrlCapability, useUrlSelectionKey } from "../../live/urlDiagnosis";
import s from "../../workbench.module.css";
import { UrlDashboardLive } from "./UrlDashboardLive";
import { UrlLiveLive, UrlReportLive } from "./UrlLiveLive";
import { UrlNewScanLive } from "./UrlNewScanLive";
import { UrlHead } from "./urlParts";
import { UrlFinishedLive, UrlRunningLive } from "./urlRecords";
import { WorkflowScopeLive } from "./workflowViews";

function Supported({ title, children }: { title: string; children: ReactNode }) {
  const cap = useUrlCapability();
  if (cap.kind === "v2") return children;
  return (
    <>
      <UrlHead title={title} crumb={title} />
      <div className={s.vm}>
        {cap.kind === "checking" || cap.kind === "unknown" ? (
          <LoadingRow text="URL 진단 서비스를 확인하는 중…" />
        ) : (
          <div role="alert">
            <p>
              {cap.kind === "unavailable"
                ? cap.message
                : "URL 진단 서비스가 설치되지 않았습니다. 운영자 설정이 필요합니다."}
            </p>
            <button type="button" className={s.btn} onClick={recheckUrl}>
              다시 조회
            </button>
          </div>
        )}
      </div>
    </>
  );
}
export const NewScanByMode = () => {
  const selection = useUrlSelectionKey();
  return (
    <Supported title="새 점검">
      <UrlNewScanLive key={selection} />
    </Supported>
  );
};
export const DashboardByMode = () => {
  const selection = useUrlSelectionKey();
  return (
    <Supported title="대시보드">
      <UrlDashboardLive key={selection} />
    </Supported>
  );
};
export const RunningByMode = () => {
  const selection = useUrlSelectionKey();
  return (
    <Supported title="진행 중인 점검">
      <UrlRunningLive key={selection} />
    </Supported>
  );
};
export const FinishedByMode = () => {
  const selection = useUrlSelectionKey();
  return (
    <Supported title="끝난 점검">
      <UrlFinishedLive key={selection} />
    </Supported>
  );
};
export const ScopeByMode = () => {
  const selection = useUrlSelectionKey();
  return (
    <Supported title="범위 확인">
      <WorkflowScopeLive key={selection} />
    </Supported>
  );
};
export const LiveByMode = () => {
  const selection = useUrlSelectionKey();
  return (
    <Supported title="점검 진행">
      <UrlLiveLive key={selection} />
    </Supported>
  );
};
export const ReportByMode = () => {
  const selection = useUrlSelectionKey();
  return (
    <Supported title="보고서">
      <UrlReportLive key={selection} />
    </Supported>
  );
};
