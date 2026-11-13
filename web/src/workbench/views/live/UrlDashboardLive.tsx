"use client";

// 대시보드 (시안 짜임: 요약 4칸 · 발견 · 검토 대상 · 진행 중인 점검 · 최근 끝난 점검 + 오른쪽 상세).
// 서버에 진단 목록 · 집계 API 가 없어 이 브라우저가 이어 보는 진단 하나만 실제 값으로 보인다.
// 발견 목록은 그 진단의 저장된 결과(보고서와 같은 원장)에서 정상 대조를 뺀 것만 보인다. 확정만 취약점이므로 제목은
// 중립 이름("발견 · 검토 대상")으로 두고 건수 옆에 확정 수를 따로 보인다(검토 필요 · 미실행 · 결과 불명은 취약점이
// 아님, 절대 규칙 5). 결과가 저장되기 전에는 비워 두고 "없음"이라고 말하지 않는다(절대 규칙 5).
// 결정 대기(요청 승인 · 경로 선택)는 시안처럼 요약 칸과 점검 상세에서 보이고, 승인 요청은 상세에서 연다.

import { type ReactNode, useRef, useState } from "react";

import { diagnosticOutcome } from "@/lib/diagnosis/diagnostic-results";
import type { RemediationEntry } from "@/lib/diagnosis/remediation";
import type { RequestApproval } from "@/lib/diagnosis/request-approvals";

import { SEVL, type SevKey } from "../../data/findings";
import { cx } from "../../lib/cx";
import { fetchRemediation } from "../../live/api";
import { fmtTokens, sevKey } from "../../live/map";
import { maskSecrets } from "../../live/mask";
import { ErrorNote, LoadingRow, NotYet } from "../../live/parts";
import { refreshWorkflow, runUrlAction, useUrl, useUrlGates } from "../../live/urlDiagnosis";
import { URL_ENDED_STATES, urlHost, urlStateText } from "../../live/urlMap";
import { usePoll } from "../../live/usePoll";
import { useShell } from "../../shell";
import { Icon } from "../../ui/icons";
import { InspHead, InspOpenButton, Isec, PaneToggle } from "../../ui/parts";
import s from "../../workbench.module.css";
import { ApprovalActions, ApprovalFacts, ApprovalRequestCode, useRequestApprovals } from "./approvals";
import { JobRow, JobSummary, jobResult, pendingRows, useCoveragePct } from "./jobSummary";
import { EntryEp, FindingDetail, ORDER, Outcome } from "./UrlLiveLive";
import { UrlHead } from "./urlParts";
import { type Chip, Chips } from "./urlRecords";

/** 쓰기 메서드 (승인 요청 딱지). 메서드 기준 이름일 뿐, GET · HEAD · query 도 상태를 바꿀 수 있다. */
const WRITE = new Set(["POST", "PUT", "PATCH", "DELETE"]);

type Sel = { kind: "job" } | { kind: "approval"; id: string } | { kind: "finding"; id: string };

/** 대시보드는 칸마다 최대 3개만 보인다 (시안 v55). 전체는 '전체 보기' → 진행 중인 점검 · 끝난 점검 화면. */
export const DASH_MAX = 3;
/** 발견 목록은 처음에 위험한 순으로 6개만, 나머지는 '모두 보기' (시안 v88). */
export const FIND_MAX = 6;
/** 새로고침 아이콘이 최소한 도는 시간(ms). 조회가 빨리 끝나도 눌렀다는 걸 알아보게 한다. */
const REFRESH_MIN_MS = 600;

/** 위험도 칸. 서버가 심각도를 안 줬거나 모르는 값이면 none(등급 없음). */
type SevF = SevKey | "none";
const SEV_RANK: readonly SevF[] = ["crit", "hi", "mid", "lo", "none"];
const FIND_CHIPS: ReadonlyArray<Chip<SevF>> = [
  ["all", "전체", true, "저장된 발견 모두 (정상 대조 결과 제외)"],
  ["crit", SEVL.crit, false, "심각도 심각"],
  ["hi", SEVL.hi, false, "심각도 높음"],
  ["mid", SEVL.mid, false, "심각도 중간"],
  ["lo", SEVL.lo, false, "심각도 낮음"],
  ["none", "등급 없음", false, "서버가 심각도를 주지 않은 발견"],
];
const sevOf = (e: RemediationEntry): SevF => sevKey(e.severity ?? "") ?? "none";
/** 위험도 막대 색 (시안 v77 요약 칸). 등급 없음은 옅은 선색. */
const SEV_BAR: Readonly<Record<SevF, string>> = {
  crit: "var(--crit)",
  hi: "var(--hi)",
  mid: "var(--mid)",
  lo: "var(--lo)",
  none: "var(--rule-2)",
};

