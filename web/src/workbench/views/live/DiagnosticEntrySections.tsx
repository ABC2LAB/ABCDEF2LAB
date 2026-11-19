"use client";

import { useEffect, useRef, useState } from "react";

import Image from "next/image";

import { ApiError, apiFetchBlob } from "@/lib/diagnosis/api";
import { diagnosticOutcome, diagnosticOutcomeLabels } from "@/lib/diagnosis/diagnostic-results";
import { DIAGNOSTIC_CAPTURE_LIMITS, type RemediationEntry, type ReproductionStep } from "@/lib/diagnosis/remediation";
import { entrySeverityLabel } from "@/lib/diagnosis/severity";

import { cx } from "../../lib/cx";
import { maskForCopy, maskSecrets } from "../../live/mask";
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
/**
 * 상세 보기 '증거 / PoC' 칸의 복사 글 (사용자 11/18 'PoC 복사'): 화면 그 칸에 보이는 것과 같은 내용 — 제목 · 위험도 · 판정 ·
 * ID, 실행 단계(메서드 · 경로 · 사전 조건 · 관찰), 저장된 요청 · 응답 증거와 잘림 안내. 칸마다 화면처럼 가린 글만 쓰고
 * (maskSecrets), 스크린샷 버튼 · 안내 글은 넣지 않는다. 실행하지 않은 단계는 만들지 않는다(절대규칙 5).
 */
export function pocCopyText(entry: RemediationEntry): string {
  const outcome = diagnosticOutcome(entry);
  const out = [
    `${outcome === "confirmed" ? "PoC" : "저장된 근거"} — ${maskSecrets(entry.title) || entry.entry_id}`,
    `[${entrySeverityLabel(entry)} · ${diagnosticOutcomeLabels[outcome]}] ID ${entry.entry_id}`,
    "",
  ];
  if (outcome !== "confirmed") out.push("이 기록은 취약점 확정이 아닙니다.", "");
  if (entry.source_load_failed)
    out.push("근거 불러오기 실패. 현재 결속을 확인할 수 없어 재현 단계를 제공하지 않습니다.", "");
  else if (entry.steps?.length) {
    for (const st of entry.steps) {
      out.push(
        `Step ${st.number}. ${maskSecrets(st.title)}`,
        `${st.method} ${maskSecrets(st.path) || "비민감 경로 미기록"}`,
      );
      if (st.prerequisites) out.push(`사전 조건: ${maskSecrets(st.prerequisites)}`);
      if (st.observation) out.push(`관찰: ${maskSecrets(st.observation)}`);
      if (st.truncated) out.push("이 단계의 관측이 잘렸습니다.");
      out.push("");
    }
    if (entry.steps_truncated) out.push("실행 관측이 잘렸습니다. 전체 재현 절차로 해석하지 마세요.", "");
  } else out.push("구조화된 실제 재현 단계 미확보. 실행하지 않은 절차를 생성하지 않습니다.", "");
  out.push("요청 · 응답 증거:", maskSecrets(entry.evidence) || "저장된 증거 본문 없음");
  if (entry.truncated) out.push("", "실제 도구 출력이 잘렸습니다. 완전한 재현 증거가 아닙니다.");
  // 이어 붙인 뒤 한 번 더 가린다(칸 경계에 걸친 값). 클립보드용이라 엔티티 · 역슬래시로 숨긴 키까지 풀어 본다(#709 리뷰)
  return maskForCopy(out.join("\n"));
}

