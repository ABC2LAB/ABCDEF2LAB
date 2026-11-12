"use client";

// 접근통제 지도 화면 (운영자 전용, 지금은 예시). v0.1 = 수평(cross-account) IDOR 조회 전용(kg-policy §0·§4.2(4)).
// 데이터는 lib/diagnosis/kg seam(getAccessMap)에서만 온다. 셀 색은 화면이 추정하지 않고 리졸버(buildCellResolver)가
// 정규화한 decision 으로만 칠한다(fail-closed, kg-policy §4.2(2)). 격자에는 판정 상태만 — 계정 원문·표식 값은 없다(절대규칙 2).
// ※ §4 운영자 전용: 내보내기·공유·모델 입력에 넣지 않는다 → export 버튼을 두지 않는다.
// ※ live 점등은 실 KG API 연동 시 getAccessMap(taskId) 만 교체하면 된다(표현/데이터 분리 — 이 화면은 안 바뀐다).
//   지금은 getAccessMap(null)=예시(MOCK_MAP), mock 배너를 띄운다.

import type { CSSProperties } from "react";
import { useEffect, useState } from "react";

import {
  type AccessDecision,
  type AccessMap,
  buildCellResolver,
  countDecisions,
  DECISION_BADGE,
  DECISION_LABEL,
  getAccessMap,
  hasProjectionIssues,
  type KgPrincipal,
  type KgResource,
  mapProjectionIssues,
  type ResolvedCell,
} from "@/lib/diagnosis/kg";

import { cx } from "../../lib/cx";
import { Icon } from "../../ui/icons";
import s from "../../workbench.module.css";
import { UrlHead } from "./urlParts";

/**
 * decision → 셀 색(인라인 React.CSSProperties). kg.ts 의 DECISION_CELL(옛 abc2lab.css 클래스)과
 * coverage.ts STATUS_STYLE(옛 토큰 --danger·--viol 등)은 지금 안 먹어서, 실재하는 workbench 토큰으로 새로 매핑한다
 * (라이트·다크 자동). 확정(vuln)과 의심(suspected)은 **서로 다른 색**이다(§4.2(3) 축소 화면 오인 방지).
 * 정상색(초록)은 allow-ok 뿐 — 미검사·기록없음·판정불가를 '안전'으로 칠하지 않는다(절대규칙 5).
 */
const DECISION_STYLE: Record<AccessDecision, CSSProperties> = {
  "allow-ok": { background: "var(--ok-bg)", color: "var(--ok)", borderColor: "var(--ok)" },
  vuln: { background: "var(--crit-bg)", color: "var(--crit)", borderColor: "var(--crit)" },
  suspected: { background: "var(--mid-bg)", color: "var(--mid)", borderColor: "var(--mid)" },
  unknown: { background: "var(--und-bg)", color: "var(--und)", borderColor: "var(--rule-2)" },
  blank: { background: "var(--well)", color: "var(--ink-3)", borderColor: "var(--rule)" },
  // 미검사는 정상색이 아님 — 점선으로 '시도 안 함'을 드러낸다(절대규칙 5, coverage.ts untested 와 같은 결).
  untested: { background: "transparent", color: "var(--ink-3)", borderColor: "var(--rule-2)", borderStyle: "dashed" },
  na: { background: "var(--well)", color: "var(--ink-3)", borderColor: "var(--rule)" },
  // deny-ok 는 리졸버가 unknown 으로 내리므로 실제로는 칠해지지 않는다(§4.2(3) Phase 3 보류). 범례엔 넣지 않는다.
  "deny-ok": { background: "var(--und-bg)", color: "var(--und)", borderColor: "var(--rule-2)" },
};

/** 헤드라인 칩·범례 표시 순서(심각/주의 → 미검사 → 해당없음). deny-ok 는 제외(산출 안 됨). */
const DISPLAY_ORDER: AccessDecision[] = ["vuln", "suspected", "allow-ok", "unknown", "blank", "untested", "na"];

