import { useState } from 'react';
import { Choice, ChoiceGroup } from '../components/Choice';
import { Screen } from '../components/Screen';
import { Stepper } from '../components/Stepper';
import { CancelLink, TopBar } from '../components/TopBar';
import { MockBanner, Strong } from '../components/ui';
import { isInternalUrl } from '../lib/net';
import { useGo } from '../nav';
import type { Target } from '../types';

type LlmMode = 'local' | 'external';
type EndpointBadge = { ok: boolean; text: string };

const DEFAULT_BADGE: EndpointBadge = { ok: true, text: '✓ 기본 내부 LLM 연결됨 · 외부 송신 0' };

/** ===== 점검 방식 ===== */
export default function TargetScreen({ target }: { target: Target }) {
  const go = useGo();

  // 인증 영역 점검 여부 (null = 아직 안 고름)
  const [auth, setAuth] = useState<boolean | null>(null);
  // 로그인 방법: 0 = 아이디·비밀번호, 1 = 세션 직접 입력
  const [authMethod, setAuthMethod] = useState(0);
  const [accountCount, setAccountCount] = useState(2);

  // LLM 실행 위치
  const [llm, setLlm] = useState<LlmMode | null>(null);
  const [endpoint, setEndpoint] = useState('http://ollama:11434/v1');
  const [endpointBadge, setEndpointBadge] = useState<EndpointBadge>(DEFAULT_BADGE);
  const [localModel, setLocalModel] = useState('');

  const testEndpoint = () => {
    const ok = isInternalUrl(endpoint.trim());
    setEndpointBadge(
      ok
        ? { ok: true, text: '✓ 내부 주소 확인됨 · 외부 송신 0' }
        : { ok: false, text: '⚠ 공인 IP · 외부로 나갑니다 — 완전 로컬 모드에서는 거부' },
    );
  };

  // 오른쪽 요약 (원본 sync())
  let localHost = 'ollama';
  try {
    localHost = new URL(endpoint.trim()).hostname;
  } catch {
    /* 주소가 아니면 기본값 */
  }
  const summaryAuth = auth === null ? '—' : auth ? '로그인 점검 포함' : '공개 영역만';
  const summaryLlm =
    llm === 'local' ? (
      <>
        로컬 · {localHost}
        {endpointBadge.ok && (
          <>
            {' '}
            <span className="pill">외부 송신 0</span>
          </>
        )}
      </>
    ) : llm === 'external' ? (
      '외부 API'
    ) : (
      '—'
    );
  const summaryModel = llm === 'local' ? localModel || '(모델 미지정)' : llm === 'external' ? '(외부 모델)' : '—';

  return (
    <Screen id="target">
      <div className="frame">
        <TopBar right={<CancelLink />} />
        <Stepper current={2} />
        <div style={{ padding: '0 24px' }}>
          <MockBanner>대상·모델 설정 흐름은 시안이에요. 모델 목록·연결은 엔진 연동 후, 평가 태그는 코드에 박지 않아요.</MockBanner>
        </div>
        <div className="wrap">
          <div className="col">
            <h1 className="big">어떻게 점검할까요?</h1>
            <p className="lead">로그인 접근과 분석 엔진, 알림을 정해 주세요.</p>

            {/* 인증 */}
            <div className="qlab">인증 · 로그인이 필요한 영역도 점검할까요?</div>
            <div className="choices" style={{ marginBottom: 0 }}>
              <Choice
                t="네, 계정 정보를 줄게요"
                d="로그인 뒤 영역까지 점검"
                on={auth === true}
                onClick={() => setAuth(true)}
              />
              <Choice
                t="아니요, 공개 영역만"
                d="로그인 없이 보이는 곳만"
                on={auth === false}
                onClick={() => setAuth(false)}
              />
            </div>
            <div
              className="panel"
              hidden={auth !== true}
              style={{ marginTop: 12, marginBottom: 22 }}
            >
              <div className="hint" style={{ marginTop: 0, marginBottom: 14 }}>
                로그인 뒤 영역을 점검하려면 <Strong>테스트 계정</Strong>이 필요해요. 계정을{' '}
                <Strong>둘 이상</Strong> 주면 서로의 자원에 접근되는지(
                <Strong>권한 경계 · IDOR</Strong>)까지 점검해요.
              </div>
              <span className="fsmall" style={{ fontSize: 13 }}>
                로그인 방법
              </span>
              <ChoiceGroup
                defaultValue={0}
                onChange={setAuthMethod}
                style={{ marginBottom: 16 }}
                options={[
                  { t: '아이디 · 비밀번호', d: '폼 로그인을 자동 수행' },
                  { t: '세션 직접 입력', d: '쿠키 · 토큰을 붙여넣기' },
                ]}
              />
              <div hidden={authMethod === 1}>
                <div>
                  {Array.from({ length: accountCount }, (_, i) => (
                    <div className="acct" key={i}>
                      <div className="aclab">
                        계정 {String.fromCharCode(65 + i)}
                        {i === 1 && (
                          <>
                            {' '}
                            <span style={{ fontWeight: 500, color: 'var(--muted)', fontSize: 11 }}>
                              · 권한 경계(IDOR) 점검용
                            </span>
                          </>
                        )}
                      </div>
                      <div className="row" style={{ gap: 10, flexWrap: 'wrap' }}>
                        <input
                          className="inp"
                          style={{ flex: '1 1 140px' }}
                          placeholder="테스트 아이디"
                        />
                        <input
                          className="inp"
                          style={{ flex: '1 1 140px' }}
                          type="password"
                          placeholder="비밀번호"
                        />
                      </div>
                    </div>
                  ))}
                </div>
                <button
                  className="btn"
                  type="button"
                  onClick={() => setAccountCount((n) => n + 1)}
                  style={{ marginTop: 12, width: '100%', borderStyle: 'dashed' }}
                >
                  ＋ 계정 추가
                </button>
              </div>
              <div hidden={authMethod !== 1}>
                <div className="acct">
                  <div className="aclab">계정 A · 세션</div>
                  <input className="inp" placeholder="Cookie 또는 Authorization 헤더 값 붙여넣기" />
                </div>
                <div className="acct">
                  <div className="aclab">
                    계정 B · 세션{' '}
                    <span style={{ fontWeight: 500, color: 'var(--muted)', fontSize: 11 }}>
                      · 권한 경계(IDOR)용
                    </span>
                  </div>
                  <input className="inp" placeholder="Cookie 또는 Authorization 헤더 값 붙여넣기" />
                </div>
                <div className="hint">
                  폼 로그인이 안 되는 SSO · OTP · 토큰 기반 앱이면 로그인된 세션 값을 그대로
                  붙여넣으세요. 저장 시 가려지고 로그 · 내보내기엔 안 나가요.
                </div>
              </div>
            </div>

            <div className="hr" />

            {/* LLM */}
            <div className="qlab">진단에 쓸 LLM을 어디서 실행할까요?</div>
            <div className="choices" style={{ marginBottom: 16 }}>
              <Choice
                t="로컬 실행"
                d="내 장비 / 내 내부망"
                on={llm === 'local'}
                onClick={() => setLlm('local')}
              />
              <Choice
                t="외부 API"
                d="OpenAI · Anthropic 등 API 키"
                on={llm === 'external'}
                onClick={() => setLlm('external')}
              />
            </div>

            <div className="panel" hidden={llm !== 'local'}>
              <div
                className={endpointBadge.ok ? 'badge ok' : 'badge bad'}
                style={{ marginBottom: 16 }}
              >
                {endpointBadge.text}
              </div>
              <label className="fl" htmlFor="l-model">
                모델
              </label>
              <div className="row" style={{ marginBottom: 8 }}>
                <select
                  className="inp"
                  id="l-model"
                  value={localModel}
                  onChange={(e) => setLocalModel(e.target.value)}
                >
                  <option value="">(연동 후 설치된 모델 목록)</option>
                </select>
                <button
                  className="btn"
                  type="button"
                  aria-label="새로고침"
                  style={{ padding: '0 15px' }}
                >
                  ↻
                </button>
              </div>
              <div className="hint">
                엔진의 <code>/v1/models</code> 연동 후 설치된 모델이 떠요(평가 모델 태그는 코드에 박지 않아요).
              </div>
              <details className="adv">
                <summary>
                  <span className="chev">▸</span> 다른 내부 LLM 지정
                </summary>
                <div style={{ paddingBottom: 12 }}>
                  <label className="fl" htmlFor="ep">
                    엔드포인트
                  </label>
                  <div className="row" style={{ marginBottom: 8 }}>
                    <input
                      className="inp"
                      id="ep"
                      type="url"
                      value={endpoint}
                      onChange={(e) => setEndpoint(e.target.value)}
                      spellCheck={false}
                    />
                    <button className="btn" type="button" onClick={testEndpoint}>
                      연결 테스트
                    </button>
                  </div>
                  <div className="hint">
                    조직 내부에 이미 LLM이 있으면 그 주소를 넣으세요(Ollama · vLLM · LM Studio …).{' '}
                    <Strong>내부망 · 로컬 주소만 허용</Strong>돼요.
                  </div>
                </div>
              </details>
              <details className="adv">
                <summary>
                  <span className="chev">▸</span> 고급 설정 (형식 · 속도 제한 · 컨텍스트 · 프록시)
                </summary>
                <div style={{ paddingBottom: 4 }}>
                  <div className="grid3" style={{ marginBottom: 14 }}>
                    <div>
                      <span className="fsmall">형식</span>
                      <select className="inp sm">
                        <option>OpenAI 호환 (기본)</option>
                        <option>Anthropic</option>
                      </select>
                    </div>
                    <div>
                      <span className="fsmall">초당 제한</span>
                      <input className="inp sm" placeholder="예: 5" />
                    </div>
                    <div>
                      <span className="fsmall">분당 제한</span>
                      <input className="inp sm" placeholder="예: 100" />
                    </div>
                  </div>
                  <span className="fsmall">컨텍스트 창 (K)</span>
                  <input className="inp sm" placeholder="예: 200" style={{ marginBottom: 14 }} />
                  <span className="fsmall">
                    프록시 <span className="opt">· 선택</span>
                  </span>
                  <input className="inp sm" placeholder="socks5://user:pass@127.0.0.1:1080" />
                  <div className="hint">
                    로컬은 API 키가 필요 없어요. 서버가 토큰을 요구할 때만 추가하세요.
                  </div>
                </div>
              </details>
            </div>

            <div className="panel api" hidden={llm !== 'external'}>
              <div className="warnbox">
                <span style={{ flex: 'none' }}>⚠</span>
                <div>
                  <strong style={{ fontWeight: 700 }}>
                    대상 데이터 · 프롬프트가 외부 제공자로 전송됩니다.
                  </strong>{' '}
                  폐쇄망 · 민감 환경에는 로컬 실행을 쓰세요.
                </div>
              </div>
              <label className="fl" htmlFor="e-prov">
                제공자
              </label>
              <select className="inp" id="e-prov" style={{ marginBottom: 16 }}>
                <option>OpenAI</option>
                <option>Anthropic</option>
                <option>OpenAI 호환</option>
              </select>
              <label className="fl" htmlFor="e-key">
                API 키
              </label>
              <input
                className="inp"
                id="e-key"
                type="password"
                defaultValue="sk-placeholder-key-0000"
                style={{ marginBottom: 8 }}
              />
              <div className="hint" style={{ marginBottom: 16 }}>
                저장 시 가려지고 로그 · 내보내기엔 안 나갑니다.
              </div>
              <label className="fl" htmlFor="e-model">
                모델
              </label>
              <input className="inp" id="e-model" placeholder="모델 id(연동 후)" />
              <details className="adv w">
                <summary>
                  <span className="chev">▸</span> 고급 설정 (Base URL · 속도 제한 · 컨텍스트 ·
                  프록시 · 세션 헤더)
                </summary>
                <div style={{ paddingBottom: 4 }}>
                  <span className="fsmall">
                    Base URL <span className="opt">· 게이트웨이 쓸 때만</span>
                  </span>
                  <input
                    className="inp sm"
                    placeholder="https://api.openai.com/v1"
                    style={{ marginBottom: 14 }}
                  />
                  <div className="grid3" style={{ marginBottom: 14 }}>
                    <div>
                      <span className="fsmall">초당 제한</span>
                      <input className="inp sm" placeholder="예: 5" />
                    </div>
                    <div>
                      <span className="fsmall">분당 제한</span>
                      <input className="inp sm" placeholder="예: 100" />
                    </div>
                    <div>
                      <span className="fsmall">컨텍스트(K)</span>
                      <input className="inp sm" placeholder="예: 200" />
                    </div>
                  </div>
                  <span className="fsmall">
                    프록시 <span className="opt">· 선택</span>
                  </span>
                  <input
                    className="inp sm"
                    placeholder="socks5://user:pass@127.0.0.1:1080"
                    style={{ marginBottom: 14 }}
                  />
                  <span className="fsmall">
                    사용자 지정 세션 헤더 <span className="opt">· 선택</span>
                  </span>
                  <input className="inp sm" placeholder="헤더 이름 (값은 세션 id 자동)" />
                </div>
              </details>
            </div>

            <div className="hr" />

            {/* 알림 */}
            <div className="qlab">점검 중 발견을 바로 알려드릴까요?</div>
            <ChoiceGroup
              style={{ marginBottom: 0 }}
              options={[
                { t: '네, 바로 알림 받을게요', d: '중요한 발견이 나오면 알려드려요' },
                { t: '아니요, 끝나고 한 번에', d: '완료 후 보고서로 확인' },
              ]}
            />
          </div>

          <aside className="side">
            <h2>점검 요약</h2>
            <div className="srow">
              <div className="k">대상</div>
              <div className="v">{target.host}</div>
            </div>
            <div className="srow">
              <div className="k">인증</div>
              <div className="v">{summaryAuth}</div>
            </div>
            <div className="hr" style={{ margin: '14px 0' }} />
            <div className="srow">
              <div className="k">LLM</div>
              <div className="v">{summaryLlm}</div>
            </div>
            <div className="srow">
              <div className="k">모델</div>
              <div className="v">{summaryModel}</div>
            </div>
            <div className="row" style={{ marginTop: 18 }}>
              <button className="btn" type="button" onClick={() => go('entry')}>
                이전
              </button>
              <button
                className="btn primary"
                type="button"
                onClick={() => go('job')}
                style={{ flex: 1 }}
              >
                다음 →
              </button>
            </div>
          </aside>
        </div>
      </div>
    </Screen>
  );
}
