package llmpolicy

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

const (
	fallbackRoutingJSON = `{"mode":"fallback","providers":[{"slug":"fixture/endpoint","name":"Fixture"},{"slug":"second/fp8","name":"Second"}]}`
	pinnedRoutingJSON   = `{"mode":"pinned"}`
	// pinnedWireProvider는 고정 모드 요청 본문의 provider 칸 바이트다(제공자 모드 설정이 들어오기 전과 같아야 한다).
	pinnedWireProvider = `{"allow_fallbacks":false,"data_collection":"deny","max_price":{"completion":1,"prompt":1},"only":["fixture/endpoint"],"require_parameters":true,"zdr":true}`
)

// routingManifest는 v4 manifest 원문이다. provider_routing 원문은 그대로 끼워 넣는다.
func routingManifest(t *testing.T, version int, routing string) []byte {
	t.Helper()
	sampling := ""
	if version >= 3 {
		sampling = defaultSamplingJSON
	}
	raw := samplingManifest(t, version, sampling)
	if routing == "" {
		return raw
	}
	return append(raw[:len(raw)-1], []byte(`,"provider_routing":`+routing+`}`)...)
}

// sentBodies는 정책이 실제로 내보낸 요청 본문들을 모은다. 응답은 reply가 순서대로 정한다.
func sentBodies(t *testing.T, p *OpenRouterPolicy, replies ...string) (map[string]json.RawMessage, []error) {
	t.Helper()
	var last map[string]json.RawMessage
	i := 0
	c := openRouterClient(p, openRouterRT(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		last = map[string]json.RawMessage{}
		if json.Unmarshal(raw, &last) != nil {
			t.Fatal("보낸 본문이 JSON이 아님")
		}
		reply := replies[i]
		i++
		return openRouterResponse(reply), nil
	}))
	var errs []error
	for range replies {
		resp, err := c.Do(openRouterRequest())
		if err == nil {
			_ = resp.Body.Close()
		}
		errs = append(errs, err)
	}
	return last, errs
}

// 고정 모드 회귀: v1·v2·v3(칸 없음)과 v4 pinned가 보내는 provider 칸이 설정이 들어오기 전 바이트와 같고, v3과 v4 pinned는
// 요청 본문 전체가 같다. 사용량 기록에도 새 칸이 생기지 않는다.
func TestOpenRouterPinnedModeUnchanged(t *testing.T) {
	bodies := map[int]map[string]json.RawMessage{}
	for _, version := range []int{1, 2, 3, 4} {
		routing := ""
		if version == 4 {
			routing = pinnedRoutingJSON
		}
		p := samplingPolicy(t, routingManifest(t, version, routing))
		body, errs := sentBodies(t, p, openRouterReply)
		if errs[0] != nil || string(body["provider"]) != pinnedWireProvider {
			t.Fatalf("v%d provider 칸: %s %v", version, body["provider"], errs[0])
		}
		bodies[version] = body
		usage, _ := json.Marshal(p.Snapshot())
		if strings.Contains(string(usage), "returned_providers") || p.Snapshot().ReturnedProvider != "Fixture" {
			t.Fatalf("v%d 사용량 기록이 바뀜: %s", version, usage)
		}
	}
	v3, _ := json.Marshal(bodies[3])
	v4, _ := json.Marshal(bodies[4])
	if string(v3) != string(v4) {
		t.Fatalf("v4 pinned 본문이 v3과 다름:\n%s\n%s", v3, v4)
	}
}

// 고정 모드는 승인 endpoint가 아닌 제공자의 응답을 받지 않는다(자동 넘김 목록에 있을 이름이어도).
func TestOpenRouterPinnedModeRejectsOtherProvider(t *testing.T) {
	for _, routing := range []string{"", pinnedRoutingJSON} {
		version := 3
		if routing != "" {
			version = 4
		}
		p := samplingPolicy(t, routingManifest(t, version, routing))
		_, errs := sentBodies(t, p, strings.Replace(openRouterReply, `"provider":"Fixture"`, `"provider":"Second"`, 1))
		if errs[0] == nil || !p.Snapshot().Blocked {
			t.Fatalf("v%d 고정 모드가 다른 제공자 응답을 받음", version)
		}
	}
}

