import type { CSSProperties, ReactNode } from 'react';
import { useGo } from '../nav';

/** ABC2LAB 로고 (원본 SVG 그대로) */
export function BrandMark() {
  return (
    <svg width="22" height="22" viewBox="0 0 24 24" fill="none">
      <path
        d="M4 19L4 12C4 7 7.6 3.5 12 3.5C16.4 3.5 20 7 20 12L20 19L18 21L16 19L14 21L12 19L10 21L8 19L6 21Z"
        stroke="var(--brand)"
        strokeWidth="1.8"
        strokeLinejoin="round"
      />
      <path
        d="M9.3 8.4L9.3 10.4M14.7 8.4L14.7 10.4"
        stroke="var(--brand)"
        strokeWidth="2"
        strokeLinecap="round"
      />
      <path
        d="M12 12L15 14.6L12 18.4L9 14.6ZM9.4 14.6L14.6 14.6"
        stroke="var(--brand)"
        strokeWidth="1.7"
        strokeLinejoin="round"
      />
    </svg>
  );
}

type BrandProps = {
  style?: CSSProperties;
  /** 비우면 로고 + "ABC2LAB" */
  children?: ReactNode;
};

/** 클릭하면 진입 화면으로 가는 브랜드 */
export function Brand({ style, children }: BrandProps) {
  const go = useGo();
  return (
    <div
      className="brand"
      role="link"
      tabIndex={0}
      style={{ cursor: 'pointer', ...style }}
      onClick={() => go('entry')}
      onKeyDown={(e) => {
        if (e.key === 'Enter') go('entry');
      }}
    >
      {children ?? (
        <>
          <BrandMark />
          ABC2LAB
        </>
      )}
    </div>
  );
}
