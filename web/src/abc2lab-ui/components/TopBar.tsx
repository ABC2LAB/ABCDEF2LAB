import type { ReactNode } from 'react';
import { signOut } from '../lib/api';
import { MOCK } from '../lib/mock';
import { useGo } from '../nav';
import { Brand } from './Brand';

type NavKey = 'dashboard' | 'entry' | 'history' | 'settings';

const NAV_ITEMS: Array<[NavKey, string]> = [
  ['dashboard', '대시보드'],
  ['entry', '새 점검'],
  ['history', '점검 기록'],
  ['settings', '설정'],
];

export function MainNav({ current }: { current?: NavKey }) {
  const go = useGo();
  return (
    <div className="nav">
      {NAV_ITEMS.map(([key, label]) => (
        <a key={key} className={current === key ? 'cur' : undefined} onClick={() => go(key)}>
          {label}
        </a>
      ))}
      {!MOCK && <a onClick={signOut}>로그아웃</a>}
    </div>
  );
}

type TopBarProps = {
  /** 상단 네비에서 굵게 표시할 메뉴 */
  current?: NavKey;
  /** 네비 대신 오른쪽에 둘 내용 (예: 마법사의 "취소") */
  right?: ReactNode;
};

export function TopBar({ current, right }: TopBarProps) {
  return (
    <div className="top">
      <Brand />
      {right ?? <MainNav current={current} />}
    </div>
  );
}

/** 마법사 화면(점검 방식 · 작업 설정)의 오른쪽 "취소" */
export function CancelLink() {
  const go = useGo();
  return (
    <span className="muted-link" onClick={() => go('dashboard')}>
      취소
    </span>
  );
}
