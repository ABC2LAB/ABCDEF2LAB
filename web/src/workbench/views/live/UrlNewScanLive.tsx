"use client";

// 새 점검 (시안 짜임: 대상 · 분석 모델 · 작업 + 오른쪽 점검 요약 · 안전 정책 · 시작).
// 순서: 입력 → [승인 범위 확인](초안 + 서버 범위 확인, DNS·HTTP·모델 호출 없음) → 서버가 돌려준 범위·예산을 오른쪽에서 보고
// 동의 상자 → [점검 시작]. 시작 전에는 task · Planner · 대상 송신 · 단회 권위를 만들지 않는다(서버 계약).
// URL 은 요청일 뿐이고 허가는 서버 설치 범위와 E② 가 정한다.
// 입력은 두 쪽 묶음(시안 fgrps · fgrp)으로 나눈다: 1쪽 대상 → [다음] → 2쪽 분석 모델 · 점검 설정 → [승인 범위 확인].
// 쪽 이동 버튼(1쪽 [다음] · 2쪽 [이전][승인 범위 확인])은 화면 바닥 줄(formbar) 오른쪽 끝에 둔다. 범위 확인 뒤 단계(승인 대기 ·
// 승인 범위 다시 확인 · 동의 · 점검 시작)는 오른쪽 요약 패널에만 둔다(패널을 접으면 패널을 펴서 본다).
// 시안의 로그인(계정 A 관리자 / B 일반) · 최근 점검한 주소 · 점검 목표 · 점검 범위 · 워커 수 · LLM 서버 주소는 서버 진단
// 입력(URLDiagnosisInput)과 catalog 에 칸이 없다. 시안 모양 그대로 그리되 꺼 두고 '연결 전' 딱지를 붙인다 — 받아도 보낼 곳이
// 없는 값을 치게 하지 않고, 시작 조건에도 넣지 않는다. 고른 상태는 지금 실제 동작(로그인 없이)만 표시하고, 서버가 정하는
// 값(점검 범위 · 워커 수 · 등록 날짜 · LLM 연결 상태 · 최근 주소)은 지어내지 않는다.

import { type ReactNode, useCallback, useEffect, useRef, useState } from "react";

import {
  type URLDiagnosisInput,
  type URLDiagnosisModel,
  type URLDiagnosisProfile,
  urlDiagnosisInput,
  type WorkflowMode,
} from "@/lib/diagnosis/diagnosis-url";
import {
  EXTERNAL_ACCOUNT_PASSWORD_NOTICE,
  EXTERNAL_ACCOUNT_STATEMENT,
  readExternalTestAccountPresence,
} from "@/lib/diagnosis/external-account-consent";

