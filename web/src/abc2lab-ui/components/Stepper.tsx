import { Fragment } from 'react';
import { cx } from '../lib/cx';

const STEPS = ['주소', '점검 방식', '작업 설정'];

/** current: 지금 단계 (1부터) */
export function Stepper({ current }: { current: number }) {
  return (
    <div className="stepper">
      {STEPS.map((label, i) => {
        const n = i + 1;
        const done = n < current;
        const now = n === current;
        return (
          <Fragment key={label}>
            {i > 0 && <span className={cx('ln', n <= current && 'on')} />}
            <div className={cx('s', done && 'done', now && 'now')}>
              <span className="n">{done ? '✓' : n}</span>
              <span className="t">{label}</span>
            </div>
          </Fragment>
        );
      })}
    </div>
  );
}
