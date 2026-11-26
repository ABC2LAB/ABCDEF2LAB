"use client";

// 취약점 상세의 '선택 검증' 칸(#950 이동찬, 계약 selected-candidate-llm-verification — 사용자 11/26 '검증기 계획대로').
// 검토 필요로 남은 비 IDOR 후보를 운영자가 골라: 모델 분석 → 업무 질문 답 → 새 요청 계획 → 운영자 승인 · 거부 → E② 로 보냄 → 모델 판정.
// 처리 상태와 판정은 다른 칸이고, 모델 판정은 프로그램 확정(E③)과 다른 딱지로 둔다 — 분석 준비 · 승인됨을 확정처럼 그리지 않는다.
// 보내기는 서버가 계획 판본 · 대상 승인 · E② 로 다시 검사한다(화면은 결정만 전한다).

import { type ReactNode, useEffect, useState } from "react";

import { ApiError } from "@/lib/diagnosis/api";

import { cx } from "../../lib/cx";
import {
  answerQuestion,
  CANCELABLE,
  cancelVerification,
  decidePlan,
  errorText,
  isOpen,
  makePlan,
  newRequestKey,
  type ProcessState,
  REASON_TEXT,
  readVerifications,
  requestVerification,
  type SourceRef,
  STATE_TEXT,
  VERDICT_TEXT,
  type Verification,
  type VerificationPage,
  WAITING_STATES,
} from "../../live/candidateVerification";
import { dateTimeOf } from "../../live/map";
import { candidateIdOf } from "../../live/productVerdicts";
import { usePoll } from "../../live/usePoll";
import { Kv } from "../../ui/parts";
import s from "../../workbench.module.css";

/** 끝나지 않은 검증을 다시 읽는 간격(ms) */
const ACTIVE_POLL_MS = 4000;
/** 성공 안내를 보이는 시간(ms) — 상태가 넘어간 뒤 지난 안내가 남지 않게. 오류는 다음 동작까지 남긴다 */
const NOTICE_MS = 6000;

/** 단계 줄: 요청 → 분석 → 계획 승인 → 보냄 → 판정 */
const STEPS = ["요청", "분석", "계획 승인", "보냄", "판정"] as const;
const STEP_OF: Readonly<Record<ProcessState, number>> = {
  requested: 0,
  checking_stored_evidence: 1,
  analysis_ready: 1,
  awaiting_approval: 2,
  rejected: 2,
  approved: 3,
  running: 3,
  evidence_ready: 3,
  assessing: 4,
  completed: 4,
  failed: -1,
  cancelled: -1,
  expired: -1,
};
const STATE_TONE: Partial<Record<ProcessState, string>> = {
  analysis_ready: "ap",
  awaiting_approval: "ap",
  failed: "deny",
  rejected: "out",
  cancelled: "out",
  expired: "out",
};
const reasonText = (code: string) => REASON_TEXT[code] ?? code;
const when = (iso: string | null | undefined) => (iso ? dateTimeOf(iso).slice(5, 16).replace("-", "/") : "—");

function Steps({ v }: { v: Verification }) {
  const at = STEP_OF[v.latest.process_state];
  // 저장 관찰만으로 판정한 길(stored_*)은 계획 승인 · 보냄을 건너뛴다
  const stored = v.latest.reason.startsWith("stored_");
  return (
    <ol className={s["cv-steps"]} aria-label="검증 단계">
      {STEPS.map((label, i) => {
        const skipped = stored && (i === 2 || i === 3);
        const done = at > i || (v.latest.process_state === "completed" && i === 4);
        return (
          <li
            key={label}
            className={cx(
              done && s.done,
              at === i && v.latest.process_state !== "completed" && s.cur,
              skipped && s.skip,
            )}
            aria-current={at === i ? "step" : undefined}
          >
            {skipped ? `${label}(건너뜀)` : label}
          </li>
        );
      })}
    </ol>
  );
}

function Sources({ refs }: { refs: SourceRef[] }) {
  if (!refs.length) return <>—</>;
  return (
    <ul className={s["cv-src"]}>
      {refs.map((r) => (
        <li key={`${r.call_ref}:${r.attempt_ref}`} title={r.observation_digest}>
          {`${r.call_ref} · ${r.attempt_ref}`}
        </li>
      ))}
    </ul>
  );
}

