"use client";

// E② 의 실제 요청별 승인(전송 전 확인). 대시보드의 '승인 요청' 상세와 점검 진행의 '확인 필요' 상자가 같이 쓴다.
// 도구 intercept 나 클라이언트 자동 승인으로 대신하지 않는다. 결정 POST 는 자동으로 다시 보내지 않고,
// 결과가 불명이면 상태 조회로 확인되기 전까지 다시 결정하지 않는다.

import { useCallback, useEffect, useRef, useState } from "react";

import { ApiError } from "@/lib/diagnosis/api";
import {
  decideRequestApproval,
  getRequestApproval,
  type RequestApproval,
  type RequestApprovalClosedReason,
} from "@/lib/diagnosis/request-approvals";

import { cx } from "../../lib/cx";
import { fetchRequestApprovals, type RequestApprovalState } from "../../live/api";
import { clockOf } from "../../live/map";
import { useUrl } from "../../live/urlDiagnosis";
import { type Poll, usePoll } from "../../live/usePoll";
import { Kv } from "../../ui/parts";
import s from "../../workbench.module.css";

export interface Approvals {
  poll: Poll<RequestApprovalState>;
  /** 승인 상태가 지금 진단 · 회차와 맞는가 (아니면 결정하지 않는다) */
  runMatches: boolean;
  rows: RequestApproval[];
  pending: RequestApproval[];
  /** 결정 중인 승인 id */
  busy: string | null;
  /** 결정 결과 불명 (조회로 확인되기 전까지 다시 결정하지 않는다) */
  unresolved: boolean;
  message: string;
  canDecide: (row: RequestApproval) => boolean;
  decide: (row: RequestApproval, decision: "allow" | "deny") => Promise<void>;
}

