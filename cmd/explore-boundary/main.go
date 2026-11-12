// explore-boundary는 탐색 경계 컨테이너의 진입점이다(docs/contracts/exploration-boundary.md §9-1).
// 브라우저 입구·직접 입구를 서로 다른 망의 주소에 따로 묶고, 엔진 서버만 닿는 유닉스 제어 소켓을 연다.
// 설정은 보호 파일 하나로만 받고 환경 변수·명령줄로 대상 값이나 범위를 받지 않는다.
package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Autumn-27/artex/exploreboundary"
	"github.com/Autumn-27/artex/internal/privateinput"
	"github.com/Autumn-27/artex/pathmask"
)

const configFormat = "jindo.explore-boundary-config/v1"

// maxConfigBytes는 설정·키·인증서 파일 한 개의 읽기 상한이다.
const maxConfigBytes = 1 << 20

type config struct {
	Format           string   `json:"format"`
	BrowserListen    string   `json:"browser_listen"`
	DirectListen     string   `json:"direct_listen"`
	ControlSocket    string   `json:"control_socket"`
	EngineUID        uint32   `json:"engine_uid"`
	CACertFile       string   `json:"ca_cert_file"`
	CAKeyFile        string   `json:"ca_key_file"`
	UpstreamCAFile   string   `json:"upstream_ca_file,omitempty"`
	PathMaskKeyFile  string   `json:"path_mask_key_file"`
	ControlEndpoints []string `json:"control_endpoints"`
	ResolverAddress  string   `json:"resolver_address,omitempty"`
	MaxRequestBytes  int64    `json:"max_request_bytes"`
	MaxResponseBytes int64    `json:"max_response_bytes"`
	MaxTunnels       int      `json:"max_tunnels"`
	MaxDeniedRecords int      `json:"max_denied_records"`
	TimeoutMS        int      `json:"timeout_ms"`
	CloseWaitMS      int      `json:"close_wait_ms"`
	LeafValiditySec  int      `json:"leaf_validity_seconds"`
}

func readJSON(path string, v any) error {
	raw, err := privateinput.ReadFile(path, maxConfigBytes)
	if err != nil {
		return err
	}
	defer clear(raw)
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(v)
}

// listenAddr는 입구 주소다. 망별 인터페이스 주소에 묶어야 하므로 0.0.0.0·loopback·이름은 받지 않는다.
func listenAddr(raw string) (netip.AddrPort, error) {
	ap, err := netip.ParseAddrPort(raw)
	if err != nil || ap.Addr().IsUnspecified() || ap.Addr().IsLoopback() || ap.Port() == 0 {
		return netip.AddrPort{}, errors.New("listen_invalid")
	}
	return ap, nil
}

func loadCA(certFile, keyFile string) (*x509.Certificate, crypto.Signer, error) {
	certPEM, err := privateinput.ReadFile(certFile, maxConfigBytes)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := privateinput.ReadFile(keyFile, maxConfigBytes)
	if err != nil {
		return nil, nil, err
	}
	defer clear(keyPEM)
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	signer, ok := pair.PrivateKey.(crypto.Signer)
	if err != nil || !ok || !cert.IsCA {
		return nil, nil, errors.New("ca_invalid")
	}
	return cert, signer, nil
}

func loadPool(file string) (*x509.CertPool, error) {
	if file == "" {
		return nil, nil
	}
	raw, err := privateinput.ReadFile(file, maxConfigBytes)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		return nil, errors.New("upstream_ca_invalid")
	}
	return pool, nil
}

func resolver(address string) (exploreboundary.Resolver, error) {
	if address == "" {
		return exploreboundary.NetResolver{Resolver: net.DefaultResolver}, nil
	}
	if _, err := netip.ParseAddrPort(address); err != nil {
		return nil, errors.New("resolver_invalid")
	}
	return exploreboundary.NetResolver{Resolver: &net.Resolver{PreferGo: true, StrictErrors: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}}, nil
}

type parts struct {
	manager *exploreboundary.Manager
	browser *exploreboundary.Proxy
	direct  *exploreboundary.Proxy
	listens [2]netip.AddrPort
	socket  string
	uid     uint32
	timeout time.Duration
}