export function DiagnosticEntrySections({
  task,
  entry,
  layout = "panel",
}: {
  task: string;
  entry: RemediationEntry;
  /** 상세를 다시 읽기. 보안 조치 · AI 프롬프트 생성을 뺀 뒤로 이 부품은 쓰지 않는다(부르는 쪽 모양 유지) */
  onReload: () => void;
  /**
   * panel = 오른쪽 취약점 상세(① AI 분석 · ② 실제 요청 · 응답).
   * page = 상세 보기 페이지, ARTEX 발견 상세 순서와 이름(요약 · 증거 / PoC · 상세 보고서, 사용자 11/17).
   * 보안 조치 · AI 수정 작업용 프롬프트 칸은 뺐다(사용자 11/19 — AI 가 권고를 비워 두고 생성 버튼을 눌러야만 채워져 빈 칸만
   * 보였다). 서버 API(조치 생성 · 프롬프트 복사)와 내려받기 보고서는 그대로다
   */
  layout?: "panel" | "page";
}) {
  const [reportMsg, setReportMsg] = useState(""),
    [pocMsg, setPocMsg] = useState("");
  const reportBox = useRef<HTMLDivElement>(null);
  // 상세 보고 복사: 원문 Markdown 이 아니라 화면에 그려진(파싱 뒤 가린) 글을 복사한다 — 원문을 복사하면 붙여 넣은 곳의
  // Markdown 이 엔티티(&#112;…)를 다시 풀어 비밀이 되살아날 수 있다(#677 리뷰). 한 번 더 가린다
  async function copyReport() {
    const box = reportBox.current;
    const shown = box ? box.innerText || box.textContent || "" : "";
    try {
      await navigator.clipboard.writeText(maskForCopy(shown));
      setReportMsg("상세 보고를 복사했습니다.");
    } catch (error) {
      console.warn("상세 보고 복사 실패", error);
      setReportMsg("복사 권한을 확인하지 못했습니다.");
    }
  }
  async function copyPoc() {
    try {
      await navigator.clipboard.writeText(pocCopyText(entry));
      setPocMsg("PoC 를 복사했습니다.");
    } catch (error) {
      console.warn("PoC 복사 실패", error);
      setPocMsg("복사 권한을 확인하지 못했습니다.");
    }
  }
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
  if (layout === "page")
    // ARTEX 발견 상세 페이지 순서(요약 → 증거 / PoC → 상세 보고서). ARTEX 의 재검증 · 연결된 트래픽 · 경로도는 제품 서버에
    // 그 기능 · API 가 없어 빈 칸으로 두지 않는다
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
          {/* 칸 맨 밑: 이 칸 내용(단계 · 요청 · 응답 증거)을 글로 복사 (사용자 11/18) */}
          <div className={s["poc-a"]}>
            <button className={cx(s.btn, s.sm)} type="button" onClick={() => void copyPoc()}>
              PoC 복사
            </button>
            {pocMsg ? <span role="status">{pocMsg}</span> : null}
          </div>
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
      </>
    );

  return (
    <>
      {/* ARTEX 발견 상세처럼(사용자 11/17): 판정 · 유형 · 위험도 · 시각은 위 상태 칸이 말하고, 상세 보고(Markdown) · 증거는
          칸 안 카드로 펼쳐 두되 높이를 묶은 상자에서 스크롤한다. 검증 방식 · 실행 · 증거 버전 · 단계별 호출/근거 ID · digest
          같은 기록 값('기술 정보')은 보는 사람에게 필요 없어 뺐다(사용자 11/19) — 내부 상세 사본(내려받기)에는 남는다 */}
      <Isec>
        {/* AI(엔진)가 쓴 요약 · 상세 보고라 '판정'으로 읽히지 않게 이름을 AI 분석으로(사용자 11/19) — 판정은 상태 칸 딱지 */}
        <h3>① AI 분석</h3>
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
        {/* E② 가 실제로 보낸 요청 · 받은 응답 기록(실행 단계 · 증거). ① AI 분석과 구분되게, 스크린샷은 저장된 단계에만
            버튼이 나오므로 이름에서 뺐다(사용자 11/19) */}
        <h3>② 실제 요청 · 응답</h3>
        {steps}
        <section className={s.dcard} aria-label="증거">
          <div className={s["dcard-h"]}>
            <h4>증거 · 요청·응답 및 실행 기록</h4>
          </div>
          <pre className={cx(s.code, s.dbox)}>{evidenceText}</pre>
        </section>
      </Isec>
    </>
  );
}