/** 실제 task 의 요청별 승인 상태와 결정. active 가 아니면 조회하지 않는다. */
export function useRequestApprovals(taskId: string, active: boolean): Approvals {
  const job = useUrl((st) => st.job);
  const flow = useUrl((st) => st.flow);
  const verified = useUrl((st) => st.verified);
  const uncertain = useUrl((st) => st.uncertain);
  // 미리보기의 비밀 값은 live/api 가 가린다
  const poll = usePoll(() => fetchRequestApprovals(taskId), [taskId], {
    enabled: active,
    intervalMs: active ? 2000 : 0,
  });
  const [busy, setBusy] = useState<string | null>(null);
  const [unconfirmed, setUnconfirmed] = useState<string | null>(null);
  const [message, setMessage] = useState("");
  const lock = useRef(false);
  const generation = useRef(0);
  const identity = useRef(taskId);
  useEffect(() => {
    identity.current = taskId;
    generation.current++;
    setBusy(null);
    setUnconfirmed(null);
    setMessage("");
    lock.current = false;
    return () => {
      generation.current++;
    };
  }, [taskId]);
  useEffect(() => {
    if (!active) {
      generation.current++;
      lock.current = false;
      setBusy(null);
    }
  }, [active]);

  const data = poll.data;
  const runMatches =
    !!data &&
    !poll.error &&
    job?.task_id === taskId &&
    ["preparing", "running"].includes(job.state) &&
    (!flow || (flow.task_id === taskId && data.run.run_ref === flow.run_ref));
  const rows = runMatches ? (data?.approvals ?? []) : [];
  const pending = rows.filter((a) => a.state === "pending");
  const observed = unconfirmed ? data?.approvals?.find((a) => a.id === unconfirmed) : null;
  const unresolved = !!unconfirmed && (!observed || observed.state === "pending");

  const gate = useCallback(
    (row: RequestApproval) => {
      const st = useUrl.getState();
      // 진행 목록에서 다시 연 점검(조회 결속)도 승인·거부할 수 있다(사용자 2026-11-22, 1차 리허설 F16). 결정 권한은
      // 서버가 실행 결속·주체로 다시 확인한다(#835). 새로고침·브라우저 재시작 뒤 운영자가 승인을 못 하면 승인 모드
      // 통제가 무너진다. 생성·시작 키는 여전히 물려받지 않는다(urlGates canStart).
      return (
        st.verified &&
        !st.uncertain &&
        st.job?.task_id === taskId &&
        ["preparing", "running"].includes(st.job.state) &&
        (!st.flow ||
          (st.flow.task_id === taskId &&
            st.flow.run_ref === row.run_ref &&
            !st.flow.paused &&
            !st.flowUncertain &&
            st.flowVerified))
      );
    },
    [taskId],
  );
  const canDecide = (row: RequestApproval) =>
    active && !busy && !unresolved && verified && !uncertain && row.can_decide && row.live && runMatches && gate(row);

  const decide = async (row: RequestApproval, decision: "allow" | "deny") => {
    if (!canDecide(row) || lock.current || flow?.paused) return;
    lock.current = true;
    const token = generation.current;
    setBusy(row.id);
    setMessage("");
    const mine = () => token === generation.current && identity.current === taskId;
    // 결정 전 확인: 여기서 멈추면 결정을 보내지 않은 것이다 — 결과 불명(다시 보내지 말라) 잠금을 걸지 않고 사유를 알린다
    // (11/24 화면 VAmPI 발견 2: 이미 닫힌 요청을 누르면 '결정 결과를 확인하지 못했습니다'만 보였다)
    let fresh: RequestApproval;
    try {
      fresh = await getRequestApproval(taskId, row.id);
    } catch {
      if (mine()) {
        setMessage(NOT_SENT_TEXT.unread);
        lock.current = false;
        setBusy(null);
      }
      return;
    }
    if (!mine() || !gate(row)) {
      if (mine()) {
        lock.current = false;
        setBusy(null);
      }
      return;
    }
    const notSent = precheck(row, fresh);
    if (notSent) {
      setMessage(notSent);
      lock.current = false;
      setBusy(null);
      poll.reload();
      return;
    }
    try {
      setUnconfirmed(row.id);
      await decideRequestApproval(taskId, fresh, decision);
      if (!mine()) return;
      setUnconfirmed(null);
      setMessage(
        decision === "allow"
          ? "이 요청 한 번만 허용했습니다. 실제 전송 결과는 진행 기록에서 따로 확인하세요."
          : "이 요청을 거부했습니다.",
      );
      poll.reload();
    } catch (error) {
      if (!mine()) return;
      if (error instanceof ApiError && error.status === 409) {
        // 409 는 저장 전에 거절한 것이다(db.ErrRequestApprovalConflict 조건부 저장) — 이번 결정은 반영되지 않았다
        setUnconfirmed(null);
        setMessage(NOT_SENT_TEXT.conflict);
        poll.reload();
        return;
      }
      setUnconfirmed(row.id);
      setMessage("결정 결과를 확인하지 못했습니다. 다시 보내지 말고 목록을 다시 조회하세요.");
    } finally {
      if (token === generation.current && identity.current === taskId) {
        lock.current = false;
        setBusy(null);
      }
    }
  };

  return { poll, runMatches, rows, pending, busy, unresolved, message, canDecide, decide };
}

/** 결정을 보내지 않았거나(결정 전 확인) 서버가 반영하지 않은(409) 경우의 안내. 결과 불명과 섞지 않는다. */
export const NOT_SENT_TEXT = {
  unread: "요청 상태를 확인하지 못해 결정을 보내지 않았습니다. 목록을 다시 조회한 뒤 다시 고르세요.",
  changed: "요청 내용이 바뀌어 결정을 보내지 않았습니다. 목록을 다시 조회한 뒤 다시 고르세요.",
  conflict:
    "서버가 이번 결정을 받지 않았습니다(이미 결정 · 만료 · 취소된 요청이거나 실행 상태가 바뀜). 목록을 다시 조회합니다.",
} as const;

