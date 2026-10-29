import { Fragment, useState } from 'react';
import { Modal } from '../components/Modal';
import { Screen } from '../components/Screen';
import { TopBar } from '../components/TopBar';
import { Sev, Strong, Sub } from '../components/ui';
import { cx } from '../lib/cx';
import { useGo } from '../nav';
import type { Target } from '../types';

/* ---------- 시안 데이터 (실제 연동 시 API 응답으로 교체) ---------- */

const PIPELINE = ['자산 탐색', '범위 확인', '점검 계획', '취약점 점검', '검증', '마무리', '보고서'];
const PIPELINE_NOW = 3;
const PROGRESS = 60;

/** 커버리지 칸: f = 검사 · 발견, o = 검사 · 이상 없음, n = 미검사 */
const COVERAGE = 'foonoonoonfonoonoonfonoonoonfonoononoononononon';

type LogDot = 'teal' | 'blue' | 'warn' | 'red';
type LogRow = { time: string; dot: LogDot; text: string; highlight?: string };

const ACTIVITY: LogRow[] = [
  {
    time: '18:33:56',
    dot: 'blue',
    text: '링크를 따라 페이지·API 경로 수집',
    highlight: 'academy.shop.example.com',
  },
  { time: '18:33:52', dot: 'teal', text: '브라우저로 화면·로그인 흐름 확인' },
  {
    time: '18:33:51',
    dot: 'warn',
    text: '안전 규칙으로 막은 시도 — 점검 프로그램을 건드리는 명령',
  },
  { time: '18:33:47', dot: 'teal', text: '브라우저로 화면·로그인 흐름 확인' },
  {
    time: '18:33:03',
    dot: 'teal',
    text: '로그인한 상태로 화면 점검',
    highlight: 'shop.example.com/signin',
  },
  {
    time: '18:32:39',
    dot: 'teal',
    text: '요청을 보내 응답·헤더 확인',
    highlight: 'shop.example.com/signin',
  },
  {
    time: '18:32:08',
    dot: 'teal',
    text: '요청을 보내 응답·헤더 확인',
    highlight: 'adminapi.shop.example.com',
  },
  { time: '18:32:06', dot: 'warn', text: '안전 규칙으로 막은 시도' },
  {
    time: '18:31:51',
    dot: 'teal',
    text: '계정 A로 주문 상세 수집 (대조군)',
    highlight: 'GET /api/orders/1012',
  },
  {
    time: '18:31:40',
    dot: 'red',
    text: '계정 B가 계정 A 주문 조회 → 표식 노출, 발견 보고',
    highlight: 'GET /api/orders/1012',
  },
];

type PlanState = 'done' | 'run' | 'todo';
const PLAN: Array<[PlanState, string]> = [
  ['done', '우선 공격 표면·자산 탐색'],
  ['done', '역할별 로그인·세션 확보 (계정 A·B)'],
  ['done', '기존 정찰 결과·커버리지 확인'],
  ['run', '수평 권한 경계(IDOR) 점검 — 주문·프로필'],
  ['todo', '수직 권한 상승 점검 — 관리자 기능'],
  ['todo', '업무 흐름 우회 — 결제·주문 (승인 필요)'],
  ['todo', '발견 → 검증 에이전트 체인 운용'],
  ['todo', '소유·권한 지식그래프 갱신'],
  ['todo', '쿠키·세션 속성 점검'],
  ['todo', '커버리지 정리 및 최종 보고'],
];

/* ---------- 상세 보기 팝업 (앱 테마와 무관하게 다크) ---------- */

