"use client";

// 새 점검 (시안 짜임: 대상 · 분석 모델 · 작업 + 오른쪽 점검 요약 · 안전 정책 · 시작).
// 순서: 입력 → [승인 범위 확인](초안 + 서버 범위 확인, DNS·HTTP·모델 호출 없음) → 서버가 돌려준 범위·예산을 오른쪽에서 보고
// 동의 상자 → [점검 시작]. 시작 전에는 task · Planner · 대상 송신 · 단회 권위를 만들지 않는다(서버 계약).
// URL 은 요청일 뿐이고 허가는 서버 설치 범위와 E② 가 정한다.
// 입력은 두 쪽 묶음(시안 fgrps · fgrp)으로 나눈다: 1쪽 대상 → [다음] → 2쪽 분석 모델 · 점검 설정 → [승인 범위 확인].
// 쪽마다 아래에 쪽 이동 줄(stepnav)이 있고, 요약 패널이 안 보이면(좁은 화면 · 접음) 폼 아래 시작 줄(formbar)에 실행 버튼을 둔다.
// 시안의 로그인(계정 A 관리자 / B 일반 · 쿠키) · 최근 점검한 주소 · 점검 목표 · 점검 범위 · 워커 수 · LLM 서버 주소 · 연결 상태는
// 서버 진단 입력(URLDiagnosisInput)과 catalog 에 칸이 없어 그리지 않는다. 받아도 보낼 곳이 없는 값을 치게 하지 않는다.

import { type ReactNode, useEffect, useRef, useState } from "react";

import { type URLDiagnosisInput, urlDiagnosisInput, type WorkflowMode } from "@/lib/diagnosis/diagnosis-url";

import { cx } from "../../lib/cx";
import { disconnectUrl, runUrlAction, setUrlConfirmed, useUrl, useUrlGates } from "../../live/urlDiagnosis";
import { scanViewFor, URL_ENDED_STATES, urlHost, urlTargetHint, usdText } from "../../live/urlMap";
import { useShell } from "../../shell";
import { useWb } from "../../store";
import { Icon } from "../../ui/icons";
import { CloseInspButton, HelpTip, InspOpenButton, PaneToggle } from "../../ui/parts";
import s from "../../workbench.module.css";
import { ExternalModelSettings } from "./ExternalModelSettings";
import { TargetApprovalPanel } from "./TargetApprovalPanel";
import { reasonText, UrlHead, UrlNotices, UrlPill } from "./urlParts";

/** 입력 → 서버에 보낼 초안 입력 (팀 lib 검사를 통과해야만). */

/** 시작 전 범위의 승인 방식(서버 값) → 안전 정책 문구. */
const APPROVAL_MODE_RULE: Readonly<
  Record<"preauthorized_scope" | "confirm_every_request" | "workbench_selective_v1", string>
> = {
  preauthorized_scope: "사전 승인한 경로와 임의 query 로 진행 · 상태 변경은 자동 승인 안 함",
  confirm_every_request: "모든 요청을 한 건씩 승인한 것만 보냄",
  workbench_selective_v1: "쓰기 메서드 요청은 한 건씩 승인한 것만 보냄",
};
// 작업 이름(display_name)은 받지 않는다 (시안 v39): 서버에서 선택 값이고 주소와 겹친다. 비우면 서버가 기본 설명을 붙인다.
function draftInput(target: string, profile: string, mode?: WorkflowMode, excludes = ""): URLDiagnosisInput | null {
  if (!profile) return null;
  try {
    return urlDiagnosisInput(target, profile, "", mode, excludes);
  } catch {
    return null;
  }
}

const MODES = [
  ["user_approval_v1", "사용자 승인", "탐색 뒤 점검할 경로를 고르고, 쓰기 메서드 요청은 한 건씩 승인합니다"],
  ["ai_auto_v1", "AI 자동", "승인 범위 안에서 탐색과 점검을 이어 갑니다 · 데이터 변경은 자동 승인 안 함"],
] as const;