/** 이미 끝난 요청의 안내(서버 상태 그대로). 결정을 보내지 않았다는 말을 붙인다. */
const CLOSED_TEXT: Record<RequestApproval["state"], string> = {
  pending: "더 이상 결정할 수 없는 요청입니다(실행이 끝났거나 닫혔습니다)",
  allowed: "이미 허용된 요청입니다",
  denied: "이미 거부된 요청입니다",
  expired: "승인 기한이 지나 닫힌 요청입니다",
  cancelled: "취소돼 닫힌 요청입니다",
};

/**
 * 결정 직전에 다시 읽은 요청을 본다. 보내도 되면 null, 아니면 화면 안내. 같은 요청(실행 · 시도 · 내용 해시)이 아니면 '바뀜',
 * 결정할 수 없는 상태면 '이미 끝남'으로 가른다(둘 다 결정은 보내지 않는다).
 */
export function precheck(row: RequestApproval, fresh: RequestApproval): string | null {
  if (
    fresh.run_ref !== row.run_ref ||
    fresh.attempt_id !== row.attempt_id ||
    fresh.request_sha256 !== row.request_sha256
  )
    return NOT_SENT_TEXT.changed;
  if (fresh.state !== "pending" || !fresh.live || !fresh.can_decide) {
    // 만료는 CLOSED_TEXT 가 이미 '승인 기한이 지나'라 겹쳐 적지 않는다
    const why =
      fresh.closed_reason && fresh.closed_reason !== "approval_expired"
        ? ` ${APPROVAL_CLOSED_REASON_TEXT[fresh.closed_reason]}`
        : "";
    return `${CLOSED_TEXT[fresh.state]}.${why} 결정을 보내지 않았습니다.`;
  }
  return null;
}

/** 서버가 붙이는 미리보기 안내 키. 값은 서버 문장 그대로 보여 준다. */
const PREVIEW_LIMIT_KEY = "표시 제한";

/**
 * 보낼 요청: 메서드 · 경로 · 대상(scheme 포함)과 서버가 준 미리보기 전부.
 * 서버(request_approvals.go approvalPreview)는 호스트가 공개로 지정한 `query.<키>` · `body.<키>` 와
 * `표시 제한` 안내만 보낸다. 원문 본문은 공개하지 않으며, 화면은 키를 바꾸거나 고르지 않고 그대로 보인다.
 */
export function ApprovalRequestCode({ row }: { row: RequestApproval }) {
  const entries = Object.entries(row.input_preview);
  const shown = entries.filter(([k]) => k !== PREVIEW_LIMIT_KEY);
  const limit = row.input_preview[PREVIEW_LIMIT_KEY];
  const text = [
    `${row.method} ${row.path}`,
    `대상: ${row.target_origin}`,
    ...(shown.length ? shown.map(([k, v]) => `${k}: ${v}`) : ["(공개로 지정된 query · body 입력 없음)"]),
  ].join("\n");
  return (
    <>
      <pre className={s.code}>{text}</pre>
      {limit ? (
        <p className={s.help} role="note">
          {`${PREVIEW_LIMIT_KEY}: ${limit}`}
        </p>
      ) : null}
    </>
  );
}

/** 승인 정보: 본문 크기 · 만료(일시정지로 연장 안 함) · 실제 attempt · 상태. 모두 서버 값. */
export function ApprovalFacts({ row }: { row: RequestApproval }) {
  return (
    <Kv
      rows={[
        ["본문 크기", `${row.body_bytes} bytes · 원문 비공개`],
        ["승인 만료", `${clockOf(row.expires_at) || row.expires_at} · 일시정지로 연장하지 않음`],
        ["실제 attempt", row.attempt_id],
        ["상태", `${APPROVAL_STATE_TEXT[row.state]} · ${row.execution_state}`],
        ...(row.closed_reason ? [["닫힌 까닭", APPROVAL_CLOSED_REASON_TEXT[row.closed_reason]] as const] : []),
      ]}
    />
  );
}

