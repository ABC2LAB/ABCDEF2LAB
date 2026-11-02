// Package llmpolicy classifies host-owned local LLM connection snapshots. It
// performs no resolution or I/O and does not itself enforce provider traffic.
package llmpolicy

import (
	"errors"
	"net/netip"
)

var ErrInvalidPolicy = errors.New("llmpolicy: invalid local LLM policy")

const (
	maxEndpoints = 64
	maxAddresses = 64
	maxURLBytes  = 8 << 10
)

type Transport uint8

const (
	TransportUnknown Transport = iota
	Direct
	Proxy
)

// Endpoint is supplied by the host, never by the LLM. Pins apply to this origin
// only; a private address is not sufficient authorization by itself.
type Endpoint struct {
	Origin    string
	Addresses []netip.Addr
}

// Input describes one final URL, complete successful resolution and selected
// dial address. Adapters must not substitute a different URL/IP after checking.
type Input struct {
	URL         string
	Transport   Transport
	ResolvedIPs []netip.Addr
	ConnectIP   netip.Addr
}

type Reason string

const (
	ReasonUnknown           Reason = "unknown"
	ReasonPolicyMissing     Reason = "policy_missing"
	ReasonTransportUnknown  Reason = "transport_unknown"
	ReasonProxyDenied       Reason = "proxy_denied"
	ReasonTransportInvalid  Reason = "transport_invalid"
	ReasonURLInvalid        Reason = "url_invalid"
	ReasonOriginDenied      Reason = "origin_denied"
	ReasonResolutionMissing Reason = "resolution_missing"
	ReasonAddressInvalid    Reason = "address_invalid"
	ReasonNonLocalAddress   Reason = "address_not_local"
	ReasonAddressDenied     Reason = "address_not_pinned"
	ReasonConnectInvalid    Reason = "connect_ip_invalid"
	ReasonConnectMismatch   Reason = "connect_ip_mismatch"
	ReasonLimitExceeded     Reason = "limit_exceeded"
	ReasonAllowed           Reason = "local_endpoint_allowed"
)

// Decision is not a reusable transmission capability. The zero value blocks.
// It retains no URL, address or underlying error that could escape via logging.
type Decision struct {
	allowed bool
	reason  Reason
}

func (d Decision) Allowed() bool { return d.allowed }
func (d Decision) Reason() Reason {
	if d.reason == "" {
		return ReasonUnknown
	}
	return d.reason
}

type binding struct {
	literal netip.Addr
	pins    map[netip.Addr]struct{}
}

// Policy is an immutable configuration snapshot, safe for concurrent decisions.
// The zero value and nil receiver block. Configuration changes require New.
type Policy struct{ origins map[string]binding }

func New(endpoints []Endpoint) (*Policy, error) {
	if len(endpoints) == 0 || len(endpoints) > maxEndpoints {
		return nil, ErrInvalidPolicy
	}
	origins := make(map[string]binding, len(endpoints))
	for _, endpoint := range endpoints {
		o, ok := parseOrigin(endpoint.Origin, true)
		if !ok || len(endpoint.Addresses) == 0 || len(endpoint.Addresses) > maxAddresses {
			return nil, ErrInvalidPolicy
		}
		if _, duplicate := origins[o.key]; duplicate {
			return nil, ErrInvalidPolicy
		}
		b := binding{literal: o.literal, pins: make(map[netip.Addr]struct{}, len(endpoint.Addresses))}
		for _, raw := range endpoint.Addresses {
			a, valid := normalizeAddress(raw)
			if !valid || !localAddress(a) || (o.literal.IsValid() && a != o.literal) {
				return nil, ErrInvalidPolicy
			}
			if _, duplicate := b.pins[a]; duplicate {
				return nil, ErrInvalidPolicy
			}
			b.pins[a] = struct{}{}
		}
		origins[o.key] = b
	}
	return &Policy{origins: origins}, nil
}

func (p *Policy) Decide(in Input) Decision {
	if p == nil || len(p.origins) == 0 {
		return blocked(ReasonPolicyMissing)
	}
	switch in.Transport {
	case Direct:
	case TransportUnknown:
		return blocked(ReasonTransportUnknown)
	case Proxy:
		return blocked(ReasonProxyDenied)
	default:
		return blocked(ReasonTransportInvalid)
	}
	if len(in.URL) > maxURLBytes || len(in.ResolvedIPs) > maxAddresses {
		return blocked(ReasonLimitExceeded)
	}
	o, ok := parseOrigin(in.URL, false)
	if !ok {
		return blocked(ReasonURLInvalid)
	}
	b, registered := p.origins[o.key]
	if !registered {
		return blocked(ReasonOriginDenied)
	}
	if len(in.ResolvedIPs) == 0 {
		return blocked(ReasonResolutionMissing)
	}
	seen := make(map[netip.Addr]struct{}, len(in.ResolvedIPs))
	var invalid, nonlocal, unpinned bool
	for _, raw := range in.ResolvedIPs {
		a, valid := normalizeAddress(raw)
		if !valid {
			invalid = true
			continue
		}
		if !localAddress(a) {
			nonlocal = true
			continue
		}
		if _, pinned := b.pins[a]; !pinned || (b.literal.IsValid() && a != b.literal) {
			unpinned = true
		}
		seen[a] = struct{}{}
	}
	// Classify the whole set before choosing a reason: DNS answer order must not
	// hide a public address behind an unregistered private address.
	if invalid {
		return blocked(ReasonAddressInvalid)
	}
	if nonlocal {
		return blocked(ReasonNonLocalAddress)
	}
	if unpinned {
		return blocked(ReasonAddressDenied)
	}
	connect, valid := normalizeAddress(in.ConnectIP)
	if !valid {
		return blocked(ReasonConnectInvalid)
	}
	if !localAddress(connect) {
		return blocked(ReasonNonLocalAddress)
	}
	if _, pinned := b.pins[connect]; !pinned {
		return blocked(ReasonAddressDenied)
	}
	if _, resolved := seen[connect]; !resolved {
		return blocked(ReasonConnectMismatch)
	}
	return Decision{allowed: true, reason: ReasonAllowed}
}

func blocked(reason Reason) Decision { return Decision{reason: reason} }

func normalizeAddress(a netip.Addr) (netip.Addr, bool) {
	if !a.IsValid() || a.Zone() != "" {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}

var localPrefixes = [...]netip.Prefix{
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fc00::/7"),
}

func localAddress(a netip.Addr) bool {
	for _, prefix := range localPrefixes {
		if prefix.Contains(a) {
			return true
		}
	}
	return false
}
