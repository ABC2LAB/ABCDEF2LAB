"use client";

// 끝난 점검의 승인 기록(사용자 11/26: 점검이 끝나면 뭘 승인 · 거부했는지 못 봄). 계약 saved-request-approvals 대로 끝난 회차는
// 저장 이력 경로로만 읽고 결정 버튼을 두지 않는다. 저장 이력에는 경로가 없어, 보낸 요청은 요청 전체 기록(E② 저널)과 승인 ID(=시도
// ID)로 이어 경로 틀 · 응답 코드 · 전송 결과를 붙인다. 보내지 않은 요청(거부 · 만료)은 어디에도 경로가 남지 않아 '경로 기록 없음'.

import { useId, useState } from "react";

import { ApiError } from "@/lib/diagnosis/api";

import { cx } from "../../lib/cx";
import { type E2RequestRow, readE2Requests } from "../../live/e2Requests";
import { clockOf } from "../../live/map";
import { EXECUTION_TEXT, readSavedApprovals, type SavedApproval, type SavedApprovals } from "../../live/savedApprovals";
import { type Poll, usePoll } from "../../live/usePoll";
import { Icon } from "../../ui/icons";
import { CloseInspButton, Kv } from "../../ui/parts";
import s from "../../workbench.module.css";
import { RequestSendResult } from "./workflowViews";

/** 경로를 잇느라 읽는 요청 전체 기록 쪽 수 상한(100줄 × 이만큼). 넘으면 나머지 승인은 경로 없이 보인다 */
const MAX_PATH_PAGES = 10;

// 거르기 칸은 결과 그대로(사용자 11/26: '만료'를 누르면 만료만 — 취소는 따로)
type Group = SavedApproval["state"];
const GROUPS: readonly (readonly [Group, string])[] = [
  ["allowed", "허용"],
  ["denied", "거부"],
  ["expired", "만료"],
  ["cancelled", "취소"],
  ["pending", "결정 안 됨"],
];
const groupOf = (r: SavedApproval): Group => r.state;
/** 머리 줄에 0 이어도 보이는 칸(취소 · 결정 안 됨은 있을 때만) */
const ALWAYS_COUNTED: readonly Group[] = ["allowed", "denied", "expired"];
const STATE_TEXT: Readonly<Record<SavedApproval["state"], string>> = {
  allowed: "허용",
  denied: "거부",
  expired: "만료",
  cancelled: "취소",
  pending: "결정 안 됨",
};
const STATE_TONE: Readonly<Record<Group, string>> = {
  allowed: "in",
  denied: "deny",
  expired: "out",
  cancelled: "out",
  pending: "ap",
};
const at = (iso: string | undefined) => (iso ? clockOf(iso) || "—" : "—");

/** 승인 기록 하나의 상세: 결과 · 보냄 · 시각 · 승인 ID, 보낸 요청이면 전송 결과(경로 틀 · 응답 · 이유) */
function ApprovalBody({ r, sent, task }: { r: SavedApproval; sent: E2RequestRow | undefined; task: string }) {
  return (
    <>
      <Kv
        rows={[
          ["결과", STATE_TEXT[r.state]],
          ["보냄", EXECUTION_TEXT[r.execution_state]],
          ["요청 시각", at(r.created_at)],
          ["결정 시각", at(r.decided_at)],
          ["만료 시각", at(r.expires_at)],
          [
            "승인 ID",
            <span key="id" className={s.mono}>
              {r.id}
            </span>,
          ],
        ]}
      />
      {sent ? (
        <RequestSendResult task={task} callRef={sent.call_ref} attemptId={sent.attempt_id} />
      ) : (
        <p className={s.help}>
          {r.execution_state === "not_sent"
            ? "보내지 않은 요청이라 경로 · 응답 기록이 없어요(승인 이력에도 경로는 저장하지 않아요)."
            : "전송 기록에서 이 요청을 찾지 못해 경로를 잇지 못했어요."}
        </p>
      )}
    </>
  );
}

/** 끝난 점검의 승인 기록(한 번만 읽는다) */
export function useSavedApprovals(task: string | null, enabled: boolean): Poll<SavedApprovals> {
  return usePoll((signal) => readSavedApprovals(task as string, signal), [task], { enabled: enabled && !!task });
}

/** 보낸 요청의 경로: 요청 전체 기록을 쪽 단위로 읽어 시도 ID → 줄. 상한을 넘으면 partial */
function useSentPaths(task: string, need: boolean) {
  return usePoll(
    async (signal) => {
      const byAttempt = new Map<string, E2RequestRow>();
      let cursor: number | null = null;
      for (let i = 0; i < MAX_PATH_PAGES; i++) {
        const page = await readE2Requests(task, cursor, signal);
        for (const r of page.rows) byAttempt.set(r.attempt_id, r);
        cursor = page.next_cursor;
        if (cursor === null) return { byAttempt, partial: false };
      }
      return { byAttempt, partial: true };
    },
    [task],
    { enabled: need },
  );
}

