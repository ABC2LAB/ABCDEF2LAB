"use client";

// 탐색 결과 화면 (운영자 전용, 지금은 예시). 발견(§7)과 경계 기록 요약(§4)을 따로 보인다.
// 데이터는 workbench/live/exploration seam 에서만 온다 — 값(헤더·쿠키·본문·query 값)은 애초에 없다(절대규칙 2).
// ※ §4: 경계 기록은 운영자 전용이다. 이 화면은 내보내기·공유·모델 입력에 넣지 않는다 → export 버튼을 두지 않는다.
// ※ live 점등은 탐색 실행기(발견 결과 전달·경계 기록) 착륙 후. 지금은 getExploration(null)=예시.
//   실 연결 때 현재 job id 만 넘기면 된다(표현/데이터 분리 — 이 화면은 안 바뀐다).

import { useEffect, useState } from "react";

import { cx } from "../../lib/cx";
import {
  changeLabel,
  DEST_CLASS_LABEL,
  type DestClass,
  type ExplorationResult,
  getExploration,
  INGRESS_LABEL,
  type Ingress,
  PROVENANCE_LABEL,
  reasonLabel,
  sourceLabel,
  summarizeBoundary,
  summarizeDiscoveries,
} from "../../live/exploration";
import { Icon } from "../../ui/icons";
import s from "../../workbench.module.css";
import { UrlHead } from "./urlParts";

const ingressLabel = (v: string) => INGRESS_LABEL[v as Ingress] ?? v;
const destClassLabel = (v: string) => DEST_CLASS_LABEL[v as DestClass] ?? v;

/** 수 칸 하나 (이름 + 값). */
function Stat({ label, value }: { label: string; value: number }) {
  return (
    <div className={s.now}>
      <span className={s.k}>{label}</span>
      <b className={s.mono}>{value}</b>
    </div>
  );
}

/** 이름별 수 줄 (사유·입구·목적지·출처). 비면 아무것도 안 그린다. */
function CountRows({ rows }: { rows: { key: string; label: string; count: number }[] }) {
  if (!rows.length) return <p className={s.help}>없음</p>;
  return (
    <ul className={s.policy}>
      {rows.map((r) => (
        <li key={r.key}>
          <span>{r.label}</span>
          <b className={s.mono}>{r.count}</b>
        </li>
      ))}
    </ul>
  );
}

