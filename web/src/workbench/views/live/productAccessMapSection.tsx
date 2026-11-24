"use client";

// 제품 접근통제 지도 구획 (계약 docs/contracts/product-access-map.md §3 · §5). 격자(§3)와 소유 · 접근 그래프(§5)는 같은 제품
// 지도 응답(GET /api/tasks/{id}/product-access-map)을 두 가지로 그린 것이다. 그래서 한 번만 읽고 [격자 | 그래프]로 보는 방법만
// 바꾼다(11/24 — 그래프가 보고서 '자산 지식 그래프' 탭에 있어 자산 그래프와 혼동됨). 오류 · 대기 · 확정 단계 전 안내는 두 보기가
// 같은 틀(ProductMapBody)을 쓴다. 평가 KG 격자와 출처가 달라 섞지 않는다.
// ※ 운영자 전용: 내보내기 · 공유 · 모델 입력에 넣지 않는다.

import { useState } from "react";

import s from "../../workbench.module.css";
import { notInstalled, ProductMapBody, ProductMapGrid, useProductAccessMap } from "./productAccessMapView";
import { ProductMapGraph } from "./productKgGraphView";

export type ProductMapView = "grid" | "graph";

const VIEWS: readonly (readonly [ProductMapView, string])[] = [
  ["grid", "격자"],
  ["graph", "그래프"],
];

/** 접근통제 지도(보고서 탭 · 지도 화면)의 제품 구획. 탭이 보일 때만, 작업마다 한 번 읽고 보기 전환은 다시 읽지 않는다. */
export function ProductAccessMapSection({
  taskId,
  active,
  defaultView = "grid",
}: {
  taskId: string | null;
  active: boolean;
  defaultView?: ProductMapView;
}) {
  const poll = useProductAccessMap(taskId, active);
  const [view, setView] = useState<ProductMapView>(defaultView);
  if (!taskId) return null;
  // 404 = 제품 관리 중계가 아니라 이 경로가 없다(소스 서버 등) → 구획을 두지 않는다
  if (poll.error && notInstalled(poll.error)) return null;
  return (
    <section className={s.isec} aria-label="제품 접근통제 지도">
      <h2>제품 접근통제 지도 (정책 선언 기준 · 수평)</h2>
      <p className={s.help}>
        정책 선언의 자원 종류 × 소유 · 교차 시험 계정과 교차 계정 판정입니다. 격자와 그래프는 같은 자료를 다르게 보인
        것이고, 평가 KG 격자 · 자산 지식 그래프와 출처가 달라 잇지 않습니다. 계정 자격 · 표식 값은 없습니다.
      </p>
      {/* biome-ignore lint/a11y/useSemanticElements: 보기 전환 버튼 묶음 (fieldset 은 폼 입력 묶음이라 맞지 않음) */}
      <div className={s.seg} role="group" aria-label="제품 접근통제 지도 보기">
        {VIEWS.map(([k, label]) => (
          <button key={k} type="button" aria-pressed={view === k} onClick={() => setView(k)}>
            {label}
          </button>
        ))}
      </div>
      <ProductMapBody poll={poll}>
        {(map, reload) =>
          view === "graph" ? (
            <ProductMapGraph map={map} reload={reload} />
          ) : (
            <ProductMapGrid map={map} reload={reload} />
          )
        }
      </ProductMapBody>
    </section>
  );
}
