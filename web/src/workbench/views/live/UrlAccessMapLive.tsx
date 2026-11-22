"use client";

// 접근통제 지도 화면 (운영자 전용). v0.1 = 수평(cross-account) IDOR 조회 전용(kg-policy §0·§4.2(4)).
// 데이터는 lib/diagnosis/kg seam(getAccessMap)에서만 온다. 셀 색은 화면이 추정하지 않고 리졸버(buildCellResolver)가
// 정규화한 decision 으로만 칠한다(fail-closed, kg-policy §4.2(2)). 격자에는 판정 상태만 — 계정 원문·표식 값은 없다(절대규칙 2).
// ※ §4 운영자 전용: 내보내기·공유·모델 입력에 넣지 않는다 → export 버튼을 두지 않는다.
// ※ 현재 Job 의 task 지도만 읽는다(GET /api/tasks/{id}/access-map). Job 전환 · 재로그인 · 조회 실패 때 이전 격자를 버리고
//   예시 · 빈 격자로 채우지 않는다(product-report-graph-read.md).

import type { CSSProperties } from "react";
import { useEffect, useState } from "react";

import { ApiError } from "@/lib/diagnosis/api";
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
import { useUrl, useUrlSelectionKey } from "../../live/urlDiagnosis";
import { useShell } from "../../shell";
import { Icon } from "../../ui/icons";
import s from "../../workbench.module.css";
import { ProductVerdictMap } from "./productVerdictViews";
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

/**
 * 읽기 실패 문구 — 상태 코드마다 뜻이 다르다(product-report-graph-read.md 오류 표). 어느 쪽이든 이전 격자 · 예시 · 빈 격자로
 * 대신하지 않는다.
 */
/** 409 = 이 점검에 지도 범위가 없음(계약 access-map-read.md 실패 표). 오류가 아니라 안내로 보인다. */
function noMapScope(error: unknown): boolean {
  return error instanceof ApiError && error.status === 409;
}

function failureText(error: unknown): string {
  const status = error instanceof ApiError ? error.status : -1;
  if (status === 409)
    return "이 점검은 접근통제 지도를 만들 수 없습니다 — KG 미등록 · 실행 전 · 세대 무효이거나 조회 중 점검 문맥이 바뀌었습니다. 빈 지도로 대신하지 않습니다.";
  if (status === 403) return "이 점검의 지도를 볼 권한을 확인하지 못했습니다. 이전 결과는 표시하지 않습니다.";
  if (status === 401) return "로그인이 끝났습니다. 다시 로그인한 뒤 선택한 점검부터 다시 조회합니다.";
  if (status === 404) return "이 점검의 task 를 찾지 못했습니다(삭제됐거나 없음). 점검 선택을 갱신하세요.";
  if (status === 400) return "지도 요청 형식이 맞지 않습니다.";
  if (status === 422) return "다른 점검의 지도이거나 응답 모양을 확인할 수 없어 표시하지 않습니다.";
  if (status === 502) return "중계 응답을 확인하지 못했습니다. 정상 데이터로 보지 않습니다.";
  if (status === 0) return "엔진에 연결하지 못했습니다. 이전 결과는 표시하지 않습니다.";
  return `접근통제 지도를 불러오지 못했습니다${status > 0 ? ` (HTTP ${status})` : ""}. 이전 결과는 표시하지 않습니다.`;
}

/**
 * 한 번 읽은 결과. key = 이 결과를 부른 요청 — 선택(useUrlSelectionKey) + task + Job revision + 진입 세대 + 다시 읽기 횟수.
 * 같은 task 를 다시 고르거나, 같은 선택에서 화면에 다시 들어오거나, 상태가 바뀌어 새로 부르면 지난 결과를 쓰지 않는다.
 */
type Read = { key: string; map: AccessMap | null; error: unknown };

/** 접근통제 지도 화면 (운영자 전용 · 현재 Job 의 task). 본문은 보고서 '접근통제 지도' 탭과 같은 AccessMapPanel 이다. */
export function UrlAccessMapLive() {
  const { route } = useShell();
  return (
    <>
      <UrlHead title="접근통제 지도" crumb="접근통제 지도" />
      <div className={s.vb}>
        <div className={s.vm}>
          <AccessMapPanel active={route === "accessmap"} />
        </div>
      </div>
    </>
  );
}

/**
 * 접근통제 지도 본문(운영자 전용 · 현재 Job 의 task). active 일 때만 읽는다 — 지도 화면은 그 화면이 보일 때, 보고서는
 * '접근통제 지도' 탭이 보일 때. 다시 active 가 되면 지난 결과를 버리고 새로 읽는다(진입 세대).
 */
