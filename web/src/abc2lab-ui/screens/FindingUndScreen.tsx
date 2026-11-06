import { Screen } from "../components/Screen";
import { TopBar } from "../components/TopBar";
import { MockBanner, Pill, Sev, Strong, Sub, VerdictBadge } from "../components/ui";
import { useApiResource } from "../lib/api";
import {
  allEvidenceUnknown,
  COMPARISON_SIGNALS,
  displayVerdict,
  type FindingRecord,
  getFindingHeld,
  heldBeforeSend,
  missingEvidence,
  notTransmitted,
  ORACLE_BADGE,
  SIGNAL_LABEL,
} from "../lib/finding";
import { useGo } from "../nav";

const bodyText = { fontSize: 14, color: "var(--muted)", lineHeight: 1.7, margin: 0 } as const;
const listText = { margin: 0, paddingLeft: 18, fontSize: 14, color: "var(--muted)", lineHeight: 1.8 } as const;

function Meta({ k, v }: { k: string; v: string }) {
  return (
    <div style={{ display: "flex", gap: 8, fontSize: 12, lineHeight: 1.8 }}>
      <span style={{ color: "var(--faint)", flex: "none", minWidth: 96 }}>{k}</span>
      <code style={{ color: "var(--muted)", wordBreak: "break-all" }}>{v}</code>
    </div>
  );
}

/**
 * 판단 불가 발견 본문(상태변경·E² 송신 전 차단 서사). record 하나로 fail-closed 렌더.
 * 실행이 안 됐으므로 어떤 취약 주장도 하지 않는다(미실행 ≠ 취약 없음, 절대규칙 5).
 */
export function FindingUndDetail({ record }: { record: FindingRecord }) {
  const display = displayVerdict(record);
  const dbadge = ORACLE_BADGE[display];
  const held = heldBeforeSend(record); // E² 송신 전 차단(held_pre_send) 에만 true
  const notSent = notTransmitted(record); // held_pre_send | none (전송·응답 없음)
  const allUnknown = allEvidenceUnknown(record); // 세 체크·6 신호 전부 unknown 일 때만 "모두 불명"
  const normalCollected = record.signals.ownerNormalData === "yes"; // 정상 대조(소유자 정상 응답) 수집됐나
  const missing = missingEvidence(record);

  return (
    <>
      <div className="row" style={{ alignItems: "center", gap: 8, marginBottom: 6, flexWrap: "wrap" }}>
        <Sev level={record.severity} />
        {dbadge.verdict ? <VerdictBadge verdict={dbadge.verdict} /> : <Pill tone="muted">{dbadge.label}</Pill>}
        <Pill tone="muted">{record.vulnClass}</Pill>
        {record.stateChanging && <Pill tone="warn">{held ? "상태 변경 · 승인 대기" : "상태 변경"}</Pill>}
      </div>
      <h1 className="big" style={{ marginBottom: 4 }}>
        {record.title}
      </h1>
      <div className="ep" style={{ fontFamily: "var(--mono)", fontSize: 13, color: "var(--muted)" }}>
        {record.resourceRef} <span style={{ color: "var(--faint)" }}>· 논리 자원 참조(raw URL 아님)</span>
      </div>

      {record.intent && (
        <div className="card" style={{ marginTop: 16 }}>
          <h3>확인하려던 것</h3>
          <p style={bodyText}>{record.intent}. 되면 비즈니스 로직 취약점이에요.</p>
        </div>
      )}

      <div className="card" style={{ borderColor: "var(--warn-border)", background: "var(--warn-weak)" }}>
        <h3 style={{ color: "var(--warn)" }}>왜 "판단 불가"인가</h3>
        <p style={{ fontSize: 14, color: "var(--warn)", lineHeight: 1.7, margin: "0 0 10px" }}>
          {held ? (
            <>
              이건 <strong>상태를 바꾸는 요청</strong>이라 전송 통제(E²)가 <strong>보내기 전에 막아뒀어요</strong>.
              사람이 승인하기 전까진 실제로 보내지 않았고, 그래서{" "}
              <strong>취약한지 아닌지 결론을 내리지 못했어요.</strong>
            </>
          ) : notSent ? (
            <>
              대상에 <strong>실제 전송이 없어</strong>(미실행) 응답 증거가 없고, 그래서 결론을 내리지 못했어요.
            </>
          ) : (
            <>증거가 부족해 결론을 내리지 못했어요. 아래 미확인 항목을 채워야 판정이 갈려요.</>
          )}
        </p>
        <div className="hint" style={{ color: "var(--warn)", marginTop: 0 }}>
          미실행·판단 불가를 "취약점 없음"·"안전"으로 적지 않아요(절대규칙 5). 확인 못 한 건 확인 못 했다고 남겨요.
        </div>
      </div>

      <div className="card">
        <h3>부족한 증거</h3>
        <ul style={listText}>
          {held ? (
            <li>실제 전송·응답이 없음(E²가 송신 전 차단, 승인 대기)</li>
          ) : notSent ? (
            <li>실제 전송·응답이 없음(미실행)</li>
          ) : null}
          {missing.map((m) => (
            <li key={m}>{m}</li>
          ))}
        </ul>
        <div className="hint">
          {allUnknown ? (
            <>
              세 체크·6 비교신호가 모두 <Strong>불명</Strong>이에요(실행 안 됨).{" "}
            </>
          ) : (
            <>충족된 신호는 아래 통제변수의 신호 상태로 확인하세요. </>
          )}
          GET이라도 상태 변경이 없다고 가정하지 않아요.
          {!normalCollected && " 정상 대조(소유자 정상 응답)도 아직 확인되지 않았어요."}
        </div>
      </div>

      <div className="card">
        <h3>
          통제변수 <Sub>· 재현·감사</Sub>
        </h3>
        <Meta k="오라클 원값" v={record.oracleValue} />
        <Meta k="전송 상태" v={record.transmission ?? "unknown"} />
        <Meta k="정책 버전" v={record.control.policyVersion} />
        <Meta k="reset 세대" v={record.control.resetGeneration} />
        <Meta k="관찰 시각" v={new Date(record.control.observedAt).toISOString()} />
        <Meta k="모델 태그" v={record.control.modelTag} />
        <Meta k="사유 코드" v={record.reasonCodes.join(" · ") || "—"} />
        <div className="hint" style={{ marginTop: 8 }}>
          신호 상태: {COMPARISON_SIGNALS.map((s) => `${s.label} ${SIGNAL_LABEL[record.signals[s.key]]}`).join(" · ")}
        </div>
      </div>
    </>
  );
}

