import { useEffect, useRef, useState } from "react";

import { ApiError } from "../lib/api";
import {
  commandDiagnosis,
  createDiagnosisDraft,
  type DiagnosisDraft,
  type DiagnosisHandle,
  type DiagnosisProfile,
  draftStateLabel,
  managementError,
  readCreatedDiagnosis,
  readDiagnosisDraft,
  readDiagnosisHandle,
  readDiagnosisProfiles,
  sameDiagnosisConfiguration,
  storeDiagnosisHandle,
  updateDiagnosisDraft,
} from "../lib/diagnosis-management";
import { connectionError, verifyDiagnosisTask } from "../lib/first-diagnosis";
import { useSelection } from "../lib/selection";
import { useGo } from "../nav";

/** 화면은 공개 진단 관리 DTO만 사용하며 보호 파일·엔진 객체를 받지 않는다. */
export function NewDiagnosisPanel() {
  const [profiles, setProfiles] = useState<DiagnosisProfile[]>([]);
  const [selected, setSelected] = useState("");
  const [draft, setDraft] = useState<DiagnosisDraft | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(true);
  const [uncertain, setUncertain] = useState(false);
  const [confirmed, setConfirmed] = useState(false);
  const [verified, setVerified] = useState(false);
  const handle = useRef<DiagnosisHandle | null>(null);
  const current = useRef(0);
  const lock = useRef(false);
  const { connectTask } = useSelection();
  const go = useGo();
  useEffect(() => {
    const generation = ++current.current;
    handle.current = readDiagnosisHandle();
    const saved = handle.current;
    if (saved) setUncertain(true);
    void (async () => {
      try {
        let items: DiagnosisProfile[] = [];
        try {
          items = await readDiagnosisProfiles();
        } catch (cause) {
          if (!saved || (cause instanceof ApiError && (cause.status === 401 || cause.status === 403))) throw cause;
          // 목록 장애는 기존 실행의 읽기·중지 제어를 막지 않는다.
          if (generation === current.current) setError(managementError(cause));
        }
        if (generation !== current.current) return;
        setProfiles(items);
        setSelected(items[0]?.id ?? "");
        if (saved) {
          const value = saved.id ? await readDiagnosisDraft(saved.id) : await readCreatedDiagnosis(saved.creationKey);
          if (generation !== current.current) return;
          handle.current = { ...saved, id: value.id };
          storeDiagnosisHandle(handle.current);
          setDraft(value);
          setVerified(true);
          setUncertain(value.state === "unknown");
          setSelected(
            items.find((item) => sameDiagnosisConfiguration(item.configuration, value.configuration))?.id ?? "",
          );
        }
      } catch (cause) {
        if (generation !== current.current) return;
        setError(managementError(cause));
        setDraft(null);
        setVerified(false);
        if (cause instanceof ApiError && (cause.status === 401 || cause.status === 403)) {
          handle.current = null;
          storeDiagnosisHandle(null);
        }
      } finally {
        if (generation === current.current) setBusy(false);
      }
    })();
    return () => {
      current.current += 1;
    };
  }, []);
  const profile = profiles.find((item) => item.id === selected);
  const dirty = !!draft && (!profile || !sameDiagnosisConfiguration(profile.configuration, draft.configuration));
  const editable = !draft || ["draft", "awaiting_approval", "prepared"].includes(draft.state);
  const shownConfiguration = draft?.configuration ?? profile?.configuration;

  async function action(kind: "save" | "validate" | "start" | "stop" | "refresh") {
    if (lock.current || (kind !== "refresh" && uncertain) || (dirty && (kind === "validate" || kind === "start")))
      return;
    lock.current = true;
    const generation = current.current;
    setBusy(true);
    setError("");
    setConfirmed(false);
    try {
      let next: DiagnosisDraft;
      if (kind === "refresh") {
        if (handle.current?.id) next = await readDiagnosisDraft(handle.current.id);
        else if (handle.current) next = await readCreatedDiagnosis(handle.current.creationKey);
        else return;
      } else if (kind === "save") {
        if (!profile) return;
        if (draft) next = await updateDiagnosisDraft(draft, profile.configuration);
        else {
          handle.current ??= { creationKey: crypto.randomUUID() };
          storeDiagnosisHandle(handle.current);
          next = await createDiagnosisDraft(profile.configuration, handle.current.creationKey);
        }
      } else {
        if (!draft) return;
        if (kind === "start") {
          if (!confirmed || draft.state !== "prepared") return;
          handle.current ??= { id: draft.id, creationKey: crypto.randomUUID() };
          handle.current.startKey ??= crypto.randomUUID();
          storeDiagnosisHandle(handle.current);
        }
        next = await commandDiagnosis(draft, kind, kind === "start" ? handle.current?.startKey : undefined);
      }
      if (generation !== current.current) return;
      handle.current = {
        creationKey: handle.current?.creationKey ?? crypto.randomUUID(),
        id: next.id,
        ...(kind !== "save" && handle.current?.startKey ? { startKey: handle.current.startKey } : {}),
      };
      storeDiagnosisHandle(handle.current);
      setDraft(next);
      setVerified(true);
      setUncertain(next.state === "unknown");
      if (kind === "refresh")
        setSelected(
          profiles.find((item) => sameDiagnosisConfiguration(item.configuration, next.configuration))?.id ?? "",
        );
    } catch (cause) {
      if (generation !== current.current) return;
      setError(managementError(cause));
      setUncertain(true);
      setVerified(false);
      // 실패한 재조회 뒤 이전 prepared/running 결과로 제어 권한을 복원하지 않는다.
      if (kind === "refresh" || (cause instanceof ApiError && (cause.status === 401 || cause.status === 403)))
        setDraft(null);
      if (cause instanceof ApiError && (cause.status === 401 || cause.status === 403)) {
        handle.current = null;
        storeDiagnosisHandle(null);
      }
    } finally {
      lock.current = false;
      if (generation === current.current) setBusy(false);
    }
  }

  const reportReadable = verified && draft?.drain_complete === true && draft.result_ready === true;
  function disconnect() {
    if (busy || lock.current) return;
    current.current += 1;
    handle.current = null;
    storeDiagnosisHandle(null);
    setDraft(null);
    setVerified(false);
    setUncertain(false);
    setConfirmed(false);
    setError("");
    setSelected(profiles[0]?.id ?? "");
  }

  async function connect() {
    if (!draft?.task_id || (uncertain && !reportReadable) || lock.current) return;
    const generation = current.current;
    const id = draft.task_id;
    const source = reportReadable || ["completed", "cancelled", "failed"].includes(draft.state) ? "report" : "run";
    lock.current = true;
    setBusy(true);
    setError("");
    try {
      await verifyDiagnosisTask(id, source);
      if (generation !== current.current) return;
      connectTask?.(id, source);
      go(source === "report" ? "report" : "live");
    } catch (cause) {
      if (generation === current.current)
        setError(`관리 API와 실행·보고서 API 연결을 확인하지 못했어요. ${connectionError(cause)}`);
    } finally {
      lock.current = false;
      if (generation === current.current) setBusy(false);
    }
  }

  return (
    <section className="card" aria-label="새 진단 초안">
      <h2>새 진단 초안</h2>
      <p className="hint">
        초안 저장은 모델이나 대상 요청을 시작하지 않아요. 운영자가 제공한 대상·범위·모델·예산 참조만 선택할 수 있어요.
        초안은 관리 서비스 메모리에 보관되어 서비스 재시작 시 사라져요.
      </p>
      <label className="fl" htmlFor="diagnosis-profile">
        운영자 제공 설정
      </label>
      <select
        className="inp"
        id="diagnosis-profile"
        value={selected}
        disabled={busy || uncertain || !editable}
        onChange={(event) => {
          setSelected(event.target.value);
          setConfirmed(false);
        }}
      >
        <option value="">승인 가능한 설정 선택</option>
        {profiles.map((item) => (
          <option key={item.id} value={item.id}>
            {item.label || item.id}
          </option>
        ))}
      </select>
      {shownConfiguration && (
        <dl aria-label={draft ? "서버에 저장된 설정" : "선택한 설정"}>
          <dt>대상 / 범위</dt>
          <dd>
            {shownConfiguration.target_ref} / {shownConfiguration.scope_ref}
          </dd>
          <dt>모델 프로필</dt>
          <dd>{shownConfiguration.model_profile_ref}</dd>
          <dt>승인 방식</dt>
          <dd>{shownConfiguration.approval_mode === "confirm_every_request" ? "매 요청 확인" : "사전 승인 범위"}</dd>
          <dt>예산 상한</dt>
          <dd>
            요청 {shownConfiguration.budget.requests} · 토큰 {shownConfiguration.budget.tokens} · 비용 {"$"}
            {(shownConfiguration.budget.cost_usd_micros / 1_000_000).toFixed(4)} ·{" "}
            {shownConfiguration.budget.duration_seconds}초
          </dd>
        </dl>
      )}
      {dirty && (
        <p className="hint">
          선택한 설정은 아직 저장되지 않았어요. 아래 상태와 시작 승인은 서버에 저장된 설정 기준이에요.
        </p>
      )}
      {error && <p role="alert">{error}</p>}
      {!busy && profiles.length === 0 && <p className="hint">제공된 설정이 없어요. 운영자 준비가 필요해요.</p>}
      {draft && (
        <div role="status">
          <strong>{draftStateLabel[draft.state]}</strong>
          <p className="hint">
            초안 {draft.id} · 수정 {draft.revision}
            <br />
            설정 지문 {draft.source_digest}
          </p>
          {draft.state === "awaiting_approval" && (
            <p>운영자가 이 초안 ID·수정 번호·설정 지문을 실제 task와 단회 권위에 결속해야 해요.</p>
          )}
          {draft.state === "completed" && <p>실행 마감은 취약점 없음이나 목표 달성을 뜻하지 않아요.</p>}
        </div>
      )}
      <div className="row" style={{ gap: 8, flexWrap: "wrap" }}>
        <button
          className="btn"
          type="button"
          disabled={busy || uncertain || !profile || !editable}
          onClick={() => void action("save")}
        >
          {draft ? "초안 설정 저장" : "실행 없이 초안 생성"}
        </button>
        <button
          className="btn"
          type="button"
          disabled={busy || uncertain || dirty || !draft || !editable}
          onClick={() => void action("validate")}
        >
          실행 전 검증
        </button>
        <button className="btn" type="button" disabled={busy || !handle.current} onClick={() => void action("refresh")}>
          초안·실행 상태 다시 조회
        </button>
      </div>
      {draft?.state === "prepared" && !uncertain && !dirty && (
        <>
          <label style={{ display: "block", marginTop: 16 }}>
            <input
              type="checkbox"
              checked={confirmed}
              disabled={busy}
              onChange={(event) => setConfirmed(event.target.checked)}
            />
            저장된 설정·예산과 운영자 승인 범위의 단회 시작을 확인했어요.
          </label>
          <button
            className="btn primary"
            type="button"
            disabled={busy || !confirmed}
            onClick={() => void action("start")}
          >
            승인된 진단 시작
          </button>
        </>
      )}
      {draft?.state === "running" && (
        <button className="btn danger" type="button" disabled={busy || uncertain} onClick={() => void action("stop")}>
          신규 전송 중지 요청
        </button>
      )}
      {uncertain && <p className="hint">요청 결과를 다시 확인하기 전에는 시작·설정 변경·중지를 다시 보내지 않아요.</p>}
      {(handle.current || draft || uncertain) && (
        <div>
          <p className="hint">
            조회 연결 해제는 이 화면의 핸들만 지워요. 기존 실행을 중지하거나 초안을 삭제하지 않아요. 실행 여부가
            불명이라면 중복 진단에 주의하세요.
          </p>
          <button className="btn" type="button" disabled={busy} onClick={disconnect}>
            다른 초안 만들기 / 조회 연결 해제
          </button>
        </div>
      )}
      {draft?.task_id && (
        <button
          className="btn"
          type="button"
          disabled={busy || (uncertain && !reportReadable)}
          onClick={() => void connect()}
        >
          실제 task 상태·보고서 연결
        </button>
      )}
    </section>
  );
}
