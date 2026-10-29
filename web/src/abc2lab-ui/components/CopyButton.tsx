import { useState } from 'react';
import { useTimeouts } from '../lib/hooks';

/** 원본 .vcopy: 누르면 클립보드에 복사하고 1.2초간 "복사됨" */
export function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  const timers = useTimeouts();
  return (
    <button
      className="vcopy"
      type="button"
      onClick={() => {
        try {
          void navigator.clipboard?.writeText(text).catch(() => {});
        } catch {
          /* 클립보드를 못 쓰는 환경이면 무시 */
        }
        setCopied(true);
        timers.clearAll();
        timers.set(() => setCopied(false), 1200);
      }}
    >
      {copied ? '복사됨' : '복사'}
    </button>
  );
}
