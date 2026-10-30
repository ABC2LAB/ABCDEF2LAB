import type { ReactNode } from 'react';
import { useNav, type ScreenId } from '../nav';

/**
 * 원본 <section class="screen" data-screen="…">.
 * 원본처럼 모든 화면을 마운트해 두고 hidden으로만 전환해서,
 * 화면을 오가도 입력값 · 선택 상태가 그대로 남아요.
 */
export function Screen({ id, children }: { id: ScreenId; children: ReactNode }) {
  const { screen } = useNav();
  return (
    <section className="screen" data-screen={id} hidden={screen !== id}>
      {children}
    </section>
  );
}
