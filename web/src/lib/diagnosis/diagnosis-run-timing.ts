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

/**
 * 실행 backend가 실제로 집행하는 회차 시간 상한(docs/contracts/diagnosis-run-timing.md §시간 상한).
 * model_close_at + model_drain_ms + confirmation_reserve_ms + shutdown_margin_ms = run_deadline_at 이 정확히 성립한다.
 */
export type URLTimeLimit = {
  run_deadline_at: string;
  model_close_at: string;
  model_drain_ms: number;
  confirmation_reserve_ms: number;
  shutdown_margin_ms: number;
};

const maxTimeLimitPartMs = 24 * 60 * 60 * 1000;

/** RFC3339Nano UTC 시각의 유닉스 나노초(BigInt). 형식이 틀리면 undefined. */
export function utcNanos(value: unknown): bigint | undefined {
  if (!validUTCInstant(value)) return undefined;
  const [, y, m, d, h, min, s, fraction = ""] =
    /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?Z$/.exec(value) ?? [];
  const date = new Date(0);
  date.setUTCFullYear(Number(y), Number(m) - 1, Number(d));
  date.setUTCHours(Number(h), Number(min), Number(s), 0);
  return BigInt(date.getTime()) * BigInt(1_000_000) + BigInt(fraction.padEnd(9, "0"));
}

/** 시간 상한 칸의 모양·식을 닫는다. 준비를 시작한 회차에만 있다. 맞지 않으면 undefined. */
export function parseURLTimeLimit(value: unknown, preparationStarted: boolean): URLTimeLimit | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value) || !preparationStarted) return undefined;
  const v = value as Record<string, unknown>;
  const keys = ["run_deadline_at", "model_close_at", "model_drain_ms", "confirmation_reserve_ms", "shutdown_margin_ms"];
  if (Object.keys(v).length !== keys.length || !keys.every((key) => Object.hasOwn(v, key))) return undefined;
  const parts = [v.model_drain_ms, v.confirmation_reserve_ms, v.shutdown_margin_ms];
  if (!parts.every((n) => Number.isSafeInteger(n) && (n as number) >= 0 && (n as number) <= maxTimeLimitPartMs))
    return undefined;
  const deadline = utcNanos(v.run_deadline_at);
  const close = utcNanos(v.model_close_at);
  const total = parts.reduce<bigint>((sum, n) => sum + BigInt(n as number) * BigInt(1_000_000), BigInt(0));
  if (deadline === undefined || close === undefined || close + total !== deadline) return undefined;
  return {
    run_deadline_at: v.run_deadline_at as string,
    model_close_at: v.model_close_at as string,
    model_drain_ms: v.model_drain_ms as number,
    confirmation_reserve_ms: v.confirmation_reserve_ms as number,
    shutdown_margin_ms: v.shutdown_margin_ms as number,
  };
}

/** 한 번 받은 시간 상한은 지우거나 바꾸지 않는다. 시작이 확인된 뒤 처음 나타나는 상한(사후 보충)도 받지 않는다. */
export function preservesURLTimeLimit(
  next: { started?: boolean; time_limit?: URLTimeLimit },
  previous: { started?: boolean; time_limit?: URLTimeLimit },
): boolean {
  const a = previous.time_limit;
  const b = next.time_limit;
  if (a === undefined) return !(previous.started === true && b !== undefined);
  return (
    b !== undefined &&
    sameURLRunTime(a.run_deadline_at, b.run_deadline_at) &&
    sameURLRunTime(a.model_close_at, b.model_close_at) &&
    a.model_drain_ms === b.model_drain_ms &&
    a.confirmation_reserve_ms === b.confirmation_reserve_ms &&
    a.shutdown_margin_ms === b.shutdown_margin_ms
  );
}
