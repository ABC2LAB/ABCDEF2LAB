package llmpolicy

import (
	"bytes"
	"encoding/json"
	"io"
)

// 제공자 모드다(manifest v4 provider_routing.mode). 고정은 승인한 endpoint 하나만 쓰고 넘기지 않는다(v1~v3과 같은
// 동작). 자동 넘김은 적어 둔 목록 안에서 순서대로 넘어간다. 어느 모드든 목록 밖 제공자의 응답은 받지 않는다.
const (
	OpenRouterProviderPinned   = "pinned"
	OpenRouterProviderFallback = "fallback"
)

// openRouterMaxFallbackProviders는 자동 넘김 목록의 형식 상한이다. 실제 목록은 설치자가 manifest에 고정한다.
const openRouterMaxFallbackProviders = 8

// openRouterProviderRouting은 manifest v4의 provider_routing이다. Providers는 자동 넘김 모드에서만 있고, 순서가 곧
// 시도 순서다. 첫 항목은 manifest의 provider_slug·expected_provider_name과 같아야 한다.
type openRouterProviderRouting struct {
	Mode      string                   `json:"mode"`
	Providers []openRouterProviderPair `json:"providers,omitempty"`
}

// openRouterProviderPair는 승인한 endpoint tag와 응답에서 검증할 표시 이름의 짝이다.
type openRouterProviderPair struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// parseOpenRouterProviderRouting은 manifest의 provider_routing 객체를 읽는다. 중복 키·모르는 칸·문자열이 아닌 값을
// 거부하고, 고정 모드면 목록이 없어야 하며, 자동 넘김이면 목록이 2개 이상 상한 이하·tag와 이름이 각각 중복 없음이어야 한다.
func parseOpenRouterProviderRouting(raw json.RawMessage) (openRouterProviderRouting, bool) {
	var out openRouterProviderRouting
	var hasMode, hasProviders bool
	d := json.NewDecoder(bytes.NewReader(raw))
	ok := manifestObjectRange(d, 1, 2, func(k string) bool {
		switch k {
		case "mode":
			hasMode = true
			return d.Decode(&out.Mode) == nil && out.Mode != ""
		case "providers":
			hasProviders = true
			if !manifestDelimiter(d, '[') {
				return false
			}
			for d.More() {
				var pair openRouterProviderPair
				var slug, name bool
				if len(out.Providers) == openRouterMaxFallbackProviders || !manifestObjectRange(d, 2, 2, func(k string) bool {
					var v any
					if d.Decode(&v) != nil {
						return false
					}
					s, isString := v.(string)
					switch k {
					case "slug":
						pair.Slug, slug = s, true
					case "name":
						pair.Name, name = s, true
					default:
						return false
					}
					return isString
				}) || !slug || !name {
					return false
				}
				out.Providers = append(out.Providers, pair)
			}
			return manifestDelimiter(d, ']')
		}
		return false
	})
	if _, err := d.Token(); !ok || err != io.EOF || !hasMode {
		return openRouterProviderRouting{}, false
	}
	switch out.Mode {
	case OpenRouterProviderPinned:
		if hasProviders {
			return openRouterProviderRouting{}, false
		}
	case OpenRouterProviderFallback:
		if len(out.Providers) < 2 {
			return openRouterProviderRouting{}, false
		}
		slugs, names := map[string]bool{}, map[string]bool{}
		for _, p := range out.Providers {
			if !openRouterSlug.MatchString(p.Slug) || !validProfileValue(p.Name, 128) || slugs[p.Slug] || names[p.Name] {
				return openRouterProviderRouting{}, false
			}
			slugs[p.Slug], names[p.Name] = true, true
		}
	default:
		return openRouterProviderRouting{}, false
	}
	return out, true
}

// providers는 이 승인이 받는 제공자 목록이다(시도 순서). 고정 모드와 v1~v3은 승인 endpoint 하나다.
func (m openRouterManifest) providers() []openRouterProviderPair {
	if m.ProviderRouting != nil && m.ProviderRouting.Mode == OpenRouterProviderFallback {
		return m.ProviderRouting.Providers
	}
	return []openRouterProviderPair{{Slug: m.ProviderSlug, Name: m.ExpectedProviderName}}
}

