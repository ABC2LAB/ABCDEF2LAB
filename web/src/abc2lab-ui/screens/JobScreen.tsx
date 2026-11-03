import { useState } from "react";

import { ChoiceGroup } from "../components/Choice";
import { Screen } from "../components/Screen";
import { Stepper } from "../components/Stepper";
import { CancelLink, TopBar } from "../components/TopBar";
import { MockBanner, Pill } from "../components/ui";
import { useGo } from "../nav";
import type { Target } from "../types";

/** 엔진 기본 워커 수 (server/manager.go defaultWorkers) */
const DEFAULT_WORKERS = 3;
const WORKER_OPTIONS = [1, 2, 3, 4, 6, 8];

/** ===== 작업 설정 ===== */
export default function JobScreen({ target }: { target: Target }) {
  const go = useGo();
  const [workers, setWorkers] = useState(DEFAULT_WORKERS);
  return (
    <Screen id="job">
      <div className="frame">
        <TopBar right={<CancelLink />} />
        <Stepper current={3} />
        <div style={{ padding: "0 24px" }}>
          <MockBanner>작업 설정 흐름은 시안이에요. 실제 점검 생성·시작은 엔진 실행 연결 후예요.</MockBanner>
        </div>
        <div className="wrap">
          <div className="col">
            <h1 className="big">거의 다 됐어요</h1>
            <p className="lead">작업 이름만 정하면 바로 시작할 수 있어요. 리소스는 필요할 때만 조정하면 돼요.</p>
            <div className="stack">
              <div>
                <label className="fl" htmlFor="j-name">
                  이름 <span style={{ color: "var(--teal)" }}>*</span>
                </label>
                <input className="inp" id="j-name" placeholder="예: shop 정기 점검" />
              </div>
              <div>
                <label className="fl" htmlFor="j-goal">
                  목표 <span className="opt">· 선택</span>
                </label>
                <input className="inp" id="j-goal" placeholder="예: 로그인 후 주문·결제 흐름의 권한 점검" />
              </div>
            </div>
            <details className="adv" open style={{ marginTop: 20 }}>
              <summary>
                <span className="chev">▸</span> 리소스 (범위 · 워커 · 제외 경로){" "}
                <span style={{ fontWeight: 500, color: "var(--muted)", fontSize: 13 }}>· 기본값으로 둬도 돼요</span>
              </summary>
              <div style={{ padding: "8px 0 4px" }}>
                <span className="fsmall" style={{ fontSize: 14 }}>
                  점검 범위
                </span>
                <ChoiceGroup
                  defaultValue={0}
                  style={{ marginBottom: 18 }}
                  options={[
                    { t: "표준", d: "주요 엔드포인트·로그인 흐름 중심" },
                    { t: "심화", d: "전체 엔드포인트·입력 변형·다단계 흐름까지" },
                    { t: "제한 없음", d: "새로 찾을 게 없을 때까지 · 자동 종료" },
                  ]}
                />
                <span className="fsmall" style={{ fontSize: 14 }}>
                  워커 수 <span className="opt">· 이번 점검만</span>
                </span>
                <div className="seg wk-seg" role="group" aria-label="워커 수">
                  {WORKER_OPTIONS.map((n) => (
                    <button key={n} type="button" aria-pressed={workers === n} onClick={() => setWorkers(n)}>
                      {n}
                      {n === DEFAULT_WORKERS && " (기본)"}
                    </button>
                  ))}
                </div>
                <div className="hint" style={{ marginBottom: 18 }}>
                  LLM이 동시에 처리하는 작업 수예요. 늘리면 GPU 부하와 토큰 사용량이 늘어나요. 대상 서버 요청 속도는
                  전송 통제가 제한하므로 워커 수와 관계없어요.
                </div>
                <div className="hr" style={{ margin: "4px 0 16px" }} />
                <span className="fsmall" style={{ fontSize: 14 }}>
                  제외 경로 <span className="opt">· 선택</span>
                </span>
                <input className="inp sm" placeholder="예: /admin/delete* , /api/*/purge" style={{ marginBottom: 8 }} />
                <div
                  className="hint"
                  style={{
                    color: "var(--danger)",
                    background: "var(--danger-weak)",
                    border: "1px solid var(--danger-border)",
                    borderRadius: 6,
                    padding: "11px 13px",
                    marginTop: 8,
                    lineHeight: 1.7,
                  }}
                >
                  여기 적은 경로는 <strong>절대 요청하지 않아요.</strong> 되돌릴 수 없는 작업(삭제 · 초기화)이 있는 곳을
                  넣으세요. 모든 점검에 공통으로 걸 규칙은 설정 → 통제 정책에서 관리해요.
                </div>
              </div>
            </details>
          </div>

          {/* 원본 시안 그대로 고정 요약 — 실제 연동 시 앞 단계 값으로 바꾸면 돼요 */}
          <aside className="side">
            <h2>점검 요약</h2>
            <div className="srow">
              <div className="k">대상</div>
              <div className="v">{target.host}</div>
            </div>
            <div className="srow">
              <div className="k">인증</div>
              <div className="v">로그인 점검 포함</div>
            </div>
            <div className="srow">
              <div className="k">LLM</div>
              <div className="v">
                로컬 · ollama <Pill>내부 주소</Pill>
              </div>
            </div>
            <div className="srow">
              <div className="k">모델</div>
              <div className="v">qwen2.5:32b</div>
            </div>
            <div className="hr" style={{ margin: "14px 0" }} />
            <div className="srow">
              <div className="k">범위 · 워커</div>
              <div className="v">표준 · {workers}</div>
            </div>
            <div className="srow">
              <div className="k">상태 변경</div>
              <div className="v">
                승인 필요 <Pill>기본</Pill>
              </div>
            </div>
            <div className="row" style={{ marginTop: 18 }}>
              <button className="btn" type="button" onClick={() => go("target")}>
                이전
              </button>
              <button className="btn primary" type="button" onClick={() => go("scope")} style={{ flex: 1 }}>
                점검 시작
              </button>
            </div>
          </aside>
        </div>
      </div>
    </Screen>
  );
}
