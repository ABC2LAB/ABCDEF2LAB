package llmpolicy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// 제품 설치(v4) 점검의 단가 상한 재조회(2026-11-23 사용자 결정): 첫 호출 전에 재조회 단가 × 1.5, 실패면 설치 상한.
// 평가 manifest(v1~v3)는 바꾸지 않는다.

var requoteNow = time.Date(2026, 11, 23, 10, 22, 0, 0, time.UTC)

// endpointsJSON은 OpenRouter 공개 목록 모양이다. prices는 tag → [입력, 출력] 토큰당 USD 문자열이다.
func endpointsJSON(prices map[string][2]string) []byte {
	type row struct {
		Tag     string            `json:"tag"`
		Pricing map[string]string `json:"pricing"`
	}
	var rows []row
	for tag, p := range prices {
		rows = append(rows, row{Tag: tag, Pricing: map[string]string{"prompt": p[0], "completion": p[1]}})
	}
	raw, _ := json.Marshal(map[string]any{"data": map[string]any{"endpoints": rows}})
	return raw
}

func fixedEndpoints(data []byte, err error) (OpenRouterEndpointsFetcher, *int) {
	calls := 0
	return func(context.Context, string) ([]byte, error) {
		calls++
		return data, err
	}, &calls
}

func wireMaxPrice(t *testing.T, p *OpenRouterPolicy) string {
	t.Helper()
	body, errs := sentBodies(t, p, openRouterReply)
	if errs[0] != nil {
		t.Fatal(errs[0])
	}
	var provider map[string]json.RawMessage
	if json.Unmarshal(body["provider"], &provider) != nil {
		t.Fatal("provider 칸")
	}
	return string(provider["max_price"])
}

// 11/23 실측 값: PrimeIntellect 입력 $0.17 · 출력 $1.52, DeepInfra $0.10 · $0.95 → 상한 $0.255 · $2.28.
func TestOpenRouterRunPriceRequote(t *testing.T) {
	p := samplingPolicy(t, routingManifest(t, 4, fallbackRoutingJSON))
	fetch, calls := fixedEndpoints(endpointsJSON(map[string][2]string{
		"fixture/endpoint": {"0.00000017", "0.00000152"}, "second/fp8": {"0.0000001", "0.00000095"}, "other/fp4": {"0.1", "0.1"}}), nil)
	if err := p.ApplyRunPriceRequote(t.Context(), fetch, requoteNow); err != nil || *calls != 1 {
		t.Fatalf("재조회: %v %d", err, *calls)
	}
	caps := p.Snapshot().PriceCaps
	if caps == nil || caps.Source != OpenRouterPriceRequoted || caps.InputMicros != 255000 || caps.OutputMicros != 2280000 ||
		caps.InstallInputMicros != 1000000 || caps.InstallOutputMicros != 1000000 || caps.FailureReason != "" || !caps.QueriedAt.Equal(requoteNow) {
		t.Fatalf("상한 기록: %+v", caps)
	}
	if len(caps.Providers) != 2 || caps.Providers[0].Slug != "fixture/endpoint" || *caps.Providers[0].OutputMicros != 1520000 ||
		caps.Providers[0].Status != OpenRouterProviderWithinCap || caps.Providers[1].Status != OpenRouterProviderWithinCap {
		t.Fatalf("제공자 기록: %+v", caps.Providers)
	}
	if got := wireMaxPrice(t, p); got != `{"completion":2.28,"prompt":0.255}` {
		t.Fatalf("요청 max_price: %s", got)
	}
	if p.ApplyRunPriceRequote(t.Context(), fetch, requoteNow) == nil {
		t.Fatal("두 번째 재조회를 받음")
	}
	usage, _ := json.Marshal(p.Snapshot())
	if !strings.Contains(string(usage), `"price_caps":{"source":"requoted"`) {
		t.Fatalf("회차 기록에 상한이 없음: %s", usage)
	}
}

// 다시 읽지 못하면 설치 상한으로 진행하고 그 사유와 '알 수 없음' 상태를 남긴다.
func TestOpenRouterRunPriceRequoteFallback(t *testing.T) {
	cases := map[string]struct {
		data   []byte
		err    error
		reason string
		status string
	}{
		"조회 실패":     {nil, errors.New("offline"), openRouterRequoteUnavailable, OpenRouterProviderUnknown},
		"JSON 아님":   {[]byte("<html>"), nil, openRouterRequoteInvalid, OpenRouterProviderUnknown},
		"목록 제공자 없음": {endpointsJSON(map[string][2]string{"other/fp8": {"0.0000001", "0.000001"}}), nil, openRouterRequoteNoProvider, OpenRouterProviderNotOffered},
		"단가 읽히지 않음": {endpointsJSON(map[string][2]string{"fixture/endpoint": {"free", "-1"}}), nil, openRouterRequoteNoProvider, OpenRouterProviderNotOffered},
	}
	for name, tc := range cases {
		p := samplingPolicy(t, routingManifest(t, 4, fallbackRoutingJSON))
		fetch, _ := fixedEndpoints(tc.data, tc.err)
		if err := p.ApplyRunPriceRequote(t.Context(), fetch, requoteNow); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		caps := p.Snapshot().PriceCaps
		if caps == nil || caps.Source != OpenRouterPriceInstallFallback || caps.FailureReason != tc.reason || caps.InputMicros != 1000000 || caps.OutputMicros != 1000000 ||
			len(caps.Providers) != 2 || caps.Providers[0].Status != tc.status {
			t.Fatalf("%s: %+v", name, caps)
		}
		if got := wireMaxPrice(t, p); got != `{"completion":1,"prompt":1}` {
			t.Fatalf("%s: 설치 상한이 아님 %s", name, got)
		}
	}
}

