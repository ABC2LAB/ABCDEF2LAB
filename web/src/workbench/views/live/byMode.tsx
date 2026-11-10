"use client";

// live 화면(대시보드 · 새 점검 · 범위 확인 · 점검 진행 · 보고서)을 서버의 새 점검 방식에 따라 고른다 (Workbench 가 이 파일만 쓴다).
// - URL 진단(diagnosis-api.v2) 서버: Url*Live (URL · 설치 프로필 → 범위 확인 → 시작 → 진행 → 저장 보고서)
// - 그 밖: 기존 화면 그대로 (POST /api/tasks 로 만든 점검, 첫 진단 모드 섹션 접기 포함)
// - 확인 중에는 어느 쪽도 그리지 않는다(기존 화면이 잠깐 보였다 바뀌지 않게). 확인 불가면 새 점검만 막고,
//   읽기 화면 셋은 기존 화면이 섹션별로 안내한다.
// 기존 화면 파일과 v2 화면 파일이 서로 import 하지 않게 고르는 자리를 따로 둔다.

import type { ReactNode } from "react";

import { cx } from "../../lib/cx";
import { LoadingRow } from "../../live/parts";
import { recheckUrl, useUrlCapability } from "../../live/urlDiagnosis";
import { Icon } from "../../ui/icons";
import s from "../../workbench.module.css";
import { DashboardLive } from "./DashboardLive";
import { LiveLive } from "./LiveLive";
import { NewScanLive } from "./NewScanLive";
import { ReportLive } from "./ReportLive";
import { ScopeLive } from "./ScopeLive";
import { UrlDashboardLive } from "./UrlDashboardLive";
import { UrlLiveLive, UrlReportLive } from "./UrlLiveLive";
import { UrlNewScanLive, UrlScopeLive } from "./UrlNewScanLive";
import { UrlHead } from "./urlParts";

/**
 * 새 점검: 서버 방식(catalog)을 먼저 확인한다. URL 진단이면 v2 폼, 404 · 501 이면 기존 v1 폼,
 * 확인 중이면 기다리고, 확인 불가면 어느 쪽으로도 만들지 않고 안내 + 다시 시도.
 */
export function NewScanByMode() {
  const cap = useUrlCapability();
  if (cap.kind === "v2") return <UrlNewScanLive />;
  if (cap.kind !== "checking" && cap.kind !== "unavailable") return <NewScanLive />;
  return (
    <>
      <UrlHead title="새 점검" crumb="새 점검" />
      <div className={s.vm}>
        {cap.kind === "checking" ? (
          <LoadingRow text="서버의 새 점검 방식을 확인하는 중…" />
        ) : (
          <div className={cx(s.note, s.warn)} role="alert">
            <Icon name="alert" />
            <span className={s["lv-grow"]}>{`새 점검 방식을 확인하지 못했습니다. ${cap.message}`}</span>
            <button className={cx(s.btn, s.sm)} type="button" onClick={recheckUrl}>
              다시 시도
            </button>
          </div>
        )}
      </div>
    </>
  );
}

/** 읽기 화면 고르기: v2 → url, 확인 중 → 기다림, 그 밖(v1 · 확인 전 · 확인 불가) → 기존 화면. */
function ReadByMode({ title, url, task }: { title: string; url: ReactNode; task: ReactNode }) {
  const cap = useUrlCapability();
  if (cap.kind === "v2") return url;
  if (cap.kind !== "checking") return task;
  return (
    <>
      <UrlHead title={title} crumb={title} />
      <div className={s.vm}>
        <LoadingRow text="서버의 점검 방식을 확인하는 중…" />
      </div>
    </>
  );
}

/** 대시보드: URL 진단이면 이어 보는 진단 · 설치 프로필(목록 API 없음), 아니면 기존 대시보드. */
export function DashboardByMode() {
  return <ReadByMode title="대시보드" url={<UrlDashboardLive />} task={<DashboardLive />} />;
}

/** 범위 확인: URL 진단이면 서버가 확인한 승인 범위 → 시작, 아니면 기존 엔드포인트 표. */
export function ScopeByMode() {
  return <ReadByMode title="범위 확인" url={<UrlScopeLive />} task={<ScopeLive />} />;
}

/** 점검 진행: URL 진단이면 진단 단계 · 사실 · 전송 통제, 아니면 기존 고른 점검 화면. */
export function LiveByMode() {
  return <ReadByMode title="점검 진행" url={<UrlLiveLive />} task={<LiveLive />} />;
}

/** 보고서: URL 진단이면 결과 저장이 확인된 실제 task 의 보고서, 아니면 기존 고른 점검 보고서. */
export function ReportByMode() {
  return <ReadByMode title="보고서" url={<UrlReportLive />} task={<ReportLive />} />;
}
