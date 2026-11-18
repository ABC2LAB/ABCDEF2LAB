"use client";

// 전송 통제 정책 화면 (E② — 승인된 origin·연결 IP·경로·메서드·상한·승인 방식·감사 결속).
// 데이터는 lib/diagnosis/control-policy seam(getControlPolicy)에서만 온다. 정규화·판정(만료·쓰기 범위·fail-closed)은
//   전부 seam(lib), 화면은 그리기만. 표현/데이터 분리.
// 현재 이어 보는 점검의 task 가 있으면 실 투영(GET /api/tasks/{id}/control-policy), 없으면 예시(배너로 명시).
// 안전(절대규칙 2): 투영에 자격 값·감사 키·토큰 원문은 애초에 없다(계정은 라벨만, 감사는 결속 여부만).
//   승인된 origin·경로·메서드는 비밀이 아니라 운영자가 봐야 할 통제 상태라 그대로 보인다.
// 편집·저장 칸은 두지 않는다 — 실행 정책은 대상 승인 판·설치 상한에서 서버가 만든다(계약 §6).

import type { CSSProperties } from "react";
import { useEffect, useState } from "react";

import {
  APPROVAL_LABEL,
  AUTH_PREP_PURPOSE,
  getControlPolicy,
  HEADER_MODE_LABEL,
  type PolicyLoad,
  type PolicyRule,
  policyExpired,
  ruleAllowsWrite,
  rulePathText,
  rulesMissingMethods,
  summarize,
} from "@/lib/diagnosis/control-policy";

import { cx } from "../../lib/cx";
import { useUrl } from "../../live/urlDiagnosis";
import { Icon } from "../../ui/icons";
import s from "../../workbench.module.css";
import { UrlHead } from "./urlParts";

// ── 인라인 스타일(뼈대). 색은 실재 workbench 토큰만(접근통제 지도·커버리지·발견 상세·전송 통제 흐름과 같은 팔레트).
const chipBase: CSSProperties = {
  display: "inline-flex",
  alignItems: "center",
  gap: 5,
  padding: "3px 9px",
  borderRadius: 999,
  border: "1px solid",
  fontSize: 12,
};
const neutralChip: CSSProperties = {
  ...chipBase,
  background: "var(--well)",
  color: "var(--ink-2)",
  borderColor: "var(--rule)",
};
const monoChip: CSSProperties = { ...neutralChip, fontFamily: "var(--mono)" };
const writeChip: CSSProperties = {
  ...chipBase,
  background: "var(--mid-bg)",
  color: "var(--mid)",
  borderColor: "var(--mid)",
};
const chipRow: CSSProperties = { display: "flex", flexWrap: "wrap", gap: 6, marginTop: 6 };
const ruleBox: CSSProperties = {
  marginTop: 10,
  border: "1px solid var(--rule)",
  borderRadius: 8,
  padding: "10px 12px",
};
const ruleHead: CSSProperties = { display: "flex", flexWrap: "wrap", alignItems: "baseline", gap: 8 };

const fmtTime = (iso: string): string => {
  const ms = Date.parse(iso);
  if (Number.isNaN(ms)) return "—";
  try {
    return `${new Date(ms).toISOString().replace("T", " ").slice(0, 16)} UTC`;
  } catch {
    return "—";
  }
};

/** 한 전송 규칙 — 승인된 origin·IP·경로·메서드·자격 헤더(이름만). methods 가 비면 '전부'가 아니라 '차단'으로 명시. */
function RuleBlock({ rule }: { rule: PolicyRule }) {
  const write = ruleAllowsWrite(rule);
  // https 가 아닌 대상은 전송(자격·세션 포함)이 평문이라 경고한다(TLS 핀 결속과 별개).
  const noTls = !rule.origin.startsWith("https://");
  return (
    <div style={ruleBox} data-rule={rule.ref}>
      <div style={ruleHead}>
        <span className={s.mono} style={{ fontWeight: 600, color: "var(--ink)" }}>
          {rule.origin}
        </span>
        <span className={s.mono} style={{ color: "var(--ink-3)", fontSize: 12 }}>
          {rule.connectIp || "연결 IP 미지정"}
        </span>
        {write ? <span style={writeChip}>쓰기 포함</span> : <span style={neutralChip}>읽기 전용</span>}
        {rule.purpose === AUTH_PREP_PURPOSE ? <span style={neutralChip}>인증 준비 전용</span> : null}
        {noTls ? (
          <span style={{ ...chipBase, background: "var(--crit-bg)", color: "var(--crit)", borderColor: "var(--crit)" }}>
            TLS 없음 (평문 전송)
          </span>
        ) : null}
      </div>
      <div style={chipRow}>
        <span style={monoChip} data-path-match={rule.pathMatch}>
          {rule.pathMatch === "exact" ? "정확 경로" : "경로"} {rulePathText(rule)}
        </span>
        {rule.pathScopeRef ? <span style={monoChip}>범위 {rule.pathScopeRef}</span> : null}
        {rule.tlsPinned ? <span style={neutralChip}>TLS 핀 결속</span> : null}
      </div>
      <div style={chipRow}>
        {rule.methods.length ? (
          rule.methods.map((m) => (
            <span key={m} style={monoChip} data-method={m}>
              {m}
            </span>
          ))
        ) : (
          <span style={{ ...chipBase, background: "var(--crit-bg)", color: "var(--crit)", borderColor: "var(--crit)" }}>
            승인 메서드 없음 — 이 규칙으로는 전송 차단
          </span>
        )}
      </div>
      {rule.purpose === AUTH_PREP_PURPOSE ? (
        <p className={s.help}>인증 준비(로그인) 전용 규칙 — 모델이 만든 진단 요청은 이 규칙으로 나가지 않습니다.</p>
      ) : null}
    </div>
  );
}

