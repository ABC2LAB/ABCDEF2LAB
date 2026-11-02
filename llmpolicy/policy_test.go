package llmpolicy_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/llmpolicy"
)

func addr(t *testing.T, value string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(value)
	if err != nil {
		t.Fatalf("test address is invalid: %v", err)
	}
	return a
}

func endpoint(t *testing.T, origin string, pins ...string) llmpolicy.Endpoint {
	t.Helper()
	addresses := make([]netip.Addr, 0, len(pins))
	for _, pin := range pins {
		addresses = append(addresses, addr(t, pin))
	}
	return llmpolicy.Endpoint{Origin: origin, Addresses: addresses}
}

func input(t *testing.T, rawURL, resolved, connected string) llmpolicy.Input {
	t.Helper()
	resolvedIPs := make([]netip.Addr, 0, 1)
	if resolved != "" {
		resolvedIPs = append(resolvedIPs, addr(t, resolved))
	}
	var connectIP netip.Addr
	if connected != "" {
		connectIP = addr(t, connected)
	}
	return llmpolicy.Input{
		URL:         rawURL,
		Transport:   llmpolicy.Direct,
		ResolvedIPs: resolvedIPs,
		ConnectIP:   connectIP,
	}
}

func mustPolicy(t *testing.T, endpoints ...llmpolicy.Endpoint) *llmpolicy.Policy {
	t.Helper()
	policy, err := llmpolicy.New(endpoints)
	if err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}
	return policy
}

func TestDecideAllowsApprovedLocalEndpoints(t *testing.T) {
	tests := []struct {
		name     string
		endpoint llmpolicy.Endpoint
		request  llmpolicy.Input
	}{
		{"loopback-v4", endpoint(t, "http://127.0.0.1", "127.0.0.1"), input(t, "http://127.0.0.1/health?x=1", "127.0.0.1", "127.0.0.1")},
		{"loopback-v4-range", endpoint(t, "http://127.0.0.2", "127.0.0.2"), input(t, "http://127.0.0.2/", "127.0.0.2", "127.0.0.2")},
		{"rfc1918-10", endpoint(t, "http://svc.test", "10.1.2.3"), input(t, "http://svc.test/a", "10.1.2.3", "10.1.2.3")},
		{"rfc1918-172", endpoint(t, "http://svc172.test", "172.16.4.5"), input(t, "http://svc172.test/a", "172.16.4.5", "172.16.4.5")},
		{"rfc1918-192", endpoint(t, "http://svc192.test", "192.168.4.5"), input(t, "http://svc192.test/a", "192.168.4.5", "192.168.4.5")},
		{"loopback-v6", endpoint(t, "http://[::1]", "::1"), input(t, "http://[::1]/", "::1", "::1")},
		{"ula-v6", endpoint(t, "http://v6.test", "fd00::5"), input(t, "http://v6.test/", "fd00::5", "fd00::5")},
		{"dns-multiple-results", endpoint(t, "http://multi.test", "10.0.0.1", "192.168.0.1"), input(t, "http://multi.test/", "10.0.0.1", "10.0.0.1")},
		{"https-default-port-and-case", endpoint(t, "HTTPS://Example.TEST:443", "127.0.0.1"), input(t, "https://example.test/path", "127.0.0.1", "127.0.0.1")},
		{"duplicate-resolution-answer", endpoint(t, "http://repeat.test", "10.0.0.2"), func() llmpolicy.Input {
			in := input(t, "http://repeat.test/", "10.0.0.2", "10.0.0.2")
			in.ResolvedIPs = []netip.Addr{addr(t, "10.0.0.2"), addr(t, "10.0.0.2")}
			return in
		}()},
		{"mapped-pin", endpoint(t, "http://mapped.test", "::ffff:192.168.0.1"), input(t, "http://mapped.test/", "192.168.0.1", "192.168.0.1")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := mustPolicy(t, test.endpoint)
			if decision := policy.Decide(test.request); !decision.Allowed() {
				t.Fatalf("approved local request was blocked: %v", decision)
			}
		})
	}
}

