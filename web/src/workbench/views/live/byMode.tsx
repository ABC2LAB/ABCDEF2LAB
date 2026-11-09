"use client";
import type { ReactNode } from "react";

import { LoadingRow } from "../../live/parts";
import { recheckUrl, useUrlCapability } from "../../live/urlDiagnosis";
import s from "../../workbench.module.css";
import { UrlDashboardLive } from "./UrlDashboardLive";
import { UrlLiveLive, UrlReportLive } from "./UrlLiveLive";
import { UrlNewScanLive, UrlScopeLive } from "./UrlNewScanLive";
import { UrlHead } from "./urlParts";

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
            <button className={s.btn} onClick={recheckUrl}>
              다시 조회
            </button>
          </div>
        )}
      </div>
    </>
  );
}
export const NewScanByMode = () => (
  <Supported title="새 점검">
    <UrlNewScanLive />
  </Supported>
);
export const DashboardByMode = () => (
  <Supported title="대시보드">
    <UrlDashboardLive />
  </Supported>
);
export const ScopeByMode = () => (
  <Supported title="범위 확인">
    <UrlScopeLive />
  </Supported>
);
export const LiveByMode = () => (
  <Supported title="점검 진행">
    <UrlLiveLive />
  </Supported>
);
export const ReportByMode = () => (
  <Supported title="보고서">
    <UrlReportLive />
  </Supported>
);
