package llmpolicy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	ErrClientOptions  = errors.New("llmpolicy: invalid HTTP client options")
	ErrRequestBlocked = errors.New("llmpolicy: HTTP request blocked")
	ErrTransport      = errors.New("llmpolicy: HTTP transport failed")
	ErrBodyClose      = errors.New("llmpolicy: request body close failed")
)

const (
	localDialTimeout         = 30 * time.Second
	localTLSHandshakeTimeout = 10 * time.Second
	localMaxRedirects        = 10
)

// LookupIPFunc is a host-supplied resolver, never a target/model callback.
// Successful resolution must include the complete current answer set.
type LookupIPFunc func(context.Context, string) ([]netip.Addr, error)

type ClientOptions struct {
	// DisableRedirects gives one admitted model call at most one HTTP request.
	// The zero value preserves the existing same-origin redirect behavior.
	DisableRedirects bool
	LookupIP         LookupIPFunc
	RootCAs          *x509.CertPool
	// Zero preserves streaming without an overall deadline; caller contexts
	// still cancel both resolution/dial and the HTTP request.
	Timeout time.Duration
}

type dialPlan struct {
	ctx      context.Context
	url      string
	origin   origin
	host     string
	port     string
	resolved []netip.Addr
	selected netip.Addr
}

type dialPlanKey struct{}

// httpAdapter owns the transport so callers cannot weaken individual dial/TLS
// hooks. The host must retain the returned client's Transport/CheckRedirect.
type httpAdapter struct {
	policy           *Policy
	disableRedirects bool
	lookup           LookupIPFunc
	base             *http.Transport
	dialer           net.Dialer
}

