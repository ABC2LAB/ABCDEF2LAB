package e2proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrProxy = errors.New("e2proxy: proxy unavailable")

const maxProxyHeaderBytes = 1 << 20

// ProxyConfig is trusted host input. The CA key stays in this process and is
// never placed in the tool's environment, argv, workspace or proxy URL.
type ProxyConfig struct {
	CA             tls.Certificate `json:"-"`
	HeaderTimeout  time.Duration
	RequestTimeout time.Duration
	MaxHeaderBytes int
	MaxConnections int
}

// Proxy binds one protected ingress to exactly one call. It never forwards a
// CONNECT tunnel. Every HTTPS message is decrypted and passed through Call.Do.
type Proxy struct{ *proxyState }

type proxyState struct {
	call              *Call
	cfg               ProxyConfig
	ca                *x509.Certificate
	signer            crypto.Signer
	mu                sync.Mutex
	listener          net.Listener
	serving           bool
	closed            bool
	conns             map[net.Conn]context.CancelFunc
	wg                sync.WaitGroup
	slots             chan struct{}
	drainOnce         sync.Once
	drained           chan struct{}
	ingressCapability [32]byte
}

func NewProxy(call *Call, cfg ProxyConfig) (*Proxy, error) {
	if call == nil {
		return nil, ErrProxy
	}
	ca, signer, err := validateProxyConfig(cfg)
	if err != nil {
		return nil, err
	}
	capability, err := call.newIngressCapability()
	if err != nil {
		return nil, ErrProxy
	}
	defer clear(capability[:])
	cfg.CA = tls.Certificate{}
	return &Proxy{proxyState: &proxyState{call: call, cfg: cfg, ca: ca, signer: signer,
		conns: make(map[net.Conn]context.CancelFunc), slots: make(chan struct{}, cfg.MaxConnections),
		drained: make(chan struct{}), ingressCapability: capability}}, nil
}

// ValidateProxyConfig validates trusted inspection inputs without creating a
// call, opening sockets, or issuing any execution authority.
func ValidateProxyConfig(cfg ProxyConfig) error {
	_, _, err := validateProxyConfig(cfg)
	return err
}

func validateProxyConfig(cfg ProxyConfig) (*x509.Certificate, crypto.Signer, error) {
	if cfg.HeaderTimeout <= 0 || cfg.RequestTimeout <= 0 || cfg.HeaderTimeout > cfg.RequestTimeout ||
		cfg.MaxHeaderBytes <= 0 || cfg.MaxHeaderBytes > maxProxyHeaderBytes || cfg.MaxConnections <= 0 ||
		cfg.MaxConnections > 128 || len(cfg.CA.Certificate) == 0 {
		return nil, nil, ErrProxy
	}
	ca, err := x509.ParseCertificate(bytes.Clone(cfg.CA.Certificate[0]))
	signer, ok := cfg.CA.PrivateKey.(crypto.Signer)
	if !ok || signer == nil {
		return nil, nil, ErrProxy
	}
	value := reflect.ValueOf(signer)
	if (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) && value.IsNil() {
		return nil, nil, ErrProxy
	}
	now := time.Now()
	if err != nil || !ca.IsCA || !ca.BasicConstraintsValid || ca.KeyUsage&x509.KeyUsageCertSign == 0 ||
		now.Before(ca.NotBefore) || !now.Before(ca.NotAfter) {
		return nil, nil, ErrProxy
	}
	certKey, err := x509.MarshalPKIXPublicKey(ca.PublicKey)
	if err != nil {
		return nil, nil, ErrProxy
	}
	signerKey, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil || !bytes.Equal(certKey, signerKey) {
		return nil, nil, ErrProxy
	}
	return ca, signer, nil
}