/**
 * 대시보드 맨 위 요약 4칸 (시안 strip). 서버에 진단 목록 · 30일 집계가 없어 이 브라우저가 이어 보는 점검 하나의
 * 실제 값만 센다. 모르는 값은 "—". 시안의 토큰 30일 집계 · 점검당 평균은 서버 집계가 없어 칸만 두고
 * '연결 전' 딱지를 붙이며, 이 점검의 서버 기록(model_usage)은 보조 줄에 보인다.
 */
function DashStrip({
  found,
  ready,
  approvals,
  selecting,
  tokens,
}: {
  /** 정상 대조를 뺀 발견 (결과 저장 전에는 ready=false) */
  found: readonly RemediationEntry[];
  ready: boolean;
  /** 승인 대기 수 (모르면 null) */
  approvals: number | null;
  /** 점검할 경로 선택 대기 중인가 */
  selecting: boolean;
  /** 이 점검의 모델 토큰 (서버 기록이 없으면 null) */
  tokens: { input: number; output: number } | null;
}) {
  const bySev = (k: SevF) => found.filter((e) => sevOf(e) === k).length;
  const byOutcome = (o: string) => found.filter((e) => diagnosticOutcome(e) === o).length;
  const sevSub = SEV_RANK.filter((k) => k !== "none" || bySev(k) > 0)
    .map((k) => `${k === "none" ? "등급 없음" : SEVL[k]} ${bySev(k)}`)
    .join(" · ");
  const decide = approvals === null ? null : approvals + (selecting ? 1 : 0);
  // 시안 결정 대기 보조 글: 남은 것만 ('승인 1 · 경로 선택 1'), 없으면 '모두 결정함'
  const decideSub = [approvals ? `승인 ${approvals}` : null, selecting ? "경로 선택 1" : null]
    .filter(Boolean)
    .join(" · ");
  const check = byOutcome("unknown") + byOutcome("not_executed");
  const tokText = (n: number) => {
    const f = fmtTokens(n);
    return `${f.n}${f.unit}`;
  };
  return (
    <section className={s.strip} aria-label="요약">
      {/* 빨간 숫자는 확정이 있을 때만 (검토 필요 · 미실행 · 결과 불명은 취약점이 아님 · 리뷰 #467) */}
      <div className={cx(s.cell, ready && byOutcome("confirmed") > 0 && s.crit)}>
        <div className={s.k}>발견 · 검토 대상</div>
        <div className={s.v} id="dSevN">
          {ready ? found.length : "—"}
        </div>
        {ready && found.length ? (
          <div className={s.stack} id="dSevStack" aria-hidden="true">
            {SEV_RANK.map((k) => (bySev(k) ? <i key={k} style={{ flex: bySev(k), background: SEV_BAR[k] }} /> : null))}
          </div>
        ) : null}
        <div className={s.sub} id="dSevSub">
          {ready ? sevSub : "결과가 저장되면 셉니다"}
        </div>
      </div>
      <div className={cx(s.cell, !!decide && s.warn)} id="dDecide">
        <div className={s.k}>결정 대기</div>
        <div className={s.v} id="dDecideN">
          {decide ?? "—"}
        </div>
        <div className={s.sub} id="dDecideSub">
          {decide === null ? "승인 대기를 확인하는 중" : decideSub || "모두 결정함"}
        </div>
      </div>
      <div className={s.cell}>
        <div className={s.k}>결과 확인</div>
        <div className={s.v}>{ready ? check : "—"}</div>
        <div className={s.sub}>
          {ready
            ? `결과 불명 ${byOutcome("unknown")} · 미실행 ${byOutcome("not_executed")}`
            : "판단 불가 · 미검사 영역"}
        </div>
      </div>
      <div className={s.cell}>
        <div className={s.k}>토큰 사용량 · 최근 30일</div>
        <div className={s.v}>
          {/* 시안 큰 수는 30일 집계라 서버 값이 없다: '—' 와 딱지만 두고, 이 점검의 서버 기록은 아래 줄에 */}—{" "}
          <NotYet why="서버에 30일 토큰 집계가 없습니다" />
        </div>
        <div className={s.sub}>
          {tokens ? `이 점검 · 입력 ${tokText(tokens.input)} · 출력 ${tokText(tokens.output)}` : "점검당 평균 —"}
        </div>
      </div>
    </section>
  );
}

