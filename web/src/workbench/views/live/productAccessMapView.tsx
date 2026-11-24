"use client";

// 제품 접근통제 지도의 격자 보기 · 공통 틀 (계약 docs/contracts/product-access-map.md §3). 행 = 정책 선언 동결본의 자원 종류, 열 = 소유(A) ·
// 교차(B) 시험 계정. 평가 KG 격자와 출처가 달라 한 격자에 섞지 않고 따로 둔다. 칸 상태는 서버가 정한 그대로 그리고
// 화면이 색을 추정하지 않는다. 소유 칸은 '선언이 정한 소유 계정'이지 A 조회 성공이 아니다(O1) — 중립색으로 둔다.
// 확정 단계 결과가 아직 없으면(confirmation=null) 격자 대신 안내만 보인다(O2).
// ※ 운영자 전용: 내보내기 · 공유 · 모델 입력에 넣지 않는다(지도 화면 고지 그대로).

import type { CSSProperties, ReactNode } from "react";

import { ApiError } from "@/lib/diagnosis/api";

import { cx } from "../../lib/cx";
import { dateTimeOf } from "../../live/map";
import {
  getProductAccessMap,
  isProductAccessMapShapeError,
  PRODUCT_MAP_ERROR_TEXT,
  PRODUCT_MAP_STATE_TEXT,
  PRODUCT_MAP_TALLY_ORDER,
  PRODUCT_MAP_TALLY_TEXT,
  type ProductAccessMap,
  type ProductMapCell,
  type ProductMapState,
  type ProductMapTally,
  productMapTally,
} from "../../live/productAccessMap";
import { CONFIRMATION_TEXT, EVIDENCE_SEAL_TEXT } from "../../live/productVerdicts";
import { type Poll, usePoll } from "../../live/usePoll";
import { verdictReasonText } from "../../live/verdictReasons";
import { Icon } from "../../ui/icons";
import s from "../../workbench.module.css";

/** 칸 색: 평가 지도와 같은 workbench 토큰. 확정 · 의심은 다른 색, 정상색(초록)은 쓰지 않는다(절대규칙 5). */
const STATE_STYLE: Record<ProductMapState, CSSProperties> = {
  confirmed: { background: "var(--crit-bg)", color: "var(--crit)", borderColor: "var(--crit)" },
  suspected: { background: "var(--mid-bg)", color: "var(--mid)", borderColor: "var(--mid)" },
  unknown: { background: "var(--und-bg)", color: "var(--und)", borderColor: "var(--rule-2)" },
  na: { background: "var(--well)", color: "var(--ink-3)", borderColor: "var(--rule)" },
  // 소유 칸은 판정이 아니다 — 채우지 않고 점선으로 '판정 대상 아님'을 드러낸다.
  owner_control: {
    background: "transparent",
    color: "var(--ink-3)",
    borderColor: "var(--rule-2)",
    borderStyle: "dashed",
  },
};
/** 합계 칩 색. 판정 없는 칸은 판단불가 판정과 같은 색이되 점선으로 갈라 보인다. */
const TALLY_STYLE: Record<ProductMapTally, CSSProperties> = {
  confirmed: STATE_STYLE.confirmed,
  suspected: STATE_STYLE.suspected,
  undecidable: STATE_STYLE.unknown,
  no_verdict: { ...STATE_STYLE.unknown, borderStyle: "dashed" },
  na: STATE_STYLE.na,
};

const wrapStyle: CSSProperties = { overflowX: "auto", marginTop: 10 };
const tableStyle: CSSProperties = { borderCollapse: "collapse", width: "100%", fontSize: 13 };
const thCol: CSSProperties = {
  textAlign: "center",
  padding: "6px 10px",
  borderBottom: "2px solid var(--rule-2)",
  color: "var(--ink-2)",
  fontWeight: 600,
};
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
  flexDirection: "column",
  alignItems: "center",
  gap: 2,
  padding: "3px 8px",
  borderRadius: 6,
  border: "1px solid",
  fontSize: 12,
  lineHeight: 1.4,
};
const roleSmall: CSSProperties = { display: "block", fontWeight: 400, color: "var(--ink-3)", fontSize: 11 };
const chipRow: CSSProperties = { display: "flex", flexWrap: "wrap", gap: 6, marginTop: 6 };
const chipBase: CSSProperties = {
  display: "inline-flex",
  alignItems: "center",
  gap: 4,
  padding: "2px 8px",
  borderRadius: 999,
  border: "1px solid",
  fontSize: 12,
};

