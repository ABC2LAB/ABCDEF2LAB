package llmpolicy

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"time"
)

// 제품 설치(manifest v4) 점검의 단가 상한 재조회(2026-11-23 사용자 결정, 화면 VAmPI 실측 발견 5).
//
// 11/23 화면 VAmPI에서 설치 승인 단가 상한(출력 $1.50)이 PrimeIntellect의 그때 단가($1.52)보다 낮아, 자동 넘김 설치는
// DeepInfra만 남고 고정 설치는 바로 404로 '결과 불명'이 됐다. 단가는 하루에도 바뀐다. 그래서 v4 점검은 시작할 때(첫 호출
// 전) OpenRouter 공개 목록을 한 번 다시 읽어, 넘김 목록 제공자의 가장 높은 단가 × 1.5를 그 점검의 상한으로 쓴다. 다시
// 읽지 못하면 설치 때 승인한 상한을 그대로 쓰고 그 사실을 기록한다. 점검당 비용 상한(max_cost)은 그대로라 총비용은 지금처럼
// 막힌다. 404가 나면 한 번 더 읽어 어느 제공자가 '단가 > 상한'으로 빠졌는지 남긴다.
//
// v1~v3(평가 manifest·공식 세션)은 바꾸지 않는다. 공식 세션은 러너가 세션 시작 직전 재조회 단가 × 1.5로 manifest를 만들고
// 시작 점검이 상한 ≥ 단가 × 1.2를 확인한다(scripts/eval/price_caps.py). 회차 안에서 상한이 바뀌면 같은 세션의 회차끼리
// 조건이 달라지므로 그 경로에는 이 재조회를 걸지 않는다.

const (
	// openRouterRunPriceFactor는 재조회 단가에 곱하는 배수다(공식 세션 규칙과 같은 1.5).
	openRouterRunPriceFactorNum = 3
	openRouterRunPriceFactorDen = 2
	// openRouterEndpointsLimit·Timeout은 공개 목록 응답 크기와 조회 시간 상한이다. 목록은 제공자 수십 개라 수십 KB다.
	openRouterEndpointsLimit   = 512 << 10
	openRouterEndpointsTimeout = 10 * time.Second
	// openRouterMaxPriceMicros는 상한 계산 결과의 형식 범위다(manifest 검사와 같은 값).
	openRouterMaxPriceMicros = 1000000000
)

// 재조회 결과의 출처와 제공자 상태 어휘.
const (
	OpenRouterPriceRequoted        = "requoted"
	OpenRouterPriceInstallFallback = "install_fallback"

	OpenRouterProviderWithinCap  = "within_cap"
	OpenRouterProviderOverCap    = "over_cap"
	OpenRouterProviderNotOffered = "not_offered"
	OpenRouterProviderUnknown    = "unknown"

	openRouterRequoteUnavailable = "requote_unavailable"
	openRouterRequoteInvalid     = "requote_invalid"
	openRouterRequoteNoProvider  = "no_listed_provider_offered"
)

var errOpenRouterRequote = errors.New("llmpolicy: OpenRouter 단가를 다시 읽지 못했습니다")

// OpenRouterProviderPrice는 넘김 목록 제공자 하나의 조회 단가와 상한 대비 상태다(100만 토큰당 USD micros).
type OpenRouterProviderPrice struct {
	Slug         string `json:"provider_slug"`
	Name         string `json:"provider_name"`
	Status       string `json:"status"`
	InputMicros  *int64 `json:"input_usd_micros_per_million"`
	OutputMicros *int64 `json:"output_usd_micros_per_million"`
}

// OpenRouterRunPriceCaps는 v4 점검 하나가 쓴 단가 상한과 그 근거다. 회차 기록(OpenRouterUsage.PriceCaps)에 남는다.
type OpenRouterRunPriceCaps struct {
	Source              string                    `json:"source"`
	InputMicros         int64                     `json:"max_input_price_usd_micros_per_million"`
	OutputMicros        int64                     `json:"max_output_price_usd_micros_per_million"`
	InstallInputMicros  int64                     `json:"install_max_input_price_usd_micros_per_million"`
	InstallOutputMicros int64                     `json:"install_max_output_price_usd_micros_per_million"`
	QueriedAt           time.Time                 `json:"queried_at"`
	FailureReason       string                    `json:"failure_reason,omitempty"`
	Providers           []OpenRouterProviderPrice `json:"providers"`
	// NotFoundChecked는 점검 중 404(받을 제공자 없음)가 나서 단가를 다시 읽은 시각이다. 그때의 제공자 상태가 Providers에 있다.
	NotFoundChecked *time.Time `json:"not_found_checked_at,omitempty"`
}

