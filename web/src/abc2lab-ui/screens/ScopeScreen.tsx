import { useState } from 'react';
import { Brand } from '../components/Brand';
import { Screen } from '../components/Screen';
import { MockBanner, Pill, Strong, Sub } from '../components/ui';
import { cx } from '../lib/cx';
import { useGo } from '../nav';
import type { Target } from '../types';

/** 경로 하나 = 줄 하나. 같은 경로의 여러 메소드는 합쳐서 보여준다 */
type ScopePath = {
  path: string;
  /** 엔진이 찾은 요청 방식. 화면엔 직접 안 보이고, 바꾸는 요청이 있으면 태그로만 */
  methods: string[];
  /** 기본 포함 여부 (기본 true) */
  on?: boolean;
  /** 태그 문구를 직접 정할 때 (예: 결제) */
  label?: string;
};

const CHANGE_WORD: Record<string, string> = { POST: '등록', PUT: '수정', PATCH: '수정', DELETE: '삭제' };

/** 바꾸는 요청이 있는 경로만 태그. 조회만 하는 경로는 아무 표시 없음 */
function changeTag(ep: ScopePath): string | null {
  const changes = ep.methods.filter((m) => m in CHANGE_WORD);
  if (changes.length === 0) return null;
  const words = ep.label ?? [...new Set(changes.map((m) => CHANGE_WORD[m]))].join('·');
  const onlyChanges = changes.length === ep.methods.length;
  return `${words}${onlyChanges ? '' : ' 포함'} · 승인 후`;
}

const g = (path: string, on?: boolean): ScopePath => ({ path, methods: ['GET'], on });

const PATHS: ScopePath[] = [
  g('/'),
  g('/products'),
  g('/products/{id}'),
  g('/search'),
  g('/health', false),
  g('/login'),
  g('/register'),
  g('/about'),
  g('/faq'),
  g('/categories'),
  g('/sitemap.xml'),
  g('/robots.txt'),
  { path: '/api/orders', methods: ['GET', 'POST'] },
  { path: '/api/orders/{id}', methods: ['GET', 'PATCH'] },
  g('/api/cart'),
  { path: '/api/cart/add', methods: ['POST'], label: '장바구니 담기' },
  { path: '/api/cart/{id}', methods: ['DELETE'], on: false },
  { path: '/api/checkout', methods: ['POST'], label: '결제' },
  { path: '/api/users/{id}', methods: ['GET', 'PUT'] },
  g('/api/profile'),
  { path: '/api/addresses', methods: ['GET', 'POST'] },
  g('/api/wishlist'),
  g('/api/reviews'),
  g('/api/notifications'),
  g('/api/coupons'),
  g('/api/messages'),
  g('/api/settings'),
  g('/admin'),
  g('/admin/stats'),
  { path: '/admin/users', methods: ['GET', 'POST'] },
  g('/admin/orders'),
  { path: '/admin/products/{id}', methods: ['PUT'] },
  g('/admin/logs'),
];

/** 처음엔 이만큼만 보여주고 나머지는 '더 보기' */
const FIRST_SHOWN = 12;

function ScopeList() {
  const [enabled, setEnabled] = useState(() => PATHS.map((ep) => ep.on !== false));
  const [showAll, setShowAll] = useState(false);
  const changeCount = PATHS.filter((ep) => changeTag(ep)).length;
  const offCount = enabled.filter((v) => !v).length;
  const shown = showAll ? PATHS : PATHS.slice(0, FIRST_SHOWN);

  return (
    <>
      <p className="scope-sum">
        경로 <Strong>{PATHS.length}</Strong>개 · 이 중 <Strong>{changeCount}</Strong>개는 바꾸는 요청이 있어 승인 후 전송
        {offCount > 0 && <> · 제외 {offCount}개</>}
      </p>
      <div className="eplist flat">
        {shown.map((ep, i) => {
          const on = enabled[i];
          const tag = changeTag(ep);
          return (
            <button
              key={ep.path}
              type="button"
              className={cx('eprow', 'eprow-btn', !on && 'off')}
              aria-pressed={on}
              aria-label={`${ep.path} ${on ? '점검함' : '제외됨'}${tag ? `, ${tag}` : ''}`}
              onClick={() => setEnabled((prev) => prev.map((v, j) => (j === i ? !v : v)))}
            >
              <code>{ep.path}</code>
              {tag && <span className="eptag">{tag}</span>}
              <span className="epstate">{on ? '점검' : '제외'}</span>
              <span className={cx('sw', 'sm', on && 'on')} aria-hidden="true">
                <i />
              </span>
            </button>
          );
        })}
        {!showAll && PATHS.length > FIRST_SHOWN && (
          <button className="epmore" type="button" onClick={() => setShowAll(true)}>
            그 외 {PATHS.length - FIRST_SHOWN}개 더 보기 ▾
          </button>
        )}
      </div>
    </>
  );
}