/** ===== 발견 상세 · 판단 불가(상태변경·E² 차단) ===== */
export default function FindingUndScreen() {
  const go = useGo();
  const { data, loading, error } = useApiResource(() => getFindingHeld(null), []);

  return (
    <Screen id="finding-und">
      <div className="frame">
        <TopBar current="history" />
        <div style={{ padding: "22px 24px", maxWidth: 760 }}>
          <div className="crumb">
            {/* biome-ignore lint/a11y/useValidAnchor: 시안 crumb 링크(href 로 키보드 이동 가능) */}
            <a
              href="#history"
              onClick={(e) => {
                e.preventDefault();
                go("history");
              }}
            >
              점검 기록
            </a>{" "}
            › {/* biome-ignore lint/a11y/useValidAnchor: 시안 crumb 링크(href 로 키보드 이동 가능) */}
            <a
              href="#report"
              onClick={(e) => {
                e.preventDefault();
                go("report");
              }}
            >
              보고서
            </a>{" "}
            › <span>발견</span>
          </div>

          <MockBanner>
            판단 불가 상세는 예시예요. 실제 판정(확정/의심/판단 불가)과 증거는 판정 레코드(E③ 오라클)·전송 통제(E²) 연동
            후 채워져요. <Strong>구조·마스킹은 실제와 같은 계약</Strong>이라 연동 시 그대로 살아요.
          </MockBanner>

          {loading || !data ? (
            <div style={{ textAlign: "center", padding: "40px 0", color: "var(--muted)" }}>
              {error ? `불러오지 못했어요. (${error})` : "불러오는 중…"}
            </div>
          ) : (
            <>
              <FindingUndDetail record={data} />
              <div className="card" style={{ marginBottom: 0 }}>
                <h3>어떻게 확정하나</h3>
                <p style={bodyText}>
                  리셋 가능한 <Strong>격리 시험환경</Strong>에서 승인 후 재현하면, 상태 변경이 실제로 일어나는지로
                  확정/정상이 갈려요(상태 변경은 조회 경로와 분리, 승인 시험환경).
                </p>
                <div className="row" style={{ marginTop: 12, flexWrap: "wrap" }}>
                  <button className="btn primary" type="button" onClick={() => go("approval")}>
                    승인하고 격리환경에서 확인 →
                  </button>
                  <button className="btn" type="button" onClick={() => go("report")}>
                    보고서로 돌아가기
                  </button>
                </div>
              </div>
            </>
          )}
        </div>
      </div>
    </Screen>
  );
}
