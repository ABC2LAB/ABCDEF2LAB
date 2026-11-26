"use client";

// 보고서 [이행점검] 탭(사용자 11/26 — 이동찬 구상: 원래 점검 보고서에서 그 뒤에 같은 대상으로 돌린 점검들의 판정 결과를 본다).
// 계약 docs/contracts/remediation-followup.md. 서버가 같은 대상 · 원래보다 뒤 회차 · 근거를 다시 대조하므로 화면은 끝난 뒤 회차를
// 물어보고 서버가 받아 준 것만 보인다. 판정은 담당자가 오른쪽 패널에서 근거 관찰을 골라 저장한다(결과를 지어내지 않는다).

import { type ReactNode, useState } from "react";

import { ApiError } from "@/lib/diagnosis/api";
import { endedURLRun, type URLRunSummary } from "@/lib/diagnosis/diagnosis-runs";

import { cx } from "../../lib/cx";
import { dateTimeOf } from "../../live/map";
import {
  canReview,
  evidenceFor,
  FOLLOWUP_RESULTS,
  type FollowupItem,
  type FollowupPage,
  type FollowupResult,
  INCONCLUSIVE_REASONS,
  type InconclusiveReason,
  REASON_TEXT,
  RESULT_TEXT,
  readFollowup,
  reasonText,
  SCOPE_TEXT,
  saveFollowupReview,
} from "../../live/remediationFollowup";
import { useUrlRuns } from "../../live/urlRunList";
import { type Poll, usePoll } from "../../live/usePoll";
import { CloseInspButton, Isec, Kv } from "../../ui/parts";
import s from "../../workbench.module.css";

/** 물어볼 뒤 회차 수(최근 것부터). 목록엔 대상이 없어 서버에 묻고, 다른 대상은 서버가 보고서를 읽기 전에 409 로 돌려준다 */
const MAX_LATER_RUNS = 20;

const RESULT_TONE: Readonly<Record<FollowupResult, string>> = { fixed: "in", reproduced: "deny", inconclusive: "out" };
const when = (iso?: string) => (iso ? dateTimeOf(iso).slice(5, 16).replace("-", "/") : "");
const countOf = (p: FollowupPage, r: FollowupResult) => p.items.filter((i) => i.result === r).length;

type Later = { row: URLRunSummary; page: FollowupPage | null; error: unknown };

/** 원래 보고서의 조치 목록(뒤 회차 없이). 탭을 둘지 정한다 */
function useFollowupBase(task: string, enabled: boolean): Poll<FollowupPage> {
  return usePoll((signal) => readFollowup(task, undefined, undefined, signal), [task], { enabled });
}

/** [이행점검] 탭을 둘지: 저장된 조치가 있거나, 없다고 단정할 수 없는 읽기 실패(404 · 409 · 모양 틀림 제외) */
function showTab(base: Poll<FollowupPage>): boolean {
  if (base.data?.items.length) return true;
  const e = base.error;
  return e instanceof ApiError && ![404, 409].includes(e.status) && e.message !== "invalid_contract";
}

/** 같은 대상으로 뒤에 돌린 끝난 점검: 회차마다 물어 서버가 받아 준 것만(다른 대상 · 권한 없음 409 · 403 은 뺀다) */
function useLater(source: string, enabled: boolean): Poll<Later[]> & { rows: URLRunSummary[] } {
  const page = useUrlRuns((st) => st.page);
  const rows = (page?.runs ?? [])
    .filter((r) => endedURLRun(r) && r.report_available && r.task_id && Number(r.task_id) > Number(source))
    .sort((a, b) => Number(b.task_id) - Number(a.task_id))
    .slice(0, MAX_LATER_RUNS);
  const poll = usePoll(
    async (signal) => {
      const got = await Promise.all(
        rows.map(async (row): Promise<Later> => {
          try {
            const t = row.task_id as string;
            return { row, page: await readFollowup(source, t, t, signal), error: null };
          } catch (error) {
            return { row, page: null, error };
          }
        }),
      );
      return got.filter((x) => !(x.error instanceof ApiError && [403, 409].includes(x.error.status)));
    },
    [source, rows.map((r) => r.task_id).join(",")],
    { enabled: enabled && rows.length > 0 },
  );
  return { ...poll, rows };
}