/** 저장된 결과 → 발견 목록: 정상 대조를 빼고 위험도 → 판정 순. */
export function dashFindings(entries: readonly RemediationEntry[]): RemediationEntry[] {
  return entries
    .filter((e) => diagnosticOutcome(e) !== "negative")
    .sort(
      (x, y) =>
        SEV_RANK.indexOf(sevOf(x)) - SEV_RANK.indexOf(sevOf(y)) ||
        ORDER.indexOf(diagnosticOutcome(x)) - ORDER.indexOf(diagnosticOutcome(y)),
    );
}

/** live 대시보드 (URL 진단 서버). */
export function UrlDashboardLive() {
  const { route, openInsp } = useShell();
  const handle = useUrl((st) => st.handle);
  const job = useUrl((st) => st.job);
  const verified = useUrl((st) => st.verified);
  const flow = useUrl((st) => st.flow);
  const g = useUrlGates();
  const visible = route === "dashboard";
  const ended = !!job && URL_ENDED_STATES.includes(job.state);
  const taskId = job?.task_id ?? "";
  const approvalsOn = !!job?.task_id && job.input.workflow_mode === "user_approval_v1" && !ended;
  const a = useRequestApprovals(taskId, visible && approvalsOn);
  // 발견: 결과가 저장된 진단만 (보고서와 같은 원장 · 같은 조건)
  const reportTask = g.reportReadable && job?.task_id ? job.task_id : null;
  const rem = usePoll(() => fetchRemediation(reportTask as string, "internal"), [reportTask], {
    enabled: visible && !!reportTask,
  });
  const remFor = reportTask && rem.data && String(rem.data.task_id) === reportTask ? rem.data : null;
  const found = remFor ? dashFindings(remFor.entries) : [];
  const result = remFor && job ? jobResult(job, remFor.entries) : null;
  const [ff, setFf] = useState<SevF | "all">("all");
  const [findAll, setFindAll] = useState(false);
  const shown = ff === "all" ? found : found.filter((e) => sevOf(e) === ff);
  const [picked, setSel] = useState<Sel | null>(null);
  const [spin, setSpin] = useState(false);
  const vmRef = useRef<HTMLDivElement>(null);
  // 고르기 전에는 가장 위험한 발견(시안 처음 선택), 없으면 승인 대기 요청(보내기 전에 사람이 정해야 함), 없으면 점검 정보
  const first = a.pending[0];
  let sel: Sel = { kind: "job" };
  if (picked) sel = picked;
  else if (found[0]) sel = { kind: "finding", id: found[0].entry_id };
  else if (first) sel = { kind: "approval", id: first.id };

  const host = handle ? urlHost(handle.input.target_url) : null;
  // 보낼 요청 승인 대기 줄: 사용자 승인 모드가 아니면 없음([]), 아직 못 읽었으면 모름(null)
  const pending = pendingRows(approvalsOn, a);
  const coverage = useCoveragePct(reportTask, visible);

  const pick = (next: Sel) => {
    setSel(next);
    openInsp();
  };
  const refresh = () => {
    if (spin) return;
    setSpin(true);
    const work: Promise<unknown>[] = [refreshWorkflow(), new Promise((r) => setTimeout(r, REFRESH_MIN_MS))];
    if (g.canRefresh) work.push(runUrlAction("refresh"));
    a.poll.reload();
    rem.reload();
    void Promise.allSettled(work).then(() => {
      setSpin(false);
      // 새로 읽었음을 본문이 한 번 옅게 깜빡여 알린다 (시안 dflash, 움직임 줄이기 설정이면 CSS 가 끈다)
      const vm = vmRef.current;
      if (!vm) return;
      vm.classList.remove(s.dflash);
      void vm.offsetWidth;
      vm.classList.add(s.dflash);
    });
  };
  // 위험도 칩: 바꾸면 처음 6개부터. 보던 발견이 빠지면 남은 첫 줄을 고른다
  const pickSev = (k: SevF | "all") => {
    setFf(k);
    setFindAll(false);
    const rest = k === "all" ? found : found.filter((e) => sevOf(e) === k);
    if (sel.kind === "finding" && rest[0] && !rest.some((e) => e.entry_id === sel.id))
      setSel({ kind: "finding", id: rest[0].entry_id });
  };

  // 발견 목록 본문 (결과가 저장되기 전에는 "없음"이라고 하지 않는다)
  let findBody: ReactNode;
  if (!handle) findBody = <p className={s.empty}>이 브라우저에서 이어 보는 점검이 없습니다.</p>;
  else if (!reportTask)
    findBody = (
      <p className={s.empty}>
        {`점검이 끝나고 결과가 저장되면 여기에 나옵니다 · 지금 ${job ? urlStateText(job.state, verified) : "상태 확인 중"}`}
      </p>
    );
  else if (!remFor && rem.error) findBody = <ErrorNote error={rem.error} onRetry={rem.reload} />;
  else if (!remFor) findBody = <LoadingRow />;
  else if (!found.length)
    findBody = <p className={s.empty}>저장된 발견이 없습니다. 결과가 없다고 안전하다는 뜻은 아닙니다.</p>;
  else
    findBody = (
      <div className={s.list} id="dTodo">
        {(findAll ? shown : shown.slice(0, FIND_MAX)).map((e) => {
          const k = sevOf(e);
          const on = sel.kind === "finding" && sel.id === e.entry_id;
          return (
            <button
              key={e.entry_id}
              className={cx(s.frow, on && s.sel)}
              type="button"
              data-dsel={`fd-${e.entry_id}`}
              aria-current={on ? "true" : undefined}
              onClick={() => pick({ kind: "finding", id: e.entry_id })}
            >
              {k === "none" ? (
                <span className={s.sev}>등급 없음</span>
              ) : (
                <span className={cx(s.sev, s[k])}>{SEVL[k]}</span>
              )}
              <span className={s.fm}>
                <span className={s.ft}>{maskSecrets(e.title) || e.entry_id}</span>
                <span className={s.fe}>
                  <span className={s.host}>{host}</span>
                  <EntryEp e={e} />
                  {e.vuln_class ? <span>{maskSecrets(e.vuln_class)}</span> : null}
                </span>
              </span>
              <span className={s.fs}>
                <Outcome o={diagnosticOutcome(e)} short />
              </span>
            </button>
          );
        })}
      </div>
    );

  // 칸마다 줄 목록. 진단 목록 API 가 없어 지금은 이어 보는 진단 하나뿐이다(생기면 여기를 채운다)
  const jobRow = handle ? (
    <JobRow
      key="job"
      sel={sel.kind === "job"}
      onPick={() => pick({ kind: "job" })}
      pending={a.pending.length}
      result={result}
      coverage={coverage}
    />
  ) : null;
  const actRows = jobRow && !ended ? [jobRow] : [];
  const doneRows = jobRow && ended ? [jobRow] : [];

  // 오른쪽 상세
  const chosen: RequestApproval | undefined =
    sel.kind === "approval" ? a.pending.find((r) => r.id === sel.id) : undefined;
  const chosenF = sel.kind === "finding" ? found.find((e) => e.entry_id === sel.id) : undefined;
  let insp: ReactNode;
  if (chosenF && reportTask) {
    insp = (
      <>
        <FindingDetail e={chosenF} host={host} taskId={reportTask} onReload={rem.reload} />
        <Isec>
          <div className={s.acts}>
            <a className={cx(s.btn, s.pri)} href="#report">
              보고서 열기
              <Icon name="arrow" />
            </a>
          </div>
        </Isec>
      </>
    );
  } else if (chosen) {
    insp = (
      <>
        <InspHead
          eyebrow={
            <>
              승인 요청 · <span className={s.host}>{host}</span>
            </>
          }
          title={`${chosen.method} ${chosen.path}`}
          chips={
            <>
              <span className={cx(s.pill, s.wait)}>승인 대기</span>
              {WRITE.has(chosen.method) ? <span className={cx(s.tag, s.ap)}>쓰기 메서드</span> : null}
            </>
          }
        />
        <Isec t="보낼 요청">
          <ApprovalRequestCode row={chosen} />
        </Isec>
        <Isec t="승인 정보">
          <ApprovalFacts row={chosen} />
        </Isec>
        <Isec>
          <ApprovalActions a={a} row={chosen} />
        </Isec>
      </>
    );
  } else if (handle) {
    insp = (
      <JobSummary
        approvals={pending}
        onApprovals={first ? () => pick({ kind: "approval", id: first.id }) : undefined}
        result={result}
        coverage={coverage}
      />
    );
  } else {
    insp = (
      <>
        <InspHead title="점검 정보" />
        <Isec>
          <p className={s.empty}>이 브라우저에서 이어 보는 점검이 없습니다.</p>
          <a className={cx(s.btn, s.pri)} href="#new">
            <Icon name="plus" />새 점검
          </a>
        </Isec>
      </>
    );
  }

  return (
    <>
      <UrlHead
        title="대시보드"
        crumb="대시보드"
        acts={
          <>
            <button className={cx(s.btn, spin && s.spin)} type="button" id="dRefresh" onClick={refresh} disabled={spin}>
              <Icon name="refresh" />
              새로고침
            </button>
            <InspOpenButton>상세</InspOpenButton>
          </>
        }
      />
      <div className={s.vb}>
        <div className={s.vm} ref={vmRef}>
          <DashStrip
            found={found}
            ready={!!remFor}
            approvals={pending ? pending.length : null}
            selecting={!!job && flow?.stage === "awaiting_selection"}
            tokens={
              job?.model_usage?.usage_known
                ? { input: job.model_usage.input_tokens, output: job.model_usage.output_tokens }
                : null
            }
          />
          <section className={s.sec} aria-labelledby="dTodoH">
            <div className={s.sh}>
              <div className={s["sh-t"]}>
                <h2 id="dTodoH">발견 · 검토 대상</h2>
                {remFor ? (
                  <span className={s.n} id="dTodoN">
                    {`${shown.length}건 · 확정 ${shown.filter((e) => diagnosticOutcome(e) === "confirmed").length}`}
                  </span>
                ) : null}
              </div>
              {shown.length > FIND_MAX ? (
                <button
                  className={s.aside}
                  type="button"
                  id="dFindMore"
                  aria-expanded={findAll}
                  onClick={() => setFindAll((v) => !v)}
                >
                  {findAll ? "접기" : "모두 보기"}
                </button>
              ) : null}
            </div>
            {remFor ? (
              <Chips
                label="위험도로 거르기"
                chips={FIND_CHIPS}
                value={ff}
                count={(k) => (k === "all" ? found.length : found.filter((e) => sevOf(e) === k).length)}
                onPick={pickSev}
              />
            ) : null}
            {findBody}
          </section>
          <section className={s.sec} aria-labelledby="dActH">
            <div className={s.sh}>
              <div className={s["sh-t"]}>
                <h2 id="dActH">진행 중인 점검</h2>
                <span className={s.n} id="dActN">{`${actRows.length}건`}</span>
              </div>
              <a className={s.aside} href="#running">
                전체 보기
              </a>
            </div>
            {actRows.length ? (
              <div className={s.list} id="dActive">
                {actRows.slice(0, DASH_MAX)}
              </div>
            ) : (
              <p className={s.empty}>진행 중인 점검이 없습니다.</p>
            )}
          </section>
          <section className={s.sec} aria-labelledby="dDoneH">
            <div className={s.sh}>
              <div className={s["sh-t"]}>
                <h2 id="dDoneH">최근 끝난 점검</h2>
                <span className={s.n} id="dDoneN">{`${doneRows.length}건`}</span>
              </div>
              <a className={s.aside} href="#finished">
                전체 보기
              </a>
            </div>
            {doneRows.length ? (
              <div className={s.list} id="dDone">
                {doneRows.slice(0, DASH_MAX)}
              </div>
            ) : (
              <p className={s.empty}>끝난 점검이 없습니다.</p>
            )}
          </section>
        </div>
        <aside className={s.vi} id="dInsp" aria-label="선택한 항목 상세">
          {insp}
        </aside>
        <PaneToggle />
      </div>
    </>
  );
}
