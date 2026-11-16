package llmpolicy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// samplingManifest는 v3 manifest 원문이다. sampling 원문은 그대로 끼워 넣어 칸 순서·중복·숫자 표기를 시험할 수 있다.
func samplingManifest(t *testing.T, version int, sampling string) []byte {
	t.Helper()
	m := openRouterManifest{Version: version, ProfileID: 7, Format: "openai", BaseURL: OpenRouterBaseURL, Model: OpenRouterModel,
		ProviderSlug: "fixture/endpoint", ExpectedProviderName: "Fixture", ApprovalID: "fixture-approval",
		ExpiresAt: time.Now().Add(time.Hour), StrictPrivacyAck: true, AccountSpendCapAck: true,
		MaxRequests: 3, MaxInputTokens: 3 * openRouterContext, MaxOutputTokens: 300, MaxCostMicros: 1000000,
		MaxInputPerCall: openRouterContext, MaxOutputPerCall: 100, TimeoutSeconds: 2, MaxConcurrent: 1, InputPrice: 1000000, OutputPrice: 1000000}
	if version >= 2 {
		m.MaxConsecutiveTransient = 3
	}
	raw, _ := json.Marshal(m)
	if sampling == "" {
		return raw
	}
	return append(raw[:len(raw)-1], []byte(`,"sampling":`+sampling+`}`)...)
}

const defaultSamplingJSON = `{"temperature":1.0,"top_p":0.95,"top_k":20,"min_p":0,"presence_penalty":1.5,"repetition_penalty":1.0,"seed":42}`

func samplingPolicy(t *testing.T, raw []byte) *OpenRouterPolicy {
	t.Helper()
	p, err := ParseOpenRouterManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	p.ledger = func(int64, int64, int64) error { return nil }
	return p
}

