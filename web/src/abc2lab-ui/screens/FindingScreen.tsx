import { Screen } from '../components/Screen';
import { TopBar } from '../components/TopBar';
import { Pill, Sev, Strong, Sub, VerdictBadge } from '../components/ui';
import { useGo } from '../nav';

const bodyText = { fontSize: 13.5, color: 'var(--muted)', lineHeight: 1.7, margin: 0 } as const;
const listText = {
  margin: 0,
  paddingLeft: 18,
  fontSize: 13.5,
  color: 'var(--muted)',
  lineHeight: 1.8,
} as const;
const cellSub = { color: 'var(--muted)', fontSize: 12 } as const;

/** ===== 발견 상세 · 확정 ===== */
export default function FindingScreen() {
  const go = useGo();
  return (
    <Screen id="finding">
      <div className="frame">
        <TopBar current="history" />
        <div style={{ padding: '22px 24px', maxWidth: 760 }}>
          <div className="crumb">
            <a onClick={() => go('history')}>점검 기록</a> ›{' '}
            <a onClick={() => go('report')}>blog.example.com</a> › <span>발견</span>
          </div>
          <div
            style={{
              fontSize: 12.5,
              color: 'var(--warn)',
              background: 'var(--warn-weak)',
              border: '1px solid var(--warn-border)',
              borderRadius: 8,
              padding: '10px 12px',
              marginBottom: 14,
            }}
          >
            <Strong>시안(목업)</Strong> — 디자인 예시 화면이에요. 아래 "확정" 배지·요청/응답·엔드포인트는{' '}
            <Strong>실데이터가 아니며</Strong>, 실제 발견의 증거 상세와 확정·의심·판단 불가 판정은{' '}
            <Strong>E③ 오라클 연동 후</Strong> 채워져요.
          </div>
          <div
            className="row"
            style={{ alignItems: 'center', gap: 8, marginBottom: 6, flexWrap: 'wrap' }}
          >
            <Sev level="high" />
            <VerdictBadge verdict="ok" />
            <Pill tone="muted">수평 IDOR</Pill>
          </div>
          <h1 className="big" style={{ marginBottom: 4 }}>
            다른 사용자의 주문 내역이 조회됨
          </h1>
          <div
            className="ep"
            style={{ fontFamily: 'var(--mono)', fontSize: 12.5, color: 'var(--muted)' }}
          >
            GET /api/orders/1012
          </div>

          <div className="card" style={{ marginTop: 16 }}>
            <h3>무엇이 문제인가</h3>
            <p style={bodyText}>
              계정 B로 로그인한 상태에서 <Strong>계정 A의 주문(1012)</Strong> 주소를 그대로
              요청했더니, 서버가 소유자 확인 없이{' '}
              <Strong>200과 함께 A의 주문 내용을 돌려줬어요.</Strong> 주문 번호만 바꾸면 남의 주문을
              볼 수 있다는 뜻이에요.
            </p>
          </div>

          <div className="card">
            <h3>
              다계정 비교 <Sub>· 같은 요청, 다른 주체</Sub>
            </h3>
            <div className="scrollx">
              <table className="cmp">
                <thead>
                  <tr>
                    <th>요청 주체</th>
                    <th>요청</th>
                    <th>응답</th>
                    <th>판정</th>
                  </tr>
                </thead>
                <tbody>
                  <tr>
                    <td>
                      <strong>계정 A</strong>
                      <br />
                      <span style={cellSub}>주문 소유자</span>
                    </td>
                    <td>
                      <code>GET /api/orders/1012</code>
                    </td>
                    <td>
                      <code>200</code> · 본인 주문
                    </td>
                    <td>
                      <span className="vbadge2 ok" style={{ fontSize: 11 }}>
                        정상
                      </span>
                    </td>
                  </tr>
                  <tr style={{ background: 'var(--danger-weak)' }}>
                    <td>
                      <strong>계정 B</strong>
                      <br />
                      <span style={cellSub}>무관한 사용자</span>
                    </td>
                    <td>
                      <code>GET /api/orders/1012</code>
                      <br />
                      <span style={cellSub}>A의 주문</span>
                    </td>
                    <td>
                      <code>200</code> ·{' '}
                      <strong style={{ color: 'var(--danger)' }}>A의 표식 노출</strong>
                    </td>
                    <td>
                      <span
                        className="vbadge2"
                        style={{
                          fontSize: 11,
                          background: 'var(--danger-weak)',
                          color: 'var(--danger)',
                        }}
                      >
                        취약
                      </span>
                    </td>
                  </tr>
                  <tr>
                    <td>
                      <strong>비로그인</strong>
                      <br />
                      <span style={cellSub}>대조군</span>
                    </td>
                    <td>
                      <code>GET /api/orders/1012</code>
                    </td>
                    <td>
                      <code>302</code> · 로그인으로
                    </td>
                    <td>
                      <span className="vbadge2 ok" style={{ fontSize: 11 }}>
                        정상 차단
                      </span>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>

          <div className="card">
            <h3>
              증거 <Sub>· 가려서 저장됨</Sub>
            </h3>
            {/* .ev 는 white-space: pre — 줄바꿈은 문자열 안의 \n 으로 넣어요 */}
            <div className="ev">
              <span className="cm"># 계정 B의 요청 (A의 주문)</span>
              {'\nGET /api/orders/1012 HTTP/1.1\nHost: blog.example.com\nCookie: session='}
              <span className="rd">[REDACTED]</span>
              {'\n\n'}
              <span className="cm"># 응답 — A의 주문에 심어둔 표식이 그대로 나옴</span>
              {'\nHTTP/1.1 200 OK\n{\n  "order_id": 1012,\n  "buyer_note": "'}
              <span className="cn">ABC2LAB-CANARY-7f3a</span>
              {'",\n  "address": "'}
              <span className="rd">[REDACTED]</span>
              {'"\n}'}
            </div>
            <div className="hint">
              값이 아니라 <Strong>A의 주문에만 심은 표식</Strong>이 B의 응답에서 나왔어요. 실제
              주소·개인정보는 저장하지 않아요(가려짐). &quot;B가 A의 자원을 실제로 받았다&quot;는
              핵심 증거예요.
            </div>
          </div>

          <div className="card">
            <h3>왜 &quot;확정&quot;인가</h3>
            <ul style={listText}>
              <li>
                B의 응답에 <Strong>A의 표식</Strong>이 그대로 담겼다 (실제로 받았음)
              </li>
              <li>B 자신의 주문에는 그 표식이 없다 (대조군과 다름)</li>
              <li>
                비로그인 요청은 차단됐다 → 접근 제어가 &quot;작동은 하는데 소유자 검사만 빠진&quot;
                상태
              </li>
            </ul>
            <div className="hint">
              세 증거가 모두 모여서 <Strong>확정</Strong>. 하나라도 비면 <Strong>판단 불가</Strong>
              로 남겨요.
            </div>
          </div>

          <div className="card" style={{ marginBottom: 0 }}>
            <h3>고치려면</h3>
            <p style={bodyText}>
              주문 조회 시 <code>order.owner_id == 세션 사용자</code>를 서버에서 검사하세요. 번호
              추측을 막으려면 순차 ID 대신 추측 어려운 식별자를 쓰는 것도 방법이에요.
            </p>
          </div>
        </div>
      </div>
    </Screen>
  );
}