// ── 인라인 스타일(격자 뼈대). 색은 DECISION_STYLE, 토큰은 workbench.module.css 의 것만 쓴다.
const wrapStyle: CSSProperties = { overflowX: "auto", marginTop: 10 };
const tableStyle: CSSProperties = { borderCollapse: "collapse", width: "100%", fontSize: 13 };
const thCol: CSSProperties = {
  textAlign: "center",
  padding: "6px 10px",
  borderBottom: "2px solid var(--rule-2)",
  verticalAlign: "bottom",
  color: "var(--ink-2)",
  fontWeight: 600,
};
const thCorner: CSSProperties = { ...thCol, textAlign: "left", color: "var(--ink-3)", fontWeight: 500 };
const thRow: CSSProperties = {
  textAlign: "left",
  padding: "6px 10px",
  borderBottom: "1px solid var(--rule)",
  whiteSpace: "nowrap",
  color: "var(--ink)",
  fontWeight: 600,
};
const tdStyle: CSSProperties = { padding: "4px 6px", borderBottom: "1px solid var(--rule)", textAlign: "center" };
const cellBase: CSSProperties = {
  display: "inline-flex",
  alignItems: "center",
  gap: 4,
  padding: "3px 8px",
  borderRadius: 6,
  border: "1px solid",
  fontSize: 12,
  lineHeight: 1.4,
  whiteSpace: "nowrap",
};
const badgeStyle: CSSProperties = {
  fontSize: 10,
  fontWeight: 700,
  padding: "0 4px",
  borderRadius: 3,
  border: "1px solid currentColor",
};
const groupRow: CSSProperties = {
  textAlign: "left",
  padding: "8px 10px 4px",
  color: "var(--ink-3)",
  fontWeight: 600,
  fontSize: 12,
  borderBottom: "1px solid var(--rule)",
};
const ownerSmall: CSSProperties = { display: "block", fontWeight: 400, color: "var(--ink-3)", fontSize: 11 };
const chipRow: CSSProperties = { display: "flex", flexWrap: "wrap", gap: 6, marginTop: 6 };
const chipBase: CSSProperties = {
  display: "inline-flex",
  alignItems: "center",
  gap: 5,
  padding: "3px 9px",
  borderRadius: 999,
  border: "1px solid",
  fontSize: 12,
};
const legendList: CSSProperties = {
  display: "flex",
  flexWrap: "wrap",
  gap: "6px 14px",
  listStyle: "none",
  padding: 0,
  margin: "6px 0 0",
};
const legendItem: CSSProperties = {
  display: "inline-flex",
  alignItems: "center",
  gap: 6,
  fontSize: 12,
  color: "var(--ink-2)",
};
const swatch: CSSProperties = { width: 14, height: 14, borderRadius: 4, border: "1px solid", flex: "0 0 auto" };
const previewRow: CSSProperties = { opacity: 0.72 };

/** 한 교차 칸. 리졸버가 준 ResolvedCell 만 쓴다 — 화면은 판정·조합하지 않는다(절대규칙 4). */
function MapCell({ resolved }: { resolved: ResolvedCell }) {
  const { decision, note, projectionError } = resolved;
  const label = DECISION_LABEL[decision];
  const badge = DECISION_BADGE[decision];
  // hover: 라벨 + 오라클 사유(note) + projection 오류 사유. note 는 seam 이 필드명·사유 코드만 담는다(값 없음, 절대규칙 2).
  const title = [label, note, projectionError].filter(Boolean).join(" · ");
  return (
    <td style={tdStyle}>
      <span data-decision={decision} title={title} style={{ ...cellBase, ...DECISION_STYLE[decision] }}>
        <span>{label}</span>
        {badge ? <b style={badgeStyle}>{badge}</b> : null}
      </span>
    </td>
  );
}

/** principal 한 행(자원 수만큼 칸). preview=수직(후속 미리보기) 행은 흐리게 + 표시. */
function MapRow({
  principal,
  resources,
  resolve,
  preview,
}: {
  principal: KgPrincipal;
  resources: KgResource[];
  resolve: (principalRef: string, resourceId: string) => ResolvedCell;
  preview?: boolean;
}) {
  return (
    <tr style={preview ? previewRow : undefined}>
      <th scope="row" style={thRow}>
        {principal.ref}
        <small style={ownerSmall}>
          {principal.role}
          {preview ? " · 미리보기" : ""}
        </small>
      </th>
      {resources.map((r) => (
        <MapCell key={r.id} resolved={resolve(principal.ref, r.id)} />
      ))}
    </tr>
  );
}

/** 색 범례(§4.2(3) 축소 화면 오인 방지 — 색을 글로도 설명). */
function Legend() {
  return (
    <ul style={legendList} aria-label="범례">
      {DISPLAY_ORDER.map((d) => (
        <li key={d} style={legendItem}>
          <span aria-hidden="true" style={{ ...swatch, ...DECISION_STYLE[d] }} />
          <span>
            {DECISION_LABEL[d]}
            {DECISION_BADGE[d] ? ` (${DECISION_BADGE[d]})` : ""}
          </span>
        </li>
      ))}
    </ul>
  );
}

