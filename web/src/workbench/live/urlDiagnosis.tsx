"use client";

// URL 진단(diagnosis-api.v2) 연결. 팀 lib(diagnosis-url)의 요청·검증·복구 함수를 그대로 쓰고,
// 상태 전이는 팀 URLDiagnosisPanel 과 같게 둔다. 작업대는 새 점검 · 범위 확인 · 점검 진행 · 보고서가
// 같은 진단을 보므로 상태를 한 스토어에 두고, 확인·주기 조회는 <UrlFeeds /> 한 곳에서만 한다.
// - 서버 기능은 읽기 전용 catalog 로 고른다: 성공 = v2, 404·501 = 기존 v1(POST /api/tasks),
//   그 밖 = 확인 불가(어느 쪽으로도 새 점검을 만들지 않는다). 변경 요청이 실패해도 다른 버전으로 다시 보내지 않는다.
// - 시작·중지는 자동 재전송하지 않는다. 결과가 불명이면 같은 진단의 상태 조회(GET)만 허용한다.
// - 브라우저에는 조회 핸들(영수증 키·최소 결속)만 팀 storeURLHandle 로 남긴다(팀 화면과 같은 칸).
//   인증값·설치 자료·상태는 저장하지 않는다.

import { useEffect } from "react";

import { create } from "zustand";

import { ApiError, subscribeSessionChanged } from "@/lib/diagnosis/api";
import { matchURLRun, type URLRunSummary } from "@/lib/diagnosis/diagnosis-runs";
import {
  commandURLDiagnosis,
  createURLDraft,
  matchURLHandle,
  matchURLJob,
  readURLCatalog,
  readURLHandle,
  readURLReceipt,
  readURLSelection,
  readURLStatus,
  sameURLInput,
  storeURLHandle,
  type URLDiagnosisCatalog,
  type URLDiagnosisHandle,
  type URLDiagnosisInput,
  type URLDiagnosisJob,
  type URLJobHandle,
  type URLRecoveredHandle,
  urlDiagnosisError,
  urlDiagnosisInput,
} from "@/lib/diagnosis/diagnosis-url";

import { getLiveAuthState, useLiveAuth } from "./auth";
import { URL_ACTIVE_STATES, URL_PRESTART_STATES } from "./urlMap";
import { loadUrlRuns, RUN_LIST_POLL_MS, refreshUrlRunsQuietly, resetUrlRuns } from "./urlRunList";
import { commandWorkflow, readWorkflow, type WorkflowSnapshot } from "./workflow";

/** 진행 중 진단 상태를 다시 읽는 간격(ms). 팀 화면과 같다. */
export const URL_POLL_MS = 2000;
/** URL API 미설치 상태는 운영자 설정이 필요하며 다른 엔진으로 전환하지 않는다. */
const UNSUPPORTED_STATUSES: readonly number[] = [404, 501];

/**
 * 서버의 새 점검 방식. unknown = 아직 확인 전(UrlFeeds 밖), checking = 확인 중,
 * v2 = URL 진단, unavailable = 미설치 또는 확인 불가(새 점검을 막는다).
 */
export type UrlCapability =
  | { kind: "unknown" }
  | { kind: "checking" }
  | { kind: "v2"; catalog: URLDiagnosisCatalog }
  | { kind: "unavailable"; message: string };

/** URL 진단 상태 (화면 넷이 함께 본다). */
export interface UrlStore {
  flow: WorkflowSnapshot | null;
  flowVerified: boolean;
  flowUncertain: boolean;
  flowBusy: boolean;
  flowError: string;
  cap: UrlCapability;
  /** 조회 핸들 (팀 storeURLHandle 과 같은 값). 있으면 새 입력을 잠근다. */
  handle: URLJobHandle | null;
  /** 이 브라우저가 만든 회차. 다른 회차를 조회해도 유지하며 주체 교체·연결 해제 때 버린다. */
  createdHandle: URLDiagnosisHandle | null;
  /** 목록 선택은 상태 검증 전에도 식별자만 보관한다. 실행 권위가 아니다. */
  selectedID: string | null;
  writeBusy: boolean;
  /** Job/주체가 바뀌면 관측 훅·화면의 누적 상태를 새로 만든다. */
  selectionEpoch: number;
  /** 마지막으로 받은 서버 진단 */
  job: URLDiagnosisJob | null;
  /** job 이 이번 조회에서 서버와 맞춰 본 값인가 */
  verified: boolean;
  /** 결과 불명: 조회(GET)만 허용한다 */
  uncertain: boolean;
  /** 요청 중 */
  busy: boolean;
  /** 시작 확인 상자 (서버 응답이 올 때마다 지운다) */
  confirmed: boolean;
  /** 마지막 안내 글 (팀 urlDiagnosisError) */
  error: string;
  /** '다른 진단 만들기' 를 누를 때마다 오른다 → 새 점검 폼을 비운다 */
  resetSeq: number;
}