/** 승인 · 거부 버튼 줄 (+ 결정 안내). 화면은 결정만 기록하고, 실제 전송은 서버가 한다. */
export function ApprovalActions({ a, row, size }: { a: Approvals; row: RequestApproval; size?: "sm" }) {
  const off = !a.canDecide(row);
  return (
    <>
      <div className={s.acts}>
        <button
          className={cx(s.btn, s.pri, size && s.sm)}
          type="button"
          disabled={off}
          onClick={() => void a.decide(row, "allow")}
        >
          {a.busy === row.id ? "기록 중…" : "이 요청 승인"}
        </button>
        <button
          className={cx(s.btn, size && s.sm)}
          type="button"
          disabled={off}
          onClick={() => void a.decide(row, "deny")}
        >
          거부
        </button>
      </div>
      {a.message ? <p role="status">{a.message}</p> : null}
      {a.unresolved ? <p role="alert">결정 결과 불명 · 조회로 확인하기 전에는 다시 결정하지 않습니다.</p> : null}
    </>
  );
}

// 허용(초록) · 거부(빨강) · 만료 · 취소(점선 회색)를 한눈에 가른다(사용자 11/25: 거부와 승인을 구별)
const APPROVAL_TONE: Readonly<Record<RequestApproval["state"], string>> = {
  pending: "ap",
  allowed: "in",
  denied: "deny",
  expired: "ex",
  cancelled: "ex",
};

/**
 * 승인 요청 한 줄(메서드 · 경로 · 계획 제목 · 상태 · 받은 시각). 누르면 그 아래에 보낼 요청 미리보기 · 승인 정보가 펼쳐지고,
 * 대기 중인 요청만 펼친 칸에 승인 · 거부를 둔다 — 무엇을 보내는지 본 뒤에 정하게.
 */
function ApprovalRow({
  a,
  r,
  open,
  onToggle,
  planTitle,
}: {
  a: Approvals;
  r: RequestApproval;
  open: boolean;
  onToggle: () => void;
  planTitle: (intentId: RequestApproval["intent_id"]) => string | undefined;
}) {
  const panel = `apq-${r.id}`;
  return (
    <li className={cx(s["apq-item"], open && s.on)}>
      <button className={s["apq-row"]} type="button" aria-expanded={open} aria-controls={panel} onClick={onToggle}>
        <span className={cx(s.m, s[r.method])}>{r.method}</span>
        <span className={s.p}>{r.path}</span>
        <span className={s["apq-plan"]}>{planTitle(r.intent_id) ?? "계획 제목 없음"}</span>
        <span className={cx(s.tag, s[APPROVAL_TONE[r.state]])}>{approvalTagText(r)}</span>
        <span className={s["apq-at"]}>{clockOf(r.created_at)}</span>
      </button>
      {open ? (
        <div className={s["lv-aprev"]} id={panel}>
          <ApprovalRequestCode row={r} />
          <ApprovalFacts row={r} />
          {r.state === "pending" ? (
            <div className={s.acts}>
              <button
                className={cx(s.btn, s.pri, s.sm)}
                type="button"
                disabled={!a.canDecide(r)}
                onClick={() => void a.decide(r, "allow")}
              >
                {a.busy === r.id ? "보내는 중…" : "승인하고 보내기"}
              </button>
              <button
                className={cx(s.btn, s.sm)}
                type="button"
                disabled={!a.canDecide(r)}
                onClick={() => void a.decide(r, "deny")}
              >
                거부
              </button>
            </div>
          ) : null}
        </div>
      ) : null}
    </li>
  );
}

const createdAt = (r: RequestApproval) => Date.parse(r.created_at) || 0;
/** 승인 대기(먼저 온 것부터) */
export const pendingApprovals = (rows: readonly RequestApproval[]) =>
  rows.filter((r) => r.state === "pending").sort((x, y) => createdAt(x) - createdAt(y));
/** 처리된 요청(허용 · 거부 · 만료 · 취소, 최근 것부터) */
export const doneApprovals = (rows: readonly RequestApproval[]) =>
  rows.filter((r) => r.state !== "pending").sort((x, y) => createdAt(y) - createdAt(x));