func TestDecideBlocksUnsafeOrMismatchedRequests(t *testing.T) {
	policy := mustPolicy(t,
		endpoint(t, "http://svc.test", "10.0.0.1"),
		endpoint(t, "http://other.test", "10.0.0.2"),
		endpoint(t, "http://127.0.0.1", "127.0.0.1"),
	)
	tests := []struct {
		name string
		make func() llmpolicy.Input
	}{
		{"unknown-transport", func() llmpolicy.Input {
			in := input(t, "http://svc.test/", "10.0.0.1", "10.0.0.1")
			in.Transport = llmpolicy.TransportUnknown
			return in
		}},
		{"proxy-transport", func() llmpolicy.Input {
			in := input(t, "http://svc.test/", "10.0.0.1", "10.0.0.1")
			in.Transport = llmpolicy.Proxy
			return in
		}},
		{"invalid-transport", func() llmpolicy.Input {
			in := input(t, "http://svc.test/", "10.0.0.1", "10.0.0.1")
			in.Transport = llmpolicy.Transport(255)
			return in
		}},
		{"unknown-origin", func() llmpolicy.Input { return input(t, "http://unknown.test/", "10.0.0.1", "10.0.0.1") }},
		{"changed-scheme", func() llmpolicy.Input { return input(t, "https://svc.test/", "10.0.0.1", "10.0.0.1") }},
		{"changed-port", func() llmpolicy.Input { return input(t, "http://svc.test:81/", "10.0.0.1", "10.0.0.1") }},
		{"localhost-public-resolution", func() llmpolicy.Input { return input(t, "http://localhost/", "8.8.8.8", "8.8.8.8") }},
		{"mixed-public-resolution", func() llmpolicy.Input {
			in := input(t, "http://svc.test/", "10.0.0.1", "10.0.0.1")
			in.ResolvedIPs = []netip.Addr{addr(t, "10.0.0.1"), addr(t, "8.8.8.8")}
			return in
		}},
		{"unapproved-private-resolution", func() llmpolicy.Input { return input(t, "http://svc.test/", "10.0.0.2", "10.0.0.2") }},
		{"other-origin-pin", func() llmpolicy.Input { return input(t, "http://svc.test/", "10.0.0.2", "10.0.0.2") }},
		{"missing-resolution", func() llmpolicy.Input { return input(t, "http://svc.test/", "", "10.0.0.1") }},
		{"missing-connect-ip", func() llmpolicy.Input { return input(t, "http://svc.test/", "10.0.0.1", "") }},
		{"connect-ip-not-in-resolution", func() llmpolicy.Input { return input(t, "http://svc.test/", "10.0.0.1", "10.0.0.2") }},
		{"connect-ip-public", func() llmpolicy.Input { return input(t, "http://svc.test/", "8.8.8.8", "8.8.8.8") }},
		{"literal-resolved-different", func() llmpolicy.Input { return input(t, "http://127.0.0.1/", "127.0.0.2", "127.0.0.2") }},
		{"external-redirect", func() llmpolicy.Input { return input(t, "http://evil.test/", "127.0.0.1", "127.0.0.1") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if decision := policy.Decide(test.make()); decision.Allowed() {
				t.Fatalf("unsafe request was allowed: %v", decision)
			}
		})
	}
}