type JobFields = Omit<UrlStore, "cap" | "resetSeq" | "selectionEpoch">;

const cleared = (): JobFields => ({
  flow: null,
  flowVerified: false,
  flowUncertain: false,
  flowBusy: false,
  flowError: "",
  handle: null,
  createdHandle: null,
  selectedID: null,
  writeBusy: false,
  job: null,
  verified: false,
  uncertain: false,
  busy: false,
  confirmed: false,
  error: "",
});

/** URL 진단 스토어. `useUrl((st) => st.job)` 처럼 원시값·스토어 안 객체를 고른다. */
export const useUrl = create<UrlStore>()(() => ({
  cap: { kind: "unknown" },
  ...cleared(),
  resetSeq: 0,
  selectionEpoch: 0,
}));

// 늦게 온 응답을 버리는 세대 번호와 겹침 방지 잠금 (팀 패널의 generation · lock ref 와 같다)
let generation = 0;
let lock = false;
let selectionRequest: AbortController | null = null;
let capRun = 0;
let flowGeneration = 0;
let flowLock = false;
let flowPending: { key: string; revision: number; operation: "selection" | "pause" | "resume"; ids: string[] } | null =
  null;

/** 진행 중인 응답을 모두 버리고 진단 상태를 비운다 (cap 은 그대로). */
function dropJob(preserveCreated = false): void {
  generation += 1;
  selectionRequest?.abort();
  selectionRequest = null;
  quietRun = null;
  flowGeneration += 1;
  lock = false;
  flowLock = false;
  flowPending = null;
  useUrl.setState((st) => {
    let createdHandle: URLDiagnosisHandle | null = null;
    if (preserveCreated) {
      createdHandle = st.createdHandle;
      if (st.handle && st.handle.kind !== "recovered") createdHandle = st.handle;
    }
    return { ...cleared(), createdHandle, selectionEpoch: st.selectionEpoch + 1 };
  });
}

/** 테스트용: 스토어를 처음 상태로. */
export function resetUrl(cap: UrlCapability = { kind: "unknown" }): void {
  capRun += 1;
  dropJob();
  resetUrlRuns();
  useUrl.setState({ cap, resetSeq: 0 });
}

function ownerOf(cap: UrlCapability): string | null {
  return cap.kind === "v2" ? cap.catalog.ownerContext : null;
}

/** 선택한 회차의 403이 다른 회차의 생성 키까지 지우지 않게 한다. */
function forgetSelectedCreation(): void {
  const st = useUrl.getState();
  const created = st.createdHandle;
  const keep = created && created.ownerContext === ownerOf(st.cap) && created.id !== st.selectedID ? created : null;
  storeURLHandle(keep);
  useUrl.setState({ createdHandle: keep });
}

/** 서버가 준 진단을 핸들에 맞춰 보고 받아들인다. 맞지 않으면 던진다(호출부가 결과 불명으로 처리). */
function accept(next: URLDiagnosisJob, saved: URLJobHandle): void {
  matchURLHandle(next, saved);
  const updated: URLJobHandle = {
    ...saved,
    id: next.id,
    revision: next.revision,
    sourceDigest: next.source_digest,
    ...(saved.bindingLocked || saved.startKey || next.preparation_started ? { bindingLocked: true as const } : {}),
  };
  storeURLHandle(updated);
  useUrl.setState({
    handle: updated,
    ...(updated.kind !== "recovered" ? { createdHandle: updated } : {}),
    selectedID: next.id,
    confirmed: false,
    job: next,
    verified: true,
    uncertain: next.state === "unknown",
  });
}