/** 완료된 결과: 모델 판정 딱지(프로그램 확정과 다름) + 판정 근거 */
function Result({ v }: { v: Verification }) {
  const f = v.final;
  const verdict = f?.verdict ?? v.latest.verdict;
  const code = f ? f.verdictCode : v.latest.verdictCode;
  return (
    <>
      <p className={s["cv-verdict-row"]}>
        <span className={cx(s["cv-mv"], verdict && s[`cv-${verdict}`])}>
          {verdict ? VERDICT_TEXT[verdict] : `모르는 판정(${code ?? "—"})`}
        </span>
        <span className={s.help}>모델 판정이에요 — 프로그램 확정(E③)과 따로 세요.</span>
      </p>
      {f ? (
        <Kv
          rows={[
            ["판단", f.claim || "—"],
            ["이유", f.reason || "—"],
            ["정상일 가능성", f.normal_explanation || "—"],
            ["한계", f.limitations || "—"],
            ["수정 권고", f.remediation || "—"],
            ["검증 기준", f.verification_criteria || "—"],
            ["근거 관찰", <Sources key="src" refs={f.sources} />],
          ]}
        />
      ) : (
        <Kv rows={[["근거 관찰", <Sources key="src" refs={v.latest.sources} />]]} />
      )}
    </>
  );
}

type Act = (run: () => Promise<unknown>, done?: string) => void;

/** 분석 준비: 모델 제안 · 업무 질문 답 · [실행 계획 만들기] */
function AnalysisStep({ v, act, busy, ids }: { v: Verification; act: Act; busy: boolean; ids: [string, string] }) {
  const a = v.analysis;
  const [drafts, setDrafts] = useState<Record<number, string>>({});
  if (!a) return <p className={s.help}>분석 기록을 아직 읽지 못했어요.</p>;
  const answered = new Map(v.answers.map((x) => [x.question_index, x]));
  return (
    <>
      <Kv
        rows={[
          ["모델 주장", a.claim || "—"],
          ["정상일 가능성", a.normal_explanation || "—"],
          ["한계", a.limitations || "—"],
        ]}
      />
      {a.actions.length ? (
        <>
          <h4 className={s["cv-h"]}>모델이 제안한 확인</h4>
          <ul className={s["cv-list"]}>
            {a.actions.map((x) => (
              <li key={`${x.method} ${x.path_hint} ${x.purpose}`}>
                <span className={s["cv-mono"]}>{`${x.method} ${x.path_hint}`}</span>
                {x.purpose ? ` — ${x.purpose}` : ""}
              </li>
            ))}
          </ul>
        </>
      ) : null}
      {a.business_questions.length ? (
        <>
          <h4 className={s["cv-h"]}>업무 질문</h4>
          <ol className={s["cv-qs"]}>
            {a.business_questions.map((q, i) => {
              const n = i + 1;
              const got = answered.get(n);
              return (
                <li key={q}>
                  <p>{q}</p>
                  {got ? (
                    <p className={s["cv-ans"]}>{got.answer}</p>
                  ) : (
                    <div className={s["cv-ansbox"]}>
                      <textarea
                        className={s.inp}
                        aria-label={`질문 ${n} 답`}
                        rows={2}
                        maxLength={2000}
                        value={drafts[n] ?? ""}
                        onChange={(e) => setDrafts((d) => ({ ...d, [n]: e.target.value }))}
                      />
                      <button
                        className={cx(s.btn, s.sm)}
                        type="button"
                        disabled={busy || !(drafts[n] ?? "").trim()}
                        onClick={() =>
                          act(
                            () => answerQuestion(ids[0], ids[1], v.id, a.digest, n, (drafts[n] ?? "").trim()),
                            "답을 저장했어요.",
                          )
                        }
                      >
                        답 저장
                      </button>
                    </div>
                  )}
                </li>
              );
            })}
          </ol>
          <p className={s.help}>답은 업무 진술로 남고, 관찰한 결과로 세지 않아요.</p>
        </>
      ) : null}
      <div className={s.acts}>
        <button
          className={cx(s.btn, s.pri)}
          type="button"
          disabled={busy}
          onClick={() =>
            act(
              () => makePlan(ids[0], ids[1], v.id, a.digest),
              "실행 계획을 만들었어요. 보낼 요청을 확인하고 승인해 주세요.",
            )
          }
        >
          실행 계획 만들기
        </button>
      </div>
    </>
  );
}

