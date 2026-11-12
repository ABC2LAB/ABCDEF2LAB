package exploreboundary

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/big"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DenialHeader는 경계가 막은 응답에 붙이는 사유 헤더다. ResultHeader는 전달했지만 대상 응답을 돌려주지 못한 결과다.
// 실행기가 받는 것은 고정 어휘뿐이다.
const (
	DenialHeader = "X-Explore-Boundary-Denial"
	ResultHeader = "X-Explore-Boundary-Result"
	// SeqHeader는 승인 대상으로 전달한 요청의 경계 기록 순번이다. 서버의 정적 fetch가 받은 본문을 같은 순번의 private
	// 관찰(응답 해시)과 맞춘다. 경계가 응답을 쓰기 전에 관찰을 남기므로 이 헤더를 받으면 그 순번을 읽을 수 있다.
	// 대상이 같은 이름을 보내면 지운다.
	SeqHeader = "X-Explore-Boundary-Seq"
)

// ProxyConfig는 입구 하나의 프록시 설정이다. 설치가 고정하고 모델·실행기 입력으로 바꾸지 않는다.
type ProxyConfig struct {
	Ingress Ingress
	// Current는 지금 열린 세션이다. 없으면 모든 요청을 session_closed로 막는다.
	Current func() *Session
	// CA·CAKey는 가로채기용 경계 CA다. 개인키는 경계 컨테이너 밖으로 나가지 않는다(제어 비밀).
	CA    *x509.Certificate
	CAKey crypto.Signer
	// Roots는 외부 호스트 TLS 검증용이다. nil이면 시스템 루트다. 승인 대상은 SPKI 고정값으로도 검증한다.
	Roots            *x509.CertPool
	MaxRequestBytes  int64
	MaxResponseBytes int64
	MaxTunnels       int
	Timeout          time.Duration
	LeafValidity     time.Duration
}

func (c ProxyConfig) valid() bool {
	return (c.Ingress == BrowserIngress || c.Ingress == DirectIngress) && c.Current != nil && c.CA != nil && c.CAKey != nil &&
		c.MaxRequestBytes > 0 && c.MaxResponseBytes > 0 && c.MaxTunnels > 0 && c.Timeout > 0 && c.LeafValidity > 0
}

// Proxy는 CONNECT를 받아 TLS를 가로채고 안쪽 요청마다 Session.Admit 판정을 받은 뒤에만 대상에 보낸다.
// 리다이렉트는 따라가지 않는다. 응답의 Location으로 온 새 요청도 다시 판정한다(§3).
type Proxy struct {
	cfg     ProxyConfig
	mu      sync.Mutex
	tunnels map[net.Conn]struct{}
}

func NewProxy(cfg ProxyConfig) (*Proxy, error) {
	if !cfg.valid() {
		return nil, ErrConfig
	}
	return &Proxy{cfg: cfg, tunnels: map[net.Conn]struct{}{}}, nil
}

