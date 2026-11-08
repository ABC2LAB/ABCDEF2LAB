package llmpolicy

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

const openRouterEndpoint = OpenRouterBaseURL + "/chat/completions"
const openRouterResponseLimit = 4 << 20

type openRouterTransport struct {
	policy *OpenRouterPolicy
	base   http.RoundTripper
}

// NewOpenRouterClient는 프록시, 리다이렉트 및 임의 TLS 설정을 제공하지 않는다.
func NewOpenRouterClient(p *OpenRouterPolicy) (*http.Client, error) {
	if p == nil || p.consume == nil {
		return nil, ErrOpenRouterPolicy
	}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	base := &http.Transport{Proxy: nil, DisableKeepAlives: true, Protocols: protocols,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: time.Duration(p.manifest.TimeoutSeconds) * time.Second,
		DialContext: openRouterDial,
	}
	return openRouterClient(p, base), nil
}
func openRouterClient(p *OpenRouterPolicy, base http.RoundTripper) *http.Client {
	return &http.Client{Transport: &openRouterTransport{policy: p, base: base},
		Timeout:       time.Duration(p.manifest.TimeoutSeconds) * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return ErrOpenRouterPolicy }}
}

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/3"), netip.MustParsePrefix("::/96"), netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("100::/64"), netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("3fff::/20"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("fc00::/7"), netip.MustParsePrefix("fe80::/10"),
}

func openRouterPublic(ip netip.Addr) bool {
	if !ip.IsValid() || ip.Zone() != "" || ip.Is4In6() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, p := range nonPublicPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}
func openRouterDial(ctx context.Context, network, address string) (net.Conn, error) {
	return openRouterDialWith(ctx, network, address, func(ctx context.Context) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", "openrouter.ai")
	}, (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: -1}).DialContext)
}
func openRouterDialWith(ctx context.Context, network, address string, lookup func(context.Context) ([]netip.Addr, error), dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	if network != "tcp" || address != "openrouter.ai:443" {
		return nil, ErrOpenRouterPolicy
	}
	ips, err := lookup(ctx)
	if err != nil || len(ips) == 0 || len(ips) > 32 {
		return nil, ErrOpenRouterPolicy
	}
	for _, ip := range ips {
		if !openRouterPublic(ip) {
			return nil, ErrOpenRouterPolicy
		}
	}
	selected := ips[0]
	c, err := dial(ctx, "tcp", net.JoinHostPort(selected.String(), "443"))
	if err != nil {
		return nil, ErrOpenRouterPolicy
	}
	peer, ok := c.RemoteAddr().(*net.TCPAddr)
	if !ok || peer.Port != 443 || peer.AddrPort().Addr().Unmap() != selected.Unmap() {
		_ = c.Close()
		return nil, ErrOpenRouterPolicy
	}
	return c, nil
}
func (t *openRouterTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	reject := func() (*http.Response, error) {
		if r != nil && r.Body != nil {
			_ = r.Body.Close()
		}
		return nil, ErrOpenRouterPolicy
	}
	if r == nil || r.URL == nil || r.URL.String() != openRouterEndpoint || r.Method != "POST" ||
		(r.Host != "" && r.Host != "openrouter.ai") || r.Body == nil || len(r.TransferEncoding) != 0 {
		return reject()
	}
	for k := range r.Header {
		if strings.EqualFold(k, "Host") || strings.EqualFold(k, "Proxy-Authorization") {
			return reject()
		}
	}
	p := t.policy
	m := p.manifest
	raw, err := io.ReadAll(io.LimitReader(r.Body, m.MaxInputPerCall-4096+1))
	_ = r.Body.Close()
	if err != nil || len(raw) > int(m.MaxInputPerCall-4096) {
		return reject()
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil {
		return reject()
	}
	var model string
	var stream bool
	var output int64
	if json.Unmarshal(body["model"], &model) != nil || model != OpenRouterModel {
		return reject()
	}
	if v, ok := body["stream"]; ok && (json.Unmarshal(v, &stream) != nil || stream) {
		return reject()
	}
	if json.Unmarshal(body["max_tokens"], &output) != nil || output < 1 || output > m.MaxOutputPerCall {
		return reject()
	}
	// 이 경로에서는 비텍스트 입력, 유료 플러그인, 라우팅/출력 한도 재정의를 허용하지 않는다.
	if _, exists := body["tool_choice"]; exists {
		return reject()
	}
	allowed := map[string]bool{"model": true, "messages": true, "tools": true, "tool_choice": true, "max_tokens": true, "stream": true, "temperature": true, "stop": true}
	for key := range body {
		if !allowed[key] {
			return reject()
		}
	}
	var messages []struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(body["messages"], &messages) != nil || len(messages) == 0 {
		return reject()
	}
	for _, msg := range messages {
		if len(msg.Content) > 0 && string(msg.Content) != "null" {
			var s string
			if json.Unmarshal(msg.Content, &s) != nil {
				return reject()
			}
		}
	}
	provider := map[string]any{"only": []string{m.ProviderSlug}, "allow_fallbacks": false, "require_parameters": true, "data_collection": "deny", "zdr": true,
		"max_price": map[string]float64{"prompt": float64(m.InputPrice) / 1000000, "completion": float64(m.OutputPrice) / 1000000}}
	body["provider"], _ = json.Marshal(provider)
	wire, err := json.Marshal(body)
	if err != nil {
		return reject()
	}
	if err = p.reserve(output); err != nil {
		return reject()
	}
	start := time.Now()
	valid := false
	failureReason := "transport_failure"
	defer func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.active--
		p.usage.LatencyMillis += time.Since(start).Milliseconds()
		if !valid {
			p.blocked = true
			p.usage.Errors++
			p.usage.FailureReason = failureReason
		}
	}()
	copy := r.Clone(r.Context())
	copy.Body = io.NopCloser(bytes.NewReader(wire))
	copy.GetBody = nil
	copy.ContentLength = int64(len(wire))
	// SDK가 제공한 인증 외의 선택적 헤더를 전송하지 않는다.
	copy.Header = make(http.Header)
	copy.Header.Set("Content-Type", "application/json")
	copy.Header.Set("Authorization", r.Header.Get("Authorization"))
	resp, err := t.base.RoundTrip(copy)
	if err != nil || resp == nil {
		return nil, ErrOpenRouterPolicy
	}
	if resp.Body == nil {
		return nil, ErrOpenRouterPolicy
	}
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, openRouterResponseLimit+1))
	_ = resp.Body.Close()
	if readErr != nil || len(data) > openRouterResponseLimit || resp.StatusCode != 200 {
		return nil, ErrOpenRouterPolicy
	}
	failureReason = "accounting_unknown"
	var result struct {
		Model    string `json:"model"`
		Provider string `json:"provider"`
		Usage    *struct {
			Input  int64    `json:"prompt_tokens"`
			Output int64    `json:"completion_tokens"`
			Cost   *float64 `json:"cost"`
		} `json:"usage"`
	}
	if !validOpenRouterJSON(data) || !validOpenRouterWireTools(data, body["tools"]) || json.Unmarshal(data, &result) != nil || result.Model != OpenRouterModel || result.Provider != m.ExpectedProviderName ||
		result.Usage == nil || result.Usage.Cost == nil || result.Usage.Input < 1 || result.Usage.Input > m.MaxInputPerCall ||
		result.Usage.Output < 0 || result.Usage.Output > output || math.IsNaN(*result.Usage.Cost) || math.IsInf(*result.Usage.Cost, 0) || *result.Usage.Cost < 0 {
		return nil, ErrOpenRouterPolicy
	}
	cost := math.Ceil(*result.Usage.Cost * 1000000)
	reserved := (m.MaxInputPerCall*m.InputPrice+999999)/1000000 + (output*m.OutputPrice+999999)/1000000
	if cost > float64(reserved) {
		return nil, ErrOpenRouterPolicy
	}
	p.mu.Lock()
	p.usage.ReturnedProvider = result.Provider
	p.usage.InputTokens += result.Usage.Input
	p.usage.OutputTokens += result.Usage.Output
	p.usage.CostMicros += int64(cost)
	p.mu.Unlock()
	valid = true
	resp.Body = io.NopCloser(bytes.NewReader(data))
	resp.ContentLength = int64(len(data))
	return resp, nil
}