// 자동 넘김: 목록 안에서만 순서대로 넘어가게 보내고(only·order 같은 목록), 개인정보·매개변수·단가 조건은 그대로다.
// 응답을 준 제공자를 호출마다 세고, 목록 밖 제공자의 응답은 받지 않는다.
func TestOpenRouterFallbackMode(t *testing.T) {
	p := samplingPolicy(t, routingManifest(t, 4, fallbackRoutingJSON))
	second := strings.Replace(openRouterReply, `"provider":"Fixture"`, `"provider":"Second"`, 1)
	body, errs := sentBodies(t, p, openRouterReply, second, second)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("호출 %d: %v", i, err)
		}
	}
	const want = `{"allow_fallbacks":true,"data_collection":"deny","max_price":{"completion":1,"prompt":1},"only":["fixture/endpoint","second/fp8"],"order":["fixture/endpoint","second/fp8"],"require_parameters":true,"zdr":true}`
	if string(body["provider"]) != want {
		t.Fatalf("자동 넘김 provider 칸: %s", body["provider"])
	}
	u := p.Snapshot()
	counts := OpenRouterProviderCounts{{Slug: "fixture/endpoint", Responses: 1}, {Slug: "second/fp8", Responses: 2}}
	if u.ReturnedProvider != "Second" || u.ReturnedProviders != counts || u.Requests != 3 || u.Blocked {
		t.Fatalf("제공자 기록: %+v", u)
	}
	raw, _ := json.Marshal(u)
	if !strings.Contains(string(raw), `"returned_providers":[{"provider_slug":"fixture/endpoint","responses":1},{"provider_slug":"second/fp8","responses":2}]`) {
		t.Fatalf("사용량 기록 JSON: %s", raw)
	}
	var back OpenRouterUsage
	if json.Unmarshal(raw, &back) != nil || back.ReturnedProviders != counts {
		t.Fatalf("사용량 기록을 다시 읽지 못함: %+v", back)
	}

	outside := samplingPolicy(t, routingManifest(t, 4, fallbackRoutingJSON))
	_, errs = sentBodies(t, outside, strings.Replace(openRouterReply, `"provider":"Fixture"`, `"provider":"Elsewhere"`, 1))
	if errs[0] == nil || !outside.Snapshot().Blocked || outside.Snapshot().ReturnedProviders != (OpenRouterProviderCounts{}) {
		t.Fatalf("목록 밖 제공자 응답을 받음: %+v", outside.Snapshot())
	}
}

