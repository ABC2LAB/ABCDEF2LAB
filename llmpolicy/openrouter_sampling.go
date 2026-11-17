package llmpolicy

import (
	"bytes"
	"encoding/json"
	"math"
)

// OpenRouterSampling은 manifest v3가 고정하는 샘플링 값이다(탐색 분리 배치 결정안 D(b), 2026-11-16 사용자 결정). 칸 이름은
// 평가 게이트웨이가 api 요청에 넣는 일곱 칸과 같다(평가 프로토콜 §6). transport가 매 요청에 이 값을 넣고, 호출자(엔진)가
// 보낸 temperature는 덮어쓴다. 다른 샘플링 칸은 요청 허용 목록 밖이라 호출자가 보낼 수 없다. 그래서 전송되는 샘플링 값의
// 출처는 manifest 하나다.
type OpenRouterSampling struct {
	Temperature       float64 `json:"temperature"`
	TopP              float64 `json:"top_p"`
	TopK              int64   `json:"top_k"`
	MinP              float64 `json:"min_p"`
	PresencePenalty   float64 `json:"presence_penalty"`
	RepetitionPenalty float64 `json:"repetition_penalty"`
	Seed              int64   `json:"seed"`
}

// OpenRouterDefaultSampling은 제품 기본 샘플링이다. 평가 프로토콜 §6 고정값(모델 공식 카드의 thinking 모드·일반 작업
// 권장값과 seed 42)과 같다. 설치 입력 예시가 이 값을 쓴다.
var OpenRouterDefaultSampling = OpenRouterSampling{Temperature: 1.0, TopP: 0.95, TopK: 20, MinP: 0, PresencePenalty: 1.5, RepetitionPenalty: 1.0, Seed: 42}

// openRouterSamplingKeys는 sampling 객체의 칸 수다.
const openRouterSamplingKeys = 7

// openRouterSamplingMaxSeed는 JSON 숫자로 정확히 오가는 정수 범위다.
const openRouterSamplingMaxSeed = 1 << 53

// valid는 OpenRouter가 받는 범위 안인지 본다.
func (s OpenRouterSampling) valid() bool {
	finite := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
	return finite(s.Temperature) && s.Temperature >= 0 && s.Temperature <= 2 &&
		finite(s.TopP) && s.TopP > 0 && s.TopP <= 1 &&
		s.TopK >= 0 && s.TopK <= 1000 &&
		finite(s.MinP) && s.MinP >= 0 && s.MinP <= 1 &&
		finite(s.PresencePenalty) && s.PresencePenalty >= -2 && s.PresencePenalty <= 2 &&
		finite(s.RepetitionPenalty) && s.RepetitionPenalty > 0 && s.RepetitionPenalty <= 2 &&
		s.Seed >= -openRouterSamplingMaxSeed && s.Seed <= openRouterSamplingMaxSeed
}

// parseOpenRouterSampling은 manifest의 sampling 객체를 읽는다. 일곱 칸이 정확히 한 번씩 있어야 하고(중복·대소문자 별칭·
// 모르는 칸 거부), 값은 JSON 숫자여야 하며, 정수 칸에 소수를 받지 않고, 범위 밖이면 거부한다.
func parseOpenRouterSampling(raw json.RawMessage) (OpenRouterSampling, bool) {
	d := json.NewDecoder(bytes.NewReader(raw))
	// encoding/json은 null을 수치 칸의 0으로 받는다. temperature·top_k·min_p·presence_penalty·seed는 0이 범위 안이라
	// 범위 검사로 걸리지 않으므로, 구조체로 읽기 전에 원문 값이 숫자인지 따로 본다(UseNumber면 숫자만 json.Number로 온다).
	d.UseNumber()
	known := map[string]bool{"temperature": true, "top_p": true, "top_k": true, "min_p": true, "presence_penalty": true, "repetition_penalty": true, "seed": true}
	if !manifestObjectRange(d, openRouterSamplingKeys, openRouterSamplingKeys, func(k string) bool {
		var v any
		if !known[k] || d.Decode(&v) != nil {
			return false
		}
		_, number := v.(json.Number)
		return number
	}) {
		return OpenRouterSampling{}, false
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	var s OpenRouterSampling
	if strict.Decode(&s) != nil || !s.valid() {
		return OpenRouterSampling{}, false
	}
	return s, true
}

// wire는 요청 본문에 넣을 칸이다.
func (s OpenRouterSampling) wire() map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, openRouterSamplingKeys)
	for k, v := range map[string]any{"temperature": s.Temperature, "top_p": s.TopP, "top_k": s.TopK, "min_p": s.MinP,
		"presence_penalty": s.PresencePenalty, "repetition_penalty": s.RepetitionPenalty, "seed": s.Seed} {
		out[k], _ = json.Marshal(v)
	}
	return out
}
