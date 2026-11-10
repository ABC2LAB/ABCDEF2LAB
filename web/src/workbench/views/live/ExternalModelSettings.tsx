"use client";
import { useEffect, useRef, useState } from "react";

import { apiFetch } from "@/lib/diagnosis/api";
import type { URLDiagnosisProfile } from "@/lib/diagnosis/diagnosis-url";

import s from "../../workbench.module.css";

// 키는 비제어 password 입력과 전송 본문에만 잠깐 존재한다. React 상태·저장소·진단 초안에는 넣지 않는다.
export function ExternalModelSettings({
  profile,
  disabled,
  onReady,
}: {
  profile: URLDiagnosisProfile | undefined;
  disabled: boolean;
  onReady: (ready: boolean) => void;
}) {
  const generation = useRef(0);
  const model = useRef<HTMLInputElement>(null);
  const secret = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [saved, setSaved] = useState(false);
  const [checking, setChecking] = useState(false);
  const profileID = profile?.id;
  const expectedModel = profile?.model?.model;
  useEffect(() => {
    let cancelled = false;
    const version = ++generation.current;
    onReady(false);
    setSaved(false);
    setMessage("");
    if (secret.current) secret.current.value = "";
    if (!profileID) return;
    setChecking(true);
    void apiFetch<{ credential?: { configured: boolean; model: string } }>(
      `/api/product/model-credentials?profile_id=${encodeURIComponent(profileID)}`,
      { cache: "no-store", redirect: "error" },
      false,
    )
      .then((r) => {
        if (cancelled || generation.current !== version) return;
        const configured = r?.credential?.configured === true && r.credential.model === expectedModel;
        setSaved(configured);
        onReady(configured && !secret.current?.value);
        setMessage(
          configured
            ? "등록된 API 키를 재사용합니다. 키 값은 브라우저로 읽어오지 않습니다."
            : "API 키를 한 번 등록하세요.",
        );
      })
      .catch(() => {
        if (!cancelled && generation.current === version)
          setMessage("등록 상태를 확인하지 못했습니다. 상태 조회를 다시 하거나 키를 직접 등록하세요.");
      })
      .finally(() => {
        if (!cancelled) setChecking(false);
      });
    return () => {
      cancelled = true;
    };
  }, [onReady, profileID, expectedModel]);
  async function save() {
    if (!profile || !secret.current || !model.current || disabled || busy) return;
    generation.current++;
    const apiKey = secret.current.value;
    secret.current.value = "";
    if (!apiKey) {
      setMessage("API 키를 입력하세요.");
      return;
    }
    setBusy(true);
    onReady(false);
    try {
      await apiFetch(
        "/api/product/model-credentials",
        {
          method: "POST",
          redirect: "error",
          body: JSON.stringify({ profile_id: profile.id, model: model.current.value.trim(), api_key: apiKey }),
        },
        false,
      );
      setMessage("서버에 키를 등록했습니다. 범위·비용 확인 후 진단을 시작하세요.");
      setSaved(true);
      onReady(true);
    } catch {
      setMessage("키를 등록하지 못했습니다. 승인된 모델명과 운영자 설치 설정을 확인하세요.");
    } finally {
      setBusy(false);
    }
  }
  return (
    <fieldset disabled={disabled || busy} className={s.field}>
      <legend>외부 LLM 연결 · OpenRouter</legend>
      <label className={s.lbl} htmlFor="externalModelName">
        모델명
      </label>
      <input
        ref={model}
        id="externalModelName"
        className={s.inp}
        defaultValue={profile?.model?.model ?? "qwen/qwen3.5-35b-a3b"}
        autoComplete="off"
        spellCheck={false}
        readOnly
        aria-label="모델명"
      />
      <label className={s.lbl} htmlFor="externalModelKey">
        API 키
      </label>
      <input
        ref={secret}
        id="externalModelKey"
        className={s.inp}
        type="password"
        autoComplete="off"
        maxLength={512}
        onChange={(e) => onReady(saved && e.target.value === "")}
      />
      <p className={s.help}>
        키는 인증된 서버 설정에만 등록합니다. 대상 응답의 외부 전송과 USD 상한은 다음 범위 확인 화면에서 별도로
        동의합니다.
      </p>
      {!profile ? <p role="alert">운영자가 외부 모델 승인·제공자·비용 한도를 먼저 설치해야 합니다.</p> : null}
      <button className={s.btn} type="button" onClick={() => void save()} disabled={!profile || disabled || busy}>
        {busy ? "등록 중…" : "모델 연결 설정 저장"}
      </button>
      <p role="status">{checking ? "등록된 키 상태 확인 중…" : message}</p>
    </fieldset>
  );
}
