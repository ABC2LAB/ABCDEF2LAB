package mcphttp

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"sync"
)

// NewScoped는 호스트가 세션별로 띄운 탐색 MCP 제어 끝점 전용이다.
// 프록시 환경·리다이렉트·인증 헤더를 물려받아 다른 망으로 세션 제어를 보내지 않는다.
func NewScoped(ctx context.Context, server, address string) (*Client, error) {
	if ctx == nil || ctx.Err() != nil || !scopedAddressValid(address) {
		return nil, errors.New("mcp scoped endpoint invalid")
	}
	transport := &http.Transport{Proxy: nil}
	c := &Client{server: server, url: address, headers: map[string]string{}, http: scopedHTTPClient(transport), protocol: protocolVersion}
	if err := c.initialize(ctx); err != nil {
		transport.CloseIdleConnections()
		return nil, err
	}
	if r := currentScopedRecorder(); r != nil && c.sessionID != "" {
		// 기록하지 못해도 탐색은 계속한다. 이 세션을 못 지우고 죽어도 경계가 조회 끊김으로 세션을 닫아 남은 페이지가
		// 대상에 닿지 못한다(문맥 정리만 못 함). 사유는 서버 로그에 남긴다.
		if err := r.Opened(address, c.sessionID); err != nil {
			log.Print("[mcp] 탐색 MCP 세션 기록 실패(재시작 정리에서 빠질 수 있음)")
		}
	}
	return c, nil
}

func scopedAddressValid(address string) bool {
	u, err := url.Parse(address)
	return err == nil && u.Scheme == "http" && u.Hostname() != "" && u.Port() != "" && (u.Path == "" || u.Path == "/" || u.Path == "/mcp") && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

func scopedHTTPClient(transport *http.Transport) *http.Client {
	return &http.Client{Transport: &scopedMCPTransport{transport: transport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// ScopedSessionRecorder는 탐색 MCP 세션이 열리고 지워질 때 그 세션 ID를 보호 저장소에 남긴다(2026-11-24 세션 수명).
// MCP는 세션을 지울 때만 브라우저 문맥을 닫으므로, 준비 서비스가 갑자기 죽으면 열린 페이지가 남는다. 다시 시작한
// 준비 서비스가 남은 ID를 DeleteScopedSession으로 지운다.
type ScopedSessionRecorder interface {
	Opened(address, sessionID string) error
	Closed(address, sessionID string)
}

var (
	scopedRecorderMu sync.Mutex
	scopedRecorder   ScopedSessionRecorder
)

// SetScopedSessionRecorder는 프로세스에 하나 있는 기록기를 정한다(제품 준비 서비스가 시작할 때 한 번).
func SetScopedSessionRecorder(r ScopedSessionRecorder) {
	scopedRecorderMu.Lock()
	scopedRecorder = r
	scopedRecorderMu.Unlock()
}

func currentScopedRecorder() ScopedSessionRecorder {
	scopedRecorderMu.Lock()
	defer scopedRecorderMu.Unlock()
	return scopedRecorder
}

// scopedSessionGone은 세션 지우기 응답이 '지웠거나 이미 없음'인지다. 405는 서버가 클라이언트의 세션 종료를 허용하지
// 않는다는 응답이라(MCP 전송 규격 Session Management) 지운 것으로 보지 않는다 — 브라우저 문맥이 살아 있을 수 있다.
func scopedSessionGone(status int) bool {
	return status == http.StatusNotFound || status >= 200 && status < 300
}

// DeleteScopedSession은 이전 실행이 남긴 탐색 MCP 세션 하나를 지운다(그 브라우저 문맥이 닫힌다). 이미 없으면 성공이다.
func DeleteScopedSession(ctx context.Context, address, sessionID string) error {
	if ctx == nil || !scopedAddressValid(address) || sessionID == "" {
		return errors.New("mcp scoped session delete invalid")
	}
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, address, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Mcp-Session-Id", sessionID)
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	res, err := scopedHTTPClient(transport).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if !scopedSessionGone(res.StatusCode) {
		return errors.New("mcp session close rejected")
	}
	return nil
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
	gone := scopedSessionGone(res.StatusCode)
	// 보통 마감에서 405(종료 미허용)는 원래처럼 오류로 돌려주지 않는다. 다만 지운 것이 아니라 기록은 남긴다.
	if !gone && res.StatusCode != http.StatusMethodNotAllowed {
		return errors.New("mcp session close rejected")
	}
	// 지운 세션만 기록에서 뺀다. 지우지 못했으면 남겨서 다음 시작 때 다시 지운다.
	if r := currentScopedRecorder(); gone && r != nil {
		r.Closed(c.url, sid)
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