/**
 * 점검 진행 가운데의 승인 요청 칸(사용자 11/24 목록, 11/25): 승인 대기 요청만 목록으로 둔다 — 처리된 요청이 계속 쌓여 대기
 * 요청이 묻혔다. 처리된 요청(허용 · 거부 · 만료 · 취소)은 머리의 [처리된 요청 n]을 누르면 오른쪽 패널에 목록으로 뜨고, 패널의
 * [← 진행 요약]으로 돌아온다(발견 목록에서 취약점을 누를 때와 같은 방식).
 */
export function ApprovalQueue({
  a,
  planTitle,
  doneShown,
  onShowDone,
}: {
  a: Approvals;
  planTitle: (intentId: RequestApproval["intent_id"]) => string | undefined;
  /** 오른쪽 패널에 처리된 요청 목록이 떠 있는가 */
  doneShown: boolean;
  onShowDone: () => void;
}) {
  // 대기 요청은 처음부터 펼쳐 미리보기 · 승인/거부가 바로 보인다(사용자 11/25). 여러 개 쌓여도 아래로 늘어놓지 않고 먼저 온
  // 하나만 그 자리에 보이며, 승인 · 거부하면 다음 요청이 같은 자리에 뜬다 — 남은 수는 머리 '대기 n'. 줄을 누르면 접힌다
  const [folded, setFolded] = useState<ReadonlySet<string>>(() => new Set());
  if (!a.rows.length) return null;
  const pending = pendingApprovals(a.rows);
  const current = pending.slice(0, 1);
  const doneN = a.rows.length - pending.length;
  return (
    <section className={cx(s.ldecide, !pending.length && s["ldecide-done"])} aria-labelledby="lDecideH">
      <div className={s.sh}>
        <h2 id="lDecideH">승인 요청</h2>
        <span className={s.n}>{`대기 ${pending.length}`}</span>
        {doneN ? (
          <button
            className={cx(s.btn, s.sm, s["apq-done"])}
            type="button"
            aria-pressed={doneShown}
            onClick={onShowDone}
          >
            {`처리된 요청 ${doneN}`}
          </button>
        ) : null}
      </div>
      {pending.length ? (
        <ul className={s.apq}>
          {current.map((r) => (
            <ApprovalRow
              key={r.id}
              a={a}
              r={r}
              open={!folded.has(r.id)}
              onToggle={() =>
                setFolded((prev) => {
                  const next = new Set(prev);
                  if (!next.delete(r.id)) next.add(r.id);
                  return next;
                })
              }
              planTitle={planTitle}
            />
          ))}
        </ul>
      ) : null}
    </section>
  );
}

/** 처리된 요청 거르기: 허용 · 거부 · 만료(만료 + 취소, 보내지 않고 닫힘) */
type DoneFilter = "allowed" | "denied" | "closed";
const DONE_FILTERS: readonly (readonly [DoneFilter, string])[] = [
  ["allowed", "허용"],
  ["denied", "거부"],
  ["closed", "만료"],
];
const doneGroup = (r: RequestApproval): DoneFilter =>
  r.state === "allowed" || r.state === "denied" ? r.state : "closed";

/**
 * 오른쪽 패널의 처리된 요청 목록(최근 것부터). 위 거르기(허용 · 거부 · 만료, 수와 함께 — 있는 것만)로 나눠 보고, 다시 누르면
 * 전체. 줄을 누르면 미리보기 · 승인 정보가 펼쳐진다(승인 · 거부 버튼은 없다).
 */