/** 탭이 보일 때만, 작업마다 한 번 읽는다(저장 자료라 다시 읽기 버튼으로만 갱신). */
export function useProductAccessMap(taskId: string | null, active: boolean): Poll<ProductAccessMap> {
  return usePoll((signal) => getProductAccessMap(taskId as string, signal), [taskId], {
    enabled: active && !!taskId,
  });
}

/** 404 = 제품 관리 중계가 아니라 이 경로가 없다(소스 서버 등) → 구획을 두지 않는다. */
export function notInstalled(error: unknown): boolean {
  return error instanceof ApiError && error.status === 404;
}

/** 409 는 오류가 아니라 '이 점검에는 제품 지도를 만들 근거가 없다'는 안내다(§2.4). */
const CONFLICT_TEXT: Record<string, string> = {
  product_access_map_no_declaration: "이 점검은 정책 선언 없이 시작해 제품 지도를 만들 수 없습니다.",
  product_access_map_roles_undeclared: "정책 선언에 소유 · 교차 계정 역할이 없어 제품 지도를 만들 수 없습니다.",
  product_access_map_binding_unavailable: "이 점검에 결속된 회차가 없어 제품 지도를 읽을 수 없습니다.",
};

function failText(error: unknown): { text: string; warn: boolean } {
  if (isProductAccessMapShapeError(error))
    return { text: "제품 지도 응답 형식을 확인하지 못해 표시하지 않습니다.", warn: true };
  const status = error instanceof ApiError ? error.status : 0;
  if (status === 409) {
    const code = error instanceof ApiError ? error.message : "";
    return { text: CONFLICT_TEXT[code] ?? "이 점검에는 제품 지도 범위가 없습니다.", warn: false };
  }
  const text: Record<number, string> = {
    0: "제품 지도를 읽지 못했습니다(연결 실패).",
    401: "제품 지도를 읽지 못했습니다 · 다시 로그인해 주세요.",
    403: "이 점검의 제품 지도를 볼 권한이 없거나 승인이 바뀌었습니다.",
    422: "제품 지도 기록이 서로 맞지 않거나 상한을 넘어 표시하지 않습니다.",
    503: "제품 지도 서비스가 응답하지 않거나 당시 정책 선언을 확인하지 못했습니다.",
  };
  return { text: text[status] ?? `제품 지도를 읽지 못했습니다 (HTTP ${status}).`, warn: true };
}

/**
 * 제품 지도 응답의 오류 · 불러오는 중 · 확정 단계 전 안내를 지도와 그래프가 같은 글로 보이게 하는 틀(계약 §3 · §5.3).
 * 그릴 수 있을 때만 children(map, reload)을 부른다.
 */
export function ProductMapBody({
  poll,
  children,
}: {
  poll: Poll<ProductAccessMap>;
  children: (map: ProductAccessMap, reload: ReactNode) => ReactNode;
}) {
  if (poll.error) {
    const { text, warn } = failText(poll.error);
    return (
      <div className={cx(s.note, warn && s.warn)} role={warn ? "alert" : "note"}>
        <Icon name={warn ? "alert" : "info"} />
        <span className={s["lv-grow"]}>{text}</span>
        <button className={cx(s.btn, s.sm)} type="button" onClick={poll.reload}>
          제품 지도 다시 읽기
        </button>
      </div>
    );
  }
  const map = poll.data;
  if (!map)
    return (
      <p className={s.empty} role="status">
        제품 지도를 불러오는 중…
      </p>
    );
  // 저장 자료라 주기 조회를 하지 않는다. 확정 단계가 끝난 뒤 · 읽은 직후에 끝난 경우를 위해 다시 읽기 버튼을 늘 둔다.
  const reload = (
    <button className={cx(s.btn, s.sm)} type="button" onClick={poll.reload}>
      제품 지도 다시 읽기
    </button>
  );
  if (!map.confirmation)
    return (
      <div className={s.note} role="note" data-testid="product-map-pending">
        <Icon name="info" />
        <span className={s["lv-grow"]}>
          확정 단계가 끝나면 교차 계정 결과가 채워집니다. 아직 확정 단계 결과가 없습니다(진행 중이거나 시작 전).
        </span>
        {reload}
      </div>
    );
  return <>{children(map, reload)}</>;
}

