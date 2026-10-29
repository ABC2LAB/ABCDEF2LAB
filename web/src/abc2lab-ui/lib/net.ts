import { DEFAULT_TARGET_URL, type Target } from '../types';

function stripToHost(url: string): string {
  return url.replace(/^https?:\/\//, '').replace(/\/.*$/, '');
}

/**
 * 원본 internal(): 내부망 · 로컬 주소면 true, 공인 주소면 false, URL이 아니면 null.
 * (localhost, 점 없는 호스트명, 127/10/192.168/172.16~31/169.254 대역)
 */
export function isInternalUrl(url: string): boolean | null {
  let h: string;
  try {
    h = new URL(url).hostname;
  } catch {
    return null;
  }
  if (!h) return null;
  if (h === 'localhost' || h === '::1') return true;
  if (h.indexOf('.') === -1) return true;
  const m = h.match(/^(\d+)\.(\d+)\.\d+\.\d+$/);
  if (!m) return false;
  const a = Number(m[1]);
  const b = Number(m[2]);
  return (
    a === 127 ||
    a === 10 ||
    (a === 192 && b === 168) ||
    (a === 172 && b >= 16 && b <= 31) ||
    (a === 169 && b === 254)
  );
}

/** 원본 hostOf() */
export function hostOf(url: string): string {
  try {
    return new URL(url).host;
  } catch {
    return stripToHost(url || '');
  }
}

/** 원본 syncTarget(): 입력값 → 요약·대시보드·팝업에 보여줄 url / host */
export function toTarget(input: string): Target {
  const url = input.trim() || DEFAULT_TARGET_URL;
  let host: string;
  try {
    host = new URL(url).host || url;
  } catch {
    host = stripToHost(url);
  }
  return { url, host };
}

/** DNS TXT 소유 확인용 1회용 토큰 (원본과 같은 형식) */
export function randomVerifyToken(): string {
  return (
    'abc2lab-verify=' +
    Math.random().toString(16).slice(2, 10) +
    Math.random().toString(16).slice(2, 8)
  );
}
