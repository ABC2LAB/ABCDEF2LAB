import { useState, type CSSProperties, type ReactNode } from 'react';
import { cx } from '../lib/cx';

export type ChoiceOption = { t: ReactNode; d: ReactNode };

type ChoiceProps = ChoiceOption & { on?: boolean; onClick?: () => void };

/** 원본 .choice 카드형 버튼 */
export function Choice({ t, d, on, onClick }: ChoiceProps) {
  return (
    <button className={cx('choice', on && 'on')} type="button" onClick={onClick}>
      <div className="t">{t}</div>
      <div className="d">{d}</div>
    </button>
  );
}

type ChoiceGroupProps = {
  options: ChoiceOption[];
  /** 처음 선택된 항목 (-1이면 아무것도 선택 안 됨) */
  defaultValue?: number;
  onChange?: (index: number) => void;
  style?: CSSProperties;
};

/** 원본 .choices[data-radio]: 하나만 고르는 그룹 */
export function ChoiceGroup({ options, defaultValue = -1, onChange, style }: ChoiceGroupProps) {
  const [selected, setSelected] = useState(defaultValue);
  return (
    <div className="choices" style={style}>
      {options.map((o, i) => (
        <Choice
          key={i}
          t={o.t}
          d={o.d}
          on={i === selected}
          onClick={() => {
            setSelected(i);
            onChange?.(i);
          }}
        />
      ))}
    </div>
  );
}
