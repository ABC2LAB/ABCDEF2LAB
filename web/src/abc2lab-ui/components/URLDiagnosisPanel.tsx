import { useCallback, useEffect, useRef, useState } from "react";

import { ApiError } from "../lib/api";
import {
  commandURLDiagnosis,
  createURLDraft,
  matchURLHandle,
  matchURLJob,
  readURLHandle,
  readURLReceipt,
  readURLStatus,
  sameURLInput,
  storeURLHandle,
  type URLDiagnosisCatalog,
  type URLDiagnosisHandle,
  type URLDiagnosisJob,
  urlDiagnosisError,
  urlDiagnosisInput,
  urlPhaseLabels,
  urlReasonLabels,
  urlStateLabels,
} from "../lib/diagnosis-url";
import { connectionError, verifyDiagnosisTask } from "../lib/first-diagnosis";
import { useSelection } from "../lib/selection";
import { useNav } from "../nav";

const authorityLabels = { not_issued: "미발급", issued: "발급됨", consumed: "소비됨", unknown: "불명" };
const pollMilliseconds = 2000;

/** v2의 준비 상태를 v1 실행 snapshot으로 추정하지 않는다. */
export function URLDiagnosisPanel({ catalog }: { catalog: URLDiagnosisCatalog }) {
  const [target, setTarget] = useState("");
  const [profileID, setProfileID] = useState(catalog.profiles.at(0)?.id ?? "");
  const [name, setName] = useState("");
  const [job, setJob] = useState<URLDiagnosisJob | null>(null);
  const [busy, setBusy] = useState(true);
  const [verified, setVerified] = useState(false);
  const [uncertain, setUncertain] = useState(false);
  const [confirmed, setConfirmed] = useState(false);
  const [error, setError] = useState("");
  const handle = useRef<URLDiagnosisHandle | null>(null);
  const generation = useRef(0);
  const lock = useRef(false);
  const { connectTask } = useSelection();
  const { go } = useNav();
  const owner = catalog.ownerContext;

  const accept = useCallback((next: URLDiagnosisJob, saved: URLDiagnosisHandle) => {
    matchURLHandle(next, saved);
    const updated: URLDiagnosisHandle = {
      ...saved,
      id: next.id,
      revision: next.revision,
      sourceDigest: next.source_digest,
      ...(saved.bindingLocked || saved.startKey || next.preparation_started ? { bindingLocked: true } : {}),
    };
    handle.current = updated;
    storeURLHandle(updated);
    setConfirmed(false);
    setJob(next);
    setTarget(next.input.target_url);
    setProfileID(next.input.profile_id);
    setName(next.input.display_name ?? "");
    setVerified(true);
    setUncertain(next.state === "unknown");
  }, []);

  // owner가 바뀌면 다른 주체의 핸들은 조회하지 않는다. 재열기/새로고침은 GET만 한다.
  useEffect(() => {
    const current = ++generation.current;
    const saved = readURLHandle(owner);
    handle.current = saved;
    lock.current = false;
    setBusy(!!saved);
    setVerified(false);
    setConfirmed(false);
    setJob(null);
    setUncertain(!!saved);
    setError("");
    if (saved) {
      void (saved.id ? readURLStatus(saved.id, owner) : readURLReceipt(saved.creationKey, owner))
        .then((next) => {
          if (current !== generation.current) return;
          accept(next, saved);
        })
        .catch((cause: unknown) => {
          if (current !== generation.current) return;
          setError(urlDiagnosisError(cause));
          if (cause instanceof ApiError && cause.status === 403) {
            handle.current = null;
            storeURLHandle(null);
          }
        })
        .finally(() => {
          if (current === generation.current) setBusy(false);
        });
    }
    return () => {
      generation.current += 1;
    };
  }, [owner, accept]);

  // 실제 서버 상태만 주기 조회한다. 실패/unknown 뒤에는 자동 재시작·재조회하지 않는다.
  useEffect(() => {
    if (!job || !verified || uncertain || busy || !["preparing", "running", "stopping"].includes(job.state)) return;
    const timer = window.setTimeout(() => {
      void action("refresh");
    }, pollMilliseconds);
    return () => window.clearTimeout(timer);
  });

  const profile = catalog.profiles.find((item) => item.id === profileID);
  const lockedInput = !!handle.current || busy;
  const startSent = !!handle.current?.startKey;
  const reportReadable = verified && job?.result_ready === true && !!job.task_id;
  const liveReadable =
    verified &&
    !uncertain &&
    job?.started === true &&
    !job.drain_complete &&
    (job.state === "running" || job.state === "stopping");
  const canStart =
    verified &&
    !uncertain &&
    !busy &&
    job?.state === "ready" &&
    !!job.scope &&
    !startSent &&
    !handle.current?.bindingLocked;
  let validInput = false;
  try {
    validInput = !!profile && !!urlDiagnosisInput(target, profileID, name);
  } catch {
    /* 잘못된 URL은 서버에 보내지 않는다. */
  }

  async function action(kind: "validate" | "start" | "stop" | "refresh") {
    if (lock.current || (kind !== "refresh" && uncertain)) return;
    if (kind === "start" && (!canStart || !confirmed)) return;
    if (kind === "validate" && (handle.current?.startKey || handle.current?.bindingLocked)) return;
    const current = ++generation.current;
    lock.current = true;
    setBusy(true);
    setError("");
    setConfirmed(false);
    try {
      let saved = handle.current;
      let next: URLDiagnosisJob;
      if (kind === "refresh") {
        if (!saved) return;
        next = saved.id ? await readURLStatus(saved.id, owner) : await readURLReceipt(saved.creationKey, owner);
      } else if (kind === "validate") {
        if (saved && !job) return;
        if (!saved) {
          const input = urlDiagnosisInput(target, profileID, name);
          if (!profile) return;
          saved = { ownerContext: owner, creationKey: crypto.randomUUID(), input };
          handle.current = saved;
          storeURLHandle(saved);
          next = await createURLDraft(input, saved.creationKey, owner);
          if (current !== generation.current) return;
          accept(next, saved);
          saved = handle.current;
        } else {
          if (!job || !sameURLInput(job.input, saved.input)) return;
          next = job;
        }
        if (!saved) return;
        // 초안/범위 확인에는 모델·대상 호출이 없다. 시작은 별도 사용자 클릭만 허용한다.
        next = await commandURLDiagnosis(next, "validate", owner);
      } else {
        if (!job || !saved) return;
        if (kind === "start") {
          saved = { ...saved, startKey: crypto.randomUUID(), startRevision: job.revision };
          handle.current = saved;
          storeURLHandle(saved);
        }
        next = await commandURLDiagnosis(job, kind, owner, kind === "start" ? saved.startKey : undefined);
      }
      if (current !== generation.current || !saved) return;
      if (job)
        matchURLJob(next, job, {
          allowPreStartRebind: !saved.startKey && !saved.bindingLocked && (kind === "validate" || kind === "refresh"),
        });
      accept(next, saved);
    } catch (cause) {
      if (current !== generation.current) return;
      setError(urlDiagnosisError(cause));
      setUncertain(true);
      setVerified(false);
      if (kind === "refresh" || (cause instanceof ApiError && [401, 403].includes(cause.status))) setJob(null);
      if (cause instanceof ApiError && cause.status === 403) {
        handle.current = null;
        storeURLHandle(null);
      }
    } finally {
      if (current === generation.current) {
        lock.current = false;
        setBusy(false);
      }
    }
  }

  function disconnect() {
    if (busy || lock.current) return;
    generation.current += 1;
    handle.current = null;
    storeURLHandle(null);
    setJob(null);
    setUncertain(false);
    setVerified(false);
    setConfirmed(false);
    setError("");
    setTarget("");
    setName("");
    setProfileID(catalog.profiles.at(0)?.id ?? "");
  }

  async function connect(source: "run" | "report") {
    if (!job?.task_id || lock.current || (source === "report" ? !reportReadable : !liveReadable)) return;
    const current = ++generation.current;
    const task = job.task_id;
    lock.current = true;
    setBusy(true);
    setError("");
    try {
      await verifyDiagnosisTask(task, source);
      if (current !== generation.current) return;
      connectTask?.(task, source);
      go(source === "report" ? "report" : "live");
    } catch (cause) {
      if (current === generation.current)
        setError(`실제 실행·보고서 API 연결을 확인하지 못했어요. ${connectionError(cause)}`);
    } finally {
      if (current === generation.current) {
        lock.current = false;
        setBusy(false);
      }
    }
  }

  return (
    <section className="card" aria-label="URL 기반 새 진단">
      <h2>URL로 새 진단</h2>
      <p className="hint">
        URL은 진단 요청이에요. 서버에 등록된 승인 범위를 확인한 뒤, 시작 버튼을 눌러야 실제 작업·모델 준비와 진단을
        시작해요.
      </p>
      <label className="fl" htmlFor="url-diagnosis-target">
        진단 대상 URL
      </label>
      <input
        className="inp"
        id="url-diagnosis-target"
        type="url"
        value={target}
        disabled={lockedInput}
        autoComplete="off"
        placeholder="https://승인된-대상/"
        maxLength={2048}
        onChange={(event) => {
          setTarget(event.target.value);
          setConfirmed(false);
        }}
      />
      <p className="hint">HTTPS 주소만 입력해 주세요. 계정·비밀번호, query, fragment는 입력하지 마세요.</p>
      <label className="fl" htmlFor="url-diagnosis-profile">
        승인된 모델 프로필
      </label>
      <select
        className="inp"
        id="url-diagnosis-profile"
        value={profileID}
        disabled={lockedInput}
        onChange={(event) => {
          setProfileID(event.target.value);
          setConfirmed(false);
        }}
      >
        <option value="">설치된 프로필 선택</option>
        {catalog.profiles.map((item) => (
          <option value={item.id} key={item.id}>
            {item.label || item.id} · {item.model_label || item.model_profile_ref}
          </option>
        ))}
      </select>
      <label className="fl" htmlFor="url-diagnosis-name">
        진단 이름 (선택)
      </label>
      <input
        className="inp"
        id="url-diagnosis-name"
        value={name}
        maxLength={160}
        disabled={lockedInput}
        autoComplete="off"
        onChange={(event) => {
          setName(event.target.value);
          setConfirmed(false);
        }}
      />
      {!catalog.profiles.length && <p className="hint">제공된 모델 프로필이 없어요. 설치자 설정이 필요해요.</p>}
      {job?.scope && (
        <dl aria-label="서버가 확인한 승인 범위">
          <dt>실제 대상 / 경로</dt>
          <dd>
            {job.scope.origin} · {job.scope.path_prefixes.join(", ")} (각 경로 자신과 하위 경로)
          </dd>
          <dt>메서드 / query / body</dt>
          <dd>
            {job.scope.methods.join(", ")} · 임의 query 허용 · body 최대 {job.scope.max_body_bytes} bytes
          </dd>
          <dt>승인 방식 / 만료</dt>
          <dd>
            {job.scope.approval_mode === "confirm_every_request" ? "매 요청 확인" : "사전 승인 범위"} ·{" "}
            {job.scope.expires_at}
          </dd>
          <dt>대상 요청 / 시간 상한</dt>
          <dd>
            {job.scope.budget.requests}회 · {job.scope.budget.duration_seconds}초
          </dd>
          <dt>모델 요청 / 토큰 / 비용 상한</dt>
          <dd>
            {job.scope.budget.model_requests}회 · 입력 {job.scope.budget.input_tokens} / 출력{" "}
            {job.scope.budget.output_tokens} · USD {(job.scope.budget.cost_usd_micros / 1_000_000).toFixed(6)}
          </dd>
        </dl>
      )}
      {error && <p role="alert">{error}</p>}
      {job && (
        <div role="status">
          <strong>{verified ? urlStateLabels[job.state] : "현재 상태 미확인"}</strong>
          <p>{urlPhaseLabels[job.phase]}</p>
          <p className="hint">
            진단 {job.id} · 수정 {job.revision}
            {job.task_id ? ` · task #${job.task_id}` : ""}
          </p>
          <p className="hint">설정 지문 {job.source_digest}</p>
          {job.reason_code && (
            <p>
              서버 사유: {urlReasonLabels[job.reason_code] ?? job.reason_code} ({job.reason_code})
            </p>
          )}
          <p>모델·작업 준비 시작: {job.preparation_started ? "확인됨" : "미확인"}</p>
          <p>실행 권위: {authorityLabels[job.authority_state]}</p>
          <p>엔진 시작: {job.started ? "확인됨" : "미확인"}</p>
          <p>마감 확인: {job.drain_complete ? "확인됨" : "미확인"}</p>
          <p>결과 저장: {job.result_ready ? "확인됨" : "미확인"}</p>
          {job.model_usage ? (
            <p>
              모델 사용: 요청 {job.model_usage.requests} · 입력 {job.model_usage.input_tokens} / 출력{" "}
              {job.model_usage.output_tokens} ·
              {job.model_usage.usage_known
                ? ` 확인된 비용 USD ${(job.model_usage.cost_usd_micros / 1_000_000).toFixed(6)}`
                : " 사용량·비용 불명"}
              {" · 예약 토큰 입력 " +
                job.model_usage.reserved_input_tokens +
                " / 출력 " +
                job.model_usage.reserved_output_tokens}
            </p>
          ) : (
            <p className="hint">모델 사용량·비용 기록은 아직 제공되지 않았어요.</p>
          )}
          <p className="hint">
            진행률·ETA를 추정하지 않아요. 실행 마감·HTTP 응답·후보 0건은 취약점 없음이나 확정을 뜻하지 않아요.
          </p>
        </div>
      )}
      <div className="row" style={{ gap: 8, flexWrap: "wrap" }}>
        <button
          className="btn"
          type="button"
          disabled={
            busy ||
            uncertain ||
            !validInput ||
            startSent ||
            !!handle.current?.bindingLocked ||
            !!job?.preparation_started ||
            (!!handle.current && !job)
          }
          onClick={() => void action("validate")}
        >
          승인 범위 확인
        </button>
        <button className="btn" type="button" disabled={busy || !handle.current} onClick={() => void action("refresh")}>
          진단 상태 다시 조회
        </button>
      </div>
      {job?.state === "ready" && (
        <>
          <label style={{ display: "block", marginTop: 16 }}>
            <input
              type="checkbox"
              checked={confirmed}
              disabled={!canStart}
              onChange={(event) => setConfirmed(event.target.checked)}
            />
            서버가 확인한 범위·모델·예산으로 준비와 단회 진단을 시작할게요.
          </label>
          <button
            className="btn primary"
            type="button"
            disabled={!canStart || !confirmed}
            onClick={() => void action("start")}
          >
            진단 시작
          </button>
        </>
      )}
      {job && ["preparing", "running"].includes(job.state) && (
        <button
          className="btn danger"
          type="button"
          disabled={busy || uncertain || !verified}
          onClick={() => void action("stop")}
        >
          진단 중지 요청
        </button>
      )}
      {(uncertain || ((startSent || handle.current?.bindingLocked) && job?.state === "ready")) && (
        <p className="hint">
          시작·모델 호출을 자동으로 재전송하지 않아요. 같은 진단의 상태를 다시 조회해 주세요. 미확인은 미실행을 뜻하지
          않아요.
        </p>
      )}
      {job?.task_id && (
        <div className="row" style={{ gap: 8, flexWrap: "wrap" }}>
          <button className="btn" type="button" disabled={busy || !liveReadable} onClick={() => void connect("run")}>
            실제 실행·요청 승인 보기
          </button>
          <button
            className="btn"
            type="button"
            disabled={busy || !reportReadable}
            onClick={() => void connect("report")}
          >
            저장 보고서 보기
          </button>
        </div>
      )}
      {(handle.current || uncertain) && (
        <div>
          <p className="hint">
            다른 진단 만들기는 이 화면의 조회 연결만 해제해요. 기존 진단을 중지하거나 삭제하지 않아요. 결과 불명인
            진단을 중복으로 만들지 않도록 주의하세요.
          </p>
          <button className="btn" type="button" disabled={busy} onClick={disconnect}>
            다른 진단 만들기 / 조회 연결 해제
          </button>
        </div>
      )}
    </section>
  );
}