// NewClient enforces a local endpoint snapshot on requests using this client.
// It does not control other clients or a host replacing its public hooks.
// http.Client may wrap errors in url.Error; do not log that outer URL verbatim.
func NewClient(policy *Policy, options ClientOptions) (*http.Client, error) {
	if policy == nil || len(policy.origins) == 0 {
		return nil, ErrInvalidPolicy
	}
	if options.Timeout < 0 {
		return nil, ErrClientOptions
	}
	lookup := options.LookupIP
	if lookup == nil {
		lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	var roots *x509.CertPool
	if options.RootCAs != nil {
		roots = options.RootCAs.Clone()
	}
	t := &httpAdapter{policy: policy, disableRedirects: options.DisableRedirects, lookup: lookup, dialer: net.Dialer{Timeout: localDialTimeout, KeepAlive: -1}}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	// A reused connection would skip the dial-time DNS/peer gate. HTTP/1 and
	// fresh connections keep the request and its verified dial plan together.
	t.base = &http.Transport{
		Proxy: nil, DialContext: t.dialContext, DisableKeepAlives: true, Protocols: protocols,
		TLSClientConfig:     &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: localTLSHandshakeTimeout,
	}
	return &http.Client{Transport: t, Timeout: options.Timeout, CheckRedirect: t.checkRedirect}, nil
}

func (t *httpAdapter) requestOrigin(r *http.Request) (origin, error) {
	if t == nil || t.policy == nil || t.base == nil || r == nil || r.URL == nil {
		return origin{}, ErrRequestBlocked
	}
	o, ok := parseOrigin(r.URL.String(), false)
	if !ok {
		return origin{}, ErrRequestBlocked
	}
	if _, ok := t.policy.origins[o.key]; !ok {
		return origin{}, ErrRequestBlocked
	}
	if r.Host != "" {
		h, ok := parseOrigin(strings.ToLower(r.URL.Scheme)+"://"+r.Host, true)
		if !ok || h.key != o.key {
			return origin{}, ErrRequestBlocked
		}
	}
	// Host is represented by Request.Host. A second value in Header can hide
	// a different apparent authority from capture or upstream wrappers.
	for name := range r.Header {
		if strings.EqualFold(name, "Host") {
			return origin{}, ErrRequestBlocked
		}
	}
	return o, nil
}

func (t *httpAdapter) resolve(ctx context.Context, o origin, host string) ([]netip.Addr, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if o.literal.IsValid() {
		return []netip.Addr{o.literal}, nil
	}
	ips, err := t.lookup(ctx, host)
	if err != nil || len(ips) == 0 || len(ips) > maxAddresses {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, ErrRequestBlocked
	}
	return append([]netip.Addr(nil), ips...), nil
}

func (t *httpAdapter) RoundTrip(r *http.Request) (*http.Response, error) {
	o, err := t.requestOrigin(r)
	if err != nil {
		return rejectRequest(r, err)
	}
	u, err := url.Parse(o.key)
	if err != nil {
		return rejectRequest(r, ErrRequestBlocked)
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		return rejectRequest(r, ErrRequestBlocked)
	}
	ips, err := t.resolve(r.Context(), o, host)
	if err != nil {
		return rejectRequest(r, err)
	}
	selected, ok := normalizeAddress(ips[0])
	if !ok || !t.policy.Decide(Input{URL: r.URL.String(), Transport: Direct, ResolvedIPs: ips, ConnectIP: selected}).Allowed() {
		return rejectRequest(r, ErrRequestBlocked)
	}
	plan := dialPlan{ctx: r.Context(), url: r.URL.String(), origin: o, host: host, port: port, resolved: ips, selected: selected}
	copy := r.Clone(context.WithValue(r.Context(), dialPlanKey{}, plan))
	copy.URL.Scheme = strings.ToLower(copy.URL.Scheme)
	resp, err := t.base.RoundTrip(copy)
	if err != nil {
		if e := r.Context().Err(); e != nil {
			return nil, e
		}
		if errors.Is(err, ErrRequestBlocked) {
			return nil, ErrRequestBlocked
		}
		return nil, ErrTransport
	}
	return resp, nil
}

func rejectRequest(r *http.Request, err error) (*http.Response, error) {
	if r != nil && r.Body != nil {
		if closeErr := r.Body.Close(); closeErr != nil {
			return nil, errors.Join(err, ErrBodyClose)
		}
	}
	return nil, err
}

func (t *httpAdapter) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	plan, ok := ctx.Value(dialPlanKey{}).(dialPlan)
	if !ok || plan.ctx == nil || network != "tcp" {
		return nil, ErrRequestBlocked
	}
	u, err := url.Parse(plan.origin.key)
	if err != nil {
		return nil, ErrRequestBlocked
	}
	want, ok := parseOrigin(u.Scheme+"://"+address, true)
	if !ok || want.key != plan.origin.key || !plan.selected.IsValid() {
		return nil, ErrRequestBlocked
	}
	dialCtx, cancel := context.WithCancel(plan.ctx)
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Bind the original request's selected address to a fresh complete answer
	// set. A changed set never falls back to a different, unchecked address.
	ips, err := t.resolve(dialCtx, plan.origin, plan.host)
	if err != nil {
		return nil, err
	}
	if !t.policy.Decide(Input{URL: plan.url, Transport: Direct, ResolvedIPs: ips, ConnectIP: plan.selected}).Allowed() {
		return nil, ErrRequestBlocked
	}
	conn, err := t.dialer.DialContext(dialCtx, "tcp", net.JoinHostPort(plan.selected.String(), plan.port))
	if err != nil {
		if e := dialCtx.Err(); e != nil {
			return nil, e
		}
		return nil, ErrTransport
	}
	peer, ok := conn.RemoteAddr().(*net.TCPAddr)
	port, err := strconv.Atoi(plan.port)
	var peerIP netip.Addr
	if ok {
		peerIP, _ = netip.AddrFromSlice(peer.IP)
	}
	if !ok || err != nil || peer.Port != port || peerIP.Unmap() != plan.selected {
		if err := conn.Close(); err != nil {
			return nil, errors.Join(ErrRequestBlocked, ErrTransport)
		}
		return nil, ErrRequestBlocked
	}
	return conn, nil
}

func (t *httpAdapter) checkRedirect(r *http.Request, via []*http.Request) error {
	if t.disableRedirects || len(via) == 0 || len(via) >= localMaxRedirects {
		return ErrRequestBlocked
	}
	next, err := t.requestOrigin(r)
	if err != nil {
		return err
	}
	first, err := t.requestOrigin(via[0])
	if err != nil || first.key != next.key {
		return ErrRequestBlocked
	}
	return nil
}

func (t *httpAdapter) CloseIdleConnections() {
	if t != nil && t.base != nil {
		t.base.CloseIdleConnections()
	}
}
