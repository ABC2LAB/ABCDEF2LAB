// 시간 표시 헬퍼 (원본 hms · clock)

const pad = (n: number) => `0${n}`.slice(-2);

/** 하루 기준 초 → "HH:MM:SS". 원본 `hms`. `.slice(0, 5)` 로 "HH:MM" 을 쓴다. */
export function hms(sec: number): string {
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  const s = sec % 60;
  return `${pad(h)}:${pad(m)}:${pad(s)}`;
}

/** 지금(또는 d) 시각 "HH:MM". 원본 `clock`. 렌더 중에 부르지 말고 effect·이벤트에서만(SSR 불일치 방지). */
export function clock(d: Date = new Date()): string {
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** 지금 시각을 하루 기준 초로. 원본 liveReset('fresh') 의 시작 시각 계산. */
export function nowSec(d: Date = new Date()): number {
  return d.getHours() * 3600 + d.getMinutes() * 60 + d.getSeconds();
}
