import { useState } from 'react';
import { Screen } from '../components/Screen';
import { TopBar } from '../components/TopBar';
import { VerifyDomainModal } from '../components/VerifyDomainModal';
import { isInternalUrl } from '../lib/net';
import { useGo } from '../nav';
import { DEFAULT_TARGET_URL } from '../types';

type Props = {
  /** "점검 시작"을 누르면 입력한 주소를 위로 올려요 (요약 · 대시보드에 반영) */
  onStart: (input: string) => void;
};

/** ===== 진입 ===== */
export default function EntryScreen({ onStart }: Props) {
  const go = useGo();
  const [url, setUrl] = useState(DEFAULT_TARGET_URL);
  /** 소유 확인 팝업: 열릴 때마다 n을 올려서 새로 마운트 */
  const [verify, setVerify] = useState<{ url: string; n: number } | null>(null);

  const start = () => {
    onStart(url);
    const trimmed = url.trim();
    // 내부망이면 바로 진행, 외부면 DNS TXT 소유 확인
    if (isInternalUrl(trimmed) === true) go('target');
    else setVerify((v) => ({ url: trimmed, n: (v?.n ?? 0) + 1 }));
  };

  return (
    <Screen id="entry">
      <div className="frame">
        <TopBar current="entry" />
        <div style={{ padding: '64px 24px', maxWidth: 600, margin: '0 auto', textAlign: 'center' }}>
          <h1 style={{ fontSize: 28, fontWeight: 900, letterSpacing: '-.6px', marginBottom: 26 }}>
            어떤 사이트를 점검할까요?
          </h1>
          <label className="fl" style={{ textAlign: 'left' }} htmlFor="e-url">
            점검할 사이트 주소
          </label>
          <div className="row" style={{ marginBottom: 12 }}>
            <input
              className="inp"
              id="e-url"
              type="url"
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && !e.nativeEvent.isComposing) start();
              }}
              aria-label="사이트 주소"
            />
            <button className="btn primary" type="button" onClick={start}>
              점검 시작
            </button>
          </div>
          <p className="hint" style={{ textAlign: 'left' }}>
            본인이 점검 권한을 가진 사이트만 입력해 주세요. 허용된 주소 범위는 설정 → 통제 정책에서
            관리해요.
          </p>
        </div>
      </div>

      {verify && (
        <VerifyDomainModal
          key={verify.n}
          url={verify.url}
          onClose={() => setVerify(null)}
          onNext={() => {
            setVerify(null);
            go('target');
          }}
        />
      )}
    </Screen>
  );
}
