import { Screen } from '../components/Screen';
import { TopBar } from '../components/TopBar';
import { MockBanner, Pill, Strong } from '../components/ui';
import { useGo } from '../nav';

const linkStyle = { color: 'var(--teal-ink)', fontWeight: 600, cursor: 'pointer' } as const;
const optionDesc = {
  fontWeight: 500,
  color: 'var(--muted)',
  fontSize: 13,
  marginTop: 2,
} as const;

/** ===== 승인 게이트 ===== */
export default function ApprovalScreen() {
  const go = useGo();
  return (
    <Screen id="approval">
      <div className="frame">
        <TopBar current="history" />
        <div style={{ padding: '22px 24px', maxWidth: 720 }}>
          <MockBanner>승인 게이트 화면은 시안이에요. 실제 승인 대기 큐는 /api/intercept/pending 연동 후 떠요.</MockBanner>
          <div className="crumb">
            <a onClick={() => go('history')}>점검 기록</a> › <span>승인</span>
          </div>
          <div
            className="row"
            style={{ alignItems: 'center', gap: 8, marginBottom: 6, flexWrap: 'wrap' }}
          >
            <span className="badge warn" style={{ cursor: 'default' }}>
              ⚠ 상태를 바꾸는 요청
            </span>
            <Pill tone="muted">api.store-dev.com</Pill>
          </div>
          <h1 className="big" style={{ marginBottom: 4 }}>
            이 요청을 보내도 될까요?
          </h1>
          <p className="lead" style={{ marginBottom: 12 }}>
            조회(GET)는 자동으로 하지만,{' '}
            <Strong>상태를 바꾸는 요청은 보내기 전에 사람이 확인</Strong>해요. 확인하기 전까지 전송
            통제(E②)가 이 요청을 막고 있어요.
          </p>
          <div className="hint" style={{ marginTop: 0, marginBottom: 22 }}>
            관련 발견 ·{' '}
            <a onClick={() => go('finding-und')} style={linkStyle}>
              결제 금액 변경이 막히는지 미확인 (판단 불가)
            </a>
          </div>

          <div className="card">
            <h3>에이전트가 하려는 일</h3>
            <p
              style={{ fontSize: 14, color: 'var(--muted)', lineHeight: 1.7, margin: '0 0 12px' }}
            >
              결제 단계에서 <Strong>금액을 임의로 바꿔도 주문이 생성되는지</Strong> 확인하려고 해요.
              성공하면 비즈니스 로직 취약점이지만, 이 요청은 <Strong>실제 주문을 생성</Strong>할 수
              있어요.
            </p>
            {/* .ev 는 white-space: pre */}
            <div className="ev">
              {'POST /api/checkout HTTP/1.1\nHost: api.store-dev.com\nCookie: session='}
              <span className="rd">[REDACTED]</span>
              {'\n\n{ "item": 55, "qty": 1, "price_override": '}
              <span className="cn">1</span>
              {' }'}
            </div>
            <div className="hint">
              바뀌는 것: <Strong>주문 1건 생성</Strong> · 되돌리기: 시험환경 리셋으로 가능 · 영향
              범위: 이 대상에 한정
            </div>
          </div>

          <div className="card" style={{ marginBottom: 16 }}>
            <h3>어떻게 할까요?</h3>
            <div className="stack" style={{ gap: 10 }}>
              <button
                className="btn"
                type="button"
                onClick={() => go('audit')}
                style={{ textAlign: 'left', borderColor: 'var(--teal-border)' }}
              >
                <strong style={{ color: 'var(--teal-ink)' }}>격리 시험환경에서만 실행</strong>
                <div style={optionDesc}>리셋 가능한 복제본에서 재현 — 권장</div>
              </button>
              <button
                className="btn"
                type="button"
                onClick={() => go('audit')}
                style={{ textAlign: 'left' }}
              >
                <strong>이번 한 번만 허용</strong>
                <div style={optionDesc}>이 요청에만 적용</div>
              </button>
              <button
                className="btn danger"
                type="button"
                onClick={() => go('history')}
                style={{ textAlign: 'left' }}
              >
                <strong>거부</strong>
                <div style={optionDesc}>보내지 않고 &quot;판단 불가&quot;로 남김</div>
              </button>
            </div>
          </div>
          <div className="hint">
            어떤 선택을 하든 <Strong>누가·언제·무엇을</Strong> 결정했는지{' '}
            <a onClick={() => go('audit')} style={linkStyle}>
              감사 로그
            </a>
            에 남아요.
          </div>
        </div>
      </div>
    </Screen>
  );
}
