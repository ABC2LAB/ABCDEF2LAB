package llmpolicy_test

import (
	"errors"
	"net/netip"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/llmpolicy"
)

func loopbackPolicy(t *testing.T) *llmpolicy.Policy {
	t.Helper()
	p, err := llmpolicy.New([]llmpolicy.Endpoint{{Origin: "http://localhost", Addresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func loopbackInput(raw string) llmpolicy.Input {
	ip := netip.MustParseAddr("127.0.0.1")
	return llmpolicy.Input{URL: raw, Transport: llmpolicy.Direct, ResolvedIPs: []netip.Addr{ip}, ConnectIP: ip}
}

func TestURLGrammarFailsClosed(t *testing.T) {
	p := loopbackPolicy(t)
	urls := []string{
		"", "localhost:80", "http:localhost", "//localhost", "ftp://localhost",
		"http://user:synthetic-secret@localhost", "http://@localhost", "http://localhost/#", "http://localhost/#fragment",
		" http://localhost", "http://localhost ", "http://localhost/\t", "http://localhost/\xff",
		"http://localhost\\@evil.invalid", "http://localhost/%00", "http://localhost/%0a", "http://localhost/%5c",
		"http://localhost/?x=%0a", "http://localhost/?x=%5c", "http://localhost/?x=%zz", "http://localhost/%ff", "http://localhost/?x=%ff",
		"http://locаlhost", "http://127.1", "http://0177.0.0.1", "http://2130706433", "http://0x7f000001", "http://0x7f.0.0.1",
		"http://[127.0.0.1]", "http://[::1%25lo]", "http://2001:db8:::80",
		"http://-localhost", "http://localhost-", "http://local..host", "http://localhost.",
		"http://localhost:", "http://localhost:0", "http://localhost:65536", "http://localhost:-1", "http://localhost:+80", "http://localhost:080",
		"http://" + strings.Repeat("a", 64) + ".internal",
	}
	for i, raw := range urls {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			d := p.Decide(loopbackInput(raw))
			if d.Allowed() || d.Reason() != llmpolicy.ReasonURLInvalid {
				t.Fatal("unsupported URL was accepted or misclassified")
			}
		})
	}
}

func TestCanonicalOriginsPreserveOnlyExplicitAuthority(t *testing.T) {
	for _, tc := range []struct{ origin, url string }{
		{"HTTP://LOCALHOST:80/", "http://localhost/path?model=a+b"},
		{"http://localhost", "http://LOCALHOST:80/a%20b?x=a%20b"},
		{"http://localhost", "http://localhost/상태?value=%23"},
		{"https://localhost", "HTTPS://LOCALHOST:443/v1/messages"},
		{"http://localhost:11434", "http://localhost:11434/api/chat"},
		{"http://xn--model-9za.internal", "http://xn--model-9za.internal/api"},
	} {
		p, err := llmpolicy.New([]llmpolicy.Endpoint{{Origin: tc.origin, Addresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}})
		if err != nil || !p.Decide(loopbackInput(tc.url)).Allowed() {
			t.Fatal("registered canonical authority was blocked")
		}
	}
	ip := netip.MustParseAddr("127.0.0.1")
	p, err := llmpolicy.New([]llmpolicy.Endpoint{{Origin: "http://[::ffff:127.0.0.1]", Addresses: []netip.Addr{ip}}})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Decide(loopbackInput("http://[::ffff:127.0.0.1]/api")).Allowed() {
		t.Fatal("explicit mapped origin was blocked")
	}
	if p.Decide(loopbackInput("http://127.0.0.1/api")).Allowed() {
		t.Fatal("mapped origin expanded into a different authority")
	}
}

func TestUnicodeHostnameDoesNotFoldIntoApprovedASCII(t *testing.T) {
	p, err := llmpolicy.New([]llmpolicy.Endpoint{{Origin: "http://k.internal", Addresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}})
	if err != nil {
		t.Fatal(err)
	}
	if d := p.Decide(loopbackInput("http://K.internal/api")); d.Allowed() || d.Reason() != llmpolicy.ReasonURLInvalid {
		t.Fatal("non-ASCII hostname folded into a registered ASCII authority")
	}
	if candidate, err := llmpolicy.New([]llmpolicy.Endpoint{{Origin: "http://K.internal", Addresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}}); candidate != nil || !errors.Is(err, llmpolicy.ErrInvalidPolicy) {
		t.Fatal("non-ASCII configuration hostname was accepted")
	}
}

func TestLocalAddressPrefixBoundaries(t *testing.T) {
	for _, tc := range []struct {
		ip      string
		allowed bool
	}{
		{"126.255.255.255", false}, {"127.0.0.1", true}, {"127.255.255.254", true}, {"128.0.0.1", false},
		{"9.255.255.255", false}, {"10.0.0.1", true}, {"10.255.255.254", true}, {"11.0.0.1", false},
		{"172.15.255.255", false}, {"172.16.0.1", true}, {"172.31.255.254", true}, {"172.32.0.1", false},
		{"192.167.255.255", false}, {"192.168.0.1", true}, {"192.168.255.254", true}, {"192.169.0.1", false},
		{"::1", true}, {"fc00::1", true}, {"fdff::1", true}, {"fe00::1", false},
		{"0.0.0.0", false}, {"::", false}, {"224.0.0.1", false}, {"255.255.255.255", false}, {"ff02::1", false},
		{"169.254.169.254", false}, {"fe80::1", false}, {"100.64.0.1", false},
		{"192.0.2.1", false}, {"198.51.100.1", false}, {"203.0.113.1", false}, {"2001:db8::1", false},
		{"64:ff9b::808:808", false}, {"2002:a00:1::1", false}, {"2001::1", false},
		{"::ffff:10.0.0.1", true}, {"::ffff:8.8.8.8", false},
	} {
		t.Run(tc.ip, func(t *testing.T) {
			ip := netip.MustParseAddr(tc.ip)
			p, err := llmpolicy.New([]llmpolicy.Endpoint{{Origin: "http://model.internal", Addresses: []netip.Addr{ip}}})
			if tc.allowed {
				if err != nil || !p.Decide(llmpolicy.Input{URL: "http://model.internal/api", Transport: llmpolicy.Direct, ResolvedIPs: []netip.Addr{ip}, ConnectIP: ip}).Allowed() {
					t.Fatal("registered local address was blocked")
				}
			} else if p != nil || !errors.Is(err, llmpolicy.ErrInvalidPolicy) {
				t.Fatal("nonlocal pin was accepted")
			}
		})
	}
}

func TestResolutionFailureReasonDoesNotDependOnAddressOrder(t *testing.T) {
	p := loopbackPolicy(t)
	public := netip.MustParseAddr("8.8.8.8")
	other := netip.MustParseAddr("10.1.2.3")
	for _, addresses := range [][]netip.Addr{{public, other, {}}, {other, {}, public}, {{}, public, other}} {
		in := loopbackInput("http://localhost/api")
		in.ResolvedIPs = addresses
		if d := p.Decide(in); d.Allowed() || d.Reason() != llmpolicy.ReasonAddressInvalid {
			t.Fatal("resolution priority changed with answer order")
		}
	}
	for _, addresses := range [][]netip.Addr{{public, other}, {other, public}} {
		in := loopbackInput("http://localhost/api")
		in.ResolvedIPs = addresses
		if d := p.Decide(in); d.Allowed() || d.Reason() != llmpolicy.ReasonNonLocalAddress {
			t.Fatal("public address was hidden by an unpinned local answer")
		}
	}
}

func TestInputAndConfigurationLimits(t *testing.T) {
	p := loopbackPolicy(t)
	const maxURLBytes = 8 << 10
	prefix := "http://localhost/"
	in := loopbackInput(prefix + strings.Repeat("a", maxURLBytes-len(prefix)))
	if !p.Decide(in).Allowed() {
		t.Fatal("exact URL byte limit was blocked")
	}
	in.URL += "a"
	if d := p.Decide(in); d.Allowed() || d.Reason() != llmpolicy.ReasonLimitExceeded {
		t.Fatal("oversized URL was accepted")
	}
	in = loopbackInput(prefix)
	in.ResolvedIPs = make([]netip.Addr, 64)
	for i := range in.ResolvedIPs {
		in.ResolvedIPs[i] = in.ConnectIP
	}
	if !p.Decide(in).Allowed() {
		t.Fatal("exact resolution limit was blocked")
	}
	in.ResolvedIPs = append(in.ResolvedIPs, in.ConnectIP)
	if d := p.Decide(in); d.Allowed() || d.Reason() != llmpolicy.ReasonLimitExceeded {
		t.Fatal("oversized resolution was accepted")
	}
	endpoints := make([]llmpolicy.Endpoint, 64)
	for i := range endpoints {
		endpoints[i] = llmpolicy.Endpoint{Origin: "http://model" + strconv.Itoa(i) + ".internal", Addresses: []netip.Addr{in.ConnectIP}}
	}
	if _, err := llmpolicy.New(endpoints); err != nil {
		t.Fatal("exact origin limit was blocked")
	}
	endpoints = append(endpoints, llmpolicy.Endpoint{Origin: "http://extra.internal", Addresses: []netip.Addr{in.ConnectIP}})
	if _, err := llmpolicy.New(endpoints); !errors.Is(err, llmpolicy.ErrInvalidPolicy) {
		t.Fatal("oversized origin list was accepted")
	}
	longHost := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	if _, err := llmpolicy.New([]llmpolicy.Endpoint{{Origin: "http://" + longHost, Addresses: []netip.Addr{in.ConnectIP}}}); err != nil {
		t.Fatal("253-byte DNS name was blocked")
	}
	if _, err := llmpolicy.New([]llmpolicy.Endpoint{{Origin: "http://" + longHost + "d", Addresses: []netip.Addr{in.ConnectIP}}}); !errors.Is(err, llmpolicy.ErrInvalidPolicy) {
		t.Fatal("oversized DNS name was accepted")
	}
}
