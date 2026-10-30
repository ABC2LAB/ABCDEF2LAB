import { useMemo } from "react";
import { Screen } from "../components/Screen";
import { TopBar } from "../components/TopBar";
import { Pill, Strong, Sub } from "../components/ui";
import { useApiResource } from "../lib/api";
import { type EngineInterceptRule, getInterceptRules, getLLM } from "../lib/engine";
import { isInternalUrl } from "../lib/net";
import { useGo } from "../nav";

const bodyText = { fontSize: 13.5, color: "var(--muted)", lineHeight: 1.7, margin: 0 } as const;
const noticeBox = {
  fontSize: 12.5,
  color: "var(--muted)",
  background: "var(--surface-2)",
  border: "1px dashed var(--line-2)",
  borderRadius: 8,
  padding: "10px 12px",
} as const;
const patStyle = {
  fontFamily: "var(--mono)",
  fontSize: 11.5,
  wordBreak: "break-all" as const,
  color: "var(--muted)",
};

/** base_url 의 **호스트**가 내부망/로컬을 가리키는지 판별 (완전 로컬 모드 표시용).
 *  전체 URL 문자열이 아니라 hostname 기준 — 쿼리에 'ollama' 가 있다고 로컬로 보지 않는다. */
function isLocalUrl(u: string): boolean {
  if (isInternalUrl(u) === true) return true; // localhost · 사설 IP · IPv6 루프백/ULA 등
  let h: string;
  try {
    h = new URL(u).hostname.replace(/^\[/, "").replace(/\]$/, "");
  } catch {
    return false;
  }
  return /^(ollama|host\.docker\.internal)$/i.test(h);
}