// Serve accepts a host-owned listener. Operational callers must use ServeUnix
// with the launched process's live peer verifier; a TCP listener alone supplies
// no attribution or network isolation evidence.
func (p *Proxy) Serve(listener net.Listener) error {
	if p == nil || p.proxyState == nil || listener == nil {
		return ErrProxy
	}
	p.mu.Lock()
	if p.closed || p.serving {
		p.mu.Unlock()
		return ErrProxy
	}
	p.serving, p.listener = true, listener
	p.mu.Unlock()
	for {
		conn, err := listener.Accept()
		if err != nil {
			p.mu.Lock()
			closed := p.closed
			p.mu.Unlock()
			if closed || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return ErrProxy
		}
		if !p.validIngress() {
			conn.Close()
			continue
		}
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			conn.Close()
			continue
		}
		select {
		case p.slots <- struct{}{}:
		default:
			p.mu.Unlock()
			conn.Close()
			continue
		}
		ctx, cancel := context.WithTimeout(p.call.Context(), p.cfg.RequestTimeout)
		p.conns[conn] = cancel
		p.wg.Add(1)
		p.mu.Unlock()
		go func() {
			defer p.wg.Done()
			defer func() {
				cancel()
				conn.Close()
				p.mu.Lock()
				delete(p.conns, conn)
				p.mu.Unlock()
				<-p.slots
			}()
			stop := context.AfterFunc(ctx, func() { conn.Close() })
			defer stop()
			p.serveConn(ctx, conn)
		}()
	}
}

// Close blocks new accepts, cancels active requests, closes all client sockets,
// and waits for every handler. A timeout leaves the caller responsible for
// retaining the call's audit/authority resources until a later successful Close.
func (p *Proxy) Close(ctx context.Context) error {
	if p == nil || p.proxyState == nil || ctx == nil {
		return ErrProxy
	}
	p.mu.Lock()
	p.closed = true
	listener := p.listener
	for conn, cancel := range p.conns {
		cancel()
		conn.Close()
	}
	p.mu.Unlock()
	if listener != nil {
		listener.Close()
	}
	p.drainOnce.Do(func() { go func() { p.wg.Wait(); close(p.drained) }() })
	select {
	case <-p.drained:
		p.mu.Lock()
		clear(p.ingressCapability[:])
		p.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ErrProxy
	}
}

func (p *Proxy) serveConn(ctx context.Context, conn net.Conn) {
	conn.SetDeadline(time.Now().Add(p.cfg.RequestTimeout))
	conn.SetReadDeadline(time.Now().Add(p.cfg.HeaderTimeout))
	reader := bufio.NewReader(conn)
	req, err := readProxyRequest(reader, p.cfg.MaxHeaderBytes)
	if err != nil {
		p.reject(ctx, "")
		writeProxyFailure(conn, http.StatusBadRequest)
		return
	}
	defer req.Body.Close()
	conn.SetReadDeadline(time.Now().Add(p.cfg.RequestTimeout))
	if req.ProtoMajor != 1 || req.ProtoMinor != 1 && req.ProtoMinor != 0 {
		p.reject(ctx, req.Method)
		writeProxyFailure(conn, http.StatusBadRequest)
		return
	}
	if req.Method == http.MethodConnect {
		p.serveCONNECT(ctx, conn, reader, req)
		return
	}
	if req.URL == nil || req.URL.Scheme != "http" || req.URL.Host == "" ||
		req.URL.User != nil || req.URL.Fragment != "" || req.URL.Opaque != "" ||
		!sameProxyAuthority(req.Host, req.URL.Host, "80") {
		p.reject(ctx, req.Method)
		writeProxyFailure(conn, http.StatusBadRequest)
		return
	}
	p.forward(ctx, conn, req)
}