func build(c config) (*parts, error) {
	if c.Format != configFormat || c.ControlSocket == "" || c.MaxTunnels <= 0 || c.TimeoutMS <= 0 || c.CloseWaitMS <= 0 || c.LeafValiditySec <= 0 || c.MaxDeniedRecords <= 0 || c.MaxRequestBytes <= 0 || c.MaxResponseBytes <= 0 {
		return nil, errors.New("config_invalid")
	}
	browserAddr, err := listenAddr(c.BrowserListen)
	if err != nil {
		return nil, err
	}
	directAddr, err := listenAddr(c.DirectListen)
	if err != nil || directAddr.Addr() == browserAddr.Addr() {
		// 두 입구가 같은 인터페이스에 있으면 입구를 망 배치로 가를 수 없다.
		return nil, errors.New("ingress_not_separated")
	}
	ca, key, err := loadCA(c.CACertFile, c.CAKeyFile)
	if err != nil {
		return nil, err
	}
	roots, err := loadPool(c.UpstreamCAFile)
	if err != nil {
		return nil, err
	}
	maskKey, err := privateinput.ReadFile(c.PathMaskKeyFile, maxConfigBytes)
	if err != nil || len(maskKey) != pathmask.KeyBytes {
		return nil, errors.New("path_mask_key_invalid")
	}
	var controls []netip.AddrPort
	for _, raw := range c.ControlEndpoints {
		ap, err := netip.ParseAddrPort(raw)
		if err != nil {
			return nil, errors.New("control_endpoint_invalid")
		}
		controls = append(controls, ap)
	}
	res, err := resolver(c.ResolverAddress)
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(c.TimeoutMS) * time.Millisecond
	// 경계 기록의 경로는 키로 가린다. 같은 키를 쓰는 회차 기록끼리만 패턴을 맞춰 볼 수 있다(§4).
	pattern := func(escaped string) string { p, _ := pathmask.Mask(escaped, maskKey); return p }
	m := &exploreboundary.Manager{Policy: exploreboundary.Policy{ControlEndpoints: controls, Resolver: res, PathPattern: pattern, MaxDeniedRecords: c.MaxDeniedRecords},
		CloseWait: time.Duration(c.CloseWaitMS) * time.Millisecond}
	proxy := func(ingress exploreboundary.Ingress) (*exploreboundary.Proxy, error) {
		return exploreboundary.NewProxy(exploreboundary.ProxyConfig{Ingress: ingress, Current: m.Current, CA: ca, CAKey: key, Roots: roots,
			MaxRequestBytes: c.MaxRequestBytes, MaxResponseBytes: c.MaxResponseBytes, MaxTunnels: c.MaxTunnels, Timeout: timeout, LeafValidity: time.Duration(c.LeafValiditySec) * time.Second})
	}
	browser, err := proxy(exploreboundary.BrowserIngress)
	if err != nil {
		return nil, err
	}
	direct, err := proxy(exploreboundary.DirectIngress)
	if err != nil {
		return nil, err
	}
	m.Proxies = []*exploreboundary.Proxy{browser, direct}
	return &parts{manager: m, browser: browser, direct: direct, listens: [2]netip.AddrPort{browserAddr, directAddr}, socket: c.ControlSocket, uid: c.EngineUID, timeout: timeout}, nil
}

func (p *parts) serve(ctx context.Context) error {
	// 제어 소켓은 엔진 서버 uid만 받는다. 파일 권한은 소유자만이다.
	if err := os.Remove(p.socket); err != nil && !os.IsNotExist(err) {
		return err
	}
	control, err := net.Listen("unix", p.socket)
	if err != nil {
		return err
	}
	if err = os.Chmod(p.socket, 0o600); err != nil {
		control.Close()
		return err
	}
	errs := make(chan error, 3)
	allow := func(conn net.Conn) bool {
		uid, err := exploreboundary.PeerUID(conn)
		return err == nil && uid == p.uid
	}
	go func() { errs <- p.manager.Serve(ctx, control, allow) }()
	for i, handler := range []http.Handler{p.browser, p.direct} {
		l, err := net.Listen("tcp", p.listens[i].String())
		if err != nil {
			return err
		}
		server := &http.Server{Handler: handler, ReadHeaderTimeout: p.timeout, ErrorLog: log.New(io.Discard, "", 0)}
		go func() { <-ctx.Done(); _ = server.Close() }()
		go func() {
			if err := server.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- err
			}
		}()
	}
	select {
	case <-ctx.Done():
		p.browser.CloseTunnels()
		p.direct.CloseTunnels()
		return nil
	case err := <-errs:
		return err
	}
}

func main() {
	configPath := flag.String("config", "", "protected boundary configuration file")
	flag.Parse()
	var c config
	if err := readJSON(*configPath, &c); err != nil {
		fmt.Fprintln(os.Stderr, "탐색 경계 설정을 읽지 못했습니다.")
		os.Exit(1)
	}
	p, err := build(c)
	if err != nil {
		// 고정 사유만 남긴다. 설정 값·경로는 출력하지 않는다.
		fmt.Fprintln(os.Stderr, "탐색 경계 설정이 올바르지 않습니다.")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := p.serve(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "탐색 경계가 멈췄습니다.")
		os.Exit(1)
	}
}
