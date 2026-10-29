import { useState } from 'react';
import { cx } from '../lib/cx';
import { useTimeouts } from '../lib/hooks';
import { hostOf, randomVerifyToken } from '../lib/net';
import { CopyButton } from './CopyButton';
import { Modal } from './Modal';

type Status = 'idle' | 'checking' | 'ok';

type Props = {
  /** 점검하려는 외부 주소 */
  url: string;
  onClose: () => void;
  /** 소유 확인 후 "다음 →" */
  onNext: () => void;
};

/**
 * 대상 소유 확인 팝업 (외부 주소 · DNS TXT).
 * 열 때마다 새로 마운트해서 토큰이 새로 만들어져요.
 * 지금은 1.3초 뒤 성공으로 보여주는 시안 동작 — 실제 DNS 조회 API로 바꿔 끼우면 돼요.
 */
export function VerifyDomainModal({ url, onClose, onNext }: Props) {
  const host = hostOf(url) || 'example.com';
  const recordName = `_abc2lab-challenge.${host}`;
  const [token] = useState(randomVerifyToken);
  const [status, setStatus] = useState<Status>('idle');
  const timers = useTimeouts();

  const verify = () => {
    setStatus('checking');
    timers.clearAll();
    timers.set(() => setStatus('ok'), 1300);
  };

  return (
    <Modal open onClose={onClose}>
      <div className="vpop" role="dialog" aria-label="대상 소유 확인">
        <div className="vpop-head">
          <div>
            <div className="vpt">외부 주소 — 소유 확인이 필요해요</div>
            <div className="vpd">
              내 소유가 아닌 서비스를 함부로 점검하는 걸 막기 위해, 도메인 통제를 확인해요.
            </div>
          </div>
          <button className="vpop-x" type="button" onClick={onClose} aria-label="닫기">
            ✕
          </button>
        </div>
        <div className="vpop-body">
          <div className="vwarn">
            <span style={{ flex: 'none' }}>⚠</span>
            <div>
              <b>{host}</b> 은(는) 외부(공인) 주소예요. DNS TXT 레코드로 도메인 소유를 증명해
              주세요.{' '}
              <span style={{ color: 'var(--muted)' }}>
                웹 파일 방식은 대상의 취약점으로 우회될 수 있어 쓰지 않아요.
              </span>
            </div>
          </div>
          <div className="vstep">
            <span className="vnum">1</span>
            <div>아래 TXT 레코드를 도메인 DNS에 추가하세요.</div>
          </div>
          <div className="vrec">
            <div className="vrr">
              <span className="vk">이름(호스트)</span>
              <code>{recordName}</code>
              <CopyButton text={recordName} />
            </div>
            <div className="vrr">
              <span className="vk">유형</span>
              <code>TXT</code>
            </div>
            <div className="vrr">
              <span className="vk">값</span>
              <code>{token}</code>
              <CopyButton text={token} />
            </div>
          </div>
          <div className="vhint">
            토큰은 1시간만 유효하고 이 주소에만 쓸 수 있어요. 권한 네임서버에 직접 조회해서 보통
            1~2분이면 확인돼요.
          </div>
          <div className="vstep">
            <span className="vnum">2</span>
            <div>추가했으면 확인을 눌러주세요.</div>
          </div>
          {status !== 'idle' && (
            <div className={cx('vstate', status)}>
              {status === 'checking'
                ? '확인 중 — 권한 네임서버에 TXT 조회…'
                : '✓ 소유 확인됨 — DNS TXT 일치. 이 주소를 허용 목록에 등록했어요.'}
            </div>
          )}
        </div>
        <div className="vpop-foot">
          <button className="btn" type="button" onClick={onClose}>
            취소
          </button>
          {status === 'ok' ? (
            <button className="btn primary" type="button" onClick={onNext}>
              다음 →
            </button>
          ) : (
            <button className="btn primary" type="button" onClick={verify}>
              확인
            </button>
          )}
        </div>
      </div>
    </Modal>
  );
}