func TestNewRejectsInvalidPolicyConfiguration(t *testing.T) {
	public := addr(t, "8.8.8.8")
	zone := addr(t, "fe80::1%eth0")
	invalid := []struct {
		name      string
		endpoints []llmpolicy.Endpoint
	}{
		{"nil", nil},
		{"empty-origin", []llmpolicy.Endpoint{{Addresses: []netip.Addr{public}}}},
		{"missing-pins", []llmpolicy.Endpoint{{Origin: "http://svc.test"}}},
		{"public-pin", []llmpolicy.Endpoint{{Origin: "http://svc.test", Addresses: []netip.Addr{public}}}},
		{"zone-pin", []llmpolicy.Endpoint{{Origin: "http://svc.test", Addresses: []netip.Addr{zone}}}},
		{"duplicate-origin-default-port", []llmpolicy.Endpoint{endpoint(t, "http://svc.test", "10.0.0.1"), endpoint(t, "http://svc.test:80", "10.0.0.1")}},
		{"duplicate-mapped-pin", []llmpolicy.Endpoint{{Origin: "http://svc.test", Addresses: []netip.Addr{addr(t, "192.168.0.1"), addr(t, "::ffff:192.168.0.1")}}}},
		{"userinfo", []llmpolicy.Endpoint{{Origin: "http://user:fixture-local-policy-secret@svc.test", Addresses: []netip.Addr{addr(t, "10.0.0.1")}}}},
		{"query", []llmpolicy.Endpoint{{Origin: "http://svc.test/?fixture-local-policy-secret", Addresses: []netip.Addr{addr(t, "10.0.0.1")}}}},
		{"fragment", []llmpolicy.Endpoint{{Origin: "http://svc.test/#fixture-local-policy-secret", Addresses: []netip.Addr{addr(t, "10.0.0.1")}}}},
		{"path", []llmpolicy.Endpoint{{Origin: "http://svc.test/path", Addresses: []netip.Addr{addr(t, "10.0.0.1")}}}},
		{"unsupported-scheme", []llmpolicy.Endpoint{{Origin: "ftp://svc.test", Addresses: []netip.Addr{addr(t, "10.0.0.1")}}}},
		{"invalid-hostname", []llmpolicy.Endpoint{{Origin: "http://svc..test", Addresses: []netip.Addr{addr(t, "10.0.0.1")}}}},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			policy, err := llmpolicy.New(test.endpoints)
			if policy != nil || err == nil || !errors.Is(err, llmpolicy.ErrInvalidPolicy) {
				t.Fatalf("invalid configuration was accepted or wrong error: policy-nil=%t error=%t", policy == nil, err != nil)
			}
		})
	}
}

func TestPolicySnapshotAndZeroValuesFailClosed(t *testing.T) {
	endpoints := []llmpolicy.Endpoint{endpoint(t, "http://snapshot.test", "10.0.0.1")}
	policy := mustPolicy(t, endpoints...)
	endpoints[0].Origin = "http://other.test"
	endpoints[0].Addresses[0] = addr(t, "10.0.0.2")
	if decision := policy.Decide(input(t, "http://snapshot.test/", "10.0.0.1", "10.0.0.1")); !decision.Allowed() {
		t.Fatalf("policy changed when caller configuration mutated: %v", decision)
	}
	if decision := policy.Decide(input(t, "http://other.test/", "10.0.0.2", "10.0.0.2")); decision.Allowed() {
		t.Fatalf("policy adopted caller mutation: %v", decision)
	}
	var nilPolicy *llmpolicy.Policy
	if nilPolicy.Decide(input(t, "http://snapshot.test/", "10.0.0.1", "10.0.0.1")).Allowed() {
		t.Fatal("nil policy allowed a request")
	}
	var zeroPolicy llmpolicy.Policy
	if zeroPolicy.Decide(input(t, "http://snapshot.test/", "10.0.0.1", "10.0.0.1")).Allowed() {
		t.Fatal("zero policy allowed a request")
	}
	var zeroDecision llmpolicy.Decision
	if zeroDecision.Allowed() {
		t.Fatal("zero decision was allowed")
	}
	if zeroDecision.Reason() == "" {
		t.Fatal("zero decision has no reason")
	}
}

func TestPolicyErrorsAndDecisionsDoNotExposeInput(t *testing.T) {
	secret := "fixture-local-policy-secret"
	_, err := llmpolicy.New([]llmpolicy.Endpoint{{Origin: "http://user:" + secret + "@svc.test/path?" + secret, Addresses: []netip.Addr{addr(t, "10.0.0.1")}}})
	if err == nil || !errors.Is(err, llmpolicy.ErrInvalidPolicy) {
		t.Fatal("sensitive invalid fixture did not fail with sentinel")
	}
	for _, rendered := range []string{fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err)} {
		if strings.Contains(rendered, secret) {
			t.Fatal("policy error exposed raw fixture")
		}
	}
	decision := mustPolicy(t, endpoint(t, "http://safe.test", "10.0.0.1")).Decide(llmpolicy.Input{URL: "http://" + secret + ".test/", Transport: llmpolicy.Direct})
	for _, rendered := range []string{fmt.Sprintf("%v", decision), fmt.Sprintf("%+v", decision), fmt.Sprintf("%#v", decision)} {
		if strings.Contains(rendered, secret) {
			t.Fatal("decision exposed raw fixture")
		}
	}
	encoded, marshalErr := json.Marshal(decision)
	if marshalErr != nil {
		t.Fatalf("decision JSON failed: %v", marshalErr)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatal("decision JSON exposed raw fixture")
	}
}