// SDK의 빈 인수 자동 보정을 거치기 전에 도구 호출의 원문 계약을 검증한다.
func validOpenRouterWireTools(data, rawTools []byte) bool {
	var defs []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if len(rawTools) > 0 && json.Unmarshal(rawTools, &defs) != nil {
		return false
	}
	names := map[string]bool{}
	for _, d := range defs {
		names[d.Function.Name] = true
	}
	var response struct {
		Error   json.RawMessage `json:"error"`
		Choices []struct {
			Error   json.RawMessage `json:"error"`
			Finish  string          `json:"finish_reason"`
			Message struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(data, &response) != nil || len(response.Choices) != 1 || (len(response.Error) > 0 && string(response.Error) != "null") {
		return false
	}
	choice := response.Choices[0]
	if (len(choice.Error) > 0 && string(choice.Error) != "null") || (choice.Finish != "stop" && choice.Finish != "tool_calls") {
		return false
	}
	ids := map[string]bool{}
	for _, tc := range choice.Message.ToolCalls {
		if tc.ID == "" || ids[tc.ID] || tc.Type != "function" || !names[tc.Function.Name] || !ValidOpenRouterToolArguments([]byte(tc.Function.Arguments)) {
			return false
		}
		ids[tc.ID] = true
	}
	return (choice.Finish == "tool_calls") == (len(choice.Message.ToolCalls) > 0)
}

// JSON 키 중복은 해석기별 의미 차이를 만들기 때문에 깊이 제한과 함께 거부한다.
func validOpenRouterJSON(data []byte) bool {
	if !validManifestEncoding(data) {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 64 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return true
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				name, ok := key.(string)
				if err != nil || !ok || seen[name] {
					return false
				}
				seen[name] = true
				if !value(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim('}')
		case '[':
			for d.More() {
				if !value(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim(']')
		default:
			return false
		}
	}
	if !value(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

// ValidOpenRouterToolArguments는 객체 형식과 중복 키 금지를 함께 확인한다.
// 도구별 스키마와 실행 권한 검사는 기존 도구 실행 경계의 책임이다.
func ValidOpenRouterToolArguments(raw []byte) bool {
	var object map[string]json.RawMessage
	return validOpenRouterJSON(raw) && json.Unmarshal(raw, &object) == nil && object != nil
}
