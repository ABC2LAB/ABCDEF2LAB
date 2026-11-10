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
  const model = useRef<HTMLInputElement>(null);
  const secret = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  useEffect(() => {
    onReady(false);
    setMessage("");
    if (secret.current) secret.current.value = "";
  }, [onReady, profile?.id]);
  async function save() {
    if (!profile || !secret.current || !model.current || disabled || busy) return;
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
        onChange={() => onReady(false)}
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
        onChange={() => onReady(false)}
      />
      <p className={s.help}>
        키는 인증된 서버 설정에만 등록합니다. 대상 응답의 외부 전송과 USD 상한은 다음 범위 확인 화면에서 별도로
        동의합니다.
      </p>
      {!profile ? <p role="alert">운영자가 외부 모델 승인·제공자·비용 한도를 먼저 설치해야 합니다.</p> : null}
      <button className={s.btn} type="button" onClick={() => void save()} disabled={!profile || disabled || busy}>
        {busy ? "등록 중…" : "모델 연결 설정 저장"}
      </button>
      <p role="status">{message}</p>
    </fieldset>
  );
}
