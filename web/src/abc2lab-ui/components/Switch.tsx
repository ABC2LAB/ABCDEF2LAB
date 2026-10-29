import { useState } from 'react';
import { cx } from '../lib/cx';

type SwitchProps = {
  /** 제어 모드: 값을 직접 넘길 때 */
  on?: boolean;
  /** 비제어 모드 초기값 */
  defaultOn?: boolean;
  onChange?: (on: boolean) => void;
  /** 작은 스위치 (.sw.sm) */
  sm?: boolean;
  className?: string;
  label?: string;
};

/** 원본 .sw 토글 */
export function Switch({ on, defaultOn = false, onChange, sm, className, label }: SwitchProps) {
  const [inner, setInner] = useState(defaultOn);
  const isOn = on ?? inner;
  return (
    <button
      type="button"
      className={cx('sw', sm && 'sm', isOn && 'on', className)}
      aria-label={label}
      aria-pressed={isOn}
      onClick={() => {
        if (on === undefined) setInner(!isOn);
        onChange?.(!isOn);
      }}
    >
      <i />
    </button>
  );
}