// OpenRouterEndpointsFetcher는 모델의 OpenRouter 공개 제공자 목록(JSON)을 읽는다.
type OpenRouterEndpointsFetcher func(ctx context.Context, model string) ([]byte, error)

// FetchOpenRouterEndpoints는 공개 목록 GET 하나다. 키·인증 헤더를 보내지 않는다. 연결은 승인 호출과 같은 주소 정책
// (openrouter.ai:443, 공인 주소만)을 따르고 프록시·리다이렉트를 쓰지 않는다.
func FetchOpenRouterEndpoints(ctx context.Context, model string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, openRouterEndpointsTimeout)
	defer cancel()
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true, Protocols: protocols,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: openRouterEndpointsTimeout,
		ResponseHeaderTimeout: openRouterEndpointsTimeout, DialContext: openRouterDial},
		CheckRedirect: func(*http.Request, []*http.Request) error { return errOpenRouterRequote }}
	// 모델 이름은 승인 manifest가 고정한 값(OpenRouterModel)이라 경로 조각 검사를 다시 하지 않는다.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, OpenRouterBaseURL+"/models/"+model+"/endpoints", nil)
	if err != nil || req.URL.Host != "openrouter.ai" {
		return nil, errOpenRouterRequote
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errOpenRouterRequote
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, openRouterEndpointsLimit+1))
	if err != nil || resp.StatusCode != http.StatusOK || len(data) > openRouterEndpointsLimit {
		return nil, errOpenRouterRequote
	}
	return data, nil
}

// openRouterEndpointPrice는 공개 목록 한 줄이다. 단가는 토큰당 USD 문자열이다.
type openRouterEndpointPrice struct {
	Tag     string `json:"tag"`
	Pricing struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
	} `json:"pricing"`
}

// parseOpenRouterEndpoints는 tag별 단가(100만 토큰당 micros, 올림)를 꺼낸다. 단가가 읽히지 않는 줄은 버린다.
func parseOpenRouterEndpoints(data []byte) (map[string][2]int64, error) {
	var body struct {
		Data struct {
			Endpoints []openRouterEndpointPrice `json:"endpoints"`
		} `json:"data"`
	}
	if json.Unmarshal(data, &body) != nil {
		return nil, errOpenRouterRequote
	}
	out := map[string][2]int64{}
	for _, e := range body.Data.Endpoints {
		in, okIn := openRouterMicrosPerMillion(e.Pricing.Prompt)
		outPrice, okOut := openRouterMicrosPerMillion(e.Pricing.Completion)
		if e.Tag == "" || !okIn || !okOut {
			continue
		}
		out[e.Tag] = [2]int64{in, outPrice}
	}
	return out, nil
}

// openRouterMicrosPerMillion은 토큰당 USD 문자열을 100만 토큰당 USD micros로 바꾼다(정확한 유리수 계산 뒤 올림).
func openRouterMicrosPerMillion(perToken string) (int64, bool) {
	r, ok := new(big.Rat).SetString(perToken)
	if !ok || r.Sign() < 0 {
		return 0, false
	}
	r.Mul(r, big.NewRat(1000000000000, 1))
	return ratCeil(r, openRouterMaxPriceMicros)
}

func ratCeil(r *big.Rat, limit int64) (int64, bool) {
	q, m := new(big.Int).QuoRem(r.Num(), r.Denom(), new(big.Int))
	if m.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() || q.Int64() > limit {
		return 0, false
	}
	return q.Int64(), true
}

// requoteCaps는 넘김 목록 제공자의 조회 단가로 상한을 계산한다. 목록 제공자 중 하나도 지금 없으면 실패다.
func (m openRouterManifest) requoteCaps(prices map[string][2]int64) (int64, int64, []OpenRouterProviderPrice, bool) {
	var maxIn, maxOut int64
	offered := false
	providers := make([]OpenRouterProviderPrice, 0, len(m.providers()))
	for _, p := range m.providers() {
		row := OpenRouterProviderPrice{Slug: p.Slug, Name: p.Name, Status: OpenRouterProviderNotOffered}
		if price, ok := prices[p.Slug]; ok {
			in, out := price[0], price[1]
			row.Status, row.InputMicros, row.OutputMicros = OpenRouterProviderWithinCap, &in, &out
			maxIn, maxOut, offered = max(maxIn, in), max(maxOut, out), true
		}
		providers = append(providers, row)
	}
	if !offered {
		return 0, 0, providers, false
	}
	capIn, okIn := ratCeil(big.NewRat(maxIn*openRouterRunPriceFactorNum, openRouterRunPriceFactorDen), openRouterMaxPriceMicros)
	capOut, okOut := ratCeil(big.NewRat(maxOut*openRouterRunPriceFactorNum, openRouterRunPriceFactorDen), openRouterMaxPriceMicros)
	if !okIn || !okOut || capIn <= 0 || capOut <= 0 {
		return 0, 0, providers, false
	}
	return capIn, capOut, providers, true
}