/** ===== 범위 확인 ===== */
export default function ScopeScreen({ target }: { target: Target }) {
  const go = useGo();
  return (
    <Screen id="scope">
      <div className="frame">
        <div className="top">
          <div style={{ minWidth: 0 }}>
            <Brand style={{ fontSize: 16 }}>
              ABC2LAB{' '}
              <span style={{ fontWeight: 500, fontSize: 14, color: 'var(--muted)', marginLeft: 4 }}>
                웹 보안 자동 점검
              </span>
            </Brand>
            <div style={{ fontSize: 13, color: 'var(--muted)', marginTop: 3 }}>
              점검 대상 · <Strong>{target.url}</Strong>
            </div>
          </div>
          <span className="badge warn" style={{ cursor: 'default' }}>
            ⏸ 탐색 완료 · 확인 대기
          </span>
        </div>
        <div style={{ padding: '22px 24px' }}>
          <MockBanner>범위 확인 화면은 시안이에요. 실제 탐색 범위·엔드포인트 목록은 엔진 연동 후 떠요.</MockBanner>
          <h1 className="big">이 범위로 점검할까요?</h1>
          <p className="lead">
            사이트를 먼저 훑어봤어요. 어디를 점검할지 확인만 해주면 바로 시작할게요.{' '}
            <Strong>그냥 두면 전부 점검해요.</Strong>
          </p>
          <div className="dgrid" style={{ marginBottom: 20 }}>
            <div className="tile">
              <div className="k">발견한 경로</div>
              <div className="v">{PATHS.length}</div>
              <div className="sub">요청 방식까지 세면 {PATHS.reduce((n, ep) => n + ep.methods.length, 0)}개</div>
            </div>
            <div className="tile">
              <div className="k">로그인 영역</div>
              <div className="v" style={{ fontSize: 18 }}>
                확인됨
              </div>
              <div className="sub">계정 A · B로 접근</div>
            </div>
            <div className="tile">
              <div className="k">상태 변경 흐름</div>
              <div className="v">3</div>
              <div className="sub">결제 · 주문 · 프로필</div>
            </div>
          </div>
          <div className="card">
            <h3>
              점검할 경로 <Sub>· 누르면 점검에서 빼거나 다시 넣을 수 있어요</Sub>
            </h3>
            <ScopeList />
            <div className="setrow" style={{ opacity: 0.55, borderTop: 'none', paddingTop: 14 }}>
              <div className="tx">
                <div className="t">
                  <code>/admin/delete*</code>{' '}
                  <span style={{ color: 'var(--muted)', fontWeight: 500 }}>· 2</span>
                </div>
                <div className="d">전역 제외 경로로 지정됨 — 펼칠 것도 없이 요청 안 해요</div>
              </div>
              <Pill tone="muted">제외</Pill>
            </div>
          </div>
          <div className="row" style={{ marginTop: 4 }}>
            <button
              className="btn primary"
              type="button"
              onClick={() => go('live')}
              style={{ flex: 1 }}
            >
              이 범위로 점검 시작
            </button>
          </div>
        </div>
      </div>
    </Screen>
  );
}