/** 저장된 핸들로 같은 진단을 다시 읽는다(GET 만). owner 가 다르면 팀 readURLHandle 이 버린다. */
async function bootJob(owner: string): Promise<void> {
  flowGeneration += 1;
  flowLock = false;
  flowPending = null;
  const current = ++generation;
  const selected = readURLSelection(owner);
  const created = readURLHandle(owner);
  const saved = selected && selected.id !== created?.id ? null : created;
  lock = false;
  useUrl.setState({
    ...cleared(),
    handle: saved,
    createdHandle: created,
    selectedID: selected ? selected.id : (saved?.id ?? null),
    busy: !!saved || !!selected,
    uncertain: !!saved || !!selected,
  });
  // 이어 보는 점검이 없으면(새 브라우저·재시작) 여기서 끝낸다. 서버 진행 목록은 기능 확인 직후 읽고(아래 loadUrlRuns),
  // 레일·진행 화면이 그 목록의 진행 중 점검을 '이 점검 열기'로 보인다(사용자 2026-11-22, F14)
  if (!saved && !selected) return;
  const request = new AbortController();
  selectionRequest = request;
  try {
    let next: URLDiagnosisJob;
    if (selected) next = await readURLStatus(selected.id, owner, request.signal);
    else if (saved?.id) next = await readURLStatus(saved.id, owner, request.signal);
    else if (saved) next = await readURLReceipt(saved.creationKey, owner);
    else return;
    if (current !== generation) return;
    accept(next, saved ?? recoveredHandle(next, owner));
  } catch (cause) {
    if (current !== generation) return;
    if (cause instanceof ApiError && cause.status === 404) {
      releaseStaleBinding(saved, created);
      return;
    }
    const patch: Partial<UrlStore> = { error: urlDiagnosisError(cause) };
    if (cause instanceof ApiError && cause.status === 403) {
      forgetSelectedCreation();
      patch.handle = null;
    }
    useUrl.setState(patch);
  } finally {
    if (current === generation) {
      selectionRequest = null;
      useUrl.setState({ busy: false });
    }
  }
}

/** 이 브라우저에 남은 진단을 서버(이 주체)가 모를 때의 안내. 실행 여부는 단정하지 않는다. */
export const STALE_BINDING_TEXT =
  "이 브라우저에 남아 있던 점검을 서버에서 찾지 못해 이어 보기를 풀었어요(재설치 · 초기화 등). 실행되지 않았다고 단정하지 않아요 — 서버 점검 목록에서 다시 고를 수 있어요.";

/**
 * 새로고침 때 다시 읽은 저장 핸들 · 조회 선택이 404 면(이 주체에게 그런 진단 · 영수증이 없음) 그 결속을 푼다.
 * 소유 문맥은 사용자 이름의 해시라 재설치 · DB 초기화 뒤에도 같아서, 앞 설치 때 열린 탭의 핸들이 그대로 통과한 채
 * 결과 불명(uncertain)으로 멈춰 새 점검 · 범위 확인이 막히고, 레일이 서버의 끝난 점검을 세지 않았다(11/24 화면 VAmPI 발견 5).
 * 서버에 다시 보내는 것은 없다 — 이 탭의 이어 보기만 푼다. 404 가 아닌 실패(연결 · 503 등)는 지금처럼 결과 불명으로 둔다.
 */
function releaseStaleBinding(saved: URLDiagnosisHandle | null, created: URLDiagnosisHandle | null): void {
  // 404 가 난 것이 생성 핸들이면 그것까지, 조회 선택만이면 다른 생성 핸들은 남긴다
  const keep = saved ? null : created;
  storeURLHandle(keep);
  useUrl.setState({
    handle: null,
    createdHandle: keep,
    selectedID: null,
    job: null,
    verified: false,
    uncertain: false,
    error: STALE_BINDING_TEXT,
  });
}

function recoveredHandle(job: URLDiagnosisJob, owner: string): URLRecoveredHandle {
  return {
    kind: "recovered",
    ownerContext: owner,
    id: job.id,
    revision: job.revision,
    sourceDigest: job.source_digest,
    input: job.input,
    bindingLocked: true,
  };
}

