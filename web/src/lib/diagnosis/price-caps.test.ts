import { describe, expect, it } from "vitest";

import { type PriceCaps, parsePriceCaps, priceCapsNotes, usdPerMillion } from "./price-caps";

function parsed(model: unknown): PriceCaps {
  const c = parsePriceCaps(model);
  if (!c) throw new Error("price_caps를 읽지 못함");
  return c;
}

// 서버 회차 기록 model.price_caps 모양(llmpolicy OpenRouterRunPriceCaps). 11/23 화면 VAmPI 값.
function caps(overrides: Record<string, unknown> = {}) {
  return {
    requests: 1,
    price_caps: {
      source: "requoted",
      max_input_price_usd_micros_per_million: 255000,
      max_output_price_usd_micros_per_million: 2280000,
      install_max_input_price_usd_micros_per_million: 200000,
      install_max_output_price_usd_micros_per_million: 1500000,
      queried_at: "2026-11-23T10:22:33Z",
      providers: [
        {
          provider_slug: "primeintellect/fp8",
          provider_name: "PrimeIntellect",
          status: "within_cap",
          input_usd_micros_per_million: 170000,
          output_usd_micros_per_million: 1520000,
        },
        {
          provider_slug: "deepinfra/fp8",
          provider_name: "DeepInfra",
          status: "within_cap",
          input_usd_micros_per_million: 100000,
          output_usd_micros_per_million: 950000,
        },
      ],
      ...overrides,
    },
  };
}

describe("단가 상한 안내", () => {
  it("재조회한 상한은 정보 한 줄", () => {
    const c = parsed(caps());
    expect(c.source).toBe("requoted");
    expect(priceCapsNotes(c)).toEqual([
      { tone: "info", text: "단가 상한: 점검 시작 때 다시 읽은 단가 × 1.5 — 입력 $0.255 · 출력 $2.28 (100만 토큰당)." },
    ]);
  });

  it("재조회 실패면 설치 상한을 썼다는 경고와 사유", () => {
    const c = parsed(
      caps({
        source: "install_fallback",
        failure_reason: "requote_unavailable",
        max_input_price_usd_micros_per_million: 200000,
        max_output_price_usd_micros_per_million: 1500000,
        providers: [
          {
            provider_slug: "primeintellect/fp8",
            provider_name: "PrimeIntellect",
            status: "unknown",
            input_usd_micros_per_million: null,
            output_usd_micros_per_million: null,
          },
        ],
      }),
    );
    expect(priceCapsNotes(c)).toEqual([
      {
        tone: "warn",
        text: "단가 상한: 단가 목록을 읽지 못함. 설치 때 승인한 상한 입력 $0.2 · 출력 $1.5 (100만 토큰당)을 썼어요. 이보다 비싼 제공자는 빠져요.",
      },
    ]);
  });

  it("404 뒤 다시 읽어 상한을 넘은 제공자는 누가 · 왜 빠졌는지 말한다", () => {
    const c = parsed(
      caps({
        not_found_checked_at: "2026-11-23T10:30:00Z",
        providers: [
          {
            provider_slug: "primeintellect/fp8",
            provider_name: "PrimeIntellect",
            status: "over_cap",
            input_usd_micros_per_million: 170000,
            output_usd_micros_per_million: 3000000,
          },
          {
            provider_slug: "deepinfra/fp8",
            provider_name: "DeepInfra",
            status: "not_offered",
            input_usd_micros_per_million: null,
            output_usd_micros_per_million: null,
          },
        ],
      }),
    );
    const texts = priceCapsNotes(c).map((n) => `${n.tone}:${n.text}`);
    expect(texts).toContain("warn:점검 중 받을 제공자가 없다는 응답(404)이 와서 단가를 다시 확인했어요.");
    expect(texts).toContain("warn:PrimeIntellect: 단가가 상한보다 높아 빠졌어요 (출력 $3 > 상한 $2.28, 100만 토큰당).");
    expect(texts).toContain("warn:DeepInfra: 지금 이 모델을 제공하지 않아 빠졌어요.");
  });

  it("칸이 없거나 모양이 어긋나면 말하지 않는다", () => {
    expect(parsePriceCaps({ requests: 1 })).toBeNull();
    expect(parsePriceCaps(undefined)).toBeNull();
    const bad: Record<string, unknown>[] = [
      { source: "guess" },
      { max_output_price_usd_micros_per_million: -1 },
      { max_output_price_usd_micros_per_million: 1.5 },
      { queried_at: "어제" },
      { failure_reason: "requote_unavailable" }, // 재조회했는데 실패 사유
      { source: "install_fallback" }, // 설치값인데 사유 없음
      { providers: [] },
      {
        providers: [
          {
            provider_slug: "x/fp8",
            provider_name: "X",
            status: "within_cap",
            input_usd_micros_per_million: null,
            output_usd_micros_per_million: null,
          },
        ],
      },
      {
        providers: [
          {
            provider_slug: "x/fp8",
            provider_name: "X",
            status: "not_offered",
            input_usd_micros_per_million: 1,
            output_usd_micros_per_million: 1,
          },
        ],
      },
      {
        providers: [
          {
            provider_slug: "x/fp8",
            provider_name: "X",
            status: "banned",
            input_usd_micros_per_million: null,
            output_usd_micros_per_million: null,
          },
        ],
      },
      { not_found_checked_at: 5 },
    ];
    for (const o of bad) expect(parsePriceCaps(caps(o))).toBeNull();
  });

  it("금액 표기", () => {
    expect(usdPerMillion(2280000)).toBe("$2.28");
    expect(usdPerMillion(255000)).toBe("$0.255");
    expect(usdPerMillion(1)).toBe("$0.000001");
    expect(usdPerMillion(0)).toBe("$0");
  });
});
