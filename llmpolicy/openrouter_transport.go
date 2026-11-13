package llmpolicy

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Autumn-27/artex/internal/diagnosticlog"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

const openRouterEndpoint = OpenRouterBaseURL + "/chat/completions"

// HTTP JSON의 바이트 상한은 모델의 토큰 예약 상한과 별개다.
const openRouterRequestLimit = 4 << 20
const openRouterResponseLimit = 4 << 20

type openRouterTransport struct {
	policy *OpenRouterPolicy
	base   http.RoundTripper
}

// NewOpenRouterClient는 프록시, 리다이렉트 및 임의 TLS 설정을 제공하지 않는다.
func NewOpenRouterClient(p *OpenRouterPolicy) (*http.Client, error) {
	if p == nil || p.ledger == nil {
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

// 다이얼 실패는 둘로 나눈다(P3 결정안 C06). 주소 정책 위반(목적지·비공인 주소·peer 불일치)은 정책을 막고,
// 이름 풀이·연결 실패는 그 호출만 실패하는 일시 오류다. 둘 다 ErrOpenRouterPolicy로 보인다.
var (
	errOpenRouterAddressPolicy = fmt.Errorf("%w: address", ErrOpenRouterPolicy)
	errOpenRouterConnect       = fmt.Errorf("%w: connect", ErrOpenRouterPolicy)
)

func openRouterDialWith(ctx context.Context, network, address string, lookup func(context.Context) ([]netip.Addr, error), dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	if network != "tcp" || address != "openrouter.ai:443" {
		return nil, errOpenRouterAddressPolicy
	}
	ips, err := lookup(ctx)
	if err != nil || len(ips) == 0 {
		return nil, errOpenRouterConnect
	}
	if len(ips) > 32 {
		return nil, errOpenRouterAddressPolicy
	}
	for _, ip := range ips {
		if !openRouterPublic(ip) {
			return nil, errOpenRouterAddressPolicy
		}
	}
	selected := ips[0]
	c, err := dial(ctx, "tcp", net.JoinHostPort(selected.String(), "443"))
	if err != nil {
		return nil, errOpenRouterConnect
	}
	peer, ok := c.RemoteAddr().(*net.TCPAddr)
	if !ok || peer.Port != 443 || peer.AddrPort().Addr().Unmap() != selected.Unmap() {
		_ = c.Close()
		return nil, errOpenRouterAddressPolicy
	}
	return c, nil
}

// openRouterTransientError는 전송 오류가 그 호출만 실패하는 일시 오류인지 본다. 주소 정책 위반과 TLS 인증서 검증
// 실패(중간자 가능성)는 정책을 막는다.
func openRouterTransientError(err error) bool {
	var certificate *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	if err == nil || errors.Is(err, errOpenRouterAddressPolicy) || errors.As(err, &certificate) || errors.As(err, &unknownAuthority) || errors.As(err, &hostname) || errors.As(err, &invalid) {
		return false
	}
	return true
}

// openRouterTransientStatus는 429와 5xx다. 그 밖의 비정상 상태는 설정 오류로 보고 막는다.
func openRouterTransientStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500 && status <= 599
}
func (t *openRouterTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	// 진단 관측은 송신 권한이나 기존 사용량 집계를 대신하지 않는다.
	diagnostic := diagnosticlog.Event{Component: "openrouter", Event: "model_request_denied", Stage: "model_call", Status: "denied", ReasonCode: "policy_denied", Model: OpenRouterModel}
	started := time.Now()
	var diagnosticContext context.Context
	if r != nil {
		diagnosticContext = r.Context()
		diagnostic.Role = diagnosticlog.RoleFrom(diagnosticContext)
	}
	if t != nil && t.policy != nil {
		diagnostic.ProviderRef = t.policy.manifest.ProviderSlug
	}
	defer func() {
		diagnostic.DurationMS = time.Since(started).Milliseconds()
		_ = diagnosticlog.Emit(diagnosticContext, diagnostic)
	}()
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
	raw, err := io.ReadAll(io.LimitReader(r.Body, openRouterRequestLimit+1))
	_ = r.Body.Close()
	if err != nil || len(raw) > openRouterRequestLimit {
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
	if err != nil || len(wire) > openRouterRequestLimit {
		return reject()
	}
	// 호출 칸은 요청 하나에만 쓴다. 같은 칸으로 온 두 번째 요청은 예약 전에 거부한다(C28 결정 2).
	slot := openRouterCallFrom(r.Context())
	if !slot.claim() {
		diagnostic.ReasonCode = "call_slot_reused"
		return reject()
	}
	// 입력 예약은 입력 상한 전체가 아니라 이 요청 크기의 상한이다(P3 결정안 C28 결정 1).
	input := openRouterReservedInput(m, len(wire))
	reserved := openRouterCallCost(m, input, output)
	if reason, reserveErr := p.reserveDiagnostic(input, output); reserveErr != nil {
		diagnostic.ReasonCode = reason
		return reject()
	}
	slot.reserved(input, output, reserved)
	diagnostic.Event = "model_request_finished"
	diagnostic.Status = "failed"
	diagnostic.ReasonCode = "transport_failure"
	diagnostic.ReservedInputTokens = input
	diagnostic.ReservedOutputTokens = output
	diagnostic.ReservedCostMicros = reserved
	diagnostic.CostUncertain = true
	begin := diagnostic
	begin.Event, begin.Status, begin.ReasonCode = "model_request_started", "started", ""
	_ = diagnosticlog.Emit(diagnosticContext, begin)
	start := time.Now()
	valid := false
	transient := false
	failureReason := "transport_failure"
	defer func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.active--
		p.usage.LatencyMillis += time.Since(start).Milliseconds()
		if valid {
			p.transient = 0
			return
		}
		p.usage.Errors++
		p.usage.FailureReason = failureReason
		// 일시 오류는 그 호출만 실패로 두고, 연속 상한에 닿으면 막는다(C06). 예약은 되돌리지 않는다.
		if transient {
			p.transient++
			if p.transient < m.transientLimit() {
				return
			}
		}
		p.blocked = true
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
		diagnostic.ReasonCode = openRouterDiagnosticTransportReason(err, r.Context())
		transient = openRouterTransientError(err)
		return nil, ErrOpenRouterPolicy
	}
	if resp.StatusCode >= 100 && resp.StatusCode <= 599 {
		diagnostic.HTTPStatus = resp.StatusCode
	}
	if resp.Body == nil {
		diagnostic.ReasonCode = "response_invalid"
		return nil, ErrOpenRouterPolicy
	}
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, openRouterResponseLimit+1))
	_ = resp.Body.Close()
	// 상태를 먼저 본다. 크기 초과와 영구 상태(4xx·1xx·3xx)는 본문이 끊겨도 설정 오류로 막는다. 정상 200의 본문이
	// 끊긴 경우와 429·5xx만 일시 오류다(P3 결정안 C06).
	switch {
	case len(data) > openRouterResponseLimit:
		transient = false
	case resp.StatusCode == http.StatusOK:
		transient = readErr != nil && openRouterTransientError(readErr)
	case openRouterTransientStatus(resp.StatusCode):
		transient = readErr == nil || openRouterTransientError(readErr)
	default:
		transient = false
	}
	if readErr != nil || len(data) > openRouterResponseLimit || resp.StatusCode != 200 {
		switch {
		case resp.StatusCode == 401:
			diagnostic.ReasonCode = "http_unauthorized"
		case resp.StatusCode == 429:
			diagnostic.ReasonCode = "http_rate_limited"
		case resp.StatusCode >= 500 && resp.StatusCode <= 599:
			diagnostic.ReasonCode = "http_server_error"
		case resp.StatusCode >= 400 && resp.StatusCode <= 499:
			diagnostic.ReasonCode = "http_client_error"
		case readErr != nil:
			diagnostic.ReasonCode = openRouterDiagnosticTransportReason(readErr, r.Context())
		default:
			diagnostic.ReasonCode = "response_invalid"
		}
		return nil, ErrOpenRouterPolicy
	}
	failureReason = "accounting_unknown"
	diagnostic.ReasonCode = "accounting_unknown"
	if !validOpenRouterJSON(data) {
		diagnostic.ReasonCode = "response_invalid"
		return nil, ErrOpenRouterPolicy
	}
	var result struct {
		Model    string `json:"model"`
		Provider string `json:"provider"`
		Usage    *struct {
			Input  int64    `json:"prompt_tokens"`
			Output int64    `json:"completion_tokens"`
			Cost   *float64 `json:"cost"`
		} `json:"usage"`
	}
	if json.Unmarshal(data, &result) != nil || result.Model != OpenRouterModel || result.Provider != m.ExpectedProviderName ||
		result.Usage == nil || result.Usage.Cost == nil || result.Usage.Input < 1 || result.Usage.Input > input ||
		result.Usage.Output < 0 || result.Usage.Output > output || math.IsNaN(*result.Usage.Cost) || math.IsInf(*result.Usage.Cost, 0) || *result.Usage.Cost < 0 {
		return nil, ErrOpenRouterPolicy
	}
	cost := math.Ceil(*result.Usage.Cost * 1000000)
	if cost > float64(reserved) {
		return nil, ErrOpenRouterPolicy
	}
	p.mu.Lock()
	p.usage.ReturnedProvider = result.Provider
	p.usage.InputTokens += result.Usage.Input
	p.usage.OutputTokens += result.Usage.Output
	p.usage.CostMicros += int64(cost)
	p.mu.Unlock()
	slot.succeeded(result.Usage.Input, result.Usage.Output, int64(cost))
	diagnostic.InputTokens, diagnostic.OutputTokens = result.Usage.Input, result.Usage.Output
	diagnostic.CostMicros, diagnostic.CostUncertain = int64(cost), false
	if !validOpenRouterWireTools(data, body["tools"]) {
		// 모델 출력이 도구 호출 계약(finish_reason·등록 도구 이름·인수 객체)에 어긋난 응답이다. 제공자가 청구한 사용량은
		// 위에서 확인·정산했으므로 회계는 깨지지 않았다. 이 호출만 실패로 두고, 연속되면 일시 오류 상한으로 막는다
		// (P3 결정안 C06 보완). JSON 깨짐·모델/제공자 불일치·사용량 없음·예약 초과는 위에서 그대로 막는다.
		failureReason = "response_invalid"
		diagnostic.ReasonCode = "response_invalid"
		transient = true
		return nil, ErrOpenRouterPolicy
	}
	valid = true
	diagnostic.Status, diagnostic.ReasonCode = "succeeded", ""
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

// 오류 문자열은 버리고 문맥과 표준 오류 타입만 분류한다.
func openRouterDiagnosticTransportReason(err error, ctx context.Context) string {
	if errors.Is(err, context.DeadlineExceeded) || ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) || ctx != nil && errors.Is(ctx.Err(), context.Canceled) {
		return "cancelled"
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return "timeout"
	}
	return "transport_failure"
}
