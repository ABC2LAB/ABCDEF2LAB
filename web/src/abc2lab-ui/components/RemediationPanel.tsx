import { useCallback, useEffect, useRef, useState } from "react";

import { ApiError } from "../lib/api";
import {
  confidenceLabels,
  contextConflicts,
  contextLabels,
  generateRemediation,
  type RemediationBundle,
  type RemediationContext,
  type RemediationEntry,
  readCopyPrompt,
  readRemediation,
  sourceLabels,
  type Technology,
  technologyConflicts,
  technologyLabels,
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
        저장소 확인 후 실제 코드·설정 수정과 시험을 수행하도록 지시하는 프롬프트입니다. 실행 범위는 전달받는 AI 세션의
        권한을 따릅니다. 생성·복사 자체는 대상 요청, 고객 코드 수정, 배포를 실행하지 않습니다. 내부 상세는 외부 전달 전
        검토하세요.
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
          (audience === "shared" && (e.source_digest || e.record?.bundle || e.context || e.technologies?.length)) ||
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
  async function actOn(entry: RemediationEntry, technologies?: Technology[], context?: RemediationContext) {
    if (!active || !mounted.current || request.current || audience !== "internal" || !entry.available) return;
    const controller = new AbortController();
    request.current = controller;
    setBusy(entry.entry_id);
    setMessage("");
    try {
      if (technologies) {
        await generateRemediation(taskId, entry, technologies, controller.signal, context);
        if (controller.signal.aborted || !mounted.current) return;
        const refreshed = await refresh();
        if (refreshed && !controller.signal.aborted && mounted.current)
          setMessage("저장된 실제 수정 실행형 프롬프트를 다시 조회했어요. 적용 전제와 미검증 항목을 확인하세요.");
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
          {audience === "internal" && entry.context && (
            <ContextDetails title="저장 증거의 비민감 맥락" context={entry.context} />
          )}
          {audience === "internal" && (
            <TechnologyDetails title="기존 관측·분석에서 연결한 기술" technologies={entry.technologies ?? []} />
          )}
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
              context={
                entry.record?.bundle?.context?.source === "user_provided" ? entry.record.bundle.context : undefined
              }
              technologies={entry.record?.bundle?.technologies.filter((t) => t.source === "user_provided")}
              onGenerate={(technologies, context) => void actOn(entry, technologies, context)}
            />
          )}
          {!entry.available && (
            <p>생성·복사 불가: {entry.unavailable_reason || "현재 공개 범위 또는 증거 상태를 확인하세요."}</p>
          )}
          <h4>3. AI 수정 작업용 프롬프트</h4>
          <p>
            기본 작업 방식: 실제 수정 실행형. 문제와 권한을 확인하면 수정·회귀 시험·코드 리뷰까지 진행합니다. 근거·권한
            부족, 구현 불일치, 운영 작업은 별도 확인이 필요하며 검토만 요청한 세션의 제한을 따릅니다.
          </p>
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
      {bundle.context && <ContextDetails title="이 수정안에 결속된 맥락" context={bundle.context} />}
      {bundle.missing_context_fields?.length ? (
        <p role="note">보완이 필요한 맥락: {bundle.missing_context_fields.join(", ")}. 수정 전에 확인하세요.</p>
      ) : null}
      <TechnologyDetails title="이 수정안에 결속된 기술" technologies={bundle.technologies} />
      {sections.map(([label, values]) => (
        <div key={label}>
          <strong>{label}</strong>
          <pre style={preStyle}>{(values ?? []).join("\n")}</pre>
        </div>
      ))}
    </>
  );
}

