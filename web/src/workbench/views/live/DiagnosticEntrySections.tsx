"use client";

import { useEffect, useRef, useState } from "react";

import Image from "next/image";

import { ApiError, apiFetchBlob } from "@/lib/diagnosis/api";
import {
  DIAGNOSTIC_CAPTURE_LIMITS,
  generateRemediation,
  type RemediationEntry,
  type ReproductionStep,
  readCopyPrompt,
} from "@/lib/diagnosis/remediation";

import { outcomeLabel } from "../../live/liveReport";
import { maskSecrets } from "../../live/mask";
import { Isec } from "../../ui/parts";
import s from "../../workbench.module.css";

const captureStatusLabels: Record<string, string> = {
  captured: "확보",
  not_captured: "미확보",
  not_supported: "캡처 미지원 또는 승인 없음",
  failed: "캡처 실패",
  storage_unavailable: "캡처 저장소 사용 불가",
  shared_omitted: "공유용에서 생략",
  capacity_omitted: "용량 초과로 생략",
  binding_invalid: "현재 결속·이미지 검증 실패",
};

function StoredScreenshot({ task, entry, step }: { task: string; entry: string; step: ReproductionStep }) {
  const [url, setUrl] = useState("");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const control = useRef<AbortController | null>(null);
  const objectURL = useRef("");
  useEffect(
    () => () => {
      control.current?.abort();
      if (objectURL.current) URL.revokeObjectURL(objectURL.current);
    },
    [],
  );
  async function load() {
    if (busy || url || !step.capture.digest) return;
    const controller = new AbortController();
    control.current = controller;
    setBusy(true);
    setMessage("");
    try {
      const q = new URLSearchParams({ task, audience: "internal", entry, step: step.id, digest: step.capture.digest });
      const blob = await apiFetchBlob(
        `/api/report/diagnostic/image?${q}`,
        {
          signal: controller.signal,
          cache: "no-store",
          redirect: "error",
        },
        "image/png",
      );
      if (blob.type !== "image/png" || blob.size > DIAGNOSTIC_CAPTURE_LIMITS.pngBytes) throw Error("invalid_image");
      const bytes = await blob.arrayBuffer();
      const digest = await crypto.subtle.digest("SHA-256", bytes);
      const hex = Array.from(new Uint8Array(digest), (n) => n.toString(16).padStart(2, "0")).join("");
      if (hex !== step.capture.digest) throw Error("image_digest_mismatch");
      if (controller.signal.aborted) return;
      objectURL.current = URL.createObjectURL(blob);
      setUrl(objectURL.current);
    } catch (error) {
      if (!controller.signal.aborted)
        setMessage(
          error instanceof ApiError && (error.status === 404 || error.status === 409)
            ? "현재 회차에서 이미지를 찾지 못했습니다. 현재 증거 버전을 다시 조회하세요."
            : "현재 증거·권한·이미지 무결성을 확인하지 못했습니다. 이미지 사용을 중지했습니다.",
        );
    } finally {
      if (!controller.signal.aborted) setBusy(false);
    }
  }
  if (step.capture.status !== "captured")
    return (
      <p className={s.help}>
        스크린샷 미확보 — 실제 요청·응답 및 실행 기록으로 증거 제공 · 상태{" "}
        {captureStatusLabels[step.capture.status] ?? "상태 미확인"}
      </p>
    );
  return (
    <figure>
      {url ? (
        <Image
          unoptimized
          width={step.capture.width ?? 1}
          height={step.capture.height ?? 1}
          src={url}
          alt={`Step ${step.number} 실행 당시의 비민감 영역 캡처`}
          style={{ maxWidth: "100%", height: "auto" }}
        />
      ) : (
        <button className={s.btn} type="button" onClick={() => void load()} disabled={busy}>
          {busy ? "이미지 검증 중…" : "검증된 증적 이미지 보기"}
        </button>
      )}
      <figcaption className={s.help}>
        {step.capture.captured_at} · SHA256 {step.capture.digest}
      </figcaption>
      {message ? <p role="alert">{message}</p> : null}
    </figure>
  );
}

