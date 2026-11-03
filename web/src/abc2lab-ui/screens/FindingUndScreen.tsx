import { Screen } from '../components/Screen';
import { TopBar } from '../components/TopBar';
import { MockBanner, Pill, Sev, Strong, VerdictBadge } from '../components/ui';
import { useGo } from '../nav';

const bodyText = { fontSize: 14, color: 'var(--muted)', lineHeight: 1.7, margin: 0 } as const;

/** ===== 발견 상세 · 판단 불가 ===== */
export default function FindingUndScreen() {
  const go = useGo();
  return (
    <Screen id="finding-und">
      <div className="frame">
        <TopBar current="history" />
        <div style={{ padding: '22px 24px', maxWidth: 760 }}>
          <MockBanner>
            판단 불가 상세는 시안이에요. 실제 판정(확정/의심/판단 불가)과 증거는 E③ 오라클 연동 후 채워져요.
          </MockBanner>
          <div className="crumb">
            <a onClick={() => go('history')}>점검 기록</a> ›{' '}
            <a onClick={() => go('report')}>blog.example.com</a> › <span>발견</span>
          </div>
          <div
            className="row"
            style={{ alignItems: 'center', gap: 8, marginBottom: 6, flexWrap: 'wrap' }}
          >
            <Sev level="medium" />
            <VerdictBadge verdict="und" />
            <Pill tone="warn">상태 변경 · 승인 대기</Pill>
          </div>
          <h1 className="big" style={{ marginBottom: 4 }}>
            결제 금액 변경이 막히는지 미확인
          </h1>
          <div
            className="ep"
            style={{ fontFamily: 'var(--mono)', fontSize: 13, color: 'var(--muted)' }}
          >
            POST /api/checkout · price_override
          </div>

          <div className="card" style={{ marginTop: 16 }}>
            <h3>확인하려던 것</h3>
            <p style={bodyText}>
              결제 단계에서 금액을 <Strong>1원으로 바꿔 보내도 주문이 생성되는지</Strong> 보려고
              했어요. 생성되면 비즈니스 로직 취약점이에요.
            </p>
          </div>

          <div
            className="card"
            style={{ borderColor: 'var(--warn-border)', background: 'var(--warn-weak)' }}
          >
            <h3 style={{ color: 'var(--warn)' }}>왜 &quot;판단 불가&quot;인가</h3>
            <p
              style={{ fontSize: 14, color: 'var(--warn)', lineHeight: 1.7, margin: '0 0 10px' }}
            >
              이건 <strong>상태를 바꾸는 요청</strong>이라 전송 통제(E²)가 보내기 전에 막아뒀어요.
              사람이 승인하기 전까진 실제로 보내지 않았고, 그래서{' '}
              <strong>취약한지 아닌지 결론을 내리지 못했어요.</strong>
            </p>
            <div className="hint" style={{ color: 'var(--warn)', marginTop: 0 }}>
              미실행을 &quot;취약점 없음&quot;이나 &quot;안전&quot;으로 적지 않아요. 확인 못 한 건
              확인 못 했다고 남겨요.
            </div>
          </div>

          <div className="card">
            <h3>부족한 증거</h3>
            <ul
              style={{
                margin: 0,
                paddingLeft: 18,
                fontSize: 14,
                color: 'var(--muted)',
                lineHeight: 1.8,
              }}
            >
              <li>실제 전송·응답이 없음 (승인 대기로 보류)</li>
              <li>
                조회(GET)만으로는 &quot;주문이 생성됐는지&quot;를 알 수 없음 — GET이라고 상태 변경이
                없다고 가정하지 않아요
              </li>
              <li>정상 금액 주문(정상 대조)도 같은 이유로 미수집</li>
            </ul>
          </div>

          <div className="card" style={{ marginBottom: 0 }}>
            <h3>어떻게 확정하나</h3>
            <p style={bodyText}>
              리셋 가능한 <Strong>격리 시험환경</Strong>에서 승인 후 재현하면, 주문이 생성되는지로
              확정/정상이 갈려요.
            </p>
            <div className="row" style={{ marginTop: 12, flexWrap: 'wrap' }}>
              <button className="btn primary" type="button" onClick={() => go('approval')}>
                승인하고 격리환경에서 확인 →
              </button>
              <button className="btn" type="button" onClick={() => go('report')}>
                보고서로 돌아가기
              </button>
            </div>
          </div>
        </div>
      </div>
    </Screen>
  );
}
