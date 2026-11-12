package mcphttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
)

// NewScoped는 호스트가 세션별로 띄운 탐색 MCP 제어 끝점 전용이다.
// 프록시 환경·리다이렉트·인증 헤더를 물려받아 다른 망으로 세션 제어를 보내지 않는다.
func NewScoped(ctx context.Context, server, address string) (*Client, error) {
	u, err := url.Parse(address)
	if ctx == nil || ctx.Err() != nil || err != nil || u.Scheme != "http" || u.Hostname() == "" || u.Port() == "" || (u.Path != "" && u.Path != "/" && u.Path != "/mcp") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("mcp scoped endpoint invalid")
	}
	transport := &http.Transport{Proxy: nil}
	c := &Client{server: server, url: address, headers: map[string]string{}, http: &http.Client{Transport: &scopedMCPTransport{transport: transport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, protocol: protocolVersion}
	if err := c.initialize(ctx); err != nil {
		transport.CloseIdleConnections()
		return nil, err
	}
	return c, nil
}

// CloseContext는 취소된 실행과 독립인 짧은 정리 문맥으로 HTTP 세션을 지우고 연결을 닫는다.
func (c *Client) CloseContext(ctx context.Context) error {
	if c == nil || ctx == nil {
		return errors.New("mcp close context invalid")
	}
	defer c.http.CloseIdleConnections()
	if c.legacySSE {
		return c.Close()
	}
	c.mu.Lock()
	sid := c.sessionID
	c.sessionID = ""
	c.mu.Unlock()
	if sid == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Mcp-Session-Id", sid)
	req.Header.Set("MCP-Protocol-Version", c.protocol)
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound && res.StatusCode != http.StatusMethodNotAllowed && (res.StatusCode < 200 || res.StatusCode >= 300) {
		return errors.New("mcp session close rejected")
	}
	return nil
}

// 제어 응답도 대상의 DOM·평가 결과를 담을 수 있어 상한을 둔다. 다른 MCP 전송에는 적용하지 않는다.
const maxScopedMCPResponseBytes int64 = 16 << 20

type scopedMCPTransport struct{ transport *http.Transport }
type scopedMCPBody struct {
	io.Reader
	io.Closer
}

func (t *scopedMCPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := t.transport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if res.ContentLength > maxScopedMCPResponseBytes {
		res.Body.Close()
		return nil, errors.New("mcp control response too large")
	}
	res.Body = &scopedMCPBody{Reader: io.LimitReader(res.Body, maxScopedMCPResponseBytes), Closer: res.Body}
	return res, nil
}
func (t *scopedMCPTransport) CloseIdleConnections() { t.transport.CloseIdleConnections() }
