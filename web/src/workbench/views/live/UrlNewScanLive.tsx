"use client";

// 새 점검 (시안 짜임: 대상 · 분석 모델 · 작업 + 오른쪽 점검 요약 · 안전 정책 · 시작).
// 순서: 입력 → [승인 범위 확인](초안 + 서버 범위 확인, DNS·HTTP·모델 호출 없음) → 서버가 돌려준 범위·예산을 오른쪽에서 보고
// 동의 상자 → [점검 시작]. 시작 전에는 task · Planner · 대상 송신 · 단회 권위를 만들지 않는다(서버 계약).
// URL 은 요청일 뿐이고 허가는 서버 설치 범위와 E② 가 정한다.
// 입력은 한 쪽(시안 fgrps · fgrp)에 대상 · 분석 모델 · 로그인 · 점검 설정을 차례로 둔다(사용자 11/24, 2쪽 · [다음] · [이전]을
// 없앰). [승인 범위 확인]은 화면 바닥 줄(formbar) 오른쪽 끝에 둔다. 범위 확인 뒤 단계(승인 대기 · 승인 범위 다시 확인 · 동의 ·
// 점검 시작)는 오른쪽 요약 패널에만 둔다(패널을 접으면 패널을 펴서 본다).
// 시안의 로그인(계정 A 관리자 / B 일반) · 최근 점검한 주소 · 점검 목표 · 점검 범위 · 워커 수 · LLM 서버 주소는 서버 진단
// 입력(URLDiagnosisInput)과 catalog 에 칸이 없다. 시안 모양 그대로 그리되 꺼 두고 '연결 전' 딱지를 붙인다 — 받아도 보낼 곳이
// 없는 값을 치게 하지 않고, 시작 조건에도 넣지 않는다. 고른 상태는 지금 실제 동작(로그인 없이)만 표시하고, 서버가 정하는
// 값(점검 범위 · 워커 수 · 등록 날짜 · LLM 연결 상태 · 최근 주소)은 지어내지 않는다.

import { type ReactNode, useCallback, useEffect, useRef, useState } from "react";

import {
  type URLDiagnosisInput,
  type URLDiagnosisModel,
  type URLDiagnosisProfile,
  type URLStartConfirmation,
  urlDiagnosisInput,
  type WorkflowMode,
} from "@/lib/diagnosis/diagnosis-url";
import { EXTERNAL_ACCOUNT_STATEMENT, readExternalTestAccountPresence } from "@/lib/diagnosis/external-account-consent";

import { cx } from "../../lib/cx";
import { NO_SPACE_USERNAME } from "../../live/accounts";
import { type ConfirmationPlan, confirmationPlanOf } from "../../live/confirmationPlan";
import { getDeclaration } from "../../live/declaration";
import { fmtDur, shortTimeOf } from "../../live/map";
import { useNow } from "../../live/parts";
import type { ResetState } from "../../live/resetConfirmation";
import { disconnectUrl, runUrlAction, setUrlConfirmed, useUrl, useUrlGates } from "../../live/urlDiagnosis";
import { scanViewFor, URL_ENDED_STATES, urlHost, urlTargetHint } from "../../live/urlMap";
import { useShell } from "../../shell";
import { useWb } from "../../store";
import { Icon } from "../../ui/icons";
import { CloseInspButton, HelpTip, InspOpenButton, PaneToggle } from "../../ui/parts";
import s from "../../workbench.module.css";
import { ApprovedWriteNotice } from "./ApprovedWriteNotice";
import { ExternalModelSettings } from "./ExternalModelSettings";
import { ResetConfirmationBox } from "./ResetConfirmationBox";
import { StuckReleaseBox } from "./StuckReleaseBox";
import { TargetApprovalPanel, type TargetStatus } from "./TargetApprovalPanel";
import { reasonText, UrlHead, UrlNotices, UrlPill } from "./urlParts";

/** 입력 → 서버에 보낼 초안 입력 (팀 lib 검사를 통과해야만). */

/** 시작 전 범위의 승인 방식(서버 값). */
type ApprovalMode = "preauthorized_scope" | "confirm_every_request" | "workbench_selective_v1";
// 점검 이름(display_name)은 선택 칸으로 받는다(사용자 11/18 — 서버 목록은 원문 URL 을 주지 않아 이름이 없으면 줄이
// 'prep-…' 진단 ID 로만 보였다). 비우면 보내지 않는다.
function draftInput(
  target: string,
  profile: string,
  name: string,
  mode?: WorkflowMode,
  excludes = "",
  externalTestAccounts = false,
  startConfirmation: URLStartConfirmation = {},
): URLDiagnosisInput | null {
  if (!profile) return null;
  try {
    return urlDiagnosisInput(target, profile, name, mode, excludes, externalTestAccounts, startConfirmation);
  } catch {
    return null;
  }
}

const MODES = [
  ["user_approval_v1", "사용자 승인", "탐색 뒤 점검할 경로를 고르고, 쓰기 메서드 요청은 한 건씩 승인합니다"],
  [
    "ai_auto_v1",
    "AI 자동",
    "승인 범위 안에서 탐색과 점검을 이어 갑니다 · 대상에 승인한 쓰기는 건별 확인 없이 보냅니다",
  ],
] as const;

// 요약 '쓰기 요청' 줄의 짧은 말 (범위 확인 뒤 scope.approval_mode). 안전 장치 목록의 긴 승인 방식 줄은 이 줄과 겹쳐
// 뺐다(사용자 11/18) — 임의 query 등 자세한 건 '정책 전체 보기'
const APPROVAL_MODE_SHORT: Readonly<Record<ApprovalMode, string>> = {
  preauthorized_scope: "쓰기 요청 없음 (읽기만)",
  confirm_every_request: "모든 요청 한 건씩 승인",
  workbench_selective_v1: "쓰기 요청 한 건씩 승인",
};
// 자동 모드(preauthorized_scope)는 범위에 쓰기 메서드가 있으면 그 쓰기를 건별 확인 없이 보낸다(C21).
const WRITE_METHODS = ["POST", "PUT", "PATCH", "DELETE"];
function approvalRule(mode: ApprovalMode, methods: readonly string[]): string {
  const writes = methods.filter((m) => WRITE_METHODS.includes(m));
  if (mode === "preauthorized_scope" && writes.length > 0) return `쓰기(${writes.join("·")}) 건별 확인 없이 보냄`;
  return APPROVAL_MODE_SHORT[mode];
}
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

/** 연결 전 딱지: 시안 칸은 있으나 서버 입력 · 데이터가 없어 꺼 둔 칸 (NotYet 과 같은 모양, 글만 '연결 전'). */
function Unwired({ why }: { why: string }) {
  return (
    <span className={cx(s.tag, s.out)} title={why}>
      연결 전<span className={s["sr-only"]}>{` · ${why}`}</span>
    </span>
  );
}
const NO_INPUT = "서버 진단 입력에 칸이 없습니다";
/** 진단 모드를 받지 않는 프로필(탐색 흐름 없음)에서 고른 진단 모드는 보내지 않는다. */
const NO_MODE = "이 모델 프로필은 진단 모드를 받지 않아 고른 값을 보내지 않습니다";