function LiveDetailModal({
  open,
  onClose,
  target,
}: {
  open: boolean;
  onClose: () => void;
  target: Target;
}) {
  const doneCount = PLAN.filter(([s]) => s === 'done').length;
  return (
    <Modal open={open} onClose={onClose}>
      <div className="rpop" role="dialog" aria-label="실시간 활동과 점검 계획">
        <div className="rpop-head">
          <div style={{ minWidth: 0, flex: 1 }}>
            <div className="rpt">{target.url}</div>
            <div className="rpbar">
              <i style={{ width: `${PROGRESS}%` }} />
            </div>
            <div className="rpd">검사 범위 {PROGRESS}% · 발견 10건</div>
          </div>
          <button className="rpop-x" type="button" onClick={onClose} aria-label="닫기">
            ✕
          </button>
        </div>
        <div className="rpop-now">
          <span className="spin" />
          <div style={{ minWidth: 0 }}>
            <div className="rpnl">지금 하는 일</div>
            <div className="rpnv">
              계정 B로 계정 A의 주문 상세에 접근되는지 확인 중 · <code>GET /api/orders/1012</code>
            </div>
          </div>
        </div>
        <div className="rpop-body">
          <div className="rp-left">
            <div className="rp-h">실시간 활동</div>
            <div className="rlegend">
              <span>
                <span className="rdot teal" />
                정상 확인
              </span>
              <span>
                <span className="rdot blue" />
                경로 수집
              </span>
              <span>
                <span className="rdot warn" />
                막은 시도
              </span>
              <span>
                <span className="rdot red" />
                발견
              </span>
            </div>
            <div className="rlog">
              {ACTIVITY.map((row, i) => (
                <div className="rrow" key={i}>
                  <span className="rt2">{row.time}</span>
                  <span className={`rdot ${row.dot}`} />
                  <span className="rx">
                    {row.text}
                    {row.highlight && (
                      <>
                        {' '}
                        <span className="rh">{row.highlight}</span>
                      </>
                    )}
                  </span>
                </div>
              ))}
            </div>
          </div>
          <div className="rp-right">
            <div className="rp-h">
              점검 계획 ·{' '}
              <span className="rmut">
                {doneCount}/{PLAN.length} 완료
              </span>
            </div>
            <div className="rplan">
              {PLAN.map(([state, text], i) => (
                <div key={i} className={cx('rpi', state !== 'todo' && state)}>
                  <span className="rc" /> {text}
                </div>
              ))}
            </div>
            <div className="rpnote">
              의도(계획)는 플래너만 만들고, 워커들이 실행해요. 같은 자원·조건의 중복 요청은 묶어서
              한 번만, 새 발견이 멈추면 스스로 종료를 판단해요.
            </div>
          </div>
        </div>
      </div>
    </Modal>
  );
}

/* ---------- 진행 대시보드 ---------- */

const small = { fontSize: 13, fontWeight: 600, color: 'var(--muted)' } as const;
const btnSm = { padding: '8px 13px', fontSize: 13 } as const;