/** 뒤 회차 하나의 요약: 수정 확인 · 다시 관찰됨 · 확인 불가 · 추가 발견 */
function summaryText(p: FollowupPage): string {
  const parts = FOLLOWUP_RESULTS.map((r) => `${RESULT_TEXT[r]} ${countOf(p, r)}`);
  if (p.general.results_state === "available") parts.push(`추가 발견 ${p.additional.length}`);
  return parts.join(" · ");
}

/** 이유 칸: '아직 판정하지 않음'은 '판정 전'과 같은 말이라 두지 않는다 */
const shownReason = (i: FollowupItem) => !!i.reason && !(i.reason === "no_linked_review" && !i.review);

/** 판정 근거로 잇지 않은 뒤 점검 발견(서버 additional). 0건이면 '추가 발견 0건'(계약) — 제목은 접어 둔다 */
function AdditionalFindings({ page }: { page: FollowupPage }) {
  if (!page.additional.length) return <p className={s.help}>추가 발견 0건</p>;
  const all = [...page.current.findings, ...page.general.findings];
  return (
    <details className={s["fu-more"]}>
      <summary>{`추가 발견 ${page.additional.length}건`}</summary>
      <p className={s.help}>판정 근거로 잇지 않은 뒤 점검 발견이에요.</p>
      <ul className={s["fu-list"]}>
        {page.additional.map((a) => (
          <li key={`${a.task_id}:${a.entry_id}`}>{all.find((f) => f.entry_id === a.entry_id)?.title ?? a.entry_id}</li>
        ))}
      </ul>
    </details>
  );
}