/** 시안 로그인 계정 줄 (역할 이름만, 값은 비운다). */
const LOGIN_ACCOUNTS = [
  ["A", "관리자"],
  ["B", "일반 · 테스트"],
] as const;
/** 점검 목표 '기타'에 적는 글 길이 상한(화면 칸만) */
const GOAL_OTHER_MAX = 80;
/** 제어 문자(줄바꿈 · 탭 등)를 뺀다 — 한 줄 칸에 남지 않게(점검 이름 칸과 같은 규칙) */
function withoutControlChars(v: string): string {
  return Array.from(v)
    .filter((ch) => ch.charCodeAt(0) >= 32 && (ch.charCodeAt(0) < 127 || ch.charCodeAt(0) > 159))
    .join("");
}
/** 시안 점검 목표. login: 계정이 있어야 하는 목표('로그인 없이 점검'이면 고를 수 없음, 시안 .goal[data-login]) */
const GOALS = [
  ["남의 정보 보기 (IDOR)", true],
  ["관리자 기능 접근", true],
  ["로그인 없이 접근", false],
  ["민감정보 노출", false],
] as const;

/** 제외 경로 칸의 흐린 예시. 경로 단위라 query(?) 없이 — 서버 · 웹 입력 검사가 '?' 를 받지 않는다. */
export const EXCLUDE_EXAMPLE = "/logout\n/delete";
/**
 * 제외 경로 칸은 한 줄에 쉼표로 나눠 적는다(사용자 11/24, 원래 줄마다 하나). 서버 입력(excluded_paths)은 그대로 줄마다 하나라
 * 쉼표로 나누고 앞뒤 공백을 지워 줄로 잇는다. 경로에는 공백을 쓸 수 없어(서버 · urlDiagnosisInput 검사) 지워도 뜻이 같다.
 * 경로 안의 쉼표는 구분자로 읽힌다.
 */
export function excludedLines(oneLine: string): string {
  return oneLine
    .split(",")
    .map((p) => p.trim())
    .filter(Boolean)
    .join("\n");
}
/** 줄마다 하나인 제외 경로를 칸에 보일 한 줄(쉼표)로 */
export function excludedOneLine(lines: string): string {
  return lines
    .split("\n")
    .map((p) => p.trim())
    .filter(Boolean)
    .join(", ");
}
/** 시작 확인 두 진술의 문장. 초안 입력(exclusions_reviewed · backup_confirmed)에 이 진술 여부가 기록된다. */
export const EXCLUSIONS_REVIEWED_STATEMENT =
  "GET으로 상태가 바뀌는 주소를 확인해 제외 경로에 넣었거나, 그런 주소가 없음을 확인했습니다.";
export const BACKUP_CONFIRMED_STATEMENT = "점검 대상이 복사본(시험 환경)이거나, 원본이면 백업이 있습니다.";

/** 외부 모델 연결 방식(mode)의 화면 이름 (시안 #nProv). */
const PROVIDER_NAME: Readonly<Record<URLDiagnosisModel["mode"], string>> = { openrouter: "OpenRouter" };

/** 모델 고르기 칸의 이름: 외부 모델은 제공자 · 승인한 endpoint, 로컬은 모델 이름. */
function profileChoiceLabel(p: URLDiagnosisProfile): string {
  return p.model ? `${PROVIDER_NAME[p.model.mode]} · ${p.model.provider}` : p.model_label;
}

/** live 새 점검 (URL 진단). */
/** [URL 인증]이 꺼진 까닭(마우스를 올리면) */
const URL_AUTH_WAIT: Partial<Record<TargetStatus, string>> = {
  invalid: "점검할 사이트 주소를 먼저 넣으세요",
  loading: "이 주소의 승인 상태를 확인하는 중입니다",
};
/** 주소 옆 인증 칸(사용자 11/24). 상태는 대상 승인 상자(TargetApprovalPanel onStatus)가 서버 답으로 알린다 */
function UrlAuthSlot({
  status,
  busy,
  expanded,
  onOpen,
}: {
  status: TargetStatus;
  busy: boolean;
  expanded: boolean;
  onOpen: () => void;
}) {
  if (status === "unsupported") return null;
  if (status === "approved") return <span className={cx(s.pill, s.ok)}>인증됨</span>;
  if (status === "pending") return <span className={cx(s.pill, s.wait)}>TXT 확인 대기</span>;
  // 확인 실패(uncertain)여도 누를 수 있다: 등록 칸을 열고 한 번 더 묻는다(등록 버튼은 확인될 때까지 꺼짐)
  const ready = status === "none" || status === "revoked" || status === "uncertain";
  return (
    <button
      className={s.btn}
      type="button"
      disabled={busy || !ready}
      title={URL_AUTH_WAIT[status]}
      aria-expanded={expanded}
      onClick={onOpen}
    >
      URL 인증
    </button>
  );
}

