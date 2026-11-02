package llmpolicy_test

import (
	"errors"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/Autumn-27/artex/llmpolicy"
)

func TestSelectedAddressMustBelongToCurrentResolution(t *testing.T) {
	one, two := netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("10.0.0.2")
	p, err := llmpolicy.New([]llmpolicy.Endpoint{{Origin: "http://model.internal", Addresses: []netip.Addr{one, two}}})
	if err != nil {
		t.Fatal(err)
	}
	in := llmpolicy.Input{URL: "http://model.internal", Transport: llmpolicy.Direct, ResolvedIPs: []netip.Addr{one}, ConnectIP: two}
	if d := p.Decide(in); d.Allowed() || d.Reason() != llmpolicy.ReasonConnectMismatch {
		t.Fatal("approved but unresolved address was allowed")
	}
	in.ConnectIP = netip.MustParseAddr("8.8.8.8")
	if d := p.Decide(in); d.Allowed() || d.Reason() != llmpolicy.ReasonNonLocalAddress {
		t.Fatal("selected public address was allowed")
	}
	for _, raw := range []string{"http://model.internal/api", "http://unregistered.internal/api", "http://model.internal/retry"} {
		in.URL, in.ConnectIP = raw, one
		if p.Decide(in).Allowed() != (strings.HasPrefix(raw, "http://model.internal/")) {
			t.Fatal("redirect/retry reused another decision")
		}
	}
}

func TestPinCountBoundaryAndInvalidAddress(t *testing.T) {
	pins := make([]netip.Addr, 64)
	for i := range pins {
		pins[i] = netip.AddrFrom4([4]byte{10, 0, 0, byte(i + 1)})
	}
	p, err := llmpolicy.New([]llmpolicy.Endpoint{{Origin: "http://model.internal", Addresses: pins}})
	if err != nil {
		t.Fatal("exact pin limit was blocked")
	}
	if !p.Decide(llmpolicy.Input{URL: "http://model.internal", Transport: llmpolicy.Direct, ResolvedIPs: pins, ConnectIP: pins[63]}).Allowed() {
		t.Fatal("registered address at pin limit was blocked")
	}
	for _, bad := range [][]netip.Addr{append(pins, netip.MustParseAddr("10.0.0.65")), {{}}} {
		if candidate, err := llmpolicy.New([]llmpolicy.Endpoint{{Origin: "http://model.internal", Addresses: bad}}); candidate != nil || !errors.Is(err, llmpolicy.ErrInvalidPolicy) {
			t.Fatal("invalid pin configuration was accepted")
		}
	}
}

func TestPolicyConcurrentDecisionsDoNotMutateConfiguration(t *testing.T) {
	p := loopbackPolicy(t)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !p.Decide(loopbackInput("http://localhost/api")).Allowed() || p.Decide(loopbackInput("https://external.invalid/api")).Allowed() {
				t.Error("concurrent decisions changed authority")
			}
		}()
	}
	wg.Wait()
}

func FuzzLocalLLMPolicyNeverExpandsAuthority(f *testing.F) {
	ip := netip.MustParseAddr("127.0.0.1")
	p, err := llmpolicy.New([]llmpolicy.Endpoint{{Origin: "http://localhost", Addresses: []netip.Addr{ip}}})
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range []struct {
		url, ip string
		mode    uint8
	}{
		{"http://localhost/api?x=fuzz-private-value", "127.0.0.1", 1},
		{"http://localhost:80", "::ffff:127.0.0.1", 1},
		{"http://localhost", "8.8.8.8", 1},
		{"http://fuzz-private-value@localhost", "127.0.0.1", 1},
		{"http://localhost/%00", "127.0.0.1", 1},
		{"http://localhost", "127.0.0.1", 0},
		{"http://localhost", "127.0.0.1", 2},
	} {
		f.Add(seed.url, seed.ip, seed.mode)
	}
	f.Fuzz(func(t *testing.T, raw, rawIP string, mode uint8) {
		a, _ := netip.ParseAddr(rawIP)
		d := p.Decide(llmpolicy.Input{URL: raw, Transport: llmpolicy.Transport(mode), ResolvedIPs: []netip.Addr{a}, ConnectIP: a})
		if !d.Allowed() {
			return
		}
		u, err := url.Parse(raw)
		if err != nil || u.User != nil || u.Opaque != "" || u.Fragment != "" || strings.ToLower(u.Scheme) != "http" || strings.ToLower(u.Hostname()) != "localhost" || u.Port() != "" && u.Port() != "80" || mode != uint8(llmpolicy.Direct) || a.Zone() != "" || a.Unmap() != ip {
			t.Fatal("decision expanded registered URL/transport/address authority")
		}
	})
}
