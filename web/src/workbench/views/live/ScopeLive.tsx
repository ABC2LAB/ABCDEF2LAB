"use client";

// 범위 확인 (live). 고른 점검의 엔드포인트 표(assets endpoint) + 선택한 경로의 API 상세(관찰한 사실 · 관련 기록 · 요청·응답 예시).
// 엔드포인트별 포함·제외 저장은 엔진에 없어 체크 상자를 끄고 한 번만 안내한다. 엔진은 점검을 만들 때 이미 시작했으므로
// demo 의 '점검 시작' 대신 '점검 진행 보기'로 이동만 한다. 숨은 화면(route 아님)은 부르지 않는다.

import { Fragment, type ReactNode, useMemo, useState } from "react";

import { cx } from "../../lib/cx";
import { fetchEndpoints } from "../../live/api";
import {
  type EpRow,
  epRow,
  groupRows,
  KIND_LABEL,
  kindCounts,
  type LiveScopeFilter,
  matchesQuery,
  SCOPE_FILTERS,
} from "../../live/crawlScope";
import { useSelectedTask } from "../../live/feeds";
import { fmtCount, hostOf, relTime } from "../../live/map";
import { ErrorNote, EstimateTag, LoadingRow, ModeNote, TaskPicker, Unsupported, useNow } from "../../live/parts";
import { usePoll } from "../../live/usePoll";
import { useShell } from "../../shell";
import { Icon } from "../../ui/icons";
import { InspHead, InspOpenButton, Pth } from "../../ui/parts";
import s from "../../workbench.module.css";
import { ApiDetailLive } from "./ApiDetailLive";
import { KindTag, LiveHead, MissingTaskNote } from "./crawlScopeParts";

/** 체크 상자를 '알 수 없음'(가운데 줄)으로: 엔진이 포함 여부를 주지 않는다. */
const unknownBox = (el: HTMLInputElement | null) => {
  if (el) el.indeterminate = true;
};

