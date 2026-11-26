"use client";

import { useEffect, useRef, useState } from "react";

import Image from "next/image";

import { ApiError, apiFetchBlob } from "@/lib/diagnosis/api";
import { diagnosticOutcome, diagnosticOutcomeLabels } from "@/lib/diagnosis/diagnostic-results";
import { DIAGNOSTIC_CAPTURE_LIMITS, type RemediationEntry, type ReproductionStep } from "@/lib/diagnosis/remediation";
import { entrySeverityLabel } from "@/lib/diagnosis/severity";

import { cx } from "../../lib/cx";
import { type BusinessRuleReview, fetchBusinessRuleReviews, reviewUnsupported } from "../../live/businessRuleReview";
import { maskForCopy } from "../../live/copyMask";
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
        스크린샷 미확보 — 저장된 요청 시도와 관찰 기록을 확인하세요 · 상태{" "}
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
          alt={`요청 ${step.number} 실행 당시의 비민감 영역 캡처`}
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
/** 서버가 단계마다 붙이는 기본 제목(reproduction/model.go '저장된 실행 관찰 n'). 번호만 되풀이해 뜻이 없다. */
const DEFAULT_STEP_TITLE = /^저장된 실행 관찰 \d+$/;

/**
 * 실행 단계 제목: '요청 n'(사용자 11/20 — 'Step 1. 저장된 실행 관찰 1'은 번호가 두 번이고 말이 어색했다). 서버 기본 제목이 아니면
 * '요청 n · 제목'. 화면 칸 · PoC 복사가 같이 쓴다.
 */
export function stepHeading(step: Pick<ReproductionStep, "number" | "title">): string {
  const title = step.title;
  return !title || DEFAULT_STEP_TITLE.test(title) ? `요청 ${step.number}` : `요청 ${step.number} · ${title}`;
}

/**
 * 상세 보기 '증거 / PoC' 칸의 복사 글 (사용자 11/18 'PoC 복사'): 화면 그 칸에 보이는 것과 같은 내용 — 제목 · 위험도 · 판정 ·
 * ID, 실행 단계(메서드 · 경로 · 사전 조건 · 관찰), 저장된 요청 · 응답 증거와 잘림 안내. 화면처럼 가리지 않은 글이고(DLP
 * 폐기), 스크린샷 버튼 · 안내 글은 넣지 않는다. 실행하지 않은 단계는 만들지 않는다(절대규칙 5).
 */
export function pocCopyText(entry: RemediationEntry): string {
  const outcome = diagnosticOutcome(entry);
  const out = [
    `${outcome === "confirmed" ? "PoC" : "저장된 근거"} — ${entry.title || entry.entry_id}`,
    `[${entrySeverityLabel(entry)} · ${diagnosticOutcomeLabels[outcome]}] ID ${entry.entry_id}`,
    "",
  ];
  if (outcome !== "confirmed") out.push("이 기록은 취약점 확정이 아닙니다.", "");
  if (entry.source_load_failed)
    out.push("근거 불러오기 실패. 현재 결속을 확인할 수 없어 재현 단계를 제공하지 않습니다.", "");
  else if (entry.steps?.length) {
    for (const st of entry.steps) {
      out.push(stepHeading(st), `${st.method} ${st.path || "비민감 경로 미기록"}`);
      if (st.prerequisites) out.push(`사전 조건: ${st.prerequisites}`);
      if (st.observation) out.push(`관찰: ${st.observation}`);
      if (st.truncated) out.push("이 단계의 관측이 잘렸습니다.");
      out.push("");
    }
    if (entry.steps_truncated) out.push("실행 관측이 잘렸습니다. 전체 재현 절차로 해석하지 마세요.", "");
  } else out.push("구조화된 실제 재현 단계 미확보. 실행하지 않은 절차를 생성하지 않습니다.", "");
  out.push("요청 시도 · 응답 관찰:", entry.evidence || "저장된 증거 본문 없음");
  if (entry.truncated) out.push("", "실제 도구 출력이 잘렸습니다. 완전한 재현 증거가 아닙니다.");
  // 이어 붙인 뒤 한 번 더 가린다(칸 경계에 걸친 값). 클립보드용이라 엔티티 · 역슬래시로 숨긴 키까지 풀어 본다(#709 리뷰)
  return out.join("\n");
}

/**
 * 업무 규칙 검토 칸(#914 인계): 서버가 이 후보에 관찰 · 검토 질문을 줬을 때만 보인다. 관찰을 먼저, 질문은 '확인할 질문(선택)'.
 * '업무 규칙 미확인' 딱지는 서버 status deferred · reason business_rule_unverified 그대로다 — 후보의 검토 상태 · 판정은 바꾸지
 * 않는다(규칙 5). 옛 서버(형식 없음)면 칸을 두지 않고, 그 밖의 실패는 한 줄로 알린다(없음으로 단정하지 않음).
 */
