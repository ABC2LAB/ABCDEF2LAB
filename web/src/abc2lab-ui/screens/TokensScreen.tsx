import { useMemo } from "react";

import { Screen } from "../components/Screen";
import { TopBar } from "../components/TopBar";
import { Strong, Sub } from "../components/ui";
import { useApiResource } from "../lib/api";
import { getTokenUsage, TOKEN_DAILY_DAYS, type TokenUsage } from "../lib/engine";
import { useGo } from "../nav";

function fmt(n: number): string {
  if (n >= 1e9) return `${(n / 1e9).toFixed(1)}B`;
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}K`;
  return String(n);
}
function dayLabel(iso: string): string {
  if (!iso) return "—";
  const parts = /^(\d{4})-(\d{2})-(\d{2})$/.exec(iso);
  if (!parts) return iso;
  return `${Number(parts[2])}/${Number(parts[3])}`;
}

const bodyText = { fontSize: 14, color: "var(--muted)", lineHeight: 1.7, margin: 0 } as const;

/** ===== 토큰 사용량 대시보드 (전역 집계) ===== */
export default function TokensScreen() {
  const go = useGo();
  const { data, loading, error, reload, fetchedAt } = useApiResource<TokenUsage>(getTokenUsage);

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

  let stateMessage: string | null;
  if (loading) {
    stateMessage = "불러오는 중…";
  } else if (error) {
    stateMessage = `토큰 사용량을 불러오지 못했어요. (${error})`;
  } else if (byProfile.length === 0 && daily.length === 0) {
    stateMessage = "아직 집계된 토큰 사용이 없어요.";
  } else {
    stateMessage = null;
  }

  return (
    <Screen id="tokens">
      <div className="frame">
        <TopBar current="settings" />
        <div style={{ padding: "22px 24px" }}>
          <div className="crumb">
            <button className="lnk" type="button" onClick={() => go("settings")}>
              설정
            </button>{" "}
            › <span>토큰 사용량</span>
          </div>
          <h1 className="big" style={{ marginBottom: 4 }}>
            토큰 사용량
          </h1>
          <p className="lead">
            전체 작업에서 기록된 운영 토큰 사용량이에요. 프로필별 누적값과 최근 {TOKEN_DAILY_DAYS}일의 UTC 일자별 집계를
            보여줘요. 같은 회차·조건을 고정한 baseline 비교와 금액 환산은 아직 지원하지 않아요.
          </p>

          <div className="row" style={{ marginBottom: 12 }}>
            <button className="btn" type="button" disabled={loading} onClick={reload}>
              새로고침
            </button>
            <Sub>{fetchedAt ? `조회 ${new Date(fetchedAt).toLocaleTimeString()}` : "조회 미확인"} · 수동 갱신</Sub>
          </div>
          {stateMessage ? (
            <div
              role={error ? "alert" : "status"}
              style={{ textAlign: "center", padding: "40px 0", color: "var(--muted)" }}
            >
              {stateMessage}
            </div>
          ) : (
            <>
              <div className="dgrid" style={{ marginBottom: 20 }}>
                <div className="tile">
                  <div className="k">누적 입력·출력 토큰</div>
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
                  <div className="k">프로필</div>
                  <div className="v">{byProfile.length}</div>
                  <div className="sub">사용된 프로필</div>
                </div>
                <div className="tile">
                  <div className="k">캐시 읽기</div>
                  <div className="v" style={{ color: totals.cacheRead > 0 ? "var(--teal-ink)" : undefined }}>
                    {fmt(totals.cacheRead)}
                  </div>
                  <div className="sub">캐시에서 읽은 토큰</div>
                </div>
              </div>

              <div className="card">
                <h3>
                  프로필별 누적 사용량 <Sub>· 입력+출력 토큰 많은 순</Sub>
                </h3>
                <div className="scrollx">
                  <table className="vtable">
                    <thead>
                      <tr>
                        {["프로필", "호출", "작업", "입력", "출력", "합계"].map((h) => (
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
                  일자별 토큰 <Sub>· 최근 {TOKEN_DAILY_DAYS}일(UTC), 입력+출력 합계</Sub>
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