function missText(error: unknown): string {
  if (error instanceof ApiError && error.status === 422) return "승인 기록이 1000건을 넘어 전부 보일 수 없어요.";
  return `승인 기록을 읽지 못했어요${error instanceof ApiError ? ` (HTTP ${error.status})` : ""}.`;
}

/**
 * 가운데 '승인 요청' 칸(끝난 점검): 머리에 허용 · 거부 · 만료 수와 [승인 기록 n]. 누르면 오른쪽 패널에 목록.
 * 기록이 없거나(빈 목록 · 404 — 승인 모드가 아니었던 점검) 아직 읽는 중이면 칸을 두지 않는다
 */
export function SavedApprovalSummary({
  poll,
  shown,
  onShow,
}: {
  poll: Poll<SavedApprovals>;
  shown: boolean;
  onShow: () => void;
}) {
  if (poll.error && !(poll.error instanceof ApiError && poll.error.status === 404))
    return (
      <section className={cx(s.ldecide, s["ldecide-done"])} aria-labelledby="lDecideH">
        <div className={s.sh}>
          <h2 id="lDecideH">승인 요청</h2>
        </div>
        <p className={s.err}>{missText(poll.error)}</p>
      </section>
    );
  const rows = poll.data?.approvals ?? [];
  if (!rows.length) return null;
  const count = (g: Group) => rows.filter((r) => groupOf(r) === g).length;
  return (
    <section className={cx(s.ldecide, s["ldecide-done"])} aria-labelledby="lDecideH">
      <div className={s.sh}>
        <h2 id="lDecideH">승인 요청</h2>
        <span className={s.n}>
          {GROUPS.filter(([g]) => ALWAYS_COUNTED.includes(g) || count(g) > 0)
            .map(([g, label]) => `${label} ${count(g)}`)
            .join(" · ")}
        </span>
        <button className={cx(s.btn, s.sm, s["apq-done"])} type="button" aria-pressed={shown} onClick={onShow}>
          {`승인 기록 ${rows.length}`}
        </button>
      </div>
    </section>
  );
}

/**
 * 오른쪽 패널의 승인 기록(최근 것부터). 결과로 거르기(있는 것만), 줄을 누르면 시각 · 보냄 상태, 보낸 요청이면 경로 틀과 전송 결과.
 * 결정 버튼은 없다(읽기 전용 이력)
 */
export function SavedApprovalHistory({
  rows,
  task,
  preview,
  wide = false,
  onPick,
  current = null,
}: {
  rows: readonly SavedApproval[];
  task: string;
  /** 앞 몇 줄만 보이고 [나머지 n개 더 보기]로 그 밑에 펼친다(고른 경로와 같은 방식, 사용자 11/25) */
  preview?: number;
  /** 넓은 자리(보고서 탭): 보냄 · 시각을 같은 줄 오른쪽에 */
  wide?: boolean;
  /** 고르기 모드(보고서): 줄을 누르면 펼치지 않고 오른쪽 패널로 보낸다(사용자 11/26) */
  onPick?: (row: SavedApproval, sent: E2RequestRow | undefined) => void;
  /** 고르기 모드에서 지금 오른쪽에 보이는 승인 ID */
  current?: string | null;
}) {
  const [open, setOpen] = useState<string | null>(null);
  const [only, setOnly] = useState<Group | null>(null);
  const [all, setAll] = useState(false);
  const listId = useId();
  // 보냈을 수 있는 요청이 있을 때만 요청 전체 기록을 읽는다
  const need = rows.some((r) => r.execution_state !== "not_sent");
  const paths = useSentPaths(task, need);
  if (!rows.length) return <p className={s.empty}>승인 기록이 없습니다.</p>;
  const sorted = [...rows].sort((x, y) => (Date.parse(y.created_at) || 0) - (Date.parse(x.created_at) || 0));
  const count = (g: Group) => rows.filter((r) => groupOf(r) === g).length;
  const chips = GROUPS.filter(([g]) => count(g) > 0);
  const filtered = only ? sorted.filter((r) => groupOf(r) === only) : sorted;
  const more = preview === undefined ? 0 : filtered.length - preview;
  const shown = all || more <= 0 ? filtered : filtered.slice(0, preview);
  const sentOf = (r: SavedApproval) => paths.data?.byAttempt.get(r.attempt_ref);
  return (
    <>
      {/* biome-ignore lint/a11y/useSemanticElements: 켜고 끄는 거르기 칸 묶음 (fieldset 은 폼 입력 묶음이라 맞지 않음) */}
      <div className={cx(s.seg, s["apq-fil"])} role="group" aria-label="결과로 거르기">
        {chips.map(([g, label]) => (
          <button key={g} type="button" aria-pressed={only === g} onClick={() => setOnly(only === g ? null : g)}>
            {`${label} ${count(g)}`}
          </button>
        ))}
      </div>
      {paths.data?.partial ? <p className={s.help}>전송 기록이 많아 앞 1000줄까지만 경로를 이었어요.</p> : null}
      {need && paths.error ? <p className={s.help}>전송 기록을 읽지 못해 경로를 잇지 못했어요.</p> : null}
      <ul className={cx(s.sap, wide && s["sap-wide"])} aria-label="승인 기록" id={listId}>
        {shown.map((r) => {
          const sent = sentOf(r);
          const isOpen = open === r.id;
          return (
            <li key={r.id}>
              <button
                className={s["sap-row"]}
                type="button"
                aria-expanded={onPick ? undefined : isOpen}
                aria-current={onPick && current === r.id ? "true" : undefined}
                onClick={() => (onPick ? onPick(r, sent) : setOpen(isOpen ? null : r.id))}
              >
                <span className={cx(s.tag, s[STATE_TONE[groupOf(r)]])}>{STATE_TEXT[r.state]}</span>
                <span className={cx(s.m, s[r.method])}>{r.method}</span>
                <span className={cx(s.p, !sent?.path && s["sap-nopath"])}>{sent?.path ?? "경로 기록 없음"}</span>
                <span
                  className={s["sap-sub"]}
                >{`${EXECUTION_TEXT[r.execution_state]} · 요청 ${at(r.created_at)}`}</span>
              </button>
              {isOpen ? (
                <div className={s["sap-open"]}>
                  <ApprovalBody r={r} sent={sent} task={task} />
                </div>
              ) : null}
            </li>
          );
        })}
      </ul>
      {more > 0 ? (
        <button
          className={s["sec-fold"]}
          type="button"
          aria-expanded={all}
          aria-controls={listId}
          onClick={() => setAll(!all)}
        >
          <Icon name="chev" />
          {all ? "접기" : `나머지 ${more}개 더 보기`}
        </button>
      ) : null}
    </>
  );
}

