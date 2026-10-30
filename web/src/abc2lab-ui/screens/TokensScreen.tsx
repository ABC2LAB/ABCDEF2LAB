import { useMemo } from "react";
import { Screen } from "../components/Screen";
import { TopBar } from "../components/TopBar";
import { Strong, Sub } from "../components/ui";
import { useApiResource } from "../lib/api";
import { type TokenUsage, getTokenUsage } from "../lib/engine";
import { useGo } from "../nav";

function fmt(n: number): string {
  if (n >= 1e9) return `${(n / 1e9).toFixed(1)}B`;
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}K`;
  return String(n);
}
function dayLabel(iso: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return `${d.getMonth() + 1}/${d.getDate()}`;
}

const bodyText = { fontSize: 13.5, color: "var(--muted)", lineHeight: 1.7, margin: 0 } as const;

/** ===== 토큰 · 비용 대시보드 (전역 집계) ===== */
export default function TokensScreen() {
  const go = useGo();
  const { data, loading, error } = useApiResource<TokenUsage>(getTokenUsage);

  const byProfile = useMemo(
    () => [...(data?.byProfile ?? [])].sort((a, b) => b.input + b.output - (a.input + a.output)),
    [data],
  );
  const daily = useMemo(() => [...(data?.daily ?? [])].sort((a, b) => a.day.localeCompare(b.day)), [data]);

  const totals = useMemo(() => {
    let input = 0;
    let output = 0;
    let calls = 0;
    let cacheRead = 0;
    for (const p of byProfile) {
      input += p.input;
      output += p.output;
      calls += p.calls;
      cacheRead += p.cacheRead;
    }
    return { input, output, calls, cacheRead, total: input + output };
  }, [byProfile]);

  const dailyMax = Math.max(1, ...daily.map((d) => d.input + d.output));

  return (
    <Screen id="tokens">
      <div className="frame">
        <TopBar current="settings" />
        <div style={{ padding: "22px 24px" }}>
          <div className="crumb">
            {/* biome-ignore lint/a11y/useValidAnchor: 원본 시안 crumb 이동(앵커 스타일 유지) */}
            <a onClick={() => go("settings")}>설정</a> › <span>토큰 · 비용</span>
          </div>
          <h1 className="big" style={{ marginBottom: 4 }}>
            토큰 · 비용
          </h1>
          <p className="lead">
            점검에 쓴 LLM 토큰을 모델별 · 일자별로 모아 봐요. baseline(원본) 대비 우리 개선의 <Strong>비용 효율</Strong>
            을 재는 근거예요.
          </p>

          {loading ? (
            <div style={{ textAlign: "center", padding: "40px 0", color: "var(--muted)" }}>불러오는 중…</div>
          ) : error ? (
            <div style={{ textAlign: "center", padding: "40px 0", color: "var(--muted)" }}>
              토큰 사용량을 불러오지 못했어요. ({error})
            </div>
          ) : byProfile.length === 0 ? (
            <div style={{ textAlign: "center", padding: "40px 0", color: "var(--muted)" }}>
              아직 집계된 토큰 사용이 없어요.
            </div>
          ) : (
            <>
              <div className="dgrid" style={{ marginBottom: 20 }}>
                <div className="tile">
                  <div className="k">총 토큰</div>
                  <div className="v">{fmt(totals.total)}</div>
                  <div className="sub">
                    입력 {fmt(totals.input)} · 출력 {fmt(totals.output)}
                  </div>
                </div>
                <div className="tile">
                  <div className="k">호출</div>
                  <div className="v">{fmt(totals.calls)}</div>
                  <div className="sub">LLM 요청 수</div>
                </div>
                <div className="tile">
                  <div className="k">모델</div>
                  <div className="v">{byProfile.length}</div>
                  <div className="sub">사용된 프로필</div>
                </div>
                <div className="tile">
                  <div className="k">캐시 읽기</div>
                  <div className="v" style={{ color: totals.cacheRead > 0 ? "var(--teal-ink)" : undefined }}>
                    {fmt(totals.cacheRead)}
                  </div>
                  <div className="sub">재사용 토큰(절감)</div>
                </div>
              </div>

              <div className="card">
                <h3>
                  모델별 사용량 <Sub>· 토큰 많은 순</Sub>
                </h3>
                <div className="scrollx">
                  <table className="vtable">
                    <thead>
                      <tr>
                        {["모델", "호출", "작업", "입력", "출력", "합계"].map((h) => (
                          <th key={h}>{h}</th>
                        ))}
                      </tr>
                    </thead>
                    <tbody>
                      {byProfile.map((p) => (
                        <tr key={p.profileName}>
                          <td className="cat">{p.profileName}</td>
                          <td className="num">{fmt(p.calls)}</td>
                          <td className="num">{p.tasks}</td>
                          <td className="num">{fmt(p.input)}</td>
                          <td className="num">{fmt(p.output)}</td>
                          <td className="num">
                            <b>{fmt(p.input + p.output)}</b>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </div>

              <div className="card" style={{ marginBottom: 0 }}>
                <h3>
                  일자별 토큰 <Sub>· 입력+출력 합계</Sub>
                </h3>
                {daily.length === 0 ? (
                  <p style={bodyText}>일자별 집계가 없어요.</p>
                ) : (
                  <div className="vbars">
                    {daily.map((d) => {
                      const sum = d.input + d.output;
                      return (
                        <div key={d.day} className="vrow" style={{ cursor: "default" }}>
                          <span className="vr-lab">{dayLabel(d.day)}</span>
                          <span className="vr-track">
                            <span
                              className="vr-fill"
                              style={{ width: `${(sum / dailyMax) * 100}%`, background: "var(--teal)" }}
                            />
                          </span>
                          <span className="vr-val">
                            <b>{fmt(sum)}</b>
                          </span>
                        </div>
                      );
                    })}
                  </div>
                )}
                <p className="hint" style={{ marginTop: 12 }}>
                  엔진이 금액(단가)을 제공하지 않아 <Strong>토큰량만</Strong> 보여줘요. 비용 환산은 모델 단가 연동
                  후예요.
                </p>
              </div>
            </>
          )}
        </div>
      </div>
    </Screen>
  );
}