/** 목록 행은 힌트다. 같은 주체의 최신 status를 검증한 뒤에만 조회 결속을 만든다. */
export async function selectUrlRun(row: URLRunSummary): Promise<boolean> {
  const st = useUrl.getState();
  const owner = ownerOf(st.cap);
  if (!owner || st.writeBusy) return false;
  const saved = [st.handle, st.createdHandle, readURLHandle(owner)].find(
    (h): h is URLDiagnosisHandle => !!h && h.kind !== "recovered" && h.ownerContext === owner && h.id === row.id,
  );
  dropJob(true);
  const current = generation;
  const request = new AbortController();
  selectionRequest = request;
  useUrl.setState({ selectedID: row.id, busy: true });
  try {
    const next = await readURLStatus(row.id, owner, request.signal);
    if (current !== generation || ownerOf(useUrl.getState().cap) !== owner) return false;
    matchURLRun(next, row);
    accept(next, saved ?? recoveredHandle(next, owner));
    return true;
  } catch (error) {
    if (current !== generation) return false;
    useUrl.setState({ error: urlDiagnosisError(error), verified: false });
    return false;
  } finally {
    if (current === generation) {
      selectionRequest = null;
      useUrl.setState({ busy: false });
    }
  }
}

/** 같은 task 번호를 다른 주체/Job이 쓰더라도 관측 결과와 선택 상태를 재사용하지 않는다. */
export function useUrlSelectionKey(): string {
  return useUrl((st) => `${ownerOf(st.cap) ?? ""}:${st.selectionEpoch}`);
}

/** catalog 로 서버 방식을 확인한다. v2 면 저장된 진단을 이어서 읽는다. */
async function checkCapability(): Promise<void> {
  const id = ++capRun;
  dropJob();
  resetUrlRuns();
  useUrl.setState({ cap: { kind: "checking" } });
  try {
    const catalog = await readURLCatalog();
    if (id !== capRun) return;
    useUrl.setState({ cap: { kind: "v2", catalog } });
    const boot = bootJob(catalog.ownerContext);
    void loadUrlRuns(catalog.ownerContext);
    await boot;
  } catch (cause) {
    if (id !== capRun) return;
    if (cause instanceof ApiError && UNSUPPORTED_STATUSES.includes(cause.status))
      useUrl.setState({
        cap: {
          kind: "unavailable",
          message: "URL 진단 서비스가 설치되지 않았습니다. 운영자가 준비 서비스 연결을 확인해야 합니다.",
        },
      });
    else useUrl.setState({ cap: { kind: "unavailable", message: urlDiagnosisError(cause) } });
  }
}

/** 서버 방식을 다시 확인한다 (확인 불가 안내의 '다시 시도'). */
export function recheckUrl(): void {
  void checkCapability();
}

/** 화면이 버튼을 켜고 끄는 데 쓰는 값 (팀 패널의 계산과 같다). */
export interface UrlGates {
  /** 시작 요청을 이미 보냈다(같은 진단을 다시 시작하지 않는다) */
  startSent: boolean;
  /** 새 입력·새 초안을 막는다 */
  inputLocked: boolean;
  /** 범위 확인(validate)을 보낼 수 있다 — 입력이 맞는지는 화면이 따로 본다 */
  canValidate: boolean;
  /** 시작 확인 상자를 켤 수 있다 */
  canStart: boolean;
  /** 중지를 보낼 수 있다 */
  canStop: boolean;
  /** 상태 다시 조회를 보낼 수 있다 */
  canRefresh: boolean;
  /** 결과 저장이 확인돼 보고서를 읽어도 된다 */
  reportReadable: boolean;
  /** 엔진 시작이 확인되고 아직 마감 전이라 실제 task 의 실행 감독자 상태(요청 승인)를 읽어도 된다 (팀 liveReadable 과 같은 뜻) */
  runReadable: boolean;
}