/** 보고서 [승인 기록] 탭을 둘지: 기록이 있거나, 없다고 단정할 수 없는 읽기 실패(404 · 모양 틀림 제외)일 때 */
export function showApprovalTab(poll: Poll<SavedApprovals>): boolean {
  if (poll.data?.approvals.length) return true;
  const e = poll.error;
  return e instanceof ApiError && e.status !== 404 && e.message !== "invalid_contract";
}

/**
 * 보고서 [승인 기록] 탭(사용자 11/26: 끝난 점검은 보고서로 가니 거기서 뭘 승인 · 거부했는지 보여야 함 — 취약점 목록 자리는 그대로
 * 두고 탭 하나로). 넓은 한 줄 목록(결과 · 메서드 · 경로 · 보냄/시각), 거르기, 줄을 누르면 그 밑으로 상세 · 전송 결과
 */
export function ReportApprovals({
  poll,
  task,
  current,
  onPick,
}: {
  poll: Poll<SavedApprovals>;
  task: string;
  current: string | null;
  onPick: (row: SavedApproval, sent: E2RequestRow | undefined) => void;
}) {
  const rows = poll.data?.approvals ?? [];
  if (poll.error && !rows.length) return <p className={s.err}>{missText(poll.error)}</p>;
  return <SavedApprovalHistory rows={rows} task={task} wide onPick={onPick} current={current} />;
}

/** 보고서 오른쪽 패널의 승인 기록 상세(가운데 [승인 기록] 탭에서 고른 줄). 고르기 전에는 고르라는 안내 */
export function ReportApprovalDetail({
  pick,
  task,
}: {
  pick: { row: SavedApproval; sent: E2RequestRow | undefined } | null;
  task: string;
}) {
  if (!pick)
    return (
      <>
        <div className={s["vi-head"]}>
          <div>
            <h2>승인 기록을 고르세요</h2>
          </div>
          <CloseInspButton label="승인 기록 상세" />
        </div>
        <p className={s.empty}>가운데 승인 기록을 누르면 여기에 상세가 나와요.</p>
      </>
    );
  const { row: r, sent } = pick;
  return (
    <section aria-label={`승인 기록 ${r.method} ${sent?.path ?? ""}`}>
      <div className={s["vi-head"]}>
        <div>
          <div className={s.eyebrow}>{`승인 기록 · 요청 ${at(r.created_at)}`}</div>
          <h2>
            <span className={cx(s.tag, s[STATE_TONE[groupOf(r)]])}>{STATE_TEXT[r.state]}</span>{" "}
            <span className={cx(s.m, s[r.method])}>{r.method}</span>{" "}
            <span className={cx(s.p, !sent?.path && s["sap-nopath"])}>{sent?.path ?? "경로 기록 없음"}</span>
          </h2>
        </div>
        <CloseInspButton label="승인 기록 상세" />
      </div>
      <div className={cx(s.isec, s["sap-detail"])}>
        <ApprovalBody r={r} sent={sent} task={task} />
      </div>
    </section>
  );
}