func deny(w http.ResponseWriter, reason string) {
	w.Header().Set(DenialHeader, reason)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	_, _ = io.WriteString(w, "explore boundary denied this request: "+reason+"\n")
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		// 평문 프록시 요청은 받지 않는다. 승인 대상은 https만이고 외부도 가로채 검증해야 판정할 수 있다.
		p.refuse(Request{Ingress: p.cfg.Ingress, Method: r.Method, Scheme: r.URL.Scheme, Host: r.URL.Hostname(), EscapedPath: r.URL.EscapedPath()}, ReasonProtocolUnsupported)
		deny(w, ReasonProtocolUnsupported)
		return
	}
	host, portText, err := net.SplitHostPort(r.Host)
	port, perr := strconv.ParseUint(portText, 10, 16)
	if err != nil || perr != nil || host == "" || port == 0 {
		// 잘못된 CONNECT authority도 경계가 막은 요청이라 기록한다(#488 리뷰 P2). 값은 기록하지 않는다.
		p.refuse(Request{Ingress: p.cfg.Ingress, Method: http.MethodConnect, Scheme: "https"}, ReasonProtocolUnsupported)
		deny(w, ReasonProtocolUnsupported)
		return
	}
	if s := p.cfg.Current(); s == nil {
		deny(w, ReasonSessionClosed)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		deny(w, ReasonProtocolUnsupported)
		return
	}
	p.mu.Lock()
	if len(p.tunnels) >= p.cfg.MaxTunnels {
		p.mu.Unlock()
		p.refuse(Request{Ingress: p.cfg.Ingress, Method: http.MethodConnect, Scheme: "https", Host: host, Port: uint16(port)}, ReasonCapExhausted)
		deny(w, ReasonCapExhausted)
		return
	}
	conn, buf, err := hijacker.Hijack()
	if err != nil {
		p.mu.Unlock()
		return
	}
	p.tunnels[conn] = struct{}{}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.tunnels, conn)
		p.mu.Unlock()
		_ = conn.Close()
	}()
	if _, err = buf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil || buf.Flush() != nil {
		return
	}
	leaf, err := p.leaf(strings.ToLower(host))
	if err != nil {
		return
	}
	// CONNECT와 같은 패킷으로 온 ClientHello가 hijack 버퍼에 남아 있을 수 있다. 그 바이트부터 읽게 한다(#488 리뷰).
	var raw net.Conn = conn
	if buf.Reader.Buffered() > 0 {
		raw = &bufferedConn{Conn: conn, r: buf.Reader}
	}
	tlsConn := tls.Server(raw, &tls.Config{Certificates: []tls.Certificate{*leaf}, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}})
	handshake, cancel := context.WithTimeout(r.Context(), p.cfg.Timeout)
	err = tlsConn.HandshakeContext(handshake)
	cancel()
	if err != nil {
		return
	}
	authority := strings.ToLower(host)
	inner := http.HandlerFunc(func(w http.ResponseWriter, in *http.Request) {
		innerHost, innerPort, splitErr := net.SplitHostPort(in.Host)
		if splitErr != nil {
			// 포트 없는 Host다. IPv6 리터럴은 괄호를 벗겨 터널 authority와 같은 표기로 비교한다(#488 리뷰).
			innerHost, innerPort = strings.TrimSuffix(strings.TrimPrefix(in.Host, "["), "]"), portText
		}
		if !strings.EqualFold(innerHost, authority) || innerPort != portText {
			// 터널 authority와 안쪽 Host가 다르면 다른 origin으로 보내는 우회다. 막은 요청도 기록한다(#488 리뷰 P2).
			// 판정 대상은 터널 authority가 아니라 안쪽 Host라 그 값으로 남긴다(Refuse가 승인 origin 여부만 본다).
			hostPort, _ := strconv.ParseUint(innerPort, 10, 16)
			p.refuse(Request{Ingress: p.cfg.Ingress, Method: in.Method, Scheme: "https", Host: innerHost, Port: uint16(hostPort), EscapedPath: in.URL.EscapedPath(),
				FetchMode: in.Header.Get("Sec-Fetch-Mode"), FetchDest: in.Header.Get("Sec-Fetch-Dest"),
				HasAuth: in.Header.Get("Authorization") != "" || in.Header.Get("Cookie") != ""}, ReasonOutOfScopeOrigin)
			deny(w, ReasonOutOfScopeOrigin)
			return
		}
		p.forward(w, in, authority, uint16(port))
	})
	// ReadTimeout은 요청 본문까지 덮는다. 본문을 멈춘 요청이 터널 자리를 계속 잡지 못하게 한다(#488 리뷰).
	server := &http.Server{Handler: inner, ReadHeaderTimeout: p.cfg.Timeout, ReadTimeout: p.cfg.Timeout, IdleTimeout: p.cfg.Timeout, MaxHeaderBytes: 1 << 16}
	tracked := &onceConn{Conn: tlsConn, done: make(chan struct{})}
	stop := context.AfterFunc(r.Context(), func() { _ = tracked.Close() })
	defer stop()
	_ = server.Serve(&singleListener{conn: tracked})
	_ = tracked.Close()
}

