"use client";
import { useUrl } from "../../live/urlDiagnosis";
import s from "../../workbench.module.css";
import { UrlHead, UrlNoJob, UrlNotices, UrlScopeList } from "./urlParts";
import { WorkflowCrawlLive } from "./workflowViews";
export function UrlAssetsLive() {
  const job = useUrl((st) => st.job);
  if (job?.input.workflow_mode) return <WorkflowCrawlLive />;
  return (
    <>
      <UrlHead title="자산 탐색" crumb="자산 탐색" />
      <div className={s.vm}>
        <section className={s.sec}>
          <h2>서버가 확인한 대상 자산</h2>
          {job?.scope ? <UrlScopeList scope={job.scope} /> : <UrlNoJob what="자산 탐색" />}
          <p className={s.help}>
            설치된 승인 범위입니다. 이 서버는 별도 자산 인벤토리·자동 탐색 API를 제공하지 않습니다.
          </p>
          <UrlNotices />
        </section>
      </div>
    </>
  );
}