/** 조치 하나의 오른쪽 패널: 원래 조치와 판정 저장 */
function FollowupPanel({
  source,
  current,
  page,
  item,
  onSaved,
}: {
  source: string;
  current: string;
  page: FollowupPage;
  item: FollowupItem;
  onSaved: (p: FollowupPage) => void;
}) {
  const evidence = evidenceFor(item, page.current);
  // 담당자가 직접 고르게 미리 고르지 않는다 — 저장된 판정이 아직 유효하면 그 판정, 근거가 하나뿐이면 그 근거만 채운다
  const kept = item.review && item.reason !== "review_evidence_changed";
  const [result, setResult] = useState<FollowupResult | null>(kept ? item.result : null);
  const [pick, setPick] = useState<string>(() => {
    const saved = item.review?.step_id;
    if (saved && evidence.some((e) => e.obs.step_id === saved)) return saved;
    return evidence.length === 1 ? evidence[0].obs.step_id : "";
  });
  const [comparable, setComparable] = useState(false);
  const [criteria, setCriteria] = useState(false);
  const [reason, setReason] = useState<InconclusiveReason>(() =>
    kept && INCONCLUSIVE_REASONS.includes(item.reason as InconclusiveReason)
      ? (item.reason as InconclusiveReason)
      : "insufficient_evidence",
  );
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ text: string; err: boolean } | null>(null);
  const chosen = evidence.find((e) => e.obs.step_id === pick);
  const withEvidence = result === "fixed" || result === "reproduced";
  const ready =
    result !== null &&
    canReview(page.current_scope) &&
    !!item.original_digest &&
    (withEvidence ? !!chosen && comparable && (result !== "fixed" || criteria) : true);
  let saveLabel = item.review ? "판정 고쳐 저장" : "판정 저장";
  if (busy) saveLabel = "저장하는 중…";
  const save = () => {
    if (!ready || busy || !item.original_digest || result === null) return;
    setBusy(true);
    setMsg(null);
    saveFollowupReview(source, current, {
      entry_id: item.entry_id,
      record_id: item.record_id,
      original_digest: item.original_digest,
      ...(withEvidence && chosen
        ? {
            evidence_entry_id: chosen.finding.entry_id,
            step_id: chosen.obs.step_id,
            binding_digest: chosen.obs.binding_digest,
          }
        : {}),
      outcome: result,
      ...(withEvidence ? {} : { reason }),
      conditions_comparable: withEvidence && comparable,
      all_criteria_met: result === "fixed" && criteria,
      expected_revision: item.review ? item.review.revision : 0,
    })
      // 저장(PUT)은 계약상 current_task 만 받아 응답에 추가 발견(general)이 없다 — 같은 회차로 한 번 다시 읽어 바꿔 끼운다.
      // 다시 읽기가 실패해도 저장은 된 것이라 저장 응답으로 그린다
      .then((p) => readFollowup(source, current, current).catch(() => p))
      .then((p) => {
        onSaved(p);
        setMsg({ text: "판정을 저장했어요.", err: false });
      })
      .catch((e: unknown) => {
        const conflict = e instanceof ApiError && e.status === 409;
        setMsg({
          text: conflict
            ? "보고서 · 관찰 · 승인 상태가 바뀌어 저장하지 않았어요. 다시 읽은 뒤 판정해 주세요."
            : `판정을 저장하지 못했어요${e instanceof ApiError ? ` (HTTP ${e.status})` : ""}.`,
          err: true,
        });
      })
      .finally(() => setBusy(false));
  };
  return (
    <section aria-label={`이행점검 ${item.title}`}>
      <div className={s["vi-head"]}>
        <div>
          <div className={s.eyebrow}>{`이행점검 · 후속 #${current} 기준`}</div>
          <h2>{item.title}</h2>
        </div>
        <CloseInspButton label="이행점검 상세" />
      </div>
      <Isec t="지금 결과">
        <Kv
          rows={[
            [
              "결과",
              <span key="r" className={cx(s.tag, s[RESULT_TONE[item.result]])}>
                {RESULT_TEXT[item.result]}
              </span>,
            ],
            ...(shownReason(item) ? ([["이유", reasonText(item.reason as string)]] as [string, ReactNode][]) : []),
            [
              "마지막 판정",
              item.review
                ? `판본 ${item.review.revision} · ${item.review.reviewer} · ${when(item.review.reviewed_at)}`
                : "판정 전",
            ],
          ]}
        />
      </Isec>
      <Isec t="원래 조치">
        <Kv
          rows={[
            ["원래 관찰", item.original_observation || "—"],
            [
              "원래 요청",
              item.original_requests.length ? (
                <span key="req" className={s["fu-mono"]}>
                  {item.original_requests
                    .map((r) => `${r.method} ${r.path}${r.vuln_class ? ` · ${r.vuln_class}` : ""}`)
                    .join("\n")}
                </span>
              ) : (
                "비교할 원래 요청 없음"
              ),
            ],
          ]}
        />
        {item.criteria.length ? (
          <>
            <h4 className={s["fu-h"]}>검증 기준</h4>
            <ul className={s["fu-list"]}>
              {item.criteria.map((c) => (
                <li key={c}>{c}</li>
              ))}
            </ul>
          </>
        ) : null}
        {item.requested_changes.length ? (
          <>
            <h4 className={s["fu-h"]}>요청한 변경</h4>
            <ul className={s["fu-list"]}>
              {item.requested_changes.map((c) => (
                <li key={c}>{c}</li>
              ))}
            </ul>
          </>
        ) : null}
      </Isec>
      <Isec t="판정">
        {!canReview(page.current_scope) ? (
          <p
            className={s.err}
          >{`${SCOPE_TEXT[page.current_scope.state] ?? page.current_scope.state} — 지금은 판정을 저장할 수 없어요.`}</p>
        ) : null}
        {!item.original_digest ? <p className={s.err}>원래 조치 판본을 확인할 수 없어 판정할 수 없어요.</p> : null}
        <fieldset className={s["fu-set"]}>
          <legend>근거 관찰(후속 점검에서 같은 요청)</legend>
          {evidence.length ? (
            evidence.map(({ finding, obs }) => (
              <label key={obs.step_id} className={s["fu-opt"]}>
                <input type="radio" name="fuEv" checked={pick === obs.step_id} onChange={() => setPick(obs.step_id)} />
                <span>
                  <span
                    className={s["fu-mono"]}
                  >{`#${obs.number} ${obs.method} ${obs.path} · ${obs.http_status}`}</span>
                  <span className={s.help}>{` ${finding.title}`}</span>
                </span>
              </label>
            ))
          ) : (
            <p className={s.help}>판정에 쓸 수 있는 같은 요청 관찰이 없어 '확인 불가'로만 판정할 수 있어요.</p>
          )}
        </fieldset>
        <fieldset className={s["fu-set"]}>
          <legend>결과</legend>
          {FOLLOWUP_RESULTS.map((r) => (
            <label key={r} className={s["fu-opt"]}>
              <input
                type="radio"
                name="fuRes"
                checked={result === r}
                disabled={r !== "inconclusive" && !evidence.length}
                onChange={() => setResult(r)}
              />
              <span>{RESULT_TEXT[r]}</span>
            </label>
          ))}
        </fieldset>
        {withEvidence ? (
          <div className={s["fu-set"]}>
            <label className={s["fu-opt"]}>
              <input type="checkbox" checked={comparable} onChange={(e) => setComparable(e.target.checked)} />
              <span>계정 · 입력 등 조건이 원래 관찰과 같아요</span>
            </label>
            {result === "fixed" ? (
              <label className={s["fu-opt"]}>
                <input type="checkbox" checked={criteria} onChange={(e) => setCriteria(e.target.checked)} />
                <span>검증 기준을 모두 만족해요</span>
              </label>
            ) : null}
          </div>
        ) : (
          <label className={s["fu-set"]}>
            <span className={s.help}>확인 불가 이유</span>
            <select className={s.inp} value={reason} onChange={(e) => setReason(e.target.value as InconclusiveReason)}>
              {INCONCLUSIVE_REASONS.map((r) => (
                <option key={r} value={r}>
                  {REASON_TEXT[r]}
                </option>
              ))}
            </select>
          </label>
        )}
        <div className={s.acts}>
          <button className={cx(s.btn, s.pri)} type="button" disabled={!ready || busy} onClick={save}>
            {saveLabel}
          </button>
        </div>
        {msg ? (
          <p className={msg.err ? s.err : s.help} role={msg.err ? "alert" : "status"}>
            {msg.text}
          </p>
        ) : null}
      </Isec>
    </section>
  );
}

