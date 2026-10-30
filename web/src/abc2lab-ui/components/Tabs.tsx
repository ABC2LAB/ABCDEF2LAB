import { useState, type ReactNode } from 'react';
import { cx } from '../lib/cx';

export type TabItem<K extends string = string> = {
  key: K;
  label: ReactNode;
  count?: ReactNode;
};

type TabsProps<K extends string> = {
  items: TabItem<K>[];
  /** 제어 모드 */
  value?: K;
  defaultValue?: K;
  onChange?: (key: K) => void;
};

/** 원본 .tabs / .tab (알약형 탭) */
export function Tabs<K extends string>({ items, value, defaultValue, onChange }: TabsProps<K>) {
  const [inner, setInner] = useState<K | undefined>(defaultValue ?? items[0]?.key);
  const current = value ?? inner;
  return (
    <div className="tabs">
      {items.map((item) => (
        <button
          key={item.key}
          className={cx('tab', item.key === current && 'on')}
          type="button"
          onClick={() => {
            if (value === undefined) setInner(item.key);
            onChange?.(item.key);
          }}
        >
          {item.label}
          {item.count !== undefined && <span className="c">{item.count}</span>}
        </button>
      ))}
    </div>
  );
}
