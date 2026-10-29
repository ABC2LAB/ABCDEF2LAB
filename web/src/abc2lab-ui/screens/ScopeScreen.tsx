import { useState, type ReactNode } from 'react';
import { Brand } from '../components/Brand';
import { Screen } from '../components/Screen';
import { Switch } from '../components/Switch';
import { Pill, Strong, Sub } from '../components/ui';
import { cx } from '../lib/cx';
import { useGo } from '../nav';
import type { Target } from '../types';

type Endpoint = {
  path: string;
  /** 기본 포함 여부 (기본 true) */
  on?: boolean;
  /** "그 외 N개 더 펼치기"를 눌러야 보이는 항목 */
  more?: boolean;
};

type AreaDef = {
  title: string;
  count: number;
  tag?: ReactNode;
  desc: string;
  switchLabel: string;
  endpoints: Endpoint[];
  moreLabel?: string;
};

const more = (paths: string[]): Endpoint[] => paths.map((path) => ({ path, more: true }));

const AREAS: AreaDef[] = [
  {
    title: '공개 영역',
    count: 12,
    desc: '로그인 없이 보이는 페이지·API',
    switchLabel: '공개 영역 전체',
    endpoints: [
      { path: 'GET /' },
      { path: 'GET /products' },
      { path: 'GET /products/{id}' },
      { path: 'GET /search' },
      { path: 'GET /health', on: false },
      ...more([
        'GET /login',
        'GET /register',
        'GET /about',
        'GET /faq',
        'GET /categories',
        'GET /sitemap.xml',
        'GET /robots.txt',
      ]),
    ],
    moreLabel: '그 외 7개 더 펼치기 ▾',
  },
  {
    title: '로그인 후 영역',
    count: 23,
    desc: '계정 A·B로 들어가는 곳 · 권한 경계(IDOR) 점검',
    switchLabel: '로그인 후 영역 전체',
    endpoints: [
      { path: 'GET /api/orders' },
      { path: 'GET /api/orders/{id}' },
      { path: 'GET /api/cart' },
      { path: 'PUT /api/users/{id}' },
      { path: 'GET /api/profile' },
      ...more([
        'GET /api/users/{id}',
        'GET /api/addresses',
        'POST /api/addresses',
        'GET /api/wishlist',
        'GET /api/reviews',
        'GET /api/notifications',
        'GET /api/coupons',
        'GET /api/messages',
        'GET /api/settings',
      ]),
    ],
    moreLabel: '그 외 18개 더 펼치기 ▾',
  },
  {
    title: '결제·주문 변경',
    count: 5,
    tag: <Pill tone="warn">상태 변경</Pill>,
    desc: '데이터를 바꾸는 흐름 · 재현할 땐 승인 + 격리환경이 필요해요',
    switchLabel: '결제 주문 변경 전체',
    endpoints: [
      { path: 'POST /api/checkout' },
      { path: 'POST /api/cart/add' },
      { path: 'PATCH /api/orders/{id}' },
      { path: 'DELETE /api/cart/{id}', on: false },
      { path: 'POST /api/orders' },
    ],
  },
  {
    title: '관리자 영역',
    count: 7,
    desc: '관리자 전용으로 보이는 기능 · 수직 권한 점검',
    switchLabel: '관리자 영역 전체',
    endpoints: [
      { path: 'GET /admin' },
      { path: 'GET /admin/stats' },
      { path: 'GET /admin/users' },
      { path: 'POST /admin/users' },
      ...more(['GET /admin/orders', 'PUT /admin/products/{id}', 'GET /admin/logs']),
    ],
    moreLabel: '그 외 3개 더 펼치기 ▾',
  },
];

function ScopeArea({ area }: { area: AreaDef }) {
  const [expanded, setExpanded] = useState(false);
  const [showMore, setShowMore] = useState(false);
  const [enabled, setEnabled] = useState(() => area.endpoints.map((ep) => ep.on !== false));

  return (
    <div className="area">
      <div className="arhead">
        <button
          className="arexp"
          type="button"
          aria-expanded={expanded}
          onClick={() => setExpanded((v) => !v)}
        >
          <span className="chev">▸</span>
          <div className="tx">
            <div className="t">
              {area.title}{' '}
              <span style={{ color: 'var(--muted)', fontWeight: 500 }}>· {area.count}</span>
              {area.tag && <> {area.tag}</>}
            </div>
            <div className="d">{area.desc}</div>
          </div>
        </button>
        <Switch defaultOn label={area.switchLabel} />
      </div>
      <div className="eplist" hidden={!expanded}>
        {area.endpoints.map((ep, i) =>
          ep.more && !showMore ? null : (
            <div key={i} className={cx('eprow', !enabled[i] && 'off')}>
              <code>{ep.path}</code>
              <Switch
                sm
                className="mu"
                on={enabled[i]}
                onChange={(on) => setEnabled((prev) => prev.map((v, j) => (j === i ? on : v)))}
              />
            </div>
          ),
        )}
        {area.moreLabel && !showMore && (
          <button className="epmore" type="button" onClick={() => setShowMore(true)}>
            {area.moreLabel}
          </button>
        )}
      </div>
    </div>
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
              <span style={{ fontWeight: 500, fontSize: 13, color: 'var(--muted)', marginLeft: 4 }}>
                웹 보안 자동 점검
              </span>
            </Brand>
            <div style={{ fontSize: 12.5, color: 'var(--muted)', marginTop: 3 }}>
              점검 대상 · <Strong>{target.url}</Strong>
            </div>
          </div>
          <span className="badge warn" style={{ cursor: 'default' }}>
            ⏸ 탐색 완료 · 확인 대기
          </span>
        </div>
        <div style={{ padding: '22px 24px' }}>
          <h1 className="big">이 범위로 점검할까요?</h1>
          <p className="lead">
            사이트를 먼저 훑어봤어요. 어디를 점검할지 확인만 해주면 바로 시작할게요.{' '}
            <Strong>그냥 두면 전부 점검해요.</Strong>
          </p>
          <div className="dgrid" style={{ marginBottom: 20 }}>
            <div className="tile">
              <div className="k">발견한 엔드포인트</div>
              <div className="v">47</div>
              <div className="sub">페이지 12 · API 35</div>
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
              점검할 영역 <Sub>· 펼쳐서 주소별로 뺄 수 있어요</Sub>
            </h3>
            {AREAS.map((area) => (
              <ScopeArea key={area.title} area={area} />
            ))}
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