/** 격자 · 그래프 위 공통 줄: 출처 딱지 · 확정 단계 상태, 합계 칩(교차 칸만), 다시 읽기, 숨긴 기록 경고. */
export function ProductMapSummary({ map, reload }: { map: ProductAccessMap; reload: ReactNode }) {
  const tallies = map.cells.map(productMapTally);
  const counts = PRODUCT_MAP_TALLY_ORDER.map((t) => [t, tallies.filter((x) => x === t).length] as const).filter(
    ([, n]) => n > 0,
  );
  const c = map.confirmation;
  return (
    <>
      <p className={s.help}>
        <span className={s.pill}>제품</span> {`출처: 정책 선언 ${map.policySource.ref} · 수평(종류 × A/B)`}
        {c ? (
          <>
            {` · ${CONFIRMATION_TEXT[c.status]}`}
            {c.reason ? (
              <span title={c.reason} data-reason={c.reason}>{` · ${verdictReasonText(c.reason)}`}</span>
            ) : null}
          </>
        ) : null}
      </p>
      {counts.length ? (
        <div style={chipRow} data-testid="product-map-counts">
          {counts.map(([t, n]) => (
            <span key={t} style={{ ...chipBase, ...TALLY_STYLE[t] }} data-tally={t}>
              {PRODUCT_MAP_TALLY_TEXT[t]} <b>{n}</b>
            </span>
          ))}
        </div>
      ) : null}
      <div style={chipRow}>{reload}</div>
      {map.hiddenRecords ? (
        <p className={cx(s.note, s.warn)} role="note">
          <Icon name="alert" />
          <span>{`판정 기록 무결성 확인 실패 ${map.hiddenRecords}건 · 값과 자원 종류를 숨겨 칸에 두지 않습니다`}</span>
        </p>
      ) : null}
    </>
  );
}

/** 격자 · 그래프 아래 공통 고지(절대 규칙 5). */
export function ProductMapFootnote() {
  return (
    <p className={s.help}>
      ‘판정 불가’ · ‘관측 근거 없음’은 취약점이 없다는 뜻이 아닙니다. 확정도 검토 대상 후보이며 자동 확정 발견이
      아닙니다.
    </p>
  );
}

/** 격자 보기: 행 = 자원 종류, 열 = 소유(A) · 교차(B). 접근통제 지도 구획(productAccessMapSection)이 그래프 보기와 바꿔 쓴다. */
export function ProductMapGrid({ map, reload }: { map: ProductAccessMap; reload: ReactNode }) {
  return (
    <>
      <ProductMapSummary map={map} reload={reload} />
      <div style={wrapStyle}>
        <table style={tableStyle} aria-label="자원 종류 × 소유 · 교차 계정 제품 접근통제 격자">
          <thead>
            <tr>
              <th scope="col" style={{ ...thCol, textAlign: "left" }}>
                자원 종류
              </th>
              <th scope="col" style={thCol}>
                {map.owner}
                <small style={roleSmall}>소유(A)</small>
              </th>
              <th scope="col" style={thCol}>
                {map.cross}
                <small style={roleSmall}>교차(B)</small>
              </th>
            </tr>
          </thead>
          <tbody>
            {map.resources.map((r) => (
              <tr key={r.resourceType}>
                <th scope="row" style={thRow} className={s.mono}>
                  {r.resourceType}
                  <small style={roleSmall}>{r.sharing === "shared" ? "공유 선언" : "소유자 전용 선언"}</small>
                </th>
                {(["owner", "cross"] as const).map((role) => {
                  const cell = map.cells.find((x) => x.resourceType === r.resourceType && x.role === role);
                  return (
                    <td key={role} style={tdStyle}>
                      {cell ? <Cell cell={cell} /> : null}
                    </td>
                  );
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <ProductMapFootnote />
    </>
  );
}

function Cell({ cell }: { cell: ProductMapCell }) {
  const v = cell.verdict;
  // 판정 불가는 저장 판정이면 판정 번호 · 사유를, 판정 없는 칸이면 투영 오류 문구를 붙여 가른다(둘을 합치지 않는다).
  const label = PRODUCT_MAP_STATE_TEXT[cell.state];
  const reasons = v ? [...v.oracleReasonCodes, ...v.productReasonCodes] : [];
  return (
    <span
      style={{ ...cellBase, ...STATE_STYLE[cell.state] }}
      data-state={cell.state}
      data-error={cell.projectionError ?? undefined}
      title={reasons.length ? reasons.join(", ") : undefined}
    >
      <span>{label}</span>
      {cell.projectionError ? <small>{PRODUCT_MAP_ERROR_TEXT[cell.projectionError]}</small> : null}
      {v && reasons.length ? <small>{reasons.map(verdictReasonText).join(" · ")}</small> : null}
      {v?.evidenceSeal?.status === "unconfirmed" ? (
        <small data-seal="unconfirmed">{EVIDENCE_SEAL_TEXT.unconfirmed}</small>
      ) : null}
      {v ? <small>{`판정 ${v.verdictId} · ${dateTimeOf(v.observedAt)}`}</small> : null}
    </span>
  );
}