// 요약 '쓰기 요청' 줄의 짧은 말 (범위 확인 뒤 scope.approval_mode, 위 APPROVAL_MODE_RULE 과 같은 뜻)
const APPROVAL_MODE_SHORT: Readonly<Record<keyof typeof APPROVAL_MODE_RULE, string>> = {
  preauthorized_scope: "상태 변경은 자동 승인 안 함",
  confirm_every_request: "모든 요청 한 건씩 승인",
  workbench_selective_v1: "쓰기 요청 한 건씩 승인",
};
/** 안전 정책 한 줄 (시안 ilist). */
function Rule({ t, sub, warn = false, tip }: { t: ReactNode; sub?: string; warn?: boolean; tip?: ReactNode }) {
  return (
    <li>
      <Icon name={warn ? "alert" : "check"} />
      <div>
        {t}
        {tip}
        {sub !== undefined ? <small>{sub}</small> : null}
      </div>
    </li>
  );
}

/** 제외 경로 칸의 흐린 예시. 경로 단위라 query(?) 없이 — 서버 · 웹 입력 검사가 '?' 를 받지 않는다. */
export const EXCLUDE_EXAMPLE = "/logout\n/delete";

/** live 새 점검 (URL 진단). */
export function UrlNewScanLive() {
  const { goTo, route } = useShell();
  const cap = useUrl((st) => st.cap);
  const handle = useUrl((st) => st.handle);
  const job = useUrl((st) => st.job);
  const flowStage = useUrl((st) => st.flow?.stage ?? null);
  const verified = useUrl((st) => st.verified);
  const busy = useUrl((st) => st.busy);
  const confirmed = useUrl((st) => st.confirmed);
  const resetSeq = useUrl((st) => st.resetSeq);
  const g = useUrlGates();
  const prefill = useWb((st) => st.prefill);
  const profiles = cap.kind === "v2" ? cap.catalog.profiles : [];
  const locals = profiles.filter((p) => !p.model?.external_transfer);
  const externals = profiles.filter((p) => p.model?.external_transfer);

  const [target, setTarget] = useState("");
  const [kind, setKind] = useState<"local" | "external">(locals.length || !externals.length ? "local" : "external");
  const [profileId, setProfileId] = useState("");
  const [workflowMode, setWorkflowMode] = useState<WorkflowMode>("user_approval_v1");
  const [excluded, setExcluded] = useState("");
  const [credentialReady, setCredentialReady] = useState(false);
  // 주소 칸을 한 번이라도 고쳤는가: 처음 들어온 빈 칸에는 빨간 안내를 띄우지 않고, 지웠을 때만 띄운다 (시안 #nHost)
  const [touched, setTouched] = useState(false);
  // 지금 보이는 쪽 (1 대상 · 2 분석 모델 · 점검 설정). 새 점검에 다시 들어오면 1쪽부터 (시안 v64)
  const [page, setPage] = useState<1 | 2>(1);
  useEffect(() => {
    if (route === "new") setPage(1);
  }, [route]);
  const seenSeq = useRef(useWb.getState().prefill?.seq ?? 0);
  const seenReset = useRef(resetSeq);

  // '다른 진단 만들기' 뒤에는 폼을 비운다 (같은 입력으로 겹쳐 만들지 않게)
  useEffect(() => {
    if (resetSeq === seenReset.current) return;
    seenReset.current = resetSeq;
    setTarget("");
    setExcluded("");
    setTouched(false);
  }, [resetSeq]);

  // 다른 화면의 '이 주소로 다시 점검' (이어 보는 진단이 없을 때만)
  useEffect(() => {
    if (!prefill || prefill.seq === seenSeq.current) return;
    seenSeq.current = prefill.seq;
    if (!useUrl.getState().handle) setTarget(prefill.url);
  }, [prefill]);

  // 조회 중인 진단이 있으면 그 입력을 보인다 (잠금)
  const shown = handle?.input;
  const choices = kind === "external" ? externals : locals;
  const pickedId = choices.some((p) => p.id === profileId) ? profileId : (choices[0]?.id ?? "");
  const pv = shown ? shown.profile_id : pickedId;
  const profile = profiles.find((p) => p.id === pv) ?? null;
  const tv = shown ? shown.target_url : target;
  const mv = shown?.workflow_mode ?? workflowMode;
  const ev = shown ? (shown.excluded_paths ?? "") : excluded;
  const external = !!profile?.model?.external_transfer;
  const lockedKind = external ? "external" : "local";
  const shownKind = shown ? lockedKind : kind;
  const input = shown
    ? null
    : draftInput(target, pv, profile?.workflow ? workflowMode : undefined, profile?.workflow ? excluded : "");

  const hint = urlTargetHint(tv);
  // 막힌 이유: 1쪽(주소)과 2쪽(모델 · 키)을 나눠, 1쪽에서는 주소 이유만 보인다
  const why1: string[] = [];
  const why2: string[] = [];
  if (!shown) {
    if (!target.trim()) why1.push("주소를 입력하세요");
    else if (hint) why1.push(hint);
    else if (!input && profile) why1.push("주소 형식을 확인하세요");
    if (!profiles.length) why2.push("설치된 모델 프로필이 없습니다");
    else if (!profile) why2.push("모델을 고르세요");
    if (external && !credentialReady) why2.push("외부 API 키를 등록하세요");
  }
  const why = [...why1, ...why2];
  // 1쪽 → 2쪽: 주소만 보고 넘긴다 (모델은 2쪽에서 고름). 조회 중인 진단은 잠긴 입력이라 그냥 넘긴다
  const canNext = !!shown || (!!target.trim() && !hint);
  const turn = (to: 1 | 2) => {
    setPage(to);
    // 쪽을 바꾸면 스크롤은 맨 위, 초점은 그 쪽 첫 섹션 제목으로 (시안 setStep: 화면 읽기 프로그램이 새 쪽 시작을 읽게)
    requestAnimationFrame(() => {
      document.getElementById("luForm")?.scrollTo?.({ top: 0 });
      document.getElementById(to === 1 ? "luStep1H" : "luStep2H")?.focus();
    });
  };
  const next = () => {
    if (canNext) turn(2);
  };
  const canSend = !shown && !!input && g.canValidate && !busy && (!external || credentialReady);
  const send = async () => {
    if (canSend && input) await runUrlAction("validate", input);
  };
  const start = async () => {
    if (job && (await runUrlAction("start"))) goTo(job.input.workflow_mode ? "crawl" : "live");
  };

  const excludedCount = ev.split("\n").filter((l) => l.trim()).length;
  const site = shown?.target_url ?? input?.target_url ?? tv.trim();
  const scope = job?.scope;
  // 요약 '쓰기 요청': 범위 확인 뒤에는 서버 승인 방식, 그 전에는 진단 모드 기준
  let writeSummary = "승인 후 전송";
  if (scope) writeSummary = APPROVAL_MODE_SHORT[scope.approval_mode];
  else if (profile?.workflow && mv === "ai_auto_v1") writeSummary = "자동 승인 안 함";
  const to = job ? scanViewFor(job, flowStage) : null;
  const started = !!job && job.state !== "ready" && job.state !== "awaiting_approval";
  const ended = !!job && URL_ENDED_STATES.includes(job.state);
  const invalid = !shown && !!target.trim() && !input && !!profile;
  // 주소 밑 한 줄 (시안 #nHost): 이어 보는 점검이 아직 도는 중이면 그 점검으로, 아니면 주소 입력 안내
  let hostLine: ReactNode = null;
  if (shown && started && !ended) {
    hostLine = (
      <>
        <span className={s.err}>이미 점검 중인 사이트</span>{" "}
        <a className={s.busylink} href="#running">
          진행 중 점검 보기
        </a>
      </>
    );
  } else if (hint) hostLine = <span className={s.err}>{hint}</span>;
  else if (invalid) hostLine = <span className={s.err}>주소 형식을 확인하세요.</span>;
  else if (!shown && touched && !target.trim())
    hostLine = <span className={s.err}>점검할 사이트 주소를 입력하세요.</span>;

  // 오른쪽 아래 실행 단계 (입력 → 범위 확인 → 동의 · 시작)
  let action: ReactNode;
  if (page === 1 && !handle) {
    action = (
      <>
        <button className={cx(s.btn, s.pri, s.full)} type="button" disabled={!canNext} onClick={next}>
          다음
        </button>
        <span className={s.why}>{why1.join(" · ")}</span>
      </>
    );
  } else if (!handle) {
    action = (
      <>
        <button className={cx(s.btn, s.pri, s.full)} type="button" disabled={!canSend} onClick={() => void send()}>
          {busy ? "확인 중…" : "승인 범위 확인"}
        </button>
        <span className={s.why}>{why.join(" · ")}</span>
      </>
    );
  } else if (!job) {
    action = <p className={s.help}>{busy ? "진단 상태를 확인하는 중…" : "진단 상태를 확인하지 못했습니다."}</p>;
  } else if (job.state === "awaiting_approval") {
    action = (
      <>
        <div className={cx(s.note, s.warn)} role="note">
          <Icon name="alert" />
          <span>
            {job.reason_code
              ? `아직 시작할 수 없습니다 · ${reasonText(job.reason_code)}`
              : "설치자 승인이 필요합니다. 입력이나 확인 상자만으로 실행 권한이 생기지 않습니다."}
          </span>
        </div>
        <button
          className={cx(s.btn, s.full)}
          type="button"
          disabled={!g.canValidate}
          onClick={() => void runUrlAction("validate")}
        >
          승인 범위 다시 확인
        </button>
      </>
    );
  } else if (job.state === "ready") {
    action = (
      <>
        {scope?.approval_mode === "confirm_every_request" ? (
          <div className={cx(s.note, s.warn)} role="note">
            <Icon name="alert" />
            <span>요청마다 확인하는 프로필은 작업대에서 시작할 수 없습니다.</span>
          </div>
        ) : null}
        <label className={s.ckline}>
          <input
            type="checkbox"
            checked={confirmed}
            disabled={!g.canStart}
            onChange={(e) => setUrlConfirmed(e.target.checked)}
          />
          <span>
            {scope?.model?.external_transfer
              ? "대상 응답·도구 출력의 외부 전송과 비용 상한에 동의하고, 위 범위로 점검을 시작합니다."
              : "위 범위로 점검을 시작합니다."}
          </span>
        </label>
        <button
          className={cx(s.btn, s.pri, s.full)}
          type="button"
          disabled={!g.canStart || !confirmed}
          onClick={() => void start()}
        >
          {busy ? "보내는 중…" : "점검 시작"}
        </button>
      </>
    );
  } else {
    action = to ? (
      <button className={cx(s.btn, s.pri, s.full)} type="button" onClick={() => goTo(to)}>
        점검 보기
        <Icon name="arrow" />
      </button>
    ) : null;
  }

  // 폼 아래 시작 줄 (시안 formbar): 1쪽 [다음], 2쪽 [승인 범위 확인]. 범위 확인 뒤 시작 단계는 요약 패널에 있어 패널 여는 버튼
  let bar: ReactNode;
  if (page === 1) {
    bar = (
      <>
        <button className={cx(s.btn, s.pri)} type="submit" disabled={!canNext}>
          다음
        </button>
        <span className={s.why}>{why1.join(" · ")}</span>
      </>
    );
  } else if (handle) {
    bar = <InspOpenButton>시작 단계 보기</InspOpenButton>;
  } else {
    bar = (
      <>
        <button className={cx(s.btn, s.pri)} type="submit" disabled={!canSend}>
          승인 범위 확인
        </button>
        <span className={s.why}>{why.join(" · ")}</span>
      </>
    );
  }

  // 절대규칙 6 고지 (시안 v52): 할 일을 제목으로, 자세한 설명 · 원래 고지 문장은 (?) 메모에. 범위 확인 앞뒤 모두 보인다
  const warnRule = (
    <Rule
      warn
      t="열면 지워지는 주소는 빼 두세요"
      sub="조회 요청도 데이터를 바꿀 수 있고, 서버는 검증하지 않음"
      tip={
        <HelpTip label="설명 보기">
          <b>어떤 사이트는 주소를 열기만 해도 데이터가 지워집니다.</b>
          <br />
          예: <span className={s.mono}>/logout</span> (로그아웃) · <span className={s.mono}>/delete?id=3</span> (3번 글
          삭제)
          <br />
          <br />
          JinDo는 찾은 주소를 직접 열어 보며 점검합니다. 이런 주소가 섞여 있으면 점검하는 도중에 실제 글이 지워지거나
          계정이 로그아웃될 수 있습니다.
          <br />
          <br />
          GET · HEAD 와 query 도 상태를 바꿀 수 있고, 서버는 그 무해성을 검증하지 않습니다. 점검 전에 <b>제외 경로</b>에
          넣어 두세요. 제외 경로에는 <span className={s.mono}>/delete</span>처럼 경로만 넣으면 query 와 상관없이
          빠집니다(<span className={s.mono}>?</span> 가 든 값은 받지 않습니다).
        </HelpTip>
      }
    />
  );

  return (
    <>
      <UrlHead
        title="새 점검"
        crumb="새 점검"
        acts={
          <>
            <a className={s.btn} href="#dashboard">
              취소
            </a>
            {/* 계정 분류(시험 계정 표시, 운영자 전용·목업). 저장 위치·승인 주체는 §9-7 미정 */}
            <a className={s.btn} href="#classify">
              계정 분류
            </a>
            <InspOpenButton>요약</InspOpenButton>
          </>
        }
      />
      <div className={s.vb}>
        <form
          className={cx(s.vm, s.nform)}
          id="luForm"
          noValidate
          data-page={page}
          onSubmit={(e) => {
            e.preventDefault();
            // 1쪽에서 Enter 는 다음 쪽으로 (시안 startScan)
            if (page === 1) next();
            else void send();
          }}
        >
          {handle ? (
            <div className={s.note} role="note">
              <Icon name="info" />
              <span className={s["lv-grow"]}>
                <b className={s.mono}>{urlHost(handle.input.target_url)}</b>{" "}
                {job ? <UrlPill job={job} verified={verified} /> : null}
                {started ? " 이 브라우저가 이어 보는 점검입니다." : null}
              </span>
              {!started || ended ? (
                <button className={cx(s.btn, s.sm)} type="button" disabled={busy} onClick={disconnectUrl}>
                  새로 입력
                </button>
              ) : null}
            </div>
          ) : null}

          <div className={s.fgrps}>
            {/* biome-ignore lint/a11y/useSemanticElements: 시안 마크업 그대로 (fieldset 테두리 · 여백 없이 묶음만) */}
            <div className={s.fgrp} role="group" aria-label="대상" hidden={page !== 1}>
              <div className={s.fsec}>
                <div className={s["fsec-h"]}>
                  <h2 id="luStep1H" tabIndex={-1}>
                    대상
                  </h2>
                </div>
                <div className={s["fsec-b"]}>
                  <div className={s.field}>
                    <label className={s.lbl} htmlFor="luUrl">
                      점검할 사이트 주소
                    </label>
                    <input
                      className={cx(s.inp, s.mono, s["w-url"])}
                      id="luUrl"
                      type="url"
                      value={tv}
                      disabled={g.inputLocked}
                      autoComplete="off"
                      spellCheck={false}
                      maxLength={2048}
                      placeholder="https://"
                      aria-describedby="luHost"
                      aria-invalid={invalid ? "true" : "false"}
                      onChange={(e) => {
                        setTarget(e.target.value);
                        setTouched(true);
                      }}
                    />
                    <div className={s.hostline} id="luHost" aria-live="polite">
                      {hostLine}
                    </div>
                  </div>
                  <TargetApprovalPanel
                    requestLimit={profile?.workflow_request_limit}
                    allowStateChanges={profile?.workflow?.allow_reversible_writes}
                    url={tv}
                    disabled={busy}
                    onChanged={() => {
                      if (handle && !job?.preparation_started) void runUrlAction("validate");
                    }}
                  />
                </div>
              </div>
              <div className={s.stepnav}>
                <button className={cx(s.btn, s.pri)} type="button" disabled={!canNext} onClick={next}>
                  다음
                  <Icon name="arrow" />
                </button>
              </div>
            </div>

            {/* biome-ignore lint/a11y/useSemanticElements: 시안 마크업 그대로 (fieldset 테두리 · 여백 없이 묶음만) */}
            <div className={s.fgrp} role="group" aria-label="분석 모델 · 점검 설정" hidden={page !== 2}>
              <div className={s.fsec}>
                <div className={s["fsec-h"]}>
                  <h2 id="luStep2H" tabIndex={-1}>
                    분석 모델
                  </h2>
                </div>
                <div className={s["fsec-b"]}>
                  {/* 시안처럼 늘 보인다. 설치된 프로필이 없는 쪽은 고를 수 없다 */}
                  <div className={s.seg} role="radiogroup" aria-label="LLM 실행 위치">
                    {(
                      [
                        ["local", "로컬 실행", locals.length],
                        ["external", "외부 API", externals.length],
                      ] as const
                    ).map(([k, label, n]) => (
                      <label key={k}>
                        <input
                          type="radio"
                          name="nLlm"
                          value={k}
                          checked={shownKind === k}
                          disabled={g.inputLocked || !n}
                          onChange={() => {
                            setKind(k);
                            setCredentialReady(false);
                          }}
                        />
                        <span>{label}</span>
                      </label>
                    ))}
                  </div>
                  {/* 시안 #nLocal 은 모델 · LLM 서버 주소 두 칸. 서버 주소 · 연결 상태는 설치 프로필이 정하고 화면으로 받지
                      않아 모델 칸만 둔다(칸 폭은 시안 두 칸 줄 그대로) */}
                  <div className={s.two}>
                    <div className={s.field}>
                      <label className={s.lbl} htmlFor="luProfile">
                        모델
                      </label>
                      <select
                        className={cx(s.inp, s["w-sel"])}
                        id="luProfile"
                        value={pv}
                        disabled={g.inputLocked || !choices.length}
                        onChange={(e) => setProfileId(e.target.value)}
                      >
                        {(shown ? profiles.filter((p) => p.id === pv) : choices).map((p) => (
                          <option key={p.id} value={p.id}>
                            {p.model_label}
                          </option>
                        ))}
                        {!profiles.length ? <option value="">설치된 모델 없음</option> : null}
                      </select>
                    </div>
                  </div>
                  {external && !shown ? (
                    <ExternalModelSettings
                      key={`${pv}:${resetSeq}`}
                      profile={profile ?? undefined}
                      disabled={g.inputLocked}
                      onReady={setCredentialReady}
                    />
                  ) : null}
                  {external ? (
                    <div className={cx(s.note, s.warn)} role="note">
                      <Icon name="alert" />
                      <div>
                        대상 데이터와 프롬프트가 외부 제공자로 전송됩니다. 폐쇄망이나 민감한 환경에서는 로컬 실행을
                        쓰세요.
                      </div>
                    </div>
                  ) : null}
                </div>
              </div>

              {/* 점검 설정: 진단 모드 · 제외 경로는 탐색 흐름이 있는 프로필만 받는다 */}
              <div className={s.fsec}>
                <div className={s["fsec-h"]}>
                  <h2>점검 설정</h2>
                </div>
                <div className={s["fsec-b"]}>
                  {profile?.workflow ? (
                    <div className={s.field}>
                      <span className={s.lbl} id="nModeL">
                        진단 모드
                      </span>
                      <div className={s.opts} role="radiogroup" aria-labelledby="nModeL">
                        {MODES.map(([id, label, help]) => (
                          <label className={s.opt} key={id}>
                            <input
                              type="radio"
                              name="nMode"
                              value={id}
                              checked={mv === id}
                              disabled={g.inputLocked}
                              onChange={() => setWorkflowMode(id)}
                            />
                            <b>{label}</b>
                            <small>{help}</small>
                          </label>
                        ))}
                      </div>
                    </div>
                  ) : null}
                  {/* 쓰기 메서드 요청 (시안 #nSend): 실행 허용은 E② · 설치 정책이 정하고(절대규칙 3) 상태 변경은 별도 승인
                      시험환경이라(절대규칙 6) 화면에서 고르는 값으로 두지 않는다. 시안의 '전체 허용' 선택지는 뺐다 (리뷰 #479) */}
                  <div className={s.field}>
                    <span className={s.lbl}>쓰기 메서드 요청 (결제 · 수정 · 삭제)</span>
                    <p className={s.help}>쓰기 요청 범위는 설치 정책과 진단 모드가 정합니다.</p>
                  </div>
                  {profile?.workflow ? (
                    <div className={s.field}>
                      <label className={s.lbl} htmlFor="nExcl">
                        제외 경로 · 선택
                      </label>
                      <textarea
                        id="nExcl"
                        className={cx(s.inp, s["w-path"])}
                        value={ev}
                        disabled={g.inputLocked}
                        maxLength={2048}
                        onChange={(e) => setExcluded(e.target.value)}
                        placeholder={EXCLUDE_EXAMPLE}
                      />
                    </div>
                  ) : null}
                </div>
              </div>
              <div className={s.stepnav}>
                <button className={s.btn} type="button" onClick={() => turn(1)}>
                  이전
                </button>
              </div>
            </div>
          </div>

          {/* 아래 시작 줄: 요약 패널이 안 보이는 화면(좁은 화면 · 패널 접음)에서만 보인다(CSS) */}
          <div className={s.formbar}>{bar}</div>
        </form>

        <aside className={s.vi} aria-label="점검 요약">
          <div className={s["vi-head"]}>
            <div>
              <div className={s.eyebrow}>시작 전 확인</div>
              <h2>점검 요약</h2>
            </div>
            <CloseInspButton label="요약" />
          </div>
          <div className={s.isec} style={{ borderTop: 0, marginTop: 10, paddingTop: 0 }}>
            <dl className={s.sumkv}>
              {/* 시안의 로그인 · 점검 범위 · 점검 목표 줄은 서버 진단 입력에 칸이 없어 두지 않는다 */}
              <dt>사이트</dt>
              <dd className={s.mono} title={site || undefined}>
                {site ? urlHost(site) : "—"}
              </dd>
              <dt>분석 모델</dt>
              <dd>{profile ? `${profile.model_label} (${external ? "외부 API" : "로컬"})` : "—"}</dd>
              <dt>쓰기 요청</dt>
              <dd>{writeSummary}</dd>
            </dl>
          </div>
          <div className={s.isec}>
            {/* 안전 장치 (시안 v41): 짧은 말로, 서버 값 · 절대규칙 6 고지는 그대로 */}
            <h3>안전 장치</h3>
            {scope ? (
              <ul className={s.policy} id="luPolicy">
                <Rule
                  t="승인된 사이트 · 경로에만 요청"
                  sub={`${scope.origin} · ${scope.path_prefixes.join(", ")} · ${scope.methods.join("/")}`}
                />
                <Rule t={APPROVAL_MODE_RULE[scope.approval_mode]} />
                {excludedCount ? <Rule t="제외한 경로는 요청 안 함" sub={`직접 넣은 ${excludedCount}개`} /> : null}
                <Rule
                  t="요청 · 시간 상한"
                  sub={`대상 요청 ${scope.budget.requests}회 · ${scope.budget.duration_seconds}초 · 모델 요청 ${scope.budget.model_requests}회 · 토큰 입력 ${scope.budget.input_tokens} · 출력 ${scope.budget.output_tokens} · body 최대 ${scope.max_body_bytes} bytes`}
                />
                {scope.model ? (
                  <Rule
                    t="외부 모델로 대상 응답 전송"
                    sub={`${scope.model.provider} · 비용 상한 ${usdText(scope.budget.cost_usd_micros)}`}
                  />
                ) : null}
                {/* 외부 CDN 사전고지 (external-llm-notice-ko.md §2): 브라우저 탐색이 대상 페이지를 그리며 외부 호스트의
                    자원을 받으면 그 호스트 로그에 흔적이 남는다. 완전 로컬 모드는 막는다(절대규칙 8). 브라우저 탐색 미착륙 = 사전 고지.
                    ※ comm_mode(external_allowed/full_local) 별도 신호가 없어 외부 모델(scope.model)을 proxy 로 쓴다.
                    TODO(이경준): comm_mode 신호가 들어오면 조건을 그걸로 바꾼다(모델 외부전송 ≠ 브라우저 CDN 전송 — 이동찬 리뷰). */}
                {scope.model && profile?.workflow ? (
                  <Rule
                    t="브라우저 탐색은 외부 자원(CDN 등)을 외부 호스트에서 받습니다"
                    sub="그 호스트 로그에 접속 IP · 시각 · 주소 · 출처가 남습니다 · 완전 로컬 모드는 막습니다"
                    tip={
                      <HelpTip label="설명 보기">
                        <b>
                          대상 페이지는 스크립트 · 스타일 · 폰트 · 이미지를 외부 호스트(CDN 등)에서 불러올 수 있습니다.
                        </b>
                        <br />
                        <br />
                        브라우저 탐색이 대상 페이지를 그릴 때 그 자원을 받으면, 그 외부 호스트 쪽 로그에 접속 IP · 시각
                        · 요청 주소 · 출처(Referer)가 남습니다. 대상 서버에 남는 기록과는 별개입니다.
                        <br />
                        <br />
                        완전 로컬 모드에서는 이 외부 전송을 막습니다(절대규칙 8). 지금은 사전 고지이고, 브라우저 탐색이
                        연결된 뒤부터 적용됩니다.
                      </HelpTip>
                    }
                  />
                ) : null}
                <Rule t="모든 요청 · 승인 기록 남김" sub={`승인 만료 ${scope.expires_at}`} />
                {warnRule}
              </ul>
            ) : (
              // 범위 확인 전 (시안 #pTarget · #pSendT · #pExcl): 서버가 늘 지키는 기본 원칙만. 범위 · 상한 수치는 확인 뒤에 나온다
              <>
                <p className={cx(s.help, s.pnote)}>범위 확인 전 기본 원칙 · 범위와 상한 수치는 확인 뒤 서버 값</p>
                <ul className={s.policy} id="luPolicy">
                  <Rule t="이 사이트에만 요청" sub={site ? urlHost(site) : "주소 없음"} />
                  {external && profile?.model ? (
                    <Rule t="외부 모델로 대상 응답 전송" sub={profile.model.provider} />
                  ) : null}
                  <Rule t="쓰기 요청은 승인 후 전송" />
                  {profile?.workflow ? (
                    <Rule t="제외한 경로는 요청 안 함" sub={excludedCount ? `직접 넣은 ${excludedCount}개` : "없음"} />
                  ) : null}
                  <Rule t="모든 요청 · 승인 기록 남김" />
                  {warnRule}
                </ul>
              </>
            )}
          </div>
          <div className={s.isec}>
            {action}
            <UrlNotices />
          </div>
        </aside>
        <PaneToggle />
      </div>
    </>
  );
}