/** 계획 승인 대기: 보낼 요청 · 로그인 준비 · 상한 · 기한, [승인하고 보내기] [거부] */
function PlanStep({ v, act, busy, ids }: { v: Verification; act: Act; busy: boolean; ids: [string, string] }) {
  const p = v.plan;
  const digest = v.plan_digest;
  if (!p || !digest) return <p className={s.help}>계획을 아직 읽지 못했어요.</p>;
  return (
    <>
      <h4 className={s["cv-h"]}>보낼 요청</h4>
      <ul className={s["cv-list"]} aria-label="보낼 요청">
        {p.actions.map((x, i) => {
          const q = x.query.length ? `?${x.query.map(([k, val]) => `${k}=${val}`).join("&")}` : "";
          return (
            // biome-ignore lint/suspicious/noArrayIndexKey: 계획 행동은 순서가 곧 식별자(같은 요청이 두 번 올 수 있음)
            <li key={i}>
              <span className={s["cv-mono"]}>{`${x.method} ${x.exact_path}${q}`}</span>
              {x.kind === "normal_control" ? <span className={cx(s.tag, s.out)}>정상 대조</span> : null}
              {x.purpose ? <span className={s.help}>{` ${x.purpose}`}</span> : null}
              {x.headers.length ? (
                <span
                  className={cx(s.help, s["cv-mono"])}
                >{` ${x.headers.map(([k, val]) => `${k}: ${val}`).join(" · ")}`}</span>
              ) : null}
            </li>
          );
        })}
      </ul>
      <Kv
        rows={[
          ...(p.auth_preparation
            ? ([
                [
                  "로그인 준비",
                  <span key="auth" className={s["cv-mono"]}>
                    {`${p.auth_preparation.method} ${p.auth_preparation.exact_path}`}
                  </span>,
                ],
              ] as [string, ReactNode][])
            : []),
          ["계정", p.account ? "등록된 시험 계정으로" : "로그인 없이"],
          ["상한", `요청 ${p.max_requests}개 · ${p.duration_seconds}초`],
          ...(p.excluded_paths.length ? ([["제외 경로", p.excluded_paths.join(", ")]] as [string, ReactNode][]) : []),
          ["승인 기한", when(p.expires_at)],
        ]}
      />
      <div className={s.acts}>
        <button
          className={cx(s.btn, s.pri)}
          type="button"
          disabled={busy}
          onClick={() =>
            act(() => decidePlan(ids[0], ids[1], v.id, digest, "approved"), "승인했어요. 서버가 다시 검사한 뒤 보내요.")
          }
        >
          승인하고 보내기
        </button>
        <button
          className={s.btn}
          type="button"
          disabled={busy}
          onClick={() => act(() => decidePlan(ids[0], ids[1], v.id, digest, "rejected"), "계획을 거부했어요.")}
        >
          거부
        </button>
      </div>
    </>
  );
}

function Current({ v, act, busy, ids }: { v: Verification; act: Act; busy: boolean; ids: [string, string] }) {
  const st = v.latest.process_state;
  let body: ReactNode = null;
  if (st === "analysis_ready") body = <AnalysisStep v={v} act={act} busy={busy} ids={ids} />;
  else if (st === "awaiting_approval") body = <PlanStep v={v} act={act} busy={busy} ids={ids} />;
  else if (st === "completed") body = <Result v={v} />;
  return (
    <div className={s["cv-cur"]}>
      <Steps v={v} />
      <p className={s["cv-state"]}>
        <span className={cx(s.tag, s[STATE_TONE[st] ?? "in"])}>{STATE_TEXT[st]}</span>
        <span>{reasonText(v.latest.reason)}</span>
        <span className={s.help}>{when(v.latest.at ?? v.requested_at)}</span>
      </p>
      {body}
      {CANCELABLE.includes(st) ? (
        <div className={s.acts}>
          <button
            className={cx(s.btn, s.sm)}
            type="button"
            disabled={busy}
            onClick={() =>
              act(
                () => cancelVerification(ids[0], ids[1], v.id, v.plan_digest),
                "취소를 보냈어요. 이미 보내는 중이던 요청은 끝까지 갈 수 있어요.",
              )
            }
          >
            검증 취소
          </button>
        </div>
      ) : null}
    </div>
  );
}

/** 지난 검증(최근 것 빼고) — 접어 둔다 */
function History({ rows }: { rows: Verification[] }) {
  if (!rows.length) return null;
  return (
    <details className={s["cv-hist"]}>
      <summary>{`이전 검증 ${rows.length}건`}</summary>
      <ul className={s["cv-list"]}>
        {rows.map((v) => (
          <li key={v.id}>
            <span className={cx(s.tag, s[STATE_TONE[v.latest.process_state] ?? "in"])}>
              {STATE_TEXT[v.latest.process_state]}
            </span>
            {v.latest.verdict ? <span className={s["cv-mv"]}>{VERDICT_TEXT[v.latest.verdict]}</span> : null}
            <span className={s.help}>{` ${when(v.requested_at)} · ${reasonText(v.latest.reason)}`}</span>
          </li>
        ))}
      </ul>
    </details>
  );
}

/** 새 검증 요청을 둘 수 있는가: 선택 검증 대상 후보이고 끝나지 않은 요청이 없을 때 */
const canRequest = (p: VerificationPage) =>
  p.automatic.reason === "selected_verification_required" &&
  !p.verifications.some((v) => isOpen(v.latest.process_state));