function BusinessRuleReviewBox({ task, entryId }: { task: string; entryId: string }) {
  const key = `${task}:${entryId}`;
  const [got, setGot] = useState<{ key: string; review: BusinessRuleReview | null; failed: boolean } | null>(null);
  useEffect(() => {
    if (!entryId.startsWith("candidate-")) return;
    let live = true;
    fetchBusinessRuleReviews(task)
      .then((m) => {
        if (live) setGot({ key, review: m.get(entryId) ?? null, failed: false });
      })
      .catch((error: unknown) => {
        if (!live) return;
        const unsupported = reviewUnsupported(error);
        if (!unsupported) console.warn("업무 규칙 검토 읽기 실패", error);
        setGot({ key, review: null, failed: !unsupported });
      });
    return () => {
      live = false;
    };
  }, [task, entryId, key]);
  if (got?.key !== key) return null;
  if (got.failed)
    return (
      <Isec>
        <p className={s.help}>업무 규칙 검토를 읽지 못했습니다.</p>
      </Isec>
    );
  if (!got.review) return null;
  return (
    <Isec>
      <section className={s.brr} aria-label="업무 규칙 검토">
        <div className={s["dcard-h"]}>
          <h3>업무 규칙 검토</h3>
          <span className={cx(s.tag, s.ap)}>업무 규칙 미확인</span>
        </div>
        <dl className={s.kv}>
          <div>
            <dt>관찰</dt>
            <dd>{got.review.observation}</dd>
          </div>
          <div>
            <dt>확인할 질문(선택)</dt>
            <dd>{got.review.question}</dd>
          </div>
        </dl>
      </section>
    </Isec>
  );
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
   * panel = 오른쪽 취약점 상세(① AI 분석 · ② 요청 시도 · 응답 관찰).
   * page = 상세 보기 페이지, ARTEX 발견 상세 순서와 이름(요약 · 증거 / PoC · 상세 보고서, 사용자 11/17).
   * 보안 조치 · AI 수정 작업용 프롬프트 칸은 뺐다(사용자 11/19 — AI 가 권고를 비워 두고 생성 버튼을 눌러야만 채워져 빈 칸만
   * 보였다). 서버 API(조치 생성 · 프롬프트 복사)와 내려받기 보고서는 그대로다
   */
  layout?: "panel" | "page";
}) {
  const [reportMsg, setReportMsg] = useState(""),
    [pocMsg, setPocMsg] = useState("");
  const reportBox = useRef<HTMLDivElement>(null);
  // 상세 보고 복사: 원문 Markdown 이 아니라 화면에 그려진 글을 가려서 복사한다. 화면은 가리지 않지만 클립보드는 화면 밖(팀 채팅 ·
  // 이슈)으로 나가므로 가린다(#975 이동찬 P2, 사용자 결정 2026-11-26). 원문을 복사하면 붙여 넣은 곳의 Markdown 이 엔티티(&#112;…)를
  // 다시 풀어 값이 되살아날 수 있어 그려진 글을 maskForCopy 로 가린다(#677 · #709 리뷰)
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
      // PoC 복사도 클립보드로 나가는 글이라 가린다(#975 이동찬 P2). 화면의 증거 칸은 그대로 보인다
      await navigator.clipboard.writeText(maskForCopy(pocCopyText(entry)));
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
            <h4>{stepHeading(step)}</h4>
            <p>
              {step.method} {step.path || "비민감 경로 미기록"}
            </p>
            {step.prerequisites ? <p>사전 조건: {step.prerequisites}</p> : null}
            <p>{step.observation}</p>
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
  const evidenceText = entry.evidence || "저장된 증거 본문 없음";
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
          <p>{entry.summary}</p>
          {truncNote}
          {e3Help}
        </Isec>
        <BusinessRuleReviewBox task={task} entryId={entry.entry_id} />
        <Isec>
          <h3>증거 / PoC</h3>
          <p className={s.help}>
            E②에 기록된 요청 시도와 응답 관찰입니다. 전송되지 않았거나 응답이 없는 시도도 포함될 수 있습니다.
          </p>
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
                <Markdown text={entry.report} />
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
      {/* 업무 규칙 검토(#914): 사람이 확인할 일이라 맨 위에 둔다. 서버가 준 후보에만 */}
      <BusinessRuleReviewBox task={task} entryId={entry.entry_id} />
      <Isec>
        {/* AI(엔진)가 쓴 요약 · 상세 보고라 '판정'으로 읽히지 않게 이름을 AI 분석으로(사용자 11/19) — 판정은 상태 칸 딱지 */}
        <h3>① AI 분석</h3>
        <p>{entry.summary}</p>
        {entry.report ? (
          <section className={s.dcard} aria-label="상세 보고">
            <div className={s["dcard-h"]}>
              <h4>상세 보고</h4>
              {reportCopy}
            </div>
            <div className={s.dbox} ref={reportBox}>
              <Markdown text={entry.report} />
            </div>
            {reportMsg ? <p role="status">{reportMsg}</p> : null}
          </section>
        ) : null}
        {truncNote}
        {e3Help}
      </Isec>
      <Isec>
        {/* E②의 요청 시도에는 거부된 미송신 시도도 포함된다. 응답이 없을 수 있으므로 송신·응답 완료를 단정하지 않는다. */}
        <h3>② 요청 시도 · 응답 관찰</h3>
        {steps}
        <section className={s.dcard} aria-label="증거">
          <div className={s["dcard-h"]}>
            <h4>증거 · 전송 통제 기록</h4>
          </div>
          <pre className={cx(s.code, s.dbox)}>{evidenceText}</pre>
        </section>
      </Isec>
    </>
  );
}
