"use client";
import { useEffect, useRef, useState } from "react";

import { apiFetch } from "@/lib/diagnosis/api";
import type { URLDiagnosisProfile } from "@/lib/diagnosis/diagnosis-url";

import { cx } from "../../lib/cx";
import { HelpTip } from "../../ui/parts";
import s from "../../workbench.module.css";

// 시안 #nApi 의 'API 키' 칸. 등록돼 있으면 '등록됨' 딱지만 보이고, 없거나 [키 바꾸기]를 누를 때만 키 입력 · [키 등록].
// 등록 날짜는 서버가 주지 않아 그리지 않는다. 모델은 설치 프로필이 정한 값을 그대로 보낸다(화면에서 고치지 않음).
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
  const secret = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [saved, setSaved] = useState(false);
  const [changing, setChanging] = useState(false);
  const [checking, setChecking] = useState(false);
  const profileID = profile?.id;
  const expectedModel = profile?.model?.model;
  useEffect(() => {
    let cancelled = false;
    const version = ++generation.current;
    onReady(false);
    setSaved(false);
    setChanging(false);
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
        setMessage(configured ? "" : "API 키를 한 번 등록하세요.");
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
    if (!profile || !secret.current || disabled || busy) return;
    generation.current++;
    const apiKey = secret.current.value;
    secret.current.value = "";
    if (!expectedModel) {
      setMessage("운영자가 외부 모델 승인 · 제공자 · 비용 한도를 먼저 설치해야 합니다.");
      return;
    }
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
          body: JSON.stringify({ profile_id: profile.id, model: expectedModel, api_key: apiKey }),
        },
        false,
      );
      setMessage("서버에 키를 등록했습니다.");
      setSaved(true);
      setChanging(false);
      onReady(true);
    } catch {
      setMessage("키를 등록하지 못했습니다. 승인된 모델명과 운영자 설치 설정을 확인하세요.");
    } finally {
      setBusy(false);
    }
  }
  // 키 입력칸은 등록 전이거나 바꿀 때만
  const asking = !saved || changing;
  return (
    <fieldset disabled={disabled || busy} className={cx(s.field, s.keyset)}>
      <legend className={s["sr-only"]}>외부 모델 API 키</legend>
      <div className={s["fsec-ht"]}>
        {asking ? (
          <label className={s.lbl} htmlFor="externalModelKey">
            API 키
          </label>
        ) : (
          <span className={s.lbl}>API 키</span>
        )}
        <HelpTip label="API 키 설명">
          키는 서버에만 등록하고 키 값은 브라우저로 읽어오지 않습니다. 외부 전송 · USD 상한 동의는 다음 범위 확인
          화면에서 따로 합니다.
        </HelpTip>
      </div>
      {asking ? (
        <div className={cx(s.hostline, s.keyrow)}>
          <input
            ref={secret}
            id="externalModelKey"
            className={s.inp}
            type="password"
            autoComplete="off"
            maxLength={512}
            placeholder={saved ? "새 키" : ""}
            onChange={(e) => onReady(saved && e.target.value === "")}
          />
          <button
            className={cx(s.btn, s.sm)}
            type="button"
            onClick={() => void save()}
            disabled={!profile || disabled || busy}
          >
            {busy ? "등록 중…" : "키 등록"}
          </button>
        </div>
      ) : (
        <div className={s.hostline}>
          <span className={cx(s.pill, s.ok)}>등록됨</span>
          <button className={cx(s.btn, s.sm)} type="button" onClick={() => setChanging(true)}>
            키 바꾸기
          </button>
        </div>
      )}
      {!profile ? <p role="alert">운영자가 외부 모델 승인 · 제공자 · 비용 한도를 먼저 설치해야 합니다.</p> : null}
      <span role="status" className={s.help}>
        {checking ? "등록된 키 확인 중…" : message}
      </span>
    </fieldset>
  );
}