/** 스토어 → 버튼 조건. */
export function urlGates(st: Pick<UrlStore, "handle" | "job" | "verified" | "uncertain" | "busy">): UrlGates {
  const { handle, job, verified, uncertain, busy } = st;
  const startSent = !!handle?.startKey;
  const recovered = handle?.kind === "recovered";
  const bound = recovered || startSent || !!handle?.bindingLocked;
  return {
    startSent,
    inputLocked: !!handle || busy,
    canValidate: !busy && !uncertain && !bound && !job?.preparation_started && !(!!handle && !job),
    // 요청별 허용·거절 UI가 연결되기 전에는 해당 모드로 새 회차를 시작하지 않는다.
    canStart:
      verified &&
      !uncertain &&
      !busy &&
      job?.state === "ready" &&
      (job.scope?.approval_mode === "preauthorized_scope" ||
        (job.scope?.approval_mode === "workbench_selective_v1" && job.input.workflow_mode === "user_approval_v1")) &&
      !startSent &&
      !handle?.bindingLocked &&
      !recovered,
    canStop: !!job && verified && !uncertain && !busy && (job.state === "preparing" || job.state === "running"),
    canRefresh: !busy && !!handle,
    reportReadable: verified && job?.result_ready === true && !!job.task_id,
    runReadable: verified && !uncertain && job?.started === true && !job.drain_complete && !!job.task_id,
  };
}

/** 지금 스토어의 버튼 조건 (구독). */
export function useUrlGates(): UrlGates {
  const handle = useUrl((st) => st.handle);
  const job = useUrl((st) => st.job);
  const verified = useUrl((st) => st.verified);
  const uncertain = useUrl((st) => st.uncertain);
  const busy = useUrl((st) => st.busy);
  return urlGates({ handle, job, verified, uncertain, busy });
}

/** 시작 확인 상자. 시작할 수 있을 때만 켠다. */
export function setUrlConfirmed(value: boolean): void {
  const st = useUrl.getState();
  useUrl.setState({ confirmed: value && urlGates(st).canStart });
}

export type UrlAction = "validate" | "start" | "stop" | "refresh";

/** 바탕 주기 조회(quiet refresh)가 도는 중이면 그 약속. 그 사이 누른 사용자 동작은 이것이 끝난 뒤에 한다. */
let quietRun: Promise<boolean> | null = null;

/**
 * 진단 요청 (팀 패널 action 과 같은 전이). 성공해서 새 상태를 받아들였으면 true.
 * - validate: 핸들이 없으면 input 으로 초안을 만들고(영수증 키 새로) 바로 범위 확인. 있으면 지금 초안을 다시 확인.
 * - start: 확인 상자가 켜진 ready 진단만. 시작 키를 먼저 저장하고 한 번만 보낸다.
 * - stop · refresh: 같은 진단에. 실패하면 결과 불명으로 두고 자동 재전송하지 않는다.
 * - opts.quiet(refresh 만): 주기 조회용. 화면의 busy 를 켜지 않는다 — 켜면 조회하는 동안 취소 버튼(canStop)이 사라졌다
 *   나타나고 옆 일시정지 버튼이 밀려 2초마다 깜빡인다. 겹침은 lock 이 그대로 막고, 그 사이 누른 동작은 조회가 끝난 뒤
 *   그 결과로 조건을 다시 본 다음 한다. 단 누른 순간의 결속(소유자 · 진단 · 핸들 · 세대)이 기다리는 사이 바뀌었으면
 *   (재연결 · 로그아웃 · 진단 교체) 그 동작은 버린다 — 다른 진단으로 보내지 않는다 (리뷰 #601).
 */
export async function runUrlAction(
  kind: UrlAction,
  input?: URLDiagnosisInput,
  opts: { quiet?: boolean } = {},
): Promise<boolean> {
  if (kind === "refresh" && opts.quiet === true) {
    if (quietRun !== null) return quietRun;
    const run = runUrlActionNow(kind, input, true);
    quietRun = run;
    try {
      return await run;
    } finally {
      if (quietRun === run) quietRun = null;
    }
  }
  if (quietRun !== null) {
    const pressed = actionBinding();
    await quietRun.catch(() => false);
    if (!sameBinding(pressed, actionBinding())) return false;
  }
  return runUrlActionNow(kind, input, false);
}

/** 사용자 동작이 어느 진단에 대한 것인지: 소유자 · 핸들(영수증 키) · 진단 ID · 세대. */
interface ActionBinding {
  owner: string | null;
  creationKey: string | null;
  jobID: string | null;
  generation: number;
}
function actionBinding(): ActionBinding {
  const st = useUrl.getState();
  return {
    owner: ownerOf(st.cap),
    creationKey: st.handle?.creationKey ?? null,
    jobID: st.job?.id ?? st.handle?.id ?? null,
    generation,
  };
}
function sameBinding(a: ActionBinding, b: ActionBinding): boolean {
  return a.owner === b.owner && a.creationKey === b.creationKey && a.jobID === b.jobID && a.generation === b.generation;
}