export function ScopeLive() {
  const { route, openInsp, goTo } = useShell();
  const visible = route === "scope";
  const { taskId, task, missing } = useSelectedTask();
  const now = useNow();
  // 목록에서 점검을 확인한 뒤에만 부른다 (지워진 점검에 헛걸음하지 않게)
  const on = visible && !!task;
  const host = task ? hostOf(task) : null;

  // 진입 때 한 번 + 새로고침
  const eps = usePoll(() => fetchEndpoints(taskId as string), [taskId], { enabled: on });
  const [filter, setFilter] = useState<LiveScopeFilter>("all");
  const [query, setQuery] = useState("");
  const [sel, setSel] = useState<{ id: number; task: string } | null>(null);

  const rows = useMemo(() => (eps.data?.assets ?? []).map(epRow), [eps.data]);
  const ordered = useMemo(() => groupRows(rows).flatMap((x) => x.rows), [rows]);

  const acts = taskId ? (
    <>
      <InspOpenButton>상세</InspOpenButton>
      <button className={cx(s.btn, s.pri)} type="button" onClick={() => goTo("live")}>
        점검 진행 보기
        <Icon name="arrow" />
      </button>
    </>
  ) : null;

  if (!taskId || missing) {
    return (
      <>
        <LiveHead title="범위 확인" phase="scope" taskId={null} task={null} />
        <div className={s.vm}>
          {missing ? <MissingTaskNote /> : null}
          <TaskPicker />
        </div>
      </>
    );
  }

  const fn = (SCOPE_FILTERS.find((f) => f[0] === filter) ?? SCOPE_FILTERS[0])[2];
  const q = query.trim().toLowerCase();
  const groups = groupRows(rows.filter((r) => fn(r) && matchesQuery(r, q)));
  // 고른 줄: 고른 것이 사라졌으면 표의 첫 줄
  const picked: EpRow | null =
    (sel && sel.task === taskId ? rows.find((r) => r.id === sel.id) : undefined) ?? ordered[0] ?? null;
  const kc = kindCounts(rows);

  let table: ReactNode;
  if (eps.unsupported) table = <ModeNote what="엔드포인트" />;
  else if (!eps.data && eps.error) table = <ErrorNote error={eps.error} onRetry={eps.reload} />;
  else if (!eps.data) table = <LoadingRow />;
  else if (!rows.length) table = <p className={s.empty}>엔진이 아직 찾은 엔드포인트가 없습니다.</p>;
  else {
    table = (
      <div className={s.stbl}>
        {groups.length ? (
          groups.map(({ g, rows: gr }) => (
            <Fragment key={g}>
              <div className={s.sgh}>
                {g}
                <span className={s.c}>{gr.length}</span>
                <span className={s.ai}>경로 첫 조각 묶음</span>
              </div>
              {gr.map((r) => {
                const cur = picked?.id === r.id;
                const seen = now ? relTime(r.ep.last_seen, now) : "";
                const d = [`파라미터 ${r.ep.params.length}`, seen ? `마지막 관찰 ${seen}` : ""]
                  .filter(Boolean)
                  .join(" · ");
                return (
                  <div key={r.id} className={cx(s.srow, cur && s.sel)}>
                    <span className={s.ck}>
                      <input
                        type="checkbox"
                        ref={unknownBox}
                        aria-label={`${r.m || "?"} ${r.p} 점검에 포함 (엔진 미지원)`}
                        disabled
                      />
                    </span>
                    <button
                      className={s.sb}
                      type="button"
                      data-lsel={r.id}
                      aria-current={cur ? "true" : undefined}
                      onClick={() => {
                        setSel({ id: r.id, task: taskId });
                        openInsp();
                      }}
                    >
                      <span className={cx(s.m, s[r.m])}>{r.m || "?"}</span>
                      <span className={s.p}>
                        <Pth p={r.p} />
                      </span>
                      <span className={s.d}>{d}</span>
                      <KindTag k={r.k} />
                    </button>
                  </div>
                );
              })}
            </Fragment>
          ))
        ) : (
          <p className={s.empty} style={{ padding: 14 }}>
            조건에 맞는 경로가 없습니다.
          </p>
        )}
      </div>
    );
  }

  return (
    <>
      <LiveHead title="범위 확인" phase="scope" taskId={taskId} task={task}>
        {acts}
      </LiveHead>
      <div className={s.vb}>
        <div className={s.vm}>
          <p className={s.lead}>
            이 점검에서 엔진이 찾은 엔드포인트입니다. 메서드·경로·파라미터·마지막 관찰은 <b>관찰한 사실</b>이고, 묶음은
            경로 첫 조각, 종류는 화면이 추정한 값입니다.
          </p>
          {eps.data ? (
            <div className={s.scount}>
              <span>
                엔드포인트<b>{fmtCount(eps.data.total)}</b>
              </span>
              <span>
                {KIND_LABEL.get}
                <b>{kc.get}</b>
              </span>
              <span className={s.ap}>
                {KIND_LABEL.ap}
                <b>{kc.ap}</b>
              </span>
              <span>
                {KIND_LABEL.login}
                <b>{kc.login}</b>
              </span>
              <span>
                {KIND_LABEL.ex}
                <b>{kc.ex}</b>
              </span>
              <EstimateTag title="종류는 메서드·경로로 화면이 추정했습니다" />
            </div>
          ) : null}
          {eps.data?.truncated ? (
            <p className={s.help}>
              {`서버 전체 ${fmtCount(eps.data.total)}개 중 ${fmtCount(eps.data.assets.length)}개만 받았습니다. 종류별 수와 표는 받은 것 기준입니다.`}
            </p>
          ) : null}
          <Unsupported what="엔드포인트별 포함·제외 저장" />
          {eps.data && rows.length ? (
            <div className={s.filters}>
              {/* biome-ignore lint/a11y/useSemanticElements: demo 와 같은 구조 (버튼 묶음 div[role=group]) */}
              <div className={s.filters} role="group" aria-label="보기 필터">
                {SCOPE_FILTERS.map(([k, label, f]) => (
                  <button
                    key={k}
                    className={s.fchip}
                    type="button"
                    aria-pressed={filter === k}
                    onClick={() => setFilter(k)}
                  >
                    {label}
                    <span className={s.c}>{rows.filter(f).length}</span>
                  </button>
                ))}
              </div>
              <input
                className={cx(s.inp, s.search)}
                type="search"
                placeholder="경로 검색"
                aria-label="경로 검색"
                value={query}
                onChange={(ev) => setQuery(ev.target.value)}
              />
            </div>
          ) : null}
          {table}
          <div className={s.note} role="note">
            <Icon name="info" />
            <span>
              엔진은 점검을 만들 때 바로 시작했습니다. 범위를 고른 뒤 시작하는 단계가 없어 &lsquo;점검 진행
              보기&rsquo;로 진행 상황만 봅니다.
            </span>
          </div>
          <div>
            <button className={cx(s.btn, s.sm)} type="button" onClick={eps.reload} disabled={eps.loading}>
              <Icon name="refresh" />
              새로고침
            </button>
          </div>
        </div>
        <aside className={s.vi} aria-label="선택한 경로 상세">
          {picked ? (
            <ApiDetailLive key={picked.id} taskId={taskId} row={picked} host={host} px="ls" visible={visible} />
          ) : (
            <>
              <InspHead title="상세" />
              <p className={s.empty}>엔드포인트를 고르면 관찰한 사실과 기록된 요청·응답을 봅니다.</p>
            </>
          )}
        </aside>
      </div>
    </>
  );
}
