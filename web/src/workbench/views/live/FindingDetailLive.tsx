"use client";

// live 발견 상세 (점검 진행 · 보고서 공통). demo FindingDetail 과 같은 머리·단락 모양에 엔진 값만 넣는다.
// - 상세 조회는 finding_id 로(fetchFinding). finding_id 가 없거나 조회가 안 되면 목록 줄 값으로 보인다.
// - 요약·증거·보고서 원문은 maskSecrets 를 거쳐 글로만(줄바꿈 유지). status 는 "처리 상태"이지 판정이 아니다.

import { cx } from "../../lib/cx";
import { fetchFinding, type LiveFinding } from "../../live/api";
import { dateTimeOf, findingTitle } from "../../live/liveReport";
import { findingStatusLabel, sevKey } from "../../live/map";
import { maskSecrets } from "../../live/mask";
import { ErrorNote, LoadingRow, MaskedNote, Unsupported } from "../../live/parts";
import { usePoll } from "../../live/usePoll";
import { InspHead, Isec, Kv, Sev } from "../../ui/parts";
import s from "../../workbench.module.css";

/** 엔진 심각도 딱지. 넷 중 하나면 <Sev>, 아니면 원문 딱지. */
export function SevBadge({ severity }: { severity: string }) {
  const k = sevKey(severity);
  return k ? <Sev k={k} /> : <span className={cx(s.tag, s.ex)}>{severity || "심각도 없음"}</span>;
}

/** 처리 상태 딱지 (판정 아님). */
export function StatusTag({ status }: { status: string }) {
  return (
    <span className={cx(s.tag, s.lg)} title="처리 상태 — 판정(확정·의심)이 아닙니다">
      {findingStatusLabel(status)}
    </span>
  );
}

export function FindingDetailLive({
  row,
  host,
  enabled,
}: {
  /** 목록 줄 (상세를 못 받으면 이 값으로) */
  row: LiveFinding;
  /** 머리에 보일 점검 대상 */
  host: string;
  /** 화면이 보일 때만 상세를 부른다 */
  enabled: boolean;
}) {
  const fid = row.finding_id;
  const detail = usePoll(() => fetchFinding(fid), [fid], { enabled: enabled && !!fid });
  const d: LiveFinding = detail.data ?? row;
  const report = detail.data?.report ?? "";
  const fromList = !fid || (!detail.data && !!detail.error);

  return (
    <>
      <InspHead
        eyebrow={
          <>
            {d.vulnclass || "분류 없음"} · <span className={s.host}>{host}</span>
          </>
        }
        title={findingTitle(d)}
        chips={
          <>
            <SevBadge severity={d.severity} />
            <StatusTag status={d.status} />
            {d.inherited ? <span className={cx(s.tag, s.ex)}>이어받은 발견</span> : null}
          </>
        }
      />
      {d.assets.length ? <div className={s["vi-ep"]}>{maskSecrets(d.assets[0].label)}</div> : null}
      {fid && detail.loading && !detail.data ? <LoadingRow text="상세 불러오는 중…" /> : null}
      {fid && detail.error && !detail.data ? (
        <Isec>
          <ErrorNote error={detail.error} onRetry={detail.reload} />
        </Isec>
      ) : null}
      {fromList ? (
        <Isec>
          <p className={s.help}>
            {fid ? "상세를 받지 못해 목록 값으로 보입니다." : "독립 발견 기록(finding_id)이 없어 목록 값으로 보입니다."}
          </p>
        </Isec>
      ) : null}
      <Isec t="요약">
        {d.summary ? (
          <p className={s["lv-pre"]}>{maskSecrets(d.summary)}</p>
        ) : (
          <p className={s.empty}>요약이 없습니다.</p>
        )}
      </Isec>
      <Isec t="증거">
        {d.evidence ? (
          <pre className={cx(s.code, s["lv-pre"])}>{maskSecrets(d.evidence)}</pre>
        ) : (
          <p className={s.empty}>증거 글이 없습니다.</p>
        )}
      </Isec>
      {report ? (
        <Isec t="상세 보고서 · Markdown 원문">
          <pre className={cx(s.code, s["lv-pre"])}>{maskSecrets(report)}</pre>
          {detail.data?.report_stale ? <p className={s.help}>증거가 바뀐 뒤 다시 만들지 않은 보고서입니다.</p> : null}
        </Isec>
      ) : null}
      <Isec>
        <MaskedNote />
      </Isec>
      {d.assets.length ? (
        <Isec t={`걸린 자산 ${d.assets.length}`}>
          <div className={s.chips}>
            {d.assets.map((a) => (
              <span key={a.id} className={s.chip} title={a.type}>
                {maskSecrets(a.label)}
              </span>
            ))}
          </div>
        </Isec>
      ) : null}
      <Isec t="기록">
        <Kv
          rows={[
            ["처리 상태", findingStatusLabel(d.status)],
            ["관련 트래픽", `${d.traffic_count}건`],
            ["발견 시각", dateTimeOf(d.ts) || "—"],
            ["발견 id", fid || "없음"],
          ]}
        />
      </Isec>
      <Isec t="판정 · 재현 명령">
        <Unsupported what="판정(확정·의심·판단 불가) · 재현 명령" />
      </Isec>
    </>
  );
}