async function runUrlActionNow(
  kind: UrlAction,
  input: URLDiagnosisInput | undefined,
  quiet: boolean,
): Promise<boolean> {
  const st = useUrl.getState();
  const owner = ownerOf(st.cap);
  if (!owner || lock || (kind !== "refresh" && st.uncertain)) return false;
  if (kind === "start" && (!urlGates(st).canStart || !st.confirmed)) return false;
  if (kind === "validate" && (st.handle?.kind === "recovered" || st.handle?.startKey || st.handle?.bindingLocked))
    return false;
  const job = st.job;
  const current = ++generation;
  lock = true;
  if (!quiet) useUrl.setState({ busy: true, writeBusy: kind !== "refresh", error: "", confirmed: false });
  try {
    let saved = st.handle;
    let next: URLDiagnosisJob;
    if (kind === "refresh") {
      if (!saved) return false;
      if (saved.id) next = await readURLStatus(saved.id, owner);
      else {
        if (saved.kind === "recovered") return false;
        next = await readURLReceipt(saved.creationKey, owner);
      }
    } else if (kind === "validate") {
      if (saved && !job) return false;
      if (!saved) {
        if (!input) return false;
        const checked = urlDiagnosisInput(
          input.target_url,
          input.profile_id,
          input.display_name ?? "",
          input.workflow_mode,
          input.excluded_paths ?? "",
          {
            exclusionsReviewed: input.exclusions_reviewed === true,
            backupConfirmed: input.backup_confirmed === true,
          },
        );
        const cap = st.cap;
        if (cap.kind !== "v2" || !cap.catalog.profiles.some((p) => p.id === checked.profile_id)) return false;
        saved = { kind: "created", ownerContext: owner, creationKey: crypto.randomUUID(), input: checked };
        storeURLHandle(saved);
        useUrl.setState({ handle: saved, createdHandle: saved });
        next = await createURLDraft(checked, saved.creationKey, owner);
        if (current !== generation) return false;
        accept(next, saved);
        saved = useUrl.getState().handle;
      } else {
        if (!job || !sameURLInput(job.input, saved.input)) return false;
        next = job;
      }
      if (!saved) return false;
      // 초안 · 범위 확인에는 모델 · 대상 호출이 없다. 시작은 따로 확인 상자 + 버튼으로만.
      next = await commandURLDiagnosis(next, "validate", owner);
    } else {
      if (!job || !saved) return false;
      if (kind === "start") {
        if (saved.kind === "recovered") return false;
        saved = { ...saved, startKey: crypto.randomUUID(), startRevision: job.revision };
        storeURLHandle(saved);
        useUrl.setState({ handle: saved, createdHandle: saved });
      }
      if (kind === "stop") {
        const fresh = await readURLStatus(job.id, owner);
        if (current !== generation) return false;
        matchURLJob(fresh, job);
        matchURLHandle(fresh, saved);
        accept(fresh, saved);
        if (!["preparing", "running"].includes(fresh.state)) {
          void loadUrlRuns(owner);
          return true;
        }
        next = await commandURLDiagnosis(fresh, "stop", owner);
      } else next = await commandURLDiagnosis(job, kind, owner, saved.startKey);
    }
    if (current !== generation || !saved) return false;
    if (job) {
      matchURLJob(next, job, {
        allowPreStartRebind: !saved.startKey && !saved.bindingLocked && (kind === "validate" || kind === "refresh"),
      });
    }
    accept(next, saved);
    if (kind !== "refresh") void loadUrlRuns(owner);
    // 바탕 조회는 시작 때 오류 글을 지우지 않으므로(지웠다 다시 뜨면 깜빡인다) 성공했을 때 지운다
    if (quiet) useUrl.setState({ error: "" });
    return true;
  } catch (cause) {
    if (current !== generation) return false;
    const patch: Partial<UrlStore> = { error: urlDiagnosisError(cause), uncertain: true, verified: false };
    if (kind === "refresh" || (cause instanceof ApiError && [401, 403].includes(cause.status))) patch.job = null;
    if (cause instanceof ApiError && cause.status === 403) {
      forgetSelectedCreation();
      patch.handle = null;
    }
    useUrl.setState(patch);
    return false;
  } finally {
    if (current === generation) {
      lock = false;
      if (!quiet) useUrl.setState({ busy: false, writeBusy: false });
    }
  }
}