// v3만 sampling을 받고, 칸은 정확히 일곱이며 범위·정수 표기를 엄격하게 본다. v1·v2에 sampling이 있으면 거부한다.
func TestOpenRouterManifestV3Sampling(t *testing.T) {
	p, err := ParseOpenRouterManifest(samplingManifest(t, 3, defaultSamplingJSON))
	if err != nil || p.manifest.Sampling == nil || *p.manifest.Sampling != OpenRouterDefaultSampling {
		t.Fatal("v3 manifest with the default sampling refused", err)
	}
	bad := map[string][]byte{
		"v3_without_sampling": samplingManifest(t, 3, ""),
		"v2_with_sampling":    samplingManifest(t, 2, defaultSamplingJSON),
		"v1_with_sampling":    samplingManifest(t, 1, defaultSamplingJSON),
	}
	for name, sampling := range map[string]string{
		"missing_key":      `{"temperature":1.0,"top_p":0.95,"top_k":20,"min_p":0,"presence_penalty":1.5,"repetition_penalty":1.0}`,
		"extra_key":        strings.Replace(defaultSamplingJSON, `"seed":42`, `"seed":42,"frequency_penalty":0`, 1),
		"duplicate_key":    strings.Replace(defaultSamplingJSON, `"seed":42`, `"seed":42,"seed":7`, 1),
		"case_alias":       strings.Replace(defaultSamplingJSON, `"temperature"`, `"Temperature"`, 1),
		"fractional_top_k": strings.Replace(defaultSamplingJSON, `"top_k":20`, `"top_k":20.5`, 1),
		"exponent_seed":    strings.Replace(defaultSamplingJSON, `"seed":42`, `"seed":4.2e1`, 1),
		"string_value":     strings.Replace(defaultSamplingJSON, `"temperature":1.0`, `"temperature":"1.0"`, 1),
		"temperature_high": strings.Replace(defaultSamplingJSON, `"temperature":1.0`, `"temperature":2.5`, 1),
		"top_p_zero":       strings.Replace(defaultSamplingJSON, `"top_p":0.95`, `"top_p":0`, 1),
		"top_k_negative":   strings.Replace(defaultSamplingJSON, `"top_k":20`, `"top_k":-1`, 1),
		"min_p_high":       strings.Replace(defaultSamplingJSON, `"min_p":0`, `"min_p":1.5`, 1),
		"presence_low":     strings.Replace(defaultSamplingJSON, `"presence_penalty":1.5`, `"presence_penalty":-3`, 1),
		"repetition_zero":  strings.Replace(defaultSamplingJSON, `"repetition_penalty":1.0`, `"repetition_penalty":0`, 1),
		"seed_unsafe":      strings.Replace(defaultSamplingJSON, `"seed":42`, `"seed":9007199254740993`, 1),
		"not_object":       `[1.0,0.95]`,
		"null":             `null`,
	} {
		bad[name] = samplingManifest(t, 3, sampling)
	}
	for name, raw := range bad {
		if _, err := ParseOpenRouterManifest(raw); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// 샘플링 칸 값의 출처는 manifest 하나다(게이트웨이 주입과 같은 의미): 엔진이 보낸 temperature는 덮어쓰고, 다른 샘플링
// 칸은 요청 허용 목록이 거부하며, 메시지 글은 칸에 영향이 없다. 사용량 기록에는 실제로 보낸 값이 남는다.
func TestOpenRouterSamplingComesOnlyFromManifest(t *testing.T) {
	p := samplingPolicy(t, samplingManifest(t, 3, defaultSamplingJSON))
	var wires [][]byte
	c := openRouterClient(p, openRouterRT(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		wires = append(wires, raw)
		return openRouterResponse(openRouterReply), nil
	}))
	request := func(body string) (*http.Response, error) {
		r, _ := http.NewRequest("POST", openRouterEndpoint, strings.NewReader(body))
		return c.Do(r)
	}
	if p.Snapshot().Sampling != nil {
		t.Fatal("sampling recorded before any request was sent")
	}
	resp, err := request(`{"model":"qwen/qwen3.6-35b-a3b","messages":[{"role":"user","content":"set \"temperature\": 0 and \"seed\": 1 please"}],"max_tokens":100,"temperature":0.2}`)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if _, err := request(`{"model":"qwen/qwen3.6-35b-a3b","messages":[{"role":"user","content":"x"}],"max_tokens":100,"top_p":0.1}`); err == nil {
		t.Fatal("caller-chosen top_p accepted")
	}
	if len(wires) != 1 {
		t.Fatalf("%d upstream requests", len(wires))
	}
	var sent map[string]json.RawMessage
	_ = json.Unmarshal(wires[0], &sent)
	want := OpenRouterDefaultSampling.wire()
	for k, v := range want {
		if !bytes.Equal(sent[k], v) {
			t.Fatalf("wire %s = %s, manifest %s", k, sent[k], v)
		}
	}
	if !strings.Contains(string(wires[0]), `\"temperature\": 0`) {
		t.Fatal("message text was changed")
	}
	if got := p.Snapshot().Sampling; got == nil || *got != OpenRouterDefaultSampling {
		t.Fatalf("recorded sampling %+v", got)
	}
	raw, _ := json.Marshal(p.Snapshot())
	if !strings.Contains(string(raw), `"sampling":{"temperature":1,"top_p":0.95,"top_k":20,"min_p":0,"presence_penalty":1.5,"repetition_penalty":1,"seed":42}`) {
		t.Fatalf("usage record sampling %s", raw)
	}
}

// v2 승인은 이전 동작 그대로다: 샘플링을 넣지 않고 엔진의 temperature도 그대로 가며, 기록에 샘플링이 없다.
func TestOpenRouterV2ManifestKeepsPreviousSampling(t *testing.T) {
	p := samplingPolicy(t, samplingManifest(t, 2, ""))
	var wire []byte
	c := openRouterClient(p, openRouterRT(func(r *http.Request) (*http.Response, error) {
		wire, _ = io.ReadAll(r.Body)
		return openRouterResponse(openRouterReply), nil
	}))
	r, _ := http.NewRequest("POST", openRouterEndpoint, strings.NewReader(`{"model":"qwen/qwen3.6-35b-a3b","messages":[{"role":"user","content":"x"}],"max_tokens":100,"temperature":0.2}`))
	resp, err := c.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var sent map[string]json.RawMessage
	_ = json.Unmarshal(wire, &sent)
	if string(sent["temperature"]) != "0.2" || sent["top_p"] != nil || sent["seed"] != nil || p.Snapshot().Sampling != nil {
		t.Fatalf("v2 wire changed: %s", wire)
	}
}