/** 접근통제 지도 (운영자 전용, 예시). */
export function UrlAccessMapLive() {
  const [map, setMap] = useState<AccessMap | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let live = true;
    // TODO(이경준): 실 KG API 연동 시 현재 task id 를 넘겨 실 지도로 점등한다(지금은 예시). getAccessMap 만 교체.
    getAccessMap(null)
      .then((m) => {
        if (live) setMap(m);
      })
      .catch(() => {
        if (live) setFailed(true);
      });
    return () => {
      live = false;
    };
  }, []);

  const resolve = map ? buildCellResolver(map.cells) : null;
  const hCounts = map ? countDecisions(map, "H") : null;
  const vCounts = map ? countDecisions(map, "V") : null;
  const hPrincipals = map ? map.principals.filter((p) => p.axis === "H") : [];
  const vPrincipals = map ? map.principals.filter((p) => p.axis === "V") : [];
  const issues = map ? mapProjectionIssues(map) : null;

  return (
    <>
      <UrlHead title="접근통제 지도" crumb="접근통제 지도" />
      <div className={s.vb}>
        <div className={s.vm}>
          {/* §4 운영자 전용 고지 — 내보내기·공유·모델 입력 금지 */}
          <div className={s.note} role="note">
            <Icon name="info" />
            <span>접근통제 지도는 운영자 전용입니다. 내보내기·공유·모델 입력에 넣지 않습니다.</span>
          </div>
          {map?.mock ? (
            <div className={cx(s.note, s.warn)} role="note">
              <Icon name="alert" />
              <span>예시 데이터입니다. 실제 접근통제 지도는 KG API 연결 후 나옵니다.</span>
            </div>
          ) : null}
          {failed ? <p className={s.empty}>접근통제 지도를 불러오지 못했습니다.</p> : null}
          {!map && !failed ? <p className={s.empty}>불러오는 중…</p> : null}

          {/* projection 무결성 경고 — 데이터 불일치를 숨기지 않는다(§4.2(2)·N4, fail-closed) */}
          {map && issues && hasProjectionIssues(issues) ? (
            <div className={cx(s.note, s.warn)} role="alert">
              <Icon name="alert" />
              <span>
                지도 데이터 불일치: 격자 밖 {issues.outOfGrid} · 중복 신원 {issues.dupPrincipals} · 중복 자원{" "}
                {issues.dupResources}. 숨기지 않고 표시합니다.
              </span>
            </div>
          ) : null}

          {map && hCounts ? (
            <section className={s.isec} aria-label="판정 요약">
              <h2>수평(교차 계정) 판정</h2>
              <p className={s.help}>
                판정 축은 수평(같은 역할·교차 계정)입니다. 수직(역할 레벨)은 아래 격자에서 후속 미리보기로만 표시합니다.
              </p>
              {DISPLAY_ORDER.some((d) => hCounts[d] > 0) ? (
                <div style={chipRow}>
                  {DISPLAY_ORDER.filter((d) => hCounts[d] > 0).map((d) => (
                    <span key={d} style={{ ...chipBase, ...DECISION_STYLE[d] }}>
                      {DECISION_LABEL[d]} <b>{hCounts[d]}</b>
                    </span>
                  ))}
                </div>
              ) : (
                <p className={s.help}>집계할 수평 판정이 없습니다.</p>
              )}
            </section>
          ) : null}

          {map && resolve ? (
            <section className={s.isec} aria-label="접근통제 격자">
              <h2>접근통제 격자</h2>
              <p className={s.help}>
                신원 × 자원 교차 접근 판정입니다. 색은 판정이며, 격자에는 계정·표식 값이 없습니다.
              </p>
              <Legend />
              {/* deny-ok(정상 차단) 보류 안내(§4.2(3) Phase 3) + 미검사 Phase 2 안내 */}
              <p className={s.help}>
                정상 차단(deny-ok)은 앱 인가 차단 관측 계약 전까지 산출하지 않습니다(Phase 3) — 교차 차단 칸은 ‘판정
                불가 (차단 미증명)’로 나옵니다. ‘미검사’는 명시적 미시도이며 ‘안전’이 아닙니다(Phase 2).
              </p>
              <div style={wrapStyle}>
                <table style={tableStyle} aria-label="신원 × 자원 접근통제 격자">
                  <thead>
                    <tr>
                      <th scope="col" style={thCorner}>
                        신원 \ 자원
                      </th>
                      {map.resources.map((r) => (
                        <th key={r.id} scope="col" style={thCol}>
                          {r.label}
                          <small style={ownerSmall}>소유 {r.ownerRef}</small>
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {hPrincipals.map((p) => (
                      <MapRow key={p.ref} principal={p} resources={map.resources} resolve={resolve} />
                    ))}
                    {vPrincipals.length ? (
                      <>
                        <tr>
                          <th scope="colgroup" colSpan={map.resources.length + 1} style={groupRow}>
                            수직 — 후속 미리보기(판정 축 아님)
                          </th>
                        </tr>
                        {vPrincipals.map((p) => (
                          <MapRow key={p.ref} principal={p} resources={map.resources} resolve={resolve} preview />
                        ))}
                      </>
                    ) : null}
                  </tbody>
                </table>
              </div>
              {vCounts && DISPLAY_ORDER.some((d) => vCounts[d] > 0) ? (
                <p className={s.help}>
                  수직 미리보기 합계:{" "}
                  {DISPLAY_ORDER.filter((d) => vCounts[d] > 0)
                    .map((d) => `${DECISION_LABEL[d]} ${vCounts[d]}`)
                    .join(" · ")}
                </p>
              ) : null}
              {/* 정책 버전은 변형·절단 없이 그대로 표기(§4.1 헤더 행 — 감사 표기와 1:1) */}
              <p className={s.help}>
                정책 버전: <span className={s.mono}>{map.policyVersion}</span>
              </p>
            </section>
          ) : null}
        </div>
      </div>
    </>
  );
}