/**
 * 조회 연결만 끊는다(다른 진단 만들기). 서버의 진단을 중지·삭제하지 않는다. 요청 중이면 아무것도 안 한다.
 * 새 점검 폼도 비운다(resetSeq) — 결과 불명인 진단을 같은 입력으로 겹쳐 만들지 않게.
 */
export function disconnectUrl(): void {
  const st = useUrl.getState();
  if (st.busy || lock) return;
  storeURLHandle(null);
  dropJob();
  useUrl.setState({ resetSeq: st.resetSeq + 1 });
}

/** 지금 서버 방식 (구독). */
export function useUrlCapability(): UrlCapability {
  return useUrl((st) => st.cap);
}

/** 이 서버가 URL 진단(v2)인가 (구독). */
export function useIsUrlMode(): boolean {
  return useUrl((st) => st.cap.kind === "v2");
}

/**
 * URL 진단 확인 · 주기 조회 실행자. Workbench 가 live 일 때 한 번 그린다(아무것도 그리지 않음).
 * 로그인이 끝나면 catalog 를 읽고, 진행 중 진단은 URL_POLL_MS 마다 상태만 다시 읽는다(문서가 숨겨지면 쉰다).
 */
export function UrlFeeds() {
  const ready = useLiveAuth().state === "ready";
  const job = useUrl((st) => st.job);
  const verified = useUrl((st) => st.verified);
  const uncertain = useUrl((st) => st.uncertain);
  const busy = useUrl((st) => st.busy);

  useEffect(
    () =>
      subscribeSessionChanged((signedIn) => {
        capRun++;
        dropJob();
        resetUrlRuns();
        useUrl.setState({ cap: { kind: "checking" } });
        if (signedIn && getLiveAuthState() === "ready") void checkCapability();
      }),
    [],
  );

  useEffect(() => {
    if (ready) {
      void checkCapability();
      return;
    }
    // 로그인 전·로그인이 끊긴 동안은 확인 중으로 둔다(새 점검 폼을 v1 로 잘못 보이지 않게)
    capRun += 1;
    dropJob();
    resetUrlRuns();
    useUrl.setState({ cap: { kind: "checking" } });
  }, [ready]);

  useEffect(() => {
    if (!job || !verified || uncertain || busy || !URL_ACTIVE_STATES.includes(job.state)) return;
    let timer: number | undefined;
    const tick = () => {
      if (document.visibilityState === "hidden") {
        timer = window.setTimeout(tick, URL_POLL_MS);
        return;
      }
      // 바탕 조회: busy 를 켜지 않아 머리의 취소 · 일시정지 버튼이 깜빡이지 않는다
      void runUrlAction("refresh", undefined, { quiet: true });
    };
    timer = window.setTimeout(tick, URL_POLL_MS);
    return () => window.clearTimeout(timer);
  }, [job, verified, uncertain, busy]);

  useEffect(() => {
    if (!ready || !job?.input.workflow_mode || !job.task_id) return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout>;
    const tick = async () => {
      if (!cancelled && document.visibilityState !== "hidden") await refreshWorkflow(job.id);
      if (!cancelled) timer = setTimeout(tick, URL_POLL_MS);
    };
    void tick();
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [ready, job?.id, job?.task_id, job?.input.workflow_mode]);

  // 점검 목록도 알아서 따라오게 RUN_LIST_POLL_MS 마다 첫 쪽을 다시 읽는다(탭이 보일 때만, 사용자 11/24: 대시보드
  // [새로고침] 없앰). 지금 점검 상태 · 승인 요청 · 탐색 흐름은 위에서 따로 읽는다
  const capOwner = useUrl((st) => (st.cap.kind === "v2" ? st.cap.catalog.ownerContext : null));
  useEffect(() => {
    if (!ready || !capOwner) return;
    const timer = window.setInterval(() => {
      if (document.visibilityState !== "hidden") refreshUrlRunsQuietly(capOwner);
    }, RUN_LIST_POLL_MS);
    return () => window.clearInterval(timer);
  }, [ready, capOwner]);

  // 데모로 바꾸면 비운다(다음 live 진입 때 다시 확인)
  useEffect(
    () => () => {
      capRun += 1;
      dropJob();
      resetUrlRuns();
      useUrl.setState({ cap: { kind: "unknown" } });
    },
    [],
  );
  return null;
}

/** 시작 전 상태인가 (초안 · 범위 확인). */
export function isUrlPrestart(job: Pick<URLDiagnosisJob, "state"> | null): boolean {
  return !!job && URL_PRESTART_STATES.includes(job.state);
}

export async function refreshWorkflow(expectedID?: string): Promise<void> {
  const st = useUrl.getState(),
    job = st.job,
    token = flowGeneration;
  if (!job?.input.workflow_mode || !job.task_id || flowLock || (expectedID && job.id !== expectedID)) return;
  try {
    const next = await readWorkflow(job);
    if (token !== flowGeneration || useUrl.getState().job?.id !== job.id) return;
    if (flowLock) return;
    const latest = useUrl.getState().flow;
    if (next && latest && next.revision < latest.revision) return;
    const pending = flowPending;
    const resolved =
      !!next && !!pending && (next.commands.some((c) => c.key === pending.key) || next.revision > pending.revision);
    if (resolved) flowPending = null;
    useUrl.setState({
      flow: next,
      flowVerified: !!next,
      ...(!pending || resolved ? { flowUncertain: false } : {}),
      flowError: "",
    });
  } catch {
    if (token === flowGeneration)
      useUrl.setState({ flowVerified: false, flowError: "탐색 상태를 확인할 수 없습니다. 다시 조회하세요." });
  }
}
export async function runWorkflowAction(
  operation: "selection" | "pause" | "resume",
  ids: string[] = [],
  replay = false,
): Promise<boolean> {
  const st = useUrl.getState(),
    job = st.job,
    current = st.flow;
  // 다시 연 점검(조회 결속)도 범위 선택을 보낼 수 있다(사용자 2026-11-22, F16). 서버가 진단 흐름 결속·주체를 다시 본다.
  if (
    flowLock ||
    (st.flowUncertain && !replay) ||
    !st.flowVerified ||
    !job ||
    !current ||
    st.uncertain ||
    !st.verified ||
    !["preparing", "running"].includes(job.state)
  )
    return false;
  flowLock = true;
  const token = flowGeneration;
  const previous = flowPending;
  if (replay && (!previous || previous.operation !== operation)) {
    flowLock = false;
    return false;
  }
  const pending =
    replay && previous ? previous : { key: crypto.randomUUID(), revision: current.revision, operation, ids: [...ids] };
  const key = pending.key;
  flowPending = pending;
  useUrl.setState({ flowBusy: true, flowError: "" });
  try {
    const next = await commandWorkflow(job, { ...current, revision: pending.revision }, operation, key, pending.ids);
    if (token !== flowGeneration) return false;
    if (!next.commands.some((c) => c.key === key)) throw new Error("unbound_command_receipt");
    flowPending = null;
    useUrl.setState({ flow: next, flowVerified: true, flowUncertain: false });
    return true;
  } catch (error) {
    if (token !== flowGeneration) return false;
    const known = error instanceof ApiError && [400, 401, 403, 404, 409].includes(error.status);
    if (known) flowPending = null;
    useUrl.setState({
      flowVerified: false,
      flowUncertain: !known,
      flowError: known
        ? "상태가 바뀌었거나 허용되지 않은 명령입니다. 다시 조회하세요."
        : "명령 결과가 불명입니다. 재전송하지 말고 상태를 다시 조회하세요.",
    });
    return false;
  } finally {
    if (token === flowGeneration) {
      flowLock = false;
      useUrl.setState({ flowBusy: false });
    }
  }
}

/** Explicit same-key replay only after a fresh GET; never an automatic POST retry. */
export async function retryWorkflowAction(): Promise<boolean> {
  const pending = flowPending;
  if (!pending || !useUrl.getState().flowUncertain || !useUrl.getState().flowVerified) return false;
  return runWorkflowAction(pending.operation, pending.ids, true);
}