export function UrlNewScanLive() {
  const { goTo, route } = useShell();
  // 승인 만료가 오늘이 아니면 날짜도 보이려고 지금 시각을 둔다(렌더 중 시계를 읽지 않음)
  const now = useNow();
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
  const [name, setName] = useState("");
  const [kind, setKind] = useState<"local" | "external">(locals.length || !externals.length ? "local" : "external");
  const [profileId, setProfileId] = useState("");
  const [workflowMode, setWorkflowMode] = useState<WorkflowMode>("user_approval_v1");
  const [excluded, setExcluded] = useState("");
  const [credentialReady, setCredentialReady] = useState(false);
  // 연결 전 칸의 화면 상태(시안 nAuth · nHow). 서버 입력에 칸이 없어 보내지 않는다. 지금 점검은 로그인 없이 진행된다.
  const [auth, setAuth] = useState<"in" | "pub">("pub");
  const [how, setHow] = useState<"form" | "sess">("form");
  const [accountRevision, setAccountRevision] = useState(0);
  // 새 점검 화면 진입 세대(아래 route 효과가 올린다) — 진입마다 정책 선언을 다시 읽는 트리거
  const [entryGen, setEntryGen] = useState(0);
  // E③: 대상 칸이 서버에서 확인한 대상(id · 상태), 그 대상 선언으로 본 확정 단계 계획, 리셋 확인 상태
  const [productTarget, setProductTarget] = useState<{ id: string; state: string } | null>(null);
  // 주소 옆 인증 칸(사용자 11/24): 대상 승인 상자가 알려 준 상태와, [URL 인증]을 누를 때마다 올리는 펼침 신호
  const [urlAuth, setUrlAuth] = useState<TargetStatus>("invalid");
  const [urlAuthOpen, setUrlAuthOpen] = useState(0);
  const [urlAuthExpanded, setUrlAuthExpanded] = useState(false);
  // 점검 목표 '기타'와 직접 적은 글(연결 전 — 서버 진단 입력에 칸이 없어 보내지 않는다)
  const [goalOther, setGoalOther] = useState(false);
  const [goalOtherText, setGoalOtherText] = useState("");
  const [confirmationPlan, setConfirmationPlan] = useState<ConfirmationPlan | null>(null);
  // 지금 대상 판에 유효한 선언이 있는지. 서버 리셋 게이트는 이때만 시작을 거절한다(e3RunStartRefusal)
  const [declarationActive, setDeclarationActive] = useState(false);
  const [resetState, setResetState] = useState<ResetState | null>(null);
  const [planUnknown, setPlanUnknown] = useState(false);
  // 승인된 대상만 선언을 읽는다(선언은 승인된 대상에만 저장된다). 못 읽으면 '확정 단계 없음'으로 추정하지 않는다(규칙 5)
  const approvedTargetId = productTarget?.state === "approved" ? productTarget.id : null;
  // biome-ignore lint/correctness/useExhaustiveDependencies: entryGen 은 화면에 들어올 때마다 다시 읽는 트리거
  useEffect(() => {
    setConfirmationPlan(null);
    setDeclarationActive(false);
    setPlanUnknown(false);
    if (!approvedTargetId) return;
    const controller = new AbortController();
    getDeclaration(approvedTargetId, controller.signal)
      .then((load) => {
        if (controller.signal.aborted) return;
        setConfirmationPlan(confirmationPlanOf(load));
        setDeclarationActive(load.state === "current");
        setPlanUnknown(load.state === "unavailable" || load.state === "invalid");
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        console.warn("정책 선언을 읽지 못해 확정 단계 여부를 모름", error instanceof Error ? error.name : "unknown");
        setPlanUnknown(true);
      });
    return () => controller.abort();
  }, [approvedTargetId, entryGen]);
  // 대상(또는 그 상태)이 바뀌면 지난 리셋 상태를 버린다. 상자는 key 로 다시 그려져 새 상태를 알린다
  const onProductTarget = useCallback((t: { id: string; state: string } | null) => {
    setProductTarget(t);
    setResetState(null);
  }, []);
  const [accountPresence, setAccountPresence] = useState<{
    key: string;
    value: boolean;
    failed: boolean;
    statement: string;
    /** 시험 계정 자격은 있는데 대상 로그인 방식이 없음(#914 login_method_missing) */
    loginMissing: boolean;
  }>({
    key: "",
    value: false,
    failed: false,
    statement: "",
    loginMissing: false,
  });
  // 로그인 방식이 없을 때 '이번에는 익명으로 점검'을 직접 골랐는가(#914 인계: 고르지 않으면 시작 화면에 머문다). 대상이 바뀌면 풀린다
  const [anonChoice, setAnonChoice] = useState({ key: "", on: false });
  const [accountConsent, setAccountConsent] = useState({ key: "", checked: false });
  // 시작 확인 두 진술. 초안 입력에 들어가 초안을 만들 때 굳으므로 '승인 범위 확인' 전에 받는다.
  const [startConfirm, setStartConfirm] = useState({ key: "", exclusions: false, backup: false });
  // 주소 칸을 한 번이라도 고쳤는가: 처음 들어온 빈 칸에는 빨간 안내를 띄우지 않고, 지웠을 때만 띄운다 (시안 #nHost)
  const [touched, setTouched] = useState(false);
  // 새 점검에 들어올 때마다 대상의 시험 계정 유무·정책 선언을 다시 읽는다. 처음 한 번만 읽으면 그 뒤 계정 분류·정책 선언
  // 화면에서 더한 계정·선언이 반영되지 않아 "로그인 없이 진행"·확정 단계 안내 없음으로 남았다(1차 리허설 F2와 같은 종류,
  // 2026-11-22 합친 빌드 자동화에서 재현)
  // 새 점검에 들어온 순간 보던 점검이 끝난 점검이면 그 점검을 떼고 빈 입력으로 시작한다(사용자 11/24: '끝난 점검이라 입력이
  // 잠겨 있습니다 · 새로 입력' 없이 새 점검을 누르면 바로 다른 점검). 들어온 순간에만 본다 — 새 점검 화면에 있는 동안 고른
  // 끝난 점검은 떼지 않는다. 진행 중 · 범위 확인 뒤 점검은 그대로 이어 본다
  const freshOnEntry = useRef(false);
  useEffect(() => {
    if (route === "new") {
      setEntryGen((n) => n + 1);
      setAccountRevision((n) => n + 1);
      freshOnEntry.current = true;
    }
  }, [route]);
  const endedJob = !!job && URL_ENDED_STATES.includes(job.state);
  useEffect(() => {
    if (route !== "new" || !freshOnEntry.current || busy) return;
    // 이어 보는 점검의 상태를 아직 읽는 중이면 기다린다
    if (handle && !job) return;
    if (endedJob) disconnectUrl();
    // 조회 중이라 떼지 못했으면(disconnectUrl 이 그냥 돌아옴) 다음 바뀜에 다시 본다
    freshOnEntry.current = endedJob && !!useUrl.getState().handle;
  }, [route, handle, job, endedJob, busy]);
  const seenSeq = useRef(useWb.getState().prefill?.seq ?? 0);
  const seenReset = useRef(resetSeq);

  // '다른 진단 만들기' 뒤에는 폼을 비운다 (같은 입력으로 겹쳐 만들지 않게)
  useEffect(() => {
    if (resetSeq === seenReset.current) return;
    seenReset.current = resetSeq;
    setTarget("");
    setName("");
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
  const nv = shown ? (shown.display_name ?? "") : name;
  const mv = shown?.workflow_mode ?? workflowMode;
  // 서버로 가는 제외 경로(줄마다 하나)와 칸에 보이는 한 줄(쉼표)
  const ev = shown ? (shown.excluded_paths ?? "") : excludedLines(excluded);
  const evShown = shown ? excludedOneLine(shown.excluded_paths ?? "") : excluded;
  const external = profile?.model?.external_transfer === true;
  const consentKey = `${tv}\u0000${pv}\u0000${resetSeq}\u0000${accountRevision}`;
  // 입력이 바뀐 렌더부터 이전 동의를 배제한다(늦은 effect/응답을 기다리지 않는다).
  if (accountConsent.key !== consentKey) setAccountConsent({ key: consentKey, checked: false });
  // 시작 확인은 대상 · 모델 · 진단 모드 · 제외 경로를 보고 한 진술이라, 그중 하나라도 바뀌면 다시 받는다.
  const confirmKey = `${tv}\u0000${pv}\u0000${mv}\u0000${ev}\u0000${resetSeq}`;
  if (startConfirm.key !== confirmKey) setStartConfirm({ key: confirmKey, exclusions: false, backup: false });
  // 초안이 있으면 서버가 기록한 진술을 그대로 보인다(없는 진술을 체크된 것처럼 보이지 않는다).
  const exclusionsReviewed = shown
    ? shown.exclusions_reviewed === true
    : startConfirm.key === confirmKey && startConfirm.exclusions;
  const backupConfirmed = shown
    ? shown.backup_confirmed === true
    : startConfirm.key === confirmKey && startConfirm.backup;
  // 대상에 시험 계정이 있는지는 로컬·외부 모두 조회한다 (서버 runBindsAccounts: 로컬 모드는 동의 없이도, 외부 모드는 external_test_accounts 동의 시 결속).
  const hasTestAccounts = accountPresence.key === consentKey && accountPresence.value;
  const consentChecked =
    external &&
    hasTestAccounts &&
    !!accountPresence.statement &&
    accountConsent.key === consentKey &&
    accountConsent.checked;
  // 이 회차가 시험 계정으로 로그인(결속)하는가. 결속된 회차는 저장된 서버 입력을, 새 초안은 조회+모드로 판단한다 (정규화·새로고침에도 유지).
  //  - 외부 모델: 외부 전송 동의(external_test_accounts)가 있어야 결속. 로컬 모델: 동의 없이도 대상에 시험 계정이 있으면 결속.
  // 로그인 방식이 없으면(#914) 이번 초안은 시험 계정으로 로그인하지 못한다 — 익명으로 점검할지 사용자가 직접 고른다
  // 시작 전(초안 · 범위 확인 뒤)까지만 막는다 — 이미 시작한 점검은 서버가 익명으로 돌고 있다(workbench-live-data)
  const notStarted = !job || job.state === "ready" || job.state === "awaiting_approval";
  const loginMissing = notStarted && hasTestAccounts && accountPresence.loginMissing;
  const anonChosen = anonChoice.key === consentKey && anonChoice.on;
  const testAccountLogin =
    !loginMissing &&
    (shown
      ? shown.external_test_accounts === true || (!external && hasTestAccounts)
      : hasTestAccounts && (!external || consentChecked));
  // 로그인 칸 '연결 전' 설명: 시험 계정으로 로그인하면 '로그인 없이' 라고 단정하지 않는다.
  const loginUnwiredWhy = testAccountLogin
    ? `${NO_INPUT} · 시험 계정으로 로그인해 점검합니다`
    : `${NO_INPUT} · 지금 점검은 로그인 없이 진행됩니다`;
  useEffect(() => {
    // 로컬·외부 모두, 새 초안·결속 회차 모두 대상의 시험 계정 유무를 조회한다 (결속 회차는 정규화된 저장 주소로 다시 확인된다).
    if (!tv || urlTargetHint(tv)) return;
    const controller = new AbortController();
    let current = true;
    void readExternalTestAccountPresence(tv, controller.signal)
      .then((value) => {
        if (current)
          setAccountPresence({
            key: consentKey,
            value: value.hasTestAccounts,
            statement: value.statement,
            failed: false,
            loginMissing: value.loginMethodMissing,
          });
      })
      .catch(() => {
        if (current)
          setAccountPresence({ key: consentKey, value: false, statement: "", failed: true, loginMissing: false });
      });
    return () => {
      current = false;
      controller.abort();
    };
  }, [tv, consentKey]);
  // 주소 옆 인증 칸: 승인되면 '인증됨', 등록 뒤 TXT 를 기다리면 'TXT 확인 대기', 그 밖엔 [URL 인증](누르면 바로 밑 소유 확인
  // 상자가 펼쳐진다). 서버에 대상 승인이 없으면(404 · 501) 칸을 두지 않는다. 승인 자체는 서버가 DNS TXT 로 확인한다
  const urlAuthSlot = (
    <UrlAuthSlot status={urlAuth} busy={busy} expanded={urlAuthExpanded} onOpen={() => setUrlAuthOpen((n) => n + 1)} />
  );
  // 시험 계정 두 줄(A 소유 · B 교차) 아래 한 줄: 이번 회차가 실제로 로그인하는지(등록 여부와 다름, #874 리뷰)
  // 승인 전엔 로그인 줄을 두지 않는다(상자가 '대상 승인 전'으로 알린다)
  // 로그인 방식 없음(#914 인계): 시작 전에 알리고, '이번에는 익명으로 점검'을 직접 골라야 넘어간다. 고르지 않으면 이 화면에
  // 머물러 위 상자에서 로그인 방식을 저장할 수 있다. 이 값만으로 MFA · 지원 안 함을 말하지 않는다
  const loginMissingBox: ReactNode = loginMissing ? (
    <div className={cx(s.note, s.warn)} role="note" aria-label="로그인 방식 없음">
      <Icon name="alert" />
      <div className={s["lv-grow"]}>
        <p>계정은 저장됐습니다. 이 대상의 로그인 방식이 설정되지 않아 인증 점검을 시작할 수 없습니다.</p>
        <label className={s.ckline}>
          <input
            type="checkbox"
            checked={anonChosen}
            onChange={(e) => setAnonChoice({ key: consentKey, on: e.target.checked })}
          />
          이번에는 익명으로 점검
        </label>
      </div>
    </div>
  ) : null;
  const lockedKind = external ? "external" : "local";
  const shownKind = shown ? lockedKind : kind;
  // 분석 모델 칸의 짜임(모델 · 서버 주소 / 제공자 · API 키 · 경고)은 고른 쪽을 따른다. 그쪽 설치 프로필이 없어도 모양은 보인다
  const extView = shownKind === "external";
  const input = shown
    ? null
    : draftInput(
        target,
        pv,
        name,
        profile?.workflow ? workflowMode : undefined,
        // 칸의 한 줄(쉼표)을 서버 형식(줄마다 하나)으로
        profile?.workflow ? excludedLines(excluded) : "",
        consentChecked,
        { exclusionsReviewed, backupConfirmed },
      );

  const hint = urlTargetHint(tv);
  // 두 진술이 다 있어야 초안을 만든다. 서버는 이 칸으로 막지 않으므로(CLI · 평가 러너는 보내지 않는다) 화면이 받는다.
  const startConfirmed = exclusionsReviewed && backupConfirmed;
  // 막힌 이유: 주소 이유(why1) 다음에 모델 · 키 · 약관 동의 이유(why2). 바닥 줄 [승인 범위 확인] 옆에 함께 보인다
  const why1: string[] = [];
  const why2: string[] = [];
  if (!shown) {
    if (!target.trim()) why1.push("주소를 입력하세요");
    else if (hint) why1.push(hint);
    else if (!input && profile) why1.push("주소 형식을 확인하세요");
    if (!profiles.length) why2.push("설치된 모델 프로필이 없습니다");
    else if (!choices.length)
      why2.push(kind === "external" ? "설치된 외부 모델 프로필이 없습니다" : "설치된 로컬 모델 프로필이 없습니다");
    else if (!profile) why2.push("모델을 고르세요");
    if (external && !credentialReady) why2.push("외부 API 키를 등록하세요");
    if (!startConfirmed) why2.push("오른쪽 약관 동의 두 가지에 체크하세요");
    if (loginMissing && !anonChosen)
      why2.push("로그인 방식이 없습니다 — 로그인 방식을 저장하거나 '이번에는 익명으로 점검'을 고르세요");
  }
  const why = [...why1, ...why2];
  const canSend =
    !shown &&
    !!input &&
    g.canValidate &&
    !busy &&
    (!external || credentialReady) &&
    startConfirmed &&
    (!loginMissing || anonChosen);
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
  if (scope) writeSummary = approvalRule(scope.approval_mode, scope.methods);
  else if (profile?.workflow && mv === "ai_auto_v1") writeSummary = "건별 확인 없이 (대상 쓰기 승인 시)";
  const to = job ? scanViewFor(job, flowStage) : null;
  const started = !!job && job.state !== "ready" && job.state !== "awaiting_approval";
  const ended = !!job && URL_ENDED_STATES.includes(job.state);
  const invalid = !shown && !!target.trim() && !input && !!profile;
  // 이어 보는 점검이 있으면 입력은 그 점검 값으로 잠긴다. 왜 잠겼고 어떻게 푸는지 한 줄로 (끝남 · 진행 중 · 범위 확인 뒤)
  let lockNote = " 범위 확인한 입력은 잠겨 있습니다. 바꾸려면 새로 입력을 누르세요.";
  if (started) lockNote = " 진행 중인 점검이라 입력이 잠겨 있습니다. 끝나거나 멈춘 뒤 새로 입력할 수 있습니다.";
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

  // 범위 확인 뒤 단계 (승인 대기 · 승인 범위 다시 확인 · 동의 · 시작 · 점검 보기). 오른쪽 요약 패널에 들어간다
  let action: ReactNode;
  if (!job) {
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
        {external && hasTestAccounts && !testAccountLogin ? (
          <div className={cx(s.note, s.warn)} role="note">
            <Icon name="alert" />
            <span>
              시험 계정 외부 전송 동의를 하지 않아 시험 계정(로그인) 없이 점검합니다. 시험 계정으로 점검하려면 새로
              입력하세요.
            </span>
          </div>
        ) : null}
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
              ? "대상 응답·도구 출력(대상이 돌려준 비밀번호·개인정보 등 포함)이 외부 모델 제공자로 전송되며, 외부 전송된 정보에 대한 책임은 사용자에게 있습니다. 이에 동의하고 비용 상한 안에서 위 범위로 점검을 시작합니다."
              : "위 범위로 점검을 시작합니다."}
          </span>
        </label>
        {loginMissing && !anonChosen ? (
          <p className={s.err}>로그인 방식이 없습니다 — '이번에는 익명으로 점검'을 고르면 시작할 수 있습니다.</p>
        ) : null}
        <button
          className={cx(s.btn, s.pri, s.full)}
          type="button"
          disabled={!g.canStart || !confirmed || (loginMissing && !anonChosen)}
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

  // 바닥 줄: 왼쪽에 막힌 이유, 오른쪽 끝에 [승인 범위 확인] (한 쪽으로 합침, 사용자 11/24 — 2쪽 · [다음] · [이전] 없음).
  // 패널을 접고 펴도 그대로다. 범위 확인 뒤에는 오른쪽 요약 패널이 다음 단계를 맡는다
  const bar = (
    <>
      <span className={s.why}>{why.join(" · ")}</span>
      {handle ? null : (
        <button className={cx(s.btn, s.pri)} type="submit" disabled={!canSend}>
          {busy ? "확인 중…" : "승인 범위 확인"}
        </button>
      )}
    </>
  );

  // 모델 칸 옆: 로컬은 LLM 서버 주소(연결 전), 외부 API 는 API 키 (범위 확인 뒤에는 입력이 잠겨 그리지 않는다)
  let modelSide: ReactNode = null;
  if (!extView) {
    modelSide = (
      <div className={s.field}>
        <div className={s["fsec-ht"]}>
          <label className={s.lbl} htmlFor="nSrv">
            LLM 서버 주소
          </label>
          <Unwired why="모델 서버 주소는 설치 프로필이 정하고, 화면에서 보거나 바꾸는 서버 입력이 없습니다" />
        </div>
        <input id="nSrv" className={cx(s.inp, s.mono)} placeholder="설치 프로필이 정함" autoComplete="off" />
      </div>
    );
  } else if (!shown) {
    // 외부 전송 시험 계정 동의는 오른쪽 요약 '약관 동의'(accountConsentBox)
    modelSide = (
      <ExternalModelSettings
        key={`${pv}:${resetSeq}`}
        profile={profile ?? undefined}
        disabled={g.inputLocked}
        onReady={setCredentialReady}
      />
    );
  }

  // 외부 모델 시험 계정 세션 전송 동의(초안 입력 external_test_accounts). 오른쪽 요약 '약관 동의'에 둔다(사용자 11/24, 원래
  // 분석 모델 칸). 초안을 만든 뒤에는 서버 입력이 정해 이 칸을 두지 않는다(요약 · 시작 단계가 말한다)
  const accountConsentBox: ReactNode =
    extView && !shown ? (
      <>
        {hasTestAccounts && accountPresence.statement ? (
          <>
            <p className={s.help}>{EXTERNAL_ACCOUNT_STATEMENT}</p>
            <label className={s.ckline}>
              <input
                type="checkbox"
                checked={consentChecked}
                disabled={g.inputLocked || busy}
                onChange={(e) => setAccountConsent({ key: consentKey, checked: e.target.checked })}
              />
              <span>{accountPresence.statement}</span>
            </label>
            <p className={s.help}>체크하지 않으면 시험 계정 없이 진행합니다.</p>
          </>
        ) : null}
        {hasTestAccounts && !accountPresence.statement ? (
          <p className={s.help}>외부 전송 동의 문장을 받지 못해 시험 계정 없이 진행합니다.</p>
        ) : null}
        {accountPresence.key === consentKey && accountPresence.failed ? (
          // [계정 다시 조회] 버튼은 뺐다(사용자 11/24, 팀 공유) — 새 점검에 다시 들어오면 다시 읽는다
          <p role="alert" className={s.help}>
            시험 계정 등록 상태를 확인하지 못했습니다. 동의를 만들지 않고 시험 계정 없이 진행합니다.
          </p>
        ) : null}
      </>
    ) : null;

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
            <InspOpenButton>요약</InspOpenButton>
          </>
        }
      />
      <div className={s.vb}>
        <form
          className={cx(s.vm, s.nform)}
          id="luForm"
          noValidate
          onSubmit={(e) => {
            e.preventDefault();
            void send();
          }}
        >
          {/* 끝난 점검은 들어오자마자 떼므로(위 freshOnEntry) 잠금 안내 · [새로 입력]을 두지 않는다(사용자 11/24) */}
          {handle && !ended ? (
            <div className={s.note} role="note">
              <Icon name="info" />
              <span className={s["lv-grow"]}>
                <b className={s.mono}>{urlHost(handle.input.target_url)}</b>{" "}
                {job ? <UrlPill job={job} verified={verified} /> : null}
                {lockNote}
              </span>
              {!started ? (
                <button className={cx(s.btn, s.sm)} type="button" disabled={busy} onClick={disconnectUrl}>
                  새로 입력
                </button>
              ) : null}
            </div>
          ) : null}

          {handle && job?.state === "ready" ? (
            <div className={cx(s.note, s.warn)} role="note">
              <Icon name="alert" />
              <span className={s["lv-grow"]}>
                검증이 끝났습니다. 오른쪽 <b>점검 요약</b> 패널에서 동의한 뒤 <b>점검 시작</b>을 누르세요.
              </span>
              <InspOpenButton>점검 요약</InspOpenButton>
            </div>
          ) : null}

          <div className={s.fgrps}>
            {/* biome-ignore lint/a11y/useSemanticElements: 시안 마크업 그대로 (fieldset 테두리 · 여백 없이 묶음만) */}
            <div className={cx(s.fgrp, s.fgrid)} role="group" aria-label="대상 · 분석 모델 · 로그인 · 점검 설정">
              {/* 두 세로 단(사용자 11/24 한눈에): 왼쪽 대상 → 분석 모델, 오른쪽 로그인 → 점검 설정. 좁으면 한 줄로 이어진다 */}
              <div className={s.fcol}>
                <div className={s.fsec}>
                  <div className={s["fsec-h"]}>
                    <h2>대상</h2>
                  </div>
                  <div className={s["fsec-b"]}>
                    {/* 점검 이름(선택): 사이트 주소보다 위(사용자 11/25). 점검 목록 · 오른쪽 패널에 이 이름으로 보인다(사용자 11/18). 서버는 160바이트 · 제어 문자 없는
                      글만 받는다 — 한글 기준 50자로 막고 제어 문자는 지운다 */}
                    <div className={s.field}>
                      <label className={s.lbl} htmlFor="luName">
                        점검 이름 (선택)
                      </label>
                      <input
                        className={cx(s.inp, s["w-url"])}
                        id="luName"
                        type="text"
                        value={nv}
                        disabled={g.inputLocked}
                        autoComplete="off"
                        maxLength={50}
                        placeholder="예: 쇼핑몰 주문 API 점검"
                        onChange={(e) => setName(withoutControlChars(e.target.value))}
                      />
                    </div>
                    <div className={s.field}>
                      <label className={cx(s.lbl, s.must)} htmlFor="luUrl">
                        점검할 사이트 주소
                      </label>
                      <div className={s.urlrow}>
                        <input
                          className={cx(s.inp, s.mono, s["w-url"])}
                          id="luUrl"
                          aria-required="true"
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
                        {urlAuthSlot}
                      </div>
                      <div className={s.hostline} id="luHost" aria-live="polite">
                        {hostLine}
                      </div>
                    </div>
                    <TargetApprovalPanel
                      requestLimit={profile?.workflow_request_limit}
                      allowStateChanges={profile?.workflow?.allow_reversible_writes}
                      url={tv}
                      disabled={busy}
                      onTarget={onProductTarget}
                      openSignal={urlAuthOpen}
                      onStatus={setUrlAuth}
                      onOpenChange={setUrlAuthExpanded}
                      onChanged={() => {
                        setAccountRevision((value) => value + 1);
                        if (handle && !job?.preparation_started) void runUrlAction("validate");
                      }}
                    />
                    {productTarget ? (
                      <ResetConfirmationBox
                        key={`${productTarget.id}:${productTarget.state}`}
                        targetId={productTarget.id}
                        disabled={busy}
                        onState={setResetState}
                      />
                    ) : null}
                    {/* 멈춘 작업 해제(C04): 결과 불명 작업이 설치 전체의 새 점검 시작을 막을 때만 보인다 */}
                    <StuckReleaseBox />
                    {/* '기존 점검 다시 열기'(진행 · 끝난 점검 목록 링크)는 두지 않는다(사용자 11/18) — 대시보드의 진행 중 · 보존된 끝난 점검 목록('전체 보기')으로 간다 */}
                  </div>
                </div>

                {/* 분석 모델: 대상 바로 밑(사용자 11/24, 원래 2쪽 맨 위) — 로컬 실행 · 외부 API 와 모델 · 제공자 */}
                <div className={s.fsec}>
                  <div className={s["fsec-h"]}>
                    <h2>분석 모델</h2>
                  </div>
                  <div className={s["fsec-b"]}>
                    {/* 시안처럼 늘 보이고 늘 고를 수 있다. 설치된 프로필이 없는 쪽은 모양만 보이고 범위 확인을 보낼 수 없다 */}
                    <div className={s.seg} role="radiogroup" aria-label="LLM 실행 위치">
                      {(
                        [
                          ["local", "로컬 실행"],
                          ["external", "외부 API"],
                        ] as const
                      ).map(([k, label]) => (
                        <label key={k}>
                          <input
                            type="radio"
                            name="nLlm"
                            value={k}
                            checked={shownKind === k}
                            disabled={g.inputLocked}
                            onChange={() => {
                              setKind(k);
                              setCredentialReady(false);
                            }}
                          />
                          <span>{label}</span>
                        </label>
                      ))}
                    </div>
                    {/* 시안 #nLocal: 모델 · LLM 서버 주소. 서버 주소는 설치 프로필이 정하고 화면으로 받지 않아 꺼 둔다.
                      연결 상태(#nLocalState)는 서버가 알려 주지 않아 그리지 않는다.
                      시안 #nApi(외부 API): 제공자 · API 키 · 외부 전송 경고. 제공자는 설치된 외부 프로필 중에서 고른다.
                      반반 두 칸이면 다른 칸과 폭이 달라 위아래로 쌓는다(사용자 11/24 '네모 크기 통일') */}
                    <div className={s.field}>
                      <label className={cx(s.lbl, s.must)} htmlFor="luProfile">
                        {extView ? "제공자" : "모델"}
                      </label>
                      <select
                        className={s.inp}
                        id="luProfile"
                        aria-required="true"
                        value={pv}
                        disabled={g.inputLocked || !choices.length}
                        onChange={(e) => setProfileId(e.target.value)}
                      >
                        {(shown ? profiles.filter((p) => p.id === pv) : choices).map((p) => (
                          <option key={p.id} value={p.id}>
                            {profileChoiceLabel(p)}
                          </option>
                        ))}
                        {!shown && !choices.length ? (
                          <option value="">{extView ? "설치된 외부 모델 없음" : "설치된 모델 없음"}</option>
                        ) : null}
                      </select>
                    </div>
                    {modelSide}
                    {/* 외부 제공자 전송 안내 상자는 뺐다(사용자 11/24, 팀 공유). 외부 전송은 오른쪽 요약 안전 장치 줄과 시작
                      동의 문장이 알린다 */}
                  </div>
                </div>
              </div>
              <div className={s.fcol}>
                {/* 로그인 칸으로 되돌림(사용자 11/24 저녁: 계정 분류 · 정책 선언 칸을 빼고 전에 있던 로그인 창 · IDOR · 쿠키 칸으로).
                  로그인 (시안 #nAuth): 서버 진단 입력에 계정 칸이 아직 없다. 화면은 시안처럼 고르고 고칠 수 있게 두되,
                  여기 넣은 값은 이 화면 상태에만 있고 서버·저장소로 보내지 않는다(연결 전) */}
                <div className={s.fsec}>
                  <div className={cx(s["fsec-h"], s["fsec-ht"])}>
                    <h2>로그인</h2>
                    <Unwired why={loginUnwiredWhy} />
                    {/* #914: 대상 시험 계정의 로그인 방식이 없으면 알리고 '이번에는 익명으로 점검'을 고르게 한다 */}
                    {approvedTargetId ? loginMissingBox : null}
                  </div>
                  <div className={s["fsec-b"]}>
                    <div className={s.seg} role="radiogroup" aria-label="로그인 여부">
                      <label>
                        <input
                          type="radio"
                          name="nAuth"
                          value="in"
                          checked={auth === "in"}
                          onChange={() => setAuth("in")}
                        />
                        <span>로그인해서 점검</span>
                      </label>
                      <label>
                        <input
                          type="radio"
                          name="nAuth"
                          value="pub"
                          checked={auth === "pub"}
                          onChange={() => setAuth("pub")}
                        />
                        <span>로그인 없이 점검</span>
                      </label>
                    </div>
                    <dl className={s.pubnote} hidden={auth !== "pub"}>
                      <dt>점검</dt>
                      <dd>
                        {GOALS.filter(([, login]) => !login).map(([t]) => (
                          <span className={s.pg} key={t}>
                            {t}
                          </span>
                        ))}
                      </dd>
                      <dt>빠짐</dt>
                      <dd>
                        {GOALS.filter(([, login]) => login).map(([t]) => (
                          <span className={cx(s.pg, s.off)} key={t}>
                            {t}
                          </span>
                        ))}
                        <small>계정이 있어야 함</small>
                      </dd>
                    </dl>
                    <div className={s["fsec-b"]} hidden={auth !== "in"}>
                      <div className={s.seg} role="radiogroup" aria-label="로그인 방법">
                        <label>
                          <input
                            type="radio"
                            name="nHow"
                            value="form"
                            checked={how === "form"}
                            onChange={() => setHow("form")}
                          />
                          <span>아이디 · 비밀번호</span>
                        </label>
                        <label>
                          <input
                            type="radio"
                            name="nHow"
                            value="sess"
                            checked={how === "sess"}
                            onChange={() => setHow("sess")}
                          />
                          <span>쿠키 붙여넣기</span>
                        </label>
                      </div>
                      {how === "form"
                        ? LOGIN_ACCOUNTS.map(([id, role]) => (
                            <div className={s.acct} key={id}>
                              <span className={s.an}>
                                {`계정 ${id}`}
                                <small className={id === "A" ? s.adm : undefined}>{role}</small>
                              </span>
                              <input
                                className={s.inp}
                                placeholder="아이디"
                                autoComplete="off"
                                {...NO_SPACE_USERNAME}
                                aria-label={`계정 ${id} (${role}) 아이디`}
                              />
                              <input
                                className={s.inp}
                                type="password"
                                placeholder="비밀번호"
                                autoComplete="new-password"
                                aria-label={`계정 ${id} (${role}) 비밀번호`}
                              />
                            </div>
                          ))
                        : LOGIN_ACCOUNTS.map(([id, role]) => (
                            <div className={s.field} key={id}>
                              <label className={s.lbl} htmlFor={`nS${id}`}>{`계정 ${id} 쿠키 · ${role}`}</label>
                              <textarea
                                className={s.inp}
                                id={`nS${id}`}
                                placeholder="Cookie: session=…"
                                autoComplete="off"
                              />
                            </div>
                          ))}
                    </div>
                  </div>
                </div>
                {/* 점검 설정: 진단 모드 · 점검 목표 · 제외 경로(사용자 11/24, 원래 2쪽). 진단 모드 · 제외 경로는 탐색 흐름이
                  있는 프로필만 받는다. 시작 확인 두 칸은 오른쪽 요약 '약관 동의', 쓰기 메서드 요청 · 점검 범위 · 워커 수는 2쪽과
                  함께 뺐다 */}
                <div className={s.fsec}>
                  <div className={s["fsec-h"]}>
                    <h2>점검 설정</h2>
                  </div>
                  <div className={s["fsec-b"]}>
                    {/* 진단 모드: 늘 보이고 고를 수 있다. 탐색 흐름이 없는 프로필은 이 값을 받지 않아 '연결 전'(보내지 않음) */}
                    <div className={s.field}>
                      <div className={s["fsec-ht"]}>
                        <span className={s.lbl} id="nModeL">
                          진단 모드
                        </span>
                        {profile?.workflow ? null : <Unwired why={NO_MODE} />}
                      </div>
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
                    {/* 점검 목표 (시안 #nGoalL): 서버 진단 입력에 칸이 없어 꺼 둔다. 계정이 있어야 하는 목표는 지금 상태(로그인
                      없이)에서 고를 수 없는 모양(.off). 무엇을 점검할지 고른 것처럼 보이지 않게 체크는 하지 않는다 */}
                    <div className={s.field}>
                      <div className={s["fsec-ht"]}>
                        <span className={s.lbl} id="nGoalL">
                          점검 목표
                        </span>
                        <Unwired why={NO_INPUT} />
                      </div>
                      {/* biome-ignore lint/a11y/useSemanticElements: 시안 마크업 그대로 */}
                      <div className={s.goals} role="group" aria-labelledby="nGoalL">
                        {GOALS.map(([t, login]) => (
                          // 계정이 있어야 하는 목표는 이번 회차가 시험 계정으로 로그인하지 않으면 흐리게 막는다(11/24: 시안의 로그인 고르기 칸을 빼서 실제 결속을 따른다)
                          <label className={cx(s.goal, login && auth !== "in" && s.off)} key={t}>
                            <input type="checkbox" disabled={login && auth !== "in"} />
                            <span>{t}</span>
                          </label>
                        ))}
                        {/* 기타(사용자 11/24): 고르면 옆에 직접 적는 칸. 다른 목표처럼 화면 상태에만 있고 서버로 보내지 않는다 */}
                        <label className={s.goal}>
                          <input
                            type="checkbox"
                            checked={goalOther}
                            onChange={(e) => {
                              setGoalOther(e.target.checked);
                              if (e.target.checked)
                                requestAnimationFrame(() => document.getElementById("nGoalOther")?.focus());
                            }}
                          />
                          <span>기타</span>
                        </label>
                        {goalOther ? (
                          <input
                            id="nGoalOther"
                            className={cx(s.inp, s["goal-other"])}
                            type="text"
                            aria-label="기타 점검 목표"
                            value={goalOtherText}
                            maxLength={GOAL_OTHER_MAX}
                            autoComplete="off"
                            placeholder="예: 쿠폰 중복 사용"
                            onChange={(e) => setGoalOtherText(withoutControlChars(e.target.value))}
                          />
                        ) : null}
                      </div>
                    </div>
                    {profile?.workflow ? (
                      <div className={s.field}>
                        <label className={s.lbl} htmlFor="nExcl">
                          제외 경로 · 선택
                        </label>
                        <input
                          id="nExcl"
                          className={cx(s.inp, s.mono, s["w-url"])}
                          type="text"
                          value={evShown}
                          disabled={g.inputLocked}
                          maxLength={2048}
                          autoComplete="off"
                          spellCheck={false}
                          onChange={(e) => setExcluded(e.target.value)}
                          placeholder={excludedOneLine(EXCLUDE_EXAMPLE)}
                        />
                      </div>
                    ) : null}
                    {/* GET 으로 상태가 바뀌는 주소 · 제외 경로 안내 상자는 뺐다(사용자 11/24, 팀 공유). 시작 확인 두 칸은 오른쪽 요약
                      '약관 동의' */}
                  </div>
                </div>
              </div>
            </div>
          </div>

          {/* 바닥 줄: 모든 화면 폭 · 패널 상태에서 늘 바닥 */}
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
              {/* 시안의 로그인 · 점검 범위 · 점검 목표 줄은 서버 진단 입력에 칸이 없어 값 대신 '—' 와 연결 전 딱지 */}
              <dt>사이트</dt>
              <dd className={s.mono} title={site || undefined}>
                {site ? urlHost(site) : "—"}
              </dd>
              <dt>로그인</dt>
              <dd>
                {testAccountLogin ? "시험 계정 로그인" : "—"} <Unwired why={loginUnwiredWhy} />
              </dd>
              <dt>분석 모델</dt>
              <dd>{profile ? `${profile.model_label} (${external ? "외부 API" : "로컬"})` : "—"}</dd>
              <dt>점검 범위</dt>
              <dd>
                — <Unwired why={NO_INPUT} />
              </dd>
              <dt>점검 목표</dt>
              <dd>
                — <Unwired why={NO_INPUT} />
              </dd>
              <dt>쓰기 요청</dt>
              <dd>{writeSummary}</dd>
            </dl>
          </div>
          <div className={s.isec}>
            {/* 범위 확인 뒤 서버가 결속한 상한과 승인 만료는 시작 결정을 위해 이 화면에 보인다(이경준 #709 P2). */}
            {/* 전송 통제 정책은 결과가 아니라 범위라 여기서 연다(작업대 실데이터 계약 §7-(1)) */}
            <div className={s.sh}>
              <h3>안전 장치</h3>
              <button className={cx(s.btn, s.sm)} type="button" onClick={() => goTo("controlpolicy")}>
                정책 전체 보기
              </button>
            </div>
            {scope ? (
              <ul className={s.policy} id="luPolicy">
                <Rule
                  t="승인된 사이트 및 경로에만 요청"
                  sub={`${scope.origin} · ${scope.path_prefixes.join(", ")} · ${scope.methods.join("/")}`}
                />
                {scope.origin.startsWith("http://") ? (
                  <Rule
                    warn
                    t="암호화하지 않는 주소 (http)"
                    sub="요청·응답(로그인 세션 포함)이 네트워크에서 그대로 보일 수 있고, 서버 신원(TLS)을 확인하지 않습니다"
                  />
                ) : null}
                {excludedCount ? <Rule t="제외한 경로는 요청 안 함" sub={`직접 넣은 ${excludedCount}개`} /> : null}
                {/* 상한 · 비용 · 승인 만료를 한 줄로(이경준 #709 P2 예시 모양, 사용자 11/19). 모델 요청 · 토큰 · 본문 상한은 줄에서 뺐다 */}
                <Rule
                  t="상한"
                  sub={[
                    `요청 ${scope.budget.requests}회`,
                    fmtDur(scope.budget.duration_seconds),
                    scope.model
                      ? `비용 $${(scope.budget.cost_usd_micros / 1_000_000).toLocaleString("en-US", { maximumFractionDigits: 6 })}`
                      : null,
                    `승인 만료 ${shortTimeOf(scope.expires_at, now) || scope.expires_at}`,
                  ]
                    .filter(Boolean)
                    .join(" · ")}
                />
                {scope.model ? <Rule t="외부 모델로 대상 응답 전송" sub={scope.model.provider} /> : null}
                {/* E③ 확정 단계 안내: 이 대상의 현재 정책 선언이 확정 단계를 켤 모양일 때. 서버가 실행 때 최종으로 정하므로
                    조건은 (?) 메모에 둔다. 선언을 못 읽으면 '없음'으로 추정하지 않고 모른다고 보인다(규칙 5) */}
                {confirmationPlan ? (
                  <Rule
                    warn
                    t="확정 단계: 대상에 시험 객체를 만듭니다"
                    sub={`소유자 전용 종류 ${confirmationPlan.types}개 · 계정 ${confirmationPlan.ownerAccount}가 만들고 ${confirmationPlan.crossAccount}가 읽어 봄`}
                    tip={
                      <HelpTip label="확정 단계 설명 보기">
                        확정 단계는 점검 시작 때 정책 선언을 고정해 읽고, 모델 단계가 끝난 뒤 프로그램이 소유자 전용
                        종류마다 시험 객체를 만들어 두 시험 계정으로 읽어 봅니다. 두 계정이 인증되고 대상 등록에 되돌릴
                        수 있는 쓰기 동의가 있을 때만 돌고, 아니면 확정 단계는 꺼진 채 점검합니다. 끝나면 대상을
                        리셋하고 확인해야 다음 확정 점검이 시작됩니다.
                      </HelpTip>
                    }
                  />
                ) : null}
                {!confirmationPlan && planUnknown ? (
                  <Rule
                    warn
                    t="확정 단계 여부를 확인하지 못했습니다"
                    sub="정책 선언을 읽지 못함 · 정책 선언 화면에서 확인"
                  />
                ) : null}
                {/* 서버가 실제로 시작을 거절할 때만(유효한 선언 + 지난 확정 단계 쓰기 + 리셋 확인 없음,
                    reset_confirmation_missing). 처음 점검하는 대상은 쓰기 기록이 없어 리셋 확인 없이 시작한다 */}
                {declarationActive && resetState?.resetRequired ? (
                  <Rule warn t="대상 리셋 확인 전" sub="리셋을 확인해야 이 점검이 시작됩니다 · 대상 칸 아래에서 확인" />
                ) : null}
                {/* 외부 CDN 사전고지 (external-llm-notice-ko.md §2): 브라우저 탐색이 대상 페이지를 그리며 외부 호스트의
                    자원을 받으면 그 호스트 로그에 흔적이 남는다. 완전 로컬 모드는 막는다(절대규칙 8). 브라우저 탐색 미착륙 = 사전 고지.
                    ※ comm_mode(external_allowed/full_local) 별도 신호가 없어 외부 모델(scope.model)을 proxy 로 쓴다.
                    TODO(이경준): comm_mode 신호가 들어오면 조건을 그걸로 바꾼다(모델 외부전송 ≠ 브라우저 CDN 전송 — 이동찬 리뷰). */}
                {scope.model && profile?.workflow ? (
                  <Rule
                    t="브라우저 탐색은 외부 자원(CDN 등)을 외부 호스트에서 받습니다"
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
                {warnRule}
              </ul>
            ) : (
              // 범위 확인 전 (시안 #pTarget · #pSendT · #pExcl): 범위 · 상한 수치는 확인 뒤에 나온다. 사이트 · 쓰기 요청은 위
              // 요약 줄이 말한다
              <ul className={s.policy} id="luPolicy">
                {external && profile?.model ? (
                  <Rule t="외부 모델로 대상 응답 전송" sub={profile.model.provider} />
                ) : null}
                {profile?.workflow && excludedCount ? (
                  <Rule t="제외한 경로는 요청 안 함" sub={`직접 넣은 ${excludedCount}개`} />
                ) : null}
                {warnRule}
              </ul>
            )}
          </div>
          <div className={s.isec}>
            {/* 약관 동의(사용자 11/24): 범위 확인 전 시작 확인 두 칸 · 외부 전송 동의, 범위 확인 뒤 같은 자리 밑에 시작 동의 */}
            <div className={s.sh}>
              <h3 id="nConfirmL" className={s.must}>
                약관 동의
              </h3>
            </div>
            <fieldset className={s.confirmset} aria-labelledby="nConfirmL">
              <label className={s.ckline}>
                <input
                  type="checkbox"
                  checked={exclusionsReviewed}
                  disabled={g.inputLocked || busy}
                  onChange={(e) =>
                    setStartConfirm({ key: confirmKey, exclusions: e.target.checked, backup: backupConfirmed })
                  }
                />
                <span>{EXCLUSIONS_REVIEWED_STATEMENT}</span>
              </label>
              <label className={s.ckline}>
                <input
                  type="checkbox"
                  checked={backupConfirmed}
                  disabled={g.inputLocked || busy}
                  onChange={(e) =>
                    setStartConfirm({
                      key: confirmKey,
                      exclusions: exclusionsReviewed,
                      backup: e.target.checked,
                    })
                  }
                />
                <span>{BACKUP_CONFIRMED_STATEMENT}</span>
              </label>
            </fieldset>
            <p className={s.help}>
              {shown
                ? "이 점검을 만들 때 기록된 확인입니다."
                : "두 확인은 점검 기록에 남습니다. 둘 다 체크해야 승인 범위 확인으로 넘어갑니다."}
            </p>
            {accountConsentBox}
            <ApprovedWriteNotice scope={scope} />
            <UrlNotices />
          </div>
          {/* 범위 확인 뒤 다음 단계(시작 동의 · [점검 시작] · 승인 대기 · 점검 보기)는 패널 바닥에 붙인다 — 위 요약 · 안전 장치가
              길어져도 패널을 끝까지 내리지 않고 바로 동의 · 시작한다(사용자 11/25) */}
          {handle && action ? <div className={s.vistick}>{action}</div> : null}
        </aside>
        <PaneToggle />
      </div>
    </>
  );
}