/**
 * 선택 검증 칸. 보고서 후보('candidate-<id>')만, 선택 검증 대상이거나 검증 기록이 있을 때만 둔다. 404(제품 관리 중계가 아님 ·
 * 후보 없음)는 두지 않는다.
 */
export function SelectedVerificationCard({ taskId, entryId }: { taskId: string; entryId: string }) {
  const candidate = candidateIdOf(entryId);
  if (candidate === null) return null;
  // 다른 후보로 바뀌면 안내 · 요청 키 · 입력을 새로(key 로 다시 그림)
  return <VerificationCard key={`${taskId}:${candidate}`} taskId={taskId} candidate={candidate} />;
}

function VerificationCard({ taskId, candidate }: { taskId: string; candidate: string }) {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ text: string; err: boolean } | null>(null);
  const [requestKey, setRequestKey] = useState(newRequestKey);
  const poll = usePoll((signal) => readVerifications(taskId, candidate, null, signal), [taskId, candidate], {
    intervalMs: open ? ACTIVE_POLL_MS : 0,
  });
  const data = poll.data;
  const rows = [...(data?.verifications ?? [])].sort((x, y) => Number(y.id) - Number(x.id));
  // 운영자 입력을 기다리지 않는 진행 중 상태일 때만 몇 초마다 다시 읽는다
  const active = rows.some((v) => isOpen(v.latest.process_state) && !WAITING_STATES.includes(v.latest.process_state));
  useEffect(() => setOpen(active), [active]);
  useEffect(() => {
    if (!msg || msg.err) return;
    const t = window.setTimeout(() => setMsg(null), NOTICE_MS);
    return () => window.clearTimeout(t);
  }, [msg]);
  // 404 = 제품 관리 중계가 아님 · 후보 없음, 409 diagnosis_not_completed = 정상 종료가 아니거나 선택 검증 판이 아닌 회차
  // (조회에도 같은 검사) — 오류가 아니라 대상이 아니므로 칸을 두지 않는다
  const e = poll.error;
  if (e instanceof ApiError && (e.status === 404 || (e.status === 409 && e.message === "diagnosis_not_completed")))
    return null;
  const ids: [string, string] = [taskId, candidate];
  const act: Act = (run, done) => {
    if (busy) return;
    setBusy(true);
    setMsg(null);
    run()
      .then(() => setMsg(done ? { text: done, err: false } : null))
      .catch((e: unknown) => {
        // 저장 관찰만으로 판정할 수 있으면 계획 없이 진행 — 실패가 아니라 안내
        const info = e instanceof ApiError && e.message === "stored_evidence_assessment_pending";
        setMsg({ text: errorText(e), err: !info });
      })
      .finally(() => {
        setBusy(false);
        poll.reload();
      });
  };
  const title = <h3>선택 검증 · 모델</h3>;
  if (poll.error && !data)
    return (
      <section className={s.isec} aria-label="선택 검증">
        {title}
        <div className={cx(s.note, s.warn)} role="alert">
          <span
            className={s["lv-grow"]}
          >{`선택 검증 기록을 읽지 못했어요${poll.error instanceof ApiError ? ` (HTTP ${poll.error.status})` : ""}.`}</span>
          <button className={cx(s.btn, s.sm)} type="button" onClick={poll.reload}>
            다시 읽기
          </button>
        </div>
      </section>
    );
  if (!data) return null;
  const eligible = data.automatic.reason === "selected_verification_required";
  if (!eligible && !rows.length) return null;
  const [latest, ...older] = rows;
  return (
    <section className={s.isec} aria-label="선택 검증">
      {title}
      <p className={s.help}>
        {`저장 관찰 ${data.stored_evidence.observation_count}건 · ${reasonText(data.automatic.reason)}`}
      </p>
      {latest ? <Current key={latest.id} v={latest} act={act} busy={busy} ids={ids} /> : null}
      {canRequest(data) ? (
        <div className={s.acts}>
          <button
            className={cx(s.btn, latest ? undefined : s.pri)}
            type="button"
            disabled={busy}
            onClick={() =>
              act(async () => {
                await requestVerification(taskId, candidate, data.evidence_digest, requestKey);
                setRequestKey(newRequestKey());
              }, "검증을 요청했어요. 모델이 저장된 관찰부터 분석해요.")
            }
          >
            {latest ? "다시 검증 요청" : "이 후보 검증 요청"}
          </button>
          <span className={s.help}>새 요청이 필요하면 계획을 따로 승인해야 보내요.</span>
        </div>
      ) : null}
      {msg ? (
        <p className={msg.err ? s.err : s.help} role={msg.err ? "alert" : "status"}>
          {msg.text}
        </p>
      ) : null}
      <History rows={older} />
    </section>
  );
}