// v4만 provider_routing을 받고 v4는 반드시 있어야 한다. 모양은 엄격하게 본다.
func TestOpenRouterManifestV4ProviderRouting(t *testing.T) {
	for name, routing := range map[string]string{"pinned": pinnedRoutingJSON, "fallback": fallbackRoutingJSON} {
		if _, err := ParseOpenRouterManifest(routingManifest(t, 4, routing)); err != nil {
			t.Fatalf("%s 거부됨: %v", name, err)
		}
	}
	many := `{"mode":"fallback","providers":[{"slug":"fixture/endpoint","name":"Fixture"}`
	for i := 0; i < openRouterMaxFallbackProviders; i++ {
		many += `,{"slug":"p` + string(rune('a'+i)) + `/fp8","name":"P` + string(rune('a'+i)) + `"}`
	}
	many += `]}`
	bad := map[string][]byte{
		"v4_without_routing": routingManifest(t, 4, ""),
		"v3_with_routing":    routingManifest(t, 3, pinnedRoutingJSON),
		"v2_with_routing":    routingManifest(t, 2, pinnedRoutingJSON),
		"v1_with_routing":    routingManifest(t, 1, pinnedRoutingJSON),
		"v4_without_sampling": func() []byte {
			raw := samplingManifest(t, 4, "")
			return append(raw[:len(raw)-1], []byte(`,"provider_routing":`+pinnedRoutingJSON+`}`)...)
		}(),
	}
	for name, routing := range map[string]string{
		"null":                  `null`,
		"string":                `"fallback"`,
		"empty":                 `{}`,
		"no_mode":               `{"providers":[{"slug":"fixture/endpoint","name":"Fixture"},{"slug":"second/fp8","name":"Second"}]}`,
		"unknown_mode":          `{"mode":"auto"}`,
		"mode_case":             `{"mode":"Pinned"}`,
		"mode_not_string":       `{"mode":1}`,
		"duplicate_mode":        `{"mode":"pinned","mode":"fallback"}`,
		"unknown_key":           `{"mode":"pinned","sort":"price"}`,
		"key_case_alias":        `{"Mode":"pinned"}`,
		"pinned_with_list":      `{"mode":"pinned","providers":[{"slug":"fixture/endpoint","name":"Fixture"},{"slug":"second/fp8","name":"Second"}]}`,
		"pinned_empty_list":     `{"mode":"pinned","providers":[]}`,
		"fallback_no_list":      `{"mode":"fallback"}`,
		"fallback_empty_list":   `{"mode":"fallback","providers":[]}`,
		"fallback_single":       `{"mode":"fallback","providers":[{"slug":"fixture/endpoint","name":"Fixture"}]}`,
		"fallback_over_max":     many,
		"list_not_array":        `{"mode":"fallback","providers":{"slug":"fixture/endpoint","name":"Fixture"}}`,
		"first_not_approved":    `{"mode":"fallback","providers":[{"slug":"second/fp8","name":"Second"},{"slug":"fixture/endpoint","name":"Fixture"}]}`,
		"first_name_differs":    `{"mode":"fallback","providers":[{"slug":"fixture/endpoint","name":"Other"},{"slug":"second/fp8","name":"Second"}]}`,
		"duplicate_slug":        `{"mode":"fallback","providers":[{"slug":"fixture/endpoint","name":"Fixture"},{"slug":"fixture/endpoint","name":"Second"}]}`,
		"duplicate_name":        `{"mode":"fallback","providers":[{"slug":"fixture/endpoint","name":"Fixture"},{"slug":"second/fp8","name":"Fixture"}]}`,
		"bad_slug":              `{"mode":"fallback","providers":[{"slug":"fixture/endpoint","name":"Fixture"},{"slug":"second fp8","name":"Second"}]}`,
		"empty_name":            `{"mode":"fallback","providers":[{"slug":"fixture/endpoint","name":"Fixture"},{"slug":"second/fp8","name":""}]}`,
		"entry_missing_name":    `{"mode":"fallback","providers":[{"slug":"fixture/endpoint","name":"Fixture"},{"slug":"second/fp8"}]}`,
		"entry_extra_key":       `{"mode":"fallback","providers":[{"slug":"fixture/endpoint","name":"Fixture"},{"slug":"second/fp8","name":"Second","sort":"price"}]}`,
		"entry_duplicate_key":   `{"mode":"fallback","providers":[{"slug":"fixture/endpoint","name":"Fixture"},{"slug":"second/fp8","slug":"third/fp8","name":"Second"}]}`,
		"entry_slug_not_string": `{"mode":"fallback","providers":[{"slug":"fixture/endpoint","name":"Fixture"},{"slug":7,"name":"Second"}]}`,
		"entry_not_object":      `{"mode":"fallback","providers":[{"slug":"fixture/endpoint","name":"Fixture"},"second/fp8"]}`,
		"trailing":              pinnedRoutingJSON + `{}`,
	} {
		bad[name] = routingManifest(t, 4, routing)
	}
	for name, raw := range bad {
		if _, err := ParseOpenRouterManifest(raw); err == nil {
			t.Errorf("%s 받아들여짐", name)
		}
	}
}

// 실행 구간 기록은 기준 시점(준비 단계 끝) 뒤의 응답만 센다. 0이 된 제공자는 빠지고 순서는 유지된다.
func TestOpenRouterProviderCountsMinus(t *testing.T) {
	var total, base OpenRouterProviderCounts
	for _, slug := range []string{"a/fp8", "b/fp8", "a/fp8", "c/fp8", "b/fp8", "b/fp8"} {
		total.add(slug)
	}
	base.add("a/fp8")
	base.add("a/fp8")
	base.add("b/fp8")
	want := OpenRouterProviderCounts{{Slug: "b/fp8", Responses: 2}, {Slug: "c/fp8", Responses: 1}}
	if got := total.Minus(base); got != want {
		t.Fatalf("%+v", got)
	}
	if got := total.Minus(OpenRouterProviderCounts{}); got != total {
		t.Fatalf("기준이 비면 그대로여야 함: %+v", got)
	}
	if got := (OpenRouterProviderCounts{}).Minus(base); got != (OpenRouterProviderCounts{}) {
		t.Fatalf("고정 모드(빈 값)는 빈 값: %+v", got)
	}
}