export function ApprovalHistory({
  a,
  planTitle,
}: {
  a: Approvals;
  planTitle: (intentId: RequestApproval["intent_id"]) => string | undefined;
}) {
  const [open, setOpen] = useState<string | null>(null);
  const [only, setOnly] = useState<DoneFilter | null>(null);
  const done = doneApprovals(a.rows);
  if (!done.length) return <p className={s.empty}>처리된 요청이 없습니다.</p>;
  const countOf = (k: DoneFilter) => done.filter((r) => doneGroup(r) === k).length;
  const chips = DONE_FILTERS.filter(([k]) => countOf(k) > 0);
  const shown = only ? done.filter((r) => doneGroup(r) === only) : done;
  return (
    <>
      {/* biome-ignore lint/a11y/useSemanticElements: 켜고 끄는 거르기 칸 묶음 (fieldset 은 폼 입력 묶음이라 맞지 않음) */}
      <div className={cx(s.seg, s["apq-fil"])} role="group" aria-label="결과로 거르기">
        {chips.map(([k, label]) => (
          <button key={k} type="button" aria-pressed={only === k} onClick={() => setOnly(only === k ? null : k)}>
            {`${label} ${countOf(k)}`}
          </button>
        ))}
      </div>
      <ApprovalHistoryList a={a} rows={shown} open={open} setOpen={setOpen} planTitle={planTitle} />
    </>
  );
}

function ApprovalHistoryList({
  a,
  rows,
  open,
  setOpen,
  planTitle,
}: {
  a: Approvals;
  rows: readonly RequestApproval[];
  open: string | null;
  setOpen: (id: string | null) => void;
  planTitle: (intentId: RequestApproval["intent_id"]) => string | undefined;
}) {
  return (
    <ul className={cx(s.apq, s["apq-side"])} aria-label="처리된 요청">
      {rows.map((r) => (
        <ApprovalRow
          key={r.id}
          a={a}
          r={r}
          open={open === r.id}
          onToggle={() => setOpen(open === r.id ? null : r.id)}
          planTitle={planTitle}
        />
      ))}
    </ul>
  );
}

/**
 * 결정 없이 닫힌 까닭(서버 closed_reason, #953). 11/25 승인 모드 회차에서 모델 단계 마감으로 닫힌 승인이 '새 요청이 앞 요청을
 * 취소한 것'처럼 보였다 — 그래서 다른 요청 때문이 아니라는 것까지 적는다. 서버 고정 코드 그대로이고 추정하지 않는다.
 */
export const APPROVAL_CLOSED_REASON_TEXT: Readonly<Record<RequestApprovalClosedReason, string>> = {
  model_stage_closed:
    "모델 단계가 끝나(확정 단계로 넘어감 등) 이 요청을 기다리던 호출이 끊겼습니다. 다른 요청이 취소한 것이 아닙니다.",
  request_timeout: "요청 · 호출 · 회차 기한에 닿아 닫혔습니다.",
  operator_stopped: "운영자가 점검을 멈춰 닫혔습니다.",
  run_closed: "실행이 닫히면서 함께 닫혔습니다.",
  approval_expired: "승인 기한이 지나 닫혔습니다.",
};

/** 줄 표시에 붙이는 짧은 까닭. 만료는 '만료'만으로 같은 뜻이라 붙이지 않는다. */
const APPROVAL_CLOSED_REASON_SHORT: Readonly<Record<RequestApprovalClosedReason, string>> = {
  model_stage_closed: "모델 단계 마감",
  request_timeout: "기한",
  operator_stopped: "운영자 종료",
  run_closed: "실행 닫힘",
  approval_expired: "",
};

/** 승인 줄의 상태 글: '취소 · 모델 단계 마감'처럼 까닭이 있으면 붙인다(이전 서버는 상태만). */
export function approvalTagText(r: RequestApproval): string {
  const short = r.closed_reason ? APPROVAL_CLOSED_REASON_SHORT[r.closed_reason] : "";
  return short ? `${APPROVAL_STATE_TEXT[r.state]} · ${short}` : APPROVAL_STATE_TEXT[r.state];
}

/** 결정 결과 글 (지난 요청 목록). */
export const APPROVAL_STATE_TEXT: Readonly<Record<RequestApproval["state"], string>> = {
  pending: "승인 대기",
  allowed: "허용",
  denied: "거부",
  expired: "만료",
  cancelled: "취소",
};
