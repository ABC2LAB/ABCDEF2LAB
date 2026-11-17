"use client";

import { Fragment, useEffect, useRef, useState } from "react";

import Image from "next/image";

import { ApiError, apiFetchBlob } from "@/lib/diagnosis/api";
import {
  DIAGNOSTIC_CAPTURE_LIMITS,
  generateRemediation,
  type RemediationEntry,
  type ReproductionStep,
  readCopyPrompt,
} from "@/lib/diagnosis/remediation";

import { cx } from "../../lib/cx";
import { outcomeLabel } from "../../live/liveReport";
import { maskSecrets } from "../../live/mask";
import { Markdown } from "../../ui/Markdown";
import { Isec } from "../../ui/parts";
import s from "../../workbench.module.css";

const captureStatusLabels: Record<ReproductionStep["capture"]["status"], string> = {
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
  layout = "panel",
}: {
  task: string;
  entry: RemediationEntry;
  onReload: () => void;
  /**
   * panel = 오른쪽 발견 상세(① 판정과 영향 · ② 재현 절차 · ③ 보안 조치 · ④ AI 프롬프트).
   * page = 상세 보기 페이지, ARTEX 발견 상세 순서와 이름(요약 · 증거 / PoC · 상세 보고서 · 보안 조치 · AI 프롬프트, 사용자 11/17)
   */
  layout?: "panel" | "page";
}) {
  const [busy, setBusy] = useState(false),
    [show, setShow] = useState(false),
    [message, setMessage] = useState(""),
    [reportMsg, setReportMsg] = useState("");
  const reportBox = useRef<HTMLDivElement>(null);
  // 상세 보고 복사: 원문 Markdown 이 아니라 화면에 그려진(파싱 뒤 가린) 글을 복사한다 — 원문을 복사하면 붙여 넣은 곳의
  // Markdown 이 엔티티(&#112;…)를 다시 풀어 비밀이 되살아날 수 있다(#677 리뷰). 한 번 더 가린다
  async function copyReport() {
    const box = reportBox.current;
    const shown = box ? box.innerText || box.textContent || "" : "";
    try {
      await navigator.clipboard.writeText(maskSecrets(shown));
      setReportMsg("상세 보고를 복사했습니다.");
    } catch (error) {
      console.warn("상세 보고 복사 실패", error);
      setReportMsg("복사 권한을 확인하지 못했습니다.");
    }
  }
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
    } catch (error) {
      if (!controller.signal.aborted)
        setMessage(
          error instanceof Error && error.message === "prompt_contains_sensitive_value"
            ? "민감값으로 보이는 내용이 있어 복사를 막았습니다. 저장 근거와 프롬프트를 검토하세요."
            : "프롬프트가 변경됐거나 복사 권한을 확인하지 못했습니다. 현재 버전을 다시 조회하세요.",
        );
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
  // 두 짜임이 같이 쓰는 조각
  const truncNote = entry.truncated ? (
    <p role="status">실제 도구 출력이 잘렸습니다. 완전한 재현 증거가 아닙니다.</p>
  ) : null;
  const e3Help = (
    <p className={s.help}>
      HTTP 성공·AI 주장·정상 종료는 취약점 확정이 아닙니다. 판정은 현재 저장된 E③ 근거만 따릅니다.
    </p>
  );
  const steps = (
    <>
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
            {step.prerequisites ? <p>사전 조건: {maskSecrets(step.prerequisites)}</p> : null}
            <p>{maskSecrets(step.observation)}</p>
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
    </>
  );
  const evidenceText = maskSecrets(entry.evidence) || "저장된 증거 본문 없음";
  const reportCopy = (
    <button className={cx(s.btn, s.sm)} type="button" onClick={() => void copyReport()}>
      복사
    </button>
  );
  const remediationBody = (
    <>
      <p className={s["lv-pre"]}>{maskSecrets(entry.remediation) || "저장된 수정 권고 없음"}</p>
      {bundle ? (
        <details>
          <summary>{`자세한 조치 (${groups.filter(([, items]) => items.length).length}묶음)`}</summary>
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
          <p className={s.help}>
            관측 기술은 저장소·배포 컴포넌트에서 다시 확인하세요. 미확인 프레임워크·파일 경로는 생성하지 않습니다.
          </p>
        </details>
      ) : (
        <p className={s.help}>유효한 저장 조치가 없습니다. 현재 근거를 확인해 명시적으로 생성하세요.</p>
      )}
    </>
  );
  const promptBody = (
    <>
      {record?.stale ? (
        <p role="alert">근거·조치·기술 또는 생성기 버전 변경: 오래된 프롬프트 사용을 차단했습니다.</p>
      ) : null}
      {bundle?.missing_context_fields?.length ? <p>보완할 맥락: {bundle.missing_context_fields.join(", ")}</p> : null}
      <div className={s.acts}>
        {bundle ? (
          <>
            <button className={s.btn} type="button" onClick={() => setShow((v) => !v)} aria-expanded={show}>
              {show ? "전체 프롬프트 접기" : "전체 프롬프트 보기"}
            </button>
            <button className={s.btn} type="button" disabled={busy} onClick={() => void copy()}>
              AI 수정 프롬프트 복사
            </button>
          </>
        ) : null}
        <button className={s.btn} type="button" disabled={busy || !entry.available} onClick={() => void generate()}>
          {busy ? "저장 근거 확인 중…" : "보안 조치·AI 프롬프트 생성·저장"}
        </button>
      </div>
      {bundle && show ? <pre className={s.code}>{maskSecrets(bundle.ai_prompt)}</pre> : null}
      {!entry.available ? <p className={s.help}>{maskSecrets(entry.unavailable_reason)}</p> : null}
      {message ? <p role="status">{message}</p> : null}
      <p className={s.help}>
        고객 저장소·원인 코드를 확인한 뒤 최소 수정과 회귀 시험을 하도록 지시하는 프롬프트입니다. 코드 수정·운영
        재현·배포 권한을 주지 않습니다.
      </p>
    </>
  );
  const tech = (
    <Isec>
      <details>
        <summary>기술 정보</summary>
        <dl className={s.kv}>
          <dt>판정 · 검증</dt>
          <dd>{`${outcomeLabel(entry.outcome)} · ${entry.verification}`}</dd>
          <dt>실행</dt>
          <dd className={s["kv-ref"]}>{entry.run || "미기록"}</dd>
          <dt>증거</dt>
          <dd
            className={s["kv-ref"]}
          >{`버전 ${entry.evidence_version} · ${entry.evidence_digest} · ${entry.observed_at}`}</dd>
          {(entry.steps ?? []).map((step) => (
            <Fragment key={step.id}>
              <dt>{`Step ${step.number}`}</dt>
              <dd className={s["kv-ref"]}>
                {`${step.observed_at} · ${step.run_ref} · 호출 ${step.call_ref || "미기록"} · 근거 ${step.evidence_ref}`}
              </dd>
            </Fragment>
          ))}
          {bundle ? (
            <>
              <dt>조치 · 프롬프트</dt>
              <dd className={s["kv-ref"]}>
                {`조치 ${bundle.remediation_version} · 증거 ${bundle.evidence_version} · 입력 ${bundle.input_digest}`}
              </dd>
            </>
          ) : null}
        </dl>
      </details>
    </Isec>
  );

  if (layout === "page")
    // ARTEX 발견 상세 페이지 순서(요약 → 증거 / PoC → 상세 보고서). ARTEX 의 재검증 · 연결된 트래픽 · 경로도는 제품 서버에
    // 그 기능 · API 가 없어 빈 칸으로 두지 않는다. 우리 쪽 보안 조치 · AI 프롬프트 · 기술 정보는 그 아래에 둔다
    return (
      <>
        <Isec>
          <h3>요약</h3>
          <p>{maskSecrets(entry.summary)}</p>
          {truncNote}
          {e3Help}
        </Isec>
        <Isec>
          <h3>증거 / PoC</h3>
          <p className={s.help}>엔진이 실제로 보내고 저장한 실행 관찰과 요청 · 응답입니다.</p>
          {steps}
          <pre className={cx(s.code, s.dbox)}>{evidenceText}</pre>
        </Isec>
        <Isec>
          <section aria-label="상세 보고">
            <div className={s["dcard-h"]}>
              <h3>상세 보고서</h3>
              {entry.report ? reportCopy : null}
            </div>
            {entry.report ? (
              <div ref={reportBox}>
                <Markdown text={maskSecrets(entry.report)} />
              </div>
            ) : (
              <p className={s.help}>저장된 상세 보고가 없습니다.</p>
            )}
            {reportMsg ? <p role="status">{reportMsg}</p> : null}
          </section>
        </Isec>
        <Isec>
          <h3>보안 조치</h3>
          {remediationBody}
        </Isec>
        <Isec>
          <h3>AI 수정 작업용 프롬프트</h3>
          {promptBody}
        </Isec>
        {tech}
      </>
    );

  return (
    <>
      {/* ARTEX 발견 상세처럼(사용자 11/17): 판정 · 유형 · 위험도 · 시각은 위 상태 칸이 말하고, 상세 보고(Markdown) · 증거는
          칸 안 카드로 펼쳐 두되 높이를 묶은 상자에서 스크롤한다. 자세한 조치 묶음은 접어 두고, 검증 방식 · 실행 · 증거 버전 ·
          단계별 호출/근거 ID · digest 같은 기록 값은 맨 아래 '기술 정보'로 모은다. 내용은 지우지 않는다 */}
      <Isec>
        <h3>① 판정과 영향</h3>
        <p>{maskSecrets(entry.summary)}</p>
        {entry.report ? (
          <section className={s.dcard} aria-label="상세 보고">
            <div className={s["dcard-h"]}>
              <h4>상세 보고</h4>
              {reportCopy}
            </div>
            <div className={s.dbox} ref={reportBox}>
              <Markdown text={maskSecrets(entry.report)} />
            </div>
            {reportMsg ? <p role="status">{reportMsg}</p> : null}
          </section>
        ) : null}
        {truncNote}
        {e3Help}
      </Isec>
      <Isec>
        <h3>② 재현 절차 및 스크린샷</h3>
        {steps}
        <section className={s.dcard} aria-label="증거">
          <div className={s["dcard-h"]}>
            <h4>증거 · 요청·응답 및 실행 기록</h4>
          </div>
          <pre className={cx(s.code, s.dbox)}>{evidenceText}</pre>
        </section>
      </Isec>
      <Isec>
        <h3>③ 보안 조치</h3>
        {remediationBody}
      </Isec>
      <Isec>
        <h3>④ AI 수정 작업용 프롬프트</h3>
        {promptBody}
      </Isec>
      {tech}
    </>
  );
}