// fallback은 자동 넘김 모드인지다.
func (m openRouterManifest) fallback() bool { return len(m.providers()) > 1 }

// providerSlug는 응답이 알려 준 제공자 표시 이름의 승인 tag다. 목록에 없는 이름이면 false다(그 응답은 받지 않는다).
func (m openRouterManifest) providerSlug(name string) (string, bool) {
	for _, p := range m.providers() {
		if p.Name == name {
			return p.Slug, true
		}
	}
	return "", false
}

// wireProvider는 요청 본문 provider 칸이다. 개인정보·매개변수·단가 조건은 모드와 상관없이 같다. 고정 모드는 only 하나에
// 넘김을 끄고, 자동 넘김은 only와 order에 같은 목록을 넣어 목록 안에서만 순서대로 넘어가게 한다.
func (m openRouterManifest) wireProvider() map[string]any {
	slugs := make([]string, 0, len(m.providers()))
	for _, p := range m.providers() {
		slugs = append(slugs, p.Slug)
	}
	provider := map[string]any{"only": slugs, "allow_fallbacks": false, "require_parameters": true, "data_collection": "deny", "zdr": true,
		"max_price": map[string]float64{"prompt": float64(m.InputPrice) / 1000000, "completion": float64(m.OutputPrice) / 1000000}}
	if m.fallback() {
		provider["order"], provider["allow_fallbacks"] = slugs, true
	}
	return provider
}

// OpenRouterProviderCount는 제공자 tag 하나가 정상 응답을 준 횟수다.
type OpenRouterProviderCount struct {
	Slug      string `json:"provider_slug"`
	Responses int64  `json:"responses"`
}

// OpenRouterProviderCounts는 자동 넘김 모드의 제공자별 정상 응답 수다. 사용량 기록이 값으로 비교·복사되므로 고정 길이
// 배열로 두고, JSON에는 채운 항목만 목록으로 쓴다. 순서는 처음 응답한 순서다.
type OpenRouterProviderCounts [openRouterMaxFallbackProviders]OpenRouterProviderCount

func (c *OpenRouterProviderCounts) add(slug string) {
	for i := range c {
		if c[i].Slug == slug || c[i].Slug == "" {
			c[i].Slug = slug
			c[i].Responses++
			return
		}
	}
}

// Minus는 기준 시점 뒤의 응답 수만 남긴다(준비 단계 호출을 뺀 실행 구간 기록). 0이 된 제공자는 뺀다.
func (c OpenRouterProviderCounts) Minus(base OpenRouterProviderCounts) OpenRouterProviderCounts {
	var out OpenRouterProviderCounts
	n := 0
	for _, e := range c {
		for _, b := range base {
			if b.Slug == e.Slug {
				e.Responses -= b.Responses
			}
		}
		if e.Slug != "" && e.Responses > 0 {
			out[n] = e
			n++
		}
	}
	return out
}

func (c OpenRouterProviderCounts) MarshalJSON() ([]byte, error) {
	out := make([]OpenRouterProviderCount, 0, len(c))
	for _, e := range c {
		if e.Slug != "" {
			out = append(out, e)
		}
	}
	return json.Marshal(out)
}

func (c *OpenRouterProviderCounts) UnmarshalJSON(data []byte) error {
	var in []OpenRouterProviderCount
	if err := json.Unmarshal(data, &in); err != nil {
		return err
	}
	if len(in) > len(c) {
		return ErrOpenRouterPolicy
	}
	*c = OpenRouterProviderCounts{}
	copy(c[:], in)
	return nil
}

// perRun은 승인 총량이 점검 하나의 값인지다(v4). v1~v3은 승인 하나를 누적해 센다.
func (p *OpenRouterPolicy) perRun() bool { return p != nil && p.manifest.Version >= 4 }

// PerRunTotals는 이 승인의 총량(max_*)이 점검 하나의 값인지다. 설치는 참이면 위임 실행 횟수만큼 합산 예산을 잡고,
// 거짓이면 지금처럼 실행 한 번으로 묶는다.
func (p *OpenRouterPolicy) PerRunTotals() bool { return p.perRun() }