// 회귀: 평가 manifest(v1~v3)는 재조회를 받지 않는다. 상한·요청 본문·회차 기록이 그대로다(공식 세션의 상한 규칙은
// 러너가 manifest를 만들 때 정하고 회차 안에서 바뀌지 않는다).
func TestOpenRouterRunPriceRequoteOnlyV4(t *testing.T) {
	fetch, calls := fixedEndpoints(endpointsJSON(map[string][2]string{"fixture/endpoint": {"0.000001", "0.00001"}}), nil)
	for _, version := range []int{1, 2, 3} {
		p := samplingPolicy(t, routingManifest(t, version, ""))
		if err := p.ApplyRunPriceRequote(t.Context(), fetch, requoteNow); !errors.Is(err, ErrOpenRouterPolicy) {
			t.Fatalf("v%d 재조회를 받음: %v", version, err)
		}
		if got := wireMaxPrice(t, p); got != `{"completion":1,"prompt":1}` {
			t.Fatalf("v%d 상한이 바뀜: %s", version, got)
		}
		usage, _ := json.Marshal(p.Snapshot())
		if strings.Contains(string(usage), "price_caps") {
			t.Fatalf("v%d 회차 기록에 새 칸: %s", version, usage)
		}
	}
	if *calls != 0 {
		t.Fatalf("v1~v3에서 목록을 읽음: %d", *calls)
	}
	// 이미 호출한 v4 점검도 바꾸지 않는다.
	p := samplingPolicy(t, routingManifest(t, 4, fallbackRoutingJSON))
	_ = wireMaxPrice(t, p)
	if p.ApplyRunPriceRequote(t.Context(), fetch, requoteNow) == nil {
		t.Fatal("호출 뒤 상한을 바꿈")
	}
}

// 점검 중 404(받을 제공자 없음)가 나면 한 번 더 읽어, 지금 상한을 넘어 빠진 제공자를 남긴다.
func TestOpenRouterNotFoundExplainsPriceExclusion(t *testing.T) {
	p := samplingPolicy(t, routingManifest(t, 4, fallbackRoutingJSON))
	prices := map[string][2]string{"fixture/endpoint": {"0.00000017", "0.00000152"}, "second/fp8": {"0.0000001", "0.00000095"}}
	calls := 0
	fetch := func(context.Context, string) ([]byte, error) {
		calls++
		if calls > 1 {
			// 시작 뒤 Fixture가 출력 단가를 $3.00으로 올렸다.
			prices["fixture/endpoint"] = [2]string{"0.00000017", "0.000003"}
		}
		return endpointsJSON(prices), nil
	}
	if err := p.ApplyRunPriceRequote(t.Context(), fetch, requoteNow); err != nil {
		t.Fatal(err)
	}
	c := openRouterClient(p, openRouterRT(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(`{"error":{"code":404,"message":"No endpoints found"}}`)), Header: make(http.Header)}, nil
	}))
	for range 2 {
		if resp, err := c.Do(openRouterRequest()); err == nil {
			_ = resp.Body.Close()
			t.Fatal("404를 받음")
		}
	}
	caps := p.Snapshot().PriceCaps
	if calls != 2 || caps == nil || caps.NotFoundChecked == nil || caps.Providers[0].Status != OpenRouterProviderOverCap ||
		*caps.Providers[0].OutputMicros != 3000000 || caps.Providers[1].Status != OpenRouterProviderWithinCap || caps.OutputMicros != 2280000 {
		t.Fatalf("404 설명: 조회 %d %+v", calls, caps)
	}
}

// 단가 문자열은 정확히 바꾸고 올린다.
func TestOpenRouterMicrosPerMillion(t *testing.T) {
	for in, want := range map[string]int64{"0.00000152": 1520000, "0.0000001": 100000, "0": 0, "0.0000000000001": 1, "1e-7": 100000} {
		if got, ok := openRouterMicrosPerMillion(in); !ok || got != want {
			t.Fatalf("%s → %d %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "free", "-0.1", "1000"} {
		if _, ok := openRouterMicrosPerMillion(bad); ok {
			t.Fatalf("%q를 받음", bad)
		}
	}
}