/** ===== 설정 · 통제 정책 ===== */
export default function SettingsScreen() {
  const go = useGo();
  // 통제 규칙·모델은 전역(task 무관) → 마운트 시 1회 조회
  const rulesRes = useApiResource(getInterceptRules);
  const llmRes = useApiResource(getLLM);

  const rules = useMemo<EngineInterceptRule[]>(() => rulesRes.data ?? [], [rulesRes.data]);
  const llm = llmRes.data;

  const ruleStats = useMemo(() => {
    const on = rules.filter((r) => r.enabled).length;
    const deny = rules.filter((r) => r.action === "deny").length;
    return { total: rules.length, on, deny };
  }, [rules]);

  return (
    <Screen id="settings">
      <div className="frame">
        <TopBar current="settings" />
        <div style={{ padding: "22px 24px", maxWidth: 780 }}>
          <h1 className="big">설정 · 통제 정책</h1>
          <p className="lead">
            실행 허용은 LLM이 아니라 <Strong>통제 규칙</Strong>이 정해요. 지금은 ARTEX 기본{" "}
            <Strong>승인 게이트(intercept)</Strong>가 에이전트 도구 호출을 거릅니다. 우리{" "}
            <Strong>전송 통제(E², 네트워크 경계)</Strong>와 변조 탐지 감사는 아직 <Strong>연동 전</Strong>이에요.
          </p>

          {/* ── 활성 통제 규칙 (E²) — 실데이터 ── */}
          <div className="card">
            <h3>
              활성 통제 규칙 <Sub>· ARTEX 승인 게이트(intercept)가 도구 호출을 거르는 규칙</Sub>
            </h3>

            {rulesRes.loading ? (
              <p style={bodyText}>불러오는 중…</p>
            ) : rulesRes.error ? (
              <p style={bodyText}>통제 규칙을 불러오지 못했어요. ({rulesRes.error})</p>
            ) : rules.length === 0 ? (
              <p style={bodyText}>등록된 통제 규칙이 없어요.</p>
            ) : (
              <>
                <div className="search" style={{ gap: 8, marginBottom: 10 }}>
                  <Pill tone="muted">전체 {ruleStats.total}</Pill>
                  <Pill>사용 중 {ruleStats.on}</Pill>
                  {ruleStats.deny > 0 && <Pill tone="warn">차단 {ruleStats.deny}</Pill>}
                </div>
                {rules.map((r) => {
                  const deny = r.action === "deny";
                  return (
                    <div key={r.id} className="frow" style={{ cursor: "default" }}>
                      <Pill tone={r.enabled ? undefined : "muted"}>{r.enabled ? "사용" : "꺼짐"}</Pill>
                      <div style={{ minWidth: 0 }}>
                        <div className="nm">{r.name}</div>
                        <div className="ep" style={patStyle}>
                          {r.match_target} · {r.match_type} · {r.pattern}
                        </div>
                      </div>
                      <div className="rt">
                        <span
                          className="badge"
                          style={{
                            padding: "5px 10px",
                            color: deny ? "var(--danger)" : "var(--teal-ink)",
                            background: deny ? "var(--danger-weak)" : undefined,
                          }}
                        >
                          {deny ? "차단" : "허용"}
                        </span>
                      </div>
                    </div>
                  );
                })}
                <div className="hint">
                  ARTEX 내장 기본 규칙이에요. 규칙 <Strong>추가·수정</Strong>은 우리 전송 통제(E²) 연동 후 열려요(그
                  전엔 읽기 전용).
                </div>
              </>
            )}
          </div>

          {/* ── 모델 — 실데이터 (/api/llm) ── */}
          <div className="card">
            <h3>
              모델 <Sub>· 어떤 LLM으로 도는가</Sub>
            </h3>
            {llmRes.loading ? (
              <p style={bodyText}>불러오는 중…</p>
            ) : llmRes.error ? (
              <p style={bodyText}>모델 설정을 불러오지 못했어요. ({llmRes.error})</p>
            ) : !llm?.configured ? (
              <>
                <div className="frow" style={{ cursor: "default" }}>
                  <Pill tone="muted">미설정</Pill>
                  <div style={{ minWidth: 0 }}>
                    <div className="nm">아직 모델이 연결되지 않았어요</div>
                    <div className="ep">
                      온프레: 로컬(Ollama) <code>ARTEX_LLM_BASE_URL</code> · 비민감: 외부 API
                    </div>
                  </div>
                </div>
                <div className="hint">
                  모델을 붙여야 점검(탐색)이 돌아요. 완전 로컬 모드의 외부 송신 차단은 E² 연동 후예요.
                </div>
              </>
            ) : (
              <div className="frow" style={{ cursor: "default" }}>
                <Pill>{isLocalUrl(llm.base_url) ? "내부" : "외부"}</Pill>
                <div style={{ minWidth: 0 }}>
                  <div className="nm">
                    {llm.provider} · {llm.model || "(모델명 미지정)"}
                  </div>
                  <div className="ep">
                    {llm.base_url || "(base_url 없음)"}
                    {llm.key_set ? " · 키 설정됨" : ""}
                    {llm.context_window_k ? ` · ctx ${llm.context_window_k}k` : ""}
                  </div>
                </div>
                <div className="rt">
                  <span
                    className="badge"
                    style={{
                      padding: "5px 10px",
                      color: isLocalUrl(llm.base_url) ? "var(--teal-ink)" : "var(--warn)",
                    }}
                  >
                    {isLocalUrl(llm.base_url) ? "내부 주소" : "외부 주소"}
                  </span>
                </div>
              </div>
            )}
          </div>

          {/* ── 권한 · 기록 ── */}
          <div className="card">
            <h3>권한 · 기록</h3>
            <div className="setrow" style={{ borderTop: "none" }}>
              <div className="tx">
                <div className="t">감사 로그</div>
                <div className="d">
                  통제가 차단했거나 승인 대기로 잡은 요청이 기록돼요. (변조 탐지 체인·권한 분리는 우리가 얹는 기여 —
                  연동 후.)
                </div>
              </div>
              <button
                className="btn"
                type="button"
                onClick={() => go("audit")}
                style={{ flex: "none", padding: "10px 14px", fontSize: 13 }}
              >
                열기
              </button>
            </div>
            <div className="setrow">
              <div className="tx">
                <div className="t">토큰 · 비용</div>
                <div className="d">모델별 · 일자별 토큰 사용량(실데이터). baseline 대비 비용 효율 측정 근거.</div>
              </div>
              <button
                className="btn"
                type="button"
                onClick={() => go("tokens")}
                style={{ flex: "none", padding: "10px 14px", fontSize: 13 }}
              >
                열기
              </button>
            </div>
          </div>

          {/* ── 준비 중 (E² 연동 예정) ── */}
          <div className="card" style={{ marginBottom: 0 }}>
            <h3>
              준비 중 <Sub>· 전송 통제(E²) 연동 후 열려요</Sub>
            </h3>
            <div style={noticeBox}>
              아래는 통제 UI로 설계만 됐고 아직 엔진에 안 붙은 항목이에요. 실데이터인 척하지 않으려고 비워 뒀어요.
            </div>
            <ul style={{ ...bodyText, paddingLeft: 18, marginTop: 12 }}>
              <li>
                <Strong>대상 주소 허용 목록 · 소유 확인</Strong> — 검증된 origin만 요청 (전용 라우트 없음)
              </li>
              <li>
                <Strong>안전 스위치</Strong> — 완전 로컬 모드 / 상태 변경 승인 필요 / canary 사용
              </li>
              <li>
                <Strong>kill-switch</Strong> — 모든 점검 즉시 중지
              </li>
            </ul>
          </div>
        </div>
      </div>
    </Screen>
  );
}
