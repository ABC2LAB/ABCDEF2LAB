type RunTiming = {
  started?: boolean;
  drain_complete?: boolean;
  started_at?: string;
  finished_at?: string;
};

// 서버가 출력하는 RFC3339Nano UTC만 받는다. Date.parse의 날짜 보정·밀리초 절삭을 사용하지 않는다.
function utcInstant(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const parts = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?Z$/.exec(value);
  if (!parts) return undefined;
  const [, y, m, d, h, min, s, fraction = ""] = parts;
  const year = Number(y);
  const month = Number(m);
  const day = Number(d);
  const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
  const days = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];
  if (
    year < 1 ||
    month < 1 ||
    month > 12 ||
    day < 1 ||
    day > days[month - 1] ||
    Number(h) > 23 ||
    Number(min) > 59 ||
    Number(s) > 59
  )
    return undefined;
  const instant = `${y}${m}${d}${h}${min}${s}${fraction.padEnd(9, "0")}`;
  return instant === "00010101000000000000000" ? undefined : instant;
}

/** 서버 RFC3339Nano UTC 시각인가(날짜 보정 · 0 시각 없음). 실행 시각과 같은 규칙. */
export function validUTCInstant(value: unknown): value is string {
  return utcInstant(value) !== undefined;
}

/** 구 기록의 생략은 허용하되, 시각은 확인된 사실에 결속하고 나노초 순서까지 검사한다. */
export function validURLRunTiming(value: Record<string, unknown>): boolean {
  const start = Object.hasOwn(value, "started_at") ? utcInstant(value.started_at) : undefined;
  const finish = Object.hasOwn(value, "finished_at") ? utcInstant(value.finished_at) : undefined;
  return (
    (!Object.hasOwn(value, "started_at") || (start !== undefined && value.started === true)) &&
    (!Object.hasOwn(value, "finished_at") || (finish !== undefined && value.drain_complete === true)) &&
    (start === undefined || finish === undefined || finish >= start)
  );
}

export function sameURLRunTime(a: string | undefined, b: string | undefined): boolean {
  const instant = utcInstant(a);
  return a === b || (instant !== undefined && instant === utcInstant(b));
}

/** 한 번 저장한 시각을 바꾸거나, 이미 true였던 구 사실의 누락 시각을 나중에 채우지 않는다. */
export function preservesURLRunTiming(next: RunTiming, previous: RunTiming): boolean {
  return (
    (previous.started_at === undefined || sameURLRunTime(next.started_at, previous.started_at)) &&
    (previous.finished_at === undefined || sameURLRunTime(next.finished_at, previous.finished_at)) &&
    !(previous.started === true && previous.started_at === undefined && next.started_at !== undefined) &&
    !(previous.drain_complete === true && previous.finished_at === undefined && next.finished_at !== undefined)
  );
}