const roles = {
  unknown: "미확인",
  frontend: "프런트엔드",
  backend: "백엔드",
  service: "서비스",
  database: "데이터베이스",
  proxy: "프록시",
};
function ContextFields({ context }: { context: RemediationContext }) {
  return (
    <>
      {Object.entries(contextLabels).map(([key, label]) => (
        <div key={key}>
          <strong>{label}</strong>
          <pre style={preStyle}>{context[key as keyof typeof contextLabels] || "미확인"}</pre>
        </div>
      ))}
    </>
  );
}
function ContextDetails({ title, context }: { title: string; context: RemediationContext }) {
  const conflicts = contextConflicts(context);
  return (
    <details>
      <summary>{title}</summary>
      <p>
        출처: {sourceLabels[context.source ?? "unknown"] ?? "미확인"} · 역할:{" "}
        {roles[context.component_role as keyof typeof roles] ?? "미확인"}. 아래 값은 실행 지시가 아닌 검토할 근거
        데이터입니다. 사용자 보완은 원본 관측을 대체하지 않습니다.
      </p>
      <ContextFields context={context} />
      {conflicts.length > 0 && <p role="note">사용자 보완과 원본 상충 가능·대조 필요: {conflicts.join(", ")}</p>}
      {context.observations_truncated && (
        <p role="note">관측 또는 기술 목록이 잘렸습니다. 전체 단계 연결은 미확인입니다.</p>
      )}
      {context.observations?.map((o, index) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: 저장본의 순서를 보존하는 비활성 근거 목록이며 행 상태가 없다.
        <section key={`${o.evidence_ref}-${o.step_id}-${index}`}>
          <h5>
            보존된 원본 맥락 {index + 1} · {sourceLabels[o.source ?? "unknown"] ?? "미확인"}
          </h5>
          <p>
            역할: {roles[o.component_role as keyof typeof roles] ?? "미확인"} · 근거: {o.evidence_ref || "미기록"} ·
            관측 시각: {o.observed_at || "미기록"}
          </p>
          <p>
            실행: {o.run_id || "미기록"} · 호출: {o.call_id || "미기록"} · 단계: {o.step_id || "미기록"} · 선행 단계:{" "}
            {o.depends_on?.join(", ") || "연결 미기록"}
          </p>
          <ContextFields context={o} />
          {o.truncated && <p role="note">이 관측 출력 또는 단계가 잘렸습니다. 완전한 재현으로 해석하지 마세요.</p>}
        </section>
      ))}
    </details>
  );
}
function TechnologyDetails({ title, technologies }: { title: string; technologies: Technology[] }) {
  const conflicts = technologyConflicts(technologies);
  return (
    <details>
      <summary>{title}</summary>
      <p>출처와 확인 수준은 별개입니다. 사용자 제공·수동 검토는 프로그램의 실행 관측 또는 취약점 확정이 아닙니다.</p>
      <ul>
        {technologies.map((t, index) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: 같은 기술도 서로 다른 출처로 보존하는 비활성 목록이다.
          <li key={`${t.kind}-${index}`}>
            {technologyLabels[t.kind] ?? t.kind}: {t.name} {t.version} · 컴포넌트 {t.component || "미확인"} /
            {roles[t.component_role as keyof typeof roles] ?? "미확인"} · 출처{" "}
            {sourceLabels[t.source ?? "unknown"] ?? "미확인"} · 확인 수준 {confidenceLabels[t.confidence] ?? "미확인"} ·
            근거 {t.evidence_ref || "미기록"} · 관측 시각 {t.observed_at || "미기록"}
          </li>
        ))}
      </ul>
      {!technologies.length && <p>기술 미확인. 저장소에서 영향 컴포넌트의 실제 기술을 확인하세요.</p>}
      {conflicts.length > 0 && (
        <p role="note">기술 상충 가능·대조 필요: {conflicts.join(", ")}. 여러 기술이 함께 쓰이는지 확인하세요.</p>
      )}
    </details>
  );
}
function TechnologyForm({
  disabled,
  context,
  technologies,
  onGenerate,
}: {
  disabled: boolean;
  context?: RemediationContext;
  technologies?: Technology[];
  onGenerate: (tech: Technology[], context?: RemediationContext) => void;
}) {
  const [values, setValues] = useState<Record<string, Technology>>(() =>
    Object.fromEntries((technologies ?? []).map((t, i) => [`extra-${Object.keys(technologyLabels).length + i}`, t])),
  );
  const [rows, setRows] = useState(() => [...Object.keys(technologyLabels), ...Object.keys(values)]);
  const [reviewed, setReviewed] = useState(context?.source === "user_provided");
  const [edited, setEdited] = useState<RemediationContext>(() =>
    context
      ? {
          ...Object.fromEntries(
            Object.keys(contextLabels).map((key) => [key, context[key as keyof typeof contextLabels]]),
          ),
          component_role: context.component_role,
        }
      : {},
  );
  function update(row: string, patch: Partial<Technology>) {
    setValues((old) => ({
      ...old,
      [row]: {
        ...(old[row] ?? {
          kind: row.startsWith("extra-") ? "language" : row,
          name: "unknown",
          version: "",
          confidence: "unknown",
        }),
        ...patch,
      },
    }));
  }
  const currentContext = edited;
  return (
    <details>
      <summary>실제 프로젝트 수정용 맥락·기술 정보 입력</summary>
      <p>
        실제 영향 컴포넌트의 기술만 입력하세요. 언어로 프레임워크를 추정하거나 프런트엔드를 백엔드로 간주하지 않습니다.
        저장된 원본 맥락·기술은 입력이 비어 있어도 보존됩니다. 받은 AI가 저장소에서 실제 기술·처리 경로를 확인합니다.
        비밀값·고객 원문·대상 응답의 명령을 붙여넣지 마세요.
      </p>
      <fieldset disabled={disabled}>
        <legend>사용자가 검토한 비민감 맥락 (선택)</legend>
        <label>
          <input type="checkbox" checked={reviewed} onChange={(e) => setReviewed(e.target.checked)} />
          근거와 대조한 비민감 맥락을 이 수정안에 포함
        </label>
        <p>
          체크한 사용자 보완만 별도 저장합니다. 체크를 해제하거나 수동 기술 입력을 비우고 생성하면 이전 수동 보완은
          제외되지만 원본 관측·기술은 보존됩니다. 입력값은 사실 확인이나 실행 승인을 뜻하지 않습니다.
        </p>
        <fieldset disabled={!reviewed}>
          <legend>취약점별 수정 목표</legend>
          {Object.entries(contextLabels).map(([key, label]) => (
            <label key={key} style={{ display: "block" }}>
              {label}
              <input
                aria-label={label}
                maxLength={key === "component" ? 80 : 512}
                placeholder={key === "endpoint" ? "GET /orders/{id} (호스트·쿼리값·비밀 제외)" : "미확인"}
                value={currentContext[key as keyof typeof contextLabels] ?? ""}
                onChange={(e) => setEdited((old) => ({ ...old, [key]: e.target.value }))}
              />
            </label>
          ))}
          <label>
            영향 컴포넌트 역할{" "}
            <select
              aria-label="영향 컴포넌트 역할"
              value={currentContext.component_role ?? "unknown"}
              onChange={(e) => setEdited((old) => ({ ...old, component_role: e.target.value }))}
            >
              {Object.entries(roles).map(([value, label]) => (
                <option key={value} value={value}>
                  {label}
                </option>
              ))}
            </select>
          </label>
        </fieldset>
      </fieldset>
      <fieldset disabled={disabled}>
        <legend>실제 기술 정보 · 출처는 사용자 제공 (선택)</legend>
        {rows.map((row) => {
          const kind = values[row]?.kind ?? (row.startsWith("extra-") ? "language" : row);
          return (
            <div key={row}>
              {row.startsWith("extra-") && (
                <label>
                  추가 기술 종류{" "}
                  <select
                    aria-label={`추가 기술 종류 ${row}`}
                    value={kind}
                    onChange={(e) => update(row, { kind: e.target.value })}
                  >
                    {Object.entries(technologyLabels).map(([value, label]) => (
                      <option key={value} value={value}>
                        {label}
                      </option>
                    ))}
                  </select>
                </label>
              )}
              <label>
                {technologyLabels[kind]}{" "}
                <input
                  aria-label={row.startsWith("extra-") ? `추가 기술 이름 ${row}` : technologyLabels[kind]}
                  maxLength={80}
                  placeholder="미확인"
                  value={values[row]?.name === "unknown" ? "" : (values[row]?.name ?? "")}
                  onChange={(e) => update(row, { name: e.target.value })}
                />
              </label>
              <label>
                버전{" "}
                <input
                  aria-label={`${row} 버전`}
                  maxLength={80}
                  value={values[row]?.version ?? ""}
                  placeholder="미확인"
                  onChange={(e) => update(row, { version: e.target.value })}
                />
              </label>
              <label>
                컴포넌트{" "}
                <input
                  aria-label={`${row} 컴포넌트`}
                  maxLength={80}
                  placeholder="미확인"
                  value={values[row]?.component ?? ""}
                  onChange={(e) => update(row, { component: e.target.value })}
                />
              </label>
              <label>
                역할{" "}
                <select
                  aria-label={`${row} 역할`}
                  value={values[row]?.component_role ?? "unknown"}
                  onChange={(e) => update(row, { component_role: e.target.value })}
                >
                  {Object.entries(roles).map(([value, label]) => (
                    <option key={value} value={value}>
                      {label}
                    </option>
                  ))}
                </select>
              </label>
              <label>
                확인 수준{" "}
                <select
                  aria-label={`${row} 확인 수준`}
                  value={values[row]?.confidence ?? "unknown"}
                  onChange={(e) => update(row, { confidence: e.target.value })}
                >
                  <option value="unknown">미확인</option>
                  <option value="confirmed">사용자가 확인함</option>
                  <option value="observed">사용자가 관측했다고 제공</option>
                  <option value="inferred">추정</option>
                </select>
              </label>
            </div>
          );
        })}
        <button
          className="btn"
          type="button"
          disabled={rows.length >= 32}
          onClick={() => setRows((old) => [...old, `extra-${old.length}`])}
        >
          다른 컴포넌트 기술 추가
        </button>
        <button
          className="btn"
          type="button"
          onClick={() =>
            onGenerate(
              Object.values(values)
                .filter((t) => t.name.trim() && t.name !== "unknown")
                .map((t) => ({
                  ...t,
                  version: t.version.trim() || "unknown",
                })),
              reviewed ? { ...currentContext, source: "user_provided" } : undefined,
            )
          }
        >
          수정 제안 생성·저장
        </button>
      </fieldset>
    </details>
  );
}
