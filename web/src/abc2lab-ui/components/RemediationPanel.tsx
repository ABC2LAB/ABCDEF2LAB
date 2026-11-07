import { useCallback, useEffect, useRef, useState } from "react";

import { ApiError } from "../lib/api";
import {
  generateRemediation,
  type RemediationBundle,
  type RemediationEntry,
  readCopyPrompt,
  readRemediation,
  type Technology,
  technologyLabels,
  technologyOptions,
} from "../lib/remediation";

const preStyle = { whiteSpace: "pre-wrap", overflowWrap: "anywhere", fontSize: 13 } as const;
export function RemediationPanel({ taskId, active }: { taskId: string; active: boolean }) {
  const [audience, setAudience] = useState("shared");
  useEffect(() => {
    if (!active) setAudience("shared");
  }, [active]);
  return (
    <div className="card">
      <h3>공통 결과 상세 · 수정 프롬프트</h3>
      <label>
        상세 공개 범위{" "}
        <select aria-label="상세 공개 범위" value={audience} onChange={(e) => setAudience(e.target.value)}>
          <option value="shared">공유용 마스킹 (기본)</option>
          <option value="internal">관리자 내부 상세</option>
        </select>
      </label>
      <p>
        선택한 점검의 전체 저장 회차입니다. 생성은 저장 증거로 수정 제안을 만들 뿐 대상 요청, 고객 코드 수정, 배포를
        실행하지 않습니다. 내부 상세는 외부 전달 전 검토하세요.
      </p>
      <RemediationEntries key={`${taskId}-${audience}`} taskId={taskId} audience={audience} active={active} />
    </div>
  );
}
function RemediationEntries({ taskId, audience, active }: { taskId: string; audience: string; active: boolean }) {
  const [entries, setEntries] = useState<RemediationEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState("");
  const [message, setMessage] = useState("");
  const request = useRef<AbortController | null>(null);
  const load = useRef<AbortController | null>(null);
  const mounted = useRef(false);
  const refresh = useCallback(async () => {
    load.current?.abort();
    const controller = new AbortController();
    load.current = controller;
    setLoading(true);
    setEntries([]);
    try {
      const data = await readRemediation(taskId, audience, controller.signal);
      if (controller.signal.aborted || !mounted.current) return;
      if (String(data.task_id) !== taskId || !Array.isArray(data.entries)) throw new Error("scope");
      const ids = new Set<string>();
      for (const e of data.entries) {
        if (
          (audience === "shared" && (e.source_digest || e.record?.bundle)) ||
          !/^(candidate|finding|verdict)-[1-9][0-9]*$/.test(e.entry_id) ||
          ids.has(e.entry_id) ||
          (e.record &&
            (e.record.entry_id !== e.entry_id ||
              (e.record.bundle &&
                (e.record.bundle.finding_id !== e.entry_id ||
                  e.record.bundle.source_digest !== e.source_digest ||
                  e.record.bundle.evidence_version !== String(e.evidence_version)))))
        )
          throw new Error("scope");
        ids.add(e.entry_id);
      }
      setEntries(data.entries);
      return true;
    } catch {
      if (!controller.signal.aborted && mounted.current)
        setMessage("상세 범위 또는 저장 증거를 확인하지 못했어요. 다시 조회해 주세요.");
    } finally {
      if (!controller.signal.aborted && mounted.current) setLoading(false);
    }
  }, [taskId, audience]);
  useEffect(() => {
    mounted.current = active;
    if (active && /^[1-9][0-9]*$/.test(taskId)) void refresh();
    else {
      setEntries([]);
      setLoading(false);
    }
    return () => {
      mounted.current = false;
      request.current?.abort();
      load.current?.abort();
    };
  }, [active, taskId, refresh]);
  async function actOn(entry: RemediationEntry, technologies?: Technology[]) {
    if (!active || !mounted.current || request.current || audience !== "internal" || !entry.available) return;
    const controller = new AbortController();
    request.current = controller;
    setBusy(entry.entry_id);
    setMessage("");
    try {
      if (technologies) {
        await generateRemediation(taskId, entry, technologies, controller.signal);
        if (controller.signal.aborted || !mounted.current) return;
        const refreshed = await refresh();
        if (refreshed && !controller.signal.aborted && mounted.current)
          setMessage("저장된 수정 제안을 다시 조회했어요. 적용 전제와 미검증 항목을 확인하세요.");
      } else {
        const record = entry.record;
        if (!record?.bundle || record.stale) throw new Error("stale");
        const result = await readCopyPrompt(taskId, entry, controller.signal);
        if (controller.signal.aborted || !mounted.current) return;
        if (
          result.entry_id !== entry.entry_id ||
          result.record_id !== record.id ||
          result.input_digest !== record.bundle.input_digest ||
          result.ai_prompt !== record.bundle.ai_prompt
        )
          throw new Error("version");
        if (!navigator.clipboard?.writeText) throw new Error("clipboard");
        await navigator.clipboard.writeText(result.ai_prompt);
        if (!controller.signal.aborted && mounted.current)
          setMessage("현재 화면과 같은 버전의 프롬프트를 복사했어요. 전달받는 AI의 실행 권한은 별도로 정하세요.");
      }
    } catch (error) {
      if (!controller.signal.aborted && mounted.current) {
        setMessage(
          error instanceof ApiError && error.status === 409
            ? "증거 또는 수정안이 바뀌었어요. 다시 조회한 뒤 직접 생성해 주세요. 이전 프롬프트는 복사하지 않았습니다."
            : "작업을 완료하지 못했어요. 권한·버전을 다시 확인하세요. 복사 실패 시 클립보드 권한도 확인하세요.",
        );
        if (error instanceof ApiError && error.status === 409) await refresh();
      }
    } finally {
      if (request.current === controller) {
        request.current = null;
        if (mounted.current) setBusy("");
      }
    }
  }
  return (
    <>
      <button
        className="btn"
        type="button"
        disabled={loading || !!busy || !active}
        onClick={() => {
          setMessage("");
          void refresh();
        }}
      >
        상세 새로고침
      </button>
      {loading && <p role="status">상세를 불러오는 중…</p>}
      {message && <p role="status">{message}</p>}
      {!loading && entries.length === 0 && <p>표시할 결과가 없거나 조회하지 못했습니다.</p>}
      {entries.map((entry) => (
        <article key={entry.entry_id} className="card">
          <h3>
            {entry.title} · {entry.entry_id}
          </h3>
          <p>
            판정: {entry.outcome} · 증거 버전: {entry.evidence_version}
          </p>
          <h4>1. 취약점 상세와 실제 재현 증거</h4>
          <pre style={preStyle}>{entry.summary || "저장된 요약 없음"}</pre>
          <pre style={preStyle}>{entry.evidence || "저장된 증거 본문 없음 · 보호 자료 별도 확인 필요"}</pre>
          <pre style={preStyle}>{entry.report || "저장된 상세 보고서 없음"}</pre>
          <h4>2. 보안 설정 · 코드 수정 방법</h4>
          {entry.record?.stale && (
            <p role="alert">
              저장된 수정안이 오래되었습니다. 새 증거로 다시 생성하세요. 이전 프롬프트는 복사할 수 없습니다.
            </p>
          )}
          {entry.record?.bundle && !entry.record.stale && <BundleDetails bundle={entry.record.bundle} />}
          {!entry.record?.bundle && <p>{entry.remediation || "아직 생성된 수정안이 없습니다."}</p>}
          {audience === "internal" && (
            <TechnologyForm
              disabled={!entry.available || !!busy || !active}
              onGenerate={(technologies) => void actOn(entry, technologies)}
            />
          )}
          {!entry.available && (
            <p>생성·복사 불가: {entry.unavailable_reason || "현재 공개 범위 또는 증거 상태를 확인하세요."}</p>
          )}
          <h4>3. AI 수정 작업용 프롬프트</h4>
          {entry.record?.bundle && !entry.record.stale ? (
            <>
              <p>
                기록 #{entry.record.id} · 조치 버전 {entry.record.bundle.remediation_version} · 보고서 버전{" "}
                {entry.record.bundle.report_version}
              </p>
              <pre style={preStyle}>{entry.record.bundle.ai_prompt}</pre>
              <button
                className="btn"
                type="button"
                disabled={!entry.available || !!busy || !active || audience !== "internal"}
                onClick={() => void actOn(entry)}
              >
                현재 버전 확인 후 프롬프트 복사
              </button>
            </>
          ) : (
            <p>복사 가능한 현재 버전의 프롬프트가 없습니다.</p>
          )}
        </article>
      ))}
    </>
  );
}
function BundleDetails({ bundle }: { bundle: RemediationBundle }) {
  const sections: [string, string[]][] = [
    ["보안 조건", bundle.security_condition],
    ["즉시 완화책", bundle.immediate_mitigation],
    ["근본 수정", bundle.root_fix],
    ["설정 변경", bundle.configuration],
    ["조건부 코드 예시", bundle.code_example],
    ["적용 전제", bundle.assumptions],
    ["검증·회귀 시험", bundle.verification],
    ["남는 위험", bundle.residual_risk],
  ];
  return (
    <>
      <p>
        기술 정보:{" "}
        {bundle.technologies
          .map(
            (t) =>
              `${technologyLabels[t.kind] ?? t.kind}: ${t.name} (${t.version}, 출처 ${t.source}, 확인수준 ${t.confidence})`,
          )
          .join(" · ") || "미확인"}
      </p>
      {sections.map(([label, values]) => (
        <div key={label}>
          <strong>{label}</strong>
          <pre style={preStyle}>{(values ?? []).join("\n")}</pre>
        </div>
      ))}
    </>
  );
}
function TechnologyForm({ disabled, onGenerate }: { disabled: boolean; onGenerate: (tech: Technology[]) => void }) {
  const [values, setValues] = useState<Record<string, Technology>>({});
  function update(kind: string, patch: Partial<Technology>) {
    setValues((old) => ({
      ...old,
      [kind]: { ...(old[kind] ?? { kind, name: "unknown", version: "", confidence: "unknown" }), ...patch },
    }));
  }
  return (
    <details>
      <summary>사용자 제공 기술 정보로 수정 제안 생성</summary>
      <p>
        확인된 기술만 선택하세요. 미입력은 미확인이며 언어만으로 프레임워크를 추정하지 않습니다. 비밀값·고객 데이터는
        입력하지 마세요. 서버가 출처를 사용자 제공으로 기록합니다.
      </p>
      <fieldset disabled={disabled}>
        <legend>기술 정보 (선택)</legend>
        {Object.entries(technologyOptions).map(([kind, options]) => (
          <div key={kind}>
            <label>
              {technologyLabels[kind]}{" "}
              <select
                aria-label={technologyLabels[kind]}
                value={values[kind]?.name ?? "unknown"}
                onChange={(e) => update(kind, { name: e.target.value })}
              >
                <option value="unknown">미확인</option>
                {options.map((name) => (
                  <option key={name}>{name}</option>
                ))}
              </select>
            </label>
            <label>
              버전{" "}
              <input
                aria-label={`${technologyLabels[kind]} 버전`}
                maxLength={19}
                pattern="[0-9]{1,4}(\.[0-9]{1,4}){0,3}"
                value={values[kind]?.version ?? ""}
                placeholder="미확인"
                onChange={(e) => update(kind, { version: e.target.value })}
              />
            </label>
            <label>
              확인 수준{" "}
              <select
                aria-label={`${technologyLabels[kind]} 확인 수준`}
                value={values[kind]?.confidence ?? "unknown"}
                onChange={(e) => update(kind, { confidence: e.target.value })}
              >
                <option value="unknown">미확인</option>
                <option value="confirmed">확인됨</option>
                <option value="observed">관측됨</option>
                <option value="inferred">추정</option>
              </select>
            </label>
          </div>
        ))}
        <button
          className="btn"
          type="button"
          onClick={() =>
            onGenerate(
              Object.values(values).map((t) => ({
                ...t,
                version: /^\d{1,4}(\.\d{1,4}){0,3}$/.test(t.version) ? t.version : "unknown",
              })),
            )
          }
        >
          수정 제안 생성·저장
        </button>
      </fieldset>
    </details>
  );
}