/**
 * 보고서 [이행점검] 탭의 상태 · 화면 묶음. 보고서 화면은 showTab 으로 탭을 두고, body 를 탭 안에, panel 을 오른쪽 패널에 둔다.
 * 고른 뒤 회차 · 조치는 이 원래 작업에서만 쓴다.
 */
export function useReportFollowup(source: string, visible: boolean, active: boolean, openInsp: () => void) {
  const base = useFollowupBase(source, visible);
  const tab = showTab(base);
  const later = useLater(source, visible && tab && active);
  const [sel, setSel] = useState<{ source: string; run: string | null; item: string | null }>({
    source,
    run: null,
    item: null,
  });
  const [saved, setSaved] = useState<Record<string, FollowupPage>>({});
  const mine = sel.source === source ? sel : { source, run: null, item: null };
  const runs = (later.data ?? []).map((x) =>
    x.page && saved[x.row.task_id as string] ? { ...x, page: saved[x.row.task_id as string] } : x,
  );
  const usable = runs.filter((x) => x.page);
  const chosen = usable.find((x) => x.row.task_id === mine.run) ?? usable[0] ?? null;
  const page = chosen?.page ?? null;
  const item = page && mine.item ? (page.items.find((i) => i.entry_id === mine.item) ?? null) : null;

  let body: ReactNode;
  if (base.error && !base.data)
    body = (
      <p
        className={s.err}
      >{`이행점검 정보를 읽지 못했어요${base.error instanceof ApiError ? ` (HTTP ${base.error.status})` : ""}.`}</p>
    );
  else if (!base.data || (later.rows.length > 0 && !later.data))
    body = <p className={s.empty}>이행점검 정보를 읽는 중</p>;
  else {
    const scope = base.data.current_scope;
    body = (
      <div className={s["fu-wrap"]}>
        <p className={s.help}>
          {`조치 ${base.data.items.length}개${scope.state === "review_required" ? "" : ` · ${SCOPE_TEXT[scope.state] ?? scope.state}`}`}
        </p>
        <section aria-label="뒤에 돌린 점검">
          <h3 className={s["fu-h"]}>뒤에 돌린 점검</h3>
          {runs.length ? (
            <ul className={s["fu-runs"]}>
              {runs.map(({ row, page: p, error }) => (
                <li key={row.id}>
                  <button
                    type="button"
                    className={s["fu-run"]}
                    aria-current={chosen?.row.id === row.id ? "true" : undefined}
                    disabled={!p}
                    onClick={() => setSel({ source, run: row.task_id ?? null, item: null })}
                  >
                    <b>{`#${row.task_id}`}</b>
                    <span className={s.help}>{when(row.finished_at ?? row.started_at)}</span>
                    <span>
                      {p
                        ? summaryText(p)
                        : `읽지 못했어요${error instanceof ApiError ? ` (HTTP ${error.status})` : ""}`}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          ) : (
            <p className={s.empty}>
              이 대상으로 뒤에 돌린 점검이 없어요. 끝난 점검의 [이 주소로 다시 점검]으로 다시 점검하면 여기에 나와요.
            </p>
          )}
        </section>
        {page && chosen ? (
          <section aria-label={`후속 #${chosen.row.task_id} 조치별 결과`}>
            <h3 className={s["fu-h"]}>{`후속 #${chosen.row.task_id} 조치별 결과`}</h3>
            <ul className={s["fu-items"]}>
              {page.items.map((i) => (
                <li key={i.entry_id}>
                  <button
                    type="button"
                    className={s["fu-item"]}
                    aria-current={item?.entry_id === i.entry_id ? "true" : undefined}
                    onClick={() => {
                      setSel({ source, run: chosen.row.task_id ?? null, item: i.entry_id });
                      openInsp();
                    }}
                  >
                    <span className={cx(s.tag, s[RESULT_TONE[i.result]])}>{RESULT_TEXT[i.result]}</span>
                    <span className={s["fu-title"]}>{i.title}</span>
                    <span className={s.help}>
                      {[
                        shownReason(i) ? reasonText(i.reason as string) : null,
                        i.review ? `판본 ${i.review.revision} · ${i.review.reviewer}` : "판정 전",
                      ]
                        .filter(Boolean)
                        .join(" · ")}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
            {page.general.results_state === "available" ? <AdditionalFindings page={page} /> : null}
          </section>
        ) : null}
      </div>
    );
  }

  const panel: ReactNode =
    page && item && chosen?.row.task_id ? (
      <FollowupPanel
        key={`${chosen.row.task_id}:${item.entry_id}:${item.review?.revision ?? 0}`}
        source={source}
        current={chosen.row.task_id}
        page={page}
        item={item}
        onSaved={(p) => setSaved((m) => ({ ...m, [chosen.row.task_id as string]: p }))}
      />
    ) : (
      <>
        <div className={s["vi-head"]}>
          <div>
            <h2>조치를 고르세요</h2>
          </div>
          <CloseInspButton label="이행점검 상세" />
        </div>
        <p className={s.empty}>가운데 조치를 누르면 여기서 원래 조치를 보고 판정해요.</p>
      </>
    );
  return {
    showTab: tab,
    count: base.data?.items.length ?? 0,
    body,
    panel,
    /** 오른쪽 패널을 맨 위로 올릴 열쇠(고른 회차 · 조치) */
    pickKey: `${chosen?.row.task_id ?? ""}:${item?.entry_id ?? ""}`,
  };
}