import { cx } from "../../lib/cx";
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
import { TargetApprovalPanel } from "./TargetApprovalPanel";
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
): URLDiagnosisInput | null {
  if (!profile) return null;
  try {
    return urlDiagnosisInput(target, profile, name, mode, excludes, externalTestAccounts);
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
/** 시안 #nWorkers 선택지. 화면 상태만이고 서버 진단 입력에 칸이 없어 보내지 않는다(실제 값은 엔진 설정). */
const WORKER_CHOICES = [1, 2, 3, 4, 6, 8] as const;
const LOGIN_ACCOUNTS = [
  ["A", "관리자"],
  ["B", "일반 · 테스트"],
] as const;
/** 시안 점검 목표. login: 계정이 있어야 하는 목표('로그인 없이 점검'이면 고를 수 없음, 시안 .goal[data-login]) */
const GOALS = [
  ["남의 정보 보기 (IDOR)", true],
  ["관리자 기능 접근", true],
  ["로그인 없이 접근", false],
  ["민감정보 노출", false],
] as const;
const DEPTHS = ["빠르게", "보통", "꼼꼼하게"] as const;

/** 제외 경로 칸의 흐린 예시. 경로 단위라 query(?) 없이 — 서버 · 웹 입력 검사가 '?' 를 받지 않는다. */
export const EXCLUDE_EXAMPLE = "/logout\n/delete";

/** 외부 모델 연결 방식(mode)의 화면 이름 (시안 #nProv). */
const PROVIDER_NAME: Readonly<Record<URLDiagnosisModel["mode"], string>> = { openrouter: "OpenRouter" };

/** 모델 고르기 칸의 이름: 외부 모델은 제공자 · 승인한 endpoint, 로컬은 모델 이름. */
function profileChoiceLabel(p: URLDiagnosisProfile): string {
  return p.model ? `${PROVIDER_NAME[p.model.mode]} · ${p.model.provider}` : p.model_label;
}

/** live 새 점검 (URL 진단). */
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
  const [accountRevision, setAccountRevision] = useState(0);
  // 새 점검 화면 진입 세대(아래 route 효과가 올린다) — 진입마다 정책 선언을 다시 읽는 트리거
  const [entryGen, setEntryGen] = useState(0);
  // E③: 대상 칸이 서버에서 확인한 대상(id · 상태), 그 대상 선언으로 본 확정 단계 계획, 리셋 확인 상태
  const [productTarget, setProductTarget] = useState<{ id: string; state: string } | null>(null);
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
  // 계정 분류 · 정책 선언 화면으로 가며 이 대상을 미리 고르게 한다
  const openPrep = (view: "classify" | "declaration") => {
    if (!approvedTargetId) return;
    useWb.getState().focusTargetOn(approvedTargetId);
    goTo(view);
  };
  let declarationText = "없음 · 확정 단계 없이 점검";
  if (declarationActive)
    declarationText = confirmationPlan ? `있음 · 소유자 전용 종류 ${confirmationPlan.types}개` : "있음";
  else if (planUnknown) declarationText = "확인하지 못함";
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
  }>({
    key: "",
    value: false,
    failed: false,
    statement: "",
  });
  const [accountConsent, setAccountConsent] = useState({ key: "", checked: false });
  // 주소 칸을 한 번이라도 고쳤는가: 처음 들어온 빈 칸에는 빨간 안내를 띄우지 않고, 지웠을 때만 띄운다 (시안 #nHost)
  const [touched, setTouched] = useState(false);
  // 지금 보이는 쪽 (1 대상 · 2 분석 모델 · 점검 설정). 새 점검에 다시 들어오면 1쪽부터 (시안 v64)
  const [page, setPage] = useState<1 | 2>(1);
  // 연결 전 칸의 화면 상태(시안 nAuth · nHow). 서버 입력에 칸이 없어 보내지 않는다. 지금 점검은 로그인 없이 진행된다.
  const [auth, setAuth] = useState<"in" | "pub">("pub");
  const [how, setHow] = useState<"form" | "sess">("form");
  // 새 점검에 들어올 때마다 대상의 시험 계정 유무·정책 선언을 다시 읽는다. 처음 한 번만 읽으면 그 뒤 계정 분류·정책 선언
  // 화면에서 더한 계정·선언이 반영되지 않아 "로그인 없이 진행"·확정 단계 안내 없음으로 남았다(1차 리허설 F2와 같은 종류,
  // 2026-11-22 합친 빌드 자동화에서 재현)
  useEffect(() => {
    if (route === "new") {
      setPage(1);
      setEntryGen((n) => n + 1);
      setAccountRevision((n) => n + 1);
    }
  }, [route]);
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
  const ev = shown ? (shown.excluded_paths ?? "") : excluded;
  const external = profile?.model?.external_transfer === true;
  const consentKey = `${tv}\u0000${pv}\u0000${resetSeq}\u0000${accountRevision}`;
  // 입력이 바뀐 렌더부터 이전 동의를 배제한다(늦은 effect/응답을 기다리지 않는다).
  if (accountConsent.key !== consentKey) setAccountConsent({ key: consentKey, checked: false });
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
  const testAccountLogin = shown
    ? shown.external_test_accounts === true || (!external && hasTestAccounts)
    : hasTestAccounts && (!external || consentChecked);
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
          });
      })
      .catch(() => {
        if (current) setAccountPresence({ key: consentKey, value: false, statement: "", failed: true });
      });
    return () => {
      current = false;
      controller.abort();
    };
  }, [tv, consentKey]);
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
        profile?.workflow ? excluded : "",
        consentChecked,
      );

  const hint = urlTargetHint(tv);
  // 막힌 이유: 1쪽(주소)과 2쪽(모델 · 키)을 나눠, 1쪽에서는 주소 이유만 보인다
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
  if (scope) writeSummary = approvalRule(scope.approval_mode, scope.methods);
  else if (profile?.workflow && mv === "ai_auto_v1") writeSummary = "건별 확인 없이 (대상 쓰기 승인 시)";
  const to = job ? scanViewFor(job, flowStage) : null;
  const started = !!job && job.state !== "ready" && job.state !== "awaiting_approval";
  const ended = !!job && URL_ENDED_STATES.includes(job.state);
  const invalid = !shown && !!target.trim() && !input && !!profile;
  // 이어 보는 점검이 있으면 입력은 그 점검 값으로 잠긴다. 왜 잠겼고 어떻게 푸는지 한 줄로 (끝남 · 진행 중 · 범위 확인 뒤)
  let lockNote = " 범위 확인한 입력은 잠겨 있습니다. 바꾸려면 새로 입력을 누르세요.";
  if (ended) lockNote = " 끝난 점검이라 입력이 잠겨 있습니다. 새 점검은 새로 입력을 누르세요.";
  else if (started) lockNote = " 진행 중인 점검이라 입력이 잠겨 있습니다. 끝나거나 멈춘 뒤 새로 입력할 수 있습니다.";
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

  // 바닥 줄: 쪽 이동 버튼은 오른쪽 끝 같은 자리 (1쪽 [다음], 2쪽 [이전][승인 범위 확인]). 패널을 접고 펴도 그대로다.
  // 2쪽만 왼쪽에 막힌 이유. 1쪽 주소 이유는 주소 칸 밑 안내(hostLine)가 이미 말해 [다음] 옆에는 두지 않는다.
  const bar = (
    <>
      {page === 1 ? null : <span className={s.why}>{why.join(" · ")}</span>}
      {page === 1 ? (
        <button className={cx(s.btn, s.pri)} type="submit" disabled={!canNext}>
          다음
          <Icon name="arrow" />
        </button>
      ) : (
        <>
          <button className={s.btn} type="button" onClick={() => turn(1)}>
            이전
          </button>
          {handle ? null : (
            <button className={cx(s.btn, s.pri)} type="submit" disabled={!canSend}>
              {busy ? "확인 중…" : "승인 범위 확인"}
            </button>
          )}
        </>
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
    modelSide = (
      <>
        <ExternalModelSettings
          key={`${pv}:${resetSeq}`}
          profile={profile ?? undefined}
          disabled={g.inputLocked}
          onReady={setCredentialReady}
        />
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
              <span>
                {accountPresence.statement}
                <br />
                {EXTERNAL_ACCOUNT_PASSWORD_NOTICE}
              </span>
            </label>
            <p className={s.help}>체크하지 않으면 시험 계정 없이 진행합니다.</p>
          </>
        ) : null}
        {hasTestAccounts && !accountPresence.statement ? (
          <p className={s.help}>외부 전송 동의 문장을 받지 못해 시험 계정 없이 진행합니다.</p>
        ) : null}
        {accountPresence.key === consentKey && accountPresence.failed ? (
          <p role="alert" className={s.help}>
            시험 계정 등록 상태를 확인하지 못했습니다. 동의를 만들지 않고 시험 계정 없이 진행합니다. 계정 조회를 다시
            시도할 수 있습니다.
            <button className={s.btn} type="button" onClick={() => setAccountRevision((value) => value + 1)}>
              계정 다시 조회
            </button>
          </p>
        ) : null}
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
                {lockNote}
              </span>
              {!started || ended ? (
                // 끝난 점검이면 새 점검이 다음 할 일이라 강조한다
                <button
                  className={cx(s.btn, s.sm, ended && s.pri)}
                  type="button"
                  disabled={busy}
                  onClick={disconnectUrl}
                >
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
            <div className={s.fgrp} role="group" aria-label="대상 · 로그인" hidden={page !== 1}>
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
                  {/* 점검 이름(선택): 점검 목록 · 오른쪽 패널에 이 이름으로 보인다(사용자 11/18). 서버는 160바이트 · 제어 문자 없는
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
                      onChange={(e) =>
                        setName(
                          Array.from(e.target.value)
                            .filter(
                              (ch) => ch.charCodeAt(0) >= 32 && (ch.charCodeAt(0) < 127 || ch.charCodeAt(0) > 159),
                            )
                            .join(""),
                        )
                      }
                    />
                  </div>
                  <TargetApprovalPanel
                    requestLimit={profile?.workflow_request_limit}
                    allowStateChanges={profile?.workflow?.allow_reversible_writes}
                    url={tv}
                    disabled={busy}
                    onTarget={onProductTarget}
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
                  {/* 계정 분류 · 정책 선언으로 잇는다(사용자 11/23: 새 점검에서 안 이어져 있었다). 승인된 대상만 —
                      계정 · 선언은 승인된 대상에만 저장된다. 그 화면은 이 대상을 미리 고르고, '새 점검으로'로 돌아온다 */}
                  {approvedTargetId ? (
                    <section className={s["prep-box"]} aria-label="계정 · 정책 선언">
                      <ul className={s.policy}>
                        <Rule
                          warn={!hasTestAccounts}
                          t="시험 계정"
                          sub={hasTestAccounts ? "등록됨 · 로그인해서 점검" : "없음 · 로그인 없이 점검"}
                        />
                        <Rule warn={!declarationActive} t="정책 선언" sub={declarationText} />
                      </ul>
                      <div className={s.acts}>
                        <button className={cx(s.btn, s.sm)} type="button" onClick={() => openPrep("classify")}>
                          계정 분류
                        </button>
                        <button className={cx(s.btn, s.sm)} type="button" onClick={() => openPrep("declaration")}>
                          정책 선언
                        </button>
                      </div>
                    </section>
                  ) : null}
                  {/* '기존 점검 다시 열기'(진행 · 끝난 점검 목록 링크)는 두지 않는다(사용자 11/18) — 대시보드의 진행 중 · 보존된 끝난 점검 목록('전체 보기')으로 간다 */}
                </div>
              </div>

              {/* 로그인 (시안 #nAuth): 서버 진단 입력에 계정 칸이 아직 없다. 화면은 시안처럼 고르고 고칠 수 있게 두되,
                  여기 넣은 값은 이 화면 상태에만 있고 서버·저장소로 보내지 않는다(연결 전) */}
              <div className={s.fsec}>
                <div className={cx(s["fsec-h"], s["fsec-ht"])}>
                  <h2>로그인</h2>
                  <Unwired why={loginUnwiredWhy} />
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
                      시안 #nApi(외부 API): 제공자 · API 키 · 외부 전송 경고. 제공자는 설치된 외부 프로필 중에서 고른다 */}
                  <div className={s.two}>
                    <div className={s.field}>
                      <label className={s.lbl} htmlFor="luProfile">
                        {extView ? "제공자" : "모델"}
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
                            {profileChoiceLabel(p)}
                          </option>
                        ))}
                        {!shown && !choices.length ? (
                          <option value="">{extView ? "설치된 외부 모델 없음" : "설치된 모델 없음"}</option>
                        ) : null}
                      </select>
                    </div>
                    {modelSide}
                  </div>
                  {extView ? (
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
                        // 시안처럼 계정이 있어야 하는 목표는 '로그인 없이'일 때 흐리게 막는다
                        <label className={cx(s.goal, login && auth !== "in" && s.off)} key={t}>
                          <input type="checkbox" disabled={login && auth !== "in"} />
                          <span>{t}</span>
                        </label>
                      ))}
                    </div>
                  </div>
                  {/* 쓰기 메서드 요청 (시안 #nSend): 실행 허용은 E② · 설치 정책이 정하고(절대규칙 3) 상태 변경은 별도 승인
                      시험환경이라(절대규칙 6) 화면에서 고르는 값으로 두지 않는다. 시안의 '전체 허용' 선택지는 뺐다 (리뷰 #479) */}
                  <div className={s.field}>
                    <span className={s.lbl}>쓰기 메서드 요청 (결제 · 수정 · 삭제)</span>
                    <p className={s.help}>쓰기 요청 범위는 설치 정책과 진단 모드가 정합니다.</p>
                  </div>
                  {/* 점검 범위 · 워커 수 (시안 #nDepthL · #nWorkers): 서버 진단 입력에 칸이 없고 실제 값은 엔진 설정이 정해,
                      고를 수는 있지만 이 화면 상태에만 있고 보내지 않는다 */}
                  <div className={s.two}>
                    <div className={s.field}>
                      <div className={s["fsec-ht"]}>
                        <span className={s.lbl} id="nDepthL">
                          점검 범위
                        </span>
                        <Unwired why={NO_INPUT} />
                      </div>
                      <div className={s.seg} role="radiogroup" aria-labelledby="nDepthL">
                        {DEPTHS.map((t) => (
                          <label key={t}>
                            <input type="radio" name="nDepth" value={t} />
                            <span>{t}</span>
                          </label>
                        ))}
                      </div>
                    </div>
                    <div className={s.field}>
                      <div className={s["fsec-ht"]}>
                        <label className={s.lbl} htmlFor="nWorkers">
                          워커 수
                        </label>
                        <Unwired why={NO_INPUT} />
                      </div>
                      <select id="nWorkers" className={cx(s.inp, s.workers)} defaultValue="">
                        <option value="">—</option>
                        {WORKER_CHOICES.map((n) => (
                          <option key={n} value={n}>
                            {n}
                          </option>
                        ))}
                      </select>
                    </div>
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
            <ApprovedWriteNotice scope={scope} />
            {handle ? action : null}
            <UrlNotices />
          </div>
        </aside>
        <PaneToggle />
      </div>
    </>
  );
}