export function AccessMapPanel({ active }: { active: boolean }) {
  // 서버에서 확인된(verified) 현재 Job 의 task 만. 없으면 부르지 않고 예시도 그리지 않는다.
  const taskId = useUrl((st) => (st.verified ? (st.job?.task_id ?? null) : null));
  // 점검 상태가 바뀌면(결속 · 실행 준비 · 마감) 열린 화면에서 다시 읽는다 — 처음 409 뒤 준비가 끝난 같은 task 도 새 응답을 본다
  const revision = useUrl((st) => st.job?.revision ?? 0);
  const selection = useUrlSelectionKey();
  const [read, setRead] = useState<Read | null>(null);
  const [attempt, setAttempt] = useState(0);
  // 진입 세대: 읽을 수 있는 상태(화면이 보이고 확인된 Job 의 task 가 있음)로 들어올 때마다 올린다 — 화면에 다시 들어올 때,
  // 상태 조회 실패 · 권한 철회로 Job 이 비었다가 같은 task · revision 으로 돌아올 때(#691 리뷰) 모두 새 조회로 구분한다.
  // 렌더 중에 맞춰서 같은 렌더에서 지난 진입의 결과를 버린다
  const live = active && !!taskId;
  const [entry, setEntry] = useState({ live, gen: 0 });
  if (entry.live !== live) setEntry({ live, gen: live ? entry.gen + 1 : entry.gen });
  const request = taskId ? `${selection}|${taskId}|${revision}|${entry.gen}|${attempt}` : "";

  useEffect(() => {
    if (!taskId || !active || !request) return;
    let current = true;
    const controller = new AbortController();
    getAccessMap(taskId, controller.signal)
      .then((m) => {
        if (current) setRead({ key: request, map: m, error: null });
      })
      .catch((error: unknown) => {
        if (current) setRead({ key: request, map: null, error });
      });
    return () => {
      current = false;
      controller.abort();
    };
  }, [taskId, active, request]);

  // 지금 요청이 받은 결과만 그린다 — 다른 선택 · task · 이전 진입 · 이전 revision 의 결과는 새 응답이 올 때까지 '불러오는 중…'
  const now = read && request && read.key === request ? read : null;
  const map = now?.map ?? null;
  const resolve = map ? buildCellResolver(map.cells) : null;
  const hCounts = map ? countDecisions(map, "H") : null;
  const vCounts = map ? countDecisions(map, "V") : null;
  const hPrincipals = map ? map.principals.filter((p) => p.axis === "H") : [];
  const vPrincipals = map ? map.principals.filter((p) => p.axis === "V") : [];
  const issues = map ? mapProjectionIssues(map) : null;

  return (
    <>
      {/* §4 운영자 전용 고지 — 내보내기·공유·모델 입력 금지 */}
      <div className={s.note} role="note">
        <Icon name="info" />
        <span>접근통제 지도는 운영자 전용입니다. 내보내기·공유·모델 입력에 넣지 않습니다.</span>
      </div>
      {!taskId ? <p className={s.empty}>실제 진단 작업을 선택하세요.</p> : null}
      {taskId && !now ? <p className={s.empty}>불러오는 중…</p> : null}
      {now?.error && noMapScope(now.error) ? (
        // 409(현재 등록·실행 범위 없음)는 이 점검에 평가 KG 지도 범위가 없다는 뜻이다. 제품 회차는 controlled-read KG
        // 등록을 하지 않아 늘 이렇다(1차 리허설 F7) — 오류 경고 대신 안내하고, 같은 화면의 제품 확정 단계 표를 보게 한다
        <p className={s.note} role="note">
          이 점검에는 평가 KG 접근통제 지도 범위가 없습니다(KG 미등록 · 실행 전 · 세대 무효). 교차 계정 확인 결과는 아래
          제품 확정 단계 표에 있습니다.
        </p>
      ) : null}
      {now?.error && !noMapScope(now.error) ? (
        <div className={cx(s.note, s.warn)} role="alert">
          <Icon name="alert" />
          <span className={s["lv-grow"]}>{failureText(now.error)}</span>
          <button className={cx(s.btn, s.sm)} type="button" onClick={() => setAttempt((n) => n + 1)}>
            다시 읽기
          </button>
        </div>
      ) : null}

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
          <p className={s.help}>신원 × 자원 교차 접근 판정입니다. 색은 판정이며, 격자에는 계정·표식 값이 없습니다.</p>
          <Legend />
          {/* deny-ok(정상 차단) 보류 안내(§4.2(3) Phase 3) + 미검사 Phase 2 안내 */}
          <p className={s.help}>
            정상 차단(deny-ok)은 앱 인가 차단 관측 계약 전까지 산출하지 않습니다(Phase 3) — 교차 차단 칸은 ‘판정 불가
            (차단 미증명)’로 나옵니다. ‘미검사’는 명시적 미시도이며 ‘안전’이 아닙니다(Phase 2).
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
      {/* 제품 확정 단계 판정은 평가 KG 격자와 축 출처가 달라 아래 표로 따로(#801 §3). 평가 지도가 409(KG 미등록)여도 읽는다 */}
      <ProductVerdictMap taskId={taskId} active={active} />
    </>
  );
}