func (p *Proxy) serveCONNECT(ctx context.Context, conn net.Conn, reader *bufio.Reader, envelope *http.Request) {
	authority, host, err := proxyAuthority(envelope.RequestURI, "")
	if err != nil || envelope.URL == nil || envelope.URL.Host != envelope.RequestURI ||
		!sameProxyAuthority(envelope.Host, authority, "") ||
		envelope.ContentLength > 0 || len(envelope.TransferEncoding) != 0 ||
		envelope.Header.Get("Upgrade") != "" {
		p.reject(ctx, http.MethodConnect)
		writeProxyFailure(conn, http.StatusBadRequest)
		return
	}
	if !p.call.CheckCONNECTAuthority(authority) {
		_, _ = p.call.Reject(ctx, http.MethodConnect, PolicyDenied)
		writeProxyFailure(conn, http.StatusBadGateway)
		return
	}
	leaf, err := p.leaf(host)
	if err != nil {
		p.reject(ctx, http.MethodConnect)
		writeProxyFailure(conn, http.StatusBadGateway)
		return
	}
	if _, err = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		p.reject(ctx, http.MethodConnect)
		return
	}
	// Preserve bytes buffered alongside CONNECT, but terminate TLS here. There
	// is deliberately no copy-to-target fallback after any parsing/TLS failure.
	secured := tls.Server(&proxyBufferedConn{Conn: conn, reader: reader}, &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			if hello.ServerName != "" && !strings.EqualFold(hello.ServerName, host) {
				return nil, ErrProxy
			}
			if len(hello.SupportedProtos) > 0 {
				supported := false
				for _, protocol := range hello.SupportedProtos {
					if protocol == "http/1.1" {
						supported = true
					}
				}
				if !supported {
					return nil, ErrProxy
				}
			}
			return &tls.Config{MinVersion: tls.VersionTLS12,
				NextProtos:   []string{"http/1.1"},
				Certificates: []tls.Certificate{leaf}}, nil
		},
	})
	if err := secured.HandshakeContext(ctx); err != nil {
		p.reject(ctx, http.MethodConnect)
		return
	}
	secured.SetReadDeadline(time.Now().Add(p.cfg.HeaderTimeout))
	inner, err := readProxyRequest(bufio.NewReader(secured), p.cfg.MaxHeaderBytes)
	if err != nil {
		p.reject(ctx, "")
		writeProxyFailure(secured, http.StatusBadRequest)
		return
	}
	defer inner.Body.Close()
	secured.SetReadDeadline(time.Now().Add(p.cfg.RequestTimeout))
	if inner.ProtoMajor != 1 || inner.ProtoMinor != 1 && inner.ProtoMinor != 0 || inner.Method == http.MethodConnect ||
		inner.URL == nil || inner.URL.IsAbs() || inner.URL.Host != "" ||
		inner.URL.Opaque != "" || inner.URL.User != nil || inner.URL.Fragment != "" ||
		!sameProxyAuthority(inner.Host, authority, "443") {
		p.reject(ctx, inner.Method)
		writeProxyFailure(secured, http.StatusBadRequest)
		return
	}
	inner.URL.Scheme, inner.URL.Host = "https", authority
	p.forward(ctx, secured, inner)
}

func (p *Proxy) forward(ctx context.Context, conn net.Conn, req *http.Request) {
	if !p.validIngress() {
		_, _ = p.call.Reject(ctx, req.Method, PolicyDenied)
		writeProxyFailure(conn, http.StatusBadGateway)
		return
	}
	if req.Header.Get("Upgrade") != "" || req.Header.Get("Expect") != "" || len(req.Trailer) != 0 || req.Method == http.MethodConnect {
		p.reject(ctx, req.Method)
		writeProxyFailure(conn, http.StatusBadRequest)
		return
	}
	req.RequestURI = ""
	req.Close = true
	resp, _, err := p.call.Do(ctx, req)
	if err != nil || resp == nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		writeProxyFailure(conn, http.StatusBadGateway)
		return
	}
	if resp.Body != nil {
		defer resp.Body.Close()
	}
	stripResponseHopHeaders(resp.Header)
	resp.Close = true
	if resp.Header == nil {
		resp.Header = make(http.Header)
	}
	resp.Header.Set("Connection", "close")
	// The core has already bounded and observed the upstream body. A client
	// write error cannot turn its target response into a policy denial.
	_ = writeProxyResponse(conn, resp)
}

func (p *Proxy) reject(ctx context.Context, method string) {
	_, _ = p.call.Reject(ctx, method, Unsupported)
}

func stripResponseHopHeaders(headers http.Header) {
	for _, value := range headers.Values("Connection") {
		for _, named := range strings.Split(value, ",") {
			headers.Del(strings.TrimSpace(named))
		}
	}
	for _, key := range []string{"Proxy-Authorization", "Proxy-Authenticate", "Proxy-Connection", "Connection", "Keep-Alive", "TE", "Trailer",
		"Transfer-Encoding", "Upgrade"} {
		delete(headers, key)
	}
}

func writeProxyFailure(conn net.Conn, code int) {
	body := "E2 request unavailable\n"
	resp := &http.Response{StatusCode: code, ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{"Content-Type": {"text/plain"}, "Connection": {"close"}},
		Body:   io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)),
		Close: true}
	_ = resp.Write(conn)
}