/** ===== 진행 대시보드 ===== */
export default function LiveScreen({ target }: { target: Target }) {
  const go = useGo();
  const [detailOpen, setDetailOpen] = useState(false);

  return (
    <Screen id="live">
      <div className="frame">
        <TopBar />
        <div style={{ padding: '22px 24px' }}>
          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'space-between',
              gap: 12,
              flexWrap: 'wrap',
              marginBottom: 16,
            }}
          >
            <div style={{ fontSize: 13, color: 'var(--muted)', minWidth: 0 }}>
              점검 대상 · <Strong>{target.url}</Strong>
            </div>
            <div className="row" style={{ flexWrap: 'wrap' }}>
              <span className="badge warn" onClick={() => go('approval')}>
                ⚠ 승인 대기 1
              </span>
              <span className="badge ok">● 진행 중</span>
              <button className="btn" type="button" style={btnSm}>
                일시정지
              </button>
              <button className="btn danger" type="button" style={btnSm}>
                종료
              </button>
            </div>
          </div>

          <div className="pipe">
            {PIPELINE.map((label, i) => (
              <Fragment key={label}>
                {i > 0 && <span className={cx('d', i <= PIPELINE_NOW && 'on')} />}
                <span className={cx('p', i < PIPELINE_NOW && 'done', i === PIPELINE_NOW && 'now')}>
                  {label}
                </span>
              </Fragment>
            ))}
          </div>
          <div style={{ fontSize: 13, color: 'var(--muted)', marginBottom: 4 }}>
            검사 범위 <Strong>{PROGRESS}%</Strong> · 정상 진행 중이에요
          </div>
          <div className="bar">
            <i style={{ width: `${PROGRESS}%` }} />
          </div>

          <div className="nowdoing" style={{ marginTop: 16 }}>
            <span className="spin" />
            <div style={{ minWidth: 0 }}>
              <div style={{ fontSize: 12, color: 'var(--muted)' }}>지금 하는 일</div>
              <div style={{ fontSize: 13.5, fontWeight: 600 }}>
                계정 B로 로그인해 계정 A의 주문 상세에 접근되는지 확인 중 ·{' '}
                <code>GET /api/orders/1012</code>
              </div>
            </div>
            <button
              className="btn"
              type="button"
              onClick={() => setDetailOpen(true)}
              style={{ marginLeft: 'auto', flex: 'none', ...btnSm }}
            >
              상세 보기
            </button>
          </div>

          <div className="dgrid" style={{ margin: '0 0 22px' }}>
            <div className="tile">
              <div className="k">예상 남은 시간</div>
              <div className="v">
                12~15<span style={small}>분</span>
              </div>
              <div className="sub">경과 18분</div>
            </div>
            <div className="tile">
              <div className="k">소모 토큰</div>
              <div className="v">
                1.2<span style={small}>M</span>
              </div>
              <div className="sub">예상 1.8~2.1M</div>
            </div>
            <div className="tile">
              <div className="k">발견한 엔드포인트</div>
              <div className="v">47</div>
              <div className="sub">점검 중인 항목 8</div>
            </div>
          </div>

          <div className="wrap" style={{ padding: 0, gap: 22 }}>
            <div className="col" style={{ flex: '1 1 300px' }}>
              <h3 style={{ fontSize: 14, marginBottom: 10 }}>
                발견 (심각도별) <Sub>· 총 10건</Sub>
              </h3>
              <div className="row" style={{ flexWrap: 'wrap', gap: 8, marginBottom: 8 }}>
                <Sev level="critical">심각 1</Sev>
                <Sev level="high">높음 2</Sev>
                <Sev level="medium">중간 3</Sev>
                <Sev level="low">낮음 4</Sev>
              </div>
              <div
                style={{
                  border: '1px solid var(--line)',
                  borderRadius: 12,
                  padding: '4px 14px',
                  marginTop: 8,
                }}
              >
                <div style={{ fontSize: 12, color: 'var(--muted)', paddingTop: 11 }}>최근 발견</div>
                <div className="find" onClick={() => go('finding')} style={{ cursor: 'pointer' }}>
                  <Sev level="critical" />
                  <span className="nm">비로그인 상태에서 관리자 통계가 열림</span>
                </div>
                <div className="find">
                  <Sev level="medium" />
                  <span className="nm">쿠키에 보안 속성이 빠져 있음</span>
                </div>
                <div className="find">
                  <Sev level="low" />
                  <span className="nm">서버 버전 정보가 노출됨</span>
                </div>
              </div>
            </div>
            <div className="col" style={{ flex: '1 1 260px' }}>
              <h3 style={{ fontSize: 14, marginBottom: 10 }}>판정 결과</h3>
              <div className="vcard">
                <div className="vbox">
                  <div className="n" style={{ color: 'var(--teal-ink)' }}>
                    4
                  </div>
                  <div className="l">확정</div>
                </div>
                <div className="vbox">
                  <div className="n" style={{ color: 'var(--mid)' }}>
                    3
                  </div>
                  <div className="l">의심</div>
                </div>
                <div className="vbox">
                  <div className="n" style={{ color: 'var(--muted)' }}>
                    2
                  </div>
                  <div className="l">판단 불가</div>
                </div>
                <div className="vbox">
                  <div className="n" style={{ color: 'var(--faint)' }}>
                    1
                  </div>
                  <div className="l">판정 중</div>
                </div>
              </div>
              <div className="hint">
                증거가 충분해야 <Strong>확정</Strong>으로 올려요. 애매하면{' '}
                <Strong>판단 불가</Strong>로 남겨 오탐을 막아요.
              </div>
              <h3 style={{ fontSize: 14, margin: '18px 0 8px' }}>
                검사 커버리지 <Sub>· 28 / 47 엔드포인트</Sub>
              </h3>
              <div className="covgrid">
                {COVERAGE.split('').map((c, i) => (
                  <span key={i} className={`cc ${c}`} />
                ))}
              </div>
              <div className="cov">
                <span>
                  <span className="dot" style={{ background: 'var(--teal)' }} />
                  검사 · 발견
                </span>
                <span>
                  <span className="dot" style={{ background: 'var(--teal-border)' }} />
                  검사 · 이상 없음
                </span>
                <span>
                  <span className="dot" style={{ background: 'var(--line-2)' }} />
                  미검사
                </span>
              </div>
            </div>
          </div>

          <div className="hr" />
          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'space-between',
              gap: 12,
              flexWrap: 'wrap',
            }}
          >
            <div style={{ fontSize: 13, color: 'var(--muted)' }}>
              보고서는 점검이 끝나면 준비돼요 · <Strong>생성 대기 중</Strong>
            </div>
            <button className="btn" type="button" onClick={() => go('history')}>
              점검 기록 보기
            </button>
          </div>
        </div>
      </div>

      <LiveDetailModal open={detailOpen} onClose={() => setDetailOpen(false)} target={target} />
    </Screen>
  );
}