/** 탐색 결과 (운영자 전용, 예시). */
export function UrlExploreLive() {
  const [data, setData] = useState<ExplorationResult | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let live = true;
    // TODO(이경준): 탐색 실행기 착륙 후 현재 job id 를 넘겨 실 결과로 점등한다(지금은 예시).
    getExploration(null)
      .then((r) => {
        if (live) setData(r);
      })
      .catch(() => {
        if (live) setFailed(true);
      });
    return () => {
      live = false;
    };
  }, []);

  const boundary = data ? summarizeBoundary(data.records) : null;
  const disc = data ? summarizeDiscoveries(data.discoveries) : null;

  return (
    <>
      <UrlHead title="탐색 결과" crumb="탐색 결과" />
      <div className={s.vb}>
        <div className={s.vm}>
          {/* §4 운영자 전용 고지 — 내보내기·공유·모델 입력 금지 */}
          <div className={s.note} role="note">
            <Icon name="info" />
            <span>경계 기록·발견은 운영자 전용입니다. 내보내기·공유·모델 입력에 넣지 않습니다.</span>
          </div>
          {data?.mock ? (
            <div className={cx(s.note, s.warn)} role="note">
              <Icon name="alert" />
              <span>예시 데이터입니다. 실제 탐색 결과는 탐색 실행기 연결 후 나옵니다.</span>
            </div>
          ) : null}
          {failed ? <p className={s.empty}>탐색 결과를 불러오지 못했습니다.</p> : null}
          {!data && !failed ? <p className={s.empty}>불러오는 중…</p> : null}

          {data && disc ? (
            <section className={s.isec} aria-label="발견">
              <h2>발견</h2>
              <p className={s.help}>탐색이 찾은 엔드포인트입니다. 진단 결과나 확정 취약점이 아닙니다.</p>
              <div className={s["crwl-top"]}>
                <Stat label="전체" value={disc.total} />
                <Stat label="실제 전송" value={disc.observed} />
                <Stat label="추정" value={disc.inferred} />
                <Stat label="범위 안" value={disc.inScope} />
                {data.discoveriesDropped ? (
                  <Stat label="범위 밖·형식 오류로 제외" value={data.discoveriesDropped} />
                ) : null}
              </div>
              <CountRows rows={disc.bySource.map((r) => ({ key: r.source, label: r.label, count: r.count }))} />
              <ul className={s.policy} aria-label="발견 목록">
                {data.discoveries.map((d) => (
                  <li key={d.id}>
                    <span className={cx(s.tag, d.provenance === "observed" ? s.in : s.ap)}>
                      {PROVENANCE_LABEL[d.provenance]}
                    </span>
                    <span className={s.mono}>
                      {d.method} {d.path}
                    </span>
                    <small>
                      {sourceLabel(d.source)} · {changeLabel(d.change)} · {d.inScope ? "범위 안" : "범위 밖"}
                      {d.queryKeys.length ? ` · query: ${d.queryKeys.join(", ")}` : ""}
                      {d.bodyKeys.length ? ` · body: ${d.bodyKeys.join(", ")}` : ""}
                      {d.contentType ? ` · ${d.contentType}` : ""}
                    </small>
                  </li>
                ))}
              </ul>
            </section>
          ) : null}

          {data && boundary ? (
            <section className={s.isec} aria-label="경계 기록">
              <h2>경계 기록</h2>
              <p className={s.help}>탐색 경계가 실제로 보냈는지의 정본입니다. 진단(E②) 차단과 따로 셉니다.</p>
              <div className={s["crwl-top"]}>
                <Stat label="전달" value={boundary.forwarded} />
                <Stat label="차단" value={boundary.denied} />
                <Stat label="인증 헤더 붙음" value={boundary.withAuthHeader} />
              </div>

              <h3>차단 사유</h3>
              <CountRows rows={boundary.byReason.map((r) => ({ key: r.reason, label: r.label, count: r.count }))} />

              <h3>입구</h3>
              <CountRows rows={boundary.byIngress.map((r) => ({ key: r.ingress, label: r.label, count: r.count }))} />

              <h3>목적지</h3>
              <CountRows
                rows={boundary.byDestClass.map((r) => ({ key: r.destClass, label: r.label, count: r.count }))}
              />

              <h3>외부 호스트</h3>
              {boundary.externalHosts.length ? (
                <ul className={s.policy}>
                  {boundary.externalHosts.map((h) => (
                    <li key={h.host}>
                      <span className={s.mono}>{h.host}</span>
                      <b className={s.mono}>{h.count}</b>
                    </li>
                  ))}
                </ul>
              ) : (
                <p className={s.help}>없음</p>
              )}

              <h3>요청 기록</h3>
              <ul className={s.policy} aria-label="경계 기록 목록">
                {data.records.map((r) => (
                  <li key={r.seq}>
                    <span className={cx(s.tag, r.decision === "forwarded" ? s.in : s.ex)}>
                      {r.decision === "forwarded" ? "전달" : "차단"}
                    </span>
                    <span className={s.mono}>
                      {r.method} {r.pathPattern ?? r.externalHost ?? "—"}
                    </span>
                    <small>
                      {ingressLabel(r.ingress)} · {destClassLabel(r.destClass)}
                      {r.reason ? ` · ${reasonLabel(r.reason)}` : ""}
                      {r.status !== null ? ` · ${r.status}` : ""}
                      {r.bytes !== null ? ` · ${r.bytes} bytes` : ""}
                      {r.hasAuthHeader ? " · 인증 헤더" : ""}
                    </small>
                  </li>
                ))}
              </ul>
            </section>
          ) : null}
        </div>
      </div>
    </>
  );
}