// markOverCap은 상한을 넘는 제공자를 over_cap으로 표시한다.
func markOverCap(providers []OpenRouterProviderPrice, capIn, capOut int64) {
	for i := range providers {
		p := &providers[i]
		if p.InputMicros != nil && p.OutputMicros != nil {
			p.Status = OpenRouterProviderWithinCap
			if *p.InputMicros > capIn || *p.OutputMicros > capOut {
				p.Status = OpenRouterProviderOverCap
			}
		}
	}
}

// ApplyRunPriceRequote는 v4 점검의 첫 호출 전에 단가 상한을 다시 정한다. 다시 읽지 못하면 설치 상한을 그대로 쓰고 그
// 사실을 기록한다(오류를 돌려주지 않는다 — 점검은 설치 상한으로 진행한다). v1~v3 승인·이미 호출한 정책은 바꾸지 않고
// ErrOpenRouterPolicy다.
func (p *OpenRouterPolicy) ApplyRunPriceRequote(ctx context.Context, fetch OpenRouterEndpointsFetcher, now time.Time) error {
	if p == nil || fetch == nil || !p.perRun() {
		return ErrOpenRouterPolicy
	}
	p.mu.Lock()
	installIn, installOut, model := p.manifest.InputPrice, p.manifest.OutputPrice, p.manifest.Model
	started := p.usage.Requests != 0 || p.active != 0 || p.usage.PriceCaps != nil
	p.mu.Unlock()
	if started {
		return ErrOpenRouterPolicy
	}
	caps := OpenRouterRunPriceCaps{Source: OpenRouterPriceInstallFallback, InputMicros: installIn, OutputMicros: installOut,
		InstallInputMicros: installIn, InstallOutputMicros: installOut, QueriedAt: now.UTC()}
	for _, pr := range p.manifest.providers() {
		caps.Providers = append(caps.Providers, OpenRouterProviderPrice{Slug: pr.Slug, Name: pr.Name, Status: OpenRouterProviderUnknown})
	}
	data, err := fetch(ctx, model)
	switch {
	case err != nil:
		caps.FailureReason = openRouterRequoteUnavailable
	default:
		prices, parseErr := parseOpenRouterEndpoints(data)
		if parseErr != nil {
			caps.FailureReason = openRouterRequoteInvalid
			break
		}
		capIn, capOut, providers, ok := p.manifest.requoteCaps(prices)
		caps.Providers = providers
		if !ok {
			caps.FailureReason = openRouterRequoteNoProvider
			break
		}
		caps.Source, caps.InputMicros, caps.OutputMicros = OpenRouterPriceRequoted, capIn, capOut
		markOverCap(caps.Providers, capIn, capOut)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.usage.Requests != 0 || p.active != 0 || p.usage.PriceCaps != nil {
		return ErrOpenRouterPolicy
	}
	p.manifest.InputPrice, p.manifest.OutputPrice = caps.InputMicros, caps.OutputMicros
	p.priceFetch = fetch
	p.usage.PriceCaps = &caps
	return nil
}

// explainNotFound는 점검 중 404(받을 제공자 없음)가 났을 때 단가를 한 번 더 읽어, 지금 상한을 넘어 빠진 제공자를 기록한다.
// 읽지 못하면 기록하지 않는다(원인을 지어내지 않는다). 재조회한 v4 점검에서만 한 번 한다.
func (p *OpenRouterPolicy) explainNotFound(ctx context.Context, now time.Time) {
	p.mu.Lock()
	fetch, caps := p.priceFetch, p.usage.PriceCaps
	if fetch == nil || caps == nil || caps.NotFoundChecked != nil {
		p.mu.Unlock()
		return
	}
	model, capIn, capOut := p.manifest.Model, p.manifest.InputPrice, p.manifest.OutputPrice
	p.mu.Unlock()
	data, err := fetch(ctx, model)
	if err != nil {
		return
	}
	prices, err := parseOpenRouterEndpoints(data)
	if err != nil {
		return
	}
	_, _, providers, _ := p.manifest.requoteCaps(prices)
	markOverCap(providers, capIn, capOut)
	at := now.UTC()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.usage.PriceCaps == nil || p.usage.PriceCaps.NotFoundChecked != nil {
		return
	}
	next := *p.usage.PriceCaps
	next.Providers, next.NotFoundChecked = providers, &at
	p.usage.PriceCaps = &next
}
