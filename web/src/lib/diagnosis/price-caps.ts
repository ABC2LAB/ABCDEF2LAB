// 제품 설치(manifest v4) 점검의 단가 상한 기록 → 화면 문구 (서버 llmpolicy OpenRouterRunPriceCaps, 회차 기록 model.price_caps,
// docs/openrouter-first-diagnosis.md '점검 시작 때 단가 상한'). 2026-11-23 사용자 결정: '결과 불명'이 단가 상한 때문이면
// 어느 제공자가 왜(단가 > 상한) 빠졌는지 화면에서 바로 보이게 한다. 모양이 하나라도 어긋나면 아무 말도 하지 않는다
// (fail-closed — 서버가 준 값이 아닌 원인을 짓지 않는다).

export type PriceCapsProviderStatus = "within_cap" | "over_cap" | "not_offered" | "unknown";

export type PriceCapsProvider = {
  slug: string;
  name: string;
  status: PriceCapsProviderStatus;
  input: number | null; // 100만 토큰당 USD micros
  output: number | null;
};

export type PriceCaps = {
  source: "requoted" | "install_fallback";
  input: number;
  output: number;
  installInput: number;
  installOutput: number;
  failureReason: string | null;
  notFoundChecked: boolean;
  providers: PriceCapsProvider[];
};

export type PriceCapsNote = { tone: "warn" | "info"; text: string };

const MAX_PRICE_MICROS = 1_000_000_000;
const MAX_PROVIDERS = 8;
const STATUSES: readonly PriceCapsProviderStatus[] = ["within_cap", "over_cap", "not_offered", "unknown"];
const FAILURE_TEXT: Readonly<Record<string, string>> = {
  requote_unavailable: "단가 목록을 읽지 못함",
  requote_invalid: "단가 목록 응답을 읽을 수 없음",
  no_listed_provider_offered: "넘김 목록의 제공자가 지금 이 모델을 제공하지 않음",
};

function obj(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}
function micros(v: unknown): v is number {
  return typeof v === "number" && Number.isSafeInteger(v) && v >= 0 && v <= MAX_PRICE_MICROS;
}
function text(v: unknown, max = 128): v is string {
  return typeof v === "string" && v.length > 0 && v.length <= max;
}

function parseProvider(v: unknown): PriceCapsProvider | null {
  if (
    !obj(v) ||
    !text(v.provider_slug) ||
    !text(v.provider_name) ||
    !STATUSES.includes(v.status as PriceCapsProviderStatus)
  )
    return null;
  const input = v.input_usd_micros_per_million;
  const output = v.output_usd_micros_per_million;
  const priced = micros(input) && micros(output);
  // 단가가 있는 상태(within_cap · over_cap)는 단가가 둘 다 있어야 하고, 없는 상태는 둘 다 null이어야 한다.
  if ((v.status === "within_cap" || v.status === "over_cap") !== priced) return null;
  if (!priced && (input !== null || output !== null)) return null;
  return {
    slug: v.provider_slug,
    name: v.provider_name,
    status: v.status as PriceCapsProviderStatus,
    input: priced ? (input as number) : null,
    output: priced ? (output as number) : null,
  };
}

/** 회차 기록의 model 칸에서 price_caps를 읽는다. 칸이 없거나(v1~v3·재조회 전) 모양이 어긋나면 null이다. */
export function parsePriceCaps(model: unknown): PriceCaps | null {
  if (!obj(model) || !obj(model.price_caps)) return null;
  const c = model.price_caps;
  if (c.source !== "requoted" && c.source !== "install_fallback") return null;
  const nums = [
    c.max_input_price_usd_micros_per_million,
    c.max_output_price_usd_micros_per_million,
    c.install_max_input_price_usd_micros_per_million,
    c.install_max_output_price_usd_micros_per_million,
  ];
  if (!nums.every(micros) || typeof c.queried_at !== "string" || !Number.isFinite(Date.parse(c.queried_at)))
    return null;
  if (c.failure_reason !== undefined && !text(c.failure_reason, 64)) return null;
  if ((c.source === "install_fallback") !== (c.failure_reason !== undefined)) return null;
  if (
    c.not_found_checked_at !== undefined &&
    (typeof c.not_found_checked_at !== "string" || !Number.isFinite(Date.parse(c.not_found_checked_at)))
  )
    return null;
  if (!Array.isArray(c.providers) || c.providers.length === 0 || c.providers.length > MAX_PROVIDERS) return null;
  const providers = c.providers.map(parseProvider);
  if (providers.some((p) => p === null)) return null;
  const [input, output, installInput, installOutput] = nums as number[];
  return {
    source: c.source,
    input,
    output,
    installInput,
    installOutput,
    failureReason: (c.failure_reason as string | undefined) ?? null,
    notFoundChecked: c.not_found_checked_at !== undefined,
    providers: providers as PriceCapsProvider[],
  };
}

/** 100만 토큰당 USD micros → "$2.28" (뒤 0은 뗀다). */
export function usdPerMillion(m: number): string {
  return `$${(m / 1_000_000).toFixed(6).replace(/\.?0+$/, "")}`;
}

/**
 * 화면 문구. 경고(warn)는 상한 때문에 빠졌거나 빠질 수 있는 경우만, 나머지는 정보(info) 한 줄이다.
 * - 재조회 실패: 설치 상한을 썼고 그보다 비싼 제공자는 빠진다는 것
 * - 점검 중 404 뒤 다시 읽은 결과: 단가 > 상한으로 빠진 제공자와 그 단가 · 상한
 * - 지금 이 모델을 제공하지 않는 넘김 목록 제공자
 */
export function priceCapsNotes(c: PriceCaps): PriceCapsNote[] {
  const notes: PriceCapsNote[] = [];
  const caps = `입력 ${usdPerMillion(c.input)} · 출력 ${usdPerMillion(c.output)} (100만 토큰당)`;
  if (c.source === "install_fallback") {
    const why = (c.failureReason ? FAILURE_TEXT[c.failureReason] : undefined) ?? "단가를 다시 읽지 못함";
    notes.push({
      tone: "warn",
      text: `단가 상한: ${why}. 설치 때 승인한 상한 ${caps}을 썼어요. 이보다 비싼 제공자는 빠져요.`,
    });
  } else {
    notes.push({ tone: "info", text: `단가 상한: 점검 시작 때 다시 읽은 단가 × 1.5 — ${caps}.` });
  }
  if (c.notFoundChecked) {
    notes.push({ tone: "warn", text: "점검 중 받을 제공자가 없다는 응답(404)이 와서 단가를 다시 확인했어요." });
  }
  for (const p of c.providers) {
    if (p.status === "over_cap" && p.input !== null && p.output !== null) {
      const parts: string[] = [];
      if (p.input > c.input) parts.push(`입력 ${usdPerMillion(p.input)} > 상한 ${usdPerMillion(c.input)}`);
      if (p.output > c.output) parts.push(`출력 ${usdPerMillion(p.output)} > 상한 ${usdPerMillion(c.output)}`);
      notes.push({
        tone: "warn",
        text: `${p.name}: 단가가 상한보다 높아 빠졌어요 (${parts.join(", ")}, 100만 토큰당).`,
      });
    } else if (p.status === "not_offered") {
      notes.push({ tone: "warn", text: `${p.name}: 지금 이 모델을 제공하지 않아 빠졌어요.` });
    }
  }
  return notes;
}