func readProxyRequest(reader *bufio.Reader, maxBytes int) (*http.Request, error) {
	var header bytes.Buffer
	lineBytes := 0
	for {
		line, err := reader.ReadSlice('\n')
		if header.Len()+len(line) > maxBytes {
			return nil, ErrProxy
		}
		header.Write(line)
		lineBytes += len(line)
		if err != nil {
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			return nil, ErrProxy
		}
		if lineBytes == 2 && bytes.Equal(line, []byte("\r\n")) {
			break
		}
		lineBytes = 0
	}
	// ReadRequest takes Host from an absolute URI and discards the wire Host.
	// Retain the bounded header value so the caller can compare both authorities
	// and preserve the validated original value when converting to origin-form.
	headers := textproto.NewReader(bufio.NewReader(bytes.NewReader(header.Bytes())))
	if _, err := headers.ReadLine(); err != nil {
		return nil, ErrProxy
	}
	fields, err := headers.ReadMIMEHeader()
	if err != nil || len(fields.Values("Host")) != 1 || fields.Get("Host") == "" {
		return nil, ErrProxy
	}
	req, err := http.ReadRequest(bufio.NewReader(io.MultiReader(bytes.NewReader(header.Bytes()), reader)))
	if err != nil {
		return nil, err
	}
	req.Host = fields.Get("Host")
	return req, nil
}

func proxyAuthority(value, defaultPort string) (string, string, error) {
	if strings.ContainsAny(value, "\\/%?#@ \t\r\n") {
		return "", "", ErrProxy
	}
	parsed, err := url.Parse("http://" + value)
	if err != nil || parsed.Host != value || parsed.User != nil {
		return "", "", ErrProxy
	}
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if port == "" {
		port = defaultPort
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || host == "" || strings.Contains(host, "%") {
		return "", "", ErrProxy
	}
	if ip := net.ParseIP(host); ip == nil {
		for _, char := range host {
			if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '.' || char == '-') {
				return "", "", ErrProxy
			}
		}
	}
	return net.JoinHostPort(host, strconv.Itoa(n)), host, nil
}

func sameProxyAuthority(a, b, defaultPort string) bool {
	aa, _, ea := proxyAuthority(a, defaultPort)
	bb, _, eb := proxyAuthority(b, defaultPort)
	return ea == nil && eb == nil && aa == bb
}

func (p *Proxy) leaf(host string) (tls.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, ErrProxy
	}
	now := time.Now()
	if now.Before(p.ca.NotBefore) || !now.Before(p.ca.NotAfter) {
		return tls.Certificate{}, ErrProxy
	}
	until := now.Add(time.Hour)
	if until.After(p.ca.NotAfter) {
		until = p.ca.NotAfter
	}
	tmpl := &x509.Certificate{SerialNumber: serial,
		Subject:   pkix.Name{CommonName: host},
		NotBefore: now.Add(-time.Minute), NotAfter: until,
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	// A distinct ephemeral leaf key avoids using the CA key for live TLS.
	key, err := newProxyLeafKey()
	if err != nil {
		return tls.Certificate{}, ErrProxy
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.ca, key.Public(), p.signer)
	if err != nil {
		return tls.Certificate{}, ErrProxy
	}
	return tls.Certificate{Certificate: [][]byte{der, p.ca.Raw}, PrivateKey: key}, nil
}

type proxyBufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *proxyBufferedConn) Read(buffer []byte) (int, error) { return c.reader.Read(buffer) }

func newProxyLeafKey() (*ecdsa.PrivateKey, error) {
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}

// validIngress consumes a host-only capability bound to this Call. The peer
// gate and target policy remain separate requirements; this token grants
// neither a target nor a new budget and never enters a wire header.
func (p *Proxy) validIngress() bool {
	if p == nil || p.proxyState == nil {
		return false
	}
	p.mu.Lock()
	capability := p.ingressCapability
	closed := p.closed
	p.mu.Unlock()
	defer clear(capability[:])
	return !closed && p.call.validIngressCapability(capability)
}
func (Proxy) String() string         { return "e2proxy.Proxy[protected]" }
func (Proxy) GoString() string       { return "e2proxy.Proxy[protected]" }
func (ProxyConfig) String() string   { return "e2proxy.ProxyConfig[protected]" }
func (ProxyConfig) GoString() string { return "e2proxy.ProxyConfig[protected]" }

// Response.Write suppresses 304 Content-Length metadata because it has no body.
// Preserve that selected-representation length without changing any content.
func writeProxyResponse(conn net.Conn, response *http.Response) error {
	if response.StatusCode == http.StatusNotModified && headerPresent(response.Header, "Content-Length") {
		if _, err := fmt.Fprintf(conn, "%s %s\r\n", response.Proto, response.Status); err != nil {
			return err
		}
		if err := response.Header.Write(conn); err != nil {
			return err
		}
		_, err := io.WriteString(conn, "\r\n")
		return err
	}
	return response.Write(conn)
}
