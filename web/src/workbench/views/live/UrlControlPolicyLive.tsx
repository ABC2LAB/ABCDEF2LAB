"use client";

// 전송 통제 정책 화면 (E② — 승인된 origin·연결 IP·경로·메서드·예산·승인 방식·감사 결속, 지금은 예시).
// 데이터는 lib/diagnosis/control-policy seam(getControlPolicy)에서만 온다. 정규화·판정(만료·쓰기 범위·fail-closed)은
//   전부 seam(lib), 화면은 그리기만(control-policy.test.ts 가 로직을 이미 검증). 표현/데이터 분리.
// 안전(절대규칙 2): 자격 헤더는 '이름'만, 감사 키·TLS 핀 개인키·예산/승인 토큰 원문은 애초에 투영에 없다(결속/참조만).
//   승인된 origin·경로·메서드는 비밀이 아니라 운영자가 봐야 할 통제 상태라 그대로 보인다.
// ※ live 점등은 E② target_policy 표시용 투영 API 연결 시 getControlPolicy(taskRef) 만 교체(이 화면은 안 바뀐다).

import type { CSSProperties } from "react";
import { useEffect, useState } from "react";

import {
  APPROVAL_LABEL,
  type ControlPolicy,
  getControlPolicy,
  normalizePolicy,
  type PolicyRule,
  policyExpired,
  ruleAllowsWrite,
  rulesMissingMethods,
  summarize,
} from "@/lib/diagnosis/control-policy";

import { cx } from "../../lib/cx";
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
      </div>
      <div style={chipRow}>
        <span style={monoChip}>경로 {rule.pathPrefix || "/"}</span>
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
      {rule.credentialHeaderNames.length ? (
        <p className={s.help}>자격 헤더(이름만): {rule.credentialHeaderNames.join(" · ")} — 값은 표시하지 않습니다.</p>
      ) : null}
    </div>
  );
}

/** 전송 통제 정책 (예시). */
export function UrlControlPolicyLive() {
  const [policy, setPolicy] = useState<ControlPolicy | null>(null);
  const [state, setState] = useState<"loading" | "ready" | "invalid" | "failed">("loading");

  useEffect(() => {
    let live = true;
    // TODO(이경준): E² target_policy 표시용 투영 API 연동 시 현재 대상/회차 ref 를 넘겨 실 정책으로 점등(지금은 예시).
    getControlPolicy(null)
      .then((raw) => {
        if (!live) return;
        const norm = normalizePolicy(raw); // 투영은 untrusted — fail-closed 로 다시 검증(절대규칙 4)
        if (norm) {
          setPolicy(norm);
          setState("ready");
        } else {
          setState("invalid");
        }
      })
      .catch(() => {
        if (live) setState("failed");
      });
    return () => {
      live = false;
    };
  }, []);

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
              승인된 전송 범위(어디로·무엇을·얼마나)와 감사 결속만 보입니다. 자격 헤더는 이름만, 감사 키·토큰 원문은
              표시하지 않습니다.
            </span>
          </div>
          {policy?.mock ? (
            <div className={cx(s.note, s.warn)} role="note">
              <Icon name="alert" />
              <span>예시 데이터입니다. 실제 전송 통제 정책은 E² target_policy 투영 API 연결 후 나옵니다.</span>
            </div>
          ) : null}
          {state === "failed" ? <p className={s.empty}>전송 통제 정책을 불러오지 못했습니다.</p> : null}
          {state === "invalid" ? (
            <div className={cx(s.note, s.warn)} role="note">
              <Icon name="alert" />
              <span>정책 투영을 확인할 수 없습니다. 통제 범위를 신뢰할 수 없어 표시하지 않습니다(fail-closed).</span>
            </div>
          ) : null}
          {state === "loading" ? <p className={s.empty}>불러오는 중…</p> : null}

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
              </div>
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
                  최대 동시 <b>{policy.limits.maxConcurrent}</b>
                </span>
                <span style={neutralChip}>
                  출력 상한 <b>{policy.limits.outputLimit}</b> B
                </span>
                <span style={monoChip}>예산 {policy.limits.budgetRef || "미지정"}</span>
              </div>
            </section>
          ) : null}
        </div>
      </div>
    </>
  );
}