// 판정·재현·조치·프롬프트는 같은 저장 항목만 사용한다. 이 화면은 대상이나 모델을 호출하지 않는다.
export function DiagnosticEntrySections({
  task,
  entry,
  onReload,
}: {
  task: string;
  entry: RemediationEntry;
  onReload: () => void;
}) {
  const [busy, setBusy] = useState(false),
    [show, setShow] = useState(false),
    [message, setMessage] = useState("");
  const control = useRef<AbortController | null>(null);
  useEffect(() => () => control.current?.abort(), []);
  const record = entry.record;
  const bundle = record && !record.stale ? record.bundle : undefined;
  async function generate() {
    if (busy || !entry.available) return;
    const controller = new AbortController();
    control.current = controller;
    setBusy(true);
    setMessage("");
    try {
      await generateRemediation(task, entry, [], controller.signal);
      if (!controller.signal.aborted) {
        setMessage("현재 저장 근거에 결속한 보안 조치·AI 프롬프트를 저장했습니다.");
        onReload();
      }
    } catch {
      if (!controller.signal.aborted)
        setMessage("현재 증거 버전을 확인하지 못해 저장하지 않았습니다. 다시 조회하세요.");
    } finally {
      if (!controller.signal.aborted) setBusy(false);
    }
  }
  async function copy() {
    if (busy || !bundle || !record) return;
    const controller = new AbortController();
    control.current = controller;
    setBusy(true);
    setMessage("");
    try {
      const reply = await readCopyPrompt(task, entry, controller.signal);
      if (controller.signal.aborted) return;
      if (
        reply.entry_id !== entry.entry_id ||
        reply.record_id !== record.id ||
        reply.input_digest !== bundle.input_digest ||
        reply.ai_prompt !== bundle.ai_prompt
      )
        throw Error("prompt_mismatch");
      // Copy the exact stored prompt only when the displayed masking projection is identical.
      // The server rebuilds from normalized DLP context; this guard also rejects a compromised response.
      if (maskSecrets(reply.ai_prompt) !== reply.ai_prompt) throw Error("prompt_contains_sensitive_value");
      await navigator.clipboard.writeText(reply.ai_prompt);
      if (!controller.signal.aborted) setMessage("같은 저장 프롬프트를 복사했습니다.");
    } catch {
      if (!controller.signal.aborted)
        setMessage("프롬프트가 변경됐거나 복사 권한을 확인하지 못했습니다. 현재 버전을 다시 조회하세요.");
    } finally {
      if (!controller.signal.aborted) setBusy(false);
    }
  }
  const groups = bundle
    ? ([
        ["수정할 보안 조건", bundle.security_condition],
        ["즉시 완화책", bundle.immediate_mitigation],
        ["근본 수정", bundle.root_fix],
        ["서버·배포 보안 설정", bundle.configuration],
        ["기술에 맞는 코드 조치", bundle.code_example],
        ["정상 기능·적용 전제", bundle.assumptions],
        ["수정 후 재검증", bundle.verification],
        ["남은 위험·검토", bundle.residual_risk],
      ] as const)
    : [];
  return (
    <>
      <Isec>
        <h3>① 판정과 영향</h3>
        <p>
          {outcomeLabel(entry.outcome)} · {entry.verification}
        </p>
        <p>
          유형 {maskSecrets(entry.vuln_class ?? "미분류")} · 위험도{" "}
          {entry.outcome === "confirmed" ? maskSecrets(entry.severity ?? "미평가") : "확정 집계 제외"}
        </p>
        <p>{maskSecrets(entry.summary)}</p>
        {entry.report ? (
          <details open>
            <summary>저장된 상세 보고</summary>
            <pre className={s.code}>{maskSecrets(entry.report)}</pre>
          </details>
        ) : null}
        {entry.truncated ? <p role="status">실제 도구 출력이 잘렸습니다. 완전한 재현 증거가 아닙니다.</p> : null}
        <p>
          실행 {entry.run} · 증거 버전 {entry.evidence_version}
        </p>
        <p className={s.help}>
          HTTP 성공·AI 주장·정상 종료는 취약점 확정이 아닙니다. 판정은 현재 저장된 E③ 근거만 따릅니다.
        </p>
      </Isec>
      <Isec>
        <h3>② 재현 절차 및 스크린샷</h3>
        {entry.source_load_failed ? (
          <p role="alert">근거 불러오기 실패. 현재 결속을 확인할 수 없어 재현 단계를 제공하지 않습니다.</p>
        ) : null}
        {entry.steps_truncated ? <p role="status">실행 관측이 잘렸습니다. 전체 재현 절차로 해석하지 마세요.</p> : null}
        {entry.steps?.length ? (
          entry.steps.map((step) => (
            <section key={step.id}>
              <h4>
                Step {step.number}. {maskSecrets(step.title)}
              </h4>
              <p>
                {step.method} {maskSecrets(step.path) || "비민감 경로 미기록"}
              </p>
              <p>사전 조건: {maskSecrets(step.prerequisites)}</p>
              <p>{maskSecrets(step.observation)}</p>
              <p className={s.help}>
                {step.observed_at} · {step.run_ref} · 호출 {step.call_ref || "미기록"} · 근거 {step.evidence_ref}
              </p>
              {step.truncated ? <p>이 단계의 관측이 잘렸습니다.</p> : null}
              <StoredScreenshot
                key={`${task}:${entry.entry_id}:${step.id}:${step.capture.digest ?? step.capture.status}`}
                task={task}
                entry={entry.entry_id}
                step={step}
              />
            </section>
          ))
        ) : (
          <p className={s.help}>구조화된 실제 재현 단계 미확보. 실행하지 않은 절차를 생성하지 않습니다.</p>
        )}
        <details>
          <summary>저장된 요청·응답 및 실행 기록</summary>
          <pre className={s.code}>{maskSecrets(entry.evidence) || "저장된 증거 본문 없음"}</pre>
          <p className={s.help}>
            증거 digest {entry.evidence_digest} · {entry.observed_at}
          </p>
        </details>
      </Isec>
      <Isec>
        <h3>③ 보안 조치</h3>
        <p className={s["lv-pre"]}>{maskSecrets(entry.remediation) || "저장된 수정 권고 없음"}</p>
        {groups.map(([title, items]) => (
          <div key={title}>
            <h4>{title}</h4>
            <ul>
              {Array.from(new Set(items)).map((v) => (
                <li key={v}>{maskSecrets(v)}</li>
              ))}
            </ul>
          </div>
        ))}
        {bundle ? (
          <p className={s.help}>
            관측 기술은 저장소·배포 컴포넌트에서 다시 확인하세요. 미확인 프레임워크·파일 경로는 생성하지 않습니다.
          </p>
        ) : (
          <p className={s.help}>유효한 저장 조치가 없습니다. 현재 근거를 확인해 명시적으로 생성하세요.</p>
        )}
      </Isec>
      <Isec>
        <h3>④ AI 수정 작업용 프롬프트</h3>
        {record?.stale ? (
          <p role="alert">근거·조치·기술 또는 생성기 버전 변경: 오래된 프롬프트 사용을 차단했습니다.</p>
        ) : null}
        {bundle ? (
          <>
            <p className={s.help}>
              조치 {bundle.remediation_version} · 증거 {bundle.evidence_version} · 입력 {bundle.input_digest}
            </p>
            {bundle.missing_context_fields?.length ? (
              <p>보완할 맥락: {bundle.missing_context_fields.join(", ")}</p>
            ) : null}
            <button className={s.btn} type="button" onClick={() => setShow((v) => !v)} aria-expanded={show}>
              {show ? "전체 프롬프트 접기" : "전체 프롬프트 보기"}
            </button>
            <button className={s.btn} type="button" disabled={busy} onClick={() => void copy()}>
              AI 수정 프롬프트 복사
            </button>
            {show ? <pre className={s.code}>{maskSecrets(bundle.ai_prompt)}</pre> : null}
          </>
        ) : null}
        <button className={s.btn} type="button" disabled={busy || !entry.available} onClick={() => void generate()}>
          {busy ? "저장 근거 확인 중…" : "보안 조치·AI 프롬프트 생성·저장"}
        </button>
        {!entry.available ? <p className={s.help}>{maskSecrets(entry.unavailable_reason)}</p> : null}
        <p className={s.help}>
          프롬프트는 고객사의 실제 저장소와 원인 코드를 확인한 뒤 최소 수정·정상/보안 회귀 시험을 수행하도록 지시합니다.
          이 보고서가 고객 코드 수정·운영 재현·배포 권한을 부여하지 않습니다.
        </p>
        {message ? <p role="status">{message}</p> : null}
      </Isec>
    </>
  );
}