func (p *Proxy) refuse(req Request, reason string) {
	if s := p.cfg.Current(); s != nil {
		s.Refuse(req, reason)
	}
}

func queryKeys(r *http.Request) []string {
	out := []string{}
	for k := range r.URL.Query() {
		out = append(out, k)
	}
	return out
}

func bodyKeys(contentType string, body []byte) []string {
	if mediaType(contentType) != "application/json" {
		return nil
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil {
		return nil
	}
	out := make([]string, 0, len(object))
	for k := range object {
		out = append(out, k)
	}
	return out
}

func mediaType(raw string) string {
	m, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return ""
	}
	return m
}

// 대상으로 보내지 않는 연결 수준·프록시 헤더다.
var hopHeaders = []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authorization", "Proxy-Authenticate", "TE", "Trailer", "Transfer-Encoding", "Upgrade"}

func (p *Proxy) forward(w http.ResponseWriter, in *http.Request, host string, port uint16) {
	s := p.cfg.Current()
	req := Request{Ingress: p.cfg.Ingress, Method: in.Method, Scheme: "https", Host: host, Port: port, EscapedPath: in.URL.EscapedPath(),
		FetchMode: in.Header.Get("Sec-Fetch-Mode"), FetchDest: in.Header.Get("Sec-Fetch-Dest"),
		HasAuth:   in.Header.Get("Authorization") != "" || in.Header.Get("Cookie") != "",
		WebSocket: strings.EqualFold(in.Header.Get("Upgrade"), "websocket")}
	if s == nil {
		deny(w, ReasonSessionClosed)
		return
	}
	if req.WebSocket {
		// WebSocket 전달은 아직 구현 전이다. 구현 안 된 경로는 열지 않는다(CLAUDE.md 규칙 3과 같은 원칙).
		s.Refuse(req, ReasonProtocolUnsupported)
		deny(w, ReasonProtocolUnsupported)
		return
	}
	body, err := io.ReadAll(io.LimitReader(in.Body, p.cfg.MaxRequestBytes+1))
	if err != nil || int64(len(body)) > p.cfg.MaxRequestBytes {
		s.Refuse(req, ReasonProtocolUnsupported)
		deny(w, ReasonProtocolUnsupported)
		return
	}
	d, ticket := s.Admit(in.Context(), req)
	if !d.Forward {
		deny(w, d.Reason)
		return
	}
	result := Result{QueryKeys: queryKeys(in), BodyKeys: bodyKeys(in.Header.Get("Content-Type"), body), ContentType: mediaType(in.Header.Get("Content-Type"))}
	defer func() { ticket.Done(result) }()
	ctx, cancel := context.WithTimeout(in.Context(), p.cfg.Timeout)
	defer cancel()
	closed := s.Closed()
	go func() {
		select {
		case <-closed:
			cancel()
		case <-ctx.Done():
		}
	}()
	out := in.Clone(ctx)
	out.RequestURI = ""
	out.URL.Scheme, out.URL.Host = "https", in.Host
	out.Header = in.Header.Clone()
	for _, h := range hopHeaders {
		out.Header.Del(h)
	}
	out.Header.Del(DenialHeader)
	out.Header.Del(ResultHeader)
	out.Header.Del(SeqHeader)
	out.Body = io.NopCloser(bytes.NewReader(body))
	out.ContentLength = int64(len(body))
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ForceAttemptHTTP2: false, TLSHandshakeTimeout: p.cfg.Timeout, ResponseHeaderTimeout: p.cfg.Timeout,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host, RootCAs: p.cfg.Roots, NextProtos: []string{"http/1.1"}},
		DialContext: func(dctx context.Context, network, _ string) (net.Conn, error) {
			// 판정이 정한 주소로만 연결한다. 실행기·요청의 이름 풀이를 쓰지 않는다.
			return (&net.Dialer{Timeout: p.cfg.Timeout}).DialContext(dctx, "tcp", d.Dial.String())
		}}
	if d.VerifySPKI != "" {
		pin := d.VerifySPKI
		transport.TLSClientConfig.VerifyConnection = func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return ErrUnavailable
			}
			sum := sha256.Sum256(state.PeerCertificates[0].RawSubjectPublicKeyInfo)
			if hex.EncodeToString(sum[:]) != pin {
				return ErrUnavailable
			}
			return nil
		}
	}
	defer transport.CloseIdleConnections()
	// 관찰을 남긴 뒤 응답을 쓴다. 순번 헤더를 받은 쪽이 바로 그 순번을 읽을 수 있어야 한다(지연 Done은 한 번만 효과).
	finish := func() {
		ticket.Done(result)
		if d.Dest == DestApproved {
			w.Header().Set(SeqHeader, strconv.FormatUint(ticket.Seq(), 10))
		}
	}
	resp, err := transport.RoundTrip(out)
	if err != nil {
		finish()
		w.Header().Set(ResultHeader, "upstream_failed")
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, p.cfg.MaxResponseBytes+1))
	result.Status, result.ResponseReceived, result.ResponseContentType = resp.StatusCode, true, mediaType(resp.Header.Get("Content-Type"))
	result.Bytes = int64(len(respBody))
	if err != nil || int64(len(respBody)) > p.cfg.MaxResponseBytes {
		// 상한을 넘는 응답은 전달하지 않는다. 받은 바이트 수만 기록한다.
		finish()
		w.Header().Set(ResultHeader, "response_limit")
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	result.ResponseSHA256 = sha256.Sum256(respBody)
	for k, vs := range resp.Header {
		// 경계 표시 헤더는 경계만 붙인다. 대상이 같은 이름을 보내 결과를 꾸미지 못하게 지운다(#488 리뷰).
		if strings.EqualFold(k, "Connection") || strings.EqualFold(k, DenialHeader) || strings.EqualFold(k, ResultHeader) || strings.EqualFold(k, SeqHeader) {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	if in.Method == http.MethodHead {
		// HEAD 응답은 본문이 없고 Content-Length가 GET일 때의 크기를 알린다. 대상 값을 그대로 둔다(#488 리뷰).
		if resp.ContentLength >= 0 {
			w.Header().Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
		}
	} else {
		w.Header().Set("Content-Length", strconv.Itoa(len(respBody)))
	}
	finish()
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)
}

// leaf는 터널마다 그 이름의 leaf 인증서를 경계 CA로 만든다. 이름 수가 묶이지 않으므로 캐시하지 않는다.
func (p *Proxy) leaf(host string) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: host}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(p.cfg.LeafValidity),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, p.cfg.CA, key.Public(), p.cfg.CAKey)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// CloseTunnels는 열린 터널을 모두 끊는다. 세션 마감·운영자 중지 때 진행 중 연결을 남기지 않는다(§6).
func (p *Proxy) CloseTunnels() {
	p.mu.Lock()
	conns := make([]net.Conn, 0, len(p.tunnels))
	for c := range p.tunnels {
		conns = append(conns, c)
	}
	p.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

// Tunnels는 열린 터널 수다.
func (p *Proxy) Tunnels() int { p.mu.Lock(); defer p.mu.Unlock(); return len(p.tunnels) }

type onceConn struct {
	net.Conn
	once sync.Once
	done chan struct{}
}

func (c *onceConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.done) })
	return err
}

type singleListener struct {
	mu   sync.Mutex
	conn *onceConn
	sent bool
}

func (l *singleListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if l.sent {
		l.mu.Unlock()
		<-l.conn.done
		return nil, net.ErrClosed
	}
	l.sent = true
	l.mu.Unlock()
	return l.conn, nil
}
func (l *singleListener) Close() error   { return nil }
func (l *singleListener) Addr() net.Addr { return l.conn.LocalAddr() }

// bufferedConn은 hijack 때 bufio가 먼저 읽어 둔 바이트를 연결보다 먼저 돌려준다.
type bufferedConn struct {
	net.Conn
	r io.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) { return c.r.Read(b) }