/** 전송 통제 정책 — 현재 점검의 실행 결속 정책(없으면 예시). */
export function UrlControlPolicyLive() {
  const task = useUrl((st) => st.job?.task_id ?? null);
  // 어느 task 의 결과인지 같이 들고, task 가 바뀌면 이전 정책을 보이지 않는다.
  const [result, setResult] = useState<{ task: string | null; load: PolicyLoad | "failed" } | null>(null);

  useEffect(() => {
    let live = true;
    const controller = new AbortController();
    getControlPolicy(task, controller.signal)
      .then((load) => {
        if (live) setResult({ task, load });
      })
      .catch(() => {
        if (live) setResult({ task, load: "failed" });
      });
    return () => {
      live = false;
      controller.abort();
    };
  }, [task]);

  const load = result && result.task === task ? result.load : null;
  const policy = load && load !== "failed" && load.state === "ready" ? load.policy : null;
  const sum = policy ? summarize(policy) : null;
  const expired = policy ? policyExpired(policy) : false;
  const missingMethods = policy ? rulesMissingMethods(policy) : [];

  return (
    <>
      <UrlHead title="전송 통제 정책" crumb="전송 통제 정책" />
      <div className={s.vb}>
        <div className={s.vm}>
          <div className={s.note} role="note">
            <Icon name="info" />
            <span>
              승인된 전송 범위(어디로·무엇을·얼마나)와 감사 결속만 보입니다. 계정은 라벨만, 자격 값·감사 키·토큰 원문은
              표시하지 않습니다. 범위는 여기서 고치지 않고 대상 등록에서 승인합니다.
            </span>
          </div>
          {policy?.mock ? (
            <div className={cx(s.note, s.warn)} role="note">
              <Icon name="alert" />
              <span>예시 데이터입니다. 점검을 시작하면 그 점검에 결속된 실제 전송 통제 정책이 나옵니다.</span>
            </div>
          ) : null}
          {load === null ? <p className={s.empty}>불러오는 중…</p> : null}
          {load === "failed" ? (
            <p className={s.empty} role="alert">
              전송 통제 정책을 불러오지 못했습니다.
            </p>
          ) : null}
          {load !== null && load !== "failed" && load.state === "unbound" ? (
            <p className={s.empty}>
              아직 이 점검에 결속된 전송 통제 정책이 없습니다. 범위 확인 뒤 점검을 시작하면 보입니다.
            </p>
          ) : null}
          {load !== null && load !== "failed" && load.state === "mismatch" ? (
            <div className={cx(s.note, s.warn)} role="alert">
              <Icon name="alert" />
              <span>실행 정책이 운영자 승인과 다릅니다. 통제 범위를 신뢰할 수 없어 표시하지 않습니다.</span>
            </div>
          ) : null}
          {load !== null && load !== "failed" && load.state === "invalid" ? (
            <div className={cx(s.note, s.warn)} role="alert">
              <Icon name="alert" />
              <span>정책 투영을 확인할 수 없습니다. 통제 범위를 신뢰할 수 없어 표시하지 않습니다(fail-closed).</span>
            </div>
          ) : null}

          {policy && sum ? (
            <section className={s.isec} aria-label="정책 요약">
              <h2>정책 요약</h2>
              <div style={chipRow}>
                <span style={neutralChip}>
                  규칙 <b>{sum.rules}</b>
                </span>
                <span style={neutralChip}>
                  origin <b>{sum.origins}</b>
                </span>
                <span style={sum.writeRules > 0 ? writeChip : neutralChip}>
                  쓰기 규칙 <b>{sum.writeRules}</b>
                </span>
                <span
                  style={
                    expired
                      ? { ...chipBase, background: "var(--crit-bg)", color: "var(--crit)", borderColor: "var(--crit)" }
                      : { ...chipBase, background: "var(--ok-bg)", color: "var(--ok)", borderColor: "var(--ok)" }
                  }
                >
                  {expired ? "유효기간 밖(만료·미발효)" : "유효기간 내"}
                </span>
              </div>
              <p className={s.help}>
                ‘승인됨’은 전송 경계가 허용하는 범위일 뿐, 실제로 그만큼 보냈다는 뜻이 아닙니다 — 실제 전송·차단은 전송
                통제 흐름에서 봅니다.
              </p>
            </section>
          ) : null}

          {policy ? (
            <section className={s.isec} aria-label="정책 메타">
              <h2>정책 · 승인 · 감사</h2>
              <div style={chipRow}>
                <span style={monoChip}>정책 {policy.policyRef}</span>
                <span style={monoChip}>개정 {policy.policyRevision || "미기재"}</span>
                <span style={neutralChip}>승인 방식: {APPROVAL_LABEL[policy.approvalMode]}</span>
                <span style={neutralChip}>자격 헤더: {HEADER_MODE_LABEL[policy.headerMode]}</span>
                {policy.fingerprint ? (
                  <span style={monoChip} title={policy.fingerprint}>
                    지문 {policy.fingerprint.slice(0, 12)}…
                  </span>
                ) : null}
              </div>
              <p className={s.help}>
                결속 시험 계정:{" "}
                {policy.accounts.length ? (
                  <span className={s.mono}>{policy.accounts.join(" · ")}</span>
                ) : (
                  "없음(자격 없이 전송)"
                )}
              </p>
              <p className={s.help}>
                유효기간 {fmtTime(policy.validFrom)} ~ {fmtTime(policy.validUntil)}
              </p>
              {expired ? (
                <div className={cx(s.note, s.warn)} role="note">
                  <Icon name="alert" />
                  <span>지금은 정책 유효기간 밖입니다(만료·미발효). 이 정책으로는 새 전송을 신뢰할 수 없습니다.</span>
                </div>
              ) : null}
              {policy.auditBound ? (
                <p className={s.help}>감사 키 결속됨(키 값은 표시하지 않습니다).</p>
              ) : (
                <div className={cx(s.note, s.warn)} role="note">
                  <Icon name="alert" />
                  <span>감사 키가 결속되지 않았습니다. 감사 없는 전송은 통제 밖으로 봅니다.</span>
                </div>
              )}
            </section>
          ) : null}

          {policy ? (
            <section className={s.isec} aria-label="어디로·무엇을">
              <h2>승인된 전송 범위 (어디로 · 무엇을)</h2>
              {missingMethods.length ? (
                <div className={cx(s.note, s.warn)} role="note">
                  <Icon name="alert" />
                  <span>
                    승인 메서드가 비어 있는 규칙 {missingMethods.length}개 — 빈 값은 ‘전부 허용’이 아니라 차단입니다.
                  </span>
                </div>
              ) : null}
              {policy.rules.length ? (
                policy.rules.map((r) => <RuleBlock key={r.ref} rule={r} />)
              ) : (
                <p className={s.empty}>승인된 전송 규칙이 없습니다.</p>
              )}
            </section>
          ) : null}

          {policy ? (
            <section className={s.isec} aria-label="얼마나">
              <h2>전송 상한 (얼마나)</h2>
              <div style={chipRow}>
                <span style={neutralChip}>
                  최대 요청 <b>{policy.limits.maxRequests}</b>
                </span>
                <span style={neutralChip}>
                  최대 시도 <b>{policy.limits.maxAttempts}</b>
                </span>
                <span style={neutralChip}>
                  최대 동시 <b>{policy.limits.maxConcurrent}</b>
                </span>
                <span style={neutralChip}>
                  요청 본문 <b>{policy.limits.maxBodyBytes}</b> B
                </span>
                <span style={neutralChip}>
                  응답 본문 <b>{policy.limits.maxResponseBodyBytes}</b> B
                </span>
                <span style={neutralChip}>
                  요청 시간 <b>{policy.limits.requestTimeoutMs}</b> ms
                </span>
              </div>
            </section>
          ) : null}
        </div>
      </div>
    </>
  );
}
